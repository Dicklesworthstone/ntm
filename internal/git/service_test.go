package git

import (
	"context"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
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
	m1, err := svc.getManager(tmp)
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
	m2, err := svc.getManager(tmp)
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

	_, err := svc.getManager(tmp)
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestWorktreeService_GetAllWorktrees_Empty(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	svc := NewWorktreeService(cfg)

	result, err := svc.GetAllWorktrees(context.Background())
	if err != nil {
		t.Fatalf("GetAllWorktrees: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d projects", len(result))
	}
}

func TestWorktreeService_GetAllWorktrees_WithManagers(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	cfg := &config.Config{}
	svc := NewWorktreeService(cfg)

	// Populate via getManager
	_, err := svc.getManager(tmp)
	if err != nil {
		t.Fatalf("getManager: %v", err)
	}

	result, err := svc.GetAllWorktrees(context.Background())
	if err != nil {
		t.Fatalf("GetAllWorktrees: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 project, got %d", len(result))
	}
	if wts, ok := result[tmp]; !ok {
		t.Error("expected project dir key in result")
	} else if len(wts) != 0 {
		t.Errorf("expected 0 worktrees (no agents), got %d", len(wts))
	}
}

func TestWorktreeService_CleanupStaleWorktrees_NoManagers(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	svc := NewWorktreeService(cfg)

	err := svc.CleanupStaleWorktrees(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("CleanupStaleWorktrees: %v", err)
	}
}

func TestWorktreeService_CleanupStaleWorktrees_WithManagers(t *testing.T) {
	t.Parallel()

	tmp := setupGitRepo(t)

	cfg := &config.Config{}
	svc := NewWorktreeService(cfg)

	_, err := svc.getManager(tmp)
	if err != nil {
		t.Fatalf("getManager: %v", err)
	}

	// No worktrees exist, so this should be a no-op
	err = svc.CleanupStaleWorktrees(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("CleanupStaleWorktrees: %v", err)
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

		// Happy paths.
		{"autoprovisioned short session matches",
			"my", "claude", canonicalSessionKey("my-claude-1"), true},
		{"hyphenated session matches its own worktree",
			"my-app", "claude", canonicalSessionKey("my-app-claude-1"), true},
		{"multi-digit agent num matches",
			"proj", "codex", canonicalSessionKey("proj-codex-12"), true},
		{"deeply hyphenated session matches",
			"a-b-c-d", "gemini", canonicalSessionKey("a-b-c-d-gemini-3"), true},
		{"same session/type different pane matches only exact pane suffix",
			"alpha-team", "claude", canonicalSessionKey("alpha-team-claude-2"), true},

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
			if got != tc.want {
				t.Errorf("sessionMatchesWorktree(%q, %q, %q) = %v, want %v",
					tc.sessionName, tc.agentType, tc.sessionID, got, tc.want)
			}
		})
	}
}
