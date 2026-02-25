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
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Config holds CLI flags.
type Config struct {
	Namespace     string
	Interval      time.Duration
	ChainInterval time.Duration
	LogLines      int64
	NoS3          bool
	DebugFile     string
}

// Run creates the bubbletea model and runs the TUI program.
func Run(cfg Config, dynClient dynamic.Interface, typedClient kubernetes.Interface) error {
	m := newModel(cfg, dynClient, typedClient)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// model is the bubbletea Model.
type model struct {
	config      Config
	dynClient   dynamic.Interface
	typedClient kubernetes.Interface
	state       AppState
	spinner     spinner.Model
	width       int
	height      int
	scrollY     int
	ready       bool
	lastChain   time.Time
}

// Scroll constants
const (
	mouseWheelDelta   = 3     // lines per mouse wheel tick
	maxScrollSentinel = 99999 // used for "scroll to end"
)

// Message types
type tickMsg struct{}
type dataMsg struct{ state AppState }
type chainMsg struct {
	chains map[VMIdentity]*VMIndex
	err    string
}

func newModel(cfg Config, dyn dynamic.Interface, typed kubernetes.Interface) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(colorCyan)

	return model{
		config:      cfg,
		dynClient:   dyn,
		typedClient: typed,
		spinner:     s,
		state: AppState{
			Chains:           make(map[VMIdentity]*VMIndex),
			Throughput:       make(map[string]ThroughputSample),
			TerminatedPods:   make(map[string]PodInfo),
			PrevBackupPhases: make(map[string]string),
		},
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.doFetch(),
		scheduleTickCmd(m.config.Interval),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.scrollY++
		case "k", "up":
			m.scrollY--
		case "g", "home":
			m.scrollY = 0
		case "G", "end":
			m.scrollY = maxScrollSentinel
		case "pgdown":
			m.scrollY += m.height / 2
		case "pgup":
			m.scrollY -= m.height / 2
		}

	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scrollY -= mouseWheelDelta
		case tea.MouseWheelDown:
			m.scrollY += mouseWheelDelta
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true

	case tickMsg:
		cmds = append(cmds, m.doFetch())
		cmds = append(cmds, scheduleTickCmd(m.config.Interval))

		// Check if we should refresh chains
		if !m.config.NoS3 && m.state.Storage != nil &&
			time.Since(m.lastChain) >= m.config.ChainInterval {
			cmds = append(cmds, m.doFetchChains())
		}

	case dataMsg:
		prevPhases := m.state.PrevBackupPhases
		m.state = msg.state
		m.writeDebugSnapshot()

		if !m.config.NoS3 && m.state.Storage != nil {
			if m.lastChain.IsZero() {
				// Initial chain fetch after first successful data fetch
				cmds = append(cmds, m.doFetchChains())
			} else {
				// Check if any backup just transitioned to Completed
				for name, newPhase := range m.state.PrevBackupPhases {
					oldPhase := prevPhases[name]
					if oldPhase != newPhase && (newPhase == "Completed" || newPhase == "PartiallyFailed") {
						cmds = append(cmds, m.doFetchChains())
						break
					}
				}
			}
		}

	case chainMsg:
		m.lastChain = time.Now()
		if msg.chains != nil {
			m.state.Chains = msg.chains
		}
		m.state.S3Error = msg.err

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	// Clamp lower bound here; upper bound is clamped in renderView
	// where actual content height is known.
	if m.scrollY < 0 {
		m.scrollY = 0
	}

	return m, tea.Batch(cmds...)
}

func (m model) View() tea.View {
	var content string
	if !m.ready {
		content = "\n  Initializing..."
	} else {
		content = renderView(m)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// doFetch runs fetchAll in a goroutine and returns a dataMsg.
func (m model) doFetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		state := fetchAll(ctx, m.dynClient, m.typedClient, m.config.Namespace, m.config.LogLines, &m.state)
		return dataMsg{state: state}
	}
}

// doFetchChains runs S3 chain fetch in a goroutine and returns a chainMsg.
func (m model) doFetchChains() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if m.state.Storage == nil {
			return chainMsg{err: "no storage info available"}
		}

		// Discover VMs from DataUploads
		vms := discoverVMs(m.state.DataUploads)
		if len(vms) == 0 {
			// No VMs discovered yet, keep existing chains
			return chainMsg{chains: m.state.Chains}
		}

		// Create S3 client
		s3Client, err := newS3Client(ctx, m.typedClient, m.config.Namespace, m.state.Storage)
		if err != nil {
			return chainMsg{err: fmt.Sprintf("S3 client: %v", err)}
		}

		// Fetch chains
		chains, errStr := fetchChains(ctx, s3Client, m.state.Storage, vms)
		// Merge with existing chains (keep VMs we already know about)
		for k, v := range m.state.Chains {
			if _, exists := chains[k]; !exists {
				chains[k] = v
			}
		}

		return chainMsg{chains: chains, err: errStr}
	}
}

func scheduleTickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

// writeDebugSnapshot writes the current state to the debug file.
func (m model) writeDebugSnapshot() {
	if m.config.DebugFile == "" {
		return
	}
	f, err := os.Create(m.config.DebugFile)
	if err != nil {
		return
	}
	defer f.Close()

	ts := m.state.LastRefresh.Format("15:04:05.000")
	fmt.Fprintf(f, "=== DEBUG SNAPSHOT %s ===\n\n", ts)

	if m.state.FetchError != "" {
		fmt.Fprintf(f, "FETCH ERROR: %s\n\n", m.state.FetchError)
	}

	if m.state.Storage != nil {
		fmt.Fprintf(f, "STORAGE: provider=%s bucket=%s prefix=%s region=%s\n",
			m.state.Storage.Provider, m.state.Storage.Bucket,
			m.state.Storage.Prefix, m.state.Storage.Region)
		fmt.Fprintf(f, "  credential=%s/%s s3url=%s pathStyle=%v\n\n",
			m.state.Storage.CredentialName, m.state.Storage.CredentialKey,
			m.state.Storage.S3URL, m.state.Storage.S3ForcePathStyle)
	} else {
		fmt.Fprintf(f, "STORAGE: nil\n\n")
	}

	fmt.Fprintf(f, "BACKUPS (%d):\n", len(m.state.Backups))
	for _, b := range m.state.Backups {
		fmt.Fprintf(f, "  name=%-40s phase=%-15s snapshotMove=%v ns=%v errors=%d warnings=%d\n",
			b.Name, b.Phase, b.SnapshotMoveData, b.Namespaces, b.Errors, b.Warnings)
		if b.FailureReason != "" {
			fmt.Fprintf(f, "    failureReason=%s\n", b.FailureReason)
		}
		if b.StartTimestamp != nil {
			fmt.Fprintf(f, "    start=%s", b.StartTimestamp.Format(time.RFC3339))
		}
		if b.CompletionTime != nil {
			fmt.Fprintf(f, " completion=%s", b.CompletionTime.Format(time.RFC3339))
		}
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f)

	fmt.Fprintf(f, "DATA UPLOADS (%d):\n", len(m.state.DataUploads))
	for _, du := range m.state.DataUploads {
		fmt.Fprintf(f, "  name=%-50s backup=%-30s phase=%-15s\n", du.Name, du.BackupName, du.Phase)
		if du.Message != "" {
			fmt.Fprintf(f, "    message=%s\n", du.Message)
		}
		fmt.Fprintf(f, "    vm=%s/%s node=%s datamover=%s\n", du.VMNamespace, du.VMName, du.Node, du.DataMover)
		fmt.Fprintf(f, "    bytes=%d/%d\n", du.BytesDone, du.TotalBytes)
	}
	fmt.Fprintln(f)

	fmt.Fprintf(f, "UPLOADER PODS (%d):\n", len(m.state.UploaderPods))
	for _, p := range m.state.UploaderPods {
		fmt.Fprintf(f, "  name=%-50s phase=%-10s node=%s type=%s\n", p.Name, p.Phase, p.Node, p.BackupType)
		fmt.Fprintf(f, "    logs (%d lines):\n", len(p.Logs))
		for _, l := range p.Logs {
			fmt.Fprintf(f, "      %s\n", l)
		}
	}
	fmt.Fprintln(f)

	if m.state.ControllerPod != nil {
		p := m.state.ControllerPod
		fmt.Fprintf(f, "CONTROLLER POD: name=%s phase=%s node=%s logs=%d\n",
			p.Name, p.Phase, p.Node, len(p.Logs))
	} else {
		fmt.Fprintf(f, "CONTROLLER POD: nil\n")
	}

	if m.state.VeleroPod != nil {
		p := m.state.VeleroPod
		fmt.Fprintf(f, "VELERO POD: name=%s phase=%s node=%s logs=%d\n",
			p.Name, p.Phase, p.Node, len(p.Logs))
	} else {
		fmt.Fprintf(f, "VELERO POD: nil\n")
	}
	fmt.Fprintln(f)

	fmt.Fprintf(f, "TERMINATED PODS CACHE (%d):\n", len(m.state.TerminatedPods))
	for name, p := range m.state.TerminatedPods {
		fmt.Fprintf(f, "  %s phase=%s type=%s logs=%d\n", name, p.Phase, p.BackupType, len(p.Logs))
	}
	fmt.Fprintln(f)

	fmt.Fprintf(f, "CHAINS (%d VMs):\n", len(m.state.Chains))
	for vm, idx := range m.state.Chains {
		fmt.Fprintf(f, "  VM %s/%s: %d checkpoints\n", vm.Namespace, vm.Name, len(idx.Checkpoints))
		for _, cp := range idx.Checkpoints {
			fmt.Fprintf(f, "    id=%s type=%s referencedBy=%v\n", cp.ID, cp.Type, cp.ReferencedBy)
		}
	}
	if m.state.S3Error != "" {
		fmt.Fprintf(f, "  S3 ERROR: %s\n", m.state.S3Error)
	}
	fmt.Fprintln(f)

	fmt.Fprintf(f, "THROUGHPUT SAMPLES (%d):\n", len(m.state.Throughput))
	for name, s := range m.state.Throughput {
		fmt.Fprintf(f, "  %s: prevBytes=%d prevTime=%s rate=%.1f B/s\n",
			name, s.PrevBytes, s.PrevTime.Format("15:04:05.000"), s.LastRate)
	}
}
