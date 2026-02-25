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
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var (
	dpaGVR = schema.GroupVersionResource{
		Group:    "oadp.openshift.io",
		Version:  "v1alpha1",
		Resource: "dataprotectionapplications",
	}
	backupGVR = schema.GroupVersionResource{
		Group:    "velero.io",
		Version:  "v1",
		Resource: "backups",
	}
	dataUploadGVR = schema.GroupVersionResource{
		Group:    "velero.io",
		Version:  "v2alpha1",
		Resource: "datauploads",
	}
)

// fetchAll orchestrates all K8s resource fetching and returns an AppState.
func fetchAll(ctx context.Context, dynClient dynamic.Interface, typedClient kubernetes.Interface, namespace string, logLines int64, prevState *AppState) AppState {
	state := AppState{
		Chains:           prevState.Chains,
		Throughput:       make(map[string]ThroughputSample),
		TerminatedPods:   make(map[string]PodInfo),
		PrevBackupPhases: make(map[string]string),
		LastRefresh:      time.Now(),
	}

	var fetchErrors []string

	// Copy previous terminated pods cache
	for k, v := range prevState.TerminatedPods {
		state.TerminatedPods[k] = v
	}

	// Copy previous throughput samples
	for k, v := range prevState.Throughput {
		state.Throughput[k] = v
	}

	// Fetch DPA
	storage, err := fetchDPA(ctx, dynClient, namespace)
	if err != nil {
		fetchErrors = append(fetchErrors, fmt.Sprintf("DPA: %v", err))
	} else {
		state.Storage = storage
	}

	// Fetch Backups
	backups, err := fetchBackups(ctx, dynClient, namespace)
	if err != nil {
		fetchErrors = append(fetchErrors, fmt.Sprintf("Backups: %v", err))
	}
	state.Backups = backups

	// Track backup phase transitions for chain refresh triggering
	for _, b := range backups {
		state.PrevBackupPhases[b.Name] = b.Phase
	}

	// Fetch DataUploads
	dataUploads, err := fetchDataUploads(ctx, dynClient, namespace)
	if err != nil {
		fetchErrors = append(fetchErrors, fmt.Sprintf("DataUploads: %v", err))
	}
	state.DataUploads = dataUploads

	// Update throughput samples
	now := time.Now()
	for _, du := range dataUploads {
		prev, hasPrev := prevState.Throughput[du.Name]
		switch {
		case !hasPrev:
			// First time seeing this DataUpload — seed the sample
			state.Throughput[du.Name] = ThroughputSample{
				PrevBytes: du.BytesDone,
				PrevTime:  now,
			}
		case du.BytesDone > prev.PrevBytes:
			// Bytes advanced — compute new rate and update baseline
			elapsed := now.Sub(prev.PrevTime).Seconds()
			rate := 0.0
			if elapsed > 0 {
				rate = float64(du.BytesDone-prev.PrevBytes) / elapsed
			}
			state.Throughput[du.Name] = ThroughputSample{
				PrevBytes: du.BytesDone,
				PrevTime:  now,
				LastRate:  rate,
			}
		default:
			// No progress — keep previous sample and fade the rate after 10s
			sample := prev
			if now.Sub(prev.PrevTime).Seconds() > 10 {
				sample.LastRate = 0
			}
			state.Throughput[du.Name] = sample
		}
	}

	// Fetch pods
	uploaderPods, controllerPod, veleroPod, err := fetchPods(ctx, typedClient, namespace, logLines)
	if err != nil {
		fetchErrors = append(fetchErrors, fmt.Sprintf("Pods: %v", err))
	}
	state.FetchError = strings.Join(fetchErrors, "; ")
	state.UploaderPods = uploaderPods
	state.ControllerPod = controllerPod
	state.VeleroPod = veleroPod

	// Cache all uploader pods — always keep the latest snapshot so we
	// never lose a pod that gets garbage collected between polls.
	// Only overwrite if the new snapshot has more info (logs, later phase).
	for _, p := range uploaderPods {
		existing, seen := state.TerminatedPods[p.Name]
		// Always update if: not seen before, or pod has progressed, or has real logs
		if !seen || p.Phase == "Succeeded" || p.Phase == "Failed" ||
			(len(p.Logs) > 0 && (len(existing.Logs) == 0 || existing.Logs[0] != p.Logs[0])) {
			state.TerminatedPods[p.Name] = p
		}
	}

	return state
}

func fetchDPA(ctx context.Context, dynClient dynamic.Interface, namespace string) (*StorageInfo, error) {
	list, err := dynClient.Resource(dpaGVR).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no DPA found")
	}

	dpa := list.Items[0]
	info := &StorageInfo{}

	// Navigate: spec.backupLocations[0].velero
	backupLocations, found, _ := unstructured.NestedSlice(dpa.Object, "spec", "backupLocations")
	if !found || len(backupLocations) == 0 {
		return nil, fmt.Errorf("no backupLocations in DPA")
	}

	bsl, ok := backupLocations[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid backupLocation format")
	}

	velero, ok := bsl["velero"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("no velero config in backupLocation")
	}

	info.Provider = getString(velero, "provider")

	// config section
	if config, ok := velero["config"].(map[string]interface{}); ok {
		info.Region = getString(config, "region")
		info.S3URL = getString(config, "s3Url")
		if v := getString(config, "s3ForcePathStyle"); v == "true" {
			info.S3ForcePathStyle = true
		}
	}

	// objectStorage section
	if objStorage, ok := velero["objectStorage"].(map[string]interface{}); ok {
		info.Bucket = getString(objStorage, "bucket")
		info.Prefix = getString(objStorage, "prefix")
	}

	// credential section
	if cred, ok := velero["credential"].(map[string]interface{}); ok {
		info.CredentialName = getString(cred, "name")
		info.CredentialKey = getString(cred, "key")
	}

	// Default credential key
	if info.CredentialKey == "" {
		info.CredentialKey = "cloud"
	}
	// Default credential name
	if info.CredentialName == "" {
		info.CredentialName = "cloud-credentials"
	}

	return info, nil
}

func fetchBackups(ctx context.Context, dynClient dynamic.Interface, namespace string) ([]BackupInfo, error) {
	list, err := dynClient.Resource(backupGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var backups []BackupInfo
	for _, item := range list.Items {
		b := BackupInfo{
			Name: item.GetName(),
		}

		if status, ok := item.Object["status"].(map[string]interface{}); ok {
			b.Phase = getString(status, "phase")
			b.StartTimestamp = getTime(status, "startTimestamp")
			b.CompletionTime = getTime(status, "completionTimestamp")
			b.FailureReason = getString(status, "failureReason")
			b.Errors = int(getInt64(status, "errors"))
			b.Warnings = int(getInt64(status, "warnings"))
		}

		if spec, ok := item.Object["spec"].(map[string]interface{}); ok {
			b.Namespaces = getStringSlice(spec, "includedNamespaces")
			b.SnapshotMoveData = getBool(spec, "snapshotMoveData")
		}

		backups = append(backups, b)
	}

	// Sort by StartTimestamp ascending (oldest first), nil timestamps last
	sort.Slice(backups, func(i, j int) bool {
		ti, tj := backups[i].StartTimestamp, backups[j].StartTimestamp
		if ti == nil && tj == nil {
			return backups[i].Name < backups[j].Name
		}
		if ti == nil {
			return false
		}
		if tj == nil {
			return true
		}
		return ti.Before(*tj)
	})

	return backups, nil
}

func fetchDataUploads(ctx context.Context, dynClient dynamic.Interface, namespace string) ([]DataUploadInfo, error) {
	list, err := dynClient.Resource(dataUploadGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var uploads []DataUploadInfo
	for _, item := range list.Items {
		du := DataUploadInfo{
			Name: item.GetName(),
		}

		labels := item.GetLabels()
		du.BackupName = labels["velero.io/backup-name"]

		annotations := item.GetAnnotations()
		du.VMName = getAnnotation(annotations, "vm-name")
		du.VMNamespace = getAnnotation(annotations, "vm-namespace")

		if status, ok := item.Object["status"].(map[string]interface{}); ok {
			du.Phase = getString(status, "phase")
			du.StartTime = getTime(status, "startTimestamp")
			du.Message = getString(status, "message")

			if progress, ok := status["progress"].(map[string]interface{}); ok {
				du.BytesDone = getInt64(progress, "bytesDone")
				du.TotalBytes = getInt64(progress, "totalBytes")
			}
		}

		if spec, ok := item.Object["spec"].(map[string]interface{}); ok {
			du.DataMover = getString(spec, "datamover")
			du.Node = getString(spec, "selectedNode")

			// Fallback VM namespace from sourceNamespace
			if du.VMNamespace == "" {
				du.VMNamespace = getString(spec, "sourceNamespace")
			}
		}

		uploads = append(uploads, du)
	}

	return uploads, nil
}

func fetchPods(ctx context.Context, typedClient kubernetes.Interface, namespace string, logLines int64) ([]PodInfo, *PodInfo, *PodInfo, error) {
	var uploaderPods []PodInfo
	var controllerPod, veleroPod *PodInfo

	// Uploader pods
	uploaders, err := typedClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "kubevirt-datamover.io/pod-type=uploader",
	})
	if err != nil {
		return nil, nil, nil, err
	}
	for _, p := range uploaders.Items {
		pi := PodInfo{
			Name:    p.Name,
			Phase:   string(p.Status.Phase),
			Node:    p.Spec.NodeName,
			PodType: "uploader",
		}
		// Extract backup type from container env (check multiple names + init containers)
		extractBackupType := func(envName string) string {
			for _, c := range p.Spec.InitContainers {
				for _, e := range c.Env {
					if e.Name == envName && e.Value != "" {
						return e.Value
					}
				}
			}
			for _, c := range p.Spec.Containers {
				for _, e := range c.Env {
					if e.Name == envName && e.Value != "" {
						return e.Value
					}
				}
			}
			return ""
		}
		if v := extractBackupType("BACKUP_TYPE"); v != "" {
			pi.BackupType = v
		} else if v := extractBackupType("KUBEVIRT_DATAMOVER_CHECKPOINT_TYPE"); v != "" {
			pi.BackupType = v
		}
		pi.Logs = fetchPodLogs(ctx, typedClient, namespace, p.Name, logLines)
		uploaderPods = append(uploaderPods, pi)
	}

	// Controller pod
	controllers, err := typedClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "control-plane=oadp-kubevirt-datamover-controller",
	})
	if err == nil && len(controllers.Items) > 0 {
		p := controllers.Items[0]
		cp := PodInfo{
			Name:    p.Name,
			Phase:   string(p.Status.Phase),
			Node:    p.Spec.NodeName,
			PodType: "controller",
		}
		cp.Logs = fetchPodLogs(ctx, typedClient, namespace, p.Name, logLines)
		controllerPod = &cp
	}

	// Velero pod
	veleros, err := typedClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "deploy=velero",
	})
	if err == nil && len(veleros.Items) > 0 {
		p := veleros.Items[0]
		vp := PodInfo{
			Name:    p.Name,
			Phase:   string(p.Status.Phase),
			Node:    p.Spec.NodeName,
			PodType: "velero",
		}
		vp.Logs = fetchPodLogs(ctx, typedClient, namespace, p.Name, logLines)
		veleroPod = &vp
	}

	return uploaderPods, controllerPod, veleroPod, nil
}

// Unstructured field helpers

func getString(obj map[string]interface{}, key string) string {
	if v, ok := obj[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt64(obj map[string]interface{}, key string) int64 {
	if v, ok := obj[key]; ok {
		switch n := v.(type) {
		case int64:
			return n
		case float64:
			return int64(n)
		case string:
			if i, err := strconv.ParseInt(n, 10, 64); err == nil {
				return i
			}
		}
	}
	return 0
}

func getTime(obj map[string]interface{}, key string) *time.Time {
	s := getString(obj, key)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

func getStringSlice(obj map[string]interface{}, key string) []string {
	if v, ok := obj[key]; ok {
		if arr, ok := v.([]interface{}); ok {
			var result []string
			for _, item := range arr {
				if s, ok := item.(string); ok {
					result = append(result, s)
				}
			}
			return result
		}
	}
	return nil
}

func getBool(obj map[string]interface{}, key string) bool {
	if v, ok := obj[key]; ok {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			return strings.EqualFold(b, "true")
		}
	}
	return false
}

// getAnnotation tries both kubevirt.io and kubevirt-datamover.io prefixes.
func getAnnotation(annotations map[string]string, suffix string) string {
	prefixes := []string{"kubevirt.io/", "kubevirt-datamover.io/"}
	for _, prefix := range prefixes {
		if v, ok := annotations[prefix+suffix]; ok && v != "" {
			return v
		}
	}
	return ""
}
