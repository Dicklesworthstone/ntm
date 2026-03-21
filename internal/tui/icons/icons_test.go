package icons

import (
	"os"
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func assertNoEmptyIcons(t *testing.T, icons IconSet) {
	t.Helper()

	v := reflect.ValueOf(icons)
	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() != reflect.String {
			continue
		}
		if v.Field(i).String() == "" {
			t.Fatalf("empty icon field %s", typ.Field(i).Name)
		}
	}
}

func assertMaxIconWidth(t *testing.T, icons IconSet, maxWidth int) {
	t.Helper()

	v := reflect.ValueOf(icons)
	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() != reflect.String {
			continue
		}
		value := v.Field(i).String()
		w := lipgloss.Width(value)
		if w > maxWidth {
			t.Fatalf("icon field %s too wide: %q (width=%d, max=%d)", typ.Field(i).Name, value, w, maxWidth)
		}
	}
}

func TestDetectDefaults(t *testing.T) {
	// Clear env vars
	os.Unsetenv("NTM_ICONS")
	os.Unsetenv("NTM_USE_ICONS")
	os.Unsetenv("NERD_FONTS")

	// Should default to ASCII
	icons := Detect()
	if icons.Session != "[]" { // ASCII session
		t.Errorf("Expected ASCII default, got session=%q", icons.Session)
	}
}

func TestDetectExplicit(t *testing.T) {
	os.Setenv("NTM_ICONS", "unicode")
	defer os.Unsetenv("NTM_ICONS")

	icons := Detect()
	if icons.Session != "◆" { // Unicode session
		t.Errorf("Expected Unicode, got session=%q", icons.Session)
	}
	assertNoEmptyIcons(t, icons)
	assertMaxIconWidth(t, icons, 2)

	os.Setenv("NTM_ICONS", "ascii")
	icons = Detect()
	if icons.Session != "[]" {
		t.Errorf("Expected ASCII, got session=%q", icons.Session)
	}
	assertNoEmptyIcons(t, icons)
}

func TestDetectAuto(t *testing.T) {
	os.Setenv("NTM_ICONS", "auto")
	defer os.Unsetenv("NTM_ICONS")
	os.Setenv("NTM_USE_ICONS", "0")
	os.Setenv("NERD_FONTS", "0")
	defer os.Unsetenv("NTM_USE_ICONS")
	defer os.Unsetenv("NERD_FONTS")

	// This depends on environment, but should return something valid
	icons := Detect()
	if icons.Session == "" {
		t.Error("Returned empty icons")
	}
	assertNoEmptyIcons(t, icons)
}

func TestWithFallbackFillsMissingIcons(t *testing.T) {
	out := NerdFonts.WithFallback(Unicode).WithFallback(ASCII)
	assertNoEmptyIcons(t, out)
	assertMaxIconWidth(t, out, 2)

	// A couple targeted sanity checks: NerdFonts has blanks that should be filled.
	if NerdFonts.Search == "" && out.Search == "" {
		t.Fatal("expected Search to be filled via fallback")
	}
	if NerdFonts.CodeQuality == "" && out.CodeQuality == "" {
		t.Fatal("expected CodeQuality to be filled via fallback")
	}
}

func TestWithFallback_IdenticalSets(t *testing.T) {
	// When fallback is identical, should return unmodified
	out := ASCII.WithFallback(ASCII)
	if !reflect.DeepEqual(out, ASCII) {
		t.Error("WithFallback of identical set should return same set")
	}
}

func TestWithFallback_FillsEmpty(t *testing.T) {
	// Create an IconSet with some empty fields
	partial := IconSet{
		Check: "Y",
		Cross: "N",
		// Everything else is empty
	}
	out := partial.WithFallback(ASCII)
	if out.Check != "Y" {
		t.Errorf("non-empty field should be preserved: Check = %q", out.Check)
	}
	if out.Cross != "N" {
		t.Errorf("non-empty field should be preserved: Cross = %q", out.Cross)
	}
	// Empty fields should be filled from fallback
	if out.Pointer != ">" {
		t.Errorf("empty field should be filled from fallback: Pointer = %q", out.Pointer)
	}
	if out.Session != "[]" {
		t.Errorf("empty field should be filled from fallback: Session = %q", out.Session)
	}
}

func TestAgentIcon(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentType string
		wantField string // Which IconSet field should be returned
	}{
		{"cc", "Claude"},
		{"claude", "Claude"},
		{"cod", "Codex"},
		{"codex", "Codex"},
		{"gmi", "Gemini"},
		{"gemini", "Gemini"},
		{"user", "Terminal"},
		{"unknown", "Robot"},
		{"", "Robot"},
		{"cursor", "Cursor"},
		{"windsurf", "Windsurf"},
		{"ws", "Windsurf"},
		{"aider", "Aider"},
		{"ollama", "Ollama"},
	}

	icons := ASCII
	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			t.Parallel()
			got := icons.AgentIcon(tt.agentType)
			if got == "" {
				t.Errorf("AgentIcon(%q) returned empty string", tt.agentType)
			}
			// Verify against the expected field
			v := reflect.ValueOf(icons)
			expected := v.FieldByName(tt.wantField).String()
			if got != expected {
				t.Errorf("AgentIcon(%q) = %q, want %q (field %s)", tt.agentType, got, expected, tt.wantField)
			}
		})
	}
}

func TestCategoryIcon(t *testing.T) {
	t.Parallel()

	tests := []struct {
		category  string
		wantField string
	}{
		{"quick actions", "Quick"},
		{"quick", "Quick"},
		{"Quick Actions", "Quick"},
		{"QUICK", "Quick"},
		{"code quality", "CodeQuality"},
		{"quality", "CodeQuality"},
		{"coordination", "Coordination"},
		{"coord", "Coordination"},
		{"investigation", "Investigation"},
		{"investigate", "Investigation"},
		{"unknown category", "General"},
		{"", "General"},
	}

	icons := ASCII
	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			t.Parallel()
			got := icons.CategoryIcon(tt.category)
			if got == "" {
				t.Errorf("CategoryIcon(%q) returned empty string", tt.category)
			}
			v := reflect.ValueOf(icons)
			expected := v.FieldByName(tt.wantField).String()
			if got != expected {
				t.Errorf("CategoryIcon(%q) = %q, want %q (field %s)", tt.category, got, expected, tt.wantField)
			}
		})
	}
}

func TestStatusIcon(t *testing.T) {
	t.Parallel()

	icons := ASCII
	if got := icons.StatusIcon(true); got != icons.Check {
		t.Errorf("StatusIcon(true) = %q, want %q", got, icons.Check)
	}
	if got := icons.StatusIcon(false); got != icons.Cross {
		t.Errorf("StatusIcon(false) = %q, want %q", got, icons.Cross)
	}
}

func TestIsASCII(t *testing.T) {
	// Save and restore default
	saved := Default
	defer func() { Default = saved }()

	SetDefault(ASCII)
	if !IsASCII() {
		t.Error("IsASCII() should be true when Default is ASCII")
	}

	SetDefault(Unicode)
	if IsASCII() {
		t.Error("IsASCII() should be false when Default is Unicode")
	}
}

func TestSetDefaultAndCurrent(t *testing.T) {
	saved := Default
	defer func() { Default = saved }()

	SetDefault(Unicode)
	got := Current()
	if !reflect.DeepEqual(got, Unicode) {
		t.Error("Current() should return Unicode after SetDefault(Unicode)")
	}

	SetDefault(ASCII)
	got = Current()
	if !reflect.DeepEqual(got, ASCII) {
		t.Error("Current() should return ASCII after SetDefault(ASCII)")
	}
}

func TestDetectNerdFonts(t *testing.T) {
	// Save and restore env
	for _, key := range []string{"NTM_ICONS", "NTM_USE_ICONS", "NERD_FONTS"} {
		old := os.Getenv(key)
		defer os.Setenv(key, old)
	}

	os.Setenv("NTM_ICONS", "nerd")
	os.Unsetenv("NTM_USE_ICONS")
	os.Unsetenv("NERD_FONTS")

	icons := Detect()
	assertNoEmptyIcons(t, icons)
	// Nerd Fonts should use the NerdFonts Claude icon
	if icons.Claude != NerdFonts.Claude {
		t.Errorf("Expected NerdFonts.Claude %q, got %q", NerdFonts.Claude, icons.Claude)
	}
}

func TestDetectLegacyEnvVar(t *testing.T) {
	for _, key := range []string{"NTM_ICONS", "NTM_USE_ICONS", "NERD_FONTS"} {
		old := os.Getenv(key)
		defer os.Setenv(key, old)
	}

	os.Unsetenv("NTM_ICONS")
	os.Setenv("NTM_USE_ICONS", "1")
	os.Unsetenv("NERD_FONTS")

	icons := Detect()
	assertNoEmptyIcons(t, icons)
	// Legacy NTM_USE_ICONS=1 should return NerdFonts
	if icons.Claude != NerdFonts.Claude {
		t.Errorf("Expected NerdFonts with legacy env, got Claude=%q", icons.Claude)
	}
}

func TestAllIconSetsComplete(t *testing.T) {
	t.Parallel()

	// Verify all three built-in icon sets have no empty fields
	assertNoEmptyIcons(t, NerdFonts.WithFallback(Unicode).WithFallback(ASCII))
	assertNoEmptyIcons(t, Unicode.WithFallback(ASCII))
	assertNoEmptyIcons(t, ASCII)
}

func TestIconSetsMaxWidth(t *testing.T) {
	t.Parallel()

	// All icons should be <= 3 columns wide (most are 1-2)
	assertMaxIconWidth(t, NerdFonts.WithFallback(Unicode).WithFallback(ASCII), 7)
	assertMaxIconWidth(t, Unicode.WithFallback(ASCII), 3)
	assertMaxIconWidth(t, ASCII, 7)
}
