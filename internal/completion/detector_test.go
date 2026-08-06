package completion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/process"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	_ = w.Close()
	os.Stdout = oldStdout
	return <-done
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 5*time.Second)
	}
	if cfg.IdleThreshold != 120*time.Second {
		t.Errorf("IdleThreshold = %v, want %v", cfg.IdleThreshold, 120*time.Second)
	}
	if !cfg.RetryOnError {
		t.Error("RetryOnError should be true by default")
	}
	if !cfg.GracefulDegrading {
		t.Error("GracefulDegrading should be true by default")
	}
	if cfg.CaptureLines != 50 {
		t.Errorf("CaptureLines = %d, want 50", cfg.CaptureLines)
	}
}

func TestNew(t *testing.T) {
	store := assignment.NewStore("test-session")
	d := New("test-session", store)

	if d.Session != "test-session" {
		t.Errorf("Session = %q, want %q", d.Session, "test-session")
	}
	if d.Store != store {
		t.Error("Store not set correctly")
	}
	if len(d.Patterns) == 0 {
		t.Error("Default completion patterns not loaded")
	}
	if len(d.FailPattern) == 0 {
		t.Error("Default failure patterns not loaded")
	}
}

func TestAddPattern(t *testing.T) {
	d := New("test-session", nil)
	initialCount := len(d.Patterns)

	err := d.AddPattern(`(?i)custom\s+complete`)
	if err != nil {
		t.Fatalf("AddPattern failed: %v", err)
	}

	if len(d.Patterns) != initialCount+1 {
		t.Errorf("Pattern count = %d, want %d", len(d.Patterns), initialCount+1)
	}
}

func TestAddPatternInvalid(t *testing.T) {
	d := New("test-session", nil)

	err := d.AddPattern(`[invalid`)
	if err == nil {
		t.Error("AddPattern should fail for invalid regex")
	}
}

func TestAddFailurePattern(t *testing.T) {
	d := New("test-session", nil)
	initialCount := len(d.FailPattern)

	err := d.AddFailurePattern(`(?i)custom\s+failure`)
	if err != nil {
		t.Fatalf("AddFailurePattern failed: %v", err)
	}

	if len(d.FailPattern) != initialCount+1 {
		t.Errorf("Pattern count = %d, want %d", len(d.FailPattern), initialCount+1)
	}
}

func TestMatchCompletionPatterns(t *testing.T) {
	d := New("test-session", nil)

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"bead complete", "I've finished the bead bd-1234 complete", true},
		{"task done", "The task bd-1234 done successfully", true},
		{"task finished", "task xyz finished successfully", true},
		{"closing bead", "closing bead bd-5678", true},
		{"br close", "Running br close bd-1234", true},
		{"marked complete", "The work was marked as complete", true},
		{"successfully completed", "Task successfully completed!", true},
		{"work complete", "My work complete for this bead", true},
		{"no match", "Just regular output without keywords", false},
		{"partial match", "The bead is still in progress", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.matchCompletionPatterns(tt.output)
			if got != tt.want {
				t.Errorf("matchCompletionPatterns(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestMatchFailurePatterns(t *testing.T) {
	d := New("test-session", nil)

	tests := []struct {
		name      string
		output    string
		wantMatch bool
	}{
		{"unable to complete", "I'm unable to complete this task", true},
		{"cannot proceed", "Cannot proceed due to missing dependencies", true},
		{"blocked by", "This is blocked by another issue", true},
		{"giving up", "I'm giving up on this approach", true},
		{"need help", "I need help with this problem", true},
		{"failed to", "Failed to compile the code", true},
		{"error fatal", "Error: fatal exception occurred", true},
		{"aborting", "Aborting the operation", true},
		{"no match", "Everything is working fine", false},
		{"success message", "Successfully deployed the feature", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.matchFailurePatterns(tt.output)
			if (got != "") != tt.wantMatch {
				t.Errorf("matchFailurePatterns(%q) = %q, wantMatch=%v", tt.output, got, tt.wantMatch)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		maxLen int
		want   string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncate", "hello world", 5, "...world"},
		{"empty", "", 10, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateOutput(tt.output, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateOutput(%q, %d) = %q, want %q", tt.output, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestCompletionEventFields(t *testing.T) {
	event := CompletionEvent{
		Pane:       2,
		AgentType:  "claude",
		BeadID:     "bd-1234",
		Method:     MethodPatternMatch,
		Timestamp:  time.Now(),
		Duration:   5 * time.Minute,
		Output:     "task complete",
		IsFailed:   false,
		FailReason: "",
	}

	if event.Pane != 2 {
		t.Errorf("Pane = %d, want 2", event.Pane)
	}
	if event.AgentType != "claude" {
		t.Errorf("AgentType = %q, want %q", event.AgentType, "claude")
	}
	if event.Method != MethodPatternMatch {
		t.Errorf("Method = %v, want %v", event.Method, MethodPatternMatch)
	}
}

func TestDetectionMethods(t *testing.T) {
	tests := []struct {
		method DetectionMethod
		want   string
	}{
		{MethodBeadClosed, "bead_closed"},
		{MethodPatternMatch, "pattern_match"},
		{MethodIdle, "idle"},
		{MethodAgentMail, "agent_mail"},
		{MethodPaneLost, "pane_lost"},
	}

	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			if string(tt.method) != tt.want {
				t.Errorf("DetectionMethod = %q, want %q", tt.method, tt.want)
			}
		})
	}
}

func TestCheckNowNoStore(t *testing.T) {
	d := New("test-session", nil)

	_, err := d.CheckNow("%1")
	if err == nil {
		t.Error("CheckNow should fail without assignment store")
	}
}

func TestCheckNowNoAssignment(t *testing.T) {
	store := assignment.NewStore("test-session")
	d := New("test-session", store)

	_, err := d.CheckNow("%99")
	if err == nil {
		t.Error("CheckNow should fail for pane with no assignment")
	}
}

func TestIdleDetection(t *testing.T) {
	store := assignment.NewStore("test-session")
	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond // Very short for testing
	d := NewWithConfig("test-session", store, cfg)

	now := time.Now()
	a := &assignment.Assignment{
		BeadID:       "bd-test",
		Pane:         0,
		OccupancyKey: "%1",
		AgentType:    "claude",
		AssignedAt:   now,
	}

	// First check - initialize activity state
	event := d.checkIdle(a, "initial output", now)
	if event != nil {
		t.Error("First checkIdle should return nil (initializing)")
	}

	// Same output - should trigger burst detection but not complete yet
	event = d.checkIdle(a, "initial output", now)
	if event != nil {
		t.Error("Second checkIdle should return nil (no burst started)")
	}

	// Change output to start burst
	event = d.checkIdle(a, "new output", now)
	if event != nil {
		t.Error("After output change, checkIdle should return nil")
	}

	// Wait for idle threshold
	time.Sleep(15 * time.Millisecond)

	// Same output after threshold - should detect idle completion
	event = d.checkIdle(a, "new output", now)
	if event == nil {
		t.Error("After idle threshold, checkIdle should return completion event")
	}
	if event != nil && event.Method != MethodIdle {
		t.Errorf("Method = %v, want %v", event.Method, MethodIdle)
	}
}

func TestIdleDetectionRequiresContinuousSafeObservation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond
	d := NewWithConfig("test-session", assignment.NewStore("test-session"), cfg)
	now := time.Now()
	a := &assignment.Assignment{BeadID: "bd-safe", Pane: 0, OccupancyKey: "%1", AssignedAt: now}
	safe := statuspkg.PaneObservation{Current: statuspkg.StateObservation{
		Status:     statuspkg.AgentStatus{State: statuspkg.StateIdle},
		Freshness:  statuspkg.FreshnessFresh,
		Confidence: 0.95,
	}}
	working := safe
	working.Current.Status.State = statuspkg.StateWorking

	if event := d.checkIdleWhenSafe(a, "unchanged", now, safe); event != nil {
		t.Fatalf("first safe observation should only initialize tracking: %+v", event)
	}
	time.Sleep(15 * time.Millisecond)
	if event := d.checkIdleWhenSafe(a, "unchanged", now, working); event != nil {
		t.Fatalf("working observation must suppress idle failure: %+v", event)
	}
	if len(d.activityTracker) != 0 {
		t.Fatalf("working observation retained stale idle timer: %+v", d.activityTracker)
	}

	time.Sleep(15 * time.Millisecond)
	if event := d.checkIdleWhenSafe(a, "unchanged", now, safe); event != nil {
		t.Fatalf("new safe interval inherited stale timeout: %+v", event)
	}
	time.Sleep(15 * time.Millisecond)
	if event := d.checkIdleWhenSafe(a, "unchanged", now, safe); event == nil || event.Method != MethodIdle {
		t.Fatalf("continuous safe idle interval did not produce idle event: %+v", event)
	}
}

func TestIdleDetectionResetsForNewAssignmentOnSamePane(t *testing.T) {
	store := assignment.NewStore("test-session")
	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond
	d := NewWithConfig("test-session", store, cfg)

	now := time.Now()
	first := &assignment.Assignment{
		BeadID:       "bd-old",
		Pane:         0,
		OccupancyKey: "%1",
		AgentType:    "claude",
		AssignedAt:   now,
	}
	second := &assignment.Assignment{
		BeadID:       "bd-new",
		Pane:         0,
		OccupancyKey: "%1",
		AgentType:    "claude",
		AssignedAt:   now.Add(time.Second),
	}

	if event := d.checkIdle(first, "initial output", now); event != nil {
		t.Fatal("first assignment should initialize idle tracking without completing")
	}
	if event := d.checkIdle(first, "updated output", now); event != nil {
		t.Fatal("first assignment should not complete when output changes")
	}

	time.Sleep(15 * time.Millisecond)

	if event := d.checkIdle(second, "updated output", second.AssignedAt); event != nil {
		t.Fatalf("new assignment should not inherit stale idle state, got %+v", event)
	}
}

func TestWatchCancellation(t *testing.T) {
	store := assignment.NewStore("test-session")
	cfg := DefaultConfig()
	cfg.PollInterval = 10 * time.Millisecond
	d := NewWithConfig("test-session", store, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	events := d.Watch(ctx)

	// Cancel immediately
	cancel()

	// Channel should close
	select {
	case _, ok := <-events:
		if ok {
			// May receive events before close, that's fine
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Events channel should close after context cancellation")
	}
}

func TestDeduplication(t *testing.T) {
	d := New("test-session", nil)
	d.Config.DedupWindow = 100 * time.Millisecond

	// Record an event
	d.mu.Lock()
	d.recentEvents["bd-test:0:attempt"] = time.Now()
	d.mu.Unlock()

	// Check if within dedup window
	d.mu.RLock()
	lastEvent, exists := d.recentEvents["bd-test:0:attempt"]
	d.mu.RUnlock()

	if !exists {
		t.Error("Event should exist in recentEvents")
	}
	if time.Since(lastEvent) >= d.Config.DedupWindow {
		t.Error("Event should be within dedup window")
	}
}

func TestRecordEventLockedScopesDedupToAssignmentAttempt(t *testing.T) {
	d := New("test-session", nil)
	d.Config.DedupWindow = 100 * time.Millisecond

	now := time.Now()
	first := &assignment.Assignment{
		BeadID:         "bd-test",
		Pane:           0,
		OccupancyKey:   "%1",
		AssignedAt:     now,
		IdempotencyKey: "first-generation",
	}
	retry := &assignment.Assignment{
		BeadID:         "bd-test",
		Pane:           0,
		OccupancyKey:   "%1",
		AssignedAt:     now,
		IdempotencyKey: "retry-generation",
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.recordEventLocked(first, now) {
		t.Fatal("first event should be recorded")
	}
	if d.recordEventLocked(first, now.Add(10*time.Millisecond)) {
		t.Fatal("same assignment attempt should be deduplicated within the window")
	}

	d.recentEvents["stale"] = now.Add(-time.Second)

	if !d.recordEventLocked(retry, now.Add(10*time.Millisecond)) {
		t.Fatal("retry attempt should not inherit the previous attempt's dedup state")
	}
	if _, exists := d.recentEvents["stale"]; exists {
		t.Fatal("expired dedup entries should be pruned")
	}
}

func TestPersistCompletionEventRejectsSupersededGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "completion-generation-cas"
	const beadID = "ntm-completion-generation-cas"
	store := assignment.NewStore(session)
	observed, err := store.Assign(beadID, "Old generation", 1, "codex", "OldAgent", "old work")
	if err != nil {
		t.Fatalf("Assign old generation: %v", err)
	}
	winner, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("LoadStoreStrict winner: %v", err)
	}
	newGeneration, err := winner.Assign(beadID, "New generation", 2, "codex", "NewAgent", "new work")
	if err != nil {
		t.Fatalf("Assign new generation: %v", err)
	}
	winner.Assignments[beadID].ClaimActor = "NewAgent/new-generation"
	winner.Assignments[beadID].ReservationRequired = true
	winner.Assignments[beadID].ReservationState = assignment.ReservationReserved
	winner.Assignments[beadID].ReservationCompleted = true
	winner.Assignments[beadID].ReservationIDs = []int{902}
	winner.Assignments[beadID].ReservedPaths = []string{"internal/completion/winner.go"}
	if err := winner.Save(); err != nil {
		t.Fatalf("persist winner lease: %v", err)
	}
	newGeneration = winner.Get(beadID)
	detector := New(session, store)
	reconcileCalls := 0
	detector.SetTerminalReconciler(func(context.Context, *assignment.Assignment) (bool, error) {
		reconcileCalls++
		return false, errors.New("stale generation reached external reconciliation")
	})
	applied, err := detector.persistCompletionEvent(t.Context(), observed, &CompletionEvent{BeadID: beadID, Pane: 1, Method: MethodPatternMatch})
	if err != nil || applied {
		t.Fatalf("stale completion applied=%v error=%v", applied, err)
	}
	failedApplied, err := detector.persistCompletionEvent(t.Context(), observed, &CompletionEvent{BeadID: beadID, Pane: 1, Method: MethodIdle, IsFailed: true, FailReason: "stale idle"})
	if err != nil || failedApplied {
		t.Fatalf("stale failure applied=%v error=%v", failedApplied, err)
	}
	stored, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload winner: %v", err)
	}
	current := stored.Get(beadID)
	if reconcileCalls != 0 {
		t.Fatalf("stale completion invoked external reconciler %d times", reconcileCalls)
	}
	if current == nil || current.AssignedAt != newGeneration.AssignedAt || current.AgentName != "NewAgent" || current.Status != assignment.StatusAssigned || current.CompletedAt != nil || current.FailedAt != nil ||
		current.ClearState != assignment.ClearStateNone || !reflect.DeepEqual(current.ReservationIDs, []int{902}) || !reflect.DeepEqual(current.ReservedPaths, []string{"internal/completion/winner.go"}) {
		t.Fatalf("stale completion mutated replacement: %+v", current)
	}
}

func TestPersistCompletionEventAppliesExactGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := assignment.NewStore("completion-exact-generation")
	observed, err := store.Assign("ntm-completion-exact", "Exact", 1, "codex", "CodexOne", "work")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	detector := New("completion-exact-generation", store)
	applied, err := detector.persistCompletionEvent(t.Context(), observed, &CompletionEvent{BeadID: observed.BeadID, Pane: observed.Pane, Method: MethodBeadClosed})
	if err != nil || !applied {
		t.Fatalf("exact completion applied=%v error=%v", applied, err)
	}
	completed := store.Get(observed.BeadID)
	if completed == nil || completed.Status != assignment.StatusCompleted || completed.CompletedAt == nil {
		t.Fatalf("exact completion state=%+v", completed)
	}
}

func TestPersistCompletionEventBackfillsAlreadyTerminalGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "completion-terminal-backfill"
		beadID  = "ntm-completion-terminal-backfill"
	)
	store := assignment.NewStore(session)
	observed, err := store.Assign(beadID, "Backfill", 1, "codex", "CodexOne", "work")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	barrier, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), observed, assignment.StatusCompleted, "")
	if err != nil || !applied || barrier == nil {
		t.Fatalf("begin terminal reconciliation barrier=%+v applied=%v error=%v", barrier, applied, err)
	}
	if _, err := store.RecordClearLeasesReleased(t.Context(), beadID); err != nil {
		t.Fatalf("record leases released: %v", err)
	}
	if _, err := store.RecordTerminalClaimReleased(t.Context(), beadID); err != nil {
		t.Fatalf("record claim released: %v", err)
	}
	if err := store.CompleteTerminalReconciliation(t.Context(), beadID, assignment.StatusCompleted, ""); err != nil {
		t.Fatalf("complete terminal reconciliation: %v", err)
	}
	terminal := store.Get(beadID)
	if terminal == nil || terminal.Status != assignment.StatusCompleted || terminal.PendingCompletionEventID != "" {
		t.Fatalf("pre-backfill terminal row=%+v", terminal)
	}

	detector := New(session, store)
	reconcileCalls := 0
	detector.SetTerminalReconciler(func(context.Context, *assignment.Assignment) (bool, error) {
		reconcileCalls++
		return false, errors.New("terminal backfill repeated external cleanup")
	})
	applied, err = detector.persistCompletionEvent(t.Context(), observed, &CompletionEvent{
		BeadID: beadID, Pane: observed.Pane, Method: MethodBeadClosed,
	})
	if err != nil || !applied {
		t.Fatalf("backfill completion applied=%v error=%v", applied, err)
	}
	if reconcileCalls != 0 {
		t.Fatalf("backfill invoked external reconciliation %d times", reconcileCalls)
	}
	backfilled := store.Get(beadID)
	if backfilled == nil || backfilled.Status != assignment.StatusCompleted || backfilled.ClearState != assignment.ClearStateNone ||
		strings.TrimSpace(backfilled.PendingCompletionEventID) == "" || backfilled.CompletionDetectedAt == nil {
		t.Fatalf("backfilled terminal row=%+v", backfilled)
	}
}

func TestPersistCompletionEventRejectsDifferentReasonFromDurableBarrier(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "completion-durable-reason-wins"
	const beadID = "ntm-completion-durable-reason-wins"
	store := assignment.NewStore(session)
	observed, err := store.Assign(beadID, "Durable failure reason", 1, "codex", "CodexOne", "work")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	barrier, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), observed, assignment.StatusFailed, "durable reason")
	if err != nil || !applied || barrier == nil {
		t.Fatalf("seed terminal barrier=%+v applied=%v error=%v", barrier, applied, err)
	}

	detector := New(session, store)
	reconcileCalls := 0
	detector.SetTerminalReconciler(func(context.Context, *assignment.Assignment) (bool, error) {
		reconcileCalls++
		return true, nil
	})
	applied, err = detector.persistCompletionEvent(t.Context(), observed, &CompletionEvent{
		BeadID: beadID, Pane: 1, Method: MethodIdle, IsFailed: true, FailReason: "competing reason",
	})
	if err != nil || applied {
		t.Fatalf("competing reason applied=%v error=%v", applied, err)
	}
	if reconcileCalls != 0 {
		t.Fatalf("competing reason invoked reconciler %d times", reconcileCalls)
	}
	current := store.Get(beadID)
	if current == nil || current.PendingTerminalStatus != assignment.StatusFailed || current.PendingTerminalReason != "durable reason" {
		t.Fatalf("durable barrier changed: %+v", current)
	}
}

func TestPendingCompletionReconciliationReleasesLeaseOnceAndEmitsOnlyAfterTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "completion-terminal-retry"
	const beadID = "ntm-completion-terminal-retry"
	store := assignment.NewStore(session)
	now := time.Now().UTC()
	store.Assignments[beadID] = &assignment.Assignment{
		BeadID: beadID, BeadTitle: "Reserved completion", Pane: 1, AgentType: "codex", AgentName: "CodexOne",
		Status: assignment.StatusAssigned, AssignedAt: now, IdempotencyKey: "completion-terminal-retry-key",
		ClaimActor: "CodexOne/completion-terminal-retry-key", DispatchTarget: "%91", OccupancyKey: "%91",
		DispatchState: assignment.DispatchSent, DispatchReceiptID: "mail-91", ReservationRequired: true,
		ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
		ReservedPaths: []string{"internal/completion/detector.go"}, ReservationIDs: []int{911},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed reserved completion: %v", err)
	}
	observed := store.Get(beadID)
	detector := New(session, store)
	leaseReleases := 0
	claimReleases := 0
	attempts := 0
	detector.SetTerminalReconciler(func(ctx context.Context, current *assignment.Assignment) (bool, error) {
		attempts++
		if current.ClearState == assignment.ClearStateReservationReleasing {
			leaseReleases++
			if !reflect.DeepEqual(current.ReservationIDs, []int{911}) {
				t.Fatalf("lease release observed IDs=%v", current.ReservationIDs)
			}
			if _, err := store.RecordClearLeasesReleased(ctx, current.BeadID); err != nil {
				return false, err
			}
			if attempts == 1 {
				return false, errors.New("injected interruption after durable lease checkpoint")
			}
		}
		current = store.Get(current.BeadID)
		if !current.TerminalClaimReleased {
			claimReleases++
			var err error
			current, err = store.RecordTerminalClaimReleased(ctx, current.BeadID)
			if err != nil {
				return false, err
			}
		}
		if err := store.CompleteTerminalReconciliation(ctx, current.BeadID, current.PendingTerminalStatus, current.PendingTerminalReason); err != nil {
			return false, err
		}
		return true, nil
	})

	applied, err := detector.persistCompletionEvent(t.Context(), observed, &CompletionEvent{BeadID: beadID, Pane: 1, Method: MethodPatternMatch})
	if err == nil || applied || !strings.Contains(err.Error(), "injected interruption") {
		t.Fatalf("interrupted completion applied=%v error=%v", applied, err)
	}
	pending := store.Get(beadID)
	if pending == nil || pending.Status != assignment.StatusAssigned || pending.ClearState != assignment.ClearStateLeasesReleased ||
		pending.PendingTerminalStatus != assignment.StatusCompleted || len(pending.ReservationIDs) != 0 || len(pending.ReservedPaths) != 0 {
		t.Fatalf("interrupted completion barrier=%+v", pending)
	}

	events := make(chan CompletionEvent, 1)
	detector.checkAll(t.Context(), events)
	select {
	case event := <-events:
		if event.BeadID != beadID || event.IsFailed {
			t.Fatalf("resumed completion event=%+v", event)
		}
	default:
		t.Fatal("completion event was not emitted after terminal reconciliation")
	}
	if leaseReleases != 1 || claimReleases != 1 || attempts != 2 {
		t.Fatalf("reconciliation side effects lease=%d claim=%d attempts=%d, want 1/1/2", leaseReleases, claimReleases, attempts)
	}
	terminal := store.Get(beadID)
	if terminal == nil || terminal.Status != assignment.StatusCompleted || terminal.CompletedAt == nil || terminal.ClearState != assignment.ClearStateNone ||
		terminal.PendingTerminalStatus != "" || len(terminal.ReservationIDs) != 0 || len(terminal.ReservedPaths) != 0 {
		t.Fatalf("terminal completion=%+v", terminal)
	}
}

func TestCompletionOutboxSingleConsumerLeaseAndExpiryRecoveryAcrossDetectors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "completion-outbox-restart"
	const beadID = "ntm-completion-outbox-restart"
	now := time.Now().UTC()
	store := assignment.NewStore(session)
	store.Assignments[beadID] = &assignment.Assignment{
		BeadID: beadID, BeadTitle: "Outbox restart", Pane: 1, AgentType: "codex", AgentName: "CodexOne",
		Status: assignment.StatusAssigned, AssignedAt: now, IdempotencyKey: "completion-outbox-generation",
		DispatchTarget: "%91", OccupancyKey: "%91", DispatchState: assignment.DispatchSent,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	observed := store.Get(beadID)
	detector := New(session, store)
	applied, err := detector.persistCompletionEvent(t.Context(), observed, &CompletionEvent{
		BeadID: beadID, Pane: 1, PaneID: "%91", Method: MethodPatternMatch, Timestamp: now,
	})
	if err != nil || !applied {
		t.Fatalf("persist terminal completion applied=%v error=%v", applied, err)
	}
	terminal := store.Get(beadID)
	if terminal == nil || terminal.Status != assignment.StatusCompleted || terminal.ClearState != assignment.ClearStateNone || terminal.PendingCompletionEventID == "" {
		t.Fatalf("terminal outbox row=%+v", terminal)
	}
	eventID := terminal.PendingCompletionEventID
	event, err := completionEventFromDurableTerminal(terminal, nil)
	if err != nil {
		t.Fatalf("build durable completion event: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if detector.emitCompletionEvent(cancelled, terminal, event, make(chan CompletionEvent)) {
		t.Fatal("cancelled enqueue reported success")
	}
	if current := store.Get(beadID); current == nil || current.PendingCompletionEventID != eventID {
		t.Fatalf("cancelled enqueue cleared outbox: %+v", current)
	}
	if current := store.Get(beadID); current.CompletionConsumerToken != "" || current.CompletionLeaseExpiresAt != nil {
		t.Fatalf("canceled pre-claim enqueue created a lease: %+v", current)
	}

	cfg := DefaultConfig()
	cfg.CompletionLeaseDuration = time.Hour
	storeA, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load detector A store: %v", err)
	}
	storeB, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load detector B store: %v", err)
	}
	detectorA := NewWithConfig(session, storeA, cfg)
	detectorA.consumerToken = "completion-consumer-a"
	detectorB := NewWithConfig(session, storeB, cfg)
	detectorB.consumerToken = "completion-consumer-b"
	eventsA := make(chan CompletionEvent, 1)
	eventsB := make(chan CompletionEvent, 1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, contender := range []struct {
		detector *CompletionDetector
		events   chan CompletionEvent
	}{{detectorA, eventsA}, {detectorB, eventsB}} {
		contender := contender
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			contender.detector.checkAll(t.Context(), contender.events)
		}()
	}
	close(start)
	wg.Wait()

	handlerCalls := 0
	var firstReplay CompletionEvent
	for _, events := range []chan CompletionEvent{eventsA, eventsB} {
		select {
		case replay := <-events:
			handlerCalls++
			firstReplay = replay
		default:
		}
	}
	if handlerCalls != 1 || firstReplay.EventID != eventID || firstReplay.BeadID != beadID || firstReplay.ConsumerToken == "" {
		t.Fatalf("concurrent detector handlers=%d replay=%+v, want exactly one", handlerCalls, firstReplay)
	}
	durable, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload claimed completion event: %v", err)
	}
	claimed := durable.Get(beadID)
	if claimed == nil || claimed.CompletionConsumerToken != firstReplay.ConsumerToken || claimed.CompletionLeaseExpiresAt == nil {
		t.Fatalf("durable completion consumer lease=%+v replay=%+v", claimed, firstReplay)
	}

	immediateStore, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load immediate recovery store: %v", err)
	}
	immediate := NewWithConfig(session, immediateStore, cfg)
	immediate.consumerToken = "completion-consumer-immediate"
	immediateEvents := make(chan CompletionEvent, 1)
	immediate.checkAll(t.Context(), immediateEvents)
	select {
	case replay := <-immediateEvents:
		t.Fatalf("live completion lease replayed to second handler: %+v", replay)
	default:
	}

	// Model a process crash: no acknowledgement and no heartbeat renewal. Make
	// the durable expiry explicit so race instrumentation cannot change the
	// live-lease and expired-lease phases of this test.
	expiredAt := time.Now().UTC().Add(-time.Second)
	durable.Assignments[beadID].CompletionLeaseExpiresAt = &expiredAt
	if err := durable.Save(); err != nil {
		t.Fatalf("expire completion lease: %v", err)
	}
	recoveryStore, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load expired-lease recovery store: %v", err)
	}
	recovery := NewWithConfig(session, recoveryStore, cfg)
	recovery.consumerToken = "completion-consumer-recovery"
	recoveryEvents := make(chan CompletionEvent, 1)
	recovery.checkAll(t.Context(), recoveryEvents)
	var recovered CompletionEvent
	select {
	case recovered = <-recoveryEvents:
	default:
		t.Fatal("expired completion lease was not replayed")
	}
	if recovered.EventID != eventID || recovered.ConsumerToken != recovery.consumerToken ||
		recovered.Timestamp != firstReplay.Timestamp || recovered.Duration != firstReplay.Duration {
		t.Fatalf("expired-lease recovery replay=%+v first=%+v", recovered, firstReplay)
	}
	if acknowledged, err := durable.AcknowledgeCompletionEvent(t.Context(), beadID, eventID, firstReplay.ConsumerToken); err != nil || acknowledged {
		t.Fatalf("stale consumer acknowledgement applied=%v error=%v", acknowledged, err)
	}
	if acknowledged, err := recoveryStore.AcknowledgeCompletionEvent(t.Context(), beadID, eventID, recovered.ConsumerToken); err != nil || !acknowledged {
		t.Fatalf("recovery consumer acknowledgement applied=%v error=%v", acknowledged, err)
	}
	afterAck := make(chan CompletionEvent, 1)
	New(session, recoveryStore).checkAll(t.Context(), afterAck)
	select {
	case replay := <-afterAck:
		t.Fatalf("acknowledged event replayed: %+v", replay)
	default:
	}
}

func TestCompletionDetectorDoesNotRefreshRecentlyQueuedExpiredLease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "completion-queued-dedup-lease"
		beadID  = "ntm-completion-queued-dedup-lease"
		eventID = "completion-queued-dedup-event"
	)
	detectedAt := time.Now().UTC()
	store := assignment.NewStore(session)
	store.Assignments[beadID] = &assignment.Assignment{
		BeadID: beadID, Status: assignment.StatusCompleted, AssignedAt: detectedAt,
		DispatchTarget: "%92", OccupancyKey: "%92", IdempotencyKey: "queued-dedup-generation",
		PendingCompletionEventID: eventID, CompletionDetectedAt: &detectedAt,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed queued completion event: %v", err)
	}

	cfg := DefaultConfig()
	cfg.CompletionLeaseDuration = 40 * time.Millisecond
	cfg.DedupWindow = 5 * time.Second
	detector := NewWithConfig(session, store, cfg)
	detector.consumerToken = "completion-queued-dedup-consumer"
	events := make(chan CompletionEvent, 1)
	terminal := store.Get(beadID)
	event, err := completionEventFromDurableTerminal(terminal, nil)
	if err != nil {
		t.Fatalf("build queued completion event: %v", err)
	}
	if !detector.emitCompletionEvent(t.Context(), terminal, event, events) {
		t.Fatal("initial queued completion emission was canceled")
	}
	select {
	case <-events:
	default:
		t.Fatal("initial queued completion event was not emitted")
	}
	claimed := store.Get(beadID)
	if claimed == nil || claimed.CompletionLeaseExpiresAt == nil {
		t.Fatalf("initial queued completion lease=%+v", claimed)
	}
	initialExpiry := *claimed.CompletionLeaseExpiresAt
	time.Sleep(cfg.CompletionLeaseDuration + 20*time.Millisecond)

	if !detector.emitCompletionEvent(t.Context(), store.Get(beadID), event, events) {
		t.Fatal("deduplicated queued completion emission was canceled")
	}
	select {
	case replay := <-events:
		t.Fatalf("recent queued completion event was re-emitted: %+v", replay)
	default:
	}
	afterDedup := store.Get(beadID)
	if afterDedup == nil || afterDedup.CompletionLeaseExpiresAt == nil || !afterDedup.CompletionLeaseExpiresAt.Equal(initialExpiry) || afterDedup.CompletionLeaseExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("recent queued completion lease was refreshed: initial=%v current=%+v", initialExpiry, afterDedup)
	}

	// Move the detector's read-only dedup marker past its window. A later scan
	// may now reacquire and replay the still-pending expired event for ACK retry.
	detector.mu.Lock()
	detector.recentEvents[assignmentAttemptKey(afterDedup)] = time.Now().Add(-cfg.DedupWindow)
	detector.mu.Unlock()
	if !detector.emitCompletionEvent(t.Context(), afterDedup, event, events) {
		t.Fatal("post-dedup queued completion emission was canceled")
	}
	select {
	case replay := <-events:
		if replay.EventID != eventID || replay.ConsumerToken != detector.consumerToken {
			t.Fatalf("post-dedup replay=%+v", replay)
		}
	default:
		t.Fatal("expired queued completion event was not replayed after dedup window")
	}
	replayed := store.Get(beadID)
	if replayed == nil || replayed.CompletionLeaseExpiresAt == nil || !replayed.CompletionLeaseExpiresAt.After(initialExpiry) {
		t.Fatalf("post-dedup completion lease was not reacquired: initial=%v current=%+v", initialExpiry, replayed)
	}
}

func TestCompletionOutboxRejectsAmbiguousPaneIdentityAndRemainsPending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "completion-outbox-pane-migration"
	const beadID = "ntm-completion-outbox-pane-migration"
	now := time.Now().UTC()
	store := assignment.NewStore(session)
	store.Assignments[beadID] = &assignment.Assignment{
		BeadID: beadID, Pane: 1, Status: assignment.StatusCompleted, AssignedAt: now,
		DispatchTarget: "0.1", OccupancyKey: "0.1", IdempotencyKey: "legacy-completion-generation",
		PendingCompletionEventID: "legacy-completion-event", CompletionDetectedAt: &now,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed ambiguous completion outbox: %v", err)
	}

	terminal := store.Get(beadID)
	event, err := completionEventFromDurableTerminal(terminal, nil)
	var migrationErr *assignment.PaneIdentityMigrationError
	if event != nil || !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) || !errors.As(err, &migrationErr) {
		t.Fatalf("durable completion event=%+v error=%v, want typed pane migration error", event, err)
	}

	events := make(chan CompletionEvent, 1)
	New(session, store).checkAll(t.Context(), events)
	select {
	case emitted := <-events:
		t.Fatalf("ambiguous completion event emitted: %+v", emitted)
	default:
	}
	current := store.Get(beadID)
	if current == nil || current.PendingCompletionEventID != "legacy-completion-event" || current.CompletionDetectedAt == nil {
		t.Fatalf("ambiguous completion outbox was acknowledged or discarded: %+v", current)
	}
}

func TestResolveAssignmentPaneUsesDurableIdentityAcrossWindows(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", WindowIndex: 0, Index: 0},
		{ID: "%9", WindowIndex: 1, Index: 0},
	}

	byID, err := resolveAssignmentPane("proj", &assignment.Assignment{Pane: 0, DispatchTarget: "%9"}, panes)
	if err != nil {
		t.Fatalf("resolve by ID: %v", err)
	}
	if byID.ID != "%9" || byID.WindowIndex != 1 {
		t.Fatalf("resolved by ID=%+v", byID)
	}

	byAddress, err := resolveAssignmentPane("proj", &assignment.Assignment{Pane: 0, DispatchTarget: "proj:1.0"}, panes)
	var migrationErr *assignment.PaneIdentityMigrationError
	if byAddress.ID != "" || !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) || !errors.As(err, &migrationErr) {
		t.Fatalf("window-address resolution=%+v error=%v, want typed migration error", byAddress, err)
	}

	stable, err := resolveAssignmentPane("proj", &assignment.Assignment{
		Pane: 1, DispatchTarget: "1", OccupancyKey: "%1",
	}, panes)
	if err != nil {
		t.Fatalf("resolve stable identity after topology change: %v", err)
	}
	if stable.ID != "%1" {
		t.Fatalf("stable identity resolved %+v, want original pane %%1", stable)
	}
}

func TestAssignmentObservationShowsWorkingRequiresFreshAssignedEvidence(t *testing.T) {
	base := statuspkg.PaneObservation{Current: statuspkg.StateObservation{
		Status:     statuspkg.AgentStatus{State: statuspkg.StateWorking},
		Freshness:  statuspkg.FreshnessFresh,
		Confidence: 0.95,
	}}
	if !assignmentObservationShowsWorking(&assignment.Assignment{Status: assignment.StatusAssigned}, base) {
		t.Fatal("fresh working observation should promote an assigned row")
	}

	for _, test := range []struct {
		name   string
		status assignment.AssignmentStatus
		mutate func(*statuspkg.PaneObservation)
	}{
		{name: "nil assignment", status: assignment.StatusAssigned},
		{name: "already working", status: assignment.StatusWorking},
		{name: "stale", status: assignment.StatusAssigned, mutate: func(observation *statuspkg.PaneObservation) {
			observation.Current.Freshness = statuspkg.FreshnessStale
		}},
		{name: "capture error", status: assignment.StatusAssigned, mutate: func(observation *statuspkg.PaneObservation) {
			observation.Current.Error = "capture failed"
		}},
		{name: "idle", status: assignment.StatusAssigned, mutate: func(observation *statuspkg.PaneObservation) {
			observation.Current.Status.State = statuspkg.StateIdle
		}},
		{name: "zero confidence", status: assignment.StatusAssigned, mutate: func(observation *statuspkg.PaneObservation) {
			observation.Current.Confidence = 0
		}},
		{name: "below actionable confidence", status: assignment.StatusAssigned, mutate: func(observation *statuspkg.PaneObservation) {
			observation.Current.Confidence = 0.74
		}},
		{name: "invalid confidence", status: assignment.StatusAssigned, mutate: func(observation *statuspkg.PaneObservation) {
			observation.Current.Confidence = 1.1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := base
			if test.mutate != nil {
				test.mutate(&observation)
			}
			var current *assignment.Assignment
			if test.name != "nil assignment" {
				current = &assignment.Assignment{Status: test.status}
			}
			if assignmentObservationShowsWorking(current, observation) {
				t.Fatalf("assignmentObservationShowsWorking(%s) = true", test.name)
			}
		})
	}
}

func TestResolveAssignmentPaneRejectsLegacyIndexWithTypedMigrationError(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", WindowIndex: 0, Index: 0},
		{ID: "%9", WindowIndex: 1, Index: 0},
	}
	legacy := &assignment.Assignment{BeadID: "ntm-legacy-pane", Pane: 0, Status: assignment.StatusAssigned}
	_, err := resolveAssignmentPane("proj", legacy, panes)
	var migrationErr *assignment.PaneIdentityMigrationError
	if !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) || !errors.As(err, &migrationErr) {
		t.Fatalf("resolve legacy error=%v, want typed migration error", err)
	}
	if migrationErr.BeadID != legacy.BeadID || migrationErr.Pane != legacy.Pane {
		t.Fatalf("migration error=%+v", migrationErr)
	}

	store := assignment.NewStore("completion-legacy-no-mutation")
	store.Assignments[legacy.BeadID] = legacy
	detector := New("completion-legacy-no-mutation", store)
	event, checkErr := detector.checkAssignment(t.Context(), legacy)
	if event != nil || !errors.Is(checkErr, assignment.ErrPaneIdentityMigrationRequired) {
		t.Fatalf("check legacy event=%+v error=%v", event, checkErr)
	}
	if current := store.Get(legacy.BeadID); current == nil || current.Status != assignment.StatusAssigned {
		t.Fatalf("legacy rejection mutated assignment: %+v", current)
	}
}

func TestCheckNowRejectsLegacyPaneAssignmentsWithMigrationError(t *testing.T) {
	store := assignment.NewStore("completion-ambiguous")
	now := time.Now().UTC()
	store.Assignments["bd-a"] = &assignment.Assignment{BeadID: "bd-a", Pane: 0, Status: assignment.StatusAssigned, AssignedAt: now}
	store.Assignments["bd-b"] = &assignment.Assignment{BeadID: "bd-b", Pane: 0, Status: assignment.StatusAssigned, AssignedAt: now}
	detector := New("completion-ambiguous", store)
	if _, err := detector.CheckNow("%1"); !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) {
		t.Fatalf("CheckNow error=%v, want migration requirement", err)
	}
}

func TestBrAvailableCaching(t *testing.T) {
	d := New("test-session", nil)

	// First call checks availability
	result1 := d.isBrAvailable()

	// Second call should use cache
	result2 := d.isBrAvailable()

	if result1 != result2 {
		t.Error("isBrAvailable should return consistent results")
	}

	// Verify cache is set
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.brAvailable == nil {
		t.Error("brAvailable cache should be set after first call")
	}
}

func TestIsBrAvailable_DoesNotWriteStdoutWhenUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	d := New("test-session", nil)
	output := captureStdout(t, func() {
		if d.isBrAvailable() {
			t.Fatal("expected br to be unavailable in isolated PATH")
		}
	})

	if output != "" {
		t.Fatalf("expected no stdout output, got %q", output)
	}
}

// TestConcurrentDedup tests concurrent access to the deduplication mechanism
// to verify thread-safety under race conditions. Run with: go test -race
func TestConcurrentDedup(t *testing.T) {
	d := New("test-session", nil)
	d.Config.DedupWindow = 100 * time.Millisecond

	var wg sync.WaitGroup
	numGoroutines := 10
	eventsPerGoroutine := 20

	// Concurrent writes to recentEvents
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				beadID := fmt.Sprintf("bd-%d-%d", goroutineID, j)
				d.mu.Lock()
				d.recentEvents[beadID] = time.Now()
				d.mu.Unlock()

				// Also do some concurrent reads
				d.mu.RLock()
				_ = d.recentEvents[beadID]
				d.mu.RUnlock()
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				d.mu.RLock()
				_ = len(d.recentEvents)
				d.mu.RUnlock()
			}
		}()
	}

	wg.Wait()

	// Verify all events were recorded
	d.mu.RLock()
	expectedCount := numGoroutines * eventsPerGoroutine
	actualCount := len(d.recentEvents)
	d.mu.RUnlock()

	if actualCount != expectedCount {
		t.Errorf("expected %d events, got %d", expectedCount, actualCount)
	}
}

// TestConcurrentActivityTracking tests concurrent access to activity tracker
func TestConcurrentActivityTracking(t *testing.T) {
	d := New("test-session", nil)

	var wg sync.WaitGroup
	numGoroutines := 10
	operationsPerGoroutine := 20

	// Concurrent activity tracker updates
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(pane int) {
			defer wg.Done()
			key := fmt.Sprintf("%%%d", pane)
			for j := 0; j < operationsPerGoroutine; j++ {
				d.mu.Lock()
				if d.activityTracker[key] == nil {
					d.activityTracker[key] = &activityState{}
				}
				d.activityTracker[key].lastOutputTime = time.Now()
				d.activityTracker[key].lastOutput = fmt.Sprintf("output-%d", j)
				d.mu.Unlock()

				// Concurrent read
				d.mu.RLock()
				_ = d.activityTracker[key]
				d.mu.RUnlock()
			}
		}(i)
	}

	wg.Wait()

	// Verify all panes were tracked
	d.mu.RLock()
	actualPanes := len(d.activityTracker)
	d.mu.RUnlock()

	if actualPanes != numGoroutines {
		t.Errorf("expected %d panes tracked, got %d", numGoroutines, actualPanes)
	}
}

func TestAddFailurePatternInvalid(t *testing.T) {
	t.Parallel()

	d := New("test-session", nil)

	err := d.AddFailurePattern(`[invalid`)
	if err == nil {
		t.Error("AddFailurePattern should fail for invalid regex")
	}
}

func TestCheckAllNilStore(t *testing.T) {
	t.Parallel()

	d := New("test-session", nil)
	d.Store = nil

	// Should not panic, just return early
	events := make(chan CompletionEvent, 10)
	ctx := context.Background()
	d.checkAll(ctx, events)

	// Channel should be empty
	select {
	case <-events:
		t.Error("checkAll with nil store should not emit events")
	default:
		// Expected - no events emitted
	}
}

func TestCheckAllEmptyStore(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	d := New("test-session", store)

	events := make(chan CompletionEvent, 10)
	ctx := context.Background()
	d.checkAll(ctx, events)

	// Channel should be empty (no active assignments)
	select {
	case <-events:
		t.Error("checkAll with empty store should not emit events")
	default:
		// Expected - no events emitted
	}
}

func TestAssignmentEligibleForCompletionScanRequiresDispatchedLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		row  *assignment.Assignment
		want bool
	}{
		{name: "nil"},
		{name: "claiming", row: &assignment.Assignment{Status: assignment.StatusClaiming}},
		{name: "claimed", row: &assignment.Assignment{Status: assignment.StatusClaimed}},
		{name: "assigned", row: &assignment.Assignment{Status: assignment.StatusAssigned}, want: true},
		{name: "working", row: &assignment.Assignment{Status: assignment.StatusWorking}, want: true},
		{name: "assigned clearing", row: &assignment.Assignment{Status: assignment.StatusAssigned, ClearState: assignment.ClearStateReservationReleasing}},
		{name: "working leases released", row: &assignment.Assignment{Status: assignment.StatusWorking, ClearState: assignment.ClearStateLeasesReleased}},
		{name: "completed", row: &assignment.Assignment{Status: assignment.StatusCompleted}},
		{name: "failed", row: &assignment.Assignment{Status: assignment.StatusFailed}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := assignmentEligibleForCompletionScan(test.row); got != test.want {
				t.Fatalf("assignmentEligibleForCompletionScan(%+v) = %t, want %t", test.row, got, test.want)
			}
		})
	}
}

func TestCheckAllContextCancelled(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	// Add an assignment that will be checked
	store.Assign("bd-test", "Test Bead", 0, "claude", "agent-1", "test prompt")

	d := New("test-session", store)

	events := make(chan CompletionEvent, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Should return early without processing
	d.checkAll(ctx, events)

	// Give a moment for any processing
	time.Sleep(10 * time.Millisecond)

	// Should not block or panic
	select {
	case <-events:
		// May or may not receive event depending on timing
	default:
		// Expected in most cases
	}
}

func TestNewWithConfigCustomSettings(t *testing.T) {
	t.Parallel()

	cfg := DetectionConfig{
		PollInterval:      1 * time.Second,
		IdleThreshold:     60 * time.Second,
		RetryOnError:      false,
		RetryInterval:     5 * time.Second,
		MaxRetries:        5,
		DedupWindow:       10 * time.Second,
		GracefulDegrading: false,
		CaptureLines:      100,
	}

	d := NewWithConfig("custom-session", nil, cfg)

	if d.Config.PollInterval != 1*time.Second {
		t.Errorf("PollInterval = %v, want 1s", d.Config.PollInterval)
	}
	if d.Config.IdleThreshold != 60*time.Second {
		t.Errorf("IdleThreshold = %v, want 60s", d.Config.IdleThreshold)
	}
	if d.Config.RetryOnError {
		t.Error("RetryOnError should be false")
	}
	if d.Config.GracefulDegrading {
		t.Error("GracefulDegrading should be false")
	}
	if d.Config.CaptureLines != 100 {
		t.Errorf("CaptureLines = %d, want 100", d.Config.CaptureLines)
	}
}

func TestNewWithConfigAppliesDefaultsAndWatchIsUsable(t *testing.T) {
	t.Parallel()

	d := NewWithConfig("default-config", nil, DetectionConfig{})
	want := DefaultConfig()

	if d.Config.PollInterval != want.PollInterval {
		t.Errorf("PollInterval = %v, want default %v", d.Config.PollInterval, want.PollInterval)
	}
	if d.Config.IdleThreshold != want.IdleThreshold {
		t.Errorf("IdleThreshold = %v, want default %v", d.Config.IdleThreshold, want.IdleThreshold)
	}
	if d.Config.RetryInterval != want.RetryInterval {
		t.Errorf("RetryInterval = %v, want default %v", d.Config.RetryInterval, want.RetryInterval)
	}
	if d.Config.MaxRetries != want.MaxRetries {
		t.Errorf("MaxRetries = %d, want default %d", d.Config.MaxRetries, want.MaxRetries)
	}
	if d.Config.DedupWindow != want.DedupWindow {
		t.Errorf("DedupWindow = %v, want default %v", d.Config.DedupWindow, want.DedupWindow)
	}
	if d.Config.CompletionLeaseDuration != want.CompletionLeaseDuration {
		t.Errorf("CompletionLeaseDuration = %v, want default %v", d.Config.CompletionLeaseDuration, want.CompletionLeaseDuration)
	}
	if d.Config.CaptureLines != want.CaptureLines {
		t.Errorf("CaptureLines = %d, want default %d", d.Config.CaptureLines, want.CaptureLines)
	}

	ctx, cancel := context.WithCancel(t.Context())
	events := d.Watch(ctx)
	cancel()

	select {
	case _, open := <-events:
		if open {
			t.Error("Watch returned an event after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("Watch did not stop after cancellation")
	}
}

func TestCheckNowWithActiveAssignment(t *testing.T) {
	testutil.AcquireGlobalTmuxTestLockForTest(t)
	t.Setenv("HOME", t.TempDir())

	session := fmt.Sprintf("completion-active-%d", time.Now().UnixNano())
	store := assignment.NewStore(session)
	if _, err := store.Assign("bd-test", "Test Bead", 5, "claude", "agent-1", "test prompt"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	store.Assignments["bd-test"].OccupancyKey = "%5"
	store.Assignments["bd-test"].DispatchTarget = "%5"
	if err := store.Save(); err != nil {
		t.Fatalf("persist canonical pane identity: %v", err)
	}

	d := New(session, store)

	// CheckNow will fail because we can't query real tmux panes,
	// but it should find the assignment and attempt to check it
	event, err := d.CheckNow("%5")
	// The error comes from tmux.GetPanes failing, not from assignment lookup
	// In test environment without tmux, this returns nil event
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// Event may be nil if tmux isn't available
	_ = event
}

func TestIdleDetectionNoBurstActive(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond
	d := NewWithConfig("test-session", store, cfg)

	now := time.Now()
	a := &assignment.Assignment{
		BeadID:       "bd-test",
		Pane:         0,
		OccupancyKey: "%1",
		AgentType:    "claude",
		AssignedAt:   now,
	}

	// Initialize state
	d.checkIdle(a, "initial", now)

	// Same output - should trigger completion because burstActive is initialized to true
	time.Sleep(15 * time.Millisecond)
	event := d.checkIdle(a, "initial", now)
	if event == nil {
		t.Error("Idle detection should trigger since burstActive is initialized to true")
	}
}

func TestActivityStateFields(t *testing.T) {
	t.Parallel()

	state := &activityState{
		lastOutputTime: time.Now(),
		lastOutput:     "test output",
		burstStarted:   time.Now().Add(-1 * time.Minute),
		burstActive:    true,
	}

	if state.lastOutput != "test output" {
		t.Errorf("lastOutput = %q, want 'test output'", state.lastOutput)
	}
	if !state.burstActive {
		t.Error("burstActive should be true")
	}
}

func TestDetectionConfigFields(t *testing.T) {
	t.Parallel()

	cfg := DetectionConfig{
		PollInterval:      1 * time.Second,
		IdleThreshold:     30 * time.Second,
		RetryOnError:      true,
		RetryInterval:     5 * time.Second,
		MaxRetries:        10,
		DedupWindow:       3 * time.Second,
		GracefulDegrading: true,
		CaptureLines:      25,
	}

	if cfg.MaxRetries != 10 {
		t.Errorf("MaxRetries = %d, want 10", cfg.MaxRetries)
	}
	if cfg.DedupWindow != 3*time.Second {
		t.Errorf("DedupWindow = %v, want 3s", cfg.DedupWindow)
	}
}

func TestCompletionEventWithFailure(t *testing.T) {
	t.Parallel()

	event := CompletionEvent{
		Pane:       1,
		AgentType:  "codex",
		BeadID:     "bd-fail",
		Method:     MethodPaneLost,
		Timestamp:  time.Now(),
		Duration:   10 * time.Minute,
		Output:     "last output before crash",
		IsFailed:   true,
		FailReason: "agent crashed unexpectedly",
	}

	if !event.IsFailed {
		t.Error("IsFailed should be true")
	}
	if event.FailReason != "agent crashed unexpectedly" {
		t.Errorf("FailReason = %q, want 'agent crashed unexpectedly'", event.FailReason)
	}
	if event.Method != MethodPaneLost {
		t.Errorf("Method = %v, want %v", event.Method, MethodPaneLost)
	}
}

func TestMatchCompletionPatternsConcurrent(t *testing.T) {
	t.Parallel()

	d := New("test-session", nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				d.matchCompletionPatterns("task bd-1234 done successfully")
				d.matchCompletionPatterns("no match here")
			}
		}()
	}
	wg.Wait()
}

func TestMatchFailurePatternsConcurrent(t *testing.T) {
	t.Parallel()

	d := New("test-session", nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				d.matchFailurePatterns("unable to complete task")
				d.matchFailurePatterns("everything is fine")
			}
		}()
	}
	wg.Wait()
}

// installFakeBr writes a fake `br` executable into a fresh dir, prepends that
// dir to PATH for the test, and returns the dir. The fake `br` emits a JSON
// array whose single object carries the given status, so checkBeadClosed sees
// the bead as that status. Skips the test if the dir cannot be prepared.
func installFakeBr(t *testing.T, status string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"# Fake br for tests: always reports a bead with a fixed status.\n" +
		"printf '%s' '[{\"id\":\"bd-x\",\"status\":\"" + status + "\"}]'\n"
	path := dir + "/br"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installFakeBrScript(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/br"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestCheckBeadClosedRetriesTransientCommandFailures(t *testing.T) {
	dir := installFakeBrScript(t, "#!/bin/sh\n"+
		"attempt_file=\"$FAKE_BR_ATTEMPT_FILE\"\n"+
		"attempt=$(cat \"$attempt_file\" 2>/dev/null || printf '0')\n"+
		"attempt=$((attempt + 1))\n"+
		"printf '%s' \"$attempt\" > \"$attempt_file\"\n"+
		"if [ \"$attempt\" -lt 3 ]; then\n"+
		"  printf '%s\\n' 'temporary br failure' >&2\n"+
		"  exit 1\n"+
		"fi\n"+
		"printf '%s' '{\"status\":\"closed\"}'\n")
	attemptFile := dir + "/attempts"
	t.Setenv("FAKE_BR_ATTEMPT_FILE", attemptFile)

	cfg := DefaultConfig()
	cfg.RetryInterval = time.Millisecond
	cfg.MaxRetries = 2
	d := NewWithConfig("retry-test", nil, cfg)

	closed, err := d.checkBeadClosed(t.Context(), "bd-retry")
	if err != nil {
		t.Fatalf("checkBeadClosed: %v", err)
	}
	if !closed {
		t.Fatal("checkBeadClosed reported open after the fake br returned closed")
	}
	attempts, err := os.ReadFile(attemptFile)
	if err != nil {
		t.Fatalf("read fake br attempts: %v", err)
	}
	if got := strings.TrimSpace(string(attempts)); got != "3" {
		t.Fatalf("br invocations = %s, want 3 (initial attempt plus two retries)", got)
	}
}

func TestCheckBeadClosedCanDisableRetries(t *testing.T) {
	dir := installFakeBrScript(t, "#!/bin/sh\n"+
		"printf '%s' x >> \"$FAKE_BR_ATTEMPT_FILE\"\n"+
		"printf '%s\\n' 'persistent br failure' >&2\n"+
		"exit 1\n")
	attemptFile := dir + "/attempts"
	t.Setenv("FAKE_BR_ATTEMPT_FILE", attemptFile)

	cfg := DefaultConfig()
	cfg.RetryOnError = false
	cfg.RetryInterval = time.Millisecond
	cfg.MaxRetries = 3
	d := NewWithConfig("no-retry-test", nil, cfg)

	if _, err := d.checkBeadClosed(t.Context(), "bd-no-retry"); err == nil {
		t.Fatal("checkBeadClosed succeeded despite fake br failure")
	}
	attempts, err := os.ReadFile(attemptFile)
	if err != nil {
		t.Fatalf("read fake br attempts: %v", err)
	}
	if got := string(attempts); got != "x" {
		t.Fatalf("br invocations = %q, want one when RetryOnError is false", got)
	}
}

// TestDetectorReloadsStoreObservesPostStartupDispatch is the Fix-1 guard. The
// completion detector is handed the watch loop's store instance at
// construction, but post-startup dispatches are recorded by a SEPARATE store
// instance (executeAssignmentsEnhanced does its own LoadStore + Assign + Save).
// Before the fix the detector's view was frozen at startup and it never saw
// anything dispatched later. checkAll now reloads the store from disk at the
// top of each tick; this test writes a NEW assignment through a second store
// instance and asserts the detector's store observes it after checkAll runs.
func TestDetectorReloadsStoreObservesPostStartupDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	session := "reload-test-session"

	// The detector holds store A (as the watch loop would).
	storeA, err := assignment.LoadStore(session)
	if err != nil {
		t.Fatalf("LoadStore A: %v", err)
	}
	d := New(session, storeA)

	// At startup the detector sees no assignments.
	if got := len(storeA.ListActive()); got != 0 {
		t.Fatalf("expected 0 active assignments at startup, got %d", got)
	}

	// A SEPARATE store instance (store B) records a post-startup dispatch and
	// persists it to disk — exactly what executeAssignmentsEnhanced does.
	storeB, err := assignment.LoadStore(session)
	if err != nil {
		t.Fatalf("LoadStore B: %v", err)
	}
	if _, err := storeB.Assign("bd-after-startup", "late bead", 1, "claude", "claude_1", "do work"); err != nil {
		t.Fatalf("storeB.Assign: %v", err)
	}

	// Before reload, store A still does not see it (frozen view).
	if got := len(storeA.ListActive()); got != 0 {
		t.Fatalf("store A should not see the dispatch before reload, got %d", got)
	}

	// Run a tick. tmux is unavailable / the pane does not exist, so no events
	// fire, but checkAll's reload must pull in the on-disk assignment.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan CompletionEvent, 4)
	d.checkAll(ctx, events)

	active := storeA.ListActive()
	if len(active) != 1 {
		t.Fatalf("after checkAll reload, store A should observe 1 active assignment, got %d", len(active))
	}
	if active[0].BeadID != "bd-after-startup" {
		t.Errorf("observed bead = %q, want %q", active[0].BeadID, "bd-after-startup")
	}
}

// TestCompletionPatternRequiresBeadClosedConfirmation is the Fix-2 prompt-echo
// guard. The completion patterns (e.g. `br\s+close`) also match the dispatch
// prompt's OWN ECHO ("run `br close <id>`"). A crashed/slow agent whose pane
// still shows that prompt must NOT be declared complete unless br confirms the
// bead is actually closed. This drives checkAssignment against a real ephemeral
// tmux pane whose content is only the prompt echo.
func TestCompletionPatternRequiresBeadClosedConfirmation(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	testutil.AcquireGlobalTmuxTestLockForTest(t)
	t.Setenv("HOME", t.TempDir())

	session := "promptecho-session"
	_ = tmux.KillSession(session)
	if err := tmux.CreateSession(session, t.TempDir()); err != nil {
		t.Skipf("CreateSession failed (host-sensitive): %v", err)
	}
	t.Cleanup(func() { killSessionAndWaitForTest(t, session) })

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Skipf("GetPanes failed: %v", err)
	}
	pane := panes[0]

	// Put the dispatch prompt ECHO (and nothing else) in the pane: the prompt
	// instructs the agent to run `br close`, which the completion pattern
	// `br\s+(close|...)` matches even though the agent has done no work.
	promptEcho := "Work on bead bd-echo: fix the thing. When done, run br close bd-echo."
	if err := tmux.SendKeys(pane.ID, promptEcho, false); err != nil {
		t.Skipf("SendKeys failed: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	// Sanity: the prompt echo really does trip the completion pattern — this is
	// the trap the fix defends against.
	out, _ := tmux.CapturePaneOutput(pane.ID, 50)
	d := New(session, assignment.NewStore(session))
	if !d.matchCompletionPatterns(out) {
		t.Skip("prompt echo did not render the completion phrase in this environment")
	}

	// Fake br reports the bead as OPEN — so the pattern match must NOT be
	// trusted as a success.
	installFakeBr(t, "open")
	brAvail := true
	d.brAvailable = &brAvail

	a := &assignment.Assignment{
		BeadID:       "bd-echo",
		Pane:         pane.Index,
		OccupancyKey: pane.ID,
		AgentType:    "claude",
		AssignedAt:   time.Now(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	event, checkErr := d.checkAssignment(ctx, a)
	if checkErr != nil {
		t.Fatalf("checkAssignment: %v", checkErr)
	}
	if event != nil && !event.IsFailed && event.Method == MethodPatternMatch {
		t.Fatalf("prompt echo wrongly marked complete: %+v", event)
	}
}

// TestIdleTimeoutWithOpenBeadReportsFailed is the second half of Fix-2: an
// idle/stalled agent whose bead is NOT closed must be reported FAILED (so the
// pane is released and the bead reassigned), not a silent success.
func TestIdleTimeoutWithOpenBeadReportsFailed(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	testutil.AcquireGlobalTmuxTestLockForTest(t)
	t.Setenv("HOME", t.TempDir())

	session := "idlefail-session"
	_ = tmux.KillSession(session)
	if err := tmux.CreateSession(session, t.TempDir()); err != nil {
		t.Skipf("CreateSession failed (host-sensitive): %v", err)
	}
	t.Cleanup(func() { killSessionAndWaitForTest(t, session) })

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Skipf("GetPanes failed: %v", err)
	}
	pane := panes[0]

	// Fake br: bead is OPEN (not closed) — an idle timeout here is a stall.
	installFakeBr(t, "open")

	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond
	cfg.CaptureLines = 50
	d := NewWithConfig(session, assignment.NewStore(session), cfg)
	brAvail := true
	d.brAvailable = &brAvail

	a := &assignment.Assignment{
		BeadID:       "bd-stalled",
		Pane:         pane.Index,
		OccupancyKey: pane.ID,
		AgentType:    "claude",
		AssignedAt:   time.Now(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Prime the idle tracker, then let the pane go quiet past the threshold.
	_, _ = d.checkAssignment(ctx, a)
	time.Sleep(20 * time.Millisecond)

	event, checkErr := d.checkAssignment(ctx, a)
	if checkErr != nil {
		t.Fatalf("checkAssignment: %v", checkErr)
	}
	if event == nil {
		t.Skip("no idle event produced in this environment (pane churn)")
	}
	if event.Method == MethodBeadClosed {
		t.Fatalf("bead was reported closed but fake br says open: %+v", event)
	}
	if event.Method == MethodIdle && !event.IsFailed {
		t.Fatalf("idle timeout with an OPEN bead must be reported FAILED, got success: %+v", event)
	}
	if event.Method == MethodIdle && event.IsFailed && event.FailReason == "" {
		t.Errorf("failed idle event should carry a fail reason")
	}
}

// GH#238 / ntm-a2t8: a quietly-working agent (silent while an external
// subprocess runs) must never be stamped failed. Any observation that cannot
// CONFIRM confident fresh idleness — stale capture, unknown state, weak
// confidence — clears the inactivity timer, so only continuously-confirmed
// idle intervals can fail an assignment.
func TestIdleDetectionUnconfirmedObservationsClearTimer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond

	base := statuspkg.PaneObservation{Current: statuspkg.StateObservation{
		Status:     statuspkg.AgentStatus{State: statuspkg.StateIdle},
		Freshness:  statuspkg.FreshnessFresh,
		Confidence: 0.95,
	}}
	stale := base
	stale.Current.Freshness = statuspkg.FreshnessStale
	unknown := base
	unknown.Current.Status.State = statuspkg.StateUnknown
	weak := base
	weak.Current.Confidence = 0.1

	cases := []struct {
		name        string
		unconfirmed statuspkg.PaneObservation
	}{
		{"stale capture", stale},
		{"unknown state", unknown},
		{"weak confidence", weak},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewWithConfig("test-session", assignment.NewStore("test-session"), cfg)
			now := time.Now()
			a := &assignment.Assignment{BeadID: "bd-quiet", Pane: 0, OccupancyKey: "%1", AssignedAt: now}

			if event := d.checkIdleWhenSafe(a, "unchanged", now, base); event != nil {
				t.Fatalf("init observation produced event: %+v", event)
			}
			time.Sleep(15 * time.Millisecond)
			if event := d.checkIdleWhenSafe(a, "unchanged", now, tc.unconfirmed); event != nil {
				t.Fatalf("unconfirmed observation stamped failure: %+v", event)
			}
			if len(d.activityTracker) != 0 {
				t.Fatalf("unconfirmed observation retained the idle timer: %+v", d.activityTracker)
			}
			// A fresh confirmed-idle interval must start from zero.
			time.Sleep(15 * time.Millisecond)
			if event := d.checkIdleWhenSafe(a, "unchanged", now, base); event != nil {
				t.Fatalf("restarted interval inherited stale timeout: %+v", event)
			}
		})
	}
}

// killSessionAndWaitForTest kills a test tmux session and blocks until its
// pane processes have actually exited.
//
// These tests point both HOME and the session's working directory at
// t.TempDir()s. A shell still shutting down writes its history file
// (.zsh_history) into $HOME WHILE t.TempDir's cleanup is removing it, and
// TempDir's RemoveAll failure ("directory not empty") marks the test failed
// even though every assertion passed. That is the real cause of these two
// suites' intermittent failures — the accompanying t.Skip is benign (bd-hzmk0).
//
// Waiting on the SESSION is not enough: tmux removes it from its own list
// synchronously while the shells die asynchronously. The pane PIDs are the
// thing that actually has to be gone, because the shell is the writer. They
// must be captured BEFORE the kill, since afterwards there is nothing left to
// ask.
//
// Registering this with t.Cleanup AFTER the TempDirs are created is what makes
// it correct: cleanups run LIFO, so the wait completes before any removal.
func killSessionAndWaitForTest(t *testing.T, session string) {
	t.Helper()

	var pids []int
	if panes, err := tmux.GetPanes(session); err == nil {
		for _, pane := range panes {
			if pane.PID > 0 {
				pids = append(pids, pane.PID)
			}
		}
	}

	if err := tmux.KillSession(session); err != nil && tmux.SessionExists(session) {
		t.Logf("kill session %s: %v", session, err)
	}

	deadline := time.Now().Add(testutil.ScaleTimeout(5 * time.Second))
	for time.Now().Before(deadline) {
		if !anyProcessAliveForTest(pids) && !tmux.SessionExists(session) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("session %s pane processes %v still alive after kill; TempDir cleanup may race a live shell", session, pids)
}

func anyProcessAliveForTest(pids []int) bool {
	for _, pid := range pids {
		if process.IsAlive(pid) {
			return true
		}
	}
	return false
}
