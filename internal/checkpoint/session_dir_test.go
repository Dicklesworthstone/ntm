package checkpoint

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// ntm#310: `display-message -p -t =session '#{pane_current_path}'` exits 0 with
// empty output (tmux 3.4, 3.6a), so checkpoints silently recorded an empty
// working_dir and skipped Git capture with clean-looking defaults. These tests
// pin both layers: the tmux target form and the resulting checkpoint fields.

func realPath(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return resolved
}

func newIsolatedTmuxSession(t *testing.T, prefix, workDir string) string {
	t.Helper()
	sessionName := prefix + time.Now().Format("150405000000")
	if err := tmux.CreateSession(sessionName, workDir); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	t.Cleanup(func() {
		if tmux.SessionExists(sessionName) {
			_ = tmux.KillSession(sessionName)
		}
	})
	return sessionName
}

func initGitRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "initial")
}

func TestGetSessionDir_UsesPaneQualifiedExactTarget(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	workDir := realPath(t, t.TempDir())
	sessionName := newIsolatedTmuxSession(t, "cpdir-", workDir)

	// Document the tmux behaviour the fix works around: the bare exact
	// session target answers a pane format with nothing. Not asserted, a
	// future tmux may fix it; the pane-qualified form is the contract.
	bare, bareErr := tmux.DefaultClient.Run("display-message", "-p", "-t", tmux.TargetSession(sessionName), "#{pane_current_path}")
	t.Logf("bare =session target: out=%q err=%v", strings.TrimSpace(bare), bareErr)

	qualified, err := tmux.DefaultClient.Run("display-message", "-p", "-t", tmux.SessionPaneTarget(sessionName), "#{pane_current_path}")
	if err != nil {
		t.Fatalf("display-message with pane-qualified target: %v", err)
	}
	if got := realPath(t, strings.TrimSpace(qualified)); got != workDir {
		t.Fatalf("pane-qualified target path = %q, want %q", got, workDir)
	}

	dir, err := getSessionDir(sessionName)
	if err != nil {
		t.Fatalf("getSessionDir: %v", err)
	}
	if got := realPath(t, dir); got != workDir {
		t.Fatalf("getSessionDir = %q, want %q", got, workDir)
	}

	// Exact matching is preserved: a session whose name merely extends this
	// one must not be selected by prefix.
	longer := newIsolatedTmuxSession(t, sessionName+"-x", t.TempDir())
	dir, err = getSessionDir(sessionName)
	if err != nil {
		t.Fatalf("getSessionDir with sibling %q present: %v", longer, err)
	}
	if got := realPath(t, dir); got != workDir {
		t.Fatalf("getSessionDir resolved to %q with sibling session present, want %q", got, workDir)
	}

	// A session that does not exist is an error, not an empty path.
	if _, err := getSessionDir(sessionName + "-missing"); err == nil {
		t.Fatal("getSessionDir on a missing session returned no error")
	}
}

func TestGetSessionDir_FollowsCurrentWindow(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	firstDir := realPath(t, t.TempDir())
	secondDir := realPath(t, t.TempDir())
	sessionName := newIsolatedTmuxSession(t, "cpwin-", firstDir)

	if err := tmux.DefaultClient.RunSilent("new-window", "-t", tmux.SessionOptionTarget(sessionName), "-c", secondDir); err != nil {
		t.Fatalf("new-window failed: %v", err)
	}
	dir, err := getSessionDir(sessionName)
	if err != nil {
		t.Fatalf("getSessionDir: %v", err)
	}
	if got := realPath(t, dir); got != secondDir {
		t.Fatalf("getSessionDir after new-window = %q, want current window dir %q", got, secondDir)
	}
}

func TestCapturer_Create_CapturesGitStateFromSessionDir(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	repoDir := realPath(t, t.TempDir())
	initGitRepoWithCommit(t, repoDir)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("modified\n"), 0o644); err != nil {
		t.Fatalf("modify README: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repoDir, "untracked"), 0o755); err != nil {
		t.Fatalf("mkdir untracked: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "untracked", "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	sessionName := newIsolatedTmuxSession(t, "cpgit-", repoDir)
	capturer := NewCapturerWithStorage(NewStorageWithDir(t.TempDir()))

	cp, err := capturer.Create(sessionName, "git-state", WithScrollbackLines(10))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := realPath(t, cp.WorkingDir); got != repoDir {
		t.Fatalf("WorkingDir = %q, want %q", got, repoDir)
	}
	if cp.WorkingDirError != "" {
		t.Fatalf("WorkingDirError = %q, want empty", cp.WorkingDirError)
	}
	if !cp.Git.Captured || !cp.Git.HasState() || cp.Git.Unavailable() {
		t.Fatalf("git state not marked captured: %+v", cp.Git)
	}
	if cp.Git.SkipReason != "" {
		t.Fatalf("SkipReason = %q, want empty", cp.Git.SkipReason)
	}
	if cp.Git.Branch == "" || cp.Git.Commit == "" {
		t.Fatalf("branch/commit empty: %+v", cp.Git)
	}
	if !cp.Git.IsDirty || cp.Git.UnstagedCount != 1 || cp.Git.UntrackedCount != 1 {
		t.Fatalf("dirty counts = dirty:%v staged:%d unstaged:%d untracked:%d, want dirty with 1 unstaged and 1 untracked",
			cp.Git.IsDirty, cp.Git.StagedCount, cp.Git.UnstagedCount, cp.Git.UntrackedCount)
	}
	if cp.Git.PatchFile == "" {
		t.Fatal("dirty tracked change did not produce a patch file")
	}
	if reason := DescribeGitUnavailable(cp); reason != "" {
		t.Fatalf("DescribeGitUnavailable = %q, want empty for captured state", reason)
	}
}

func TestCapturer_Create_GitDisabledIsRecorded(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	repoDir := realPath(t, t.TempDir())
	initGitRepoWithCommit(t, repoDir)
	sessionName := newIsolatedTmuxSession(t, "cpnogit-", repoDir)
	capturer := NewCapturerWithStorage(NewStorageWithDir(t.TempDir()))

	cp, err := capturer.Create(sessionName, "no-git", WithGitCapture(false), WithScrollbackLines(10))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if cp.Git.HasState() || !cp.Git.Unavailable() || cp.Git.SkipReason != GitSkipDisabled {
		t.Fatalf("git state = %+v, want unavailable with reason %q", cp.Git, GitSkipDisabled)
	}
}

func TestGitStateNotCaptured(t *testing.T) {
	if got := gitStateNotCaptured(true, nil); got != (GitState{}) {
		t.Fatalf("enabled with resolved dir = %+v, want zero value awaiting capture", got)
	}
	if got := gitStateNotCaptured(false, nil); got.SkipReason != GitSkipDisabled || got.Captured {
		t.Fatalf("disabled = %+v, want %q", got, GitSkipDisabled)
	}
	got := gitStateNotCaptured(true, os.ErrNotExist)
	if got.SkipReason != GitSkipWorkingDirUnavailable || got.SkipDetail != os.ErrNotExist.Error() || got.Captured {
		t.Fatalf("unresolved dir = %+v, want %q with detail", got, GitSkipWorkingDirUnavailable)
	}
	if !got.Unavailable() || got.HasState() {
		t.Fatalf("unresolved dir state must read as unavailable: %+v", got)
	}
}

func TestCaptureGitState_NonRepositoryIsMarkedSkipped(t *testing.T) {
	c := NewCapturerWithStorage(NewStorageWithDir(t.TempDir()))
	state, err := c.captureGitState(t.TempDir(), "session", "chk-1")
	if err != nil {
		t.Fatalf("captureGitState on plain dir: %v", err)
	}
	if state.Captured || state.SkipReason != GitSkipNotRepository || state.HasState() {
		t.Fatalf("plain dir state = %+v, want skip reason %q", state, GitSkipNotRepository)
	}
}

func TestCaptureGitState_RepositoryIsMarkedCaptured(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	c := NewCapturerWithStorage(NewStorageWithDir(t.TempDir()))
	state, err := c.captureGitState(repoDir, "session", "chk-1")
	if err != nil {
		t.Fatalf("captureGitState: %v", err)
	}
	if !state.Captured || state.SkipReason != "" || !state.HasState() || state.Unavailable() {
		t.Fatalf("repo state = %+v, want captured", state)
	}
}

// Checkpoints written before `captured` existed must keep reading as real git
// state, and their clean zero values must not be reported as unavailable.
func TestGitState_LegacyJSONWithoutCapturedFlag(t *testing.T) {
	var legacy GitState
	if err := json.Unmarshal([]byte(`{"branch":"main","commit":"abc123","is_dirty":false,"staged_count":0,"unstaged_count":0,"untracked_count":0}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if !legacy.HasState() || legacy.Unavailable() {
		t.Fatalf("legacy state with branch must count as captured: %+v", legacy)
	}

	var legacyEmpty GitState
	if err := json.Unmarshal([]byte(`{"branch":"","commit":"","is_dirty":false,"staged_count":0,"unstaged_count":0,"untracked_count":0}`), &legacyEmpty); err != nil {
		t.Fatalf("unmarshal legacy empty: %v", err)
	}
	if legacyEmpty.HasState() || legacyEmpty.Unavailable() {
		t.Fatalf("legacy empty state is undeclared, neither captured nor unavailable: %+v", legacyEmpty)
	}

	var skipped GitState
	if err := json.Unmarshal([]byte(`{"captured":false,"skip_reason":"working_dir_unavailable","skip_detail":"tmux reported no current path","branch":"","commit":""}`), &skipped); err != nil {
		t.Fatalf("unmarshal skipped: %v", err)
	}
	if skipped.HasState() || !skipped.Unavailable() {
		t.Fatalf("skipped state must read as unavailable: %+v", skipped)
	}
}

func TestDescribeGitUnavailable(t *testing.T) {
	if got := DescribeGitUnavailable(nil); got != "" {
		t.Fatalf("nil checkpoint = %q, want empty", got)
	}
	cp := &Checkpoint{
		WorkingDirError: "tmux reported no current path for the active pane of session s",
		Git:             GitState{SkipReason: GitSkipWorkingDirUnavailable, SkipDetail: "tmux reported no current path for the active pane of session s"},
	}
	want := GitSkipWorkingDirUnavailable + ": " + cp.WorkingDirError
	if got := DescribeGitUnavailable(cp); got != want {
		t.Fatalf("working dir unavailable = %q, want %q", got, want)
	}
	failed := &Checkpoint{Git: GitState{SkipReason: GitSkipCaptureFailed, SkipDetail: "getting git branch: boom"}}
	if got := DescribeGitUnavailable(failed); got != "capture_failed (getting git branch: boom)" {
		t.Fatalf("capture failed = %q", got)
	}
	captured := &Checkpoint{Git: GitState{Captured: true, Branch: "main", Commit: "abc"}}
	if got := DescribeGitUnavailable(captured); got != "" {
		t.Fatalf("captured = %q, want empty", got)
	}
}

func TestIntegrity_ReportsGitSkipReason(t *testing.T) {
	storage := NewStorageWithDir(t.TempDir())
	cp := &Checkpoint{
		Version:         CurrentVersion,
		ID:              GenerateID("skip"),
		Name:            "skip",
		SessionName:     "sess",
		CreatedAt:       time.Now(),
		WorkingDirError: "tmux reported no current path",
		Git:             GitState{SkipReason: GitSkipWorkingDirUnavailable, SkipDetail: "tmux reported no current path"},
		Session:         SessionState{Panes: []PaneState{{Index: 0, ID: "%0", Title: "p"}}},
		PaneCount:       1,
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	result := cp.Verify(storage)
	if result.Details["has_git_state"] != "false" {
		t.Fatalf("has_git_state = %q, want false", result.Details["has_git_state"])
	}
	if result.Details["git_skip_reason"] != GitSkipWorkingDirUnavailable {
		t.Fatalf("git_skip_reason = %q, want %q", result.Details["git_skip_reason"], GitSkipWorkingDirUnavailable)
	}
	foundGit, foundDir := false, false
	for _, w := range result.Warnings {
		if strings.Contains(w, "git state was not captured") && strings.Contains(w, GitSkipWorkingDirUnavailable) {
			foundGit = true
		}
		if strings.Contains(w, "checkpoint has no working_dir: tmux reported no current path") {
			foundDir = true
		}
	}
	if !foundGit || !foundDir {
		t.Fatalf("warnings = %v, want git skip and working_dir error surfaced", result.Warnings)
	}

	// Disabled capture is a choice, not a defect: no warning, reason still recorded.
	cp.Git = GitState{SkipReason: GitSkipDisabled}
	cp.WorkingDirError = ""
	cp.WorkingDir = t.TempDir()
	result = cp.Verify(storage)
	if result.Details["git_skip_reason"] != GitSkipDisabled {
		t.Fatalf("git_skip_reason = %q, want %q", result.Details["git_skip_reason"], GitSkipDisabled)
	}
	for _, w := range result.Warnings {
		if strings.Contains(w, "git state was not captured") {
			t.Fatalf("disabled capture must not warn: %v", result.Warnings)
		}
	}
}
