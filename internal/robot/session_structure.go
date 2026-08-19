// Package robot provides machine-readable output for AI agents.
// session_structure.go implements session structure detection (bd-1ws17).
package robot

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
