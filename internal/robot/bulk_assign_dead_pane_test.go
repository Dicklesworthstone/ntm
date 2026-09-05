package robot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ntm#311: a retained dead pane (remain-on-exit) reports an empty
// #{pane_current_path}. The resolver must skip dead panes instead of failing
// the whole session on their empty path, while a live pane with an empty
// path, distinct repositories and an all-dead session keep failing closed.

func TestResolveLiveSessionProjectSkipsDeadPanes(t *testing.T) {
	repo, _ := setupResolverRepoWithWorktrees(t, "dead-pane-live", 0)

	panes := []tmux.Pane{{ID: "%1"}, {ID: "%2", Dead: true}}
	var asked []string
	got, err := ResolveLiveSessionProjectContext(t.Context(), "dead-pane-session", panes, func(_ context.Context, paneID string) (string, error) {
		asked = append(asked, paneID)
		if paneID == "%2" {
			return "", nil // what tmux answers for a dead pane
		}
		return repo, nil
	})
	if err != nil {
		t.Fatalf("live+dead session rejected: %v", err)
	}
	if !sameResolverPath(t, got, repo) {
		t.Fatalf("resolved project = %q, want %q", got, repo)
	}
	if len(asked) != 1 || asked[0] != "%1" {
		t.Fatalf("current-path lookups = %v, want only the live pane %%1", asked)
	}
}

func TestResolveLiveSessionProjectLivePaneWithEmptyPathFailsClosed(t *testing.T) {
	repo, _ := setupResolverRepoWithWorktrees(t, "dead-pane-empty-live", 0)

	panes := []tmux.Pane{{ID: "%1"}, {ID: "%2"}}
	_, err := ResolveLiveSessionProjectContext(t.Context(), "empty-live-session", panes, func(_ context.Context, paneID string) (string, error) {
		if paneID == "%2" {
			return "", nil
		}
		return repo, nil
	})
	if err == nil || !strings.Contains(err.Error(), `pane %2: current path "" is not absolute`) {
		t.Fatalf("live pane with empty path error = %v, want not-absolute rejection", err)
	}
	if errors.Is(err, ErrNoLivePanes) {
		t.Fatalf("empty path on a live pane must not read as no-live-panes: %v", err)
	}
}

func TestResolveLiveSessionProjectDeadPanesDoNotMaskDistinctRepos(t *testing.T) {
	first, _ := setupResolverRepoWithWorktrees(t, "dead-pane-distinct-a", 0)
	second, _ := setupResolverRepoWithWorktrees(t, "dead-pane-distinct-b", 0)

	paneDirs := map[string]string{"%1": first, "%3": second}
	panes := []tmux.Pane{{ID: "%1"}, {ID: "%2", Dead: true}, {ID: "%3"}}
	_, err := ResolveLiveSessionProjectContext(t.Context(), "distinct-with-dead", panes, func(_ context.Context, paneID string) (string, error) {
		return paneDirs[paneID], nil
	})
	if err == nil || !strings.Contains(err.Error(), "panes span multiple project roots") {
		t.Fatalf("distinct repos with a dead pane error = %v, want multiple-project-roots rejection", err)
	}
	if strings.Contains(err.Error(), "%2") {
		t.Fatalf("dead pane %%2 must not be listed under a project root: %v", err)
	}
}

func TestResolveLiveSessionProjectAllDeadIsDeliberateError(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1", Dead: true}, {ID: "%2", Dead: true}}
	called := false
	_, err := ResolveLiveSessionProjectContext(t.Context(), "all-dead", panes, func(context.Context, string) (string, error) {
		called = true
		return "", nil
	})
	if err == nil {
		t.Fatal("all-dead session resolved a project")
	}
	if !errors.Is(err, ErrNoLivePanes) {
		t.Fatalf("all-dead error = %v, want ErrNoLivePanes", err)
	}
	if !strings.Contains(err.Error(), "all 2 panes are dead") {
		t.Fatalf("all-dead error %q does not name the dead-pane count", err)
	}
	if strings.Contains(err.Error(), "multiple project roots") {
		t.Fatalf("all-dead must not be reported as a multi-root conflict: %v", err)
	}
	if called {
		t.Fatal("current-path lookup was called for a dead pane")
	}
}

func TestResolveLiveSessionProjectNoPanesIsDeliberateError(t *testing.T) {
	_, err := ResolveLiveSessionProjectContext(t.Context(), "no-panes", nil, func(context.Context, string) (string, error) {
		return "", nil
	})
	if !errors.Is(err, ErrNoLivePanes) || !strings.Contains(err.Error(), "session has no panes") {
		t.Fatalf("no-panes error = %v, want ErrNoLivePanes naming the empty session", err)
	}
}
