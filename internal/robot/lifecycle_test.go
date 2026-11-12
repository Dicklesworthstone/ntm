package robot

import (
	"context"
	"errors"
	"fmt"
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

	killed := killProcessesGracefully(context.Background(), []int{pid})
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
	// A result that could not be verified must be distinguishable from one
	// that verified the pane was destroyed. Both have ShellPreserved=false.
	unverified := LifecyclePaneResult{ShellPreserved: false, VerificationFailed: true}
	destroyed := LifecyclePaneResult{ShellPreserved: false}

	if unverified.VerificationFailed == destroyed.VerificationFailed {
		t.Fatal("an unverified result is indistinguishable from a destroyed pane")
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
	for _, mode := range []string{"single", "double", "omp", "guard-race", "gone", "dead-after", "lookup-failure", "replaced-before", "replaced-after", "already-shell"} {
		cases = append(cases, testCase{mode: mode, verb: "exit"})
	}
	cases = append(cases, testCase{mode: "already-shell", verb: "kill"})
	const script = `#!/bin/sh
set -eu
root="$NTM_LIFECYCLE_SHELL_TEST_ROOT"
mode="$NTM_LIFECYCLE_SHELL_TEST_MODE"
printf '%s\n' "$1" >> "$root/calls"
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
    if [ "$mode" = omp ]; then agent=omp; command=omp; fi
    if [ "$mode" = replaced-before ] && [ -f "$root/listed" ]; then pid="$NTM_LIFECYCLE_OTHER_PID"; fi
    if [ -f "$root/changed" ]; then
      case "$mode" in
        gone) exit 0 ;;
        dead-after) dead=1; command='' ;;
        lookup-failure) printf 'temporary transport failure\n' >&2; exit 77 ;;
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
    if [ "$mode" = guard-race ]; then : > "$root/changed"; fi
    if [ -f "$root/changed" ]; then
      printf 'ntm_pane_identity_changed\n'
      exit 0
    fi
    eval "set -- $6"
    [ "$1" = send-keys ] && [ "$3" = '%7' ] || exit 54
    printf '%s\n' "$4" >> "$root/keys"
    if [ "$4" = Escape ]; then exit 0; fi
    [ "$4" = C-c ] || exit 55
    if [ "$mode" = double ] && [ ! -f "$root/interrupted" ]; then
      : > "$root/interrupted"
    else
      : > "$root/changed"
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
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
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
// transport's foreground command. Keeping stdin open leaves both interactive
// shells and command-mode read fixtures alive without spawning agent children.
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

func TestLifecycleShellTransportHelper(t *testing.T) {
	root := os.Getenv("NTM_LIFECYCLE_SHELL_TEST_ROOT")
	if root == "" {
		return
	}
	mode := os.Getenv("NTM_LIFECYCLE_SHELL_TEST_MODE")
	verb := os.Getenv("NTM_LIFECYCLE_SHELL_TEST_VERB")
	shellPID := startLifecycleRootProcess(t, "/bin/sh", "-i")
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
		out, err := GetExitCLI(t.Context(), opts)
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
		if success || len(results) != 1 || results[0].Exited || results[0].ShellPreserved ||
			results[0].VerificationFailed != (mode == "lookup-failure") ||
			strings.Contains(results[0].Detail, "--robot-kill-agent") || keys != "C-c\n" {
			t.Fatalf("unavailable or replaced pane got misleading exit evidence or extra input: success=%t results=%+v keys=%q",
				success, results, keys)
		}
		return
	}
	if !success || len(results) != 1 || !results[0].Exited || !results[0].ShellPreserved || results[0].VerificationFailed ||
		results[0].ShellPID != shellPID || strings.Contains(results[0].Detail, "failed") {
		t.Fatalf("verified shell exit was not successful: success=%t results=%+v keys=%q", success, results, keys)
	}
	wantKeys := "C-c\n"
	switch mode {
	case "double":
		wantKeys = "C-c\nC-c\n"
	case "omp":
		wantKeys = "Escape\nC-c\n"
	case "already-shell", "guard-race":
		wantKeys = ""
	}
	if keys != wantKeys {
		t.Fatalf("exit keys = %q, want %q; an already-exited agent must receive no follow-up input", keys, wantKeys)
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
