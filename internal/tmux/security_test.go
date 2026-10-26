package tmux

import (
	"fmt"
	"strings"
	"testing"
)

func TestBuildPaneCommandRejectsUnsafeWorkingDirectory(t *testing.T) {
	for _, directory := range []string{"/work\nprivate-value", "/work\tprivate-value", "/work\x7fprivate-value", "/work\u0085private-value"} {
		command, err := BuildPaneCommand(directory, "codex\t--model retained-model")
		if err == nil || command != "" || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("working directory did not fail with a redacted error: %v", err)
		}
	}
	if _, err := BuildPaneCommand("/work/safe directory", "codex\t--model retained-model"); err != nil {
		t.Fatalf("command tabs no longer work: %v", err)
	}
}

func TestBuildPaneCommand_Security(t *testing.T) {
	// Test directory with characters that are dangerous in double quotes but safe in single quotes
	dir := "$HOME"
	cmd := "ls"

	// Go's %q produces "$HOME" which allows shell expansion
	// We want '$HOME' which prevents expansion

	// Check current behavior (vulnerable) or fixed behavior
	got, err := BuildPaneCommand(dir, cmd)
	if err != nil {
		t.Fatalf("BuildPaneCommand failed: %v", err)
	}

	// We expect single quotes for maximum safety
	expected := fmt.Sprintf("cd '%s' && %s", dir, cmd)
	if got != expected {
		// If it's currently using %q, it will likely be: cd "$HOME" && ls
		t.Logf("Got: %s", got)
		t.Logf("Expected: %s", expected)

		// This assertion will fail on the current codebase, confirming the "vulnerability" (or lack of strict quoting)
		// We want to verify it fails first, then fix it.
		// For now, I'll fail if it matches the unsafe pattern
		if got == fmt.Sprintf("cd %q && %s", dir, cmd) {
			t.Errorf("BuildPaneCommand is using unsafe double quotes: %s", got)
		} else if got != expected {
			// It might be something else entirely
			t.Errorf("BuildPaneCommand returned unexpected format. Got: %s, Want: %s", got, expected)
		}
	}
}
