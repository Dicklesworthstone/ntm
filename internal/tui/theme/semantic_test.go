package theme

import (
	"testing"
)

func TestSemanticPalette(t *testing.T) {
	t.Run("from CatppuccinMocha", func(t *testing.T) {
		p := CatppuccinMocha.Semantic()

		// Verify backgrounds are set
		if p.BgPrimary == "" {
			t.Error("BgPrimary should not be empty")
		}
		if p.BgSecondary == "" {
			t.Error("BgSecondary should not be empty")
		}

		// Verify foregrounds
		if p.FgPrimary == "" {
			t.Error("FgPrimary should not be empty")
		}

		// Verify agents
		if p.AgentClaude == "" {
			t.Error("AgentClaude should not be empty")
		}
		if p.AgentCodex == "" {
			t.Error("AgentCodex should not be empty")
		}
		if p.AgentGemini == "" {
			t.Error("AgentGemini should not be empty")
		}
		if p.AgentGrok == "" {
			t.Error("AgentGrok should not be empty")
		}
		if p.AgentGrok != CatppuccinMocha.Pink {
			t.Errorf("AgentGrok = %q, want theme pink %q", p.AgentGrok, CatppuccinMocha.Pink)
		}
		if p.AgentUser == "" {
			t.Error("AgentUser should not be empty")
		}

		// Verify status colors
		if p.StatusSuccess == "" {
			t.Error("StatusSuccess should not be empty")
		}
		if p.StatusError == "" {
			t.Error("StatusError should not be empty")
		}
	})

	t.Run("from CatppuccinMacchiato", func(t *testing.T) {
		p := CatppuccinMacchiato.Semantic()

		if p.BgPrimary == "" {
			t.Error("BgPrimary should not be empty")
		}
	})

	t.Run("from Nord", func(t *testing.T) {
		p := Nord.Semantic()

		if p.BgPrimary == "" {
			t.Error("BgPrimary should not be empty")
		}
	})
}

func TestAgentColor(t *testing.T) {
	p := CatppuccinMocha.Semantic()

	tests := []struct {
		agentType string
		expected  string
	}{
		{"claude", string(p.AgentClaude)},
		{"cc", string(p.AgentClaude)},
		{"claude_code", string(p.AgentClaude)},
		{"codex", string(p.AgentCodex)},
		{"cod", string(p.AgentCodex)},
		{"openai-codex", string(p.AgentCodex)},
		{"gemini", string(p.AgentGemini)},
		{"gmi", string(p.AgentGemini)},
		{"google-gemini", string(p.AgentGemini)},
		{"grok", string(p.AgentGrok)},
		{"grok-build", string(p.AgentGrok)},
		{"ws", string(p.AgentWindsurf)},
		{"oc", string(p.AgentOpencode)},
		{"opencode", string(p.AgentOpencode)},
		{"user", string(p.AgentUser)},
		{"unknown", string(p.AgentUnknown)},
		{"other", string(p.AgentUnknown)},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := p.AgentColor(tt.agentType)
			if string(got) != tt.expected {
				t.Errorf("AgentColor(%q) = %q, want %q", tt.agentType, got, tt.expected)
			}
		})
	}
}

func TestStatusColor(t *testing.T) {
	p := CatppuccinMocha.Semantic()

	tests := []struct {
		status   string
		expected string
	}{
		{"success", string(p.StatusSuccess)},
		{"ok", string(p.StatusSuccess)},
		{"complete", string(p.StatusSuccess)},
		{"completed", string(p.StatusSuccess)},
		{"done", string(p.StatusSuccess)},
		{"warning", string(p.StatusWarning)},
		{"warn", string(p.StatusWarning)},
		{"attention", string(p.StatusWarning)},
		{"error", string(p.StatusError)},
		{"fail", string(p.StatusError)},
		{"failed", string(p.StatusError)},
		{"failure", string(p.StatusError)},
		{"info", string(p.StatusInfo)},
		{"information", string(p.StatusInfo)},
		{"pending", string(p.StatusPending)},
		{"running", string(p.StatusPending)},
		{"in_progress", string(p.StatusPending)},
		{"working", string(p.StatusPending)},
		{"idle", string(p.StatusIdle)},
		{"inactive", string(p.StatusIdle)},
		{"waiting", string(p.StatusIdle)},
		{"disabled", string(p.StatusDisabled)},
		{"unavailable", string(p.StatusDisabled)},
		{"unknown", string(p.FgSecondary)},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := p.StatusColor(tt.status)
			if string(got) != tt.expected {
				t.Errorf("StatusColor(%q) = %q, want %q", tt.status, got, tt.expected)
			}
		})
	}
}

func TestSemanticPaletteConsistency(t *testing.T) {
	// Verify semantic palette is consistent across themes
	themes := []struct {
		name  string
		theme Theme
	}{
		{"Mocha", CatppuccinMocha},
		{"Macchiato", CatppuccinMacchiato},
		{"Nord", Nord},
	}

	// Verify all themes produce valid semantic palettes
	for _, tt := range themes {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.theme.Semantic()

			// All colors should be non-empty
			fields := []struct {
				name  string
				color string
			}{
				{"BgPrimary", string(p.BgPrimary)},
				{"BgSecondary", string(p.BgSecondary)},
				{"FgPrimary", string(p.FgPrimary)},
				{"FgSecondary", string(p.FgSecondary)},
				{"StatusSuccess", string(p.StatusSuccess)},
				{"StatusError", string(p.StatusError)},
				{"AgentClaude", string(p.AgentClaude)},
				{"AgentCodex", string(p.AgentCodex)},
				{"AgentGemini", string(p.AgentGemini)},
			}

			for _, f := range fields {
				if f.color == "" {
					t.Errorf("%s: %s should not be empty", tt.name, f.name)
				}
			}
		})
	}
}
