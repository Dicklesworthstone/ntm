package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
)

func TestRollbackResponseToMap(t *testing.T) {

	t.Run("basic fields", func(t *testing.T) {
		r := RollbackResponse{
			CheckpointID:   "cp-123",
			CheckpointName: "before-refactor",
			GitRestored:    true,
			DryRun:         false,
		}

		m := rollbackResponseToMap(r)
		if m["checkpoint_id"] != "cp-123" {
			t.Errorf("checkpoint_id = %v", m["checkpoint_id"])
		}
		if m["checkpoint_name"] != "before-refactor" {
			t.Errorf("checkpoint_name = %v", m["checkpoint_name"])
		}
		if m["git_restored"] != true {
			t.Errorf("git_restored = %v", m["git_restored"])
		}
		if m["dry_run"] != false {
			t.Errorf("dry_run = %v", m["dry_run"])
		}
		if _, ok := m["stash_created"]; ok {
			t.Error("stash_created should not be present when false")
		}
		if _, ok := m["stash_ref"]; ok {
			t.Error("stash_ref should not be present when stash not created")
		}
		if _, ok := m["warnings"]; ok {
			t.Error("warnings should not be present when empty")
		}
	})

	t.Run("with stash", func(t *testing.T) {
		r := RollbackResponse{
			CheckpointID:   "cp-456",
			CheckpointName: "snapshot",
			StashCreated:   true,
			StashRef:       "stash@{0}",
		}

		m := rollbackResponseToMap(r)
		if m["stash_created"] != true {
			t.Errorf("stash_created = %v", m["stash_created"])
		}
		if m["stash_ref"] != "stash@{0}" {
			t.Errorf("stash_ref = %v", m["stash_ref"])
		}
	})

	t.Run("with warnings", func(t *testing.T) {
		r := RollbackResponse{
			CheckpointID:   "cp-789",
			CheckpointName: "pre-deploy",
			Warnings:       []string{"dirty working tree", "untracked files present"},
		}

		m := rollbackResponseToMap(r)
		warnings, ok := m["warnings"].([]string)
		if !ok {
			t.Fatal("warnings should be a string slice")
		}
		if len(warnings) != 2 {
			t.Errorf("warnings count = %d, want 2", len(warnings))
		}
	})

	t.Run("dry run", func(t *testing.T) {
		r := RollbackResponse{
			CheckpointID:   "cp-dry",
			CheckpointName: "test",
			DryRun:         true,
		}

		m := rollbackResponseToMap(r)
		if m["dry_run"] != true {
			t.Errorf("dry_run = %v, want true", m["dry_run"])
		}
	})
}

func TestCheckpointToResponse(t *testing.T) {

	now := time.Now()

	t.Run("basic without details", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-abc",
			Name:        "initial",
			Description: "first checkpoint",
			SessionName: "dev-session",
			WorkingDir:  "/tmp/project",
			CreatedAt:   now,
			PaneCount:   3,
		}

		resp := checkpointToResponse(cp, false)
		if resp.ID != "cp-abc" {
			t.Errorf("ID = %q", resp.ID)
		}
		if resp.Name != "initial" {
			t.Errorf("Name = %q", resp.Name)
		}
		if resp.Description != "first checkpoint" {
			t.Errorf("Description = %q", resp.Description)
		}
		if resp.SessionName != "dev-session" {
			t.Errorf("SessionName = %q", resp.SessionName)
		}
		if resp.WorkingDir != "/tmp/project" {
			t.Errorf("WorkingDir = %q", resp.WorkingDir)
		}
		if resp.PaneCount != 3 {
			t.Errorf("PaneCount = %d", resp.PaneCount)
		}
		if resp.Git != nil {
			t.Error("Git should be nil when branch is empty")
		}
		if resp.Session != nil {
			t.Error("Session should be nil when includeDetails is false")
		}
	})

	t.Run("with git info", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-git",
			Name:        "with-git",
			SessionName: "dev",
			CreatedAt:   now,
			Git: checkpoint.GitState{
				Branch:         "main",
				Commit:         "abc123",
				IsDirty:        true,
				StagedCount:    2,
				UnstagedCount:  1,
				UntrackedCount: 3,
				PatchFile:      "diff.patch",
			},
		}

		resp := checkpointToResponse(cp, false)
		if resp.Git == nil {
			t.Fatal("Git should be populated when branch is set")
		}
		if resp.Git.Branch != "main" {
			t.Errorf("Git.Branch = %q", resp.Git.Branch)
		}
		if resp.Git.Commit != "abc123" {
			t.Errorf("Git.Commit = %q", resp.Git.Commit)
		}
		if !resp.Git.IsDirty {
			t.Error("Git.IsDirty should be true")
		}
		if resp.Git.StagedCount != 2 {
			t.Errorf("Git.StagedCount = %d", resp.Git.StagedCount)
		}
		if resp.Git.UnstagedCount != 1 {
			t.Errorf("Git.UnstagedCount = %d", resp.Git.UnstagedCount)
		}
		if resp.Git.UntrackedCount != 3 {
			t.Errorf("Git.UntrackedCount = %d", resp.Git.UntrackedCount)
		}
		if !resp.Git.HasPatch {
			t.Error("Git.HasPatch should be true when PatchFile is set")
		}
	})

	t.Run("with session details", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-details",
			Name:        "detailed",
			SessionName: "dev",
			CreatedAt:   now,
			PaneCount:   3,
			Session: checkpoint.SessionState{
				Panes: []checkpoint.PaneState{
					{AgentType: "cc"},
					{AgentType: "cod"},
					{AgentType: ""},
				},
				Layout:          "tiled",
				ActivePaneIndex: 1,
			},
		}

		resp := checkpointToResponse(cp, true)
		if resp.Session == nil {
			t.Fatal("Session should be populated when includeDetails is true")
		}
		if resp.Session.PaneCount != 3 {
			t.Errorf("Session.PaneCount = %d", resp.Session.PaneCount)
		}
		if resp.Session.ActivePaneIndex != 1 {
			t.Errorf("Session.ActivePaneIndex = %d", resp.Session.ActivePaneIndex)
		}
		if resp.Session.Layout != "tiled" {
			t.Errorf("Session.Layout = %q", resp.Session.Layout)
		}
		if len(resp.Session.AgentTypes) != 2 {
			t.Errorf("Session.AgentTypes = %v, want [cc cod]", resp.Session.AgentTypes)
		}
	})

	t.Run("metadata only details preserve stored pane count", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-metadata-only",
			Name:        "metadata-only",
			SessionName: "dev",
			CreatedAt:   now,
			PaneCount:   3,
		}

		resp := checkpointToResponse(cp, true)
		if resp.Session == nil {
			t.Fatal("Session should be populated when includeDetails is true")
		}
		if resp.Session.PaneCount != 3 {
			t.Errorf("Session.PaneCount = %d, want 3", resp.Session.PaneCount)
		}
		if len(resp.Session.AgentTypes) != 0 {
			t.Errorf("Session.AgentTypes = %v, want empty", resp.Session.AgentTypes)
		}
	})

	t.Run("no details skips session", func(t *testing.T) {
		cp := &checkpoint.Checkpoint{
			ID:          "cp-nodetails",
			Name:        "bare",
			SessionName: "dev",
			CreatedAt:   now,
			Session: checkpoint.SessionState{
				Panes: []checkpoint.PaneState{{AgentType: "cc"}},
			},
		}

		resp := checkpointToResponse(cp, false)
		if resp.Session != nil {
			t.Error("Session should be nil when includeDetails is false")
		}
	})
}

// bd-456fv: a checkpoint's WorkingDir is caller-supplied — POST
// /checkpoints/import accepts an arbitrary target_dir and otherwise trusts the
// archive's own metadata.json — and rollback runs `git stash push`, a
// detaching `git checkout`, and `git apply --3way` inside it. Confinement to
// the project directory is what stops a caller holding only sessions:write
// from rewriting an unrelated repository on the host.
func TestResolveCheckpointWorkDirConfinesToProjectDir(t *testing.T) {
	s, _ := setupTestServer(t)
	root := s.projectDirSnapshot()

	inside := filepath.Join(root, "nested", "repo")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("mkdir inside: %v", err)
	}
	outside := t.TempDir()

	t.Run("accepts a directory inside the project", func(t *testing.T) {
		got, err := s.resolveCheckpointWorkDir(inside)
		if err != nil {
			t.Fatalf("resolveCheckpointWorkDir(inside) = %v, want it accepted", err)
		}
		if got == "" {
			t.Fatal("resolved path is empty")
		}
	})

	t.Run("refuses a directory outside the project", func(t *testing.T) {
		if _, err := s.resolveCheckpointWorkDir(outside); err == nil {
			t.Fatalf("resolveCheckpointWorkDir(%q) was accepted; git would run in an unrelated repository", outside)
		}
	})

	t.Run("refuses traversal back out of the project", func(t *testing.T) {
		if _, err := s.resolveCheckpointWorkDir(filepath.Join(root, "..")); err == nil {
			t.Fatal("parent-directory traversal was accepted")
		}
	})

	t.Run("refuses a symlink pointing outside the project", func(t *testing.T) {
		link := filepath.Join(root, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := s.resolveCheckpointWorkDir(link); err == nil {
			t.Fatal("a symlink inside the project pointing outside it was accepted")
		}
	})

	t.Run("refuses a missing directory", func(t *testing.T) {
		if _, err := s.resolveCheckpointWorkDir(filepath.Join(root, "does-not-exist")); err == nil {
			t.Fatal("a nonexistent working directory was accepted")
		}
	})
}

// The commit reaches `git checkout` as a bare argv element and is sliced for
// display, so a non-object-id value is both an argument-injection vector and a
// panic (the slice is on the dry-run path).
func TestCheckpointCommitPatternRejectsNonObjectIDs(t *testing.T) {
	valid := []string{"abc1234", "0123456789abcdef0123456789abcdef01234567"}
	for _, commit := range valid {
		if !checkpointCommitPattern.MatchString(commit) {
			t.Errorf("commit %q rejected, want accepted", commit)
		}
	}
	invalid := []string{"", "abc", "--force", "-B branch", "HEAD", "main", "abc123z", "abc1234 --force"}
	for _, commit := range invalid {
		if checkpointCommitPattern.MatchString(commit) {
			t.Errorf("commit %q accepted, want rejected", commit)
		}
	}
}

// The export handler interpolates sessionName into the Content-Disposition
// header and into a temp filename, but was the one checkpoint handler that
// never validated it. ValidateSessionName's allowlist is what excludes CR/LF.
func TestHandleExportCheckpointValidatesSessionName(t *testing.T) {
	s, _ := setupTestServer(t)

	for _, name := range []string{
		"bad\r\nX-Injected: 1",
		"has space",
		"has/slash",
		"",
	} {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("sessionName", name)
		rctx.URLParams.Add("checkpointId", "cp-1")
		req := httptest.NewRequest("GET", "/api/v1/sessions/x/checkpoints/cp-1/export", nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()

		s.handleExportCheckpoint(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("session name %q: got %d, want 400", name, rec.Code)
		}
		if got := rec.Header().Get("X-Injected"); got != "" {
			t.Errorf("session name %q injected a response header", name)
		}
	}
}
