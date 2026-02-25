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
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// renderView assembles all sections and applies scrolling.
func renderView(m model) string {
	width := m.width
	if width <= 0 {
		width = 80
	}

	var sections []string

	sections = append(sections, renderHeader(m, width))

	if m.state.FetchError != "" {
		sections = append(sections, errorStyle.Render("  Errors: "+m.state.FetchError))
	}

	if m.state.Storage != nil {
		sections = append(sections, renderStorage(m.state.Storage, width))
	}

	// Active backups (InProgress, New, Accepted, Prepared, WaitingForPluginOperations)
	activeBackups := filterActiveBackups(m.state.Backups)
	sections = append(sections, renderActiveBackups(activeBackups, m.state, width))

	// Completed/failed backups summary
	completedBackups := filterCompletedBackups(m.state.Backups)
	if len(completedBackups) > 0 {
		sections = append(sections, renderCompletedSummary(completedBackups, m.state, width))
	}

	// Backup chains from S3
	if len(m.state.Chains) > 0 {
		sections = append(sections, renderChains(m.state.Chains, width))
	} else if m.state.S3Error != "" {
		sections = append(sections, sectionHeader("BACKUP CHAINS", width))
		sections = append(sections, dimStyle.Render("  S3: "+m.state.S3Error))
	}

	// Uploader pods (always show section to avoid layout jumping)
	sections = append(sections, renderUploaderPods(m.state.UploaderPods, width))

	// Terminated pods cache
	terminatedVisible := filterTerminatedVisible(m.state.TerminatedPods, m.state.UploaderPods)
	if len(terminatedVisible) > 0 {
		sections = append(sections, renderTerminatedPods(terminatedVisible, width))
	}

	// Controller pod
	if m.state.ControllerPod != nil {
		sections = append(sections, renderPodSection("CONTROLLER", *m.state.ControllerPod, width))
	}

	// Velero pod
	if m.state.VeleroPod != nil {
		sections = append(sections, renderPodSection("VELERO", *m.state.VeleroPod, width))
	}

	// Pre-compute content height to determine if scrollable
	preContent := strings.Join(sections, "\n")
	preLineCount := strings.Count(preContent, "\n") + 1
	footerLineCount := 2 // separator + nav line
	totalLines := preLineCount + footerLineCount
	visibleLines := m.height - 1
	if visibleLines < 1 {
		visibleLines = 1
	}
	scrollable := totalLines > visibleLines

	sections = append(sections, renderFooter(m, width, scrollable, m.scrollY, totalLines, visibleLines))

	content := strings.Join(sections, "\n")
	lines := strings.Split(content, "\n")

	// Apply scrolling with defensive upper-bound clamping
	totalLines = len(lines)
	maxScroll := totalLines - visibleLines
	if maxScroll < 0 {
		maxScroll = 0
	}
	scrollY := m.scrollY
	if scrollY > maxScroll {
		scrollY = maxScroll
	}

	end := scrollY + visibleLines
	if end > totalLines {
		end = totalLines
	}

	return strings.Join(lines[scrollY:end], "\n")
}

// renderHeader renders the title bar.
func renderHeader(m model, width int) string {
	title := titleStyle.Render("  OADP KubeVirt DataMover Monitor")

	var statusParts []string
	activeCount := countActiveBackups(m.state.Backups)
	if activeCount > 0 {
		statusParts = append(statusParts, m.spinner.View()+fmt.Sprintf(" %d active", activeCount))
	} else {
		statusParts = append(statusParts, dimStyle.Render("idle"))
	}
	statusParts = append(statusParts, dimStyle.Render(fmt.Sprintf("ns:%s", m.config.Namespace)))
	if !m.state.LastRefresh.IsZero() {
		statusParts = append(statusParts, dimStyle.Render(m.state.LastRefresh.Format("15:04:05")))
	}

	status := strings.Join(statusParts, "  ")
	return title + "  " + status + "\n" + separator(width)
}

// renderStorage renders the storage configuration section.
func renderStorage(s *StorageInfo, width int) string {
	var b strings.Builder
	b.WriteString(sectionHeader("STORAGE", width) + "\n")

	row := func(k, v string) {
		b.WriteString(fmt.Sprintf("  %s %s\n", keyStyle.Render(k+":"), valueStyle.Render(v)))
	}

	row("Provider", s.Provider)
	row("Bucket", s.Bucket)
	if s.Prefix != "" {
		row("Prefix", s.Prefix)
	}
	row("Region", s.Region)
	if s.S3URL != "" {
		row("Endpoint", s.S3URL)
	}
	row("Credential", fmt.Sprintf("%s/%s", s.CredentialName, s.CredentialKey))

	return b.String()
}

// renderActiveBackups renders all active backup details.
func renderActiveBackups(backups []BackupInfo, state AppState, width int) string {
	var b strings.Builder
	b.WriteString(sectionHeader("ACTIVE BACKUPS", width) + "\n")

	if len(backups) == 0 {
		b.WriteString(dimStyle.Render("  (none)") + "\n")
		return b.String()
	}

	for _, bkp := range backups {
		b.WriteString(renderBackupDetail(bkp, state, width))
	}

	return b.String()
}

// renderBackupDetail renders a single backup with its DataUploads and workflow.
func renderBackupDetail(bkp BackupInfo, state AppState, width int) string {
	var b strings.Builder

	// Look up DataUpload once for this backup
	var du *DataUploadInfo
	if bkp.SnapshotMoveData {
		du = findDataUploadForBackup(bkp.Name, state.DataUploads)
	}

	// Backup header: name + phase badge + backup type badge
	b.WriteString(fmt.Sprintf("  %s  %s", valueStyle.Render(bkp.Name), phaseBadge(bkp.Phase)))

	// Backup type (Full/Incremental) — prominent badge
	if bkp.SnapshotMoveData {
		if btype := getBackupType(bkp.Name, du, state); btype != "" {
			b.WriteString("  " + typeBadge(btype))
		}
	}

	// Elapsed time
	if bkp.StartTimestamp != nil {
		if bkp.CompletionTime != nil {
			elapsed := bkp.CompletionTime.Sub(*bkp.StartTimestamp)
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s", elapsed.Round(time.Second))))
		} else {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s", elapsedSince(bkp.StartTimestamp))))
		}
	}

	// Namespaces
	if len(bkp.Namespaces) > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  ns:[%s]", strings.Join(bkp.Namespaces, ","))))
	}

	// Error/warning counts
	if bkp.Errors > 0 || bkp.Warnings > 0 {
		var counts []string
		if bkp.Errors > 0 {
			counts = append(counts, errorStyle.Render(fmt.Sprintf("%d error(s)", bkp.Errors)))
		}
		if bkp.Warnings > 0 {
			counts = append(counts, dimStyle.Render(fmt.Sprintf("%d warn", bkp.Warnings)))
		}
		b.WriteString("  " + strings.Join(counts, " "))
	}
	b.WriteString("\n")

	// Failure reason from Backup CR
	if bkp.FailureReason != "" {
		b.WriteString("    " + errorStyle.Render("Reason: "+bkp.FailureReason) + "\n")
	}

	// Workflow pipeline for snapshotMoveData backups
	if bkp.SnapshotMoveData {
		hasPod := hasUploaderPodForDU(du, state.UploaderPods, state.TerminatedPods)
		step := computeWorkflowStep(bkp, du, hasPod)
		failed := isBackupFailed(bkp, du)
		b.WriteString(renderWorkflow(step, failed))
	}

	// DataUploads for this backup
	for _, du := range state.DataUploads {
		if du.BackupName != bkp.Name {
			continue
		}
		b.WriteString(renderDataUpload(du, state, width))
	}

	return b.String()
}

// renderDataUpload renders a single DataUpload with progress bar.
func renderDataUpload(du DataUploadInfo, state AppState, width int) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("    %s %s  %s",
		phaseDot(du.Phase),
		dimStyle.Render(du.Name),
		phaseBadge(du.Phase),
	))

	// VM info
	if du.VMName != "" {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  vm:%s/%s", du.VMNamespace, du.VMName)))
	}

	// Node
	if du.Node != "" {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  node:%s", du.Node)))
	}
	b.WriteString("\n")

	// Progress bar
	if du.TotalBytes > 0 {
		bar := renderProgressBar(du.BytesDone, du.TotalBytes, width-8)

		// Throughput from pre-computed rate
		throughput := ""
		if sample, ok := state.Throughput[du.Name]; ok && sample.LastRate > 0 {
			throughput = throughputStr(int64(sample.LastRate))
		}

		b.WriteString(fmt.Sprintf("      %s  %s/%s",
			bar,
			humanBytes(du.BytesDone),
			humanBytes(du.TotalBytes),
		))
		if throughput != "" {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s", throughput)))
		}
		b.WriteString("\n")
	}

	// DataUpload failure message
	if du.Message != "" && (du.Phase == "Failed" || du.Phase == "Canceled") {
		b.WriteString("      " + errorStyle.Render(du.Message) + "\n")
	}

	return b.String()
}

// renderProgressBar renders a text-based progress bar.
func renderProgressBar(done, total int64, maxWidth int) string {
	barWidth := maxWidth - 20
	if barWidth < 10 {
		barWidth = 10
	}
	if barWidth > 40 {
		barWidth = 40
	}

	pct := float64(done) / float64(total)
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(barWidth))
	empty := barWidth - filled

	bar := progressFilled.Render(strings.Repeat("█", filled)) +
		progressEmpty.Render(strings.Repeat("░", empty))

	return fmt.Sprintf("[%s] %3d%%", bar, int(pct*100))
}

// renderCompletedSummary renders a compact summary of completed backups.
func renderCompletedSummary(backups []BackupInfo, state AppState, width int) string {
	var b strings.Builder
	b.WriteString(sectionHeader("RECENT BACKUPS", width) + "\n")

	for _, bkp := range backups {
		elapsed := ""
		if bkp.StartTimestamp != nil && bkp.CompletionTime != nil {
			d := bkp.CompletionTime.Sub(*bkp.StartTimestamp)
			elapsed = dimStyle.Render(fmt.Sprintf("  %s", d.Round(time.Second)))
		}

		// Look up DataUpload once per backup
		var du *DataUploadInfo
		if bkp.SnapshotMoveData {
			du = findDataUploadForBackup(bkp.Name, state.DataUploads)
		}

		// Backup type badge
		typeBadgeStr := ""
		if bkp.SnapshotMoveData {
			if btype := getBackupType(bkp.Name, du, state); btype != "" {
				typeBadgeStr = "  " + typeBadge(btype)
			}
		}

		// Error/warning counts
		errWarn := ""
		if bkp.Errors > 0 || bkp.Warnings > 0 {
			var counts []string
			if bkp.Errors > 0 {
				counts = append(counts, errorStyle.Render(fmt.Sprintf("%d err", bkp.Errors)))
			}
			if bkp.Warnings > 0 {
				counts = append(counts, dimStyle.Render(fmt.Sprintf("%d warn", bkp.Warnings)))
			}
			errWarn = "  " + strings.Join(counts, " ")
		}

		b.WriteString(fmt.Sprintf("  %s %s  %s%s%s%s\n",
			phaseDot(bkp.Phase),
			bkp.Name,
			phaseBadge(bkp.Phase),
			typeBadgeStr,
			errWarn,
			elapsed,
		))

		// Failure reason
		if bkp.FailureReason != "" {
			b.WriteString("    " + errorStyle.Render("Reason: "+bkp.FailureReason) + "\n")
		}

		// DataUpload failure message
		if du != nil && du.Message != "" && (du.Phase == "Failed" || du.Phase == "Canceled") {
			b.WriteString("    " + errorStyle.Render("DU: "+du.Message) + "\n")
		}

		// Show workflow for snapshotMoveData backups
		if bkp.SnapshotMoveData {
			hasPod := hasUploaderPodForDU(du, state.UploaderPods, state.TerminatedPods)
			step := computeWorkflowStep(bkp, du, hasPod)
			failed := isBackupFailed(bkp, du)
			b.WriteString(renderWorkflow(step, failed))
		}
	}

	return b.String()
}

// renderChains renders all VM backup chains.
func renderChains(chains map[VMIdentity]*VMIndex, width int) string {
	var b strings.Builder
	b.WriteString(sectionHeader("BACKUP CHAINS (S3)", width) + "\n")

	for _, idx := range chains {
		b.WriteString(renderChainTree(idx, width))
		b.WriteString("\n")
	}

	return b.String()
}

// renderChainTree renders one VM's backup chain.
func renderChainTree(idx *VMIndex, width int) string {
	var b strings.Builder

	totalSize := vmIndexTotalSize(idx)
	b.WriteString(fmt.Sprintf("  %s (%s)%s\n",
		valueStyle.Render(idx.VMName),
		dimStyle.Render(idx.Namespace),
		dimStyle.Render(fmt.Sprintf("%sTotal: %s", strings.Repeat(" ", 4), humanBytes(totalSize))),
	))

	for i, cp := range idx.Checkpoints {
		size := checkpointTotalSize(cp)
		ts := shortTimestamp(cp.Timestamp)
		backups := strings.Join(cp.ReferencedBy, ",")
		if backups == "" {
			backups = "-"
		}

		var prefix string
		if cp.Type == "Full" {
			prefix = chainFullDot.Render("●") + " Full "
		} else {
			isLast := i == len(idx.Checkpoints)-1
			nextIsFull := !isLast && idx.Checkpoints[i+1].Type == "Full"
			if isLast || nextIsFull {
				prefix = chainIncrConn.Render("└─") + " Incr "
			} else {
				prefix = chainIncrConn.Render("├─") + " Incr "
			}
		}

		status := lipgloss.NewStyle().Foreground(colorGreen).Render("✓ Completed")

		b.WriteString(fmt.Sprintf("  %s| %s | %8s | %s     %s\n",
			prefix,
			fmt.Sprintf("%-6s", backups),
			humanBytes(size),
			ts,
			status,
		))
	}

	return b.String()
}

// renderUploaderPods renders all uploader pods.
func renderUploaderPods(pods []PodInfo, width int) string {
	var b strings.Builder
	b.WriteString(sectionHeader("UPLOADER PODS", width) + "\n")

	if len(pods) == 0 {
		b.WriteString(dimStyle.Render("  (none)") + "\n")
		return b.String()
	}

	for _, p := range pods {
		b.WriteString(renderPodInfo(p, width))
	}

	return b.String()
}

// renderTerminatedPods renders cached terminated pods with their last logs.
func renderTerminatedPods(pods []PodInfo, width int) string {
	var b strings.Builder
	b.WriteString(sectionHeader("TERMINATED PODS (cached)", width) + "\n")

	for _, p := range pods {
		b.WriteString(renderPodInfo(p, width))
	}

	return b.String()
}

// renderPodSection renders a named pod section (controller, velero).
func renderPodSection(title string, pod PodInfo, width int) string {
	var b strings.Builder
	b.WriteString(sectionHeader(title, width) + "\n")
	b.WriteString(renderPodInfo(pod, width))
	return b.String()
}

// renderPodInfo renders a single pod with its logs.
func renderPodInfo(pod PodInfo, width int) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("  %s %s  %s",
		phaseDot(pod.Phase),
		valueStyle.Render(pod.Name),
		phaseBadge(pod.Phase),
	))

	if pod.Node != "" {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  node:%s", pod.Node)))
	}
	if pod.BackupType != "" {
		b.WriteString("  " + typeBadge(pod.BackupType))
	}
	b.WriteString("\n")

	// Logs
	if len(pod.Logs) > 0 {
		b.WriteString(renderLogs(pod.Logs, width))
	}

	return b.String()
}

// renderLogs renders indented log lines.
func renderLogs(logs []string, width int) string {
	var b strings.Builder
	for _, line := range logs {
		truncated := truncateLogLine(line, width-6)
		b.WriteString("      " + truncated + "\n")
	}
	return b.String()
}

// renderFooter renders navigation hints with scroll position when applicable.
func renderFooter(m model, width int, scrollable bool, scrollY, totalLines, visibleLines int) string {
	var b strings.Builder
	b.WriteString(separator(width) + "\n")

	var left string
	if scrollable {
		pos := fmt.Sprintf("%d/%d", scrollY+visibleLines, totalLines)
		left = dimStyle.Render("  j/k:scroll  g/G:top/bottom  pgup/pgdn  q:quit  " + pos)
	} else {
		left = dimStyle.Render("  q:quit")
	}

	right := ""
	if m.config.NoS3 {
		right = dimStyle.Render("S3:disabled")
	} else {
		right = dimStyle.Render(fmt.Sprintf("refresh:%s  chain:%s", m.config.Interval, m.config.ChainInterval))
	}

	// Estimate left text length (without ANSI codes)
	leftLen := 8 // "  q:quit"
	if scrollable {
		leftLen = 52 + len(fmt.Sprintf("%d/%d", scrollY+visibleLines, totalLines))
	}
	gap := width - leftLen - len(fmt.Sprintf("refresh:%s  chain:%s", m.config.Interval, m.config.ChainInterval)) - 2
	if gap < 2 {
		gap = 2
	}
	b.WriteString(left + strings.Repeat(" ", gap) + right)

	return b.String()
}

// throughputStr formats bytes/sec into a human-readable throughput string.
func throughputStr(bps int64) string {
	return humanBytes(bps) + "/s"
}

// ── Workflow pipeline ──────────────────────────────────────────────────

// Workflow steps in order. Each maps to observable Backup/DataUpload/Pod state.
const (
	wfBackup     = iota // Backup CR created (Velero)
	wfDataUpload        // DataUpload created (Velero Plugin)
	wfVMBT              // VMBT prepared (DM Controller)
	wfVMB               // VM snapshot in progress (KubeVirt)
	wfPreparing         // PV rebind + pod creation (DM Controller)
	wfUploading         // Data transfer to S3 (DM Pod)
	wfFinalizing        // Finalizing metadata (Velero)
	wfDone              // Complete
	wfCount             // sentinel
)

// workflowStepInfo describes a single workflow step with its owner and description.
type workflowStepInfo struct {
	Label       string // Short label for pipeline display
	Owner       string // Component responsible
	Description string // What's happening at this step
}

var workflowSteps = [wfCount]workflowStepInfo{
	{Label: "Backup", Owner: "Velero", Description: "scanning namespace resources"},
	{Label: "DU", Owner: "Velero Plugin", Description: "creating DataUpload, validating VM"},
	{Label: "VMBT", Owner: "DM Controller", Description: "preparing backup tracker from S3"},
	{Label: "VMB", Owner: "KubeVirt", Description: "taking VM disk snapshot (QEMU)"},
	{Label: "Prepare", Owner: "DM Controller", Description: "rebinding PV, creating uploader pod"},
	{Label: "Upload", Owner: "DM Pod", Description: "transferring data to object storage"},
	{Label: "Finalize", Owner: "Velero", Description: "finalizing backup metadata"},
	{Label: "Done", Owner: "", Description: ""},
}

// findDataUploadForBackup returns the first DataUpload matching a backup name.
func findDataUploadForBackup(backupName string, dus []DataUploadInfo) *DataUploadInfo {
	for i := range dus {
		if dus[i].BackupName == backupName {
			return &dus[i]
		}
	}
	return nil
}

// hasUploaderPodForDU checks if an uploader pod exists for this DataUpload (live or cached).
func hasUploaderPodForDU(du *DataUploadInfo, livePods []PodInfo, cached map[string]PodInfo) bool {
	if du == nil {
		return false
	}
	podName := "kubevirt-dm-" + du.Name
	for _, p := range livePods {
		if p.Name == podName {
			return true
		}
	}
	if _, ok := cached[podName]; ok {
		return true
	}
	return false
}

// computeWorkflowStep determines which workflow step is current based on observable state.
func computeWorkflowStep(bkp BackupInfo, du *DataUploadInfo, hasPod bool) int {
	// Terminal states
	switch bkp.Phase {
	case "Completed":
		return wfDone
	case "Failed", "PartiallyFailed", "Canceled":
		if du == nil {
			return wfBackup
		}
		switch du.Phase {
		case "Completed":
			return wfFinalizing
		case "InProgress", "Failed", "Canceled":
			return wfUploading
		case "Prepared":
			return wfPreparing
		case "Accepted":
			return wfVMB
		case "New":
			return wfDataUpload
		default:
			return wfBackup
		}
	case "Finalizing", "FinalizingPartiallyFailed":
		return wfFinalizing
	}

	if du == nil {
		return wfBackup
	}

	switch du.Phase {
	case "", "New":
		return wfDataUpload
	case "Accepted":
		// DU=Accepted covers: VMBT creation, VMB creation, VMB running.
		// VMBT creation is near-instant, so by the time we poll, VMB is the active step.
		return wfVMB
	case "Prepared":
		if hasPod {
			return wfUploading
		}
		return wfPreparing
	case "InProgress":
		return wfUploading
	case "Completed":
		return wfFinalizing
	case "Failed", "Canceled":
		return wfUploading
	default:
		return wfBackup
	}
}

// isBackupFailed returns true if the backup or its DataUpload is in a failed state.
func isBackupFailed(bkp BackupInfo, du *DataUploadInfo) bool {
	switch bkp.Phase {
	case "Failed", "PartiallyFailed", "Canceled":
		return true
	}
	if du != nil {
		switch du.Phase {
		case "Failed", "Canceled":
			return true
		}
	}
	return false
}

// getBackupType returns "Full" or "Incremental" for a backup, checking chain data and pod cache.
func getBackupType(backupName string, du *DataUploadInfo, state AppState) string {
	// 1. Check S3 chain data (most reliable, works for completed backups)
	for _, idx := range state.Chains {
		for _, cp := range idx.Checkpoints {
			for _, ref := range cp.ReferencedBy {
				if ref == backupName {
					return cp.Type
				}
			}
		}
	}
	// 2. Check cached uploader pod env var (works for active/recent backups)
	if du != nil {
		podName := "kubevirt-dm-" + du.Name
		for _, p := range state.UploaderPods {
			if p.Name == podName && p.BackupType != "" {
				return p.BackupType
			}
		}
		if p, ok := state.TerminatedPods[podName]; ok && p.BackupType != "" {
			return p.BackupType
		}
	}
	return ""
}

// renderWorkflow renders a compact horizontal workflow pipeline with active step description.
//
// Active example:
//
//	✓ Backup → ✓ DU → ✓ VMBT → ◉ VMB → ○ Prepare → ○ Upload → ○ Finalize → ○ Done
//	◉ KubeVirt: taking VM disk snapshot (QEMU)
//
// Completed example:
//
//	✓ Backup → ✓ DU → ✓ VMBT → ✓ VMB → ✓ Prepare → ✓ Upload → ✓ Finalize → ✓ Done
func renderWorkflow(currentStep int, failed bool) string {
	arrow := dimStyle.Render(" → ")

	var parts []string
	for i := 0; i < wfCount; i++ {
		label := workflowSteps[i].Label
		var part string

		switch {
		case i < currentStep:
			part = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ " + label)
		case i == currentStep:
			switch {
			case failed:
				part = lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("✗ " + label)
			case currentStep == wfDone:
				part = lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render("✓ " + label)
			default:
				part = lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render("◉ " + label)
			}
		default:
			if failed {
				continue
			}
			part = dimStyle.Render("○ " + label)
		}
		parts = append(parts, part)
	}

	result := "    " + strings.Join(parts, arrow) + "\n"

	// Add description line for active (non-terminal) step
	if currentStep < wfDone {
		info := workflowSteps[currentStep]
		if failed {
			result += "    " + lipgloss.NewStyle().Foreground(colorRed).Render(
				fmt.Sprintf("✗ %s: %s", info.Owner, info.Description)) + "\n"
		} else {
			result += "    " + lipgloss.NewStyle().Foreground(colorYellow).Render(
				fmt.Sprintf("◉ %s: %s", info.Owner, info.Description)) + "\n"
		}
	}

	return result
}

// Helper filters

// isActivePhase returns true if the backup phase represents an in-progress backup.
func isActivePhase(phase string) bool {
	switch phase {
	case "InProgress", "New", "Accepted", "Prepared", "",
		"WaitingForPluginOperations", "WaitingForPluginOperationsPartiallyFailed",
		"Finalizing", "FinalizingPartiallyFailed":
		return true
	}
	return false
}

func filterActiveBackups(backups []BackupInfo) []BackupInfo {
	var result []BackupInfo
	for _, b := range backups {
		if isActivePhase(b.Phase) {
			result = append(result, b)
		}
	}
	return result
}

func filterCompletedBackups(backups []BackupInfo) []BackupInfo {
	var result []BackupInfo
	for _, b := range backups {
		if !isActivePhase(b.Phase) {
			result = append(result, b)
		}
	}
	return result
}

func countActiveBackups(backups []BackupInfo) int {
	count := 0
	for _, b := range backups {
		if isActivePhase(b.Phase) {
			count++
		}
	}
	return count
}

// filterTerminatedVisible returns terminated pods not currently in the live list.
func filterTerminatedVisible(terminated map[string]PodInfo, live []PodInfo) []PodInfo {
	liveNames := make(map[string]bool)
	for _, p := range live {
		liveNames[p.Name] = true
	}
	var result []PodInfo
	for _, p := range terminated {
		if !liveNames[p.Name] {
			result = append(result, p)
		}
	}
	return result
}
