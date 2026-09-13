package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/resilience"
)

func TestBuildFileChangeRecordersSharedCheckoutUsesOneSessionRecorder(t *testing.T) {
	dir := t.TempDir()
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}

	recorders := buildFileChangeRecorders(t.Context(), manifest)
	if len(recorders) != 1 {
		t.Fatalf("shared checkout produced %d recorders, want 1", len(recorders))
	}
}

// Worktree isolation is what makes per-agent attribution honest: each agent owns
// a tree, so git can say who touched a file.
func TestBuildFileChangeRecordersOnePerAgentWorktree(t *testing.T) {
	dir := t.TempDir()
	for _, agent := range []string{"cc-1", "cod-2", "gmi-3"} {
		if err := os.MkdirAll(filepath.Join(dir, ".ntm", "worktrees", "sess", agent), 0o755); err != nil {
			t.Fatalf("mkdir worktree for %s: %v", agent, err)
		}
	}
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}

	recorders := buildFileChangeRecorders(t.Context(), manifest)
	if len(recorders) != 3 {
		t.Fatalf("three agent worktrees produced %d recorders, want 3", len(recorders))
	}
}

// A worktrees directory that exists but holds no agent trees must still record
// something rather than silently tracking nothing.
func TestBuildFileChangeRecordersEmptyWorktreeDirFallsBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ntm", "worktrees", "sess"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: dir}

	if got := len(buildFileChangeRecorders(t.Context(), manifest)); got != 1 {
		t.Errorf("empty worktree dir produced %d recorders, want 1 session-level fallback", got)
	}
}

func TestBuildFileChangeRecordersRejectsIncompleteManifest(t *testing.T) {
	for name, manifest := range map[string]*resilience.SpawnManifest{
		"nil":         nil,
		"no session":  {ProjectDir: t.TempDir()},
		"no data dir": {Session: "sess"},
	} {
		if got := buildFileChangeRecorders(t.Context(), manifest); got != nil {
			t.Errorf("%s manifest produced %d recorders, want none", name, len(got))
		}
	}
}

// Sampling is best-effort: a non-repository must be logged and skipped, never
// allowed to take down the monitor loop that drives it.
func TestSampleFileChangesToleratesFailuresAndNils(t *testing.T) {
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: t.TempDir()}
	recorders := buildFileChangeRecorders(t.Context(), manifest)

	sampleFileChanges(t.Context(), append(recorders, nil))
	sampleFileChanges(t.Context(), nil)
}

func TestDescribeFileChangeRecorders(t *testing.T) {
	manifest := &resilience.SpawnManifest{Session: "sess", ProjectDir: t.TempDir()}
	if got := describeFileChangeRecorders(nil); got == "" {
		t.Error("empty recorder set should still describe itself")
	}
	if got := describeFileChangeRecorders(buildFileChangeRecorders(t.Context(), manifest)); got == "" {
		t.Error("single recorder should describe itself")
	}
}
