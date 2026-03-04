package agent

import (
	"regexp"
	"testing"
)

func TestMatchAny(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		patterns []string
		want     bool
	}{
		{
			name:     "exact match",
			text:     "You've hit your limit",
			patterns: ccRateLimitPatterns,
			want:     true,
		},
		{
			name:     "case insensitive match",
			text:     "RATE LIMIT EXCEEDED",
			patterns: ccRateLimitPatterns,
			want:     true,
		},
		{
			name:     "no match",
			text:     "Everything is working fine",
			patterns: ccRateLimitPatterns,
			want:     false,
		},
		{
			name:     "partial match in longer text",
			text:     "Error: You've hit your limit. Please wait and try again.",
			patterns: ccRateLimitPatterns,
			want:     true,
		},
		{
			name:     "working pattern - code block",
			text:     "Here's the code:\n```go\nfunc main() {}\n```",
			patterns: ccWorkingPatterns,
			want:     true,
		},
		{
			name:     "working pattern - file operation",
			text:     "Writing to internal/agent/types.go",
			patterns: ccWorkingPatterns,
			want:     true,
		},
		{
			name:     "cursor rate limit",
			text:     "rate limit hit, please wait",
			patterns: cursorRateLimitPatterns,
			want:     true,
		},
		{
			name:     "cursor working",
			text:     "analyzing your code for issues",
			patterns: cursorWorkingPatterns,
			want:     true,
		},
		{
			name:     "windsurf rate limit",
			text:     "too many requests, slow down",
			patterns: windsurfRateLimitPatterns,
			want:     true,
		},
		{
			name:     "windsurf working",
			text:     "searching for references in project",
			patterns: windsurfWorkingPatterns,
			want:     true,
		},
		{
			name:     "aider rate limit",
			text:     "quota exceeded for today",
			patterns: aiderRateLimitPatterns,
			want:     true,
		},
		{
			name:     "aider working - applied edit",
			text:     "applied edit to main.py",
			patterns: aiderWorkingPatterns,
			want:     true,
		},
		{
			name:     "aider working - repo-map",
			text:     "updating repo-map for context",
			patterns: aiderWorkingPatterns,
			want:     true,
		},
		{
			name:     "cursor error",
			text:     "error: connection lost",
			patterns: cursorErrorPatterns,
			want:     true,
		},
		{
			name:     "windsurf error",
			text:     "failed: invalid configuration",
			patterns: windsurfErrorPatterns,
			want:     true,
		},
		{
			name:     "aider error",
			text:     "exception: file not found",
			patterns: aiderErrorPatterns,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchAny(tt.text, tt.patterns); got != tt.want {
				t.Errorf("matchAny() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchAnyRegex(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		patterns []*regexp.Regexp
		want     bool
	}{
		{
			name:     "prompt match",
			text:     "What would you like me to do? >",
			patterns: ccIdlePatterns,
			want:     true,
		},
		{
			name:     "human prompt",
			text:     "Human: ",
			patterns: ccIdlePatterns,
			want:     true,
		},
		{
			name:     "codex prompt",
			text:     "47% context left · ? for shortcuts",
			patterns: codIdlePatterns,
			want:     true,
		},
		{
			name:     "codex chevron prompt",
			text:     "› Write tests for @filename",
			patterns: codIdlePatterns,
			want:     true,
		},
		{
			name:     "no match",
			text:     "Processing your request...",
			patterns: ccIdlePatterns,
			want:     false,
		},
		{
			name:     "cursor prompt",
			text:     "cursor> ",
			patterns: cursorIdlePatterns,
			want:     true,
		},
		{
			name:     "windsurf prompt",
			text:     "windsurf> ",
			patterns: windsurfIdlePatterns,
			want:     true,
		},
		{
			name:     "aider prompt",
			text:     "aider> ",
			patterns: aiderIdlePatterns,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchAnyRegex(tt.text, tt.patterns); got != tt.want {
				t.Errorf("matchAnyRegex() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCollectMatches(t *testing.T) {
	text := "Rate limit exceeded. Please wait and try again later."
	matches := collectMatches(text, ccRateLimitPatterns)

	if len(matches) == 0 {
		t.Error("Expected at least one match")
	}

	// Should find multiple patterns
	found := map[string]bool{}
	for _, m := range matches {
		found[m] = true
	}

	if !found["rate limit"] && !found["rate limit exceeded"] {
		t.Error("Expected to find 'rate limit' pattern")
	}
	if !found["please wait"] {
		t.Error("Expected to find 'please wait' pattern")
	}
}

func TestExtractFloat(t *testing.T) {
	tests := []struct {
		name    string
		pattern *regexp.Regexp
		text    string
		want    *float64
	}{
		{
			name:    "codex context percentage",
			pattern: codContextPattern,
			text:    "47% context left · ? for shortcuts",
			want:    floatPtr(47.0),
		},
		{
			name:    "codex low context",
			pattern: codContextPattern,
			text:    "5% context left",
			want:    floatPtr(5.0),
		},
		{
			name:    "gemini memory",
			pattern: gmiMemoryPattern,
			text:    "gemini-3-pro-preview /model | 396.8 MB",
			want:    floatPtr(396.8),
		},
		{
			name:    "no match returns nil",
			pattern: codContextPattern,
			text:    "No context information here",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFloat(tt.pattern, tt.text)
			if tt.want == nil {
				if got != nil {
					t.Errorf("extractFloat() = %v, want nil", *got)
				}
			} else {
				if got == nil {
					t.Errorf("extractFloat() = nil, want %v", *tt.want)
				} else if *got != *tt.want {
					t.Errorf("extractFloat() = %v, want %v", *got, *tt.want)
				}
			}
		})
	}
}

func TestExtractInt(t *testing.T) {
	tests := []struct {
		name    string
		pattern *regexp.Regexp
		text    string
		want    *int64
	}{
		{
			name:    "codex token count with commas",
			pattern: codTokenPattern,
			text:    "Token usage: total=219,582 input=206,150",
			want:    intPtr(219582),
		},
		{
			name:    "simple number",
			pattern: codTokenPattern,
			text:    "Token usage: total=1000",
			want:    intPtr(1000),
		},
		{
			name:    "no match",
			pattern: codTokenPattern,
			text:    "No token information",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInt(tt.pattern, tt.text)
			if tt.want == nil {
				if got != nil {
					t.Errorf("extractInt() = %v, want nil", *got)
				}
			} else {
				if got == nil {
					t.Errorf("extractInt() = nil, want %v", *tt.want)
				} else if *got != *tt.want {
					t.Errorf("extractInt() = %v, want %v", *got, *tt.want)
				}
			}
		})
	}
}

func TestGetLastNLines(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		n     int
		lines int // expected number of lines
	}{
		{
			name:  "more lines than n",
			text:  "line1\nline2\nline3\nline4\nline5",
			n:     3,
			lines: 3,
		},
		{
			name:  "fewer lines than n",
			text:  "line1\nline2",
			n:     5,
			lines: 2,
		},
		{
			name:  "exactly n lines",
			text:  "line1\nline2\nline3",
			n:     3,
			lines: 3,
		},
		{
			name:  "empty text",
			text:  "",
			n:     3,
			lines: 1, // Split produces one empty element
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getLastNLines(tt.text, tt.n)
			// Count lines in result
			var lineCount int
			if result == "" {
				lineCount = 0
			} else {
				lineCount = 1
				for _, c := range result {
					if c == '\n' {
						lineCount++
					}
				}
			}

			// For non-empty text, verify we got the right count
			if tt.text != "" && lineCount > tt.n {
				t.Errorf("getLastNLines() returned %d lines, want at most %d", lineCount, tt.n)
			}
		})
	}
}

func TestStripANSICodes(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "color codes",
			text: "\x1b[32mSuccess\x1b[0m",
			want: "Success",
		},
		{
			name: "bold and color",
			text: "\x1b[1;31mError:\x1b[0m Something went wrong",
			want: "Error: Something went wrong",
		},
		{
			name: "no codes",
			text: "Plain text",
			want: "Plain text",
		},
		{
			name: "cursor movement",
			text: "\x1b[2J\x1b[HClear screen",
			want: "Clear screen",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripANSICodes(tt.text); got != tt.want {
				t.Errorf("stripANSICodes() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHeaderPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		pattern *regexp.Regexp
		want    bool
	}{
		{
			name:    "claude opus",
			text:    "Claude Opus 4.5 is ready",
			pattern: ccHeaderPattern,
			want:    true,
		},
		{
			name:    "claude sonnet",
			text:    "Using sonnet 3.5",
			pattern: ccHeaderPattern,
			want:    true,
		},
		{
			name:    "codex",
			text:    "OpenAI Codex CLI",
			pattern: codHeaderPattern,
			want:    true,
		},
		{
			name:    "gpt model",
			text:    "GPT-4 turbo",
			pattern: codHeaderPattern,
			want:    true,
		},
		{
			name:    "gemini",
			text:    "gemini-2.0-flash-preview",
			pattern: gmiHeaderPattern,
			want:    true,
		},
		{
			name:    "google ai",
			text:    "Google AI Studio",
			pattern: gmiHeaderPattern,
			want:    true,
		},
		{
			name:    "cursor header",
			text:    "Cursor AI ready",
			pattern: cursorHeaderPattern,
			want:    true,
		},
		{
			name:    "cursor plain",
			text:    "cursor session started",
			pattern: cursorHeaderPattern,
			want:    true,
		},
		{
			name:    "windsurf header",
			text:    "Windsurf IDE connected",
			pattern: windsurfHeaderPattern,
			want:    true,
		},
		{
			name:    "windsurf plain",
			text:    "windsurf is ready",
			pattern: windsurfHeaderPattern,
			want:    true,
		},
		{
			name:    "aider header",
			text:    "aider chat session",
			pattern: aiderHeaderPattern,
			want:    true,
		},
		{
			name:    "aider plain",
			text:    "Aider started",
			pattern: aiderHeaderPattern,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.pattern.MatchString(tt.text); got != tt.want {
				t.Errorf("HeaderPattern.MatchString(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestGetPatternSet(t *testing.T) {
	tests := []struct {
		agentType         AgentType
		hasRateLimitPats  bool
		hasWorkingPats    bool
		hasIdlePats       bool
		hasContextPattern bool
	}{
		{
			agentType:         AgentTypeClaudeCode,
			hasRateLimitPats:  true,
			hasWorkingPats:    true,
			hasIdlePats:       true,
			hasContextPattern: false, // Claude uses warnings, not explicit pattern
		},
		{
			agentType:         AgentTypeCodex,
			hasRateLimitPats:  true,
			hasWorkingPats:    true,
			hasIdlePats:       true,
			hasContextPattern: true, // Codex has explicit context pattern
		},
		{
			agentType:         AgentTypeGemini,
			hasRateLimitPats:  true,
			hasWorkingPats:    true,
			hasIdlePats:       true,
			hasContextPattern: false,
		},
		{
			agentType:         AgentTypeCursor,
			hasRateLimitPats:  true,
			hasWorkingPats:    true,
			hasIdlePats:       true,
			hasContextPattern: false,
		},
		{
			agentType:         AgentTypeWindsurf,
			hasRateLimitPats:  true,
			hasWorkingPats:    true,
			hasIdlePats:       true,
			hasContextPattern: false,
		},
		{
			agentType:         AgentTypeAider,
			hasRateLimitPats:  true,
			hasWorkingPats:    true,
			hasIdlePats:       true,
			hasContextPattern: false,
		},
		{
			agentType:         AgentTypeUnknown,
			hasRateLimitPats:  false,
			hasWorkingPats:    false,
			hasIdlePats:       false,
			hasContextPattern: false,
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.agentType), func(t *testing.T) {
			ps := GetPatternSet(tt.agentType)

			if (len(ps.RateLimitPatterns) > 0) != tt.hasRateLimitPats {
				t.Errorf("RateLimitPatterns presence = %v, want %v",
					len(ps.RateLimitPatterns) > 0, tt.hasRateLimitPats)
			}
			if (len(ps.WorkingPatterns) > 0) != tt.hasWorkingPats {
				t.Errorf("WorkingPatterns presence = %v, want %v",
					len(ps.WorkingPatterns) > 0, tt.hasWorkingPats)
			}
			if (len(ps.IdlePatterns) > 0) != tt.hasIdlePats {
				t.Errorf("IdlePatterns presence = %v, want %v",
					len(ps.IdlePatterns) > 0, tt.hasIdlePats)
			}
			if (ps.ContextPattern != nil) != tt.hasContextPattern {
				t.Errorf("ContextPattern presence = %v, want %v",
					ps.ContextPattern != nil, tt.hasContextPattern)
			}
		})
	}
}

func TestYoloModePattern(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"YOLO mode: ON", true},
		{"YOLO mode: OFF", true},
		{"yolo mode: on", true},
		{"No YOLO here", false},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := gmiYoloPattern.MatchString(tt.text); got != tt.want {
				t.Errorf("gmiYoloPattern.MatchString(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestShellModePattern(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"! ls -la", true},
		{"!ls", true},
		{"  ! pwd", false}, // Leading whitespace - pattern starts with ^
		{"Normal text", false},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := gmiShellModePattern.MatchString(tt.text); got != tt.want {
				t.Errorf("gmiShellModePattern.MatchString(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestCCSpinnerPattern(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		// Active spinner (present continuous + ellipsis + timing)
		{"active spinner Bunning", "✢ Bunning… (3m 42s · thinking)", true},
		{"active spinner Scurrying", "· Scurrying… (2m 0s · thought for 5s)", true},
		{"active spinner Zesting", "✶ Zesting… (1m 42s · thought for 1s)", true},
		{"active spinner short", "Churning… (5s · thinking)", true},

		// Running indicator
		{"tool running", "Running… (1m 9s · timeout 5m)", true},

		// Past tense completion
		{"past tense Baked", "✻ Baked for 5m 24s", true},
		{"past tense Churned", "✻ Churned for 3m 42s", true},

		// Thinking indicators
		{"thinking bare", "· thinking", true},
		{"thought for", "· thought for 20s", true},

		// Should NOT match
		{"plain text", "Everything is working fine", false},
		{"bypass status", "bypass permissions on", false},
		{"prompt only", ">", false},
		{"code output", "func main() {}", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ccSpinnerPattern.MatchString(tt.text); got != tt.want {
				t.Errorf("ccSpinnerPattern.MatchString(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestCCIdlePatterns_BypassRemoved(t *testing.T) {
	// Verify "bypass permissions on" no longer matches idle patterns.
	// This was a permanent status bar indicator that caused false idle detection.
	text := "bypass permissions on (shift+tab to toggle)"
	if matchAnyRegex(text, ccIdlePatterns) {
		t.Error("ccIdlePatterns should NOT match 'bypass permissions on' — it is a permanent status bar, not a state indicator")
	}
}

// Test that all regex patterns compile correctly
func TestAllPatternsCompile(t *testing.T) {
	// If we got here, all patterns compiled (they're package-level vars)
	// This test documents that fact and catches any future regex errors

	patterns := []*regexp.Regexp{
		codContextPattern,
		codTokenPattern,
		gmiMemoryPattern,
		gmiYoloPattern,
		gmiShellModePattern,
		ccHeaderPattern,
		codHeaderPattern,
		gmiHeaderPattern,
		cursorHeaderPattern,
		windsurfHeaderPattern,
		aiderHeaderPattern,
		ansiPattern,
		ccSpinnerPattern,
	}

	for i, p := range patterns {
		if p == nil {
			t.Errorf("Pattern %d is nil", i)
		}
	}

	// Check slice patterns
	patternSlices := [][]*regexp.Regexp{
		ccIdlePatterns,
		codIdlePatterns,
		gmiIdlePatterns,
		cursorIdlePatterns,
		windsurfIdlePatterns,
		aiderIdlePatterns,
	}

	for i, ps := range patternSlices {
		for j, p := range ps {
			if p == nil {
				t.Errorf("Pattern slice %d, element %d is nil", i, j)
			}
		}
	}
}

// Helper functions for creating pointers
func floatPtr(v float64) *float64 {
	return &v
}

func intPtr(v int64) *int64 {
	return &v
}
