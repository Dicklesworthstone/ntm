package cli

import (
	"testing"
)

// ---------------------------------------------------------------------------
// detectAgentTypeFromTitle — 0% → 100%
// ---------------------------------------------------------------------------

func TestDetectAgentTypeFromTitle(t *testing.T) {

	tests := []struct {
		name  string
		title string
		want  string
	}{
		// Claude detection
		{"claude via __cc prefix", "myproject__cc_1", "claude"},
		{"claude via keyword", "project Claude Code", "claude"},
		{"claude mixed case", "CLAUDE session", "claude"},

		// Codex detection
		{"codex via __cod prefix", "myproject__cod_2", "codex"},
		{"codex via keyword", "Codex agent running", "codex"},

		// Gemini detection
		{"gemini via __gmi prefix", "myproject__gmi_1", "gemini"},
		{"gemini via keyword", "gemini-pro session", "gemini"},

		// New agent detection
		{"cursor via __cursor prefix", "myproject__cursor_1", "cursor"},
		{"windsurf via __windsurf prefix", "myproject__windsurf_2", "windsurf"},
		{"windsurf via __ws prefix", "myproject__ws_2", "windsurf"},
		{"aider via __aider prefix", "myproject__aider_3", "aider"},
		{"ollama via __ollama prefix", "myproject__ollama_4", "ollama"},

		// User detection
		{"user via __user prefix", "myproject__user_1", "user"},
		{"user via keyword", "user terminal", "user"},

		// Session names can themselves contain double underscores
		{"session name with embedded double underscore", "my__project__cc_1", "claude"},

		// Unknown
		{"unknown agent type", "random-pane-title", "unknown"},
		{"partial token should stay unknown", "project__ccidental_1", "unknown"},
		{"empty title", "", "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectAgentTypeFromTitle(tc.title)
			if got != tc.want {
				t.Errorf("detectAgentTypeFromTitle(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatTokenCount — 0% → 100%
// ---------------------------------------------------------------------------

func TestFormatTokenCount(t *testing.T) {

	tests := []struct {
		name   string
		tokens int
		want   string
	}{
		{"zero", 0, "0"},
		{"small", 42, "42"},
		{"under 1K", 999, "999"},
		{"exactly 1K", 1000, "1.0K"},
		{"1500 tokens", 1500, "1.5K"},
		{"under 1M", 999999, "1000.0K"},
		{"exactly 1M", 1000000, "1.0M"},
		{"1.5M tokens", 1500000, "1.5M"},
		{"large", 10000000, "10.0M"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTokenCount(tc.tokens)
			if got != tc.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tc.tokens, got, tc.want)
			}
		})
	}
}
