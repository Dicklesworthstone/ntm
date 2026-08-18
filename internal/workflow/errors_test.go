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

// snapshot reads calls under the same lock add uses: the TimeoutMonitor
// invokes actions on its own goroutine.
func (a *recordingActions) snapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
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
	calls := actions.snapshot()
	if len(calls) != len(want) {
		t.Fatalf("calls=%v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls=%v", calls)
		}
	}
}
func TestTimeoutMonitorFiresOnlyForCurrentStage(t *testing.T) {
	actions := &recordingActions{}
	h := NewErrorHandler(ErrorHandlingConfig{OnTimeout: ErrorActionNotify}, actions)
	m := NewTimeoutMonitor(time.Millisecond, h, func() string { return "build" })
	m.Start(context.Background(), "build")
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls := actions.snapshot()
		if len(calls) == 1 && calls[0] == "notify" {
			return
		}
		if len(calls) > 1 || time.Now().After(deadline) {
			t.Fatalf("calls=%v", calls)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
