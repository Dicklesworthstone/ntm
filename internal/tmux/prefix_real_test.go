//go:build integration

package tmux

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Prefix-collision regression tests (bd-pcssq)
//
// Field evidence (ts2, 2026-08-01): spawning `midas_edge` while
// `midas_edge_api` existed prefix-matched the bare `-t midas_edge` targets and
// landed the new panes INSIDE midas_edge_api with cross-wired titles. All
// session-name `-t` targets must therefore be exact-matched via
// TargetSession/ExactTarget ("=" sigil).
//
// Run with: go test -tags=integration -run TestRealPrefix ./internal/tmux/
// =============================================================================

func TestRealPrefixCollisionSessionTargets(t *testing.T) {
	skipIfNoTmux(t)

	base := uniqueSessionName("pfx")
	longer := base + "_api"
	dir := t.TempDir()

	t.Cleanup(func() {
		cleanupSession(t, longer)
		cleanupSession(t, base)
	})

	// Only the longer session exists.
	if err := CreateSession(longer, dir); err != nil {
		t.Fatalf("CreateSession(%s): %v", longer, err)
	}
	time.Sleep(100 * time.Millisecond)

	// has-session for the shorter name must NOT prefix-match the longer one.
	if SessionExists(base) {
		t.Fatalf("SessionExists(%q) = true: prefix-matched %q", base, longer)
	}
	// And a nonexistent prefix of the longer session must not match either
	// (the "fo" vs "foo_bar" case).
	if SessionExists(longer[:len(longer)-3]) {
		t.Fatalf("SessionExists(%q) = true: prefix-matched %q", longer[:len(longer)-3], longer)
	}

	// SplitWindow against the short name must fail, not land a pane in longer.
	longerPanesBefore, err := GetPanes(longer)
	if err != nil {
		t.Fatalf("GetPanes(%s): %v", longer, err)
	}
	if _, err := DefaultClient.SplitWindow(base, dir); err == nil {
		t.Fatalf("SplitWindow(%q) succeeded with only %q present: pane landed in the wrong session", base, longer)
	}
	longerPanesAfter, err := GetPanes(longer)
	if err != nil {
		t.Fatalf("GetPanes(%s): %v", longer, err)
	}
	if len(longerPanesAfter) != len(longerPanesBefore) {
		t.Fatalf("SplitWindow(%q) grew %q from %d to %d panes", base, longer, len(longerPanesBefore), len(longerPanesAfter))
	}

	// KillSession for the short name must not tear down the longer session.
	if err := KillSession(base); err == nil {
		t.Errorf("KillSession(%q) succeeded with only %q present", base, longer)
	}
	if !SessionExists(longer) {
		t.Fatalf("KillSession(%q) killed %q", base, longer)
	}

	// Now create the short session too and verify targeting is precise.
	if err := CreateSession(base, dir); err != nil {
		t.Fatalf("CreateSession(%s): %v", base, err)
	}
	time.Sleep(100 * time.Millisecond)
	if !SessionExists(base) {
		t.Fatal("short session should exist after creation")
	}

	// Panes split against the short session must land in the short session.
	paneID, err := DefaultClient.SplitWindow(base, dir)
	if err != nil {
		t.Fatalf("SplitWindow(%s): %v", base, err)
	}
	owner, err := DefaultClient.Run("display-message", "-p", "-t", ExactTarget(paneID), "#{session_name}")
	if err != nil {
		t.Fatalf("display-message for %s: %v", paneID, err)
	}
	if strings.TrimSpace(owner) != base {
		t.Fatalf("SplitWindow(%q) pane %s landed in session %q", base, paneID, owner)
	}
	if got, _ := GetPanes(longer); len(got) != len(longerPanesBefore) {
		t.Fatalf("SplitWindow(%q) modified %q pane count %d -> %d", base, longer, len(longerPanesBefore), len(got))
	}

	// Title set on the short session's pane must not touch the longer session.
	longerPaneID := longerPanesBefore[0].ID
	longerTitleBefore, err := DefaultClient.GetPaneTitle(longerPaneID)
	if err != nil {
		t.Fatalf("GetPaneTitle(%s): %v", longerPaneID, err)
	}
	if err := DefaultClient.SetPaneTitle(paneID, "pfx_regression_title"); err != nil {
		t.Fatalf("SetPaneTitle: %v", err)
	}
	if titleAfter, err := DefaultClient.GetPaneTitle(longerPaneID); err == nil && titleAfter != longerTitleBefore {
		t.Fatalf("SetPaneTitle on %q cross-wired title of %q pane: %q -> %q", base, longer, longerTitleBefore, titleAfter)
	}

	// send-keys against the short session must not reach the longer session.
	marker := fmt.Sprintf("pfx_marker_%d", time.Now().UnixNano())
	firstWin, err := GetFirstWindow(base)
	if err != nil {
		t.Fatalf("GetFirstWindow(%s): %v", base, err)
	}
	target := fmt.Sprintf("%s:%d.0", base, firstWin)
	if err := DefaultClient.SendKeys(target, "echo "+marker, true); err != nil {
		t.Fatalf("SendKeys(%s): %v", target, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	seenInBase := false
	for time.Now().Before(deadline) {
		out, err := DefaultClient.CapturePaneOutput(target, 200)
		if err == nil && strings.Contains(out, marker) {
			seenInBase = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !seenInBase {
		t.Fatalf("marker never appeared in %s", target)
	}
	longerOut, err := DefaultClient.CapturePaneOutput(longerPaneID, 200)
	if err != nil {
		t.Fatalf("CapturePaneOutput(%s): %v", longerPaneID, err)
	}
	if strings.Contains(longerOut, marker) {
		t.Fatalf("send-keys marker leaked into %q", longer)
	}

	// Killing the short session must leave the longer one alive.
	if err := KillSession(base); err != nil {
		t.Fatalf("KillSession(%s): %v", base, err)
	}
	if !SessionExists(longer) {
		t.Fatalf("KillSession(%q) killed %q", base, longer)
	}
	if SessionExists(base) {
		t.Fatal("short session should be gone after kill")
	}
}
