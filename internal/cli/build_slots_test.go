package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func reconcileTestRegistry() *agentmail.SessionAgentRegistry {
	registry := agentmail.NewSessionAgentRegistry("proj", "/test/project")
	registry.AddAgent("cc_1", "%1", "LiveAgent")
	registry.AddAgent("cc_2", "%9", "GoneAgent")
	registry.AddAgent("cod_1", "%8", "TokenlessAgent")
	registry.SetRegistrationToken("LiveAgent", "tok-live")
	registry.SetRegistrationToken("GoneAgent", "tok-gone")
	return registry
}

func reconcileTestLease(agent, slot string) agentmail.BuildSlotLease {
	return agentmail.BuildSlotLease{
		Slot:      slot,
		Agent:     agent,
		Branch:    "main",
		ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(30 * time.Minute)},
	}
}

func reconcileTestDeps(registry *agentmail.SessionAgentRegistry, leases []agentmail.BuildSlotLease, listErr error, released *[]agentmail.BuildSlotLease, releaseErr error) buildSlotReconcileDeps {
	return buildSlotReconcileDeps{
		loadRegistry: func(session string, projectKeys ...string) (*agentmail.SessionAgentRegistry, error) {
			return registry, nil
		},
		listLeases: func(projectKey string, now time.Time) ([]agentmail.BuildSlotLease, error) {
			return leases, listErr
		},
		release: func(_ context.Context, projectKey string, reg *agentmail.SessionAgentRegistry, lease agentmail.BuildSlotLease) error {
			if releaseErr != nil {
				return releaseErr
			}
			if released != nil {
				*released = append(*released, lease)
			}
			return nil
		},
	}
}

func TestReconcileBuildSlotLeasesNoTransition(t *testing.T) {
	t.Parallel()
	released := []agentmail.BuildSlotLease{}
	deps := reconcileTestDeps(reconcileTestRegistry(), []agentmail.BuildSlotLease{reconcileTestLease("GoneAgent", "s")}, nil, &released, nil)

	// Same mode before and after: worktree→worktree and shared→shared.
	for _, mode := range []bool{true, false} {
		result := reconcileBuildSlotLeasesForTransition(t.Context(), "proj", "/test/project", mode, mode, nil, deps)
		if result.Transition || len(released) != 0 {
			t.Fatalf("mode %v→%v: unexpected reconciliation: %+v", mode, mode, result)
		}
	}
	t.Logf("decision: reconciliation only fires on an isolation-mode CHANGE; steady-state spawns leave every lease alone")
}

func TestReconcileBuildSlotLeasesReleasesOrphansOnTransition(t *testing.T) {
	t.Parallel()
	released := []agentmail.BuildSlotLease{}
	leases := []agentmail.BuildSlotLease{
		reconcileTestLease("LiveAgent", "frontend"),     // pane %1 alive → keep
		reconcileTestLease("GoneAgent", "frontend"),     // orphan with token → release
		reconcileTestLease("TokenlessAgent", "backend"), // orphan, no token → warn
		reconcileTestLease("ForeignAgent", "misc"),      // unknown holder → untouched
	}
	deps := reconcileTestDeps(reconcileTestRegistry(), leases, nil, &released, nil)
	panes := []tmux.Pane{{ID: "%1", Title: "cc_1", Type: tmux.AgentUser}}

	result := reconcileBuildSlotLeasesForTransition(t.Context(), "proj", "/test/project", true, false, panes, deps)
	if !result.Transition {
		t.Fatal("worktree→shared must be detected as a transition")
	}
	if len(released) != 1 || released[0].Agent != "GoneAgent" {
		t.Fatalf("released = %+v, want exactly GoneAgent's lease", released)
	}
	if len(result.Released) != 1 || result.Released[0] != "frontend/GoneAgent" {
		t.Fatalf("result.Released = %v", result.Released)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %v, want one for the tokenless orphan", result.Warnings)
	}
	t.Logf("decision: NTM itself never acquires build slots; transition release covers leases of NTM-registered pane identities that lost their panes, using their persisted tokens. Warning: %s", result.Warnings[0])
}

func TestReconcileBuildSlotLeasesDegradesWhenListingUnavailable(t *testing.T) {
	t.Parallel()
	released := []agentmail.BuildSlotLease{}
	deps := reconcileTestDeps(reconcileTestRegistry(), nil, agentmail.ErrBuildSlotListingUnavailable, &released, nil)

	result := reconcileBuildSlotLeasesForTransition(t.Context(), "proj", "/test/project", false, true, nil, deps)
	if !result.Transition {
		t.Fatal("shared→worktree must be detected as a transition")
	}
	if result.Skipped == "" || len(released) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("expected degraded skip with no side effects: %+v", result)
	}
	t.Logf("decision: Agent Mail archive/server unavailable → spawn proceeds with a note (%q); the transition is never blocked", result.Skipped)
}

func TestReconcileBuildSlotLeasesSkipsQuietlyWithoutRegistry(t *testing.T) {
	t.Parallel()
	listCalls := 0
	deps := buildSlotReconcileDeps{
		loadRegistry: func(string, ...string) (*agentmail.SessionAgentRegistry, error) { return nil, nil },
		listLeases: func(string, time.Time) ([]agentmail.BuildSlotLease, error) {
			listCalls++
			return nil, nil
		},
		release: func(context.Context, string, *agentmail.SessionAgentRegistry, agentmail.BuildSlotLease) error {
			t.Fatal("release must not run without a registry")
			return nil
		},
	}

	result := reconcileBuildSlotLeasesForTransition(t.Context(), "proj", "/test/project", false, true, nil, deps)
	if !result.Transition || result.Skipped != "" || listCalls != 0 {
		t.Fatalf("no-registry transitions must be silent no-ops: %+v listCalls=%d", result, listCalls)
	}
	t.Logf("decision: fresh sessions (no registry) transition silently — nothing NTM-orchestrated could hold a lease")
}

func TestReconcileBuildSlotLeasesReportsReleaseFailures(t *testing.T) {
	t.Parallel()
	deps := reconcileTestDeps(reconcileTestRegistry(), []agentmail.BuildSlotLease{reconcileTestLease("GoneAgent", "s")}, nil, nil, errors.New("mcp: server busy"))

	result := reconcileBuildSlotLeasesForTransition(t.Context(), "proj", "/test/project", true, false, nil, deps)
	if len(result.Released) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("release failure must become a warning: %+v", result)
	}
}
