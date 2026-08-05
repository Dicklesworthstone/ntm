package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const worktreeIntegrationTestTimeout = 2 * time.Minute

func runGitTestCommand(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), worktreeIntegrationTestTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// sequentialWorktreeIntegrationContext raises the production helper timeout
// only while a non-parallel integration test is running. Top-level sequential
// tests never overlap package parallel tests, so the scoped override is
// race-free and is restored before any parallel test resumes.
func sequentialWorktreeIntegrationContext(t *testing.T) context.Context {
	t.Helper()
	originalCommandTimeout := worktreeGitCommandTimeout
	worktreeGitCommandTimeout = worktreeIntegrationTestTimeout
	t.Cleanup(func() {
		worktreeGitCommandTimeout = originalCommandTimeout
	})

	ctx, cancel := context.WithTimeout(t.Context(), worktreeIntegrationTestTimeout)
	t.Cleanup(cancel)
	return ctx
}

// setupGitRepo creates a temporary git repo with an initial commit.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()

	cmds := [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		if out, err := runGitTestCommand(t, tmp, args...); err != nil {
			t.Skipf("%v failed: %v\n%s", args, err, out)
		}
	}
	return tmp
}

func assertStringEqual(t *testing.T, got, want string) {
	t.Helper()
	if strings.Compare(got, want) != 0 {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func assertStringNotEqual(t *testing.T, got, unwanted string) {
	t.Helper()
	if strings.Compare(got, unwanted) == 0 {
		t.Fatalf("got %q, want a distinct value", got)
	}
}

func TestIsGitRepository(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	missing := filepath.Join(tmp, "missing")
	if isRepository, err := IsGitRepository(t.Context(), missing); err != nil {
		t.Fatalf("IsGitRepository missing dir: %v", err)
	} else if isRepository {
		t.Fatal("expected missing dir to not be a git repo")
	}

	if isRepository, err := IsGitRepository(t.Context(), tmp); err != nil {
		t.Fatalf("IsGitRepository temp dir: %v", err)
	} else if isRepository {
		t.Fatal("expected temp dir to not be a git repo")
	}

	if out, err := runGitTestCommand(t, tmp, "init"); err != nil {
		t.Skipf("git init failed, skipping test: %v\n%s", err, out)
	}

	if isRepository, err := IsGitRepository(t.Context(), tmp); err != nil {
		t.Fatalf("IsGitRepository initialized repo: %v", err)
	} else if !isRepository {
		t.Fatal("expected directory to be detected as git repo after init")
	}
}

func TestIsGitRepositoryRequiresAndPropagatesCallerContext(t *testing.T) {
	tmp := t.TempDir()
	if isRepository, err := IsGitRepository(nil, tmp); isRepository || err == nil || !strings.Contains(err.Error(), "requires a command context") {
		t.Fatalf("nil context result=(%t, %v)", isRepository, err)
	}
	canceledCtx, cancelCanceled := context.WithCancel(t.Context())
	cancelCanceled()
	if isRepository, err := IsGitRepository(canceledCtx, tmp); isRepository || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context result=(%t, %v), want context.Canceled", isRepository, err)
	}

	stateDir := t.TempDir()
	started := filepath.Join(stateDir, "started")
	fakeGit := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
: > "$WORKTREE_TEST_GIT_STARTED"
exec sleep 30
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write repository-detection git: %v", err)
	}
	t.Setenv("WORKTREE_TEST_GIT_STARTED", started)
	t.Setenv("PATH", filepath.Dir(fakeGit)+string(os.PathListSeparator)+os.Getenv("PATH"))

	type repositoryResult struct {
		isRepository bool
		err          error
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan repositoryResult, 1)
	go func() {
		isRepository, err := IsGitRepository(ctx, tmp)
		result <- repositoryResult{isRepository: isRepository, err: err}
	}()
	waitForWorktreeTestPath(t, started)
	cancel()

	select {
	case got := <-result:
		if got.isRepository || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("in-flight cancellation result=(%t, %v), want context.Canceled", got.isRepository, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("IsGitRepository did not join canceled git command")
	}
}

func TestCanonicalSessionKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		want  string
		want2 string
	}{
		{"empty falls back", "", "session", ""},
		{"preserves identity-bearing suffix", "alpha-team-claude-12", "alpha-team-claude-12", ""},
		{"preserves repeated safe separators", "alpha--team-claude-1", "alpha--team-claude-1", ""},
		{"normalizes leading dot for git-ref safety", ".alpha--team-claude-1", "alpha--team-claude-1", ""},
		{"normalizes trailing dot for git-ref safety", "alpha--team-claude-1.", "alpha--team-claude-1", ""},
		{"normalizes disallowed characters", "alpha team/claude:12", "alpha-team-claude-12", ""},
		{"collapses repeated separators", "alpha---team***claude", "alpha-team-claude", ""},
		{"normalizes invalid git ref dot run", "alpha..team", "alpha-team", ""},
		{"normalizes invalid git ref .lock suffix", "alpha-team.lock", "alpha-team-lock", ""},
		{"distinct pane IDs remain distinct", "alpha-team-claude-1", "alpha-team-claude-1", "alpha-team-claude-2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := canonicalSessionKey(tc.in)
			assertStringEqual(t, got, tc.want)
			if tc.want2 != "" {
				got2 := canonicalSessionKey(tc.want2)
				assertStringNotEqual(t, got, got2)
			}
		})
	}
}

func TestCanonicalAgentKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		want  string
		want2 string
	}{
		{"empty falls back", "", "agent", ""},
		{"preserves normal agent type", "claude", "claude", ""},
		{"preserves repeated safe separators", "alpha--team", "alpha--team", ""},
		{"normalizes leading/trailing dot for git-ref safety", ".alpha--team.", "alpha--team", ""},
		{"normalizes path separators", "../evil/type", "evil-type", ""},
		{"collapses punctuation", "***codex///agent***", "codex-agent", ""},
		{"normalizes invalid git ref dot run", "alpha..team", "alpha-team", ""},
		{"normalizes invalid git ref .lock suffix", "alpha.lock", "alpha-lock", ""},
		{"distinct safe IDs remain distinct", "alpha--team", "alpha--team", "alpha-team"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := canonicalAgentKey(tc.in)
			assertStringEqual(t, got, tc.want)
			if tc.want2 != "" {
				got2 := canonicalAgentKey(tc.want2)
				assertStringNotEqual(t, got, got2)
			}
		})
	}
}

func TestWorktreeManager_ProvisionWorktreeSanitizesAgentName(t *testing.T) {
	repo := setupGitRepo(t)
	wm, err := NewWorktreeManager(t.Context(), repo)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := sequentialWorktreeIntegrationContext(t)
	registerWorktreeCleanup(t, wm, "../evil/type", "sess/one")
	info, err := wm.ProvisionWorktree(ctx, "../evil/type", "sess/one")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	assertStringEqual(t, filepath.Base(info.Path), "agent-9-evil-type-session-8-sess-one")
	assertStringEqual(t, info.Branch, "agent/evil-type/sess-one")
	assertStringEqual(t, info.Agent, "evil-type")
}

// bd-2gutl: even after character canonicalization, ref components like ".."
// and ".lock" remain git-invalid and must be normalized before branch creation.
func TestWorktreeManager_ProvisionWorktreeNormalizesInvalidRefComponentPatterns(t *testing.T) {
	repo := setupGitRepo(t)
	wm, err := NewWorktreeManager(t.Context(), repo)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := sequentialWorktreeIntegrationContext(t)
	registerWorktreeCleanup(t, wm, "alpha..team.lock", "sess..one.lock")
	info, err := wm.ProvisionWorktree(ctx, "alpha..team.lock", "sess..one.lock")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	assertStringEqual(t, filepath.Base(info.Path), "agent-15-alpha-team-lock-session-13-sess-one-lock")
	assertStringEqual(t, info.Branch, "agent/alpha-team-lock/sess-one-lock")
	assertStringEqual(t, info.Agent, "alpha-team-lock")
}

func TestWorktreeManager_ProvisionWorktreeDistinctSafeAgentKeysDoNotAlias(t *testing.T) {
	repo := setupGitRepo(t)
	wm, err := NewWorktreeManager(t.Context(), repo)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := sequentialWorktreeIntegrationContext(t)
	registerWorktreeCleanup(t, wm, "alpha--team", "sess/one")
	first, err := wm.ProvisionWorktree(ctx, "alpha--team", "sess/one")
	if err != nil {
		t.Fatalf("ProvisionWorktree first: %v", err)
	}
	registerWorktreeCleanup(t, wm, "alpha-team", "sess/one")
	second, err := wm.ProvisionWorktree(ctx, "alpha-team", "sess/one")
	if err != nil {
		t.Fatalf("ProvisionWorktree second: %v", err)
	}

	assertStringEqual(t, filepath.Base(first.Path), "agent-11-alpha--team-session-8-sess-one")
	assertStringEqual(t, filepath.Base(second.Path), "agent-10-alpha-team-session-8-sess-one")
	assertStringNotEqual(t, first.Path, second.Path)
	assertStringNotEqual(t, first.Branch, second.Branch)
	assertStringNotEqual(t, first.Agent, second.Agent)
}

func TestWorktreeManager_ProvisionWorktreeSeparatesAgentAndSessionBoundaries(t *testing.T) {
	repo := setupGitRepo(t)
	wm, err := NewWorktreeManager(t.Context(), repo)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	// The race suite can heavily delay short-lived git subprocesses. Keep this
	// real integration test sequential and give every command a generous,
	// still-bounded fixture deadline.
	ctx := sequentialWorktreeIntegrationContext(t)
	registerWorktreeCleanup(t, wm, "a-b", "c")
	first, err := wm.ProvisionWorktree(ctx, "a-b", "c")
	if err != nil {
		t.Fatalf("ProvisionWorktree first: %v", err)
	}

	registerWorktreeCleanup(t, wm, "a", "b-c")
	second, err := wm.ProvisionWorktree(ctx, "a", "b-c")
	if err != nil {
		t.Fatalf("ProvisionWorktree second: %v", err)
	}

	assertStringEqual(t, filepath.Base(first.Path), "agent-3-a-b-session-1-c")
	assertStringEqual(t, filepath.Base(second.Path), "agent-1-a-session-3-b-c")
	assertStringNotEqual(t, first.Path, second.Path)
	assertStringNotEqual(t, first.Branch, second.Branch)
	assertStringNotEqual(t, first.Agent, second.Agent)

	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("expected 2 distinct worktrees, got %d", len(worktrees))
	}
}

func registerWorktreeCleanup(t *testing.T, wm *WorktreeManager, agentName, sessionID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeIntegrationTestTimeout)
		defer cancel()
		if err := wm.RemoveWorktree(ctx, agentName, sessionID); err != nil {
			t.Errorf("cleanup worktree %s/%s: %v", agentName, sessionID, err)
		}
	})
}

func TestWorktreeManager_ProvisionAndList_PreservesSanitizedHyphenatedAgent(t *testing.T) {
	repo := setupGitRepo(t)
	wm, err := NewWorktreeManager(t.Context(), repo)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := sequentialWorktreeIntegrationContext(t)
	registerWorktreeCleanup(t, wm, "../evil/type", "sess/one")
	info, err := wm.ProvisionWorktree(ctx, "../evil/type", "sess/one")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	assertStringEqual(t, info.Agent, "evil-type")

	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(worktrees))
	}
	assertStringEqual(t, worktrees[0].Agent, "evil-type")
}

func TestWorktreeManager_worktreeExists(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	wm := &WorktreeManager{baseRepo: tmp}

	worktreePath := filepath.Join(tmp, ".git", "worktrees", "agent-cc-123")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree path: %v", err)
	}

	exists, err := wm.worktreeExists(t.Context(), "agent-cc-123")
	if err != nil {
		t.Fatalf("worktreeExists error: %v", err)
	}
	if !exists {
		t.Fatal("expected worktree to exist")
	}

	exists, err = wm.worktreeExists(t.Context(), "missing")
	if err != nil {
		t.Fatalf("worktreeExists error: %v", err)
	}
	if exists {
		t.Fatal("expected missing worktree to return false")
	}
}

func TestWorktreeManager_getWorktreeInfo_UsesCommandContextForBranchLookup(t *testing.T) {
	repo := setupGitRepo(t)
	wm := &WorktreeManager{baseRepo: repo}

	worktreeName := "agent-timeout-branch"
	workingDir := filepath.Join(repo, "..", worktreeName)
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("mkdir working dir: %v", err)
	}

	fakeDir := t.TempDir()
	fakeGit := filepath.Join(fakeDir, "git")
	// Simulate a long-running git call; the caller's shorter deadline must win
	// over the manager's command timeout.
	script := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+oldPath)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := wm.getWorktreeInfo(ctx, worktreeName)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("getWorktreeInfo error = %v, want context deadline", err)
	}
	if !strings.Contains(err.Error(), "failed to get branch") {
		t.Fatalf("error = %q, want branch lookup failure", err.Error())
	}
	maxExpected := 2 * time.Second
	if elapsed > maxExpected {
		t.Fatalf("getWorktreeInfo elapsed %v, expected timeout-bounded return within %v", elapsed, maxExpected)
	}
}

func TestWorktreeManagerOperationsRejectNilAndCanceledContextsBeforeGit(t *testing.T) {
	baseRepo := t.TempDir()
	wm := &WorktreeManager{projectDir: baseRepo, baseRepo: baseRepo}
	gitLog := filepath.Join(t.TempDir(), "git.log")
	fakeGit := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$WORKTREE_TEST_GIT_LOG"
exit 91
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write fail-if-called git: %v", err)
	}
	t.Setenv("WORKTREE_TEST_GIT_LOG", gitLog)
	t.Setenv("PATH", filepath.Dir(fakeGit)+string(os.PathListSeparator)+os.Getenv("PATH"))

	operations := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "provision",
			run: func(ctx context.Context) error {
				_, err := wm.ProvisionWorktree(ctx, "cc", "cancel-before-provision")
				return err
			},
		},
		{
			name: "list",
			run: func(ctx context.Context) error {
				_, err := wm.ListWorktrees(ctx)
				return err
			},
		},
		{name: "remove", run: func(ctx context.Context) error {
			return wm.RemoveWorktree(ctx, "cc", "cancel-before-remove")
		}},
		{name: "cleanup", run: func(ctx context.Context) error {
			return wm.CleanupStaleWorktrees(ctx, 0)
		}},
		{name: "sync", run: func(ctx context.Context) error {
			return wm.SyncWorktree(ctx, baseRepo)
		}},
	}

	for _, operation := range operations {
		t.Run(operation.name+" nil", func(t *testing.T) {
			err := operation.run(nil)
			if err == nil || !strings.Contains(err.Error(), "requires a command context") {
				t.Fatalf("nil context error = %v", err)
			}
		})
		t.Run(operation.name+" canceled", func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := operation.run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled context error = %v, want context.Canceled", err)
			}
		})
	}

	if data, err := os.ReadFile(gitLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected contexts invoked git: err=%v log=%s", err, data)
	}
}

func TestWorktreeManagerProvisionCancellationStopsBeforeMutation(t *testing.T) {
	baseRepo := t.TempDir()
	wm := &WorktreeManager{projectDir: baseRepo, baseRepo: baseRepo}
	stateDir := t.TempDir()
	gitLog := filepath.Join(stateDir, "git.log")
	started := filepath.Join(stateDir, "started")
	mutation := filepath.Join(stateDir, "mutation")
	fakeGit := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$WORKTREE_TEST_GIT_LOG"
if [ "${1:-}" = "rev-parse" ] && [ "${2:-}" = "--abbrev-ref" ]; then
  : > "$WORKTREE_TEST_GIT_STARTED"
  exec sleep 30
fi
: > "$WORKTREE_TEST_GIT_MUTATION"
exit 92
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write blocking git: %v", err)
	}
	t.Setenv("WORKTREE_TEST_GIT_LOG", gitLog)
	t.Setenv("WORKTREE_TEST_GIT_STARTED", started)
	t.Setenv("WORKTREE_TEST_GIT_MUTATION", mutation)
	t.Setenv("PATH", filepath.Dir(fakeGit)+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := wm.ProvisionWorktree(ctx, "cc", "blocking-provision")
		result <- err
	}()
	waitForWorktreeTestPath(t, started)
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ProvisionWorktree error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ProvisionWorktree did not join canceled git command")
	}

	data, err := os.ReadFile(gitLog)
	if err != nil {
		t.Fatalf("read git log: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "rev-parse --abbrev-ref HEAD" {
		t.Fatalf("git calls = %q, want only blocking branch lookup", got)
	}
	if _, err := os.Stat(mutation); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provision cancellation reached git mutation: %v", err)
	}
	workingDir := filepath.Join(baseRepo, "..", worktreeNameForKeys("cc", "blocking-provision"))
	if _, err := os.Stat(workingDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provision cancellation created worktree path: %v", err)
	}
}

func TestWorktreeManagerProvisionCancellationReportsRetainedCheckout(t *testing.T) {
	baseRepo := setupGitRepo(t)
	wm := &WorktreeManager{projectDir: baseRepo, baseRepo: baseRepo}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("resolve real git: %v", err)
	}
	stateDir := t.TempDir()
	gitLog := filepath.Join(stateDir, "git.log")
	addCompleted := filepath.Join(stateDir, "add-completed")
	fakeGit := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$WORKTREE_TEST_GIT_LOG"
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "add" ]; then
  "$WORKTREE_TEST_REAL_GIT" "$@"
  status=$?
  if [ "$status" -ne 0 ]; then exit "$status"; fi
  : > "$WORKTREE_TEST_ADD_COMPLETED"
  exec sleep 30
fi
exec "$WORKTREE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write add-then-block git: %v", err)
	}
	t.Setenv("WORKTREE_TEST_GIT_LOG", gitLog)
	t.Setenv("WORKTREE_TEST_REAL_GIT", realGit)
	t.Setenv("WORKTREE_TEST_ADD_COMPLETED", addCompleted)
	t.Setenv("PATH", filepath.Dir(fakeGit)+string(os.PathListSeparator)+os.Getenv("PATH"))

	type provisionResult struct {
		info *WorktreeInfo
		err  error
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan provisionResult, 1)
	go func() {
		info, provisionErr := wm.ProvisionWorktree(ctx, "cc", "retained-after-cancel")
		result <- provisionResult{info: info, err: provisionErr}
	}()
	waitForWorktreeTestPath(t, addCompleted)
	cancel()

	var got provisionResult
	select {
	case got = <-result:
	case <-time.After(4 * time.Second):
		t.Fatal("ProvisionWorktree did not join add-then-block git command")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("ProvisionWorktree error = %v, want context.Canceled", got.err)
	}
	wantPath := filepath.Join(baseRepo, "..", worktreeNameForKeys("cc", "retained-after-cancel"))
	wantBranch := "agent/cc/retained-after-cancel"
	if got.info == nil || got.info.Path != wantPath || got.info.Branch != wantBranch || got.info.Agent != "cc" {
		t.Fatalf("retained worktree info = %+v, want path=%s branch=%s agent=cc", got.info, wantPath, wantBranch)
	}
	if !strings.Contains(got.err.Error(), "checkout retained at "+wantPath) || !strings.Contains(got.err.Error(), "branch "+wantBranch) {
		t.Fatalf("retained worktree error omits path/branch evidence: %v", got.err)
	}
	if stat, err := os.Stat(wantPath); err != nil || !stat.IsDir() {
		t.Fatalf("retained worktree path missing: stat=%v err=%v", stat, err)
	}
	if stat, err := os.Stat(filepath.Join(wantPath, ".git")); err != nil || stat.IsDir() {
		t.Fatalf("retained checkout .git marker invalid: stat=%v err=%v", stat, err)
	}

	data, err := os.ReadFile(gitLog)
	if err != nil {
		t.Fatalf("read retained checkout git log: %v", err)
	}
	logText := string(data)
	if strings.Count(logText, "worktree add") != 1 || strings.Contains(logText, "rev-parse HEAD") {
		t.Fatalf("retained checkout crossed post-add cancellation boundary: %q", logText)
	}
}

func TestWorktreeManagerCleanupCancellationStopsFallbackAndNextRemoval(t *testing.T) {
	baseRepo := t.TempDir()
	wm := &WorktreeManager{projectDir: baseRepo, baseRepo: baseRepo}
	stateDir := t.TempDir()
	firstPath := filepath.Join(stateDir, "agent-first")
	secondPath := filepath.Join(stateDir, "agent-second")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create listed worktree %s: %v", path, err)
		}
		old := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("age listed worktree %s: %v", path, err)
		}
	}

	gitLog := filepath.Join(stateDir, "git.log")
	started := filepath.Join(stateDir, "started")
	fallbackMutation := filepath.Join(stateDir, "fallback-mutation")
	fakeGit := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$WORKTREE_TEST_GIT_LOG"
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "list" ]; then
  printf 'worktree %s\nHEAD aaa111\nbranch refs/heads/agent/cc/first\n\n' "$WORKTREE_TEST_FIRST_PATH"
  printf 'worktree %s\nHEAD bbb222\nbranch refs/heads/agent/cod/second\n' "$WORKTREE_TEST_SECOND_PATH"
  exit 0
fi
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "remove" ]; then
  : > "$WORKTREE_TEST_GIT_STARTED"
  printf 'not a working tree\n' >&2
  exec sleep 30
fi
: > "$WORKTREE_TEST_GIT_FALLBACK_MUTATION"
exit 93
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write cleanup git: %v", err)
	}
	t.Setenv("WORKTREE_TEST_GIT_LOG", gitLog)
	t.Setenv("WORKTREE_TEST_GIT_STARTED", started)
	t.Setenv("WORKTREE_TEST_GIT_FALLBACK_MUTATION", fallbackMutation)
	t.Setenv("WORKTREE_TEST_FIRST_PATH", firstPath)
	t.Setenv("WORKTREE_TEST_SECOND_PATH", secondPath)
	t.Setenv("PATH", filepath.Dir(fakeGit)+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- wm.CleanupStaleWorktrees(ctx, 0) }()
	waitForWorktreeTestPath(t, started)
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CleanupStaleWorktrees error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CleanupStaleWorktrees did not join canceled git command")
	}

	data, err := os.ReadFile(gitLog)
	if err != nil {
		t.Fatalf("read cleanup git log: %v", err)
	}
	logText := string(data)
	if strings.Count(logText, "worktree remove") != 1 || !strings.Contains(logText, firstPath) || strings.Contains(logText, secondPath+"\n") {
		t.Fatalf("cleanup git calls crossed cancellation boundary: %q", logText)
	}
	if _, err := os.Stat(fallbackMutation); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup cancellation reached branch fallback or later mutation: %v", err)
	}
}

// waitForWorktreeTestPath blocks until another goroutine creates the sentinel
// file, or the budget expires.
//
// The budget scales: it is waiting on a goroutine that shells out to git, and
// a fixed 3s assumed an idle machine. On a box also running an agent swarm the
// wait expired while the code under test was behaving correctly, which is what
// made these four tests the most frequent false failures in the suite
// (bd-hzmk0). Raise it with NTM_TEST_TIMEOUT_SCALE.
func waitForWorktreeTestPath(t *testing.T, path string) {
	t.Helper()
	budget := testutil.ScaleTimeout(3 * time.Second)
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s after %v (set %s to scale test budgets on a loaded machine)",
		path, budget, testutil.TimeoutScaleEnv)
}

func TestWorktreeManager_parseWorktreeList(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	agentPath := filepath.Join(tmp, "agent-cc-123")
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
		t.Fatalf("mkdir agent path: %v", err)
	}
	otherPath := filepath.Join(tmp, "normal")
	if err := os.MkdirAll(otherPath, 0o755); err != nil {
		t.Fatalf("mkdir other path: %v", err)
	}

	modTime := time.Unix(1700000000, 0)
	if err := os.Chtimes(agentPath, modTime, modTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	output := fmt.Sprintf(
		"worktree %s\nHEAD abcdef\nbranch refs/heads/agent/cc/abc123\n\nworktree %s\nHEAD 111111\nbranch refs/heads/main\n",
		agentPath,
		otherPath,
	)

	wm := &WorktreeManager{}
	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList error: %v", err)
	}

	if len(worktrees) != 1 {
		t.Fatalf("expected 1 agent worktree, got %d", len(worktrees))
	}

	wt := worktrees[0]
	if wt.Path != agentPath {
		t.Errorf("Path = %q, want %q", wt.Path, agentPath)
	}
	if wt.Branch != "agent/cc/abc123" {
		t.Errorf("Branch = %q, want %q", wt.Branch, "agent/cc/abc123")
	}
	if wt.Commit != "abcdef" {
		t.Errorf("Commit = %q, want %q", wt.Commit, "abcdef")
	}
	if wt.Agent != "cc" {
		t.Errorf("Agent = %q, want %q", wt.Agent, "cc")
	}
	if diff := wt.LastUsed.Sub(modTime); diff > time.Second || diff < -time.Second {
		t.Errorf("LastUsed = %v, want ~%v (diff %v)", wt.LastUsed, modTime, diff)
	}
}

func TestNewWorktreeManager(t *testing.T) {
	t.Parallel()

	t.Run("nil context", func(t *testing.T) {
		_, err := NewWorktreeManager(nil, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "requires a command context") {
			t.Fatalf("NewWorktreeManager nil context error = %v", err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := NewWorktreeManager(ctx, t.TempDir())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("NewWorktreeManager canceled context error = %v", err)
		}
	})

	t.Run("valid git repo", func(t *testing.T) {
		t.Parallel()
		tmp := setupGitRepo(t)

		wm, err := NewWorktreeManager(t.Context(), tmp)
		if err != nil {
			t.Fatalf("NewWorktreeManager: %v", err)
		}
		if wm.projectDir != tmp {
			t.Errorf("projectDir = %q, want %q", wm.projectDir, tmp)
		}
		if wm.baseRepo != tmp {
			t.Errorf("baseRepo = %q, want %q", wm.baseRepo, tmp)
		}
	})

	t.Run("non-git directory", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()

		_, err := NewWorktreeManager(t.Context(), tmp)
		if err == nil {
			t.Fatal("expected error for non-git directory")
		}
		if !strings.Contains(err.Error(), "not a git repository") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestWorktreeManager_parseWorktreeList_EmptyInput(t *testing.T) {
	t.Parallel()

	wm := &WorktreeManager{}

	worktrees, err := wm.parseWorktreeList("")
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 worktrees for empty input, got %d", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_WhitespaceOnly(t *testing.T) {
	t.Parallel()

	wm := &WorktreeManager{}

	worktrees, err := wm.parseWorktreeList("   \n\n  \n  ")
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 worktrees for whitespace input, got %d", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_NoAgentWorktrees(t *testing.T) {
	t.Parallel()

	wm := &WorktreeManager{}
	output := "worktree /tmp/myproject\nHEAD abc123\nbranch refs/heads/main\n\nworktree /tmp/feature\nHEAD def456\nbranch refs/heads/feature\n"

	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 agent worktrees, got %d", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_IgnoresAgentTextInParentPath(t *testing.T) {
	t.Parallel()

	wm := &WorktreeManager{}
	output := "worktree /tmp/my-agent-repo/normal\nHEAD abc123\nbranch refs/heads/main\n"

	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected parent path containing agent- to be ignored, got %d worktrees", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_ExcludesPrimaryCheckoutOnAgentBranch(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	wm := &WorktreeManager{baseRepo: repoPath}
	output := fmt.Sprintf("worktree %s\nHEAD abc123\nbranch refs/heads/agent/evil-type/sess-one\n", repoPath)

	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected primary checkout path to be excluded, got %d worktrees", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_ExcludesPrimaryCheckoutWithSymlinkBaseRepo(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	realRepoPath := filepath.Join(tmp, "repo-real")
	if err := os.MkdirAll(realRepoPath, 0o755); err != nil {
		t.Fatalf("mkdir real repo: %v", err)
	}

	symlinkRepoPath := filepath.Join(tmp, "repo-link")
	if err := os.Symlink(realRepoPath, symlinkRepoPath); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	wm := &WorktreeManager{baseRepo: symlinkRepoPath}
	output := fmt.Sprintf("worktree %s\nHEAD abc123\nbranch refs/heads/agent/evil-type/sess-one\n", realRepoPath)

	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected primary checkout path to be excluded for symlinked base repo, got %d worktrees", len(worktrees))
	}
}

func TestWorktreeManager_parseWorktreeList_IncludesAgentBranchWithoutAgentBasename(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	worktreePath := filepath.Join(tmp, "normal-name")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	wm := &WorktreeManager{}
	output := fmt.Sprintf("worktree %s\nHEAD abc123\nbranch refs/heads/agent/evil-type/sess-one\n", worktreePath)

	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("expected agent branch worktree, got %d worktrees", len(worktrees))
	}
	assertStringEqual(t, worktrees[0].Agent, "evil-type")
}

func TestWorktreeManager_parseWorktreeList_MultipleAgents(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path1 := filepath.Join(tmp, "agent-cc-sess1")
	path2 := filepath.Join(tmp, "agent-cod-sess2")
	path3 := filepath.Join(tmp, "agent-gmi-sess3")
	for _, p := range []string{path1, path2, path3} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	output := fmt.Sprintf(
		"worktree %s\nHEAD aaa111\nbranch refs/heads/agent/cc/sess1\n\n"+
			"worktree %s\nHEAD bbb222\nbranch refs/heads/agent/cod/sess2\n\n"+
			"worktree %s\nHEAD ccc333\nbranch refs/heads/agent/gmi/sess3\n",
		path1, path2, path3,
	)

	wm := &WorktreeManager{}
	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 3 {
		t.Fatalf("expected 3 worktrees, got %d", len(worktrees))
	}

	agents := map[string]bool{}
	for _, wt := range worktrees {
		agents[wt.Agent] = true
	}
	for _, expected := range []string{"cc", "cod", "gmi"} {
		if !agents[expected] {
			t.Errorf("expected agent %q in results", expected)
		}
	}
}

func TestWorktreeManager_parseWorktreeList_DetachedHead(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	agentPath := filepath.Join(tmp, "agent-cc-detached")
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Detached HEAD has no branch line, instead "HEAD abc123" followed by "detached"
	output := fmt.Sprintf("worktree %s\nHEAD abc123\ndetached\n", agentPath)

	wm := &WorktreeManager{}
	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(worktrees))
	}
	if worktrees[0].Branch != "" {
		t.Errorf("expected empty branch for detached HEAD, got %q", worktrees[0].Branch)
	}
}

func TestWorktreeManager_parseWorktreeList_AgentNameExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		basename  string
		branchRef string
		wantAgent string
	}{
		{"simple fallback", "agent-cc-12345678", "refs/heads/main", "cc"},
		{"codex fallback", "agent-cod-abcdefgh", "refs/heads/main", "cod"},
		{"gemini fallback", "agent-gmi-sessid12", "refs/heads/main", "gmi"},
		{"no dash after agent fallback", "agent-onlyone", "refs/heads/main", "onlyone"},
		{"length-prefixed fallback", "agent-9-evil-type-session-8-sess-one", "refs/heads/main", "evil-type"},
		{"length-prefixed fallback with marker in agent", "agent-15-alpha-session-x-session-1-y", "refs/heads/main", "alpha-session-x"},
		{"hyphenated canonical key from branch", "agent-evil-type-sess-one", "refs/heads/agent/evil-type/sess-one", "evil-type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmp := t.TempDir()
			agentPath := filepath.Join(tmp, tt.basename)
			if err := os.MkdirAll(agentPath, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			output := fmt.Sprintf("worktree %s\nHEAD abc123\nbranch %s\n", agentPath, tt.branchRef)

			wm := &WorktreeManager{}
			worktrees, err := wm.parseWorktreeList(output)
			if err != nil {
				t.Fatalf("parseWorktreeList: %v", err)
			}
			if len(worktrees) != 1 {
				t.Fatalf("expected 1 worktree, got %d", len(worktrees))
			}
			if worktrees[0].Agent != tt.wantAgent {
				t.Errorf("Agent = %q, want %q", worktrees[0].Agent, tt.wantAgent)
			}
		})
	}
}

func TestWorktreeManager_parseWorktreeList_NonExistentPath(t *testing.T) {
	t.Parallel()

	// Path that contains "agent-" but doesn't exist on disk
	output := "worktree /nonexistent/agent-cc-test1234\nHEAD abc123\nbranch refs/heads/agent/cc/test1234\n"

	wm := &WorktreeManager{}
	worktrees, err := wm.parseWorktreeList(output)
	if err != nil {
		t.Fatalf("parseWorktreeList: %v", err)
	}
	// Should still parse the worktree (os.Stat failure means LastUsed defaults to now)
	if len(worktrees) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(worktrees))
	}
	// LastUsed should be approximately now since stat failed
	if time.Since(worktrees[0].LastUsed) > 5*time.Second {
		t.Errorf("LastUsed should be ~now for non-existent path, got %v", worktrees[0].LastUsed)
	}
}

// Integration tests using real git repos

func TestWorktreeManager_ProvisionAndList(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision a worktree
	info, err := wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	if info.Agent != "cc" {
		t.Errorf("Agent = %q, want %q", info.Agent, "cc")
	}
	if !strings.HasPrefix(info.Branch, "agent/cc/") {
		t.Errorf("Branch = %q, want prefix %q", info.Branch, "agent/cc/")
	}
	if info.Commit == "" {
		t.Error("expected non-empty commit hash")
	}
	if info.Path == "" {
		t.Error("expected non-empty path")
	}

	// Verify the path was actually created
	if _, err := os.Stat(info.Path); err != nil {
		t.Errorf("worktree path does not exist: %v", err)
	}

	// List worktrees
	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("expected 1 worktree, got %d", len(worktrees))
	}
	if worktrees[0].Agent != "cc" {
		t.Errorf("listed Agent = %q, want %q", worktrees[0].Agent, "cc")
	}
}

func TestWorktreeManager_ProvisionExisting(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// First provision
	info1, err := wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	// Second provision for same agent/session should return existing
	info2, err := wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree (existing): %v", err)
	}

	if info1.Branch != info2.Branch {
		t.Errorf("expected same branch, got %q vs %q", info1.Branch, info2.Branch)
	}
}

func TestWorktreeManager_RemoveWorktree(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision
	info, err := wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	// Verify exists
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("worktree path should exist: %v", err)
	}

	// Remove
	if err := wm.RemoveWorktree(ctx, "cc", "session1234"); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// Verify no agent worktrees remain
	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 worktrees after remove, got %d", len(worktrees))
	}
}

func TestWorktreeManager_RemoveWorktree_NonExistent(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	// Removing a non-existent worktree should not error
	err = wm.RemoveWorktree(context.Background(), "cc", "nonexist1234")
	if err != nil {
		t.Fatalf("RemoveWorktree (non-existent) should not error: %v", err)
	}
}

func TestWorktreeManager_MultipleProvisions(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision multiple worktrees
	_, err = wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree cc: %v", err)
	}
	_, err = wm.ProvisionWorktree(ctx, "cod", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree cod: %v", err)
	}

	// List should return both
	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(worktrees))
	}

	agents := map[string]bool{}
	for _, wt := range worktrees {
		agents[wt.Agent] = true
	}
	if !agents["cc"] || !agents["cod"] {
		t.Errorf("expected agents cc and cod, got %v", agents)
	}
}

func TestWorktreeManager_CleanupStaleWorktrees(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision a worktree
	_, err = wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	// Cleanup with 0 duration should remove it (everything is stale)
	if err := wm.CleanupStaleWorktrees(ctx, 0); err != nil {
		t.Fatalf("CleanupStaleWorktrees: %v", err)
	}

	// Verify cleaned up
	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 0 {
		t.Errorf("expected 0 worktrees after cleanup, got %d", len(worktrees))
	}
}

func TestWorktreeManager_CleanupStaleWorktrees_RemovesListedLegacyPath(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)
	wm, err := NewWorktreeManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()
	legacyPath := filepath.Join(tmp, "..", "agent-a-b-c")
	if out, err := runGitTestCommand(t, tmp, "worktree", "add", "-b", "agent/a-b/c", legacyPath); err != nil {
		t.Fatalf("legacy git worktree add failed: %v\n%s", err, out)
	}

	if err := wm.CleanupStaleWorktrees(ctx, 0); err != nil {
		t.Fatalf("CleanupStaleWorktrees: %v", err)
	}

	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 0 {
		t.Fatalf("expected legacy worktree to be removed by listed path, got %d", len(worktrees))
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy worktree path to be gone, stat err=%v", err)
	}
}

func TestWorktreeManager_CleanupStaleWorktrees_RecentKept(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	wm, err := NewWorktreeManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}

	ctx := context.Background()

	// Provision a worktree
	_, err = wm.ProvisionWorktree(ctx, "cc", "session1234")
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}

	// Cleanup with long maxAge should keep recent worktrees
	if err := wm.CleanupStaleWorktrees(ctx, 24*time.Hour); err != nil {
		t.Fatalf("CleanupStaleWorktrees: %v", err)
	}

	worktrees, err := wm.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(worktrees) != 1 {
		t.Errorf("expected 1 worktree (recent, kept), got %d", len(worktrees))
	}
}
