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
		{"codex", string(p.AgentCodex)},
		{"cod", string(p.AgentCodex)},
		{"gemini", string(p.AgentGemini)},
		{"gmi", string(p.AgentGemini)},
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

func TestSemantic(t *testing.T) {
	// Test the global Semantic function
	p := Semantic()

	if p.BgPrimary == "" {
		t.Error("Semantic() should return a valid palette")
	}
}

func TestNewSemanticStyles(t *testing.T) {
	s := NewSemanticStyles(CatppuccinMocha)

	// Verify styles are created
	t.Run("text styles", func(t *testing.T) {
		// Render something to verify they work
		text := "test"
		if s.TextPrimary.Render(text) == "" {
			t.Error("TextPrimary.Render should not return empty")
		}
		if s.TextSecondary.Render(text) == "" {
			t.Error("TextSecondary.Render should not return empty")
		}
		if s.TextMuted.Render(text) == "" {
			t.Error("TextMuted.Render should not return empty")
		}
		if s.TextDisabled.Render(text) == "" {
			t.Error("TextDisabled.Render should not return empty")
		}
	})

	t.Run("status styles", func(t *testing.T) {
		text := "status"
		if s.TextSuccess.Render(text) == "" {
			t.Error("TextSuccess.Render should not return empty")
		}
		if s.TextWarning.Render(text) == "" {
			t.Error("TextWarning.Render should not return empty")
		}
		if s.TextError.Render(text) == "" {
			t.Error("TextError.Render should not return empty")
		}
		if s.TextInfo.Render(text) == "" {
			t.Error("TextInfo.Render should not return empty")
		}
	})

	t.Run("badge styles", func(t *testing.T) {
		text := "badge"
		if s.BadgeDefault.Render(text) == "" {
			t.Error("BadgeDefault.Render should not return empty")
		}
		if s.BadgeSuccess.Render(text) == "" {
			t.Error("BadgeSuccess.Render should not return empty")
		}
		if s.BadgeClaude.Render(text) == "" {
			t.Error("BadgeClaude.Render should not return empty")
		}
		if s.BadgeCodex.Render(text) == "" {
			t.Error("BadgeCodex.Render should not return empty")
		}
		if s.BadgeGemini.Render(text) == "" {
			t.Error("BadgeGemini.Render should not return empty")
		}
		if s.BadgeUser.Render(text) == "" {
			t.Error("BadgeUser.Render should not return empty")
		}
	})

	t.Run("container styles", func(t *testing.T) {
		text := "content"
		if s.Surface.Render(text) == "" {
			t.Error("Surface.Render should not return empty")
		}
		if s.Card.Render(text) == "" {
			t.Error("Card.Render should not return empty")
		}
		if s.CardRaised.Render(text) == "" {
			t.Error("CardRaised.Render should not return empty")
		}
	})

	t.Run("input styles", func(t *testing.T) {
		text := "input"
		if s.Input.Render(text) == "" {
			t.Error("Input.Render should not return empty")
		}
		if s.InputFocused.Render(text) == "" {
			t.Error("InputFocused.Render should not return empty")
		}
		if s.InputError.Render(text) == "" {
			t.Error("InputError.Render should not return empty")
		}
	})

	t.Run("selection styles", func(t *testing.T) {
		text := "item"
		if s.Selected.Render(text) == "" {
			t.Error("Selected.Render should not return empty")
		}
		if s.Unselected.Render(text) == "" {
			t.Error("Unselected.Render should not return empty")
		}
	})
}

func TestDefaultSemanticStyles(t *testing.T) {
	s := DefaultSemanticStyles()

	if s.TextPrimary.Render("test") == "" {
		t.Error("DefaultSemanticStyles() should return valid styles")
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
