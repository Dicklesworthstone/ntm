package theme

import "testing"

// The configured theme is the fallback for every Current() caller when
// NTM_THEME is unset; an explicit NTM_THEME still wins.
func TestCurrentFallsBackToConfiguredTheme(t *testing.T) {
	t.Setenv("NTM_THEME", "")
	t.Setenv("NTM_NO_COLOR", "0")
	withDetector(t, func() bool { return true })
	t.Cleanup(func() { SetConfigured("") })

	SetConfigured("latte")
	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("Current() with configured latte and no NTM_THEME = base %s, want Latte", got.Base)
	}

	t.Setenv("NTM_THEME", "nord")
	if got := Current(); got.Base != Nord.Base {
		t.Fatalf("Current() must prefer NTM_THEME over the configured theme, got base %s", got.Base)
	}

	t.Setenv("NTM_THEME", "   ")
	SetConfigured("  latte ")
	if got := Current(); got.Base != CatppuccinLatte.Base {
		t.Fatalf("blank NTM_THEME must fall back to the trimmed configured name, got base %s", got.Base)
	}

	SetConfigured("")
	if got := Current(); got.Base != CatppuccinMocha.Base {
		t.Fatalf("clearing the configured theme must restore auto-detection (dark), got base %s", got.Base)
	}
}
