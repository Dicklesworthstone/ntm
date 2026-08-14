package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type smartRestartPromptDispatcherFunc func(context.Context, dispatchsvc.Request) (dispatchsvc.Result, error)

func (f smartRestartPromptDispatcherFunc) Execute(ctx context.Context, req dispatchsvc.Request) (dispatchsvc.Result, error) {
	return f(ctx, req)
}

type deterministicSmartRestartExecutor struct {
	launches                  []string
	mutations                 []string
	shellPID                  int
	shellPIDErr               error
	ready                     bool
	readyConfigured           bool
	readyErr                  error
	childAlive                bool
	captureOutput             string
	captureErr                error
	captureCancel             context.CancelFunc
	observationContextsActive []bool
}

type cancelAfterSmartRestartLaunchExecutor struct {
	*deterministicSmartRestartExecutor
	cancel context.CancelFunc
}

func (e *cancelAfterSmartRestartLaunchExecutor) sendKeys(ctx context.Context, session string, win, pane int, keys string) error {
	if err := e.deterministicSmartRestartExecutor.sendKeys(ctx, session, win, pane, keys); err != nil {
		return err
	}
	e.cancel()
	return nil
}

func (e *deterministicSmartRestartExecutor) exitAgent(_ context.Context, _ string, _, _ int, _ string, seq *RestartSequence) error {
	e.mutations = append(e.mutations, "exit")
	seq.ExitMethod = "test_exit"
	return nil
}

func (*deterministicSmartRestartExecutor) waitForShellReturn(context.Context, string, int, int, time.Duration) (bool, string, error) {
	return true, "", nil
}

func (e *deterministicSmartRestartExecutor) hardKillAgent(context.Context, string, int, int, *RestartSequence) (*HardKillResult, error) {
	e.mutations = append(e.mutations, "hard-kill")
	return &HardKillResult{Success: true}, nil
}

func (e *deterministicSmartRestartExecutor) sendKeys(_ context.Context, _ string, win, pane int, keys string) error {
	e.mutations = append(e.mutations, "send-keys")
	e.launches = append(e.launches, fmt.Sprintf("%d.%d:%s", win, pane, keys))
	return nil
}

func TestValidateSmartRestartTargetsRejectsWholeGrokBatch(t *testing.T) {
	for _, panes := range []map[string]PaneWorkStatus{
		{"0": {AgentType: "grok"}},
		{
			"0": {AgentType: "cc"},
			"1": {AgentType: "grok-build"},
		},
	} {
		if err := validateSmartRestartTargets(panes); !errors.Is(err, agent.ErrAutomatedRelaunchNotImplemented) {
			t.Fatalf("validateSmartRestartTargets() error = %v, want Grok relaunch sentinel", err)
		}
	}
}

func TestExecuteRestartRejectsGrokBeforeMutation(t *testing.T) {
	executor := &deterministicSmartRestartExecutor{}
	seq, err := executeRestart(t.Context(), "proj", 0, 1, "grok-build", SmartRestartOptions{
		Force:        true,
		HardKill:     true,
		HardKillOnly: true,
		executor:     executor,
	})
	if seq == nil || seq.AgentType != "grok-build" || seq.AgentLaunched {
		t.Fatalf("executeRestart() sequence = %+v, want unlaunched Grok sequence", seq)
	}
	var structured *StructuredError
	if !errors.As(err, &structured) || structured.Code != ErrCodeNotImplemented || structured.Phase != "preflight" {
		t.Fatalf("executeRestart() error = %T %+v, want NOT_IMPLEMENTED preflight error", err, err)
	}
	if len(executor.mutations) != 0 || len(executor.launches) != 0 {
		t.Fatalf("executeRestart() mutated unsupported target: mutations=%v launches=%v", executor.mutations, executor.launches)
	}
}

func TestExecuteRestartReturnsCallerCancellationAfterLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executor := &cancelAfterSmartRestartLaunchExecutor{
		deterministicSmartRestartExecutor: &deterministicSmartRestartExecutor{
			shellPID:        321,
			ready:           true,
			readyConfigured: true,
			childAlive:      true,
		},
		cancel: cancel,
	}
	seq, err := executeRestart(ctx, "proj", 0, 1, "cc", SmartRestartOptions{
		PostWaitTime: time.Minute,
		executor:     executor,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeRestart() error=%v, want context.Canceled", err)
	}
	if seq == nil || !seq.AgentLaunched || seq.AgentLaunchStatus != SmartRestartLaunchReady || !seq.LaunchAttempted ||
		seq.ShellPID != 321 || seq.ProcessAlive == nil || !*seq.ProcessAlive || len(executor.launches) != 1 {
		t.Fatalf("canceled smart restart sequence=%+v launches=%v", seq, executor.launches)
	}
	if len(executor.observationContextsActive) != 2 || !executor.observationContextsActive[0] || !executor.observationContextsActive[1] {
		t.Fatalf("independent observation did not use a live context: %v", executor.observationContextsActive)
	}
}

func TestObserveSmartRestartLaunchAfterCancellationReportsStrongestKnownState(t *testing.T) {
	tests := []struct {
		name             string
		executor         *deterministicSmartRestartExecutor
		wantStatus       SmartRestartLaunchStatus
		wantLaunched     bool
		wantProcessAlive *bool
		wantObservation  bool
	}{
		{
			name: "ready child",
			executor: &deterministicSmartRestartExecutor{
				shellPID:        101,
				ready:           true,
				readyConfigured: true,
				childAlive:      true,
			},
			wantStatus:       SmartRestartLaunchReady,
			wantLaunched:     true,
			wantProcessAlive: pointerToBool(true),
		},
		{
			name: "live child without readiness",
			executor: &deterministicSmartRestartExecutor{
				shellPID:        102,
				readyConfigured: true,
				childAlive:      true,
			},
			wantStatus:       SmartRestartLaunchUnknown,
			wantProcessAlive: pointerToBool(true),
		},
		{
			name: "no live child",
			executor: &deterministicSmartRestartExecutor{
				shellPID:        103,
				readyConfigured: true,
			},
			wantStatus:       SmartRestartLaunchNotReady,
			wantProcessAlive: pointerToBool(false),
		},
		{
			name: "PID observation failed",
			executor: &deterministicSmartRestartExecutor{
				shellPIDErr:     errors.New("PID unavailable"),
				readyConfigured: true,
			},
			wantStatus:      SmartRestartLaunchUnknown,
			wantObservation: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			seq := &RestartSequence{
				AgentType:         "cod",
				AgentLaunchStatus: SmartRestartLaunchUnknown,
				LaunchAttempted:   true,
			}

			observeSmartRestartLaunchAfterCancellation(ctx, "proj", 0, 1, "cod", "proj:0.1", tt.executor, seq)

			if seq.AgentLaunchStatus != tt.wantStatus || seq.AgentLaunched != tt.wantLaunched {
				t.Fatalf("launch sequence=%+v, want status=%s launched=%t", seq, tt.wantStatus, tt.wantLaunched)
			}
			if tt.wantProcessAlive == nil {
				if seq.ProcessAlive != nil {
					t.Fatalf("process_alive=%v, want unobserved", *seq.ProcessAlive)
				}
			} else if seq.ProcessAlive == nil || *seq.ProcessAlive != *tt.wantProcessAlive {
				t.Fatalf("process_alive=%v, want %t", seq.ProcessAlive, *tt.wantProcessAlive)
			}
			if (seq.LaunchObservationError != "") != tt.wantObservation {
				t.Fatalf("launch_observation_error=%q, want present=%t", seq.LaunchObservationError, tt.wantObservation)
			}
			if len(tt.executor.observationContextsActive) != 2 || !tt.executor.observationContextsActive[0] || !tt.executor.observationContextsActive[1] {
				t.Fatalf("observation contexts=%v, want two live independent calls", tt.executor.observationContextsActive)
			}
		})
	}
}

func TestMaterializeSmartRestartCancellationActionsUsesSortedRemainingTargets(t *testing.T) {
	panes := map[string]PaneWorkStatus{
		"2.1": {AgentType: "cc", Recommendation: "SAFE_TO_RESTART"},
		"0.2": {AgentType: "cod", Recommendation: "SAFE_TO_RESTART"},
		"1.0": {AgentType: "gmi", Recommendation: "SAFE_TO_RESTART"},
	}
	keys := sortedSmartRestartPaneKeys(panes)
	wantKeys := []string{"0.2", "1.0", "2.1"}
	if fmt.Sprint(keys) != fmt.Sprint(wantKeys) {
		t.Fatalf("sorted keys=%v, want %v", keys, wantKeys)
	}
	out := &SmartRestartOutput{
		RobotResponse: NewRobotResponse(true),
		Actions: map[string]RestartAction{
			"0.2": {Action: ActionFailed},
		},
		Summary: RestartSummary{
			Failed:        1,
			PanesByAction: map[string][]string{"FAILED": {"0.2"}},
		},
	}

	materializeSmartRestartCancellationActions(out, panes, keys[1:], context.Canceled)
	setSmartRestartCancellation(out, context.Canceled)
	finalizeSmartRestartOutput(out, SmartRestartOptions{Session: "proj"})

	if out.Success || out.ErrorCode != ErrCodeTimeout || out.Summary.Skipped != 2 {
		t.Fatalf("canceled output=%+v", out)
	}
	if fmt.Sprint(out.Summary.PanesByAction["SKIPPED"]) != fmt.Sprint([]string{"1.0", "2.1"}) {
		t.Fatalf("skipped order=%v", out.Summary.PanesByAction["SKIPPED"])
	}
	for _, paneKey := range []string{"1.0", "2.1"} {
		action := out.Actions[paneKey]
		if action.Action != ActionSkipped || action.PreCheck == nil || !strings.Contains(action.Reason, "not attempted") {
			t.Fatalf("pending action %s=%+v", paneKey, action)
		}
	}
}

func pointerToBool(value bool) *bool {
	return &value
}

func (e *deterministicSmartRestartExecutor) getShellPID(ctx context.Context, _ string, _, _ int) (int, error) {
	e.observationContextsActive = append(e.observationContextsActive, ctx != nil && ctx.Err() == nil)
	return e.shellPID, e.shellPIDErr
}

func (e *deterministicSmartRestartExecutor) waitForPaneAgentReady(ctx context.Context, _ string, _ int, _ string, _ time.Duration) (bool, error) {
	e.observationContextsActive = append(e.observationContextsActive, ctx != nil && ctx.Err() == nil)
	if !e.readyConfigured {
		return true, e.readyErr
	}
	return e.ready, e.readyErr
}

func (e *deterministicSmartRestartExecutor) capturePaneOutput(context.Context, string, int) (string, error) {
	if e.captureCancel != nil {
		e.captureCancel()
	}
	return e.captureOutput, e.captureErr
}

func (e *deterministicSmartRestartExecutor) hasChildAlive(shellPID int) bool {
	return shellPID > 0 && e.childAlive
}

// =============================================================================
// Unit Tests for --robot-smart-restart (bd-2c7f4, bd-2eo1l)
// =============================================================================

// TestDecideRestart tests the decision matrix for restart actions.
func TestDecideRestart(t *testing.T) {
	tests := []struct {
		name               string
		status             PaneWorkStatus
		force              bool
		wantRestart        bool
		wantReasonContains string
		wantWarning        bool
	}{
		// Working agent scenarios
		{
			name: "working agent without force - skip",
			status: PaneWorkStatus{
				IsWorking:      true,
				Recommendation: "DO_NOT_INTERRUPT",
			},
			force:              false,
			wantRestart:        false,
			wantReasonContains: "actively working",
		},
		{
			name: "working agent with force - restart with warning",
			status: PaneWorkStatus{
				IsWorking:      true,
				Recommendation: "DO_NOT_INTERRUPT",
			},
			force:              true,
			wantRestart:        true,
			wantReasonContains: "FORCED",
			wantWarning:        true,
		},

		// Idle agent scenarios
		{
			name: "idle agent safe to restart",
			status: PaneWorkStatus{
				IsIdle:         true,
				IsWorking:      false,
				Recommendation: "SAFE_TO_RESTART",
			},
			force:              false,
			wantRestart:        true,
			wantReasonContains: "idle",
		},

		// Context low scenarios
		{
			name: "low context working - skip",
			status: PaneWorkStatus{
				IsWorking:      true,
				IsContextLow:   true,
				Recommendation: "CONTEXT_LOW_CONTINUE",
			},
			force:              false,
			wantRestart:        false,
			wantReasonContains: "working", // IsWorking check comes first
		},
		{
			name: "low context idle - restart",
			status: PaneWorkStatus{
				IsWorking:        false,
				IsIdle:           true,
				IsContextLow:     true,
				ContextRemaining: float64Ptr(12.0),
				Recommendation:   "CONTEXT_LOW_CONTINUE",
			},
			force:              false,
			wantRestart:        true,
			wantReasonContains: "low context",
		},

		// Rate limited scenarios
		{
			name: "rate limited without force - skip",
			status: PaneWorkStatus{
				IsRateLimited:  true,
				Recommendation: "RATE_LIMITED_WAIT",
			},
			force:              false,
			wantRestart:        false,
			wantReasonContains: "Rate limited",
		},
		{
			name: "rate limited with force - restart with warning",
			status: PaneWorkStatus{
				IsRateLimited:  true,
				Recommendation: "RATE_LIMITED_WAIT",
			},
			force:              true,
			wantRestart:        true,
			wantReasonContains: "FORCED",
			wantWarning:        true,
		},

		// Error state scenarios
		{
			name: "error state - restart",
			status: PaneWorkStatus{
				Recommendation: "ERROR_STATE",
			},
			force:              false,
			wantRestart:        true,
			wantReasonContains: "error state",
		},

		// Unknown state scenarios
		{
			name: "unknown state without force - skip",
			status: PaneWorkStatus{
				Recommendation: "UNKNOWN",
			},
			force:              false,
			wantRestart:        false,
			wantReasonContains: "manual inspection",
		},
		{
			name: "unknown state with force - restart with warning",
			status: PaneWorkStatus{
				Recommendation: "UNKNOWN",
			},
			force:              true,
			wantRestart:        true,
			wantReasonContains: "FORCED",
			wantWarning:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldRestart, reason, warning := decideRestart(&tt.status, tt.force)

			if shouldRestart != tt.wantRestart {
				t.Errorf("decideRestart() shouldRestart = %v, want %v", shouldRestart, tt.wantRestart)
			}

			if !smartContains(reason, tt.wantReasonContains) {
				t.Errorf("decideRestart() reason = %q, want to contain %q", reason, tt.wantReasonContains)
			}

			if tt.wantWarning && warning == "" {
				t.Errorf("decideRestart() expected warning, got empty")
			}
			if !tt.wantWarning && warning != "" {
				t.Errorf("decideRestart() expected no warning, got %q", warning)
			}
		})
	}
}

// TestDefaultSmartRestartOptions tests default options.
func TestDefaultSmartRestartOptions(t *testing.T) {
	opts := DefaultSmartRestartOptions()

	if opts.LinesCaptured != 100 {
		t.Errorf("DefaultSmartRestartOptions().LinesCaptured = %d, want 100", opts.LinesCaptured)
	}

	if opts.PostWaitTime != 6000000000 { // 6 seconds in nanoseconds
		t.Errorf("DefaultSmartRestartOptions().PostWaitTime = %v, want 6s", opts.PostWaitTime)
	}

	if opts.Force {
		t.Error("DefaultSmartRestartOptions().Force should be false")
	}

	if opts.DryRun {
		t.Error("DefaultSmartRestartOptions().DryRun should be false")
	}
}

// TestRestartActionTypes tests action type constants.
func TestRestartActionTypes(t *testing.T) {
	tests := []struct {
		action   RestartActionType
		expected string
	}{
		{ActionRestarted, "RESTARTED"},
		{ActionSkipped, "SKIPPED"},
		{ActionWaiting, "WAITING"},
		{ActionFailed, "FAILED"},
		{ActionWouldRestart, "WOULD_RESTART"},
	}

	for _, tt := range tests {
		if string(tt.action) != tt.expected {
			t.Errorf("RestartActionType = %q, want %q", tt.action, tt.expected)
		}
	}
}

// TestBuildWaitInfo tests wait info construction.
func TestBuildWaitInfo(t *testing.T) {
	status := &PaneWorkStatus{
		IsRateLimited: true,
	}

	info := buildWaitInfo(status)

	if info == nil {
		t.Fatal("buildWaitInfo() returned nil")
	}

	if info.Suggestion == "" {
		t.Error("buildWaitInfo() should provide a suggestion")
	}

	if info.WaitSeconds <= 0 {
		t.Errorf("buildWaitInfo() WaitSeconds = %d, want > 0", info.WaitSeconds)
	}
}

// TestAppendPaneToAction tests pane tracking in summary.
func TestAppendPaneToAction(t *testing.T) {
	panesByAction := make(map[string][]string)

	appendPaneToAction(panesByAction, "RESTARTED", "0.2")
	appendPaneToAction(panesByAction, "RESTARTED", "1.3")
	appendPaneToAction(panesByAction, "SKIPPED", "1.4")

	if len(panesByAction["RESTARTED"]) != 2 {
		t.Errorf("RESTARTED panes = %d, want 2", len(panesByAction["RESTARTED"]))
	}

	if len(panesByAction["SKIPPED"]) != 1 {
		t.Errorf("SKIPPED panes = %d, want 1", len(panesByAction["SKIPPED"]))
	}

	// Check pane values
	if panesByAction["RESTARTED"][0] != "0.2" || panesByAction["RESTARTED"][1] != "1.3" {
		t.Errorf("RESTARTED panes = %v, want [0.2, 1.3]", panesByAction["RESTARTED"])
	}
}

// TestLooksLikeShellPrompt tests shell prompt detection.
func TestLooksLikeShellPrompt(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "bash prompt with dollar",
			output: "user@host:~$ ",
			want:   true,
		},
		{
			name:   "zsh prompt with percent",
			output: "user@host % ",
			want:   true,
		},
		{
			name:   "root prompt with hash",
			output: "root@host:~# ",
			want:   true,
		},
		{
			name:   "fish prompt",
			output: "user@host ~/projects ❯ ",
			want:   true,
		},
		{
			name:   "simple arrow prompt",
			output: "→ ",
			want:   true,
		},
		{
			name:   "ends with dollar",
			output: "some text$",
			want:   true,
		},
		{
			name:   "ends with greater than",
			output: "prompt>",
			want:   true,
		},
		{
			name:   "claude code output",
			output: "╭─ Claude Code\n│ Working on task...\n╰─────────────────────",
			want:   false,
		},
		{
			name:   "codex output",
			output: "Codex> Processing your request...",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
		{
			name:   "multiline with prompt at end",
			output: "some output\nmore output\nuser@host:~$ ",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeShellPrompt(tt.output)
			if got != tt.want {
				t.Errorf("looksLikeShellPrompt(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

// TestContainsSuffix tests suffix checking.
func TestContainsSuffix(t *testing.T) {
	tests := []struct {
		s      string
		suffix string
		want   bool
	}{
		{"hello world", "world", true},
		{"hello world", "hello", false},
		{"test", "test", true},
		{"test", "testing", false},
		{"", "", true},
		{"a", "ab", false},
	}

	for _, tt := range tests {
		got := containsSuffix(tt.s, tt.suffix)
		if got != tt.want {
			t.Errorf("containsSuffix(%q, %q) = %v, want %v", tt.s, tt.suffix, got, tt.want)
		}
	}
}

// TestTrimSpace tests whitespace trimming.
func TestTrimSpace(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"  hello  ", "hello"},
		{"\t\ttab\t\t", "tab"},
		{"\n\nnewline\n\n", "newline"},
		{"no whitespace", "no whitespace"},
		{"   ", ""},
		{"", ""},
		{" a ", "a"},
	}

	for _, tt := range tests {
		got := trimSpace(tt.input)
		if got != tt.want {
			t.Errorf("trimSpace(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestIsSpace tests whitespace character detection.
func TestIsSpace(t *testing.T) {
	tests := []struct {
		c    byte
		want bool
	}{
		{' ', true},
		{'\t', true},
		{'\n', true},
		{'\r', true},
		{'a', false},
		{'0', false},
		{'$', false},
	}

	for _, tt := range tests {
		got := isSpace(tt.c)
		if got != tt.want {
			t.Errorf("isSpace(%q) = %v, want %v", tt.c, got, tt.want)
		}
	}
}

// TestFormatReasonWithPercent tests percentage formatting in reasons.
func TestFormatReasonWithPercent(t *testing.T) {
	tests := []struct {
		format string
		pct    float64
		want   string
	}{
		{"Idle with low context (%.0f%%)", 12.0, "Idle with low context (12%)"},
		{"Usage at %.0f%%", 85.5, "Usage at 86%"}, // Rounds up
		{"%.0f%% remaining", 0.0, "0% remaining"},
		{"No format", 50.0, "No format"},
	}

	for _, tt := range tests {
		got := formatReasonWithPercent(tt.format, tt.pct)
		if got != tt.want {
			t.Errorf("formatReasonWithPercent(%q, %.1f) = %q, want %q", tt.format, tt.pct, got, tt.want)
		}
	}
}

// TestSimpleError tests the error helper.
func TestSimpleError(t *testing.T) {
	err := newError("test error")
	if err.Error() != "test error" {
		t.Errorf("newError() = %q, want %q", err.Error(), "test error")
	}

	wrapped := wrapError("prefix", err)
	if wrapped.Error() != "prefix: test error" {
		t.Errorf("wrapError() = %q, want %q", wrapped.Error(), "prefix: test error")
	}
}

// TestPreCheckInfo tests the PreCheckInfo structure.
func TestPreCheckInfo(t *testing.T) {
	pct := 15.0
	info := PreCheckInfo{
		Recommendation:   "SAFE_TO_RESTART",
		IsWorking:        false,
		IsIdle:           true,
		IsRateLimited:    false,
		IsContextLow:     true,
		ContextRemaining: &pct,
		Confidence:       0.95,
		AgentType:        "cc",
	}

	if info.Recommendation != "SAFE_TO_RESTART" {
		t.Error("PreCheckInfo.Recommendation mismatch")
	}
	if info.IsWorking {
		t.Error("PreCheckInfo.IsWorking should be false")
	}
	if !info.IsIdle {
		t.Error("PreCheckInfo.IsIdle should be true")
	}
	if info.IsRateLimited {
		t.Error("PreCheckInfo.IsRateLimited should be false")
	}
	if !info.IsContextLow {
		t.Error("PreCheckInfo.IsContextLow should be true")
	}
	if info.ContextRemaining == nil || *info.ContextRemaining != 15.0 {
		t.Error("PreCheckInfo.ContextRemaining mismatch")
	}
	if info.Confidence != 0.95 {
		t.Error("PreCheckInfo.Confidence mismatch")
	}
	if info.AgentType != "cc" {
		t.Error("PreCheckInfo.AgentType mismatch")
	}
}

// TestRestartSequence tests the RestartSequence structure.
func TestRestartSequence(t *testing.T) {
	seq := RestartSequence{
		ExitMethod:     "double_ctrl_c",
		ExitDurationMs: 3000,
		ShellConfirmed: true,
		AgentLaunched:  true,
		AgentType:      "cc",
		PromptSent:     true,
	}

	if seq.ExitMethod != "double_ctrl_c" {
		t.Error("RestartSequence.ExitMethod mismatch")
	}
	if seq.ExitDurationMs != 3000 {
		t.Error("RestartSequence.ExitDurationMs mismatch")
	}
	if !seq.ShellConfirmed {
		t.Error("RestartSequence.ShellConfirmed should be true")
	}
	if !seq.AgentLaunched {
		t.Error("RestartSequence.AgentLaunched should be true")
	}
	if seq.AgentType != "cc" {
		t.Error("RestartSequence.AgentType mismatch")
	}
	if !seq.PromptSent {
		t.Error("RestartSequence.PromptSent should be true")
	}
}

// TestPostStateInfo tests the PostStateInfo structure.
func TestPostStateInfo(t *testing.T) {
	info := PostStateInfo{
		AgentRunning: true,
		AgentType:    "cod",
		Confidence:   0.87,
	}

	if !info.AgentRunning {
		t.Error("PostStateInfo.AgentRunning should be true")
	}
	if info.AgentType != "cod" {
		t.Error("PostStateInfo.AgentType mismatch")
	}
	if info.Confidence != 0.87 {
		t.Error("PostStateInfo.Confidence mismatch")
	}
}

// TestWaitInfo tests the WaitInfo structure.
func TestWaitInfo(t *testing.T) {
	info := WaitInfo{
		ResetsAt:    "2026-01-20T18:00:00Z",
		WaitSeconds: 3600,
		Suggestion:  "Consider caam account switch",
	}

	if info.ResetsAt != "2026-01-20T18:00:00Z" {
		t.Error("WaitInfo.ResetsAt mismatch")
	}
	if info.WaitSeconds != 3600 {
		t.Error("WaitInfo.WaitSeconds mismatch")
	}
	if info.Suggestion != "Consider caam account switch" {
		t.Error("WaitInfo.Suggestion mismatch")
	}
}

// TestRestartAction tests the RestartAction structure.
func TestRestartAction(t *testing.T) {
	action := RestartAction{
		Action:  ActionRestarted,
		Reason:  "Agent is idle",
		Warning: "",
	}

	if action.Action != ActionRestarted {
		t.Error("RestartAction.Action mismatch")
	}
	if action.Reason != "Agent is idle" {
		t.Error("RestartAction.Reason mismatch")
	}
}

// TestRestartSummary tests the RestartSummary structure.
func TestRestartSummary(t *testing.T) {
	summary := RestartSummary{
		Restarted:     2,
		Skipped:       1,
		Waiting:       1,
		Failed:        0,
		WouldRestart:  0,
		PanesByAction: make(map[string][]string),
	}

	summary.PanesByAction["RESTARTED"] = []string{"0.2", "1.3"}
	summary.PanesByAction["SKIPPED"] = []string{"1.4"}
	summary.PanesByAction["WAITING"] = []string{"2.5"}

	if summary.Restarted != 2 {
		t.Error("RestartSummary.Restarted mismatch")
	}
	if summary.Skipped != 1 {
		t.Error("RestartSummary.Skipped mismatch")
	}
	if summary.Waiting != 1 {
		t.Error("RestartSummary.Waiting mismatch")
	}
	if summary.Failed != 0 {
		t.Error("RestartSummary.Failed mismatch")
	}
}

// TestDecisionMatrix tests the full decision matrix from the spec.
func TestDecisionMatrix(t *testing.T) {
	// Table from spec:
	// | Pre-Check State | Context | Rate Limited | Force | Action |
	// |-----------------|---------|--------------|-------|--------|
	// | Working | Any | No | No | SKIP |
	// | Working | Any | No | Yes | RESTART (with warning) |
	// | Working | Any | Yes | No | SKIP (let finish) |
	// | Idle | >20% | No | No | OPTIONAL (can restart) |
	// | Idle | <20% | No | No | RESTART recommended |
	// | Idle | Any | Yes | No | WAIT for reset |
	// | Error | Any | Any | No | RESTART |
	// | Unknown | Any | Any | No | SKIP + WARN |

	tests := []struct {
		name        string
		status      PaneWorkStatus
		force       bool
		wantRestart bool
	}{
		// Working + No rate limit + No force = SKIP
		{
			name: "working-no-limit-no-force",
			status: PaneWorkStatus{
				IsWorking:      true,
				IsRateLimited:  false,
				Recommendation: "DO_NOT_INTERRUPT",
			},
			force:       false,
			wantRestart: false,
		},
		// Working + No rate limit + Force = RESTART
		{
			name: "working-no-limit-force",
			status: PaneWorkStatus{
				IsWorking:      true,
				IsRateLimited:  false,
				Recommendation: "DO_NOT_INTERRUPT",
			},
			force:       true,
			wantRestart: true,
		},
		// Working + Rate limited + No force = SKIP (let finish)
		{
			name: "working-rate-limited-no-force",
			status: PaneWorkStatus{
				IsWorking:      true,
				IsRateLimited:  true,
				Recommendation: "RATE_LIMITED_WAIT",
			},
			force:       false,
			wantRestart: false,
		},
		// Idle + Context > 20% + No rate limit = RESTART (optional)
		{
			name: "idle-high-context-no-limit",
			status: PaneWorkStatus{
				IsWorking:        false,
				IsIdle:           true,
				IsContextLow:     false,
				ContextRemaining: float64Ptr(50.0),
				IsRateLimited:    false,
				Recommendation:   "SAFE_TO_RESTART",
			},
			force:       false,
			wantRestart: true,
		},
		// Idle + Context < 20% + No rate limit = RESTART
		{
			name: "idle-low-context-no-limit",
			status: PaneWorkStatus{
				IsWorking:        false,
				IsIdle:           true,
				IsContextLow:     true,
				ContextRemaining: float64Ptr(12.0),
				IsRateLimited:    false,
				Recommendation:   "CONTEXT_LOW_CONTINUE",
			},
			force:       false,
			wantRestart: true,
		},
		// Idle + Rate limited = WAIT (handled separately in SmartRestart)
		// Error = RESTART
		{
			name: "error-state",
			status: PaneWorkStatus{
				Recommendation: "ERROR_STATE",
			},
			force:       false,
			wantRestart: true,
		},
		// Unknown = SKIP
		{
			name: "unknown-state",
			status: PaneWorkStatus{
				Recommendation: "UNKNOWN_RECOMMENDATION",
			},
			force:       false,
			wantRestart: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, _ := decideRestart(&tt.status, tt.force)
			if got != tt.wantRestart {
				t.Errorf("decideRestart() = %v, want %v", got, tt.wantRestart)
			}
		})
	}
}

// Helper function for tests
func float64Ptr(v float64) *float64 {
	return &v
}

// smartContains checks if s contains substr (case-insensitive for flexibility).
func smartContains(s, substr string) bool {
	if substr == "" {
		return true
	}
	// Simple case-sensitive contains
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	// Also try lowercase
	sLower := toLower(s)
	subLower := toLower(substr)
	for i := 0; i <= len(sLower)-len(subLower); i++ {
		if sLower[i:i+len(subLower)] == subLower {
			return true
		}
	}
	return false
}

// toLower converts a string to lowercase (simple ASCII-only).
func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}

// =============================================================================
// Hard Kill Tests (bd-bh74z)
// =============================================================================

// TestHardKillResult tests the HardKillResult structure.
func TestHardKillResult(t *testing.T) {
	result := HardKillResult{
		ShellPID:   12345,
		ChildPID:   12346,
		KillMethod: "kill_9",
		Success:    true,
	}

	if result.ShellPID != 12345 {
		t.Errorf("HardKillResult.ShellPID = %d, want 12345", result.ShellPID)
	}
	if result.ChildPID != 12346 {
		t.Errorf("HardKillResult.ChildPID = %d, want 12346", result.ChildPID)
	}
	if result.KillMethod != "kill_9" {
		t.Errorf("HardKillResult.KillMethod = %q, want 'kill_9'", result.KillMethod)
	}
	if !result.Success {
		t.Error("HardKillResult.Success should be true")
	}
}

// TestHardKillResultNoChild tests when no child process is found.
func TestHardKillResultNoChild(t *testing.T) {
	result := HardKillResult{
		ShellPID:   12345,
		ChildPID:   0,
		KillMethod: "no_child_process",
		Success:    true,
	}

	if result.KillMethod != "no_child_process" {
		t.Errorf("HardKillResult.KillMethod = %q, want 'no_child_process'", result.KillMethod)
	}
	if !result.Success {
		t.Error("no_child_process should still be success (agent already exited)")
	}
}

// TestRestartSequenceWithHardKill tests the RestartSequence with hard kill fields.
func TestRestartSequenceWithHardKill(t *testing.T) {
	seq := RestartSequence{
		ExitMethod:     "hard_kill",
		ExitDurationMs: 1000,
		ShellConfirmed: true,
		AgentLaunched:  true,
		AgentType:      "cc",
		HardKillUsed:   true,
		HardKillResult: &HardKillResult{
			ShellPID:   54321,
			ChildPID:   54322,
			KillMethod: "kill_9",
			Success:    true,
		},
	}

	if seq.ExitMethod != "hard_kill" {
		t.Errorf("RestartSequence.ExitMethod = %q, want 'hard_kill'", seq.ExitMethod)
	}
	if !seq.HardKillUsed {
		t.Error("RestartSequence.HardKillUsed should be true")
	}
	if seq.HardKillResult == nil {
		t.Fatal("RestartSequence.HardKillResult should not be nil")
	}
	if seq.HardKillResult.ShellPID != 54321 {
		t.Errorf("HardKillResult.ShellPID = %d, want 54321", seq.HardKillResult.ShellPID)
	}
}

// TestSmartRestartOptionsWithHardKill tests the options struct with hard kill flags.
func TestSmartRestartOptionsWithHardKill(t *testing.T) {
	opts := SmartRestartOptions{
		Session:      "test-session",
		Panes:        []int{2, 3, 4},
		Force:        false,
		DryRun:       false,
		HardKill:     true,
		HardKillOnly: false,
	}

	if !opts.HardKill {
		t.Error("SmartRestartOptions.HardKill should be true")
	}
	if opts.HardKillOnly {
		t.Error("SmartRestartOptions.HardKillOnly should be false")
	}
}

// TestSmartRestartOptionsHardKillOnly tests the hard kill only option.
func TestSmartRestartOptionsHardKillOnly(t *testing.T) {
	opts := SmartRestartOptions{
		Session:      "test-session",
		Panes:        []int{2},
		HardKill:     false, // Doesn't matter when HardKillOnly is true
		HardKillOnly: true,
	}

	if !opts.HardKillOnly {
		t.Error("SmartRestartOptions.HardKillOnly should be true")
	}
}

// TestDefaultOptionsNoHardKill tests that hard kill is disabled by default.
func TestDefaultOptionsNoHardKill(t *testing.T) {
	opts := DefaultSmartRestartOptions()

	if opts.HardKill {
		t.Error("DefaultSmartRestartOptions().HardKill should be false")
	}
	if opts.HardKillOnly {
		t.Error("DefaultSmartRestartOptions().HardKillOnly should be false")
	}
}

// TestSplitBySpace tests the splitBySpace helper.
func TestSplitBySpace(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"one two three", []string{"one", "two", "three"}},
		{"  spaced   out  ", []string{"spaced", "out"}},
		{"single", []string{"single"}},
		{"", nil},
		{"\t\ttabs\t\there\t", []string{"tabs", "here"}},
		{"2 12345", []string{"2", "12345"}}, // Like tmux list-panes output
	}

	for _, tt := range tests {
		got := splitBySpace(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("splitBySpace(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitBySpace(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestRestartCanonicalAgentType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"cc", "cc"},
		{"claude", "cc"},
		{"claude_code", "cc"},
		{"codex-cli", "cod"},
		{"openai-codex", "cod"},
		{"google-gemini", "gmi"},
		{"antigravity", "agy"},
		{"agy", "agy"},
		{"grok-build", "grok"},
		{"opencode", "oc"},
		{"ws", "windsurf"},
		{"ollama", "ollama"},
		{"mystery-agent", "unknown"},
		{"", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := string(restartCanonicalAgentType(tt.input)); got != tt.want {
				t.Errorf("restartCanonicalAgentType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRestartLaunchAlias(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude", "cc"},
		{"claude_code", "cc"},
		{"codex", "cod"},
		{"openai-codex", "cod"},
		{"google-gemini", "gmi"},
		{"antigravity", "agy"},
		{"grok", ""},
		{"xai_grok_build", ""},
		{"opencode", "oc"},
		{"ws", "windsurf"},
		{"aider", "aider"},
		{"ollama", "ollama"},
		{"unknown", "cc"},
		{"mystery-agent", "cc"},
		{"", "cc"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := restartLaunchAlias(tt.input); got != tt.want {
				t.Errorf("restartLaunchAlias(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestConfirmShellReturned verifies the #187 shell-return confirmation logic:
// process death is the primary signal; the prompt-glyph heuristic is only a
// fallback when process state is unknowable, and frames a live agent renders
// (Claude's own "❯" input line) must be rejected there.
func TestConfirmShellReturned(t *testing.T) {
	claudeIdleFrame := strings.Join([]string{
		"● Done. The command ran to completion exactly as expected.",
		"──────────────────────────────",
		"❯",
		"──────────────────────────────",
		"  ? for shortcuts        Claude Sonnet · 4% context",
	}, "\n")
	plainShellPrompt := strings.Join([]string{
		"some earlier output",
		"~/projects/demo ❯",
	}, "\n")

	tests := []struct {
		name        string
		childAlive  bool
		childKnown  bool
		paneContent string
		want        bool
	}{
		{
			name:       "agent child alive means not returned regardless of content",
			childAlive: true, childKnown: true,
			paneContent: plainShellPrompt,
			want:        false,
		},
		{
			name:       "process death is authoritative even with agent-looking content",
			childAlive: false, childKnown: true,
			paneContent: claudeIdleFrame,
			want:        true,
		},
		{
			name:       "fallback rejects live Claude frame despite prompt glyph",
			childAlive: false, childKnown: false,
			paneContent: claudeIdleFrame,
			want:        false,
		},
		{
			name:       "fallback accepts a plain shell prompt",
			childAlive: false, childKnown: false,
			paneContent: plainShellPrompt,
			want:        true,
		},
		{
			name:       "fallback rejects mid-teardown output with no prompt",
			childAlive: false, childKnown: false,
			paneContent: "Shutting down agent...",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confirmShellReturned(tt.childAlive, tt.childKnown, tt.paneContent); got != tt.want {
				t.Errorf("confirmShellReturned(%v, %v, ...) = %v, want %v", tt.childAlive, tt.childKnown, got, tt.want)
			}
		})
	}
}

func TestPaneShowsLiveAgent(t *testing.T) {
	claudeFrame := "✻ Churning… (esc to interrupt)\n❯\n  Claude Sonnet"
	if !paneShowsLiveAgent(claudeFrame) {
		t.Error("paneShowsLiveAgent should classify a Claude frame as a live agent")
	}
	if paneShowsLiveAgent("~/projects/demo ❯") {
		t.Error("paneShowsLiveAgent should not classify a bare shell prompt as an agent")
	}
}

// TestParseNTMPanesCarriesWindowIndex verifies fix #172(b): parseNTMPanes now
// carries the pane's real WindowIndex so emitted W.P addresses round-trip on
// multi-window / window-per-agent layouts instead of hardcoding window 0.
func TestParseNTMPanesCarriesWindowIndex(t *testing.T) {
	panes := []tmux.Pane{
		// Window-per-agent: each agent in its own window, all at pane index 0.
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Type: tmux.AgentType("claude"), Title: "s__cc_1"},
		{ID: "%2", Index: 0, WindowIndex: 1, NTMIndex: 1, Type: tmux.AgentType("codex"), Title: "s__cod_1"},
		{ID: "%3", Index: 0, WindowIndex: 2, NTMIndex: 1, Type: tmux.AgentType("gemini"), Title: "s__gmi_1"},
	}

	out := parseNTMPanes(panes)

	wantWindow := map[string]int{"claude": 0, "codex": 1, "gemini": 2}
	for typ, infos := range out {
		if len(infos) != 1 {
			t.Fatalf("expected one pane for type %q, got %d", typ, len(infos))
		}
		got := infos[0].WindowIndex
		if want, ok := wantWindow[typ]; ok && got != want {
			t.Errorf("type %q: WindowIndex = %d, want %d", typ, got, want)
		}
		// The emitted address must be window.pane, not 0.pane.
		addr := strings.Replace("W.P", "W", string(rune('0'+got)), 1)
		if got != 0 && addr == "0.P" {
			t.Errorf("type %q: address collapsed to window 0", typ)
		}
	}
	if out["codex"][0].WindowIndex != 1 {
		t.Errorf("codex pane should round-trip to window 1, got %d", out["codex"][0].WindowIndex)
	}
}

// TestGetSmartRestartUnknownSessionFailsLoud exercises the real GetSmartRestart
// code path for a session that does not exist: it must propagate
// success:false / SESSION_NOT_FOUND from the pre-check rather than the default
// success:true envelope. This needs no live tmux because SessionExists returns
// false for a random name.
func TestGetSmartRestartUnknownSessionFailsLoud(t *testing.T) {
	out, err := GetSmartRestart(t.Context(), SmartRestartOptions{
		Session:       "ntm-nonexistent-session-for-test-172",
		LinesCaptured: 10,
	})
	if err != nil {
		t.Fatalf("GetSmartRestart returned unexpected error: %v", err)
	}
	if out.Success {
		t.Errorf("expected success=false for nonexistent session, got true")
	}
	if out.ErrorCode != ErrCodeSessionNotFound {
		t.Errorf("expected error_code=%q, got %q", ErrCodeSessionNotFound, out.ErrorCode)
	}
}

// TestSmartRestartTargetingHint verifies the fail-loud remediation hint (#172):
// it must surface the panes that were actually evaluated, point at
// --robot-is-working, and warn about window-local --panes addressing only when a
// --panes filter was supplied.
func TestSmartRestartTargetingHint(t *testing.T) {
	t.Run("with panes filter lists evaluated panes and window warning", func(t *testing.T) {
		opts := SmartRestartOptions{Session: "proj", Panes: []int{2}}
		out := &SmartRestartOutput{
			Actions: map[string]RestartAction{
				"1.0": {Action: ActionFailed},
				"2.0": {Action: ActionFailed},
			},
		}
		hint := smartRestartTargetingHint(opts, out)
		if !strings.Contains(hint, "window-local") {
			t.Errorf("expected window-local warning when --panes set, got %q", hint)
		}
		if !strings.Contains(hint, "1.0") || !strings.Contains(hint, "2.0") {
			t.Errorf("expected evaluated panes 1.0 and 2.0 in hint, got %q", hint)
		}
		if !strings.Contains(hint, "--robot-is-working=proj") {
			t.Errorf("expected --robot-is-working=proj remediation, got %q", hint)
		}
	})

	t.Run("no panes filter omits window warning", func(t *testing.T) {
		opts := SmartRestartOptions{Session: "proj"}
		out := &SmartRestartOutput{Actions: map[string]RestartAction{}}
		hint := smartRestartTargetingHint(opts, out)
		if strings.Contains(hint, "window-local") {
			t.Errorf("did not expect window-local warning without --panes, got %q", hint)
		}
		if !strings.Contains(hint, "No panes were evaluated") {
			t.Errorf("expected 'No panes were evaluated' note, got %q", hint)
		}
	})
}

// TestSmartRestartFailLoudClassification documents the fail-loud decision the
// GetSmartRestart tail applies to the assembled summary (#172). It exercises the
// same branch logic over a synthesized output so we don't need a live tmux.
func TestSmartRestartFailLoudClassification(t *testing.T) {
	classify := func(out *SmartRestartOutput, dryRun bool) bool {
		// Mirror of the fail-loud tail in GetSmartRestart.
		if dryRun {
			return out.Success
		}
		restartable := out.Summary.Restarted + out.Summary.Failed +
			out.Summary.Skipped + out.Summary.Waiting
		if out.Summary.Failed > 0 {
			return false
		}
		if restartable == 0 {
			return false
		}
		return out.Success
	}

	tests := []struct {
		name    string
		summary RestartSummary
		dryRun  bool
		want    bool
	}{
		{"failed action flips to false", RestartSummary{Failed: 1}, false, false},
		{"empty target set flips to false", RestartSummary{}, false, false},
		{"successful restart stays true", RestartSummary{Restarted: 2}, false, true},
		{"skipped-only stays true (target resolved)", RestartSummary{Skipped: 1}, false, true},
		{"waiting-only stays true (target resolved)", RestartSummary{Waiting: 1}, false, true},
		{"dry-run preview stays true", RestartSummary{WouldRestart: 1}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &SmartRestartOutput{
				RobotResponse: NewRobotResponse(true),
				Summary:       tt.summary,
			}
			if got := classify(out, tt.dryRun); got != tt.want {
				t.Errorf("classify(%+v, dryRun=%v) = %v, want %v", tt.summary, tt.dryRun, got, tt.want)
			}
		})
	}
}

func TestDispatchSmartRestartPromptUsesCanonicalService(t *testing.T) {
	var delivered dispatchsvc.Delivery
	service, _, err := newRobotDispatchService(
		redaction.Config{Mode: redaction.ModeRedact},
		dispatchsvc.DelivererFunc(func(_ context.Context, delivery dispatchsvc.Delivery) error {
			delivered = delivery
			return nil
		}),
		nil,
	)
	if err != nil {
		t.Fatalf("newRobotDispatchService: %v", err)
	}

	var request dispatchsvc.Request
	dispatcher := smartRestartPromptDispatcherFunc(func(ctx context.Context, req dispatchsvc.Request) (dispatchsvc.Result, error) {
		request = req
		return service.Execute(ctx, req)
	})
	prompt := "continue the work with password=hunter2hunter2"
	outcome, err := dispatchSmartRestartPrompt(context.Background(), "proj", 7, 3, "codex", true, SmartRestartOptions{
		Prompt:         prompt,
		promptDispatch: dispatcher,
	})
	if err != nil {
		t.Fatalf("dispatchSmartRestartPrompt: %v", err)
	}
	if outcome.Status != PromptDeliveryDelivered || outcome.Delivered != 1 || outcome.ReceiptStatus != string(dispatchsvc.ReceiptDelivered) {
		t.Fatalf("prompt outcome = %+v, want one delivered receipt", outcome)
	}
	if len(request.Panes) != 1 || len(request.Selectors) != 1 || request.Selectors[0] != "7.3" || !request.RequireSingleSelector {
		t.Fatalf("dispatch request did not enforce exact 7.3 target: %+v", request)
	}
	if request.Session != "proj" || !request.IncludeUser || !request.Submit || !request.StopOnFailure {
		t.Fatalf("dispatch request safety fields = %+v", request)
	}
	if got := delivered.Target.Ref.Physical(); got != "7.3" {
		t.Fatalf("delivery physical target = %q, want 7.3", got)
	}
	if delivered.Session != "proj" || delivered.Target.AgentType != tmux.AgentCodex {
		t.Fatalf("delivery target = %+v, session = %q", delivered.Target, delivered.Session)
	}
	if delivered.Protocol != dispatchsvc.ProtocolDoubleEnter || delivered.EnterDelay != tmux.DoubleEnterFirstDelay || delivered.SecondEnterDelay != tmux.DoubleEnterSecondDelay {
		t.Fatalf("delivery protocol = %+v", delivered)
	}
	if delivered.Message == prompt || strings.Contains(delivered.Message, "hunter2hunter2") {
		t.Fatalf("final message was not redacted: %q", delivered.Message)
	}
}

func TestDispatchSmartRestartPromptRejectsRedactionPreflightFailure(t *testing.T) {
	deliveries := 0
	service, err := dispatchsvc.NewService(dispatchsvc.Ports{
		Redactor: dispatchsvc.FinalMessageRedactorFunc(func(context.Context, dispatchsvc.Target, string) (dispatchsvc.RedactionResult, error) {
			return dispatchsvc.RedactionResult{}, errors.New("policy unavailable")
		}),
		Deliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			deliveries++
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("dispatchsvc.NewService: %v", err)
	}

	outcome, err := dispatchSmartRestartPrompt(context.Background(), "proj", 2, 4, "cc", true, SmartRestartOptions{
		Prompt:         "continue",
		promptDispatch: service,
	})
	if err == nil || !strings.Contains(err.Error(), "redaction_failed") || !strings.Contains(err.Error(), "policy unavailable") {
		t.Fatalf("redaction preflight error = %v", err)
	}
	if outcome.Status != PromptDeliveryFailed || outcome.DispatchCode != string(dispatchsvc.ErrRedaction) {
		t.Fatalf("redaction preflight outcome = %+v, want failed/%s", outcome, dispatchsvc.ErrRedaction)
	}
	if deliveries != 0 {
		t.Fatalf("redaction preflight failure performed %d deliveries, want 0", deliveries)
	}
}

func TestDispatchSmartRestartPromptClassifiesBlockedPreflight(t *testing.T) {
	deliveries := 0
	service, err := dispatchsvc.NewService(dispatchsvc.Ports{
		Redactor: dispatchsvc.FinalMessageRedactorFunc(func(context.Context, dispatchsvc.Target, string) (dispatchsvc.RedactionResult, error) {
			return dispatchsvc.RedactionResult{Mode: "block", Blocked: true}, nil
		}),
		Deliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			deliveries++
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("dispatchsvc.NewService: %v", err)
	}

	outcome, err := dispatchSmartRestartPrompt(context.Background(), "proj", 2, 4, "cc", true, SmartRestartOptions{
		Prompt:         "continue",
		promptDispatch: service,
	})
	if err == nil || !strings.Contains(err.Error(), "redaction_blocked") {
		t.Fatalf("blocked preflight error = %v", err)
	}
	if outcome.Status != PromptDeliveryBlocked || outcome.Blocked != 1 || outcome.DispatchCode != string(dispatchsvc.ErrRedactionBlocked) {
		t.Fatalf("blocked preflight outcome = %+v", outcome)
	}
	if deliveries != 0 {
		t.Fatalf("blocked preflight performed %d deliveries, want 0", deliveries)
	}
}

func TestDispatchSmartRestartPromptRejectsAmbiguousOutcome(t *testing.T) {
	expectedTarget := dispatchsvc.Target{Ref: tmux.PaneRef{WindowIndex: 2, PaneIndex: 4}}
	deliveredReceipt := dispatchsvc.Receipt{Target: expectedTarget, Status: dispatchsvc.ReceiptDelivered}

	tests := []struct {
		name        string
		result      dispatchsvc.Result
		dispatchErr error
		want        string
		wantStatus  PromptDeliveryStatus
	}{
		{
			name: "transport error after delivered receipt",
			result: dispatchsvc.Result{
				Success:   true,
				Delivered: 1,
				Receipts:  []dispatchsvc.Receipt{deliveredReceipt},
			},
			dispatchErr: errors.New("transport acknowledgement lost"),
			want:        "outcome is ambiguous",
			wantStatus:  PromptDeliveryAmbiguous,
		},
		{
			name: "successful aggregate without receipt",
			result: dispatchsvc.Result{
				Success:   true,
				Delivered: 1,
				Targets:   []dispatchsvc.Target{expectedTarget},
				Receipts:  []dispatchsvc.Receipt{},
			},
			want:       "got 0 receipts",
			wantStatus: PromptDeliveryAmbiguous,
		},
		{
			name: "receipt for a different pane",
			result: dispatchsvc.Result{
				Success:   true,
				Delivered: 1,
				Targets:   []dispatchsvc.Target{expectedTarget},
				Receipts: []dispatchsvc.Receipt{{
					Target: dispatchsvc.Target{Ref: tmux.PaneRef{WindowIndex: 9, PaneIndex: 9}},
					Status: dispatchsvc.ReceiptDelivered,
				}},
			},
			want:       "targeted pane 9.9, want 2.4",
			wantStatus: PromptDeliveryAmbiguous,
		},
		{
			name: "failed terminal receipt",
			result: dispatchsvc.Result{
				Failed:  1,
				Targets: []dispatchsvc.Target{expectedTarget},
				Receipts: []dispatchsvc.Receipt{{
					Target: expectedTarget,
					Status: dispatchsvc.ReceiptFailed,
				}},
			},
			want:       "was not confirmed",
			wantStatus: PromptDeliveryFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher := smartRestartPromptDispatcherFunc(func(context.Context, dispatchsvc.Request) (dispatchsvc.Result, error) {
				return tt.result, tt.dispatchErr
			})
			outcome, err := dispatchSmartRestartPrompt(context.Background(), "proj", 2, 4, "cc", true, SmartRestartOptions{
				Prompt:         "continue",
				promptDispatch: dispatcher,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("dispatch error = %v, want substring %q", err, tt.want)
			}
			if outcome.Status != tt.wantStatus {
				t.Fatalf("dispatch outcome = %+v, want status %s", outcome, tt.wantStatus)
			}
		})
	}
}

func TestDispatchSmartRestartPromptDoesNotActuateBeforeReadyOrDuringDryRun(t *testing.T) {
	dispatches := 0
	dispatcher := smartRestartPromptDispatcherFunc(func(context.Context, dispatchsvc.Request) (dispatchsvc.Result, error) {
		dispatches++
		return dispatchsvc.Result{}, errors.New("unexpected dispatch")
	})

	tests := []struct {
		name   string
		ready  bool
		dryRun bool
	}{
		{name: "failed ready gate", ready: false},
		{name: "dry run", ready: true, dryRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, err := dispatchSmartRestartPrompt(context.Background(), "proj", 1, 6, "cc", tt.ready, SmartRestartOptions{
				DryRun:         tt.dryRun,
				Prompt:         "continue",
				promptDispatch: dispatcher,
			})
			if err != nil {
				t.Fatalf("dispatchSmartRestartPrompt: %v", err)
			}
			if outcome.Status != PromptDeliveryNotAttempted {
				t.Fatalf("safety gate outcome = %+v, want not_attempted", outcome)
			}
		})
	}
	if dispatches != 0 {
		t.Fatalf("safety gates performed %d dispatches, want 0", dispatches)
	}
}

func TestExecuteRestartReportsRequestedPromptOutcome(t *testing.T) {
	expectedTarget := dispatchsvc.Target{Ref: tmux.PaneRef{WindowIndex: 2, PaneIndex: 4}}
	expectedReceipt := dispatchsvc.Receipt{Target: expectedTarget, Status: dispatchsvc.ReceiptDelivered}

	tests := []struct {
		name        string
		result      dispatchsvc.Result
		dispatchErr error
		wantStatus  PromptDeliveryStatus
		wantSent    bool
		wantErr     bool
	}{
		{
			name: "blocked preflight",
			result: dispatchsvc.Result{
				Targets:  []dispatchsvc.Target{expectedTarget},
				Receipts: []dispatchsvc.Receipt{{Target: expectedTarget, Status: dispatchsvc.ReceiptBlocked}},
				Blocked:  1,
			},
			dispatchErr: &dispatchsvc.Error{Code: dispatchsvc.ErrRedactionBlocked, Err: errors.New("blocked by policy")},
			wantStatus:  PromptDeliveryBlocked,
			wantErr:     true,
		},
		{
			name:        "transport failure before delivery",
			result:      dispatchsvc.Result{Targets: []dispatchsvc.Target{expectedTarget}, Receipts: []dispatchsvc.Receipt{}},
			dispatchErr: errors.New("transport unavailable"),
			wantStatus:  PromptDeliveryFailed,
			wantErr:     true,
		},
		{
			name: "wrong receipt",
			result: dispatchsvc.Result{
				Success:   true,
				Delivered: 1,
				Targets:   []dispatchsvc.Target{expectedTarget},
				Receipts: []dispatchsvc.Receipt{{
					Target: dispatchsvc.Target{Ref: tmux.PaneRef{WindowIndex: 9, PaneIndex: 9}},
					Status: dispatchsvc.ReceiptDelivered,
				}},
			},
			wantStatus: PromptDeliveryAmbiguous,
			wantErr:    true,
		},
		{
			name: "delivered receipt plus transport error",
			result: dispatchsvc.Result{
				Success:   true,
				Delivered: 1,
				Targets:   []dispatchsvc.Target{expectedTarget},
				Receipts:  []dispatchsvc.Receipt{expectedReceipt},
			},
			dispatchErr: errors.New("acknowledgement lost"),
			wantStatus:  PromptDeliveryAmbiguous,
			wantErr:     true,
		},
		{
			name: "confirmed delivery",
			result: dispatchsvc.Result{
				Success:   true,
				Delivered: 1,
				Targets:   []dispatchsvc.Target{expectedTarget},
				Receipts:  []dispatchsvc.Receipt{expectedReceipt},
			},
			wantStatus: PromptDeliveryDelivered,
			wantSent:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &deterministicSmartRestartExecutor{}
			type contextKey struct{}
			callerCtx := context.WithValue(t.Context(), contextKey{}, tt.name)
			dispatcher := smartRestartPromptDispatcherFunc(func(gotCtx context.Context, _ dispatchsvc.Request) (dispatchsvc.Result, error) {
				if gotCtx != callerCtx {
					t.Fatalf("prompt dispatch context=%v, want caller context", gotCtx)
				}
				return tt.result, tt.dispatchErr
			})
			seq, err := executeRestart(callerCtx, "proj", 2, 4, "cc", SmartRestartOptions{
				Prompt:         "continue",
				PostWaitTime:   time.Millisecond,
				promptDispatch: dispatcher,
				executor:       executor,
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("executeRestart error = %v, wantErr=%t", err, tt.wantErr)
			}
			if seq == nil || !seq.AgentLaunched {
				t.Fatalf("restart sequence = %+v, want launched agent", seq)
			}
			if seq.PromptOutcome == nil || seq.PromptOutcome.Status != tt.wantStatus {
				t.Fatalf("prompt outcome = %+v, want status %s", seq.PromptOutcome, tt.wantStatus)
			}
			if seq.PromptSent != tt.wantSent {
				t.Fatalf("prompt_sent = %t, want %t", seq.PromptSent, tt.wantSent)
			}
			if len(executor.launches) != 1 || executor.launches[0] != "2.4:cc\n" {
				t.Fatalf("launches = %q, want exact pane 2.4 launch", executor.launches)
			}
			if tt.wantErr {
				var structured *StructuredError
				if !errors.As(err, &structured) || structured.Code != ErrCodePromptSendFailed || structured.Phase != "prompt" {
					t.Fatalf("executeRestart error = %T %+v, want prompt structured error", err, err)
				}
			}
		})
	}
}

func TestSmartRestartPromptFailureIsTopLevelFailureAndExitOne(t *testing.T) {
	structured := NewStructuredError(ErrCodePromptSendFailed, "requested prompt was not confirmed").WithPhase("prompt").WithPane(4)
	out := &SmartRestartOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "proj",
		Actions: map[string]RestartAction{
			"2.4": {
				Action:      ActionRestarted,
				PromptError: structured,
				RestartSequence: &RestartSequence{
					AgentLaunched: true,
					PromptOutcome: &PromptDeliveryOutcome{Requested: true, Target: "2.4", Status: PromptDeliveryAmbiguous},
				},
			},
		},
		Summary: RestartSummary{
			Restarted:              1,
			PromptFailed:           1,
			PanesWithPromptFailure: []string{"2.4"},
			PanesByAction:          map[string][]string{"RESTARTED": {"2.4"}},
		},
	}
	finalizeSmartRestartOutput(out, SmartRestartOptions{Session: "proj", Prompt: "continue"})
	if out.Success || out.ErrorCode != ErrCodePromptSendFailed {
		t.Fatalf("top-level response = %+v, want PROMPT_SEND_FAILED", out.RobotResponse)
	}
	if out.Actions["2.4"].Action != ActionRestarted || !out.Actions["2.4"].RestartSequence.AgentLaunched {
		t.Fatalf("restart status was hidden by prompt failure: %+v", out.Actions["2.4"])
	}

	stdout, err := captureStdout(t, func() error {
		return encodeTerminalRobotOutput(out, out.RobotResponse, "robot smart-restart failed")
	})
	var exitErr *ProcessExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !exitErr.JSONWritten() {
		t.Fatalf("terminal result = %T %v, want written exit-1 ProcessExitError", err, err)
	}
	var decoded SmartRestartOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("terminal output is not JSON: %v\n%s", err, stdout)
	}
	if decoded.Success || decoded.ErrorCode != ErrCodePromptSendFailed || decoded.Summary.PromptFailed != 1 {
		t.Fatalf("terminal output = %+v", decoded)
	}
	decodedAction := decoded.Actions["2.4"]
	if decodedAction.Action != ActionRestarted || decodedAction.PromptError == nil || decodedAction.PromptError.Code != ErrCodePromptSendFailed {
		t.Fatalf("terminal action lost restart/prompt distinction: %+v", decodedAction)
	}
	if decodedAction.RestartSequence == nil || decodedAction.RestartSequence.PromptOutcome == nil || decodedAction.RestartSequence.PromptOutcome.Status != PromptDeliveryAmbiguous {
		t.Fatalf("terminal action lost structured prompt outcome: %+v", decodedAction)
	}
}

func TestApplyRestartExecutionOutcomePreservesRestartStatusOnPromptFailure(t *testing.T) {
	out := &SmartRestartOutput{
		RobotResponse: NewRobotResponse(true),
		Actions:       map[string]RestartAction{},
		Summary: RestartSummary{
			PanesByAction: map[string][]string{},
		},
	}
	action := RestartAction{}
	seq := &RestartSequence{
		AgentLaunched: true,
		PromptOutcome: &PromptDeliveryOutcome{
			Requested: true,
			Target:    "2.4",
			Status:    PromptDeliveryBlocked,
			Blocked:   1,
		},
	}
	promptErr := NewStructuredError(ErrCodePromptSendFailed, "blocked by redaction policy").WithPhase("prompt").WithPane(4)
	verified := &PostStateInfo{AgentRunning: true, AgentType: "cc", Confidence: 1}

	applyRestartExecutionOutcome(out, &action, "2.4", "idle", seq, promptErr, func() (*PostStateInfo, error) {
		return verified, nil
	})

	if action.Action != ActionRestarted || action.RestartSequence != seq || action.PostState != verified {
		t.Fatalf("action = %+v, want honest restarted state", action)
	}
	if action.PromptError != promptErr || action.StructuredError != nil || action.Error != "" {
		t.Fatalf("prompt failure was not isolated from restart error fields: %+v", action)
	}
	if out.Summary.Restarted != 1 || out.Summary.Failed != 0 || out.Summary.PromptFailed != 1 {
		t.Fatalf("summary = %+v, want restarted=1 failed=0 prompt_failed=1", out.Summary)
	}
	if got := out.Summary.PanesWithPromptFailure; len(got) != 1 || got[0] != "2.4" {
		t.Fatalf("panes_with_prompt_failure = %v, want [2.4]", got)
	}
}

func TestApplyRestartExecutionOutcomeCountsConfirmedPrompt(t *testing.T) {
	out := &SmartRestartOutput{Summary: RestartSummary{PanesByAction: map[string][]string{}}}
	action := RestartAction{}
	seq := &RestartSequence{
		AgentLaunched: true,
		PromptSent:    true,
		PromptOutcome: &PromptDeliveryOutcome{Requested: true, Target: "2.4", Status: PromptDeliveryDelivered, Delivered: 1},
	}

	applyRestartExecutionOutcome(out, &action, "2.4", "idle", seq, nil, nil)

	if action.Action != ActionRestarted || action.RestartSequence != seq {
		t.Fatalf("action = %+v, want restarted delivery", action)
	}
	if out.Summary.Restarted != 1 || out.Summary.PromptDelivered != 1 || out.Summary.PromptFailed != 0 {
		t.Fatalf("summary = %+v, want one confirmed prompt", out.Summary)
	}
}

func TestApplyRestartExecutionOutcomePreservesPartialSequenceOnFailure(t *testing.T) {
	out := &SmartRestartOutput{Summary: RestartSummary{PanesByAction: map[string][]string{}}}
	action := RestartAction{}
	processAlive := true
	seq := &RestartSequence{
		ExitMethod:        "exit_command",
		ShellConfirmed:    true,
		AgentLaunched:     true,
		AgentLaunchStatus: SmartRestartLaunchReady,
		LaunchAttempted:   true,
		ProcessAlive:      &processAlive,
		ShellPID:          123,
		AgentType:         "cod",
	}

	applyErr := applyRestartExecutionOutcome(out, &action, "0", "forced", seq, context.Canceled, nil)

	if applyErr != nil || action.Action != ActionFailed || action.RestartSequence != seq {
		t.Fatalf("failed action lost partial sequence: %+v", action)
	}
	if action.Error == "" || out.Summary.Failed != 1 || fmt.Sprint(out.Summary.PanesByAction["FAILED"]) != "[0]" {
		t.Fatalf("failed action summary=%+v action=%+v", out.Summary, action)
	}
}

func TestApplyRestartExecutionOutcomePropagatesVerificationCancellation(t *testing.T) {
	out := &SmartRestartOutput{Summary: RestartSummary{PanesByAction: map[string][]string{}}}
	action := RestartAction{}
	seq := &RestartSequence{
		AgentLaunched:     true,
		AgentLaunchStatus: SmartRestartLaunchReady,
		LaunchAttempted:   true,
	}

	err := applyRestartExecutionOutcome(out, &action, "0", "forced", seq, nil, func() (*PostStateInfo, error) {
		return nil, context.Canceled
	})

	if !errors.Is(err, context.Canceled) || action.Action != ActionRestarted || action.RestartSequence != seq ||
		!strings.Contains(action.Error, "verification canceled") || out.Summary.Restarted != 1 {
		t.Fatalf("verification cancellation result err=%v action=%+v summary=%+v", err, action, out.Summary)
	}
}

func TestVerifyRestartPropagatesCancellationAfterCapture(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executor := &deterministicSmartRestartExecutor{
		captureOutput: "Codex>\n100% context left\n",
		captureCancel: cancel,
	}

	postState, err := verifyRestart(ctx, "proj", 0, 1, SmartRestartOptions{executor: executor})

	if postState != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("verifyRestart() state=%+v err=%v, want propagated context cancellation", postState, err)
	}
}

// =============================================================================
// bd-moje8: honest failure verdicts
// =============================================================================

// A restart-sequence failure must be blamed on what actually failed, not on
// the pre-check's idle classification. Field bug: FAILED with reason "Agent is
// idle" and top-level INTERNAL_ERROR while restart_sequence showed
// shell_confirmed:false.
func TestApplyRestartExecutionOutcomeShellUnconfirmedReasonNotIdle(t *testing.T) {
	output := &SmartRestartOutput{
		RobotResponse: NewRobotResponse(true),
		Actions:       make(map[string]RestartAction),
		Summary:       RestartSummary{PanesByAction: make(map[string][]string)},
	}
	seq := &RestartSequence{
		AgentType:      "claude",
		ExitMethod:     "ctrl_c",
		ShellConfirmed: false,
	}
	structErr := newRestartError(
		ErrCodeShellNotReturned,
		"Shell did not return within 12s after exit - agent may still be running",
		"post_exit",
		1,
		"claude",
		[]string{"exit-agent-claude", "wait-shell-return"},
		"",
	)
	action := RestartAction{PreCheck: &PreCheckInfo{Recommendation: "SAFE_TO_RESTART", IsIdle: true}}

	if verifyErr := applyRestartExecutionOutcome(output, &action, "1", "Agent is idle", seq, structErr, nil); verifyErr != nil {
		t.Fatalf("applyRestartExecutionOutcome() verify error = %v", verifyErr)
	}
	output.Actions["1"] = action

	if action.Action != ActionFailed {
		t.Fatalf("action = %q, want FAILED", action.Action)
	}
	if strings.Contains(action.Reason, "Agent is idle") {
		t.Fatalf("failure reason %q still blames the agent's idleness", action.Reason)
	}
	if !strings.Contains(action.Reason, "restart exit sequence unconfirmed") ||
		!strings.Contains(action.Reason, "did not return to shell") ||
		!strings.Contains(action.Reason, "exit_method=ctrl_c") {
		t.Fatalf("failure reason = %q, want the exit-sequence explanation with the exit method", action.Reason)
	}
	if action.StructuredError == nil || action.StructuredError.Code != ErrCodeShellNotReturned {
		t.Fatalf("structured error = %+v, want SHELL_NOT_RETURNED", action.StructuredError)
	}

	finalizeSmartRestartOutput(output, SmartRestartOptions{Session: "demo"})
	if output.Success {
		t.Fatal("output.Success = true, want false after a failed restart")
	}
	if output.ErrorCode != ErrCodeShellNotReturned {
		t.Fatalf("output.ErrorCode = %q, want %q (not INTERNAL_ERROR)", output.ErrorCode, ErrCodeShellNotReturned)
	}
}

// Failures without a structured code keep the INTERNAL_ERROR fallback, and
// mixed codes must not pretend one cause explains every pane.
func TestSmartRestartFailureCodeFallsBackOnMixedOrMissingCodes(t *testing.T) {
	build := func(codes ...string) *SmartRestartOutput {
		output := &SmartRestartOutput{Actions: make(map[string]RestartAction)}
		for i, code := range codes {
			action := RestartAction{Action: ActionFailed}
			if code != "" {
				action.StructuredError = NewStructuredError(code, "boom")
			}
			output.Actions[fmt.Sprintf("%d", i)] = action
		}
		return output
	}
	if got := smartRestartFailureCode(build(ErrCodeShellNotReturned, ErrCodeShellNotReturned)); got != ErrCodeShellNotReturned {
		t.Fatalf("uniform code = %q, want SHELL_NOT_RETURNED", got)
	}
	if got := smartRestartFailureCode(build(ErrCodeShellNotReturned, ErrCodeCCLaunchFailed)); got != ErrCodeInternalError {
		t.Fatalf("mixed codes = %q, want INTERNAL_ERROR", got)
	}
	if got := smartRestartFailureCode(build("")); got != ErrCodeInternalError {
		t.Fatalf("missing code = %q, want INTERNAL_ERROR", got)
	}
}

// An unknown-state skip must say WHICH probe was inconclusive and how to
// escalate, instead of a bare "Unknown state".
func TestDecideRestartUnknownStateIncludesProbeDetailAndEscalation(t *testing.T) {
	status := PaneWorkStatus{
		Recommendation:       "SOMETHING_NEW",
		ObservationState:     "degraded",
		ObservationFreshness: "stale",
		Confidence:           0.3,
	}
	shouldRestart, reason, warning := decideRestart(&status, false)
	if shouldRestart || warning != "" {
		t.Fatalf("decideRestart() = (%v, %q, %q), want a skip without warning", shouldRestart, reason, warning)
	}
	for _, want := range []string{
		"Unknown state",
		`recommendation="SOMETHING_NEW"`,
		`observation_state="degraded"`,
		`freshness="stale"`,
		"confidence=0.30",
		"--force",
		"--robot-restart-pane",
	} {
		if !strings.Contains(reason, want) {
			t.Fatalf("unknown-state reason = %q, want it to contain %q", reason, want)
		}
	}
}

// A declined pane with a requested prompt must state explicitly that the
// prompt was NOT delivered, so orchestrators cannot misread the decline as a
// completed rotation.
func TestSmartRestartDeclineMarksPromptNotDelivered(t *testing.T) {
	action := RestartAction{Action: ActionSkipped, Reason: "Agent is actively working"}
	markSmartRestartPromptNotAttempted(&action, SmartRestartOptions{Prompt: "start fresh on bd-x"})
	if action.PromptOutcome == nil ||
		!action.PromptOutcome.Requested ||
		action.PromptOutcome.Status != PromptDeliveryNotAttempted {
		t.Fatalf("prompt outcome = %+v, want requested + not_attempted", action.PromptOutcome)
	}
	if !strings.Contains(action.Reason, "NOT delivered") {
		t.Fatalf("decline reason = %q, want an explicit not-delivered statement", action.Reason)
	}

	// Without a requested prompt the decline stays untouched.
	plain := RestartAction{Action: ActionSkipped, Reason: "Agent is actively working"}
	markSmartRestartPromptNotAttempted(&plain, SmartRestartOptions{})
	if plain.PromptOutcome != nil || plain.Reason != "Agent is actively working" {
		t.Fatalf("prompt-less decline was mutated: %+v", plain)
	}
}

// A successful run that skipped panes in an unknown state must carry the
// documented escalation hint.
func TestFinalizeSmartRestartAddsUnknownStateEscalationHint(t *testing.T) {
	output := &SmartRestartOutput{
		RobotResponse: NewRobotResponse(true),
		Actions: map[string]RestartAction{
			"1": {Action: ActionSkipped, Reason: "Unknown state - manual inspection needed (probe inconclusive: ...)"},
		},
		Summary: RestartSummary{Skipped: 1, PanesByAction: map[string][]string{"SKIPPED": {"1"}}},
	}
	finalizeSmartRestartOutput(output, SmartRestartOptions{Session: "demo"})
	if !output.Success {
		t.Fatalf("output = %+v, want success for a pure skip run", output.RobotResponse)
	}
	if !strings.Contains(output.Hint, "--force") || !strings.Contains(output.Hint, "--robot-restart-pane") {
		t.Fatalf("hint = %q, want the unknown-state escalation hint", output.Hint)
	}
}
