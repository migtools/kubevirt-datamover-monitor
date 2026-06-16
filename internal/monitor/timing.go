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
	"os"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

// watchEventMsg carries a DataUpload phase observation from the K8s Watch.
type watchEventMsg struct {
	Name        string
	BackupName  string
	Phase       string
	VMName      string
	VMNamespace string
	EventType   watch.EventType
}

// watchClosedMsg signals the watch channel has been closed.
type watchClosedMsg struct{}

// allPhases defines the ordered columns for the timing report.
var allPhases = []string{
	"New", "Accepted", "Prepared", "InProgress",
	"Canceling", "Canceled", "Completed", "Failed",
}

var terminalPhases = map[string]bool{
	"Completed": true,
	"Failed":    true,
	"Canceled":  true,
}

// startDataUploadWatch runs a reconnecting K8s Watch on DataUpload resources.
// Events are sent to the provided channel. Exits when ctx is cancelled.
func startDataUploadWatch(ctx context.Context, dynClient dynamic.Interface, namespace string, events chan<- watchEventMsg) {
	defer close(events)
	for {
		if ctx.Err() != nil {
			return
		}
		watcher, err := dynClient.Resource(dataUploadGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		for event := range watcher.ResultChan() {
			if event.Type == watch.Error {
				continue
			}
			obj, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}

			msg := watchEventMsg{
				Name:      obj.GetName(),
				EventType: event.Type,
			}

			labels := obj.GetLabels()
			msg.BackupName = labels["velero.io/backup-name"]

			annotations := obj.GetAnnotations()
			msg.VMName = getAnnotation(annotations, "vm-name")
			msg.VMNamespace = getAnnotation(annotations, "vm-namespace")

			if status, ok := obj.Object["status"].(map[string]interface{}); ok {
				msg.Phase = getString(status, "phase")
			}

			if msg.VMNamespace == "" {
				if spec, ok := obj.Object["spec"].(map[string]interface{}); ok {
					msg.VMNamespace = getString(spec, "sourceNamespace")
				}
			}

			select {
			case events <- msg:
			case <-ctx.Done():
				watcher.Stop()
				return
			}
		}

		if ctx.Err() != nil {
			return
		}
	}
}

// readWatchEvent returns a Cmd that reads one event from the watch channel.
func readWatchEvent(ch <-chan watchEventMsg) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return watchClosedMsg{}
		}
		return event
	}
}

// recordPhaseTransition updates the timing state for a DataUpload.
func recordPhaseTransition(timings map[string]*DataUploadTiming, msg watchEventMsg) {
	if msg.Phase == "" {
		return
	}

	timing, exists := timings[msg.Name]
	if !exists {
		timing = &DataUploadTiming{
			Name:        msg.Name,
			BackupName:  msg.BackupName,
			VMName:      msg.VMName,
			VMNamespace: msg.VMNamespace,
		}
		timings[msg.Name] = timing
	}

	if timing.BackupName == "" && msg.BackupName != "" {
		timing.BackupName = msg.BackupName
	}
	if timing.VMName == "" && msg.VMName != "" {
		timing.VMName = msg.VMName
	}
	if timing.VMNamespace == "" && msg.VMNamespace != "" {
		timing.VMNamespace = msg.VMNamespace
	}

	if timing.CurrentPhase == msg.Phase {
		return
	}

	timing.CurrentPhase = msg.Phase
	timing.Transitions = append(timing.Transitions, PhaseTransition{
		Phase:     msg.Phase,
		EnteredAt: time.Now(),
	})

	if terminalPhases[msg.Phase] {
		timing.FinalPhase = msg.Phase
	}
}

// writeTimingReport writes the timing data as a markdown pivot table.
func writeTimingReport(filename string, timings map[string]*DataUploadTiming) error {
	if len(timings) == 0 {
		return nil
	}

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	now := time.Now()
	fmt.Fprintf(f, "# DataUpload Phase Timing Report\n")
	fmt.Fprintf(f, "*Last updated: %s*\n\n", now.Format("2006-01-02 15:04:05 MST"))

	byBackup := make(map[string][]*DataUploadTiming)
	for _, t := range timings {
		key := t.BackupName
		if key == "" {
			key = "(unknown)"
		}
		byBackup[key] = append(byBackup[key], t)
	}

	backupNames := make([]string, 0, len(byBackup))
	for name := range byBackup {
		backupNames = append(backupNames, name)
	}
	sort.Strings(backupNames)

	for _, backupName := range backupNames {
		dus := byBackup[backupName]
		sort.Slice(dus, func(i, j int) bool {
			return dus[i].Name < dus[j].Name
		})

		fmt.Fprintf(f, "## Backup: %s\n\n", backupName)

		activePhaseCols := relevantPhases(dus)

		fmt.Fprintf(f, "| DataUpload | VM |")
		for _, phase := range activePhaseCols {
			fmt.Fprintf(f, " %s |", phase)
		}
		fmt.Fprintf(f, " Total |\n")

		fmt.Fprintf(f, "|---|---|")
		for range activePhaseCols {
			fmt.Fprintf(f, "---|")
		}
		fmt.Fprintf(f, "---|\n")

		for _, du := range dus {
			shortName := du.Name
			if strings.HasPrefix(shortName, du.BackupName) {
				shortName = "..." + shortName[len(du.BackupName):]
			}

			fmt.Fprintf(f, "| %s | %s |", shortName, du.VMName)

			for _, phase := range activePhaseCols {
				dur := phaseDuration(du, phase, now)
				fmt.Fprintf(f, " %s |", dur)
			}

			total := totalElapsed(du, now)
			fmt.Fprintf(f, " %s |\n", total)
		}

		fmt.Fprintln(f)
	}

	return nil
}

// relevantPhases returns only the phases that appear in any of the provided timings.
func relevantPhases(dus []*DataUploadTiming) []string {
	seen := make(map[string]bool)
	for _, du := range dus {
		for _, t := range du.Transitions {
			seen[t.Phase] = true
		}
	}
	var result []string
	for _, p := range allPhases {
		if seen[p] {
			result = append(result, p)
		}
	}
	return result
}

// phaseDuration returns the formatted duration a DataUpload spent in a phase.
func phaseDuration(du *DataUploadTiming, phase string, now time.Time) string {
	for i, t := range du.Transitions {
		if t.Phase != phase {
			continue
		}

		if i+1 < len(du.Transitions) {
			return formatDuration(du.Transitions[i+1].EnteredAt.Sub(t.EnteredAt))
		}

		if terminalPhases[phase] {
			if phase == "Completed" {
				return "✓"
			}
			return "✗"
		}

		return formatDuration(now.Sub(t.EnteredAt)) + " ⏳"
	}
	return "—"
}

// totalElapsed returns the total time from first transition to now (or completion).
func totalElapsed(du *DataUploadTiming, now time.Time) string {
	if len(du.Transitions) == 0 {
		return "—"
	}
	start := du.Transitions[0].EnteredAt
	end := now
	suffix := " ⏳"

	if du.FinalPhase != "" {
		end = du.Transitions[len(du.Transitions)-1].EnteredAt
		suffix = ""
	}

	return formatDuration(end.Sub(start)) + suffix
}

// formatDuration renders a duration in human-friendly format.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	d = d.Round(time.Second)

	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
