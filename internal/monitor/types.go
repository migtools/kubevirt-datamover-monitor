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

import "time"

// StorageInfo holds BSL configuration extracted from the DPA.
type StorageInfo struct {
	Provider         string
	Bucket           string
	Prefix           string
	Region           string
	S3URL            string
	S3ForcePathStyle bool
	CredentialName   string
	CredentialKey    string
}

// BackupInfo represents a Velero Backup CR.
type BackupInfo struct {
	Name             string
	Phase            string
	StartTimestamp   *time.Time
	CompletionTime   *time.Time
	Namespaces       []string
	SnapshotMoveData bool
	FailureReason    string
	Errors           int
	Warnings         int
}

// DataUploadInfo represents a Velero DataUpload CR.
type DataUploadInfo struct {
	Name        string
	BackupName  string
	Phase       string
	BytesDone   int64
	TotalBytes  int64
	VMName      string
	VMNamespace string
	Node        string
	DataMover   string
	StartTime   *time.Time
	Message     string
}

// PodInfo represents a relevant pod with optional log lines.
type PodInfo struct {
	Name       string
	Phase      string
	Node       string
	BackupType string // "Full" or "Incremental" from env
	Logs       []string
	PodType    string // "uploader", "controller", "velero"
}

// FileEntry represents a file within a checkpoint.
type FileEntry struct {
	Filename   string `json:"filename"`
	DiskName   string `json:"diskName"`
	Size       int64  `json:"size"`
	ObjectPath string `json:"objectPath"`
}

// CheckpointEntry represents one checkpoint in the S3 index.json.
type CheckpointEntry struct {
	ID           string      `json:"id"`
	Type         string      `json:"type"` // "Full" or "Incremental"
	Parent       string      `json:"parent"`
	Timestamp    string      `json:"timestamp"`
	VMBackup     string      `json:"vmBackup"`
	Files        []FileEntry `json:"files"`
	PVCs         []string    `json:"pvcs"`
	ReferencedBy []string    `json:"referencedBy"`
}

// VMIndex represents the top-level S3 index.json for a VM.
type VMIndex struct {
	VMName      string            `json:"vmName"`
	Namespace   string            `json:"namespace"`
	Checkpoints []CheckpointEntry `json:"checkpoints"`
	LastUpdated string            `json:"lastUpdated"`
}

// VMIdentity uniquely identifies a VM.
type VMIdentity struct {
	Namespace string
	Name      string
}

// ThroughputSample tracks bytes over time for throughput calculation.
type ThroughputSample struct {
	PrevBytes int64     // bytes at previous measurement point
	PrevTime  time.Time // time of previous measurement
	LastRate  float64   // last computed rate in bytes/sec (kept for display stability)
}

// AppState aggregates all fetched data.
type AppState struct {
	Storage          *StorageInfo
	Backups          []BackupInfo
	DataUploads      []DataUploadInfo
	UploaderPods     []PodInfo
	ControllerPod    *PodInfo
	VeleroPod        *PodInfo
	Chains           map[VMIdentity]*VMIndex
	LastRefresh      time.Time
	Throughput       map[string]ThroughputSample // keyed by DataUpload name
	S3Error          string
	FetchError       string
	TerminatedPods   map[string]PodInfo // cache of pods that completed/failed
	PrevBackupPhases map[string]string  // previous backup phases for transition detection
}
