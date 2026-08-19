package robot

import (
	"testing"
)

// =============================================================================
// Unit tests for pane activity indicators (bd-3v1w7)
// =============================================================================

func TestHashContent_Deterministic(t *testing.T) {
	h1 := hashContent("hello world")
	h2 := hashContent("hello world")
	if h1 != h2 {
		t.Errorf("hashContent not deterministic: %s != %s", h1, h2)
	}
}

func TestHashContent_Different(t *testing.T) {
	h1 := hashContent("hello")
	h2 := hashContent("world")
	if h1 == h2 {
		t.Error("hashContent produced same hash for different inputs")
	}
}

func TestHashContent_Empty(t *testing.T) {
	h := hashContent("")
	if h == "" {
		t.Error("hashContent returned empty string for empty input")
	}
}

func TestActivityStatus_StringValues(t *testing.T) {
	if string(StatusActive) != "active" {
		t.Errorf("StatusActive = %q, want 'active'", StatusActive)
	}
	if string(StatusIdle) != "idle" {
		t.Errorf("StatusIdle = %q, want 'idle'", StatusIdle)
	}
	if string(StatusStalled) != "stalled" {
		t.Errorf("StatusStalled = %q, want 'stalled'", StatusStalled)
	}
}

func TestColorConstants(t *testing.T) {
	if ColorActive != "#00ff00" {
		t.Errorf("ColorActive = %q, want '#00ff00'", ColorActive)
	}
	if ColorIdle != "#ffff00" {
		t.Errorf("ColorIdle = %q, want '#ffff00'", ColorIdle)
	}
	if ColorStalled != "#ff0000" {
		t.Errorf("ColorStalled = %q, want '#ff0000'", ColorStalled)
	}
}
