package dashboard

import (
	"testing"

	"github.com/charmbracelet/bubbles/help"
)

// TestKeyMapImplementsHelpKeyMap verifies KeyMap implements help.KeyMap interface.
func TestKeyMapImplementsHelpKeyMap(t *testing.T) {
	var _ help.KeyMap = dashKeys
	t.Log("KeyMap implements help.KeyMap interface")
}

// TestKeyMapShortHelp verifies ShortHelp returns expected bindings.
func TestKeyMapShortHelp(t *testing.T) {
	bindings := dashKeys.ShortHelp()

	if len(bindings) == 0 {
		t.Fatal("ShortHelp() returned empty slice")
	}

	// Should include essential bindings
	wantKeys := []string{"?", "q", "tab", "z", "r"}
	for i, want := range wantKeys {
		if i >= len(bindings) {
			t.Errorf("ShortHelp() missing binding at index %d (want key containing %q)", i, want)
			continue
		}
		keys := bindings[i].Keys()
		found := false
		for _, k := range keys {
			if k == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ShortHelp()[%d] = %v, want key containing %q", i, keys, want)
		}
	}

	t.Logf("ShortHelp() returned %d bindings", len(bindings))
}

// TestKeyMapFullHelp verifies FullHelp returns grouped bindings.
func TestKeyMapFullHelp(t *testing.T) {
	groups := dashKeys.FullHelp()

	if len(groups) == 0 {
		t.Fatal("FullHelp() returned empty slice")
	}

	// Should have multiple groups
	if len(groups) < 3 {
		t.Errorf("FullHelp() returned %d groups, want at least 3", len(groups))
	}

	// Each group should have bindings
	for i, group := range groups {
		if len(group) == 0 {
			t.Errorf("FullHelp()[%d] is empty", i)
		}
		t.Logf("FullHelp()[%d] has %d bindings", i, len(group))
	}

	// Verify navigation group (first) contains up/down/zoom
	navGroup := groups[0]
	navKeySet := make(map[string]bool)
	for _, b := range navGroup {
		for _, k := range b.Keys() {
			navKeySet[k] = true
		}
	}
	for _, want := range []string{"up", "down", "z"} {
		if !navKeySet[want] {
			t.Errorf("FullHelp() navigation group missing %q key", want)
		}
	}

	foundPaneView := false
	for _, group := range groups {
		for _, binding := range group {
			for _, keyName := range binding.Keys() {
				if keyName == "v" {
					foundPaneView = true
				}
			}
		}
	}
	if !foundPaneView {
		t.Fatal("FullHelp() missing pane table toggle key 'v'")
	}

	for _, group := range groups {
		for _, binding := range group {
			for _, keyName := range binding.Keys() {
				if keyName == "s" {
					t.Fatal("FullHelp() should not advertise dead send binding 's'")
				}
				if keyName == "left" || keyName == "right" || keyName == "h" || keyName == "l" {
					t.Fatal("FullHelp() should not advertise dead global left/right bindings")
				}
			}
		}
	}

	tabCount := 0
	shiftTabCount := 0
	for _, group := range groups {
		for _, binding := range group {
			for _, keyName := range binding.Keys() {
				if keyName == "tab" {
					tabCount++
				}
				if keyName == "shift+tab" {
					shiftTabCount++
				}
			}
		}
	}
	if tabCount != 1 {
		t.Fatalf("FullHelp() should advertise tab exactly once, got %d occurrences", tabCount)
	}
	if shiftTabCount != 1 {
		t.Fatalf("FullHelp() should advertise shift+tab exactly once, got %d occurrences", shiftTabCount)
	}
}

// TestKeyMapBindingsHaveHelp verifies all returned bindings have help text.
func TestKeyMapBindingsHaveHelp(t *testing.T) {
	// Check ShortHelp bindings
	for i, b := range dashKeys.ShortHelp() {
		help := b.Help()
		if help.Key == "" || help.Desc == "" {
			t.Errorf("ShortHelp()[%d] missing help text: key=%q desc=%q", i, help.Key, help.Desc)
		}
	}

	// Check FullHelp bindings
	for gi, group := range dashKeys.FullHelp() {
		for bi, b := range group {
			help := b.Help()
			if help.Key == "" || help.Desc == "" {
				t.Errorf("FullHelp()[%d][%d] missing help text: key=%q desc=%q", gi, bi, help.Key, help.Desc)
			}
		}
	}
}

// BenchmarkKeyMapShortHelp benchmarks ShortHelp allocation.
func BenchmarkKeyMapShortHelp(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = dashKeys.ShortHelp()
	}
}

// BenchmarkKeyMapFullHelp benchmarks FullHelp allocation.
func BenchmarkKeyMapFullHelp(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = dashKeys.FullHelp()
	}
}
