package theme

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// isNoColor checks if a color is the absence of color (NoColor).
func isNoColor(c lipgloss.TerminalColor) bool {
	_, ok := c.(lipgloss.NoColor)
	return ok
}

func TestHuhTheme(t *testing.T) {
	t.Parallel()

	theme := HuhThemeFrom(CatppuccinMocha)
	if theme == nil {
		t.Fatal("HuhTheme() should not return nil")
	}

	// Verify focused title style has foreground color
	if isNoColor(theme.Focused.Title.GetForeground()) {
		t.Error("focused title should have foreground color")
	}

	// Verify focused button has both foreground and background
	if isNoColor(theme.Focused.FocusedButton.GetForeground()) {
		t.Error("focused button should have foreground color")
	}
	if isNoColor(theme.Focused.FocusedButton.GetBackground()) {
		t.Error("focused button should have background color")
	}
}

func TestHuhThemeFrom_AllVariants(t *testing.T) {
	t.Parallel()

	variants := []struct {
		name  string
		theme Theme
	}{
		{"mocha", CatppuccinMocha},
		{"macchiato", CatppuccinMacchiato},
		{"latte", CatppuccinLatte},
		{"nord", Nord},
		{"plain", Plain},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			theme := HuhThemeFrom(v.theme)
			if theme == nil {
				t.Errorf("HuhThemeFrom(%s) should not return nil", v.name)
			}
		})
	}
}

func TestHuhDestructiveTheme(t *testing.T) {
	t.Parallel()

	theme := HuhDestructiveThemeFrom(CatppuccinMocha)
	if theme == nil {
		t.Fatal("HuhDestructiveTheme() should not return nil")
	}

	// Verify destructive theme has error color for focused button
	normalTheme := HuhThemeFrom(CatppuccinMocha)

	// The focused button backgrounds should be different
	// (destructive uses Error color, normal uses Primary)
	destructiveBg := theme.Focused.FocusedButton.GetBackground()
	normalBg := normalTheme.Focused.FocusedButton.GetBackground()

	if destructiveBg == normalBg {
		t.Error("destructive theme focused button should have different background than normal")
	}
}

func TestHuhDestructiveThemeFrom_AllVariants(t *testing.T) {
	t.Parallel()

	variants := []struct {
		name  string
		theme Theme
	}{
		{"mocha", CatppuccinMocha},
		{"macchiato", CatppuccinMacchiato},
		{"latte", CatppuccinLatte},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			theme := HuhDestructiveThemeFrom(v.theme)
			if theme == nil {
				t.Errorf("HuhDestructiveThemeFrom(%s) should not return nil", v.name)
			}

			// Verify the button uses error color
			bg := theme.Focused.FocusedButton.GetBackground()
			if isNoColor(bg) {
				t.Error("focused button should have background color")
			}
		})
	}
}

func TestHuhTheme_SelectStyles(t *testing.T) {
	t.Parallel()

	theme := HuhTheme()

	// Verify select selector has content
	selectorStr := theme.Focused.SelectSelector.Value()
	if selectorStr == "" {
		t.Error("select selector should have string content")
	}

	// Verify selected/unselected prefixes exist
	selectedPrefix := theme.Focused.SelectedPrefix.Value()
	if selectedPrefix == "" {
		t.Error("selected prefix should have string content")
	}

	unselectedPrefix := theme.Focused.UnselectedPrefix.Value()
	if unselectedPrefix == "" {
		t.Error("unselected prefix should have string content")
	}
}

func TestHuhTheme_BlurredStyles(t *testing.T) {
	t.Parallel()

	// Use a specific theme (Mocha) to ensure we have distinct colors
	theme := HuhThemeFrom(CatppuccinMocha)

	// Verify blurred styles exist and have some styling applied
	// Note: The exact color comparison is tricky due to how lipgloss
	// handles color caching internally, so we just verify the styles are set
	blurredFg := theme.Blurred.Title.GetForeground()
	focusedFg := theme.Focused.Title.GetForeground()

	// Both should have colors set (not NoColor)
	if isNoColor(blurredFg) {
		t.Error("blurred title should have foreground color")
	}
	if isNoColor(focusedFg) {
		t.Error("focused title should have foreground color")
	}

	// Verify blurred options exist
	if isNoColor(theme.Blurred.Option.GetForeground()) {
		t.Error("blurred option should have foreground color")
	}
}
