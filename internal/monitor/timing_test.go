/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package monitor

import (
	"os"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/watch"
)

// ── formatDuration ──────────────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{0, "<1s"},
		{500 * time.Millisecond, "<1s"},
		{1 * time.Second, "1s"},
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 0s"},
		{90 * time.Second, "1m 30s"},
		{3600 * time.Second, "1h 0m 0s"},
		{3661 * time.Second, "1h 1m 1s"},
		{7384 * time.Second, "2h 3m 4s"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.input)
		if got != tt.expected {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ── recordPhaseTransition ───────────────────────────────────────────────

func TestRecordPhaseTransition_NewEntry(t *testing.T) {
	timings := make(map[string]*DataUploadTiming)
	msg := watchEventMsg{
		Name:        "du-abc",
		BackupName:  "backup-1",
		Phase:       "New",
		VMName:      "my-vm",
		VMNamespace: "my-ns",
		EventType:   watch.Added,
	}

	recordPhaseTransition(timings, msg)

	du, ok := timings["du-abc"]
	if !ok {
		t.Fatal("expected timing entry for du-abc")
	}
	if du.CurrentPhase != "New" {
		t.Errorf("CurrentPhase = %q, want %q", du.CurrentPhase, "New")
	}
	if len(du.Transitions) != 1 {
		t.Fatalf("expected 1 transition, got %d", len(du.Transitions))
	}
	if du.Transitions[0].Phase != "New" {
		t.Errorf("transition phase = %q, want %q", du.Transitions[0].Phase, "New")
	}
	if du.VMName != "my-vm" {
		t.Errorf("VMName = %q, want %q", du.VMName, "my-vm")
	}
}

func TestRecordPhaseTransition_SkipsDuplicate(t *testing.T) {
	timings := make(map[string]*DataUploadTiming)
	msg := watchEventMsg{
		Name:      "du-abc",
		Phase:     "New",
		EventType: watch.Added,
	}
	recordPhaseTransition(timings, msg)
	recordPhaseTransition(timings, msg)

	if len(timings["du-abc"].Transitions) != 1 {
		t.Errorf("expected 1 transition (duplicate skipped), got %d", len(timings["du-abc"].Transitions))
	}
}

func TestRecordPhaseTransition_RecordsSequence(t *testing.T) {
	timings := make(map[string]*DataUploadTiming)
	phases := []string{"New", "Accepted", "Prepared", "InProgress", "Completed"}

	for _, phase := range phases {
		recordPhaseTransition(timings, watchEventMsg{
			Name:      "du-abc",
			Phase:     phase,
			EventType: watch.Modified,
		})
	}

	du := timings["du-abc"]
	if len(du.Transitions) != 5 {
		t.Fatalf("expected 5 transitions, got %d", len(du.Transitions))
	}
	for i, phase := range phases {
		if du.Transitions[i].Phase != phase {
			t.Errorf("transition[%d].Phase = %q, want %q", i, du.Transitions[i].Phase, phase)
		}
	}
	if du.FinalPhase != "Completed" {
		t.Errorf("FinalPhase = %q, want %q", du.FinalPhase, "Completed")
	}
}

func TestRecordPhaseTransition_EmptyPhaseIgnored(t *testing.T) {
	timings := make(map[string]*DataUploadTiming)
	recordPhaseTransition(timings, watchEventMsg{
		Name:  "du-abc",
		Phase: "",
	})
	if len(timings) != 0 {
		t.Errorf("expected empty map for empty phase, got %d entries", len(timings))
	}
}

func TestRecordPhaseTransition_FailedSetsTerminal(t *testing.T) {
	timings := make(map[string]*DataUploadTiming)
	recordPhaseTransition(timings, watchEventMsg{Name: "du-1", Phase: "New"})
	recordPhaseTransition(timings, watchEventMsg{Name: "du-1", Phase: "Failed"})

	du := timings["du-1"]
	if du.FinalPhase != "Failed" {
		t.Errorf("FinalPhase = %q, want %q", du.FinalPhase, "Failed")
	}
}

func TestRecordPhaseTransition_MetadataBackfill(t *testing.T) {
	timings := make(map[string]*DataUploadTiming)

	recordPhaseTransition(timings, watchEventMsg{
		Name:  "du-1",
		Phase: "New",
	})
	recordPhaseTransition(timings, watchEventMsg{
		Name:       "du-1",
		Phase:      "Accepted",
		BackupName: "backup-1",
		VMName:     "vm-1",
	})

	du := timings["du-1"]
	if du.BackupName != "backup-1" {
		t.Errorf("BackupName = %q, want %q", du.BackupName, "backup-1")
	}
	if du.VMName != "vm-1" {
		t.Errorf("VMName = %q, want %q", du.VMName, "vm-1")
	}
}

// ── relevantPhases ──────────────────────────────────────────────────────

func TestRelevantPhases(t *testing.T) {
	now := time.Now()
	dus := []*DataUploadTiming{
		{
			Transitions: []PhaseTransition{
				{Phase: "New", EnteredAt: now},
				{Phase: "Accepted", EnteredAt: now.Add(time.Second)},
				{Phase: "Completed", EnteredAt: now.Add(2 * time.Second)},
			},
		},
		{
			Transitions: []PhaseTransition{
				{Phase: "New", EnteredAt: now},
				{Phase: "Failed", EnteredAt: now.Add(time.Second)},
			},
		},
	}

	got := relevantPhases(dus)
	want := []string{"New", "Accepted", "Completed", "Failed"}
	if len(got) != len(want) {
		t.Fatalf("relevantPhases = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("relevantPhases[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ── phaseDuration ───────────────────────────────────────────────────────

func TestPhaseDuration(t *testing.T) {
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	now := base.Add(2*time.Minute + 30*time.Second)

	du := &DataUploadTiming{
		Transitions: []PhaseTransition{
			{Phase: "New", EnteredAt: base},
			{Phase: "Accepted", EnteredAt: base.Add(3 * time.Second)},
			{Phase: "InProgress", EnteredAt: base.Add(10 * time.Second)},
		},
		CurrentPhase: "InProgress",
	}

	if got := phaseDuration(du, "New", now); got != "3s" {
		t.Errorf("phaseDuration(New) = %q, want %q", got, "3s")
	}
	if got := phaseDuration(du, "Accepted", now); got != "7s" {
		t.Errorf("phaseDuration(Accepted) = %q, want %q", got, "7s")
	}
	if got := phaseDuration(du, "InProgress", now); !strings.HasSuffix(got, "⏳") {
		t.Errorf("phaseDuration(InProgress) = %q, want suffix ⏳", got)
	}
	if got := phaseDuration(du, "Completed", now); got != "—" {
		t.Errorf("phaseDuration(Completed) = %q, want %q", got, "—")
	}
}

func TestPhaseDuration_TerminalCompleted(t *testing.T) {
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	du := &DataUploadTiming{
		Transitions: []PhaseTransition{
			{Phase: "New", EnteredAt: base},
			{Phase: "Completed", EnteredAt: base.Add(5 * time.Second)},
		},
		FinalPhase: "Completed",
	}
	if got := phaseDuration(du, "Completed", base.Add(10*time.Second)); got != "✓" {
		t.Errorf("phaseDuration(Completed terminal) = %q, want %q", got, "✓")
	}
}

func TestPhaseDuration_TerminalFailed(t *testing.T) {
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	du := &DataUploadTiming{
		Transitions: []PhaseTransition{
			{Phase: "New", EnteredAt: base},
			{Phase: "Failed", EnteredAt: base.Add(5 * time.Second)},
		},
		FinalPhase: "Failed",
	}
	if got := phaseDuration(du, "Failed", base.Add(10*time.Second)); got != "✗" {
		t.Errorf("phaseDuration(Failed terminal) = %q, want %q", got, "✗")
	}
}

// ── totalElapsed ────────────────────────────────────────────────────────

func TestTotalElapsed(t *testing.T) {
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	now := base.Add(2*time.Minute + 5*time.Second)

	duActive := &DataUploadTiming{
		Transitions: []PhaseTransition{
			{Phase: "New", EnteredAt: base},
			{Phase: "InProgress", EnteredAt: base.Add(10 * time.Second)},
		},
	}
	got := totalElapsed(duActive, now)
	if !strings.HasSuffix(got, "⏳") {
		t.Errorf("totalElapsed(active) = %q, want suffix ⏳", got)
	}

	duCompleted := &DataUploadTiming{
		Transitions: []PhaseTransition{
			{Phase: "New", EnteredAt: base},
			{Phase: "Completed", EnteredAt: base.Add(90 * time.Second)},
		},
		FinalPhase: "Completed",
	}
	got = totalElapsed(duCompleted, now)
	if got != "1m 30s" {
		t.Errorf("totalElapsed(completed) = %q, want %q", got, "1m 30s")
	}
}

func TestTotalElapsed_Empty(t *testing.T) {
	du := &DataUploadTiming{}
	if got := totalElapsed(du, time.Now()); got != "—" {
		t.Errorf("totalElapsed(empty) = %q, want %q", got, "—")
	}
}

// ── writeTimingReport ───────────────────────────────────────────────────

func TestWriteTimingReport_Empty(t *testing.T) {
	err := writeTimingReport("/dev/null", make(map[string]*DataUploadTiming))
	if err != nil {
		t.Errorf("writeTimingReport(empty) returned error: %v", err)
	}
}

func TestWriteTimingReport_Content(t *testing.T) {
	tmpFile := t.TempDir() + "/report.md"
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	timings := map[string]*DataUploadTiming{
		"backup-1-abc": {
			Name:       "backup-1-abc",
			BackupName: "backup-1",
			VMName:     "my-vm",
			Transitions: []PhaseTransition{
				{Phase: "New", EnteredAt: base},
				{Phase: "Accepted", EnteredAt: base.Add(2 * time.Second)},
				{Phase: "Completed", EnteredAt: base.Add(60 * time.Second)},
			},
			FinalPhase: "Completed",
		},
	}

	err := writeTimingReport(tmpFile, timings)
	if err != nil {
		t.Fatalf("writeTimingReport() error: %v", err)
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		"# DataUpload Phase Timing Report",
		"## Backup: backup-1",
		"my-vm",
		"New",
		"Accepted",
		"Completed",
		"2s",
		"✓",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("report missing %q\nContent:\n%s", want, content)
		}
	}
}
