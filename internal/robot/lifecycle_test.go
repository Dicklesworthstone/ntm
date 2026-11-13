package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestRemoteProcessOperationsRefuseBeforeObservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording command fixture requires a POSIX shell")
	}
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	const script = `#!/bin/sh
printf '%s\n' "$0 $*" >> "$NTM_REMOTE_PROCESS_CONTROL_CALLS"
exit 79
`
	for _, name := range []string{"tmux", "ssh"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NTM_REMOTE_PROCESS_CONTROL_CALLS", calls)
	t.Setenv("NTM_TMUX_BINARY", filepath.Join(root, "tmux"))
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	original := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("operator@remote-fixture")
	t.Cleanup(func() { tmux.DefaultClient = original })
	assertUnobserved := func(t *testing.T) {
		t.Helper()
		if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remote process-control refusal crossed a tmux or SSH observation boundary: %v", err)
		}
	}

	for _, relaunch := range []bool{false, true} {
		exit, err := GetExitCLI(t.Context(), LifecycleOptions{Session: "remote-fixture", Relaunch: relaunch})
		if err != nil || exit == nil || exit.Success || exit.ErrorCode != ErrCodeInvalidFlag ||
			len(exit.Results) != 0 || !strings.Contains(exit.Error, "without --ssh") {
			t.Fatalf("exit-cli relaunch=%t response=%+v err=%v", relaunch, exit, err)
		}
		assertUnobserved(t)
		out, err := GetKillAgent(t.Context(), LifecycleOptions{Session: "remote-fixture", Relaunch: relaunch})
		if err != nil || out == nil || out.Success || out.ErrorCode != ErrCodeInvalidFlag ||
			len(out.Results) != 0 || !strings.Contains(out.Error, "without --ssh") {
			t.Fatalf("kill-agent relaunch=%t response=%+v err=%v", relaunch, out, err)
		}
		assertUnobserved(t)
	}

	seq := &RestartSequence{}
	result, err := hardKillAgent(t.Context(), "remote-fixture", 0, 1, seq)
	if !errors.Is(err, errRemoteAgentProcessOperations) || result == nil || result.Success ||
		result.ShellPID != 0 || result.ChildPID != 0 || seq.ExitMethod != "" {
		t.Fatalf("direct hard-kill bypassed host admission: result=%+v sequence=%+v err=%v", result, seq, err)
	}
	assertUnobserved(t)

	for _, mode := range []string{"soft", "fallback", "hard-only"} {
		for _, dryRun := range []bool{false, true} {
			out, err := GetSmartRestart(t.Context(), SmartRestartOptions{
				Session: "remote-fixture", HardKill: mode == "fallback", HardKillOnly: mode == "hard-only", DryRun: dryRun,
			})
			if err != nil || out == nil || out.Success || out.ErrorCode != ErrCodeInvalidFlag ||
				len(out.Actions) != 0 || out.DryRun != dryRun || !strings.Contains(out.Error, "without --ssh") {
				t.Fatalf("smart restart mode=%s dryRun=%t response=%+v err=%v", mode, dryRun, out, err)
			}
			assertUnobserved(t)
		}
	}
}

// The double-Ctrl+C exit contract (ntm-3lbb): Claude Code treats the pair as
// "exit" only when the second tap lands roughly 0.1-0.3s after the first —
// slower reads as two separate interrupts, faster risks coalescing. Operator
// docs used to ship three conflicting timings; this pin makes the code the
// single source of truth.
func TestLifecycleDoubleTapGap_WithinExitWindow(t *testing.T) {
	if lifecycleDoubleTapGap < 100*time.Millisecond || lifecycleDoubleTapGap > 300*time.Millisecond {
		t.Fatalf("lifecycleDoubleTapGap %v outside the empirical 100-300ms double-Ctrl+C exit window", lifecycleDoubleTapGap)
	}
}

func TestJoinLifecycleDetail(t *testing.T) {
	if got := joinLifecycleDetail("", "a"); got != "a" {
		t.Fatalf("empty existing: got %q", got)
	}
	if got := joinLifecycleDetail("a", "b"); got != "a; b" {
		t.Fatalf("joined: got %q", got)
	}
}

func TestKillProcessesGracefully_TermsCooperativeProcess(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	snapshot, err := process.SnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original, ok := snapshot[pid]
	if !ok {
		t.Fatalf("owned sleep process %d absent from initial snapshot", pid)
	}
	killed, err := killProcessesGracefully(t.Context(), map[int]process.SnapshotProcess{pid: original}, nil)
	if err != nil {
		t.Fatalf("kill owned cooperative process: %v", err)
	}
	// Reap the zombie so IsAlive-based assertions see the true state.
	_, _ = cmd.Process.Wait()

	if len(killed) != 1 || killed[0] != pid {
		t.Fatalf("expected killed=[%d], got %v", pid, killed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for process.IsAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if process.IsAlive(pid) {
		t.Fatalf("pid %d still alive after graceful kill", pid)
	}
}

func TestObserveLifecycleProcessesTracksOriginalLifetimes(t *testing.T) {
	first := process.SnapshotProcess{PID: 42, PPID: 1, State: "S", Started: "Thu Nov 12 10:00:00 2026"}
	second := process.SnapshotProcess{PID: 43, PPID: 42, State: "S", Started: "Thu Nov 12 10:00:01 2026"}
	original := map[int]process.SnapshotProcess{first.PID: first, second.PID: second}
	reused := first
	reused.Started = "Thu Nov 12 11:00:00 2026"
	zombie := first
	zombie.State = "Z"
	for _, tc := range []struct {
		name     string
		snapshot map[int]process.SnapshotProcess
		want     string
	}{
		{"captured process lives", map[int]process.SnapshotProcess{42: first}, "running"},
		{"captured descendant survives", map[int]process.SnapshotProcess{43: second}, "running"},
		{"complete absence", map[int]process.SnapshotProcess{}, "exited"},
		{"terminated zombie", map[int]process.SnapshotProcess{42: zombie}, "exited"},
		{"reused PID belongs to another process", map[int]process.SnapshotProcess{42: reused}, "exited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := observeLifecycleProcesses(tc.snapshot, original); got != tc.want {
				t.Fatalf("original process state = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKillProcessesGracefullyRefusesWithoutFreshEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owned process and command fixture requires POSIX")
	}
	for _, mode := range []string{"cancelled", "process-failure", "pane-guard-failure"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command("sleep", "60")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			snapshot, err := process.SnapshotContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			pid := cmd.Process.Pid
			original, ok := snapshot[pid]
			if !ok {
				t.Fatalf("owned process %d missing before refusal test", pid)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var guard func(context.Context) error
			path := os.Getenv("PATH")
			switch mode {
			case "cancelled":
				cancel()
			case "process-failure":
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, "ps"), []byte("#!/bin/sh\nexit 78\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", root+string(os.PathListSeparator)+path)
			case "pane-guard-failure":
				guard = func(context.Context) error { return errors.New("original pane replaced") }
			}
			killed, err := killProcessesGracefully(ctx, map[int]process.SnapshotProcess{pid: original}, guard)
			t.Setenv("PATH", path)
			if err == nil || len(killed) != 0 || !process.IsAlive(pid) {
				t.Fatalf("unverified kill signaled its target: killed=%v err=%v alive=%t", killed, err, process.IsAlive(pid))
			}
		})
	}
}

func TestLifecycleVerbs_RequireSession(t *testing.T) {
	out, err := GetExitCLI(context.Background(), LifecycleOptions{})
	if err != nil {
		t.Fatalf("GetExitCLI: %v", err)
	}
	if out.Success || out.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for empty session, got success=%v code=%s", out.Success, out.ErrorCode)
	}
	kout, err := GetKillAgent(context.Background(), LifecycleOptions{})
	if err != nil {
		t.Fatalf("GetKillAgent: %v", err)
	}
	if kout.Success || kout.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for empty session, got success=%v code=%s", kout.Success, kout.ErrorCode)
	}
}

// bd-3izr9: refreshLifecyclePane must distinguish three outcomes, because
// collapsing them made a single transient tmux error report shell_preserved:
// false — telling the operator the verb had destroyed the pane, which is the
// one thing both lifecycle verbs promise never to do.
func TestRefreshLifecyclePane_ClassifiesOutcomes(t *testing.T) {
	t.Run("a session that does not exist proves the pane is absent", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("recording command fixture requires a POSIX shell")
		}
		path := filepath.Join(t.TempDir(), "tmux")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'session not found: fixture\\n' >&2\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("NTM_TMUX_BINARY", path)
		// Killing a session's last pane destroys the session, so the listing
		// legitimately errors. That is a definitive answer, not a transient
		// failure; treating it as one reported a successful kill as a failure.
		_, lookup := refreshLifecyclePane(context.Background(), "ntm-nonexistent-session-bd3izr9", "%999")
		if lookup != paneAbsent {
			t.Fatalf("lookup = %v, want paneAbsent for a session that does not exist", lookup)
		}
	})

	t.Run("a cancelled context does not claim the pane is gone", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, lookup := refreshLifecyclePane(ctx, "any-session", "%1")
		if lookup == paneAbsent {
			t.Fatal("a cancelled lookup reported paneAbsent; cancellation proves nothing about the pane")
		}
	})
}

// The tri-state must be wired into the result, not just computed.
func TestLifecyclePaneResult_VerificationFailedIsDistinctFromDestroyed(t *testing.T) {
	// These dispositions all fail the preservation contract, but callers must
	// be able to decide whether retrying or escalating can still affect a process.
	for _, tc := range []struct {
		name, pane, process string
		exited, unverified  bool
	}{
		{"destroyed after exit", "missing", "exited", true, false},
		{"missing with surviving process", "missing", "running", false, false},
		{"observation unavailable", "unknown", "unknown", false, true},
		{"agent still active", "present", "running", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := LifecyclePaneResult{PaneState: tc.pane, ProcessState: tc.process, Exited: tc.exited, VerificationFailed: tc.unverified}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["pane_state"] != tc.pane || decoded["process_state"] != tc.process || decoded["exited"] != tc.exited || decoded["shell_preserved"] != false {
				t.Fatalf("structured lifecycle evidence lost in JSON: %s", data)
			}
			if tc.unverified && decoded["verification_failed"] != true {
				t.Fatalf("unknown evidence lost its verification failure: %s", data)
			}
		})
	}
}

func TestLifecycleShellAdmissionAndExitEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lifecycle transport fixture requires POSIX processes")
	}
	type testCase struct {
		mode     string
		verb     string
		relaunch bool
	}
	var cases []testCase
	for _, verb := range []string{"exit", "kill"} {
		for _, mode := range []string{"direct", "command", "unreadable", "missing-pid", "dead", "mixed", "rotation", "expected-pid", "expected-agent"} {
			for _, relaunch := range []bool{false, true} {
				cases = append(cases, testCase{mode: mode, verb: verb, relaunch: relaunch})
			}
		}
	}
	for _, mode := range []string{"single", "double", "omp", "guard-race", "gone", "gone-running", "dead-after", "lookup-failure", "pane-transient", "process-failure", "process-transient", "send-error-exited", "send-error-running", "cancelled-exited", "cancelled-running", "replacement-child", "still-running", "retry", "replaced-before", "replaced-after", "already-shell"} {
		cases = append(cases, testCase{mode: mode, verb: "exit"})
	}
	cases = append(cases, testCase{mode: "already-shell", verb: "kill"}, testCase{mode: "kill-tree", verb: "kill"})
	const script = `#!/bin/sh
set -eu
root="$NTM_LIFECYCLE_SHELL_TEST_ROOT"
mode="$NTM_LIFECYCLE_SHELL_TEST_MODE"
printf '%s\n' "$1" >> "$root/calls"
retire_agent() {
  kill -INT "$NTM_LIFECYCLE_AGENT_PID"
  tries=0
  while kill -0 "$NTM_LIFECYCLE_AGENT_PID" 2>/dev/null; do
    tries=$((tries + 1))
    [ "$tries" -lt 200 ] || exit 59
    sleep 0.01
  done
}
case "$1" in
  has-session) ;;
  list-panes)
    pid="$NTM_LIFECYCLE_ROOT_PID"
    command=claude
    agent=cc
    dead=0
    rotation=''
    if [ "$mode" = dead ]; then dead=1; fi
    if [ "$mode" = rotation ]; then rotation=handoff-in-progress; fi
    if [ "$mode" = already-shell ]; then command=sh; fi
    if [ "$mode" = kill-tree ] && ! kill -0 "$NTM_LIFECYCLE_AGENT_PID" 2>/dev/null; then command=sh; fi
    if [ "$mode" = omp ]; then agent=omp; command=omp; fi
    if [ "$mode" = replaced-before ] && [ -f "$root/listed" ]; then pid="$NTM_LIFECYCLE_OTHER_PID"; fi
    if [ -f "$root/changed" ]; then
      case "$mode" in
        gone|gone-running) exit 0 ;;
        dead-after) dead=1; command='' ;;
        lookup-failure) printf 'temporary transport failure\n' >&2; exit 77 ;;
        pane-transient)
          if [ ! -f "$root/pane-failed" ]; then : > "$root/pane-failed"; exit 77; fi
          command=sh
          ;;
        replacement-child) command=claude ;;
        replaced-after) pid="$NTM_LIFECYCLE_OTHER_PID"; command=sh ;;
        *) command=sh ;;
      esac
    fi
    printf '%%7_NTM_SEP_1_NTM_SEP_session__%s_1_NTM_SEP_%s_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_%s_NTM_SEP__NTM_SEP__NTM_SEP_%s_NTM_SEP_[%s]\n' "$agent" "$command" "$pid" "$agent" "$dead" "$rotation"
    if [ "$mode" = mixed ]; then
      printf '%%8_NTM_SEP_2_NTM_SEP_session__cc_2_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_0_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0_NTM_SEP_[]\n' "$NTM_LIFECYCLE_OTHER_PID"
    fi
    : > "$root/listed"
    ;;
  display-message)
    [ "$5" = '[#{@ntm_agent_type}]' ] || exit 51
    if [ "$mode" = omp ]; then printf '[omp]\n'; else printf '[cc]\n'; fi
    ;;
  if-shell)
    [ "$2" = '-F' ] && [ "$4" = '%7' ] || exit 52
    case "$5" in
      *"#{==:#{pane_pid},$NTM_LIFECYCLE_ROOT_PID}"*) ;;
      *) exit 53 ;;
    esac
    if [ "$mode" = guard-race ]; then retire_agent; : > "$root/changed"; fi
    if [ -f "$root/changed" ] && [ "$mode" != replacement-child ]; then
      printf 'ntm_pane_identity_changed\n'
      exit 0
    fi
    eval "set -- $6"
    [ "$1" = send-keys ] && [ "$3" = '%7' ] || exit 54
    printf '%s\n' "$4" >> "$root/keys"
    if [ "$4" = Escape ]; then exit 0; fi
    [ "$4" = C-c ] || exit 55
    case "$mode" in
      send-error-running) printf 'lost send response\n' >&2; exit 77 ;;
      cancelled-running) : > "$root/cancel"; exec sleep 60 ;;
      gone-running) : > "$root/changed"; exit 0 ;;
      still-running) kill -INT "$NTM_LIFECYCLE_AGENT_PID"; exit 0 ;;
    esac
    if [ "$mode" = double ] && [ ! -f "$root/interrupted" ]; then
      kill -INT "$NTM_LIFECYCLE_AGENT_PID"
      : > "$root/interrupted"
    else
      retire_agent
      : > "$root/changed"
      case "$mode" in
        send-error-exited) printf 'lost send response\n' >&2; exit 77 ;;
        cancelled-exited) : > "$root/cancel"; exec sleep 60 ;;
        replacement-child)
          tries=0
          while [ ! -s "$root/next-agent-ready" ]; do
            tries=$((tries + 1))
            [ "$tries" -lt 200 ] || exit 60
            sleep 0.01
          done
          ;;
      esac
    fi
    ;;
  send-keys)
    printf 'unguarded pane input\n' >&2
    exit 56
    ;;
  *) exit 57 ;;
esac
`
	for _, tc := range cases {
		t.Run(tc.verb+"/"+tc.mode+"/relaunch="+strconv.FormatBool(tc.relaunch), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "tmux")
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLifecycleShellTransportHelper$")
			cmd.Env = append(os.Environ(),
				"NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+path,
				"NTM_LIFECYCLE_SHELL_TEST_ROOT="+root, "NTM_LIFECYCLE_SHELL_TEST_MODE="+tc.mode,
				"NTM_LIFECYCLE_SHELL_TEST_VERB="+tc.verb, "NTM_LIFECYCLE_SHELL_TEST_RELAUNCH="+strconv.FormatBool(tc.relaunch),
				"HOME="+root, "NTM_CONFIG="+filepath.Join(root, "config.toml"))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("lifecycle shell evidence: %v\n%s", err, output)
			}
		})
	}
}

// Use real process arguments for shell admission, independently of the tmux
// transport's foreground command. Keeping stdin open leaves interactive shells
// and command-mode read fixtures alive until their owning test cleans them up.
func startLifecycleRootProcess(t *testing.T, name string, args ...string) int {
	t.Helper()
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// The transport fixtures own real shell and agent lifetimes. Only terminal
// process-group metadata is modeled: a pipe-backed shell has no controlling
// terminal, whereas the native tmux tests below exercise actual job control.
type lifecycleShellProcess struct {
	PID       int
	AgentPID  int
	HelperPID int
	input     io.WriteCloser
}

func lifecycleAgentCommand(ready, mode string) string {
	return "env NTM_TEST_TMUX_ENV_OWNED=1 NTM_LIFECYCLE_AGENT_READY=" + tmux.ShellQuote(ready) +
		" NTM_LIFECYCLE_AGENT_MODE=" + tmux.ShellQuote(mode) + " " + tmux.ShellQuote(os.Args[0]) +
		" -test.run=^TestLifecycleAgentProcessHelper$"
}

func waitLifecycleAgentPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 && process.IsAlive(pid) {
				return pid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("owned agent did not become ready: %s", path)
	return 0
}

func startLifecycleShellProcess(t *testing.T, root, mode string, active bool) *lifecycleShellProcess {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-i")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	fixture := &lifecycleShellProcess{PID: cmd.Process.Pid, input: input}
	t.Cleanup(func() {
		// A test deliberately reparents this helper; retain its owned identity
		// so a failing assertion cannot leak it outside the shell's child tree.
		if fixture.HelperPID > 0 {
			if helper, err := os.FindProcess(fixture.HelperPID); err == nil {
				_ = helper.Kill()
			}
		}
		// All remaining cleanup is restricted to this owned shell's children.
		for _, pid := range process.GetChildPIDs(fixture.PID, 16) {
			if child, err := os.FindProcess(pid); err == nil {
				_ = child.Kill()
			}
		}
		_ = input.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if active {
		ready := filepath.Join(root, "agent-ready")
		if _, err := fmt.Fprintln(input, lifecycleAgentCommand(ready, mode)); err != nil {
			t.Fatal(err)
		}
		fixture.AgentPID = waitLifecycleAgentPID(t, ready)
		if mode == "kill-tree" {
			fixture.HelperPID = waitLifecycleAgentPID(t, ready+"-helper")
		}
	}
	return fixture
}

// Preserve authoritative liveness, parentage, kernel command and process start
// time from real ps. Model only the job-control groups absent from pipe fixtures.
func modelLifecycleForeground(t *testing.T, root, mode string, shellPID int) {
	t.Helper()
	realPS, err := exec.LookPath("ps")
	if err != nil {
		t.Fatal(err)
	}
	const script = `#!/bin/sh
set -eu
if [ "$*" != '-ww -axo pid=,ppid=,pgid=,tpgid=,stat=,lstart=,ucomm=' ]; then
  exec "$NTM_LIFECYCLE_REAL_PS" "$@"
fi
root="$NTM_LIFECYCLE_PROCESS_ROOT"
if [ -f "$root/changed" ]; then
  case "$NTM_LIFECYCLE_PROCESS_MODE" in
    process-failure) printf 'process observation unavailable\n' >&2; exit 78 ;;
    process-transient)
      if [ ! -f "$root/ps-failed" ]; then : > "$root/ps-failed"; exit 78; fi
      ;;
  esac
fi
"$NTM_LIFECYCLE_REAL_PS" "$@" | awk -v root="$NTM_LIFECYCLE_MODEL_ROOT_PID" '
  { rows[NR] = $0; if ($2 == root && $5 !~ /^[ZX]/) child = $1 }
  END {
    for (i = 1; i <= NR; i++) {
      n = split(rows[i], fields)
      if (fields[1] == root) { fields[3] = root; fields[4] = child ? child : root }
      if (fields[2] == root) { fields[3] = fields[1]; fields[4] = fields[1] }
      for (j = 1; j <= n; j++) printf "%s%s", fields[j], j == n ? "\n" : " "
    }
  }'
`
	if err := os.WriteFile(filepath.Join(root, "ps"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_LIFECYCLE_REAL_PS", realPS)
	t.Setenv("NTM_LIFECYCLE_PROCESS_ROOT", root)
	t.Setenv("NTM_LIFECYCLE_PROCESS_MODE", mode)
	t.Setenv("NTM_LIFECYCLE_MODEL_ROOT_PID", strconv.Itoa(shellPID))
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLifecycleAgentProcessHelper(t *testing.T) {
	ready := os.Getenv("NTM_LIFECYCLE_AGENT_READY")
	if ready == "" {
		return
	}
	interrupts := make(chan os.Signal, 8)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	mode := os.Getenv("NTM_LIFECYCLE_AGENT_MODE")
	if mode == "kill-tree" {
		// The parent exits on TERM; this already captured child ignores TERM
		// and HUP, so the lifecycle operation must finish it after reparenting.
		helperReady := ready + "-helper"
		child := exec.Command("/bin/sh", "-c", `trap '' HUP TERM; printf '%s' "$$" > "$NTM_LIFECYCLE_TREE_HELPER_READY"; exec sleep 60`)
		child.Env = append(os.Environ(), "NTM_LIFECYCLE_TREE_HELPER_READY="+helperReady)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
		waitLifecycleAgentPID(t, helperReady)
	}
	if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	for count := 1; ; count++ {
		<-interrupts
		if err := os.WriteFile(ready+"-interrupts", []byte(strconv.Itoa(count)), 0600); err != nil {
			t.Fatal(err)
		}
		if mode == "still-running" || (mode == "double" && count < 2) {
			continue
		}
		return
	}
}

func afterLifecycleFile(t *testing.T, path string, action func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(path); err == nil {
					action()
					return
				}
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func TestLifecycleShellTransportHelper(t *testing.T) {
	root := os.Getenv("NTM_LIFECYCLE_SHELL_TEST_ROOT")
	if root == "" {
		return
	}
	mode := os.Getenv("NTM_LIFECYCLE_SHELL_TEST_MODE")
	verb := os.Getenv("NTM_LIFECYCLE_SHELL_TEST_VERB")
	fixture := startLifecycleShellProcess(t, root, mode, mode != "already-shell")
	shellPID := fixture.PID
	otherPID := startLifecycleRootProcess(t, "cat")
	rootPID := shellPID
	switch mode {
	case "direct":
		rootPID = otherPID
	case "command":
		rootPID = startLifecycleRootProcess(t, "/bin/sh", "-ic", "read ignored")
	case "unreadable":
		rootPID = 2000000001
	case "missing-pid":
		rootPID = 0
	}
	t.Setenv("NTM_LIFECYCLE_ROOT_PID", strconv.Itoa(rootPID))
	t.Setenv("NTM_LIFECYCLE_OTHER_PID", strconv.Itoa(otherPID))
	t.Setenv("NTM_LIFECYCLE_AGENT_PID", strconv.Itoa(fixture.AgentPID))
	modelLifecycleForeground(t, root, mode, shellPID)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	afterLifecycleFile(t, filepath.Join(root, "cancel"), cancel)
	if mode == "replacement-child" {
		afterLifecycleFile(t, filepath.Join(root, "changed"), func() {
			_, _ = fmt.Fprintln(fixture.input, lifecycleAgentCommand(filepath.Join(root, "next-agent-ready"), "still-running"))
		})
	}
	opts := LifecycleOptions{
		Session:  "session",
		Relaunch: os.Getenv("NTM_LIFECYCLE_SHELL_TEST_RELAUNCH") == "true",
	}
	if mode == "expected-pid" || mode == "expected-agent" {
		opts.ExpectedPane = &tmux.Pane{ID: "%7", PID: shellPID, Type: tmux.AgentClaude}
		if mode == "expected-pid" {
			opts.ExpectedPane.PID = otherPID
		} else {
			opts.ExpectedPane.Type = tmux.AgentCodex
		}
	}
	var success bool
	var errorCode string
	var results []LifecyclePaneResult
	if verb == "exit" {
		out, err := GetExitCLI(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		success, errorCode, results = out.Success, out.ErrorCode, out.Results
	} else {
		out, err := GetKillAgent(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		success, errorCode, results = out.Success, out.ErrorCode, out.Results
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	keys, calls := read("keys"), read("calls")
	if !process.IsAlive(shellPID) || !process.IsAlive(otherPID) {
		t.Fatal("pane-preserving lifecycle action terminated a fixture root")
	}
	switch mode {
	case "direct", "command", "unreadable", "missing-pid", "dead", "mixed", "rotation", "expected-pid", "expected-agent":
		if success || errorCode != ErrCodeInvalidFlag || len(results) != 0 ||
			keys != "" || strings.Contains(calls, "if-shell") || strings.Contains(calls, "send-keys") {
			t.Fatalf("unsafe shell admission crossed the mutation boundary: success=%t code=%s results=%+v calls=%s keys=%q",
				success, errorCode, results, calls, keys)
		}
		return
	case "replaced-before":
		if success || len(results) != 1 || results[0].ShellPreserved || keys != "" || strings.Contains(calls, "if-shell") {
			t.Fatalf("replaced pane was acted on: success=%t results=%+v calls=%s keys=%q", success, results, calls, keys)
		}
		return
	case "gone", "dead-after", "lookup-failure", "replaced-after":
		wantPane := "present"
		if mode == "gone" {
			wantPane = "missing"
		} else if mode == "lookup-failure" {
			wantPane = "unknown"
		}
		if success || len(results) != 1 || !results[0].Exited || results[0].ProcessState != "exited" ||
			results[0].PaneState != wantPane || results[0].ShellPreserved ||
			results[0].VerificationFailed != (mode == "lookup-failure") ||
			strings.Contains(results[0].Detail, "--robot-kill-agent") || keys != "C-c\n" {
			t.Fatalf("unavailable or replaced pane got misleading exit evidence or extra input: success=%t results=%+v keys=%q",
				success, results, keys)
		}
		return
	case "gone-running", "send-error-running", "cancelled-running", "still-running":
		wantPane, wantKeys := "present", "C-c\n"
		if mode == "gone-running" {
			wantPane = "missing"
		}
		if mode == "still-running" {
			wantKeys = "C-c\nC-c\nC-c\nC-c\n"
		}
		if success || len(results) != 1 || results[0].Exited || results[0].ProcessState != "running" ||
			results[0].PaneState != wantPane || results[0].VerificationFailed || keys != wantKeys || !process.IsAlive(fixture.AgentPID) {
			t.Fatalf("live original agent or canceled action was misclassified/replayed: success=%t results=%+v keys=%q", success, results, keys)
		}
		return
	case "process-failure":
		if success || len(results) != 1 || results[0].Exited || results[0].ProcessState != "unknown" ||
			!results[0].VerificationFailed || keys != "C-c\n" {
			t.Fatalf("failed process observation was treated as absence: success=%t results=%+v keys=%q", success, results, keys)
		}
		return
	case "replacement-child":
		if success || len(results) != 1 || !results[0].Exited || results[0].ProcessState != "exited" ||
			!results[0].ShellPreserved || keys != "C-c\n" {
			t.Fatalf("successor foreground agent was signaled or mistaken for original: success=%t results=%+v keys=%q", success, results, keys)
		}
		pid := waitLifecycleAgentPID(t, filepath.Join(root, "next-agent-ready"))
		if !process.IsAlive(pid) {
			t.Fatal("successor agent did not survive the original exit")
		}
		if _, err := os.Stat(filepath.Join(root, "next-agent-ready-interrupts")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successor agent received input: %v", err)
		}
		if results[0].ProcessIdentity == nil {
			t.Fatal("compound caller did not receive original process identity")
		}
		opts.ExpectedProcesses = results[0].ProcessIdentity
		opts.ExpectedPane = &tmux.Pane{ID: "%7", PID: shellPID, Type: tmux.AgentClaude}
		escalated, err := GetKillAgent(t.Context(), opts)
		if err != nil || escalated.Success || len(escalated.Results) != 1 || !escalated.Results[0].Refused ||
			len(escalated.Results[0].Killed) != 0 || !process.IsAlive(pid) || read("keys") != keys {
			t.Fatalf("bound escalation targeted the replacement: response=%+v err=%v successorAlive=%t", escalated, err, process.IsAlive(pid))
		}
		return
	}
	if !success || len(results) != 1 || !results[0].Exited || !results[0].ShellPreserved || results[0].VerificationFailed ||
		results[0].ShellPID != shellPID || results[0].PaneState != "present" || results[0].ProcessState != "exited" {
		t.Fatalf("verified shell exit was not successful: success=%t results=%+v keys=%q", success, results, keys)
	}
	wantKeys := "C-c\n"
	switch mode {
	case "double":
		wantKeys = "C-c\nC-c\n"
	case "omp":
		wantKeys = "Escape\nC-c\n"
	case "already-shell", "guard-race", "kill-tree":
		wantKeys = ""
	}
	if keys != wantKeys {
		t.Fatalf("exit keys = %q, want %q; an already-exited agent must receive no follow-up input", keys, wantKeys)
	}
	if mode == "kill-tree" {
		if process.IsAlive(fixture.AgentPID) || process.IsAlive(fixture.HelperPID) || len(results[0].Killed) != 2 {
			t.Fatalf("kill abandoned a captured helper after its parent exited: result=%+v parentAlive=%t helperAlive=%t", results[0], process.IsAlive(fixture.AgentPID), process.IsAlive(fixture.HelperPID))
		}
	}
	if mode == "retry" {
		again, err := GetExitCLI(t.Context(), opts)
		if err != nil || !again.Success || len(again.Results) != 1 || !again.Results[0].Exited ||
			again.Results[0].PaneState != "present" || again.Results[0].ProcessState != "exited" || read("keys") != keys {
			t.Fatalf("retry after confirmed exit was not idempotent: response=%+v err=%v keys=%q", again, err, read("keys"))
		}
	}
}

// Exercise terminal job control and actual pane survival on TestMain's private
// tmux server. No installed agent CLI, provider credentials, or user pane is used.
func TestLifecycleNativePanePreservation(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"direct", "single", "double", "omp", "send-error", "cancelled", "gone", "lookup-failure", "kill", "kill-background", "kill-stopped"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			session := fmt.Sprintf("ntm-lifecycle-379-%d", time.Now().UnixNano())
			if _, err := tmux.DefaultClient.Run("new-session", "-d", "-s", session, "/bin/sh", "-i"); err != nil {
				t.Fatalf("create disposable lifecycle session: %v", err)
			}
			t.Cleanup(func() {
				// Explicit binary bypasses fault injection; the inherited private
				// TMUX_TMPDIR still restricts cleanup to this test process's server.
				_ = exec.Command(realTmux, "kill-session", "-t", session).Run()
			})
			panes, err := tmux.GetPanes(session)
			if err != nil || len(panes) != 1 {
				t.Fatalf("find owned pane: panes=%+v err=%v", panes, err)
			}
			pane := panes[0]
			ready := filepath.Join(root, "agent-ready")
			agentMode := "single"
			if mode == "double" {
				agentMode = "double"
			}
			if mode == "direct" {
				_, err = tmux.DefaultClient.Run("respawn-pane", "-k", "-t", pane.ID,
					"env", "NTM_TEST_TMUX_ENV_OWNED=1", "NTM_LIFECYCLE_AGENT_READY="+ready,
					"NTM_LIFECYCLE_AGENT_MODE="+agentMode, os.Args[0], "-test.run=^TestLifecycleAgentProcessHelper$")
			} else {
				command := lifecycleAgentCommand(ready, agentMode)
				if mode == "kill-background" {
					command += " &"
				}
				err = tmux.DefaultClient.RunSilent("send-keys", "-t", pane.ID, "-l", "--", command)
				if err == nil {
					err = tmux.DefaultClient.RunSilent("send-keys", "-t", pane.ID, "Enter")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			agentPID := waitLifecycleAgentPID(t, ready)
			if mode == "kill-stopped" {
				if err := exec.Command("kill", "-STOP", strconv.Itoa(agentPID)).Run(); err != nil {
					t.Fatalf("stop owned agent process: %v", err)
				}
			}
			agentType := tmux.AgentClaude
			if mode == "omp" {
				agentType = tmux.AgentOmp
			}
			if err := tmux.DefaultClient.SetPaneAgentTypeContext(t.Context(), pane.ID, agentType); err != nil {
				t.Fatal(err)
			}
			panes, err = tmux.GetPanes(session)
			if err != nil || len(panes) != 1 {
				t.Fatalf("refresh owned agent pane: panes=%+v err=%v", panes, err)
			}
			pane = panes[0]
			const wrapper = `#!/bin/sh
set -eu
root="$NTM_LIFECYCLE_NATIVE_ROOT"
mode="$NTM_LIFECYCLE_NATIVE_MODE"
if [ "$1" = list-panes ] && [ "$mode" = lookup-failure ] && [ -f "$root/changed" ]; then
  printf 'injected read failure\n' >&2
  exit 77
fi
if [ "$1" = if-shell ]; then
  case "$6" in
    *C-c*)
      printf 'C-c\n' >> "$root/keys"
      "$NTM_LIFECYCLE_NATIVE_TMUX" "$@"
      if [ "$mode" = double ] && [ ! -f "$root/first" ]; then : > "$root/first"; exit 0; fi
      tries=0
      while kill -0 "$NTM_LIFECYCLE_NATIVE_AGENT_PID" 2>/dev/null; do
        tries=$((tries + 1))
        [ "$tries" -lt 200 ] || exit 79
        sleep 0.01
      done
      : > "$root/changed"
      case "$mode" in
        send-error) printf 'lost delivery response\n' >&2; exit 77 ;;
        cancelled) : > "$root/cancel"; exec sleep 60 ;;
        gone) "$NTM_LIFECYCLE_NATIVE_TMUX" kill-pane -t "$NTM_LIFECYCLE_NATIVE_PANE" ;;
      esac
      exit 0
      ;;
    *C-d*) printf 'C-d\n' >> "$root/keys" ;;
  esac
fi
exec "$NTM_LIFECYCLE_NATIVE_TMUX" "$@"
`
			wrapperPath := filepath.Join(root, "tmux")
			if err := os.WriteFile(wrapperPath, []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NTM_TMUX_BINARY", wrapperPath)
			t.Setenv("NTM_LIFECYCLE_NATIVE_TMUX", realTmux)
			t.Setenv("NTM_LIFECYCLE_NATIVE_ROOT", root)
			t.Setenv("NTM_LIFECYCLE_NATIVE_MODE", mode)
			t.Setenv("NTM_LIFECYCLE_NATIVE_AGENT_PID", strconv.Itoa(agentPID))
			t.Setenv("NTM_LIFECYCLE_NATIVE_PANE", pane.ID)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			afterLifecycleFile(t, filepath.Join(root, "cancel"), cancel)
			opts := LifecycleOptions{Session: session, Panes: []string{pane.ID}}
			var success bool
			var results []LifecyclePaneResult
			if strings.HasPrefix(mode, "kill") {
				out, err := GetKillAgent(ctx, opts)
				if err != nil {
					t.Fatal(err)
				}
				success, results = out.Success, out.Results
			} else {
				out, err := GetExitCLI(ctx, opts)
				if err != nil {
					t.Fatal(err)
				}
				success, results = out.Success, out.Results
			}
			keys, err := os.ReadFile(filepath.Join(root, "keys"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if mode == "direct" {
				if success || len(results) != 0 || len(keys) != 0 || !process.IsAlive(agentPID) {
					t.Fatalf("direct-exec refusal killed or signaled its pane root: success=%t results=%+v keys=%q", success, results, keys)
				}
				again, err := GetExitCLI(t.Context(), opts)
				if err != nil || again.Success || !process.IsAlive(agentPID) {
					t.Fatalf("retry made a direct-exec refusal destructive: response=%+v err=%v", again, err)
				}
				return
			}
			wantPane := "present"
			if mode == "gone" {
				wantPane = "missing"
			} else if mode == "lookup-failure" {
				wantPane = "unknown"
			}
			wantPreserved := mode != "gone" && mode != "lookup-failure"
			if len(results) != 1 || !results[0].Exited || results[0].ProcessState != "exited" ||
				results[0].PaneState != wantPane || results[0].ShellPreserved != wantPreserved ||
				results[0].VerificationFailed != (mode == "lookup-failure") || success != wantPreserved {
				t.Fatalf("native lifecycle evidence: success=%t results=%+v keys=%q", success, results, keys)
			}
			wantKeys := "C-c\n"
			if mode == "double" {
				wantKeys += "C-c\n"
			} else if strings.HasPrefix(mode, "kill") {
				wantKeys = ""
			}
			if string(keys) != wantKeys || process.IsAlive(agentPID) {
				t.Fatalf("native exit used unexpected input or left agent alive: keys=%q want=%q alive=%t", keys, wantKeys, process.IsAlive(agentPID))
			}
			if wantPreserved {
				remaining, err := tmux.GetPanes(session)
				if err != nil || len(remaining) != 1 || remaining[0].ID != pane.ID || remaining[0].PID != pane.PID || !remaining[0].IdleShell() {
					t.Fatalf("original native shell/pane was not preserved: panes=%+v err=%v", remaining, err)
				}
				again, err := GetExitCLI(t.Context(), opts)
				if err != nil || !again.Success || len(again.Results) != 1 || !again.Results[0].ShellPreserved {
					t.Fatalf("native safe retry: response=%+v err=%v", again, err)
				}
				after, _ := os.ReadFile(filepath.Join(root, "keys"))
				if string(after) != string(keys) {
					t.Fatalf("retry sent input to restored shell: before=%q after=%q", keys, after)
				}
			}
		})
	}
}

func TestDirectExecMockAgentHelper(t *testing.T) {
	if os.Getenv("NTM_MOCK_DIRECT_EXEC_AGENT") != "1" {
		return
	}
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	doneCh := make(chan struct{})
	go func() {
		var buf [16]byte
		for {
			n, err := os.Stdin.Read(buf[:])
			for i := 0; i < n; i++ {
				if buf[i] == 0x03 || buf[i] == 0x04 {
					close(doneCh)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-sigCh:
	case <-doneCh:
	case <-time.After(30 * time.Second):
	}
	os.Exit(0)
}

func TestExitCLI_DirectExecProtectionAndOptInFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real tmux lifecycle test requires POSIX")
	}
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux not installed")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "agent-workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	directExecCmd := fmt.Sprintf("env NTM_MOCK_DIRECT_EXEC_AGENT=1 %s -test.run=^TestDirectExecMockAgentHelper$", tmux.ShellQuote(selfExe))

	waitForForegroundAgent := func(t *testing.T, session string) tmux.Pane {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			panes, err := tmux.GetPanes(session)
			if err == nil && len(panes) > 0 && panes[0].PID > 0 && !panes[0].Dead &&
				panes[0].Command != "" && panes[0].Command != "tmux" && panes[0].Command != "env" &&
				!tmux.PaneCommandIsShell(panes[0].Command) {
				return panes[0]
			}
			time.Sleep(50 * time.Millisecond)
		}
		panes, _ := tmux.GetPanes(session)
		t.Fatalf("agent foreground process never appeared in %s: panes=%+v", session, panes)
		return tmux.Pane{}
	}

	t.Run("default refuses direct-exec root agent and leaves pane untouched", func(t *testing.T) {
		session := fmt.Sprintf("ntm-direct-refuse-%d", time.Now().UnixNano())
		t.Cleanup(func() { _ = tmux.KillSession(session) })
		if err := tmux.DefaultClient.RunSilent("new-session", "-d", "-s", session, "-c", workDir, directExecCmd); err != nil {
			t.Fatalf("new-session: %v", err)
		}
		pane := waitForForegroundAgent(t, session)
		if err := tmux.SetPaneAgentIdentityContext(t.Context(), pane.ID, session+"__cc_1", tmux.AgentClaude); err != nil {
			t.Fatalf("SetPaneAgentIdentityContext: %v", err)
		}

		out, err := GetExitCLI(t.Context(), LifecycleOptions{Session: session})
		if err != nil {
			t.Fatalf("GetExitCLI: %v", err)
		}
		if out.Success || out.ErrorCode != ErrCodeInvalidFlag || !strings.Contains(out.Error, "DIRECT_EXEC_NO_SHELL") || !strings.Contains(out.Hint, "--respawn-shell") {
			t.Fatalf("expected DIRECT_EXEC_NO_SHELL refusal with --respawn-shell hint, got %+v", out)
		}
		if !process.IsAlive(pane.PID) || !tmux.SessionExists(session) {
			t.Fatalf("refusal destroyed direct-exec pane or session (pid=%d)", pane.PID)
		}
	})

	t.Run("respawn-shell replaces direct-exec single-pane agent with interactive shell in same cwd", func(t *testing.T) {
		session := fmt.Sprintf("ntm-direct-respawn-%d", time.Now().UnixNano())
		t.Cleanup(func() { _ = tmux.KillSession(session) })
		if err := tmux.DefaultClient.RunSilent("new-session", "-d", "-s", session, "-c", workDir, directExecCmd); err != nil {
			t.Fatalf("new-session: %v", err)
		}
		pane := waitForForegroundAgent(t, session)
		if err := tmux.SetPaneAgentIdentityContext(t.Context(), pane.ID, session+"__cc_1", tmux.AgentClaude); err != nil {
			t.Fatalf("SetPaneAgentIdentityContext: %v", err)
		}

		out, err := GetExitCLI(t.Context(), LifecycleOptions{Session: session, RespawnShell: true})
		if err != nil {
			t.Fatalf("GetExitCLI --respawn-shell: %v", err)
		}
		if !out.Success || len(out.Results) != 1 {
			t.Fatalf("expected success with 1 result, got %+v", out)
		}
		res := out.Results[0]
		if !res.Exited || !res.ShellPreserved || res.Status != "respawned_shell" || res.LineageMode != "direct_exec" || res.VerificationFailed {
			t.Fatalf("unexpected respawn-shell result: %+v", res)
		}
		if !tmux.SessionExists(session) {
			t.Fatal("single-pane session was destroyed during --respawn-shell")
		}
		gotCwd, err := restartPaneWorkingDirectory(t.Context(), pane.ID)
		if err != nil || gotCwd != workDir {
			t.Fatalf("respawned shell cwd = %q (err=%v), want %q", gotCwd, err, workDir)
		}
	})

	t.Run("allow-pane-close exits direct-exec pane cleanly without false verification_failed", func(t *testing.T) {
		session := fmt.Sprintf("ntm-direct-close-%d", time.Now().UnixNano())
		t.Cleanup(func() { _ = tmux.KillSession(session) })
		if err := tmux.DefaultClient.RunSilent("new-session", "-d", "-s", session, "-c", workDir, directExecCmd); err != nil {
			t.Fatalf("new-session: %v", err)
		}
		pane := waitForForegroundAgent(t, session)
		if err := tmux.SetPaneAgentIdentityContext(t.Context(), pane.ID, session+"__cc_1", tmux.AgentClaude); err != nil {
			t.Fatalf("SetPaneAgentIdentityContext: %v", err)
		}

		out, err := GetExitCLI(t.Context(), LifecycleOptions{Session: session, AllowPaneClose: true})
		if err != nil {
			t.Fatalf("GetExitCLI --allow-pane-close: %v", err)
		}
		if !out.Success || len(out.Results) != 1 {
			t.Fatalf("expected success with 1 result, got %+v", out)
		}
		res := out.Results[0]
		if !res.Exited || !res.PaneClosed || res.Status != "pane_closed" || res.VerificationFailed {
			t.Fatalf("expected pane_closed without verification_failed, got %+v", res)
		}
	})
}
