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
printf '%s\n' "$*" >> "$NTM_NATIVE_RECOVERY_ROOT/calls"
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) ;;
  display-message)
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
    ;;
  load-buffer)
    [ -f "$NTM_NATIVE_RECOVERY_ROOT/recorded" ] || exit 53
    cat >> "$NTM_NATIVE_RECOVERY_ROOT/calls"
    [ "$NTM_NATIVE_RECOVERY_MODE" != 'delivery-failure' ] || exit 54
    ;;
  paste-buffer|delete-buffer) ;;
  capture-pane) printf 'OpenAI Codex\nCodex>\n100%% context left\n' ;;
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
	if mode == "success" {
		// The production readiness gate also requires a live child. Give the
		// recording pane a real child, and join it even if an assertion fails.
		child := exec.CommandContext(ctx, "sleep", "30")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = child.Process.Kill()
			_ = child.Wait()
		})
	}

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
			copy := liveSpec
			return &copy, nil
		},
		PaneWorkingDir: func(context.Context, string) (string, error) { return liveDir, nil },
		SetLaunchSpec: func(_ context.Context, target string, spec tmux.AgentLaunchSpec) error {
			if target != pane.ID {
				t.Fatalf("recovery metadata retargeted from %s to %s", pane.ID, target)
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
