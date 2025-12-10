package theme

import "testing"

func withDetector(t *testing.T, detector func() bool) {
	original := detectDarkBackground
	detectDarkBackground = detector
	t.Cleanup(func() {
		detectDarkBackground = original
	})
}

func TestCurrentAutoUsesLightThemeWhenBackgroundIsLight(t *testing.T) {
	t.Setenv("NTM_THEME", "")
	withDetector(t, func() bool { return false })

	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("expected light theme (Latte) for light background, got base %s", got.Base)
	}
}

func TestCurrentAutoUsesDarkThemeWhenBackgroundIsDark(t *testing.T) {
	t.Setenv("NTM_THEME", "")
	withDetector(t, func() bool { return true })

	if got := Current(); got.Base != CatppuccinMocha.Base {
		t.Fatalf("expected dark theme (Mocha) for dark background, got base %s", got.Base)
	}
}

func TestCurrentRespectsExplicitThemeOverrides(t *testing.T) {
	t.Setenv("NTM_THEME", "latte")
	withDetector(t, func() bool { return true })

	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("expected Latte when explicitly requested, got base %s", got.Base)
	}

	t.Setenv("NTM_THEME", "mocha")
	withDetector(t, func() bool { return false })

	if got := Current(); got.Base != CatppuccinMocha.Base {
		t.Fatalf("expected Mocha when explicitly requested, got base %s", got.Base)
	}
}

func TestCurrentTreatsAutoValueAsDetection(t *testing.T) {
	t.Setenv("NTM_THEME", "auto")
	withDetector(t, func() bool { return false })

	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("expected Latte for auto detection on light background, got base %s", got.Base)
	}
}

func TestCurrentMacchiatoTheme(t *testing.T) {
	t.Setenv("NTM_THEME", "macchiato")
	withDetector(t, func() bool { return true })

	if got := Current(); got.Base != CatppuccinMacchiato.Base {
		t.Fatalf("expected Macchiato when requested, got base %s", got.Base)
	}
}

func TestCurrentNordTheme(t *testing.T) {
	t.Setenv("NTM_THEME", "nord")
	withDetector(t, func() bool { return true })

	if got := Current(); got.Base != Nord.Base {
		t.Fatalf("expected Nord when requested, got base %s", got.Base)
	}
}

func TestCurrentLightAlias(t *testing.T) {
	t.Setenv("NTM_THEME", "light")
	withDetector(t, func() bool { return true })

	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("expected Latte for 'light' alias, got base %s", got.Base)
	}
}

func TestCurrentUnknownFallsBackToAuto(t *testing.T) {
	t.Setenv("NTM_THEME", "unknown-theme")
	withDetector(t, func() bool { return true })

	// Unknown should fall through to autoTheme()
	if got := Current(); got.Base != CatppuccinMocha.Base {
		t.Fatalf("expected Mocha for unknown theme with dark background, got base %s", got.Base)
	}
}

func TestThemeColors(t *testing.T) {
	themes := []struct {
		name  string
		theme Theme
	}{
		{"Mocha", CatppuccinMocha},
		{"Macchiato", CatppuccinMacchiato},
		{"Latte", CatppuccinLatte},
		{"Nord", Nord},
	}

	for _, tt := range themes {
		t.Run(tt.name, func(t *testing.T) {
			// Verify all required colors are set
			if tt.theme.Base == "" {
				t.Error("Base color should not be empty")
			}
			if tt.theme.Text == "" {
				t.Error("Text color should not be empty")
			}
			if tt.theme.Primary == "" {
				t.Error("Primary color should not be empty")
			}
			if tt.theme.Claude == "" {
				t.Error("Claude color should not be empty")
			}
			if tt.theme.Codex == "" {
				t.Error("Codex color should not be empty")
			}
			if tt.theme.Gemini == "" {
				t.Error("Gemini color should not be empty")
			}
		})
	}
}

func TestNewStyles(t *testing.T) {
	s := NewStyles(CatppuccinMocha)

	// Test various styles render correctly
	text := "test"
	if s.Normal.Render(text) == "" {
		t.Error("Normal style should render")
	}
	if s.Bold.Render(text) == "" {
		t.Error("Bold style should render")
	}
	if s.Success.Render(text) == "" {
		t.Error("Success style should render")
	}
	if s.Error.Render(text) == "" {
		t.Error("Error style should render")
	}
	if s.Claude.Render(text) == "" {
		t.Error("Claude style should render")
	}
}

func TestDefaultStyles(t *testing.T) {
	s := DefaultStyles()

	if s.Normal.Render("test") == "" {
		t.Error("DefaultStyles() should return working styles")
	}
}

func TestGradient(t *testing.T) {
	theme := CatppuccinMocha

	t.Run("fewer steps than colors", func(t *testing.T) {
		grad := theme.Gradient(3)
		if len(grad) != 3 {
			t.Errorf("expected 3 colors, got %d", len(grad))
		}
	})

	t.Run("exact steps as colors", func(t *testing.T) {
		grad := theme.Gradient(5)
		if len(grad) != 5 {
			t.Errorf("expected 5 colors, got %d", len(grad))
		}
	})

	t.Run("more steps than colors", func(t *testing.T) {
		grad := theme.Gradient(10)
		if len(grad) != 10 {
			t.Errorf("expected 10 colors, got %d", len(grad))
		}
	})

	t.Run("colors are not empty", func(t *testing.T) {
		grad := theme.Gradient(3)
		for i, c := range grad {
			if c == "" {
				t.Errorf("gradient color %d should not be empty", i)
			}
		}
	})
}
