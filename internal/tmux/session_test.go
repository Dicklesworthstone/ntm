package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestMain(m *testing.M) {
	if os.Getenv("NTM_TEST_TMUX_ENV_OWNED") == "1" {
		os.Exit(m.Run())
	}

	tmuxRoot, err := os.MkdirTemp(tmuxTestTempBase(), "ntm-tmux-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated tmux root: %v\n", err)
		os.Exit(1)
	}
	for _, setting := range []struct {
		key   string
		value string
	}{
		{key: "TMUX", value: ""},
		{key: "TMUX_PANE", value: ""},
		{key: "TMUX_TMPDIR", value: tmuxRoot},
	} {
		if err := os.Setenv(setting.key, setting.value); err != nil {
			_ = os.RemoveAll(tmuxRoot)
			fmt.Fprintf(os.Stderr, "set %s for tmux isolation: %v\n", setting.key, err)
			os.Exit(1)
		}
	}
	cleanupBinary := findInstalledTmuxBinaryPath()
	if cleanupBinary != "" {
		startupCtx, cancelStartup := context.WithTimeout(context.Background(), 8*time.Second)
		cmd := exec.CommandContext(
			startupCtx,
			cleanupBinary,
			"start-server", ";", "set-option", "-s", "exit-empty", "off",
		)
		cmd.Env = isolatedTmuxTestEnvironment(tmuxRoot)
		output, startupErr := cmd.CombinedOutput()
		contextErr := startupCtx.Err()
		cancelStartup()
		if startupErr != nil {
			_ = os.RemoveAll(tmuxRoot)
			fmt.Fprintf(
				os.Stderr,
				"start isolated tmux server: %s: %v: %s\n",
				cleanupBinary,
				errors.Join(startupErr, contextErr),
				strings.TrimSpace(string(output)),
			)
			os.Exit(1)
		}
	}

	code := m.Run()
	if cleanupBinary != "" {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 8*time.Second)
		cmd := exec.CommandContext(cleanupCtx, cleanupBinary, "kill-server")
		cmd.Env = isolatedTmuxTestEnvironment(tmuxRoot)
		output, cleanupErr := cmd.CombinedOutput()
		contextErr := cleanupCtx.Err()
		cancelCleanup()
		if cleanupErr != nil {
			wrapped := fmt.Errorf(
				"%s kill-server: %w: %s",
				cleanupBinary,
				errors.Join(cleanupErr, contextErr),
				strings.TrimSpace(string(output)),
			)
			if ClassifyCommandError(wrapped).Kind != CommandErrorNoServer {
				fmt.Fprintf(os.Stderr, "stop isolated tmux server: %v\n", wrapped)
				code = 1
			}
		}
	}
	if err := os.RemoveAll(tmuxRoot); err != nil {
		fmt.Fprintf(os.Stderr, "remove isolated tmux root %s: %v\n", tmuxRoot, err)
		code = 1
	}
	os.Exit(code)
}

func tmuxTestTempBase() string {
	if runtime.GOOS == "windows" {
		return os.TempDir()
	}
	return "/tmp"
}

func isolatedTmuxTestEnvironment(root string) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "TMUX", "TMUX_PANE", "TMUX_TMPDIR":
			continue
		default:
			env = append(env, entry)
		}
	}
	return append(env, "TMUX=", "TMUX_PANE=", "TMUX_TMPDIR="+root)
}

func TestPaneCommandIsShell(t *testing.T) {
	for _, test := range []struct {
		command string
		want    bool
	}{
		{command: "zsh", want: true},
		{command: "/bin/bash", want: true},
		{command: "  /usr/local/bin/fish  ", want: true},
		{command: "/bin/ash", want: true},
		{command: "mksh", want: true},
		{command: "yash", want: true},
		{command: "xonsh", want: true},
		{command: "elvish", want: true},
		{command: "pwsh.exe", want: true},
		{command: "grok"},
		{command: "/opt/bin/grok"},
		{command: ""},
	} {
		if got := PaneCommandIsShell(test.command); got != test.want {
			t.Fatalf("PaneCommandIsShell(%q) = %v, want %v", test.command, got, test.want)
		}
	}
}

func TestValidatePaneLaunchBaseline(t *testing.T) {
	tests := []struct {
		name    string
		pane    Pane
		wantErr bool
	}{
		{name: "empty observation", pane: Pane{ID: "%1"}},
		{name: "shell", pane: Pane{ID: "%2", Command: "zsh"}},
		{name: "shell path", pane: Pane{ID: "%3", Command: "/bin/bash"}},
		{name: "occupied utility", pane: Pane{ID: "%4", Command: "sleep"}, wantErr: true},
		{name: "occupied agent", pane: Pane{ID: "%5", Command: "/opt/bin/grok"}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePaneLaunchBaseline(test.pane)
			if !test.wantErr {
				if err != nil {
					t.Fatalf("ValidatePaneLaunchBaseline(%+v) error = %v", test.pane, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidatePaneLaunchBaseline(%+v) error = nil, want occupied-pane rejection", test.pane)
			}
			for _, want := range []string{test.pane.ID, strings.TrimSpace(test.pane.Command), "pre-launch", "non-shell", "idle shell"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("ValidatePaneLaunchBaseline(%+v) error = %q, want %q", test.pane, err, want)
				}
			}
		})
	}
}

func TestWaitForPaneProcessStartRequiresStableNonShellObservation(t *testing.T) {
	observations := [][]Pane{
		{{ID: "%7", Command: "zsh"}},
		{{ID: "%7", Command: "/opt/bin/grok"}},
		{{ID: "%7", Command: "grok"}},
	}
	calls := 0
	pane, err := waitForPaneProcessStartContext(
		t.Context(), "proj", "%7", 100*time.Millisecond, time.Millisecond, 2,
		func(context.Context, string) ([]Pane, error) {
			index := calls
			calls++
			if index >= len(observations) {
				index = len(observations) - 1
			}
			return observations[index], nil
		},
	)
	if err != nil {
		t.Fatalf("waitForPaneProcessStartContext returned error: %v", err)
	}
	if pane.ID != "%7" || calls != 3 {
		t.Fatalf("pane=%+v calls=%d, want %%7 after three observations", pane, calls)
	}
}

func TestWaitForPaneProcessStartRejectsImmediatelyExitingProcess(t *testing.T) {
	observations := [][]Pane{
		{{ID: "%7", Command: "ash"}},
		{{ID: "%7", Command: "grok"}},
		{{ID: "%7", Command: "ash"}},
	}
	calls := 0
	_, err := waitForPaneProcessStartContext(
		t.Context(), "proj", "%7", 10*time.Millisecond, time.Millisecond, 2,
		func(context.Context, string) ([]Pane, error) {
			index := calls
			calls++
			if index >= len(observations) {
				index = len(observations) - 1
			}
			return observations[index], nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), `current command "ash"`) {
		t.Fatalf("error = %v, want stable-process timeout reporting ash", err)
	}
	if calls < 3 {
		t.Fatalf("pane observations=%d, want at least three", calls)
	}
}

func TestWaitForPaneProcessStartBoundsBlockingObservation(t *testing.T) {
	started := time.Now()
	_, err := waitForPaneProcessStartContext(
		t.Context(), "proj", "%7", 20*time.Millisecond, time.Millisecond, 2,
		func(ctx context.Context, _ string) ([]Pane, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("error = %v, want bounded list-panes deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("blocking pane observation took %s, want a bounded timeout", elapsed)
	}
}

func TestWaitForPaneProcessStartReportsObservationFailureAndCancellation(t *testing.T) {
	observationErr := errors.New("tmux socket unavailable")
	_, err := waitForPaneProcessStartContext(
		t.Context(), "proj", "%7", 5*time.Millisecond, time.Millisecond, 2,
		func(context.Context, string) ([]Pane, error) { return nil, observationErr },
	)
	if err == nil || !strings.Contains(err.Error(), observationErr.Error()) {
		t.Fatalf("error = %v, want observation failure", err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = waitForPaneProcessStartContext(
		canceled, "proj", "%7", time.Second, time.Millisecond, 2,
		func(context.Context, string) ([]Pane, error) { return []Pane{{ID: "%7", Command: "zsh"}}, nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context.Canceled", err)
	}
}

func TestClassifySessionExistsResultUsesExactExitStatus(t *testing.T) {
	exitOne := commandExitError(t, 1)
	exitTwo := commandExitError(t, 2)
	exit255 := commandExitError(t, 255)
	wrappedExitOne := fmt.Errorf("tmux has-session -t absent: %w: can't find session: absent", exitOne)
	wrappedExitTwo := fmt.Errorf("tmux has-session -t broken: %w: can't find session: broken", exitTwo)
	noServer := fmt.Errorf("tmux has-session -t first: %w: no server running on /tmp/tmux-1000/default", exitOne)
	noSocket := fmt.Errorf("tmux has-session -t first: %w: error connecting to /tmp/tmux-1000/default (No such file or directory)", exitOne)
	permissionSocket := fmt.Errorf("tmux has-session -t private: %w: error connecting to /tmp/tmux-1000/default (Permission denied)", exitOne)
	staleSocket := fmt.Errorf("tmux has-session -t stale: %w: error connecting to /tmp/tmux-1000/default (Connection refused)", exitOne)
	remoteFailure := fmt.Errorf("ssh -- host tmux has-session -t absent: %w: can't find session: absent", exit255)
	binaryMissing := &exec.Error{Name: "missing-tmux", Err: exec.ErrNotFound}
	permissionDenied := fmt.Errorf("tmux: %w", os.ErrPermission)
	unknown := errors.New("unexpected transport failure")

	for _, test := range []struct {
		name       string
		err        error
		wantExists bool
		wantErr    bool
	}{
		{name: "exists", wantExists: true},
		{name: "bare exit one is ambiguous", err: exitOne, wantErr: true},
		{name: "wrapped has-session exit one", err: wrappedExitOne},
		{name: "first session without server", err: noServer},
		{name: "first session without socket", err: noSocket},
		{name: "socket permission denied", err: permissionSocket, wantErr: true},
		{name: "stale socket connection refused", err: staleSocket, wantErr: true},
		{name: "exit two", err: exitTwo, wantErr: true},
		{name: "wrapped exit two", err: wrappedExitTwo, wantErr: true},
		{name: "remote exit 255", err: remoteFailure, wantErr: true},
		{name: "text-only named session absent", err: errors.New("can't find session: absent"), wantErr: true},
		{name: "text-only no server", err: errors.New("no server running on /tmp/tmux.sock"), wantErr: true},
		{name: "binary unavailable", err: binaryMissing, wantErr: true},
		{name: "permission denied", err: permissionDenied, wantErr: true},
		{name: "canceled", err: context.Canceled, wantErr: true},
		{name: "deadline", err: context.DeadlineExceeded, wantErr: true},
		{name: "unknown", err: unknown, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			exists, err := classifySessionExistsResult(test.err)
			if exists != test.wantExists || (err != nil) != test.wantErr {
				t.Fatalf("classifySessionExistsResult(%v) = (%v, %v), want exists=%v error=%v", test.err, exists, err, test.wantExists, test.wantErr)
			}
			if test.wantErr && err != test.err {
				t.Fatalf("infrastructure error identity changed: got %v want %v", err, test.err)
			}
		})
	}
}

func TestSendBufferCancellationRunsBoundedCleanupWithoutEnter(t *testing.T) {
	if os.Getenv("NTM_TEST_BUFFER_CLEANUP_HELPER") == "1" {
		root := os.Getenv("NTM_TEST_BUFFER_CLEANUP_ROOT")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- NewClient("").SendBufferWithDelayContext(ctx, "%1", "first\nsecond", true, time.Hour)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(root, "paste-started")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("fake tmux never entered paste-buffer")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("buffer send error = %v, want context.Canceled", err)
		}
		logData, err := os.ReadFile(filepath.Join(root, "tmux.log"))
		if err != nil {
			t.Fatalf("read fake tmux log: %v", err)
		}
		logText := string(logData)
		if !strings.Contains(logText, "load-buffer") || !strings.Contains(logText, "paste-buffer") ||
			!strings.Contains(logText, "delete-buffer") || strings.Contains(logText, "send-keys") {
			t.Fatalf("fake tmux calls = %q, want load/paste/delete and no Enter", logText)
		}
		return
	}

	root := t.TempDir()
	scriptPath := filepath.Join(root, "tmux")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$1" >> "$NTM_TEST_BUFFER_CLEANUP_ROOT/tmux.log"
case "$1" in
  load-buffer)
    cat >/dev/null
    ;;
  paste-buffer)
    : > "$NTM_TEST_BUFFER_CLEANUP_ROOT/paste-started"
    exec /bin/sleep 30
    ;;
  delete-buffer)
    : > "$NTM_TEST_BUFFER_CLEANUP_ROOT/delete-called"
    ;;
  send-keys)
    : > "$NTM_TEST_BUFFER_CLEANUP_ROOT/enter-called"
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSendBufferCancellationRunsBoundedCleanupWithoutEnter$")
	cmd.Env = append(os.Environ(),
		"NTM_TEST_BUFFER_CLEANUP_HELPER=1",
		"NTM_TEST_BUFFER_CLEANUP_ROOT="+root,
		"NTM_TEST_TMUX_ENV_OWNED=1",
		"NTM_TMUX_BINARY="+scriptPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("buffer cleanup helper failed: %v output=%s", err, output)
	}
}

// createTestSession creates a unique test session with cleanup
func createTestSession(t *testing.T) string {
	t.Helper()
	if !IsInstalled() {
		t.Skip("tmux not installed")
	}
	acquireGlobalTmuxTestLock(t)
	name := fmt.Sprintf("ntm_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = KillSession(name) // ignore error on cleanup
	})
	if err := CreateSession(name, os.TempDir()); err != nil {
		// Skip instead of fail when tmux server has issues (common on CI)
		t.Skipf("failed to create test session: %v", err)
	}
	// Small delay to let tmux settle
	time.Sleep(100 * time.Millisecond)
	return name
}

// skipIfNoTmux skips the test if tmux is not installed
func skipIfNoTmux(t *testing.T) {
	t.Helper()
	if !IsInstalled() {
		t.Skip("tmux not installed, skipping test")
	}
}

func TestValidateSessionName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"myproject", false},
		{"my-project", false},
		{"my_project", false},
		{"MyProject123", false},
		{"", true},            // empty
		{"my.project", true},  // dot rejected
		{"my project", true},  // space rejected
		{"my:project", true},  // contains :
		{"my/project", true},  // contains /
		{"my\\project", true}, // contains \
		{"my;project", true},  // semicolon rejected
		{"my&project", true},  // ampersand rejected
		{"my$project", true},  // dollar rejected
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSessionName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSessionName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestCreateSessionRejectsInvalidNameBeforeInvokingTmux(t *testing.T) {
	client := NewClient("")

	err := client.CreateSessionWithHistoryLimit("bad/session", t.TempDir(), 0)
	if err == nil {
		t.Fatal("CreateSessionWithHistoryLimit() error = nil, want invalid session name error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("CreateSessionWithHistoryLimit() error = %v, want invalid session name", err)
	}
}

func TestSessionCreationAndSplitHonorPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient("")

	if err := client.CreateSessionWithHistoryLimitContext(ctx, "cancel_test", t.TempDir(), DefaultHistoryLimit); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateSessionWithHistoryLimitContext() error = %v, want context.Canceled", err)
	}
	if _, err := client.SplitWindowContext(ctx, "cancel_test", t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("SplitWindowContext() error = %v, want context.Canceled", err)
	}
	if err := client.ApplyTiledLayoutContext(ctx, "cancel_test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ApplyTiledLayoutContext() error = %v, want context.Canceled", err)
	}
	if err := client.KillPaneContext(ctx, "%999"); !errors.Is(err, context.Canceled) {
		t.Fatalf("KillPaneContext() error = %v, want context.Canceled", err)
	}
	if _, err := client.CapturePaneVisibleContext(ctx, "%999"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CapturePaneVisibleContext() error = %v, want context.Canceled", err)
	}
}

func TestSessionCreationAndSplitRejectNilContext(t *testing.T) {
	client := NewClient("")

	if err := client.CreateSessionWithHistoryLimitContext(nil, "nil_context_test", t.TempDir(), DefaultHistoryLimit); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("CreateSessionWithHistoryLimitContext(nil) error = %v, want required-context error", err)
	}
	if _, err := client.SplitWindowContext(nil, "nil_context_test", t.TempDir()); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("SplitWindowContext(nil) error = %v, want required-context error", err)
	}
	if err := client.ApplyTiledLayoutContext(nil, "nil_context_test"); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("ApplyTiledLayoutContext(nil) error = %v, want required-context error", err)
	}
	if err := client.KillPaneContext(nil, "%999"); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("KillPaneContext(nil) error = %v, want required-context error", err)
	}
}

func TestApplyTiledLayoutContextFailureHelper(t *testing.T) {
	failCommand := os.Getenv("NTM_LAYOUT_FAIL_COMMAND")
	if failCommand == "" {
		return
	}

	err := NewClient("").ApplyTiledLayoutContext(context.Background(), "layout-test")
	if err == nil {
		t.Fatalf("ApplyTiledLayoutContext() error = nil, want %s failure", failCommand)
	}
	if !strings.Contains(err.Error(), failCommand) || !strings.Contains(err.Error(), "layout-test:0") {
		t.Fatalf("ApplyTiledLayoutContext() error = %v, want command %q and window target", err, failCommand)
	}
}

func TestApplyTiledLayoutContextReturnsActuationFailures(t *testing.T) {
	root := t.TempDir()
	tmuxPath := filepath.Join(root, "tmux")
	script := `#!/bin/sh
case "${1:-}" in
  list-windows)
    printf '0\n'
    ;;
  display-message)
    if [ "${NTM_LAYOUT_FAIL_COMMAND:-}" = "display-message" ]; then exit 71; fi
    if [ "${NTM_LAYOUT_FAIL_COMMAND:-}" = "resize-pane" ]; then printf '1\n'; else printf '0\n'; fi
    ;;
  resize-pane)
    if [ "${NTM_LAYOUT_FAIL_COMMAND:-}" = "resize-pane" ]; then exit 72; fi
    ;;
  select-layout)
    if [ "${NTM_LAYOUT_FAIL_COMMAND:-}" = "select-layout" ]; then exit 73; fi
    ;;
  *)
    exit 64
    ;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write layout tmux: %v", err)
	}

	for _, failCommand := range []string{"display-message", "resize-pane", "select-layout"} {
		t.Run(failCommand, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestApplyTiledLayoutContextFailureHelper$")
			cmd.Env = append(os.Environ(),
				"NTM_TEST_TMUX_ENV_OWNED=1",
				"NTM_TMUX_BINARY="+tmuxPath,
				"NTM_LAYOUT_FAIL_COMMAND="+failCommand,
			)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("layout failure helper timed out: %v output=%s", ctx.Err(), output)
			}
			if err != nil {
				t.Fatalf("layout failure helper failed: %v output=%s", err, output)
			}
		})
	}
}

func TestSessionPostCreationFailureHelper(t *testing.T) {
	mutation := os.Getenv("NTM_SESSION_MUTATION_FAILURE")
	if mutation == "" {
		return
	}

	client := NewClient("")
	switch mutation {
	case "history-limit":
		err := client.CreateSessionWithHistoryLimitContext(context.Background(), "created-session", t.TempDir(), 50000)
		if err == nil || !strings.Contains(err.Error(), "created-session") || !strings.Contains(err.Error(), "setting history limit failed") {
			t.Fatalf("CreateSessionWithHistoryLimitContext() error = %v, want truthful post-create failure", err)
		}
	case "split-layout":
		paneID, err := client.SplitWindowContext(context.Background(), "created-session", t.TempDir())
		if paneID != "%42" || err == nil || !strings.Contains(err.Error(), paneID) || !strings.Contains(err.Error(), "applying tiled layout failed") {
			t.Fatalf("SplitWindowContext() = (%q, %v), want created pane and truthful layout failure", paneID, err)
		}
	default:
		t.Fatalf("unknown mutation failure %q", mutation)
	}
}

func TestSessionCreationAndSplitReturnPostCreationFailures(t *testing.T) {
	root := t.TempDir()
	tmuxPath := filepath.Join(root, "tmux")
	script := `#!/bin/sh
case "${1:-}" in
  new-session)
	exit 0
	;;
  set-option)
	exit 81
	;;
  list-windows)
	printf '0\n'
	;;
  split-window)
	printf '%%42\n'
	;;
  select-layout)
	exit 82
	;;
  *)
	exit 64
	;;
esac
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write post-creation tmux: %v", err)
	}

	for _, mutation := range []string{"history-limit", "split-layout"} {
		t.Run(mutation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionPostCreationFailureHelper$")
			cmd.Env = append(os.Environ(),
				"NTM_TEST_TMUX_ENV_OWNED=1",
				"NTM_TMUX_BINARY="+tmuxPath,
				"NTM_SESSION_MUTATION_FAILURE="+mutation,
			)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("post-creation helper timed out: %v output=%s", ctx.Err(), output)
			}
			if err != nil {
				t.Fatalf("post-creation helper failed: %v output=%s", err, output)
			}
		})
	}
}

func TestAgentTypeFromTitle(t *testing.T) {
	tests := []struct {
		title    string
		expected AgentType
	}{
		{"myproject__cc_1", AgentClaude},
		{"myproject__cod_1", AgentCodex},
		{"myproject__gmi_1", AgentGemini},
		{"myproject", AgentUser},
		{"zsh", AgentUser},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			// Create a pane and check type detection logic
			pane := Pane{Title: tt.title, Type: AgentUser}

			// Apply same logic as GetPanes
			if contains(pane.Title, "__cc") {
				pane.Type = AgentClaude
			} else if contains(pane.Title, "__cod") {
				pane.Type = AgentCodex
			} else if contains(pane.Title, "__gmi") {
				pane.Type = AgentGemini
			}

			if pane.Type != tt.expected {
				t.Errorf("Expected type %v for title %q, got %v", tt.expected, tt.title, pane.Type)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestInTmux(t *testing.T) {
	// This will be false in test environment
	// Just verify the function doesn't panic
	_ = InTmux()
}

func TestIsInstalled(t *testing.T) {
	// This checks if tmux is installed on the system
	// Just verify the function doesn't panic
	_ = IsInstalled()
}

func TestSanitizePaneCommand(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple command", "claude --model opus", false},
		{"tabs allowed", "codex\t--dangerously-bypass-approvals-and-sandbox", false},
		{"newline rejected", "echo hi\necho bye", true},
		{"carriage return rejected", "echo hi\recho bye", true},
		{"escape rejected", "echo \x1b[31mred\x1b[0m", true},
		{"null byte rejected", "echo hi\x00", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SanitizePaneCommand(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SanitizePaneCommand(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// ============== Session Management Tests ==============

func TestCreateSession(t *testing.T) {
	skipIfNoTmux(t)
	session := createTestSession(t)

	// Verify session exists
	if !SessionExists(session) {
		t.Errorf("session %s should exist after creation", session)
	}
}

func TestPrivateServerSurvivesEmptySessionTransition(t *testing.T) {
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	first := fmt.Sprintf("ntm_test_empty_transition_first_%d", time.Now().UnixNano())
	second := fmt.Sprintf("ntm_test_empty_transition_second_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = KillSession(first)
		_ = KillSession(second)
	})
	if err := CreateSession(first, t.TempDir()); err != nil {
		t.Fatalf("create first isolated session: %v", err)
	}
	if err := KillSession(first); err != nil {
		t.Fatalf("kill sole isolated session: %v", err)
	}

	exitEmpty, err := DefaultClient.Run("show-options", "-sv", "exit-empty")
	if err != nil {
		t.Fatalf("query isolated server after its last session exited: %v", err)
	}
	if got := strings.TrimSpace(exitEmpty); got != "off" {
		t.Fatalf("isolated server exit-empty = %q, want off", got)
	}
	if err := CreateSession(second, t.TempDir()); err != nil {
		t.Fatalf("create session immediately after empty transition: %v", err)
	}
}

func TestCreateSessionWithDir(t *testing.T) {
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	// Create temp directory
	tmpDir := t.TempDir()

	name := fmt.Sprintf("ntm_test_dir_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = KillSession(name) })

	if err := CreateSession(name, tmpDir); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify session was created
	if !SessionExists(name) {
		t.Errorf("session %s should exist", name)
	}
}

func TestSessionExistsNonExistent(t *testing.T) {
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	nonExistent := "ntm_definitely_nonexistent_12345"
	if SessionExists(nonExistent) {
		t.Errorf("session %s should not exist", nonExistent)
	}
}

func TestKillSession(t *testing.T) {
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	name := fmt.Sprintf("ntm_test_kill_%d", time.Now().UnixNano())

	if err := CreateSession(name, os.TempDir()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if !SessionExists(name) {
		t.Fatalf("session %s should exist before kill", name)
	}

	if err := KillSession(name); err != nil {
		t.Errorf("KillSession failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if SessionExists(name) {
		t.Errorf("session %s should not exist after kill", name)
	}
}

func TestListSessions(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}

	found := false
	for _, s := range sessions {
		if s.Name == session {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("session %s not found in ListSessions result", session)
	}
}

func TestGetAllPanes_NoServerReturnsEmpty(t *testing.T) {
	skipIfNoTmux(t)

	// Point tmux at an empty socket directory so no server exists. Also
	// neutralize TMUX so a CI/agent shell that already lives inside a tmux
	// server cannot leak its panes into list-panes -a (bd-wt9d4).
	tmuxTmpDir, err := os.MkdirTemp(tmuxTestTempBase(), "ntm-tmux-test-*")
	if err != nil {
		t.Fatalf("create short tmux socket root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(tmuxTmpDir); err != nil {
			t.Errorf("remove short tmux socket root %q: %v", tmuxTmpDir, err)
		}
	})
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)
	t.Setenv("TMUX", "")

	panes, err := GetAllPanes()
	if err != nil {
		t.Fatalf("GetAllPanes error: %v", err)
	}
	if panes == nil {
		t.Fatal("expected non-nil panes map")
	}
	if len(panes) != 0 {
		t.Fatalf("expected no panes, got %d", len(panes))
	}
}

func TestGetSession(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	s, err := GetSession(session)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if s.Name != session {
		t.Errorf("GetSession returned wrong name: got %s, want %s", s.Name, session)
	}

	if s.Windows < 1 {
		t.Errorf("GetSession should show at least 1 window, got %d", s.Windows)
	}
}

func TestGetSessionNonExistent(t *testing.T) {
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	_, err := GetSession("ntm_nonexistent_session")
	if err == nil {
		t.Error("GetSession should fail for non-existent session")
	}
}

// ============== Pane Operations Tests ==============

func TestGetPanes(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	if len(panes) < 1 {
		t.Errorf("GetPanes should return at least 1 pane, got %d", len(panes))
	}

	// Verify pane has expected fields
	pane := panes[0]
	if pane.ID == "" {
		t.Error("pane ID should not be empty")
	}
	if pane.Width == 0 || pane.Height == 0 {
		t.Errorf("pane dimensions should be positive: %dx%d", pane.Width, pane.Height)
	}
}

func TestSplitWindow(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Get initial pane count
	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}
	initialCount := len(panes)

	// Split window
	paneID, err := SplitWindow(session, os.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if paneID == "" {
		t.Error("SplitWindow should return pane ID")
	}

	// Verify pane count increased
	panes, err = GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	if len(panes) != initialCount+1 {
		t.Errorf("expected %d panes after split, got %d", initialCount+1, len(panes))
	}
}

func TestSplitWindowMultiple(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Create 4 more panes (5 total)
	for i := 0; i < 4; i++ {
		_, err := SplitWindow(session, os.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow %d failed: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	if len(panes) != 5 {
		t.Errorf("expected 5 panes, got %d", len(panes))
	}
}

func TestSetPaneTitle(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Get the first pane
	panes, err := GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	paneID := panes[0].ID
	newTitle := "test_pane_title"

	if err := SetPaneTitle(paneID, newTitle); err != nil {
		t.Fatalf("SetPaneTitle failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify title changed
	panes, err = GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}

	found := false
	for _, p := range panes {
		if p.ID == paneID && p.Title == newTitle {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("pane title should be %q", newTitle)
	}
}

// ============== Key Sending Tests ==============

func TestSendKeys(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Send some text without enter
	text := "hello world"
	panes, _ := GetPanes(session)
	target := panes[0].ID

	if err := SendKeys(target, text, false); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Capture output to verify (text should be in buffer)
	output, err := CapturePaneOutput(target, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Normalize by removing all whitespace to handle terminal line wrapping on
	// narrow CI terminals (e.g., "hello wor\nld" should still match "hello world")
	removeAllWhitespace := func(s string) string {
		var result []rune
		for _, r := range s {
			if !unicode.IsSpace(r) {
				result = append(result, r)
			}
		}
		return string(result)
	}
	normalizedOutput := removeAllWhitespace(output)
	normalizedText := removeAllWhitespace(text)
	if !strings.Contains(normalizedOutput, normalizedText) {
		t.Logf("output: %q", output)
		t.Logf("normalized: %q", normalizedOutput)
		t.Errorf("output should contain %q (normalized: %q)", text, normalizedText)
	}
}

func TestSendKeysWithEnter(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Send echo command with enter
	if err := SendKeys(target, "echo TESTMARKER123", true); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // wait for command to execute

	output, err := CapturePaneOutput(target, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	if !strings.Contains(output, "TESTMARKER123") {
		t.Logf("output: %q", output)
		t.Errorf("output should contain TESTMARKER123")
	}
}

func TestSendInterrupt(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Just verify interrupt doesn't error
	if err := SendInterrupt(target); err != nil {
		t.Errorf("SendInterrupt failed: %v", err)
	}
}

func TestSendKeysMultiByteChunking(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Create a payload larger than 4096 bytes with multi-byte characters.
	// '€' is 3 bytes (0xE2 0x82 0xAC).
	const numChars = 2000
	payload := strings.Repeat("€", numChars)

	// Use 'cat' to echo back input, avoiding shell line-editor limits (readline/zle)
	// which might choke on 6000+ byte lines or drop characters when flooded.
	if err := SendKeys(target, "cat", true); err != nil {
		t.Fatalf("Failed to start cat: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Send the payload
	if err := SendKeys(target, payload, false); err != nil {
		t.Fatalf("SendKeys failed: %v", err)
	}
	// Send EOF to cat
	if err := SendKeys(target, "", true); err != nil { // Enter to ensure newline
		t.Fatalf("Failed to send newline: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := SendInterrupt(target); err != nil {
		t.Fatalf("Failed to interrupt cat: %v", err)
	}

	// Capture output
	output, err := CapturePaneOutput(target, 500) // Increase capture lines
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Analysis:
	// If the fix works, we should see very few UTF-8 replacement characters ().
	// If the fix failed, naive splitting would break the multi-byte char '€'
	// resulting in invalid UTF-8 sequences which are typically rendered as .
	// Allow a small tolerance (<=3) for terminal timing issues on CI.
	replacementCount := strings.Count(output, "\ufffd")
	if replacementCount > 3 {
		t.Errorf("Found %d UTF-8 replacement characters (), confirming corruption.", replacementCount)
	} else if replacementCount > 0 {
		t.Logf("Found %d UTF-8 replacement characters (within tolerance)", replacementCount)
	}

	// Due to shell/tmux buffer limits/rate-limiting, we might not get the full 6000 chars echoed back
	// in a short test window. But we should get a significant amount (at least the first chunk).
	// And crucially, it should be valid UTF-8 (no replacement chars).
	if len(output) < 100 {
		t.Errorf("Captured output too short: %d bytes", len(output))
	}

	// Verify we see our payload characters
	if !strings.Contains(output, "€€€") {
		t.Error("Output does not contain payload characters")
	}
}

// ============== Output Capture Tests ==============

func TestCapturePaneOutput(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	target := panes[0].ID

	output, err := CapturePaneOutput(target, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Output should be a string (possibly empty for new pane)
	if output == "" {
		// This is fine for a new pane
		t.Log("captured empty output (expected for new pane)")
	}
}

func TestCapturePaneOutputWithContent(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Generate some output
	SendKeys(target, "echo LINE1; echo LINE2; echo LINE3", true)
	time.Sleep(300 * time.Millisecond)

	output, err := CapturePaneOutput(target, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	// Should contain our echo output
	if !strings.Contains(output, "LINE1") {
		t.Logf("output: %q", output)
		t.Error("output should contain LINE1")
	}
}

// ============== Layout Tests ==============

func TestApplyTiledLayout(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Create multiple panes
	for i := 0; i < 3; i++ {
		_, err := SplitWindow(session, os.TempDir())
		if err != nil {
			t.Fatalf("SplitWindow failed: %v", err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	// Apply tiled layout
	if err := ApplyTiledLayout(session); err != nil {
		t.Errorf("ApplyTiledLayout failed: %v", err)
	}
}

func TestZoomPane(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Create another pane so we can zoom
	_, err := SplitWindow(session, os.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Get the first pane index
	panes, err := GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("Failed to get panes: %v", err)
	}
	firstPaneIndex := panes[0].Index

	// Zoom first pane
	if err := ZoomPane(session, firstPaneIndex); err != nil {
		t.Errorf("ZoomPane failed: %v", err)
	}
}

// ============== Helper Functions Tests ==============

func TestGetFirstWindow(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	winIdx, err := GetFirstWindow(session)
	if err != nil {
		t.Fatalf("GetFirstWindow failed: %v", err)
	}

	// Window index should be 0 or 1 depending on tmux config
	if winIdx < 0 || winIdx > 1 {
		t.Errorf("unexpected first window index: %d", winIdx)
	}
}

func TestGetDefaultPaneIndex(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	paneIdx, err := GetDefaultPaneIndex(session)
	if err != nil {
		t.Fatalf("GetDefaultPaneIndex failed: %v", err)
	}

	// Pane index should be 0 or 1 depending on tmux config
	if paneIdx < 0 || paneIdx > 1 {
		t.Errorf("unexpected default pane index: %d", paneIdx)
	}
}

func TestGetCurrentSession(t *testing.T) {
	acquireGlobalTmuxTestLock(t)

	// This will return empty string when not in tmux
	session := GetCurrentSession()
	t.Logf("GetCurrentSession returned: %q", session)
}

func TestGetCurrentSessionContextRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := DefaultClient.GetCurrentSessionContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetCurrentSessionContext() error = %v, want context.Canceled", err)
	}
}

// ============== Additional Tests for Coverage ==============

func TestEnsureInstalled(t *testing.T) {
	err := EnsureInstalled()
	if IsInstalled() && err != nil {
		t.Errorf("EnsureInstalled should not error when tmux is installed: %v", err)
	}
	if !IsInstalled() && err == nil {
		t.Error("EnsureInstalled should error when tmux is not installed")
	}
}

func TestGetPaneActivity(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate some activity
	SendKeys(paneID, "echo activity", true)
	time.Sleep(300 * time.Millisecond)

	activity, err := GetPaneActivity(paneID)
	if err != nil {
		// Some tmux versions may not support this - skip test
		t.Skipf("GetPaneActivity not supported: %v", err)
	}

	// Activity time should be recent (within last minute)
	if time.Since(activity) > time.Minute {
		t.Errorf("pane activity should be recent, got %v ago", time.Since(activity))
	}
}

func TestParsePaneActivityTimestamp(t *testing.T) {
	now := time.Date(2026, 1, 25, 6, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		raw       string
		want      time.Time
		wantError bool
	}{
		{name: "empty", raw: "", want: now},
		{name: "whitespace", raw: "   ", want: now},
		{name: "zero", raw: "0", want: now},
		{name: "negative", raw: "-1", want: now},
		{name: "valid", raw: "123", want: time.Unix(123, 0)},
		{name: "invalid", raw: "abc", wantError: true},
	}

	for _, tt := range tests {
		got, err := parsePaneActivityTimestamp(tt.raw, now)
		if tt.wantError {
			if err == nil {
				t.Fatalf("%s: expected error for %q", tt.name, tt.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error for %q: %v", tt.name, tt.raw, err)
		}
		if !got.Equal(tt.want) {
			t.Fatalf("%s: got %v want %v", tt.name, got, tt.want)
		}
	}
}

func TestGetPanesWithActivity(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	// Create additional pane
	_, err := SplitWindow(session, os.TempDir())
	if err != nil {
		t.Fatalf("SplitWindow failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Generate activity
	panes, _ := GetPanes(session)
	for _, p := range panes {
		SendKeys(p.ID, "echo test", true)
	}
	time.Sleep(300 * time.Millisecond)

	// Get panes with activity
	panesWithActivity, err := GetPanesWithActivity(session)
	if err != nil {
		t.Fatalf("GetPanesWithActivity failed: %v", err)
	}

	if len(panesWithActivity) != len(panes) {
		t.Errorf("expected %d panes with activity, got %d", len(panes), len(panesWithActivity))
	}

	// Verify each pane has activity info
	for _, p := range panesWithActivity {
		if p.LastActivity.IsZero() {
			t.Errorf("pane %s should have activity timestamp", p.Pane.ID)
		}
	}
}

func TestIsRecentlyActive(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate recent activity
	SendKeys(paneID, "echo recent", true)
	time.Sleep(300 * time.Millisecond)

	// Should be recently active (within 1 minute)
	recent, err := IsRecentlyActive(paneID, time.Minute)
	if err != nil {
		// Some tmux versions may not support this - skip test
		t.Skipf("IsRecentlyActive not supported: %v", err)
	}

	if !recent {
		t.Error("pane should be recently active after generating output")
	}
}

func TestGetPaneLastActivityAge(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)

	panes, _ := GetPanes(session)
	paneID := panes[0].ID

	// Generate activity
	SendKeys(paneID, "echo age test", true)
	time.Sleep(300 * time.Millisecond)

	age, err := GetPaneLastActivityAge(paneID)
	if err != nil {
		// Some tmux versions may not support this - skip test
		t.Skipf("GetPaneLastActivityAge not supported: %v", err)
	}

	// Age should be small (just generated activity)
	if age > 5*time.Second {
		t.Errorf("pane activity age should be < 5s, got %v", age)
	}
}

func TestListSessionsNoServer(t *testing.T) {
	// This just verifies the function handles edge cases
	// When tmux is running, it should return sessions or empty list
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	sessions, err := ListSessions()
	// Should not error even if no sessions exist
	if err != nil {
		t.Logf("ListSessions returned error: %v", err)
	}
	t.Logf("ListSessions returned %d sessions", len(sessions))
}

func TestGetPanesWithBadSession(t *testing.T) {
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	_, err := GetPanes("nonexistent_session_xyz")
	if err == nil {
		t.Error("GetPanes should fail for non-existent session")
	}
}

func TestSplitWindowWithBadSession(t *testing.T) {
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)

	_, err := SplitWindow("nonexistent_session_xyz", os.TempDir())
	if err == nil {
		t.Error("SplitWindow should fail for non-existent session")
	}
}

func TestAgentType_ProfileName(t *testing.T) {
	tests := []struct {
		agentType AgentType
		expected  string
	}{
		{AgentClaude, "Claude"},
		{AgentCodex, "Codex"},
		{AgentGemini, "Gemini"},
		{AgentUser, "User"},
		{AgentType("unknown"), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.agentType), func(t *testing.T) {
			result := tt.agentType.ProfileName()
			if result != tt.expected {
				t.Errorf("ProfileName() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// ============== Binary Path Resolution Tests ==============

func TestBinaryPath(t *testing.T) {
	// BinaryPath should return a non-empty path
	path := BinaryPath()
	if path == "" {
		t.Fatal("BinaryPath() returned empty string")
	}

	// Path should either be a full path or "tmux" as fallback
	if path != "tmux" && !strings.HasPrefix(path, "/") {
		t.Errorf("BinaryPath() should return absolute path or 'tmux', got %q", path)
	}

	// If tmux is installed, the path should be a valid executable
	if IsInstalled() {
		// Path should point to an existing file
		if _, err := os.Stat(path); err != nil && path != "tmux" {
			t.Errorf("BinaryPath() returned non-existent path: %q", path)
		}
	}
}

func TestBinaryPathConsistency(t *testing.T) {
	// BinaryPath should return the same value when called multiple times
	// (it uses sync.Once internally)
	path1 := BinaryPath()
	path2 := BinaryPath()
	path3 := BinaryPath()

	if path1 != path2 || path2 != path3 {
		t.Errorf("BinaryPath() inconsistent: %q, %q, %q", path1, path2, path3)
	}
}

func TestResolveTmuxBinaryPathUsesExplicitOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-tmux")
	t.Setenv("NTM_TMUX_BINARY", "  "+override+"  ")
	if got := resolveTmuxBinaryPath(); got != override {
		t.Fatalf("resolveTmuxBinaryPath() = %q, want override %q", got, override)
	}
}

func TestBinaryPathExplicitOverrideWinsAfterInstalledPathIsCached(t *testing.T) {
	t.Setenv("NTM_TMUX_BINARY", "")
	if baseline := BinaryPath(); baseline == "" {
		t.Fatal("BinaryPath() returned an empty baseline")
	}
	override := filepath.Join(t.TempDir(), "late-custom-tmux")
	t.Setenv("NTM_TMUX_BINARY", "  "+override+"  ")
	if got := BinaryPath(); got != override {
		t.Fatalf("BinaryPath() after cache initialization = %q, want override %q", got, override)
	}
}

func TestBinaryPathPreferredLocations(t *testing.T) {
	// BinaryPath should prefer standard install locations
	path := BinaryPath()

	// If tmux is installed in a standard location, BinaryPath should find it
	preferredPaths := []string{
		"/usr/bin/tmux",
		"/usr/local/bin/tmux",
		"/opt/homebrew/bin/tmux",
	}

	// Check if path matches one of the preferred locations
	isPreferred := path == "tmux" // fallback is also acceptable
	for _, p := range preferredPaths {
		if path == p {
			isPreferred = true
			break
		}
	}

	// If tmux is installed but path is not from preferred locations,
	// it should at least be in PATH
	if IsInstalled() && !isPreferred {
		t.Logf("BinaryPath returned non-standard path: %q (this is fine if tmux is installed elsewhere)", path)
	}
}

// ============== Pure Function Tests ==============

func TestTagsFromTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		title string
		want  []string
	}{
		{"simple tags", "session__cc_1[frontend]", []string{"frontend"}},
		{"multiple tags", "session__cc_1[frontend,api]", []string{"frontend", "api"}},
		{"no tags", "session__cc_1", nil},
		{"empty tags", "session__cc_1[]", nil},
		{"not NTM format", "just_a_title", nil},
		{"with variant and tags", "session__cc_1_opus[backend]", []string{"backend"}},
		{"tags with spaces", "session__cc_1[tag1, tag2]", []string{"tag1", "tag2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tagsFromTitle(tt.title)
			if len(got) != len(tt.want) {
				t.Errorf("tagsFromTitle(%q) = %v, want %v", tt.title, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tagsFromTitle(%q)[%d] = %q, want %q", tt.title, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseAgentFromTitleEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		title       string
		wantType    AgentType
		wantIndex   int
		wantVariant string
		wantTags    []string
	}{
		{
			name:        "basic claude",
			title:       "myproject__cc_1",
			wantType:    AgentType("cc"),
			wantIndex:   1,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "codex with variant",
			title:       "myproject__cod_2_gpt4o",
			wantType:    AgentType("cod"),
			wantIndex:   2,
			wantVariant: "gpt4o",
			wantTags:    nil,
		},
		{
			name:        "gemini with tags",
			title:       "myproject__gmi_3[api,backend]",
			wantType:    AgentType("gmi"),
			wantIndex:   3,
			wantVariant: "",
			wantTags:    []string{"api", "backend"},
		},
		{
			name:        "full format",
			title:       "myproject__cc_1_opus[frontend,ui]",
			wantType:    AgentType("cc"),
			wantIndex:   1,
			wantVariant: "opus",
			wantTags:    []string{"frontend", "ui"},
		},
		{
			name:        "non-NTM format",
			title:       "zsh",
			wantType:    AgentUser,
			wantIndex:   0,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "double underscore no suffix",
			title:       "just__",
			wantType:    AgentUser,
			wantIndex:   0,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "single underscore",
			title:       "project_name",
			wantType:    AgentUser,
			wantIndex:   0,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "high index",
			title:       "session__cc_999",
			wantType:    AgentType("cc"),
			wantIndex:   999,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "custom agent type",
			title:       "session__aider_1",
			wantType:    AgentType("aider"),
			wantIndex:   1,
			wantVariant: "",
			wantTags:    nil,
		},
		{
			name:        "variant with special chars",
			title:       "session__cc_1_opus-4.5",
			wantType:    AgentType("cc"),
			wantIndex:   1,
			wantVariant: "opus-4.5",
			wantTags:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotIndex, gotVariant, gotTags := parseAgentFromTitle(tt.title)
			if gotType != tt.wantType {
				t.Errorf("parseAgentFromTitle(%q) type = %q, want %q", tt.title, gotType, tt.wantType)
			}
			if gotIndex != tt.wantIndex {
				t.Errorf("parseAgentFromTitle(%q) index = %d, want %d", tt.title, gotIndex, tt.wantIndex)
			}
			if gotVariant != tt.wantVariant {
				t.Errorf("parseAgentFromTitle(%q) variant = %q, want %q", tt.title, gotVariant, tt.wantVariant)
			}
			if len(gotTags) != len(tt.wantTags) {
				t.Errorf("parseAgentFromTitle(%q) tags = %v, want %v", tt.title, gotTags, tt.wantTags)
			} else {
				for i := range gotTags {
					if gotTags[i] != tt.wantTags[i] {
						t.Errorf("parseAgentFromTitle(%q) tags[%d] = %q, want %q", tt.title, i, gotTags[i], tt.wantTags[i])
					}
				}
			}
		})
	}
}

func TestDetectAgentFromCommandEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    AgentType
	}{
		// Claude variants
		{"claude bare", "claude", AgentClaude},
		{"claude with args", "claude --model opus", AgentClaude},
		{"claude path", "/usr/bin/claude", AgentClaude},
		{"cc alias", "cc", AgentUser},
		{"cc with args", "cc --resume", AgentUser},

		// Codex variants
		{"codex bare", "codex", AgentCodex},
		{"codex with args", "codex --model gpt4o", AgentCodex},
		{"codex path", "/opt/codex", AgentCodex},
		{"cod alias", "cod", AgentUser},
		{"cod with args", "cod --dangerously-bypass", AgentUser},

		// Gemini variants
		{"gemini bare", "gemini", AgentGemini},
		{"gemini with args", "gemini --model flash", AgentGemini},
		{"gemini path", "/bin/gemini", AgentGemini},
		{"gmi alias", "gmi", AgentUser},
		{"gmi with args", "gmi start", AgentUser},

		// Grok Build requires an exact executable basename because "grok" is
		// common in unrelated executable names and search arguments.
		{"grok bare", "grok", AgentGrok},
		{"grok with args", "grok --always-approve", AgentGrok},
		{"grok path", "/home/user/.local/bin/grok --always-approve", AgentGrok},
		{"grok windows path", `C:\\Tools\\grok.exe --always-approve`, AgentGrok},
		{"ngrok", "ngrok http 8080", AgentUser},
		{"grok exporter", "grok-exporter", AgentUser},
		{"grok search argument", "rg grok", AgentUser},

		// Cursor
		{"cursor bare", "cursor", AgentCursor},
		{"cursor with args", "cursor .", AgentCursor},
		{"cursor path", "/Applications/cursor", AgentCursor},

		// Windsurf
		{"windsurf bare", "windsurf", AgentWindsurf},
		{"windsurf with args", "windsurf --new-window", AgentWindsurf},
		{"windsurf path", "/snap/bin/windsurf", AgentWindsurf},

		// Aider
		{"aider bare", "aider", AgentAider},
		{"aider with args", "aider --watch", AgentAider},
		{"aider path", "/home/user/.local/bin/aider", AgentAider},

		// User fallback
		{"bash", "bash", AgentUser},
		{"zsh", "zsh", AgentUser},
		{"vim", "vim", AgentUser},
		{"empty", "", AgentUser},
		{"random command", "some_random_program", AgentUser},

		// Case insensitivity
		{"CLAUDE uppercase", "CLAUDE", AgentClaude},
		{"Claude mixed", "Claude", AgentClaude},
		{"CODEX uppercase", "CODEX", AgentCodex},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectAgentFromCommand(tt.command)
			if got != tt.want {
				t.Errorf("detectAgentFromCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectAgentFromArgvRequiresGrokExecutable(t *testing.T) {
	positive := [][]string{{"grok", "--always-approve"}, {"/opt/xai/grok", "--model", "current"}}
	for _, argv := range positive {
		if got := detectAgentFromArgv(argv); got != AgentGrok {
			t.Errorf("detectAgentFromArgv(%q) = %q, want %q", argv, got, AgentGrok)
		}
	}
	negative := [][]string{{"ngrok", "http", "8080"}, {"grok-exporter"}, {"rg", "grok"}, {"bash", "-lc", "echo grok"}}
	for _, argv := range negative {
		if got := detectAgentFromArgv(argv); got == AgentGrok {
			t.Errorf("detectAgentFromArgv(%q) falsely detected Grok", argv)
		}
	}
}

// Regression test for acfs#267: when an agent runs under a wrapper
// like Bun, tmux's pane_current_command reports the wrapper, not the
// agent. We need to identify the wrapper as a candidate for deeper
// inspection (process-tree walk) rather than treating it as a plain
// user pane.
func TestIsAgentWrapperCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    bool
	}{
		// tmux `pane_current_command` returns the immediate process
		// basename — bare names like "bun", "node", or absolute paths.
		// It does NOT return argv, so all realistic inputs are short.
		{"bun bare", "bun", true},
		{"bun absolute path", "/home/jerry/.bun/bin/bun", true},
		{"node bare", "node", true},
		{"npx bare", "npx", true},
		{"deno bare", "deno", true},
		{"python bare", "python", true},
		{"python3 bare", "python3", true},

		// Shells — also wrappers when the agent was launched directly
		// inside the pane shell.
		{"sh", "sh", true},
		{"bash", "bash", true},
		{"zsh", "zsh", true},

		// Direct agent / non-wrapper commands.
		{"claude bare", "claude", false},
		{"codex bare", "codex", false},
		{"gemini bare", "gemini", false},
		{"random program", "some_other_tool", false},
		{"empty", "", false},

		// Case insensitivity / whitespace.
		{"BUN uppercase", "BUN", true},
		{"  bun with spaces", "  bun  ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAgentWrapperCommand(tt.command)
			if got != tt.want {
				t.Errorf("isAgentWrapperCommand(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

// Tests the joined-argv detection path inside detectAgentFromProcessTree
// without spawning real processes. The logic that runs at each frame
// is `detectAgentFromCommand(strings.Join(argv, " "))` plus per-arg
// detection — exercise both shapes against the patterns we care
// about for issue acfs#267.
func TestDetectAgentFromCommand_BunCodexArgvShape(t *testing.T) {
	t.Parallel()

	// The exact argv shape from the issue body.
	bunCodexArgv := []string{
		"bun",
		"/home/jerry/.bun/bin/codex",
		"--dangerously-bypass-approvals-and-sandbox",
		"-m", "gpt-5.5-codex",
	}
	if got := detectAgentFromCommand(strings.Join(bunCodexArgv, " ")); got != AgentCodex {
		t.Errorf("joined bun-codex argv: detectAgentFromCommand = %q, want %q", got, AgentCodex)
	}

	// And one of the arg elements alone — `/home/.../codex` should
	// match through the `/codex` suffix branch.
	if got := detectAgentFromCommand("/home/jerry/.bun/bin/codex"); got != AgentCodex {
		t.Errorf("argv[1] /home/.../codex: detectAgentFromCommand = %q, want %q", got, AgentCodex)
	}
}

func TestFormatTagsEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tags []string
		want string
	}{
		{"nil", nil, ""},
		{"empty slice", []string{}, ""},
		{"single tag", []string{"frontend"}, "[frontend]"},
		{"two tags", []string{"api", "backend"}, "[api,backend]"},
		{"three tags", []string{"a", "b", "c"}, "[a,b,c]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTags(tt.tags)
			if got != tt.want {
				t.Errorf("FormatTags(%v) = %q, want %q", tt.tags, got, tt.want)
			}
		})
	}
}

func TestParseTagsEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tagStr string
		want   []string
	}{
		{"empty", "", nil},
		{"single", "frontend", []string{"frontend"}},
		{"multiple", "a,b,c", []string{"a", "b", "c"}},
		{"with spaces", "a, b, c", []string{"a", "b", "c"}},
		{"trailing comma", "a,b,", []string{"a", "b"}},
		{"leading comma", ",a,b", []string{"a", "b"}},
		{"only commas", ",,", nil},
		{"whitespace only entries", "a, ,b", []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTags(tt.tagStr)
			if len(got) != len(tt.want) {
				t.Errorf("parseTags(%q) = %v, want %v", tt.tagStr, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseTags(%q)[%d] = %q, want %q", tt.tagStr, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ============== Pure Function Tests ==============

func TestFormatPaneName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		session   string
		agentType string
		index     int
		variant   string
		want      string
	}{
		{
			name:      "claude agent no variant",
			session:   "myproject",
			agentType: "cc",
			index:     1,
			variant:   "",
			want:      "myproject__cc_1",
		},
		{
			name:      "codex agent no variant",
			session:   "myproject",
			agentType: "cod",
			index:     2,
			variant:   "",
			want:      "myproject__cod_2",
		},
		{
			name:      "gemini agent no variant",
			session:   "backend",
			agentType: "gmi",
			index:     3,
			variant:   "",
			want:      "backend__gmi_3",
		},
		{
			name:      "claude with variant",
			session:   "myproject",
			agentType: "cc",
			index:     1,
			variant:   "opus",
			want:      "myproject__cc_1_opus",
		},
		{
			name:      "codex with variant",
			session:   "frontend",
			agentType: "cod",
			index:     4,
			variant:   "gpt5",
			want:      "frontend__cod_4_gpt5",
		},
		{
			name:      "user pane",
			session:   "test",
			agentType: "user",
			index:     0,
			variant:   "",
			want:      "test__user_0",
		},
		{
			name:      "hyphenated session name",
			session:   "my-cool-project",
			agentType: "cc",
			index:     1,
			variant:   "",
			want:      "my-cool-project__cc_1",
		},
		{
			name:      "underscore session name",
			session:   "my_other_project",
			agentType: "gmi",
			index:     5,
			variant:   "pro",
			want:      "my_other_project__gmi_5_pro",
		},
		{
			name:      "high index",
			session:   "swarm",
			agentType: "cc",
			index:     100,
			variant:   "",
			want:      "swarm__cc_100",
		},
		{
			name:      "zero index",
			session:   "proj",
			agentType: "user",
			index:     0,
			variant:   "",
			want:      "proj__user_0",
		},
		{
			name:      "claude alias canonicalized",
			session:   "proj",
			agentType: "claude",
			index:     2,
			variant:   "",
			want:      "proj__cc_2",
		},
		{
			name:      "codex alias canonicalized",
			session:   "proj",
			agentType: "openai-codex",
			index:     3,
			variant:   "gpt5",
			want:      "proj__cod_3_gpt5",
		},
		{
			name:      "unknown plugin type preserved",
			session:   "proj",
			agentType: "custom-plugin",
			index:     1,
			variant:   "",
			want:      "proj__custom-plugin_1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatPaneName(tt.session, tt.agentType, tt.index, tt.variant)
			if got != tt.want {
				t.Errorf("FormatPaneName(%q, %q, %d, %q) = %q, want %q",
					tt.session, tt.agentType, tt.index, tt.variant, got, tt.want)
			}
		})
	}
}

func TestNeedsBufferSend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType AgentType
		content   string
		want      bool
	}{
		// Gemini cases - uses buffer for newlines
		{
			name:      "gemini single line",
			agentType: AgentGemini,
			content:   "hello world",
			want:      false,
		},
		{
			name:      "gemini with newline",
			agentType: AgentGemini,
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "gemini multiple newlines",
			agentType: AgentGemini,
			content:   "a\nb\nc\nd",
			want:      true,
		},
		{
			name:      "gemini empty string",
			agentType: AgentGemini,
			content:   "",
			want:      false,
		},
		// Codex cases - uses buffer for newlines or large content (>512)
		{
			name:      "codex single line short",
			agentType: AgentCodex,
			content:   "hello world",
			want:      false,
		},
		{
			name:      "codex with newline",
			agentType: AgentCodex,
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "codex long content no newline",
			agentType: AgentCodex,
			content:   strings.Repeat("a", 513),
			want:      true,
		},
		{
			name:      "codex exactly 512 chars",
			agentType: AgentCodex,
			content:   strings.Repeat("b", 512),
			want:      false,
		},
		{
			name:      "codex 511 chars",
			agentType: AgentCodex,
			content:   strings.Repeat("c", 511),
			want:      false,
		},
		{
			name:      "codex empty string",
			agentType: AgentCodex,
			content:   "",
			want:      false,
		},
		{
			name:      "codex alias with newline",
			agentType: AgentType("codex"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "codex alias long content no newline",
			agentType: AgentType("codex"),
			content:   strings.Repeat("a", 513),
			want:      true,
		},
		{
			name:      "codex cli alias with newline",
			agentType: AgentType("codex-cli"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "openai codex alias long content no newline",
			agentType: AgentType("openai-codex"),
			content:   strings.Repeat("a", 513),
			want:      true,
		},
		// Claude cases - uses buffer for multi-line content
		{
			name:      "claude single line",
			agentType: AgentClaude,
			content:   "hello world",
			want:      false,
		},
		{
			name:      "claude with newline",
			agentType: AgentClaude,
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "claude long content",
			agentType: AgentClaude,
			content:   strings.Repeat("x", 1000),
			want:      false,
		},
		// Claude single-line autocomplete-trigger tokens — must buffer (#198)
		{
			name:      "claude single line with filename",
			agentType: AgentClaude,
			content:   "open README.md and summarize",
			want:      true,
		},
		{
			name:      "claude single line with path",
			agentType: AgentClaude,
			content:   "read .orch-dispatch/x.txt then start",
			want:      true,
		},
		{
			name:      "claude single line with leading slash path",
			agentType: AgentClaude,
			content:   "look at src/main.go",
			want:      true,
		},
		{
			name:      "claude single line with at mention",
			agentType: AgentClaude,
			content:   "review @internal/tmux/session.go",
			want:      true,
		},
		{
			name:      "claude single line plain prose no submit risk",
			agentType: AgentClaude,
			content:   "Reply with one word: ALIVE",
			want:      false,
		},
		{
			name:      "claude single line prose with sentence period",
			agentType: AgentClaude,
			content:   "Say hello. Then stop",
			want:      false,
		},
		{
			name:      "claude alias with newline",
			agentType: AgentType("claude"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "claude underscore alias with newline",
			agentType: AgentType("claude_code"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "gemini alias with newline",
			agentType: AgentType("gemini"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "gemini cli alias with newline",
			agentType: AgentType("gemini-cli"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "google gemini alias with newline",
			agentType: AgentType("google-gemini"),
			content:   "line1\nline2",
			want:      true,
		},
		{
			name:      "codex alias trimmed and cased",
			agentType: AgentType(" CodEx "),
			content:   strings.Repeat("z", 513),
			want:      true,
		},
		// User pane - never uses buffer
		{
			name:      "user single line",
			agentType: AgentUser,
			content:   "echo hello",
			want:      false,
		},
		{
			name:      "user with newline",
			agentType: AgentUser,
			content:   "echo line1\necho line2",
			want:      false,
		},
		// Other agent types - never uses buffer
		{
			name:      "cursor single line",
			agentType: AgentCursor,
			content:   "test",
			want:      false,
		},
		{
			name:      "cursor with newline",
			agentType: AgentCursor,
			content:   "a\nb",
			want:      false,
		},
		{
			name:      "windsurf with newline",
			agentType: AgentWindsurf,
			content:   "x\ny",
			want:      false,
		},
		{
			name:      "aider with newline",
			agentType: AgentAider,
			content:   "test\ndata",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsBufferSend(tt.agentType, tt.content)
			if got != tt.want {
				t.Errorf("needsBufferSend(%v, %q) = %v, want %v",
					tt.agentType, truncateForLog(tt.content), got, tt.want)
			}
		})
	}
}

// truncateForLog truncates long strings for readable test output
func truncateForLog(s string) string {
	if len(s) > 50 {
		return s[:47] + "..."
	}
	return s
}

func TestPaneSelectorTopologyHelpers(t *testing.T) {
	panes := []Pane{
		{ID: "%0", WindowIndex: 0, Index: 0},
		{ID: "%1", WindowIndex: 0, Index: 1},
		{ID: "%2", WindowIndex: 1, Index: 0},
		{ID: "%3", WindowIndex: 1, Index: 1},
	}
	if !PanesSpanMultipleWindows(panes) {
		t.Fatal("PanesSpanMultipleWindows returned false for two-window fixture")
	}
	if got := PaneTargetKey(panes[3], true); got != "1.1" {
		t.Fatalf("PaneTargetKey multi-window = %q, want 1.1", got)
	}
	if got := PaneTargetKey(panes[3], false); got != "1" {
		t.Fatalf("PaneTargetKey single-window = %q, want 1", got)
	}

	tests := []struct {
		name        string
		pane        Pane
		selector    string
		multiWindow bool
		want        bool
	}{
		{name: "pane id", pane: panes[3], selector: "%3", multiWindow: true, want: true},
		{name: "window pane", pane: panes[3], selector: "1.1", multiWindow: true, want: true},
		{name: "multi-window bare selects window", pane: panes[2], selector: "1", multiWindow: true, want: true},
		{name: "multi-window bare rejects another window", pane: panes[1], selector: "1", multiWindow: true, want: false},
		{name: "single-window bare selects pane", pane: panes[1], selector: "1", multiWindow: false, want: true},
		{name: "malformed", pane: panes[1], selector: "1.x", multiWindow: true, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PaneMatchesSelector(test.pane, test.selector, test.multiWindow); got != test.want {
				t.Fatalf("PaneMatchesSelector(%+v, %q, %v) = %v, want %v", test.pane, test.selector, test.multiWindow, got, test.want)
			}
		})
	}
}

func TestParsePaneSelectorStrict(t *testing.T) {
	tests := []struct {
		input string
		kind  PaneSelectorKind
		valid bool
	}{
		{input: "0", kind: PaneSelectorPaneIndex, valid: true},
		{input: " 12 ", kind: PaneSelectorPaneIndex, valid: true},
		{input: "2.7", kind: PaneSelectorWindowPane, valid: true},
		{input: "%19", kind: PaneSelectorID, valid: true},
		{input: ""},
		{input: "-1"},
		{input: "+1"},
		{input: "%"},
		{input: "%x"},
		{input: "1."},
		{input: ".1"},
		{input: "1.2.3"},
		{input: "1 .2"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			selector, err := ParsePaneSelector(test.input)
			if !test.valid {
				if err == nil {
					t.Fatalf("ParsePaneSelector(%q) succeeded: %+v", test.input, selector)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePaneSelector(%q) error: %v", test.input, err)
			}
			if selector.Kind != test.kind {
				t.Fatalf("ParsePaneSelector(%q) kind = %v, want %v", test.input, selector.Kind, test.kind)
			}
		})
	}
}

func TestResolvePaneSelectorsCanonicalContract(t *testing.T) {
	panes := []Pane{
		{ID: "%3", WindowIndex: 1, Index: 1},
		{ID: "%0", WindowIndex: 0, Index: 0},
		{ID: "%2", WindowIndex: 1, Index: 0},
		{ID: "%1", WindowIndex: 0, Index: 1},
	}

	selected, err := ResolvePaneSelectors(panes, []string{"0.1", "%1", "1.0"}, false)
	if err != nil {
		t.Fatalf("ResolvePaneSelectors aliases: %v", err)
	}
	if len(selected) != 2 || selected[0].ID != "%1" || selected[1].ID != "%2" {
		t.Fatalf("ResolvePaneSelectors aliases = %+v, want %%1 then %%2", selected)
	}

	window, err := ResolvePaneSelectors(panes, []string{"1"}, false)
	if err != nil {
		t.Fatalf("ResolvePaneSelectors window: %v", err)
	}
	if len(window) != 2 || window[0].ID != "%2" || window[1].ID != "%3" {
		t.Fatalf("ResolvePaneSelectors window = %+v, want %%2 then %%3", window)
	}

	if _, err := ResolvePaneSelectors(panes, []string{"1"}, true); err == nil || !strings.Contains(err.Error(), "matched 2 panes") {
		t.Fatalf("ambiguous singular error = %v", err)
	}
	if _, err := ResolvePaneSelectors(panes, []string{"9.9"}, false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing selector error = %v", err)
	}
	if _, err := ResolvePaneSelectors(panes, []string{"1.x"}, false); err == nil || !strings.Contains(err.Error(), "invalid pane selector") {
		t.Fatalf("invalid selector error = %v", err)
	}
}

func TestResolvePaneSelectorsRealTmuxTopologyMatrix(t *testing.T) {
	session := createTestSession(t)
	directory := t.TempDir()

	initial, err := GetPanes(session)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial panes = %d, err = %v; want one pane", len(initial), err)
	}
	baseWindow := initial[0].WindowIndex
	if _, err := DefaultClient.Run("split-window", "-d", "-t", fmt.Sprintf("%s:%d", session, baseWindow), "-c", directory); err != nil {
		t.Fatalf("split initial window: %v", err)
	}

	singleWindow, err := GetPanes(session)
	if err != nil {
		t.Fatalf("get single-window panes: %v", err)
	}
	if len(singleWindow) != 2 || PanesSpanMultipleWindows(singleWindow) {
		t.Fatalf("single-window topology = %+v, want two panes in one window", singleWindow)
	}
	for i, pane := range SortPanesByTopology(singleWindow) {
		if _, err := DefaultClient.Run("select-pane", "-t", pane.ID, "-T", fmt.Sprintf("%s__cc_%d", session, i+1)); err != nil {
			t.Fatalf("title single-window pane %s: %v", pane.ID, err)
		}
	}
	singleWindow, err = GetPanes(session)
	if err != nil {
		t.Fatalf("refresh single-window panes: %v", err)
	}
	orderedSingle := SortPanesByTopology(singleWindow)
	bare := fmt.Sprintf("%d", orderedSingle[1].Index)
	selected, err := ResolvePaneSelectors(singleWindow, []string{bare, orderedSingle[1].ID}, false)
	if err != nil {
		t.Fatalf("single-window alias resolution: %v", err)
	}
	if len(selected) != 1 || selected[0].ID != orderedSingle[1].ID {
		t.Fatalf("single-window alias resolution = %+v, want %s once", selected, orderedSingle[1].ID)
	}

	if _, err := DefaultClient.Run("new-window", "-d", "-t", session, "-c", directory); err != nil {
		t.Fatalf("create second window: %v", err)
	}
	multiWindow, err := GetPanes(session)
	if err != nil {
		t.Fatalf("get multi-window panes: %v", err)
	}
	secondWindow := -1
	for _, pane := range multiWindow {
		if pane.WindowIndex != baseWindow {
			secondWindow = pane.WindowIndex
			break
		}
	}
	if secondWindow < 0 {
		t.Fatalf("second window not found in %+v", multiWindow)
	}
	if _, err := DefaultClient.Run("split-window", "-d", "-t", fmt.Sprintf("%s:%d", session, secondWindow), "-c", directory); err != nil {
		t.Fatalf("split second window: %v", err)
	}

	multiWindow, err = GetPanes(session)
	if err != nil {
		t.Fatalf("refresh multi-window panes: %v", err)
	}
	orderedMulti := SortPanesByTopology(multiWindow)
	if len(orderedMulti) != 4 || !PanesSpanMultipleWindows(orderedMulti) {
		t.Fatalf("multi-window topology = %+v, want four panes across two windows", orderedMulti)
	}
	for i, pane := range orderedMulti {
		if _, err := DefaultClient.Run("select-pane", "-t", pane.ID, "-T", fmt.Sprintf("%s__cod_%d", session, i+1)); err != nil {
			t.Fatalf("title multi-window pane %s: %v", pane.ID, err)
		}
	}
	multiWindow, err = GetPanes(session)
	if err != nil {
		t.Fatalf("refresh titled multi-window panes: %v", err)
	}
	orderedMulti = SortPanesByTopology(multiWindow)

	exact := orderedMulti[2]
	aliases := []string{exact.Ref().Physical(), exact.ID, exact.Ref().Physical()}
	selected, err = ResolvePaneSelectors(multiWindow, aliases, true)
	if err != nil {
		t.Fatalf("multi-window exact alias resolution: %v", err)
	}
	if len(selected) != 1 || selected[0].ID != exact.ID {
		t.Fatalf("multi-window aliases = %+v, want %s once", selected, exact.ID)
	}

	windowSelector := fmt.Sprintf("%d", secondWindow)
	windowPanes, err := ResolvePaneSelectors(multiWindow, []string{windowSelector}, false)
	if err != nil {
		t.Fatalf("bare multi-window selector: %v", err)
	}
	if len(windowPanes) != 2 {
		t.Fatalf("bare window selector resolved %d panes, want 2: %+v", len(windowPanes), windowPanes)
	}
	if _, err := ResolvePaneSelectors(multiWindow, []string{windowSelector}, true); err == nil {
		t.Fatal("singular bare window selector succeeded, want ambiguity error")
	} else if selectorErr, ok := err.(*PaneSelectorError); !ok || selectorErr.Kind != PaneSelectorAmbiguous {
		t.Fatalf("singular bare window error = %T %v, want ambiguous PaneSelectorError", err, err)
	}
	if _, err := ResolvePaneSelectors(multiWindow, []string{"999.999"}, false); err == nil {
		t.Fatal("missing physical selector succeeded")
	} else if selectorErr, ok := err.(*PaneSelectorError); !ok || selectorErr.Kind != PaneSelectorNotFound {
		t.Fatalf("missing selector error = %T %v, want not-found PaneSelectorError", err, err)
	}
	if _, err := ResolvePaneSelectors(multiWindow, []string{"1.x"}, false); err == nil {
		t.Fatal("malformed selector succeeded")
	} else if selectorErr, ok := err.(*PaneSelectorError); !ok || selectorErr.Kind != PaneSelectorInvalid {
		t.Fatalf("malformed selector error = %T %v, want invalid PaneSelectorError", err, err)
	}

	// Put the operator pane after an agent in topology order. Selector identity
	// must remain stable regardless of where the user pane appears.
	userPane := orderedMulti[1]
	if _, err := DefaultClient.Run("select-pane", "-t", userPane.ID, "-T", "operator"); err != nil {
		t.Fatalf("mark reordered user pane: %v", err)
	}
	withUser, err := GetPanes(session)
	if err != nil {
		t.Fatalf("get reordered-user topology: %v", err)
	}
	orderedUser := SortPanesByTopology(withUser)
	if len(orderedUser) != 4 || orderedUser[0].Type == AgentUser || orderedUser[1].Type != AgentUser {
		t.Fatalf("user pane order = %+v, want user pane second", orderedUser)
	}
	selected, err = ResolvePaneSelectors(withUser, []string{exact.ID}, true)
	if err != nil || len(selected) != 1 || selected[0].ID != exact.ID {
		t.Fatalf("exact selection with reordered user = %+v, err=%v", selected, err)
	}
}

// tmux's command parser treats an argument ending in an unescaped ";" as a
// command terminator, and "--" does not protect it. Verified on tmux 3.5a:
// send-keys -l -- 'A=x;' delivers "A=x" and 'C=y;;' delivers "C=y;", while
// 'D=z\;' correctly delivers "D=z;".
func TestEscapeTrailingSemicolon(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"plain":       "plain",
		"A=x;":        `A=x\;`,
		";":           `\;`,
		"C=y;;":       `C=y;\;`,
		"a;b":         "a;b",     // interior semicolons are delivered verbatim
		`D=z\;`:       `D=z\;`,   // already escaped: leave alone
		`E=w\\;`:      `E=w\\\;`, // an escaped backslash then a bare ";" still needs escaping
		"trailing ; ": "trailing ; ",
	}
	for input, want := range cases {
		if got := escapeTrailingSemicolon(input); got != want {
			t.Errorf("escapeTrailingSemicolon(%q) = %q, want %q", input, got, want)
		}
	}
}

// Tests for the codex submission-verification heuristics (ntm-8ubn).
func TestCodexComposerHoldsPayload(t *testing.T) {
	message := "Fix the auth layer and report blockers.\nThen run the tests."

	cases := []struct {
		name    string
		capture string
		want    bool
	}{
		{
			name:    "unsubmitted prompt visible in composer",
			capture: "some earlier output\n› Fix the auth layer and report blockers.\n  Then run the tests.\n",
			want:    true,
		},
		{
			name:    "pasted placeholder in composer",
			capture: "banner\n› [Pasted text +12 lines]\n",
			want:    true,
		},
		{
			name:    "empty composer",
			capture: "response text\n› \n",
			want:    false,
		},
		{
			name:    "idle placeholder suggestion does not count as payload",
			capture: "response text\n› Try \"summarize this repo\"\n",
			want:    false,
		},
		{
			name:    "submitted prompt echoed in history without composer marker",
			capture: "user: Fix the auth layer and report blockers.\ncodex is thinking...\n",
			want:    false,
		},
		{
			// The critical false-failure case: codex echoes submitted user
			// messages into the transcript with a "›" prefix. Only the
			// bottom-most "›" line (the live composer) may count as payload.
			name:    "submitted prompt echoed with marker in transcript, empty composer below",
			capture: "› Fix the auth layer and report blockers.\n\nWorked for 2m 10s\nAll done, tests pass.\n› \n",
			want:    false,
		},
		{
			name:    "transcript echo above but payload still in composer below",
			capture: "› Fix the auth layer and report blockers.\nsome response\n› Fix the auth layer and report blockers.\n",
			want:    true,
		},
		{
			name:    "no composer marker at all",
			capture: "plain shell output\n$\n",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexComposerHoldsPayload(tc.capture, message); got != tc.want {
				t.Errorf("codexComposerHoldsPayload() = %v, want %v\ncapture:\n%s", got, tc.want, tc.capture)
			}
		})
	}
}

func TestCodexLooksWorking(t *testing.T) {
	if !codexLooksWorking("Working (3s • Esc to interrupt)") {
		t.Error("working footer should classify as working")
	}
	if codexLooksWorking("› waiting for input\n") {
		t.Error("idle composer should not classify as working")
	}
}

// Tests for the Claude submission-verification heuristic (GH#241 / ntm-utq6).
func TestClaudeComposerHoldsPayload(t *testing.T) {
	message := "Refactor the dispatcher and run the tests.\nReport blockers."

	cases := []struct {
		name    string
		capture string
		want    bool
	}{
		{
			name:    "unsubmitted prompt in composer",
			capture: "some output\n╭────╮\n│ ❯ Refactor the dispatcher and run the tests. │\n╰────╯\n",
			want:    true,
		},
		{
			name:    "pasted stand-in in composer",
			capture: "banner\n│ ❯ [Pasted text #1 +8 lines] │\n",
			want:    true,
		},
		{
			name:    "empty composer",
			capture: "response done\n│ ❯  │\n⏵⏵ bypass permissions on\n",
			want:    false,
		},
		{
			name:    "history user turn with plain > above empty composer",
			capture: "> Refactor the dispatcher and run the tests.\nWorked on it.\n│ ❯  │\n",
			want:    false,
		},
		{
			name:    "no composer marker",
			capture: "plain shell\n$\n",
			want:    false,
		},
		{
			name:    "history echo above but payload still in composer below",
			capture: "❯ Refactor the dispatcher and run the tests.\nsome text\n│ ❯ Refactor the dispatcher and run the tests. │\n",
			want:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeComposerHoldsPayload(tc.capture, message); got != tc.want {
				t.Errorf("claudeComposerHoldsPayload() = %v, want %v\ncapture:\n%s", got, tc.want, tc.capture)
			}
		})
	}
}
