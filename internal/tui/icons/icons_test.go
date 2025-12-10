package icons

import (
	"os"
	"testing"
)

func TestDetectDefaults(t *testing.T) {
	// Clear env vars
	os.Unsetenv("NTM_ICONS")
	os.Unsetenv("NTM_USE_ICONS")
	os.Unsetenv("NERD_FONTS")
	
	// Should default to ASCII
	icons := Detect()
	if icons.Check != "[x]" { // ASCII check
		t.Errorf("Expected ASCII default, got check=%q", icons.Check)
	}
}

func TestDetectExplicit(t *testing.T) {
	os.Setenv("NTM_ICONS", "unicode")
	defer os.Unsetenv("NTM_ICONS")
	
	icons := Detect()
	if icons.Check != "✓" { // Unicode check
		t.Errorf("Expected Unicode, got check=%q", icons.Check)
	}
	
	os.Setenv("NTM_ICONS", "ascii")
	icons = Detect()
	if icons.Check != "[x]" {
		t.Errorf("Expected ASCII, got check=%q", icons.Check)
	}
}

func TestDetectAuto(t *testing.T) {
	os.Setenv("NTM_ICONS", "auto")
	defer os.Unsetenv("NTM_ICONS")
	
	// This depends on environment, but should return something valid
	icons := Detect()
	if icons.Check == "" {
		t.Error("Returned empty icons")
	}
}