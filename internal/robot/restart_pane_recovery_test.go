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
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// Run the public restart surface against a recording tmux executable in an
// isolated test process. This exercises the real respawn, PID confirmation,
// metadata publication, command delivery, and readiness paths without a server.
func TestGetRestartPaneNativeRecoveryBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording tmux executable requires a POSIX shell")
	}
	for _, mode := range []string{
		"success", "dry-run", "missing-target", "multiple-targets", "prompt-override", "model-override",
		"missing-spec", "omitted-env", "changed-prompt", "opaque-command", "missing-session-id",
		"stale-pane-before", "stale-spec-before", "stale-cwd-before",
		"stale-pane-after", "stale-spec-after", "stale-cwd-after",
		"callback-error", "callback-cancel", "stale-final-pid", "stale-respawn-boundary",
		"respawn-failure", "soft-respawn", "unverified-respawn", "metadata-failure", "delivery-failure",
	} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "tmux")
			const script = `#!/bin/sh
set -eu
if [ "$1" = if-shell ]; then printf '%s\n' "$1" >> "$NTM_NATIVE_RECOVERY_ROOT/calls"; else printf '%s\n' "$*" >> "$NTM_NATIVE_RECOVERY_ROOT/calls"; fi
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) ;;
  if-shell)
    if [ -f "$NTM_NATIVE_RECOVERY_ROOT/respawned" ]; then
      pid="$NTM_NATIVE_RECOVERY_NEW_PID"
    elif [ "$NTM_NATIVE_RECOVERY_MODE" = 'stale-respawn-boundary' ] && [ -f "$NTM_NATIVE_RECOVERY_ROOT/first-pid-probe" ]; then
      pid=112
    else
      pid=111
    fi
    case "$5" in *"#{pane_pid},$pid}"*) ;; *) printf 'ntm_pane_identity_changed\n'; exit 0 ;; esac
    eval "set -- $6"
    exec "$0" "$@"
    ;;
  display-message)
    case "$*" in *'@ntm_agent_type'*) printf '[cod]\n'; exit 0 ;; esac
    if [ -f "$NTM_NATIVE_RECOVERY_ROOT/respawned" ]; then
      case "$NTM_NATIVE_RECOVERY_MODE" in
        unverified-respawn) exit 55 ;;
        soft-respawn) printf '111\n' ;;
        *)
          : > "$NTM_NATIVE_RECOVERY_ROOT/verified-pid"
          printf '%s\n' "$NTM_NATIVE_RECOVERY_NEW_PID"
          ;;
      esac
    elif [ "$NTM_NATIVE_RECOVERY_MODE" = 'stale-final-pid' ]; then
      printf '112\n'
    elif [ "$NTM_NATIVE_RECOVERY_MODE" = 'stale-respawn-boundary' ]; then
      if [ -f "$NTM_NATIVE_RECOVERY_ROOT/first-pid-probe" ]; then
        printf '112\n'
      else
        : > "$NTM_NATIVE_RECOVERY_ROOT/first-pid-probe"
        printf '111\n'
      fi
    else
      printf '111\n'
    fi
    ;;
  respawn-pane)
    [ -f "$NTM_NATIVE_RECOVERY_ROOT/activated" ] || exit 51
    [ "$NTM_NATIVE_RECOVERY_MODE" != 'respawn-failure' ] || exit 52
    : > "$NTM_NATIVE_RECOVERY_ROOT/respawned"
    ;;
  set-option) ;;
  send-keys)
    [ -f "$NTM_NATIVE_RECOVERY_ROOT/recorded" ] || exit 53
    [ "$NTM_NATIVE_RECOVERY_MODE" != 'delivery-failure' ] || exit 54
    : > "$NTM_NATIVE_RECOVERY_ROOT/launched"
    ;;
  load-buffer)
    [ -f "$NTM_NATIVE_RECOVERY_ROOT/recorded" ] || exit 53
    cat >> "$NTM_NATIVE_RECOVERY_ROOT/calls"
    [ "$NTM_NATIVE_RECOVERY_MODE" != 'delivery-failure' ] || exit 54
    ;;
  paste-buffer|delete-buffer) ;;
  capture-pane) printf 'OpenAI Codex\n› Ask Codex to do anything\n100%% context left\n' ;;
  *) exit 56 ;;
esac
`
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGetRestartPaneNativeRecoveryTransportHelper$")
			cmd.Env = append(os.Environ(), "NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+binary,
				"NTM_NATIVE_RECOVERY_ROOT="+root, "NTM_NATIVE_RECOVERY_MODE="+mode)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("native recovery %s: %v\n%s", mode, err, output)
			}
		})
	}
}

func TestGetRestartPaneNativeRecoveryTransportHelper(t *testing.T) {
	root := os.Getenv("NTM_NATIVE_RECOVERY_ROOT")
	if root == "" {
		return
	}
	mode := os.Getenv("NTM_NATIVE_RECOVERY_MODE")
	t.Setenv("NTM_NATIVE_RECOVERY_NEW_PID", strconv.Itoa(os.Getpid()))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pane := tmux.Pane{ID: "%7", Index: 1, NTMIndex: 2, PID: 111, Type: tmux.AgentCodex,
		Title: "recovery__cod_2_reviewer", Command: "codex", Width: 100, Height: 30}
	saved := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex,
		Command: "/opt/codex resume 'old-conversation' --model saved/model -c 'model_reasoning_effort=high' --custom=retained",
		Model:   "saved/model", ModelAlias: "saved", ReasoningEffort: "high", Persona: "reviewer"}
	switch mode {
	case "omitted-env":
		saved.OmittedEnv = []string{"EXPLICIT_API_TOKEN"}
	case "changed-prompt":
		saved.SystemPromptFile = filepath.Join(root, "persona.md")
		saved.SystemPromptSHA256 = strings.Repeat("0", 64)
		if err := os.WriteFile(saved.SystemPromptFile, []byte("changed instructions"), 0600); err != nil {
			t.Fatal(err)
		}
	case "opaque-command":
		saved.Command = "unverified-wrapper codex --model saved/model"
	}
	beforeSpec, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	livePane, liveSpec, liveDir := pane, saved, root
	activationCalls, listCalls := 0, 0
	var recorded *tmux.AgentLaunchSpec
	appendEvent := func(event string) {
		t.Helper()
		file, err := os.OpenFile(filepath.Join(root, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteString(event + "\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("record event: write=%v close=%v", writeErr, closeErr)
		}
	}
	deps := &RestartPaneDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			listCalls++
			if mode == "missing-target" {
				return nil, nil
			}
			if listCalls > 1 {
				switch mode {
				case "stale-pane-before":
					livePane.PID = 112
				case "stale-spec-before":
					liveSpec.Command = "codex --model replacement"
				case "stale-cwd-before":
					liveDir = filepath.Dir(root)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "respawned")); err == nil {
				livePane.PID, livePane.Command = os.Getpid(), "sh"
				if _, err := os.Stat(filepath.Join(root, "launched")); err == nil {
					livePane.Command = "codex"
				}
			}
			panes := []tmux.Pane{livePane}
			if mode == "multiple-targets" {
				other := pane
				other.ID, other.Index, other.NTMIndex = "%8", 2, 3
				panes = append(panes, other)
			}
			return panes, nil
		},
		LoadManifest: func(string) (*resilience.SpawnManifest, error) { return nil, os.ErrNotExist },
		ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
			if mode == "missing-spec" {
				return nil, nil
			}
			if recorded != nil {
				return cloneRestartLaunchSpec(recorded), nil
			}
			copy := liveSpec
			return &copy, nil
		},
		PaneWorkingDir: func(context.Context, string) (string, error) { return liveDir, nil },
		ObserveAgentProcess: func(observeCtx context.Context, target string, rootPID int) (int, error) {
			if observeCtx.Err() != nil {
				return 0, observeCtx.Err()
			}
			if target != pane.ID || rootPID != os.Getpid() {
				t.Fatalf("readiness lost the recorded replacement: target=%s pid=%d", target, rootPID)
			}
			// This transport has no real tty. Supply its simulated foreground
			// observation explicitly; real-tmux tests exercise the live observer.
			if _, err := os.Stat(filepath.Join(root, "launched")); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return 0, nil
				}
				return 0, err
			}
			return rootPID + 1, nil
		},
		SetLaunchSpec: func(_ context.Context, target string, expectedPID int, spec tmux.AgentLaunchSpec) error {
			if target != pane.ID {
				t.Fatalf("recovery metadata retargeted from %s to %s", pane.ID, target)
			}
			if expectedPID != os.Getpid() {
				t.Fatalf("metadata replacement PID=%d, want %d", expectedPID, os.Getpid())
			}
			if _, err := os.Stat(filepath.Join(root, "verified-pid")); err != nil {
				t.Fatal("metadata changed before a new process was positively observed")
			}
			if mode == "metadata-failure" {
				return errors.New("metadata persistence unavailable")
			}
			recorded = &spec
			appendEvent("metadata")
			data, err := json.Marshal(spec)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, "recorded"), data, 0600)
		},
	}
	recovery := &NativeSessionRecovery{ExpectedPane: pane, ExpectedSpec: saved, WorkingDir: root, NativeSessionID: "current-conversation"}
	recovery.BeforeRespawn = func(callbackCtx context.Context) error {
		if callbackCtx != ctx {
			t.Fatal("account activation lost the caller's context")
		}
		activationCalls++
		appendEvent("activate")
		if err := os.WriteFile(filepath.Join(root, "activated"), nil, 0600); err != nil {
			return err
		}
		switch mode {
		case "stale-pane-after":
			livePane.PID = 112
		case "stale-spec-after":
			liveSpec.Command = "codex --model replacement"
		case "stale-cwd-after":
			liveDir = filepath.Dir(root)
		case "callback-error":
			return errors.New("account activation refused")
		case "callback-cancel":
			cancel()
		}
		return nil
	}
	if mode == "missing-session-id" {
		recovery.NativeSessionID = ""
	}
	settings := config.Default()
	settings.Agents.Codex = "codex --model unrelated-default"
	opts := RestartPaneOptions{Session: "recovery", Panes: []string{pane.ID}, Config: settings,
		ProjectDir: root, Deps: deps, Recovery: recovery, DryRun: mode == "dry-run"}
	switch mode {
	case "multiple-targets":
		opts.Panes = nil
	case "prompt-override":
		opts.Prompt = "a different task"
	case "model-override":
		opts.Model = "a-different-model"
	}
	out, err := GetRestartPaneContext(ctx, opts)
	if err != nil || out == nil {
		t.Fatalf("restart result=%+v err=%v", out, err)
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(calls)
	afterSpec, err := json.Marshal(saved)
	if err != nil || string(beforeSpec) != string(afterSpec) {
		t.Fatalf("native recovery changed its saved input: %s -> %s, %v", beforeSpec, afterSpec, err)
	}
	if mode == "dry-run" {
		if !out.Success || !out.DryRun || len(out.WouldAffect) != 1 || activationCalls != 0 || recorded != nil || strings.Contains(log, "respawn-pane") {
			t.Fatalf("dry run changed account or pane: output=%+v activations=%d calls=%s", out, activationCalls, log)
		}
		return
	}
	if mode == "success" || mode == "delivery-failure" {
		expected := saved
		expected.Command = "/opt/codex resume 'current-conversation' --model saved/model -c 'model_reasoning_effort=high' --custom=retained"
		if recorded == nil || !reflect.DeepEqual(*recorded, expected) {
			t.Fatalf("native resume dropped recorded settings: got=%+v want=%+v", recorded, expected)
		}
		command := "cd " + tmux.ShellQuote(root) + " && " + expected.Command
		if !strings.Contains(log, command) || strings.Contains(log, "unrelated-default") || strings.Contains(log, "old-conversation") {
			t.Fatalf("recovery delivered the wrong command or worktree: %s", log)
		}
		activateAt, respawnAt := strings.Index(log, "activate\n"), strings.Index(log, "respawn-pane")
		metadataAt, sendAt := strings.Index(log, "metadata\n"), strings.Index(log, "send-keys")
		if activationCalls != 1 || activateAt < 0 || respawnAt <= activateAt || metadataAt <= respawnAt || sendAt <= metadataAt {
			t.Fatalf("recovery crossed lifecycle boundaries out of order: activations=%d calls=%s", activationCalls, log)
		}
		if len(out.Restarted) != 1 || out.PaneShellPIDs["1"].Before != 111 || out.PaneShellPIDs["1"].After != os.Getpid() {
			t.Fatalf("verified respawn evidence lost: %+v", out)
		}
		if mode == "success" {
			if !out.Success || !out.AgentRelaunched["1"] || !out.ProcessAlive["1"] || len(out.Failed) != 0 {
				t.Fatalf("ready native recovery was not reported: %+v", out)
			}
			return
		}
	}
	if out.Success {
		t.Fatalf("incomplete native recovery reported success: mode=%s output=%+v calls=%s", mode, out, log)
	}
	if mode == "delivery-failure" {
		if out.AgentRelaunched["1"] || len(out.Failed) == 0 {
			t.Fatalf("failed native command delivery counted as ready: %+v", out)
		}
		return
	}
	if recorded != nil || strings.Contains(log, "send-keys") {
		t.Fatalf("failed recovery published or delivered a replacement command: mode=%s recorded=%+v calls=%s", mode, recorded, log)
	}
	wantActivation := 0
	switch mode {
	case "stale-pane-after", "stale-spec-after", "stale-cwd-after", "callback-error", "callback-cancel",
		"stale-final-pid", "stale-respawn-boundary", "respawn-failure", "soft-respawn", "unverified-respawn", "metadata-failure":
		wantActivation = 1
	}
	if activationCalls != wantActivation {
		t.Fatalf("activation crossed a failed preflight: mode=%s got=%d want=%d calls=%s", mode, activationCalls, wantActivation, log)
	}
	switch mode {
	case "respawn-failure", "soft-respawn", "unverified-respawn", "metadata-failure":
		if !strings.Contains(log, "respawn-pane") || len(out.Failed) == 0 {
			t.Fatalf("partial respawn effects disappeared: output=%+v calls=%s", out, log)
		}
	default:
		if strings.Contains(log, "respawn-pane") {
			t.Fatalf("native recovery killed a pane after refusal: mode=%s output=%+v calls=%s", mode, out, log)
		}
	}
}

// Exercise prompt policy through the public robot envelope and the production
// guarded sender. The recording transport deliberately holds the staged prompt
// until submission verification rescues it, proving that verification uses the
// same prepared bytes as delivery.
func TestPrintRestartPaneOrdinaryPromptPolicyPreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording tmux executable requires a POSIX shell")
	}
	const secret = "hunter2hunter2"
	for _, test := range []struct {
		name, mode, badConfig                                    string
		plain, noPrompt, batch, legacy, dead, missingEnvironment bool
	}{
		{name: "block", mode: "block"},
		{name: "redact", mode: "redact"},
		{name: "warn", mode: "warn"},
		{name: "off", mode: "off"},
		{name: "safe-block", mode: "block", plain: true},
		{name: "blocked-batch", mode: "block", batch: true},
		{name: "legacy-title", mode: "redact", legacy: true},
		{name: "dead-pane", mode: "redact", dead: true},
		{name: "missing-environment", mode: "redact", missingEnvironment: true},
		{name: "invalid-global-policy", mode: "off", badConfig: "global"},
		{name: "invalid-project-policy", mode: "off", badConfig: "project"},
		{name: "no-prompt-keeps-config-fallback", mode: "off", badConfig: "global", noPrompt: true},
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry-run=%t", test.name, dryRun), func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("HOME", root)
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
				t.Setenv("NTM_RESTART_PROMPT_POLICY_ROOT", root)
				binary := filepath.Join(root, "tmux")
				const script = `#!/bin/sh
set -eu
root="$NTM_RESTART_PROMPT_POLICY_ROOT"
printf '%s\n' "$1" >> "$root/operations"
if [ "$1" != if-shell ]; then printf '%s\n' "$*" >> "$root/calls"; fi
case "$1" in
  has-session) ;;
  if-shell)
    pid=2000000001
    if [ -f "$root/respawned" ]; then pid=2000000002; fi
    case "$5" in *"#{pane_pid},$pid}"*) ;; *) printf 'ntm_pane_identity_changed\n'; exit 0 ;; esac
    eval "set -- $6"
    exec "$0" "$@"
    ;;
  display-message)
    case "$*" in
      *@ntm_agent_type*) printf '[codex]\n' ;;
      *pane_current_command*)
        if [ -f "$root/launched" ]; then printf 'codex\n'; else printf 'sh\n'; fi
        ;;
      *pane_pid*)
        if [ -f "$root/respawned" ]; then printf '2000000002\n'; else printf '2000000001\n'; fi
        ;;
      *) exit 51 ;;
    esac
    ;;
  respawn-pane) : > "$root/respawned" ;;
  set-option) ;;
  send-keys)
    [ -f "$root/recorded" ] || exit 52
    case "$4" in
      -l)
        if [ -f "$root/launched" ]; then
          printf '%s' "$6" > "$root/prompt"
        else
          printf '%s' "$6" > "$root/launch-command"
        fi
        ;;
      Enter)
        if [ -f "$root/launched" ]; then
          [ -f "$root/prompt" ] || exit 53
          count=0
          if [ -f "$root/prompt-enters" ]; then count=$(cat "$root/prompt-enters"); fi
          printf '%s' "$((count + 1))" > "$root/prompt-enters"
        else
          : > "$root/launched"
        fi
        ;;
      *) exit 54 ;;
    esac
    ;;
  load-buffer)
    [ -f "$root/recorded" ] || exit 55
    if [ -f "$root/launched" ]; then cat > "$root/prompt"; else cat > "$root/launch-command"; fi
    ;;
  paste-buffer|delete-buffer) ;;
  capture-pane)
    [ -f "$root/launched" ] || exit 0
    count=0
    if [ -f "$root/prompt-enters" ]; then count=$(cat "$root/prompt-enters"); fi
    if [ -f "$root/prompt" ] && [ "$count" -lt 3 ]; then
      printf 'OpenAI Codex\n› '
      cat "$root/prompt"
      printf '\n'
    else
      printf 'OpenAI Codex\n› Ask Codex to do anything\n100%% context left\n'
    fi
    ;;
  *) exit 56 ;;
esac
`
				if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("NTM_TMUX_BINARY", binary)
				originalClient := tmux.DefaultClient
				tmux.DefaultClient = tmux.NewClient("")
				t.Cleanup(func() { tmux.DefaultClient = originalClient })
				policy := config.Default()
				policy.Redaction.Mode = test.mode
				if test.badConfig != "" {
					t.Chdir(root)
					path := config.DefaultPath()
					if test.badConfig == "project" {
						path = filepath.Join(root, ".ntm", "config.toml")
					}
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("[redaction]\nmode = \""+secret), 0600); err != nil {
						t.Fatal(err)
					}
					policy = nil
				}
				pane := tmux.Pane{ID: "%7", Index: 2, PID: 2000000001, Type: tmux.AgentCodex,
					Title: "restart-policy__cod_2", Command: "sh", Width: 100, Height: 30}
				if test.legacy {
					pane.Type = tmux.AgentUnknown
				}
				if test.dead {
					pane.Dead, pane.Command = true, "codex"
				}
				panes := []tmux.Pane{pane}
				selectors := []string{pane.ID}
				if test.batch {
					second := pane
					second.ID, second.WindowIndex, second.Index = "%8", 1, 0
					panes = append(panes, second)
					selectors = append(selectors, second.ID)
				}
				saved := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: "codex"}
				if test.missingEnvironment {
					saved.OmittedEnv = []string{"REQUIRED_API_TOKEN"}
					t.Setenv("REQUIRED_API_TOKEN", "ambient-must-not-authorize-recovery")
				}
				prompt := "password=" + secret
				if test.plain {
					prompt = "Continue safe work"
				}
				if test.noPrompt {
					prompt = ""
				}
				exists := func(name string) bool {
					t.Helper()
					_, err := os.Stat(filepath.Join(root, name))
					if err != nil && !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
					return err == nil
				}
				read := func(name string) string {
					t.Helper()
					data, err := os.ReadFile(filepath.Join(root, name))
					if err != nil && !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
					return string(data)
				}
				var recorded *tmux.AgentLaunchSpec
				var launchReads, identityCalls int
				SetRestartPaneIdentityHook(func(context.Context, string, []RestartedAgentPane) { identityCalls++ })
				t.Cleanup(func() { SetRestartPaneIdentityHook(nil) })
				deps := RestartPaneDependencies{
					ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
						current := append([]tmux.Pane(nil), panes...)
						if exists("respawned") {
							current[0].PID, current[0].Command, current[0].Dead = 2000000002, "sh", false
						}
						if exists("launched") {
							current[0].Type, current[0].Command = tmux.AgentCodex, "codex"
						}
						return current, nil
					},
					LoadManifest: func(string) (*resilience.SpawnManifest, error) { return nil, os.ErrNotExist },
					ReadLaunchSpec: func(context.Context, string) (*tmux.AgentLaunchSpec, error) {
						launchReads++
						if recorded != nil {
							return cloneRestartLaunchSpec(recorded), nil
						}
						return cloneRestartLaunchSpec(&saved), nil
					},
					PaneWorkingDir: func(context.Context, string) (string, error) { return root, nil },
					SetLaunchSpec: func(_ context.Context, target string, pid int, spec tmux.AgentLaunchSpec) error {
						if target != pane.ID || pid != 2000000002 || !exists("respawned") {
							t.Fatal("launch metadata was published outside the verified replacement")
						}
						recorded = cloneRestartLaunchSpec(&spec)
						return os.WriteFile(filepath.Join(root, "recorded"), []byte("recorded"), 0600)
					},
					ObserveAgentProcess: func(ctx context.Context, target string, pid int) (int, error) {
						if ctx.Err() != nil {
							return 0, ctx.Err()
						}
						if target != pane.ID || pid != 2000000002 {
							t.Fatal("readiness lost the recorded replacement identity")
						}
						if exists("launched") {
							return 2000000003, nil
						}
						return 0, nil
					},
				}
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				encoded, terminalErr := captureStdout(t, func() error {
					return PrintRestartPaneContext(ctx, RestartPaneOptions{
						Session: "restart-policy", Panes: selectors, DryRun: dryRun,
						Prompt: prompt, Config: policy, Deps: &deps,
					})
				})
				var out RestartPaneOutput
				if err := json.Unmarshal([]byte(encoded), &out); err != nil {
					t.Fatalf("restart did not emit a valid robot envelope: %v", err)
				}
				if strings.Contains(encoded+fmt.Sprint(terminalErr), secret) {
					t.Fatal("restart prompt or malformed policy leaked into diagnostics")
				}
				if (test.mode == "block" || test.mode == "redact") && strings.Contains(read("calls")+read("prompt"), secret) {
					t.Fatal("blocked or redacted prompt reached the transport unfiltered")
				}
				blocked := test.mode == "block" && !test.plain && !test.noPrompt
				badPolicy := test.badConfig != "" && !test.noPrompt
				refused := blocked || badPolicy || test.missingEnvironment
				if out.DryRun != dryRun {
					t.Fatal("restart lost the requested preview mode")
				}
				if prompt != "" && !badPolicy {
					if out.Redaction == nil || out.Redaction.Mode != test.mode {
						t.Fatal("restart omitted the effective outgoing-message policy")
					}
					if test.mode != "off" && !test.plain && out.Redaction.Findings == 0 {
						t.Fatal("restart prompt policy did not scan the requested content")
					}
					if (test.mode == "warn" || test.mode == "redact") && len(out.Warnings) == 0 {
						t.Fatal("restart discarded the safe policy warning")
					}
				}
				if dryRun || refused {
					if exists("respawned") || exists("launched") || exists("prompt") || recorded != nil ||
						identityCalls != 0 || len(out.Restarted) != 0 || out.PromptSent {
						t.Fatal("preview or refused prompt crossed the restart mutation boundary")
					}
					for _, operation := range []string{"respawn-pane", "set-option", "send-keys", "load-buffer", "paste-buffer"} {
						if strings.Contains(read("operations"), operation+"\n") {
							t.Fatalf("preview or refusal reached %s", operation)
						}
					}
					if refused {
						if out.Success || terminalErr == nil || len(out.WouldAffect) != 0 {
							t.Fatal("refused restart advertised success or affected panes")
						}
						if blocked && !strings.Contains(out.Error, "redaction_blocked") {
							t.Fatal("blocked prompt did not return the shared dispatch-policy refusal")
						}
						if badPolicy && !strings.Contains(out.Error, "configuration") {
							t.Fatal("broken policy was not refused explicitly")
						}
						if test.missingEnvironment && !strings.Contains(out.Error, "REQUIRED_API_TOKEN") {
							t.Fatal("prompt policy bypassed the missing recovery-environment refusal")
						}
						if (blocked || badPolicy) && launchReads != 0 {
							t.Fatal("prompt policy refusal happened after launch preparation")
						}
					} else if !out.Success || terminalErr != nil || !reflect.DeepEqual(out.WouldAffect, []string{"2"}) {
						t.Fatal("allowed prompt preview did not succeed without mutation")
					}
					return
				}
				if !out.Success || terminalErr != nil || !out.AgentRelaunched["2"] || !out.ProcessAlive["2"] ||
					identityCalls != 1 || !reflect.DeepEqual(out.Restarted, []string{"2"}) {
					t.Fatalf("allowed restart did not complete: success=%t code=%s error=%s", out.Success, out.ErrorCode, out.Error)
				}
				if test.noPrompt {
					if out.PromptSent || exists("prompt") || out.Redaction != nil {
						t.Fatal("prompt-free restart unexpectedly prepared or delivered a message")
					}
					return
				}
				delivered := read("prompt")
				if test.mode == "redact" {
					if delivered == prompt || !strings.Contains(delivered, "[REDACTED:") {
						t.Fatal("redact mode did not deliver the prepared sanitized message")
					}
				} else if delivered != prompt {
					t.Fatal("allowed prompt bytes changed unexpectedly")
				}
				if !out.PromptSent || out.PromptDelivery["2"] != RestartPromptDelivered || read("prompt-enters") != "3" {
					t.Fatal("submission verification did not rescue and confirm the exact prepared prompt")
				}
			})
		}
	}
}

func TestRestartReadinessRequiresStableForegroundEvidence(t *testing.T) {
	for _, tc := range []struct {
		name             string
		observations     []int
		captureFailureAt int
		wantReady        bool
		wantCaptures     int
	}{
		{name: "stable agent", observations: []int{71, 71, 71, 71}, wantReady: true, wantCaptures: 2},
		{name: "launch wrapper only", observations: []int{0}, wantReady: false},
		{name: "wrapper starts agent", observations: []int{0, 0, 71, 71, 71, 71}, wantReady: true, wantCaptures: 3},
		{name: "process changes during capture", observations: []int{71, 72, 72, 72, 72, 72}, wantReady: true, wantCaptures: 3},
		{name: "process changes between captures", observations: []int{71, 71, 72, 72, 72, 72}, wantReady: true, wantCaptures: 3},
		{name: "agent stops after one observation", observations: []int{71, 71, 0}, wantReady: false},
		{name: "capture failure resets evidence", observations: []int{71}, captureFailureAt: 2, wantReady: true, wantCaptures: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observations, captures int
			ready, err := waitForPaneAgentReadyWithContext(t.Context(), "%7", 100, "codex",
				100*time.Millisecond, time.Millisecond,
				func(ctx context.Context, target string) (string, error) {
					if ctx.Err() != nil || target != "%7" {
						t.Fatalf("capture lost pane/context: target=%s err=%v", target, ctx.Err())
					}
					captures++
					if captures == tc.captureFailureAt {
						return "", errors.New("capture unavailable")
					}
					return "OpenAI Codex\n› Ask Codex to do anything\n", nil
				},
				func(ctx context.Context, target string, rootPID int) (int, error) {
					if ctx.Err() != nil || target != "%7" || rootPID != 100 {
						t.Fatalf("observation lost expected root: target=%s pid=%d err=%v", target, rootPID, ctx.Err())
					}
					pid := tc.observations[min(observations, len(tc.observations)-1)]
					observations++
					return pid, nil
				})
			if err != nil || ready != tc.wantReady {
				t.Fatalf("readiness=%v err=%v, want %v", ready, err, tc.wantReady)
			}
			if tc.wantCaptures > 0 && captures != tc.wantCaptures {
				t.Fatalf("captured %d frames, want %d stable-evidence frames", captures, tc.wantCaptures)
			}
		})
	}

	t.Run("replacement during capture refuses readiness", func(t *testing.T) {
		var observations, captures int
		ready, err := waitForPaneAgentReadyWithContext(t.Context(), "%7", 100, "codex",
			time.Second, time.Millisecond,
			func(context.Context, string) (string, error) {
				captures++
				return "OpenAI Codex\n›\n", nil
			},
			func(context.Context, string, int) (int, error) {
				observations++
				if observations == 2 {
					return 0, tmux.ErrPaneIdentityChanged
				}
				return 71, nil
			})
		if ready || !errors.Is(err, tmux.ErrPaneIdentityChanged) || captures != 1 {
			t.Fatalf("changed pane ready=%v err=%v captures=%d", ready, err, captures)
		}
	})

	t.Run("unknown root is not optional", func(t *testing.T) {
		ready, err := waitForPaneAgentReadyWithContext(t.Context(), "%7", 0, "codex",
			time.Second, time.Millisecond,
			func(context.Context, string) (string, error) {
				t.Fatal("unknown root reached capture")
				return "", nil
			},
			func(context.Context, string, int) (int, error) {
				t.Fatal("unknown root reached process observation")
				return 71, nil
			})
		if ready || err == nil {
			t.Fatalf("unknown root readiness=%v err=%v", ready, err)
		}
	})
}

func TestRestartReadinessRequiresCurrentCodexUI(t *testing.T) {
	for _, tc := range []struct {
		name, kind, output string
		want               bool
	}{
		{"banner only", "codex", "OpenAI Codex starting\n", false},
		{"shell prompt", "codex", "$\n", false},
		{"update dialog", "codex", "OpenAI Codex\nCodex update available!\n› 1. Update now\n  2. Skip\nPress enter to continue\n", false},
		{"trust dialog", "cod", "Do you trust the contents of this directory?\n› 1. Yes, continue\n  2. No, quit\n", false},
		{"ultra numbered menu", "codex-cli", "Choose a model\n» 1) Codex\n  2) Cancel\n", false},
		{"unnumbered dialog chrome", "codex", "› Yes, continue\nPress Enter to Continue\n", false},
		{"transcript browsing", "codex", "› an earlier prompt\nBrowsing transcript · ↵ rewind · esc back\n", false},
		{"composer", "cod", "OpenAI Codex\n› Ask Codex to do anything\n", true},
		{"ultra composer", "codex-cli", "» Ask Codex to do anything\n", true},
		{"composer after dismissed dialog", "codex", "› 1. Update now\nPress enter to continue\n› Ask Codex to do anything\n", true},
		{"held draft composer", "codex", "» keep this unsubmitted draft\n", true},
		{"working", "codex", "• Working (12s • esc to interrupt)\n", true},
		{"other provider unchanged", "claude", "Claude Code v1.2.3\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready, err := waitForPaneAgentReadyWithContext(t.Context(), "%7", 100, tc.kind,
				50*time.Millisecond, time.Millisecond,
				func(context.Context, string) (string, error) { return tc.output, nil },
				func(context.Context, string, int) (int, error) { return 101, nil })
			if err != nil || ready != tc.want {
				t.Fatalf("readiness=%v err=%v, want %v", ready, err, tc.want)
			}
		})
	}
}

func TestRestartPSForegroundRequiresMatchingTerminalAndRunningState(t *testing.T) {
	for _, tc := range []struct {
		name, snapshot string
		candidate      int
		want           bool
	}{
		{"running wrapper child", "10 pts/3 Ss\n20 pts/3 S+\n", 20, true},
		{"exec over root", "10 ttys001 S+\n", 10, true},
		{"another terminal", "10 pts/3 Ss\n20 pts/4 S+\n", 20, false},
		{"background process", "10 pts/3 Ss+\n20 pts/3 S\n", 20, false},
		{"stopped", "10 ttys001 Ss\n20 ttys001 T+\n", 20, false},
		{"tracing stop", "10 pts/3 Ss\n20 pts/3 t+\n", 20, false},
		{"zombie", "10 pts/3 Ss\n20 pts/3 Z+\n", 20, false},
		{"no terminal", "10 ?? S\n20 ?? S+\n", 20, false},
		{"missing root", "20 pts/3 S+\n", 20, false},
		{"missing candidate", "10 pts/3 Ss\n", 20, false},
		{"malformed", "unavailable\n", 20, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := restartPSForegroundMatches(tc.snapshot, 10, tc.candidate); got != tc.want {
				t.Fatalf("foreground=%v, want %v", got, tc.want)
			}
		})
	}
}

// Exercise the production observer through the public restart operation.
// The stopped case is the real failure: the private FIFO shell has printed
// the Codex startup banner but stops before any provider is launched.
// A running provider showing a startup dialog is also not ready for a prompt.
func TestRealRestartReadinessRequiresRunningForegroundProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real tmux fixture requires POSIX process control")
	}
	testutil.RequireTmuxThrottled(t)
	for _, mode := range []string{"direct-agent", "exec-agent", "fifo-wrapper", "stopped-fifo", "stopped-fifo-with-prompt", "banner-helper", "startup-dialog"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			session := fmt.Sprintf("ntm-restart-readiness-%d", time.Now().UnixNano())
			if _, err := tmux.DefaultClient.RunContext(t.Context(), "new-session", "-d", "-s", session,
				"-c", root, "/bin/sh", "-p", "-i"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tmux.KillSession(session) })

			var pane tmux.Pane
			deadline := time.Now().Add(5 * time.Second)
			for {
				panes, err := tmux.GetPanesContext(t.Context(), session)
				if err != nil {
					t.Fatal(err)
				}
				if len(panes) == 1 && panes[0].PID > 0 && panes[0].RunsBareShell() {
					pane = panes[0]
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("fixture never reached its initial shell: %+v", panes)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := tmux.DefaultClient.SetPaneAgentTypeContext(t.Context(), pane.ID, tmux.AgentCodex); err != nil {
				t.Fatal(err)
			}

			command := "printf 'OpenAI Codex\\n› Ask Codex to do anything\\n'; /bin/cat"
			if mode == "exec-agent" {
				command = "printf 'OpenAI Codex\\n› Ask Codex to do anything\\n'; exec /bin/cat"
			}
			stopped := strings.HasPrefix(mode, "stopped-fifo")
			unready := stopped || mode == "banner-helper" || mode == "startup-dialog"
			stoppedPath := filepath.Join(root, "stopped.pid")
			neverStarted := filepath.Join(root, "must-not-start")
			dialogInput := filepath.Join(root, "dialog-input")
			if mode == "fifo-wrapper" {
				// The extra command keeps the wrapper shell alive while cat,
				// our accepting provider stand-in, owns the same foreground tty.
				command = "/bin/sh -c " + tmux.ShellQuote(command+"; :")
			}
			if mode == "banner-helper" {
				command = "printf 'OpenAI Codex starting\\n'; /bin/sleep 30; printf unexpected > " + tmux.ShellQuote(neverStarted)
			}
			if mode == "startup-dialog" {
				// A live foreground provider owns the tty, but its next Enter
				// would select a menu item. Record any mistakenly delivered input.
				command = "printf 'OpenAI Codex starting\\nCodex update available!\\n› 1. Update now\\n  2. Skip\\n  Press enter to continue\\n'; /bin/cat > " +
					tmux.ShellQuote(dialogInput) + "; printf unexpected > " + tmux.ShellQuote(neverStarted)
			}
			if stopped {
				command = "printf 'OpenAI Codex starting\\n'; printf '%s\\n' \"$$\" > " + tmux.ShellQuote(stoppedPath) +
					"; kill -STOP $$; printf unexpected > " + tmux.ShellQuote(neverStarted)
				t.Cleanup(func() {
					raw, err := os.ReadFile(stoppedPath)
					if err != nil {
						return
					}
					pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
					if err != nil || pid <= 0 {
						return
					}
					if state, _, err := process.GetProcessState(pid); err == nil && state == "T" {
						if child, err := os.FindProcess(pid); err == nil {
							_ = child.Kill()
						}
					}
				})
			}
			spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentCodex, Command: command}
			var environment map[string]string
			if mode != "direct-agent" && mode != "exec-agent" {
				spec.OmittedEnv = []string{"RECOVERY_FIXTURE"}
				environment = map[string]string{"RECOVERY_FIXTURE": "private-readiness-fixture"}
			}
			if err := tmux.DefaultClient.SetPaneLaunchSpecContext(t.Context(), pane.ID, spec); err != nil {
				t.Fatal(err)
			}
			opts := RestartPaneOptions{Session: session, Panes: []string{pane.ID}, Config: config.Default(), Environment: environment}
			if mode == "stopped-fifo-with-prompt" || mode == "banner-helper" || mode == "startup-dialog" {
				opts.Prompt = "UNEXPECTED_RESTART_PROMPT"
			}
			ctx := t.Context()
			if unready {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 4*time.Second)
				defer cancel()
			}
			out, err := GetRestartPaneContext(ctx, opts)
			if err != nil || out == nil {
				t.Fatalf("restart result=%+v err=%v", out, err)
			}
			key := strconv.Itoa(pane.Index)
			replacement := out.PaneShellPIDs[key].After
			if replacement <= 0 || replacement == pane.PID {
				t.Fatalf("fixture did not replace its shell: %+v", out)
			}
			if unready {
				if out.Success || out.AgentRelaunched[key] || out.PromptSent || len(out.Failed) == 0 || (stopped && out.ProcessAlive[key]) {
					t.Fatalf("incomplete launch was reported as a recovered agent: %+v", out)
				}
				if stopped {
					raw, err := os.ReadFile(stoppedPath)
					if err != nil {
						t.Fatal(err)
					}
					pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
					if err != nil {
						t.Fatal(err)
					}
					if state, _, err := process.GetProcessState(pid); err != nil || state != "T" {
						t.Fatalf("launch wrapper is not stopped: state=%s err=%v", state, err)
					}
				} else if candidate, err := observeRestartForegroundProcess(t.Context(), pane.ID, replacement); err != nil || candidate <= 0 {
					t.Fatalf("fixture foreground process stopped before readiness refusal was exercised: candidate=%d err=%v", candidate, err)
				}
				if _, err := os.Stat(neverStarted); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unready launch continued unexpectedly: %v", err)
				}
				capture, err := tmux.CapturePaneVisibleContext(t.Context(), pane.ID)
				if err != nil || !strings.Contains(capture, "OpenAI Codex starting") ||
					strings.Contains(capture, "UNEXPECTED_RESTART_PROMPT") {
					t.Fatalf("unready fixture lost banner or received prompt: capture=%q err=%v", capture, err)
				}
				if mode == "startup-dialog" {
					if !strings.Contains(capture, "› 1. Update now") || !strings.Contains(capture, "Press enter to continue") {
						t.Fatalf("fixture did not remain at its startup dialog: %q", capture)
					}
					if received, err := os.ReadFile(dialogInput); err != nil || len(received) != 0 {
						t.Fatalf("startup dialog received terminal input: bytes=%d err=%v", len(received), err)
					}
				}
				return
			}
			if !out.Success || !out.AgentRelaunched[key] || !out.ProcessAlive[key] || len(out.Failed) != 0 {
				t.Fatalf("running foreground provider was not recovered: %+v", out)
			}
			if candidate, err := observeRestartForegroundProcess(t.Context(), pane.ID, replacement); err != nil || candidate <= 0 {
				t.Fatalf("running provider disappeared: candidate=%d err=%v", candidate, err)
			} else if mode == "exec-agent" && candidate != replacement {
				t.Fatalf("exec launch was not recognized at its childless root: candidate=%d root=%d", candidate, replacement)
			}
			if _, err := observeRestartForegroundProcess(t.Context(), pane.ID, pane.PID); !errors.Is(err, tmux.ErrPaneIdentityChanged) {
				t.Fatalf("observer adopted a different pane generation: %v", err)
			}
		})
	}
}
