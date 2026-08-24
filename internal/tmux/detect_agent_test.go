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
		{"agy-locked --model 'Gemini 3.1 Pro (High)' --dangerously-skip-permissions", agent.AgentTypeAntigravity},
		{"agy --model 'Gemini 3.1 Pro (High)' --dangerously-skip-permissions", agent.AgentTypeAntigravity},
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
			argv:     []string{"/Users/dev/.local/bin/agy-locked", "--model", "Gemini 3.1 Pro (High)", "--dangerously-skip-permissions"},
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

// TestDetectAgentFromCommand_Pi is the bd-ffv3d pi detection proof. pi is a
// plugin agent whose pane title is rewritten by the pi process itself via OSC 0
// ("π - <cwd>"), so the ntm-formatted title is gone by the time the readiness
// poll lists the pane. The command name is the only signal left, and it must
// classify as pi — while coincidental "pi" tokens (ping, pip, `rg pi`) do not.
func TestDetectAgentFromCommand_Pi(t *testing.T) {
	tests := []struct {
		command  string
		expected agent.AgentType
	}{
		// Real launch shapes.
		{"pi", agent.AgentTypePi},
		{"pi --approve --model kimi-k2.7", agent.AgentTypePi},
		{"/home/user/.local/bin/pi --approve", agent.AgentTypePi},

		// "pi" is a short generic token: only the executable basename counts.
		{"ping", agent.AgentTypeUser},
		{"pip install requests", agent.AgentTypeUser},
		{"vim pi.py", agent.AgentTypeUser},
		{"rg pi", agent.AgentTypeUser},
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

// TestDetectAgentFromArgv_Pi feeds the process-tree detector a fixture pi
// process and non-pi fixtures, mirroring the agy proof: the executable-only
// rule must hold for argv[0] and never leak through a coincidental argument.
func TestDetectAgentFromArgv_Pi(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		expected agent.AgentType
	}{
		{
			name:     "bare pi binary",
			argv:     []string{"pi"},
			expected: agent.AgentTypePi,
		},
		{
			name:     "pi with launch args",
			argv:     []string{"/home/user/.local/bin/pi", "--approve", "--model", "kimi-k2.7"},
			expected: agent.AgentTypePi,
		},
		{
			name:     "pi as an argument is not pi",
			argv:     []string{"rg", "pi"},
			expected: agent.AgentTypeUser,
		},
		{
			name:     "unrelated process is not pi",
			argv:     []string{"python3", "pi.py"},
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

// TestParsePaneFromPartsPiTitleOverwritten is the end-to-end type-derivation
// proof for bd-ffv3d: a pi pane whose title has been rewritten by pi's OSC 0
// sequence ("π - <cwd>") must still resolve to AgentPi through the command
// fallback, or the readiness poll classifies it as a user pane and every pi
// spawn times out at state=unknown.
func TestParsePaneFromPartsPiTitleOverwritten(t *testing.T) {
	pane, err := parsePaneFromParts(
		[]string{"%1", "1", "π - tmp", "pi", "80", "24", "1"},
		[]string{"123", "0"},
	)
	if err != nil {
		t.Fatalf("parsePaneFromParts: %v", err)
	}
	if pane.Type != AgentPi {
		t.Fatalf("pane.Type = %q, want %q (title %q, command %q)", pane.Type, AgentPi, pane.Title, pane.Command)
	}
}
