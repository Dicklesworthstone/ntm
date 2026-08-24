package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"text/template"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	sessionpkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/startup"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestExecuteRootWithSignalsSecondSignalUsesDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal semantics required")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestExecuteRootWithSignalsSecondSignalHelper$")
	cmd.Env = append(os.Environ(), "NTM_TEST_ROOT_SECOND_SIGNAL_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("helper stdout pipe: %v", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("start signal helper: %v", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	joined := false
	t.Cleanup(func() {
		if !joined && cmd.Process != nil {
			_ = cmd.Process.Kill()
			<-waitCh
		}
	})

	lines := make(chan string, 8)
	scannerErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		scannerErr <- scanner.Err()
		close(lines)
	}()
	observed := make([]string, 0, 2)
	waitForLine := func(want string, timeout time.Duration) {
		t.Helper()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("signal helper exited before %q: observed=%q scanner_err=%v", want, observed, <-scannerErr)
				}
				observed = append(observed, line)
				if line == "LOCAL_SIGNAL_MISSING" {
					t.Fatalf("signal helper missed command-local notification while waiting for %q: observed=%q", want, observed)
				}
				if line == want {
					return
				}
			case <-timer.C:
				var scanErr error
				select {
				case scanErr = <-scannerErr:
				default:
				}
				t.Fatalf("timed out after %s waiting for signal helper line %q: observed=%q scanner_err=%v", timeout, want, observed, scanErr)
			}
		}
	}

	waitForLine("READY", 45*time.Second)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send first interrupt: %v", err)
	}
	waitForLine("CANCELED_AND_LOCAL_NOTIFIED", 5*time.Second)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send second interrupt: %v", err)
	}
	joinTimer := time.NewTimer(5 * time.Second)
	defer joinTimer.Stop()
	select {
	case err = <-waitCh:
		joined = true
	case <-joinTimer.C:
		_ = cmd.Process.Kill()
		err = <-waitCh
		joined = true
		t.Fatalf("signal helper did not exit within 5s after the second interrupt: wait_err=%v observed=%q", err, observed)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("second interrupt wait error = %v, want signal exit", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Fatalf("second interrupt status = %v, want SIGINT", exitErr.Sys())
	}
}

func TestClassifyRobotExecuteErrorMapsCancellationToTimeout(t *testing.T) {
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("initialize robot persistence: %w", context.Canceled),
	} {
		code, hint := classifyRobotExecuteError(err)
		if code != robot.ErrCodeTimeout || !strings.Contains(strings.ToLower(hint), "cancellation") {
			t.Fatalf("classifyRobotExecuteError(%v) = (%q, %q), want TIMEOUT cancellation guidance", err, code, hint)
		}
	}
}

func TestClassifyRobotExecuteErrorMapsMarkedInputToInvalidFlag(t *testing.T) {
	err := markCLIInvalidInput(errors.New("invalid selected configuration"))
	code, hint := classifyRobotExecuteError(fmt.Errorf("coordinator validation: %w", err))
	if code != robot.ErrCodeInvalidFlag {
		t.Fatalf("classifyRobotExecuteError() code = %q, want %q", code, robot.ErrCodeInvalidFlag)
	}
	if !strings.Contains(strings.ToLower(hint), "configuration") {
		t.Fatalf("classifyRobotExecuteError() hint = %q, want configuration guidance", hint)
	}
}

func TestRobotInvocationFromArgsPrefersOperationOverGlobalModifiers(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantRobot   bool
		wantCommand string
	}{
		{name: "format before bulk", args: []string{"--robot-format=json", "--robot-bulk-assign=proj"}, wantRobot: true, wantCommand: "robot-bulk-assign"},
		{name: "limit before status", args: []string{"--robot-limit=10", "--robot-status"}, wantRobot: true, wantCommand: "robot-status"},
		{name: "deprecated format before overlay", args: []string{"--robot-output-format=json", "--robot-overlay"}, wantRobot: true, wantCommand: "robot-overlay"},
		{name: "modifier only retains fallback", args: []string{"--robot-format=json", "--robot-limit=10"}, wantRobot: true, wantCommand: "robot-format"},
		{name: "terminator stops scan", args: []string{"--", "--robot-status"}, wantRobot: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotRobot, gotCommand := robotInvocationFromArgs(test.args)
			if gotRobot != test.wantRobot || gotCommand != test.wantCommand {
				t.Fatalf("robotInvocationFromArgs(%q) = (%v, %q), want (%v, %q)", test.args, gotRobot, gotCommand, test.wantRobot, test.wantCommand)
			}
		})
	}
}

func TestJSONInvocationFromArgsHonorsExplicitBooleanAndTerminator(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "global true", args: []string{"--json"}, want: true},
		{name: "global explicit true", args: []string{"replay", "--json=true"}, want: true},
		{name: "invalid json value preserves machine intent", args: []string{"--json=bogus", "version"}, want: true},
		{name: "global false", args: []string{"--json=false", "status"}, want: false},
		{name: "global last true wins", args: []string{"--json=false", "status", "--json"}, want: true},
		{name: "global last false wins", args: []string{"--json", "status", "--json=false"}, want: false},
		{name: "format equals", args: []string{"ensemble", "compare", "a", "b", "--format=json"}, want: true},
		{name: "format separate", args: []string{"ensemble", "presets", "--format", "json"}, want: true},
		{name: "short format equals", args: []string{"ensemble", "compare", "a", "b", "-f=json"}, want: true},
		{name: "short format attached", args: []string{"ensemble", "compare", "a", "b", "-fjson"}, want: true},
		{name: "short format separate", args: []string{"ensemble", "presets", "-f", "json"}, want: true},
		{name: "format case insensitive", args: []string{"modes", "list", "--format=JSON"}, want: true},
		{name: "format command alias", args: []string{"work", "commit-readiness", "--format=json"}, want: true},
		{name: "format last non-json wins", args: []string{"ensemble", "compare", "a", "b", "--format=json", "--format", "yaml"}, want: false},
		{name: "format last json wins", args: []string{"ensemble", "compare", "a", "b", "--format=yaml", "--format", "json"}, want: true},
		{name: "mixed format last non-json wins", args: []string{"ensemble", "compare", "a", "b", "-f=json", "--format", "yaml"}, want: false},
		{name: "mixed format last json wins", args: []string{"ensemble", "compare", "a", "b", "--format=yaml", "-f", "json"}, want: true},
		{name: "global true forces yaml", args: []string{"--json=true", "ensemble", "compare", "a", "b", "--format=yaml"}, want: true},
		{name: "pflag numeric bool", args: []string{"--json=1", "status"}, want: true},
		{name: "inherited json last false wins across command", args: []string{"--json=true", "handoff", "list", "--json=false"}, want: false},
		{name: "inherited json last false wins after command", args: []string{"handoff", "list", "--json=true", "--json=false"}, want: false},
		{name: "inherited global last false wins", args: []string{"--json=true", "status", "--json=false"}, want: false},
		{name: "global false does not negate format", args: []string{"--json=false", "ensemble", "compare", "a", "b", "--format=json"}, want: true},
		{name: "trailing global false does not negate format", args: []string{"ensemble", "compare", "a", "b", "--format=json", "--json=false"}, want: true},
		{name: "config value before command", args: []string{"--config", "/tmp/ntm.toml", "ensemble", "presets", "--format=json"}, want: true},
		{name: "root integer value before command", args: []string{"--limit", "5", "ensemble", "presets", "--format=json"}, want: true},
		{name: "root string value before command", args: []string{"--since", "7d", "summary", "--format=json"}, want: true},
		{name: "format before command", args: []string{"--format=json", "analytics"}, want: true},
		{name: "format before default json command overrides default", args: []string{"--format=csv", "audit", "export", "s"}, want: false},
		{name: "output equals", args: []string{"worktree", "list", "--output=json"}, want: true},
		{name: "output separate", args: []string{"worktree", "list", "--output", "json"}, want: true},
		{name: "short output equals", args: []string{"worktree", "list", "-o=json"}, want: true},
		{name: "short output attached", args: []string{"worktree", "list", "-ojson"}, want: true},
		{name: "short output separate", args: []string{"worktree", "list", "-o", "json"}, want: true},
		{name: "attached output last non-json wins", args: []string{"worktree", "list", "-ojson", "-otable"}, want: false},
		{name: "output last non-json wins", args: []string{"worktree", "list", "--output=json", "-o", "table"}, want: false},
		{name: "worktree output before command", args: []string{"--output=json", "worktree", "list"}, want: true},
		{name: "unscoped output is not format", args: []string{"audit", "export", "s", "--output=json", "--format=csv"}, want: false},
		{name: "audit export defaults json", args: []string{"audit", "export", "s"}, want: true},
		{name: "audit export explicit csv", args: []string{"audit", "export", "s", "--format=csv"}, want: false},
		{name: "metrics export defaults json", args: []string{"metrics", "export"}, want: true},
		{name: "metrics export explicit prometheus", args: []string{"metrics", "export", "-f", "prometheus"}, want: false},
		{name: "work graph defaults json", args: []string{"work", "graph"}, want: true},
		{name: "work graph explicit dot", args: []string{"work", "graph", "--format=dot"}, want: false},
		{name: "cass injection format is not output", args: []string{"cass", "preview", "--format=json"}, want: false},
		{name: "cass injection short format is not output", args: []string{"cass", "preview", "-f=json"}, want: false},
		{name: "checkpoint archive format is not output", args: []string{"checkpoint", "export", "s", "id", "--format=json"}, want: false},
		{name: "short format requires real shorthand", args: []string{"analytics", "-f=json"}, want: false},
		{name: "attached short format requires real shorthand", args: []string{"analytics", "-fjson"}, want: false},
		{name: "unscoped format is not output", args: []string{"status", "--format=json"}, want: false},
		{name: "terminator stops global scan", args: []string{"--", "--json"}, want: false},
		{name: "terminator stops format scan", args: []string{"ensemble", "compare", "a", "b", "--", "--format=json"}, want: false},
		{name: "terminator stops output scan", args: []string{"worktree", "list", "--", "--output=json"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := jsonInvocationFromArgs(test.args); got != test.want {
				t.Fatalf("jsonInvocationFromArgs(%q) = %v, want %v", test.args, got, test.want)
			}
		})
	}
}

func TestJSONFlagIsDeclaredOnlyAsRootPersistentFlag(t *testing.T) {
	if rootCmd.PersistentFlags().Lookup("json") == nil {
		t.Fatal("root command is missing its persistent --json flag")
	}

	var inspect func(*cobra.Command)
	inspect = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			if child.LocalNonPersistentFlags().Lookup("json") != nil {
				t.Errorf("command %q redeclares --json instead of inheriting the root persistent flag", child.CommandPath())
			}
			inspect(child)
		}
	}
	inspect(rootCmd)
}

func TestExecuteRootWithSignalsSecondSignalHelper(t *testing.T) {
	if os.Getenv("NTM_TEST_ROOT_SECOND_SIGNAL_HELPER") != "1" {
		return
	}
	localSignals := make(chan os.Signal, 1)
	signal.Notify(localSignals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(localSignals)

	_ = executeRootWithSignals(func(ctx context.Context) error {
		fmt.Println("READY")
		<-ctx.Done()
		select {
		case <-localSignals:
			fmt.Println("CANCELED_AND_LOCAL_NOTIFIED")
		case <-time.After(5 * time.Second):
			fmt.Println("LOCAL_SIGNAL_MISSING")
		}
		select {}
	})
}

// resetFlags resets global flags to default values between tests
func resetFlags() {
	jsonOutput = false
	noColor = false
	redactMode = ""
	allowSecret = false
	robotHelp = false
	robotStatus = false
	robotVersion = false
	robotPlan = false
	robotSnapshot = false
	robotSince = ""
	robotTail = ""
	robotWatchBead = ""
	robotWatchBeadID = ""
	robotProxyStatus = false
	robotLines = 20
	robotPanes = ""
	robotHistoryPane = ""
	robotSend = ""
	robotSendMsg = ""
	robotSendMsgFile = ""
	robotSendEnter = true
	robotSendAll = false
	robotSendType = ""
	robotSendExclude = ""
	robotSendDelay = 0
	robotAssignStrategy = "simple"
	robotBulkAssign = ""
	robotBulkAssignFromBV = false
	robotBulkAssignAlloc = ""
	robotBulkAssignStrategy = "impact"
	robotBulkAssignSkip = ""
	robotBulkAssignTemplate = ""
	robotBulkAssignParallel = false
	robotBulkAssignStagger = 0
	robotRequireReservation = false
	robotReservationPaths = ""
	robotDiff = ""
	robotDiffSince = "15m"
	robotHistorySince = ""
	robotHistoryType = ""
	robotSummarySince = "30m"
	robotTokensSince = ""
	robotWaitTimeout = "5m"
	robotWaitPoll = "2s"
	robotWaitPanes = ""
	robotWaitType = ""
	robotWaitAny = false
	robotProductivity = ""
	robotProductivityWindow = "30m"
	robotConvergedStreak = 3
	robotRouteStrategy = "least-loaded"
	robotRouteType = ""
	robotRouteExclude = ""
	robotSpawnTimeout = "30s"
	robotSpawnStrategy = "top-n"
	robotSpawn = ""
	robotSpawnCC = ""
	robotSpawnCod = ""
	robotSpawnGmi = ""
	robotSpawnAgy = ""
	robotSpawnGrok = ""
	robotSpawnPreset = ""
	robotSpawnNoUser = false
	robotSpawnWait = false
	robotSpawnSafety = false
	robotSpawnDir = ""
	robotSpawnAssignWork = false
	robotSpawnNames = ""
	robotSpawnLabel = ""
	robotInterruptMsg = ""
	robotInterruptAll = false
	robotInterruptForce = false
	robotInterruptTimeout = "10s"
	robotReplayDryRun = false
	robotPipelineDryRun = false
	robotPaletteInfo = false
	robotPaletteSession = ""
	robotPaletteCategory = ""
	robotPaletteSearch = ""
	robotDismissAlert = ""
	robotDismissSession = ""
	robotDismissAll = false
	robotAgentHealthVerbose = false
	robotSmartRestartDryRun = false
	robotSmartRestartVerbose = false
	robotSmartRestartForce = false
	robotSmartRestartHardKill = false
	robotSmartRestartHardKillOnly = false
	robotFormat = ""
	robotVerbosity = ""
	robotMonitorOutput = ""
	robotSaveOutput = ""
}

func TestRobotSpawnOptionsFromFlagsPreservesDurationAndCopiesInputs(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	robotSpawn = "duration-spawn"
	robotSpawnLabel = "goal"
	robotSpawnCC = "1"
	robotSpawnCod = "2:gpt-5.3-codex:high"
	robotSpawnGmi = "3"
	robotSpawnAgy = "4"
	robotSpawnGrok = "5"
	robotSpawnPreset = "custom"
	robotSpawnNoUser = true
	robotSpawnDir = t.TempDir()
	robotSpawnWait = true
	robotSpawnSafety = true
	robotSpawnAssignWork = true
	robotSpawnStrategy = "diverse"
	robotSpawnNames = "Alice, Bob"
	robotRequireReservation = true
	paths := []string{"internal/robot/**", "internal/cli/**"}

	opts, err := robotSpawnOptionsFromFlags(nil, 500*time.Millisecond, paths, true)
	if err != nil {
		t.Fatalf("robotSpawnOptionsFromFlags: %v", err)
	}
	if opts.ReadyTimeout != 500*time.Millisecond {
		t.Fatalf("ReadyTimeout=%s, want 500ms", opts.ReadyTimeout)
	}
	if opts.CodModel != "gpt-5.3-codex" || opts.CodReasoningEffort != "high" {
		t.Fatalf("Cod model/effort=%q/%q, want gpt-5.3-codex/high", opts.CodModel, opts.CodReasoningEffort)
	}
	if opts.Session != robotSpawn || opts.Label != robotSpawnLabel || opts.CCCount != 1 || opts.CodCount != 2 || opts.GmiCount != 3 || opts.AgyCount != 4 || opts.GrokCount != 5 {
		t.Fatalf("spawn identity/count options=%+v", opts)
	}
	if !opts.NoUserPane || !opts.WaitReady || !opts.DryRun || !opts.Safety || !opts.AssignWork || !opts.RequireReservation {
		t.Fatalf("spawn boolean options=%+v", opts)
	}
	if opts.Preset != "custom" || opts.WorkingDir != robotSpawnDir || opts.AssignStrategy != "diverse" {
		t.Fatalf("spawn value options=%+v", opts)
	}
	if !reflect.DeepEqual(opts.CustomNames, []string{"Alice", "Bob"}) {
		t.Fatalf("CustomNames=%v", opts.CustomNames)
	}
	if !reflect.DeepEqual(opts.ReservationPaths, paths) {
		t.Fatalf("ReservationPaths=%v, want %v", opts.ReservationPaths, paths)
	}
	paths[0] = "mutated"
	if opts.ReservationPaths[0] != "internal/robot/**" {
		t.Fatalf("ReservationPaths aliases caller input: %v", opts.ReservationPaths)
	}
}

func TestSuppressRobotDiagnosticsRestoresWriters(t *testing.T) {
	originalSlog := slog.Default()
	originalLogWriter := log.Writer()
	t.Cleanup(func() {
		slog.SetDefault(originalSlog)
		log.SetOutput(originalLogWriter)
	})

	var slogOutput bytes.Buffer
	var logOutput bytes.Buffer
	var commandErrorOutput bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&slogOutput, nil)))
	log.SetOutput(&logOutput)
	cmd := &cobra.Command{Use: "test"}
	cmd.SetErr(&commandErrorOutput)
	cmd.Flags().String("legacy", "", "")
	if err := cmd.Flags().MarkDeprecated("legacy", "use --current instead"); err != nil {
		t.Fatalf("mark legacy flag deprecated: %v", err)
	}

	restore := suppressRobotDiagnostics(cmd)
	if message := cmd.Flags().Lookup("legacy").Deprecated; message != "" {
		t.Fatalf("deprecated flag message remained active during suppression: %q", message)
	}
	slog.Warn("suppressed slog diagnostic")
	log.Print("suppressed log diagnostic")
	fmt.Fprint(cmd.ErrOrStderr(), "suppressed cobra diagnostic")
	if slogOutput.Len() != 0 || logOutput.Len() != 0 || commandErrorOutput.Len() != 0 {
		t.Fatalf("diagnostics escaped suppression: slog=%q log=%q cobra=%q", slogOutput.String(), logOutput.String(), commandErrorOutput.String())
	}

	restore()
	if message := cmd.Flags().Lookup("legacy").Deprecated; message != "use --current instead" {
		t.Fatalf("deprecated flag message was not restored: %q", message)
	}
	slog.Warn("restored slog diagnostic")
	log.Print("restored log diagnostic")
	fmt.Fprint(cmd.ErrOrStderr(), "restored cobra diagnostic")
	if !strings.Contains(slogOutput.String(), "restored slog diagnostic") {
		t.Fatalf("slog writer was not restored: %q", slogOutput.String())
	}
	if !strings.Contains(logOutput.String(), "restored log diagnostic") {
		t.Fatalf("log writer was not restored: %q", logOutput.String())
	}
	if commandErrorOutput.String() != "restored cobra diagnostic" {
		t.Fatalf("cobra error writer was not restored: %q", commandErrorOutput.String())
	}
}

func TestMaybeRunStartupCleanupMarksOnlyCompleteSuccess(t *testing.T) {
	originalCfgFile := cfgFile
	t.Cleanup(func() { cfgFile = originalCfgFile })

	t.Run("read failure remains retryable", func(t *testing.T) {
		configRoot := t.TempDir()
		cfgFile = filepath.Join(configRoot, "ntm", "config.toml")
		t.Setenv("TMPDIR", filepath.Join(configRoot, "missing-temp-root"))

		MaybeRunStartupCleanup(true, 24, false)

		if _, err := os.Stat(lastCleanupFile()); !os.IsNotExist(err) {
			t.Fatalf("failed cleanup marker stat error = %v, want not-exist", err)
		}
	})

	t.Run("complete success records interval", func(t *testing.T) {
		configRoot := t.TempDir()
		tempRoot := filepath.Join(configRoot, "empty-temp-root")
		if err := os.MkdirAll(tempRoot, 0o700); err != nil {
			t.Fatalf("create empty temp root: %v", err)
		}
		cfgFile = filepath.Join(configRoot, "ntm", "config.toml")
		t.Setenv("TMPDIR", tempRoot)

		MaybeRunStartupCleanup(true, 24, false)

		if _, err := os.Stat(lastCleanupFile()); err != nil {
			t.Fatalf("successful cleanup marker: %v", err)
		}
	})
}

func TestShouldInitializeRobotPersistenceSkipsStatelessOverlay(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() {
		os.Args = origArgs
	})

	cmd := &cobra.Command{Use: "ntm"}
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "overlay only",
			args: []string{"ntm", "--robot-overlay"},
			want: false,
		},
		{
			name: "overlay with value spelling",
			args: []string{"ntm", "--robot-overlay=true"},
			want: false,
		},
		{
			name: "format before overlay remains stateless",
			args: []string{"ntm", "--robot-format=json", "--robot-overlay"},
			want: false,
		},
		{
			name: "overlay before deprecated format remains stateless",
			args: []string{"ntm", "--robot-overlay", "--robot-output-format=json"},
			want: false,
		},
		{
			name: "stateful robot flag",
			args: []string{"ntm", "--robot-status"},
			want: true,
		},
		{
			name: "modifier before stateful operation initializes",
			args: []string{"ntm", "--robot-limit=10", "--robot-status"},
			want: true,
		},
		{
			name: "stateful flag still wins when mixed",
			args: []string{"ntm", "--robot-overlay", "--robot-status"},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = tc.args
			if got := shouldInitializeRobotPersistence(cmd); got != tc.want {
				t.Fatalf("shouldInitializeRobotPersistence() = %v, want %v", got, tc.want)
			}
		})
	}
}

func isolateSessionAgentStorage(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	// Resolve symlinks so production code that canonicalizes paths
	// (os.Getwd, git rev-parse) matches what tests pass back in.
	// On macOS, t.TempDir() returns /var/folders/... but os.Getwd
	// after chdir returns /private/var/folders/... — keep them aligned.
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// bd-ev740 / bd-jba66 precedent: clear ambient NTM_CONFIG so
	// state.DefaultPath does not route the state DB into a hostile or
	// non-writable path that an outer CI/agent shell may have exported
	// (e.g. /nonexistent/config.toml).
	t.Setenv("NTM_CONFIG", "")
}

// canonicalTempDir wraps t.TempDir with EvalSymlinks so the returned
// path matches what production code sees via os.Getwd() or
// `git rev-parse --show-toplevel`. On macOS, t.TempDir() returns
// "/var/folders/..." but those calls return the canonical
// "/private/var/folders/..." form; comparing the two fails only on
// macOS-latest CI.
//
// Use this in any test that constructs a tempdir path and then compares
// it to a path emitted by code that may have canonicalized it.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return resolved
}

func createCLIWorkspaceProjectRoot(t *testing.T) (string, string) {
	t.Helper()

	root := canonicalTempDir(t)
	cmd := exec.Command("git", "init", "-b", "main", root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initialize workspace git repository: %v\n%s", err, output)
	}
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested workspace dir: %v", err)
	}

	return root, nested
}

func TestResolveRobotFormat_DefaultAuto(t *testing.T) {
	resetFlags()
	t.Setenv("NTM_ROBOT_FORMAT", "")
	t.Setenv("NTM_OUTPUT_FORMAT", "")
	t.Setenv("TOON_DEFAULT_FORMAT", "")

	if err := resolveRobotFormat(nil); err != nil {
		t.Fatalf("resolveRobotFormat() error = %v", err)
	}

	if robot.GetOutputFormat() != robot.FormatAuto {
		t.Errorf("OutputFormat default = %q, want %q", robot.GetOutputFormat(), robot.FormatAuto)
	}
}

func TestResolveRobotFormat_EnvFallback(t *testing.T) {
	resetFlags()
	t.Setenv("NTM_ROBOT_FORMAT", "toon")
	t.Setenv("NTM_OUTPUT_FORMAT", "")
	t.Setenv("TOON_DEFAULT_FORMAT", "")

	if err := resolveRobotFormat(nil); err != nil {
		t.Fatalf("resolveRobotFormat() error = %v", err)
	}

	if robot.GetOutputFormat() != robot.FormatTOON {
		t.Errorf("OutputFormat from env = %q, want %q", robot.GetOutputFormat(), robot.FormatTOON)
	}
}

func TestRobotLineDefaultsUseCommandContracts(t *testing.T) {
	resetFlags()

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Int("lines", 20, "")

	if got := robotIsWorkingLines(cmd); got != robot.DefaultIsWorkingOptions().LinesCaptured {
		t.Fatalf("robotIsWorkingLines() = %d, want %d", got, robot.DefaultIsWorkingOptions().LinesCaptured)
	}
	if got := robotAgentHealthLines(cmd); got != robot.DefaultAgentHealthOptions().LinesCaptured {
		t.Fatalf("robotAgentHealthLines() = %d, want %d", got, robot.DefaultAgentHealthOptions().LinesCaptured)
	}
	if got := robotSmartRestartLines(cmd); got != robot.DefaultSmartRestartOptions().LinesCaptured {
		t.Fatalf("robotSmartRestartLines() = %d, want %d", got, robot.DefaultSmartRestartOptions().LinesCaptured)
	}
	if got := robotMonitorLines(cmd); got != robot.DefaultMonitorConfig().LinesCaptured {
		t.Fatalf("robotMonitorLines() = %d, want %d", got, robot.DefaultMonitorConfig().LinesCaptured)
	}
}

func TestRobotLineDefaultsRespectExplicitOverride(t *testing.T) {
	resetFlags()
	robotLines = 37

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Int("lines", 20, "")
	if err := cmd.Flags().Set("lines", "37"); err != nil {
		t.Fatalf("set lines flag: %v", err)
	}

	if got := robotIsWorkingLines(cmd); got != 37 {
		t.Fatalf("robotIsWorkingLines() = %d, want 37", got)
	}
	if got := robotAgentHealthLines(cmd); got != 37 {
		t.Fatalf("robotAgentHealthLines() = %d, want 37", got)
	}
	if got := robotSmartRestartLines(cmd); got != 37 {
		t.Fatalf("robotSmartRestartLines() = %d, want 37", got)
	}
	if got := robotMonitorLines(cmd); got != 37 {
		t.Fatalf("robotMonitorLines() = %d, want 37", got)
	}
}

func TestRunQuickUsesDefaultProjectsBaseWhenConfigNil(t *testing.T) {
	base := t.TempDir()
	t.Setenv("NTM_PROJECTS_BASE", base)

	oldCfg := cfg
	oldJSON := jsonOutput
	cfg = nil
	jsonOutput = true
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	})

	name := "quick-fallback"
	if err := runQuick(name, quickOptions{
		NoGit:          true,
		NoVSCode:       true,
		NoClaudeConfig: true,
	}); err != nil {
		t.Fatalf("runQuick() error = %v", err)
	}

	want := filepath.Join(base, name)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected project directory %q to exist: %v", want, err)
	}
}

func TestResolveMessageCommandScopeRejectsMissingOrCanceledContext(t *testing.T) {
	t.Run("nil command", func(t *testing.T) {
		_, _, err := resolveMessageCommandScope(nil)
		if err == nil || !strings.Contains(err.Error(), "requires a command context") {
			t.Fatalf("resolveMessageCommandScope(nil) error = %v, want missing context error", err)
		}
	})

	t.Run("nil context", func(t *testing.T) {
		_, _, err := resolveMessageCommandScope(&cobra.Command{})
		if err == nil || !strings.Contains(err.Error(), "requires a command context") {
			t.Fatalf("resolveMessageCommandScope() error = %v, want missing context error", err)
		}
	})

	t.Run("pre-canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		cmd := &cobra.Command{}
		cmd.SetContext(ctx)
		_, _, err := resolveMessageCommandScope(cmd)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("resolveMessageCommandScope() error = %v, want context.Canceled", err)
		}
	})
}

func TestMessageSubcommandsRejectPreCanceledContextDirectly(t *testing.T) {
	tests := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{name: "inbox", cmd: newMessageInboxCmd},
		{name: "send", cmd: newMessageSendCmd, args: []string{"TargetAgent", "body"}},
		{name: "read", cmd: newMessageReadCmd, args: []string{"am-1"}},
		{name: "ack", cmd: newMessageAckCmd, args: []string{"am-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			cmd := tt.cmd()
			cmd.SetContext(ctx)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)

			err := cmd.RunE(cmd, tt.args)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("direct %s command error = %v, want context.Canceled", tt.name, err)
			}
			if output.Len() != 0 {
				t.Fatalf("direct %s command emitted output after cancellation: %q", tt.name, output.String())
			}
		})
	}
}

func TestResolveMessageScopeUsesSessionProjectDir(t *testing.T) {
	isolateSessionAgentStorage(t)

	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotAgent, err := resolveMessageScope(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("resolveMessageScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotAgent != "ntm_mysession" {
		t.Fatalf("agent name = %q, want %q", gotAgent, "ntm_mysession")
	}
}

func TestResolveMessageScopeRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	root, nested := createCLIWorkspaceProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	_, _, err := resolveMessageScope(t.Context(), "mysession")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveMessageScopeFallsBackToProjectRoot(t *testing.T) {
	isolateSessionAgentStorage(t)

	projectDir := canonicalTempDir(t)

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotAgent, err := resolveMessageScope(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveMessageScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	wantAgent := "ntm_" + filepath.Base(projectDir)
	if gotAgent != wantAgent {
		t.Fatalf("agent name = %q, want %q", gotAgent, wantAgent)
	}
}

func TestResolveMessageScopeInfersLabeledSessionFromCurrentProject(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	isolateSessionAgentStorage(t)

	projectsBase := canonicalTempDir(t)
	projectDir := filepath.Join(projectsBase, "messageproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	fullSession := "messageproject--frontend"
	_ = tmux.KillSession(fullSession)
	if err := tmux.CreateSession(fullSession, projectDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", fullSession, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(fullSession) })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotAgent, err := resolveMessageScope(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveMessageScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	wantAgent := "ntm_" + fullSession
	if gotAgent != wantAgent {
		t.Fatalf("agent name = %q, want %q", gotAgent, wantAgent)
	}
}

func TestResolveMessageScopeNormalizesExplicitPrefix(t *testing.T) {
	isolateSessionAgentStorage(t)

	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "messageprefix")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotAgent, err := resolveMessageScope(t.Context(), "messagep")
	if err != nil {
		t.Fatalf("resolveMessageScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotAgent != "ntm_messageprefix" {
		t.Fatalf("agent name = %q, want %q", gotAgent, "ntm_messageprefix")
	}
}

func TestResolveMessageScopeUsesSavedSessionAgentIdentity(t *testing.T) {
	isolateSessionAgentStorage(t)

	projectsBase := t.TempDir()
	resolvedProject := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(resolvedProject, 0o755); err != nil {
		t.Fatalf("mkdir resolved project: %v", err)
	}
	actualProject := t.TempDir()
	saveSessionAgentForTest(t, "mysession", actualProject, "GreenCastle")

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotAgent, err := resolveMessageScope(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("resolveMessageScope() error = %v", err)
	}
	if gotDir != actualProject {
		t.Fatalf("project dir = %q, want %q", gotDir, actualProject)
	}
	if gotAgent != "GreenCastle" {
		t.Fatalf("agent name = %q, want %q", gotAgent, "GreenCastle")
	}
}

func TestResolveMessageScopeUsesSavedSessionAgentWhenInferringSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	projectDir := t.TempDir()
	session := filepath.Base(projectDir)
	saveSessionAgentForTest(t, session, projectDir, "BlueLake")

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotAgent, err := resolveMessageScope(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveMessageScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotAgent != "BlueLake" {
		t.Fatalf("agent name = %q, want %q", gotAgent, "BlueLake")
	}
}

func TestResolveMessageScopeUsesCurrentPaneRegistryIdentity(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	isolateSessionAgentStorage(t)

	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "messagepaneidentity")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	session := "messagepaneidentity"
	_ = tmux.KillSession(session)
	if err := tmux.CreateSession(session, projectDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", session, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes(%q): %v", session, err)
	}
	if len(panes) == 0 {
		t.Fatal("expected at least one pane")
	}

	saveSessionAgentForTest(t, session, projectDir, "BlueLake")
	saveSessionAgentRegistryForTest(t, session, projectDir, "", panes[0].ID, "GreenCastle")
	t.Setenv("TMUX_PANE", panes[0].ID)

	gotDir, gotAgent, err := resolveMessageScope(t.Context(), session)
	if err != nil {
		t.Fatalf("resolveMessageScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotAgent != "GreenCastle" {
		t.Fatalf("agent name = %q, want %q", gotAgent, "GreenCastle")
	}
}

func TestResolveMessageScopeRejectsInvalidSessionName(t *testing.T) {
	_, _, err := resolveMessageScope(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolvePipelineProjectDirForSessionUsesSessionProjectDir(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, err := resolvePipelineProjectDirForSession(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("resolvePipelineProjectDirForSession() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
}

func TestResolvePipelineProjectDirForSessionRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	root, nested := createCLIWorkspaceProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	_, err := resolvePipelineProjectDirForSession(t.Context(), "mysession")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolvePipelineProjectDirForSessionFallsBackToProjectRoot(t *testing.T) {
	projectDir := canonicalTempDir(t)

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, err := resolvePipelineProjectDirForSession(t.Context(), "")
	if err != nil {
		t.Fatalf("resolvePipelineProjectDirForSession() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
}

func TestResolvePipelineProjectDirForSessionRejectsInvalidSessionName(t *testing.T) {
	_, err := resolvePipelineProjectDirForSession(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestPipelineLintCmdValidWorkflowDoesNotRequireSession(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	path := writePipelineLintWorkflow(t, `
schema_version: "2.0"
name: lint-workflow
steps:
  - id: step1
    agent: claude
    prompt: Do something
`)

	cmd := newPipelineCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"lint", path})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("pipeline lint returned error: %v; stderr=%q", err, errOut.String())
	}
	if got := out.String(); !strings.Contains(got, "Validation: ok") || !strings.Contains(got, "Workflow: lint-workflow") {
		t.Fatalf("unexpected lint output: %q", got)
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
}

func TestPipelineLintCmdJSONIncludesNormalizedWorkflowOnValidationFailure(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	path := writePipelineLintWorkflow(t, `
schema_version: "2.0"
steps:
  - id: step1
    agent: claude
    prompt: Do something
`)

	cmd := newPipelineCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"lint", path})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("pipeline lint returned nil error for invalid workflow")
	}

	var result struct {
		Success            bool            `json:"success"`
		ErrorCode          string          `json:"error_code"`
		Errors             []any           `json:"errors"`
		NormalizedWorkflow json.RawMessage `json:"normalized_workflow"`
	}
	if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
		t.Fatalf("json.Unmarshal() error = %v; output=%q", decodeErr, out.String())
	}
	if result.Success {
		t.Fatalf("success = true, want false; result=%+v", result)
	}
	if result.ErrorCode != "VALIDATION_FAILED" {
		t.Fatalf("error_code = %q, want VALIDATION_FAILED", result.ErrorCode)
	}
	if len(result.NormalizedWorkflow) == 0 || string(result.NormalizedWorkflow) == "null" {
		t.Fatal("normalized_workflow is empty")
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected validation errors")
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr in json mode: %q", errOut.String())
	}
}

func writePipelineLintWorkflow(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "workflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return path
}

func TestResolveRobotSessionProjectScopeNormalizesExplicitPrefix(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	projectsBase := canonicalTempDir(t)
	fullSession := "robotrootsession"
	projectDir := filepath.Join(projectsBase, fullSession)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	oldJSON := jsonOutput
	cfg = &config.Config{ProjectsBase: projectsBase}
	jsonOutput = false
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	})

	_ = tmux.KillSession(fullSession)
	if err := tmux.CreateSession(fullSession, projectDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", fullSession, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(fullSession) })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotSession, gotDir, err := resolveRobotSessionProjectScope(t.Context(), "robotroot")
	if err != nil {
		t.Fatalf("resolveRobotSessionProjectScope() error = %v", err)
	}
	if gotSession != fullSession {
		t.Fatalf("session = %q, want %q", gotSession, fullSession)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
}

func TestResolveRobotSessionProjectScopeRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	root, nested := createCLIWorkspaceProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotSession, gotDir, err := resolveRobotSessionProjectScope(t.Context(), "robotmissing")
	if err == nil {
		t.Fatalf("expected missing session project error, got session=%q dir=%q", gotSession, gotDir)
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveRobotSessionProjectScopeRejectsInvalidSessionName(t *testing.T) {
	_, _, err := resolveRobotSessionProjectScope(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveRobotLiveSessionNormalizesExplicitPrefix(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	fullSession := "robotlivesession"
	projectDir := t.TempDir()

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = tmux.KillSession(fullSession)
	if err := tmux.CreateSession(fullSession, projectDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", fullSession, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(fullSession) })

	got, err := resolveRobotLiveSession(t.Context(), "robotlive")
	if err != nil {
		t.Fatalf("resolveRobotLiveSession() error = %v", err)
	}
	if got != fullSession {
		t.Fatalf("session = %q, want %q", got, fullSession)
	}
}

func TestResolveRobotLiveSessionPreservesMissingSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	missing := "robotlivemissingzzzz"
	if tmux.SessionExists(missing) {
		t.Skipf("session %q unexpectedly exists", missing)
	}

	got, err := resolveRobotLiveSession(t.Context(), missing)
	if err != nil {
		t.Fatalf("resolveRobotLiveSession() error = %v", err)
	}
	if got != missing {
		t.Fatalf("session = %q, want %q", got, missing)
	}
}

func TestResolveRobotLiveSessionRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveRobotLiveSession(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveRobotLiveSessionHonorsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := resolveRobotLiveSession(ctx, "robotlive")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resolveRobotLiveSession(canceled) error = %v, want context.Canceled", err)
	}
}

func TestResolveRobotLiveSessionRequiresContext(t *testing.T) {
	_, err := resolveRobotLiveSession(nil, "robotlive")
	if err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("resolveRobotLiveSession(nil) error = %v, want required-context error", err)
	}
}

func TestResolveOptionalRobotLiveSessionAllowsEmpty(t *testing.T) {
	got, err := resolveOptionalRobotLiveSession(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveOptionalRobotLiveSession() error = %v", err)
	}
	if got != "" {
		t.Fatalf("session = %q, want empty", got)
	}
}

func TestResolveRobotOfflineCapableSessionNormalizesConfiguredProjectPrefix(t *testing.T) {
	origCfg := cfg
	origJSON := jsonOutput
	t.Cleanup(func() {
		cfg = origCfg
		jsonOutput = origJSON
	})

	projectsBase := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectsBase, "robotproject"), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}
	jsonOutput = false

	got, err := resolveRobotOfflineCapableSession(t.Context(), "robotpro")
	if err != nil {
		t.Fatalf("resolveRobotOfflineCapableSession() error = %v", err)
	}
	if got != "robotproject" {
		t.Fatalf("session = %q, want %q", got, "robotproject")
	}
}

func TestResolveRobotOfflineCapableSessionRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveRobotOfflineCapableSession(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveRobotSessionFilterNormalizesExplicitPrefix(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	fullSession := "robotfiltersession"
	projectDir := t.TempDir()

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = tmux.KillSession(fullSession)
	if err := tmux.CreateSession(fullSession, projectDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", fullSession, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(fullSession) })

	got, err := resolveRobotSessionFilter(t.Context(), "robotfilter")
	if err != nil {
		t.Fatalf("resolveRobotSessionFilter() error = %v", err)
	}
	if got != fullSession {
		t.Fatalf("session = %q, want %q", got, fullSession)
	}
}

func TestResolveOptionalRobotSessionFilterAllowsEmpty(t *testing.T) {
	got, err := resolveOptionalRobotSessionFilter(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveOptionalRobotSessionFilter() error = %v", err)
	}
	if got != "" {
		t.Fatalf("session = %q, want empty", got)
	}
}

func TestResolveWorktreeScopeUsesSessionProjectDir(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotSession, err := resolveWorktreeScope(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("resolveWorktreeScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotSession != "mysession" {
		t.Fatalf("session = %q, want %q", gotSession, "mysession")
	}
}

func TestResolveWorktreeScopeRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	root, nested := createCLIWorkspaceProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	_, _, err := resolveWorktreeScope(t.Context(), "mysession")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveWorktreeScopeFallsBackToProjectRoot(t *testing.T) {
	projectDir := canonicalTempDir(t)

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotSession, err := resolveWorktreeScope(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveWorktreeScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotSession != filepath.Base(projectDir) {
		t.Fatalf("session = %q, want %q", gotSession, filepath.Base(projectDir))
	}
}

func TestResolveWorktreeScopeInfersLabeledSessionFromCurrentProject(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	projectsBase := canonicalTempDir(t)
	projectDir := filepath.Join(projectsBase, "scopeproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	fullSession := "scopeproject--frontend"
	_ = tmux.KillSession(fullSession)
	if err := tmux.CreateSession(fullSession, projectDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", fullSession, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(fullSession) })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotSession, err := resolveWorktreeScope(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveWorktreeScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotSession != fullSession {
		t.Fatalf("session = %q, want %q", gotSession, fullSession)
	}
}

func TestResolveWorktreeScopeRejectsInvalidSessionName(t *testing.T) {
	_, _, err := resolveWorktreeScope(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveContextBuildScopeUsesCurrentSessionProjectDir(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotSession, err := resolveContextBuildScope(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("resolveContextBuildScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotSession != "mysession" {
		t.Fatalf("session = %q, want %q", gotSession, "mysession")
	}
}

func TestResolveContextBuildScopeRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	root, nested := createCLIWorkspaceProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	_, _, err := resolveContextBuildScope(t.Context(), "mysession")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveContextBuildScopeFallsBackToProjectRoot(t *testing.T) {
	projectDir := canonicalTempDir(t)

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotSession, err := resolveContextBuildScope(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveContextBuildScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotSession != filepath.Base(projectDir) {
		t.Fatalf("session = %q, want %q", gotSession, filepath.Base(projectDir))
	}
}

func TestResolveContextBuildScopeInfersLabeledSessionFromCurrentProject(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	projectsBase := canonicalTempDir(t)
	projectDir := filepath.Join(projectsBase, "contextscope")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	fullSession := "contextscope--frontend"
	_ = tmux.KillSession(fullSession)
	if err := tmux.CreateSession(fullSession, projectDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", fullSession, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(fullSession) })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, gotSession, err := resolveContextBuildScope(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveContextBuildScope() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
	if gotSession != fullSession {
		t.Fatalf("session = %q, want %q", gotSession, fullSession)
	}
}

func TestResolveContextBuildScopeRejectsInvalidSessionName(t *testing.T) {
	_, _, err := resolveContextBuildScope(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveContextBuildScopeUsesSavedSessionAgentProjectKey(t *testing.T) {
	isolateSessionAgentStorage(t)

	projectsBase := t.TempDir()
	resolvedProject := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(resolvedProject, 0o755); err != nil {
		t.Fatalf("mkdir resolved project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	actualProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual project git dir: %v", err)
	}
	saveSessionAgentForTest(t, "mysession", actualProject, "GreenCastle")

	gotDir, gotSession, err := resolveContextBuildScope(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("resolveContextBuildScope() error = %v", err)
	}
	if gotDir != actualProject {
		t.Fatalf("project dir = %q, want saved session agent project %q", gotDir, actualProject)
	}
	if gotSession != "mysession" {
		t.Fatalf("session = %q, want %q", gotSession, "mysession")
	}
}

func TestResolveEnsembleProjectDirForSessionUsesSessionProjectDir(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, err := resolveEnsembleProjectDirForSession(t.Context(), "mysession")
	if err != nil {
		t.Fatalf("resolveEnsembleProjectDirForSession() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
}

func TestResolveEnsembleProjectDirForSessionRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	root, nested := createCLIWorkspaceProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	_, err := resolveEnsembleProjectDirForSession(t.Context(), "mysession")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveEnsembleProjectDirForSessionResolvesProjectScopedPrefix(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, err := resolveEnsembleProjectDirForSession(t.Context(), "mypro")
	if err != nil {
		t.Fatalf("resolveEnsembleProjectDirForSession() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
}

func TestResolveEnsembleProjectDirForSessionRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveEnsembleProjectDirForSession(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveEnsembleProjectDirForSessionFallsBackToProjectRoot(t *testing.T) {
	projectDir := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir ntm dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write ntm config: %v", err)
	}
	nestedDir := filepath.Join(projectDir, "nested")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	gotDir, err := resolveEnsembleProjectDirForSession(t.Context(), "")
	if err != nil {
		t.Fatalf("resolveEnsembleProjectDirForSession() error = %v", err)
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
}

func TestResolveGitProjectDirRejectsInvalidSessionName(t *testing.T) {
	_, _, err := resolveGitProjectDir(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveProfileSwitchProjectDirRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveProfileSwitchProjectDirContext(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestRunProfileSwitchRejectsInvalidSessionName(t *testing.T) {
	err := runProfileSwitch(t.Context(), "cc_1", "reviewer", "../escape", "", true, true)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveScaleSessionRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveScaleSession(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveAddSessionRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveAddSession(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestRunEnsembleStatus_UsesPersistedStateWhenSessionOffline(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-ensemble-status",
		Question:          "What happened?",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	var buf bytes.Buffer
	if err := runEnsembleStatus(&buf, state.SessionName, ensembleStatusOptions{Format: "json"}); err != nil {
		t.Fatalf("runEnsembleStatus error: %v", err)
	}

	var out ensembleStatusOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal status output: %v", err)
	}
	if !out.Exists {
		t.Fatal("expected status output to exist for persisted offline session")
	}
	if out.Status != ensemble.EnsembleStopped.String() {
		t.Fatalf("status = %q, want %q", out.Status, ensemble.EnsembleStopped)
	}
	if !out.SynthesisReady {
		t.Fatal("expected synthesis_ready=true when persisted offline session has completed outputs")
	}
}

func TestRunEnsembleStatus_AllErrorSessionNotSynthesisReady(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-ensemble-errors",
		Question:          "Why did this fail?",
		Status:            ensemble.EnsembleError,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentError, Error: "failed"},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	var buf bytes.Buffer
	if err := runEnsembleStatus(&buf, state.SessionName, ensembleStatusOptions{Format: "json"}); err != nil {
		t.Fatalf("runEnsembleStatus error: %v", err)
	}

	var out ensembleStatusOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal status output: %v", err)
	}
	if out.SynthesisReady {
		t.Fatal("expected synthesis_ready=false when all assignments errored")
	}
	if out.StatusCounts.Error != 1 {
		t.Fatalf("error count = %d, want 1", out.StatusCounts.Error)
	}
}

func TestRunEnsembleStop_MarksOfflineActiveStateStopped(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-ensemble-stop",
		Question:          "Stop this orphaned run",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentActive},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	var buf bytes.Buffer
	err := runEnsembleStop(&buf, state.SessionName, ensembleStopOptions{Format: "json", Yes: true})
	if err != nil {
		t.Fatalf("runEnsembleStop error: %v", err)
	}

	var out ensembleStopOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal stop output: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success, got message=%q error=%q", out.Message, out.Error)
	}
	if out.FinalStatus != ensemble.EnsembleStopped.String() {
		t.Fatalf("final status = %q, want %q", out.FinalStatus, ensemble.EnsembleStopped)
	}

	saved, err := ensemble.LoadSession(state.SessionName)
	if err != nil {
		t.Fatalf("LoadSession after stop error: %v", err)
	}
	if saved.Status != ensemble.EnsembleStopped {
		t.Fatalf("saved status = %q, want %q", saved.Status, ensemble.EnsembleStopped)
	}
}

func TestRunEnsembleSynthesize_UsesSavedOutputsWhenSessionOffline(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	outputPath := filepath.Join(t.TempDir(), "offline-synth-output.json")
	modeOutput := ensemble.ModeOutput{
		ModeID: "deductive",
		Thesis: "Offline synthesis thesis",
		TopFindings: []ensemble.Finding{{
			Finding:    "Offline synthesis finding",
			Impact:     ensemble.ImpactMedium,
			Confidence: 0.8,
		}},
		Confidence:  0.8,
		GeneratedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(modeOutput)
	if err != nil {
		t.Fatalf("marshal mode output: %v", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatalf("write mode output: %v", err)
	}

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-ensemble-synthesize",
		Question:          "Synthesize this offline run",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: outputPath},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	var buf bytes.Buffer
	if err := runEnsembleSynthesize(t.Context(), &buf, state.SessionName, synthesizeOptions{Format: "json"}); err != nil {
		t.Fatalf("runEnsembleSynthesize error: %v", err)
	}
	if !strings.Contains(buf.String(), "\"summary\"") {
		t.Fatalf("expected synthesized JSON output, got %q", buf.String())
	}
}

func TestRunEnsembleSynthesize_RejectsResumeWithoutStream(t *testing.T) {
	var buf bytes.Buffer
	err := runEnsembleSynthesize(t.Context(), &buf, "missing-session", synthesizeOptions{
		Resume: true,
	})
	if err == nil {
		t.Fatal("runEnsembleSynthesize() error = nil, want invalid flag error")
	}
	if !strings.Contains(err.Error(), "--resume requires --stream") {
		t.Fatalf("error = %v, want --resume requires --stream", err)
	}
}

func TestRunEnsembleSynthesize_RejectsResumeWithoutRunID(t *testing.T) {
	var buf bytes.Buffer
	err := runEnsembleSynthesize(t.Context(), &buf, "missing-session", synthesizeOptions{
		Stream: true,
		Resume: true,
	})
	if err == nil {
		t.Fatal("runEnsembleSynthesize() error = nil, want invalid flag error")
	}
	if !strings.Contains(err.Error(), "--resume requires --run-id") {
		t.Fatalf("error = %v, want --resume requires --run-id", err)
	}
}

func TestRunEnsembleSynthesize_RejectsRunIDWithoutStream(t *testing.T) {
	var buf bytes.Buffer
	err := runEnsembleSynthesize(t.Context(), &buf, "missing-session", synthesizeOptions{
		RunID: "checkpoint-run",
	})
	if err == nil {
		t.Fatal("runEnsembleSynthesize() error = nil, want invalid flag error")
	}
	if !strings.Contains(err.Error(), "--run-id requires --stream") {
		t.Fatalf("error = %v, want --run-id requires --stream", err)
	}
}

func TestResolveEnsembleStateCommandSession_ExplicitOfflineSession(t *testing.T) {
	res, err := resolveEnsembleStateCommandSession("offline-explicit-session", io.Discard)
	if err != nil {
		t.Fatalf("resolveEnsembleStateCommandSession() error = %v", err)
	}
	if res.Session != "offline-explicit-session" {
		t.Fatalf("Session = %q, want %q", res.Session, "offline-explicit-session")
	}
	if res.Inferred {
		t.Fatal("expected explicit session resolution, got inferred")
	}
}

func TestNewEnsembleSynthesizeCmd_AllowsExplicitOfflineSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	outputPath := filepath.Join(t.TempDir(), "offline-synth-cmd-output.json")
	modeOutput := ensemble.ModeOutput{
		ModeID: "deductive",
		Thesis: "Offline synth command thesis",
		TopFindings: []ensemble.Finding{{
			Finding:    "Offline synth command finding",
			Impact:     ensemble.ImpactMedium,
			Confidence: 0.8,
		}},
		Confidence:  0.8,
		GeneratedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(modeOutput)
	if err != nil {
		t.Fatalf("marshal mode output: %v", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatalf("write mode output: %v", err)
	}

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-synth-command-session",
		Question:          "Synthesize this explicit offline session",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: outputPath},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	cmd := newEnsembleSynthesizeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{state.SessionName, "--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "\"summary\"") {
		t.Fatalf("expected synthesized JSON output, got %q", out.String())
	}
}

func TestNewEnsembleStopCmd_AllowsExplicitOfflineSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-stop-command-session",
		Question:          "Stop this explicit offline session",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentActive},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	cmd := newEnsembleStopCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{state.SessionName, "--yes", "--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "\"success\": true") {
		t.Fatalf("expected successful stop JSON output, got %q", out.String())
	}
}

func TestNewEnsembleProvenanceCmd_AllowsExplicitOfflineSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	outputPath := filepath.Join(t.TempDir(), "offline-provenance-cmd-output.json")
	modeOutput := ensemble.ModeOutput{
		ModeID: "deductive",
		Thesis: "Offline provenance command thesis",
		TopFindings: []ensemble.Finding{{
			Finding:    "Offline provenance command finding",
			Impact:     ensemble.ImpactMedium,
			Confidence: 0.75,
		}},
		Confidence:  0.75,
		GeneratedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(modeOutput)
	if err != nil {
		t.Fatalf("marshal mode output: %v", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatalf("write mode output: %v", err)
	}

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-provenance-command-session",
		Question:          "Show provenance for this explicit offline session",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: outputPath},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	cmd := newEnsembleProvenanceCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--session", state.SessionName, "--all", "--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "\"chains\"") {
		t.Fatalf("expected provenance JSON output, got %q", out.String())
	}
}

func TestRunEnsembleProvenance_UsesSavedOutputsWhenSessionOffline(t *testing.T) {
	isolateSessionAgentStorage(t)
	ensemble.CloseDefaultStateStore()
	t.Cleanup(ensemble.CloseDefaultStateStore)

	outputPath := filepath.Join(t.TempDir(), "offline-provenance-output.json")
	modeOutput := ensemble.ModeOutput{
		ModeID: "deductive",
		Thesis: "Offline provenance thesis",
		TopFindings: []ensemble.Finding{{
			Finding:    "Offline provenance finding",
			Impact:     ensemble.ImpactMedium,
			Confidence: 0.75,
		}},
		Confidence:  0.75,
		GeneratedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(modeOutput)
	if err != nil {
		t.Fatalf("marshal mode output: %v", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		t.Fatalf("write mode output: %v", err)
	}

	state := &ensemble.EnsembleSession{
		SessionName:       "offline-ensemble-provenance",
		Question:          "Show provenance for this offline run",
		Status:            ensemble.EnsembleStopped,
		SynthesisStrategy: ensemble.StrategyConsensus,
		CreatedAt:         time.Now().UTC(),
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "pane-1", AgentType: "cc", Status: ensemble.AssignmentDone, OutputPath: outputPath},
		},
	}
	if err := ensemble.SaveSession("", state); err != nil {
		t.Fatalf("SaveSession error: %v", err)
	}

	var buf bytes.Buffer
	if err := runEnsembleProvenance(&buf, state.SessionName, "", provenanceOptions{Format: "json", Stats: true}); err != nil {
		t.Fatalf("runEnsembleProvenance error: %v", err)
	}
	if !strings.Contains(buf.String(), "\"stats\"") {
		t.Fatalf("expected provenance stats JSON, got %q", buf.String())
	}
}

func TestResolvePipelineProjectDirForSessionFallsBackToProjectRootFromNestedDir(t *testing.T) {
	projectDir := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0755); err != nil {
		t.Fatalf("mkdir ntm root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0644); err != nil {
		t.Fatalf("write ntm config: %v", err)
	}
	nestedDir := filepath.Join(projectDir, "nested")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	got, err := resolvePipelineProjectDirForSession(t.Context(), "")
	if err != nil {
		t.Fatalf("resolvePipelineProjectDirForSession() error = %v", err)
	}
	if got != projectDir {
		t.Fatalf("project dir = %q, want %q", got, projectDir)
	}
}

func TestResolvePipelineProjectDirForSessionResolvesProjectScopedPrefix(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	got, err := resolvePipelineProjectDirForSession(t.Context(), "mypro")
	if err != nil {
		t.Fatalf("resolvePipelineProjectDirForSession() error = %v", err)
	}
	if got != projectDir {
		t.Fatalf("project dir = %q, want %q", got, projectDir)
	}
}

func TestResolvePipelineSessionRejectsInvalidSessionName(t *testing.T) {
	_, err := resolvePipelineSession("../escape", nil)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveResumeScopeUsesSessionProjectDir(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	session, got, err := resolveResumeScope(t.Context(), "mysession", true)
	if err != nil {
		t.Fatalf("resolveResumeScope() error = %v", err)
	}
	if session != "mysession" {
		t.Fatalf("session = %q, want %q", session, "mysession")
	}
	if got != projectDir {
		t.Fatalf("project dir = %q, want %q", got, projectDir)
	}
}

func TestResolveResumeScopeRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	root, nested := createCLIWorkspaceProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	_, _, err := resolveResumeScope(t.Context(), "mysession", true)
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveResumeScopeUsesCurrentWorkspaceOnlyForStoredHandoff(t *testing.T) {
	isolateSessionAgentStorage(t)

	workspace := canonicalTempDir(t)
	handoffDir := filepath.Join(workspace, ".ntm", "handoffs", "mysession")
	if err := os.MkdirAll(handoffDir, 0o755); err != nil {
		t.Fatalf("mkdir handoff dir: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(workspace, "unregistered-projects")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	session, gotDir, err := resolveResumeScope(t.Context(), "mysession", true)
	if err != nil {
		t.Fatalf("resolveResumeScope() error = %v", err)
	}
	if session != "mysession" {
		t.Fatalf("session = %q, want mysession", session)
	}
	if gotDir != workspace {
		t.Fatalf("project dir = %q, want %q", gotDir, workspace)
	}
}

func TestResolveResumeScopeUsesProjectRootStoredHandoffFromNestedDirectory(t *testing.T) {
	isolateSessionAgentStorage(t)

	root, nested := createCLIWorkspaceProjectRoot(t)
	handoffDir := filepath.Join(root, ".ntm", "handoffs", "mysession--frontend")
	if err := os.MkdirAll(handoffDir, 0o755); err != nil {
		t.Fatalf("mkdir handoff dir: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "unregistered-projects")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	session, gotDir, err := resolveResumeScope(t.Context(), "mysession--front", true)
	if err != nil {
		t.Fatalf("resolveResumeScope() error = %v", err)
	}
	if session != "mysession--frontend" {
		t.Fatalf("session = %q, want mysession--frontend", session)
	}
	if gotDir != root {
		t.Fatalf("project dir = %q, want %q", gotDir, root)
	}
}

func TestResolveResumeScopeRejectsInvalidSessionName(t *testing.T) {
	_, _, err := resolveResumeScope(t.Context(), "../escape", true)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveResumeScopeResolvesStoredHandoffSessionPrefix(t *testing.T) {
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "mysession")
	handoffDir := filepath.Join(projectDir, ".ntm", "handoffs", "mysession--frontend")
	if err := os.MkdirAll(handoffDir, 0o755); err != nil {
		t.Fatalf("mkdir handoff dir: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	otherDir := t.TempDir()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	session, gotDir, err := resolveResumeScope(t.Context(), "myse--front", true)
	if err != nil {
		t.Fatalf("resolveResumeScope() error = %v", err)
	}
	if session != "mysession--frontend" {
		t.Fatalf("session = %q, want %q", session, "mysession--frontend")
	}
	if gotDir != projectDir {
		t.Fatalf("project dir = %q, want %q", gotDir, projectDir)
	}
}

func TestResolveResumeSourceProjectDirUsesHandoffSourceProject(t *testing.T) {
	sourceProject := t.TempDir()
	handoffPath := filepath.Join(sourceProject, ".ntm", "handoffs", "sourcesession", "2026-04-03-1200.yaml")
	if err := os.MkdirAll(filepath.Dir(handoffPath), 0o755); err != nil {
		t.Fatalf("mkdir handoff dir: %v", err)
	}
	if err := os.WriteFile(handoffPath, []byte("goal: test\nnow: now\n"), 0o644); err != nil {
		t.Fatalf("write handoff: %v", err)
	}

	projectsBase := t.TempDir()
	staleProject := filepath.Join(projectsBase, "newsession")
	if err := os.MkdirAll(staleProject, 0o755); err != nil {
		t.Fatalf("mkdir stale project: %v", err)
	}

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: projectsBase}
	t.Cleanup(func() { cfg = oldCfg })

	projectDir, err := resolveResumeSourceProjectDir(t.Context(), "newsession", "sourcesession", handoffPath, true)
	if err != nil {
		t.Fatalf("resolveResumeSourceProjectDir() error = %v", err)
	}
	if projectDir != sourceProject {
		t.Fatalf("project dir = %q, want %q", projectDir, sourceProject)
	}
}

func TestProjectDirFromHandoffPathSupportsArchive(t *testing.T) {
	projectDir := t.TempDir()
	archivedPath := filepath.Join(projectDir, ".ntm", "handoffs", "mysession", ".archive", "2026-04-03-1200.yaml")

	got, ok := projectDirFromHandoffPath(archivedPath)
	if !ok {
		t.Fatal("expected project dir to be inferred from archived handoff path")
	}
	if got != projectDir {
		t.Fatalf("project dir = %q, want %q", got, projectDir)
	}
}

func TestAddDataToBundleSanitizesArchivePath(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	file, err := addDataToBundle(zw, `dir\test.txt`, []byte("content"))
	if err != nil {
		t.Fatalf("addDataToBundle() error = %v", err)
	}
	if file.Path != "dir/test.txt" {
		t.Fatalf("manifest path = %q, want dir/test.txt", file.Path)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("zip entries = %d, want 1", len(zr.File))
	}
	if zr.File[0].Name != "dir/test.txt" {
		t.Fatalf("zip entry = %q, want dir/test.txt", zr.File[0].Name)
	}
}

func TestSupportBundleSessionPathRejectsTraversal(t *testing.T) {
	if _, err := supportBundleSessionPath("../escape", "snapshot.json"); err == nil {
		t.Fatal("expected unsafe session path error")
	}
}

func TestAddDataToBundleRejectsUnsafeArchivePath(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	t.Cleanup(func() {
		if err := zw.Close(); err != nil {
			t.Logf("close zip: %v", err)
		}
	})

	if _, err := addDataToBundle(zw, "../escape.txt", []byte("content")); err == nil {
		t.Fatal("expected unsafe archive path error")
	}
}

func TestResolveReplaySessionRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveReplaySession("history-session", "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveReplaySessionRejectsEmptyHistorySession(t *testing.T) {
	_, err := resolveReplaySession("", "")
	if err == nil {
		t.Fatal("expected empty session error")
	}
	if !strings.Contains(err.Error(), "history entry session is empty") {
		t.Fatalf("expected empty history session error, got %v", err)
	}
}

func TestRunSaveRejectsInvalidSessionName(t *testing.T) {
	var buf bytes.Buffer
	err := runSave(&buf, "../escape", t.TempDir(), 10, AgentFilter{All: true})
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestRunCopyRejectsInvalidSessionName(t *testing.T) {
	var buf bytes.Buffer
	err := runCopy(&buf, "../escape", AgentFilter{}, CopyOptions{Last: 10})
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestLogsCmdRejectsInvalidSessionNameInJSONMode(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	cmd := newLogsCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"../escape"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestRunSessionsSaveRejectsInvalidSessionName(t *testing.T) {
	err := runSessionsSave("../escape", sessionpkg.SaveOptions{})
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestRebalanceCmdRejectsInvalidSessionNameInJSONMode(t *testing.T) {
	cmd := newRebalanceCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"../escape", "--format", "json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestReviewQueueCmdRejectsInvalidSessionNameInJSONMode(t *testing.T) {
	cmd := newReviewQueueCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"../escape", "--format", "json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveCheckpointLiveSessionArgRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveCheckpointLiveSessionArg("../escape", nil)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveCheckpointStorageSessionArgRejectsInvalidSessionName(t *testing.T) {
	_, err := resolveCheckpointStorageSessionArg("../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveCheckpointStorageSessionArgAllowsOfflineSession(t *testing.T) {
	got, err := resolveCheckpointStorageSessionArg("mysession")
	if err != nil {
		t.Fatalf("resolveCheckpointStorageSessionArg() error = %v", err)
	}
	if got != "mysession" {
		t.Fatalf("session = %q, want %q", got, "mysession")
	}
}

func TestResolveCheckpointStorageSessionArgResolvesOfflinePrefixMatch(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	storage := checkpoint.NewStorage()
	if err := os.MkdirAll(filepath.Join(storage.BaseDir, "mysession"), 0o755); err != nil {
		t.Fatalf("mkdir storage session: %v", err)
	}

	got, err := resolveCheckpointStorageSessionArg("my")
	if err != nil {
		t.Fatalf("resolveCheckpointStorageSessionArg() error = %v", err)
	}
	if got != "mysession" {
		t.Fatalf("session = %q, want %q", got, "mysession")
	}
}

func TestResolveRollbackSessionsNormalizesExplicitPrefix(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	fullSession := "rollbackprefixsession"
	prefix := "rollbackprefix"
	workDir := t.TempDir()
	_ = tmux.KillSession(fullSession)
	if err := tmux.CreateSession(fullSession, workDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", fullSession, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(fullSession) })

	storageSession, liveSession, err := resolveRollbackSessions(prefix, nil, true)
	if err != nil {
		t.Fatalf("resolveRollbackSessions() error = %v", err)
	}
	if storageSession != fullSession {
		t.Fatalf("storage session = %q, want %q", storageSession, fullSession)
	}
	if liveSession != fullSession {
		t.Fatalf("live session = %q, want %q", liveSession, fullSession)
	}
}

func TestRollbackCmd_InvalidCheckpointReportsLoadFailure(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	storage := checkpoint.NewStorage()
	sessionName := "rollback-invalid"
	checkpointID := "broken-rb"
	cpDir := filepath.Join(storage.BaseDir, sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	cmd := newRollbackCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{sessionName, checkpointID, "--no-git"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want load failure")
	}
	if !strings.Contains(err.Error(), "loading checkpoint:") {
		t.Fatalf("error = %q, want loading checkpoint context", err)
	}
	if strings.Contains(err.Error(), "no checkpoint found matching") {
		t.Fatalf("error = %q, want exact invalid checkpoint load failure", err)
	}
}

func TestRunChangesNormalizesExplicitPrefix(t *testing.T) {
	origStore := tracker.GlobalFileChanges
	store := tracker.NewFileChangeStore(100)
	tracker.GlobalFileChanges = store
	t.Cleanup(func() { tracker.GlobalFileChanges = origStore })

	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "changeproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	oldJSON := jsonOutput
	cfg = &config.Config{ProjectsBase: projectsBase}
	jsonOutput = false
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	})

	store.Add(tracker.RecordedFileChange{
		Timestamp: time.Now(),
		Session:   "changeproject",
		Agents:    []string{"agent-1"},
		Change: tracker.FileChange{
			Path: filepath.Join(projectDir, "file.go"),
			Type: tracker.FileModified,
		},
	})

	out, err := captureStdout(t, func() error { return runChanges(t.Context(), "changep") })
	if err != nil {
		t.Fatalf("runChanges() error = %v", err)
	}
	if strings.Contains(out, "No file changes recorded.") {
		t.Fatalf("expected recorded change output, got %q", out)
	}
	if !strings.Contains(out, "agent-1") {
		t.Fatalf("expected agent name in output, got %q", out)
	}
}

func TestRunConflictsNormalizesExplicitPrefix(t *testing.T) {
	origStore := tracker.GlobalFileChanges
	store := tracker.NewFileChangeStore(100)
	tracker.GlobalFileChanges = store
	t.Cleanup(func() { tracker.GlobalFileChanges = origStore })

	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "conflictproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	oldJSON := jsonOutput
	cfg = &config.Config{ProjectsBase: projectsBase}
	jsonOutput = false
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	})

	path := filepath.Join(projectDir, "file.go")
	store.Add(tracker.RecordedFileChange{
		Timestamp: time.Now().Add(-2 * time.Minute),
		Session:   "conflictproject",
		Agents:    []string{"agent-1"},
		Change: tracker.FileChange{
			Path: path,
			Type: tracker.FileModified,
		},
	})
	store.Add(tracker.RecordedFileChange{
		Timestamp: time.Now().Add(-1 * time.Minute),
		Session:   "conflictproject",
		Agents:    []string{"agent-2"},
		Change: tracker.FileChange{
			Path: path,
			Type: tracker.FileModified,
		},
	})

	out, err := captureStdout(t, func() error { return runConflicts(t.Context(), "conflictp", "24h", 10) })
	if err != nil {
		t.Fatalf("runConflicts() error = %v", err)
	}
	if strings.Contains(out, "No conflicts detected.") {
		t.Fatalf("expected conflict output, got %q", out)
	}
	if !strings.Contains(out, "conflictproject") {
		t.Fatalf("expected session name in output, got %q", out)
	}
	if !strings.Contains(out, "file.go") {
		t.Fatalf("expected conflicted file in output, got %q", out)
	}
}

func TestRunContextRotationPendingNormalizesExplicitPrefix(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "pending.jsonl")
	origStore := ctxmon.DefaultPendingRotationStore
	ctxmon.DefaultPendingRotationStore = ctxmon.NewPendingRotationStoreWithPath(storePath)
	t.Cleanup(func() { ctxmon.DefaultPendingRotationStore = origStore })

	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, "rotationproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	oldCfg := cfg
	oldJSON := jsonOutput
	cfg = &config.Config{ProjectsBase: projectsBase}
	jsonOutput = true
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	})

	if err := ctxmon.AddPendingRotation(&ctxmon.PendingRotation{
		AgentID:        "agent-1",
		SessionName:    "rotationproject",
		ContextPercent: 91.2,
		CreatedAt:      time.Now(),
		TimeoutAt:      time.Now().Add(5 * time.Minute),
		DefaultAction:  ctxmon.ConfirmRotate,
		WorkDir:        projectDir,
	}); err != nil {
		t.Fatalf("AddPendingRotation() error = %v", err)
	}

	out, err := captureStdout(t, func() error { return runContextRotationPending(t.Context(), "rotationp") })
	if err != nil {
		t.Fatalf("runContextRotationPending() error = %v", err)
	}

	var result PendingRotationsResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; output=%q", err, out)
	}
	if result.Count != 1 {
		t.Fatalf("result.Count = %d, want 1", result.Count)
	}
	if len(result.Pending) != 1 {
		t.Fatalf("len(result.Pending) = %d, want 1", len(result.Pending))
	}
	if result.Pending[0].SessionName != "rotationproject" {
		t.Fatalf("result.Pending[0].SessionName = %q, want %q", result.Pending[0].SessionName, "rotationproject")
	}
}

func TestCheckpointListCmdRejectsInvalidSessionName(t *testing.T) {
	cmd := newCheckpointListCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"../escape"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestCheckpointSaveCmdRejectsInvalidSessionName(t *testing.T) {
	cmd := newCheckpointSaveCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"../escape"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestBuildAttachResponseRejectsInvalidSessionName(t *testing.T) {
	_, err := buildAttachResponse(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestBuildStatusResponseRejectsInvalidSessionName(t *testing.T) {
	_, err := buildStatusResponse(t.Context(), "../escape", statusOptions{})
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestBuildInterruptResponseRejectsInvalidSessionName(t *testing.T) {
	_, err := buildInterruptResponse(t.Context(), "../escape", nil)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestBuildKillResponseRejectsInvalidSessionName(t *testing.T) {
	_, err := buildKillResponse(t.Context(), "../escape", true, nil, true, false)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestBuildViewResponseRejectsInvalidSessionName(t *testing.T) {
	_, err := buildViewResponse(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestBuildZoomResponseRejectsInvalidSessionName(t *testing.T) {
	_, err := buildZoomResponse(t.Context(), "../escape", 1)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestRunExtractRejectsInvalidSessionName(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	err := runExtract("../escape", "", "", false, 10, false, false, 0)
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestRunExtractJSONFailureOwnsTerminalResult(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	stdout, err := captureStdout(t, func() error {
		return runExtract("../escape", "", "", false, 10, false, false, 0)
	})
	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runExtract error = %v, want errJSONFailure", err)
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("runExtract error = %v, want preserved validation cause", err)
	}

	var envelope output.ErrorResponse
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode exactly one JSON failure document: %v; stdout=%q", err, stdout)
	}
	if !strings.Contains(envelope.Error, "invalid session name") {
		t.Fatalf("failure envelope = %+v", envelope)
	}
}

func TestGetSessionWorkDirRejectsInvalidSessionName(t *testing.T) {
	_, err := getSessionWorkDir("../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveRobotFormat_NtmOutputFormatFallback(t *testing.T) {
	resetFlags()
	t.Setenv("NTM_ROBOT_FORMAT", "")
	t.Setenv("NTM_OUTPUT_FORMAT", "toon")
	t.Setenv("TOON_DEFAULT_FORMAT", "")

	if err := resolveRobotFormat(nil); err != nil {
		t.Fatalf("resolveRobotFormat() error = %v", err)
	}

	if robot.GetOutputFormat() != robot.FormatTOON {
		t.Errorf("OutputFormat from NTM_OUTPUT_FORMAT = %q, want %q", robot.GetOutputFormat(), robot.FormatTOON)
	}
}

func TestResolveRobotFormat_ToonDefaultFallback(t *testing.T) {
	resetFlags()
	t.Setenv("NTM_ROBOT_FORMAT", "")
	t.Setenv("NTM_OUTPUT_FORMAT", "")
	t.Setenv("TOON_DEFAULT_FORMAT", "toon")

	if err := resolveRobotFormat(nil); err != nil {
		t.Fatalf("resolveRobotFormat() error = %v", err)
	}

	if robot.GetOutputFormat() != robot.FormatTOON {
		t.Errorf("OutputFormat from TOON_DEFAULT_FORMAT = %q, want %q", robot.GetOutputFormat(), robot.FormatTOON)
	}
}

func TestResolveRobotFormat_FlagOverridesEnv(t *testing.T) {
	resetFlags()
	t.Setenv("NTM_ROBOT_FORMAT", "toon")
	robotFormat = "json"

	if err := resolveRobotFormat(nil); err != nil {
		t.Fatalf("resolveRobotFormat() error = %v", err)
	}

	if robot.GetOutputFormat() != robot.FormatJSON {
		t.Errorf("OutputFormat from flag = %q, want %q", robot.GetOutputFormat(), robot.FormatJSON)
	}
}

func TestResolveRobotFormat_InvalidValueFallsBack(t *testing.T) {
	resetFlags()
	robotFormat = "xml"

	if err := resolveRobotFormat(nil); err == nil {
		t.Fatal("resolveRobotFormat() error = nil, want invalid format error")
	}

	if robot.GetOutputFormat() != robot.FormatAuto {
		t.Errorf("OutputFormat invalid = %q, want %q", robot.GetOutputFormat(), robot.FormatAuto)
	}
}

func TestIsSessionMissingError(t *testing.T) {

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "cant find session", err: os.ErrNotExist, want: false},
		{name: "tmux cant find session", err: errors.New("can't find session: ntm"), want: true},
		{name: "session not found", err: errors.New("session not found: ntm"), want: true},
		{name: "has-session output", err: errors.New("tmux has-session -t ntm: exit status 1"), want: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := isSessionMissingError(tc.err); got != tc.want {
				t.Fatalf("isSessionMissingError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestResolveRobotFormat_ConfigFallback(t *testing.T) {
	resetFlags()
	t.Setenv("NTM_ROBOT_FORMAT", "")
	t.Setenv("NTM_OUTPUT_FORMAT", "")
	t.Setenv("TOON_DEFAULT_FORMAT", "")

	cfg := &config.Config{
		Robot: config.RobotConfig{
			Output: config.RobotOutputConfig{
				Format: "toon",
			},
		},
	}

	if err := resolveRobotFormat(cfg); err != nil {
		t.Fatalf("resolveRobotFormat() error = %v", err)
	}

	if robot.GetOutputFormat() != robot.FormatTOON {
		t.Errorf("OutputFormat from config = %q, want %q", robot.GetOutputFormat(), robot.FormatTOON)
	}
}

func TestRobotOutputFormatFlagAliasRegistered(t *testing.T) {
	if rootCmd.Flags().Lookup("robot-output-format") == nil {
		t.Fatal("expected --robot-output-format flag to be registered (alias for --robot-format)")
	}
}

func TestRobotProductivityFlagsRegistered(t *testing.T) {
	for _, name := range []string{"robot-productivity", "productivity-window", "converged-streak"} {
		if rootCmd.Flags().Lookup(name) == nil {
			t.Fatalf("--%s is not registered", name)
		}
	}
}

func TestRobotProxyStatusFlagRegistered(t *testing.T) {
	if rootCmd.Flags().Lookup("robot-proxy-status") == nil {
		t.Fatal("expected --robot-proxy-status flag to be registered")
	}
}

func TestSmartRestartHardKillFlagsRegistered(t *testing.T) {
	if rootCmd.Flags().Lookup("hard-kill") == nil {
		t.Fatal("expected --hard-kill flag to be registered")
	}
	if rootCmd.Flags().Lookup("hard-kill-only") == nil {
		t.Fatal("expected --hard-kill-only flag to be registered")
	}
}

func TestAtomicRobotAssignmentReservationFlagsRegistered(t *testing.T) {
	for _, name := range []string{"require-reservation", "reservation-paths"} {
		if rootCmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag to be registered", name)
		}
	}
}

func TestParseRobotReservationPathsArgStrict(t *testing.T) {
	paths, err := parseRobotReservationPathsArg("internal/robot/**, docs/**,internal/robot/**")
	if err != nil {
		t.Fatalf("parse paths: %v", err)
	}
	if len(paths) != 2 || paths[0] != "internal/robot/**" || paths[1] != "docs/**" {
		t.Fatalf("paths=%v", paths)
	}
	for _, raw := range []string{"internal/**,", ",internal/**", "internal/**,,docs/**"} {
		if _, err := parseRobotReservationPathsArg(raw); err == nil {
			t.Fatalf("parseRobotReservationPathsArg(%q) succeeded", raw)
		}
	}
}

func TestSharedDryRunAndVerboseHelpMentionsCurrentCommands(t *testing.T) {
	dryRunFlag := rootCmd.Flags().Lookup("dry-run")
	if dryRunFlag == nil {
		t.Fatal("expected --dry-run flag to be registered")
	}
	for _, want := range []string{"--robot-smart-restart", "--robot-pipeline-run", "--robot-replay"} {
		if !strings.Contains(dryRunFlag.Usage, want) {
			t.Fatalf("--dry-run usage missing %q: %q", want, dryRunFlag.Usage)
		}
	}

	verboseFlag := rootCmd.Flags().Lookup("verbose")
	if verboseFlag == nil {
		t.Fatal("expected --verbose flag to be registered")
	}
	for _, want := range []string{"--robot-is-working", "--robot-agent-health", "--robot-smart-restart"} {
		if !strings.Contains(verboseFlag.Usage, want) {
			t.Fatalf("--verbose usage missing %q: %q", want, verboseFlag.Usage)
		}
	}
}

// sessionAutoSelectPossible returns true if the CLI would auto-select a session.
// This happens when exactly one tmux session is running.
func sessionAutoSelectPossible() bool {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return false
	}
	return len(sessions) == 1
}

// TestExecuteHelp verifies that the root command executes successfully
func TestExecuteHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--help"})

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() with --help failed: %v", err)
	}
}

// TestVersionCmdExecutes tests the version subcommand runs without error
func TestVersionCmdExecutes(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"default version", []string{"version"}},
		{"short version", []string{"version", "--short"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs(tt.args)

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("Execute() failed: %v", err)
			}
		})
	}
}

// TestConfigPathCmdExecutes tests the config path subcommand runs
func TestConfigPathCmdExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"config", "path"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestConfigShowCmdExecutes tests the config show subcommand runs
func TestConfigShowCmdExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"config", "show"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

func TestConfigShowJSONIncludesSafetyProfile(t *testing.T) {
	resetFlags()
	t.Setenv("NTM_CONFIG", "")
	previousCfg := cfg
	previousCfgFile := cfgFile
	t.Cleanup(func() {
		cfg = previousCfg
		cfgFile = previousCfgFile
		startup.ResetConfig()
	})

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[assign]\noperator_gated_labels = [\"security-review\"]\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(config) failed: %v", err)
	}
	cfg = nil
	startup.ResetConfig()

	output, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"--json", "--config", configPath, "config", "show"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	safetyAny, ok := parsed["safety"]
	if !ok {
		t.Fatalf("expected safety key in output")
	}
	safety, ok := safetyAny.(map[string]any)
	if !ok {
		t.Fatalf("expected safety to be object, got %T", safetyAny)
	}

	profile, _ := safety["profile"].(string)
	if profile == "" {
		t.Fatalf("expected safety.profile to be non-empty")
	}
	if profile != config.SafetyProfileStandard {
		t.Fatalf("safety.profile = %q, want %q", profile, config.SafetyProfileStandard)
	}

	preflight, ok := safety["preflight"].(map[string]any)
	if !ok {
		t.Fatalf("expected safety.preflight to be object, got %T", safety["preflight"])
	}
	if enabled, ok := preflight["enabled"].(bool); !ok || !enabled {
		t.Fatalf("expected safety.preflight.enabled=true, got %v", preflight["enabled"])
	}

	if _, exists := parsed["health"]; exists {
		t.Fatal("config show JSON still exposes the removed top-level health object")
	}
	resilience, ok := parsed["resilience"].(map[string]any)
	if !ok {
		t.Fatalf("expected resilience object, got %T", parsed["resilience"])
	}
	for _, key := range []string{"auto_restart", "max_restarts", "health_check_seconds"} {
		if _, exists := resilience[key]; !exists {
			t.Fatalf("resilience JSON omitted %q: %#v", key, resilience)
		}
	}
	assign, ok := parsed["assign"].(map[string]any)
	if !ok {
		t.Fatalf("expected assign object, got %T", parsed["assign"])
	}
	labels, ok := assign["operator_gated_labels"].([]any)
	if !ok || len(labels) != 1 || labels[0] != "security-review" {
		t.Fatalf("assign.operator_gated_labels = %#v", assign["operator_gated_labels"])
	}
}

// TestDepsCmdExecutes tests the deps command runs
func TestDepsCmdExecutes(t *testing.T) {
	fakeToolsDir := filepath.Join(repoRoot(t), "testdata", "faketools")
	toolDir := t.TempDir()
	writeFakeVersionTool(t, toolDir, "tmux", "tmux 3.4")

	t.Setenv("PATH", toolDir+":"+fakeToolsDir+":"+os.Getenv("PATH"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")

	resetFlags()
	rootCmd.SetArgs([]string{"--json", "deps"})

	out, err := captureStdout(t, func() error {
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected deps command to emit JSON output")
	}
}

func TestDepsCmdSmoke(t *testing.T) {
	fakeToolsDir := filepath.Join(repoRoot(t), "testdata", "faketools")
	toolDir := t.TempDir()

	for _, tool := range []struct {
		name    string
		version string
	}{
		{name: "tmux", version: "tmux 3.4"},
		{name: "claude", version: "claude 1.0.0"},
		{name: "codex", version: "codex 0.1.0"},
		{name: "gemini", version: "gemini 0.9.0"},
		{name: "fzf", version: "fzf 0.55.0"},
		{name: "git", version: "git version 2.49.0"},
	} {
		writeFakeVersionTool(t, toolDir, tool.name, tool.version)
	}

	t.Setenv("PATH", toolDir+":"+fakeToolsDir+":"+os.Getenv("PATH"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")

	t.Run("json output", func(t *testing.T) {
		resetFlags()

		out, err := captureStdout(t, func() error {
			rootCmd.SetArgs([]string{"--json", "deps"})
			return rootCmd.Execute()
		})
		if err != nil {
			t.Fatalf("Execute() failed: %v", err)
		}

		var resp output.DepsResponse
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("unmarshal deps JSON: %v\noutput=%s", err, out)
		}
		if !resp.AllInstalled {
			t.Fatalf("AllInstalled = false, want true")
		}

		depsByName := make(map[string]output.DependencyCheck, len(resp.Dependencies))
		for _, dep := range resp.Dependencies {
			depsByName[dep.Name] = dep
		}

		for _, name := range []string{"tmux", "Claude Code", "OpenAI Codex", "Gemini CLI (legacy)", "fzf", "git"} {
			dep, ok := depsByName[name]
			if !ok {
				t.Fatalf("missing dependency %q in response: %+v", name, resp.Dependencies)
			}
			if !dep.Installed {
				t.Fatalf("dependency %q marked not installed", name)
			}
			if dep.Path == "" {
				t.Fatalf("dependency %q missing path", name)
			}
		}

		agentMail, ok := depsByName["Agent Mail"]
		if !ok {
			t.Fatalf("missing Agent Mail check in response: %+v", resp.Dependencies)
		}
		if agentMail.Installed {
			t.Fatalf("Agent Mail should be unavailable in smoke test response")
		}
	})

	t.Run("verbose text output", func(t *testing.T) {
		resetFlags()

		out, err := captureStdout(t, func() error {
			rootCmd.SetArgs([]string{"deps", "-v"})
			return rootCmd.Execute()
		})
		if err != nil {
			t.Fatalf("Execute() failed: %v", err)
		}

		plain := status.StripANSI(out)
		for _, want := range []string{
			"NTM Dependency Check",
			"Required:",
			"tmux",
			"AI Agents:",
			"Claude Code",
			"OpenAI Codex",
			"Gemini CLI",
			"Recommended:",
			"fzf",
			"git",
			"Services:",
			"Agent Mail",
			"Flywheel Tools:",
			"All required dependencies installed.",
		} {
			if !strings.Contains(plain, want) {
				t.Fatalf("verbose deps output missing %q\noutput:\n%s", want, plain)
			}
		}
	})
}

func TestDepsCmdSmokeMissingOptionalTools(t *testing.T) {
	toolDir := t.TempDir()
	writeFakeVersionTool(t, toolDir, "tmux", "tmux 3.4")

	t.Setenv("PATH", toolDir)
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")

	resetFlags()

	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"deps", "-v"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}

	plain := status.StripANSI(out)
	for _, want := range []string{
		"tmux",
		"Claude Code",
		"Install: npm install -g @anthropic-ai/claude-code",
		"OpenAI Codex",
		"Install: npm install -g @openai/codex",
		"Gemini CLI",
		"Install: npm install -g @google/gemini-cli",
		"Agent Mail",
		"Flywheel Tools:",
		"No AI agents installed.",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing expected degraded deps output %q\noutput:\n%s", want, plain)
		}
	}
}

func TestCheckDepWithPathKeepsVersionOutputOnNonZeroExit(t *testing.T) {
	toolDir := t.TempDir()
	expectedPath := filepath.Join(toolDir, "codex")
	writeFakeVersionToolWithExit(t, toolDir, "codex", "codex-cli 0.999.0", 17)

	t.Setenv("PATH", toolDir)

	status, version, path := checkDepWithPath(depCheck{
		Name:        "OpenAI Codex",
		Command:     "codex",
		VersionArgs: []string{"--version"},
	})

	if status != "found" {
		t.Fatalf("status = %q, want %q", status, "found")
	}
	if version != "codex-cli 0.999.0" {
		t.Fatalf("version = %q, want %q", version, "codex-cli 0.999.0")
	}
	if path != expectedPath {
		t.Fatalf("path = %q, want %q", path, expectedPath)
	}
}

func TestCheckDepWithPathReturnsInstalledWithoutVersionWhenCommandIsSilent(t *testing.T) {
	toolDir := t.TempDir()
	expectedPath := filepath.Join(toolDir, "gemini")
	writeFakeVersionToolWithExit(t, toolDir, "gemini", "", 9)

	t.Setenv("PATH", toolDir)

	status, version, path := checkDepWithPath(depCheck{
		Name:        "Gemini CLI",
		Command:     "gemini",
		VersionArgs: []string{"--version"},
	})

	if status != "found" {
		t.Fatalf("status = %q, want %q", status, "found")
	}
	if version != "" {
		t.Fatalf("version = %q, want empty string", version)
	}
	if path != expectedPath {
		t.Fatalf("path = %q, want %q", path, expectedPath)
	}
}

func TestSanitizeDependencyVersion(t *testing.T) {
	raw := "\x1b]0;spoofed title\a\x1b[31mgrok 0.2.89\x1b[0m\n\tbuild 8b63\u202e\x00"
	if got, want := sanitizeDependencyVersion(raw), "grok 0.2.89 build 8b63"; got != want {
		t.Fatalf("sanitizeDependencyVersion() = %q, want %q", got, want)
	}

	long := strings.Repeat("v", 300)
	got := sanitizeDependencyVersion(long)
	if len([]rune(strings.TrimSuffix(got, "..."))) != 240 || !strings.HasSuffix(got, "...") {
		t.Fatalf("long sanitized version has unexpected bound: %d runes, suffix=%q", len([]rune(got)), got[len(got)-3:])
	}
}

func TestGrokDependencyChecksAreOptionalAndSanitized(t *testing.T) {
	var found bool
	for _, dep := range defaultDepChecks() {
		if dep.Command != "grok" {
			continue
		}
		found = true
		if dep.Required || dep.Category != "AI Agents" || !reflect.DeepEqual(dep.VersionArgs, []string{"--version"}) {
			t.Fatalf("unexpected Grok dependency contract: %+v", dep)
		}
	}
	if !found {
		t.Fatal("default dependency checks omit Grok Build")
	}

	var doctorGrok *DepCheck
	checks := checkDependencies(t.Context())
	for i := range checks {
		if checks[i].Name == "grok" {
			doctorGrok = &checks[i]
			break
		}
	}
	if doctorGrok == nil || doctorGrok.Status != "ok" {
		t.Fatalf("doctor Grok dependency = %+v, want optional ok check", doctorGrok)
	}
	if doctorGrok.Version != sanitizeDependencyVersion(doctorGrok.Version) {
		t.Fatalf("doctor Grok version is not sanitized: %q", doctorGrok.Version)
	}
}

func TestCheckDepWithPathSanitizesFakeGrokVersion(t *testing.T) {
	toolDir := t.TempDir()
	toolPath := filepath.Join(toolDir, "grok")
	script := "#!/bin/sh\nprintf '\\033[31mgrok 9.9.9\\033[0m\\nsecond\\tfield\\n'\n"
	if err := os.WriteFile(toolPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake Grok binary: %v", err)
	}
	t.Setenv("PATH", toolDir)

	status, version, path := checkDepWithPath(depCheck{
		Name: "Grok Build (xAI)", Command: "grok", VersionArgs: []string{"--version"},
	})
	if status != "found" || version != "grok 9.9.9 second field" || path != toolPath {
		t.Fatalf("fake Grok dependency = status:%q version:%q path:%q", status, version, path)
	}
}

func writeFakeVersionTool(t *testing.T, dir, name, version string) {
	t.Helper()
	writeFakeVersionToolWithExit(t, dir, name, version, 0)
}

// scaleDepVersionTimeout widens the production `--version` budget for tests
// that shell out to a fake tool, restoring it afterwards.
//
// The production 2s is right for `ntm deps`, which must not hang. But these
// tests spend that budget on SHELL STARTUP for a throwaway script, and on a
// machine also running an agent swarm that alone can exceed 2s — the command
// is killed, the version comes back empty, and the test fails while
// checkDepWithPath is behaving correctly (bd-hzmk0). Safe to mutate directly:
// no test in this file calls t.Parallel().
func scaleDepVersionTimeout(t *testing.T) {
	t.Helper()
	original := depVersionTimeout
	depVersionTimeout = testutil.ScaleTimeout(4 * time.Second)
	t.Cleanup(func() { depVersionTimeout = original })
}

func writeFakeVersionToolWithExit(t *testing.T, dir, name, version string, exitCode int) {
	t.Helper()
	scaleDepVersionTimeout(t)

	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n"
	if version != "" {
		script += fmt.Sprintf("printf '%%s\\n' %q\n", version)
	}
	script += fmt.Sprintf("exit %d\n", exitCode)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// TestListCmdExecutes tests list command executes
func TestListCmdExecutes(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	resetFlags()
	rootCmd.SetArgs([]string{"list"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestListCmdJSONExecutes tests list command with JSON output executes
func TestListCmdJSONExecutes(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	resetFlags()
	rootCmd.SetArgs([]string{"list", "--json"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestSpawnValidation tests spawn command argument validation
func TestSpawnValidation(t *testing.T) {
	// Initialize config for spawn command
	cfg = config.Default()

	tests := []struct {
		name        string
		args        []string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "missing session name",
			args:        []string{"spawn"},
			expectError: true,
			errorMsg:    "accepts 1 arg",
		},
		{
			name:        "no agents specified",
			args:        []string{"spawn", "testproject"},
			expectError: true,
			errorMsg:    "no agents specified",
		},
		{
			name:        "invalid session name with colon",
			args:        []string{"spawn", "test:project", "--cc=1"},
			expectError: true,
			errorMsg:    "cannot contain ':'",
		},
		{
			name:        "invalid session name with dot",
			args:        []string{"spawn", "test.project", "--cc=1"},
			expectError: true,
			errorMsg:    "cannot contain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs(tt.args)

			err := rootCmd.Execute()

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing %q, got: %v", tt.errorMsg, err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestSessionsCreateKernelHandlerDecodesMapInputAndValidatesLabelShape(t *testing.T) {
	resetFlags()

	// Under the canonical LAST-"--" split (internal/config/label.go), the base
	// of "project--bad--label" is "project--bad", which trips the reserved
	// project-name validation.
	_, err := kernel.Run(context.Background(), "sessions.create", map[string]interface{}{
		"session": "project--bad--label",
		"panes":   1,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved-separator project-name error, got %v", err)
	}

	// A malformed label (bad shape, no extra separator) still fails label validation.
	_, err = kernel.Run(context.Background(), "sessions.create", map[string]interface{}{
		"session": "project--bad!label",
		"panes":   1,
	})
	if err == nil {
		t.Fatal("expected label validation error")
	}
	if !strings.Contains(err.Error(), "invalid label") {
		t.Fatalf("expected invalid-label validation error, got %v", err)
	}
}

func TestAddValidationRejectsReservedLabelSeparatorInProjectName(t *testing.T) {
	cfg = config.Default()

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "without label",
			args: []string{"add", "my--project", "--cc=1"},
		},
		{
			name: "with label",
			args: []string{"add", "my--project", "--label", "frontend", "--cc=1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs(tt.args)

			err := rootCmd.Execute()
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), "contains '--'") {
				t.Fatalf("expected reserved-separator validation error, got: %v", err)
			}
		})
	}
}

func TestAddValidationRejectsInvalidSessionNames(t *testing.T) {
	cfg = config.Default()

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "colon",
			args:    []string{"add", "test:project", "--cc=1"},
			wantErr: "cannot contain ':'",
		},
		{
			name:    "dot",
			args:    []string{"add", "test.project", "--cc=1"},
			wantErr: "cannot contain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs(tt.args)

			err := rootCmd.Execute()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestIsJSONOutput tests the JSON output detection
func TestIsJSONOutput(t *testing.T) {
	// Save original value
	original := jsonOutput
	defer func() { jsonOutput = original }()

	jsonOutput = false
	if IsJSONOutput() {
		t.Error("Expected IsJSONOutput() to return false")
	}

	jsonOutput = true
	if !IsJSONOutput() {
		t.Error("Expected IsJSONOutput() to return true")
	}
}

// TestGetFormatter tests the formatter creation
func TestGetFormatter(t *testing.T) {
	formatter := GetFormatter()
	if formatter == nil {
		t.Fatal("Expected non-nil formatter")
	}
}

// TestBuildInfo tests that build info variables are set
func TestBuildInfo(t *testing.T) {
	// These should have default values even if not set by build
	if Version == "" {
		t.Error("Version should not be empty")
	}
	if Commit == "" {
		t.Error("Commit should not be empty")
	}
	if Date == "" {
		t.Error("Date should not be empty")
	}
	if BuiltBy == "" {
		t.Error("BuiltBy should not be empty")
	}
}

// TestRobotVersionExecutes tests robot-version flag executes
func TestRobotVersionExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--robot-version"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestRobotHelpExecutes tests robot-help flag executes
func TestRobotHelpExecutes(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--robot-help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestRobotStatusExecutes tests the robot-status flag executes
func TestRobotStatusExecutes(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-status"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestRobotSnapshotExecutes tests the robot-snapshot flag executes
func TestRobotSnapshotExecutes(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-snapshot"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestRobotPlanExecutes tests the robot-plan flag executes
func TestRobotPlanExecutes(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-plan"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestAttachCmdNoArgs tests attach command without arguments
func TestAttachCmdNoArgs(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Initialize config
	cfg = config.Default()
	resetFlags()
	rootCmd.SetArgs([]string{"attach"})

	err := rootCmd.Execute()
	// Should not error - just lists sessions
	if err != nil && !strings.Contains(err.Error(), "no server running") {
		t.Logf("Attach without args result: %v", err)
	}
}

// TestStatusCmdRequiresArg tests status command requires session name
func TestStatusCmdRequiresArg(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"status"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for status without session name")
	}
	if !strings.Contains(err.Error(), "accepts 1 arg") {
		t.Errorf("Expected 'accepts 1 arg' error, got: %v", err)
	}
}

func TestResolveAddSetupScopeResolvesProjectScopedPrefix(t *testing.T) {
	isolateSessionAgentStorage(t)

	oldCfg := cfg
	origWd, _ := os.Getwd()
	cfg = config.Default()
	t.Cleanup(func() {
		cfg = oldCfg
		_ = os.Chdir(origWd)
	})

	base := t.TempDir()
	projectsBase := filepath.Join(base, "projects")
	projectDir := filepath.Join(projectsBase, "demo")
	if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(project .beads) failed: %v", err)
	}
	cfg.ProjectsBase = projectsBase

	unrelatedWd := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(unrelatedWd, 0o755); err != nil {
		t.Fatalf("MkdirAll(unrelated wd) failed: %v", err)
	}
	if err := os.Chdir(unrelatedWd); err != nil {
		t.Fatalf("Chdir(unrelated wd) failed: %v", err)
	}

	resolvedSession, dir, err := resolveAddSetupScope(t.Context(), "de")
	if err == nil {
		if resolvedSession != "demo" {
			t.Fatalf("resolved session = %q, want %q", resolvedSession, "demo")
		}
		if dir != projectDir {
			t.Fatalf("project dir = %q, want %q", dir, projectDir)
		}
		return
	}
	t.Fatalf("resolveAddSetupScope() error = %v", err)
}

func TestResolveAddSetupScopeRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	oldCfg := cfg
	origWd, _ := os.Getwd()
	cfg = config.Default()
	t.Cleanup(func() {
		cfg = oldCfg
		_ = os.Chdir(origWd)
	})

	root, nested := createCLIWorkspaceProjectRoot(t)
	cfg.ProjectsBase = filepath.Join(root, "projects-base")

	if err := os.Chdir(nested); err != nil {
		t.Fatalf("Chdir(workspace nested) failed: %v", err)
	}

	_, _, err := resolveAddSetupScope(t.Context(), "demo")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if got := err.Error(); !strings.Contains(got, "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveAddSetupScopeRejectsAmbiguousProjectScopedPrefix(t *testing.T) {
	isolateSessionAgentStorage(t)

	oldCfg := cfg
	origWd, _ := os.Getwd()
	cfg = config.Default()
	t.Cleanup(func() {
		cfg = oldCfg
		_ = os.Chdir(origWd)
	})

	base := t.TempDir()
	projectsBase := filepath.Join(base, "projects")
	for _, name := range []string{"demo", "design"} {
		projectDir := filepath.Join(projectsBase, name)
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s project dir) failed: %v", name, err)
		}
	}
	cfg.ProjectsBase = projectsBase

	unrelatedWd := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(unrelatedWd, 0o755); err != nil {
		t.Fatalf("MkdirAll(unrelated wd) failed: %v", err)
	}
	if err := os.Chdir(unrelatedWd); err != nil {
		t.Fatalf("Chdir(unrelated wd) failed: %v", err)
	}

	_, _, err := resolveAddSetupScope(t.Context(), "de")
	if err == nil {
		t.Fatal("expected ambiguous prefix error")
	}
	if got := err.Error(); !strings.Contains(got, "ambiguous") {
		t.Fatalf("expected ambiguous prefix error, got %v", err)
	}
}

// TestAddCmdRequiresSession tests add command requires session name
func TestAddCmdRequiresSession(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"add"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for add without session name")
	}
}

// TestZoomCmdRequiresArgs tests zoom command requires arguments
func TestZoomCmdRequiresArgs(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"zoom"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for zoom without arguments")
	}
}

// TestSendCmdRequiresArgs tests send command requires arguments
func TestSendCmdRequiresArgs(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"send"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for send without arguments")
	}
}

// TestCompletionCmdExecutes tests completion subcommand executes
func TestCompletionCmdExecutes(t *testing.T) {
	shells := []string{"bash", "zsh", "fish", "powershell"}

	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs([]string{"completion", shell})

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("completion %s failed: %v", shell, err)
			}
		})
	}
}

// TestShellCmdExecutes tests shell subcommand for shell integration executes
func TestShellCmdExecutes(t *testing.T) {
	shells := []string{"bash", "zsh"}

	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			resetFlags()
			rootCmd.SetArgs([]string{"shell", shell})

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("shell %s failed: %v", shell, err)
			}
		})
	}
}

// TestKillCmdRequiresSession tests kill command requires session name
func TestKillCmdRequiresSession(t *testing.T) {
	// Isolate environment
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	defer os.Chdir(oldWd)
	oldTmux := os.Getenv("TMUX")
	os.Unsetenv("TMUX")
	defer os.Setenv("TMUX", oldTmux)

	resetFlags()
	rootCmd.SetArgs([]string{"kill"})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for kill without session name")
	}
}

// TestViewCmdRequiresSession tests view command requires session name
func TestViewCmdRequiresSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Isolate environment
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	defer os.Chdir(oldWd)
	oldTmux := os.Getenv("TMUX")
	os.Unsetenv("TMUX")
	defer os.Setenv("TMUX", oldTmux)

	if sessionAutoSelectPossible() {
		t.Skip("Skipping: exactly one tmux session running (auto-selection applies)")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"view"})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	err := rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for view without session name, but got success. Output: %s", buf.String())
	}
}

// TestCopyCmdRequiresSession tests copy command requires session name
// when no session can be auto-selected (0 or 2+ sessions running).
func TestCopyCmdRequiresSession(t *testing.T) {
	// Isolate environment FIRST to ensure sessionAutoSelectPossible behaves correctly if it depends on CWD/Env
	// But sessionAutoSelectPossible uses tmux list-sessions, which connects to server.
	// We only need to block INFERENCE.
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	defer os.Chdir(oldWd)
	oldTmux := os.Getenv("TMUX")
	os.Unsetenv("TMUX")
	defer os.Setenv("TMUX", oldTmux)

	if sessionAutoSelectPossible() {
		t.Skip("Skipping: exactly one tmux session running (auto-selection applies)")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"copy"})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for copy without session name")
	}
}

// TestSaveCmdRequiresSession tests save command requires session name
// when no session can be auto-selected (0 or 2+ sessions running).
func TestSaveCmdRequiresSession(t *testing.T) {
	// Isolate environment
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	defer os.Chdir(oldWd)
	oldTmux := os.Getenv("TMUX")
	os.Unsetenv("TMUX")
	defer os.Setenv("TMUX", oldTmux)

	if sessionAutoSelectPossible() {
		t.Skip("Skipping: exactly one tmux session running (auto-selection applies)")
	}

	resetFlags()
	rootCmd.SetArgs([]string{"save"})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	err := rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for save without session name, but got success. Output: %s", buf.String())
	}
}

// TestTutorialCmdHelp tests the tutorial command help
func TestTutorialCmdHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"tutorial", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("tutorial --help failed: %v", err)
	}
}

// TestDashboardCmdHelp tests the dashboard command help
func TestDashboardCmdHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"dashboard", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("dashboard --help failed: %v", err)
	}
}

// TestPaletteCmdHelp tests the palette command help
func TestPaletteCmdHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"palette", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("palette --help failed: %v", err)
	}
}

func TestPaletteStatePathUsesSelectedConfigPath(t *testing.T) {
	oldCfgFile := cfgFile
	cfgFile = ""
	t.Cleanup(func() {
		cfgFile = oldCfgFile
	})

	customPath := filepath.Join(t.TempDir(), "custom", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(config dir) failed: %v", err)
	}
	if err := os.WriteFile(customPath, []byte(`theme = "nord"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(config) failed: %v", err)
	}
	cfgFile = customPath

	if got := paletteStatePath(t.TempDir()); got != customPath {
		t.Fatalf("paletteStatePath() = %q, want %q", got, customPath)
	}
}

func TestPaletteWatchPathsIncludeProjectConfig(t *testing.T) {
	oldCfgFile := cfgFile
	cfgFile = ""
	t.Cleanup(func() {
		cfgFile = oldCfgFile
	})

	base := t.TempDir()
	globalPath := filepath.Join(base, "global", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(global dir) failed: %v", err)
	}
	if err := os.WriteFile(globalPath, []byte(`theme = "nord"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(global config) failed: %v", err)
	}
	cfgFile = globalPath

	projectDir := filepath.Join(base, "project")
	projectConfigPath := filepath.Join(projectDir, ".ntm", "config.toml")
	palettePath := filepath.Join(projectDir, ".ntm", "palette.md")
	if err := os.MkdirAll(filepath.Dir(projectConfigPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(project .ntm) failed: %v", err)
	}
	projectConfigBody := `[palette]
file = "palette.md"
`
	if err := os.WriteFile(projectConfigPath, []byte(projectConfigBody), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) failed: %v", err)
	}
	paletteBody := `## Project
### project_cmd | Project Command
Use project palette.
`
	if err := os.WriteFile(palettePath, []byte(paletteBody), 0o644); err != nil {
		t.Fatalf("WriteFile(project palette) failed: %v", err)
	}

	paths, err := loadPaletteWatchPaths(projectDir)
	if err != nil {
		t.Fatalf("loadPaletteWatchPaths() failed: %v", err)
	}
	want := []string{globalPath, projectConfigPath, palettePath}
	if len(paths) != len(want) {
		t.Fatalf("paletteWatchPaths() len = %d, want %d (%v)", len(paths), len(want), paths)
	}
	for i, wantPath := range want {
		if paths[i] != wantPath {
			t.Fatalf("paletteWatchPaths()[%d] = %q, want %q", i, paths[i], wantPath)
		}
	}
}

func TestPaletteWatchPathsIgnoreUnrelatedCwdPalette(t *testing.T) {
	oldCfgFile := cfgFile
	origWd, _ := os.Getwd()
	cfgFile = ""
	t.Cleanup(func() {
		cfgFile = oldCfgFile
		_ = os.Chdir(origWd)
	})

	base := t.TempDir()
	globalPath := filepath.Join(base, "global", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(global dir) failed: %v", err)
	}
	if err := os.WriteFile(globalPath, []byte(`theme = "nord"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(global config) failed: %v", err)
	}
	cfgFile = globalPath

	projectDir := filepath.Join(base, "project")
	projectConfigPath := filepath.Join(projectDir, ".ntm", "config.toml")
	palettePath := filepath.Join(projectDir, ".ntm", "palette.md")
	if err := os.MkdirAll(filepath.Dir(projectConfigPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(project .ntm) failed: %v", err)
	}
	projectConfigBody := `[palette]
file = "palette.md"
`
	if err := os.WriteFile(projectConfigPath, []byte(projectConfigBody), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) failed: %v", err)
	}
	paletteBody := `## Project
### project_cmd | Project Command
Use project palette.
`
	if err := os.WriteFile(palettePath, []byte(paletteBody), 0o644); err != nil {
		t.Fatalf("WriteFile(project palette) failed: %v", err)
	}

	unrelatedWd := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(unrelatedWd, 0o755); err != nil {
		t.Fatalf("MkdirAll(unrelated wd) failed: %v", err)
	}
	ambientPalettePath := filepath.Join(unrelatedWd, "command_palette.md")
	ambientPaletteBody := `## Ambient
### ambient_cmd | Ambient Command
Ambient palette.
`
	if err := os.WriteFile(ambientPalettePath, []byte(ambientPaletteBody), 0o644); err != nil {
		t.Fatalf("WriteFile(ambient palette) failed: %v", err)
	}
	if err := os.Chdir(unrelatedWd); err != nil {
		t.Fatalf("Chdir(unrelated wd) failed: %v", err)
	}

	paths, err := loadPaletteWatchPaths(projectDir)
	if err != nil {
		t.Fatalf("loadPaletteWatchPaths() failed: %v", err)
	}
	want := []string{globalPath, projectConfigPath, palettePath}
	if len(paths) != len(want) {
		t.Fatalf("loadPaletteWatchPaths() len = %d, want %d (%v)", len(paths), len(want), paths)
	}
	for i, wantPath := range want {
		if paths[i] != wantPath {
			t.Fatalf("loadPaletteWatchPaths()[%d] = %q, want %q", i, paths[i], wantPath)
		}
	}
}

func TestLoadPaletteWatchConfigUsesMergedConfig(t *testing.T) {
	oldCfgFile := cfgFile
	cfgFile = ""
	t.Cleanup(func() {
		cfgFile = oldCfgFile
	})

	base := t.TempDir()
	globalPath := filepath.Join(base, "global", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(global dir) failed: %v", err)
	}
	if err := os.WriteFile(globalPath, []byte(`theme = "nord"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(global config) failed: %v", err)
	}
	cfgFile = globalPath

	projectDir := filepath.Join(base, "project")
	projectConfigPath := filepath.Join(projectDir, ".ntm", "config.toml")
	palettePath := filepath.Join(projectDir, ".ntm", "palette.md")
	if err := os.MkdirAll(filepath.Dir(projectConfigPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(project .ntm) failed: %v", err)
	}
	projectConfigBody := `[palette]
file = "palette.md"
`
	if err := os.WriteFile(projectConfigPath, []byte(projectConfigBody), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) failed: %v", err)
	}
	paletteBody := `## Project
### project_cmd | Project Command
Use project palette.
`
	if err := os.WriteFile(palettePath, []byte(paletteBody), 0o644); err != nil {
		t.Fatalf("WriteFile(project palette) failed: %v", err)
	}

	loaded, err := loadPaletteWatchConfig(projectDir)
	if err != nil {
		t.Fatalf("loadPaletteWatchConfig() failed: %v", err)
	}
	found := false
	for _, cmd := range loaded.Palette {
		if cmd.Key == "project_cmd" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected merged palette to include project_cmd, got %#v", loaded.Palette)
	}
}

func TestPaletteConfigContextDirPrefersExplicitSessionProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	oldCfg := cfg
	oldCfgFile := cfgFile
	origWd, _ := os.Getwd()
	cfgFile = ""
	cfg = config.Default()
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		_ = os.Chdir(origWd)
	})

	base := t.TempDir()
	projectsBase := filepath.Join(base, "projects")
	const sessionName = "palette-explicit-project"
	projectDir := filepath.Join(projectsBase, sessionName)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(project dir) failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(project beads dir) failed: %v", err)
	}
	cfg.ProjectsBase = projectsBase

	unrelatedWd := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(unrelatedWd, 0o755); err != nil {
		t.Fatalf("MkdirAll(unrelated wd) failed: %v", err)
	}
	if err := os.Chdir(unrelatedWd); err != nil {
		t.Fatalf("Chdir(unrelated wd) failed: %v", err)
	}

	got, err := paletteConfigContextDir(t.Context(), sessionName, true)
	if err != nil {
		t.Fatalf("paletteConfigContextDir() error = %v", err)
	}
	if got != projectDir {
		t.Fatalf("paletteConfigContextDir() = %q, want %q", got, projectDir)
	}
}

func TestPaletteConfigContextDirResolvesProjectScopedPrefix(t *testing.T) {
	isolateSessionAgentStorage(t)

	oldCfg := cfg
	oldCfgFile := cfgFile
	origWd, _ := os.Getwd()
	cfgFile = ""
	cfg = config.Default()
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		_ = os.Chdir(origWd)
	})

	base := t.TempDir()
	projectsBase := filepath.Join(base, "projects")
	projectDir := filepath.Join(projectsBase, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(project dir) failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(project beads dir) failed: %v", err)
	}
	cfg.ProjectsBase = projectsBase

	unrelatedWd := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(unrelatedWd, 0o755); err != nil {
		t.Fatalf("MkdirAll(unrelated wd) failed: %v", err)
	}
	if err := os.Chdir(unrelatedWd); err != nil {
		t.Fatalf("Chdir(unrelated wd) failed: %v", err)
	}

	got, err := paletteConfigContextDir(t.Context(), "de", true)
	if err != nil {
		t.Fatalf("paletteConfigContextDir() error = %v", err)
	}
	if got != projectDir {
		t.Fatalf("paletteConfigContextDir() = %q, want %q", got, projectDir)
	}
}

func TestPaletteConfigContextDirRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	oldCfg := cfg
	oldCfgFile := cfgFile
	origWd, _ := os.Getwd()
	cfgFile = ""
	cfg = config.Default()
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		_ = os.Chdir(origWd)
	})

	root, nested := createCLIWorkspaceProjectRoot(t)
	cfg.ProjectsBase = filepath.Join(root, "projects-base")

	if err := os.Chdir(nested); err != nil {
		t.Fatalf("Chdir(workspace nested) failed: %v", err)
	}

	_, err := paletteConfigContextDir(t.Context(), "demo", true)
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestLoadPaletteRuntimeConfigPrefersExplicitSessionProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	oldCfg := cfg
	oldCfgFile := cfgFile
	origWd, _ := os.Getwd()
	cfgFile = ""
	cfg = config.Default()
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		_ = os.Chdir(origWd)
	})

	base := t.TempDir()
	globalPath := filepath.Join(base, "global", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(global dir) failed: %v", err)
	}
	if err := os.WriteFile(globalPath, []byte(`theme = "nord"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(global config) failed: %v", err)
	}
	cfgFile = globalPath

	projectsBase := filepath.Join(base, "projects")
	const sessionName = "palette-runtime-project"
	projectDir := filepath.Join(projectsBase, sessionName)
	projectConfigPath := filepath.Join(projectDir, ".ntm", "config.toml")
	palettePath := filepath.Join(projectDir, ".ntm", "palette.md")
	if err := os.MkdirAll(filepath.Dir(projectConfigPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(project .ntm) failed: %v", err)
	}
	projectConfigBody := `[palette]
file = "palette.md"
`
	if err := os.WriteFile(projectConfigPath, []byte(projectConfigBody), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) failed: %v", err)
	}
	paletteBody := `## Project
### project_cmd | Project Command
Use project palette.
`
	if err := os.WriteFile(palettePath, []byte(paletteBody), 0o644); err != nil {
		t.Fatalf("WriteFile(project palette) failed: %v", err)
	}
	cfg.ProjectsBase = projectsBase

	unrelatedWd := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(unrelatedWd, 0o755); err != nil {
		t.Fatalf("MkdirAll(unrelated wd) failed: %v", err)
	}
	if err := os.Chdir(unrelatedWd); err != nil {
		t.Fatalf("Chdir(unrelated wd) failed: %v", err)
	}

	loaded, err := loadPaletteRuntimeConfig(t.Context(), sessionName, true)
	if err != nil {
		t.Fatalf("loadPaletteRuntimeConfig() failed: %v", err)
	}
	found := false
	for _, cmd := range loaded.Palette {
		if cmd.Key == "project_cmd" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected runtime palette to include project_cmd, got %#v", loaded.Palette)
	}
}

func TestLoadPaletteRuntimeConfigRejectsAmbiguousExplicitSessionPrefix(t *testing.T) {
	isolateSessionAgentStorage(t)

	oldCfg := cfg
	oldCfgFile := cfgFile
	origWd, _ := os.Getwd()
	cfgFile = ""
	cfg = config.Default()
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		_ = os.Chdir(origWd)
	})

	base := t.TempDir()
	globalPath := filepath.Join(base, "global", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(global dir) failed: %v", err)
	}
	if err := os.WriteFile(globalPath, []byte(`theme = "nord"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(global config) failed: %v", err)
	}
	cfgFile = globalPath

	projectsBase := filepath.Join(base, "projects")
	for _, name := range []string{"demo", "design"} {
		projectDir := filepath.Join(projectsBase, name)
		if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s .ntm) failed: %v", name, err)
		}
	}
	cfg.ProjectsBase = projectsBase

	unrelatedWd := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(unrelatedWd, 0o755); err != nil {
		t.Fatalf("MkdirAll(unrelated wd) failed: %v", err)
	}
	if err := os.Chdir(unrelatedWd); err != nil {
		t.Fatalf("Chdir(unrelated wd) failed: %v", err)
	}

	_, err := loadPaletteRuntimeConfig(t.Context(), "de", true)
	if err == nil {
		t.Fatal("expected ambiguous explicit session prefix error")
	}
	if got := err.Error(); !strings.Contains(got, "ambiguous") {
		t.Fatalf("expected ambiguous prefix error, got %v", err)
	}
}

// TestQuickCmdRequiresName tests quick command requires project name
func TestQuickCmdRequiresName(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"quick"})

	err := rootCmd.Execute()
	if err == nil {
		t.Error("Expected error for quick without project name")
	}
}

// TestUpgradeCmdHelp tests the upgrade command help
func TestUpgradeCmdHelp(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"upgrade", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("upgrade --help failed: %v", err)
	}
}

// TestGetAssetName tests the asset name generation for different platforms
func TestGetAssetName(t *testing.T) {
	// Note: This tests the actual runtime values, so results depend on where tests run
	name := getAssetName()

	// Must start with ntm_
	if !strings.HasPrefix(name, "ntm_") {
		t.Errorf("getAssetName() = %q, want prefix 'ntm_'", name)
	}

	// Must contain underscore separators (not dashes)
	parts := strings.Split(name, "_")
	if len(parts) != 3 {
		t.Errorf("getAssetName() = %q, want 3 parts separated by underscore", name)
	}
}

// TestGetArchiveAssetName tests archive asset name generation
func TestGetArchiveAssetName(t *testing.T) {
	tests := []struct {
		version  string
		wantPre  string
		wantPost string
	}{
		{"1.4.1", "ntm_1.4.1_", ".tar.gz"},
		{"2.0.0", "ntm_2.0.0_", ".tar.gz"},
		{"0.1.0-beta", "ntm_0.1.0-beta_", ".tar.gz"},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			name := getArchiveAssetName(tt.version)

			if !strings.HasPrefix(name, tt.wantPre) {
				t.Errorf("getArchiveAssetName(%q) = %q, want prefix %q", tt.version, name, tt.wantPre)
			}
			if !strings.HasSuffix(name, tt.wantPost) {
				t.Errorf("getArchiveAssetName(%q) = %q, want suffix %q", tt.version, name, tt.wantPost)
			}
		})
	}
}

// TestVersionComparison tests the version comparison logic
func TestVersionComparison(t *testing.T) {
	tests := []struct {
		current   string
		latest    string
		wantNewer bool
	}{
		{"1.0.0", "1.1.0", true},
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "2.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.1.0", "1.0.0", false},
		{"2.0.0", "1.9.9", false},
		{"dev", "1.0.0", true},
		{"", "1.0.0", true},
		{"v1.0.0", "v1.1.0", true},
		{"1.0", "1.0.1", true},
		{"1.0.0-beta", "1.0.0", false}, // normalizeVersion strips suffix, so they're equal
	}

	for _, tt := range tests {
		t.Run(tt.current+"_vs_"+tt.latest, func(t *testing.T) {
			got := isNewerVersion(tt.current, tt.latest)
			if got != tt.wantNewer {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.wantNewer)
			}
		})
	}
}

// TestNormalizeVersion tests version normalization
func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v1.0.0", "1.0.0"},
		{"1.0.0", "1.0.0"},
		{"1.0.0-beta", "1.0.0"},
		{"1.0.0+build", "1.0.0"},
		{"v2.1.3-rc1", "2.1.3"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeVersion(tt.input)
			if got != tt.want {
				t.Errorf("normalizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestFormatSize tests the size formatting function
func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{16219443, "15.5 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatSize(tt.bytes)
			if got != tt.want {
				t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

type goreleaserConfig struct {
	ProjectName       string                      `yaml:"project_name"`
	Builds            []goreleaserBuild           `yaml:"builds"`
	UniversalBinaries []goreleaserUniversalBinary `yaml:"universal_binaries"`
	Archives          []goreleaserArchive         `yaml:"archives"`
}

type goreleaserBuild struct {
	Goarm []string `yaml:"goarm"`
}

type goreleaserUniversalBinary struct {
	Replace bool `yaml:"replace"`
}

type goreleaserArchive struct {
	ID              string                     `yaml:"id"`
	Formats         []string                   `yaml:"formats"`
	NameTemplate    string                     `yaml:"name_template"`
	FormatOverrides []goreleaserFormatOverride `yaml:"format_overrides"`
}

type goreleaserFormatOverride struct {
	Goos    string   `yaml:"goos"`
	Formats []string `yaml:"formats"`
}

type archiveTemplateContext struct {
	ProjectName string
	Version     string
	Os          string
	Arch        string
	Arm         string
}

func loadGoReleaserConfig(t *testing.T) goreleaserConfig {
	t.Helper()

	path := findGoReleaserConfigPath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var cfg goreleaserConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if cfg.ProjectName == "" {
		t.Fatalf("project_name missing in %s", path)
	}
	return cfg
}

func findGoReleaserConfigPath(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		path := filepath.Join(dir, ".goreleaser.yaml")
		if _, err := os.Stat(path); err == nil {
			return path
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find .goreleaser.yaml from %s", dir)
		}
		dir = parent
	}
}

func findArchive(cfg goreleaserConfig, wantBinary bool) *goreleaserArchive {
	for i := range cfg.Archives {
		isBinary := containsStringValue(cfg.Archives[i].Formats, "binary")
		if isBinary == wantBinary {
			return &cfg.Archives[i]
		}
	}
	return nil
}

func containsStringValue(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func hasUniversalBinary(cfg goreleaserConfig) bool {
	for _, ub := range cfg.UniversalBinaries {
		if ub.Replace {
			return true
		}
	}
	return false
}

func defaultGoarm(cfg goreleaserConfig) string {
	for _, b := range cfg.Builds {
		if len(b.Goarm) > 0 {
			return b.Goarm[0]
		}
	}
	return ""
}

func normalizedTemplateArch(cfg goreleaserConfig, goos, goarch string) (string, string) {
	if goos == "darwin" && hasUniversalBinary(cfg) {
		return "all", ""
	}
	if goarch == "arm" {
		return "arm", defaultGoarm(cfg)
	}
	return goarch, ""
}

func renderNameTemplate(t *testing.T, tmpl string, ctx archiveTemplateContext) string {
	t.Helper()

	tpl, err := template.New("name").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		t.Fatalf("parse name_template: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, ctx); err != nil {
		t.Fatalf("render name_template: %v", err)
	}
	return strings.TrimSpace(buf.String())
}

func archiveFormatForOS(archive *goreleaserArchive, goos string) string {
	if archive == nil {
		return ""
	}
	for _, override := range archive.FormatOverrides {
		if override.Goos == goos && len(override.Formats) > 0 {
			return override.Formats[0]
		}
	}
	if len(archive.Formats) > 0 {
		return archive.Formats[0]
	}
	return ""
}

// TestUpgradeAssetNamingContract validates that upgrade.go asset naming
// matches the GoReleaser naming convention. This is a CONTRACT TEST that
// catches drift between .goreleaser.yaml and upgrade.go before users hit it.
//
// GoReleaser naming patterns (from .goreleaser.yaml):
//   - Archives: ntm_VERSION_OS_ARCH.tar.gz (or .zip for windows)
//   - Binaries: ntm_OS_ARCH
//   - macOS: uses "all" for universal binary (replaces amd64/arm64)
//   - Linux ARM: uses "armv7" suffix
//
// See CONTRIBUTING.md "Release Infrastructure" section for full documentation
// on the upgrade naming contract and how to safely make changes.
func TestUpgradeAssetNamingContract(t *testing.T) {
	cfg := loadGoReleaserConfig(t)
	archive := findArchive(cfg, false)
	if archive == nil {
		t.Fatalf("no non-binary archive found in .goreleaser.yaml")
	}
	binaryArchive := findArchive(cfg, true)
	if binaryArchive == nil {
		t.Fatalf("no binary archive found in .goreleaser.yaml")
	}

	// These test cases represent platform combinations we must support.
	// Expected names are derived from .goreleaser.yaml at test time.
	tests := []struct {
		name    string
		goos    string
		goarch  string
		version string
	}{
		{
			name:    "darwin_arm64",
			goos:    "darwin",
			goarch:  "arm64",
			version: "1.4.1",
		},
		{
			name:    "darwin_amd64",
			goos:    "darwin",
			goarch:  "amd64",
			version: "1.4.1",
		},
		{
			name:    "linux_amd64",
			goos:    "linux",
			goarch:  "amd64",
			version: "2.0.0",
		},
		{
			name:    "linux_arm64",
			goos:    "linux",
			goarch:  "arm64",
			version: "1.5.0",
		},
		{
			name:    "linux_arm",
			goos:    "linux",
			goarch:  "arm",
			version: "1.5.0",
		},
		{
			name:    "windows_amd64",
			goos:    "windows",
			goarch:  "amd64",
			version: "1.4.1",
		},
		{
			name:    "freebsd_amd64",
			goos:    "freebsd",
			goarch:  "amd64",
			version: "1.4.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arch, arm := normalizedTemplateArch(cfg, tt.goos, tt.goarch)
			ctx := archiveTemplateContext{
				ProjectName: cfg.ProjectName,
				Version:     tt.version,
				Os:          tt.goos,
				Arch:        arch,
				Arm:         arm,
			}

			archiveBase := renderNameTemplate(t, archive.NameTemplate, ctx)
			archiveExt := archiveFormatForOS(archive, tt.goos)
			wantArchive := archiveBase
			if archiveExt != "" {
				wantArchive = archiveBase + "." + archiveExt
			}

			wantBinaryName := renderNameTemplate(t, binaryArchive.NameTemplate, ctx)

			// Simulate the asset name generation for this platform
			gotArchive := simulateGetArchiveAssetName(tt.version, tt.goos, tt.goarch)
			gotBinary := simulateGetAssetName(tt.goos, tt.goarch)

			if gotArchive != wantArchive {
				t.Errorf("Archive name mismatch for %s/%s:\n  got:  %q\n  want: %q\n"+
					"  This likely means upgrade.go is out of sync with .goreleaser.yaml",
					tt.goos, tt.goarch, gotArchive, wantArchive)
			}
			if gotBinary != wantBinaryName {
				t.Errorf("Binary name mismatch for %s/%s:\n  got:  %q\n  want: %q\n"+
					"  This likely means upgrade.go is out of sync with .goreleaser.yaml",
					tt.goos, tt.goarch, gotBinary, wantBinaryName)
			}
		})
	}
}

// simulateGetAssetName mirrors getAssetName() but for a specific platform
// This allows testing cross-platform naming without runtime.GOOS/GOARCH
func simulateGetAssetName(goos, goarch string) string {
	arch := goarch
	// macOS uses universal binary ("all") that works on both amd64 and arm64
	if goos == "darwin" {
		arch = "all"
	}
	// 32-bit ARM uses "armv7" suffix (GoReleaser builds with goarm=7)
	if goarch == "arm" {
		arch = "armv7"
	}
	return "ntm_" + goos + "_" + arch
}

// simulateGetArchiveAssetName mirrors getArchiveAssetName() but for a specific platform
func simulateGetArchiveAssetName(version, goos, goarch string) string {
	arch := goarch
	if goos == "darwin" {
		arch = "all"
	}
	// 32-bit ARM uses "armv7" suffix (GoReleaser builds with goarm=7)
	if goarch == "arm" {
		arch = "armv7"
	}
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return "ntm_" + version + "_" + goos + "_" + arch + "." + ext
}

// TestUpgradeAssetNamingConsistency verifies the actual functions produce
// consistent results with our test simulations on the current platform
func TestUpgradeAssetNamingConsistency(t *testing.T) {
	// The real functions use runtime.GOOS/GOARCH, so we test that the
	// current platform produces expected patterns

	realBinary := getAssetName()
	// Binary should always start with ntm_ and use underscore separators
	if !strings.HasPrefix(realBinary, "ntm_") {
		t.Errorf("getAssetName() = %q, should start with 'ntm_'", realBinary)
	}
	parts := strings.Split(realBinary, "_")
	if len(parts) != 3 {
		t.Errorf("getAssetName() = %q, should have 3 underscore-separated parts", realBinary)
	}

	realArchive := getArchiveAssetName("1.0.0")
	// Archive should have format: ntm_VERSION_OS_ARCH.ext
	if !strings.HasPrefix(realArchive, "ntm_1.0.0_") {
		t.Errorf("getArchiveAssetName('1.0.0') = %q, should start with 'ntm_1.0.0_'", realArchive)
	}
	if !strings.HasSuffix(realArchive, ".tar.gz") && !strings.HasSuffix(realArchive, ".zip") {
		t.Errorf("getArchiveAssetName() = %q, should end with .tar.gz or .zip", realArchive)
	}

	// Log for debugging
	t.Logf("Current platform produces: binary=%q, archive=%q", realBinary, realArchive)
}

func TestParseRobotSinceWindowAcceptsRFC3339(t *testing.T) {
	resetFlags()

	sinceTS := time.Now().UTC().Add(-90 * time.Minute).Truncate(time.Second)
	got, err := parseRobotSinceWindow(sinceTS.Format(time.RFC3339), time.Minute, "since")
	if err != nil {
		t.Fatalf("parseRobotSinceWindow() error = %v", err)
	}

	if got < 89*time.Minute || got > 91*time.Minute {
		t.Fatalf("parseRobotSinceWindow() = %v, want about 90m", got)
	}
}

func TestParseRobotSinceWindowRejectsFutureTimestamp(t *testing.T) {
	resetFlags()

	future := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	_, err := parseRobotSinceWindow(future.Format(time.RFC3339), time.Minute, "since")
	if err == nil {
		t.Fatal("parseRobotSinceWindow() error = nil, want future timestamp error")
	}
	if !strings.Contains(err.Error(), "future") {
		t.Fatalf("parseRobotSinceWindow() error = %q, want future timestamp message", err)
	}
}

func TestResolveRobotMailCheckUsesSharedCanonicalFlags(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("since", "", "")
	cmd.Flags().String("cass-since", "", "")
	cmd.Flags().Int("limit", 10, "")
	cmd.Flags().Int("cass-limit", 10, "")

	robotSince = "2025-01-01"
	cassLimit = 25
	if err := cmd.Flags().Set("since", robotSince); err != nil {
		t.Fatalf("set since: %v", err)
	}
	if err := cmd.Flags().Set("limit", "25"); err != nil {
		t.Fatalf("set limit: %v", err)
	}

	if got := resolveRobotMailCheckSince(cmd); got != robotSince {
		t.Fatalf("resolveRobotMailCheckSince() = %q, want %q", got, robotSince)
	}
	if got := resolveRobotMailCheckLimit(cmd); got != 25 {
		t.Fatalf("resolveRobotMailCheckLimit() = %d, want 25", got)
	}
}

func TestResolveRobotMailCheckLimitDefaultsToCommandBehavior(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Int("limit", 10, "")
	cmd.Flags().Int("cass-limit", 10, "")

	cassLimit = 10
	if got := resolveRobotMailCheckLimit(cmd); got != 0 {
		t.Fatalf("resolveRobotMailCheckLimit() = %d, want 0 so command can use its own default", got)
	}
}

func TestResolveRobotMailCheckSincePrefersDeprecatedAliasWhenExplicitlySet(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("since", "", "")
	cmd.Flags().String("cass-since", "", "")

	cassSince = "2025-02-01"
	if err := cmd.Flags().Set("cass-since", cassSince); err != nil {
		t.Fatalf("set cass-since: %v", err)
	}

	if got := resolveRobotMailCheckSince(cmd); got != cassSince {
		t.Fatalf("resolveRobotMailCheckSince() = %q, want deprecated explicit value %q", got, cassSince)
	}
}

func TestResolveRobotSharedFlagUsesCanonicalSharedValueWhenSpecificHasDefault(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("strategy", "", "")

	robotRouteStrategy = "least-loaded"
	robotAssignStrategy = "balanced"
	if err := cmd.Flags().Set("strategy", "balanced"); err != nil {
		t.Fatalf("set strategy: %v", err)
	}

	if got := resolveRobotRouteStrategy(cmd); got != "balanced" {
		t.Fatalf("resolveRobotRouteStrategy() = %q, want %q", got, "balanced")
	}
}

func TestResolveRobotSaveOutputSupportsCanonicalAndDeprecatedFlags(t *testing.T) {
	t.Run("canonical output", func(t *testing.T) {
		resetFlags()
		t.Cleanup(resetFlags)

		cmd := &cobra.Command{Use: "test"}
		cmd.Flags().String("output", "", "")
		cmd.Flags().String("save-output", "", "")
		robotMonitorOutput = "/tmp/canonical-save.json"
		if err := cmd.Flags().Set("output", robotMonitorOutput); err != nil {
			t.Fatalf("set output: %v", err)
		}

		if got := resolveRobotSaveOutput(cmd); got != robotMonitorOutput {
			t.Fatalf("resolveRobotSaveOutput() = %q, want canonical output %q", got, robotMonitorOutput)
		}
	})

	t.Run("explicit deprecated alias wins", func(t *testing.T) {
		resetFlags()
		t.Cleanup(resetFlags)

		cmd := &cobra.Command{Use: "test"}
		cmd.Flags().String("output", "", "")
		cmd.Flags().String("save-output", "", "")
		robotMonitorOutput = "/tmp/canonical-save.json"
		robotSaveOutput = "/tmp/deprecated-save.json"
		if err := cmd.Flags().Set("output", robotMonitorOutput); err != nil {
			t.Fatalf("set output: %v", err)
		}
		if err := cmd.Flags().Set("save-output", robotSaveOutput); err != nil {
			t.Fatalf("set save-output: %v", err)
		}

		if got := resolveRobotSaveOutput(cmd); got != robotSaveOutput {
			t.Fatalf("resolveRobotSaveOutput() = %q, want deprecated explicit output %q", got, robotSaveOutput)
		}
	})
}

func TestResolveRobotBulkAssignStrategySupportsCanonicalAndDeprecatedFlags(t *testing.T) {
	t.Run("canonical strategy", func(t *testing.T) {
		resetFlags()
		t.Cleanup(resetFlags)
		cmd := &cobra.Command{Use: "test"}
		cmd.Flags().String("strategy", "balanced", "")
		cmd.Flags().String("bulk-strategy", "impact", "")
		robotAssignStrategy = "ready"
		if err := cmd.Flags().Set("strategy", robotAssignStrategy); err != nil {
			t.Fatalf("set strategy: %v", err)
		}
		if got := resolveRobotBulkAssignStrategy(cmd); got != "ready" {
			t.Fatalf("resolveRobotBulkAssignStrategy() = %q, want ready", got)
		}
	})

	t.Run("explicit deprecated alias wins", func(t *testing.T) {
		resetFlags()
		t.Cleanup(resetFlags)
		cmd := &cobra.Command{Use: "test"}
		cmd.Flags().String("strategy", "balanced", "")
		cmd.Flags().String("bulk-strategy", "impact", "")
		robotAssignStrategy = "ready"
		robotBulkAssignStrategy = "stale"
		if err := cmd.Flags().Set("strategy", robotAssignStrategy); err != nil {
			t.Fatalf("set strategy: %v", err)
		}
		if err := cmd.Flags().Set("bulk-strategy", robotBulkAssignStrategy); err != nil {
			t.Fatalf("set bulk-strategy: %v", err)
		}
		if got := resolveRobotBulkAssignStrategy(cmd); got != "stale" {
			t.Fatalf("resolveRobotBulkAssignStrategy() = %q, want stale", got)
		}
	})
}

func TestResolveRobotProviderUsesSharedCanonicalFlags(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("account-status-provider", "", "")
	cmd.Flags().String("accounts-list-provider", "", "")
	cmd.Flags().String("quota-check-provider", "", "")

	if err := cmd.Flags().Set("provider", "claude"); err != nil {
		t.Fatalf("set provider: %v", err)
	}

	if got := resolveRobotProvider(cmd, "account-status-provider", robotAccountStatusProvider); got != "claude" {
		t.Fatalf("resolveRobotProvider(account-status) = %q, want claude", got)
	}
	if got := resolveRobotProvider(cmd, "accounts-list-provider", robotAccountsListProvider); got != "claude" {
		t.Fatalf("resolveRobotProvider(accounts-list) = %q, want claude", got)
	}
	if got := resolveRobotProvider(cmd, "quota-check-provider", robotQuotaCheckProvider); got != "claude" {
		t.Fatalf("resolveRobotProvider(quota-check) = %q, want claude", got)
	}
}

func TestResolveRobotProviderPrefersDeprecatedAliasWhenExplicitlySet(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("accounts-list-provider", "", "")

	if err := cmd.Flags().Set("provider", "claude"); err != nil {
		t.Fatalf("set provider: %v", err)
	}
	robotAccountsListProvider = "openai"
	if err := cmd.Flags().Set("accounts-list-provider", "openai"); err != nil {
		t.Fatalf("set accounts-list-provider: %v", err)
	}

	if got := resolveRobotProvider(cmd, "accounts-list-provider", robotAccountsListProvider); got != "openai" {
		t.Fatalf("resolveRobotProvider() = %q, want deprecated explicit value %q", got, "openai")
	}
}

// TestRobotTailExecutes tests robot-tail flag executes
func TestRobotTailExecutes(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-tail", "nonexistent_session_xyz"})

	// Will error because session doesn't exist, but shouldn't panic
	_ = rootCmd.Execute()
}

// TestRobotTailWithLines tests robot-tail with --lines flag
func TestRobotTailWithLines(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	resetFlags()
	rootCmd.SetArgs([]string{"--robot-tail", "nonexistent", "--lines", "50"})

	// Will error because session doesn't exist
	_ = rootCmd.Execute()
}

// TestRobotDiffExecutes tests robot-diff flag executes
// Note: This test is skipped because robot-diff requires a valid session and
// the handler calls os.Exit(1) on error which fails the test process.
// Flag parsing is tested in TestRobotDiffFlagParsing.
func TestRobotDiffExecutes(t *testing.T) {
	t.Skip("requires valid tmux session; flag parsing tested in TestRobotDiffFlagParsing")
}

// TestRobotDiffWithSince tests robot-diff with --diff-since flag
// Note: Skipped for the same reason as TestRobotDiffExecutes.
func TestRobotDiffWithSince(t *testing.T) {
	t.Skip("requires valid tmux session; flag parsing tested in TestRobotDiffFlagParsing")
}

// TestRobotDiffFlagParsing tests that --robot-diff flag is registered properly
func TestRobotDiffFlagParsing(t *testing.T) {
	resetFlags()

	// Parse the flags
	err := rootCmd.ParseFlags([]string{"--robot-diff", "test_session", "--diff-since", "1h"})
	if err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}

	if robotDiff != "test_session" {
		t.Errorf("expected robotDiff='test_session', got '%s'", robotDiff)
	}

	if robotDiffSince != "1h" {
		t.Errorf("expected robotDiffSince='1h', got '%s'", robotDiffSince)
	}
}

func TestRobotDismissAlertFlagParsingWithDismissAll(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	err := rootCmd.ParseFlags([]string{"--robot-dismiss-alert", "--dismiss-all"})
	if err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}

	if robotDismissAlert != "__present__" {
		t.Fatalf("expected robotDismissAlert='__present__', got %q", robotDismissAlert)
	}
	if !robotDismissAll {
		t.Fatal("expected robotDismissAll=true")
	}
}

func TestProjectScopedRobotFlagsAcceptNoSessionValue(t *testing.T) {
	tests := []struct {
		flag  string
		value *string
	}{
		{flag: "--robot-health", value: &robotHealth},
		{flag: "--robot-files", value: &robotFiles},
		{flag: "--robot-metrics", value: &robotMetrics},
	}

	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			resetFlags()
			t.Cleanup(resetFlags)

			if err := rootCmd.ParseFlags([]string{tt.flag}); err != nil {
				t.Fatalf("ParseFlags(%s) failed: %v", tt.flag, err)
			}
			if *tt.value != "__present__" {
				t.Fatalf("%s = %q, want no-session sentinel", tt.flag, *tt.value)
			}
		})
	}
}

func TestClassifyRobotExecuteErrorMapsMissingFlagArgumentToInvalidFlag(t *testing.T) {
	code, _ := classifyRobotExecuteError(errors.New("flag needs an argument: --robot-files"))
	if code != robot.ErrCodeInvalidFlag {
		t.Fatalf("classifyRobotExecuteError() code = %q, want %q", code, robot.ErrCodeInvalidFlag)
	}
}

// TestGlobalJSONFlag tests the global --json flag works
func TestGlobalJSONFlag(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	resetFlags()
	rootCmd.SetArgs([]string{"--json", "list"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

// TestGlobalConfigFlag tests the global --config flag parses
func TestGlobalConfigFlag(t *testing.T) {
	resetFlags()
	rootCmd.SetArgs([]string{"--config", "/nonexistent/config.toml", "version"})

	// Should still work even with nonexistent config (falls back to defaults)
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
}

func TestConfigPathCmdUsesGlobalConfigFlag(t *testing.T) {
	resetFlags()
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg = nil
	cfgFile = ""
	startup.ResetConfig()
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		startup.ResetConfig()
	})

	customPath := filepath.Join(t.TempDir(), "custom.toml")
	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"--config", customPath, "config", "path"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	if got := strings.TrimSpace(out); got != customPath {
		t.Fatalf("config path output = %q, want %q", got, customPath)
	}
}

func TestConfigInitCmdUsesGlobalConfigFlag(t *testing.T) {
	resetFlags()
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg = nil
	cfgFile = ""
	startup.ResetConfig()
	cfgHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	expectedDefaultPath := filepath.Join(cfgHome, "ntm", "config.toml")
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		startup.ResetConfig()
	})

	customPath := filepath.Join(t.TempDir(), "custom", "ntm.toml")
	_, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"--config", customPath, "config", "init"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	if _, err := os.Stat(customPath); err != nil {
		t.Fatalf("custom config path not created: %v", err)
	}
	if _, err := os.Stat(expectedDefaultPath); !os.IsNotExist(err) {
		t.Fatalf("default config path should remain untouched, stat err = %v", err)
	}
}

func TestConfigSetProjectsBaseUsesGlobalConfigFlag(t *testing.T) {
	resetFlags()
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg = nil
	cfgFile = ""
	startup.ResetConfig()
	cfgHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	expectedDefaultPath := filepath.Join(cfgHome, "ntm", "config.toml")
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		startup.ResetConfig()
	})

	customPath := filepath.Join(t.TempDir(), "custom", "ntm.toml")
	projectsBase := filepath.Join(t.TempDir(), "projects")
	_, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"--config", customPath, "config", "set", "projects-base", projectsBase})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	data, err := os.ReadFile(customPath)
	if err != nil {
		t.Fatalf("ReadFile(customPath) failed: %v", err)
	}
	if !strings.Contains(string(data), projectsBase) {
		t.Fatalf("custom config missing projects_base %q", projectsBase)
	}
	if _, err := os.Stat(expectedDefaultPath); !os.IsNotExist(err) {
		t.Fatalf("default config path should remain untouched, stat err = %v", err)
	}
}

func TestConfigEditCmdUsesGlobalConfigFlag(t *testing.T) {
	resetFlags()
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg = nil
	cfgFile = ""
	startup.ResetConfig()
	cfgHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		startup.ResetConfig()
	})

	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "custom", "ntm.toml")
	capturePath := filepath.Join(tmpDir, "editor-arg.txt")
	editorPath := filepath.Join(tmpDir, "editor.sh")
	editorScript := "#!/bin/sh\nprintf '%s' \"$1\" > \"$CAPTURE_FILE\"\n"
	if err := os.WriteFile(editorPath, []byte(editorScript), 0o755); err != nil {
		t.Fatalf("WriteFile(editor) failed: %v", err)
	}
	t.Setenv("EDITOR", editorPath)
	t.Setenv("CAPTURE_FILE", capturePath)

	_, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"--config", customPath, "config", "edit"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	arg, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile(capturePath) failed: %v", err)
	}
	if got := strings.TrimSpace(string(arg)); got != customPath {
		t.Fatalf("editor path = %q, want %q", got, customPath)
	}
	if _, err := os.Stat(customPath); err != nil {
		t.Fatalf("custom config path not created: %v", err)
	}
}

func TestConfigResetCmdUsesGlobalConfigFlag(t *testing.T) {
	resetFlags()
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg = nil
	cfgFile = ""
	startup.ResetConfig()
	cfgHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		startup.ResetConfig()
	})

	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "custom", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(custom) failed: %v", err)
	}
	if err := os.WriteFile(customPath, []byte("projects_base = \"/custom/path\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(custom) failed: %v", err)
	}
	defaultPath := config.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(default) failed: %v", err)
	}
	if err := os.WriteFile(defaultPath, []byte("# default-marker\nprojects_base = \"/default/path\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(default) failed: %v", err)
	}

	_, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"--config", customPath, "config", "reset", "--confirm"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	loaded, err := config.Load(customPath)
	if err != nil {
		t.Fatalf("Load(customPath) failed after reset: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected custom config to exist after reset")
	}
	defaultData, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatalf("ReadFile(defaultPath) failed: %v", err)
	}
	if !strings.Contains(string(defaultData), "default-marker") {
		t.Fatalf("default config path was unexpectedly modified")
	}
}

func TestLastCleanupFileUsesGlobalConfigFlag(t *testing.T) {
	resetFlags()
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg = nil
	cfgFile = ""
	startup.ResetConfig()
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		startup.ResetConfig()
	})

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	customPath := filepath.Join(tmpDir, "custom", "ntm.toml")
	cfgFile = customPath

	got := lastCleanupFile()
	want := filepath.Join(tmpDir, "custom", ".last-cleanup")
	if got != want {
		t.Fatalf("lastCleanupFile() = %q, want %q", got, want)
	}

	markCleanupDone()
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("selected-config cleanup marker missing: %v", err)
	}

	legacyPath := filepath.Join(tmpDir, "xdg", "ntm", ".last-cleanup")
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy cleanup marker path should remain untouched, stat err = %v", err)
	}
}

func TestConfigGetUsesProjectMergedConfig(t *testing.T) {
	resetFlags()
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg = nil
	cfgFile = ""
	startup.ResetConfig()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() failed: %v", err)
	}
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		startup.ResetConfig()
		_ = os.Chdir(origWD)
	})

	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "custom", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(config dir) failed: %v", err)
	}
	if err := os.WriteFile(customPath, []byte("[alerts]\nenabled = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(global config) failed: %v", err)
	}

	projectDir := filepath.Join(tmpDir, "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("MkdirAll(project .ntm) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte("[alerts]\nenabled = false\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(project config) failed: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir(projectDir) failed: %v", err)
	}

	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"--config", customPath, "config", "get", "alerts.enabled"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	if got := strings.TrimSpace(out); got != "false" {
		t.Fatalf("config get output = %q, want false", got)
	}
}

func envWithOverrides(env []string, overrides ...string) []string {
	replacements := make(map[string]string, len(overrides))
	for _, override := range overrides {
		key, value, ok := strings.Cut(override, "=")
		if !ok {
			continue
		}
		replacements[key] = value
	}

	merged := make([]string, 0, len(env)+len(replacements))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replaced := replacements[key]; replaced {
				continue
			}
		}
		merged = append(merged, entry)
	}

	for key, value := range replacements {
		merged = append(merged, key+"="+value)
	}
	return merged
}

func TestRobotStateCommandsWorkWithCGODisabledReleaseBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping nested release build in short mode")
	}
	root := repoRoot(t)
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "ntm")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}

	buildCmd := exec.Command("go", "build", "-trimpath", "-o", binaryPath, "./cmd/ntm")
	buildCmd.Dir = root
	buildCmd.Env = envWithOverrides(os.Environ(), "CGO_ENABLED=0")
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CGO-disabled go build failed: %v\n%s", err, strings.TrimSpace(string(buildOut)))
	}

	homeDir := filepath.Join(tmpDir, "home")
	configHome := filepath.Join(tmpDir, "xdg")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(homeDir) failed: %v", err)
	}
	if err := os.MkdirAll(configHome, 0o755); err != nil {
		t.Fatalf("MkdirAll(configHome) failed: %v", err)
	}

	commands := []string{"--robot-status", "--robot-snapshot"}
	for _, flag := range commands {
		t.Run(flag, func(t *testing.T) {
			cmd := exec.Command(binaryPath, flag)
			cmd.Dir = tmpDir
			cmd.Env = envWithOverrides(os.Environ(), "HOME="+homeDir, "XDG_CONFIG_HOME="+configHome)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			if err != nil {
				t.Fatalf("CGO-disabled release binary %s failed: %v\nstdout=%s\nstderr=%s", flag, err, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
			}
			if strings.Contains(stderr.String(), "requires cgo") || strings.Contains(stdout.String(), "requires cgo") {
				t.Fatalf("%s output still reports cgo sqlite stub: stdout=%s stderr=%s", flag, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
			}

			var payload map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
				t.Fatalf("%s did not return JSON: %v\nstdout=%s\nstderr=%s", flag, err, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
			}
			if success, _ := payload["success"].(bool); !success {
				t.Fatalf("%s success=false in CGO-disabled build: %v", flag, payload)
			}
		})
	}
}

func TestRootDispatcherDoesNotExitProcess(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate cli test source")
	}
	rootFile := filepath.Join(filepath.Dir(testFile), "root.go")
	file, err := parser.ParseFile(token.NewFileSet(), rootFile, nil, 0)
	if err != nil {
		t.Fatalf("parse root dispatcher: %v", err)
	}

	var exitCalls []token.Pos
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Exit" {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if ok && pkg.Name == "os" {
			exitCalls = append(exitCalls, call.Pos())
		}
		return true
	})
	if len(exitCalls) != 0 {
		t.Fatalf("root dispatcher must return typed errors instead of calling os.Exit; found %d call(s)", len(exitCalls))
	}
}

func TestRobotProcessContractHelper(t *testing.T) {
	rawArgs := os.Getenv("NTM_ROBOT_CONTRACT_ARGS")
	if rawArgs == "" {
		return
	}

	var args []string
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		t.Fatalf("decode helper args: %v", err)
	}
	if len(args) == 1 && args[0] == "__robot_contract_unavailable__" {
		err := robot.EncodeErrorJSON(errors.New("feature unavailable"), robot.ErrCodeNotImplemented, "Use a supported robot surface", "robot-test")
		os.Exit(ExitCode(err))
	}
	os.Args = append([]string{"ntm"}, args...)
	if err := Execute(); err != nil {
		os.Exit(ExitCode(err))
	}
	os.Exit(0)
}

func TestRobotProcessErrorContract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level robot contract integration in short mode")
	}
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	configHome := filepath.Join(tmpDir, "xdg")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(homeDir) failed: %v", err)
	}
	if err := os.MkdirAll(configHome, 0o755); err != nil {
		t.Fatalf("MkdirAll(configHome) failed: %v", err)
	}
	invalidConfig := filepath.Join(tmpDir, "invalid.toml")
	if err := os.WriteFile(invalidConfig, []byte("[robot\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(invalidConfig) failed: %v", err)
	}

	tests := []struct {
		name            string
		args            []string
		errorCode       string
		expectedExit    int
		expectedCommand string
	}{
		{name: "unknown robot flag", args: []string{"--robot-status", "--not-a-real-flag"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "invalid pagination", args: []string{"--robot-status", "--robot-limit=-1"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "invalid duration", args: []string{"--robot-attention", "--attention-timeout=not-a-duration"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "invalid panes", args: []string{"--robot-rano-stats", "--panes=not-a-pane"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "robot send empty singular pane", args: []string{"--robot-send=proj", "--msg=work", "--pane="}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "robot send singular and plural panes", args: []string{"--robot-send=proj", "--msg=work", "--pane=1", "--panes=2"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "robot send malformed verify render", args: []string{"--robot-send=proj", "--msg=work", "--verify-render=not-a-bool"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "missing session", args: []string{"--robot-agent-names=ntm-robot-contract-missing-session"}, errorCode: robot.ErrCodeSessionNotFound, expectedExit: 1},
		{name: "unknown docs topic", args: []string{"--robot-docs=not-a-topic"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "unknown docs topic forces json from toon", args: []string{"--robot-docs=not-a-topic", "--robot-format=toon"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "unknown schema type", args: []string{"--robot-schema=not-a-schema"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "unknown schema type forces json from toon", args: []string{"--robot-schema=not-a-schema", "--robot-format=toon"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "missing environment session forces json from toon", args: []string{"--robot-env=ntm-robot-contract-missing-session", "--robot-format=toon"}, errorCode: robot.ErrCodeSessionNotFound, expectedExit: 1},
		{name: "missing error scan session forces json from toon", args: []string{"--robot-errors=ntm-robot-contract-missing-session", "--robot-format=toon"}, errorCode: robot.ErrCodeSessionNotFound, expectedExit: 1},
		{name: "unsupported error timestamp filter", args: []string{"--robot-errors=ntm-robot-contract-missing-session", "--errors-since=5m"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "invalid overlay cursor forces json from toon", args: []string{"--robot-overlay", "--overlay-cursor=-1", "--robot-format=toon"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "unknown capability command", args: []string{"--robot-capabilities", "--capability-command=not-a-command"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "unknown capability category", args: []string{"--robot-capabilities", "--capability-category=not-a-category"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "invalid robot format", args: []string{"--robot-status", "--robot-format=xml"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "invalid robot verbosity", args: []string{"--robot-status", "--robot-verbosity=noisy"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "invalid robot redaction", args: []string{"--robot-status", "--redact=erase"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "invalid robot config", args: []string{"--config", invalidConfig, "--robot-status"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "unknown coordinator toggle", args: []string{"--json", "coordinator", "enable", "not-a-feature"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "invalid coordinator interval", args: []string{"--json", "coordinator", "enable", "digest", "--interval=later"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "coordinator interval below minimum", args: []string{"--json", "coordinator", "enable", "digest", "--interval=1s"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "coordinator interval on wrong feature", args: []string{"--json", "coordinator", "enable", "auto-assign", "--interval=30m"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1},
		{name: "format before bulk metadata", args: []string{"--robot-format=json", "--robot-bulk-assign=proj", "--not-a-real-flag"}, errorCode: robot.ErrCodeInvalidFlag, expectedExit: 1, expectedCommand: "robot-bulk-assign"},
		{name: "default ensemble spawn stub", args: []string{"--robot-ensemble-spawn=contract", "--robot-format=toon"}, errorCode: robot.ErrCodeNotImplemented, expectedExit: 2},
		{name: "unavailable feature", args: []string{"__robot_contract_unavailable__"}, errorCode: robot.ErrCodeNotImplemented, expectedExit: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawArgs, err := json.Marshal(tt.args)
			if err != nil {
				t.Fatalf("encode helper args: %v", err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
			cmd.Dir = tmpDir
			cmd.Env = envWithOverrides(os.Environ(),
				"HOME="+homeDir,
				"XDG_CONFIG_HOME="+configHome,
				"NTM_NO_COLOR=1",
				"NTM_CONFIG=",
				"NTM_ROBOT_FORMAT=",
				"NTM_OUTPUT_FORMAT=",
				"TOON_DEFAULT_FORMAT=",
				"NTM_ROBOT_VERBOSITY=",
				"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs),
			)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err = cmd.Run()
			exitCode := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("command failed without process exit status: %v", err)
				}
				exitCode = exitErr.ExitCode()
			}
			if exitCode != tt.expectedExit {
				t.Fatalf("exit code = %d, want %d; stdout=%s stderr=%s", exitCode, tt.expectedExit, stdout.String(), stderr.String())
			}
			if got := strings.TrimSpace(stderr.String()); got != "" {
				t.Fatalf("stderr = %q, want empty", got)
			}

			var payload map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
				t.Fatalf("stdout is not exactly one JSON document: %v\nstdout=%q", err, stdout.String())
			}
			if success, ok := payload["success"].(bool); !ok || success {
				t.Fatalf("success = %v, want false; payload=%v", payload["success"], payload)
			}
			if got, _ := payload["output_format"].(string); got != "json" {
				t.Fatalf("output_format = %q, want json; payload=%v", got, payload)
			}
			if got, _ := payload["error_code"].(string); got != tt.errorCode {
				t.Fatalf("error_code = %q, want %q; payload=%v", got, tt.errorCode, payload)
			}
			if meta, ok := payload["_meta"].(map[string]any); ok {
				if got, ok := meta["exit_code"].(float64); ok && int(got) != exitCode {
					t.Fatalf("_meta.exit_code = %d, process exit = %d", int(got), exitCode)
				}
				if tt.expectedCommand != "" && meta["command"] != tt.expectedCommand {
					t.Fatalf("_meta.command = %q, want %q; payload=%v", meta["command"], tt.expectedCommand, payload)
				}
			} else if tt.expectedCommand != "" {
				t.Fatalf("missing _meta.command %q; payload=%v", tt.expectedCommand, payload)
			}
		})
	}
}

func TestGlobalJSONProcessBoundaryOwnsExactlyOneDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level JSON contract integration in short mode")
	}
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	configHome := filepath.Join(tmpDir, "config")
	dataHome := filepath.Join(tmpDir, "data")
	for _, dir := range []string{homeDir, configHome, dataHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create isolated directory: %v", err)
		}
	}

	tests := []struct {
		name          string
		args          []string
		wantExit      int
		wantErrorCode string
	}{
		{name: "raw parse error", args: []string{"--json", "--not-a-real-flag"}, wantExit: 1, wantErrorCode: robot.ErrCodeInvalidFlag},
		{name: "raw command error", args: []string{"--json", "replay", "--last"}, wantExit: 1, wantErrorCode: robot.ErrCodeInvalidFlag},
		{name: "already owned failure", args: []string{"--json", "sessions", "show", "ntm-json-contract-missing"}, wantExit: 1},
		{name: "Codex goal validation owns failure", args: []string{"--json", "send", "proj", "goal", "--codex-goal"}, wantExit: 1, wantErrorCode: robot.ErrCodeInvalidFlag},
		{name: "profiling does not append a document", args: []string{"--json", "--profile-startup", "version"}, wantExit: 0},
		{name: "last repeated json flag enables machine mode", args: []string{"--json=false", "--json", "--profile-startup", "version"}, wantExit: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rawArgs, err := json.Marshal(test.args)
			if err != nil {
				t.Fatalf("encode helper args: %v", err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
			cmd.Dir = tmpDir
			cmd.Env = envWithOverrides(os.Environ(),
				"HOME="+homeDir,
				"XDG_CONFIG_HOME="+configHome,
				"XDG_DATA_HOME="+dataHome,
				"NTM_CONFIG=",
				"NTM_NO_COLOR=1",
				"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs),
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err = cmd.Run()
			exitCode := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run helper: %v", err)
				}
				exitCode = exitErr.ExitCode()
			}
			if exitCode != test.wantExit {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q", exitCode, test.wantExit, stdout.String(), stderr.String())
			}
			if strings.TrimSpace(stderr.String()) != "" {
				t.Fatalf("stderr=%q, want empty machine diagnostics", stderr.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
				t.Fatalf("stdout is not exactly one JSON document: %v\nstdout=%q", err, stdout.String())
			}
			if test.wantErrorCode != "" && payload["error_code"] != test.wantErrorCode {
				t.Fatalf("error_code=%v want=%s payload=%v", payload["error_code"], test.wantErrorCode, payload)
			}
		})
	}

	t.Run("last repeated json flag disables machine mode", func(t *testing.T) {
		args, err := json.Marshal([]string{"--json", "--json=false", "--not-a-real-flag"})
		if err != nil {
			t.Fatalf("encode helper args: %v", err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
		cmd.Dir = tmpDir
		cmd.Env = envWithOverrides(os.Environ(),
			"HOME="+homeDir,
			"XDG_CONFIG_HOME="+configHome,
			"XDG_DATA_HOME="+dataHome,
			"NTM_CONFIG=",
			"NTM_NO_COLOR=1",
			"NTM_ROBOT_CONTRACT_ARGS="+string(args),
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("human repeated-json process error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
		if json.Valid(stdout.Bytes()) || !strings.Contains(stderr.String(), "unknown flag") {
			t.Fatalf("last --json=false did not restore human diagnostics: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})
}

func TestGlobalJSONStartupEncryptionFailuresAreSingleDocuments(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level JSON contract integration in short mode")
	}
	tmpDir := t.TempDir()
	missingKeyConfig := filepath.Join(tmpDir, "missing-key.toml")
	invalidKeyringConfig := filepath.Join(tmpDir, "invalid-keyring.toml")
	if err := os.WriteFile(missingKeyConfig, []byte("[encryption]\nenabled = true\nkey_source = \"env\"\nkey_env = \"NTM_JSON_ENCRYPTION_KEY\"\nkey_format = \"hex\"\n"), 0o600); err != nil {
		t.Fatalf("write missing-key config: %v", err)
	}
	validKey := strings.Repeat("11", 32)
	if err := os.WriteFile(invalidKeyringConfig, []byte("[encryption]\nenabled = true\nkey_source = \"env\"\nkey_env = \"NTM_JSON_ENCRYPTION_KEY\"\nkey_format = \"hex\"\nactive_key_id = \"current\"\n[encryption.keyring]\ncurrent = \""+validKey+"\"\nold = \"not-hex\"\n"), 0o600); err != nil {
		t.Fatalf("write invalid-keyring config: %v", err)
	}

	tests := []struct {
		name      string
		config    string
		key       string
		wantError string
	}{
		{name: "missing key", config: missingKeyConfig, wantError: "encryption key resolution failed"},
		{name: "invalid keyring", config: invalidKeyringConfig, key: validKey, wantError: "encryption keyring resolution failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args, err := json.Marshal([]string{"--config", test.config, "--json", "config", "show"})
			if err != nil {
				t.Fatalf("encode helper args: %v", err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
			cmd.Dir = tmpDir
			cmd.Env = envWithOverrides(os.Environ(),
				"HOME="+filepath.Join(tmpDir, "home"),
				"XDG_CONFIG_HOME="+filepath.Join(tmpDir, "config-home"),
				"NTM_JSON_ENCRYPTION_KEY="+test.key,
				"NTM_NO_COLOR=1",
				"NTM_ROBOT_CONTRACT_ARGS="+string(args),
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("exit error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if strings.TrimSpace(stderr.String()) != "" {
				t.Fatalf("stderr=%q, want empty", stderr.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
				t.Fatalf("stdout is not exactly one JSON document: %v\nstdout=%q", err, stdout.String())
			}
			if payload["success"] != false || payload["error_code"] != robot.ErrCodeInternalError || !strings.Contains(fmt.Sprint(payload["error"]), test.wantError) {
				t.Fatalf("payload=%v, want %q INTERNAL_ERROR", payload, test.wantError)
			}
		})
	}
}

// A bad encryption key must abort the command instead of degrading to plaintext
// persistence. Human invocations get the error on stderr and a non-zero exit
// (docs/ENCRYPTION_SPEC.md, "Failure Modes").
func TestStartupEncryptionFailureFailsClosedForHumans(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level startup contract integration in short mode")
	}
	tmpDir := t.TempDir()
	missingKeyConfig := filepath.Join(tmpDir, "missing-key.toml")
	if err := os.WriteFile(missingKeyConfig, []byte("[encryption]\nenabled = true\nkey_source = \"env\"\nkey_env = \"NTM_HUMAN_ENCRYPTION_KEY\"\nkey_format = \"hex\"\n"), 0o600); err != nil {
		t.Fatalf("write missing-key config: %v", err)
	}

	args, err := json.Marshal([]string{"--config", missingKeyConfig, "config", "show"})
	if err != nil {
		t.Fatalf("encode helper args: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
	cmd.Dir = tmpDir
	cmd.Env = envWithOverrides(os.Environ(),
		"HOME="+filepath.Join(tmpDir, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(tmpDir, "config-home"),
		"NTM_HUMAN_ENCRYPTION_KEY=",
		"NTM_NO_COLOR=1",
		"NTM_ROBOT_CONTRACT_ARGS="+string(args),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("expected non-zero exit, error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "encryption key resolution failed") {
		t.Fatalf("stderr=%q, want it to report the key resolution failure", stderr.String())
	}
	if strings.Contains(stderr.String(), "encryption disabled") {
		t.Fatalf("stderr=%q, must not fall back to unencrypted persistence", stderr.String())
	}
}

func TestConfigPathFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "default", args: nil, want: config.DefaultPath()},
		{name: "equals form", args: []string{"--config=/tmp/custom.toml"}, want: "/tmp/custom.toml"},
		{name: "separate value", args: []string{"--json", "--config", "/tmp/custom.toml", "status"}, want: "/tmp/custom.toml"},
		{name: "stops at terminator", args: []string{"--", "--config", "/tmp/custom.toml"}, want: config.DefaultPath()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := configPathFromArgs(tt.args); got != tt.want {
				t.Fatalf("configPathFromArgs(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestConfigureStartupConfigPathPreservesExplicitSelection(t *testing.T) {
	originalCfgFile := cfgFile
	originalArgs := os.Args
	originalEnvPath, hadOriginalEnvPath := os.LookupEnv("NTM_CONFIG")
	t.Cleanup(func() {
		cfgFile = originalCfgFile
		os.Args = originalArgs
		if hadOriginalEnvPath {
			startup.SetConfigPath(originalEnvPath)
		} else {
			startup.SetConfigPath("")
		}
	})

	t.Run("implicit XDG default stays implicit", func(t *testing.T) {
		cfgFile = ""
		os.Args = []string{"ntm", "status"}
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
		t.Setenv("NTM_CONFIG", "")

		configureStartupConfigPath()

		if value, ok := os.LookupEnv("NTM_CONFIG"); ok {
			t.Fatalf("NTM_CONFIG = %q, want unset for implicit default", value)
		}
	})

	t.Run("config flag is propagated", func(t *testing.T) {
		cfgFile = filepath.Join(t.TempDir(), "selected.toml")
		os.Args = []string{"ntm", "status"}
		t.Setenv("NTM_CONFIG", "")

		configureStartupConfigPath()

		if got := os.Getenv("NTM_CONFIG"); got != cfgFile {
			t.Fatalf("NTM_CONFIG = %q, want explicit flag path %q", got, cfgFile)
		}
	})

	t.Run("environment selection is preserved", func(t *testing.T) {
		cfgFile = ""
		os.Args = []string{"ntm", "status"}
		envPath := filepath.Join(t.TempDir(), "selected.toml")
		t.Setenv("NTM_CONFIG", envPath)

		configureStartupConfigPath()

		if got := os.Getenv("NTM_CONFIG"); got != envPath {
			t.Fatalf("NTM_CONFIG = %q, want explicit environment path %q", got, envPath)
		}
	})
}

func TestSelectedConfigAllowsProjectionRefresh(t *testing.T) {
	originalCfgFile := cfgFile
	originalArgs := os.Args
	t.Cleanup(func() {
		cfgFile = originalCfgFile
		os.Args = originalArgs
	})

	t.Run("implicit default", func(t *testing.T) {
		cfgFile = ""
		os.Args = []string{"ntm", "status"}
		t.Setenv("NTM_CONFIG", "")
		if !selectedConfigAllowsProjectionRefresh() {
			t.Fatal("implicit default config unexpectedly blocked projection refresh")
		}
	})

	t.Run("explicit regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "selected.toml")
		if err := os.WriteFile(path, []byte("[assign]\n"), 0o600); err != nil {
			t.Fatalf("write selected config: %v", err)
		}
		cfgFile = path
		os.Args = []string{"ntm", "status"}
		t.Setenv("NTM_CONFIG", "")
		if !selectedConfigAllowsProjectionRefresh() {
			t.Fatal("regular explicit config blocked projection refresh")
		}
	})

	t.Run("explicit missing file", func(t *testing.T) {
		cfgFile = filepath.Join(t.TempDir(), "missing.toml")
		os.Args = []string{"ntm", "status"}
		t.Setenv("NTM_CONFIG", "")
		if selectedConfigAllowsProjectionRefresh() {
			t.Fatal("missing explicit config allowed projection refresh")
		}
	})

	t.Run("explicit non-regular path", func(t *testing.T) {
		cfgFile = t.TempDir()
		os.Args = []string{"ntm", "status"}
		t.Setenv("NTM_CONFIG", "")
		if selectedConfigAllowsProjectionRefresh() {
			t.Fatal("non-regular explicit config allowed projection refresh")
		}
	})

	t.Run("missing environment selection", func(t *testing.T) {
		cfgFile = ""
		os.Args = []string{"ntm", "status"}
		t.Setenv("NTM_CONFIG", filepath.Join(t.TempDir(), "missing-from-env.toml"))
		if selectedConfigAllowsProjectionRefresh() {
			t.Fatal("missing NTM_CONFIG selection allowed projection refresh")
		}
	})
}

func TestRobotAssignmentCommandRequiresPolicyPreflight(t *testing.T) {
	originalAssign := robotAssign
	originalBulkAssign := robotBulkAssign
	originalSpawn := robotSpawn
	originalSpawnAssignWork := robotSpawnAssignWork
	originalRestartPane := robotRestartPane
	originalRestartPaneBead := robotRestartPaneBead
	t.Cleanup(func() {
		robotAssign = originalAssign
		robotBulkAssign = originalBulkAssign
		robotSpawn = originalSpawn
		robotSpawnAssignWork = originalSpawnAssignWork
		robotRestartPane = originalRestartPane
		robotRestartPaneBead = originalRestartPaneBead
	})

	tests := []struct {
		name            string
		assign          string
		bulkAssign      string
		spawn           string
		spawnAssignWork bool
		restartPane     string
		restartBead     string
		want            bool
	}{
		{name: "assignment recommendations", assign: "project", want: true},
		{name: "bulk assignment", bulkAssign: "project", want: true},
		{name: "spawn with assignment", spawn: "project", spawnAssignWork: true, want: true},
		{name: "restart with bead assignment", restartPane: "project", restartBead: "ntm-123", want: true},
		{name: "spawn without assignment", spawn: "project"},
		{name: "assignment flag without spawn", spawnAssignWork: true},
		{name: "restart without bead assignment", restartPane: "project"},
		{name: "bead flag without restart", restartBead: "ntm-123"},
		{name: "no assignment mutation"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			robotAssign = test.assign
			robotBulkAssign = test.bulkAssign
			robotSpawn = test.spawn
			robotSpawnAssignWork = test.spawnAssignWork
			robotRestartPane = test.restartPane
			robotRestartPaneBead = test.restartBead

			if got := robotAssignmentCommandRequiresPolicyPreflight(); got != test.want {
				t.Fatalf("robotAssignmentCommandRequiresPolicyPreflight() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPluginsListUsesGlobalConfigFlag(t *testing.T) {
	resetFlags()
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg = nil
	cfgFile = ""
	startup.ResetConfig()
	cfgHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		startup.ResetConfig()
	})

	customPath := filepath.Join(t.TempDir(), "custom", "ntm.toml")
	commandsDir := filepath.Join(filepath.Dir(customPath), "commands")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(commandsDir) failed: %v", err)
	}
	pluginPath := filepath.Join(commandsDir, "sample-plugin")
	pluginScript := "#" + "!/bin/sh\n# Description: Sample plugin\necho sample\n"
	if err := os.WriteFile(pluginPath, []byte(pluginScript), 0o755); err != nil {
		t.Fatalf("WriteFile(plugin) failed: %v", err)
	}

	out, err := captureStdout(t, func() error {
		rootCmd.SetArgs([]string{"--config", customPath, "plugins", "list"})
		return rootCmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute() failed: %v", err)
	}
	if !strings.Contains(out, "sample-plugin") {
		t.Fatalf("plugins list output = %q, want sample-plugin", out)
	}
}

func TestCommandPluginDiscoveryUsesGlobalConfigFlag(t *testing.T) {
	root := repoRoot(t)
	base := t.TempDir()
	customPath := filepath.Join(base, "custom", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(config dir) failed: %v", err)
	}
	configBody := "projects_base = \"" + filepath.Join(base, "projects") + "\"\n"
	if err := os.WriteFile(customPath, []byte(configBody), 0o644); err != nil {
		t.Fatalf("WriteFile(config) failed: %v", err)
	}
	commandsDir := filepath.Join(filepath.Dir(customPath), "commands")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(commandsDir) failed: %v", err)
	}
	pluginPath := filepath.Join(commandsDir, "print-config-path")
	pluginScript := "#" + "!/bin/sh\nprintf '%s' \"$NTM_CONFIG_PATH\"\n"
	if err := os.WriteFile(pluginPath, []byte(pluginScript), 0o755); err != nil {
		t.Fatalf("WriteFile(plugin) failed: %v", err)
	}

	stdout, stderr, code := runNTM(t, root, "--config", customPath, "print-config-path")
	if code != 0 {
		t.Fatalf("plugin command failed (code=%d): %s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != customPath {
		t.Fatalf("plugin stdout = %q, want %q", got, customPath)
	}
}

func TestSpawnAndAddPluginFlagsUseGlobalConfigFlag(t *testing.T) {
	root := repoRoot(t)
	base := t.TempDir()
	customPath := filepath.Join(base, "custom", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(config dir) failed: %v", err)
	}
	configBody := fmt.Sprintf("projects_base = %q\n", filepath.Join(base, "projects"))
	if err := os.WriteFile(customPath, []byte(configBody), 0o644); err != nil {
		t.Fatalf("WriteFile(config) failed: %v", err)
	}
	agentsDir := filepath.Join(filepath.Dir(customPath), "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(agentsDir) failed: %v", err)
	}
	pluginBody := "[agent]\nname = \"reviewbot\"\nalias = \"rb\"\ncommand = \"echo reviewbot\"\ndescription = \"Review Bot\"\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewbot.toml"), []byte(pluginBody), 0o644); err != nil {
		t.Fatalf("WriteFile(agent plugin) failed: %v", err)
	}

	for _, subcommand := range []string{"spawn", "add"} {
		t.Run(subcommand, func(t *testing.T) {
			stdout, stderr, code := runNTM(t, root, "--config", customPath, subcommand, "--help")
			if code != 0 {
				t.Fatalf("%s --help failed (code=%d): %s", subcommand, code, stderr)
			}
			if !strings.Contains(stdout, "--reviewbot") {
				t.Fatalf("%s --help output missing --reviewbot: %q", subcommand, stdout)
			}
			if !strings.Contains(stdout, "--rb") {
				t.Fatalf("%s --help output missing --rb alias: %q", subcommand, stdout)
			}
		})
	}
}

// TestMultipleSubcommands tests various subcommand combinations
func TestMultipleSubcommands(t *testing.T) {
	helpCommands := []string{
		"spawn --help",
		"add --help",
		"send --help",
		"create --help",
		"quick --help",
		"view --help",
		"zoom --help",
		"copy --help",
		"save --help",
		"kill --help",
		"attach --help",
		"list --help",
		"status --help",
		"config --help",
	}

	for _, cmd := range helpCommands {
		t.Run(cmd, func(t *testing.T) {
			resetFlags()
			args := strings.Split(cmd, " ")
			rootCmd.SetArgs(args)

			err := rootCmd.Execute()
			if err != nil {
				t.Fatalf("%s failed: %v", cmd, err)
			}
		})
	}
}

// TestVerifyUpgrade exercises the post-upgrade verifier through its real
// subprocess boundary. The fixture rejects any invocation other than
// `version --short`, so this test covers both the command contract and exact
// normalized version matching.
func TestVerifyUpgrade(t *testing.T) {
	tests := []struct {
		name            string
		expectedVersion string
		actualOutput    string
		shouldFail      bool
	}{
		{
			name:            "exact match",
			expectedVersion: "1.4.1",
			actualOutput:    "1.4.1",
			shouldFail:      false,
		},
		{
			name:            "match with v prefix in expected",
			expectedVersion: "v1.4.1",
			actualOutput:    "1.4.1",
			shouldFail:      false,
		},
		{
			name:            "match with v prefix in actual",
			expectedVersion: "1.4.1",
			actualOutput:    "v1.4.1",
			shouldFail:      false,
		},
		{
			name:            "mismatch major version",
			expectedVersion: "2.0.0",
			actualOutput:    "1.4.1",
			shouldFail:      true,
		},
		{
			name:            "mismatch minor version",
			expectedVersion: "1.5.0",
			actualOutput:    "1.4.1",
			shouldFail:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("upgrade verifier fixture requires a POSIX shell")
			}
			binaryPath := filepath.Join(t.TempDir(), "ntm")
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" != "version" ] || [ "$2" != "--short" ]; then
  exit 64
fi
printf '%%s\n' %q
`, tc.actualOutput)
			if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
				t.Fatalf("write fake upgraded binary: %v", err)
			}

			err := verifyUpgrade(binaryPath, tc.expectedVersion)
			if tc.shouldFail {
				if err == nil {
					t.Fatalf("verifyUpgrade(%q, %q) succeeded, want mismatch error", tc.actualOutput, tc.expectedVersion)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifyUpgrade(%q, %q) error: %v", tc.actualOutput, tc.expectedVersion, err)
			}
		})
	}
}

// TestRestoreBackup tests the backup restoration logic
func TestRestoreBackup(t *testing.T) {
	// Create a temp directory for test files
	tempDir, err := os.MkdirTemp("", "ntm-restore-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("successful restore", func(t *testing.T) {
		currentPath := filepath.Join(tempDir, "ntm-current")
		backupPath := currentPath + ".old"

		// Create "broken" current binary
		if err := os.WriteFile(currentPath, []byte("broken"), 0755); err != nil {
			t.Fatalf("Failed to create current file: %v", err)
		}

		// Create "working" backup
		if err := os.WriteFile(backupPath, []byte("working"), 0755); err != nil {
			t.Fatalf("Failed to create backup file: %v", err)
		}

		// Restore
		if err := restoreBackup(currentPath, backupPath); err != nil {
			t.Fatalf("restoreBackup failed: %v", err)
		}

		// Verify current has backup content
		content, err := os.ReadFile(currentPath)
		if err != nil {
			t.Fatalf("Failed to read restored file: %v", err)
		}
		if string(content) != "working" {
			t.Errorf("Restored content mismatch: got %q, want %q", string(content), "working")
		}

		// Verify backup was removed (renamed to current)
		if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
			t.Error("Backup file should not exist after restore")
		}
	})

	t.Run("backup not found", func(t *testing.T) {
		currentPath := filepath.Join(tempDir, "ntm-nonexistent")
		backupPath := currentPath + ".old"

		err := restoreBackup(currentPath, backupPath)
		if err == nil {
			t.Error("Expected error when backup doesn't exist")
		}
		if !strings.Contains(err.Error(), "backup file not found") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})
}

// TestVerifyChecksum tests the SHA256 checksum verification
func TestVerifyChecksum(t *testing.T) {
	// Create a temp directory for test files
	tempDir, err := os.MkdirTemp("", "ntm-checksum-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("valid checksum", func(t *testing.T) {
		testContent := []byte("test content for checksum verification")
		testFile := filepath.Join(tempDir, "test-valid.bin")
		if err := os.WriteFile(testFile, testContent, 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		// Compute the actual hash for the test content
		h := sha256.Sum256(testContent)
		expectedHash := hex.EncodeToString(h[:])

		err := verifyChecksum(testFile, expectedHash)
		if err != nil {
			t.Errorf("verifyChecksum failed for valid file: %v", err)
		}
	})

	t.Run("invalid checksum", func(t *testing.T) {
		testContent := []byte("test content")
		testFile := filepath.Join(tempDir, "test-invalid.bin")
		if err := os.WriteFile(testFile, testContent, 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
		err := verifyChecksum(testFile, wrongHash)
		if err == nil {
			t.Error("Expected error for checksum mismatch")
		}
		if !strings.Contains(err.Error(), "checksum mismatch") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		err := verifyChecksum(filepath.Join(tempDir, "nonexistent"), "somehash")
		if err == nil {
			t.Error("Expected error for nonexistent file")
		}
	})

	t.Run("case insensitive hash", func(t *testing.T) {
		testContent := []byte("case test")
		testFile := filepath.Join(tempDir, "test-case.bin")
		if err := os.WriteFile(testFile, testContent, 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		h := sha256.Sum256(testContent)
		lowerHash := hex.EncodeToString(h[:])
		upperHash := strings.ToUpper(lowerHash)

		// Both upper and lower case should work
		if err := verifyChecksum(testFile, upperHash); err != nil {
			t.Errorf("Upper case hash should work: %v", err)
		}
		if err := verifyChecksum(testFile, lowerHash); err != nil {
			t.Errorf("Lower case hash should work: %v", err)
		}
	})
}

func writeInstallScriptArchive(t *testing.T, path string) {
	t.Helper()

	payload := []byte(`#!/bin/sh
if [ "${1:-}" = "version" ]; then
    printf '%s\n' 'ntm fixture v1.19.2'
    exit 0
fi
exit 1
`)
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "ntm",
		Mode: 0o755,
		Size: int64(len(payload)),
	}); err != nil {
		t.Fatalf("write installer fixture header: %v", err)
	}
	if _, err := tarWriter.Write(payload); err != nil {
		t.Fatalf("write installer fixture payload: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close installer fixture tar stream: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close installer fixture gzip stream: %v", err)
	}
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatalf("write installer fixture archive: %v", err)
	}
}

func installScriptTestPATH(t *testing.T, binDir string) string {
	t.Helper()

	// Exclude host downloaders and rm so the fixture cannot reach the network or
	// execute the installer's recursive cleanup command.
	required := []string{
		"awk", "cat", "chmod", "cp", "grep", "gzip", "head", "mkdir",
		"mv", "sed", "tar", "tr", "uname",
	}
	for _, name := range required {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("required installer test command %q not found: %v", name, err)
		}
		if err := os.Symlink(path, filepath.Join(binDir, name)); err != nil {
			t.Fatalf("link installer test command %q: %v", name, err)
		}
	}

	for _, name := range []string{"sha256sum", "shasum"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if err := os.Symlink(path, filepath.Join(binDir, name)); err != nil {
			t.Fatalf("link installer checksum command %q: %v", name, err)
		}
		return binDir
	}
	t.Fatalf("installer test requires sha256sum or shasum")
	return ""
}

func TestInstallScriptChecksumSourcesEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash installer E2E requires a Unix host")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	var platformOS string
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd":
		platformOS = runtime.GOOS
	default:
		t.Skipf("installer does not support E2E host OS %q", runtime.GOOS)
	}
	var platformArch string
	switch runtime.GOARCH {
	case "amd64", "arm64":
		platformArch = runtime.GOARCH
	case "arm":
		platformArch = "armv7"
	default:
		t.Skipf("installer does not support E2E host architecture %q", runtime.GOARCH)
	}

	const version = "v1.19.2"
	assetName := fmt.Sprintf("ntm_1.19.2_%s_%s.tar.gz", platformOS, platformArch)
	apiURL := "https://api.github.com/repos/Dicklesworthstone/ntm/releases/tags/" + version
	releaseBase := "https://github.com/Dicklesworthstone/ntm/releases/download/" + version
	installScript := filepath.Join(repoRoot(t), "install.sh")

	const fakeDownloader = `#!/bin/bash
set -euo pipefail
out=""
url=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o|-O)
            out="$2"
            shift 2
            ;;
		-qO-)
			out="-"
			shift
			;;
        -*)
            shift
            ;;
        *)
            url="$1"
            shift
            ;;
    esac
done

copy_fixture() {
	local source_path="$1"
	if [ -z "$out" ] || [ "$out" = "-" ]; then
		cat "$source_path"
	else
		cp "$source_path" "$out"
	fi
}

printf '%s\n' "$url" >> "$NTM_TEST_REQUEST_LOG"
if [ "$url" = "$NTM_TEST_API_URL" ]; then
	copy_fixture "$NTM_TEST_RELEASE_JSON"
	exit 0
fi

case "$url" in
	"$NTM_TEST_RELEASE_BASE/$NTM_TEST_ASSET_NAME")
		copy_fixture "$NTM_TEST_ASSET"
		;;
	"$NTM_TEST_RELEASE_BASE/checksums.txt")
		case "$NTM_TEST_CHECKSUM_MODE" in
			goreleaser) copy_fixture "$NTM_TEST_GO_CHECKSUMS" ;;
			mismatch) copy_fixture "$NTM_TEST_BAD_CHECKSUMS" ;;
			*) exit 22 ;;
		esac
        ;;
	"$NTM_TEST_RELEASE_BASE/SHA256SUMS")
		case "$NTM_TEST_CHECKSUM_MODE" in
			aggregate) copy_fixture "$NTM_TEST_DSR_CHECKSUMS" ;;
			aggregate-mismatch) copy_fixture "$NTM_TEST_BAD_CHECKSUMS" ;;
			*) exit 22 ;;
		esac
        ;;
	"$NTM_TEST_RELEASE_BASE/$NTM_TEST_ASSET_NAME.sha256")
		case "$NTM_TEST_CHECKSUM_MODE" in
			sidecar) copy_fixture "$NTM_TEST_DSR_SIDECAR" ;;
			sidecar-mismatch) copy_fixture "$NTM_TEST_BAD_CHECKSUMS" ;;
			*) exit 22 ;;
		esac
        ;;
    *)
        exit 22
        ;;
esac
`
	const fakeCleanup = `#!/bin/bash
set -euo pipefail
if [ "$#" -ne 2 ] || [ "$1" != "-rf" ]; then
	exit 97
fi
case "$2" in
	"$NTM_TEST_TMPROOT"/*) ;;
	*) exit 98 ;;
esac
printf '%s\n' "$*" >> "$NTM_TEST_CLEANUP_LOG"
`
	const fakeMktemp = `#!/bin/bash
set -euo pipefail
if [ "$#" -ne 1 ] || [ "$1" != "-d" ]; then
	exit 96
fi
dir="$NTM_TEST_TMPROOT/ntm-installer"
mkdir "$dir"
printf '%s\n' "$dir"
`

	cases := []struct {
		name             string
		mode             string
		wantSuccess      bool
		checksumRequests []string
		wantOutput       string
	}{
		// Probe order is SHA256SUMS first (what current DSR releases actually
		// publish), then the legacy checksums.txt, then the per-asset sidecar
		// (#247). Probes for absent candidates fall through silently.
		{name: "GoReleaser aggregate", mode: "goreleaser", wantSuccess: true, checksumRequests: []string{"SHA256SUMS", "checksums.txt"}},
		{name: "DSR aggregate", mode: "aggregate", wantSuccess: true, checksumRequests: []string{"SHA256SUMS"}},
		{name: "DSR per-asset sidecar", mode: "sidecar", wantSuccess: true, checksumRequests: []string{"SHA256SUMS", "checksums.txt", assetName + ".sha256"}},
		{name: "missing checksums", mode: "missing", checksumRequests: []string{"SHA256SUMS", "checksums.txt", assetName + ".sha256"}, wantOutput: "Could not download release checksums"},
		{name: "mismatched legacy aggregate fails closed", mode: "mismatch", checksumRequests: []string{"SHA256SUMS", "checksums.txt"}, wantOutput: "Checksum mismatch"},
		{name: "mismatched DSR aggregate fails closed", mode: "aggregate-mismatch", checksumRequests: []string{"SHA256SUMS"}, wantOutput: "Checksum mismatch"},
		{name: "mismatched DSR sidecar fails closed", mode: "sidecar-mismatch", checksumRequests: []string{"SHA256SUMS", "checksums.txt", assetName + ".sha256"}, wantOutput: "Checksum mismatch"},
	}

	for _, downloader := range []string{"curl", "wget"} {
		for _, test := range cases {
			t.Run(downloader+"/"+test.name, func(t *testing.T) {
				dir := t.TempDir()
				assetPath := filepath.Join(dir, assetName)
				writeInstallScriptArchive(t, assetPath)
				asset, err := os.ReadFile(assetPath)
				if err != nil {
					t.Fatalf("read installer fixture archive: %v", err)
				}
				digest := sha256.Sum256(asset)
				validLine := fmt.Sprintf("%x  %s\n", digest, assetName)
				invalidLine := fmt.Sprintf("%064x  %s\n", 0, assetName)

				releaseJSON := filepath.Join(dir, "release.json")
				goChecksums := filepath.Join(dir, "go-checksums.txt")
				dsrChecksums := filepath.Join(dir, "dsr-checksums.txt")
				dsrSidecar := filepath.Join(dir, "dsr-sidecar.txt")
				badChecksums := filepath.Join(dir, "bad-checksums.txt")
				for path, content := range map[string]string{
					releaseJSON:  fmt.Sprintf(`{"tag_name":%q,"assets":[{"name":%q}]}\n`, version, assetName),
					goChecksums:  validLine,
					dsrChecksums: validLine,
					dsrSidecar:   validLine,
					badChecksums: invalidLine,
				} {
					if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
						t.Fatalf("write checksum fixture %s: %v", path, err)
					}
				}

				binDir := filepath.Join(dir, "bin")
				if err := os.Mkdir(binDir, 0o755); err != nil {
					t.Fatalf("create fake bin: %v", err)
				}
				if err := os.WriteFile(filepath.Join(binDir, downloader), []byte(fakeDownloader), 0o755); err != nil {
					t.Fatalf("write fake %s: %v", downloader, err)
				}
				if err := os.WriteFile(filepath.Join(binDir, "rm"), []byte(fakeCleanup), 0o755); err != nil {
					t.Fatalf("write fake cleanup command: %v", err)
				}
				if err := os.WriteFile(filepath.Join(binDir, "mktemp"), []byte(fakeMktemp), 0o755); err != nil {
					t.Fatalf("write fake mktemp command: %v", err)
				}

				requestLog := filepath.Join(dir, "requests.log")
				cleanupLog := filepath.Join(dir, "cleanup.log")
				tmpRoot := filepath.Join(dir, "tmp")
				if err := os.Mkdir(tmpRoot, 0o755); err != nil {
					t.Fatalf("create installer temp root: %v", err)
				}
				installDir := filepath.Join(dir, "installed")
				cmd := exec.Command(bashPath, installScript, "--version="+version, "--dir="+installDir, "--no-shell")
				cmd.Env = envWithOverrides(os.Environ(),
					"HOME="+filepath.Join(dir, "home"),
					"PATH="+installScriptTestPATH(t, binDir),
					"NTM_TEST_API_URL="+apiURL,
					"NTM_TEST_ASSET_NAME="+assetName,
					"NTM_TEST_ASSET="+assetPath,
					"NTM_TEST_CHECKSUM_MODE="+test.mode,
					"NTM_TEST_REQUEST_LOG="+requestLog,
					"NTM_TEST_RELEASE_JSON="+releaseJSON,
					"NTM_TEST_RELEASE_BASE="+releaseBase,
					"NTM_TEST_GO_CHECKSUMS="+goChecksums,
					"NTM_TEST_DSR_CHECKSUMS="+dsrChecksums,
					"NTM_TEST_DSR_SIDECAR="+dsrSidecar,
					"NTM_TEST_BAD_CHECKSUMS="+badChecksums,
					"NTM_TEST_CLEANUP_LOG="+cleanupLog,
					"NTM_TEST_TMPROOT="+tmpRoot,
				)
				output, err := cmd.CombinedOutput()
				if test.wantSuccess && err != nil {
					t.Fatalf("installer E2E failed: %v; output:\n%s", err, output)
				}
				if !test.wantSuccess && err == nil {
					t.Fatalf("installer E2E unexpectedly succeeded; output:\n%s", output)
				}
				if !test.wantSuccess {
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
						t.Fatalf("installer exit = %v, want status 1; output:\n%s", err, output)
					}
				}
				if test.wantOutput != "" && !strings.Contains(string(output), test.wantOutput) {
					t.Fatalf("installer output missing %q:\n%s", test.wantOutput, output)
				}

				requests, err := os.ReadFile(requestLog)
				if err != nil {
					t.Fatalf("read request log: %v", err)
				}
				gotRequests := strings.Split(strings.TrimSpace(string(requests)), "\n")
				wantRequests := []string{apiURL, releaseBase + "/" + assetName}
				for _, name := range test.checksumRequests {
					wantRequests = append(wantRequests, releaseBase+"/"+name)
				}
				if !reflect.DeepEqual(gotRequests, wantRequests) {
					t.Fatalf("installer requests = %q, want %q", gotRequests, wantRequests)
				}

				cleanup, err := os.ReadFile(cleanupLog)
				if err != nil {
					t.Fatalf("read installer cleanup log: %v", err)
				}
				cleanupLine := strings.TrimSpace(string(cleanup))
				wantCleanup := "-rf " + filepath.Join(tmpRoot, "ntm-installer")
				if cleanupLine != wantCleanup {
					t.Fatalf("installer cleanup target = %q, want %q", cleanupLine, wantCleanup)
				}

				installedBinary := filepath.Join(installDir, "ntm")
				if !test.wantSuccess {
					if _, statErr := os.Stat(installedBinary); !os.IsNotExist(statErr) {
						t.Fatalf("failed install left binary behind; stat error = %v", statErr)
					}
					return
				}

				versionOutput, err := exec.Command(installedBinary, "version").CombinedOutput()
				if err != nil {
					t.Fatalf("execute installed fixture: %v; output: %s", err, versionOutput)
				}
				if got := strings.TrimSpace(string(versionOutput)); got != "ntm fixture v1.19.2" {
					t.Fatalf("installed fixture output = %q", got)
				}
			})
		}
	}
}

func TestInstallScriptExecutionEntryPoints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash installer contract requires a Unix host")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	installScript := filepath.Join(repoRoot(t), "install.sh")
	script, err := os.ReadFile(installScript)
	if err != nil {
		t.Fatalf("read install script: %v", err)
	}
	args := []string{"--version=invalid", "--dir=" + t.TempDir(), "--no-shell"}
	tests := []struct {
		name string
		cmd  *exec.Cmd
	}{
		{name: "direct", cmd: exec.Command("bash", append([]string{installScript}, args...)...)},
		{name: "stdin", cmd: exec.Command("bash", append([]string{"-s", "--"}, args...)...)},
	}
	tests[1].cmd.Stdin = bytes.NewReader(script)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := test.cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("installer did not execute; output:\n%s", output)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("installer exit = %v, want status 1; output:\n%s", err, output)
			}
			if !strings.Contains(string(output), "Invalid version: invalid") {
				t.Fatalf("installer did not reach version validation; output:\n%s", output)
			}
		})
	}
}

// TestProgressWriter tests the download progress writer
func TestProgressWriter(t *testing.T) {
	t.Run("write updates downloaded count", func(t *testing.T) {
		var buf bytes.Buffer
		pw := &progressWriter{
			writer:     &buf,
			total:      100,
			startTime:  time.Now(),
			lastUpdate: time.Now().Add(-time.Second), // Force immediate update
			isTTY:      false,                        // Disable TTY output for test
		}

		data := []byte("hello")
		n, err := pw.Write(data)
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != len(data) {
			t.Errorf("Write returned %d, want %d", n, len(data))
		}
		if pw.downloaded != int64(len(data)) {
			t.Errorf("downloaded = %d, want %d", pw.downloaded, len(data))
		}
		if buf.String() != "hello" {
			t.Errorf("buffer content = %q, want %q", buf.String(), "hello")
		}
	})

	t.Run("formatSize handles various sizes", func(t *testing.T) {
		tests := []struct {
			bytes int64
			want  string
		}{
			{0, "0 B"},
			{512, "512 B"},
			{1024, "1.0 KB"},
			{1536, "1.5 KB"},
			{1048576, "1.0 MB"},
			{10485760, "10.0 MB"},
		}

		for _, tc := range tests {
			got := formatSize(tc.bytes)
			if got != tc.want {
				t.Errorf("formatSize(%d) = %q, want %q", tc.bytes, got, tc.want)
			}
		}
	})
}

// TestHasLegacyShellIntegration tests detection of legacy "ntm init" shell commands
func TestHasLegacyShellIntegration(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ntm-shell-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("detects legacy ntm init bash", func(t *testing.T) {
		rcFile := filepath.Join(tempDir, ".bashrc")
		content := `# Some config
export PATH="/usr/local/bin:$PATH"

# NTM - Named Tmux Manager
eval "$(ntm init bash)"
`
		if err := os.WriteFile(rcFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		if !hasLegacyShellIntegration(rcFile) {
			t.Error("Expected to detect legacy shell integration")
		}
	})

	t.Run("detects legacy ntm init zsh", func(t *testing.T) {
		rcFile := filepath.Join(tempDir, ".zshrc")
		content := `# Some config
eval "$(ntm init zsh)"
`
		if err := os.WriteFile(rcFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		if !hasLegacyShellIntegration(rcFile) {
			t.Error("Expected to detect legacy shell integration")
		}
	})

	t.Run("detects legacy ntm init fish", func(t *testing.T) {
		rcFile := filepath.Join(tempDir, "config.fish")
		content := `# Fish config
ntm init fish | source
`
		if err := os.WriteFile(rcFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		if !hasLegacyShellIntegration(rcFile) {
			t.Error("Expected to detect legacy shell integration")
		}
	})

	t.Run("does not detect current ntm shell", func(t *testing.T) {
		rcFile := filepath.Join(tempDir, ".bashrc-current")
		content := `# Some config
eval "$(ntm shell bash)"
`
		if err := os.WriteFile(rcFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		if hasLegacyShellIntegration(rcFile) {
			t.Error("Should not detect current shell command as legacy")
		}
	})

	t.Run("handles nonexistent file", func(t *testing.T) {
		if hasLegacyShellIntegration(filepath.Join(tempDir, "nonexistent")) {
			t.Error("Should return false for nonexistent file")
		}
	})
}

// TestUpgradeShellRCFile tests the shell rc file upgrade function
func TestUpgradeShellRCFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ntm-upgrade-shell-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("upgrades ntm init to ntm shell for bash", func(t *testing.T) {
		rcFile := filepath.Join(tempDir, ".bashrc")
		originalContent := `# Some config
export PATH="/usr/local/bin:$PATH"

# NTM - Named Tmux Manager
eval "$(ntm init bash)"
`
		if err := os.WriteFile(rcFile, []byte(originalContent), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		if err := upgradeShellRCFile(rcFile); err != nil {
			t.Fatalf("upgradeShellRCFile failed: %v", err)
		}

		content, err := os.ReadFile(rcFile)
		if err != nil {
			t.Fatalf("Failed to read upgraded file: %v", err)
		}

		if strings.Contains(string(content), "ntm init") {
			t.Error("File should not contain 'ntm init' after upgrade")
		}
		if !strings.Contains(string(content), "ntm shell bash") {
			t.Error("File should contain 'ntm shell bash' after upgrade")
		}

		// Verify backup was created
		backupPath := rcFile + ".ntm-backup"
		backupContent, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatalf("Failed to read backup file: %v", err)
		}
		if string(backupContent) != originalContent {
			t.Error("Backup should contain original content")
		}
	})

	t.Run("upgrades ntm init to ntm shell for zsh", func(t *testing.T) {
		rcFile := filepath.Join(tempDir, ".zshrc")
		originalContent := `eval "$(ntm init zsh)"`
		if err := os.WriteFile(rcFile, []byte(originalContent), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		if err := upgradeShellRCFile(rcFile); err != nil {
			t.Fatalf("upgradeShellRCFile failed: %v", err)
		}

		content, err := os.ReadFile(rcFile)
		if err != nil {
			t.Fatalf("Failed to read upgraded file: %v", err)
		}

		if !strings.Contains(string(content), "ntm shell zsh") {
			t.Error("File should contain 'ntm shell zsh' after upgrade")
		}
	})

	t.Run("upgrades ntm init to ntm shell for fish", func(t *testing.T) {
		rcFile := filepath.Join(tempDir, "config.fish")
		originalContent := `ntm init fish | source`
		if err := os.WriteFile(rcFile, []byte(originalContent), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		if err := upgradeShellRCFile(rcFile); err != nil {
			t.Fatalf("upgradeShellRCFile failed: %v", err)
		}

		content, err := os.ReadFile(rcFile)
		if err != nil {
			t.Fatalf("Failed to read upgraded file: %v", err)
		}

		if !strings.Contains(string(content), "ntm shell fish") {
			t.Error("File should contain 'ntm shell fish' after upgrade")
		}
	})

	t.Run("returns error for nonexistent file", func(t *testing.T) {
		err := upgradeShellRCFile(filepath.Join(tempDir, "nonexistent"))
		if err == nil {
			t.Error("Expected error for nonexistent file")
		}
	})
}

type scanRunnerFunc func(context.Context, string, scanner.ScanOptions) (*scanner.ScanResult, error)

func (f scanRunnerFunc) Scan(ctx context.Context, path string, opts scanner.ScanOptions) (*scanner.ScanResult, error) {
	return f(ctx, path, opts)
}

func TestRunScanJSONDependencyAndExecutionFailures(t *testing.T) {
	oldJSONOutput := jsonOutput
	oldScanIsAvailable := scanIsAvailable
	oldNewScanRunner := newScanRunner
	jsonOutput = true
	t.Cleanup(func() {
		jsonOutput = oldJSONOutput
		scanIsAvailable = oldScanIsAvailable
		newScanRunner = oldNewScanRunner
	})

	t.Run("missing UBS", func(t *testing.T) {
		scanIsAvailable = func() bool { return false }
		stdout, runErr := captureStdout(t, func() error {
			return runScan("/project", scanner.ScanOptions{}, false, false, false, scanner.BridgeConfig{}, false, false, false)
		})
		if !errors.Is(runErr, errJSONFailure) || !errors.Is(runErr, scanner.ErrNotInstalled) {
			t.Fatalf("error = %v, want errJSONFailure joined with ErrNotInstalled", runErr)
		}
		assertScanJSONResult(t, stdout, false)
	})

	t.Run("scanner construction failure", func(t *testing.T) {
		cause := errors.New("scanner construction sentinel")
		scanIsAvailable = func() bool { return true }
		newScanRunner = func() (scanRunner, error) { return nil, cause }
		stdout, runErr := captureStdout(t, func() error {
			return runScan("/project", scanner.ScanOptions{}, false, false, false, scanner.BridgeConfig{}, false, false, false)
		})
		if !errors.Is(runErr, errJSONFailure) || !errors.Is(runErr, cause) {
			t.Fatalf("error = %v, want errJSONFailure joined with construction cause", runErr)
		}
		assertScanJSONResult(t, stdout, false)
	})

	t.Run("UBS execution failure", func(t *testing.T) {
		cause := errors.New("ubs execution sentinel")
		scanIsAvailable = func() bool { return true }
		newScanRunner = func() (scanRunner, error) {
			return scanRunnerFunc(func(context.Context, string, scanner.ScanOptions) (*scanner.ScanResult, error) {
				return nil, cause
			}), nil
		}
		stdout, runErr := captureStdout(t, func() error {
			return runScan("/project", scanner.ScanOptions{}, false, false, false, scanner.BridgeConfig{}, false, false, false)
		})
		if !errors.Is(runErr, errJSONFailure) || !errors.Is(runErr, cause) {
			t.Fatalf("error = %v, want errJSONFailure joined with UBS cause", runErr)
		}
		assertScanJSONResult(t, stdout, false)
	})
}

func TestRunScanJSONSeverityParity(t *testing.T) {
	oldJSONOutput := jsonOutput
	oldScanIsAvailable := scanIsAvailable
	oldNewScanRunner := newScanRunner
	jsonOutput = true
	scanIsAvailable = func() bool { return true }
	t.Cleanup(func() {
		jsonOutput = oldJSONOutput
		scanIsAvailable = oldScanIsAvailable
		newScanRunner = oldNewScanRunner
	})

	tests := []struct {
		name          string
		totals        scanner.ScanTotals
		failOnWarning bool
		wantFailure   bool
	}{
		{name: "warning allowed", totals: scanner.ScanTotals{Warning: 1}, wantFailure: false},
		{name: "warning blocked", totals: scanner.ScanTotals{Warning: 1}, failOnWarning: true, wantFailure: true},
		{name: "critical always blocked", totals: scanner.ScanTotals{Critical: 1}, wantFailure: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &scanner.ScanResult{Project: "/project", Totals: tt.totals}
			newScanRunner = func() (scanRunner, error) {
				return scanRunnerFunc(func(context.Context, string, scanner.ScanOptions) (*scanner.ScanResult, error) {
					return result, nil
				}), nil
			}
			stdout, runErr := captureStdout(t, func() error {
				return runScan("/project", scanner.ScanOptions{FailOnWarning: tt.failOnWarning}, false, false, false, scanner.BridgeConfig{}, false, false, false)
			})
			if tt.wantFailure {
				if !errors.Is(runErr, errJSONFailure) {
					t.Fatalf("error = %v, want errJSONFailure", runErr)
				}
			} else if runErr != nil {
				t.Fatalf("error = %v, want nil", runErr)
			}
			assertScanJSONResult(t, stdout, !tt.wantFailure)
		})
	}
}

func TestRunScanJSONRequestedIntegrationFailuresAreTerminal(t *testing.T) {
	oldJSONOutput := jsonOutput
	oldScanIsAvailable := scanIsAvailable
	oldNewScanRunner := newScanRunner
	oldCreateBeadsFromScan := createBeadsFromScan
	oldUpdateBeadsFromScan := updateBeadsFromScan
	oldNotifyScanResults := notifyScanResults
	jsonOutput = true
	scanIsAvailable = func() bool { return true }
	result := &scanner.ScanResult{
		Project:  "/project",
		Totals:   scanner.ScanTotals{Warning: 1},
		Findings: []scanner.Finding{{File: "main.go", Severity: scanner.SeverityWarning, Message: "warning"}},
	}
	newScanRunner = func() (scanRunner, error) {
		return scanRunnerFunc(func(context.Context, string, scanner.ScanOptions) (*scanner.ScanResult, error) {
			return result, nil
		}), nil
	}
	t.Cleanup(func() {
		jsonOutput = oldJSONOutput
		scanIsAvailable = oldScanIsAvailable
		newScanRunner = oldNewScanRunner
		createBeadsFromScan = oldCreateBeadsFromScan
		updateBeadsFromScan = oldUpdateBeadsFromScan
		notifyScanResults = oldNotifyScanResults
	})

	tests := []struct {
		name        string
		createBeads bool
		updateBeads bool
		notify      bool
		wantError   string
		configure   func()
	}{
		{
			name: "create returns error", createBeads: true, wantError: "create sentinel",
			configure: func() {
				createBeadsFromScan = func(*scanner.ScanResult, scanner.BridgeConfig) (*scanner.BridgeResult, error) {
					return nil, errors.New("create sentinel")
				}
			},
		},
		{
			name: "create reports partial errors", createBeads: true, wantError: "1 operation(s) failed",
			configure: func() {
				createBeadsFromScan = func(*scanner.ScanResult, scanner.BridgeConfig) (*scanner.BridgeResult, error) {
					return &scanner.BridgeResult{Errors: 1}, nil
				}
			},
		},
		{
			name: "update returns error", updateBeads: true, wantError: "update sentinel",
			configure: func() {
				updateBeadsFromScan = func(*scanner.ScanResult, scanner.BridgeConfig) (*scanner.BridgeResult, error) {
					return nil, errors.New("update sentinel")
				}
			},
		},
		{
			name: "update reports partial errors", updateBeads: true, wantError: "1 operation(s) failed",
			configure: func() {
				updateBeadsFromScan = func(*scanner.ScanResult, scanner.BridgeConfig) (*scanner.BridgeResult, error) {
					return &scanner.BridgeResult{Errors: 1}, nil
				}
			},
		},
		{
			name: "notify returns error", notify: true, wantError: "notify sentinel",
			configure: func() {
				notifyScanResults = func(context.Context, *scanner.ScanResult, string) error {
					return errors.New("notify sentinel")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createBeadsFromScan = oldCreateBeadsFromScan
			updateBeadsFromScan = oldUpdateBeadsFromScan
			notifyScanResults = oldNotifyScanResults
			tt.configure()

			stdout, runErr := captureStdout(t, func() error {
				return runScan("/project", scanner.ScanOptions{}, tt.createBeads, tt.updateBeads, tt.notify, scanner.BridgeConfig{}, false, false, false)
			})
			if !errors.Is(runErr, errJSONFailure) || !strings.Contains(runErr.Error(), tt.wantError) {
				t.Fatalf("error = %v, want errJSONFailure containing %q", runErr, tt.wantError)
			}
			assertScanJSONResult(t, stdout, false)
		})
	}
}

func assertScanJSONResult(t *testing.T, stdout string, wantSuccess bool) {
	t.Helper()
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("single JSON response decode failed: %v\nstdout=%s", err, stdout)
	}
	if response["success"] != wantSuccess {
		t.Fatalf("success = %v, want %v", response["success"], wantSuccess)
	}
	if !wantSuccess && (response["error"] == nil || response["error"] == "") {
		t.Fatalf("error = %v, want non-empty failure cause", response["error"])
	}
}

func TestOpenAPIGenerateStdoutEmitsSingleOpenAPIDocument(t *testing.T) {
	originalJSONOutput := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = originalJSONOutput })

	cmd := newOpenAPIGenerateCmd()
	cmd.SetArgs([]string{"--stdout"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("openapi generate --stdout: %v", err)
	}

	var spec map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &spec); err != nil {
		t.Fatalf("--stdout must contain exactly one JSON document: %v\noutput:\n%s", err, stdout.String())
	}
	if got := spec["openapi"]; got != "3.1.0" {
		t.Fatalf("OpenAPI version = %#v, want 3.1.0", got)
	}
}

func TestOpenAPIGenerateRejectsPositionalArgumentBeforeWritingOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "openapi.json")
	cmd := newOpenAPIGenerateCmd()
	cmd.SetArgs([]string{"unexpected", "--output", output})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("openapi generate accepted an unexpected positional argument")
	}
	if !strings.Contains(err.Error(), "unknown command") && !strings.Contains(err.Error(), "unknown arg") {
		t.Fatalf("openapi generate error = %v, want Cobra positional-argument validation", err)
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output file stat error = %v, want not exist", statErr)
	}
}

// Field-evidence regressions: unknown flags get a did-you-mean suggestion,
// and cobra usage errors classify as INVALID_FLAG rather than
// INTERNAL_ERROR with misleading "retry" advice.
func TestClassifyRobotExecuteErrorSuggestsNearestFlag(t *testing.T) {
	code, hint := classifyRobotExecuteError(errors.New("unknown flag: --robot-state"))
	if code != robot.ErrCodeInvalidFlag {
		t.Fatalf("code = %q, want INVALID_FLAG", code)
	}
	if !strings.Contains(hint, "--robot-status") {
		t.Errorf("hint = %q, want a --robot-status suggestion", hint)
	}

	// A wildly-off guess gets no suggestion, just the discovery hint.
	_, hint = classifyRobotExecuteError(errors.New("unknown flag: --zzzzqqqq"))
	if strings.Contains(hint, "Did you mean") {
		t.Errorf("hint = %q, want no suggestion for a hopeless guess", hint)
	}
}

// TestClassifyRobotExecuteErrorSuggestsSessionForRobotSession guards the
// --robot-session near-miss: a caller reaching for the shared --session scope
// (e.g. to scope --robot-snapshot) should be pointed at --session, not at the
// nearest robot-* flag.
func TestClassifyRobotExecuteErrorSuggestsSessionForRobotSession(t *testing.T) {
	code, hint := classifyRobotExecuteError(errors.New("unknown flag: --robot-session"))
	if code != robot.ErrCodeInvalidFlag {
		t.Fatalf("code = %q, want INVALID_FLAG", code)
	}
	if !strings.Contains(hint, "--session") {
		t.Errorf("hint = %q, want a --session suggestion", hint)
	}
}

func TestClassifyRobotExecuteErrorUnknownCommandIsInvalidFlag(t *testing.T) {
	code, hint := classifyRobotExecuteError(errors.New(`unknown command "myproject" for "ntm"`))
	if code != robot.ErrCodeInvalidFlag {
		t.Fatalf("code = %q, want INVALID_FLAG (deterministic usage error, not internal)", code)
	}
	if strings.Contains(strings.ToLower(hint), "retry") {
		t.Errorf("hint = %q, must not advise retrying a deterministic usage error", hint)
	}
}
