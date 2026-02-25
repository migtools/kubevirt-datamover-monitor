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

// Color palette
var (
	colorGreen   = lipgloss.Color("2")
	colorYellow  = lipgloss.Color("3")
	colorBlue    = lipgloss.Color("4")
	colorMagenta = lipgloss.Color("5")
	colorCyan    = lipgloss.Color("6")
	colorWhite   = lipgloss.Color("7")
	colorRed     = lipgloss.Color("1")
	colorDim     = lipgloss.Color("8")
)

// Layout styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorCyan)

	sectionHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorBlue)

	dimStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	keyStyle = lipgloss.NewStyle().
			Foreground(colorCyan)

	valueStyle = lipgloss.NewStyle().
			Foreground(colorWhite)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true)
)

// Phase badge styles
var (
	completedBadge = lipgloss.NewStyle().
			Background(colorGreen).
			Foreground(lipgloss.Color("0")).
			Padding(0, 1).
			Bold(true)

	inProgressBadge = lipgloss.NewStyle().
			Background(colorYellow).
			Foreground(lipgloss.Color("0")).
			Padding(0, 1).
			Bold(true)

	failedBadge = lipgloss.NewStyle().
			Background(colorRed).
			Foreground(colorWhite).
			Padding(0, 1).
			Bold(true)

	pendingBadge = lipgloss.NewStyle().
			Background(colorDim).
			Foreground(colorWhite).
			Padding(0, 1)

	newBadge = lipgloss.NewStyle().
			Background(colorBlue).
			Foreground(colorWhite).
			Padding(0, 1)
)

// Backup type badges
var (
	fullBadge = lipgloss.NewStyle().
			Background(colorBlue).
			Foreground(colorWhite).
			Padding(0, 1).
			Bold(true)

	incrementalBadge = lipgloss.NewStyle().
				Background(colorMagenta).
				Foreground(colorWhite).
				Padding(0, 1).
				Bold(true)
)

// Progress bar styles
var (
	progressFilled = lipgloss.NewStyle().
			Foreground(colorCyan)

	progressEmpty = lipgloss.NewStyle().
			Foreground(colorDim)
)

// Log highlighting styles
var (
	logError   = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
	logWarn    = lipgloss.NewStyle().Foreground(colorYellow).Bold(true)
	logSuccess = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
	logActive  = lipgloss.NewStyle().Foreground(colorCyan)
)

// Chain tree styles
var (
	chainFullDot  = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
	chainIncrConn = lipgloss.NewStyle().Foreground(colorDim)
)

// phaseBadge returns a styled badge for a given phase string.
func phaseBadge(phase string) string {
	switch phase {
	case "Completed", "Succeeded":
		return completedBadge.Render(phase)
	case "InProgress", "Accepted", "Prepared",
		"WaitingForPluginOperations", "Finalizing":
		return inProgressBadge.Render(phase)
	case "Failed", "PartiallyFailed",
		"WaitingForPluginOperationsPartiallyFailed", "FinalizingPartiallyFailed":
		return failedBadge.Render(phase)
	case "New":
		return newBadge.Render(phase)
	case "Canceled":
		return failedBadge.Render(phase)
	default:
		if phase == "" {
			return pendingBadge.Render("Pending")
		}
		return pendingBadge.Render(phase)
	}
}

// phaseDot returns a colored dot character for a phase.
func phaseDot(phase string) string {
	switch phase {
	case "Completed", "Succeeded":
		return lipgloss.NewStyle().Foreground(colorGreen).Render("●")
	case "InProgress", "Accepted", "Prepared",
		"WaitingForPluginOperations", "Finalizing":
		return lipgloss.NewStyle().Foreground(colorYellow).Render("●")
	case "Failed", "PartiallyFailed",
		"WaitingForPluginOperationsPartiallyFailed", "FinalizingPartiallyFailed":
		return lipgloss.NewStyle().Foreground(colorRed).Render("●")
	default:
		return lipgloss.NewStyle().Foreground(colorDim).Render("●")
	}
}

// typeBadge returns a styled badge for Full or Incremental.
func typeBadge(t string) string {
	switch strings.ToLower(t) {
	case "full":
		return fullBadge.Render("Full")
	case "incremental":
		return incrementalBadge.Render("Incr")
	default:
		return pendingBadge.Render(t)
	}
}

// sectionHeader renders a section header line: ── TITLE ─────────
func sectionHeader(title string, width int) string {
	prefix := "── "
	suffix := " "
	content := prefix + title + suffix
	remaining := width - len(content)
	if remaining < 0 {
		remaining = 0
	}
	return sectionHeaderStyle.Render(content + strings.Repeat("─", remaining))
}

// separator renders a thin horizontal line.
func separator(width int) string {
	return dimStyle.Render(strings.Repeat("─", width))
}

// humanBytes formats bytes into a human-readable string.
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < len(units)-1; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(b)/float64(div), units[exp])
}

// elapsedSince returns a human-readable elapsed time string.
func elapsedSince(t *time.Time) string {
	if t == nil {
		return ""
	}
	d := time.Since(*t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// shortTimestamp formats a timestamp string to a compact form.
func shortTimestamp(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.Format("2006-01-02 15:04:05")
}
