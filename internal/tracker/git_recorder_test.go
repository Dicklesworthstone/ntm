package tracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v\n%s", err, out)
	}
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "tracked.txt", "original")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "init")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// newTestRecorder samples repo and keys the ledger by that same directory, which
// is the shared-checkout shape; the worktree shape is covered by
// TestTwoAgentWorktreesProduceDetectableConflict.
func newTestRecorder(repo, identity string, store *FileChangeStore) *GitRecorder {
	return NewGitRecorder(GitRecorderConfig{
		Root:       repo,
		ProjectDir: repo,
		Session:    "sess",
		Identity:   identity,
		Store:      store,
	})
}

func sample(t *testing.T, r *GitRecorder) int {
	t.Helper()
	n, err := r.Sample(t.Context())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	return n
}

// A tree that was already dirty when monitoring started did not change now.
func TestGitRecorderFirstSampleOnlyBaselines(t *testing.T) {
	repo := newGitRepo(t)
	writeFile(t, repo, "tracked.txt", "edited before monitoring")

	store := NewFileChangeStore(50)
	r := newTestRecorder(repo, "cc-1", store)

	if n := sample(t, r); n != 0 {
		t.Errorf("first sample recorded %d changes, want 0 (baseline only)", n)
	}
	if got := len(store.All()); got != 0 {
		t.Errorf("store has %d entries after baseline, want 0", got)
	}
}

func TestGitRecorderRecordsNewAndModifiedFiles(t *testing.T) {
	repo := newGitRepo(t)
	store := NewFileChangeStore(50)
	r := newTestRecorder(repo, "cc-1", store)
	sample(t, r)

	writeFile(t, repo, "brand_new.txt", "new content")
	writeFile(t, repo, "tracked.txt", "modified content")

	if n := sample(t, r); n != 2 {
		t.Fatalf("recorded %d changes, want 2", n)
	}

	byPath := map[string]FileChange{}
	for _, entry := range store.All() {
		byPath[entry.Change.Path] = entry.Change
		if len(entry.Agents) != 1 || entry.Agents[0] != "cc-1" {
			t.Errorf("entry for %s attributed to %v, want [cc-1]", entry.Change.Path, entry.Agents)
		}
		if entry.Session != "sess" {
			t.Errorf("entry session = %q, want %q", entry.Session, "sess")
		}
	}
	if got := byPath["brand_new.txt"].Type; got != FileAdded {
		t.Errorf("brand_new.txt type = %q, want %q", got, FileAdded)
	}
	if got := byPath["tracked.txt"].Type; got != FileModified {
		t.Errorf("tracked.txt type = %q, want %q", got, FileModified)
	}
}

// A file that stays dirty without being touched again must not re-record on
// every tick, or a single edit would look like continuous churn.
func TestGitRecorderIgnoresUnchangedDirtyFile(t *testing.T) {
	repo := newGitRepo(t)
	store := NewFileChangeStore(50)
	r := newTestRecorder(repo, "cc-1", store)
	sample(t, r)

	writeFile(t, repo, "tracked.txt", "edited once")
	if n := sample(t, r); n != 1 {
		t.Fatalf("first edit recorded %d changes, want 1", n)
	}
	if n := sample(t, r); n != 0 {
		t.Errorf("unchanged dirty file recorded %d changes, want 0", n)
	}
}

func TestGitRecorderRecordsRepeatEdits(t *testing.T) {
	repo := newGitRepo(t)
	store := NewFileChangeStore(50)
	r := newTestRecorder(repo, "cc-1", store)
	sample(t, r)

	writeFile(t, repo, "tracked.txt", "first edit")
	sample(t, r)
	writeFile(t, repo, "tracked.txt", "second edit, a different length entirely")
	if n := sample(t, r); n != 1 {
		t.Errorf("second edit recorded %d changes, want 1", n)
	}
}

// Committing is not deleting. The delta walks paths that left `git status`, so
// without a stat against the tree root every commit would look like a deletion.
func TestGitRecorderCommitIsNotADeletion(t *testing.T) {
	repo := newGitRepo(t)
	store := NewFileChangeStore(50)
	r := newTestRecorder(repo, "cc-1", store)
	sample(t, r)

	writeFile(t, repo, "tracked.txt", "edited then committed")
	sample(t, r)

	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "commit the edit")
	sample(t, r)

	for _, entry := range store.All() {
		if entry.Change.Type == FileDeleted {
			t.Errorf("committing %s was recorded as a deletion", entry.Change.Path)
		}
	}
}

func TestGitRecorderRecordsDeletion(t *testing.T) {
	repo := newGitRepo(t)
	store := NewFileChangeStore(50)
	r := newTestRecorder(repo, "cc-1", store)
	sample(t, r)

	if err := os.Remove(filepath.Join(repo, "tracked.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	sample(t, r)

	var found bool
	for _, entry := range store.All() {
		if entry.Change.Path == "tracked.txt" && entry.Change.Type == FileDeleted {
			found = true
		}
	}
	if !found {
		t.Error("deleting a tracked file was not recorded as a deletion")
	}
}

func TestGitRecorderHonorsGitignore(t *testing.T) {
	repo := newGitRepo(t)
	writeFile(t, repo, ".gitignore", "noise/\n")
	gitCmd(t, repo, "add", ".gitignore")
	gitCmd(t, repo, "commit", "-m", "ignore noise")

	store := NewFileChangeStore(50)
	r := newTestRecorder(repo, "cc-1", store)
	sample(t, r)

	writeFile(t, repo, "noise/generated.bin", "build output")
	if n := sample(t, r); n != 0 {
		t.Errorf("ignored files recorded %d changes, want 0", n)
	}
}

// Repo-relative paths are what let the same file compare equal across two
// agents' worktrees; absolute paths would never match and no conflict could
// ever be detected.
func TestGitRecorderPathsAreRepoRelative(t *testing.T) {
	repo := newGitRepo(t)
	store := NewFileChangeStore(50)
	r := newTestRecorder(repo, "cc-1", store)
	sample(t, r)

	writeFile(t, repo, "internal/pkg/file.go", "package pkg")
	sample(t, r)

	entries := store.All()
	if len(entries) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(entries))
	}
	if got := entries[0].Change.Path; got != "internal/pkg/file.go" {
		t.Errorf("path = %q, want repo-relative %q", got, "internal/pkg/file.go")
	}
}

// The end-to-end feature: two agents editing the same repo-relative path in
// their own worktrees must surface as a conflict naming both agents.
func TestTwoAgentWorktreesProduceDetectableConflict(t *testing.T) {
	repoA := newGitRepo(t)
	repoB := newGitRepo(t)

	store := NewFileChangeStore(50)
	agentA := newTestRecorder(repoA, "cc-1", store)
	agentB := newTestRecorder(repoB, "cod-2", store)
	sample(t, agentA)
	sample(t, agentB)

	writeFile(t, repoA, "internal/serve/server.go", "edited by cc-1")
	writeFile(t, repoB, "internal/serve/server.go", "edited by cod-2, different length")
	sample(t, agentA)
	sample(t, agentB)

	conflicts := DetectConflicts(store.All())
	if len(conflicts) != 1 {
		t.Fatalf("detected %d conflicts, want 1: %+v", len(conflicts), conflicts)
	}
	c := conflicts[0]
	if c.Path != "internal/serve/server.go" {
		t.Errorf("conflict path = %q, want %q", c.Path, "internal/serve/server.go")
	}
	if len(c.Agents) != 2 || c.Agents[0] != "cc-1" || c.Agents[1] != "cod-2" {
		t.Errorf("conflict agents = %v, want [cc-1 cod-2]", c.Agents)
	}
}

// One agent editing one file repeatedly is not a conflict. This is what a
// shared-tree "attribute to every agent in the session" scheme would have
// reported as a critical three-way conflict.
func TestSingleAgentRepeatEditsAreNotAConflict(t *testing.T) {
	repo := newGitRepo(t)
	store := NewFileChangeStore(50)
	r := newTestRecorder(repo, "cc-1", store)
	sample(t, r)

	writeFile(t, repo, "tracked.txt", "edit one")
	sample(t, r)
	writeFile(t, repo, "tracked.txt", "edit two, longer than the first")
	sample(t, r)

	if conflicts := DetectConflicts(store.All()); len(conflicts) != 0 {
		t.Errorf("one agent's repeat edits reported %d conflicts, want 0: %+v", len(conflicts), conflicts)
	}
}

func TestGitRecorderNonRepoIsAnErrorNotAPanic(t *testing.T) {
	dir := t.TempDir()
	r := newTestRecorder(dir, "cc-1", NewFileChangeStore(10))
	if _, err := r.Sample(t.Context()); err == nil {
		t.Error("sampling a non-repository should report an error")
	}
}

func TestFileChangeStoreAddWrapsAtLimit(t *testing.T) {
	store := NewFileChangeStore(3)
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		store.Add(RecordedFileChange{
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Change:    FileChange{Path: name, Type: FileModified},
		})
	}

	all := store.All()
	if len(all) != 3 {
		t.Fatalf("store kept %d entries, want 3", len(all))
	}
	want := []string{"c", "d", "e"}
	for i, entry := range all {
		if entry.Change.Path != want[i] {
			t.Errorf("entry %d = %q, want %q (oldest-first after wrap)", i, entry.Change.Path, want[i])
		}
	}
}
