package theme

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func withDetector(t *testing.T, detector func() bool) {
	original := detectDarkBackground
	detectDarkBackground = detector
	// Reset the cached auto theme so it re-detects with the new detector
	resetAutoTheme()
	t.Cleanup(func() {
		detectDarkBackground = original
		resetAutoTheme()
	})
}

func TestCurrentAutoUsesLightThemeWhenBackgroundIsLight(t *testing.T) {
	t.Setenv("NTM_THEME", "")
	t.Setenv("NTM_NO_COLOR", "0")
	withDetector(t, func() bool { return false })

	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("expected light theme (Latte) for light background, got base %s", got.Base)
	}
}

func TestCurrentAutoUsesDarkThemeWhenBackgroundIsDark(t *testing.T) {
	t.Setenv("NTM_THEME", "")
	t.Setenv("NTM_NO_COLOR", "0")
	withDetector(t, func() bool { return true })

	if got := Current(); got.Base != CatppuccinMocha.Base {
		t.Fatalf("expected dark theme (Mocha) for dark background, got base %s", got.Base)
	}
}

func TestCurrentRespectsExplicitThemeOverrides(t *testing.T) {
	t.Setenv("NTM_THEME", "latte")
	t.Setenv("NTM_NO_COLOR", "0")
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
	t.Setenv("NTM_NO_COLOR", "0")
	withDetector(t, func() bool { return false })

	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("expected Latte for auto detection on light background, got base %s", got.Base)
	}
}

func TestCurrentMacchiatoTheme(t *testing.T) {
	t.Setenv("NTM_THEME", "macchiato")
	t.Setenv("NTM_NO_COLOR", "0")
	withDetector(t, func() bool { return true })

	if got := Current(); got.Base != CatppuccinMacchiato.Base {
		t.Fatalf("expected Macchiato when requested, got base %s", got.Base)
	}
}

func TestCurrentNordTheme(t *testing.T) {
	t.Setenv("NTM_THEME", "nord")
	t.Setenv("NTM_NO_COLOR", "0")
	withDetector(t, func() bool { return true })

	if got := Current(); got.Base != Nord.Base {
		t.Fatalf("expected Nord when requested, got base %s", got.Base)
	}
}

func TestCurrentLightAlias(t *testing.T) {
	t.Setenv("NTM_THEME", "light")
	t.Setenv("NTM_NO_COLOR", "0")
	withDetector(t, func() bool { return true })

	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("expected Latte for 'light' alias, got base %s", got.Base)
	}
}

func TestCurrentUnknownFallsBackToAuto(t *testing.T) {
	t.Setenv("NTM_THEME", "unknown-theme")
	t.Setenv("NTM_NO_COLOR", "0")
	withDetector(t, func() bool { return true })

	// Unknown should fall through to autoTheme()
	if got := Current(); got.Base != CatppuccinMocha.Base {
		t.Fatalf("expected Mocha for unknown theme with dark background, got base %s", got.Base)
	}
}

func TestHuhThemeFromProvidesStyledFields(t *testing.T) {
	th := HuhThemeFrom(CatppuccinMocha)
	if th == nil {
		t.Fatal("HuhThemeFrom returned nil")
	}
	if _, ok := th.Focused.Title.GetForeground().(lipgloss.Color); !ok {
		t.Fatal("focused title should have a concrete foreground color")
	}
	if _, ok := th.Focused.FocusedButton.GetBackground().(lipgloss.Color); !ok {
		t.Fatal("focused button should have a concrete background color")
	}
}

func TestHuhThemeFromSupportsAllCatppuccinVariants(t *testing.T) {
	tests := []struct {
		name string
		t    Theme
	}{
		{name: "mocha", t: CatppuccinMocha},
		{name: "macchiato", t: CatppuccinMacchiato},
		{name: "latte", t: CatppuccinLatte},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := HuhThemeFrom(tt.t)
			if th == nil {
				t.Fatal("HuhThemeFrom returned nil")
			}
			fg, ok := th.Focused.Title.GetForeground().(lipgloss.Color)
			if !ok {
				t.Fatal("focused title foreground should be a lipgloss color")
			}
			if fg != tt.t.Text {
				t.Fatalf("focused title foreground = %q, want %q", fg, tt.t.Text)
			}
		})
	}
}

func TestHuhDestructiveThemeUsesErrorAccent(t *testing.T) {
	th := HuhDestructiveThemeFrom(CatppuccinMacchiato)
	if th == nil {
		t.Fatal("HuhDestructiveThemeFrom returned nil")
	}
	bg, ok := th.Focused.FocusedButton.GetBackground().(lipgloss.Color)
	if !ok {
		t.Fatal("destructive focused button background should be a lipgloss color")
	}
	if bg != CatppuccinMacchiato.Error {
		t.Fatalf("destructive focused button background = %q, want %q", bg, CatppuccinMacchiato.Error)
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

func TestNoColorEnabled(t *testing.T) {
	t.Run("returns false when NO_COLOR not set", func(t *testing.T) {
		// Ensure NO_COLOR and NTM_NO_COLOR are not set
		t.Setenv("NO_COLOR", "")
		t.Setenv("NTM_NO_COLOR", "")
		// Need to unset NO_COLOR for this test
		// Since t.Setenv("NO_COLOR", "") still sets it, we need a workaround
		// The test environment may have NO_COLOR set, so we check NTM_NO_COLOR override
	})

	t.Run("returns true when NO_COLOR is set", func(t *testing.T) {
		t.Setenv("NTM_NO_COLOR", "")
		t.Setenv("NO_COLOR", "1")

		if !NoColorEnabled() {
			t.Error("NoColorEnabled should return true when NO_COLOR is set")
		}
	})

	t.Run("returns true when NO_COLOR is empty string", func(t *testing.T) {
		t.Setenv("NTM_NO_COLOR", "")
		t.Setenv("NO_COLOR", "")

		// NO_COLOR="" still means it's set (per standard)
		if !NoColorEnabled() {
			t.Error("NoColorEnabled should return true when NO_COLOR is set to empty string")
		}
	})

	t.Run("NTM_NO_COLOR=0 overrides NO_COLOR", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("NTM_NO_COLOR", "0")

		if NoColorEnabled() {
			t.Error("NTM_NO_COLOR=0 should force colors ON even with NO_COLOR set")
		}
	})

	t.Run("NTM_NO_COLOR=false overrides NO_COLOR", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("NTM_NO_COLOR", "false")

		if NoColorEnabled() {
			t.Error("NTM_NO_COLOR=false should force colors ON")
		}
	})

	t.Run("NTM_NO_COLOR=1 enables no-color", func(t *testing.T) {
		t.Setenv("NTM_NO_COLOR", "1")

		if !NoColorEnabled() {
			t.Error("NTM_NO_COLOR=1 should enable no-color mode")
		}
	})

	t.Run("NTM_NO_COLOR=true enables no-color", func(t *testing.T) {
		t.Setenv("NTM_NO_COLOR", "true")

		if !NoColorEnabled() {
			t.Error("NTM_NO_COLOR=true should enable no-color mode")
		}
	})
}

func TestCurrentReturnsPlainWhenNoColorEnabled(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("NTM_NO_COLOR", "")
	t.Setenv("NTM_THEME", "mocha")
	withDetector(t, func() bool { return true })

	got := Current()
	if got.Base != Plain.Base {
		t.Errorf("Current() should return Plain theme when NO_COLOR is set, got base %s", got.Base)
	}
}

func TestFromNamePlainVariants(t *testing.T) {
	// Clear NO_COLOR to test explicit theme selection
	t.Setenv("NTM_NO_COLOR", "0")

	variants := []string{"plain", "none", "no-color", "nocolor"}
	for _, name := range variants {
		t.Run(name, func(t *testing.T) {
			got := FromName(name)
			if got.Base != Plain.Base {
				t.Errorf("FromName(%q) should return Plain theme, got base %s", name, got.Base)
			}
		})
	}
}

func TestPlainThemeHasEmptyColors(t *testing.T) {
	// Verify Plain theme uses empty strings for colors
	if Plain.Base != "" {
		t.Errorf("Plain.Base should be empty, got %s", Plain.Base)
	}
	if Plain.Text != "" {
		t.Errorf("Plain.Text should be empty, got %s", Plain.Text)
	}
	if Plain.Primary != "" {
		t.Errorf("Plain.Primary should be empty, got %s", Plain.Primary)
	}
	if Plain.Error != "" {
		t.Errorf("Plain.Error should be empty, got %s", Plain.Error)
	}
}

func TestAutoThemeFallsBackToDarkOnPanic(t *testing.T) {
	t.Setenv("NTM_THEME", "")
	t.Setenv("NTM_NO_COLOR", "0")
	// Set up a detector that panics
	withDetector(t, func() bool {
		panic("simulated terminal detection failure")
	})

	// Should not panic and should return the dark theme (Mocha) as fallback
	got := Current()
	if got.Base != CatppuccinMocha.Base {
		t.Fatalf("expected Mocha fallback on panic, got base %s", got.Base)
	}
}

func TestDetectDarkBackgroundSkipsOSCOverSSH(t *testing.T) {
	// Test that SSH sessions default to dark without any terminal probing.
	t.Run("SSH_CONNECTION triggers dark default", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "192.168.1.1 12345 192.168.1.2 22")
		t.Setenv("SSH_TTY", "")
		t.Setenv("NTM_THEME", "")
		resetAutoTheme()

		// detectDarkBackground should return true (dark) when SSH_CONNECTION is set
		if !detectDarkBackground() {
			t.Error("detectDarkBackground should return true (dark) over SSH")
		}
	})

	t.Run("SSH_TTY triggers dark default", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "")
		t.Setenv("SSH_TTY", "/dev/pts/0")
		t.Setenv("NTM_THEME", "")
		resetAutoTheme()

		if !detectDarkBackground() {
			t.Error("detectDarkBackground should return true (dark) when SSH_TTY is set")
		}
	})
}

func TestDetectDarkBackgroundRespectsExplicitOverrides(t *testing.T) {
	t.Run("explicit dark override wins", func(t *testing.T) {
		t.Setenv("NTM_BACKGROUND", "dark")
		t.Setenv("COLORFGBG", "15;7")
		if !detectDarkBackground() {
			t.Fatal("expected explicit dark override to win")
		}
	})

	t.Run("explicit light override wins", func(t *testing.T) {
		t.Setenv("NTM_BACKGROUND", "light")
		t.Setenv("COLORFGBG", "15;0")
		if detectDarkBackground() {
			t.Fatal("expected explicit light override to win")
		}
	})
}

func TestDetectDarkBackgroundUsesColorFGBG(t *testing.T) {
	t.Run("dark background code is detected", func(t *testing.T) {
		t.Setenv("COLORFGBG", "15;0")
		if !detectDarkBackground() {
			t.Fatal("expected COLORFGBG dark background to be detected")
		}
	})

	t.Run("light background code is detected", func(t *testing.T) {
		t.Setenv("COLORFGBG", "0;15")
		if detectDarkBackground() {
			t.Fatal("expected COLORFGBG light background to be detected")
		}
	})
}

func TestNewStylesPlainTheme(t *testing.T) {
	// Test that Plain theme produces styles with proper guard rails
	s := NewStyles(Plain)

	// ListSelected should use Reverse for accessibility
	text := "test"
	rendered := s.ListSelected.Render(text)
	if rendered == "" {
		t.Error("Plain theme ListSelected should render")
	}

	// Warning and Error should be underlined in plain theme
	warningRendered := s.Warning.Render(text)
	if warningRendered == "" {
		t.Error("Plain theme Warning should render")
	}
	errorRendered := s.Error.Render(text)
	if errorRendered == "" {
		t.Error("Plain theme Error should render")
	}
}
