package tracker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The rest of this package's ledger tests use a fake backend so they never touch
// the user's real state.db. That leaves the actual tracker -> state.Open path —
// the one that runs in production — unexercised, which is precisely the kind of
// gap that let the original bug ship. This test drives the real SQLite store,
// pointed at a throwaway directory.
func TestLedgerRoundTripsThroughRealStateStore(t *testing.T) {
	// DefaultPath prefers NTM_CONFIG, then XDG_CONFIG_HOME. Clear the former so
	// a developer's exported config cannot redirect this at their real DB.
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Restore the genuine backend for this test only; TestMain installs a fake.
	previous := openBackend
	openBackend = realOpenBackend
	t.Cleanup(func() { openBackend = previous })

	repo := newGitRepo(t)
	chdir(t, repo)

	writer := NewGitRecorder(GitRecorderConfig{
		Root:       repo,
		ProjectDir: repo,
		Session:    "sess",
		Identity:   "cc-1",
		Store:      NewFileChangeStore(50),
	})
	sample(t, writer)
	writeFile(t, repo, "internal/serve/server.go", "written by the monitor process")
	if n := sample(t, writer); n != 1 {
		t.Fatalf("recorder observed %d changes, want 1", n)
	}

	// Stand in for a separate reader process: nothing in this ring.
	original := GlobalFileChanges
	GlobalFileChanges = NewFileChangeStore(500)
	t.Cleanup(func() { GlobalFileChanges = original })

	changes := RecordedChangesSince(time.Now().Add(-time.Hour))
	if len(changes) != 1 {
		t.Fatalf("reader saw %d changes through the real store, want 1", len(changes))
	}
	if changes[0].Change.Path != "internal/serve/server.go" {
		t.Errorf("path = %q, want internal/serve/server.go", changes[0].Change.Path)
	}
	if len(changes[0].Agents) != 1 || changes[0].Agents[0] != "cc-1" {
		t.Errorf("agents = %v, want [cc-1]", changes[0].Agents)
	}
	if changes[0].Session != "sess" {
		t.Errorf("session = %q, want sess", changes[0].Session)
	}

	// And the whole point: two agents on one path become a reportable conflict
	// for a reader that recorded none of it itself.
	other := NewGitRecorder(GitRecorderConfig{
		Root:       repo,
		ProjectDir: repo,
		Session:    "sess",
		Identity:   "cod-2",
		Store:      NewFileChangeStore(50),
	})
	sample(t, other)
	writeFile(t, repo, "internal/serve/server.go", "and now a second agent, at a different length")
	sample(t, other)

	conflicts := ConflictsSince(time.Now().Add(-time.Hour), "")
	if len(conflicts) != 1 {
		t.Fatalf("detected %d conflicts through the real store, want 1: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].Path != "internal/serve/server.go" {
		t.Errorf("conflict path = %q", conflicts[0].Path)
	}
	if len(conflicts[0].Agents) != 2 {
		t.Errorf("conflict agents = %v, want both agents", conflicts[0].Agents)
	}
}

// A reader standing outside any recorded project sees nothing, rather than
// another project's conflicts.
func TestRealStoreReaderOutsideProjectSeesNothing(t *testing.T) {
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	previous := openBackend
	openBackend = realOpenBackend
	t.Cleanup(func() { openBackend = previous })

	repo := newGitRepo(t)
	writer := NewGitRecorder(GitRecorderConfig{
		Root: repo, ProjectDir: repo, Session: "sess", Identity: "cc-1",
		Store: NewFileChangeStore(50),
	})
	sample(t, writer)
	writeFile(t, repo, "tracked.txt", "recorded against that repo")
	sample(t, writer)

	elsewhere := filepath.Join(t.TempDir(), "unrelated")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	chdir(t, elsewhere)

	original := GlobalFileChanges
	GlobalFileChanges = NewFileChangeStore(500)
	t.Cleanup(func() { GlobalFileChanges = original })

	if changes := RecordedChangesSince(time.Now().Add(-time.Hour)); len(changes) != 0 {
		t.Errorf("reader outside the project saw %d changes: %+v", len(changes), changes)
	}
}
