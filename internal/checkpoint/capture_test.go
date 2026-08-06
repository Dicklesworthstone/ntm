package checkpoint

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// =============================================================================
// Capturer.FindByPattern (bd-9czd7)
// =============================================================================

func TestCapturer_FindByPattern(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "find-session"

	// Save 3 checkpoints with known IDs and names
	checkpoints := []struct {
		id   string
		name string
	}{
		{"20260101-100000-0001-alpha", "alpha"},
		{"20260101-110000-0002-beta-release", "beta-release"},
		{"20260101-120000-0003-alpha-final", "alpha-final"},
	}
	for _, c := range checkpoints {
		cp := &Checkpoint{
			ID: c.id, Name: c.name, SessionName: session,
			CreatedAt: time.Now(), Session: SessionState{},
		}
		if err := storage.Save(cp); err != nil {
			t.Fatalf("Save(%s): %v", c.id, err)
		}
	}

	tests := []struct {
		name    string
		pattern string
		want    int
	}{
		{"exact name match", "alpha", 1},
		{"case insensitive name", "ALPHA", 1},
		{"id prefix", "20260101-100000", 1},
		{"wildcard name", "alpha*", 2},     // alpha, alpha-final
		{"wildcard suffix", "*release", 1}, // beta-release
		{"no match", "nonexistent", 0},
		{"all wildcard", "*", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			matches, err := capturer.FindByPattern(session, tt.pattern)
			if err != nil {
				t.Fatalf("FindByPattern(%q): %v", tt.pattern, err)
			}
			if len(matches) != tt.want {
				t.Errorf("FindByPattern(%q) returned %d matches, want %d", tt.pattern, len(matches), tt.want)
			}
		})
	}
}

func TestCapturer_FindByPattern_NoSession(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)

	matches, err := capturer.FindByPattern("nonexistent", "anything")
	if err != nil {
		t.Fatalf("FindByPattern on nonexistent session: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(matches))
	}
}

// =============================================================================
// Capturer.List (bd-9czd7)
// =============================================================================

func TestCapturer_List(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "list-session"

	// Save 3 checkpoints with different times
	times := []time.Time{
		time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	}
	for i, ts := range times {
		cp := &Checkpoint{
			ID:   fmt.Sprintf("20260101-%02d0000-000%d-cp", ts.Hour(), i),
			Name: fmt.Sprintf("cp-%d", i), SessionName: session,
			CreatedAt: ts, Session: SessionState{},
		}
		if err := storage.Save(cp); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	list, err := capturer.List(session)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List returned %d, want 3", len(list))
	}
	// Verify newest first
	if !list[0].CreatedAt.After(list[1].CreatedAt) {
		t.Errorf("expected newest first: %v vs %v", list[0].CreatedAt, list[1].CreatedAt)
	}
}

func TestCapturer_List_Empty(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)

	list, err := capturer.List("no-session")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0, got %d", len(list))
	}
}

// =============================================================================
// Capturer.GetLatest (bd-9czd7)
// =============================================================================

func TestCapturer_GetLatest(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "latest-session"

	// Save 2 checkpoints
	cp1 := &Checkpoint{
		ID: "20260101-100000-0001-old", Name: "old", SessionName: session,
		CreatedAt: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), Session: SessionState{},
	}
	cp2 := &Checkpoint{
		ID: "20260101-120000-0002-new", Name: "new", SessionName: session,
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Session: SessionState{},
	}
	storage.Save(cp1)
	storage.Save(cp2)

	latest, err := capturer.GetLatest(session)
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if latest.Name != "new" {
		t.Errorf("GetLatest().Name = %q, want new", latest.Name)
	}
}

func TestCapturer_GetLatest_NoCheckpoints(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)

	_, err := capturer.GetLatest("empty-session")
	if err == nil {
		t.Error("GetLatest should error with no checkpoints")
	}
}

// =============================================================================
// Capturer.GetByIndex (bd-9czd7)
// =============================================================================

func TestCapturer_GetByIndex(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "idx-session"

	names := []string{"oldest", "middle", "newest"}
	for i, name := range names {
		cp := &Checkpoint{
			ID:   fmt.Sprintf("20260101-%02d0000-000%d-%s", 10+i, i, name),
			Name: name, SessionName: session,
			CreatedAt: time.Date(2026, 1, 1, 10+i, 0, 0, 0, time.UTC),
			Session:   SessionState{},
		}
		storage.Save(cp)
	}

	tests := []struct {
		index    int
		wantName string
		wantErr  bool
	}{
		{1, "newest", false}, // 1-indexed, newest first
		{2, "middle", false},
		{3, "oldest", false},
		{0, "", true},  // out of range
		{4, "", true},  // out of range
		{-1, "", true}, // negative
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("index_%d", tt.index), func(t *testing.T) {
			cp, err := capturer.GetByIndex(session, tt.index)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetByIndex(%d): %v", tt.index, err)
			}
			if cp.Name != tt.wantName {
				t.Errorf("GetByIndex(%d).Name = %q, want %q", tt.index, cp.Name, tt.wantName)
			}
		})
	}
}

func TestCapturer_GetByIndex_PrefersCreatedAtOverDirectoryModTime(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "idx-created-at-session"

	older := &Checkpoint{
		ID:          "20260101-100000-0001-older",
		Name:        "older",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	newer := &Checkpoint{
		ID:          "20260101-120000-0002-newer",
		Name:        "newer",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(older); err != nil {
		t.Fatalf("Save(older) failed: %v", err)
	}
	if err := storage.Save(newer); err != nil {
		t.Fatalf("Save(newer) failed: %v", err)
	}

	olderDir := storage.CheckpointDir(session, older.ID)
	newerDir := storage.CheckpointDir(session, newer.ID)
	olderModTime := time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC)
	newerModTime := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	if err := os.Chtimes(olderDir, olderModTime, olderModTime); err != nil {
		t.Fatalf("Chtimes(olderDir) failed: %v", err)
	}
	if err := os.Chtimes(newerDir, newerModTime, newerModTime); err != nil {
		t.Fatalf("Chtimes(newerDir) failed: %v", err)
	}

	cp, err := capturer.GetByIndex(session, 1)
	if err != nil {
		t.Fatalf("GetByIndex(1): %v", err)
	}
	if cp.ID != newer.ID {
		t.Fatalf("GetByIndex(1).ID = %q, want %q", cp.ID, newer.ID)
	}
}

func TestCapturer_GetByIndex_RejectsInvalidNewerCheckpoint(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "idx-invalid-session"

	valid := &Checkpoint{
		ID:          "20260101-100000-0001-valid",
		Name:        "valid",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(valid) failed: %v", err)
	}
	validDir := storage.CheckpointDir(session, valid.ID)
	validTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(validDir) failed: %v", err)
	}

	invalidDir := storage.CheckpointDir(session, "20260101-120000-0002-invalid")
	if err := os.MkdirAll(invalidDir, 0755); err != nil {
		t.Fatalf("MkdirAll(invalidDir) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, MetadataFile), []byte("not valid json"), 0600); err != nil {
		t.Fatalf("WriteFile(metadata) failed: %v", err)
	}
	newestTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, newestTime, newestTime); err != nil {
		t.Fatalf("Chtimes(invalidDir) failed: %v", err)
	}

	_, err := capturer.GetByIndex(session, 1)
	if err == nil {
		t.Fatal("GetByIndex(1) error = nil, want invalid newer checkpoint error")
	}
	if !strings.Contains(err.Error(), "checkpoint selection blocked by invalid checkpoint") {
		t.Fatalf("GetByIndex(1) error = %v, want invalid newer checkpoint context", err)
	}
}

func TestCapturer_captureSessionState_UsesSessionSelectedPaneAcrossWindows(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	sessionName := "cpcap-" + time.Now().Format("150405000000")
	workDir := t.TempDir()

	if err := tmux.CreateSession(sessionName, workDir); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	t.Cleanup(func() {
		if tmux.SessionExists(sessionName) {
			_ = tmux.KillSession(sessionName)
		}
	})

	if err := tmux.DefaultClient.RunSilent("new-window", "-d", "-t", sessionName, "-c", workDir); err != nil {
		t.Fatalf("new-window failed: %v", err)
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("len(panes) = %d, want 2", len(panes))
	}
	sort.Slice(panes, func(i, j int) bool {
		if panes[i].WindowIndex != panes[j].WindowIndex {
			return panes[i].WindowIndex < panes[j].WindowIndex
		}
		return panes[i].Index < panes[j].Index
	})

	if err := tmux.DefaultClient.RunSilent("select-pane", "-t", panes[0].ID); err != nil {
		t.Fatalf("select-pane failed: %v", err)
	}

	state, err := capturer.captureSessionState(sessionName)
	if err != nil {
		t.Fatalf("captureSessionState failed: %v", err)
	}
	if len(state.Panes) != 2 {
		t.Fatalf("len(state.Panes) = %d, want 2", len(state.Panes))
	}

	selectedPaneID, err := tmux.DefaultClient.Run("display-message", "-p", "-t", sessionName, "#{pane_id}")
	if err != nil {
		t.Fatalf("display-message pane_id failed: %v", err)
	}

	expectedIndex := -1
	for i := range state.Panes {
		if state.Panes[i].ID == selectedPaneID {
			expectedIndex = i
			break
		}
	}
	if expectedIndex < 0 {
		t.Fatalf("selected pane %q not found in captured panes %#v", selectedPaneID, state.Panes)
	}
	if state.ActivePaneIndex != expectedIndex {
		t.Fatalf("ActivePaneIndex = %d, want %d for selected pane %q", state.ActivePaneIndex, expectedIndex, selectedPaneID)
	}
}

func TestCapturer_captureSessionState_CapturesPerWindowLayouts(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	sessionName := "cplayout-" + time.Now().Format("150405000000")
	workDir := t.TempDir()

	if err := tmux.CreateSession(sessionName, workDir); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	t.Cleanup(func() {
		if tmux.SessionExists(sessionName) {
			_ = tmux.KillSession(sessionName)
		}
	})

	firstWindow, err := tmux.GetFirstWindow(sessionName)
	if err != nil {
		t.Fatalf("GetFirstWindow failed: %v", err)
	}
	firstTarget := fmt.Sprintf("%s:%d", sessionName, firstWindow)
	if _, err := tmux.DefaultClient.Run("split-window", "-t", firstTarget, "-c", workDir, "-P", "-F", "#{pane_id}"); err != nil {
		t.Fatalf("split-window first window failed: %v", err)
	}
	if err := tmux.DefaultClient.RunSilent("select-layout", "-t", firstTarget, "even-horizontal"); err != nil {
		t.Fatalf("select-layout first window failed: %v", err)
	}

	secondWindowIndex, err := tmux.DefaultClient.Run("new-window", "-d", "-t", sessionName, "-c", workDir, "-P", "-F", "#{window_index}")
	if err != nil {
		t.Fatalf("new-window failed: %v", err)
	}
	secondTarget := fmt.Sprintf("%s:%s", sessionName, strings.TrimSpace(secondWindowIndex))
	if _, err := tmux.DefaultClient.Run("split-window", "-t", secondTarget, "-c", workDir, "-P", "-F", "#{pane_id}"); err != nil {
		t.Fatalf("split-window second window failed: %v", err)
	}
	if err := tmux.DefaultClient.RunSilent("select-layout", "-t", secondTarget, "main-vertical"); err != nil {
		t.Fatalf("select-layout second window failed: %v", err)
	}

	expectedLayouts, err := getSessionWindowLayouts(sessionName)
	if err != nil {
		t.Fatalf("getSessionWindowLayouts failed: %v", err)
	}
	if len(expectedLayouts) != 2 {
		t.Fatalf("len(expectedLayouts) = %d, want 2", len(expectedLayouts))
	}

	state, err := capturer.captureSessionState(sessionName)
	if err != nil {
		t.Fatalf("captureSessionState failed: %v", err)
	}

	if state.Layout != "" {
		t.Fatalf("Layout = %q, want empty legacy layout for multi-window capture", state.Layout)
	}
	if !windowLayoutsEqual(state.WindowLayouts, expectedLayouts) {
		t.Fatalf("WindowLayouts = %#v, want %#v", state.WindowLayouts, expectedLayouts)
	}
}

func TestCanUseLegacyLayoutFallback(t *testing.T) {
	tests := []struct {
		name  string
		panes []PaneState
		want  bool
	}{
		{
			name: "single window allowed",
			panes: []PaneState{
				{WindowIndex: 2, Index: 0},
				{WindowIndex: 2, Index: 1},
			},
			want: true,
		},
		{
			name: "multi window rejected",
			panes: []PaneState{
				{WindowIndex: 0, Index: 0},
				{WindowIndex: 1, Index: 0},
			},
			want: false,
		},
		{
			name:  "no panes allowed",
			panes: nil,
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canUseLegacyLayoutFallback(tt.panes); got != tt.want {
				t.Fatalf("canUseLegacyLayoutFallback(%#v) = %v, want %v", tt.panes, got, tt.want)
			}
		})
	}
}

// =============================================================================
// Capturer.ParseCheckpointRef (bd-9czd7)
// =============================================================================

func TestCapturer_ParseCheckpointRef_Keywords(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "ref-session"

	cp := &Checkpoint{
		ID: "20260101-120000-0001-mycp", Name: "mycp", SessionName: session,
		CreatedAt: time.Now(), Session: SessionState{},
	}
	storage.Save(cp)

	keywords := []string{"last", "latest", "~1", "~", "LAST", "Latest"}
	for _, kw := range keywords {
		t.Run(kw, func(t *testing.T) {
			got, err := capturer.ParseCheckpointRef(session, kw)
			if err != nil {
				t.Fatalf("ParseCheckpointRef(%q): %v", kw, err)
			}
			if got.Name != "mycp" {
				t.Errorf("ParseCheckpointRef(%q).Name = %q, want mycp", kw, got.Name)
			}
		})
	}
}

func TestCapturer_ParseCheckpointRef_TildeN(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "tilde-session"

	for i := 0; i < 3; i++ {
		cp := &Checkpoint{
			ID:   fmt.Sprintf("20260101-%02d0000-000%d-cp%d", 10+i, i, i),
			Name: fmt.Sprintf("cp%d", i), SessionName: session,
			CreatedAt: time.Date(2026, 1, 1, 10+i, 0, 0, 0, time.UTC),
			Session:   SessionState{},
		}
		storage.Save(cp)
	}

	// ~2 = second newest = cp1
	got, err := capturer.ParseCheckpointRef(session, "~2")
	if err != nil {
		t.Fatalf("ParseCheckpointRef(~2): %v", err)
	}
	if got.Name != "cp1" {
		t.Errorf("~2 got Name=%q, want cp1", got.Name)
	}

	// ~3 = oldest = cp0
	got, err = capturer.ParseCheckpointRef(session, "~3")
	if err != nil {
		t.Fatalf("ParseCheckpointRef(~3): %v", err)
	}
	if got.Name != "cp0" {
		t.Errorf("~3 got Name=%q, want cp0", got.Name)
	}
}

func TestCapturer_ParseCheckpointRef_TildeN_RejectsInvalidMoreRecentCheckpoint(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "tilde-invalid-session"

	for i := 0; i < 2; i++ {
		cp := &Checkpoint{
			ID:          fmt.Sprintf("20260101-%02d0000-000%d-cp%d", 10+i, i, i),
			Name:        fmt.Sprintf("cp%d", i),
			SessionName: session,
			CreatedAt:   time.Date(2026, 1, 1, 10+i, 0, 0, 0, time.UTC),
			Session:     SessionState{},
		}
		if err := storage.Save(cp); err != nil {
			t.Fatalf("Save(%s) failed: %v", cp.ID, err)
		}
		validTime := time.Date(2026, 1, 1, 10+i, 0, 0, 0, time.UTC)
		if err := os.Chtimes(storage.CheckpointDir(session, cp.ID), validTime, validTime); err != nil {
			t.Fatalf("Chtimes(%s) failed: %v", cp.ID, err)
		}
	}

	invalidDir := storage.CheckpointDir(session, "20260101-120000-0009-invalid")
	if err := os.MkdirAll(invalidDir, 0755); err != nil {
		t.Fatalf("MkdirAll(invalidDir) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, MetadataFile), []byte("not valid json"), 0600); err != nil {
		t.Fatalf("WriteFile(metadata) failed: %v", err)
	}
	newestTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, newestTime, newestTime); err != nil {
		t.Fatalf("Chtimes(invalidDir) failed: %v", err)
	}

	_, err := capturer.ParseCheckpointRef(session, "~2")
	if err == nil {
		t.Fatal("ParseCheckpointRef(~2) error = nil, want invalid newer checkpoint error")
	}
	if !strings.Contains(err.Error(), "checkpoint selection blocked by invalid checkpoint") {
		t.Fatalf("ParseCheckpointRef(~2) error = %v, want invalid newer checkpoint context", err)
	}
}

func TestCapturer_ParseCheckpointRef_ExactID(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "exact-session"

	exactID := "20260101-120000-0001-exact"
	cp := &Checkpoint{
		ID: exactID, Name: "exact", SessionName: session,
		CreatedAt: time.Now(), Session: SessionState{},
	}
	storage.Save(cp)

	got, err := capturer.ParseCheckpointRef(session, exactID)
	if err != nil {
		t.Fatalf("ParseCheckpointRef(exactID): %v", err)
	}
	if got.ID != exactID {
		t.Errorf("got ID=%q, want %q", got.ID, exactID)
	}
}

func TestCapturer_ParseCheckpointRef_ExactID_RejectsNonDirectoryPath(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "exact-nondir-session"

	exactID := "20260101-120000-0001-exact"
	fallbackID := exactID + "-other"
	cp := &Checkpoint{
		ID: fallbackID, Name: "other", SessionName: session,
		CreatedAt: time.Now(), Session: SessionState{},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save(%s): %v", fallbackID, err)
	}

	exactPath := filepath.Join(storage.BaseDir, session, exactID)
	if err := os.WriteFile(exactPath, []byte("not a checkpoint directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", exactPath, err)
	}

	_, err := capturer.ParseCheckpointRef(session, exactID)
	if err == nil {
		t.Fatal("ParseCheckpointRef(exactID) error = nil, want non-directory path rejection")
	}
	if !strings.Contains(err.Error(), "checkpoint path is not a directory") {
		t.Fatalf("ParseCheckpointRef(exactID) error = %v, want non-directory path context", err)
	}
}

func TestCapturer_ParseCheckpointRef_ExactID_RejectsSymlinkPath(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "exact-symlink-session"

	exactID := "20260101-120000-0001-symlink"
	fallbackID := exactID + "-other"
	cp := &Checkpoint{
		ID: fallbackID, Name: "other", SessionName: session,
		CreatedAt: time.Now(), Session: SessionState{},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save(%s): %v", fallbackID, err)
	}

	exactPath := filepath.Join(storage.BaseDir, session, exactID)
	if err := os.Symlink(t.TempDir(), exactPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := capturer.ParseCheckpointRef(session, exactID)
	if err == nil {
		t.Fatal("ParseCheckpointRef(exactID) error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "checkpoint path must not be a symlink") {
		t.Fatalf("ParseCheckpointRef(exactID) error = %v, want symlink rejection", err)
	}
}

func TestCapturer_ParseCheckpointRef_NamePattern(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "pattern-session"

	cp := &Checkpoint{
		ID: "20260101-120000-0001-deploy-v2", Name: "deploy-v2", SessionName: session,
		CreatedAt: time.Now(), Session: SessionState{},
	}
	storage.Save(cp)

	// Exact name match (case insensitive)
	got, err := capturer.ParseCheckpointRef(session, "deploy-v2")
	if err != nil {
		t.Fatalf("ParseCheckpointRef(deploy-v2): %v", err)
	}
	if got.Name != "deploy-v2" {
		t.Errorf("got Name=%q, want deploy-v2", got.Name)
	}
}

func TestCapturer_ParseCheckpointRef_NotFound(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "notfound-session"

	// Create at least the session directory
	cp := &Checkpoint{
		ID: "20260101-120000-0001-existing", Name: "existing", SessionName: session,
		CreatedAt: time.Now(), Session: SessionState{},
	}
	storage.Save(cp)

	_, err := capturer.ParseCheckpointRef(session, "nonexistent-id-that-wont-match")
	if err == nil {
		t.Error("expected error for non-matching ref")
	}
	if !strings.Contains(err.Error(), "no checkpoint found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCapturer_ParseCheckpointRef_Ambiguous(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "ambig-session"

	// Create 2 checkpoints with names matching a wildcard
	for i := 0; i < 2; i++ {
		cp := &Checkpoint{
			ID:   fmt.Sprintf("20260101-1%d0000-000%d-deploy-v%d", i, i, i),
			Name: fmt.Sprintf("deploy-v%d", i), SessionName: session,
			CreatedAt: time.Date(2026, 1, 1, 10+i, 0, 0, 0, time.UTC),
			Session:   SessionState{},
		}
		storage.Save(cp)
	}

	_, err := capturer.ParseCheckpointRef(session, "deploy*")
	if err == nil {
		t.Error("expected ambiguous error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCapturer_ParseCheckpointRef_InvalidTilde(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	session := "invalid-session"

	cp := &Checkpoint{
		ID: "20260101-120000-0001-x", Name: "x", SessionName: session,
		CreatedAt: time.Now(), Session: SessionState{},
	}
	storage.Save(cp)

	_, err := capturer.ParseCheckpointRef(session, "~abc")
	if err == nil {
		t.Error("expected error for ~abc")
	}
	if !strings.Contains(err.Error(), "invalid checkpoint reference") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Existing tests below
// =============================================================================

func TestCapturer_CaptureGitState(t *testing.T) {
	// Create temp dir for git repo
	tmpDir, err := os.MkdirTemp("", "ntm-capture-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	if err := exec.Command("git", "-C", tmpDir, "init").Run(); err != nil {
		t.Fatalf("Failed to git init: %v", err)
	}

	// Configure git user for commits
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@example.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test User").Run()

	// Create a file and commit it
	readme := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(readme, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}
	exec.Command("git", "-C", tmpDir, "add", ".").Run()
	exec.Command("git", "-C", tmpDir, "commit", "-m", "Initial commit").Run()

	c := NewCapturer()

	// Test success case
	state, err := c.captureGitState(tmpDir, "session", "chk-1")
	if err != nil {
		t.Errorf("captureGitState failed on valid repo: %v", err)
	}
	if state.Branch == "" {
		t.Error("Expected branch to be captured")
	}
	if state.StatusFile != GitStatusFile {
		t.Errorf("StatusFile = %q, want %q", state.StatusFile, GitStatusFile)
	}

	// Test failure case: corrupt the repo
	// Deleting .git/HEAD makes many git commands fail
	if err := os.Remove(filepath.Join(tmpDir, ".git", "HEAD")); err != nil {
		t.Fatalf("Failed to remove .git/HEAD: %v", err)
	}

	_, err = c.captureGitState(tmpDir, "session", "chk-2")
	if err == nil {
		t.Error("captureGitState should fail on corrupt repo")
	}
}

func TestCapturer_CaptureGitState_DirtySavesPatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-capture-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := exec.Command("git", "-C", tmpDir, "init").Run(); err != nil {
		t.Fatalf("Failed to git init: %v", err)
	}
	exec.Command("git", "-C", tmpDir, "config", "user.email", "test@example.com").Run()
	exec.Command("git", "-C", tmpDir, "config", "user.name", "Test User").Run()

	readme := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(readme, []byte("initial"), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}
	exec.Command("git", "-C", tmpDir, "add", ".").Run()
	exec.Command("git", "-C", tmpDir, "commit", "-m", "Initial commit").Run()

	// Modify tracked file to produce a diff.
	if err := os.WriteFile(readme, []byte("updated"), 0644); err != nil {
		t.Fatalf("Failed to update file: %v", err)
	}

	storage := NewStorageWithDir(tmpDir)
	c := NewCapturerWithStorage(storage)

	checkpointID := "chk-dirty"
	if err := os.MkdirAll(storage.CheckpointDir("session", checkpointID), 0755); err != nil {
		t.Fatalf("Failed to create checkpoint dir: %v", err)
	}

	state, err := c.captureGitState(tmpDir, "session", checkpointID)
	if err != nil {
		t.Fatalf("captureGitState failed on dirty repo: %v", err)
	}
	if !state.IsDirty {
		t.Fatal("expected dirty state")
	}
	if state.PatchFile != GitPatchFile {
		t.Fatalf("expected patch file %q, got %q", GitPatchFile, state.PatchFile)
	}

	patch, err := storage.LoadGitPatch("session", checkpointID)
	if err != nil {
		t.Fatalf("LoadGitPatch failed: %v", err)
	}
	if patch == "" {
		t.Fatal("expected git patch content")
	}
	if !strings.Contains(patch, "updated") {
		t.Fatalf("expected patch to contain updated content, got: %s", patch)
	}
}

func TestRunGitCommand_ReapsProcessOnOutputLimit(t *testing.T) {
	if os.Getenv("NTM_TEST_GIT_SPAM") == "1" {
		chunk := make([]byte, 64*1024)
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				return
			}
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunGitCommand_ReapsProcessOnOutputLimit$")
	cmd.Env = append(os.Environ(), "NTM_TEST_GIT_SPAM=1")

	_, err := runGitCommand(cmd, context.Background(), "status")
	if err == nil || !strings.Contains(err.Error(), "output exceeded limit") {
		t.Fatalf("runGitCommand error = %v, want output-limit error", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("runGitCommand returned before reaping the oversized-output process")
	}
}

func TestIsGitRepoWithCommand_RespectsCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git helper uses /bin/sh")
	}

	dir := t.TempDir()
	gitPath := filepath.Join(dir, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if isGitRepoWithCommand(ctx, gitPath, dir, filepath.Join(dir, ".git")) {
		t.Fatal("isGitRepoWithCommand() = true for a cancelled git probe")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("isGitRepoWithCommand() returned after %s, want prompt cancellation", elapsed)
	}
}

func TestRunGitCommandReportsTimeoutWhileReadingOutput(t *testing.T) {
	if os.Getenv("NTM_TEST_GIT_HANG") == "1" {
		select {}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunGitCommandReportsTimeoutWhileReadingOutput$")
	cmd.Env = append(os.Environ(), "NTM_TEST_GIT_HANG=1")

	_, err := runGitCommand(cmd, ctx, "status")
	if err == nil || !strings.Contains(err.Error(), "git command timed out") {
		t.Fatalf("runGitCommand error = %v, want git command timeout", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("runGitCommand returned before reaping the timed-out process")
	}
}
