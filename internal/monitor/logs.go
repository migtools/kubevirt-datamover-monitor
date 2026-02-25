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
	"encoding/json"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// fetchPodLogs retrieves the last N lines of logs from a pod.
func fetchPodLogs(ctx context.Context, client kubernetes.Interface, namespace, podName string, tailLines int64) []string {
	opts := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}
	req := client.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return []string{dimStyle.Render(fmt.Sprintf("(logs unavailable: %v)", err))}
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		return []string{dimStyle.Render(fmt.Sprintf("(error reading logs: %v)", err))}
	}

	return filterLogLines(string(data))
}

// filterLogLines processes raw log output into display-ready lines.
func filterLogLines(raw string) []string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Try to parse as JSON log — use level-based highlighting
		if parsed := parseJSONLogLine(line); parsed != nil {
			result = append(result, highlightByLevel(parsed))
		} else {
			// Unstructured log — use keyword-based highlighting
			result = append(result, highlightLogLine(line))
		}
	}
	return result
}

// jsonLog represents a structured JSON log entry.
type jsonLog struct {
	Level      string `json:"level"`
	Msg        string `json:"msg"`
	Controller string `json:"controller"`
	Error      string `json:"error"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
}

// parsedLogLine holds the formatted text and the extracted log level.
type parsedLogLine struct {
	text  string
	level string // "error", "info", "debug", "warn", etc.
}

// parseJSONLogLine tries to parse a JSON log line into a compact summary.
func parseJSONLogLine(line string) *parsedLogLine {
	if !strings.HasPrefix(line, "{") {
		return nil
	}

	var entry jsonLog
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return nil
	}

	if entry.Msg == "" {
		return nil
	}

	var parts []string
	if entry.Level != "" {
		parts = append(parts, fmt.Sprintf("[%s]", strings.ToUpper(entry.Level)))
	}
	if entry.Controller != "" {
		parts = append(parts, fmt.Sprintf("(%s)", entry.Controller))
	}
	parts = append(parts, entry.Msg)
	if entry.Name != "" {
		parts = append(parts, fmt.Sprintf("name=%s", entry.Name))
	}
	if entry.Namespace != "" {
		parts = append(parts, fmt.Sprintf("ns=%s", entry.Namespace))
	}
	if entry.Error != "" {
		parts = append(parts, fmt.Sprintf("err=%s", entry.Error))
	}

	return &parsedLogLine{
		text:  strings.Join(parts, " "),
		level: strings.ToLower(entry.Level),
	}
}

// highlightByLevel applies color based on the structured log level.
func highlightByLevel(pl *parsedLogLine) string {
	switch pl.level {
	case "error", "fatal", "panic":
		return logError.Render(pl.text)
	case "warn", "warning":
		return logWarn.Render(pl.text)
	case "info":
		// For info lines, still highlight key messages
		lower := strings.ToLower(pl.text)
		if strings.Contains(lower, "completed") || strings.Contains(lower, "success") {
			return logSuccess.Render(pl.text)
		}
		if strings.Contains(lower, "uploading") || strings.Contains(lower, "progress") {
			return logActive.Render(pl.text)
		}
		return pl.text
	default:
		return pl.text
	}
}

// highlightLogLine applies color highlighting to unstructured log lines based on keywords.
func highlightLogLine(line string) string {
	lower := strings.ToLower(line)

	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
		return logError.Render(line)
	}
	if strings.Contains(lower, "completed") || strings.Contains(lower, "success") {
		return logSuccess.Render(line)
	}
	if strings.Contains(lower, "uploading") || strings.Contains(lower, "progress") || strings.Contains(lower, "reconcil") {
		return logActive.Render(line)
	}

	return line
}

// truncateLogLine truncates a line to the given width.
func truncateLogLine(line string, maxWidth int) string {
	if maxWidth <= 0 || len(line) <= maxWidth {
		return line
	}
	if maxWidth <= 3 {
		return line[:maxWidth]
	}
	return line[:maxWidth-3] + "..."
}
