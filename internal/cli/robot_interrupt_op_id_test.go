package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestRobotInterruptOpIDRetryAcrossProcessesReplays drives the user surface
// end to end: every invocation is a separate `ntm` process (the
// TestRobotProcessContractHelper re-exec) sharing one isolated state DB, the
// way an orchestrator retries after a timeout.
//
//   - `--robot-interrupt=S --op-id=ID --msg=TASK` interrupts once and delivers
//     the task once;
//   - the identical retry from a NEW process replays the recorded outcome
//     with no second Ctrl+C and no second delivery;
//   - --robot-send-receipt=ID reports the interrupt outcome;
//   - reusing ID with a different task fails IDEMPOTENCY_CONFLICT untouched;
//   - --robot-send-receipt combined with --robot-interrupt is refused rather
//     than silently skipping the interrupt.
//
// Ground truth is testutil.InterruptFixture's log of received SIGINTs and
// submitted lines.
func TestRobotInterruptOpIDRetryAcrossProcessesReplays(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	run := newRobotProcessRunner(t)
	fx := testutil.StartInterruptFixture(t, "cliopid")
	marker := fmt.Sprintf("ntm-cli-opid-%d", time.Now().UnixNano())
	opID := "op-cli-" + marker

	interrupt := func(task string) []string {
		return []string{
			"--robot-interrupt=" + fx.Session, "--panes=" + fx.PaneID,
			"--force", "--no-wait", "--msg=" + task, "--op-id=" + opID,
		}
	}
	operationOf := func(payload map[string]any) map[string]any {
		op, _ := payload["operation"].(map[string]any)
		return op
	}

	first, code := run(interrupt("new task " + marker)...)
	if code != 0 || first["success"] != true {
		t.Fatalf("first interrupt exit=%d payload=%v, want success", code, first)
	}
	if op := operationOf(first); op == nil || op["kind"] != "interrupt" || op["status"] != "completed" || op["replayed"] == true {
		t.Fatalf("first operation = %v, want a fresh completed interrupt operation", op)
	}
	fx.WaitForEvents(t, marker, 1, 1)

	retry, code := run(interrupt("new task " + marker)...)
	fx.AssertQuiet(t, marker, 1, 1)
	if code != 0 || retry["success"] != true {
		t.Fatalf("retry exit=%d payload=%v, want replayed success", code, retry)
	}
	if op := operationOf(retry); op == nil || op["replayed"] != true || op["operation_id"] != opID {
		t.Fatalf("retry operation = %v, want the replayed %s", op, opID)
	}
	if retry["interrupted_at"] != first["interrupted_at"] {
		t.Fatalf("retry interrupted_at = %v, want the recorded %v", retry["interrupted_at"], first["interrupted_at"])
	}

	receipt, code := run("--robot-send-receipt=" + opID)
	if code != 0 || receipt["success"] != true || receipt["session"] != fx.Session {
		t.Fatalf("receipt exit=%d payload=%v, want the %s receipt", code, receipt, fx.Session)
	}
	if op := operationOf(receipt); op == nil || op["kind"] != "interrupt" || op["status"] != "completed" {
		t.Fatalf("receipt operation = %v, want completed interrupt", op)
	}
	outcome, _ := receipt["interrupt_outcome"].(map[string]any)
	if outcome == nil || outcome["success"] != true || outcome["message_sent"] != true {
		t.Fatalf("receipt interrupt_outcome = %v, want the recorded successful interrupt with the task delivered", receipt["interrupt_outcome"])
	}

	statusDoc, code := run("--robot-action-status=" + opID)
	if code != 0 || statusDoc["success"] != true || statusDoc["request_id"] != opID || statusDoc["terminal"] != true || statusDoc["status"] != "completed" {
		t.Fatalf("action-status exit=%d payload=%v, want terminal completed status for %s", code, statusDoc, opID)
	}

	waitDoc, code := run("--robot-wait-request", "--request-id="+opID, "--timeout=2s", "--poll=25ms")
	if code != 0 || waitDoc["success"] != true || waitDoc["request_id"] != opID || waitDoc["terminal"] != true {
		t.Fatalf("wait-request exit=%d payload=%v, want immediate terminal completion for %s", code, waitDoc, opID)
	}

	cancelDoc, code := run("--robot-cancel-request=" + opID)
	if code != 1 || cancelDoc["error_code"] != "REQUEST_NOT_CANCELLABLE" {
		t.Fatalf("cancel-request on completed op exit=%d payload=%v, want REQUEST_NOT_CANCELLABLE", code, cancelDoc)
	}

	conflict, code := run(interrupt("different task " + marker)...)
	if code != 1 || conflict["error_code"] != robot.ErrCodeIdempotencyConflict {
		t.Fatalf("conflicting reuse exit=%d payload=%v, want IDEMPOTENCY_CONFLICT", code, conflict)
	}

	combined, code := run("--robot-send-receipt="+opID, "--robot-interrupt="+fx.Session, "--force")
	if code != 1 || combined["error_code"] != robot.ErrCodeInvalidFlag ||
		!strings.Contains(fmt.Sprint(combined["error"]), "--robot-interrupt") {
		t.Fatalf("receipt+interrupt exit=%d payload=%v, want INVALID_FLAG naming --robot-interrupt", code, combined)
	}
	fx.AssertQuiet(t, marker, 1, 1)
}

// TestRobotInterruptTypeFilterInterruptsOnlyThatAgentType drives
// `--robot-interrupt=S --type=cod` against a session holding a claude pane
// and a codex pane. --type used to be accepted and ignored, so every agent
// in the session was interrupted and re-tasked.
func TestRobotInterruptTypeFilterInterruptsOnlyThatAgentType(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	run := newRobotProcessRunner(t)
	claude := testutil.StartInterruptFixture(t, "clitype")
	codex := claude.SplitPane(t)
	claude.SetTitle(t, claude.Session+"__cc_1")
	codex.SetTitle(t, claude.Session+"__cod_1")
	marker := fmt.Sprintf("ntm-cli-type-%d", time.Now().UnixNano())

	// No follow-up task: the recording panes are titled as agents but have
	// no agent composer, so delivery readiness would refuse the paste.
	payload, code := run("--robot-interrupt="+claude.Session, "--type=cod", "--force", "--no-wait")
	if code != 0 || payload["success"] != true {
		t.Fatalf("typed interrupt exit=%d payload=%v, want success", code, payload)
	}
	interrupted, _ := payload["interrupted"].([]any)
	if len(interrupted) != 1 {
		t.Fatalf("interrupted = %v, want exactly the codex pane", payload["interrupted"])
	}
	codex.WaitForEvents(t, marker, 1, 0)
	claude.AssertQuiet(t, marker, 0, 0)
}

// Exercise the real Cobra, kernel and robot paths with only the tmux transport
// controlled. The recorded key destinations prove Pi is actually interrupted,
// user panes are untouched, and a failed sibling cannot end the sweep early.
func TestPiInterruptCLIAndRobotRespectTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux transport fixture requires /bin/sh")
	}
	const script = `#!/bin/sh
case "$1" in
  -V) printf 'tmux 3.5a\n' ;;
  has-session) ;;
  list-sessions) printf 'pi-control_NTM_SEP_1_NTM_SEP_0_NTM_SEP_1700000000\n' ;;
  list-panes)
    activity=''
    case "$*" in *'#{window_activity}'*) activity='1700000000_NTM_SEP_' ;; esac
    printf '%%10_NTM_SEP_0_NTM_SEP_pi-control__user_1[focus]_NTM_SEP_bash_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_%s1040_NTM_SEP_0_NTM_SEP_user_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]\n' "$activity"
    printf '%%11_NTM_SEP_1_NTM_SEP_pi-control__cc_1[other]_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_%s1041_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]\n' "$activity"
    printf '%%12_NTM_SEP_2_NTM_SEP_pi-control__pi_1[focus]_NTM_SEP_pi_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_%s1042_NTM_SEP_0_NTM_SEP_pi-agent_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]\n' "$activity"
    ;;
  capture-pane) printf 'Working\n' ;;
  display-message)
    case "$*" in
      *'@ntm_rotation_token'*) printf '[]\n' ;;
      *'window_activity'*) printf '1700000000\n' ;;
      *) printf '%s\n' "$NTM_PI_INTERRUPT_ROOT" ;;
    esac
    ;;
  send-keys)
    printf '%s\n' "$*" >> "$NTM_PI_INTERRUPT_ROOT/keys"
    if [ "$3" = "$NTM_PI_INTERRUPT_FAIL" ]; then
      printf "can't find pane: %s\n" "$3" >&2
      exit 1
    fi
    ;;
  show-options|show-environment) ;;
  *) printf 'unexpected tmux command: %s\n' "$*" >&2; exit 97 ;;
esac
`
	for _, tc := range []struct {
		name       string
		args       []string
		json       bool
		robot      bool
		failTarget string
		wantKeys   string
	}{
		{name: "human mixed batch", args: []string{"interrupt", "pi-control"}, wantKeys: "send-keys -t %11 C-c\nsend-keys -t %12 C-c\n"},
		{name: "human tagged Pi", args: []string{"interrupt", "pi-control", "--tag=focus"}, wantKeys: "send-keys -t %12 C-c\n"},
		{name: "kernel mixed batch", args: []string{"interrupt", "pi-control", "--json"}, json: true, wantKeys: "send-keys -t %11 C-c\nsend-keys -t %12 C-c\n"},
		{name: "kernel tagged Pi", args: []string{"interrupt", "pi-control", "--json", "--tag=focus"}, json: true, wantKeys: "send-keys -t %12 C-c\n"},
		{name: "kernel sibling fails", args: []string{"interrupt", "pi-control", "--json"}, json: true, failTarget: "%11", wantKeys: "send-keys -t %11 C-c\nsend-keys -t %12 C-c\n"},
		{name: "robot Pi", args: []string{"--robot-interrupt=pi-control", "--type=pi", "--force", "--no-wait"}, json: true, robot: true, wantKeys: "send-keys -t %12 C-c\n"},
		{name: "robot Pi alias", args: []string{"--robot-interrupt=pi-control", "--type=pi-agent", "--force", "--no-wait"}, json: true, robot: true, wantKeys: "send-keys -t %12 C-c\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "tmux")
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			args, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
			cmd.Dir = root
			cmd.Env = envWithOverrides(os.Environ(),
				"HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"),
				"NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+binary, "NTM_PI_INTERRUPT_ROOT="+root, "NTM_PI_INTERRUPT_FAIL="+tc.failTarget,
				"NTM_NO_COLOR=1", "NTM_CONFIG=", "NTM_ROBOT_FORMAT=", "NTM_OUTPUT_FORMAT=", "TOON_DEFAULT_FORMAT=", "NTM_ROBOT_VERBOSITY=",
				"NTM_ROBOT_CONTRACT_ARGS="+string(args))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			runErr := cmd.Run()
			if (runErr == nil) != (tc.failTarget == "") {
				t.Fatalf("interrupt error=%v stdout=%s stderr=%s", runErr, stdout.String(), stderr.String())
			}
			if runErr != nil {
				var exitErr *exec.ExitError
				if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
					t.Fatalf("partial interrupt exit=%v, want exit code 1", runErr)
				}
			}
			keys, err := os.ReadFile(filepath.Join(root, "keys"))
			if err != nil || string(keys) != tc.wantKeys {
				t.Fatalf("actual interrupt destinations=%q want=%q err=%v stdout=%s stderr=%s", keys, tc.wantKeys, err, stdout.String(), stderr.String())
			}
			wantCount := strings.Count(tc.wantKeys, "\n")
			if tc.robot {
				var response robot.InterruptOutput
				if err := json.Unmarshal(stdout.Bytes(), &response); err != nil || !response.Success || len(response.Interrupted) != 1 || response.Interrupted[0] != "2" {
					t.Fatalf("robot Pi interrupt response=%+v err=%v stdout=%s", response, err, stdout.String())
				}
			} else if tc.json {
				var response output.InterruptResponse
				if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
					t.Fatalf("decode kernel interrupt: %v stdout=%s", err, stdout.String())
				}
				wantFailed := 0
				if tc.failTarget != "" {
					wantFailed = 1
				}
				if response.Success != (wantFailed == 0) || response.Interrupted != wantCount-wantFailed || response.Failed != wantFailed || len(response.Panes) != wantCount {
					t.Fatalf("kernel interrupt response=%+v", response)
				}
				pi := response.Panes[len(response.Panes)-1]
				if pi.PaneID != "%12" || pi.AgentType != "pi" || pi.Status != "interrupted" {
					t.Fatalf("Pi receipt=%+v, want successful canonical Pi interrupt", pi)
				}
			} else if !strings.Contains(stdout.String(), fmt.Sprintf("Sent Ctrl+C to %d agent pane(s)", wantCount)) {
				t.Fatalf("human interrupt count not truthful: %s", stdout.String())
			}
		})
	}
}

// newRobotProcessRunner returns a runner that executes each robot
// invocation as a separate `ntm` process (the TestRobotProcessContractHelper
// re-exec) sharing one isolated home and state DB, on this process's
// isolated tmux server.
func newRobotProcessRunner(t *testing.T) func(args ...string) (map[string]any, int) {
	t.Helper()
	return newRobotProcessRunnerWithEnv(t)
}

func newRobotProcessRunnerWithEnv(t *testing.T, extraEnv ...string) func(args ...string) (map[string]any, int) {
	t.Helper()
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	configHome := filepath.Join(tmpDir, "xdg")
	for _, dir := range []string{homeDir, configHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create isolated directory: %v", err)
		}
	}
	return func(args ...string) (map[string]any, int) {
		t.Helper()
		rawArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("encode helper args: %v", err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
		cmd.Dir = tmpDir
		overrides := []string{
			"HOME=" + homeDir,
			"XDG_CONFIG_HOME=" + configHome,
			"NTM_NO_COLOR=1",
			"NTM_CONFIG=",
			"NTM_ROBOT_FORMAT=",
			"NTM_OUTPUT_FORMAT=",
			"TOON_DEFAULT_FORMAT=",
			"NTM_ROBOT_VERBOSITY=",
			// Keep the child on this process's isolated tmux server, where
			// the fixture session lives.
			"NTM_TEST_TMUX_ENV_OWNED=1",
			"NTM_ROBOT_CONTRACT_ARGS=" + string(rawArgs),
		}
		overrides = append(overrides, extraEnv...)
		cmd.Env = envWithOverrides(os.Environ(), overrides...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		exitCode := 0
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("ntm %v failed without an exit status: %v", args, err)
			}
			exitCode = exitErr.ExitCode()
		}
		var payload map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
			t.Fatalf("ntm %v stdout is not one JSON document: %v\nstdout=%q\nstderr=%q", args, err, stdout.String(), stderr.String())
		}
		return payload, exitCode
	}
}

// TestRobotCanonicalFlagsReachTheirCommands drives the canonical flags the
// deprecation hints point at (--category, --session, --all, --brief) through
// separate `ntm` processes. Each was accepted and silently ignored by its
// command before, so a caller following the hint got unfiltered (or, for
// --robot-dismiss-alert --all --session, every-session) behavior.
func TestRobotCanonicalFlagsReachTheirCommands(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	run := newRobotProcessRunner(t)

	all, code := run("--robot-palette")
	allCommands, _ := all["commands"].([]any)
	if code != 0 || len(allCommands) == 0 {
		t.Fatalf("--robot-palette exit=%d commands=%d, want the default palette", code, len(allCommands))
	}
	filtered, code := run("--robot-palette", "--category=no-such-category")
	if cmds, _ := filtered["commands"].([]any); code != 0 || len(cmds) != 0 {
		t.Fatalf("--robot-palette --category=no-such-category exit=%d commands=%d, want none", code, len(cmds))
	}

	// --session scopes the dismissal (ignored, it made --all dismiss alerts
	// in every session) and --all selects dismiss-all (ignored, an ID-less
	// call failed with "alert ID required").
	dismissed, code := run("--robot-dismiss-alert", "--all", "--session=ntm-no-such-session")
	if code != 0 || dismissed["session"] != "ntm-no-such-session" || dismissed["dismissed_count"] != float64(0) {
		t.Fatalf("--robot-dismiss-alert --all --session=S exit=%d payload=%v, want a dismiss-all scoped to S", code, dismissed)
	}

	fx := testutil.StartInterruptFixture(t, "clibrief")
	brief, code := run("--robot-diagnose="+fx.Session, "--brief", "--fix")
	if code != 1 || brief["error_code"] != robot.ErrCodeInvalidFlag {
		t.Fatalf("--robot-diagnose --brief --fix exit=%d payload=%v, want INVALID_FLAG", code, brief)
	}
}
