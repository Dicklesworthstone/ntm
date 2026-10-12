package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func rollbackProbeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func rollbackProbe(t *testing.T) (string, *checkpoint.Checkpoint) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	rollbackProbeGit(t, root, "init", "-q", "-b", "main")
	rollbackProbeGit(t, root, "config", "user.name", "Recovery test")
	rollbackProbeGit(t, root, "config", "user.email", "recovery@example.invalid")
	rollbackProbeGit(t, root, "config", "commit.gpgsign", "false")
	file := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(file, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rollbackProbeGit(t, root, "add", ".")
	rollbackProbeGit(t, root, "commit", "-qm", "base")
	commit := rollbackProbeGit(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(file, []byte("saved\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", root, "diff", "--binary", "--full-index", commit, "--")
	patch, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint.Checkpoint{Version: checkpoint.CurrentVersion, ID: "rollback-probe", SessionName: "ntm-rollback-probe", WorkingDir: root, CreatedAt: time.Now(),
		Git: checkpoint.GitState{Captured: true, IsDirty: true, Commit: commit, PatchFile: checkpoint.GitPatchFile}}
	storage := checkpoint.NewStorage()
	if err := storage.SaveGitPatch(cp.SessionName, cp.ID, string(patch)); err != nil {
		t.Fatal(err)
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	return root, cp
}

func rollbackProbeOutput(t *testing.T, run func() error) (string, error) {
	t.Helper()
	oldJSON, oldStdout := jsonOutput, os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	jsonOutput, os.Stdout = true, w
	defer func() { jsonOutput, os.Stdout = oldJSON, oldStdout; _ = r.Close(); _ = w.Close() }()
	runErr := run()
	_ = w.Close()
	os.Stdout = oldStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

func TestRollbackCorruptPatchNeverReportsSuccess(t *testing.T) {
	root, cp := rollbackProbe(t)
	path := filepath.Join(checkpoint.NewStorage().CheckpointDir(cp.SessionName, cp.ID), checkpoint.GitPatchFile)
	if err := os.WriteFile(path, []byte("corrupt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := rollbackProbeGit(t, root, "status", "--porcelain")
	out, err := rollbackProbeOutput(t, func() error { return performRollback(cp, root, false, false) })
	if err == nil || strings.Contains(out, `"rolled_back":true`) || rollbackProbeGit(t, root, "status", "--porcelain") != before || rollbackProbeGit(t, root, "stash", "list") != "" {
		t.Fatalf("corrupt rollback changed state or reported success: %s, %v", out, err)
	}
}

func TestRollbackApplyFailurePreservesStashAndReturnsError(t *testing.T) {
	root, cp := rollbackProbe(t)
	hook := filepath.Join(root, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'concurrent write\\n' > tracked.txt\n"), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := rollbackProbeOutput(t, func() error { return performRollback(cp, root, false, false) })
	if err == nil || !strings.Contains(err.Error(), "rollback incomplete") || strings.Contains(out, `"rolled_back":true`) || rollbackProbeGit(t, root, "stash", "list") == "" {
		t.Fatalf("failed application lost stash or reported success: %s, %v", out, err)
	}
}

func TestRollbackCheckoutRefusesToOverwriteIgnoredWork(t *testing.T) {
	root, cp := rollbackProbe(t)
	rollbackProbeGit(t, root, "update-index", "--force-remove", "tracked.txt")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("tracked.txt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rollbackProbeGit(t, root, "add", ".gitignore")
	rollbackProbeGit(t, root, "commit", "-qm", "file now ignored")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("private current work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := gitCheckout(root, cp.Git.Commit); err == nil {
		t.Fatal("checkout overwrote an ignored working file")
	}
	got, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil || string(got) != "private current work\n" {
		t.Fatalf("ignored current work was lost: %q, %v", got, err)
	}
}

func TestCheckpointSaveIncludeUntrackedFlagReachesCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a tmux session")
	}
	testutil.RequireTmuxThrottled(t)
	root, _ := rollbackProbe(t)
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package captured\n"), 0644); err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("ntm-cli-code-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })
	cmd := newCheckpointSaveCmd()
	if err := cmd.Flags().Set("include-untracked", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("scrollback", "0"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, []string{session}); err != nil {
		t.Fatal(err)
	}
	cp, err := checkpoint.NewStorage().GetLatest(session)
	if err != nil || cp.Git.UntrackedCaptured != 1 || !cp.HasGitPatch() {
		t.Fatalf("CLI option did not reach saved Git snapshot: %+v, %v", cp, err)
	}
}

func TestCheckpointSaveRejectsIncompatibleGitFlagsBeforeLookup(t *testing.T) {
	cmd := newCheckpointSaveCmd()
	for _, name := range []string{"no-git", "include-untracked"} {
		if err := cmd.Flags().Set(name, "true"); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmd.RunE(cmd, []string{"absent-session"}); err == nil || !strings.Contains(err.Error(), "--include-untracked") {
		t.Fatalf("incompatible flags were not rejected before session lookup: %v", err)
	}
}
