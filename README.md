# kubevirt-datamover-monitor

Real-time TUI dashboard for monitoring OADP KubeVirt DataMover backup operations.

## Installation

### Pre-built binary

Download from [Releases](https://github.com/migtools/kubevirt-datamover-monitor/releases):

```bash
# Linux (x86_64)
curl -LO https://github.com/migtools/kubevirt-datamover-monitor/releases/latest/download/kubevirt-datamover-monitor-linux-amd64
chmod +x kubevirt-datamover-monitor-linux-amd64
sudo mv kubevirt-datamover-monitor-linux-amd64 /usr/local/bin/kubevirt-datamover-monitor
```

### Build from source

```bash
git clone https://github.com/migtools/kubevirt-datamover-monitor.git
cd kubevirt-datamover-monitor
make build
# binary is in bin/
./bin/kubevirt-datamover-monitor
```

## Usage

```bash
# Log in to cluster

# Monitor the default namespace (openshift-adp)
kubevirt-datamover-monitor

# Monitor a specific namespace
kubevirt-datamover-monitor -n my-namespace

# Disable S3 chain fetch (shows only K8s data)
kubevirt-datamover-monitor -no-s3

# Custom polling intervals
kubevirt-datamover-monitor -interval 5s -chain-interval 60s
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-n`, `-namespace` | `openshift-adp` | Namespace to watch |
| `-kubeconfig` | `$KUBECONFIG` or `~/.kube/config` | Path to kubeconfig |
| `-interval` | `2s` | K8s polling interval |
| `-chain-interval` | `30s` | S3 chain refresh interval |
| `-log-lines` | `10` | Tail lines per pod |
| `-no-s3` | `false` | Disable S3 backup chain fetch |
| `-debug-file` | | Write debug snapshots to file |
| `-version` | | Print version and exit |

### Keyboard

| Key | Action |
|-----|--------|
| `j` / `k` | Scroll down / up |
| `g` / `G` | Jump to top / bottom |
| `PgUp` / `PgDn` | Page up / down |
| Mouse wheel | Scroll |
| `q` / `Ctrl+C` | Quit |

## Prerequisites

- `kubeconfig` configured for a cluster with [OADP](https://github.com/openshift/oadp-operator) installed
- S3 credentials configured via the DPA resource (optional - use `-no-s3` to skip)

## What it shows

- Velero Backup and DataUpload CR status with phase badges
- Workflow pipeline: Backup -> DataUpload -> VMBT -> VMB -> Prepare -> Upload -> Finalize -> Done
- Data transfer progress bars with throughput
- Per-VM backup chain tree from S3 (full and incremental checkpoints with sizes)
- Pod logs from uploader, controller, and velero pods (with JSON log parsing)
- Failure reasons and error messages

## License

Apache License 2.0 - see [LICENSE](LICENSE) for details.
