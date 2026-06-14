package status

import (
	"testing"
)

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no ansi",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "color codes",
			input:    "\x1b[32mgreen\x1b[0m text",
			expected: "green text",
		},
		{
			name:     "multiple codes",
			input:    "\x1b[1m\x1b[34mbold blue\x1b[0m",
			expected: "bold blue",
		},
		{
			name:     "cursor movement",
			input:    "\x1b[2Jclear screen",
			expected: "clear screen",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripANSI(tt.input)
			if result != tt.expected {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsPromptLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		agentType string
		expected  bool
	}{
		// Claude prompts
		{name: "claude prompt lowercase", line: "claude>", agentType: "cc", expected: true},
		{name: "claude long-form alias", line: "claude>", agentType: "claude", expected: true},
		{name: "claude prompt with space", line: "claude> ", agentType: "cc", expected: true},
		{name: "Claude prompt uppercase", line: "Claude>", agentType: "cc", expected: true},

		// Codex prompts
		{name: "codex prompt", line: "codex>", agentType: "cod", expected: true},
		{name: "codex long-form alias", line: "codex>", agentType: " CodEx ", expected: true},
		{name: "codex chevron prompt", line: "› Write tests for @filename", agentType: "cod", expected: true},
		// Shell prompts should NOT match for known agent types - a shell $ in cod/cc/gmi means agent exited
		{name: "shell prompt for codex means exited", line: "user@host:~$", agentType: "cod", expected: false},
		{name: "shell prompt for codex alias means exited", line: "user@host:~$", agentType: "codex", expected: false},

		// Gemini prompts
		{name: "gemini prompt", line: "gemini>", agentType: "gmi", expected: true},
		{name: "gemini long-form alias", line: "gemini>", agentType: "gemini", expected: true},
		{name: "Gemini prompt", line: "Gemini>", agentType: "gmi", expected: true},

		// User shell prompts
		{name: "dollar prompt", line: "user@host:~$ ", agentType: "user", expected: true},
		{name: "percent prompt", line: "user@host %", agentType: "user", expected: true},
		{name: "starship prompt", line: "~/project ❯", agentType: "user", expected: true},

		// Generic prompts
		{name: "generic > prompt", line: ">", agentType: "", expected: true},
		{name: "generic > prompt with space", line: "> ", agentType: "", expected: true},

		// Non-prompts
		{name: "regular text", line: "hello world", agentType: "cc", expected: false},
		{name: "empty string", line: "", agentType: "cc", expected: false},
		{name: "whitespace only", line: "   ", agentType: "cc", expected: false},

		// With ANSI codes
		{name: "prompt with ansi", line: "\x1b[32mclaude>\x1b[0m", agentType: "cc", expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPromptLine(tt.line, tt.agentType)
			if result != tt.expected {
				t.Errorf("IsPromptLine(%q, %q) = %v, want %v", tt.line, tt.agentType, result, tt.expected)
			}
		})
	}
}

func TestDetectIdleFromOutput(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		agentType string
		expected  bool
	}{
		{
			name:      "claude idle at prompt",
			output:    "Some previous output\nMore text\nclaude>",
			agentType: "cc",
			expected:  true,
		},
		{
			name:      "claude working",
			output:    "Processing request...\nGenerating code...\n",
			agentType: "cc",
			expected:  false,
		},
		{
			name:      "claude prompt with trailing newlines",
			output:    "Output\nclaude>\n\n",
			agentType: "cc",
			expected:  true,
		},
		{
			name:      "codex at shell prompt means agent exited not idle",
			output:    "Command completed\nuser@host:~$",
			agentType: "cod",
			expected:  false, // shell prompt in cod pane means agent exited, not idle at codex> prompt
		},
		{
			name:      "codex alias at shell prompt still means exited not idle",
			output:    "Command completed\nuser@host:~$",
			agentType: "codex",
			expected:  false,
		},
		{
			name:      "codex at codex prompt",
			output:    "Command completed\ncodex>",
			agentType: "cod",
			expected:  true, // actual codex prompt means idle
		},
		{
			name:      "codex alias at codex prompt",
			output:    "Command completed\ncodex>",
			agentType: " CodEx ",
			expected:  true,
		},
		{
			name:      "codex at chevron prompt",
			output:    "Command completed\n› Write tests for @filename",
			agentType: "cod",
			expected:  true, // codex chevron prompt means idle
		},
		{
			name:      "gemini idle",
			output:    "Response complete.\ngemini>",
			agentType: "gmi",
			expected:  true,
		},
		{
			name:      "empty output",
			output:    "",
			agentType: "cc",
			expected:  false,
		},
		{
			name:      "only whitespace",
			output:    "\n\n   \n",
			agentType: "cc",
			expected:  false,
		},
		{
			name:      "output with ansi codes",
			output:    "\x1b[32mSuccess!\x1b[0m\n\x1b[34mclaude>\x1b[0m",
			agentType: "cc",
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectIdleFromOutput(tt.output, tt.agentType)
			if result != tt.expected {
				t.Errorf("DetectIdleFromOutput(%q, %q) = %v, want %v",
					tt.output, tt.agentType, result, tt.expected)
			}
		})
	}
}

func TestGetLastNonEmptyLine(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{
			name:     "simple output",
			output:   "line1\nline2\nline3",
			expected: "line3",
		},
		{
			name:     "trailing newlines",
			output:   "line1\nline2\n\n\n",
			expected: "line2",
		},
		{
			name:     "with ansi",
			output:   "\x1b[32mcolored\x1b[0m\n",
			expected: "colored",
		},
		{
			name:     "empty",
			output:   "",
			expected: "",
		},
		{
			name:     "only whitespace",
			output:   "   \n\t\n  ",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetLastNonEmptyLine(tt.output)
			if result != tt.expected {
				t.Errorf("GetLastNonEmptyLine(%q) = %q, want %q",
					tt.output, result, tt.expected)
			}
		})
	}
}

func TestIsPromptLine_LiteralMatch(t *testing.T) {
	// Test that literal matching works (for patterns that use Literal instead of Regex)
	// First add a literal pattern for testing
	originalLen := len(promptPatterns)

	// Add a test pattern with Literal
	promptPatterns = append(promptPatterns, PromptPattern{
		AgentType:   "test",
		Literal:     "test_prompt$",
		Description: "test literal prompt",
	})

	defer func() {
		// Restore original patterns
		promptPatterns = promptPatterns[:originalLen]
	}()

	// Test literal matching
	if !IsPromptLine("command test_prompt$", "test") {
		t.Error("should match literal prompt suffix")
	}
}

func TestIsPromptLine_AgentTypeFiltering(t *testing.T) {
	// Test that patterns are filtered by agent type
	// Note: Generic patterns (empty AgentType) match ALL agent types as fallback
	tests := []struct {
		line      string
		agentType string
		expected  bool
	}{
		// Cursor patterns match cursor agent type
		{"cursor>", "cursor", true},
		// Generic pattern ">$" is a fallback that matches any agent type
		{"cursor>", "cc", true}, // Falls through to generic ">$" pattern

		// Windsurf patterns match windsurf
		{"windsurf>", "windsurf", true},
		// Generic fallback pattern matches
		{"windsurf>", "cod", true}, // Falls through to generic ">$" pattern

		// Aider patterns
		{"aider>", "aider", true},
		// Generic fallback pattern matches
		{"aider>", "gmi", true}, // Falls through to generic ">$" pattern

		// But non-prompt lines don't match
		{"just some text", "cursor", false},
		{"running command...", "windsurf", false},
	}

	for _, tt := range tests {
		t.Run(tt.line+"_"+tt.agentType, func(t *testing.T) {
			result := IsPromptLine(tt.line, tt.agentType)
			if result != tt.expected {
				t.Errorf("IsPromptLine(%q, %q) = %v, want %v",
					tt.line, tt.agentType, result, tt.expected)
			}
		})
	}
}

func TestDetectIdleFromOutput_MultipleLines(t *testing.T) {
	// DetectIdleFromOutput scans up to maxIdleScanLines (12) trailing
	// non-empty lines for a prompt, then rejects the verdict if an active
	// spinner sits below the matched prompt.
	tests := []struct {
		name      string
		output    string
		agentType string
		expected  bool
	}{
		{
			// Prompt in second-to-last non-empty line
			name:      "prompt in second line from end",
			output:    "output\nclaude>\n\n",
			agentType: "cc",
			expected:  true,
		},
		{
			// Prompt within the scan window is still detected
			name:      "prompt in third line from end",
			output:    "claude>\nfollowup\nmore",
			agentType: "cc",
			expected:  true,
		},
		{
			// Prompt a handful of lines back (the old 3-line window missed
			// this; the wider window catches it).
			name:      "prompt 5 lines from end within window",
			output:    "claude>\na\nb\nc\nd",
			agentType: "cc",
			expected:  true,
		},
		{
			// REAL Claude layout (from internal/cli/outputs/): the "❯ " input
			// box is pinned to the BOTTOM, with the status footer below it and
			// the (now finished) turn's content above. No active spinner is the
			// most-recent dynamic marker, so the pane is idle and dispatchable.
			name: "cc finished turn with bottom-pinned box is idle",
			output: "● All changes applied; tests pass.\n" +
				"\n" +
				"✻ Cooked for 2m 10s\n" +
				"───────────\n" +
				"❯ \n" +
				"───────────\n" +
				"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n",
			agentType: "cc",
			expected:  true,
		},
		{
			// CRITICAL false-idle guard, REAL layout: the active spinner renders
			// just ABOVE the bottom-pinned input box while a turn is in flight.
			// The box is always drawn, so its presence is NOT idleness — the
			// most-recent dynamic marker is the spinner. MUST NOT report idle.
			name: "cc working with spinner above bottom box",
			output: "● Running the suite.\n" +
				"✻ Scurrying… (ctrl+c to interrupt · 12s · thinking)\n" +
				"───────────\n" +
				"❯ \n" +
				"───────────\n" +
				"  ⏵⏵ bypass permissions on\n",
			agentType: "cc",
			expected:  false,
		},
		{
			// "new task?" footer parked below the box after a turn ends, with no
			// active spinner — idle and dispatchable.
			name: "cc new task footer is idle",
			output: "● Done.\n" +
				"✻ Baked for 5m 0s\n" +
				"───────────\n" +
				"❯ \n" +
				"───────────\n" +
				"new task? /clear to save 12.3k tokens\n",
			agentType: "cc",
			expected:  true,
		},
		{
			// Prompt beyond the scan window must NOT be detected — guard against
			// false positives from very-old prompt text deep in scrollback.
			name: "prompt beyond scan window",
			output: "claude>\n" +
				"l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n" +
				"l11\nl12\nl13",
			agentType: "cc",
			expected:  false,
		},
		{
			name:      "prompt as last line after work output",
			output:    "exec /bin/bash --norc --noprofile\necho BASH_READY\nPS1='$ '; echo IDLE_MARKER\nIDLE_MARKER\n$",
			agentType: "user",
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectIdleFromOutput(tt.output, tt.agentType)
			if result != tt.expected {
				t.Errorf("DetectIdleFromOutput = %v, want %v", result, tt.expected)
			}
		})
	}
}
