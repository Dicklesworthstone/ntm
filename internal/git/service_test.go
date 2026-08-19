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

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestNewWorktreeService(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	svc := NewWorktreeService(cfg)
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	if svc.managers == nil {
		t.Fatal("expected managers map to be initialized")
	}
	if svc.config != cfg {
		t.Fatal("expected config to be stored")
	}
}

func TestWorktreeService_getManager(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	cfg := &config.Config{}
	svc := NewWorktreeService(cfg)

	// First call creates a new manager
	m1, err := svc.getManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("getManager: %v", err)
	}
	if m1 == nil {
		t.Fatal("expected non-nil manager")
	}
	if m1.projectDir != tmp {
		t.Errorf("projectDir = %q, want %q", m1.projectDir, tmp)
	}

	// Second call returns the cached manager
	m2, err := svc.getManager(t.Context(), tmp)
	if err != nil {
		t.Fatalf("getManager (cached): %v", err)
	}
	if m1 != m2 {
		t.Fatal("expected same manager instance from cache")
	}
}

func TestWorktreeService_getManager_NotGitRepo(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := &config.Config{}
	svc := NewWorktreeService(cfg)

	_, err := svc.getManager(t.Context(), tmp)
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestWorktreeService_getManager_RequiresLiveCallerContext(t *testing.T) {
	t.Parallel()

	svc := NewWorktreeService(&config.Config{})
	projectDir := t.TempDir()

	if manager, err := svc.getManager(nil, projectDir); manager != nil || err == nil || !strings.Contains(err.Error(), "requires a command context") {
		t.Fatalf("getManager nil context result=(%v, %v), want context requirement", manager, err)
	}

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if manager, err := svc.getManager(canceledCtx, projectDir); manager != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("getManager canceled context result=(%v, %v), want context.Canceled", manager, err)
	}

	// A cached manager must not allow a canceled caller to cross the API boundary.
	svc.managers[projectDir] = &WorktreeManager{projectDir: projectDir, baseRepo: projectDir}
	if manager, err := svc.getManager(canceledCtx, projectDir); manager != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("getManager cached canceled context result=(%v, %v), want context.Canceled", manager, err)
	}
}

func TestWorktreeServiceMethodsRequireLiveCallerContext(t *testing.T) {
	t.Parallel()

	svc := NewWorktreeService(&config.Config{})
	operations := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "auto provision session",
			run: func(ctx context.Context) error {
				_, err := svc.AutoProvisionSession(ctx, "session")
				return err
			},
		},
		{
			name: "cleanup session worktrees",
			run: func(ctx context.Context) error {
				_, err := svc.CleanupSessionWorktrees(ctx, "session")
				return err
			},
		},
		{
			name: "get session worktree status",
			run: func(ctx context.Context) error {
				_, err := svc.GetSessionWorktreeStatus(ctx, "session")
				return err
			},
		},
	}

	for _, operation := range operations {
		t.Run(operation.name+" nil context", func(t *testing.T) {
			if err := operation.run(nil); err == nil || !strings.Contains(err.Error(), "requires a command context") {
				t.Fatalf("nil context error = %v, want context requirement", err)
			}
		})
		t.Run(operation.name+" canceled context", func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := operation.run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled context error = %v, want context.Canceled", err)
			}
		})
	}
}

// GH#251 phase 2: grok panes are now valid automated targets, so a mixed
// cod+grok batch provisions worktrees and updates panes for both agents.
func TestWorktreeService_AutoProvisionSessionProvisionsMixedGrokBatch(t *testing.T) {
	t.Parallel()

	repo := setupGitRepo(t)
	sessionName := filepath.Base(repo)
	svc := NewWorktreeService(&config.Config{ProjectsBase: filepath.Dir(repo)})
	svc.detectAgentPanesFn = func(ctx context.Context, gotSession string) ([]AgentPane, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if gotSession != sessionName {
			t.Fatalf("detected session = %q, want %q", gotSession, sessionName)
		}
		return []AgentPane{
			{PaneID: "%1", AgentType: "cod", AgentNum: 1, Title: sessionName + "__cod_1"},
			{PaneID: "%2", AgentType: "grok-build", AgentNum: 2, Title: sessionName + "__grok_2"},
		}, nil
	}
	mutatedPanes := []string{}
	svc.changeDirectoryInPaneFn = func(ctx context.Context, paneID, workingDir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if workingDir == "" {
			t.Fatalf("pane %s mutation received empty worktree path", paneID)
		}
		mutatedPanes = append(mutatedPanes, paneID)
		return nil
	}

	response, err := svc.AutoProvisionSession(context.Background(), sessionName)
	if err != nil {
		t.Fatalf("AutoProvisionSession() error = %v, want mixed cod+grok batch accepted", err)
	}
	if response == nil || len(response.Errors) != 0 {
		t.Fatalf("AutoProvisionSession() response = %+v, want no provisioning errors", response)
	}
	if len(response.Provisions) != 2 || response.SuccessCount != 2 {
		t.Fatalf("provisions = %+v (success=%d), want both cod and grok panes provisioned", response.Provisions, response.SuccessCount)
	}
	byPane := map[string]WorktreeProvision{}
	for _, p := range response.Provisions {
		byPane[p.PaneID] = p
	}
	if p, ok := byPane["%1"]; !ok || p.AgentType != "cod" || p.WorktreePath == "" || p.Branch == "" {
		t.Fatalf("cod provision = %+v, want populated worktree", p)
	}
	if p, ok := byPane["%2"]; !ok || p.AgentType != "grok-build" || p.WorktreePath == "" || p.Branch == "" {
		t.Fatalf("grok provision = %+v, want populated worktree like the cod pane", p)
	}
	if len(mutatedPanes) != 2 || mutatedPanes[0] != "%1" || mutatedPanes[1] != "%2" {
		t.Fatalf("pane mutations = %v, want both panes updated in order", mutatedPanes)
	}
}

func TestWorktreeServicePaneDiscoveryThreadsLiveCallerContext(t *testing.T) {
	svc := NewWorktreeService(&config.Config{})
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "caller"))
	defer cancel()
	started := make(chan struct{})
	var discoveredSession string
	var discoveredContext any
	svc.detectAgentPanesFn = func(gotCtx context.Context, sessionName string) ([]AgentPane, error) {
		discoveredSession = sessionName
		discoveredContext = gotCtx.Value(contextKey{})
		close(started)
		<-gotCtx.Done()
		return nil, gotCtx.Err()
	}

	result := make(chan error, 1)
	go func() {
		_, err := svc.sessionAgentPanes(ctx, "session")
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("pane discovery did not receive caller context")
	}
	if discoveredSession != "session" || discoveredContext != "caller" {
		t.Fatalf("pane discovery received session=%q context=%v", discoveredSession, discoveredContext)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sessionAgentPanes error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pane discovery did not return after caller cancellation")
	}
}

func TestWaitForWorktreePaneDelayHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- waitForWorktreePaneDelay(ctx, time.Minute) }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForWorktreePaneDelay error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pane delay did not return after caller cancellation")
	}
}

func TestWorktreeServiceAutoProvisionCancellationRetainsCheckoutAndSkipsPaneMutation(t *testing.T) {
	repo := setupGitRepo(t)
	sessionName := filepath.Base(repo)
	svc := NewWorktreeService(&config.Config{ProjectsBase: filepath.Dir(repo)})
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "auto-provision"))
	defer cancel()
	var discoveredSession string
	var discoveredContext any
	svc.detectAgentPanesFn = func(gotCtx context.Context, gotSession string) ([]AgentPane, error) {
		discoveredSession = gotSession
		discoveredContext = gotCtx.Value(contextKey{})
		return []AgentPane{{PaneID: "%1", AgentType: "cc", AgentNum: 1, Title: sessionName + "__cc_1"}}, nil
	}

	paneBoundary := make(chan struct{})
	allowPaneMutation := make(chan struct{})
	paneMutated := false
	var mutationPaneID, mutationWorkingDir string
	var mutationContext any
	svc.changeDirectoryInPaneFn = func(gotCtx context.Context, paneID, workingDir string) error {
		mutationPaneID = paneID
		mutationWorkingDir = workingDir
		mutationContext = gotCtx.Value(contextKey{})
		close(paneBoundary)
		select {
		case <-gotCtx.Done():
			return gotCtx.Err()
		case <-allowPaneMutation:
			paneMutated = true
			return nil
		}
	}

	type autoProvisionResult struct {
		response *AutoProvisionResponse
		err      error
	}
	result := make(chan autoProvisionResult, 1)
	go func() {
		response, err := svc.AutoProvisionSession(ctx, sessionName)
		result <- autoProvisionResult{response: response, err: err}
	}()
	select {
	case <-paneBoundary:
	case <-time.After(15 * time.Second):
		t.Fatal("auto-provision did not reach post-checkout pane boundary")
	}
	if discoveredSession != sessionName || discoveredContext != "auto-provision" {
		t.Fatalf("pane discovery received session=%q context=%v", discoveredSession, discoveredContext)
	}
	if mutationPaneID != "%1" || mutationWorkingDir == "" || mutationContext != "auto-provision" {
		t.Fatalf("pane mutation boundary received pane=%q path=%q context=%v", mutationPaneID, mutationWorkingDir, mutationContext)
	}
	cancel()

	var got autoProvisionResult
	select {
	case got = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("auto-provision did not return after pane-boundary cancellation")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("AutoProvisionSession error = %v, want context.Canceled", got.err)
	}
	if got.response == nil || len(got.response.Provisions) != 1 || got.response.SuccessCount != 1 {
		t.Fatalf("AutoProvisionSession response = %+v, want one retained provision", got.response)
	}
	provision := got.response.Provisions[0]
	if provision.PaneID != "%1" || provision.AgentType != "cc" || provision.WorktreePath == "" || provision.Branch == "" {
		t.Fatalf("retained provision = %+v", provision)
	}
	if !strings.Contains(got.err.Error(), "checkout provisioned at "+provision.WorktreePath) ||
		!strings.Contains(got.err.Error(), "branch "+provision.Branch) {
		t.Fatalf("cancellation error omits retained checkout evidence: %v", got.err)
	}
	if stat, err := os.Stat(provision.WorktreePath); err != nil || !stat.IsDir() {
		t.Fatalf("retained checkout missing: stat=%v err=%v", stat, err)
	}
	if paneMutated {
		t.Fatal("pane mutation ran after caller cancellation")
	}
}

func TestWorktreeServiceAutoProvisionAddCancellationMaterializesRetainedProvision(t *testing.T) {
	repo := setupGitRepo(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("resolve real git: %v", err)
	}
	sessionName := filepath.Base(repo)
	sessionID := buildSessionWorktreeID(sessionName, "cc", 1)
	wantPath := filepath.Join(repo, "..", worktreeNameForKeys("cc", sessionID))
	wantBranch := "agent/cc/" + sessionID
	stateDir := t.TempDir()
	gitLog := filepath.Join(stateDir, "git.log")
	addCompleted := filepath.Join(stateDir, "add-completed")
	fakeGit := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$WORKTREE_SERVICE_GIT_LOG"
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "add" ]; then
  "$WORKTREE_SERVICE_REAL_GIT" "$@"
  command_status=$?
  if [ "$command_status" -ne 0 ]; then exit "$command_status"; fi
  : > "$WORKTREE_SERVICE_ADD_COMPLETED"
  trap '' INT
  exec sleep 30
fi
exec "$WORKTREE_SERVICE_REAL_GIT" "$@"
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o700); err != nil {
		t.Fatalf("write auto-provision add-then-block git: %v", err)
	}
	t.Setenv("WORKTREE_SERVICE_GIT_LOG", gitLog)
	t.Setenv("WORKTREE_SERVICE_REAL_GIT", realGit)
	t.Setenv("WORKTREE_SERVICE_ADD_COMPLETED", addCompleted)
	t.Setenv("PATH", filepath.Dir(fakeGit)+string(os.PathListSeparator)+os.Getenv("PATH"))

	svc := NewWorktreeService(&config.Config{ProjectsBase: filepath.Dir(repo)})
	svc.detectAgentPanesFn = func(ctx context.Context, gotSession string) ([]AgentPane, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if gotSession != sessionName {
			return nil, fmt.Errorf("session = %q, want %q", gotSession, sessionName)
		}
		return []AgentPane{{PaneID: "%1", AgentType: "cc", AgentNum: 1, Title: sessionName + "__cc_1"}}, nil
	}
	paneMutationCount := 0
	svc.changeDirectoryInPaneFn = func(context.Context, string, string) error {
		paneMutationCount++
		return errors.New("pane mutation must not run after retained add cancellation")
	}

	type autoProvisionResult struct {
		response *AutoProvisionResponse
		err      error
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan autoProvisionResult, 1)
	go func() {
		response, provisionErr := svc.AutoProvisionSession(ctx, sessionName)
		result <- autoProvisionResult{response: response, err: provisionErr}
	}()
	waitForWorktreeTestPath(t, addCompleted)
	cancel()

	var got autoProvisionResult
	select {
	case got = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("auto-provision did not join canceled add command")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("AutoProvisionSession error = %v, want context.Canceled", got.err)
	}
	if got.response == nil || got.response.TotalProvisions != 1 || got.response.SuccessCount != 1 ||
		len(got.response.Provisions) != 1 || len(got.response.Errors) != 1 {
		t.Fatalf("AutoProvisionSession response = %+v, want one retained provision and one cancellation error", got.response)
	}
	provision := got.response.Provisions[0]
	wantChangeDir := fmt.Sprintf("cd %s", tmux.ShellQuote(wantPath))
	if provision.PaneID != "%1" || provision.AgentType != "cc" || provision.WorktreePath != wantPath ||
		provision.Branch != wantBranch || provision.Commit != "" || provision.ChangeDir != wantChangeDir {
		t.Fatalf("retained provision = %+v, want path=%s branch=%s change_dir=%q", provision, wantPath, wantBranch, wantChangeDir)
	}
	if !strings.Contains(got.err.Error(), "checkout retained at "+wantPath) || !strings.Contains(got.err.Error(), "branch "+wantBranch) {
		t.Fatalf("cancellation error omits retained checkout evidence: %v", got.err)
	}
	if !strings.Contains(got.response.Errors[0].Error, "checkout retained at "+wantPath) {
		t.Fatalf("response error omits retained checkout evidence: %+v", got.response.Errors[0])
	}
	if paneMutationCount != 0 {
		t.Fatalf("retained add cancellation reached %d pane mutations", paneMutationCount)
	}
	if stat, err := os.Stat(wantPath); err != nil || !stat.IsDir() {
		t.Fatalf("retained checkout missing: stat=%v err=%v", stat, err)
	}
	gitData, err := os.ReadFile(gitLog)
	if err != nil {
		t.Fatalf("read auto-provision git audit: %v", err)
	}
	gitAudit := string(gitData)
	if strings.Count(gitAudit, "worktree add") != 1 || strings.Contains(gitAudit, "rev-parse HEAD") {
		t.Fatalf("auto-provision crossed retained add cancellation boundary: %q", gitAudit)
	}
}

func TestWorktreeService_CleanupSessionWorktrees_RemovesListedLegacyPath(t *testing.T) {
	t.Parallel()

	repo := setupGitRepo(t)
	sessionName := filepath.Base(repo)
	cfg := &config.Config{ProjectsBase: filepath.Dir(repo)}
	svc := NewWorktreeService(cfg)

	sessionID := canonicalSessionKey(sessionName + "-claude-1")
	legacyPath := filepath.Join(repo, "..", "agent-claude-"+sessionID)
	cmd := exec.Command("git", "worktree", "add", "-b", "agent/claude/"+sessionID, legacyPath)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("legacy git worktree add failed: %v\n%s", err, out)
	}

	removed, err := svc.CleanupSessionWorktrees(context.Background(), sessionName)
	if err != nil {
		t.Fatalf("CleanupSessionWorktrees: %v", err)
	}
	if removed < 1 {
		t.Fatalf("expected at least one worktree removed, got %d", removed)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy session worktree path to be gone, stat err=%v", err)
	}
}

// bd-y9ndb + bd-l542u: matching must avoid prefix overlap ("my" vs
// "my-app") and preserve uniqueness for full "<session>-<agent>-<num>"
// identities stored through canonicalSessionKey(...).
func TestSessionMatchesWorktree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		sessionName string
		agentType   string
		sessionID   string
		want        bool
	}{
		// Pre-fix data-loss cases.
		{"shorter session must not match longer-named worktree",
			"my", "claude", canonicalSessionKey("my-app-claude-1"), false},
		{"prefix-overlap with same agent type must not match",
			"app", "claude", canonicalSessionKey("app2-claude-1"), false},
		{"shared prefix with different middle segment must not match",
			"foo", "codex", canonicalSessionKey("foo-bar-codex-1"), false},
		{"same first 8 chars but different session identity must not match",
			"alpha-team-x", "claude", canonicalSessionKey("alpha-team-y-claude-1"), false},
		{"single-dash session must not match double-dash worktree id",
			"alpha-team", "claude", canonicalSessionKey("alpha--team-claude-1"), false},

		// Happy paths.
		{"autoprovisioned short session matches",
			"my", "claude", canonicalSessionKey("my-claude-1"), true},
		{"hyphenated session matches its own worktree",
			"my-app", "claude", canonicalSessionKey("my-app-claude-1"), true},
		{"multi-digit agent num matches",
			"proj", "codex", canonicalSessionKey("proj-codex-12"), true},
		{"deeply hyphenated session matches",
			"a-b-c-d", "gemini", canonicalSessionKey("a-b-c-d-gemini-3"), true},
		{"double-dash session matches its own worktree id",
			"alpha--team", "claude", canonicalSessionKey("alpha--team-claude-1"), true},
		{"same session/type different pane matches only exact pane suffix",
			"alpha-team", "claude", canonicalSessionKey("alpha-team-claude-2"), true},
		{"sanitized agent key with canonicalized session id matches",
			"sess", canonicalAgentKey("../evil/type"), buildSessionWorktreeID("sess", "../evil/type", 1), true},

		// Negative paths — wrong agent type, missing num, etc.
		{"wrong agent type does not match",
			"my", "codex", canonicalSessionKey("my-claude-1"), false},
		{"missing trailing num does not match",
			"zz", "cc", "zz-cc-", false},
		{"non-digit suffix for short base does not match",
			"zz", "cc", "zz-cc-ab", false},
		{"empty session never matches",
			"", "claude", canonicalSessionKey("x-claude-1"), false},
		{"empty agent never matches",
			"my", "", canonicalSessionKey("my-claude-1"), false},
		{"empty session id never matches",
			"my", "claude", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sessionMatchesWorktree(tc.sessionName, tc.agentType, tc.sessionID)
			if (got && !tc.want) || (!got && tc.want) {
				t.Errorf("sessionMatchesWorktree(%q, %q, %q) = %v, want %v",
					tc.sessionName, tc.agentType, tc.sessionID, got, tc.want)
			}
		})
	}
}

func TestBuildSessionWorktreeIDCanonicalizesAgentType(t *testing.T) {
	t.Parallel()

	got := buildSessionWorktreeID("sess", "../evil/type", 1)
	want := canonicalSessionKey("sess-evil-type-1")
	if got != want {
		t.Fatalf("buildSessionWorktreeID() = %q, want %q", got, want)
	}
}
