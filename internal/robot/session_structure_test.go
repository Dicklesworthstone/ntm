package robot

import (
	"testing"
)

// TestSessionStructure_PaneTarget tests target string generation.
func TestSessionStructure_PaneTarget(t *testing.T) {
	tests := []struct {
		name      string
		structure SessionStructure
		paneIndex int
		expected  string
	}{
		{
			name: "standard NTM pane target",
			structure: SessionStructure{
				SessionName: "myproject",
				WindowIndex: 1,
			},
			paneIndex: 2,
			expected:  "myproject:1.2",
		},
		{
			name: "control pane target",
			structure: SessionStructure{
				SessionName: "test",
				WindowIndex: 1,
			},
			paneIndex: 1,
			expected:  "test:1.1",
		},
		{
			name: "non-standard window",
			structure: SessionStructure{
				SessionName: "legacy",
				WindowIndex: 0,
			},
			paneIndex: 3,
			expected:  "legacy:0.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.structure.PaneTarget(tt.paneIndex)
			t.Logf("TEST: %s | Input: pane=%d | Expected: %q | Got: %q", tt.name, tt.paneIndex, tt.expected, got)
			if got != tt.expected {
				t.Errorf("PaneTarget(%d) = %q, want %q", tt.paneIndex, got, tt.expected)
			}
		})
	}
}

// TestSessionStructure_AgentPaneTargets tests agent target generation.
func TestSessionStructure_AgentPaneTargets(t *testing.T) {
	tests := []struct {
		name      string
		structure SessionStructure
		expected  []string
	}{
		{
			name: "standard 3-agent layout",
			structure: SessionStructure{
				SessionName:     "myproject",
				WindowIndex:     1,
				ControlPane:     1,
				AgentPaneStart:  2,
				AgentPaneEnd:    4,
				TotalAgentPanes: 3,
				PaneIndices:     []int{1, 2, 3, 4},
			},
			expected: []string{"myproject:1.2", "myproject:1.3", "myproject:1.4"},
		},
		{
			name: "single agent layout",
			structure: SessionStructure{
				SessionName:     "test",
				WindowIndex:     1,
				ControlPane:     1,
				AgentPaneStart:  2,
				AgentPaneEnd:    2,
				TotalAgentPanes: 1,
				PaneIndices:     []int{1, 2},
			},
			expected: []string{"test:1.2"},
		},
		{
			name: "no agents",
			structure: SessionStructure{
				SessionName:     "empty",
				WindowIndex:     1,
				ControlPane:     1,
				AgentPaneStart:  0,
				AgentPaneEnd:    0,
				TotalAgentPanes: 0,
				PaneIndices:     []int{1},
			},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.structure.AgentPaneTargets()
			t.Logf("TEST: %s | PaneIndices: %v | Expected: %v | Got: %v", tt.name, tt.structure.PaneIndices, tt.expected, got)
			if len(got) != len(tt.expected) {
				t.Errorf("AgentPaneTargets() returned %d targets, want %d", len(got), len(tt.expected))
				return
			}
			for i := range tt.expected {
				if got[i] != tt.expected[i] {
					t.Errorf("AgentPaneTargets()[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

// TestSessionStructure_ControlPaneTarget tests control pane target.
func TestSessionStructure_ControlPaneTarget(t *testing.T) {
	s := SessionStructure{
		SessionName: "myproject",
		WindowIndex: 1,
		ControlPane: 1,
	}

	expected := "myproject:1.1"
	got := s.ControlPaneTarget()
	t.Logf("TEST: ControlPaneTarget | Session: %s | Expected: %q | Got: %q", s.SessionName, expected, got)

	if got != expected {
		t.Errorf("ControlPaneTarget() = %q, want %q", got, expected)
	}
}

// TestSessionStructure_HasAgents tests agent detection.
func TestSessionStructure_HasAgents(t *testing.T) {
	tests := []struct {
		name            string
		totalAgentPanes int
		expected        bool
	}{
		{"no agents", 0, false},
		{"one agent", 1, true},
		{"multiple agents", 3, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SessionStructure{TotalAgentPanes: tt.totalAgentPanes}
			got := s.HasAgents()
			t.Logf("TEST: %s | TotalAgentPanes: %d | Expected: %v | Got: %v", tt.name, tt.totalAgentPanes, tt.expected, got)
			if got != tt.expected {
				t.Errorf("HasAgents() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestSessionStructure_IsValidAgentPane tests agent pane validation.
func TestSessionStructure_IsValidAgentPane(t *testing.T) {
	s := SessionStructure{
		ControlPane: 1,
		PaneIndices: []int{1, 2, 3, 4},
	}

	tests := []struct {
		paneIndex int
		expected  bool
	}{
		{1, false}, // control pane, not agent
		{2, true},  // valid agent pane
		{3, true},  // valid agent pane
		{4, true},  // valid agent pane
		{5, false}, // doesn't exist
		{0, false}, // doesn't exist
	}

	for _, tt := range tests {
		got := s.IsValidAgentPane(tt.paneIndex)
		t.Logf("TEST: IsValidAgentPane | Pane: %d | Expected: %v | Got: %v", tt.paneIndex, tt.expected, got)
		if got != tt.expected {
			t.Errorf("IsValidAgentPane(%d) = %v, want %v", tt.paneIndex, got, tt.expected)
		}
	}
}

// TestSessionStructure_classifyLayout tests NTM layout classification.
func TestSessionStructure_classifyLayout(t *testing.T) {
	tests := []struct {
		name          string
		structure     SessionStructure
		expectNTM     bool
		expectWarning bool
	}{
		{
			name: "standard NTM layout",
			structure: SessionStructure{
				WindowIndex:    1,
				ControlPane:    1,
				TotalPanes:     4,
				AgentPaneStart: 2,
			},
			expectNTM:     true,
			expectWarning: false,
		},
		{
			name: "base-index-0 NTM layout",
			structure: SessionStructure{
				WindowIndex:    1,
				ControlPane:    0,
				TotalPanes:     3,
				AgentPaneStart: 1,
			},
			expectNTM:     true,
			expectWarning: false,
		},
		{
			name: "non-standard window index",
			structure: SessionStructure{
				WindowIndex:    0,
				ControlPane:    1,
				TotalPanes:     4,
				AgentPaneStart: 2,
			},
			expectNTM:     false,
			expectWarning: true,
		},
		{
			name: "control-only session",
			structure: SessionStructure{
				WindowIndex:    1,
				ControlPane:    1,
				TotalPanes:     1,
				AgentPaneStart: 2,
			},
			expectNTM:     false,
			expectWarning: true,
		},
		{
			name: "non-standard agent start",
			structure: SessionStructure{
				WindowIndex:    1,
				ControlPane:    1,
				TotalPanes:     4,
				AgentPaneStart: 3,
			},
			expectNTM:     false,
			expectWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.structure
			s.classifyLayout()

			t.Logf("TEST: %s | IsNTMLayout: %v (expect %v) | Warnings: %v (expect warning: %v)",
				tt.name, s.IsNTMLayout, tt.expectNTM, s.Warnings, tt.expectWarning)

			if s.IsNTMLayout != tt.expectNTM {
				t.Errorf("IsNTMLayout = %v, want %v", s.IsNTMLayout, tt.expectNTM)
			}
			if tt.expectWarning && len(s.Warnings) == 0 {
				t.Errorf("expected warning but got none")
			}
			if !tt.expectWarning && len(s.Warnings) > 0 {
				t.Errorf("unexpected warnings: %v", s.Warnings)
			}
		})
	}
}

// TestSessionStructure_findPrimaryWindow tests window selection.
func TestSessionStructure_findPrimaryWindow(t *testing.T) {
	tests := []struct {
		name      string
		windowIDs []int
		expected  int
	}{
		{"standard NTM", []int{1}, 1},
		{"window 1 preferred", []int{0, 1, 2}, 1},
		{"no window 1", []int{0, 2}, 0},
		{"empty", []int{}, 0},
		{"non-standard", []int{3, 4, 5}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SessionStructure{WindowIDs: tt.windowIDs}
			got := s.findPrimaryWindow()
			t.Logf("TEST: %s | WindowIDs: %v | Expected: %d | Got: %d", tt.name, tt.windowIDs, tt.expected, got)
			if got != tt.expected {
				t.Errorf("findPrimaryWindow() = %d, want %d", got, tt.expected)
			}
		})
	}
}

// TestSessionStructure_countAgentPanes tests agent pane counting.
func TestSessionStructure_countAgentPanes(t *testing.T) {
	tests := []struct {
		name           string
		controlPane    int
		agentPaneStart int
		paneIndices    []int
		expected       int
	}{
		{
			name:           "standard layout",
			controlPane:    1,
			agentPaneStart: 2,
			paneIndices:    []int{1, 2, 3, 4},
			expected:       3,
		},
		{
			name:           "control only",
			controlPane:    1,
			agentPaneStart: 2,
			paneIndices:    []int{1},
			expected:       0,
		},
		{
			name:           "gaps in panes",
			controlPane:    1,
			agentPaneStart: 2,
			paneIndices:    []int{1, 3, 5},
			expected:       2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SessionStructure{
				ControlPane:    tt.controlPane,
				AgentPaneStart: tt.agentPaneStart,
				PaneIndices:    tt.paneIndices,
			}
			got := s.countAgentPanes()
			t.Logf("TEST: %s | Panes: %v | Control: %d | Start: %d | Expected: %d | Got: %d",
				tt.name, tt.paneIndices, tt.controlPane, tt.agentPaneStart, tt.expected, got)
			if got != tt.expected {
				t.Errorf("countAgentPanes() = %d, want %d", got, tt.expected)
			}
		})
	}
}
