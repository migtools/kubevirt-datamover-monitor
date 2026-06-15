# DataUpload Phase Timing Feature

## Overview

The phase timing feature tracks how long each Velero `DataUpload` custom resource
spends in each phase during a backup operation. It uses a Kubernetes Watch for
near-instant transition detection and writes a continuously-updated markdown
report to disk.

## Background: DataUpload Lifecycle

During a `snapshotMoveData` backup, the DataUpload phase progresses through:

```
New → Accepted → Prepared → InProgress → Completed
                                       → Failed
                          → Canceling  → Canceled
```

When using the Kubevirt Datamover, the the phases are defined as follows:

| Phase | Meaning |
|-------|---------|
| New | Velero backup workflow created the DataUpload; kubevirt datamover validates VM exists, is running, and has CBT enabled |
| Accepted | VMBT prepared from S3 state, backup mode resolved (full vs incremental), VirtualMachineBackup created, waiting for CBT snapshot to complete |
| Prepared | VirtualMachineBackup completed; PV rebound from VM namespace to OADP namespace, datamover pod launched |
| InProgress | Datamover pod uploading backup data to BSL (S3/object store) |
| Canceling | Cancellation request triggered, processing |
| Canceled | Cancellation completed successfully |
| Completed | Data transfer finished successfully |
| Failed | Data movement failed due to an error |

Understanding how long each DataUpload spends in each phase is critical for
identifying bottlenecks (e.g., slow snapshot preparation, slow data transfer)
and diagnosing failures. In particular, the "Accepted" phase duration tells us how much time the kubevirt controller spent processing the VMB and generating the qcow2 files, and the "InProgress" phase duration tells us how much time the kubevirt datamover pod spent actually uploading the backup files to the BSL and the related metadata updates.

## Architecture

### Data Flow

```
┌─────────────────────────────────────────┐
│  startDataUploadWatch() goroutine       │
│  K8s Watch on datauploads.velero.io     │
│  Auto-reconnects on watch expiry        │
│  Parses phase from status.phase         │
└──────────────┬──────────────────────────┘
               │ watchEventMsg (buffered chan, cap 100)
┌──────────────▼──────────────────────────┐
│  Bubble Tea Update() loop               │
│  readWatchEvent() → watchEventMsg       │
│  recordPhaseTransition() records entry  │
│  time.Now() stamped on each transition  │
└──────────────┬──────────────────────────┘
               │ writePhaseTimingReport() on each poll tick
┌──────────────▼──────────────────────────┐
│  datamover-report.md (CWD)              │
│  Overwritten every ~2s with latest data │
└─────────────────────────────────────────┘
```

### Why a K8s Watch (not polling)

The existing monitor polls DataUploads every 2 seconds via `List()`. Phase
transitions that happen between polls would be timestamped with ±2s accuracy.
A K8s Watch delivers events as they happen, giving near-instant transition
detection. The Watch is independent of the poll cycle — it runs in its own
goroutine and feeds events through a channel into the Bubble Tea event loop.

## Files Changed

### `internal/monitor/types.go`

Added two types:

```go
type PhaseTransition struct {
    Phase     string
    EnteredAt time.Time
}

type DataUploadTiming struct {
    Name         string
    BackupName   string
    VMName       string
    VMNamespace  string
    Transitions  []PhaseTransition
    CurrentPhase string
    FinalPhase   string // set on Completed/Failed/Canceled
}
```

`PhaseTransition` records the wall-clock time when a DataUpload entered a given
phase. `DataUploadTiming` accumulates the full ordered list of transitions for
one DataUpload, plus metadata extracted from labels and annotations.

### `internal/monitor/timing.go` (new file)

This is the core of the feature. Key components:

#### Watch Goroutine

```go
func startDataUploadWatch(ctx context.Context, dynClient dynamic.Interface,
    namespace string, events chan<- watchEventMsg)
```

- Opens a K8s Watch on `velero.io/v2alpha1/datauploads` in the target namespace.
- Parses each watch event into a `watchEventMsg` containing name, backup name,
  phase, VM name/namespace, and event type (Added/Modified/Deleted).
- Sends events to a buffered channel (capacity 100).
- Auto-reconnects when the watch expires (server-side timeout) or errors.
- Exits when `ctx` is cancelled (on program quit).
- Defers `close(events)` so the reader receives `watchClosedMsg`.

Label/annotation extraction reuses the same helpers from `watcher.go`
(`getAnnotation`, `getString`) to parse VM identity from:
- `velero.io/backup-name` label
- `kubevirt.io/vm-name` or `kubevirt-datamover.io/vm-name` annotation
- `kubevirt.io/vm-namespace` or `kubevirt-datamover.io/vm-namespace` annotation
- `spec.sourceNamespace` as fallback for VM namespace

#### Bubble Tea Integration

```go
func readWatchEvent(ch <-chan watchEventMsg) tea.Cmd
```

Returns a `tea.Cmd` that blocks on the channel and yields one `watchEventMsg`
or `watchClosedMsg` (if the channel is closed). After `Update()` processes the
event, it re-issues this command to read the next one.

#### Phase Transition Recording

```go
func recordPhaseTransition(timings map[string]*DataUploadTiming, msg watchEventMsg)
```

- Creates a new `DataUploadTiming` on first sight of a DataUpload name.
- Skips the event if the phase hasn't changed (handles Watch reconnects that
  replay ADDED events for existing resources).
- Appends a `PhaseTransition` with `time.Now()` as the entry timestamp.
- Backfills metadata (backup name, VM name) if it was missing on the first event.
- Sets `FinalPhase` when a terminal phase is reached (Completed/Failed/Canceled).

#### Markdown Report Generation

```go
func writeTimingReport(filename string, timings map[string]*DataUploadTiming) error
```

Generates a pivot-table markdown report:
1. Groups DataUploads by backup name, sorted alphabetically.
2. For each backup, determines which phase columns are relevant (only phases
   that were observed by any DU in that backup group).
3. Renders one row per DataUpload with columns for each phase showing duration.
4. DataUpload names are truncated by stripping the backup name prefix.

Phase duration rendering:
- Completed phases show the elapsed time (e.g., "3s", "1m 45s").
- The currently-active phase shows elapsed time with ⏳ suffix.
- The `Completed` terminal phase shows ✓.
- `Failed` or `Canceled` terminal phases show ✗.
- Phases not reached show —.
- Total column shows wall-clock time from first transition to completion
  (or to now, with ⏳, if still running).

#### Duration Formatting

```go
func formatDuration(d time.Duration) string
```

Renders durations in human-friendly format: `<1s`, `5s`, `1m 30s`, `2h 3m 4s`.

### `internal/monitor/app.go`

Changes to integrate the Watch into the Bubble Tea model:

1. **Model fields added:**
   - `phaseTimings map[string]*DataUploadTiming` — accumulated timing state
   - `watchEvents <-chan watchEventMsg` — read end of the watch channel
   - `watchCancel context.CancelFunc` — cancels the watch goroutine on quit

2. **`Config` struct:** Added `ReportFile string` field.

3. **`newModel()`:** Creates the buffered channel, starts the watch goroutine,
   initializes `phaseTimings`.

4. **`Init()`:** Adds `readWatchEvent(m.watchEvents)` to the initial command
   batch to begin consuming watch events.

5. **`Update()`:**
   - `watchEventMsg` case: calls `recordPhaseTransition()`, re-queues read.
   - `watchClosedMsg` case: no-op (watch ended).
   - `dataMsg` case: calls `m.writePhaseTimingReport()` after debug snapshot.
   - Quit handler: calls `m.watchCancel()` to stop the watch goroutine.

6. **`writePhaseTimingReport()`:** Delegates to `writeTimingReport()` if
   `ReportFile` is non-empty.

### `cmd/kubevirt-datamover-monitor/main.go`

Added `--report-file` CLI flag with default `datamover-report.md`. Passed
through to `monitor.Config.ReportFile`.

### `internal/monitor/timing_test.go` (new file)

15 unit tests covering:
- `formatDuration` — sub-second, seconds, minutes, hours
- `recordPhaseTransition` — new entry, duplicate skipping, full sequence,
  empty phase handling, Failed terminal, metadata backfill
- `relevantPhases` — only observed phases returned in canonical order
- `phaseDuration` — completed phase, active phase with ⏳, terminal ✓/✗, missing —
- `totalElapsed` — active (⏳) vs completed, empty transitions
- `writeTimingReport` — empty map no-op, content verification

## Report Output Example

When the monitor is running during a backup, `datamover-report.md` in the
current working directory will contain something like:

```markdown
# DataUpload Phase Timing Report
*Last updated: 2026-06-15 09:02:15 MDT*

## Backup: my-vm-backup-20260615

| DataUpload | VM | New | Accepted | Prepared | InProgress | Completed | Total |
|---|---|---|---|---|---|---|---|
| ...abc12 | my-vm | 2s | 2s | 43s | 1m 45s ⏳ | — | 2m 32s ⏳ |
| ...def34 | db-vm | 1s | 2s | 11s | 1m 55s | ✓ | 2m 9s |
```

## Usage

The report is enabled by default. To view it live in a second terminal:

```bash
watch -n 2 cat datamover-report.md
```

Or to disable it:

```bash
kubevirt-datamover-monitor --report-file ""
```

## Edge Cases and Limitations

### Monitor starts mid-backup

If a DataUpload already exists when the monitor starts, the Watch's initial
ADDED event captures the current phase with `time.Now()` as the entry time.
Earlier phases that already completed are not visible — timing begins from
the point the monitor observes the resource.

### Watch reconnection

K8s watches have a server-side timeout (typically 5–10 minutes). When the
watch expires, the goroutine reconnects. The new watch replays ADDED events
for existing resources. `recordPhaseTransition()` ignores these if the phase
hasn't changed (duplicate suppression via `CurrentPhase` comparison).

### DataUpload CRD not installed

If the DataUpload CRD doesn't exist yet (e.g., no OADP/Velero installed), the
Watch will fail to open. The goroutine retries every 5 seconds. The rest of
the monitor continues to function normally — the report simply won't have data.

### Multiple DataUploads per backup

Each DataUpload gets its own row in the report. A backup that snapshots
multiple PVCs will produce multiple DataUploads, each tracked independently.

### Canceled DataUploads

The Canceling → Canceled flow is tracked like any other transition. The report
shows the Canceling duration and ✗ for the Canceled column.

## Future Enhancements

Possible improvements an engineer could pick up:

1. **DataDownload timing** — Mirror this feature for restore operations using
   `velero.io/v2alpha1/datadownloads`. The architecture is identical: Watch,
   record transitions, write report. Would need a `DataDownloadTiming` type
   and a parallel watch goroutine.

2. **Historical persistence** — Currently, timing data lives only in memory
   and is lost when the monitor exits. Could serialize to JSON and reload on
   restart.

3. **TUI integration** — Display phase timing as a section in the TUI itself,
   not just the markdown file.

4. **Aggregate statistics** — After multiple backups, show min/max/avg per
   phase across all observed DataUploads.

5. **Alerting on slow phases** — Flag when a phase exceeds a configurable
   threshold duration.
