package tmux

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

func TestDetectAgentFromCommand_FalsePositives(t *testing.T) {
	tests := []struct {
		command  string
		expected agent.AgentType
	}{
		{"cursor", agent.AgentTypeCursor},
		{"/usr/bin/cursor", agent.AgentTypeCursor},
		{"cursor run", agent.AgentTypeCursor}, // If pane_current_command includes args (rare)

		// Potential false positives with simple Contains
		{"my-cursor-script.sh", agent.AgentTypeUser},
		{"vim cursor.c", agent.AgentTypeUser}, // If tmux reports "vim cursor.c"
		{"libncurses", agent.AgentTypeUser},   // "curses" contains "curs"? No, but "cursor" contains "curs"
		{"recursor", agent.AgentTypeUser},

		{"windsurf", agent.AgentTypeWindsurf},
		{"/opt/windsurf/bin/windsurf", agent.AgentTypeWindsurf},
		{"tailwindsurfing", agent.AgentTypeUser}, // False positive candidate
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := detectAgentFromCommand(tt.command)
			if got != tt.expected {
				t.Errorf("detectAgentFromCommand(%q) = %v, want %v", tt.command, got, tt.expected)
			}
		})
	}
}

// TestDetectAgentFromCommand_Antigravity is the bd-ws7-docs-ux-truth-tqh3l.5
// agy detection proof: the real launch shapes (agy, agy-locked with the
// pinned display model, path-qualified binaries) classify as Antigravity,
// while coincidental "agy"/"gemini" tokens do not.
func TestDetectAgentFromCommand_Antigravity(t *testing.T) {
	tests := []struct {
		command  string
		expected agent.AgentType
	}{
		// Real launch shapes.
		{"agy", agent.AgentTypeAntigravity},
		{"agy-locked", agent.AgentTypeAntigravity},
		{"/Users/dev/.local/bin/agy-locked", agent.AgentTypeAntigravity},
		{"agy-locked --model 'Gemini 3.8 Flash (High)' --dangerously-skip-permissions", agent.AgentTypeAntigravity},
		{"agy --model 'Gemini 3.8 Flash (High)' --dangerously-skip-permissions", agent.AgentTypeAntigravity},
		{"antigravity", agent.AgentTypeAntigravity},

		// The pinned model name contains "Gemini"; it must never classify the
		// pane as a gemini agent, and "agy" as an argument is not an agent.
		{"rg agy", agent.AgentTypeUser},
		{"vim agy_notes.txt", agent.AgentTypeUser},
		{"gemini --yolo", agent.AgentTypeGemini},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := detectAgentFromCommand(tt.command)
			if got != tt.expected {
				t.Errorf("detectAgentFromCommand(%q) = %v, want %v", tt.command, got, tt.expected)
			}
		})
	}
}

// TestDetectAgentFromArgv_Antigravity feeds the process-tree detector a
// fixture agy process (real binary name / cmdline shape) and a non-agy
// fixture, per the bead's named proof: the detection branch itself is under
// test, not just the launcher passthrough.
func TestDetectAgentFromArgv_Antigravity(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		expected agent.AgentType
	}{
		{
			name:     "agy-locked with pinned model",
			argv:     []string{"/Users/dev/.local/bin/agy-locked", "--model", "Gemini 3.8 Flash (High)", "--dangerously-skip-permissions"},
			expected: agent.AgentTypeAntigravity,
		},
		{
			name:     "bare agy binary",
			argv:     []string{"agy"},
			expected: agent.AgentTypeAntigravity,
		},
		{
			name:     "agy as an argument is not agy",
			argv:     []string{"rg", "agy"},
			expected: agent.AgentTypeUser,
		},
		{
			name:     "unrelated process is not agy",
			argv:     []string{"python3", "manage_agy_configs.py"},
			expected: agent.AgentTypeUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectAgentFromArgv(tt.argv)
			if got != tt.expected {
				t.Errorf("detectAgentFromArgv(%v) = %v, want %v", tt.argv, got, tt.expected)
			}
		})
	}
}
