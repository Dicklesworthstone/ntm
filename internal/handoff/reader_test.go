package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewReader(t *testing.T) {
	r := NewReader("/tmp/test-project")
	if r.baseDir != "/tmp/test-project/.ntm/handoffs" {
		t.Errorf("expected baseDir to be /tmp/test-project/.ntm/handoffs, got %s", r.baseDir)
	}
	if r.cacheExpiry != 30*time.Second {
		t.Errorf("expected cacheExpiry to be 30s, got %v", r.cacheExpiry)
	}
}

func TestNewReaderWithOptions(t *testing.T) {
	r := NewReaderWithOptions("/tmp/test", 5*time.Minute, nil)
	if r.cacheExpiry != 5*time.Minute {
		t.Errorf("expected cacheExpiry to be 5m, got %v", r.cacheExpiry)
	}
}

func setupTestReader(t *testing.T) (*Reader, string) {
	t.Helper()
	tmpDir := t.TempDir()
	r := NewReader(tmpDir)
	return r, tmpDir
}

func createHandoffFile(t *testing.T, baseDir, session, filename, content string) string {
	t.Helper()
	dir := filepath.Join(baseDir, ".ntm", "handoffs", session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	return path
}

func TestFindLatest(t *testing.T) {
	t.Run("finds most recent handoff", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		// Create two handoff files with different timestamps in names
		createHandoffFile(t, tmpDir, "test-session", "2025-01-01-1200.yaml", `
goal: "First goal"
now: "First now"
version: "1.0"
`)
		createHandoffFile(t, tmpDir, "test-session", "2025-01-02-1200.yaml", `
goal: "Second goal"
now: "Second now"
version: "1.0"
`)

		h, path, err := r.FindLatest("test-session")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected handoff, got nil")
		}
		if h.Goal != "Second goal" {
			t.Errorf("expected 'Second goal', got %q", h.Goal)
		}
		if !filepath.IsAbs(path) {
			t.Errorf("expected absolute path, got %s", path)
		}
	})

	t.Run("returns nil for non-existent session", func(t *testing.T) {
		r, _ := setupTestReader(t)

		h, path, err := r.FindLatest("non-existent")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h != nil {
			t.Errorf("expected nil handoff, got %+v", h)
		}
		if path != "" {
			t.Errorf("expected empty path, got %s", path)
		}
	})

	t.Run("returns nil for empty directory", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		// Create empty directory
		dir := filepath.Join(tmpDir, ".ntm", "handoffs", "empty-session")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}

		h, path, err := r.FindLatest("empty-session")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h != nil {
			t.Errorf("expected nil handoff, got %+v", h)
		}
		if path != "" {
			t.Errorf("expected empty path, got %s", path)
		}
	})

	t.Run("skips directories", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "test-session", "handoff.yaml", `
goal: "Test goal"
now: "Test now"
version: "1.0"
`)
		// Create .archive subdirectory
		archiveDir := filepath.Join(tmpDir, ".ntm", "handoffs", "test-session", ".archive")
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			t.Fatalf("failed to create archive directory: %v", err)
		}

		h, _, err := r.FindLatest("test-session")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected handoff, got nil")
		}
		if h.Goal != "Test goal" {
			t.Errorf("expected 'Test goal', got %q", h.Goal)
		}
	})

	t.Run("falls back when newest handoff is malformed", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "test-session", "2025-01-01-1200.yaml", `
goal: "Valid goal"
now: "Valid now"
version: "1.0"
`)
		createHandoffFile(t, tmpDir, "test-session", "2025-01-02-1200.yaml", `
goal: [invalid yaml
this is not valid
`)

		h, _, err := r.FindLatest("test-session")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected fallback handoff, got nil")
		}
		if h.Goal != "Valid goal" {
			t.Fatalf("expected fallback to valid older handoff, got %q", h.Goal)
		}
	})

	t.Run("skips symlinked newest handoff file", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "test-session", "2025-01-01-1200.yaml", `
goal: "Valid goal"
now: "Valid now"
version: "1.0"
`)
		outsidePath := filepath.Join(t.TempDir(), "outside.yaml")
		if err := os.WriteFile(outsidePath, []byte("goal: external\nnow: external\n"), 0o644); err != nil {
			t.Fatalf("failed to write outside file: %v", err)
		}
		linkPath := filepath.Join(tmpDir, ".ntm", "handoffs", "test-session", "2025-01-02-1200.yaml")
		if err := os.Symlink(outsidePath, linkPath); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		h, path, err := r.FindLatest("test-session")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected valid handoff, got nil")
		}
		if h.Goal != "Valid goal" {
			t.Fatalf("expected symlinked newest file to be skipped, got %q", h.Goal)
		}
		if strings.HasSuffix(path, "2025-01-02-1200.yaml") {
			t.Fatalf("expected symlink path to be skipped, got %s", path)
		}
	})
}

func TestFindLatestAny(t *testing.T) {
	t.Run("finds most recent across sessions", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		// Create handoffs in different sessions with different timestamps
		createHandoffFile(t, tmpDir, "session-a", "handoff.yaml", `
goal: "Session A goal"
now: "Session A now"
version: "1.0"
created_at: 2025-01-01T10:00:00Z
`)
		createHandoffFile(t, tmpDir, "session-b", "handoff.yaml", `
goal: "Session B goal"
now: "Session B now"
version: "1.0"
created_at: 2025-01-02T10:00:00Z
`)

		h, _, err := r.FindLatestAny()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected handoff, got nil")
		}
		if h.Goal != "Session B goal" {
			t.Errorf("expected 'Session B goal', got %q", h.Goal)
		}
	})

	t.Run("falls back to file modtime when created_at is missing", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		olderPath := createHandoffFile(t, tmpDir, "session-a", "handoff.yaml", `
goal: "Session A goal"
now: "Session A now"
version: "1.0"
`)
		newerPath := createHandoffFile(t, tmpDir, "session-b", "handoff.yaml", `
goal: "Session B goal"
now: "Session B now"
version: "1.0"
`)

		olderTime := time.Now().Add(-2 * time.Hour)
		newerTime := time.Now().Add(-1 * time.Hour)
		if err := os.Chtimes(olderPath, olderTime, olderTime); err != nil {
			t.Fatalf("failed to set older mtime: %v", err)
		}
		if err := os.Chtimes(newerPath, newerTime, newerTime); err != nil {
			t.Fatalf("failed to set newer mtime: %v", err)
		}

		h, path, err := r.FindLatestAny()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected handoff, got nil")
		}
		if h.Goal != "Session B goal" {
			t.Fatalf("expected newest modtime handoff, got %q", h.Goal)
		}
		if path != newerPath {
			t.Fatalf("expected newest modtime path %s, got %s", newerPath, path)
		}
	})

	t.Run("skips hidden session directories", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "visible", "handoff.yaml", `
goal: "Visible goal"
now: "Visible now"
version: "1.0"
created_at: 2025-01-01T10:00:00Z
`)
		createHandoffFile(t, tmpDir, ".hidden", "handoff.yaml", `
goal: "Hidden goal"
now: "Hidden now"
version: "1.0"
created_at: 2025-01-02T10:00:00Z
`)

		h, path, err := r.FindLatestAny()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected handoff, got nil")
		}
		if h.Goal != "Visible goal" {
			t.Fatalf("expected visible handoff, got %q", h.Goal)
		}
		if !strings.Contains(path, filepath.Join("visible", "handoff.yaml")) {
			t.Fatalf("expected visible session path, got %s", path)
		}
	})

	t.Run("returns nil when no handoffs exist", func(t *testing.T) {
		r, _ := setupTestReader(t)

		h, path, err := r.FindLatestAny()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h != nil {
			t.Errorf("expected nil handoff, got %+v", h)
		}
		if path != "" {
			t.Errorf("expected empty path, got %s", path)
		}
	})
}

func TestRead(t *testing.T) {
	t.Run("parses valid YAML", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		path := createHandoffFile(t, tmpDir, "test", "handoff.yaml", `
version: "1.0"
session: test
goal: "Test goal"
now: "Test now"
status: complete
outcome: SUCCEEDED
done_this_session:
  - task: "Task 1"
    files:
      - file1.go
      - file2.go
blockers:
  - "Blocker 1"
decisions:
  key1: "value1"
findings:
  key2: "value2"
`)

		h, err := r.Read(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if h.Goal != "Test goal" {
			t.Errorf("goal: expected 'Test goal', got %q", h.Goal)
		}
		if h.Now != "Test now" {
			t.Errorf("now: expected 'Test now', got %q", h.Now)
		}
		if h.Status != "complete" {
			t.Errorf("status: expected 'complete', got %q", h.Status)
		}
		if h.Outcome != "SUCCEEDED" {
			t.Errorf("outcome: expected 'SUCCEEDED', got %q", h.Outcome)
		}
		if len(h.DoneThisSession) != 1 {
			t.Errorf("done_this_session: expected 1, got %d", len(h.DoneThisSession))
		}
		if len(h.Blockers) != 1 {
			t.Errorf("blockers: expected 1, got %d", len(h.Blockers))
		}
		if h.Decisions["key1"] != "value1" {
			t.Errorf("decisions: expected 'value1', got %q", h.Decisions["key1"])
		}
		if h.Findings["key2"] != "value2" {
			t.Errorf("findings: expected 'value2', got %q", h.Findings["key2"])
		}
	})

	t.Run("returns error for non-existent file", func(t *testing.T) {
		r, _ := setupTestReader(t)

		_, err := r.Read("/non/existent/file.yaml")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("returns error for malformed YAML", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		path := createHandoffFile(t, tmpDir, "test", "bad.yaml", `
goal: [invalid yaml
this is not valid
`)

		_, err := r.Read(path)
		if err == nil {
			t.Fatal("expected error for malformed YAML, got nil")
		}
	})

	t.Run("continues on validation warnings", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		// Missing required fields but valid YAML
		path := createHandoffFile(t, tmpDir, "test", "partial.yaml", `
version: "1.0"
session: test
status: invalid_status
`)

		h, err := r.Read(path)
		if err != nil {
			t.Fatalf("expected no error for validation warnings, got %v", err)
		}
		if h == nil {
			t.Fatal("expected handoff even with validation warnings")
		}
	})
}

func TestExtractGoalNow(t *testing.T) {
	t.Run("extracts goal and now", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "test", "handoff.yaml", `
goal: "Test goal value"
now: "Test now value"
other: ignored
`)

		goal, now, err := r.ExtractGoalNow("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if goal != "Test goal value" {
			t.Errorf("goal: expected 'Test goal value', got %q", goal)
		}
		if now != "Test now value" {
			t.Errorf("now: expected 'Test now value', got %q", now)
		}
	})

	t.Run("returns empty for non-existent session", func(t *testing.T) {
		r, _ := setupTestReader(t)

		goal, now, err := r.ExtractGoalNow("non-existent")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if goal != "" || now != "" {
			t.Errorf("expected empty strings, got goal=%q now=%q", goal, now)
		}
	})

	t.Run("handles quoted values", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "test", "handoff.yaml", `
goal: "Quoted goal"
now: 'Single quoted now'
`)

		goal, now, err := r.ExtractGoalNow("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if goal != "Quoted goal" {
			t.Errorf("goal: expected 'Quoted goal', got %q", goal)
		}
		if now != "Single quoted now" {
			t.Errorf("now: expected 'Single quoted now', got %q", now)
		}
	})

	t.Run("caches results", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)
		r.cacheExpiry = 1 * time.Hour // Long expiry for test

		createHandoffFile(t, tmpDir, "test", "handoff.yaml", `
goal: "Cached goal"
now: "Cached now"
`)

		// First call - cache miss
		goal1, now1, err := r.ExtractGoalNow("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify cache has entry
		r.cacheMu.RLock()
		cacheLen := len(r.goalNowCache)
		r.cacheMu.RUnlock()
		if cacheLen != 1 {
			t.Errorf("expected 1 cache entry, got %d", cacheLen)
		}

		// Second call - should hit cache
		goal2, now2, err := r.ExtractGoalNow("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if goal1 != goal2 || now1 != now2 {
			t.Errorf("cache should return same values")
		}
	})

	t.Run("invalidates cache on mod time change", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)
		r.cacheExpiry = 1 * time.Hour

		path := createHandoffFile(t, tmpDir, "test", "handoff.yaml", `
goal: "Original goal"
now: "Original now"
`)

		// First call - cache miss
		goal1, _, err := r.ExtractGoalNow("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if goal1 != "Original goal" {
			t.Errorf("expected 'Original goal', got %q", goal1)
		}

		// Update file (changes mod time)
		time.Sleep(10 * time.Millisecond) // Ensure different mod time
		if err := os.WriteFile(path, []byte(`
goal: "Updated goal"
now: "Updated now"
`), 0o644); err != nil {
			t.Fatalf("failed to update file: %v", err)
		}

		// Second call - should detect mod time change
		goal2, _, err := r.ExtractGoalNow("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if goal2 != "Updated goal" {
			t.Errorf("expected 'Updated goal' after mod time change, got %q", goal2)
		}
	})

	t.Run("skips symlinked newest file", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "test", "2025-01-01.yaml", `
goal: "Valid goal"
now: "Valid now"
`)
		outsidePath := filepath.Join(t.TempDir(), "outside.yaml")
		if err := os.WriteFile(outsidePath, []byte("goal: external\nnow: external\n"), 0o644); err != nil {
			t.Fatalf("failed to write outside file: %v", err)
		}
		linkPath := filepath.Join(tmpDir, ".ntm", "handoffs", "test", "2025-01-02.yaml")
		if err := os.Symlink(outsidePath, linkPath); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		goal, now, err := r.ExtractGoalNow("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if goal != "Valid goal" || now != "Valid now" {
			t.Fatalf("expected fallback to regular file, got goal=%q now=%q", goal, now)
		}
	})
}

func TestInvalidateCache(t *testing.T) {
	r, tmpDir := setupTestReader(t)
	r.cacheExpiry = 1 * time.Hour

	createHandoffFile(t, tmpDir, "test", "handoff.yaml", `
goal: "Test"
now: "Test"
`)

	// Populate cache
	_, _, _ = r.ExtractGoalNow("test")

	r.cacheMu.RLock()
	if len(r.goalNowCache) != 1 {
		t.Fatalf("expected 1 cache entry before invalidation")
	}
	r.cacheMu.RUnlock()

	// Invalidate
	r.InvalidateCache()

	r.cacheMu.RLock()
	if len(r.goalNowCache) != 0 {
		t.Errorf("expected 0 cache entries after invalidation, got %d", len(r.goalNowCache))
	}
	r.cacheMu.RUnlock()
}

func TestListHandoffs(t *testing.T) {
	t.Run("lists handoffs sorted by date", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "test", "2025-01-01.yaml", `
goal: "First"
now: "First now"
status: complete
`)
		time.Sleep(10 * time.Millisecond)
		createHandoffFile(t, tmpDir, "test", "2025-01-02.yaml", `
goal: "Second"
now: "Second now"
status: partial
`)
		time.Sleep(10 * time.Millisecond)
		createHandoffFile(t, tmpDir, "test", "auto-handoff-123.yaml", `
goal: "Auto"
now: "Auto now"
status: blocked
`)

		metas, err := r.ListHandoffs("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(metas) != 3 {
			t.Fatalf("expected 3 handoffs, got %d", len(metas))
		}

		// Should be sorted by date descending (most recent first)
		if metas[0].Goal != "Auto" {
			t.Errorf("expected most recent (Auto) first, got %q", metas[0].Goal)
		}
		if !metas[0].IsAuto {
			t.Error("expected IsAuto=true for auto-handoff file")
		}
		if metas[0].Status != "blocked" {
			t.Errorf("expected status 'blocked', got %q", metas[0].Status)
		}
	})

	t.Run("returns nil for non-existent session", func(t *testing.T) {
		r, _ := setupTestReader(t)

		metas, err := r.ListHandoffs("non-existent")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if metas != nil {
			t.Errorf("expected nil, got %v", metas)
		}
	})

	t.Run("skips directories", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "test", "handoff.yaml", `
goal: "Test"
now: "Test"
`)
		// Create subdirectory
		subdir := filepath.Join(tmpDir, ".ntm", "handoffs", "test", "subdir")
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatalf("failed to create subdir: %v", err)
		}

		metas, err := r.ListHandoffs("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(metas) != 1 {
			t.Errorf("expected 1 handoff (not subdirectory), got %d", len(metas))
		}
	})

	t.Run("skips symlinked yaml files", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "test", "2025-01-01.yaml", `
goal: "Visible"
now: "Visible"
status: complete
`)
		outsidePath := filepath.Join(t.TempDir(), "outside.yaml")
		if err := os.WriteFile(outsidePath, []byte("goal: external\nnow: external\nstatus: blocked\n"), 0o644); err != nil {
			t.Fatalf("failed to write outside file: %v", err)
		}
		linkPath := filepath.Join(tmpDir, ".ntm", "handoffs", "test", "2025-01-02.yaml")
		if err := os.Symlink(outsidePath, linkPath); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		metas, err := r.ListHandoffs("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(metas) != 1 {
			t.Fatalf("expected only regular handoff to be listed, got %d", len(metas))
		}
		if filepath.Base(metas[0].Path) != "2025-01-01.yaml" {
			t.Fatalf("expected regular file to remain, got %s", metas[0].Path)
		}
	})
}

func TestListSessions(t *testing.T) {
	t.Run("lists all sessions", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "alpha", "handoff.yaml", "goal: A\nnow: A")
		createHandoffFile(t, tmpDir, "beta", "handoff.yaml", "goal: B\nnow: B")
		createHandoffFile(t, tmpDir, "gamma", "handoff.yaml", "goal: C\nnow: C")

		sessions, err := r.ListSessions()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(sessions) != 3 {
			t.Fatalf("expected 3 sessions, got %d", len(sessions))
		}

		// Should be sorted alphabetically
		if sessions[0] != "alpha" || sessions[1] != "beta" || sessions[2] != "gamma" {
			t.Errorf("unexpected session order: %v", sessions)
		}
	})

	t.Run("skips empty session directories", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "visible", "handoff.yaml", "goal: A\nnow: A")
		emptyDir := filepath.Join(tmpDir, ".ntm", "handoffs", "empty")
		if err := os.MkdirAll(emptyDir, 0o755); err != nil {
			t.Fatalf("failed to create empty dir: %v", err)
		}

		sessions, err := r.ListSessions()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 1 || sessions[0] != "visible" {
			t.Fatalf("expected only visible session, got %v", sessions)
		}
	})

	t.Run("returns nil when no sessions exist", func(t *testing.T) {
		r, _ := setupTestReader(t)

		sessions, err := r.ListSessions()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sessions != nil {
			t.Errorf("expected nil, got %v", sessions)
		}
	})

	t.Run("skips hidden directories", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "visible", "handoff.yaml", "goal: A\nnow: A")
		// Create hidden directory
		hiddenDir := filepath.Join(tmpDir, ".ntm", "handoffs", ".hidden")
		if err := os.MkdirAll(hiddenDir, 0o755); err != nil {
			t.Fatalf("failed to create hidden dir: %v", err)
		}

		sessions, err := r.ListSessions()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(sessions) != 1 || sessions[0] != "visible" {
			t.Errorf("expected only 'visible' session, got %v", sessions)
		}
	})

	t.Run("skips symlink-backed session directories", func(t *testing.T) {
		r, tmpDir := setupTestReader(t)

		createHandoffFile(t, tmpDir, "visible", "handoff.yaml", "goal: A\nnow: A")
		outsideDir := t.TempDir()
		if err := os.Symlink(outsideDir, filepath.Join(tmpDir, ".ntm", "handoffs", "linked")); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		sessions, err := r.ListSessions()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 1 || sessions[0] != "visible" {
			t.Fatalf("expected only visible session, got %v", sessions)
		}
	})
}

func TestBaseDir(t *testing.T) {
	r := NewReader("/tmp/project")
	if r.BaseDir() != "/tmp/project/.ntm/handoffs" {
		t.Errorf("expected /tmp/project/.ntm/handoffs, got %s", r.BaseDir())
	}
}

func TestExtractGoalNowFromContent(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantGoal string
		wantNow  string
	}{
		{
			name:     "simple values",
			content:  "goal: test goal\nnow: test now\n",
			wantGoal: "test goal",
			wantNow:  "test now",
		},
		{
			name:     "double quoted values",
			content:  `goal: "quoted goal"` + "\n" + `now: "quoted now"` + "\n",
			wantGoal: "quoted goal",
			wantNow:  "quoted now",
		},
		{
			name:     "single quoted values",
			content:  "goal: 'single quoted'\nnow: 'also single'\n",
			wantGoal: "single quoted",
			wantNow:  "also single",
		},
		{
			name:     "mixed with other fields",
			content:  "version: 1.0\ngoal: the goal\nstatus: complete\nnow: the now\noutcome: SUCCEEDED\n",
			wantGoal: "the goal",
			wantNow:  "the now",
		},
		{
			name:     "missing goal",
			content:  "now: only now\n",
			wantGoal: "",
			wantNow:  "only now",
		},
		{
			name:     "missing now",
			content:  "goal: only goal\n",
			wantGoal: "only goal",
			wantNow:  "",
		},
		{
			name:     "empty content",
			content:  "",
			wantGoal: "",
			wantNow:  "",
		},
		{
			name:     "whitespace handling",
			content:  "goal:   spaced goal   \nnow:   spaced now   \n",
			wantGoal: "spaced goal",
			wantNow:  "spaced now",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goal, now := extractGoalNowFromContent([]byte(tt.content))
			if goal != tt.wantGoal {
				t.Errorf("goal: expected %q, got %q", tt.wantGoal, goal)
			}
			if now != tt.wantNow {
				t.Errorf("now: expected %q, got %q", tt.wantNow, now)
			}
		})
	}
}

func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 10, "this is..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncateForLog(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForLog(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestReaderConcurrentAccess(t *testing.T) {
	r, tmpDir := setupTestReader(t)
	r.cacheExpiry = 1 * time.Hour

	createHandoffFile(t, tmpDir, "test", "handoff.yaml", `
goal: "Concurrent goal"
now: "Concurrent now"
`)

	var wg sync.WaitGroup
	errCh := make(chan error, 100)

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := r.ExtractGoalNow("test")
			if err != nil {
				errCh <- err
			}
		}()
	}

	// Concurrent invalidations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.InvalidateCache()
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent access error: %v", err)
	}
}

// Benchmarks

func BenchmarkExtractGoalNow(b *testing.B) {
	tmpDir := b.TempDir()
	r := NewReader(tmpDir)

	dir := filepath.Join(tmpDir, ".ntm", "handoffs", "bench")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(dir, "handoff.yaml")
	content := `goal: "Benchmark goal"
now: "Benchmark now"
version: "1.0"
status: complete`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.ExtractGoalNow("bench")
	}
}

func BenchmarkExtractGoalNowCacheHit(b *testing.B) {
	tmpDir := b.TempDir()
	r := NewReader(tmpDir)
	r.cacheExpiry = 1 * time.Hour

	dir := filepath.Join(tmpDir, ".ntm", "handoffs", "bench")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(dir, "handoff.yaml")
	content := `goal: "Benchmark goal"
now: "Benchmark now"`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatal(err)
	}

	// Warm up cache
	r.ExtractGoalNow("bench")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.ExtractGoalNow("bench")
	}
}

func BenchmarkReadHandoff(b *testing.B) {
	tmpDir := b.TempDir()
	r := NewReader(tmpDir)

	dir := filepath.Join(tmpDir, ".ntm", "handoffs", "bench")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(dir, "handoff.yaml")
	content := `version: "1.0"
session: bench
goal: "Benchmark goal"
now: "Benchmark now"
status: complete
outcome: SUCCEEDED
done_this_session:
  - task: "Task 1"
    files: [file1.go, file2.go]
  - task: "Task 2"
    files: [file3.go]
blockers: ["blocker 1", "blocker 2"]
decisions:
  key1: value1
  key2: value2
findings:
  finding1: result1`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Read(path)
	}
}
