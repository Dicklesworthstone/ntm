package robot

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

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
  display-message)
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
			return []tmux.Pane{{ID: "%7", Index: 1, NTMIndex: 1, Type: tmux.AgentClaude, Command: "claude"}}, nil
		},
		LoadManifest: func(string) (*resilience.SpawnManifest, error) { return nil, os.ErrNotExist },
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
			copy := saved
			return &copy, nil
		},
		PaneWorkingDir: func(context.Context, string) (string, error) { return root, nil },
		SetLaunchSpec: func(_ context.Context, target string, spec tmux.AgentLaunchSpec) error {
			if target != "%7" {
				t.Fatalf("metadata target=%q", target)
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
		"non-agent", "missing-spec", "delivery-failure",
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(mode+"/dry-run="+strconv.FormatBool(dryRun), func(t *testing.T) {
				root := t.TempDir()
				binary := filepath.Join(root, "tmux")
				const script = `#!/bin/sh
set -eu
root="$NTM_RESTART_ENV_TEST_ROOT"
printf '%s\n' "$*" >> "$root/calls"
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) ;;
  display-message)
    if [ -f "$root/respawned" ]; then
      printf '%s\n' "$NTM_RESTART_ENV_TEST_NEW_PID"
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
      printf 'OpenAI Codex\nCodex>\n100%% context left\n'
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
: > "$root/agent-launched"
`
	agentPath := filepath.Join(root, "codex")
	if err := os.WriteFile(agentPath, []byte(agentScript), 0700); err != nil {
		t.Fatal(err)
	}
	keys := []string{"AGENT_MAIL_AGENT", "BEADS_DIR", "BV_ROBOT_NOT_READY_LABELS", "PATH", "ZAI_SEAT_ROLE"}
	environment := map[string]string{
		"AGENT_MAIL_AGENT":           "private-recovery-agent",
		"BEADS_DIR":                  filepath.Join(root, "private beads' directory"),
		"BV_ROBOT_NOT_READY_LABELS": "private-label,operator-gated",
		"PATH":                       "/usr/bin:/bin:/private-recovery-path",
		"ZAI_SEAT_ROLE":              "private-role'; $(touch " + tmux.ShellQuote(filepath.Join(root, "injected")) + ") literal suffix",
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
	wantPreflight := mode == "reported" || mode == "empty-value" || mode == "model-override" || mode == "ordinary" || mode == "delivery-failure"
	panes := []tmux.Pane{pane}
	switch mode {
	case "empty-value":
		environment["ZAI_SEAT_ROLE"] = ""
		wantEnvironment[4] = ""
	case "model-override":
		opts.Model = "replacement/model@low"
	case "ordinary":
		saved.OmittedEnv = nil
		opts.Environment = nil
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
	})
	t.Cleanup(func() { SetRestartPaneIdentityHook(nil) })
	opts.Deps = &RestartPaneDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
		LoadManifest: func(string) (*resilience.SpawnManifest, error) { return nil, os.ErrNotExist },
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
			if mode == "missing-spec" {
				return nil, nil
			}
			copy := saved
			return &copy, nil
		},
		PaneWorkingDir: func(context.Context, string) (string, error) { return root, nil },
		SetLaunchSpec: func(_ context.Context, target string, spec tmux.AgentLaunchSpec) error {
			if target != "%7" {
				t.Fatalf("metadata target = %q", target)
			}
			if _, err := os.Stat(filepath.Join(root, "respawned")); err != nil {
				t.Fatal("metadata changed before respawn")
			}
			recorded = &spec
			data, err := json.Marshal(spec)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, "recorded"), data, 0600)
		},
	}
	if wantPreflight && !dryRun && mode != "delivery-failure" {
		// The real readiness gate needs a live child, independently of the
		// captured Codex banner. Keep one owned process until the test ends.
		child := exec.CommandContext(t.Context(), "sleep", "30")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = child.Process.Kill()
			_ = child.Wait()
		})
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
	if !identityRefreshed || recorded == nil || !reflect.DeepEqual(recorded.OmittedEnv, saved.OmittedEnv) {
		t.Fatal("restart lost identity refresh or durable environment omission markers")
	}
	if err := recorded.ValidateReplay(tmux.AgentCodex); (err == nil) != (mode == "ordinary") {
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
	if mode != "ordinary" {
		got := strings.Split(string(read("agent-environment")), "\x00")
		want := append(wantEnvironment, "")
		if !reflect.DeepEqual(got, want) {
			t.Fatal("replacement did not receive the exact explicit environment")
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
