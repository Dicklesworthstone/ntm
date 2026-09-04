package robot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestPrepareRestartLaunchPlanPreservesBoundProfile(t *testing.T) {
	pane := tmux.Pane{
		ID: "%7", Index: 2, NTMIndex: 1, Type: tmux.AgentClaude, Variant: "opus",
	}
	deps := restartPaneDeps(&RestartPaneDependencies{
		LoadManifest: func(string) (*resilience.SpawnManifest, error) {
			return &resilience.SpawnManifest{Agents: []resilience.AgentConfig{{
				PaneID: "%7", PaneIndex: 1, Type: "cc", Model: "opus",
				LaunchBinding: &resilience.LaunchBinding{
					Provider: "cc", Launcher: "caam", Identifier: "profile-a",
				},
			}}}, nil
		},
		PrepareLaunchCommand: func(
			_ context.Context,
			provider, binary string,
			binding *resilience.LaunchBinding,
			command string,
		) (string, resilience.LaunchAffinity, error) {
			if provider != "claude" || binary != "/opt/caam" ||
				binding == nil || binding.Identifier != "profile-a" {
				t.Fatalf("provider=%q binary=%q binding=%+v", provider, binary, binding)
			}
			return "bound:" + command, resilience.LaunchAffinityPreserved, nil
		},
	})
	cfg := config.Default()
	cfg.Integrations.CAAM.BinaryPath = "/opt/caam"

	plan, err := prepareRestartLaunchPlan(
		context.Background(), "session", []tmux.Pane{pane}, false, cfg, restartLaunchOverride{}, deps,
	)
	if err != nil {
		t.Fatalf("prepareRestartLaunchPlan: %v", err)
	}
	if plan.Affinity["2"] != resilience.LaunchAffinityPreserved ||
		!strings.HasPrefix(plan.Commands["2"], "bound:") {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPrepareRestartLaunchPlanFallsBackToLogicalPaneIdentityAfterRecovery(t *testing.T) {
	pane := tmux.Pane{
		ID: "%99", Index: 3, NTMIndex: 1, Type: tmux.AgentClaude, Variant: "opus",
	}
	var got *resilience.LaunchBinding
	deps := restartPaneDeps(&RestartPaneDependencies{
		LoadManifest: func(string) (*resilience.SpawnManifest, error) {
			return &resilience.SpawnManifest{Agents: []resilience.AgentConfig{{
				PaneID: "%7", PaneIndex: 1, Type: "cc", Model: "opus",
				LaunchBinding: &resilience.LaunchBinding{
					Provider: "cc", Launcher: "caam", Identifier: "profile-a",
				},
			}}}, nil
		},
		PrepareLaunchCommand: func(
			_ context.Context,
			_, _ string,
			binding *resilience.LaunchBinding,
			command string,
		) (string, resilience.LaunchAffinity, error) {
			got = binding
			return command, resilience.LaunchAffinityPreserved, nil
		},
	})

	if _, err := prepareRestartLaunchPlan(
		context.Background(), "session", []tmux.Pane{pane}, false, config.Default(), restartLaunchOverride{}, deps,
	); err != nil {
		t.Fatalf("prepareRestartLaunchPlan: %v", err)
	}
	if got == nil || got.Identifier != "profile-a" {
		t.Fatalf("recovered pane binding = %+v, want profile-a", got)
	}
}

func TestPrepareRestartLaunchPlanLegacyAffinityIsExplicit(t *testing.T) {
	pane := tmux.Pane{ID: "%7", Index: 2, Type: tmux.AgentClaude}
	deps := restartPaneDeps(&RestartPaneDependencies{
		LoadManifest: func(string) (*resilience.SpawnManifest, error) {
			return &resilience.SpawnManifest{Agents: []resilience.AgentConfig{{
				PaneID: "%7", PaneIndex: 2, Type: "cc", Command: "claude",
			}}}, nil
		},
		PrepareLaunchCommand: func(
			_ context.Context,
			_, _ string,
			binding *resilience.LaunchBinding,
			command string,
		) (string, resilience.LaunchAffinity, error) {
			if binding != nil {
				t.Fatalf("legacy row unexpectedly had binding %+v", binding)
			}
			return command, resilience.LaunchAffinityUnknown, nil
		},
	})

	plan, err := prepareRestartLaunchPlan(
		context.Background(), "session", []tmux.Pane{pane}, false, config.Default(), restartLaunchOverride{}, deps,
	)
	if err != nil {
		t.Fatalf("prepareRestartLaunchPlan: %v", err)
	}
	if plan.Affinity["2"] != resilience.LaunchAffinityUnknown {
		t.Fatalf("affinity = %q, want unknown", plan.Affinity["2"])
	}
}

func TestPrepareRestartLaunchPlanResolutionFailureStopsBeforeMutationBoundary(t *testing.T) {
	pane := tmux.Pane{ID: "%7", Index: 2, Type: tmux.AgentClaude}
	deps := restartPaneDeps(&RestartPaneDependencies{
		LoadManifest: func(string) (*resilience.SpawnManifest, error) {
			return &resilience.SpawnManifest{Agents: []resilience.AgentConfig{{
				PaneID: "%7", PaneIndex: 2, Type: "cc",
				LaunchBinding: &resilience.LaunchBinding{
					Provider: "cc", Launcher: "caam", Identifier: "missing-profile",
				},
			}}}, nil
		},
		PrepareLaunchCommand: func(
			context.Context,
			string, string,
			*resilience.LaunchBinding,
			string,
		) (string, resilience.LaunchAffinity, error) {
			return "", "", errors.New("cannot resolve caam:cc/missing-profile")
		},
	})

	_, err := prepareRestartLaunchPlan(
		context.Background(), "session", []tmux.Pane{pane}, false, config.Default(), restartLaunchOverride{}, deps,
	)
	if err == nil || !strings.Contains(err.Error(), "missing-profile") {
		t.Fatalf("error = %v, want named unresolved binding", err)
	}
}
