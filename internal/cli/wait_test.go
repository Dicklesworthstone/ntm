package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// The transport is fake; the Cobra command, pane parser, classifiers, wait
// engine, response encoding, and process-exit adapter are the real path.
func installWaitCommandTmux(t *testing.T, unreadable ...string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"panes": "%41_NTM_SEP_1_NTM_SEP_wait-project__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_1041_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n" +
			"%51_NTM_SEP_1_NTM_SEP_custom-title_NTM_SEP_codex_NTM_SEP_120_NTM_SEP_40_NTM_SEP_0_NTM_SEP_1051_NTM_SEP_1_NTM_SEP_cod_NTM_SEP__NTM_SEP__NTM_SEP_0\n",
		"capture-%41": "claude >\n",
		"capture-%51": "codex >\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, pane := range unreadable {
		if err := os.WriteFile(filepath.Join(dir, "unreadable-"+pane), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	const script = `#!/bin/sh
if [ -f "$NTM_CLI_WAIT_TEST_ROOT/block-$1" ]; then
  printf '%s\n' "$1" > "$NTM_CLI_WAIT_TEST_ROOT/blocked-stage"
  exec sleep 2
fi
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  list-sessions) printf 'wait-project_NTM_SEP_2_NTM_SEP_0_NTM_SEP_1700000000\n' ;;
  has-session)
    if [ "$3" != "=wait-project" ]; then
      printf "can't find session: %s\n" "$3" >&2
      exit 1
    fi
    ;;
  list-panes) cat "$NTM_CLI_WAIT_TEST_ROOT/panes" ;;
  capture-pane)
    pane="$3"
    printf '%s\n' "$pane" >> "$NTM_CLI_WAIT_TEST_ROOT/captured"
    if [ -f "$NTM_CLI_WAIT_TEST_ROOT/hold-$pane" ]; then
      printf '%s\n' "$1" > "$NTM_CLI_WAIT_TEST_ROOT/blocked-stage"
      while [ ! -f "$NTM_CLI_WAIT_TEST_ROOT/release" ]; do :; done
    fi
    if [ -f "$NTM_CLI_WAIT_TEST_ROOT/unreadable-$pane" ]; then
      printf "can't find pane: %s\n" "$pane" >&2
      exit 1
    fi
    cat "$NTM_CLI_WAIT_TEST_ROOT/capture-$pane"
    ;;
  *) printf 'unexpected tmux command: %s\n' "$*" >&2; exit 97 ;;
esac
`
	bin := filepath.Join(dir, "tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	t.Setenv("NTM_CLI_WAIT_TEST_ROOT", dir)
	t.Setenv("TMUX", "")
	previous := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = previous })
	return dir
}

func waitCommandHarness(t *testing.T, args ...string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	previousJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = previousJSON })
	cmd := &cobra.Command{Use: "ntm", SilenceErrors: true, SilenceUsage: true}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
	cmd.AddCommand(newWaitCmd())
	out, diagnostics := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(diagnostics)
	cmd.SetArgs(append([]string{"wait"}, args...))
	return cmd, out, diagnostics
}

func decodeWaitCommandResponse(t *testing.T, out *bytes.Buffer) robot.WaitResponse {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var response robot.WaitResponse
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("wait did not emit a JSON response: %v; output=%q", err, out.String())
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("wait emitted more than one JSON document or human text: %v; output=%q", err, out.String())
	}
	if response.Agents == nil {
		t.Fatalf("required agents array was null: %s", out.String())
	}
	return response
}

func assertWaitCommandFailure(t *testing.T, err error, written bool) {
	t.Helper()
	var processExit *robot.ProcessExitError
	if !errors.As(err, &processExit) || processExit.JSONWritten() != written || ExitCode(err) != 1 {
		t.Fatalf("wait exit = %v, want process exit1 with JSONWritten=%t", err, written)
	}
}

func startWaitCommand(t *testing.T, cmd *cobra.Command, parent context.Context) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() {
		done <- cmd.ExecuteContext(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("wait command did not stop during cleanup")
		}
	})
	return cancel, done
}

func awaitWaitCommandStage(t *testing.T, dir, stage string, done <-chan error) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		data, _ := os.ReadFile(filepath.Join(dir, "blocked-stage"))
		if strings.TrimSpace(string(data)) == stage {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("wait exited before blocking in %s: %v", stage, err)
		case <-deadline.C:
			t.Fatalf("wait never entered %s", stage)
		case <-poll.C:
		}
	}
}

func TestWaitCommandCancellationInterruptsTmuxRead(t *testing.T) {
	for _, stage := range []string{"list-sessions", "has-session", "list-panes", "capture-pane"} {
		t.Run(stage, func(t *testing.T) {
			dir := installWaitCommandTmux(t)
			if err := os.WriteFile(filepath.Join(dir, "block-"+stage), nil, 0600); err != nil {
				t.Fatal(err)
			}
			cmd, out, _ := waitCommandHarness(t, "wait-project", "--pane=%51", "--until=idle", "--timeout=1m", "--poll=30s", "--json")
			cancel, done := startWaitCommand(t, cmd, t.Context())
			awaitWaitCommandStage(t, dir, stage, done)
			cancel()
			select {
			case err := <-done:
				assertWaitCommandFailure(t, err, true)
				response := decodeWaitCommandResponse(t, out)
				if response.Success || response.ErrorCode != "CANCELED" || !errors.Is(err, context.Canceled) {
					t.Fatalf("interrupted %s became success or another failure: %+v; %v", stage, response, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("wait did not interrupt its active %s read", stage)
			}
		})
	}
}

func TestWaitCommandTimeoutInterruptsTmuxRead(t *testing.T) {
	for _, stage := range []string{"has-session", "list-panes", "capture-pane"} {
		t.Run(stage, func(t *testing.T) {
			dir := installWaitCommandTmux(t)
			if err := os.WriteFile(filepath.Join(dir, "block-"+stage), nil, 0600); err != nil {
				t.Fatal(err)
			}
			cmd, out, _ := waitCommandHarness(t, "wait-project", "--pane=%51", "--until=idle", "--timeout=150ms", "--poll=30s", "--json")
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			start := time.Now()
			err := cmd.ExecuteContext(ctx)
			elapsed := time.Since(start)
			assertWaitCommandFailure(t, err, true)
			response := decodeWaitCommandResponse(t, out)
			if response.Success || response.ErrorCode != robot.ErrCodeTimeout || elapsed > time.Second {
				t.Fatalf("active %s read postponed or changed timeout: elapsed=%s response=%+v", stage, elapsed, response)
			}
			data, readErr := os.ReadFile(filepath.Join(dir, "blocked-stage"))
			if readErr != nil || strings.TrimSpace(string(data)) != stage {
				t.Fatalf("test did not enter the intended %s read: %q; %v", stage, data, readErr)
			}
			if stage == "capture-pane" && strings.Join(response.AgentsPending, ",") != "%51" {
				t.Fatalf("first-capture timeout lost its selected physical target: %+v", response)
			}
		})
	}
}

func TestWaitCommandTimeoutRejectsLateReadyCapture(t *testing.T) {
	dir := installWaitCommandTmux(t)
	if err := os.WriteFile(filepath.Join(dir, "hold-%51"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd, out, _ := waitCommandHarness(t, "wait-project", "--pane=%51", "--until=idle", "--timeout=150ms", "--poll=30s", "--json")
	_, done := startWaitCommand(t, cmd, t.Context())
	awaitWaitCommandStage(t, dir, "capture-pane", done)

	// Without a deadline-aware capture, this transport eventually returns a
	// valid ready prompt, which used to publish success after the timeout.
	releaseDone := make(chan error, 1)
	release := time.AfterFunc(350*time.Millisecond, func() {
		releaseDone <- os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
	})
	t.Cleanup(func() {
		if release.Stop() {
			if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
				t.Error(err)
			}
		} else if err := <-releaseDone; err != nil {
			t.Error(err)
		}
	})

	select {
	case err := <-done:
		assertWaitCommandFailure(t, err, true)
		response := decodeWaitCommandResponse(t, out)
		if response.Success || response.ErrorCode != robot.ErrCodeTimeout || strings.Join(response.AgentsPending, ",") != "%51" {
			t.Fatalf("late ready capture bypassed the deadline or lost pending target: %+v; %v", response, err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not interrupt the delayed ready capture")
	}
}

func TestWaitCommandKeepsUnreadableTargetsPending(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   []string
		success bool
	}{
		{name: "all"},
		{name: "explicit all", flags: []string{"--pane=-1"}},
		{name: "any", flags: []string{"--any"}, success: true},
		{name: "count needs two", flags: []string{"--any", "--count=2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installWaitCommandTmux(t, "%41")
			args := []string{"wait-project", "--until=idle", "--timeout=500ms", "--poll=30s", "--json"}
			cmd, out, diagnostics := waitCommandHarness(t, append(args, tc.flags...)...)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			err := cmd.ExecuteContext(ctx)
			response := decodeWaitCommandResponse(t, out)
			if response.Success != tc.success || response.Session != "wait-project" || response.Condition != "idle" {
				t.Fatalf("unexpected wait outcome: %+v; error=%v", response, err)
			}
			if tc.success {
				if err != nil || len(response.Agents) != 1 || response.Agents[0].Pane != "%51" || response.Agents[0].State != "WAITING" {
					t.Fatalf("ANY did not accept the one observed matching agent: %+v; error=%v", response, err)
				}
			} else {
				assertWaitCommandFailure(t, err, true)
				if response.ErrorCode != robot.ErrCodeTimeout || strings.Join(response.AgentsPending, ",") != "%41" {
					t.Fatalf("unreadable target was dropped from the ALL/count set: %+v", response)
				}
			}
			if diagnostics.Len() != 0 {
				t.Fatalf("JSON wait leaked diagnostics: %q", diagnostics.String())
			}
		})
	}
}

func TestWaitCommandTimeoutInterruptsLongPoll(t *testing.T) {
	installWaitCommandTmux(t, "%41")
	cmd, out, _ := waitCommandHarness(t, "wait-project", "--until=idle", "--timeout=150ms", "--poll=30s", "--json")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := cmd.ExecuteContext(ctx)
	elapsed := time.Since(start)
	assertWaitCommandFailure(t, err, true)
	response := decodeWaitCommandResponse(t, out)
	if response.ErrorCode != robot.ErrCodeTimeout || elapsed > time.Second {
		t.Fatalf("poll interval postponed timeout: elapsed=%s response=%+v", elapsed, response)
	}
	if strings.Join(response.AgentsPending, ",") != "%41" {
		t.Fatalf("timeout lost unreadable target evidence: %+v", response)
	}
}

func TestWaitCommandCancellationInterruptsLongPoll(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		name := "human"
		if jsonMode {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			dir := installWaitCommandTmux(t)
			args := []string{"wait-project", "--until=generating", "--timeout=1m", "--poll=30s"}
			if jsonMode {
				args = append(args, "--json")
			}
			cmd, out, _ := waitCommandHarness(t, args...)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- cmd.ExecuteContext(ctx)
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("wait command did not stop during cleanup")
				}
			})
			deadline := time.After(2 * time.Second)
			poll := time.NewTicker(5 * time.Millisecond)
			defer poll.Stop()
		observing:
			for {
				select {
				case err := <-done:
					t.Fatalf("wait exited before observing both pending targets: %v", err)
				case <-deadline:
					t.Fatal("wait never observed both targets")
				case <-poll.C:
					data, _ := os.ReadFile(filepath.Join(dir, "captured"))
					if strings.Contains(string(data), "%41\n") && strings.Contains(string(data), "%51\n") {
						break observing
					}
				}
			}
			start := time.Now()
			cancel()
			select {
			case err := <-done:
				assertWaitCommandFailure(t, err, jsonMode)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("wait lost cancellation cause: %v", err)
				}
				if jsonMode {
					response := decodeWaitCommandResponse(t, out)
					if response.ErrorCode != "CANCELED" {
						t.Fatalf("wait changed cancellation into another failure: %+v", response)
					}
				} else if !strings.Contains(out.String(), "context canceled") {
					t.Fatalf("human wait omitted cancellation outcome: %q", out.String())
				}
			case <-time.After(time.Second):
				t.Fatalf("wait ignored cancellation during its long poll (%s)", time.Since(start))
			}
		})
	}
}

func TestWaitCommandCanonicalSelectorsDoNotRetargetAcrossWindows(t *testing.T) {
	for _, selector := range []string{"%51", "1.1", "1"} {
		t.Run(selector, func(t *testing.T) {
			dir := installWaitCommandTmux(t, "%41")
			cmd, out, _ := waitCommandHarness(t, "wait-project", "--pane="+selector, "--until=idle", "--timeout=1s", "--json")
			if err := cmd.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("canonical selector %q failed: %v; %s", selector, err, out.String())
			}
			response := decodeWaitCommandResponse(t, out)
			if !response.Success || len(response.Agents) != 1 || response.Agents[0].Pane != "%51" {
				t.Fatalf("canonical selector %q matched the wrong physical pane: %+v", selector, response)
			}
			data, err := os.ReadFile(filepath.Join(dir, "captured"))
			if err != nil || string(data) != "%51\n" {
				t.Fatalf("canonical selector captured an unselected pane: %q; %v", data, err)
			}
		})
	}

	t.Run("legacy single-window index", func(t *testing.T) {
		dir := installWaitCommandTmux(t)
		panes := "%41_NTM_SEP_1_NTM_SEP_wait-project__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_1041_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n"
		if err := os.WriteFile(filepath.Join(dir, "panes"), []byte(panes), 0600); err != nil {
			t.Fatal(err)
		}
		cmd, out, _ := waitCommandHarness(t, "wait-project", "--pane=1", "--until=idle", "--timeout=1s", "--json")
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatalf("legacy --pane=1 failed: %v; %s", err, out.String())
		}
		response := decodeWaitCommandResponse(t, out)
		if len(response.Agents) != 1 || response.Agents[0].Pane != "%41" {
			t.Fatalf("single-window pane index changed meaning: %+v", response)
		}
	})
}

func TestWaitCommandCountAndAgentTypeAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "two distinct agents", args: []string{"--any", "--count=2"}, want: "%41,%51"},
		{name: "claude alias", args: []string{"--type=cc"}, want: "%41"},
		{name: "codex alias and parsed type", args: []string{"--type=openai-codex"}, want: "%51"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installWaitCommandTmux(t)
			args := []string{"wait-project", "--until=idle,healthy", "--timeout=1s", "--json"}
			cmd, out, _ := waitCommandHarness(t, append(args, tc.args...)...)
			if err := cmd.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("wait with %v failed: %v; %s", tc.args, err, out.String())
			}
			response := decodeWaitCommandResponse(t, out)
			var panes []string
			for _, agent := range response.Agents {
				panes = append(panes, agent.Pane)
			}
			if !response.Success || strings.Join(panes, ",") != tc.want {
				t.Fatalf("wait with %v matched the wrong targets: %+v", tc.args, response)
			}
		})
	}
}

func TestWaitCommandJSONPreservesFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session string
		flags   []string
		code    string
	}{
		{name: "invalid condition", flags: []string{"--until=nonsense"}, code: robot.ErrCodeInvalidFlag},
		{name: "invalid selector", flags: []string{"--pane=one.two"}, code: robot.ErrCodeInvalidFlag},
		{name: "missing pane", flags: []string{"--pane=%999"}, code: robot.ErrCodePaneNotFound},
		{name: "missing session", session: "missing", code: robot.ErrCodeSessionNotFound},
		{name: "invalid type", flags: []string{"--type=nonsense"}, code: robot.ErrCodeInvalidFlag},
		{name: "zero count", flags: []string{"--any", "--count=0"}, code: robot.ErrCodeInvalidFlag},
		{name: "zero poll", flags: []string{"--poll=0"}, code: robot.ErrCodeInvalidFlag},
		{name: "negative timeout", flags: []string{"--timeout=-1s"}, code: robot.ErrCodeInvalidFlag},
		{name: "agent error", flags: []string{"--exit-on-error"}, code: "AGENT_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := installWaitCommandTmux(t)
			if tc.code == "AGENT_ERROR" {
				if err := os.WriteFile(filepath.Join(dir, "capture-%41"), []byte("Rate limit exceeded\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			session := tc.session
			if session == "" {
				session = "wait-project"
			}
			args := []string{session, "--timeout=1s", "--json"}
			cmd, out, diagnostics := waitCommandHarness(t, append(args, tc.flags...)...)
			err := cmd.ExecuteContext(t.Context())
			assertWaitCommandFailure(t, err, true)
			response := decodeWaitCommandResponse(t, out)
			if response.Success || response.ErrorCode != tc.code || response.Error == "" {
				t.Fatalf("failure was flattened or lost: %+v; error=%v", response, err)
			}
			if diagnostics.Len() != 0 {
				t.Fatalf("failure leaked a second diagnostic: %q", diagnostics.String())
			}
			if tc.code == "AGENT_ERROR" && (len(response.Agents) != 1 || response.Agents[0].Pane != "%41" || response.Agents[0].State != "ERROR") {
				t.Fatalf("agent failure lost its originating pane: %+v", response)
			}
		})
	}
}

func TestWaitCommandCanceledBeforeSessionLookup(t *testing.T) {
	dir := installWaitCommandTmux(t)
	cmd, out, _ := waitCommandHarness(t, "wait-project", "--json")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := cmd.ExecuteContext(ctx)
	assertWaitCommandFailure(t, err, true)
	response := decodeWaitCommandResponse(t, out)
	if response.ErrorCode != "CANCELED" || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled wait did not preserve cancellation: %+v %v", response, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "captured")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-canceled wait still captured pane output: %v", err)
	}
}

func TestWaitCommandHumanOutputShowsResultAndPhysicalPane(t *testing.T) {
	installWaitCommandTmux(t, "%41")
	cmd, out, diagnostics := waitCommandHarness(t, "wait-project", "--pane=1.1", "--until=idle", "--timeout=1s")
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("human wait failed: %v; %q", err, out.String())
	}
	for _, fragment := range []string{"Waiting for 'wait-project' until idle", "Condition 'idle' met", "%51: WAITING"} {
		if !strings.Contains(out.String(), fragment) {
			t.Fatalf("human wait omitted %q: %q", fragment, out.String())
		}
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("explicit session produced unexpected diagnostics: %q", diagnostics.String())
	}
}

type waitCommandFailingWriter struct{ err error }

func (w waitCommandFailingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWaitCommandJSONEncodingFailurePreservesWriterError(t *testing.T) {
	installWaitCommandTmux(t)
	cmd, _, _ := waitCommandHarness(t, "wait-project", "--pane=%51", "--until=idle", "--timeout=1s", "--json")
	writeErr := errors.New("wait output writer failed")
	cmd.SetOut(waitCommandFailingWriter{err: writeErr})
	err := cmd.ExecuteContext(t.Context())
	assertWaitCommandFailure(t, err, false)
	if !errors.Is(err, writeErr) {
		t.Fatalf("wait lost the actual encoding failure: %v", err)
	}
}
