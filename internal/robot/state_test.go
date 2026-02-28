package robot

import (
	"strings"
	"testing"
)

// TestDetermineState tests the determineState function for all possible agent states
// Covers: ntm-k5pv - Test robot-status JSON output for all agent states
func TestDetermineState(t *testing.T) {
	tests := []struct {
		name          string
		output        string
		agentType     string
		expectedState string
		description   string
	}{
		// Error state tests - using actual patterns from internal/status/errors.go
		{
			name:          "error_panic",
			output:        "panic: runtime error: index out of range\n",
			agentType:     "claude",
			expectedState: "error",
			description:   "Go panic should be detected as error state",
		},
		{
			name:          "error_python_traceback",
			output:        "Traceback (most recent call last):\n  File \"test.py\", line 1, in <module>\n    import nonexistent\nModuleNotFoundError: No module named 'nonexistent'\n",
			agentType:     "codex",
			expectedState: "error",
			description:   "Python traceback should be detected as error state",
		},
		{
			name:          "error_segmentation_fault",
			output:        "Segmentation fault (core dumped)\n",
			agentType:     "gemini",
			expectedState: "error",
			description:   "Segmentation fault should be detected as error state",
		},
		{
			name:          "error_fatal",
			output:        "FATAL: database connection failed\n",
			agentType:     "claude",
			expectedState: "error",
			description:   "FATAL error should be detected as error state",
		},
		{
			name:          "error_generic_prefix",
			output:        "Error: file not found\n",
			agentType:     "codex",
			expectedState: "error",
			description:   "Error prefix should be detected as error state",
		},

		// Idle state tests - claude agent (using translated type "cc")
		{
			name:          "idle_claude_simple_prompt",
			output:        "> ",
			agentType:     "claude",
			expectedState: "idle",
			description:   "Claude simple prompt should be detected as idle state",
		},
		{
			name:          "idle_claude_named_prompt",
			output:        "claude> ",
			agentType:     "claude",
			expectedState: "idle",
			description:   "Claude named prompt should be detected as idle state",
		},
		{
			name:          "idle_claude_numbered_prompt",
			output:        "  5  > ",
			agentType:     "claude",
			expectedState: "idle",
			description:   "Claude numbered prompt should be detected as idle state",
		},

		// Idle state tests - codex agent (using translated type "cod")
		{
			name:          "idle_codex_prompt",
			output:        "codex> ",
			agentType:     "codex",
			expectedState: "idle",
			description:   "Codex prompt should be detected as idle state",
		},

		// Idle state tests - gemini agent (using translated type "gmi")
		{
			name:          "idle_gemini_prompt",
			output:        "gemini> ",
			agentType:     "gemini",
			expectedState: "idle",
			description:   "Gemini prompt should be detected as idle state",
		},

		// Idle state tests - user panes
		{
			name:          "idle_user_empty",
			output:        "",
			agentType:     "",
			expectedState: "idle",
			description:   "Empty output for user pane should be detected as idle state",
		},
		{
			name:          "idle_user_whitespace",
			output:        "   \n  \t  ",
			agentType:     "user",
			expectedState: "idle",
			description:   "Whitespace-only output for user pane should be detected as idle state",
		},
		{
			name:          "idle_bash_prompt",
			output:        "$ ",
			agentType:     "",
			expectedState: "idle",
			description:   "Bash prompt should be detected as idle state",
		},

		// Active state tests
		{
			name:          "active_claude_thinking",
			output:        "I'm analyzing the code structure to understand the dependencies...",
			agentType:     "claude",
			expectedState: "active",
			description:   "Claude actively working should be detected as active state",
		},
		{
			name:          "active_codex_generating",
			output:        "// Generating function implementation\nfunction processData(input) {\n  // Processing...",
			agentType:     "codex",
			expectedState: "active",
			description:   "Codex generating code should be detected as active state",
		},
		{
			name:          "active_gemini_processing",
			output:        "Processing your request... analyzing data patterns...",
			agentType:     "gemini",
			expectedState: "active",
			description:   "Gemini processing should be detected as active state",
		},
		{
			name:          "active_command_running",
			output:        "installing dependencies...\nfetching packages...",
			agentType:     "claude",
			expectedState: "active",
			description:   "Command execution output should be detected as active state",
		},
		{
			name:          "active_multiline_output",
			output:        "Line 1 of processing\nLine 2 of processing\nLine 3 of processing\n",
			agentType:     "codex",
			expectedState: "active",
			description:   "Multiline output should be detected as active state",
		},

		// Edge cases
		{
			name:          "edge_unknown_agent_type",
			output:        "Some output text",
			agentType:     "unknown",
			expectedState: "active",
			description:   "Unknown agent type with output should default to active state",
		},
		{
			name:          "edge_empty_output_non_user",
			output:        "",
			agentType:     "claude",
			expectedState: "active",
			description:   "Empty output for non-user agent should default to active state",
		},
		{
			name:          "edge_ansi_codes",
			output:        "\033[32mGreen text\033[0m\nSome output",
			agentType:     "claude",
			expectedState: "active",
			description:   "Output with ANSI codes should be detected as active state",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := determineState(tt.output, tt.agentType)
			if result != tt.expectedState {
				t.Errorf("determineState(%q, %q) = %q, want %q\nDescription: %s",
					tt.output, tt.agentType, result, tt.expectedState, tt.description)
			}
		})
	}
}

// TestTranslateAgentTypeForStatus tests the agent type translation
func TestTranslateAgentTypeForStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"claude", "cc"},
		{"codex", "cod"},
		{"gemini", "gmi"},
		{"unknown", ""},
		{"", ""},
		{"user", "user"},
		{"cursor", "cursor"},
		{"windsurf", "windsurf"},
		{"aider", "aider"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := translateAgentTypeForStatus(tt.input)
			if result != tt.expected {
				t.Errorf("translateAgentTypeForStatus(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestDetermineStateErrorConditions tests error detection logic specifically
func TestDetermineStateErrorConditions(t *testing.T) {
	// Only include error patterns that are actually detected by the status package
	errorOutputs := []string{
		"Error: file not found",             // ErrorGeneric - "Error:" prefix
		"FATAL: database connection failed", // ErrorCrash - "FATAL:" literal
		"Segmentation fault",                // ErrorCrash - literal match
		"panic: runtime error",              // ErrorCrash - "panic:" literal
	}

	agentTypes := []string{"claude", "codex", "gemini", "user", ""}

	for _, output := range errorOutputs {
		for _, agentType := range agentTypes {
			prefix := output
			if len(prefix) > 10 {
				prefix = prefix[:10]
			}
			t.Run("error_"+strings.ReplaceAll(prefix, " ", "_")+"_"+agentType, func(t *testing.T) {
				result := determineState(output, agentType)
				if result != "error" {
					t.Errorf("determineState(%q, %q) = %q, want %q", output, agentType, result, "error")
				}
			})
		}
	}
}

// TestDetermineStateIdleConditions tests idle detection logic specifically
func TestDetermineStateIdleConditions(t *testing.T) {
	idleTests := []struct {
		output     string
		agentType  string
		shouldIdle bool
	}{
		// Should be idle - using actual prompt patterns
		{"$ ", "", true},             // Generic shell prompt
		{"> ", "claude", true},       // Claude simple prompt
		{"codex> ", "codex", true},   // Codex named prompt
		{"gemini> ", "gemini", true}, // Gemini named prompt
		{"", "user", true},           // Empty user pane
		{"   ", "", true},            // Whitespace only in user/generic pane
		{"\n\n", "user", true},       // Newlines only in user pane

		// Claude Code TUI patterns (version banner, status bar, welcome)
		{"Claude Code v1.0.31\nbypass permissions on\n", "claude", true},          // Welcome screen
		{"Claude Code v2.0.0\n", "claude", true},                                 // Version banner alone
		{"bypass permissions on\n", "claude", true},                               // Status bar alone
		{"welcome back\n", "claude", true},                                        // Welcome message
		{"some output\nClaude Code v1.0.31\nbypass permissions on\n", "claude", true}, // Mixed with scrollback

		// Should NOT be idle (active instead)
		{"$ ls\nfile1.txt\nfile2.txt", "", false}, // Command with output
		{"Processing...", "claude", false},        // Claude working
		{"function test() {", "codex", false},     // Codex generating code
		{"Hello world", "gemini", false},          // Gemini output
	}

	for _, tt := range idleTests {
		t.Run("idle_"+strings.ReplaceAll(tt.output, " ", "_")+"_"+tt.agentType, func(t *testing.T) {
			result := determineState(tt.output, tt.agentType)
			expectedState := "active"
			if tt.shouldIdle {
				expectedState = "idle"
			}
			if result != expectedState {
				t.Errorf("determineState(%q, %q) = %q, want %q", tt.output, tt.agentType, result, expectedState)
			}
		})
	}
}

// TestDetermineStateDeterminism ensures consistent results for the same input
func TestDetermineStateDeterminism(t *testing.T) {
	testCases := []struct {
		output    string
		agentType string
	}{
		{"Error: something went wrong", "claude"},
		{"Human: hello", "claude"},
		{"Processing data...", "codex"},
		{"", "user"},
		{"$ ", ""},
	}

	for _, tc := range testCases {
		// Run the same test multiple times
		var results []string
		for i := 0; i < 10; i++ {
			results = append(results, determineState(tc.output, tc.agentType))
		}

		// Ensure all results are the same
		firstResult := results[0]
		for i, result := range results {
			if result != firstResult {
				t.Errorf("determineState is not deterministic: run %d returned %q, expected %q", i, result, firstResult)
			}
		}
	}
}
