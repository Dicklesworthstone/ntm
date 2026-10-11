package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	sessionPkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

type recordingDistributeAtomicExecutor struct {
	calls   int
	request assignment.AtomicRequest
	result  assignment.AtomicResult
	err     error
}

func (e *recordingDistributeAtomicExecutor) Execute(_ context.Context, request assignment.AtomicRequest) (assignment.AtomicResult, error) {
	e.calls++
	e.request = request
	return e.result, e.err
}

func TestExecuteDistributeDispatchRejectsCanceledContextBeforeTopologyOrSend(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := executeDistributeDispatch(ctx, t.TempDir(), "unused", robot.DistributeRecommendation{BeadID: "bd-cancel", PaneID: "%1"}, "must not send")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeDistributeDispatch error = %v, want context.Canceled", err)
	}
}

func TestExecuteDistributeDispatchBuildsOneReplayStableAtomicRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldCfg := cfg
	oldGetPanes := getDistributePanes
	oldLoadStore := loadDistributeStore
	oldNewExecutor := newDistributeAtomicExecutor
	cfg = config.Default()
	t.Cleanup(func() {
		cfg = oldCfg
		getDistributePanes = oldGetPanes
		loadDistributeStore = oldLoadStore
		newDistributeAtomicExecutor = oldNewExecutor
	})

	panes := []tmux.Pane{
		{ID: "%41", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
		{ID: "%91", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude},
	}
	getDistributePanes = func(context.Context, string) ([]tmux.Pane, error) {
		return append([]tmux.Pane(nil), panes...), nil
	}
	store := assignment.NewStore("proj")
	store.ClearedGenerations["ntm-work"] = 3
	loadDistributeStore = func(session string) (*assignment.AssignmentStore, error) {
		if session != "proj" {
			t.Fatalf("load store session = %q, want proj", session)
		}
		return store, nil
	}
	recorder := &recordingDistributeAtomicExecutor{}
	newDistributeAtomicExecutor = func(gotStore *assignment.AssignmentStore, projectDir string) distributeAtomicExecutor {
		if gotStore != store || projectDir != "/workspace/project" {
			t.Fatalf("atomic factory store=%p dir=%q, want %p and target project", gotStore, projectDir, store)
		}
		return recorder
	}
	prompt := "Please work on ntm-work"
	wantKey := distributeAssignmentIdempotencyKey("proj", "ntm-work", "%91", prompt, 3)
	recorder.result = assignment.AtomicResult{
		Assignment: &assignment.Assignment{BeadID: "ntm-work", ClaimActor: "proj-agent/ntm-key", DispatchReceiptID: assignment.DispatchDeliveryID("%91", "double_enter", wantKey)},
		Dispatch:   assignment.DispatchReceipt{DeliveryID: assignment.DispatchDeliveryID("%91", "double_enter", wantKey)},
		Sent:       true,
	}

	result, err := executeDistributeDispatch(t.Context(), "/workspace/project", "proj", robot.DistributeRecommendation{
		BeadID: "ntm-work", Title: "Live title", PaneID: "%91", PaneTarget: "1.1", AgentType: "claude",
	}, prompt)
	if err != nil {
		t.Fatalf("executeDistributeDispatch: %v", err)
	}
	if recorder.calls != 1 {
		t.Fatalf("atomic Execute calls = %d, want 1", recorder.calls)
	}
	request := recorder.request
	if request.BeadID != "ntm-work" || request.BeadTitle != "Live title" || request.Target != "%91" || request.OccupancyKey != "%91" ||
		request.Pane != 1 || request.AgentType != "cc" || request.AgentName != "proj_cc_1_1" || request.Prompt != prompt || request.IdempotencyKey != wantKey {
		t.Fatalf("atomic request = %+v", request)
	}
	if request.Actor != "proj-distribute-proj_cc_1_1" {
		t.Fatalf("atomic actor = %q", request.Actor)
	}
	if result.Target.ID != "%91" || result.IdempotencyKey != wantKey || result.Protocol != dispatchsvc.ProtocolDoubleEnter || !result.Atomic.Sent {
		t.Fatalf("atomic distribute result = %+v", result)
	}
}

func TestDistributeAssignmentIdempotencyAndReceiptContracts(t *testing.T) {
	base := distributeAssignmentIdempotencyKey("proj", "ntm-work", "%9", "prompt", 0)
	if base == "" || distributeAssignmentIdempotencyKey("proj", "ntm-work", "%9", "prompt", 0) != base {
		t.Fatal("same distribute intent did not produce one stable key")
	}
	for name, changed := range map[string]string{
		"session": distributeAssignmentIdempotencyKey("other", "ntm-work", "%9", "prompt", 0),
		"bead":    distributeAssignmentIdempotencyKey("proj", "ntm-other", "%9", "prompt", 0),
		"pane":    distributeAssignmentIdempotencyKey("proj", "ntm-work", "%10", "prompt", 0),
		"prompt":  distributeAssignmentIdempotencyKey("proj", "ntm-work", "%9", "different", 0),
		"clear":   distributeAssignmentIdempotencyKey("proj", "ntm-work", "%9", "prompt", 1),
	} {
		if changed == base {
			t.Fatalf("%s change reused distribute idempotency key", name)
		}
	}

	deliveryID := assignment.DispatchDeliveryID("%9", "double_enter", base)
	result := distributeAtomicDispatchResult{
		Atomic: assignment.AtomicResult{
			Assignment: &assignment.Assignment{ClaimActor: "agent/ntm-key", DispatchReceiptID: deliveryID},
			Dispatch:   assignment.DispatchReceipt{DeliveryID: deliveryID},
			Sent:       true,
			Replayed:   true,
		},
		Redaction:      dispatchsvc.RedactionReceipt{Mode: "redact", Findings: 2},
		IdempotencyKey: base,
	}
	receipt := buildDistributeDispatchReceipt(robot.DistributeRecommendation{BeadID: "ntm-work", PaneID: "%9", PaneTarget: "0.1"}, result, nil)
	if receipt.Status != dispatchsvc.ReceiptDelivered || receipt.Protocol != dispatchsvc.ProtocolDoubleEnter ||
		receipt.DeliveryID != deliveryID || receipt.IdempotencyKey != base || receipt.ClaimActor != "agent/ntm-key" ||
		!receipt.Replayed || receipt.Redaction.Findings != 2 || receipt.Error != "" {
		t.Fatalf("replayed distribute receipt = %+v", receipt)
	}
	if got := distributeProtocolFromDeliveryID("malformed"); got != "" {
		t.Fatalf("malformed delivery protocol = %q, want empty", got)
	}
}

func TestOutputSendCommandErrorClassifiesOwnedJSONFailures(t *testing.T) {
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })

	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "invalid policy", err: markCLIInvalidInput(errors.New("invalid assignment policy")), code: robot.ErrCodeInvalidFlag},
		{name: "canceled", err: context.Canceled, code: robot.ErrCodeTimeout},
		{name: "runtime", err: errors.New("live bead changed"), code: robot.ErrCodeInternalError},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, err := captureStdout(t, func() error { return outputSendCommandError("proj", test.err) })
			if !errors.Is(err, errJSONFailure) {
				t.Fatalf("outputSendCommandError error = %v, want owned JSON failure", err)
			}
			var result SendResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("decode send failure JSON: %v output=%q", err, stdout)
			}
			if result.Success || result.ErrorCode != test.code || result.Targets == nil || len(result.Targets) != 0 {
				t.Fatalf("send failure result = %+v, want code %s and empty targets", result, test.code)
			}
		})
	}
}

func TestRunCodexGoalSendValidationOwnsOneJSONFailure(t *testing.T) {
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })

	tests := []struct {
		name string
		pane string
		body string
	}{
		{name: "missing pane", body: "goal"},
		{name: "missing body", pane: "%1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout, err := captureStdout(t, func() error {
				return runCodexGoalSend(t.Context(), "proj", test.pane, test.body)
			})
			if !errors.Is(err, errJSONFailure) {
				t.Fatalf("runCodexGoalSend error = %v, want owned JSON failure", err)
			}
			var output CodexGoalSendResult
			if err := json.Unmarshal([]byte(stdout), &output); err != nil {
				t.Fatalf("decode exactly one JSON document: %v output=%q", err, stdout)
			}
			if output.Success || output.ErrorCode != robot.ErrCodeInvalidFlag || output.State != "failed" {
				t.Fatalf("validation output = %+v", output)
			}
		})
	}
}

func TestRunCodexGoalSendPreCanceledContextReturnsTimeoutJSON(t *testing.T) {
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stdout, err := captureStdout(t, func() error {
		return runCodexGoalSend(ctx, "proj", "%1", "goal")
	})
	if !errors.Is(err, errJSONFailure) || !errors.Is(err, context.Canceled) {
		t.Fatalf("runCodexGoalSend error = %v, want owned canceled failure", err)
	}
	var output CodexGoalSendResult
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode timeout JSON: %v output=%q", err, stdout)
	}
	if output.Success || output.ErrorCode != robot.ErrCodeTimeout {
		t.Fatalf("timeout output = %+v", output)
	}
}

func TestEmitCodexGoalSendResultSurfacesEncoderFailure(t *testing.T) {
	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = originalStdout
		_ = reader.Close()
	})
	err = emitCodexGoalSendResult(CodexGoalSendResult{RobotResponse: robot.NewRobotResponse(true)}, nil, "", "", true)
	if err == nil || errors.Is(err, errJSONFailure) || !strings.Contains(err.Error(), "encode Codex goal-send response") {
		t.Fatalf("encoder error = %v, want surfaced encode failure", err)
	}
}

func TestWaitCodexGoalContextCancelsWithoutDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	if err := waitCodexGoalContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled wait took %s", elapsed)
	}
}

func TestResolveSendSessionForCommandContextRejectsCanceledContextBeforeTopology(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := resolveSendSessionForCommandContext(ctx, "unused")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resolveSendSessionForCommandContext error = %v, want context.Canceled", err)
	}
}

func TestShellDispatchOrdererPreservesSelectedOrder(t *testing.T) {
	t.Parallel()
	selected := []tmux.Pane{
		{ID: "%3", Index: 2, WindowIndex: 0, Type: tmux.AgentCodex},
		{ID: "%1", Index: 0, WindowIndex: 0, Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, WindowIndex: 0, Type: tmux.AgentGemini},
	}
	planned := []dispatchsvc.Target{
		{Pane: selected[1], Ref: selected[1].Ref(), Address: "0"},
		{Pane: selected[2], Ref: selected[2].Ref(), Address: "1"},
		{Pane: selected[0], Ref: selected[0].Ref(), Address: "2"},
	}
	ordered, err := shellDispatchOrderer(selected).OrderTargets(context.Background(), dispatchsvc.OrderInput{Session: "proj", Targets: planned})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{ordered[0].Ref.ID, ordered[1].Ref.ID, ordered[2].Ref.ID}
	if want := []string{"%3", "%1", "%2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestShellDispatchProtocolMatchesLegacySendPromptBehavior(t *testing.T) {
	t.Parallel()
	planner := shellDispatchProtocolPlanner{}
	for _, tc := range []struct {
		name     string
		paneType tmux.AgentType
		submit   bool
		want     dispatchsvc.ProtocolPlan
	}{
		{name: "user paste and enter", paneType: tmux.AgentUser, submit: true, want: dispatchsvc.ProtocolPlan{Protocol: dispatchsvc.ProtocolSingleEnter, EnterDelay: tmux.DefaultEnterDelay}},
		{name: "claude double enter", paneType: tmux.AgentClaude, submit: true, want: dispatchsvc.ProtocolPlan{Protocol: dispatchsvc.ProtocolDoubleEnter, EnterDelay: tmux.DoubleEnterFirstDelay, SecondEnterDelay: tmux.DoubleEnterSecondDelay}},
		{name: "unknown historically double enters", paneType: tmux.AgentUnknown, submit: true, want: dispatchsvc.ProtocolPlan{Protocol: dispatchsvc.ProtocolDoubleEnter, EnterDelay: tmux.DoubleEnterFirstDelay, SecondEnterDelay: tmux.DoubleEnterSecondDelay}},
		{name: "stage only", paneType: tmux.AgentClaude, submit: false, want: dispatchsvc.ProtocolPlan{Protocol: dispatchsvc.ProtocolStageOnly}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planner.PlanDelivery(context.Background(), dispatchsvc.Target{Pane: tmux.Pane{Type: tc.paneType}}, tc.submit)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("plan = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestShellFinalMessageRedactorParity(t *testing.T) {
	t.Parallel()
	message := "password=hunter2hunter2"
	for _, mode := range []redaction.Mode{redaction.ModeOff, redaction.ModeWarn, redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			want := redaction.ScanAndRedact(message, redaction.Config{Mode: mode})
			got, err := shellFinalMessageRedactor(redaction.Config{Mode: mode}).RedactFinalMessage(context.Background(), dispatchsvc.Target{}, message)
			if err != nil {
				t.Fatal(err)
			}
			if got.Message != want.Output || got.Blocked != want.Blocked || got.Findings != len(want.Findings) || got.Mode != string(mode) {
				t.Fatalf("result = %+v, scan = %+v", got, want)
			}
		})
	}
}

func TestShellDispatchRequestUsesStableSelectorsAndFailurePolicy(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, Type: tmux.AgentClaude},
		{ID: "%2", Index: 0, WindowIndex: 1, Type: tmux.AgentCodex},
	}
	req := shellDispatchRequest("proj", panes, []tmux.Pane{panes[1], panes[0]}, "work", true)
	if req.Session != "proj" || req.Message != "work" || !req.Submit || !req.IncludeUser || !req.StopOnFailure {
		t.Fatalf("request = %+v", req)
	}
	if want := []string{"%2", "%1"}; !reflect.DeepEqual(req.Selectors, want) {
		t.Fatalf("selectors = %v, want %v", req.Selectors, want)
	}
}

func TestSaveDeliveredPromptSkipsUndeliveredPrompts(t *testing.T) {
	entry := sessionPkg.PromptEntry{Content: "must not persist"}
	if err := saveDeliveredPrompt(0, entry); err != nil {
		t.Fatalf("zero-delivery prompt persistence error = %v, want nil", err)
	}
	if err := saveDeliveredPrompt(1, entry); err == nil || !strings.Contains(err.Error(), "session name is required") {
		t.Fatalf("delivered prompt persistence error = %v, want session validation", err)
	}
}

func TestFinishSendResultCollectsWithoutGlobalJSONMode(t *testing.T) {
	oldJSONOutput := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSONOutput })

	cause := errors.New("delivery failed")
	collected := &sendExecutionResult{}
	err := finishSendResult(SendOptions{
		executionPolicy: sendExecutionCollect,
		executionResult: collected,
	}, SendResult{
		Success: false,
		Session: "proj--b",
		Failed:  1,
		Error:   cause.Error(),
	}, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("finishSendResult error = %v, want original cause", err)
	}
	if !collected.recorded || collected.result.Success || collected.result.Session != "proj--b" {
		t.Fatalf("collected result = %+v, want recorded failure", collected)
	}
	if collected.result.Targets == nil || len(collected.result.Targets) != 0 {
		t.Fatalf("collected targets = %#v, want non-nil empty slice", collected.result.Targets)
	}
}

func TestEmitBatchResultPreservesFailureCause(t *testing.T) {
	cause := errors.New("pane delivery failed")
	stdout, err := captureStdout(t, func() error {
		return emitBatchResult(BatchResult{
			Success: false,
			Session: "proj",
			Total:   1,
			Failed:  1,
			Results: []BatchPromptResult{},
		}, cause)
	})
	if !errors.Is(err, errJSONFailure) || !errors.Is(err, cause) {
		t.Fatalf("emitBatchResult error = %v, want terminal sentinel and original cause", err)
	}

	var result BatchResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode exactly one batch failure document: %v; stdout=%q", err, stdout)
	}
	if result.Success || result.Failed != 1 || result.Session != "proj" {
		t.Fatalf("batch failure result = %+v", result)
	}
}

func TestFinishSendDryRunResultCollectsDeliverySummary(t *testing.T) {
	collected := &sendExecutionResult{}
	err := finishSendDryRunResult(SendOptions{
		executionPolicy: sendExecutionCollect,
		executionResult: collected,
	}, SendDryRunResult{
		Success: true,
		DryRun:  true,
		Session: "proj--a",
		Total:   2,
		WouldSend: []SendDryRunEntry{
			{Pane: "0.1"},
			{Pane: "1.0"},
		},
	})
	if err != nil {
		t.Fatalf("finishSendDryRunResult error = %v", err)
	}
	if !collected.recorded || !collected.dryRun || collected.wouldSend != 2 || !collected.result.Success {
		t.Fatalf("collected dry-run result = %+v", collected)
	}
	if want := []string{"0.1", "1.0"}; !reflect.DeepEqual(collected.result.Targets, want) {
		t.Fatalf("collected dry-run targets = %v, want %v", collected.result.Targets, want)
	}
}

func TestBuildSendProjectResultAggregatesAndSortsReceipts(t *testing.T) {
	input := []sendProjectSessionResult{
		{Session: "proj--z", Success: false, Failed: 1, ErrorCode: robot.ErrCodeTimeout, Error: "canceled"},
		{Session: "proj--a", Success: true, Targets: []string{"0"}, Delivered: 2},
		{Session: "proj--m", Success: false, Targets: []string{"1"}, Delivered: 1, Failed: 3, ErrorCode: sendErrorCodeFailed, Error: "delivery failed"},
	}

	result := buildSendProjectResult("proj", input)
	if result.Success || result.Project != "proj" || result.MatchedSessions != 3 || result.SucceededSessions != 1 || result.FailedSessions != 2 {
		t.Fatalf("project aggregate = %+v", result)
	}
	if result.Delivered != 3 || result.FailedDeliveries != 4 || result.ErrorCode != robot.ErrCodeTimeout || result.Error == "" {
		t.Fatalf("project delivery summary = %+v", result)
	}
	gotOrder := []string{result.Sessions[0].Session, result.Sessions[1].Session, result.Sessions[2].Session}
	if want := []string{"proj--a", "proj--m", "proj--z"}; !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("project session order = %v, want %v", gotOrder, want)
	}
	if result.Sessions[2].Targets == nil || len(result.Sessions[2].Targets) != 0 {
		t.Fatalf("timeout targets = %#v, want non-nil empty slice", result.Sessions[2].Targets)
	}
	if input[0].Targets != nil {
		t.Fatalf("buildSendProjectResult mutated caller input: %+v", input[0])
	}
}

func TestSendProjectSessionFromExecutionClassifiesCancellation(t *testing.T) {
	cause := fmt.Errorf("dispatch interrupted: %w", context.DeadlineExceeded)
	collected := &sendExecutionResult{
		recorded: true,
		result: SendResult{
			Success:   false,
			Session:   "proj--a",
			Targets:   []string{"0"},
			ErrorCode: sendErrorCodeFailed,
			Error:     cause.Error(),
		},
	}

	result, err := sendProjectSessionFromExecution("proj--a", collected, cause)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("session result error = %v, want deadline exceeded", err)
	}
	if result.Success || result.ErrorCode != robot.ErrCodeTimeout || result.Session != "proj--a" {
		t.Fatalf("session cancellation result = %+v", result)
	}
}

func TestExecuteShellDispatchDryRunPreflightsFinalRedaction(t *testing.T) {
	oldCfg := cfg
	cfg = config.Default()
	cfg.Redaction.Mode = string(redaction.ModeBlock)
	t.Cleanup(func() { cfg = oldCfg })

	panes := []tmux.Pane{{ID: "%1", Index: 0, WindowIndex: 0, Type: tmux.AgentClaude}}
	safeOutput := shellPromptForOutput("password=hunter2hunter2")
	if strings.Contains(safeOutput, "hunter2hunter2") || !strings.Contains(safeOutput, "REDACTED") {
		t.Fatalf("safe dry-run output leaked blocked prompt: %q", safeOutput)
	}
	result, err := executeShellDispatch(
		context.Background(),
		"proj",
		panes,
		panes,
		"password=hunter2hunter2",
		true,
	)
	var dispatchErr *dispatchsvc.Error
	if !errors.As(err, &dispatchErr) || dispatchErr.Code != dispatchsvc.ErrRedactionBlocked {
		t.Fatalf("dry-run error = %v, want %s", err, dispatchsvc.ErrRedactionBlocked)
	}
	if result.Success || result.Delivered != 0 || result.Blocked != 1 {
		t.Fatalf("dry-run blocked result = %+v", result)
	}
}

func TestResolveDistributeRecommendationPaneUsesExactIDAcrossDuplicateLocalIndexes(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		{ID: "%41", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
		{ID: "%91", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude},
	}
	rec := robot.DistributeRecommendation{BeadID: "ntm-window-one", PaneID: "%91", PaneTarget: "1.1", AgentType: "claude"}
	target, err := resolveDistributeRecommendationPane(rec, panes)
	if err != nil {
		t.Fatalf("resolve exact distribute target: %v", err)
	}
	if target.ID != "%91" || target.WindowIndex != 1 || target.Index != 1 {
		t.Fatalf("resolved target=%+v, want window-one pane ID", target)
	}

	for name, invalid := range map[string]robot.DistributeRecommendation{
		"missing id":    {BeadID: rec.BeadID, PaneTarget: rec.PaneTarget, AgentType: rec.AgentType},
		"unknown id":    {BeadID: rec.BeadID, PaneID: "%404", PaneTarget: rec.PaneTarget, AgentType: rec.AgentType},
		"agent changed": {BeadID: rec.BeadID, PaneID: rec.PaneID, PaneTarget: rec.PaneTarget, AgentType: "codex"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveDistributeRecommendationPane(invalid, panes); err == nil {
				t.Fatalf("resolveDistributeRecommendationPane(%+v) succeeded", invalid)
			}
		})
	}
}

func TestUnifiedDistributeServiceStopsBeforeDeliveryWhenIdleGateFails(t *testing.T) {
	oldCfg := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = oldCfg })
	pane := tmux.Pane{ID: "%999999", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude}
	gateCalls := 0
	service, err := newShellDispatchServiceWithGate("proj", []tmux.Pane{pane}, nil, activeShellDispatchRedactionConfig(),
		func(context.Context, dispatchsvc.Request, []dispatchsvc.Delivery) error {
			gateCalls++
			return errors.New("target became busy")
		})
	if err != nil {
		t.Fatalf("newShellDispatchServiceWithGate: %v", err)
	}
	result, err := service.Execute(t.Context(), shellDispatchRequest("proj", []tmux.Pane{pane}, []tmux.Pane{pane}, "work", true))
	var dispatchErr *dispatchsvc.Error
	if !errors.As(err, &dispatchErr) || dispatchErr.Code != dispatchsvc.ErrLifecycle {
		t.Fatalf("busy gate error=%v, want lifecycle failure", err)
	}
	if gateCalls != 1 || result.Delivered != 0 || result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("busy gate calls=%d result=%+v", gateCalls, result)
	}
}

func TestRunSendWithTargetsGateSeesFinalMessageAndRefusesTransport(t *testing.T) {
	_, logPath := workflowCommandTmuxFixture(t, "stable")
	cfg.Redaction.Mode = string(redaction.ModeRedact)
	cfg.Robot.Semantic.Stamp = true
	refusal := errors.New("workflow stage evidence changed")
	gateCalls := 0
	collected := &sendExecutionResult{}
	err := runSendWithTargets(SendOptions{
		Context: t.Context(), Session: "evidence-run", PaneSelector: "%91",
		BasePrompt: "Review all changes carefully", Prompt: "Inspect the update with password=hunter2hunter2",
		PromptSource: "workflow", ForceNonInteractive: true, NoHooks: true, NoCASS: true,
		executionPolicy: sendExecutionCollect, executionResult: collected,
		beforeDispatch: func(ctx context.Context, request dispatchsvc.Request, deliveries []dispatchsvc.Delivery) error {
			gateCalls++
			if err := ctx.Err(); err != nil {
				t.Fatalf("gate received cancelled context: %v", err)
			}
			if request.Session != "evidence-run" || len(deliveries) != 1 || deliveries[0].Target.Pane.ID != "%91" || deliveries[0].Target.Pane.PID != 4242 {
				t.Fatalf("gate lost exact delivery identity: request=%+v deliveries=%+v", request, deliveries)
			}
			message := deliveries[0].Message
			if !strings.Contains(message, "Review all changes carefully") || !strings.Contains(message, "Inspect the update") || !strings.Contains(message, "[REDACTED:PASSWORD:") || strings.Contains(message, "hunter2hunter2") || !strings.Contains(message, "NTM-Pane: evidence-run/0.1") {
				t.Fatalf("gate did not receive final composed, redacted, stamped message: %q", message)
			}
			return refusal
		},
	})
	if !errors.Is(err, refusal) || gateCalls != 1 || !collected.recorded || collected.result.Success || collected.result.Delivered != 0 {
		t.Fatalf("send ignored gate refusal: calls=%d result=%+v error=%v", gateCalls, collected, err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"load-buffer", "paste-buffer", "send-keys"} {
		if strings.Contains(string(log), operation) {
			t.Fatalf("refused final message reached %s: %s", operation, log)
		}
	}
}

// TestSendRealSession tests sending a prompt to a real tmux session
func TestSendRealSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir for projects
	tmpDir, err := os.MkdirTemp("", "ntm-test-send")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save/Restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true // Use JSON output to avoid polluting test logs

	// Use /bin/cat explicitly to avoid shell aliases (e.g., cat -> bat) which
	// have different input/output behavior and can cause test flakiness.
	cfg.Agents.Claude = testAgentBinCatCommandTemplate

	sessionName := fmt.Sprintf("ntm-test-send-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Define agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
	}

	// Create project dir
	projectDir := filepath.Join(tmpDir, sessionName)
	err = os.MkdirAll(projectDir, 0755)
	if err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn session
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}
	err = spawnSessionLogicContext(t.Context(), opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Wait for session to settle - needs enough time for:
	// 1. Shell to initialize (load zshrc, plugins, etc.)
	// 2. The cd && agent command to be processed
	// 3. Agent (cat) to be ready to receive input
	time.Sleep(1500 * time.Millisecond)

	// Send a prompt
	prompt := "Hello NTM Test"
	targets := SendTargets{} // Empty targets = default behavior (all agents)

	// Send to all agents (skip user pane default)
	err = runSendWithTargets(SendOptions{
		Session:   sessionName,
		Prompt:    prompt,
		Targets:   targets,
		TargetAll: true,
		SkipFirst: false,
	})
	if err != nil {
		t.Fatalf("runSendWithTargets failed: %v", err)
	}

	// Wait for keys to be processed by tmux/shell
	time.Sleep(500 * time.Millisecond)

	// Verify output in pane
	// We spawned 1 Claude agent, so it should be at index 1 (index 0 is user)
	// We need to find the pane ID or just use index
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	var agentPane *tmux.Pane
	for i := range panes {
		if panes[i].Type == tmux.AgentClaude {
			agentPane = &panes[i]
			break
		}
	}

	if agentPane == nil {
		t.Fatal("Agent pane not found")
	}

	output, err := tmux.CapturePaneOutput(agentPane.ID, 10)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}

	if !strings.Contains(output, prompt) {
		t.Errorf("Pane output did not contain prompt %q. Got:\n%s", prompt, output)
	}

	// Redaction: redact mode should replace sensitive substrings before sending to panes.
	cfg.Redaction.Mode = "redact"
	redactSecret := "prefix password=hunter2hunter2 suffix"
	if err := runSendWithTargets(SendOptions{
		Session:   sessionName,
		Prompt:    redactSecret,
		Targets:   SendTargets{},
		TargetAll: true,
		SkipFirst: false,
	}); err != nil {
		t.Fatalf("runSendWithTargets (redact) failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	redactedOutput, err := tmux.CapturePaneOutput(agentPane.ID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if strings.Contains(redactedOutput, "hunter2hunter2") {
		t.Fatalf("expected secret to be redacted in pane output, got:\n%s", redactedOutput)
	}
	if !strings.Contains(redactedOutput, "[REDACTED:PASSWORD:") {
		t.Fatalf("expected redaction placeholder in pane output, got:\n%s", redactedOutput)
	}

	// Redaction: block mode should abort send (and not leak secrets to panes).
	cfg.Redaction.Mode = "block"
	blockSecret := "prefix password=blocksecretblocksecret suffix"
	err = runSendWithTargets(SendOptions{
		Session:   sessionName,
		Prompt:    blockSecret,
		Targets:   SendTargets{},
		TargetAll: true,
		SkipFirst: false,
	})
	if err == nil {
		t.Fatalf("expected error in block mode")
	}
	var blocked redactionBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected redactionBlockedError, got %T: %v", err, err)
	}

	time.Sleep(500 * time.Millisecond)
	blockedOutput, err := tmux.CapturePaneOutput(agentPane.ID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if strings.Contains(blockedOutput, "blocksecretblocksecret") {
		t.Fatalf("expected secret not to appear in pane output when blocked, got:\n%s", blockedOutput)
	}
}

// TestGetPromptContentFromArgs tests reading prompt from positional arguments
func TestGetPromptContentFromArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		prefix    string
		suffix    string
		want      string
		wantSrc   string
		wantError bool
	}{
		{
			name:    "single arg",
			args:    []string{"hello world"},
			want:    "hello world",
			wantSrc: "args",
		},
		{
			name:    "multiple args joined",
			args:    []string{"hello", "world"},
			want:    "hello world",
			wantSrc: "args",
		},
		{
			name:      "no args error",
			args:      []string{},
			wantError: true,
		},
		{
			name:    "prefix/suffix ignored for args",
			args:    []string{"hello"},
			prefix:  "PREFIX",
			suffix:  "SUFFIX",
			want:    "hello", // prefix/suffix don't apply to args
			wantSrc: "args",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotSrc, err := getPromptContent(tt.args, "", tt.prefix, tt.suffix)
			if tt.wantError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if tt.wantSrc != "" && gotSrc != tt.wantSrc {
				t.Errorf("source: got %q, want %q", gotSrc, tt.wantSrc)
			}
		})
	}
}

func TestGetSessionWorkingDirRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	session := fmt.Sprintf("missing-send-project-%d", time.Now().UnixNano())

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	if got := getSessionWorkingDir(t.Context(), session, false); got != "" {
		t.Fatalf("getSessionWorkingDir explicit = %q, want empty", got)
	}
}

func TestGetSessionWorkingDirAllowsWorkspaceFallbackForInferredSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := canonicalTempDir(t)
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	if got := getSessionWorkingDir(t.Context(), "ntm", true); got != cwdRepo {
		t.Fatalf("getSessionWorkingDir inferred = %q, want %q", got, cwdRepo)
	}
}

// TestGetPromptContentFromFile tests reading prompt from a file
func TestGetPromptContentFromFile(t *testing.T) {
	// Create a temp file with content
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "prompt.txt")
	content := "This is the prompt content"
	writeErr := os.WriteFile(testFile, []byte(content), 0644)
	if writeErr != nil {
		t.Fatalf("Failed to write test file: %v", writeErr)
	}

	// Create empty file for error test
	emptyFile := filepath.Join(tmpDir, "empty.txt")
	writeErr = os.WriteFile(emptyFile, []byte(""), 0644)
	if writeErr != nil {
		t.Fatalf("Failed to write empty file: %v", writeErr)
	}

	tests := []struct {
		name       string
		promptFile string
		prefix     string
		suffix     string
		want       string
		wantError  bool
	}{
		{
			name:       "file content",
			promptFile: testFile,
			want:       content,
		},
		{
			name:       "file with prefix",
			promptFile: testFile,
			prefix:     "PREFIX:",
			want:       "PREFIX:\n" + content,
		},
		{
			name:       "file with suffix",
			promptFile: testFile,
			suffix:     ":SUFFIX",
			want:       content + "\n:SUFFIX",
		},
		{
			name:       "file with prefix and suffix",
			promptFile: testFile,
			prefix:     "START",
			suffix:     "END",
			want:       "START\n" + content + "\nEND",
		},
		{
			name:       "nonexistent file error",
			promptFile: "/nonexistent/path/file.txt",
			wantError:  true,
		},
		{
			name:       "empty file error",
			promptFile: emptyFile,
			wantError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotSrc, err := getPromptContent([]string{}, tt.promptFile, tt.prefix, tt.suffix)
			if tt.wantError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			wantSrc := "file:" + tt.promptFile
			if gotSrc != wantSrc {
				t.Errorf("source: got %q, want %q", gotSrc, wantSrc)
			}
		})
	}
}

func TestShuffledPermutation_DeterministicSeed(t *testing.T) {

	seed := int64(12345)
	seedUsed1, perm1 := shuffledPermutation(32, seed)
	seedUsed2, perm2 := shuffledPermutation(32, seed)

	if seedUsed1 != seed || seedUsed2 != seed {
		t.Fatalf("expected seedUsed to match provided seed, got %d and %d", seedUsed1, seedUsed2)
	}
	if len(perm1) != len(perm2) {
		t.Fatalf("perm length mismatch: %d vs %d", len(perm1), len(perm2))
	}
	for i := range perm1 {
		if perm1[i] != perm2[i] {
			t.Fatalf("expected identical permutations for same seed, mismatch at %d: %v vs %v", i, perm1, perm2)
		}
	}
}

func TestShuffledPermutation_IsPermutation(t *testing.T) {

	_, perm := shuffledPermutation(100, 999)
	seen := make(map[int]bool, len(perm))
	for _, v := range perm {
		if v < 0 || v >= 100 {
			t.Fatalf("perm contains out-of-range value %d", v)
		}
		if seen[v] {
			t.Fatalf("perm contains duplicate value %d", v)
		}
		seen[v] = true
	}
}

func TestPermutePanes_AppliesPermutation(t *testing.T) {

	panes := []tmux.Pane{
		{Index: 10},
		{Index: 11},
		{Index: 12},
		{Index: 13},
	}
	perm := []int{2, 0, 3, 1}
	out := permutePanes(panes, perm)
	if len(out) != len(panes) {
		t.Fatalf("permutePanes length = %d, want %d", len(out), len(panes))
	}
	got := []int{out[0].Index, out[1].Index, out[2].Index, out[3].Index}
	want := []int{12, 10, 13, 11}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("permutePanes[%d] = %d, want %d (got=%v)", i, got[i], want[i], got)
		}
	}
}

// TestBuildPrompt tests the buildPrompt helper function
func TestBuildPrompt(t *testing.T) {
	tests := []struct {
		name    string
		content string
		prefix  string
		suffix  string
		want    string
	}{
		{
			name:    "content only",
			content: "hello",
			want:    "hello",
		},
		{
			name:    "with prefix",
			content: "hello",
			prefix:  "PREFIX:",
			want:    "PREFIX:\nhello",
		},
		{
			name:    "with suffix",
			content: "hello",
			suffix:  ":SUFFIX",
			want:    "hello\n:SUFFIX",
		},
		{
			name:    "with both",
			content: "hello",
			prefix:  "START",
			suffix:  "END",
			want:    "START\nhello\nEND",
		},
		{
			name:    "content with whitespace trimmed",
			content: "  hello  \n",
			want:    "hello",
		},
		{
			name:    "multiline content",
			content: "line1\nline2\nline3",
			prefix:  "BEGIN",
			suffix:  "DONE",
			want:    "BEGIN\nline1\nline2\nline3\nDONE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPrompt(tt.content, tt.prefix, tt.suffix)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTruncatePrompt tests the truncatePrompt helper
func TestTruncatePrompt(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer prompt", 10, "this is..."},
		{"", 10, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncatePrompt(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncatePrompt(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestExtractLikelyCommands(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "simple git command",
			input: "git status",
			want:  []string{"git status"},
		},
		{
			name:  "prefixed shell prompt",
			input: "  $ rm -rf /tmp",
			want:  []string{"rm -rf /tmp"},
		},
		{
			name:  "command in fenced block",
			input: "```bash\nrm -rf /var/tmp\n```",
			want:  []string{"rm -rf /var/tmp"},
		},
		{
			name:  "flag-only heuristic",
			input: "deploy --force",
			want:  []string{"deploy --force"},
		},
		{
			name:  "non-command text",
			input: "please review the changes",
			want:  nil,
		},
		{
			name:  "multiple commands",
			input: "git status\nrm -rf /tmp\njust some text",
			want:  []string{"git status", "rm -rf /tmp"},
		},
		{
			name:  "markdown dash bullet with command chain",
			input: "  - run br list && git status",
			want:  []string{"run br list && git status"},
		},
		{
			name:  "asterisk bullet with force flag",
			input: "* deploy --force",
			want:  []string{"deploy --force"},
		},
		{
			name:  "plus bullet",
			input: "+ git push --force origin main",
			want:  []string{"git push --force origin main"},
		},
		{
			name:  "checkbox bullet",
			input: "- [ ] make clean && make all",
			want:  []string{"make clean && make all"},
		},
		{
			name:  "checked checkbox bullet",
			input: "- [x] cargo build || cargo check",
			want:  []string{"cargo build || cargo check"},
		},
		{
			name:  "ordered list",
			input: "1. rm -rf ./build\n2) git status | cat",
			want:  []string{"rm -rf ./build", "git status | cat"},
		},
		{
			name:  "nested bullet under quote",
			input: "> - sudo rm -rf /var/tmp",
			want:  []string{"sudo rm -rf /var/tmp"},
		},
		{
			name:  "leading-dash flag text is not treated as a bullet",
			input: "-rf is a dangerous flag; grep -- -rf script.sh",
			want:  []string{"-rf is a dangerous flag; grep -- -rf script.sh"},
		},
		{
			name:  "redirection digits are not an ordered list",
			input: "2> /dev/null ls | wc -l",
			want:  []string{"2> /dev/null ls | wc -l"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLikelyCommands(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("extractLikelyCommands got %d commands, want %d: got=%v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("extractLikelyCommands[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLooksLikeShellCommand(t *testing.T) {
	tests := []struct {
		line   string
		expect bool
	}{
		{"git status", true},
		{"sudo rm -rf /", true},
		{"echo hello", false},
		{"foo && bar", true},
		{"use --force when needed", true},
		{"just some words", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := looksLikeShellCommand(tt.line)
			if got != tt.expect {
				t.Fatalf("looksLikeShellCommand(%q) = %v, want %v", tt.line, got, tt.expect)
			}
		})
	}
}

// TestSendFlagNoOptDefVal verifies that --cc/--cod/--gmi flags work without consuming
// the next positional argument as the flag value. This tests the NoOptDefVal fix.
// Before the fix: "ntm send session --cod hello" would fail because "hello" was consumed by --cod
// After the fix: "ntm send session --cod hello" correctly parses "hello" as the prompt
func TestSendFlagNoOptDefVal(t *testing.T) {
	cmd := newSendCmd()

	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		checkMsg string
	}{
		{
			name:     "cod flag before prompt",
			args:     []string{"testsession", "--cod", "hello world"},
			wantErr:  false, // Should NOT error - prompt should be parsed correctly
			checkMsg: "flag before prompt should work with NoOptDefVal",
		},
		{
			name:     "cc flag before prompt",
			args:     []string{"testsession", "--cc", "test prompt"},
			wantErr:  false,
			checkMsg: "cc flag before prompt should work",
		},
		{
			name:     "gmi flag before prompt",
			args:     []string{"testsession", "--gmi", "another prompt"},
			wantErr:  false,
			checkMsg: "gmi flag before prompt should work",
		},
		{
			name:     "multiple flags before prompt",
			args:     []string{"testsession", "--cc", "--cod", "multi agent prompt"},
			wantErr:  false,
			checkMsg: "multiple flags before prompt should work",
		},
		{
			name:     "flag with variant value",
			args:     []string{"testsession", "--cc=opus", "prompt with variant"},
			wantErr:  false,
			checkMsg: "flag with explicit variant should work",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fresh command for each test
			testCmd := newSendCmd()
			testCmd.SetArgs(tt.args)

			// Just parse flags - don't execute (would need tmux)
			err := testCmd.ParseFlags(tt.args)
			if tt.wantErr && err == nil {
				t.Errorf("%s: expected error but got nil", tt.checkMsg)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("%s: unexpected error: %v", tt.checkMsg, err)
			}

			// Verify the prompt wasn't consumed by the flag
			// After parsing flags, remaining args should contain the prompt
			remainingArgs := testCmd.Flags().Args()
			if !tt.wantErr && len(remainingArgs) < 2 {
				t.Errorf("%s: expected prompt in remaining args, got: %v", tt.checkMsg, remainingArgs)
			}
		})
	}

	_ = cmd // silence unused warning
}

// TestParseBatchFile tests the batch file parser
func TestParseBatchFile(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name      string
		content   string
		want      []string
		wantSrc   []string
		wantError bool
	}{
		{
			name:    "simple one per line",
			content: "prompt one\nprompt two\nprompt three",
			want:    []string{"prompt one", "prompt two", "prompt three"},
			wantSrc: []string{"line:1", "line:2", "line:3"},
		},
		{
			name:    "with comments",
			content: "# This is a comment\nprompt one\n# Another comment\nprompt two",
			want:    []string{"prompt one", "prompt two"},
			wantSrc: []string{"line:2", "line:4"},
		},
		{
			name:    "with empty lines",
			content: "prompt one\n\n\nprompt two\n\n",
			want:    []string{"prompt one", "prompt two"},
			wantSrc: []string{"line:1", "line:4"},
		},
		{
			name:    "separator format",
			content: "First prompt\nwith multiple lines\n---\nSecond prompt",
			want:    []string{"First prompt\nwith multiple lines", "Second prompt"},
			wantSrc: []string{"line:1", "line:4"},
		},
		{
			name:    "separator with comments",
			content: "# Header comment\nFirst prompt\n---\n# Comment in second\nSecond prompt",
			want:    []string{"First prompt", "Second prompt"},
			wantSrc: []string{"line:2", "line:5"},
		},
		{
			name:    "leading separator",
			content: "---\nFirst prompt\n---\nSecond prompt",
			want:    []string{"First prompt", "Second prompt"},
			wantSrc: []string{"line:2", "line:4"},
		},
		{
			name:      "empty file",
			content:   "",
			wantError: true,
		},
		{
			name:      "only whitespace",
			content:   "   \n\n   ",
			wantError: true,
		},
		{
			name:      "only comments",
			content:   "# comment 1\n# comment 2",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file with content
			testFile := filepath.Join(tmpDir, fmt.Sprintf("batch_%s.txt", tt.name))
			writeErr := os.WriteFile(testFile, []byte(tt.content), 0644)
			if writeErr != nil {
				t.Fatalf("Failed to write test file: %v", writeErr)
			}

			got, err := parseBatchFile(testFile)
			if tt.wantError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d prompts, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i].Text != tt.want[i] {
					t.Errorf("prompt %d: got %q, want %q", i, got[i].Text, tt.want[i])
				}
				if len(tt.wantSrc) > 0 && got[i].Source != tt.wantSrc[i] {
					t.Errorf("prompt %d source: got %q, want %q", i, got[i].Source, tt.wantSrc[i])
				}
			}
		})
	}

	// Test nonexistent file
	t.Run("nonexistent file", func(t *testing.T) {
		_, err := parseBatchFile("/nonexistent/path/file.txt")
		if err == nil {
			t.Error("Expected error for nonexistent file")
		}
	})
}

func TestSendDryRunDoesNotSendToPane(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir, err := os.MkdirTemp("", "ntm-test-send-dry-run")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save/Restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Checkpoints.Enabled = false
	jsonOutput = true // avoid polluting test logs

	// Use a simple echoing agent so we can detect sends
	cfg.Agents.Claude = testAgentCatCommandTemplate

	sessionName := fmt.Sprintf("ntm-test-send-dry-run-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	projectDir := filepath.Join(tmpDir, sessionName)
	err = os.MkdirAll(projectDir, 0755)
	if err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
	}
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}
	err = spawnSessionLogicContext(t.Context(), opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	prompt := "NTM_TEST_DRY_RUN_SHOULD_NOT_SEND"
	err = runSendWithTargets(SendOptions{
		Session:      sessionName,
		Prompt:       prompt,
		PromptSource: "args",
		Targets:      SendTargets{}, // default targeting = agent panes
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("runSendWithTargets failed: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	var agentPane *tmux.Pane
	for i := range panes {
		if panes[i].Type == tmux.AgentClaude {
			agentPane = &panes[i]
			break
		}
	}
	if agentPane == nil {
		t.Fatal("Agent pane not found")
	}

	output, err := tmux.CapturePaneOutput(agentPane.ID, 30)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if strings.Contains(output, prompt) {
		t.Errorf("Dry-run should not send prompt %q, but it appeared in pane output. Got:\n%s", prompt, output)
	}
}

func TestSendDefaultTargetsAllAgentsWithoutUserPane(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir, err := os.MkdirTemp("", "ntm-test-send-no-user")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldCfg := cfg
	oldJSONOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJSONOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Checkpoints.Enabled = false
	cfg.Agents.Claude = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName := fmt.Sprintf("ntm-test-send-no-user-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
		{Type: AgentTypeClaude, Index: 2, Model: "test-model"},
	}
	if err := spawnSessionLogicContext(t.Context(), SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  2,
		UserPane: false,
	}); err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	dryRunTargets := func(skipFirst bool) SendDryRunResult {
		t.Helper()

		oldStdout := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create stdout pipe: %v", err)
		}
		os.Stdout = w
		defer func() {
			os.Stdout = oldStdout
			_ = r.Close()
		}()

		sendErr := runSendWithTargets(SendOptions{
			Session:      sessionName,
			Prompt:       "dry-run target coverage",
			PromptSource: "args",
			Targets:      SendTargets{},
			SkipFirst:    skipFirst,
			DryRun:       true,
		})
		_ = w.Close()
		os.Stdout = oldStdout

		stdoutBytes, readErr := io.ReadAll(r)
		if readErr != nil {
			t.Fatalf("failed reading stdout: %v", readErr)
		}
		if sendErr != nil {
			t.Fatalf("runSendWithTargets failed: %v (stdout=%q)", sendErr, strings.TrimSpace(string(stdoutBytes)))
		}

		var result SendDryRunResult
		if err := json.Unmarshal(stdoutBytes, &result); err != nil {
			t.Fatalf("failed to parse dry-run JSON: %v (stdout=%q)", err, strings.TrimSpace(string(stdoutBytes)))
		}
		return result
	}

	defaultResult := dryRunTargets(false)
	if defaultResult.Total != 2 || len(defaultResult.WouldSend) != 2 {
		t.Fatalf("default send targeted %d panes (%d entries), want both agents", defaultResult.Total, len(defaultResult.WouldSend))
	}

	explicitSkipResult := dryRunTargets(true)
	if explicitSkipResult.Total != 1 || len(explicitSkipResult.WouldSend) != 1 {
		t.Fatalf("explicit skip-first targeted %d panes (%d entries), want one agent", explicitSkipResult.Total, len(explicitSkipResult.WouldSend))
	}
}

func TestResolveShellSendSelectorsTopologyAware(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%3", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude},
		{ID: "%0", WindowIndex: 0, Index: 0, Type: tmux.AgentUser},
		{ID: "%2", WindowIndex: 1, Index: 0, Type: tmux.AgentCodex},
		{ID: "%1", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
	}

	ids := func(selected []tmux.Pane) []string {
		out := make([]string, 0, len(selected))
		for _, pane := range selected {
			out = append(out, pane.ID)
		}
		return out
	}

	tests := []struct {
		name      string
		selectors []string
		singular  bool
		want      []string
		wantErr   string
	}{
		{name: "exact window pane", selectors: []string{"1.1"}, singular: true, want: []string{"%3"}},
		{name: "exact pane id", selectors: []string{"%3"}, singular: true, want: []string{"%3"}},
		{name: "bare multi-window selects window", selectors: []string{"1"}, want: []string{"%2", "%3"}},
		{name: "singular bare multi-pane window is ambiguous", selectors: []string{"1"}, singular: true, wantErr: "matched 2 panes"},
		{name: "mixed aliases deduplicate", selectors: []string{"0.1", "%1", "1.0"}, want: []string{"%1", "%2"}},
		{name: "missing selector fails whole request", selectors: []string{"0.1", "9.9"}, wantErr: "not found"},
		{name: "malformed selector", selectors: []string{"1.x"}, wantErr: "invalid pane selector"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected, err := resolveShellSendSelectors(panes, test.selectors, test.singular)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("resolveShellSendSelectors error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveShellSendSelectors returned error: %v", err)
			}
			if got := ids(selected); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("resolved pane IDs = %v, want %v", got, test.want)
			}
		})
	}

	singleWindow := []tmux.Pane{
		{ID: "%11", WindowIndex: 4, Index: 0},
		{ID: "%12", WindowIndex: 4, Index: 1},
	}
	selected, err := resolveShellSendSelectors(singleWindow, []string{"1"}, true)
	if err != nil {
		t.Fatalf("single-window bare selector returned error: %v", err)
	}
	if got := ids(selected); !reflect.DeepEqual(got, []string{"%12"}) {
		t.Fatalf("single-window bare selector IDs = %v, want [%%12]", got)
	}

	entries := buildSendDryRunEntries(panes, "review", "args", true)
	wantRefs := []string{"1.1", "0.0", "1.0", "0.1"}
	for i, entry := range entries {
		if entry.Pane != wantRefs[i] || entry.PaneID != panes[i].ID {
			t.Fatalf("dry-run entry %d = pane %q id %q, want pane %q id %q", i, entry.Pane, entry.PaneID, wantRefs[i], panes[i].ID)
		}
	}
}

func TestSendDryRunTargetsRealMultiWindowSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir := t.TempDir()
	oldCfg := cfg
	oldJSONOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJSONOutput
	}()
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Checkpoints.Enabled = false
	jsonOutput = true

	sessionName := fmt.Sprintf("ntm-test-send-multi-window-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(sessionName, tmpDir); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	defer func() { _ = tmux.KillSession(sessionName) }()

	paneID, err := tmux.DefaultClient.Run("new-window", "-d", "-t", sessionName, "-c", tmpDir, "-P", "-F", "#{pane_id}", "cat")
	if err != nil {
		t.Fatalf("creating second window: %v", err)
	}
	paneID = strings.TrimSpace(paneID)
	if err := tmux.SetPaneTitle(paneID, sessionName+"__cc_1_test-model"); err != nil {
		t.Fatalf("setting second-window pane title: %v", err)
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}
	if !tmux.PanesSpanMultipleWindows(panes) {
		t.Fatalf("fixture panes do not span multiple windows: %+v", panes)
	}
	var target tmux.Pane
	found := false
	for _, pane := range panes {
		if pane.ID == paneID {
			target = pane
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("second-window pane %s not found in %+v", paneID, panes)
	}
	wantRef := fmt.Sprintf("%d.%d", target.WindowIndex, target.Index)

	dryRun := func(opts SendOptions) SendDryRunResult {
		t.Helper()
		opts.Session = sessionName
		opts.Prompt = "topology-safe dry run"
		opts.PromptSource = "args"
		opts.DryRun = true
		opts.NoHooks = true

		oldStdout := os.Stdout
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatalf("creating stdout pipe: %v", pipeErr)
		}
		os.Stdout = w
		defer func() {
			os.Stdout = oldStdout
			_ = r.Close()
		}()

		sendErr := runSendWithTargets(opts)
		_ = w.Close()
		os.Stdout = oldStdout
		output, readErr := io.ReadAll(r)
		if readErr != nil {
			t.Fatalf("reading dry-run output: %v", readErr)
		}
		if sendErr != nil {
			t.Fatalf("dry run failed: %v (stdout=%q)", sendErr, strings.TrimSpace(string(output)))
		}
		var result SendDryRunResult
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("parsing dry-run JSON: %v (stdout=%q)", err, strings.TrimSpace(string(output)))
		}
		return result
	}

	exact := dryRun(SendOptions{PaneSelector: wantRef})
	if exact.Total != 1 || len(exact.WouldSend) != 1 || exact.WouldSend[0].Pane != wantRef || exact.WouldSend[0].PaneID != paneID {
		t.Fatalf("exact selector result = %+v, want pane %s (%s)", exact, wantRef, paneID)
	}

	bareWindow := dryRun(SendOptions{PaneSelectors: []string{fmt.Sprint(target.WindowIndex)}, PanesSpecified: true})
	if bareWindow.Total != 1 || len(bareWindow.WouldSend) != 1 || bareWindow.WouldSend[0].Pane != wantRef {
		t.Fatalf("bare window selector result = %+v, want only %s", bareWindow, wantRef)
	}
}

func TestParseShellPaneSelectorsStrict(t *testing.T) {
	selectors, err := parseShellPaneSelectors("0, 1.2, %7")
	if err != nil {
		t.Fatalf("parseShellPaneSelectors returned error: %v", err)
	}
	if want := []string{"0", "1.2", "%7"}; !reflect.DeepEqual(selectors, want) {
		t.Fatalf("selectors = %v, want %v", selectors, want)
	}

	for _, raw := range []string{"all", "0,,1", "-1", "1.x", "%%7", "1.2.3"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseShellPaneSelectors(raw); err == nil {
				t.Fatalf("parseShellPaneSelectors(%q) succeeded, want error", raw)
			}
		})
	}
}

func TestSendCommandRejectsIncompatiblePaneSelectors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "pane and panes", args: []string{"session", "prompt", "--pane=1", "--panes=2"}, wantErr: "cannot use --pane and --panes together"},
		{name: "skip first and pane", args: []string{"session", "prompt", "--skip-first", "--pane=1"}, wantErr: "cannot combine --skip-first"},
		{name: "all token", args: []string{"session", "prompt", "--panes=all"}, wantErr: "invalid pane selector"},
		{name: "batch explicit pane", args: []string{"session", "--batch=batch.txt", "--pane=1"}, wantErr: "cannot combine --batch"},
		{name: "distribute explicit pane", args: []string{"session", "--distribute", "--pane=1"}, wantErr: "cannot combine --distribute"},
		{name: "distribute skip first", args: []string{"session", "--distribute", "--skip-first"}, wantErr: "cannot combine --distribute with --skip-first"},
		{name: "smart skip first", args: []string{"session", "prompt", "--smart", "--skip-first"}, wantErr: "cannot combine --skip-first with --smart"},
		{name: "codex goal plural", args: []string{"session", "goal", "--codex-goal", "--panes=1"}, wantErr: "requires exactly one --pane"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := newSendCmd()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(test.args)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("send error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

// TestSendCommandRejectsConflictingSmartRouteFlags: every combination where
// smart routing would be silently ignored or contradicted — so the prompt
// would go somewhere other than one routed agent — fails before touching
// tmux, naming the flag the operator actually typed (--smart or --route).
func TestSendCommandRejectsConflictingSmartRouteFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "route skip first", args: []string{"session", "prompt", "--route=least-loaded", "--skip-first"}, wantErr: "cannot combine --skip-first with --route"},
		{name: "route all", args: []string{"session", "prompt", "--route=least-loaded", "--all"}, wantErr: "cannot combine --all with --route"},
		{name: "smart all", args: []string{"session", "prompt", "--smart", "--all"}, wantErr: "cannot combine --all with --smart"},
		{name: "route project", args: []string{"--project=myproject", "--route=sticky"}, wantErr: "cannot combine --project with --route"},
		{name: "smart project", args: []string{"--project=myproject", "--smart"}, wantErr: "cannot combine --project with --smart"},
		{name: "route batch", args: []string{"session", "--batch=batch.txt", "--route=round-robin"}, wantErr: "cannot combine --batch with --route"},
		{name: "smart batch", args: []string{"session", "--batch=batch.txt", "--smart"}, wantErr: "cannot combine --batch with --smart"},
		{name: "route distribute", args: []string{"session", "--distribute", "--route=least-loaded"}, wantErr: "cannot combine --distribute with --route"},
		{name: "smart distribute", args: []string{"session", "--distribute", "--smart"}, wantErr: "cannot combine --distribute with --smart"},
		{name: "unknown route strategy", args: []string{"session", "prompt", "--route=bogus"}, wantErr: "invalid routing strategy: bogus"},
		{name: "unknown route lists affinity", args: []string{"session", "prompt", "--route=bogus"}, wantErr: "explicit, affinity)"},
		{name: "empty route strategy", args: []string{"session", "prompt", "--route="}, wantErr: "--route requires a strategy"},
		// Without a pane the explicit strategy used to fall back to least-loaded.
		{name: "explicit route without pane", args: []string{"session", "prompt", "--route=explicit"}, wantErr: "--route=explicit needs --pane"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := newSendCmd()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(test.args)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("send error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

// TestSendRouteFlagImpliesSmartRouting is the behavioral proof for the
// --route-without---smart bug. docs/ORCHESTRATION_FEATURES.md promises that
// `ntm send S --cc --route=least-loaded "..."` routes to ONE Claude agent,
// but the strategy was only read under --smart (default false), so the
// prompt went to every Claude pane. Driven through the real send command in
// dry-run mode, so nothing is typed into the panes.
func TestSendRouteFlagImpliesSmartRouting(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	isolateSessionAgentStorage(t)

	tmpDir := t.TempDir()
	oldCfg := cfg
	oldJSONOutput := jsonOutput
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSONOutput
	})
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Checkpoints.Enabled = false
	jsonOutput = true

	sessionName := fmt.Sprintf("ntm-test-send-route-implies-smart-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(sessionName, tmpDir); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(sessionName) })

	var claudePaneIDs []string
	for i := 1; i <= 2; i++ {
		paneID, err := tmux.DefaultClient.Run("split-window", "-d", "-t", sessionName, "-c", tmpDir, "-P", "-F", "#{pane_id}", "cat")
		if err != nil {
			t.Fatalf("creating claude pane %d: %v", i, err)
		}
		paneID = strings.TrimSpace(paneID)
		if err := tmux.SetPaneTitle(paneID, fmt.Sprintf("%s__cc_%d_test-model", sessionName, i)); err != nil {
			t.Fatalf("titling claude pane %d: %v", i, err)
		}
		claudePaneIDs = append(claudePaneIDs, paneID)
	}

	send := func(extra ...string) SendDryRunResult {
		t.Helper()
		args := append([]string{sessionName, "--cc", "--dry-run", "--no-cass-check", "--no-hooks"}, extra...)
		args = append(args, "route this prompt")

		cmd := newSendCmd()
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs(args)

		oldStdout := os.Stdout
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatalf("creating stdout pipe: %v", pipeErr)
		}
		os.Stdout = w
		sendErr := cmd.Execute()
		_ = w.Close()
		os.Stdout = oldStdout
		output, readErr := io.ReadAll(r)
		_ = r.Close()
		if readErr != nil {
			t.Fatalf("reading send output: %v", readErr)
		}
		if sendErr != nil {
			t.Fatalf("send %v failed: %v (stdout=%q)", extra, sendErr, strings.TrimSpace(string(output)))
		}
		var result SendDryRunResult
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("parsing send %v dry-run JSON: %v (stdout=%q)", extra, err, strings.TrimSpace(string(output)))
		}
		return result
	}

	// The fixture's blast radius: --cc alone addresses both Claude panes.
	if broadcast := send(); broadcast.Total != 2 || broadcast.RoutedTo != nil {
		t.Fatalf("--cc without routing = total %d routed_to %+v, want both Claude panes and no routing", broadcast.Total, broadcast.RoutedTo)
	}

	routed := send("--route=least-loaded")
	if routed.Total != 1 || len(routed.WouldSend) != 1 {
		t.Fatalf("--cc --route=least-loaded would send to %d panes (%+v), want exactly 1: --route must imply --smart", routed.Total, routed.WouldSend)
	}
	if routed.RoutedTo == nil || routed.RoutedTo.Strategy != string(robot.StrategyLeastLoaded) {
		t.Fatalf("routed_to = %+v, want a least-loaded routing decision", routed.RoutedTo)
	}
	if target := routed.WouldSend[0].PaneID; target != routed.RoutedTo.PaneID || !slices.Contains(claudePaneIDs, target) {
		t.Fatalf("routed send targets %s (routed_to %s), want one of the Claude panes %v", target, routed.RoutedTo.PaneID, claudePaneIDs)
	}

	// Agent Mail is off in this fixture, so no pane holds reservations:
	// affinity is accepted, still routes to ONE pane, and says it fell back.
	affinity := send("--route=affinity")
	if affinity.Total != 1 || affinity.RoutedTo == nil || affinity.RoutedTo.Strategy != string(robot.StrategyAffinity) {
		t.Fatalf("--route=affinity = total %d routed_to %+v, want one pane routed via affinity", affinity.Total, affinity.RoutedTo)
	}
	if !strings.Contains(affinity.RoutedTo.Reason, "fallback to least-loaded") {
		t.Fatalf("affinity routing reason = %q, want the least-loaded fallback reported", affinity.RoutedTo.Reason)
	}

	// An explicit --pane still wins over --route, exactly as it does over --smart.
	explicitID := claudePaneIDs[1]
	explicit := send("--route=least-loaded", "--pane="+explicitID)
	if explicit.Total != 1 || len(explicit.WouldSend) != 1 || explicit.WouldSend[0].PaneID != explicitID || explicit.RoutedTo != nil {
		t.Fatalf("--route with --pane=%s = %+v (routed_to %+v), want only the explicit pane and no routing", explicitID, explicit.WouldSend, explicit.RoutedTo)
	}
}

// Smart routing picks among exactly the panes the same filters would
// broadcast to. It used to honor only a single --cc/--cod/--gmi/--agy, so a
// variant (--cc=sonnet), a tag, or another agent type (--grok) could be
// routed to an agent outside the requested set.
func TestSendSmartRoutingStaysWithinVariantTagAndTypeFilters(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	isolateSessionAgentStorage(t)

	tmpDir := t.TempDir()
	oldCfg := cfg
	oldJSONOutput := jsonOutput
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSONOutput
	})
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Checkpoints.Enabled = false
	jsonOutput = true

	sessionName := fmt.Sprintf("ntm-test-send-route-filters-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(sessionName, tmpDir); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(sessionName) })

	newPane := func(title string) string {
		t.Helper()
		paneID, err := tmux.DefaultClient.Run("split-window", "-d", "-t", sessionName, "-c", tmpDir, "-P", "-F", "#{pane_id}", "cat")
		if err != nil {
			t.Fatalf("creating pane %s: %v", title, err)
		}
		paneID = strings.TrimSpace(paneID)
		if err := tmux.SetPaneTitle(paneID, sessionName+"__"+title); err != nil {
			t.Fatalf("titling pane %s: %v", title, err)
		}
		return paneID
	}
	// Created first, so every tie-break that ignores the filters lands here.
	opus := newPane("cc_1_opus")
	sonnet := newPane("cc_2_sonnet[backend]")
	grok := newPane("grok_1")

	route := func(filters ...string) SendDryRunResult {
		t.Helper()
		args := append([]string{sessionName}, filters...)
		args = append(args, "--route=least-loaded", "--dry-run", "--no-cass-check", "--no-hooks", "route this prompt")
		cmd := newSendCmd()
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs(args)
		oldStdout := os.Stdout
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatalf("creating stdout pipe: %v", pipeErr)
		}
		os.Stdout = w
		sendErr := cmd.Execute()
		_ = w.Close()
		os.Stdout = oldStdout
		output, _ := io.ReadAll(r)
		_ = r.Close()
		if sendErr != nil {
			t.Fatalf("send %v failed: %v (stdout=%q)", filters, sendErr, strings.TrimSpace(string(output)))
		}
		var result SendDryRunResult
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("parsing send %v dry-run JSON: %v (stdout=%q)", filters, err, strings.TrimSpace(string(output)))
		}
		if result.Total != 1 || len(result.WouldSend) != 1 || result.RoutedTo == nil {
			t.Fatalf("send %v = %+v, want exactly one routed pane", filters, result)
		}
		return result
	}

	if got := route("--cc=sonnet").WouldSend[0].PaneID; got != sonnet {
		t.Fatalf("--cc=sonnet routed to %s, want the sonnet pane %s (opus is %s)", got, sonnet, opus)
	}
	if got := route("--grok").WouldSend[0].PaneID; got != grok {
		t.Fatalf("--grok routed to %s, want the grok pane %s", got, grok)
	}
	if got := route("--tag=backend").WouldSend[0].PaneID; got != sonnet {
		t.Fatalf("--tag=backend routed to %s, want the tagged pane %s", got, sonnet)
	}
}

func TestSendSmartRouteIsDisabledWhenPanesSpecified(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir, err := os.MkdirTemp("", "ntm-test-send-smart-route-panes")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save/Restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Checkpoints.Enabled = false
	jsonOutput = true // avoid polluting test logs
	cfg.Agents.Claude = testAgentCatCommandTemplate

	sessionName := fmt.Sprintf("ntm-test-send-smart-route-panes-%d", time.Now().UnixNano())
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	projectDir := filepath.Join(tmpDir, sessionName)
	err = os.MkdirAll(projectDir, 0755)
	if err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
	}
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}
	err = spawnSessionLogicContext(t.Context(), opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("failed to get panes: %v", err)
	}

	var userPaneID string
	var userPaneIndex int
	for _, p := range panes {
		if p.Type == tmux.AgentUser {
			userPaneID = p.ID
			userPaneIndex = p.Index
		}
	}
	if userPaneID == "" {
		t.Fatal("User pane not found")
	}

	// If smart routing were applied, it would ignore the user pane and select an agent pane.
	// With --panes specified, we expect routing to be skipped and the command to be sent to the explicitly selected pane.
	prompt := "echo $PWD"

	// Capture only the send command's JSON output (stdout) and assert that it targeted the explicitly specified pane.
	// This avoids relying on the user shell actually executing the command.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
	}()

	sendErr := runSendWithTargets(SendOptions{
		Session:        sessionName,
		Prompt:         prompt,
		PromptSource:   "args",
		Targets:        SendTargets{},
		PanesSpecified: true,
		PaneSelectors:  []string{fmt.Sprint(userPaneIndex)},
		SmartRoute:     true,
	})

	_ = w.Close()
	os.Stdout = oldStdout

	stdoutBytes, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatalf("failed reading stdout: %v", readErr)
	}
	if sendErr != nil {
		t.Fatalf("runSendWithTargets failed: %v (stdout=%q)", sendErr, strings.TrimSpace(string(stdoutBytes)))
	}

	var res SendResult
	err = json.Unmarshal(stdoutBytes, &res)
	if err != nil {
		t.Fatalf("failed to parse send JSON: %v (stdout=%q)", err, strings.TrimSpace(string(stdoutBytes)))
	}
	if len(res.Targets) != 1 || res.Targets[0] != fmt.Sprint(userPaneIndex) {
		t.Fatalf("expected targets [%d], got %v", userPaneIndex, res.Targets)
	}
	if res.RoutedTo != nil {
		t.Fatalf("expected routed_to to be omitted when --panes is explicitly set, got %+v", *res.RoutedTo)
	}
}

// TestRemoveComments tests the comment removal helper
func TestRemoveComments(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"no comments", "no comments"},
		{"# full line comment", ""},
		{"text\n# comment\nmore text", "text\nmore text"},
		{"  # indented comment", ""},
		{"text # not a comment", "text # not a comment"},
		{"line1\nline2\nline3", "line1\nline2\nline3"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := removeComments(tt.input)
			if got != tt.want {
				t.Errorf("removeComments(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestTruncateForPreview tests the preview truncation helper
func TestTruncateForPreview(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"this is a longer string", 10, "this is..."},
		{"", 10, ""},
		{"multi\nline\ntext", 20, "multi line text"},
		{"  whitespace  ", 15, "whitespace"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncateForPreview(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForPreview(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// TestBuildTargetDescription tests the target description builder
func TestBuildTargetDescription(t *testing.T) {
	tests := []struct {
		name      string
		cc        bool
		cod       bool
		gmi       bool
		agy       bool
		all       bool
		skipFirst bool
		paneIdx   int
		want      string
	}{
		{"specific pane", false, false, false, false, false, false, 2, "pane:2"},
		{"all panes", false, false, false, false, true, false, -1, "all"},
		{"claude only", true, false, false, false, false, false, -1, "cc"},
		{"codex only", false, true, false, false, false, false, -1, "cod"},
		{"gemini only", false, false, true, false, false, false, -1, "gmi"},
		{"antigravity only", false, false, false, true, false, false, -1, "agy"},
		{"cc and cod", true, true, false, false, false, false, -1, "cc,cod"},
		{"all types", true, true, true, true, false, false, -1, "cc,cod,gmi,agy"},
		{"no filter skip first", false, false, false, false, false, true, -1, "agents"},
		{"no filter no skip", false, false, false, false, false, false, -1, "all-agents"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildTargetDescription(tt.cc, tt.cod, tt.gmi, tt.agy, tt.all, tt.skipFirst, tt.paneIdx, nil)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildObservedTargetTypes(t *testing.T) {
	panes := []tmux.Pane{
		{Type: tmux.AgentUser},
		{Type: tmux.AgentType("claude_code")},
		{Type: tmux.AgentType("openai-codex")},
		{Type: tmux.AgentCodex},
		{Type: tmux.AgentType("ws")},
		{Type: tmux.AgentOllama},
		{Type: tmux.AgentUnknown},
	}

	got := buildObservedTargetTypes(panes)
	if got != "claude,codex,windsurf,ollama" {
		t.Fatalf("buildObservedTargetTypes() = %q, want %q", got, "claude,codex,windsurf,ollama")
	}
}

func TestFilterPanesForBatch(t *testing.T) {
	// Sample panes for testing
	panes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser, Title: "user_0"},
		{Index: 1, Type: tmux.AgentClaude, Title: "cc_1", Tags: []string{"frontend"}},
		{Index: 2, Type: tmux.AgentCodex, Title: "cod_2", Tags: []string{"backend", "api"}},
		{Index: 3, Type: tmux.AgentGemini, Title: "gmi_3", Tags: []string{"docs"}},
		{Index: 4, Type: tmux.AgentClaude, Title: "cc_4", Tags: []string{"backend"}},
	}

	tests := []struct {
		name     string
		opts     SendOptions
		wantLen  int
		wantIdxs []int // expected pane indices in result
	}{
		{
			name:     "no filters excludes user pane",
			opts:     SendOptions{},
			wantLen:  4,
			wantIdxs: []int{1, 2, 3, 4},
		},
		{
			// ntm-hykz: --all is an agent broadcast; the user pane joins
			// only with an explicit --include-user.
			name:     "TargetAll excludes user pane by default",
			opts:     SendOptions{TargetAll: true},
			wantLen:  4,
			wantIdxs: []int{1, 2, 3, 4},
		},
		{
			name:     "TargetAll with IncludeUser includes everything",
			opts:     SendOptions{TargetAll: true, IncludeUser: true},
			wantLen:  5,
			wantIdxs: []int{0, 1, 2, 3, 4},
		},
		{
			name:     "TargetAll skip first uses input topology order",
			opts:     SendOptions{TargetAll: true, SkipFirst: true},
			wantLen:  4,
			wantIdxs: []int{1, 2, 3, 4},
		},
		{
			name: "filter by tag frontend",
			opts: SendOptions{
				Tags: []string{"frontend"},
			},
			wantLen:  1,
			wantIdxs: []int{1},
		},
		{
			name: "filter by tag backend (multiple matches)",
			opts: SendOptions{
				Tags: []string{"backend"},
			},
			wantLen:  2,
			wantIdxs: []int{2, 4},
		},
		{
			name: "filter by multiple tags (OR logic)",
			opts: SendOptions{
				Tags: []string{"frontend", "docs"},
			},
			wantLen:  2,
			wantIdxs: []int{1, 3},
		},
		{
			name: "filter by agent type claude",
			opts: SendOptions{
				Targets: SendTargets{{Type: AgentTypeClaude}},
			},
			wantLen:  2,
			wantIdxs: []int{1, 4},
		},
		{
			name: "filter by agent type codex",
			opts: SendOptions{
				Targets: SendTargets{{Type: AgentTypeCodex}},
			},
			wantLen:  1,
			wantIdxs: []int{2},
		},
		{
			name: "filter by agent type gemini",
			opts: SendOptions{
				Targets: SendTargets{{Type: AgentTypeGemini}},
			},
			wantLen:  1,
			wantIdxs: []int{3},
		},
		{
			name: "combined tag and type filter",
			opts: SendOptions{
				Tags:    []string{"backend"},
				Targets: SendTargets{{Type: AgentTypeClaude}},
			},
			wantLen:  1,
			wantIdxs: []int{4}, // cc_4 has backend tag
		},
		{
			name: "filter with no matches",
			opts: SendOptions{
				Tags: []string{"nonexistent"},
			},
			wantLen:  0,
			wantIdxs: []int{},
		},
		{
			name: "multiple agent types",
			opts: SendOptions{
				Targets: SendTargets{
					{Type: AgentTypeClaude},
					{Type: AgentTypeGemini},
				},
			},
			wantLen:  3,
			wantIdxs: []int{1, 3, 4},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterPanesForBatch(panes, tt.opts)

			if len(got) != tt.wantLen {
				t.Errorf("filterPanesForBatch() returned %d panes, want %d", len(got), tt.wantLen)
				return
			}

			for i, idx := range tt.wantIdxs {
				if i >= len(got) {
					t.Errorf("missing pane at position %d", i)
					continue
				}
				if got[i].Index != idx {
					t.Errorf("pane[%d].Index = %d, want %d", i, got[i].Index, idx)
				}
			}
		})
	}
}

func TestFilterPanesForBatchEmpty(t *testing.T) {
	// Test with empty panes slice
	got := filterPanesForBatch([]tmux.Pane{}, SendOptions{})
	if len(got) != 0 {
		t.Errorf("filterPanesForBatch(empty) returned %d panes, want 0", len(got))
	}
}

func TestFilterPanesForBatchAllUser(t *testing.T) {
	// Test with only user panes (should return empty without TargetAll)
	userPanes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser},
		{Index: 1, Type: tmux.AgentUser},
	}

	got := filterPanesForBatch(userPanes, SendOptions{})
	if len(got) != 0 {
		t.Errorf("filterPanesForBatch(user panes) returned %d panes, want 0", len(got))
	}

	// TargetAll alone no longer reaches user panes (ntm-hykz)...
	got = filterPanesForBatch(userPanes, SendOptions{TargetAll: true})
	if len(got) != 0 {
		t.Errorf("filterPanesForBatch(user panes, TargetAll) returned %d panes, want 0", len(got))
	}

	// ...but --include-user opts them in deliberately.
	got = filterPanesForBatch(userPanes, SendOptions{TargetAll: true, IncludeUser: true})
	if len(got) != 2 {
		t.Errorf("filterPanesForBatch(user panes, TargetAll+IncludeUser) returned %d panes, want 2", len(got))
	}
}

// --- Tests for base prompt feature (bd-3ejl) ---

func TestApplyBasePrompt(t *testing.T) {
	tests := []struct {
		name string
		base string
		user string
		want string
	}{
		{
			name: "empty base returns user unchanged",
			base: "",
			user: "do the thing",
			want: "do the thing",
		},
		{
			name: "empty user returns base",
			base: "Always run tests",
			user: "",
			want: "Always run tests",
		},
		{
			name: "both empty returns empty",
			base: "",
			user: "",
			want: "",
		},
		{
			name: "base prepended with separator",
			base: "Follow coding standards",
			user: "Implement feature X",
			want: "Follow coding standards\n\nImplement feature X",
		},
		{
			name: "multiline base",
			base: "Rule 1: run tests\nRule 2: use br",
			user: "Fix the bug",
			want: "Rule 1: run tests\nRule 2: use br\n\nFix the bug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyBasePrompt(tt.base, tt.user)
			if got != tt.want {
				t.Errorf("applyBasePrompt(%q, %q) = %q, want %q", tt.base, tt.user, got, tt.want)
			}
		})
	}
}

func TestResolveBasePrompt_FlagPriority(t *testing.T) {
	got, err := resolveBasePrompt("from flag", "", "from config", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from flag" {
		t.Errorf("expected flag value, got %q", got)
	}
}

func TestResolveBasePrompt_FlagFilePriority(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "base.txt")
	if err := os.WriteFile(path, []byte("from flag file\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := resolveBasePrompt("", path, "from config", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from flag file" {
		t.Errorf("expected flag file contents, got %q", got)
	}
}

func TestResolveBasePrompt_ConfigValue(t *testing.T) {
	got, err := resolveBasePrompt("", "", "from config", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from config" {
		t.Errorf("expected config value, got %q", got)
	}
}

func TestResolveBasePrompt_ConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "base_config.txt")
	if err := os.WriteFile(path, []byte("  from config file  \n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := resolveBasePrompt("", "", "", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from config file" {
		t.Errorf("expected trimmed config file contents, got %q", got)
	}
}

func TestResolveBasePrompt_AllEmpty(t *testing.T) {
	got, err := resolveBasePrompt("", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestResolveBasePrompt_MissingFlagFile(t *testing.T) {
	_, err := resolveBasePrompt("", "/nonexistent/base.txt", "", "")
	if err == nil {
		t.Fatal("expected error for missing flag file")
	}
	if !strings.Contains(err.Error(), "--base-prompt-file") {
		t.Errorf("error should mention --base-prompt-file, got: %v", err)
	}
}

func TestResolveBasePrompt_MissingConfigFile(t *testing.T) {
	_, err := resolveBasePrompt("", "", "", "/nonexistent/base.txt")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
	if !strings.Contains(err.Error(), "send.base_prompt_file") {
		t.Errorf("error should mention config, got: %v", err)
	}
}

// --- bd-2wzs: priority-order tests ---

func TestParsePriorityAnnotation(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"no annotation", "just text", -1},
		{"priority 0", "# priority: 0", 0},
		{"priority 4", "# priority: 4", 4},
		{"priority 2 with text", "# priority: 2\nDo something", 2},
		{"mixed comments", "# note\n# priority: 1\nwork", 1},
		{"uppercase", "# PRIORITY: 1", 1},
		{"no space after colon", "# priority:3", 3},
		{"out of range", "# priority: 9", -1},
		{"negative", "# priority: -1", -1},
		{"empty value", "# priority: ", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePriorityAnnotation(tt.text)
			if got != tt.want {
				t.Errorf("parsePriorityAnnotation(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestSortBatchByPriority(t *testing.T) {

	t.Run("sorts P0 before P2 before unset", func(t *testing.T) {
		prompts := []BatchPrompt{
			{Text: "unset", Source: "line:1", Priority: -1},
			{Text: "p2", Source: "line:2", Priority: 2},
			{Text: "p0", Source: "line:3", Priority: 0},
		}
		sortBatchByPriority(prompts)
		if prompts[0].Priority != 0 {
			t.Errorf("first should be P0, got P%d", prompts[0].Priority)
		}
		if prompts[1].Priority != 2 {
			t.Errorf("second should be P2, got P%d", prompts[1].Priority)
		}
		if prompts[2].Priority != -1 {
			t.Errorf("third should be unset, got P%d", prompts[2].Priority)
		}
	})

	t.Run("stable sort preserves order within same priority", func(t *testing.T) {
		prompts := []BatchPrompt{
			{Text: "first-p1", Source: "line:1", Priority: 1},
			{Text: "second-p1", Source: "line:2", Priority: 1},
			{Text: "third-p1", Source: "line:3", Priority: 1},
		}
		sortBatchByPriority(prompts)
		if prompts[0].Text != "first-p1" || prompts[1].Text != "second-p1" || prompts[2].Text != "third-p1" {
			t.Errorf("stable sort broken: %s, %s, %s", prompts[0].Text, prompts[1].Text, prompts[2].Text)
		}
	})

	t.Run("all unset preserves order", func(t *testing.T) {
		prompts := []BatchPrompt{
			{Text: "a", Priority: -1},
			{Text: "b", Priority: -1},
			{Text: "c", Priority: -1},
		}
		sortBatchByPriority(prompts)
		if prompts[0].Text != "a" || prompts[1].Text != "b" || prompts[2].Text != "c" {
			t.Errorf("order changed: %s, %s, %s", prompts[0].Text, prompts[1].Text, prompts[2].Text)
		}
	})

	t.Run("single element", func(t *testing.T) {
		prompts := []BatchPrompt{{Text: "only", Priority: 3}}
		sortBatchByPriority(prompts)
		if prompts[0].Text != "only" {
			t.Error("single element sort failed")
		}
	})
}

func TestParseBatchFile_PriorityAnnotations(t *testing.T) {

	t.Run("multi-line format extracts priority", func(t *testing.T) {
		content := "---\n# priority: 0\nCritical fix\n---\n# priority: 2\nMedium task\n---\nNo priority\n"
		dir := t.TempDir()
		path := dir + "/batch.txt"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		prompts, err := parseBatchFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(prompts) != 3 {
			t.Fatalf("expected 3 prompts, got %d", len(prompts))
		}
		if prompts[0].Priority != 0 {
			t.Errorf("prompt 0: want priority 0, got %d", prompts[0].Priority)
		}
		if prompts[1].Priority != 2 {
			t.Errorf("prompt 1: want priority 2, got %d", prompts[1].Priority)
		}
		if prompts[2].Priority != -1 {
			t.Errorf("prompt 2: want priority -1, got %d", prompts[2].Priority)
		}
	})

	t.Run("simple format extracts priority from preceding comment", func(t *testing.T) {
		content := "# priority: 1\nHigh priority task\nRegular task\n# priority: 3\nLow priority task\n"
		dir := t.TempDir()
		path := dir + "/batch.txt"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		prompts, err := parseBatchFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(prompts) != 3 {
			t.Fatalf("expected 3 prompts, got %d", len(prompts))
		}
		if prompts[0].Priority != 1 {
			t.Errorf("prompt 0: want priority 1, got %d", prompts[0].Priority)
		}
		if prompts[1].Priority != -1 {
			t.Errorf("prompt 1: want priority -1 (no annotation), got %d", prompts[1].Priority)
		}
		if prompts[2].Priority != 3 {
			t.Errorf("prompt 2: want priority 3, got %d", prompts[2].Priority)
		}
	})
}

func TestFilterPanesForBatchCanonicalizesAliasTypes(t *testing.T) {

	panes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser, Title: "user_0"},
		{Index: 1, Type: tmux.AgentType("openai-codex"), Title: "cod_1"},
		{Index: 2, Type: tmux.AgentType("google-gemini"), Title: "gmi_2"},
		{Index: 3, Type: tmux.AgentType("claude_code"), Title: "cc_3"},
	}

	got := filterPanesForBatch(panes, SendOptions{Targets: SendTargets{{Type: AgentTypeCodex}, {Type: AgentTypeGemini}}})
	if len(got) != 2 {
		t.Fatalf("filterPanesForBatch(alias types) len = %d, want 2", len(got))
	}
	if got[0].Index != 1 || got[1].Index != 2 {
		t.Fatalf("filterPanesForBatch(alias types) = %+v, want pane indices [1 2]", got)
	}
}

// TestSendForceNonInteractiveFlag verifies that --force-non-interactive is registered
// and parses without consuming a positional argument.
func TestSendForceNonInteractiveFlag(t *testing.T) {
	cmd := newSendCmd()
	flag := cmd.Flags().Lookup("force-non-interactive")
	if flag == nil {
		t.Fatal("--force-non-interactive flag is not registered on `ntm send`")
	}
	if flag.DefValue != "false" {
		t.Errorf("--force-non-interactive default = %q, want %q", flag.DefValue, "false")
	}
	// The flag is wrapper-friendly; the help text must call out which classes
	// are bypassed and which fail closed so callers can audit the contract.
	usage := flag.Usage
	for _, want := range []string{"CASS", "fail closed", "non_interactive_forced"} {
		if !strings.Contains(usage, want) {
			t.Errorf("--force-non-interactive usage missing %q, got: %q", want, usage)
		}
	}

	// Parse with the flag set; the prompt must still land in remaining args
	// (i.e., the flag must not swallow a positional).
	parsed := newSendCmd()
	args := []string{"my-session", "--force-non-interactive", "do the thing"}
	if err := parsed.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%v) error = %v", args, err)
	}
	remaining := parsed.Flags().Args()
	if len(remaining) < 2 || remaining[1] != "do the thing" {
		t.Errorf("remaining args = %v, want prompt to be parsed positionally", remaining)
	}
}

// TestSendResultJSONOmitsForceFieldByDefault asserts that the JSON output stays
// backwards-compatible: the new `non_interactive_forced` field has `omitempty`,
// so callers that don't set the flag never see it appear.
func TestSendResultJSONOmitsForceFieldByDefault(t *testing.T) {
	cases := []struct {
		name    string
		payload any
	}{
		{"SendResult", SendResult{Success: true, Session: "s", Targets: []string{}}},
		{"SendDryRunResult", SendDryRunResult{Success: true, DryRun: true, Session: "s"}},
		{"BatchResult", BatchResult{Success: true, Session: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if strings.Contains(string(b), "non_interactive_forced") {
				t.Errorf("default %s JSON includes the field; want omitempty: %s", tc.name, b)
			}
		})
	}
}

// TestSendResultJSONEmitsForceFieldWhenSet asserts the inverse: when callers
// pass --force-non-interactive, the JSON contract surfaces it for downstream
// auditing/log analysis.
func TestSendResultJSONEmitsForceFieldWhenSet(t *testing.T) {
	cases := []struct {
		name    string
		payload any
	}{
		{"SendResult", SendResult{Success: true, Session: "s", Targets: []string{}, NonInteractiveForced: true}},
		{"SendDryRunResult", SendDryRunResult{Success: true, DryRun: true, Session: "s", NonInteractiveForced: true}},
		{"BatchResult", BatchResult{Success: true, Session: "s", NonInteractiveForced: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if !strings.Contains(string(b), `"non_interactive_forced":true`) {
				t.Errorf("%s JSON missing non_interactive_forced=true: %s", tc.name, b)
			}
		})
	}
}

// ============================================================================
// FIX D: ntm kill orphan reap
// ============================================================================

// TestOrphanReapExcluded verifies the exclusion predicate never lets the reap
// target init, the running process, or its parent.
func TestOrphanReapExcluded(t *testing.T) {
	cases := []struct {
		pid  int
		want bool
		desc string
	}{
		{0, true, "pid 0"},
		{1, true, "init"},
		{-5, true, "negative pid"},
		{os.Getpid(), true, "self"},
		{os.Getppid(), true, "parent"},
		{1 << 30, false, "arbitrary high pid not excluded"},
	}
	for _, c := range cases {
		if got := orphanReapExcluded(c.pid); got != c.want {
			t.Errorf("orphanReapExcluded(%d) = %v, want %v (%s)", c.pid, got, c.want, c.desc)
		}
	}
}

// TestCollectPaneDescendants_RecursiveAndExclusion spawns a real two-level
// process subtree (sh -> sleep) under the test process and verifies
// collectPaneDescendants walks it recursively, while never returning self, the
// parent, init, or the supplied pane-shell PID itself.
func TestCollectPaneDescendants_RecursiveAndExclusion(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	// sh (level 1) execs a subshell that backgrounds a sleep and waits, giving
	// us a deterministic shell-with-descendant tree rooted at cmd.Process.Pid.
	cmd := exec.Command("sh", "-c", "sleep 30 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subtree: %v", err)
	}
	root := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		// Reap the immediate child to avoid a lingering zombie.
		_, _ = cmd.Process.Wait()
	})

	// Give the backgrounded sleep a moment to appear as a child of the shell.
	deadline := time.Now().Add(2 * time.Second)
	var got []int
	for time.Now().Before(deadline) {
		got = collectPaneDescendants([]int{root})
		if len(got) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(got) == 0 {
		t.Fatal("expected to collect at least one descendant of the spawned shell")
	}

	for _, pid := range got {
		if pid == root {
			t.Errorf("pane-shell PID %d must NOT be in the descendant set (the shell is reaped by kill-session)", root)
		}
		if orphanReapExcluded(pid) {
			t.Errorf("collected PID %d is excluded (self/parent/init) and must never appear", pid)
		}
		if !process.IsAlive(pid) {
			t.Errorf("collected PID %d should be alive while the subtree runs", pid)
		}
	}

	// Excluding the inputs: passing self / parent / init as pane PIDs must
	// collect nothing dangerous (their real children are not ntm agents, but
	// the predicate guarantees self/parent/init are never returned either way).
	for _, pid := range collectPaneDescendants([]int{os.Getpid(), os.Getppid(), 1, 0}) {
		if orphanReapExcluded(pid) {
			t.Errorf("collectPaneDescendants returned excluded PID %d", pid)
		}
	}
}

// --all excludes the user pane unless --include-user (ntm-hykz).
func TestFilterPanesForBatchAllExcludesUserPane(t *testing.T) {
	panes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser, Title: "proj"},
		{Index: 1, Type: tmux.AgentClaude, Title: "proj__cc_1"},
		{Index: 2, Type: tmux.AgentCodex, Title: "proj__cod_1"},
	}

	all := filterPanesForBatch(panes, SendOptions{TargetAll: true})
	for _, p := range all {
		if p.Type == tmux.AgentUser {
			t.Fatalf("--all included the user pane without --include-user: %+v", all)
		}
	}
	if len(all) != 2 {
		t.Fatalf("--all selected %d panes, want 2 agent panes", len(all))
	}

	withUser := filterPanesForBatch(panes, SendOptions{TargetAll: true, IncludeUser: true})
	if len(withUser) != 3 {
		t.Fatalf("--all --include-user selected %d panes, want all 3", len(withUser))
	}
}

func TestSendLoopModeFlag(t *testing.T) {
	cmd := newSendCmd()
	flag := cmd.Flags().Lookup("loop-mode")
	if flag == nil {
		t.Fatal("send command is missing --loop-mode")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--loop-mode default = %q, want false", flag.DefValue)
	}
}

func killResidentFixture(t *testing.T) string {
	t.Helper()
	isolateIdentityDirs(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	oldConfig, oldJSON, oldClient := cfg, jsonOutput, tmux.DefaultClient
	cfg, jsonOutput, tmux.DefaultClient = config.Default(), false, tmux.NewClient("")
	cfg.AgentMail.Enabled = false
	t.Cleanup(func() { cfg, jsonOutput, tmux.DefaultClient = oldConfig, oldJSON, oldClient })
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	t.Setenv("NTM_KILL_TEST_LOG", logPath)
	t.Setenv("NTM_KILL_TEST_CWD", dir)
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_KILL_TEST_LOG"
case "$1" in
  list-sessions) echo 'shutdown-target_NTM_SEP_1_NTM_SEP_0_NTM_SEP_today' ;;
  list-panes) echo '%91_NTM_SEP_1_NTM_SEP_shutdown-target__cc_1[work]_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0' ;;
  display-message) printf '%s\n' "$NTM_KILL_TEST_CWD" ;;
esac
`
	bin := filepath.Join(dir, "tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	return logPath
}

func TestKillWaitsForResidentShutdownBeforeSessionMutation(t *testing.T) {
	for _, surface := range []string{"plain", "response"} {
		for _, cancelStop := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%t", surface, cancelStop), func(t *testing.T) {
				logPath := killResidentFixture(t)
				oldStop := stopKillSessionMonitor
				t.Cleanup(func() { stopKillSessionMonitor = oldStop })
				entered, release := make(chan string, 1), make(chan struct{})
				stopKillSessionMonitor = func(ctx context.Context, session string) error {
					entered <- session
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-release:
						log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0600)
						if err != nil {
							return err
						}
						defer log.Close()
						_, err = fmt.Fprintln(log, "monitor-joined")
						return err
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				done, completed := make(chan error, 1), make(chan struct{})
				t.Cleanup(func() {
					cancel()
					<-completed
				})
				go func() {
					defer close(completed)
					if surface == "plain" {
						done <- runKill(ctx, io.Discard, "shutdown-target", true, nil, true, false)
						return
					}
					response, err := buildKillResponse(ctx, "shutdown-target", true, nil, true, false)
					if err == nil && (response == nil || !response.Killed || response.Session != "shutdown-target") {
						err = fmt.Errorf("invalid session kill receipt: %+v", response)
					}
					done <- err
				}()
				select {
				case session := <-entered:
					if session != "shutdown-target" {
						t.Fatalf("stop session=%q, want resolved shutdown-target", session)
					}
				case err := <-done:
					t.Fatalf("kill returned without joining resident: %v", err)
				case <-ctx.Done():
					t.Fatal("kill did not reach resident shutdown")
				}
				before, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(before), "kill-session") || strings.Contains(string(before), "kill-pane") {
					t.Fatalf("kill acted before the resident joined: %s", before)
				}
				if cancelStop {
					cancel()
				} else {
					close(release)
				}
				err = <-done
				after, readErr := os.ReadFile(logPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if cancelStop {
					if !errors.Is(err, context.Canceled) || string(after) != string(before) {
						t.Fatalf("canceled shutdown err=%v; tmux changed from %s to %s", err, before, after)
					}
					return
				}
				joined := strings.Index(string(after), "monitor-joined")
				killed := strings.Index(string(after), "kill-session -t =shutdown-target")
				if err != nil || joined < 0 || killed <= joined || !strings.Contains(string(after)[joined:], "list-panes") {
					t.Fatalf("kill must snapshot and mutate only after resident joined: err=%v calls=%s", err, after)
				}
			})
		}
	}
}

func TestKillTaggedPanesKeepsResidentRunning(t *testing.T) {
	for _, surface := range []string{"plain", "response"} {
		t.Run(surface, func(t *testing.T) {
			logPath := killResidentFixture(t)
			oldStop := stopKillSessionMonitor
			t.Cleanup(func() { stopKillSessionMonitor = oldStop })
			stopKillSessionMonitor = func(context.Context, string) error {
				t.Error("partial pane kill stopped the session resident")
				return errors.New("must keep the resident")
			}
			var err error
			if surface == "plain" {
				err = runKill(t.Context(), io.Discard, "shutdown-target", true, []string{"work"}, true, false)
			} else {
				_, err = buildKillResponse(t.Context(), "shutdown-target", true, []string{"work"}, true, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "kill-pane -t %91") || strings.Contains(string(calls), "kill-session") {
				t.Fatalf("partial kill did not keep the session: %s", calls)
			}
		})
	}
}

func TestKillRemoteSessionKeepsLocalMonitorAndProcesses(t *testing.T) {
	for _, surface := range []string{"plain", "response"} {
		t.Run(surface, func(t *testing.T) {
			logPath := killResidentFixture(t)
			dir := filepath.Dir(logPath)
			const sshScript = `#!/bin/sh
if [ "$1" != -- ] || [ "$2" != operator@remote.example ]; then exit 2; fi
printf 'remote-host %s\n' "$2" >> "$NTM_KILL_TEST_LOG"
exec /bin/sh -c "$3"
`
			if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(sshScript), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			tmux.DefaultClient = tmux.NewClient("operator@remote.example")
			oldStop, oldCollect, oldReap := stopKillSessionMonitor, collectKillPaneDescendants, reapKillOrphans
			t.Cleanup(func() {
				stopKillSessionMonitor, collectKillPaneDescendants, reapKillOrphans = oldStop, oldCollect, oldReap
			})
			stops, collections, reaps := 0, 0, 0
			stopKillSessionMonitor = func(context.Context, string) error {
				stops++
				return errors.New("same-named local resident must remain running")
			}
			collectKillPaneDescendants = func([]int) []int {
				collections++
				return []int{12345}
			}
			reapKillOrphans = func([]int) { reaps++ }
			var err error
			if surface == "plain" {
				err = runKill(t.Context(), io.Discard, "shutdown-target", true, nil, true, false)
			} else {
				_, err = buildKillResponse(t.Context(), "shutdown-target", true, nil, true, false)
			}
			if err != nil || stops != 0 || collections != 0 || reaps != 0 {
				t.Fatalf("remote kill crossed local process boundary: err=%v stops=%d collections=%d reaps=%d", err, stops, collections, reaps)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "remote-host operator@remote.example") || !strings.Contains(string(calls), "kill-session -t =shutdown-target") {
				t.Fatalf("remote session was not killed: %s", calls)
			}
		})
	}
}

// Both kill surfaces share one implementation (bd-zp9su): a local session kill
// reaps orphaned agent processes and emits the session-killed webhook event
// whether it came from the terminal or from --json. Before, the terminal kill
// emitted no events and the JSON kill reaped no orphans.
func TestKillSurfacesReapOrphansAndEmitKillEvents(t *testing.T) {
	for _, surface := range []string{"plain", "response"} {
		t.Run(surface, func(t *testing.T) {
			killResidentFixture(t)
			oldStop, oldCollect, oldReap := stopKillSessionMonitor, collectKillPaneDescendants, reapKillOrphans
			t.Cleanup(func() {
				stopKillSessionMonitor, collectKillPaneDescendants, reapKillOrphans = oldStop, oldCollect, oldReap
			})
			stopKillSessionMonitor = func(context.Context, string) error { return nil }
			collectKillPaneDescendants = func([]int) []int { return []int{4242} }
			var reaped []int
			reapKillOrphans = func(pids []int) { reaped = append(reaped, pids...) }

			killed := make(chan struct{}, 4)
			unsubscribe := events.DefaultBus.Subscribe(events.WebhookSessionKilled, func(e events.BusEvent) {
				if e.EventSession() == "shutdown-target" {
					killed <- struct{}{}
				}
			})
			defer unsubscribe()

			var err error
			if surface == "plain" {
				err = runKill(t.Context(), io.Discard, "shutdown-target", true, nil, true, false)
			} else {
				_, err = buildKillResponse(t.Context(), "shutdown-target", true, nil, true, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(reaped) != 1 || reaped[0] != 4242 {
				t.Fatalf("kill reaped %v, want the collected agent subtree [4242]", reaped)
			}
			events.DefaultEmitter().Flush(2 * time.Second)
			select {
			case <-killed:
			case <-time.After(2 * time.Second):
				t.Fatal("kill emitted no session-killed webhook event")
			}
		})
	}
}

// installFakeDCG puts a stand-in dcg first on PATH and enables the dcg
// integration under an isolated HOME/NTM_CONFIG. The stand-in blocks
// "git reset --hard" (exit 1 with a verdict), fails "git push --force" with a
// usage error (exit 2, an adapter error), and allows everything else.
func installFakeDCG(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("NTM_CONFIG", filepath.Join(root, "ntm", "config.toml"))

	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  echo "dcg 0.5.0"
  exit 0
fi
if [ "${1:-}" = "--robot" ] && [ "${2:-}" = "test" ]; then
  case "${6:-}" in
    "git reset --hard")
      echo '{"command":"git reset --hard","reason":"destroys uncommitted work"}'
      exit 1 ;;
    "git push --force")
      echo "error: unexpected argument" >&2
      exit 2 ;;
  esac
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "dcg"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake dcg: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	adapter := tools.NewDCGAdapter()
	adapter.InvalidateAvailabilityCache()
	t.Cleanup(adapter.InvalidateAvailabilityCache)

	oldCfg := cfg
	cfg = config.Default()
	cfg.Integrations.DCG.Enabled = true
	t.Cleanup(func() { cfg = oldCfg })
}

// dcgBlockedCommandCount reports the blocked_commands `ntm metrics` shows for
// the session.
func dcgBlockedCommandCount(t *testing.T, session string) int64 {
	t.Helper()
	store, collector, err := getMetricsCollector(session)
	if err != nil {
		t.Fatalf("getMetricsCollector: %v", err)
	}
	if store == nil {
		t.Fatal("state store did not open under NTM_CONFIG")
	}
	defer store.Close()
	report, err := collector.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	return report.BlockedCommands
}

// --robot-send (robot.GetSend, also behind REST sends) must apply the same dcg
// guard as `ntm send`. It used to apply only redaction, so a destructive
// command line reached non-Claude agents unchecked and uncounted (bd-bck5m).
func TestRobotSendAppliesDCGGuard(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installFakeDCG(t)

	// cat stands in for the codex CLI: it echoes delivered input without
	// executing it, and is not a bare shell dispatch would refuse.
	session := fmt.Sprintf("ntm_dcgrobot_%d", time.Now().UnixNano())
	paneID, err := tmux.DefaultClient.Run(
		"new-session", "-d", "-s", session, "-c", t.TempDir(),
		"-P", "-F", "#{pane_id}", "cat",
	)
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	paneID = strings.TrimSpace(paneID)
	t.Cleanup(func() { _ = tmux.KillSession(session) })
	if _, err := tmux.DefaultClient.Run("select-pane", "-t", paneID, "-T", session+"__cod_1"); err != nil {
		t.Fatalf("title pane as codex: %v", err)
	}

	blocked, err := robot.GetSend(robot.SendOptions{Session: session, Message: "git reset --hard", Pane: paneID})
	if err != nil {
		t.Fatalf("GetSend: %v", err)
	}
	if blocked.Success || blocked.ErrorCode != robot.ErrCodeDestructiveCommandBlocked || !blocked.Blocked {
		t.Fatalf("send = success %v code %q blocked %v error %q, want the dcg block",
			blocked.Success, blocked.ErrorCode, blocked.Blocked, blocked.Error)
	}
	if !strings.Contains(blocked.Error, "destroys uncommitted work") || len(blocked.Successful) != 0 {
		t.Fatalf("blocked send error %q successful %v, want the dcg reason and no delivery", blocked.Error, blocked.Successful)
	}
	if got := dcgBlockedCommandCount(t, session); got != 1 {
		t.Fatalf("BlockedCommands = %d, want the one robot-blocked send", got)
	}

	// --robot-send --track resolves and dispatches on its own, so it must
	// refuse the same payload rather than deliver it and wait for an ack.
	tracked, err := robot.GetSendAndAck(robot.SendAndAckOptions{
		SendOptions:  robot.SendOptions{Session: session, Message: "git reset --hard", Pane: paneID},
		AckTimeoutMs: 200,
	})
	if err != nil {
		t.Fatalf("GetSendAndAck: %v", err)
	}
	if tracked.Success || tracked.ErrorCode != robot.ErrCodeDestructiveCommandBlocked ||
		tracked.Send.ErrorCode != robot.ErrCodeDestructiveCommandBlocked || len(tracked.Send.Successful) != 0 {
		t.Fatalf("tracked send = success %v code %q send code %q successful %v, want the dcg block",
			tracked.Success, tracked.ErrorCode, tracked.Send.ErrorCode, tracked.Send.Successful)
	}
	if got := dcgBlockedCommandCount(t, session); got != 2 {
		t.Fatalf("BlockedCommands = %d, want both robot-blocked sends", got)
	}

	failed, err := robot.GetSend(robot.SendOptions{Session: session, Message: "git push --force", Pane: paneID})
	if err != nil {
		t.Fatalf("GetSend: %v", err)
	}
	if failed.Success || failed.ErrorCode != robot.ErrCodeInternalError || failed.Blocked || len(failed.Successful) != 0 {
		t.Fatalf("send = success %v code %q blocked %v successful %v, want a fail-closed check error",
			failed.Success, failed.ErrorCode, failed.Blocked, failed.Successful)
	}

	allowed, err := robot.GetSend(robot.SendOptions{Session: session, Message: "git status", Pane: paneID})
	if err != nil {
		t.Fatalf("GetSend: %v", err)
	}
	if allowed.ErrorCode == robot.ErrCodeDestructiveCommandBlocked || len(allowed.Successful) != 1 {
		t.Fatalf("allowed send = code %q error %q successful %v failed %+v, want delivery",
			allowed.ErrorCode, allowed.Error, allowed.Successful, allowed.Failed)
	}

	var output string
	deadline := time.Now().Add(5 * time.Second)
	for {
		output, err = tmux.CapturePaneOutput(paneID, 50)
		if err == nil && strings.Contains(output, "git status") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("allowed send never rendered in the pane: output=%q err=%v", output, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if strings.Contains(output, "git reset") || strings.Contains(output, "git push") {
		t.Fatalf("a refused command reached the pane:\n%s", output)
	}

	// An --interrupt-msg follow-up is refused before any Ctrl+C is sent.
	interrupted, err := robot.GetInterrupt(robot.InterruptOptions{
		Session: session, Message: "git reset --hard", Force: true, NoWait: true,
	})
	if err != nil {
		t.Fatalf("GetInterrupt: %v", err)
	}
	if interrupted.Success || interrupted.ErrorCode != robot.ErrCodeDestructiveCommandBlocked ||
		len(interrupted.Interrupted) != 0 || interrupted.MessageSent {
		t.Fatalf("interrupt = success %v code %q interrupted %v message_sent %v, want the dcg block before Ctrl+C",
			interrupted.Success, interrupted.ErrorCode, interrupted.Interrupted, interrupted.MessageSent)
	}
	if got := dcgBlockedCommandCount(t, session); got != 3 {
		t.Fatalf("BlockedCommands = %d, want all three robot-blocked messages", got)
	}
}

// A dcg-blocked send must count toward the session's destructive_cmd_incidents
// in `ntm metrics`. The metric's only feed used to be an event-bus type nothing
// published, so it reported zero blocked commands and a met target forever.
func TestMaybeBlockSendWithDCGRecordsBlockedCommandMetric(t *testing.T) {
	installFakeDCG(t)

	panes := []tmux.Pane{{ID: "%2", Index: 2, Title: "dcgmetric__cod_1", Type: tmux.AgentCodex}}
	err := maybeBlockSendWithDCG(context.Background(), "git reset --hard", "dcgmetric", panes)
	if !errors.Is(err, robot.ErrSendCommandBlocked) || !strings.Contains(err.Error(), "blocked by dcg: destroys uncommitted work") {
		t.Fatalf("maybeBlockSendWithDCG error = %v, want the dcg block", err)
	}

	store, collector, err := getMetricsCollector("dcgmetric")
	if err != nil {
		t.Fatalf("getMetricsCollector: %v", err)
	}
	if store == nil {
		t.Fatal("state store did not open under NTM_CONFIG")
	}
	defer store.Close()
	report, err := collector.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if report.BlockedCommands != 1 {
		t.Fatalf("BlockedCommands = %d, want the one dcg-blocked send", report.BlockedCommands)
	}
	for _, target := range report.TargetComparison {
		if target.Metric == "destructive_cmd_incidents" && target.Status == "met" {
			t.Fatalf("destructive_cmd_incidents reported %q with a blocked command recorded", target.Status)
		}
	}
}

// installFakeBrShow puts a `br` on PATH that answers `br show <beadID> --json`
// with rowJSON the way beads_rust does (after any --lock-timeout/--no-db
// prefix the bv client adds) and fails for any other bead. Every invocation is
// logged as "<working dir>|<args>".
func installFakeBrShow(t *testing.T, beadID, rowJSON string) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "br-calls.log")
	rowPath := filepath.Join(binDir, "bead.json")
	if err := os.WriteFile(rowPath, []byte(rowJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s|%%s\n' "$PWD" "$*" >> '%s'
while [ $# -gt 0 ]; do
  case "$1" in
    --lock-timeout) shift 2 ;;
    --no-db) shift ;;
    *) break ;;
  esac
done
if [ "$1" = show ] && [ "$2" = '%s' ] && [ "$3" = --json ]; then
  cat '%s'
  exit 0
fi
echo "Issue not found: $2" >&2
exit 1
`, logPath, beadID, rowPath)
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// waitForLiveAgentPanes waits until the session shows want agent panes whose
// agent command (not a bare shell) is in the foreground, so delivery is not
// refused as PANE_AGENT_DEAD while the fake agents are still starting.
func waitForLiveAgentPanes(t *testing.T, session string, want int) []tmux.Pane {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var agents []tmux.Pane
	for time.Now().Before(deadline) {
		panes, err := tmux.GetPanes(session)
		if err == nil {
			agents = agents[:0]
			live := true
			for _, pane := range panes {
				if pane.Type == tmux.AgentUser {
					continue
				}
				agents = append(agents, pane)
				if strings.TrimSpace(pane.Command) == "" || pane.AgentCLIDead() {
					live = false
				}
			}
			if live && len(agents) == want {
				time.Sleep(300 * time.Millisecond)
				return agents
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("session %s did not reach %d live agent panes: %+v", session, want, agents)
	return nil
}

// TestSendTemplateRendersPerPaneAgentAndBeadContext drives `ntm send
// --template` end to end. The bug: the template was rendered once, before the
// fan-out, with no agent or bead context, so every pane received the literal
// "You are Agent #{{agent_num}} ({{agent_type}})" and "br update {{bead_id}}".
func TestSendTemplateRendersPerPaneAgentAndBeadContext(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg-config"))
	oldCfg, oldJSONOutput := cfg, jsonOutput
	t.Cleanup(func() { cfg, jsonOutput = oldCfg, oldJSONOutput })
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Checkpoints.Enabled = false
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	const beadID = "bd-tmpl7"
	brLog := installFakeBrShow(t, beadID,
		`[{"id":"bd-tmpl7","title":"Wire template context","description":"Render the template once per pane.","status":"open","priority":1,"issue_type":"bug"}]`)

	sessionName := fmt.Sprintf("ntm-test-send-template-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = tmux.KillSession(sessionName) })
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := spawnSessionLogicContext(t.Context(), SpawnOptions{
		Session: sessionName,
		Agents: []FlatAgent{
			{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
			{Type: AgentTypeClaude, Index: 2, Model: "test-model"},
			{Type: AgentTypeCodex, Index: 1, Model: "test-model"},
		},
		CCCount:  2,
		CodCount: 1,
		UserPane: true,
	}); err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}
	agents := waitForLiveAgentPanes(t, sessionName, 3)
	byID := make(map[string]tmux.Pane, len(agents))
	for _, pane := range agents {
		byID[pane.ID] = pane
	}

	identity := func(pane tmux.Pane) string {
		return fmt.Sprintf("You are Agent #%d (%s)", pane.NTMIndex, robot.ResolveAgentType(string(pane.Type)))
	}
	identities := make(map[string]bool, len(agents))
	for _, pane := range agents {
		identities[identity(pane)] = true
	}
	wantIdentities := map[string]bool{
		"You are Agent #1 (claude)": true,
		"You are Agent #2 (claude)": true,
		"You are Agent #1 (codex)":  true,
	}
	if !reflect.DeepEqual(identities, wantIdentities) {
		t.Fatalf("fixture pane identities = %v, want %v (%+v)", identities, wantIdentities, agents)
	}
	// assertOwnRendering checks text carries pane's identity, none of the
	// other panes', the bead context, and no unfilled placeholder.
	assertOwnRendering := func(t *testing.T, pane tmux.Pane, text string) {
		t.Helper()
		for other := range identities {
			if strings.Contains(text, other) != (other == identity(pane)) {
				t.Fatalf("pane %s (%s) rendering has wrong identity (want only %q):\n%s", pane.ID, pane.Title, identity(pane), text)
			}
		}
		for _, want := range []string{
			"# Marching Orders: bd-tmpl7",
			"**Wire template context**",
			"**Priority:** P1",
			"Render the template once per pane.",
			"br update bd-tmpl7 --status closed",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("pane %s rendering lacks %q:\n%s", pane.ID, want, text)
			}
		}
		if strings.Contains(text, "{{") {
			t.Fatalf("pane %s rendering kept a literal placeholder:\n%s", pane.ID, text)
		}
	}
	runSend := func(t *testing.T, extra ...string) (string, error) {
		t.Helper()
		cmd := newSendCmd()
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{sessionName, "--template", "marching_orders", "--no-cass-check", "--no-hooks"}, extra...))
		return captureStdout(t, func() error { return cmd.ExecuteContext(t.Context()) })
	}
	assertNothingDelivered := func(t *testing.T) {
		t.Helper()
		for _, pane := range agents {
			output, err := tmux.CapturePaneOutput(pane.ID, 200)
			if err != nil {
				t.Fatalf("CapturePaneOutput(%s): %v", pane.ID, err)
			}
			if strings.Contains(output, "Marching Orders") {
				t.Fatalf("pane %s received a prompt from a refused send:\n%s", pane.ID, output)
			}
		}
	}

	t.Run("dry run previews each pane's own rendering", func(t *testing.T) {
		stdout, err := runSend(t, "--bead", beadID, "--dry-run")
		if err != nil {
			t.Fatalf("send --dry-run failed: %v (stdout=%s)", err, stdout)
		}
		var result SendDryRunResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("parse dry-run JSON: %v (stdout=%s)", err, stdout)
		}
		if !result.Success || result.Total != 3 || len(result.WouldSend) != 3 {
			t.Fatalf("dry run = %+v, want the three agent panes", result)
		}
		for _, entry := range result.WouldSend {
			pane, ok := byID[entry.PaneID]
			if !ok {
				t.Fatalf("dry run targeted unexpected pane %+v", entry)
			}
			assertOwnRendering(t, pane, entry.Prompt)
			if entry.Source != "template:marching_orders" {
				t.Fatalf("dry-run source = %q", entry.Source)
			}
		}
		assertNothingDelivered(t)
	})

	t.Run("dry run numbers each pane in send order", func(t *testing.T) {
		cmd := newSendCmd()
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{sessionName, "-t", "batch_assign", "--bead", beadID, "--dry-run", "--no-cass-check", "--no-hooks"})
		stdout, err := captureStdout(t, func() error { return cmd.ExecuteContext(t.Context()) })
		if err != nil {
			t.Fatalf("batch_assign dry run failed: %v (stdout=%s)", err, stdout)
		}
		var result SendDryRunResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("parse dry-run JSON: %v (stdout=%s)", err, stdout)
		}
		if len(result.WouldSend) != 3 {
			t.Fatalf("dry run = %+v, want three panes", result)
		}
		for i, entry := range result.WouldSend {
			pane := byID[entry.PaneID]
			wantHeader := fmt.Sprintf("# Assignment %d/3: bd-tmpl7\n\nAgent #%d, you have been assigned: **Wire template context**", i+1, pane.NTMIndex)
			if !strings.HasPrefix(entry.Prompt, wantHeader) || !strings.Contains(entry.Prompt, "This is one of 3 tasks") ||
				!strings.Contains(entry.Prompt, "`br update bd-tmpl7 --status in_progress`") || strings.Contains(entry.Prompt, "{{") {
				t.Fatalf("entry %d (pane %s) prompt does not start with %q:\n%s", i, entry.PaneID, wantHeader, entry.Prompt)
			}
		}
	})

	t.Run("missing bead context is refused before any pane", func(t *testing.T) {
		stdout, err := runSend(t)
		if err == nil {
			t.Fatalf("send without --bead succeeded: %s", stdout)
		}
		var result SendResult
		if jsonErr := json.Unmarshal([]byte(stdout), &result); jsonErr != nil {
			t.Fatalf("parse failure JSON: %v (stdout=%s)", jsonErr, stdout)
		}
		if result.Success || result.ErrorCode != robot.ErrCodeInvalidFlag ||
			!strings.Contains(result.Error, `template "marching_orders" has unresolved variable(s): bead_id, bead_title`) ||
			!strings.Contains(result.Error, "--bead") {
			t.Fatalf("failure envelope = %+v, want unresolved bead variables with a --bead hint", result)
		}
		assertNothingDelivered(t)
	})

	t.Run("a pane the template cannot be filled for refuses the whole send", func(t *testing.T) {
		stdout, err := runSend(t, "--bead", beadID, "--all", "--include-user")
		if err == nil {
			t.Fatalf("send to the user pane succeeded: %s", stdout)
		}
		var result SendResult
		if jsonErr := json.Unmarshal([]byte(stdout), &result); jsonErr != nil {
			t.Fatalf("parse failure JSON: %v (stdout=%s)", jsonErr, stdout)
		}
		if result.Success || result.Delivered != 0 || result.ErrorCode != robot.ErrCodeInvalidFlag ||
			!strings.Contains(result.Error, "rendering prompt for pane") ||
			!strings.Contains(result.Error, "unresolved variable(s): agent_num") ||
			!strings.Contains(result.Error, "carries no ntm agent number") {
			t.Fatalf("failure envelope = %+v, want the user pane's missing agent number", result)
		}
		assertNothingDelivered(t)
	})

	t.Run("delivery gives each pane its own identity", func(t *testing.T) {
		stdout, err := runSend(t, "--bead", beadID)
		if err != nil {
			t.Fatalf("send failed: %v (stdout=%s)", err, stdout)
		}
		var result SendResult
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("parse send JSON: %v (stdout=%s)", err, stdout)
		}
		if !result.Success || result.Delivered != 3 || result.Failed != 0 || strings.Contains(result.PromptPreview, "{{") {
			t.Fatalf("send result = %+v, want three deliveries and a filled preview", result)
		}

		for _, pane := range agents {
			var output string
			deadline := time.Now().Add(5 * time.Second)
			for {
				output, err = tmux.CapturePaneOutput(pane.ID, 200)
				if err != nil {
					t.Fatalf("CapturePaneOutput(%s): %v", pane.ID, err)
				}
				if strings.Contains(output, "br update bd-tmpl7 --add-note") || time.Now().After(deadline) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			assertOwnRendering(t, pane, output)
		}

		// History records what each pane actually received, one entry per
		// distinct rendering, instead of one entry with placeholders.
		entries, err := history.ReadForSession(sessionName)
		if err != nil {
			t.Fatalf("history.ReadForSession: %v", err)
		}
		recorded := 0
		for _, entry := range entries {
			if entry.Template != "marching_orders" || !entry.Success {
				continue
			}
			recorded++
			if len(entry.Targets) != 1 {
				t.Fatalf("history entry targets = %v, want one pane per distinct rendering", entry.Targets)
			}
			matched := 0
			for want := range identities {
				if strings.Contains(entry.Prompt, want) {
					matched++
				}
			}
			if matched != 1 || strings.Contains(entry.Prompt, "{{") {
				t.Fatalf("history entry prompt is not one pane's rendering:\n%s", entry.Prompt)
			}
		}
		if recorded != 3 {
			t.Fatalf("history recorded %d successful template entries, want 3", recorded)
		}

		// The bead was read from the session's project, never the caller's cwd.
		calls, err := os.ReadFile(brLog)
		if err != nil {
			t.Fatalf("read fake br log: %v", err)
		}
		wantDir, err := filepath.EvalSymlinks(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
		if len(lines) == 0 || lines[0] == "" {
			t.Fatal("fake br was never invoked")
		}
		for _, line := range lines {
			dir, args, _ := strings.Cut(line, "|")
			gotDir, err := filepath.EvalSymlinks(dir)
			if err != nil || gotDir != wantDir || !strings.Contains(args, "show bd-tmpl7 --json") {
				t.Fatalf("br invocation %q, want `show bd-tmpl7 --json` in %s", line, wantDir)
			}
		}
	})
}

func TestRenderSendTargetPromptsComposesAndRedactsEachPane(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Default()
	cfg.Redaction.Mode = string(redaction.ModeRedact)

	panes := []tmux.Pane{
		{ID: "%4", Index: 1, NTMIndex: 1, Type: tmux.AgentClaude},
		{ID: "%5", Index: 2, NTMIndex: 1, Type: tmux.AgentCodex},
	}
	var calls []string
	opts := SendOptions{
		BasePrompt: "Follow AGENTS.md",
		promptForTarget: func(pane tmux.Pane, index, total int) (string, error) {
			calls = append(calls, fmt.Sprintf("%s:%d/%d", pane.ID, index, total))
			return fmt.Sprintf("pane %s uses password=hunter2hunter2", pane.ID), nil
		},
	}
	prompts, err := renderSendTargetPrompts(opts, panes, false)
	if err != nil {
		t.Fatalf("renderSendTargetPrompts: %v", err)
	}
	if want := []string{"%4:0/2", "%5:1/2"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("renderer calls = %v, want send-order index/total %v", calls, want)
	}
	for i, prompt := range prompts {
		if !strings.HasPrefix(prompt, "Follow AGENTS.md\n\npane "+panes[i].ID+" uses ") ||
			strings.Contains(prompt, "hunter2hunter2") || !strings.Contains(prompt, "[REDACTED:PASSWORD:") {
			t.Fatalf("prompt %d = %q, want base prompt first and the secret redacted", i, prompt)
		}
	}

	if prompts, err := renderSendTargetPrompts(SendOptions{}, panes, false); err != nil || prompts != nil {
		t.Fatalf("plain send rendered per-target prompts %v, %v; want nil", prompts, err)
	}

	refusal := errors.New("unresolved agent_num")
	opts.promptForTarget = func(pane tmux.Pane, _, _ int) (string, error) {
		if pane.ID == "%5" {
			return "", refusal
		}
		return "ok", nil
	}
	if _, err := renderSendTargetPrompts(opts, panes, false); !errors.Is(err, refusal) || !strings.Contains(err.Error(), "rendering prompt for pane 2") {
		t.Fatalf("render failure = %v, want it wrapped with the pane address", err)
	}
}

func TestShellDispatchServiceSendsEachTargetItsOwnMessage(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Default()

	panes := []tmux.Pane{
		{ID: "%71", WindowIndex: 0, Index: 1, NTMIndex: 1, Type: tmux.AgentClaude},
		{ID: "%72", WindowIndex: 0, Index: 2, NTMIndex: 2, Type: tmux.AgentClaude},
	}
	messages := sendTargetMessages(panes, []string{"You are Agent #1", "You are Agent #2"})
	stop := errors.New("captured")
	var got map[string]string
	service, err := newShellDispatchServiceWithGate("proj", panes, messages, activeShellDispatchRedactionConfig(),
		func(_ context.Context, _ dispatchsvc.Request, deliveries []dispatchsvc.Delivery) error {
			got = make(map[string]string, len(deliveries))
			for _, delivery := range deliveries {
				got[delivery.Target.Pane.ID] = delivery.Message
			}
			return stop
		})
	if err != nil {
		t.Fatalf("newShellDispatchServiceWithGate: %v", err)
	}
	if _, err := service.Execute(t.Context(), shellDispatchRequest("proj", panes, panes, "shared {{agent_num}}", true)); !errors.Is(err, stop) {
		t.Fatalf("Execute error = %v, want the capturing gate's refusal", err)
	}
	if want := map[string]string{"%71": "You are Agent #1", "%72": "You are Agent #2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("final messages = %v, want each pane's own rendering %v", got, want)
	}

	// A per-target map that misses a planned pane fails preflight instead of
	// falling back to the shared (unrendered) message.
	partial := sendTargetMessages(panes[:1], []string{"You are Agent #1"})
	service, err = newShellDispatchServiceWithGate("proj", panes, partial, activeShellDispatchRedactionConfig(), nil)
	if err != nil {
		t.Fatalf("newShellDispatchServiceWithGate: %v", err)
	}
	_, err = service.Prepare(t.Context(), shellDispatchRequest("proj", panes, panes, "shared {{agent_num}}", true))
	var dispatchErr *dispatchsvc.Error
	if !errors.As(err, &dispatchErr) || dispatchErr.Code != dispatchsvc.ErrMessageBuild || !strings.Contains(err.Error(), "no rendered prompt for pane 2") {
		t.Fatalf("Prepare with a missing per-target message = %v, want a message-build failure", err)
	}
}

func TestGroupSendPromptsAndDeliveryCounts(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Type: tmux.AgentClaude},
		{ID: "%3", Index: 3, Type: tmux.AgentCodex},
	}
	shared := groupSendPrompts("same", nil, panes, false)
	if len(shared) != 1 || shared[0].prompt != "same" || !reflect.DeepEqual(shared[0].targets, []string{"1", "2", "3"}) {
		t.Fatalf("plain send groups = %+v, want one group with every target", shared)
	}

	groups := groupSendPrompts("shared", []string{"A", "B", "A"}, panes, false)
	if len(groups) != 2 || groups[0].prompt != "A" || !reflect.DeepEqual(groups[0].targets, []string{"1", "3"}) ||
		!reflect.DeepEqual(groups[0].agentTypes, []string{string(tmux.AgentClaude), string(tmux.AgentCodex)}) ||
		groups[1].prompt != "B" || !reflect.DeepEqual(groups[1].targets, []string{"2"}) {
		t.Fatalf("per-pane groups = %+v, want A:[1 3] B:[2] in send order", groups)
	}

	countSendGroupDeliveries(groups, []dispatchsvc.Receipt{
		{Target: dispatchsvc.Target{Ref: panes[0].Ref()}, Status: dispatchsvc.ReceiptDelivered},
		{Target: dispatchsvc.Target{Ref: panes[1].Ref()}, Status: dispatchsvc.ReceiptFailed},
		{Target: dispatchsvc.Target{Ref: panes[2].Ref()}, Status: dispatchsvc.ReceiptDelivered},
	})
	if groups[0].delivered != 2 || groups[1].delivered != 0 {
		t.Fatalf("group deliveries = %d/%d, want 2/0", groups[0].delivered, groups[1].delivered)
	}
	if got := groupSendPrompts("x", nil, nil, false); len(got) != 0 {
		t.Fatalf("no panes produced groups %+v", got)
	}
}

func TestSendCommandRejectsMisusedTemplateFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "bead without template", args: []string{"session", "prompt", "--bead", "bd-1"}, wantErr: "--bead only fills template variables; use it with --template"},
		{name: "empty bead", args: []string{"session", "-t", "marching_orders", "--bead", "  "}, wantErr: "--bead requires a bead ID"},
		{name: "template with batch", args: []string{"session", "-t", "marching_orders", "--batch", "prompts.txt"}, wantErr: "cannot combine --template with --batch"},
		{name: "template with distribute", args: []string{"session", "-t", "marching_orders", "--distribute"}, wantErr: "cannot combine --template with --distribute"},
		{name: "template with project", args: []string{"-t", "marching_orders", "--project", "proj"}, wantErr: "cannot combine --template with --project"},
		{name: "template with codex goal", args: []string{"session", "-t", "marching_orders", "--codex-goal", "--pane=1"}, wantErr: "cannot combine --template with --codex-goal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := newSendCmd()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(test.args)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("send error = %v, want substring %q", err, test.wantErr)
			}
			if !errors.Is(err, errCLIInvalidInput) {
				t.Fatalf("send error = %v, want it classified as invalid CLI input", err)
			}
		})
	}
}

func TestShellDispatchRefusesCodexTranscriptBeforePrompt(t *testing.T) {
	for _, clearInput := range []bool{false, true} {
		t.Run(fmt.Sprintf("clear-input=%v", clearInput), func(t *testing.T) {
			projectRoot, logPath := workflowCommandTmuxFixture(t, "stable")
			const capture = "› an earlier user prompt\nBrowsing transcript · ↑↓/jk scroll · ←→/hl prompts · ⌃t details · ↵ rewind · esc back\n"
			if err := os.WriteFile(filepath.Join(projectRoot, "pane-output"), []byte(capture), 0600); err != nil {
				t.Fatal(err)
			}
			panes := []tmux.Pane{{ID: "%91", Type: tmux.AgentCodex, Command: "codex", Width: 120}}
			service, err := newShellDispatchService("proj", panes, activeShellDispatchRedactionConfig())
			if err != nil {
				t.Fatal(err)
			}
			const message = "never submit this into transcript history"
			request := shellDispatchRequest("proj", panes, panes, message, true)
			request.ClearInput = clearInput
			result, err := service.Execute(t.Context(), request)
			wantError := "Enter would rewind"
			wantEscapes := 0
			if clearInput {
				wantError = "COMPOSER_NOT_CLEARED"
				wantEscapes = 1
			}
			if err == nil || !strings.Contains(err.Error(), wantError) || result.Success || result.Delivered != 0 || result.Failed != 1 {
				t.Fatalf("shell send accepted transcript browsing: result=%+v error=%v", result, err)
			}
			commands, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(commands)
			if strings.Count(calls, " Escape\n") != wantEscapes {
				t.Fatalf("unexpected modal dismissal keys:\n%s", calls)
			}
			for _, forbidden := range []string{message, " Enter\n", " C-u\n", " C-e\n", " BSpace\n", "load-buffer", "paste-buffer"} {
				if strings.Contains(calls, forbidden) {
					t.Fatalf("shell send actuated %q while history owned the keyboard:\n%s", forbidden, calls)
				}
			}
		})
	}
}
