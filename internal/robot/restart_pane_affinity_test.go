package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRestartPanePreflightRejectsChangedEvidence(t *testing.T) {
	for _, change := range []string{"stable", "missing", "duplicate", "physical-id", "pid", "window", "index", "type", "foreground", "dead", "service", "rotation", "spec", "cwd", "list-error", "spec-error", "cwd-error", "display-title"} {
		t.Run(change, func(t *testing.T) {
			pane := tmux.Pane{ID: "%7", PID: 111, Index: 2, WindowIndex: 1, NTMIndex: 3,
				Type: tmux.AgentCodex, Title: "session__cod_3_saved", Command: "zsh"}
			spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex,
				Command: "codex --model saved", Model: "saved"}
			preflight := &RestartPanePreflight{session: "session", targets: []restartPaneSnapshot{{pane: pane, launchSpec: cloneRestartLaunchSpec(&spec), workingDir: "/original"}}}
			current := pane
			liveSpec, liveDir := spec, "/original"
			panes := []tmux.Pane{current}
			switch change {
			case "missing":
				panes = nil
			case "duplicate":
				panes = append(panes, current)
			case "physical-id":
				panes[0].ID = "%8"
			case "pid":
				panes[0].PID++
			case "window":
				panes[0].WindowIndex++
			case "index":
				panes[0].Index++
			case "type":
				panes[0].Type = tmux.AgentClaude
			case "foreground":
				panes[0].Command = "codex"
			case "dead":
				panes[0].Dead = true
			case "service":
				panes[0].Service = "build-server"
			case "rotation":
				panes[0].RotationToken = "another-owner"
			case "spec":
				liveSpec.Command = "codex --model private-changed-command"
			case "cwd":
				liveDir = "/private-changed-directory"
			case "display-title":
				panes[0].Title = "Provider status updated normally"
				panes[0].NTMIndex, panes[0].Variant = 0, ""
			}
			deps := RestartPaneDependencies{
				ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
					if change == "list-error" {
						return nil, errors.New("topology unavailable")
					}
					return panes, nil
				},
				ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
					if change == "spec-error" {
						return nil, errors.New("metadata unavailable")
					}
					return &liveSpec, nil
				},
				PaneWorkingDir: func(context.Context, string) (string, error) {
					if change == "cwd-error" {
						return "", errors.New("directory unavailable")
					}
					return liveDir, nil
				},
			}
			err := preflight.observe(t.Context(), deps)
			wantOK := change == "stable" || change == "display-title"
			if (err == nil) != wantOK {
				t.Fatalf("preflight observation error=%v, want success=%t", err, wantOK)
			}
			if err != nil && (strings.Contains(err.Error(), "private-changed-command") || strings.Contains(err.Error(), "private-changed-directory")) {
				t.Fatalf("changed launch input leaked into refusal: %v", err)
			}
		})
	}
}

func TestRestartPaneReplacementKeepsOwnedPIDAndAcceptsProviderTransitions(t *testing.T) {
	for _, change := range []string{"shell", "provider-started", "foreign-pid", "foreground-takeover", "spec-changed", "foreign-rotation", "missing"} {
		t.Run(change, func(t *testing.T) {
			original := tmux.Pane{ID: "%7", PID: 111, Index: 1, NTMIndex: 1, Type: tmux.AgentCodex, Title: "session__cod_1", Command: "codex"}
			spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: "codex --model saved"}
			preflight := &RestartPanePreflight{session: "session", targets: []restartPaneSnapshot{{pane: original, launchSpec: cloneRestartLaunchSpec(&spec), workingDir: "/original"}}}
			current := original
			current.PID, current.Command = 222, "zsh"
			current.Title, current.NTMIndex = "Fresh shell prompt", 0
			liveSpec := spec
			requireShell := true
			panes := []tmux.Pane{current}
			switch change {
			case "provider-started":
				panes[0].Command, panes[0].Title = "codex", "Provider's normal task title"
				requireShell = false
			case "foreign-pid":
				panes[0].PID = 333
			case "foreground-takeover":
				panes[0].Command = "codex"
			case "spec-changed":
				liveSpec.Command = "codex --model unrelated"
			case "foreign-rotation":
				panes[0].RotationToken = "other-owner"
			case "missing":
				panes = nil
			}
			deps := RestartPaneDependencies{
				ListPanes:      func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
				ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) { return &liveSpec, nil },
				PaneWorkingDir: func(context.Context, string) (string, error) { return "/original", nil },
			}
			err := preflight.observeReplacement(t.Context(), deps, "%7", 222, requireShell, &spec)
			wantOK := change == "shell" || change == "provider-started"
			if (err == nil) != wantOK {
				t.Fatalf("replacement observation error=%v, want success=%t", err, wantOK)
			}
		})
	}
}

func TestRestartPanePreflightDoesNotAliasOrExposeLaunchInputs(t *testing.T) {
	pane := tmux.Pane{ID: "%7", PID: 111, Index: 1, NTMIndex: 1, Type: tmux.AgentCodex, Command: "zsh", Tags: []string{"original"}}
	spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex,
		Command: "codex --custom private-original-command", OmittedEnv: []string{"REQUIRED_KEY"}}
	deps := restartPaneDeps(&RestartPaneDependencies{
		LoadManifest:   func(string) (*resilience.SpawnManifest, error) { return nil, os.ErrNotExist },
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) { return &spec, nil },
		PaneWorkingDir: func(context.Context, string) (string, error) { return t.TempDir(), nil },
	})
	plan, err := prepareRestartLaunchPlan(t.Context(), "session", []tmux.Pane{pane}, false, config.Default(), restartLaunchOverride{}, deps, map[string]string{"REQUIRED_KEY": "private-runtime-value"})
	defer plan.close()
	if err != nil {
		t.Fatal(err)
	}
	spec.Command, spec.OmittedEnv[0], pane.Tags[0] = "codex --model changed", "CHANGED_KEY", "changed"
	if plan.Preflight.targets[0].launchSpec.Command != "codex --custom private-original-command" || plan.Preflight.targets[0].launchSpec.OmittedEnv[0] != "REQUIRED_KEY" || plan.Preflight.targets[0].pane.Tags[0] != "original" {
		t.Fatal("mutable caller state changed the completed preflight")
	}
	response, err := json.Marshal(RestartPaneOutput{Preflight: plan.Preflight})
	if err != nil {
		t.Fatal(err)
	}
	options, err := json.Marshal(RestartPaneOptions{Preflight: plan.Preflight})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(response)+string(options), "Preflight") {
		t.Fatal("opaque preflight appeared in robot JSON")
	}
	for _, private := range []string{"private-original-command", "private-runtime-value", "REQUIRED_KEY", "targets"} {
		if strings.Contains(string(response)+string(options)+fmt.Sprintf("%+v %#v", plan.Preflight, plan.Preflight), private) {
			t.Fatalf("opaque preflight exposed private metadata or values containing %q", private)
		}
	}
}

func TestRestartAssignmentFenceKeepsTheVerifiedReplacement(t *testing.T) {
	for _, change := range []string{"stable", "unarmed", "pid", "spec", "observation-pid", "different-delivery-target", "delivery-replacement"} {
		t.Run(change, func(t *testing.T) {
			original := tmux.Pane{ID: "%7", PID: 111, Index: 1, Type: tmux.AgentCodex, Command: "zsh"}
			current := original
			current.PID, current.Command = 222, "codex"
			spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: "codex --model preserved"}
			liveSpec := spec
			preflight := &RestartPanePreflight{session: "session", targets: []restartPaneSnapshot{{pane: original, launchSpec: cloneRestartLaunchSpec(&spec), workingDir: "/original"}}}
			deliveries := 0
			type contextMarker struct{}
			ctx := context.WithValue(t.Context(), contextMarker{}, "original-context")
			fence := &restartAssignmentFence{preflight: preflight, target: "%7", pid: 222, spec: spec}
			fence.deps = RestartPaneDependencies{
				ListPanes:      func(context.Context, string) ([]tmux.Pane, error) { return []tmux.Pane{current}, nil },
				ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) { return &liveSpec, nil },
				PaneWorkingDir: func(context.Context, string) (string, error) { return "/original", nil },
				ObserveSession: func(context.Context, string) (statuspkg.SessionObservation, error) {
					observed := current
					if change == "observation-pid" {
						observed.PID = 333
					}
					return statuspkg.SessionObservation{Session: "session", Panes: []statuspkg.PaneObservation{{Pane: observed.Ref(), Metadata: observed}}}, nil
				},
				DispatchDeliverer: dispatchsvc.DelivererFunc(func(guarded context.Context, delivery dispatchsvc.Delivery) error {
					deliveries++
					if guarded == ctx || guarded.Value(contextMarker{}) != "original-context" || guarded.Err() != nil {
						t.Fatal("assignment delivery did not retain the caller context under its pane guard")
					}
					if delivery.Target.Ref.ID != "%7" || delivery.Target.Pane.PID != 222 {
						t.Fatal("assignment delivered to a different pane process")
					}
					if change == "delivery-replacement" {
						current.PID = 333
					}
					return nil
				}),
			}
			delivery := dispatchsvc.Delivery{Session: "session", Target: dispatchsvc.Target{Ref: current.Ref(), Pane: current, AgentType: current.Type}}
			switch change {
			case "unarmed":
				fence.preflight = nil
			case "pid":
				current.PID = 333
			case "spec":
				liveSpec.Command = "codex --model unrelated"
			case "different-delivery-target":
				delivery.Target.Ref.ID = "%8"
			}
			if change != "unarmed" {
				_, err := fence.listPanes(ctx, "session")
				wantError := change == "pid" || change == "spec"
				if (err != nil) != wantError {
					t.Fatalf("fenced topology error=%v, want error=%t", err, wantError)
				}
			}
			_, observationErr := fence.observeSession(ctx, "session")
			wantObservationError := change != "stable" && change != "different-delivery-target" && change != "delivery-replacement"
			if (observationErr != nil) != wantObservationError {
				t.Fatalf("fenced observation error=%v, want error=%t", observationErr, wantObservationError)
			}
			if change == "observation-pid" {
				// A refused observation must not be used to reach the delivery
				// stage; the caller stops before claiming or submitting work.
				return
			}
			err := fence.Deliver(ctx, delivery)
			if change == "stable" {
				if err != nil || deliveries != 1 {
					t.Fatalf("verified assignment delivery error=%v calls=%d", err, deliveries)
				}
			} else if change == "delivery-replacement" {
				if err == nil || deliveries != 1 || dispatchsvc.IsGuaranteedNoDeliveryActuation(err) {
					t.Fatalf("post-delivery replacement was incorrectly reported: error=%v calls=%d", err, deliveries)
				}
			} else if err == nil || deliveries != 0 || !dispatchsvc.IsGuaranteedNoDeliveryActuation(err) {
				t.Fatalf("changed assignment target was not refused before delivery: error=%v calls=%d", err, deliveries)
			}
		})
	}
}

func TestRestartDeliveryPreflightBoundsEveryPaneBeforeMutation(t *testing.T) {
	long := "printf '%s' '" + strings.Repeat("x", 6000) + "'"
	plan := restartLaunchPlan{Commands: map[string]string{"1": long, "2": long, "3": "codex"}}
	defer plan.close()
	if err := plan.prepareDelivery(t.Context(), "session", nil); err != nil {
		t.Fatal(err)
	}
	if len(plan.handoffs) != 2 || plan.handoffs["1"] == nil || plan.handoffs["2"] == nil || plan.handoffs["1"] == plan.handoffs["2"] || plan.handoffs["3"] != nil {
		t.Fatal("long pane launches did not receive independent handoffs while the ordinary launch stayed direct")
	}
	if plan.DeliveryCommands["1"] != long || plan.DeliveryCommands["2"] != long || plan.DeliveryCommands["3"] != "codex" {
		t.Fatal("delivery preflight changed the launch commands")
	}
}

func TestRestartDeliveryPreflightRefusesRemoteLongLaunch(t *testing.T) {
	original := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("not-contacted.invalid")
	t.Cleanup(func() { tmux.DefaultClient = original })
	plan := restartLaunchPlan{Commands: map[string]string{"1": "codex --arg '" + strings.Repeat("x", 6000) + "'"}}
	defer plan.close()
	err := plan.prepareDelivery(t.Context(), "session", nil)
	if err == nil || !strings.Contains(err.Error(), "tmux host") || len(plan.handoffs) != 0 {
		t.Fatalf("remote long launch preflight error=%v, handoffs=%d", err, len(plan.handoffs))
	}
}

func TestRestartDeliveryPreflightRejectsInvalidPreparedCommand(t *testing.T) {
	for _, command := range []string{"", "private-launcher\npath codex", strings.Repeat("x", 6000) + "\r"} {
		plan := restartLaunchPlan{Commands: map[string]string{"1": command}}
		err := plan.prepareDelivery(t.Context(), "session", nil)
		plan.close()
		if err == nil || len(plan.handoffs) != 0 || strings.Contains(err.Error(), "private-launcher") {
			t.Fatalf("invalid final command was not refused safely in preflight: error=%v handoffs=%d", err, len(plan.handoffs))
		}
	}
}

func TestPrepareRestartLaunchPlanPreservesBoundProfile(t *testing.T) {
	pane := tmux.Pane{
		ID: "%7", Index: 2, NTMIndex: 1, Type: tmux.AgentClaude, Variant: "opus",
	}
	deps := restartPaneDeps(&RestartPaneDependencies{
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) { return nil, nil },
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
		context.Background(), "session", []tmux.Pane{pane}, false, cfg, restartLaunchOverride{}, deps, nil,
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
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) { return nil, nil },
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
		context.Background(), "session", []tmux.Pane{pane}, false, config.Default(), restartLaunchOverride{}, deps, nil,
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
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) { return nil, nil },
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
		context.Background(), "session", []tmux.Pane{pane}, false, config.Default(), restartLaunchOverride{}, deps, nil,
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
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) { return nil, nil },
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
		context.Background(), "session", []tmux.Pane{pane}, false, config.Default(), restartLaunchOverride{}, deps, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "missing-profile") {
		t.Fatalf("error = %v, want named unresolved binding", err)
	}
}

func TestPrepareRestartLaunchPlanUsesSavedCommandAfterConfigAndTitleChange(t *testing.T) {
	saved := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex,
		Command: `codex -m 'pinned/model' -c 'model_reasoning_effort="high"' --custom 'keep me'`,
		Model:   "pinned/model", Persona: "architect", ReasoningEffort: "high",
	}
	dir := t.TempDir()
	deps := restartPaneDeps(&RestartPaneDependencies{
		LoadManifest: func(string) (*resilience.SpawnManifest, error) { return nil, os.ErrNotExist },
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
			copy := saved
			return &copy, nil
		},
		PaneWorkingDir: func(context.Context, string) (string, error) { return dir, nil },
		PrepareLaunchCommand: func(context.Context, string, string, *resilience.LaunchBinding, string) (string, resilience.LaunchAffinity, error) {
			t.Fatal("saved command fell back to reconstructed legacy launch")
			return "", "", nil
		},
	})
	cfg := config.Default()
	cfg.Agents.Codex = "codex --unrelated-current-config"
	pane := tmux.Pane{ID: "%7", Index: 2, NTMIndex: 4, Type: tmux.AgentCodex, Title: "rewritten by agent", Variant: "wrong-title-model"}
	plan, err := prepareRestartLaunchPlan(t.Context(), "session", []tmux.Pane{pane}, false, cfg, restartLaunchOverride{}, deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Commands["2"] != saved.Command || plan.Specs["2"].Persona != "architect" || plan.Replay["2"] == nil || plan.Directories["2"] != dir {
		t.Fatalf("saved launch was not preserved: %+v", plan)
	}
	for _, scenario := range []string{"corrupt read", "different provider", "missing environment"} {
		t.Run(scenario, func(t *testing.T) {
			brokenDeps := deps
			brokenDeps.ReadLaunchSpec = func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
				copy := saved
				switch scenario {
				case "corrupt read":
					return nil, errors.New("bad metadata")
				case "different provider":
					copy.AgentType = tmux.AgentClaude
				case "missing environment":
					copy.OmittedEnv = []string{"API_TOKEN"}
				}
				return &copy, nil
			}
			if _, err := prepareRestartLaunchPlan(t.Context(), "session", []tmux.Pane{pane}, false, cfg, restartLaunchOverride{}, brokenDeps, nil); err == nil {
				t.Fatal("unreplayable saved launch was silently reconstructed")
			}
		})
	}
}

func TestRestartSavedLaunchSpecOverridesPreserveExistingFlagsAndPersona(t *testing.T) {
	saved := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude,
		Command: `claude --model 'original' --effort 'low' --append-system-prompt-file '/work/persona.md' --custom 'literal;value'`,
		Model:   "original", Persona: "architect", ReasoningEffort: "low",
	}
	got, err := restartSavedLaunchSpec(config.Default(), saved, "claude", restartLaunchOverride{Model: "new/custom-model", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := " --model 'new/custom-model' --effort 'high'"
	if !strings.HasSuffix(got.Command, wantSuffix) || strings.Contains(got.Command, "--model 'original'") || strings.Contains(got.Command, "--effort 'low'") || !strings.Contains(got.Command, "--custom 'literal;value'") || !strings.Contains(got.Command, "--append-system-prompt-file '/work/persona.md'") || got.Persona != "architect" || got.Model != "new/custom-model" || got.ReasoningEffort != "high" {
		t.Fatalf("override lost launch context: %+v", got)
	}
	if saved.Model != "original" || saved.ReasoningEffort != "low" {
		t.Fatal("override modified predecessor specification")
	}
	opaqueArgs, err := restartSavedLaunchSpec(config.Default(), saved, "claude", restartLaunchOverride{Model: "new/custom-model", Effort: "high", Args: "--extra 'literal value'"})
	if err != nil || opaqueArgs.Model != "" || opaqueArgs.ModelAlias != "" || opaqueArgs.ReasoningEffort != "" || opaqueArgs.Persona != "" || !strings.HasSuffix(opaqueArgs.Command, "--extra 'literal value'") {
		t.Fatalf("raw argument overrides must retain exact command without claiming stale descriptors: %+v (%v)", opaqueArgs, err)
	}
	for _, command := range []string{
		"claude; echo later", "claude && echo later", "claude | cat", "claude > log",
		"claude # comment", "claude -- prompt", "claude mcp", "wrapper claude",
		"sh -c 'claude'", "claude $(cat flags)", "claude `cat flags`", `"cl\aude" --model old`,
		"BAD-NAME=x claude", "claude 'unterminated", "claude \\",
		"claude --model old mcp", "claude --profile account login", "claude --model *", "claude --model ~/alias", "claude --model {one,two}",
	} {
		t.Run(command, func(t *testing.T) {
			opaque := saved
			opaque.Command = command
			if _, err := restartSavedLaunchSpec(config.Default(), opaque, "claude", restartLaunchOverride{Model: "new"}); err == nil {
				t.Fatalf("opaque command accepted override: %q", command)
			}
			unchanged, err := restartSavedLaunchSpec(config.Default(), opaque, "claude", restartLaunchOverride{})
			if err != nil || unchanged.Command != command {
				t.Fatalf("zero-override replay should preserve opaque command: %+v (%v)", unchanged, err)
			}
		})
	}
	for _, args := range []string{"--extra foo; echo", "--extra $(cat flags)", "-- prompt", "--model last", "--effort low"} {
		if _, err := restartSavedLaunchSpec(config.Default(), saved, "claude", restartLaunchOverride{Args: args}); err == nil {
			t.Fatalf("opaque additional arguments accepted: %q", args)
		}
	}
}

func TestRestartSavedCodexModelOverrideReplacesScalarOption(t *testing.T) {
	defaultCommand, err := config.GenerateAgentCommand(config.DefaultAgentTemplates().Codex, config.AgentTemplateVars{Model: "initial/model"})
	if err != nil {
		t.Fatal(err)
	}
	repeated := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: defaultCommand}
	for _, nextModel := range []string{"first/model", "second/model"} {
		repeated, err = restartSavedLaunchSpec(config.Default(), repeated, "codex", restartLaunchOverride{Model: nextModel})
		if err != nil {
			t.Fatalf("repeated override %q failed: %v", nextModel, err)
		}
		if strings.Count(repeated.Command, "-m ") != 1 || !strings.Contains(repeated.Command, "-m '"+nextModel+"'") || !strings.Contains(repeated.Command, "--search") {
			t.Fatalf("repeated default-command override lost scalar selection: %q", repeated.Command)
		}
	}
	for _, modelArg := range []string{"-m old", "--model='old'", "-mold", "-m=old"} {
		for _, configArg := range []string{`-c 'model_reasoning_effort="low"'`, `--config 'model_reasoning_effort="low"'`, `--config='model_reasoning_effort="low"'`, `-c'model_reasoning_effort="low"'`, `-c='model_reasoning_effort="low"'`} {
			t.Run(modelArg+"/"+configArg, func(t *testing.T) {
				command := "MODE='literal value' codex --dangerously-bypass-approvals-and-sandbox " + modelArg + " " + configArg + ` -c 'model="old-config"' -c 'check_for_update_on_startup=false' --search`
				saved := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: command, Model: "old"}
				got, err := restartSavedLaunchSpec(config.Default(), saved, "codex", restartLaunchOverride{Model: "replacement/model", Effort: "high"})
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(got.Command, "old") || strings.Count(got.Command, "-m ") != 1 || strings.Count(got.Command, "model_reasoning_effort=") != 1 || !strings.Contains(got.Command, "-m 'replacement/model'") || !strings.Contains(got.Command, "MODE='literal value'") || !strings.Contains(got.Command, `-c 'check_for_update_on_startup=false'`) || !strings.Contains(got.Command, "--search") {
					t.Fatalf("scalar override retained duplicate flags or lost literal arguments: %q", got.Command)
				}
			})
		}
	}
	for _, args := range []string{`-cmodel='other'`, `-c='model_reasoning_effort="low"'`, `--config='model="other"'`, `-mnew`} {
		if err := validateRestartExtraArguments(args, "codex"); err == nil {
			t.Fatalf("raw scalar override accepted: %q", args)
		}
	}
	for _, command := range []string{`claude --system-prompt '--model' hello --model=old`, `claude '--model' old`, `claude --custom --model old`} {
		saved := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: command}
		if _, err := restartSavedLaunchSpec(config.Default(), saved, "claude", restartLaunchOverride{Model: "new"}); err == nil {
			t.Fatalf("ambiguous scalar flag/value accepted: %q", command)
		}
	}
}

func TestGetRestartPaneRefreshesMetadataAfterRespawnBeforeDelivery(t *testing.T) {
	for _, mode := range []string{"metadata-failure", "delivery-failure", "respawn-failure", "unverified-respawn", "dry-run"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "tmux")
			script := `#!/bin/sh
set -eu
printf '%s\n' "$1" >> "$NTM_RESTART_SPEC_TEST_ROOT/calls"
case "$1" in
  has-session) ;;
  if-shell)
    if [ -f "$NTM_RESTART_SPEC_TEST_ROOT/respawned" ]; then pid=222; else pid=111; fi
    case "$5" in *"#{pane_pid},$pid}"*) ;; *) printf 'ntm_pane_identity_changed\n'; exit 0 ;; esac
    eval "set -- $6"
    exec "$0" "$@"
    ;;
  display-message)
    case "$*" in *'@ntm_agent_type'*) printf '[cc]\n'; exit 0 ;; esac
    if [ -f "$NTM_RESTART_SPEC_TEST_ROOT/respawned" ]; then
      if [ "$NTM_RESTART_SPEC_TEST_MODE" = 'unverified-respawn' ]; then exit 55; fi
      printf '222\n'
    else
      printf '111\n'
    fi
    ;;
  respawn-pane)
    if [ "$NTM_RESTART_SPEC_TEST_MODE" = 'respawn-failure' ]; then exit 51; fi
    : > "$NTM_RESTART_SPEC_TEST_ROOT/respawned"
    ;;
  set-option) ;;
  load-buffer)
    [ -f "$NTM_RESTART_SPEC_TEST_ROOT/recorded" ] || exit 52
    cat >/dev/null
    exit 53
    ;;
  delete-buffer) ;;
  send-keys)
    [ -f "$NTM_RESTART_SPEC_TEST_ROOT/recorded" ] || exit 52
    exit 53
    ;;
  *) exit 54 ;;
esac
`
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGetRestartPaneMetadataTransportHelper$")
			cmd.Env = append(os.Environ(), "NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+path,
				"NTM_RESTART_SPEC_TEST_ROOT="+root, "NTM_RESTART_SPEC_TEST_MODE="+mode)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("restart metadata: %v\n%s", err, output)
			}
		})
	}
}

func TestGetRestartPaneMetadataTransportHelper(t *testing.T) {
	root := os.Getenv("NTM_RESTART_SPEC_TEST_ROOT")
	if root == "" {
		return
	}
	mode := os.Getenv("NTM_RESTART_SPEC_TEST_MODE")
	saved := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentClaude, Command: "claude --model 'original' --custom 'retained'", Model: "original"}
	var recorded *tmux.AgentLaunchSpec
	identityRefreshed := false
	SetRestartPaneIdentityHook(func(context.Context, string, []RestartedAgentPane) {
		identityRefreshed = true
	})
	t.Cleanup(func() { SetRestartPaneIdentityHook(nil) })
	deps := &RestartPaneDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			pane := tmux.Pane{ID: "%7", PID: 111, Index: 1, NTMIndex: 1, Type: tmux.AgentClaude, Command: "claude"}
			if _, err := os.Stat(filepath.Join(root, "respawned")); err == nil {
				pane.PID, pane.Command = 222, "sh"
			}
			return []tmux.Pane{pane}, nil
		},
		LoadManifest: func(string) (*resilience.SpawnManifest, error) { return nil, os.ErrNotExist },
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
			if recorded != nil {
				return cloneRestartLaunchSpec(recorded), nil
			}
			copy := saved
			return &copy, nil
		},
		PaneWorkingDir: func(context.Context, string) (string, error) { return root, nil },
		SetLaunchSpec: func(_ context.Context, target string, expectedPID int, spec tmux.AgentLaunchSpec) error {
			if target != "%7" {
				t.Fatalf("metadata target=%q", target)
			}
			if expectedPID != 222 {
				t.Fatalf("metadata replacement PID=%d, want 222", expectedPID)
			}
			if _, err := os.Stat(filepath.Join(root, "respawned")); err != nil {
				t.Fatal("metadata changed before old process was replaced")
			}
			if mode == "metadata-failure" {
				return errors.New("recording transport unavailable")
			}
			recorded = &spec
			return os.WriteFile(filepath.Join(root, "recorded"), nil, 0600)
		},
	}
	out, err := GetRestartPaneContext(t.Context(), RestartPaneOptions{
		Session: "session", Panes: []string{"1"}, Model: "new/model@high", AgentArgs: "--extra preserved",
		Config: config.Default(), ProjectDir: root, DryRun: mode == "dry-run", Deps: deps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if identityRefreshed != (mode == "metadata-failure" || mode == "delivery-failure") {
		t.Fatalf("identity refresh crossed an unverified respawn: mode=%s refreshed=%t", mode, identityRefreshed)
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if mode == "dry-run" {
		if !out.Success || !out.DryRun || recorded != nil || strings.Contains(string(calls), "respawn-pane") {
			t.Fatalf("dry run mutated lifecycle: output=%+v calls=%s", out, calls)
		}
		return
	}
	if out.Success || len(out.Failed) == 0 {
		t.Fatalf("partial lifecycle failure reported success: %+v", out)
	}
	if mode != "delivery-failure" {
		if recorded != nil || strings.Contains(string(calls), "send-keys") || strings.Contains(string(calls), "load-buffer") {
			t.Fatalf("launch crossed failed metadata/respawn boundary: recorded=%+v calls=%s", recorded, calls)
		}
		return
	}
	if recorded == nil || recorded.Model != "" || recorded.ReasoningEffort != "" || !strings.Contains(recorded.Command, "--custom 'retained'") || strings.Contains(recorded.Command, "--model 'original'") || !strings.Contains(recorded.Command, "--model 'new/model' --effort 'high' --extra preserved") {
		t.Fatalf("new launch specification was not preserved before delivery: %+v", recorded)
	}
	if len(out.Restarted) != 1 || !strings.Contains(string(calls), "load-buffer") {
		t.Fatalf("delivery failure evidence lost: output=%+v calls=%s", out, calls)
	}
}

// Exercise the shared restart surface with the same omitted keys reported in
// GH #367. The fake tmux executes the delivered launch command, so a successful
// result proves the replacement received the environment, not just that the
// preflight accepted a populated map.
func TestGetRestartPaneExplicitEnvironmentRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording tmux executable requires a POSIX shell")
	}
	for _, mode := range []string{
		"reported", "empty-value", "model-override", "ordinary", "missing", "partial", "unknown",
		"invalid-name", "nul-value", "multiline-value", "implicit", "multiple-targets", "native-recovery",
		"non-agent", "missing-spec", "invalid-directory", "delivery-failure",
		"stale-before-respawn", "stale-after-respawn", "stale-after-identity", "stale-after-metadata", "stale-spec-after-metadata", "stale-after-readiness",
		"long-command", "long-managed-command",
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(mode+"/dry-run="+strconv.FormatBool(dryRun), func(t *testing.T) {
				root := t.TempDir()
				binary := filepath.Join(root, "tmux")
				const script = `#!/bin/sh
set -eu
root="$NTM_RESTART_ENV_TEST_ROOT"
if [ "$1" = if-shell ]; then printf '%s\n' "$1" >> "$root/calls"; else printf '%s\n' "$*" >> "$root/calls"; fi
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) ;;
  if-shell)
    if [ -f "$root/respawned" ]; then pid="$NTM_RESTART_ENV_TEST_NEW_PID"; else pid=2000000001; fi
    if [ -f "$root/foreign-pid" ]; then pid=$((pid + 1)); fi
    case "$5" in *"#{pane_pid},$pid}"*) ;; *) printf 'ntm_pane_identity_changed\n'; exit 0 ;; esac
    eval "set -- $6"
    exec "$0" "$@"
    ;;
  display-message)
    case "$*" in *'@ntm_agent_type'*) printf '[cod]\n'; exit 0 ;; esac
    if [ -f "$root/respawned" ]; then
      if [ -f "$root/foreign-pid" ]; then printf '%s\n' "$((NTM_RESTART_ENV_TEST_NEW_PID + 1))"; else printf '%s\n' "$NTM_RESTART_ENV_TEST_NEW_PID"; fi
    else
      printf '2000000001\n'
    fi
    ;;
  respawn-pane) : > "$root/respawned" ;;
  set-option) ;;
  send-keys)
    [ -f "$root/recorded" ] || exit 51
    case "${4:-}" in
      -l)
        printf '%s' "$6" > "$root/delivered-command"
        if [ "$NTM_RESTART_ENV_TEST_MODE" = delivery-failure ]; then
          printf '%s' "$6" >&2
          exit 52
        fi
        ;;
      Enter)
        /bin/sh -c "$(cat "$root/delivered-command")" > "$root/agent-output" 2>&1 < /dev/null &
        ;;
      *) exit 53 ;;
    esac
    ;;
  load-buffer)
    [ -f "$root/recorded" ] || exit 54
    cat > "$root/delivered-command"
    if [ "$NTM_RESTART_ENV_TEST_MODE" = delivery-failure ]; then
      cat "$root/delivered-command" >&2
      exit 55
    fi
    ;;
  paste-buffer|delete-buffer) ;;
  capture-pane)
    if [ -f "$root/agent-launched" ]; then
      printf 'OpenAI Codex\n› Ask Codex to do anything\n100%% context left\n'
    fi
    ;;
  *) exit 56 ;;
esac
`
				if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRestartPaneEnvironmentRecoveryTransportHelper$")
				cmd.Env = append(os.Environ(), "NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+binary,
					"NTM_RESTART_ENV_TEST_ROOT="+root, "NTM_RESTART_ENV_TEST_MODE="+mode,
					"NTM_RESTART_ENV_TEST_DRY_RUN="+strconv.FormatBool(dryRun))
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("environment recovery: %v\n%s", err, output)
				}
			})
		}
	}
}

func TestRestartPaneEnvironmentRecoveryTransportHelper(t *testing.T) {
	root := os.Getenv("NTM_RESTART_ENV_TEST_ROOT")
	if root == "" {
		return
	}
	mode := os.Getenv("NTM_RESTART_ENV_TEST_MODE")
	dryRun := os.Getenv("NTM_RESTART_ENV_TEST_DRY_RUN") == "true"
	t.Setenv("NTM_RESTART_ENV_TEST_NEW_PID", strconv.Itoa(os.Getpid()))
	const agentScript = `#!/bin/sh
set -eu
root="$NTM_RESTART_ENV_TEST_ROOT"
printf '%s\000' "${AGENT_MAIL_AGENT-}" "${BEADS_DIR-}" "${BV_ROBOT_NOT_READY_LABELS-}" "${PATH-}" "${ZAI_SEAT_ROLE-}" > "$root/agent-environment"
printf '%s\000' "$@" > "$root/agent-argv"
printf '%s' "${AGENT_MAIL_PROJECT-}" > "$root/agent-mail-project"
: > "$root/agent-launched"
`
	agentPath := filepath.Join(root, "codex")
	if err := os.WriteFile(agentPath, []byte(agentScript), 0700); err != nil {
		t.Fatal(err)
	}
	keys := []string{"AGENT_MAIL_AGENT", "BEADS_DIR", "BV_ROBOT_NOT_READY_LABELS", "PATH", "ZAI_SEAT_ROLE"}
	environment := map[string]string{
		"AGENT_MAIL_AGENT":          "private-recovery-agent",
		"BEADS_DIR":                 filepath.Join(root, "private beads' directory"),
		"BV_ROBOT_NOT_READY_LABELS": "private-label,operator-gated",
		"PATH":                      "/usr/bin:/bin:/private-recovery-path",
		"ZAI_SEAT_ROLE":             "private-role'; $(touch " + tmux.ShellQuote(filepath.Join(root, "injected")) + ") literal suffix",
	}
	wantEnvironment := make([]string, len(keys))
	for i, key := range keys {
		wantEnvironment[i] = environment[key]
	}
	// Ambient values deliberately disagree with the explicit snapshot. Even
	// complete ambient values must not authorize recovery without opt-in.
	for _, key := range keys {
		if key != "PATH" {
			t.Setenv(key, "ambient-unrelated-value")
		}
	}
	pane := tmux.Pane{ID: "%7", Index: 2, NTMIndex: 2, PID: 2000000001, Type: tmux.AgentCodex,
		Title: "session__cod_2_saved", Command: "zsh", Width: 100, Height: 30}
	saved := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex,
		Command: tmux.ShellQuote(agentPath) + " -m saved/model -c 'model_reasoning_effort=high' --custom retained",
		Model:   "saved/model", ReasoningEffort: "high", Persona: "reviewer", OmittedEnv: keys}
	opts := RestartPaneOptions{Session: "session", Panes: []string{"%7"}, DryRun: dryRun,
		Config: config.Default(), ProjectDir: root, Environment: environment}
	ordinary := mode == "ordinary" || mode == "long-command" || mode == "long-managed-command"
	wantPreflight := mode == "reported" || mode == "empty-value" || mode == "model-override" || ordinary || mode == "delivery-failure" || strings.HasPrefix(mode, "stale-after-") || mode == "stale-spec-after-metadata"
	panes := []tmux.Pane{pane}
	switch mode {
	case "empty-value":
		environment["ZAI_SEAT_ROLE"] = ""
		wantEnvironment[4] = ""
	case "model-override":
		opts.Model = "replacement/model@low"
	case "ordinary", "long-command", "long-managed-command":
		saved.OmittedEnv = nil
		opts.Environment = nil
		if mode == "long-command" {
			saved.Command += " --payload " + tmux.ShellQuote(strings.Repeat("x", 6000))
		}
		if mode == "long-managed-command" {
			saved.AgentMailProject = "/" + strings.Repeat("managed-project/", 100)
		}
	case "missing":
		opts.Environment = nil
	case "partial":
		delete(environment, "ZAI_SEAT_ROLE")
	case "unknown":
		environment["UNRELATED_TOKEN"] = "private-unrelated-token"
	case "invalid-name":
		environment["INVALID=private-assignment"] = "private-invalid-name-value"
	case "nul-value":
		environment["ZAI_SEAT_ROLE"] = "private-nul-value\x00suffix"
	case "multiline-value":
		environment["ZAI_SEAT_ROLE"] = "private-control-value\nsuffix"
	case "implicit":
		opts.Panes = nil
	case "multiple-targets":
		second := pane
		second.ID, second.Index, second.NTMIndex = "%8", 3, 3
		panes = append(panes, second)
		opts.Panes = []string{"%7", "%8"}
	case "native-recovery":
		opts.Recovery = &NativeSessionRecovery{ExpectedPane: pane, ExpectedSpec: saved, WorkingDir: root, NativeSessionID: "original-conversation"}
	case "non-agent":
		panes[0].Type = tmux.AgentUser
	}
	var recorded *tmux.AgentLaunchSpec
	identityRefreshed := false
	SetRestartPaneIdentityHook(func(context.Context, string, []RestartedAgentPane) {
		identityRefreshed = true
		if mode == "stale-after-identity" {
			if err := os.WriteFile(filepath.Join(root, "foreign-pid"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Cleanup(func() { SetRestartPaneIdentityHook(nil) })
	listCalls := 0
	opts.Deps = &RestartPaneDependencies{
		ObserveAgentProcess: func(observeCtx context.Context, target string, rootPID int) (int, error) {
			if observeCtx.Err() != nil {
				return 0, observeCtx.Err()
			}
			if target != "%7" || rootPID != os.Getpid() {
				t.Fatalf("readiness lost the recorded replacement: target=%s pid=%d", target, rootPID)
			}
			// The recording transport has no tty or real foreground group.
			// Its launch marker is the explicit simulated process observation.
			if _, err := os.Stat(filepath.Join(root, "agent-launched")); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return 0, nil
				}
				return 0, err
			}
			return rootPID + 1, nil
		},
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			listCalls++
			current := append([]tmux.Pane(nil), panes...)
			if mode == "stale-before-respawn" && listCalls > 1 {
				current[0].PID++
			}
			if _, err := os.Stat(filepath.Join(root, "respawned")); err == nil {
				current[0].PID, current[0].Command = os.Getpid(), "sh"
				if mode == "stale-after-respawn" {
					current[0].PID++
				}
				if _, err := os.Stat(filepath.Join(root, "agent-launched")); err == nil {
					current[0].Command = "codex"
					current[0].Title, current[0].NTMIndex = "Provider task title", 0
					if mode == "stale-after-readiness" {
						current[0].PID++
					}
				}
			}
			if _, err := os.Stat(filepath.Join(root, "foreign-pid")); err == nil {
				current[0].PID++
			}
			return current, nil
		},
		LoadManifest: func(string) (*resilience.SpawnManifest, error) { return nil, os.ErrNotExist },
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
			if mode == "missing-spec" {
				return nil, nil
			}
			if recorded != nil {
				if mode == "stale-spec-after-metadata" {
					other := *recorded
					other.Command = "codex --model unrelated"
					return &other, nil
				}
				return cloneRestartLaunchSpec(recorded), nil
			}
			copy := saved
			return &copy, nil
		},
		PaneWorkingDir: func(context.Context, string) (string, error) {
			if mode == "invalid-directory" {
				return filepath.Join(root, "invalid\ndirectory"), nil
			}
			return root, nil
		},
		SetLaunchSpec: func(_ context.Context, target string, expectedPID int, spec tmux.AgentLaunchSpec) error {
			if target != "%7" {
				t.Fatalf("metadata target = %q", target)
			}
			if expectedPID != os.Getpid() {
				t.Fatalf("metadata replacement PID=%d, want %d", expectedPID, os.Getpid())
			}
			if _, err := os.Stat(filepath.Join(root, "respawned")); err != nil {
				t.Fatal("metadata changed before respawn")
			}
			recorded = &spec
			if mode == "stale-after-metadata" {
				if err := os.WriteFile(filepath.Join(root, "foreign-pid"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, err := json.Marshal(spec)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, "recorded"), data, 0600)
		},
	}
	out, err := GetRestartPaneContext(t.Context(), opts)
	if err != nil || out == nil {
		t.Fatalf("restart returned no structured result: %v", err)
	}
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	responseJSON, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	serializableOpts := opts
	serializableOpts.Deps, serializableOpts.Recovery = nil, nil
	optsJSON, err := json.Marshal(serializableOpts)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(read("calls"))
	publicData := calls + string(read("delivered-command")) + string(read("recorded")) + string(read("agent-output")) + string(read("agent-argv")) + string(responseJSON) + string(optsJSON)
	for _, value := range wantEnvironment {
		if value != "" && strings.Contains(publicData, value) {
			t.Fatal("recovery environment value appeared in commands, arguments, metadata, output, or diagnostics")
		}
	}
	for _, value := range []string{"private-unrelated-token", "private-assignment", "private-invalid-name-value", "private-nul-value", "private-control-value"} {
		if strings.Contains(publicData, value) {
			t.Fatal("invalid recovery input leaked into diagnostics")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "injected")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("environment value was interpreted as shell syntax")
	}
	if dryRun || !wantPreflight {
		if recorded != nil || identityRefreshed || len(out.Restarted) != 0 || strings.Contains(calls, "respawn-pane") || strings.Contains(calls, "send-keys") || strings.Contains(calls, "load-buffer") || strings.Contains(calls, "set-option") {
			t.Fatalf("preflight crossed mutation boundary: success=%t calls=%s", out.Success, calls)
		}
		if out.Success != wantPreflight {
			t.Fatalf("preflight success=%t, want %t: %s", out.Success, wantPreflight, out.Error)
		}
		if wantPreflight && (!out.DryRun || !reflect.DeepEqual(out.WouldAffect, []string{"2"})) {
			t.Fatalf("successful dry-run did not report the selected pane: %+v", out)
		}
		if !wantPreflight && out.Error == "" {
			t.Fatal("failed preflight omitted its explanation")
		}
		return
	}
	if strings.HasPrefix(mode, "stale-after-") || mode == "stale-spec-after-metadata" {
		if out.Success || len(out.Restarted) != 1 || len(out.Failed) == 0 || out.AgentRelaunched["2"] {
			t.Fatalf("changed replacement was reported as successfully relaunched: mode=%s output=%+v calls=%s", mode, out, calls)
		}
		if mode == "stale-after-respawn" || mode == "stale-after-identity" {
			if recorded != nil {
				t.Fatal("stale replacement had its launch metadata overwritten")
			}
		}
		if mode != "stale-after-readiness" && (strings.Contains(calls, "send-keys") || strings.Contains(calls, "load-buffer")) {
			t.Fatalf("launch input reached a changed replacement: %s", calls)
		}
		return
	}
	if !identityRefreshed || recorded == nil || !reflect.DeepEqual(recorded.OmittedEnv, saved.OmittedEnv) {
		t.Fatal("restart lost identity refresh or durable environment omission markers")
	}
	if err := recorded.ValidateReplay(tmux.AgentCodex); (err == nil) != ordinary {
		t.Fatal("recovery changed whether future unattended replay requires explicit environment")
	}
	if mode == "delivery-failure" {
		if out.Success || len(out.Failed) == 0 || len(out.Restarted) != 1 || out.AgentRelaunched["2"] {
			t.Fatalf("delivery failure reported successful recovery: %+v", out)
		}
		return
	}
	if !out.Success || !out.AgentRelaunched["2"] || !out.ProcessAlive["2"] || len(out.Restarted) != 1 {
		t.Fatalf("replacement agent was not relaunched: %+v", out)
	}
	if !ordinary {
		got := strings.Split(string(read("agent-environment")), "\x00")
		want := append(wantEnvironment, "")
		if !reflect.DeepEqual(got, want) {
			t.Fatal("replacement did not receive the exact explicit environment")
		}
	}
	if mode == "long-command" || mode == "long-managed-command" {
		launcher := string(read("delivered-command"))
		if len(launcher) > tmux.MaxTypedLaunchCommandBytes || !strings.HasPrefix(launcher, "/bin/sh -p ") {
			t.Fatal("long launch was typed into the interactive shell instead of using the private handoff")
		}
		if mode == "long-command" && !strings.Contains(string(read("agent-argv")), strings.Repeat("x", 6000)) {
			t.Fatal("private handoff truncated the long saved argument")
		}
		if mode == "long-managed-command" && string(read("agent-mail-project")) != saved.AgentMailProject {
			t.Fatal("private handoff did not preserve the full managed environment prefix")
		}
	}
	if mode == "model-override" {
		args := string(read("agent-argv"))
		if recorded.Model != "replacement/model" || recorded.ReasoningEffort != "low" || !strings.Contains(args, "replacement/model\x00") || !strings.Contains(args, "model_reasoning_effort=low\x00") || strings.Contains(args, "saved/model") {
			t.Fatal("environment recovery lost the explicit model or effort override")
		}
	} else if recorded.Command != saved.Command || recorded.Persona != saved.Persona || recorded.Model != saved.Model || recorded.ReasoningEffort != saved.ReasoningEffort {
		t.Fatal("environment recovery changed the saved launch command or persona")
	}
}
