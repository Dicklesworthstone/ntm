package workflow

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingActions struct {
	mu    sync.Mutex
	calls []string
}

func (a *recordingActions) add(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, s)
}
func (a *recordingActions) RestartAgent(context.Context, string) error { a.add("restart"); return nil }
func (a *recordingActions) Pause(context.Context, string) error        { a.add("pause"); return nil }
func (a *recordingActions) SkipStage(context.Context) error            { a.add("skip"); return nil }
func (a *recordingActions) Abort(context.Context, error) error         { a.add("abort"); return nil }
func (a *recordingActions) RetryStage(context.Context) error           { a.add("retry"); return nil }
func (a *recordingActions) Notify(context.Context, *WorkflowError, string) error {
	a.add("notify")
	return nil
}

func TestErrorHandlerActionsAndRetries(t *testing.T) {
	actions := &recordingActions{}
	h := NewErrorHandler(ErrorHandlingConfig{OnAgentCrash: ErrorActionRestartAgent, OnAgentError: ErrorActionPause, OnTriggerFailed: ErrorActionSkipStage, OnTimeout: ErrorActionRetry, MaxRetriesPerStage: 1}, actions)
	for _, err := range []*WorkflowError{{Type: ErrorAgentCrash}, {Type: ErrorAgentError}, {Type: ErrorTriggerFailed}, {Type: ErrorTimeout, Stage: "build"}, {Type: ErrorTimeout, Stage: "build"}} {
		if got := h.Handle(context.Background(), err); got != nil {
			t.Fatal(got)
		}
	}
	want := []string{"restart", "pause", "skip", "retry", "abort"}
	if len(actions.calls) != len(want) {
		t.Fatalf("calls=%v", actions.calls)
	}
	for i := range want {
		if actions.calls[i] != want[i] {
			t.Fatalf("calls=%v", actions.calls)
		}
	}
}
func TestTimeoutMonitorFiresOnlyForCurrentStage(t *testing.T) {
	actions := &recordingActions{}
	h := NewErrorHandler(ErrorHandlingConfig{OnTimeout: ErrorActionNotify}, actions)
	m := NewTimeoutMonitor(time.Millisecond, h, func() string { return "build" })
	m.Start(context.Background(), "build")
	time.Sleep(20 * time.Millisecond)
	if len(actions.calls) != 1 || actions.calls[0] != "notify" {
		t.Fatalf("calls=%v", actions.calls)
	}
}
