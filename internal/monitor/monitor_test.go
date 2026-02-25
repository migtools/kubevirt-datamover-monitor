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
	"testing"
)

// ── humanBytes ──────────────────────────────────────────────────────────

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
		{1099511627776, "1.0 TiB"},
		{1125899906842624, "1.0 PiB"},
		{2 * 1125899906842624, "2.0 PiB"},
	}
	for _, tt := range tests {
		got := humanBytes(tt.input)
		if got != tt.expected {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ── parseAWSCredentials ─────────────────────────────────────────────────

func TestParseAWSCredentials(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantKey   string
		wantSec   string
		wantError bool
	}{
		{
			name: "standard format",
			input: `[default]
aws_access_key_id = AKIAIOSFODNN7EXAMPLE
aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`,
			wantKey: "AKIAIOSFODNN7EXAMPLE",
			wantSec: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
		{
			name: "no section header",
			input: `aws_access_key_id = AKID
aws_secret_access_key = SECRET`,
			wantKey: "AKID",
			wantSec: "SECRET",
		},
		{
			name: "with comments",
			input: `# AWS credentials
[default]
aws_access_key_id = AKID
# secret below
aws_secret_access_key = SECRET`,
			wantKey: "AKID",
			wantSec: "SECRET",
		},
		{
			name: "no spaces around equals",
			input: `[default]
aws_access_key_id=AKID
aws_secret_access_key=SECRET`,
			wantKey: "AKID",
			wantSec: "SECRET",
		},
		{
			name:      "missing access key",
			input:     `aws_secret_access_key = SECRET`,
			wantError: true,
		},
		{
			name:      "missing secret key",
			input:     `aws_access_key_id = AKID`,
			wantError: true,
		},
		{
			name:      "empty input",
			input:     ``,
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, sec, err := parseAWSCredentials(tt.input)
			if tt.wantError {
				if err == nil {
					t.Errorf("expected error, got key=%q sec=%q", key, sec)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != tt.wantKey {
				t.Errorf("access key = %q, want %q", key, tt.wantKey)
			}
			if sec != tt.wantSec {
				t.Errorf("secret key = %q, want %q", sec, tt.wantSec)
			}
		})
	}
}

// ── Log parsing ─────────────────────────────────────────────────────────

func TestParseJSONLogLine(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantLevel string
		wantNil   bool
	}{
		{
			name:      "info level",
			input:     `{"level":"info","msg":"reconciling backup","controller":"backup"}`,
			wantLevel: "info",
		},
		{
			name:      "error level",
			input:     `{"level":"error","msg":"backup failed","error":"timeout"}`,
			wantLevel: "error",
		},
		{
			name:    "not JSON",
			input:   "this is plain text",
			wantNil: true,
		},
		{
			name:    "JSON without msg",
			input:   `{"level":"info","ts":"2026-01-01"}`,
			wantNil: true,
		},
		{
			name:    "malformed JSON",
			input:   `{"level":"info","msg":}`,
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseJSONLogLine(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if got.level != tt.wantLevel {
				t.Errorf("level = %q, want %q", got.level, tt.wantLevel)
			}
		})
	}
}

func TestFilterLogLines(t *testing.T) {
	raw := `{"level":"info","msg":"starting"}
{"level":"error","msg":"something failed","error":"timeout"}

plain text line
`
	result := filterLogLines(raw)
	if len(result) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(result), result)
	}
}

// ── truncateLogLine ─────────────────────────────────────────────────────

func TestTruncateLogLine(t *testing.T) {
	tests := []struct {
		line     string
		maxWidth int
		expected string
	}{
		{"hello world", 20, "hello world"},
		{"hello world", 11, "hello world"},
		{"hello world", 10, "hello w..."},
		{"hello world", 5, "he..."},
		{"hello world", 3, "hel"},
		{"hello world", 0, "hello world"},
		{"", 10, ""},
	}
	for _, tt := range tests {
		got := truncateLogLine(tt.line, tt.maxWidth)
		if got != tt.expected {
			t.Errorf("truncateLogLine(%q, %d) = %q, want %q", tt.line, tt.maxWidth, got, tt.expected)
		}
	}
}

// ── Phase helpers ───────────────────────────────────────────────────────

func TestIsActivePhase(t *testing.T) {
	active := []string{"InProgress", "New", "Accepted", "Prepared", "",
		"WaitingForPluginOperations", "WaitingForPluginOperationsPartiallyFailed",
		"Finalizing", "FinalizingPartiallyFailed"}
	for _, p := range active {
		if !isActivePhase(p) {
			t.Errorf("isActivePhase(%q) = false, want true", p)
		}
	}

	completed := []string{"Completed", "Failed", "PartiallyFailed", "Canceled"}
	for _, p := range completed {
		if isActivePhase(p) {
			t.Errorf("isActivePhase(%q) = true, want false", p)
		}
	}
}

func TestFilterActiveBackups(t *testing.T) {
	backups := []BackupInfo{
		{Name: "bkp-1", Phase: "Completed"},
		{Name: "bkp-2", Phase: "InProgress"},
		{Name: "bkp-3", Phase: "Failed"},
		{Name: "bkp-4", Phase: "New"},
	}
	active := filterActiveBackups(backups)
	if len(active) != 2 {
		t.Fatalf("expected 2 active, got %d", len(active))
	}
	if active[0].Name != "bkp-2" || active[1].Name != "bkp-4" {
		t.Errorf("unexpected active backups: %v", active)
	}
}

func TestCountActiveBackups(t *testing.T) {
	backups := []BackupInfo{
		{Phase: "Completed"},
		{Phase: "InProgress"},
		{Phase: "Failed"},
		{Phase: "Finalizing"},
	}
	if got := countActiveBackups(backups); got != 2 {
		t.Errorf("countActiveBackups = %d, want 2", got)
	}
}

// ── Workflow step computation ───────────────────────────────────────────

func TestComputeWorkflowStep(t *testing.T) {
	tests := []struct {
		name   string
		bkp    BackupInfo
		du     *DataUploadInfo
		hasPod bool
		want   int
	}{
		{
			name: "completed backup",
			bkp:  BackupInfo{Phase: "Completed"},
			want: wfDone,
		},
		{
			name: "no DU yet",
			bkp:  BackupInfo{Phase: "InProgress"},
			du:   nil,
			want: wfBackup,
		},
		{
			name: "DU new",
			bkp:  BackupInfo{Phase: "InProgress"},
			du:   &DataUploadInfo{Phase: "New"},
			want: wfDataUpload,
		},
		{
			name: "DU accepted",
			bkp:  BackupInfo{Phase: "InProgress"},
			du:   &DataUploadInfo{Phase: "Accepted"},
			want: wfVMB,
		},
		{
			name:   "DU prepared with pod",
			bkp:    BackupInfo{Phase: "InProgress"},
			du:     &DataUploadInfo{Phase: "Prepared"},
			hasPod: true,
			want:   wfUploading,
		},
		{
			name:   "DU prepared without pod",
			bkp:    BackupInfo{Phase: "InProgress"},
			du:     &DataUploadInfo{Phase: "Prepared"},
			hasPod: false,
			want:   wfPreparing,
		},
		{
			name: "DU in progress",
			bkp:  BackupInfo{Phase: "InProgress"},
			du:   &DataUploadInfo{Phase: "InProgress"},
			want: wfUploading,
		},
		{
			name: "DU completed, backup finalizing",
			bkp:  BackupInfo{Phase: "Finalizing"},
			du:   &DataUploadInfo{Phase: "Completed"},
			want: wfFinalizing,
		},
		{
			name: "failed backup, DU failed",
			bkp:  BackupInfo{Phase: "PartiallyFailed"},
			du:   &DataUploadInfo{Phase: "Failed"},
			want: wfUploading,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeWorkflowStep(tt.bkp, tt.du, tt.hasPod)
			if got != tt.want {
				t.Errorf("computeWorkflowStep() = %d, want %d", got, tt.want)
			}
		})
	}
}

// ── discoverVMs ─────────────────────────────────────────────────────────

func TestDiscoverVMs(t *testing.T) {
	dus := []DataUploadInfo{
		{VMName: "vm-1", VMNamespace: "ns-1"},
		{VMName: "vm-1", VMNamespace: "ns-1"}, // duplicate
		{VMName: "vm-2", VMNamespace: "ns-2"},
		{VMName: "", VMNamespace: "ns-3"}, // missing name
	}
	vms := discoverVMs(dus)
	if len(vms) != 2 {
		t.Fatalf("expected 2 VMs, got %d: %v", len(vms), vms)
	}
}

// ── getAnnotation ───────────────────────────────────────────────────────

func TestGetAnnotation(t *testing.T) {
	annotations := map[string]string{
		"kubevirt.io/vm-name":                "my-vm",
		"kubevirt-datamover.io/vm-namespace": "my-ns",
	}

	if got := getAnnotation(annotations, "vm-name"); got != "my-vm" {
		t.Errorf("getAnnotation(vm-name) = %q, want %q", got, "my-vm")
	}
	if got := getAnnotation(annotations, "vm-namespace"); got != "my-ns" {
		t.Errorf("getAnnotation(vm-namespace) = %q, want %q", got, "my-ns")
	}
	if got := getAnnotation(annotations, "missing"); got != "" {
		t.Errorf("getAnnotation(missing) = %q, want empty", got)
	}
	if got := getAnnotation(nil, "vm-name"); got != "" {
		t.Errorf("getAnnotation(nil, vm-name) = %q, want empty", got)
	}
}

// ── Unstructured field helpers ──────────────────────────────────────────

func TestGetString(t *testing.T) {
	obj := map[string]interface{}{
		"name":  "test",
		"count": int64(42),
	}
	if got := getString(obj, "name"); got != "test" {
		t.Errorf("getString(name) = %q, want %q", got, "test")
	}
	if got := getString(obj, "count"); got != "" {
		t.Errorf("getString(count) = %q, want empty (wrong type)", got)
	}
	if got := getString(obj, "missing"); got != "" {
		t.Errorf("getString(missing) = %q, want empty", got)
	}
}

func TestGetInt64(t *testing.T) {
	obj := map[string]interface{}{
		"int":    int64(42),
		"float":  float64(3.14),
		"string": "100",
		"bad":    "not-a-number",
	}
	if got := getInt64(obj, "int"); got != 42 {
		t.Errorf("getInt64(int) = %d, want 42", got)
	}
	if got := getInt64(obj, "float"); got != 3 {
		t.Errorf("getInt64(float) = %d, want 3", got)
	}
	if got := getInt64(obj, "string"); got != 100 {
		t.Errorf("getInt64(string) = %d, want 100", got)
	}
	if got := getInt64(obj, "bad"); got != 0 {
		t.Errorf("getInt64(bad) = %d, want 0", got)
	}
	if got := getInt64(obj, "missing"); got != 0 {
		t.Errorf("getInt64(missing) = %d, want 0", got)
	}
}

func TestGetBool(t *testing.T) {
	obj := map[string]interface{}{
		"yes":    true,
		"no":     false,
		"str_t":  "true",
		"str_f":  "false",
		"str_up": "TRUE",
	}
	if got := getBool(obj, "yes"); !got {
		t.Error("getBool(yes) = false, want true")
	}
	if got := getBool(obj, "no"); got {
		t.Error("getBool(no) = true, want false")
	}
	if got := getBool(obj, "str_t"); !got {
		t.Error("getBool(str_t) = false, want true")
	}
	if got := getBool(obj, "str_f"); got {
		t.Error("getBool(str_f) = true, want false")
	}
	if got := getBool(obj, "str_up"); !got {
		t.Error("getBool(str_up) = false, want true")
	}
	if got := getBool(obj, "missing"); got {
		t.Error("getBool(missing) = true, want false")
	}
}

// ── shortTimestamp ───────────────────────────────────────────────────────

func TestShortTimestamp(t *testing.T) {
	got := shortTimestamp("2026-02-24T16:11:47.716Z")
	want := "2026-02-24 16:11:47"
	if got != want {
		t.Errorf("shortTimestamp() = %q, want %q", got, want)
	}
	// Invalid input returned as-is
	if got := shortTimestamp("not-a-timestamp"); got != "not-a-timestamp" {
		t.Errorf("shortTimestamp(invalid) = %q, want original", got)
	}
}

// ── checkpointTotalSize ─────────────────────────────────────────────────

func TestCheckpointTotalSize(t *testing.T) {
	cp := CheckpointEntry{
		Files: []FileEntry{
			{Size: 100},
			{Size: 200},
			{Size: 300},
		},
	}
	if got := checkpointTotalSize(cp); got != 600 {
		t.Errorf("checkpointTotalSize() = %d, want 600", got)
	}

	empty := CheckpointEntry{}
	if got := checkpointTotalSize(empty); got != 0 {
		t.Errorf("checkpointTotalSize(empty) = %d, want 0", got)
	}
}

// ── isBackupFailed ──────────────────────────────────────────────────────

func TestIsBackupFailed(t *testing.T) {
	if !isBackupFailed(BackupInfo{Phase: "Failed"}, nil) {
		t.Error("Failed backup should be failed")
	}
	if !isBackupFailed(BackupInfo{Phase: "PartiallyFailed"}, nil) {
		t.Error("PartiallyFailed backup should be failed")
	}
	if !isBackupFailed(BackupInfo{Phase: "InProgress"}, &DataUploadInfo{Phase: "Failed"}) {
		t.Error("InProgress backup with failed DU should be failed")
	}
	if isBackupFailed(BackupInfo{Phase: "InProgress"}, &DataUploadInfo{Phase: "InProgress"}) {
		t.Error("InProgress backup with InProgress DU should not be failed")
	}
	if isBackupFailed(BackupInfo{Phase: "Completed"}, nil) {
		t.Error("Completed backup should not be failed")
	}
}
