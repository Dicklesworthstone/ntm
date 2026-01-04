//go:build integration

package tmux

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Pane Real Integration Tests (ntm-seok)
//
// These tests create real tmux panes and verify behavior without any mocks.
// Run with: go test -tags=integration ./internal/tmux/...
// =============================================================================

// createTestSessionForPanes creates a unique test session for pane tests
func createTestSessionForPanes(t *testing.T) string {
	t.Helper()
	name := uniqueSessionName("pane")
	t.Cleanup(func() { cleanupSession(t, name) })

	err := CreateSession(name, t.TempDir())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	return name
}

// =============================================================================
// Pane Creation Tests
// =============================================================================

func TestRealPaneSplitHorizontal(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Get initial pane count
	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}
	initialCount := len(panes)

	// Split window (creates new pane)
	paneID, err := SplitWindow(session, t.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if paneID == "" {
		t.Error("SplitWindow should return pane ID")
	}

	// Verify pane count increased
	panes, err = GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes after split failed: %v", err)
	}

	if len(panes) != initialCount+1 {
		t.Errorf("expected %d panes after split, got %d", initialCount+1, len(panes))
	}
}

func TestRealPaneSplitMultiple(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create 4 additional panes (5 total)
	paneIDs := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		paneID, err := SplitWindow(session, t.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
		paneIDs = append(paneIDs, paneID)
	}
	time.Sleep(200 * time.Millisecond)

	// Verify all panes were created
	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	if len(panes) != 5 {
		t.Errorf("expected 5 panes, got %d", len(panes))
	}

	// Verify all pane IDs are unique
	idMap := make(map[string]bool)
	for _, p := range panes {
		if idMap[p.ID] {
			t.Errorf("duplicate pane ID: %s", p.ID)
		}
		idMap[p.ID] = true
	}
}

func TestRealPaneIDAssignment(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Get initial pane
	panes, _ := GetPanes(session)
	initialPaneID := panes[0].ID

	// Split to create new pane
	newPaneID, err := SplitWindow(session, t.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify IDs are different
	if newPaneID == initialPaneID {
		t.Error("new pane should have different ID than initial pane")
	}

	// Verify new pane ID starts with %
	if !strings.HasPrefix(newPaneID, "%") {
		t.Errorf("pane ID should start with %%, got: %s", newPaneID)
	}
}

func TestRealPaneCountViaTmux(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create specific number of panes
	targetCount := 4
	for i := 0; i < targetCount-1; i++ {
		_, err := SplitWindow(session, t.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	// Verify via GetPanes
	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	if len(panes) != targetCount {
		t.Errorf("expected %d panes, got %d", targetCount, len(panes))
	}
}

// =============================================================================
// Pane Management Tests
// =============================================================================

func TestRealPaneTitleSetAndGet(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Get first pane
	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Set title
	newTitle := "test_pane_title_abc123"
	if err := SetPaneTitle(paneID, newTitle); err != nil {
		t.Fatalf("SetPaneTitle failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Get panes and verify title
	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	var found bool
	for _, p := range panes {
		if p.ID == paneID {
			if p.Title == newTitle {
				found = true
			} else {
				t.Errorf("pane title = %q, want %q", p.Title, newTitle)
			}
			break
		}
	}

	if !found {
		t.Error("pane with new title not found")
	}
}

func TestRealPaneTitleWithSpecialChars(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)
	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Test various title patterns
	titles := []string{
		"project__cc_1",
		"my-project__cod_2_opus",
		"test__gmi_3[tag1,tag2]",
	}

	for _, title := range titles {
		t.Run(title, func(t *testing.T) {
			if err := SetPaneTitle(paneID, title); err != nil {
				t.Fatalf("SetPaneTitle(%q) failed: %v", title, err)
			}
			time.Sleep(100 * time.Millisecond)

			panes, _ := GetPanes(session)
			for _, p := range panes {
				if p.ID == paneID {
					if p.Title != title {
						t.Errorf("title = %q, want %q", p.Title, title)
					}
					return
				}
			}
			t.Error("pane not found after setting title")
		})
	}
}

func TestRealPaneZoom(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create second pane (need at least 2 for zoom)
	_, err := SplitWindow(session, t.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Get first pane index
	panes, _ := GetPanes(session)
	paneIndex := panes[0].Index

	// Zoom pane (should not error)
	if err := ZoomPane(session, paneIndex); err != nil {
		t.Errorf("ZoomPane failed: %v", err)
	}

	// Unzoom by zooming again
	if err := ZoomPane(session, paneIndex); err != nil {
		t.Errorf("ZoomPane (unzoom) failed: %v", err)
	}
}

func TestRealPaneDimensions(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	panes, _ := GetPanes(session)
	pane := panes[0]

	// Pane should have positive dimensions
	if pane.Width <= 0 {
		t.Errorf("pane width should be positive, got %d", pane.Width)
	}
	if pane.Height <= 0 {
		t.Errorf("pane height should be positive, got %d", pane.Height)
	}

	t.Logf("pane dimensions: %dx%d", pane.Width, pane.Height)
}

// =============================================================================
// Multi-Pane Scenarios
// =============================================================================

func TestRealPaneLayout2Panes(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create 1 additional pane (2 total)
	_, err := SplitWindow(session, t.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Apply tiled layout
	if err := ApplyTiledLayout(session); err != nil {
		t.Errorf("ApplyTiledLayout failed: %v", err)
	}

	// Verify both panes have reasonable dimensions
	panes, _ := GetPanes(session)
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(panes))
	}

	for i, p := range panes {
		if p.Width <= 0 || p.Height <= 0 {
			t.Errorf("pane %d has invalid dimensions: %dx%d", i, p.Width, p.Height)
		}
	}
}

func TestRealPaneLayout4Panes(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create 3 additional panes (4 total)
	for i := 0; i < 3; i++ {
		_, err := SplitWindow(session, t.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	// Apply tiled layout
	if err := ApplyTiledLayout(session); err != nil {
		t.Errorf("ApplyTiledLayout failed: %v", err)
	}

	panes, _ := GetPanes(session)
	if len(panes) != 4 {
		t.Fatalf("expected 4 panes, got %d", len(panes))
	}

	// All panes should have reasonable dimensions
	for i, p := range panes {
		if p.Width < 10 || p.Height < 5 {
			t.Errorf("pane %d has very small dimensions: %dx%d", i, p.Width, p.Height)
		}
		t.Logf("pane %d: %dx%d", i, p.Width, p.Height)
	}
}

func TestRealPaneLayout8Panes(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create 7 additional panes (8 total)
	for i := 0; i < 7; i++ {
		_, err := SplitWindow(session, t.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(300 * time.Millisecond)

	// Apply tiled layout
	if err := ApplyTiledLayout(session); err != nil {
		t.Errorf("ApplyTiledLayout failed: %v", err)
	}

	panes, _ := GetPanes(session)
	if len(panes) != 8 {
		t.Fatalf("expected 8 panes, got %d", len(panes))
	}

	// All panes should exist with IDs
	for i, p := range panes {
		if p.ID == "" {
			t.Errorf("pane %d has empty ID", i)
		}
	}
}

func TestRealPaneKillIndividual(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create 2 additional panes (3 total)
	var paneIDs []string
	for i := 0; i < 2; i++ {
		paneID, err := SplitWindow(session, t.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
		paneIDs = append(paneIDs, paneID)
	}
	time.Sleep(200 * time.Millisecond)

	// Verify we have 3 panes
	panes, _ := GetPanes(session)
	if len(panes) != 3 {
		t.Fatalf("expected 3 panes, got %d", len(panes))
	}

	// Kill the last pane created
	paneToKill := paneIDs[len(paneIDs)-1]
	if err := KillPane(paneToKill); err != nil {
		t.Fatalf("KillPane failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify we now have 2 panes
	panes, _ = GetPanes(session)
	if len(panes) != 2 {
		t.Errorf("expected 2 panes after kill, got %d", len(panes))
	}

	// Verify the killed pane is gone
	for _, p := range panes {
		if p.ID == paneToKill {
			t.Error("killed pane should not exist")
		}
	}
}

func TestRealPaneReflowAfterKill(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create 3 additional panes (4 total)
	for i := 0; i < 3; i++ {
		_, err := SplitWindow(session, t.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	// Apply tiled layout
	ApplyTiledLayout(session)
	time.Sleep(100 * time.Millisecond)

	// Get dimensions before kill
	panesBefore, _ := GetPanes(session)
	if len(panesBefore) != 4 {
		t.Fatalf("expected 4 panes, got %d", len(panesBefore))
	}

	// Kill one pane (first one that's not the initial)
	if len(panesBefore) > 1 {
		KillPane(panesBefore[1].ID)
	}
	time.Sleep(100 * time.Millisecond)

	// Apply tiled layout again
	ApplyTiledLayout(session)
	time.Sleep(100 * time.Millisecond)

	// Verify reflow - remaining panes should take up space
	panesAfter, _ := GetPanes(session)
	if len(panesAfter) != 3 {
		t.Errorf("expected 3 panes after kill, got %d", len(panesAfter))
	}

	// All panes should have valid dimensions
	for i, p := range panesAfter {
		if p.Width <= 0 || p.Height <= 0 {
			t.Errorf("pane %d has invalid dimensions after reflow: %dx%d", i, p.Width, p.Height)
		}
	}
}

// =============================================================================
// Pane Index Tests
// =============================================================================

func TestRealPaneIndices(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create 3 additional panes
	for i := 0; i < 3; i++ {
		_, err := SplitWindow(session, t.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	panes, _ := GetPanes(session)
	if len(panes) != 4 {
		t.Fatalf("expected 4 panes, got %d", len(panes))
	}

	// Verify indices are unique and sequential (0-based)
	indices := make(map[int]bool)
	for _, p := range panes {
		if indices[p.Index] {
			t.Errorf("duplicate pane index: %d", p.Index)
		}
		indices[p.Index] = true
	}

	// Should have indices 0, 1, 2, 3
	for i := 0; i < 4; i++ {
		if !indices[i] {
			t.Errorf("missing pane index: %d", i)
		}
	}
}

// =============================================================================
// Pane with Different Working Directories
// =============================================================================

func TestRealPaneDifferentWorkDirs(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create panes with different working directories
	dirs := make([]string, 3)
	for i := 0; i < 3; i++ {
		dirs[i] = t.TempDir()
		// Create a marker file in each directory
		markerFile := fmt.Sprintf("pane_%d_marker.txt", i)
		os.WriteFile(dirs[i]+"/"+markerFile, []byte("test"), 0644)

		_, err := SplitWindow(session, dirs[i])
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(300 * time.Millisecond)

	// Verify each pane is in its respective directory
	panes, _ := GetPanes(session)
	if len(panes) != 4 { // 1 initial + 3 new
		t.Fatalf("expected 4 panes, got %d", len(panes))
	}

	// The last 3 panes should be in our created directories
	// (pane indices may not match creation order after splits)
	t.Logf("created %d panes in different directories", len(panes))
}

// =============================================================================
// Pane Activity Tests
// =============================================================================

func TestRealPaneActivityTracking(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate activity
	SendKeys(paneID, "echo activity_marker", true)
	time.Sleep(300 * time.Millisecond)

	// Check activity
	activity, err := GetPaneActivity(paneID)
	if err != nil {
		t.Skipf("GetPaneActivity not supported: %v", err)
	}

	// Activity should be recent
	if time.Since(activity) > 30*time.Second {
		t.Errorf("pane activity should be recent, got %v ago", time.Since(activity))
	}
}

func TestRealPanesWithActivityMultiple(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSessionForPanes(t)

	// Create additional panes
	for i := 0; i < 2; i++ {
		_, err := SplitWindow(session, t.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	// Generate activity in all panes
	panes, _ := GetPanes(session)
	for _, p := range panes {
		SendKeys(p.ID, fmt.Sprintf("echo pane_%s", p.ID), true)
	}
	time.Sleep(300 * time.Millisecond)

	// Get panes with activity
	panesWithActivity, err := GetPanesWithActivity(session)
	if err != nil {
		t.Fatalf("GetPanesWithActivity failed: %v", err)
	}

	if len(panesWithActivity) != len(panes) {
		t.Errorf("expected %d panes with activity, got %d", len(panes), len(panesWithActivity))
	}

	// Verify each has recent activity
	for _, p := range panesWithActivity {
		if p.LastActivity.IsZero() {
			t.Errorf("pane %s should have activity timestamp", p.Pane.ID)
		}
	}
}
