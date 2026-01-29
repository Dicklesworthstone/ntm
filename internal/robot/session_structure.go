// Package robot provides machine-readable output for AI agents.
// session_structure.go implements session structure detection (bd-1ws17).
package robot

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SessionStructure contains comprehensive session layout information.
// This is used to understand NTM session conventions for agent automation.
type SessionStructure struct {
	// Window information
	WindowIndex int      `json:"window_index"` // Primary window where agents live
	WindowCount int      `json:"window_count"` // Total windows in session
	WindowIDs   []int    `json:"window_ids"`   // All window indices
	WindowNames []string `json:"window_names"` // Window names if set

	// Pane layout
	ControlPane     int   `json:"control_pane"`      // Control shell pane (typically 1)
	AgentPaneStart  int   `json:"agent_pane_start"`  // First agent pane index
	AgentPaneEnd    int   `json:"agent_pane_end"`    // Last agent pane index
	TotalAgentPanes int   `json:"total_agent_panes"` // Count of agent panes
	PaneIndices     []int `json:"pane_indices"`      // All pane indices in primary window
	TotalPanes      int   `json:"total_panes"`       // Total panes across all windows

	// Session metadata
	SessionName string `json:"session_name"`
	IsNTMLayout bool   `json:"is_ntm_layout"` // Matches NTM convention
	Layout      string `json:"layout"`        // tmux layout string

	// Detection notes
	DetectionMethod string   `json:"detection_method"` // How structure was determined
	Warnings        []string `json:"warnings,omitempty"`
}

// DetectSessionStructure performs comprehensive session structure detection.
// It identifies window/pane layout, control pane, and agent panes.
func DetectSessionStructure(session string) (*SessionStructure, error) {
	if session == "" {
		return nil, fmt.Errorf("session name required")
	}

	structure := &SessionStructure{
		SessionName:     session,
		DetectionMethod: "tmux_query",
	}

	// Step 1: Detect windows
	if err := structure.detectWindows(session); err != nil {
		return nil, fmt.Errorf("detecting windows: %w", err)
	}

	// Step 2: Detect panes in primary window
	if err := structure.detectPanes(session); err != nil {
		return nil, fmt.Errorf("detecting panes: %w", err)
	}

	// Step 3: Determine NTM layout
	structure.classifyLayout()

	return structure, nil
}

// detectWindows queries tmux for window information.
func (s *SessionStructure) detectWindows(session string) error {
	tmuxPath := tmux.BinaryPath()
	// Get window list: index and name
	out, err := exec.Command(tmuxPath, "list-windows", "-t", session,
		"-F", "#{window_index}|#{window_name}").Output()
	if err != nil {
		return fmt.Errorf("list-windows failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return fmt.Errorf("no windows found")
	}

	s.WindowCount = len(lines)
	s.WindowIDs = make([]int, 0, len(lines))
	s.WindowNames = make([]string, 0, len(lines))

	for _, line := range lines {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) < 1 {
			continue
		}

		idx, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}

		s.WindowIDs = append(s.WindowIDs, idx)
		if len(parts) > 1 {
			s.WindowNames = append(s.WindowNames, parts[1])
		}
	}

	// NTM convention: window index 1 is primary
	// If window 1 exists, use it; otherwise use first available
	s.WindowIndex = s.findPrimaryWindow()

	if s.WindowCount > 1 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("multiple windows detected (%d); using window %d as primary", s.WindowCount, s.WindowIndex))
	}

	return nil
}

// findPrimaryWindow determines which window contains agents.
// NTM convention is window index 1.
func (s *SessionStructure) findPrimaryWindow() int {
	// Prefer window 1 (NTM convention)
	for _, idx := range s.WindowIDs {
		if idx == 1 {
			return 1
		}
	}
	// Fall back to first window if 1 doesn't exist
	if len(s.WindowIDs) > 0 {
		return s.WindowIDs[0]
	}
	return 0
}

// detectPanes queries tmux for pane information in the primary window.
func (s *SessionStructure) detectPanes(session string) error {
	target := fmt.Sprintf("%s:%d", session, s.WindowIndex)
	tmuxPath := tmux.BinaryPath()

	// Get pane list: index and pid
	out, err := exec.Command(tmuxPath, "list-panes", "-t", target,
		"-F", "#{pane_index}|#{pane_pid}|#{pane_current_command}").Output()
	if err != nil {
		return fmt.Errorf("list-panes failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return fmt.Errorf("no panes found in window %d", s.WindowIndex)
	}

	s.TotalPanes = len(lines)
	s.PaneIndices = make([]int, 0, len(lines))

	minIdx := -1
	maxIdx := -1

	for _, line := range lines {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 1 {
			continue
		}

		idx, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}

		s.PaneIndices = append(s.PaneIndices, idx)

		if minIdx == -1 || idx < minIdx {
			minIdx = idx
		}
		if idx > maxIdx {
			maxIdx = idx
		}
	}

	// NTM convention:
	// - Control pane is the first pane (respects pane-base-index)
	// - Agent panes are subsequent panes
	if minIdx >= 0 {
		s.ControlPane = minIdx
	}
	if s.ControlPane != 1 && len(s.PaneIndices) > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("pane 1 not found; using pane %d as control", s.ControlPane))
	}

	// Agent panes are all panes except control
	if len(s.PaneIndices) > 1 {
		s.AgentPaneStart = s.ControlPane + 1
		s.AgentPaneEnd = maxIdx
		s.TotalAgentPanes = s.countAgentPanes()
	} else {
		// Only control pane exists
		s.AgentPaneStart = 0
		s.AgentPaneEnd = 0
		s.TotalAgentPanes = 0
	}

	// Get layout string
	s.detectLayout(target)

	return nil
}

// countAgentPanes counts panes that are agents (not control).
func (s *SessionStructure) countAgentPanes() int {
	count := 0
	for _, idx := range s.PaneIndices {
		if idx != s.ControlPane && idx >= s.AgentPaneStart {
			count++
		}
	}
	return count
}

// detectLayout gets the tmux layout string.
func (s *SessionStructure) detectLayout(target string) {
	tmuxPath := tmux.BinaryPath()
	out, err := exec.Command(tmuxPath, "display-message", "-t", target,
		"-p", "#{window_layout}").Output()
	if err == nil {
		s.Layout = strings.TrimSpace(string(out))
	}
}

// classifyLayout determines if session matches NTM conventions.
func (s *SessionStructure) classifyLayout() {
	// NTM standard layout:
	// - Window index 1
	// - Pane 1 = control shell
	// - Panes 2+ = agents
	// - Total panes >= 2

	isNTM := s.WindowIndex == 1 &&
		s.ControlPane == 1 &&
		s.TotalPanes >= 2 &&
		s.AgentPaneStart == 2

	s.IsNTMLayout = isNTM

	if !isNTM && len(s.Warnings) == 0 {
		s.Warnings = append(s.Warnings, "session does not match standard NTM layout")
		s.DetectionMethod = "best_effort"
	}
}

// PaneTarget returns a tmux target string for a specific pane.
func (s *SessionStructure) PaneTarget(paneIndex int) string {
	return fmt.Sprintf("%s:%d.%d", s.SessionName, s.WindowIndex, paneIndex)
}

// AgentPaneTargets returns target strings for all agent panes.
func (s *SessionStructure) AgentPaneTargets() []string {
	targets := make([]string, 0, s.TotalAgentPanes)
	for _, idx := range s.PaneIndices {
		if idx != s.ControlPane && idx >= s.AgentPaneStart {
			targets = append(targets, s.PaneTarget(idx))
		}
	}
	return targets
}

// ControlPaneTarget returns the target string for the control pane.
func (s *SessionStructure) ControlPaneTarget() string {
	return s.PaneTarget(s.ControlPane)
}

// HasAgents returns true if the session has agent panes.
func (s *SessionStructure) HasAgents() bool {
	return s.TotalAgentPanes > 0
}

// IsValidAgentPane returns true if the pane index is a valid agent pane.
func (s *SessionStructure) IsValidAgentPane(paneIndex int) bool {
	for _, idx := range s.PaneIndices {
		if idx == paneIndex && idx != s.ControlPane {
			return true
		}
	}
	return false
}
