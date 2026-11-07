package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	h := NewErrorHandler(ErrorHandlingConfig{OnAgentCrash: ErrorActionRestartAgent, OnAgentError: ErrorActionPause, OnTriggerFailed: ErrorActionSkipStage, OnTimeout: ErrorActionRetry, MaxRetriesPerStage: 1, MaxRestartsPerAgent: 1}, actions)
	for _, err := range []*WorkflowError{{Type: ErrorAgentCrash, AgentID: "%1"}, {Type: ErrorAgentError, AgentID: "%1"}, {Type: ErrorTriggerFailed}, {Type: ErrorTimeout, Stage: "build"}, {Type: ErrorTimeout, Stage: "build"}} {
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

// restart_agent is bounded per agent: a crash loop restarts each agent at
// most MaxRestartsPerAgent times, then aborts. Budgets are independent per
// agent, and an error without a faulted agent cannot restart anything.
func TestErrorHandlerRestartBudgetIsPerAgent(t *testing.T) {
	actions := &recordingActions{}
	h := NewErrorHandler(ErrorHandlingConfig{OnAgentCrash: ErrorActionRestartAgent, OnAgentError: ErrorActionRestartAgent, MaxRestartsPerAgent: 2}, actions)
	for _, err := range []*WorkflowError{
		{Type: ErrorAgentCrash, AgentID: "%1"},
		{Type: ErrorAgentError, AgentID: "%1"}, // same agent, different fault kind: shared budget
		{Type: ErrorAgentCrash, AgentID: "%2"},
		{Type: ErrorAgentCrash, AgentID: "%1"}, // third fault of %1: budget exhausted
	} {
		if got := h.Handle(context.Background(), err); got != nil {
			t.Fatal(got)
		}
	}
	want := []string{"restart", "restart", "restart", "abort"}
	if calls := actions.snapshot(); strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls=%v, want %v", calls, want)
	}
	if err := h.Handle(context.Background(), &WorkflowError{Type: ErrorAgentCrash}); err == nil || !strings.Contains(err.Error(), "names none") {
		t.Fatalf("restart without a faulted agent = %v, want refusal", err)
	}
	none := NewErrorHandler(ErrorHandlingConfig{OnAgentCrash: ErrorActionRestartAgent}, actions)
	if err := none.Handle(context.Background(), &WorkflowError{Type: ErrorAgentCrash, AgentID: "%3"}); err != nil {
		t.Fatal(err)
	}
	if calls := actions.snapshot(); calls[len(calls)-1] != "abort" {
		t.Fatalf("zero restart budget must abort, calls=%v", calls)
	}
}

func TestErrorHandlerRecoveryBudgetsSurviveResume(t *testing.T) {
	config := ErrorHandlingConfig{
		OnTimeout: ErrorActionRetry, MaxRetriesPerStage: 2,
		OnAgentCrash: ErrorActionRestartAgent, MaxRestartsPerAgent: 2,
	}
	var saved RecoveryBudgets
	actions := &recordingActions{}
	persisted := 0
	for attempt := 0; attempt < 3; attempt++ {
		h := NewErrorHandler(config, actions)
		if err := h.ConfigureRecovery(saved, func(next RecoveryBudgets) error {
			// The attempted side effect is not allowed to precede this write.
			if len(actions.snapshot()) != persisted {
				t.Fatal("recovery action ran before its durable budget reservation")
			}
			saved = next
			persisted++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, fault := range []*WorkflowError{
			{Type: ErrorTimeout, Stage: "build"},
			{Type: ErrorAgentCrash, AgentID: "%1", Stage: "build"},
		} {
			if err := h.Handle(t.Context(), fault); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls := strings.Join(actions.snapshot(), ","); calls != "retry,restart,retry,restart,abort,abort" {
		t.Fatalf("restored recovery actions = %s", calls)
	}
	if persisted != 4 || saved.Retries["build"] != 2 || saved.Restarts["%1"] != 2 {
		t.Fatalf("saved budgets = %+v, writes = %d", saved, persisted)
	}
}

func TestErrorHandlerRecoveryRefusesSideEffectsWhenSaveFails(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		config ErrorHandlingConfig
		fault  WorkflowError
	}{
		{"retry", ErrorHandlingConfig{OnTimeout: ErrorActionRetry, MaxRetriesPerStage: 1}, WorkflowError{Type: ErrorTimeout, Stage: "build"}},
		{"restart", ErrorHandlingConfig{OnAgentCrash: ErrorActionRestartAgent, MaxRestartsPerAgent: 1}, WorkflowError{Type: ErrorAgentCrash, AgentID: "%1"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			actions := &recordingActions{}
			h := NewErrorHandler(scenario.config, actions)
			saveErr := errors.New("checkpoint unavailable")
			fail := true
			writes := 0
			if err := h.ConfigureRecovery(RecoveryBudgets{}, func(RecoveryBudgets) error {
				writes++
				if fail {
					return saveErr
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.Handle(t.Context(), &scenario.fault); !errors.Is(err, saveErr) {
				t.Fatalf("save failure = %v", err)
			}
			if calls := actions.snapshot(); len(calls) != 0 {
				t.Fatalf("unpersisted action ran: %v", calls)
			}
			fail = false
			if err := h.Handle(t.Context(), &scenario.fault); err != nil {
				t.Fatal(err)
			}
			if calls := strings.Join(actions.snapshot(), ","); calls != scenario.name || writes != 2 {
				t.Fatalf("failed save consumed budget: calls=%s, writes=%d", calls, writes)
			}
		})
	}
}

func TestErrorHandlerRecoverySnapshotsDoNotAlias(t *testing.T) {
	config := ErrorHandlingConfig{
		OnTimeout: ErrorActionRetry, MaxRetriesPerStage: 2,
		OnAgentCrash: ErrorActionRestartAgent, MaxRestartsPerAgent: 2,
	}
	actions := &recordingActions{}
	h := NewErrorHandler(config, actions)
	restored := RecoveryBudgets{Retries: map[string]int{"build": 1}, Restarts: map[string]int{"%1": 1}}
	var published RecoveryBudgets
	if err := h.ConfigureRecovery(restored, func(next RecoveryBudgets) error {
		published = next
		next.Retries["build"] = 0
		next.Restarts["%1"] = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restored.Retries["build"], restored.Restarts["%1"] = 0, 0
	for _, fault := range []*WorkflowError{
		{Type: ErrorTimeout, Stage: "build"},
		{Type: ErrorAgentCrash, AgentID: "%1"},
		{Type: ErrorTimeout, Stage: "build"},
		{Type: ErrorAgentCrash, AgentID: "%1"},
	} {
		if err := h.Handle(t.Context(), fault); err != nil {
			t.Fatal(err)
		}
		if published.Retries != nil {
			published.Retries["build"], published.Restarts["%1"] = 0, 0
		}
	}
	if calls := strings.Join(actions.snapshot(), ","); calls != "retry,restart,abort,abort" {
		t.Fatalf("external map mutation changed recovery budgets: %s", calls)
	}
}

func TestErrorHandlerRecoveryValidatesRestoredBudgets(t *testing.T) {
	for _, invalid := range []RecoveryBudgets{
		{Retries: map[string]int{"build": -1}},
		{Retries: map[string]int{" ": 1}},
		{Restarts: map[string]int{"%1": -1}},
		{Restarts: map[string]int{"": 1}},
	} {
		actions := &recordingActions{}
		h := NewErrorHandler(ErrorHandlingConfig{OnTimeout: ErrorActionRetry, MaxRetriesPerStage: 1}, actions)
		if err := h.ConfigureRecovery(RecoveryBudgets{Retries: map[string]int{"build": 1}}, nil); err != nil {
			t.Fatal(err)
		}
		if err := h.ConfigureRecovery(invalid, nil); err == nil {
			t.Fatalf("accepted invalid recovery budgets: %+v", invalid)
		}
		if err := h.Handle(t.Context(), &WorkflowError{Type: ErrorTimeout, Stage: "build"}); err != nil {
			t.Fatal(err)
		}
		if calls := strings.Join(actions.snapshot(), ","); calls != "abort" {
			t.Fatalf("rejected restore replaced existing budget: %s", calls)
		}
	}
}

func TestErrorHandlerRecoveryDoesNotActAfterCancellation(t *testing.T) {
	for _, cancelDuringSave := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelDuringSave), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			actions := &recordingActions{}
			h := NewErrorHandler(ErrorHandlingConfig{OnTimeout: ErrorActionRetry, MaxRetriesPerStage: 1}, actions)
			writes := 0
			if err := h.ConfigureRecovery(RecoveryBudgets{}, func(RecoveryBudgets) error {
				writes++
				cancel()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !cancelDuringSave {
				cancel()
			}
			fault := &WorkflowError{Type: ErrorTimeout, Stage: "build"}
			if err := h.Handle(ctx, fault); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled recovery = %v", err)
			}
			if calls := actions.snapshot(); len(calls) != 0 {
				t.Fatalf("canceled recovery action ran: %v", calls)
			}
			wantWrites := 0
			if cancelDuringSave {
				wantWrites = 1
			}
			if writes != wantWrites {
				t.Fatalf("writes=%d, want %d", writes, wantWrites)
			}
			if cancelDuringSave {
				if err := h.Handle(t.Context(), fault); err != nil {
					t.Fatal(err)
				}
				if calls := strings.Join(actions.snapshot(), ","); calls != "abort" {
					t.Fatalf("persisted reservation was refunded after cancellation: %s", calls)
				}
			}
		})
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
