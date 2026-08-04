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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	sessionPkg "github.com/Dicklesworthstone/ntm/internal/session"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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

func TestValidateDistributeBeadDetailsFailsClosedOnEveryAutomationGate(t *testing.T) {
	now := time.Now().UTC()
	recommendation := robot.DistributeRecommendation{BeadID: "ntm-work", Title: "Stale title"}
	validDetails := func() *bv.BeadAssignmentDetails {
		return &bv.BeadAssignmentDetails{ID: "ntm-work", Title: "Live title", Status: "open"}
	}
	if err := validateDistributeBeadDetails(recommendation, validDetails(), now); err != nil {
		t.Fatalf("valid live details rejected: %v", err)
	}
	past := validDetails()
	pastDefer := now.Add(-time.Minute)
	past.DeferUntil = &pastDefer
	if err := validateDistributeBeadDetails(recommendation, past, now); err != nil {
		t.Fatalf("expired defer gate rejected: %v", err)
	}

	tests := []struct {
		name    string
		rec     robot.DistributeRecommendation
		details *bv.BeadAssignmentDetails
		want    string
	}{
		{name: "missing recommendation ID", rec: robot.DistributeRecommendation{}, details: validDetails(), want: "no bead ID"},
		{name: "missing details", rec: recommendation, details: nil, want: "no live assignment details"},
		{name: "mismatched ID", rec: recommendation, details: &bv.BeadAssignmentDetails{ID: "other", Status: "open"}, want: "does not match"},
		{name: "blocked", rec: recommendation, details: &bv.BeadAssignmentDetails{ID: "ntm-work", Status: "open", BlockedBy: []string{"ntm-dependency"}}, want: "blocked by dependencies"},
		{name: "operator gated", rec: recommendation, details: &bv.BeadAssignmentDetails{ID: "ntm-work", Status: "open", Labels: []string{" HUMAN-INPUT "}}, want: "operator-gated"},
		{name: "not open", rec: recommendation, details: &bv.BeadAssignmentDetails{ID: "ntm-work", Status: "in_progress"}, want: "want open"},
		{name: "assigned", rec: recommendation, details: &bv.BeadAssignmentDetails{ID: "ntm-work", Status: "open", Assignee: "agent-7"}, want: "already assigned"},
		{name: "deferred", rec: recommendation, details: func() *bv.BeadAssignmentDetails {
			details := validDetails()
			deferUntil := now.Add(time.Hour)
			details.DeferUntil = &deferUntil
			return details
		}(), want: "deferred until"},
		{name: "pinned", rec: recommendation, details: &bv.BeadAssignmentDetails{ID: "ntm-work", Status: "open", Pinned: true}, want: "pinned"},
		{name: "ephemeral", rec: recommendation, details: &bv.BeadAssignmentDetails{ID: "ntm-work", Status: "open", Ephemeral: true}, want: "ephemeral"},
		{name: "template", rec: recommendation, details: &bv.BeadAssignmentDetails{ID: "ntm-work", Status: "open", Template: true}, want: "template"},
		{name: "wisp", rec: recommendation, details: &bv.BeadAssignmentDetails{ID: "ntm-work", Status: "open", Wisp: true}, want: "wisp"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDistributeBeadDetails(test.rec, test.details, now)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want substring %q", err, test.want)
			}
		})
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

func TestDistributeDispatchGateRequiresFreshConfidentIdleObservation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC()
	original := newAssignSessionObserver
	t.Cleanup(func() { newAssignSessionObserver = original })
	setObservation := func(state statuspkg.AgentState, freshness statuspkg.ObservationFreshness, confidence float64, observedAt time.Time, observationErr string) {
		newAssignSessionObserver = func() assignSessionObserver {
			return fixedAssignSessionObserver{observation: statuspkg.SessionObservation{
				Session:    "proj",
				ObservedAt: observedAt,
				Panes: []statuspkg.PaneObservation{{
					Pane: tmux.PaneRef{ID: "%91", WindowIndex: 1, PaneIndex: 1},
					Current: statuspkg.StateObservation{
						Status: statuspkg.AgentStatus{State: state}, ObservedAt: observedAt,
						Freshness: freshness, Confidence: confidence, Error: observationErr,
					},
				}},
			}}
		}
	}
	delivery := dispatchsvc.Delivery{Target: dispatchsvc.Target{
		Pane: tmux.Pane{ID: "%91", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude},
		Ref:  tmux.PaneRef{ID: "%91", WindowIndex: 1, PaneIndex: 1}, Address: "1.1",
	}}
	gate := distributeDispatchGate("proj")

	setObservation(statuspkg.StateIdle, statuspkg.FreshnessFresh, 0.99, now, "")
	if err := gate(t.Context(), dispatchsvc.Request{}, []dispatchsvc.Delivery{delivery}); err != nil {
		t.Fatalf("fresh idle gate: %v", err)
	}

	for name, setup := range map[string]func(){
		"busy": func() { setObservation(statuspkg.StateWorking, statuspkg.FreshnessFresh, 0.99, now, "") },
		"stale": func() {
			setObservation(statuspkg.StateIdle, statuspkg.FreshnessStale, 0.99, now.Add(-statuspkg.DispatchObservationMaxAge-time.Second), "")
		},
		"capture failure": func() {
			setObservation(statuspkg.StateUnknown, statuspkg.FreshnessUnavailable, 0, now, "capture unavailable")
		},
	} {
		t.Run(name, func(t *testing.T) {
			setup()
			if err := gate(t.Context(), dispatchsvc.Request{}, []dispatchsvc.Delivery{delivery}); err == nil {
				t.Fatalf("%s observation passed distribute gate", name)
			}
		})
	}
}

func TestDistributeDispatchGateRejectsExactDurableOccupancyAcrossDuplicateLocalIndexes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC()
	original := newAssignSessionObserver
	t.Cleanup(func() { newAssignSessionObserver = original })
	newAssignSessionObserver = func() assignSessionObserver {
		return fixedAssignSessionObserver{observation: statuspkg.SessionObservation{
			Session: "proj", ObservedAt: now,
			Panes: []statuspkg.PaneObservation{{
				Pane: tmux.PaneRef{ID: "%91", WindowIndex: 1, PaneIndex: 1},
				Current: statuspkg.StateObservation{
					Status: statuspkg.AgentStatus{State: statuspkg.StateIdle}, ObservedAt: now,
					Freshness: statuspkg.FreshnessFresh, Confidence: 0.99,
				},
			}},
		}}
	}
	delivery := dispatchsvc.Delivery{Target: dispatchsvc.Target{
		Pane: tmux.Pane{ID: "%91", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude},
		Ref:  tmux.PaneRef{ID: "%91", WindowIndex: 1, PaneIndex: 1}, Address: "1.1",
	}}
	store := assignment.NewStore("proj")
	store.Assignments["ntm-other-window"] = &assignment.Assignment{
		BeadID: "ntm-other-window", Pane: 1, Status: assignment.StatusAssigned, AssignedAt: now,
		OccupancyKey: "%90", DispatchTarget: "%90",
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed other-window assignment: %v", err)
	}
	gate := distributeDispatchGate("proj")
	if err := gate(t.Context(), dispatchsvc.Request{}, []dispatchsvc.Delivery{delivery}); err != nil {
		t.Fatalf("same local index in another physical pane blocked dispatch: %v", err)
	}

	store.Assignments["ntm-target"] = &assignment.Assignment{
		BeadID: "ntm-target", Pane: 1, Status: assignment.StatusAssigned, AssignedAt: now,
		OccupancyKey: "%91", DispatchTarget: "%91",
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed exact target assignment: %v", err)
	}
	if err := gate(t.Context(), dispatchsvc.Request{}, []dispatchsvc.Delivery{delivery}); err == nil || !strings.Contains(err.Error(), "durable active assignment") {
		t.Fatalf("exact active occupancy error=%v", err)
	}
}

func TestUnifiedDistributeServiceStopsBeforeDeliveryWhenIdleGateFails(t *testing.T) {
	oldCfg := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = oldCfg })
	pane := tmux.Pane{ID: "%999999", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude}
	gateCalls := 0
	service, err := newShellDispatchServiceWithGate("proj", []tmux.Pane{pane}, activeShellDispatchRedactionConfig(),
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

func TestResolveSendSessionForCommandNormalizesExplicitPrefix(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-send-prefix-%d", time.Now().UnixNano())
	prefix := sessionName[:len(sessionName)-4]
	workDir := t.TempDir()

	_ = tmux.KillSession(sessionName)
	if err := tmux.CreateSession(sessionName, workDir); err != nil {
		t.Fatalf("CreateSession(%q): %v", sessionName, err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(sessionName) })

	gotSession, inferred, err := resolveSendSessionForCommand(prefix)
	if err != nil {
		t.Fatalf("resolveSendSessionForCommand() error = %v", err)
	}
	if inferred {
		t.Fatal("expected explicit prefix not to be inferred")
	}
	if gotSession != sessionName {
		t.Fatalf("resolveSendSessionForCommand() session = %q, want %q", gotSession, sessionName)
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
		_ = syscall.Kill(root, syscall.SIGKILL)
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
