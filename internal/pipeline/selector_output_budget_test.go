package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func budgetAccountingReceipt(id string, cap int64) CommandEvaluationState {
	return CommandEvaluationState{
		Version: commandEvaluationVersion, StepID: id, Status: commandExecutionStarting,
		StepHash: strings.Repeat("a", 64), InvocationHash: strings.Repeat("b", 64),
		StartedAt: time.Unix(1700000000, 0), StdoutLimit: cap, StderrLimit: 1,
	}
}

func TestSelectorOutputBudgetAccountingDoesNotOverflow(t *testing.T) {
	const maximum = int64(1<<63 - 1)
	e := &Executor{
		limits: LimitsConfig{MaxSelectorOutputBytesTotal: maximum},
		state: &ExecutionState{CommandEvaluations: map[string]CommandEvaluationState{
			"one": budgetAccountingReceipt("one", maximum),
			"two": budgetAccountingReceipt("two", maximum),
		}},
	}
	e.stateMu.Lock()
	wake, err := e.selectorOutputAdmissionLocked("next", 1)
	e.stateMu.Unlock()
	if wake != nil || !errors.Is(err, ErrSelectorOutputBudget) {
		t.Fatalf("overflow admitted output or waited on abandoned receipts: wake=%v err=%v", wake, err)
	}
	// An exact int64 boundary remains valid; this test allocates no capture
	// buffers and exercises the arithmetic with durable scalar evidence only.
	e.state.CommandEvaluations = map[string]CommandEvaluationState{
		"one": budgetAccountingReceipt("one", maximum-1),
	}
	e.stateMu.Lock()
	wake, err = e.selectorOutputAdmissionLocked("next", 1)
	e.stateMu.Unlock()
	if wake != nil || err != nil {
		t.Fatalf("exact maximum boundary was refused: wake=%v err=%v", wake, err)
	}
}

func TestSelectorOutputBudgetStaleOwnerCannotReleaseOrWake(t *testing.T) {
	record := budgetAccountingReceipt("active", 4)
	e := &Executor{
		limits: LimitsConfig{MaxSelectorOutputBytesTotal: 4},
		state:  &ExecutionState{CommandEvaluations: map[string]CommandEvaluationState{record.StepID: record}},
	}
	e.stateMu.Lock()
	e.startSelectorOutputReservationLocked(record)
	wake, err := e.selectorOutputAdmissionLocked("next", 1)
	e.stateMu.Unlock()
	if wake == nil || err != nil {
		t.Fatalf("active reservation did not permit waiting: wake=%v err=%v", wake, err)
	}
	stale := record
	stale.StartedAt = stale.StartedAt.Add(-time.Nanosecond)
	e.finishSelectorOutputReservation(stale)
	select {
	case <-wake:
		t.Fatal("stale callback woke waiters for a newer owner")
	default:
	}
	e.stateMu.RLock()
	owner, exists := e.selectorOutputActive[record.StepID]
	e.stateMu.RUnlock()
	if !exists || !owner.matches(record) {
		t.Fatal("stale callback removed the current reservation owner")
	}
	// A callback can return without trustworthy settlement. End its runtime
	// ownership, wake waiters, and retain the entire original durable fence.
	e.finishSelectorOutputReservation(record)
	select {
	case <-wake:
	default:
		t.Fatal("terminal callback did not notify waiting admission")
	}
	e.stateMu.Lock()
	wake, err = e.selectorOutputAdmissionLocked("next", 1)
	preserved := e.state.CommandEvaluations[record.StepID]
	e.stateMu.Unlock()
	if wake != nil || !errors.Is(err, ErrSelectorOutputBudget) || preserved.Status != commandExecutionStarting {
		t.Fatalf("abandoned reservation was released or treated as an active waiter: wake=%v err=%v record=%+v", wake, err, preserved)
	}
}

func TestSelectorOutputBudgetAttemptResetPreservesInterruptedFence(t *testing.T) {
	cfg := DefaultExecutorConfig("budget-reset")
	cfg.ProjectDir, cfg.DryRun = t.TempDir(), true
	e := NewExecutor(cfg)
	record := budgetAccountingReceipt("prior", 4)
	prior := &ExecutionState{
		RunID: "budget-reset", WorkflowID: "budget-reset", Status: StatusFailed,
		CommandEvaluationVersion: commandEvaluationVersion,
		CommandEvaluations:       map[string]CommandEvaluationState{record.StepID: record},
	}
	workflow := &Workflow{Name: "budget-reset", Steps: []Step{{ID: "prior", Branch: "$(printf x)", Branches: map[string]interface{}{
		"x": Step{ID: "selected", Command: "exit 0"},
	}}}}
	workflow.Settings.Limits.MaxSelectorOutputBytesTotal = 4
	e.state, e.limits = prior, workflow.Settings.Limits.EffectiveLimits()
	e.stateMu.Lock()
	e.startSelectorOutputReservationLocked(record)
	wake, err := e.selectorOutputAdmissionLocked("next", 1)
	e.stateMu.Unlock()
	if err != nil || wake == nil {
		t.Fatalf("could not establish prior attempt waiter: %v", err)
	}
	if _, err := e.Resume(context.Background(), workflow, prior, nil); !errors.Is(err, errCommandOutputUnsettled) {
		t.Fatalf("resume did not preserve the interrupted launch fence: %v", err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("resume left a prior attempt notification open")
	}
	e.stateMu.Lock()
	wake, err = e.selectorOutputAdmissionLocked("next", 1)
	active := len(e.selectorOutputActive)
	preserved := e.state.CommandEvaluations[record.StepID]
	e.stateMu.Unlock()
	if wake != nil || !errors.Is(err, ErrSelectorOutputBudget) || active != 0 || !selectorOutputReservationFor(record).matches(preserved) {
		t.Fatalf("resume treated old ownership as releasable: wake=%v err=%v active=%d", wake, err, active)
	}
	// Reusing the Executor for a genuinely new run also clears runtime owners.
	// Dry-run exercises Run's initialization without dispatching any process.
	e.stateMu.Lock()
	e.startSelectorOutputReservationLocked(record)
	e.selectorOutputWake = make(chan struct{})
	staleWake := e.selectorOutputWake
	e.stateMu.Unlock()
	fresh := &Workflow{Name: "fresh-budget", Steps: []Step{{ID: "fresh", Command: "exit 0"}}}
	state, err := e.Run(context.Background(), fresh, nil, nil)
	if err != nil || state.Status != StatusCompleted {
		t.Fatalf("fresh dry run failed: %+v %v", state, err)
	}
	select {
	case <-staleWake:
	default:
		t.Fatal("fresh run left a stale attempt notification open")
	}
	if len(e.selectorOutputActive) != 0 || len(state.CommandEvaluations) != 0 {
		t.Fatal("fresh run inherited selector ownership or journal from the prior run")
	}
}

func TestSelectorOutputBudgetRejectsUnaccountableEvidence(t *testing.T) {
	for _, kind := range []string{"legacy", "missing_cap", "unknown_status", "oversized_output", "unfinished_output"} {
		t.Run(kind, func(t *testing.T) {
			record := budgetAccountingReceipt("saved", 4)
			switch kind {
			case "legacy":
				record.Version, record.StdoutLimit, record.StderrLimit = 1, 0, 0
			case "missing_cap":
				record.StdoutLimit = 0
			case "unknown_status":
				record.Status = "future"
			case "oversized_output":
				record.Status, record.Output = commandExecutionSettled, []byte("12345")
			case "unfinished_output":
				record.Output = []byte("x")
			}
			e := &Executor{state: &ExecutionState{CommandEvaluations: map[string]CommandEvaluationState{record.StepID: record}}}
			e.stateMu.Lock()
			wake, err := e.selectorOutputAdmissionLocked("new", 1)
			e.stateMu.Unlock()
			if wake != nil || err == nil {
				t.Fatalf("unaccountable evidence authorized a fresh source: wake=%v err=%v", wake, err)
			}
		})
	}
	e := &Executor{state: &ExecutionState{}, limits: LimitsConfig{MaxCommandStdoutBytes: 4}}
	e.stateMu.Lock()
	wake, err := e.selectorOutputAdmissionLocked("new", 4)
	e.stateMu.Unlock()
	if wake != nil || err != nil {
		t.Fatalf("unset aggregate limit did not inherit the default: wake=%v err=%v", wake, err)
	}
}

func TestSelectorOutputBudgetNegativeResumeOverridePreservesCheckpoint(t *testing.T) {
	cfg := DefaultExecutorConfig("budget-negative")
	cfg.ProjectDir = t.TempDir()
	cfg.ResumeOptions.MaxSelectorOutputBytesTotal = -1
	prior := &ExecutionState{RunID: "budget-negative", WorkflowID: "budget", Status: StatusFailed, UpdatedAt: time.Unix(1700000000, 0)}
	if err := SaveState(cfg.ProjectDir, prior); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, prior.RunID))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	// A graph error would persist a failed attempt before ordinary resume
	// validation. A negative override must be refused before that mutation.
	workflow := &Workflow{Name: "budget", Steps: []Step{{ID: "invalid", Command: "exit 0", DependsOn: []string{"missing"}}}}
	if _, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil); err == nil || !strings.Contains(err.Error(), "max_selector_output_bytes_total") {
		t.Fatalf("negative override was not rejected at entry: %v", err)
	}
	after, err := os.ReadFile(pipelineStatePath(cfg.ProjectDir, prior.RunID))
	if err != nil {
		t.Fatal(err)
	}
	current, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(identity, current) {
		t.Fatal("rejected negative override changed the durable or in-memory checkpoint")
	}
	if _, err := normalizeResumeOptions(cfg.ResumeOptions); err == nil {
		t.Fatal("background/shared resume normalization accepted a negative override")
	}
}
