package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/output"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

type scriptedSpawnObserver struct {
	mu           sync.Mutex
	observations []statuspkg.SessionObservation
	errors       []error
	calls        int
}

type blockingSpawnObserver struct {
	entered chan struct{}
	once    sync.Once
}

func (o *blockingSpawnObserver) Observe(ctx context.Context, _ string) (statuspkg.SessionObservation, error) {
	o.once.Do(func() { close(o.entered) })
	<-ctx.Done()
	return statuspkg.SessionObservation{}, ctx.Err()
}

func (o *scriptedSpawnObserver) Observe(_ context.Context, _ string) (statuspkg.SessionObservation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.observations) == 0 {
		return statuspkg.SessionObservation{}, errors.New("no scripted observation")
	}
	index := o.calls
	if index >= len(o.observations) {
		index = len(o.observations) - 1
	}
	o.calls++
	var err error
	if index < len(o.errors) {
		err = o.errors[index]
	}
	return o.observations[index], err
}

type recordingSpawnDispatcher struct {
	mu       sync.Mutex
	messages []string
	panes    []string
	failAt   int
}

func (d *recordingSpawnDispatcher) Dispatch(_ context.Context, paneID, message string) (dispatchsvc.Receipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.panes = append(d.panes, paneID)
	d.messages = append(d.messages, message)
	if d.failAt > 0 && len(d.messages) == d.failAt {
		return dispatchsvc.Receipt{}, errors.New("scripted dispatch failure")
	}
	pane := tmux.Pane{ID: paneID, WindowIndex: 0, Index: 1, Type: tmux.AgentClaude}
	return dispatchsvc.Receipt{
		Target:   dispatchsvc.Target{Pane: pane, Ref: pane.Ref(), Address: "1", AgentType: tmux.AgentClaude},
		Status:   dispatchsvc.ReceiptDelivered,
		Protocol: dispatchsvc.ProtocolDoubleEnter,
	}, nil
}

func testSpawnPaneObservation(now time.Time, pane tmux.Pane, state statuspkg.AgentState) statuspkg.PaneObservation {
	confidence := 0.95
	rawOutput := ""
	if state == statuspkg.StateUnknown {
		confidence = 0.25
	} else if state == statuspkg.StateIdle {
		switch pane.Type.Canonical() {
		case tmux.AgentClaude:
			rawOutput = "Claude Code v0.0.0\n❯ "
		case tmux.AgentCodex:
			rawOutput = "codex>"
		case tmux.AgentGemini:
			rawOutput = "gemini>"
		default:
			rawOutput = "agent>"
		}
	}
	return statuspkg.PaneObservation{
		Pane:      pane.Ref(),
		PaneName:  pane.Title,
		AgentType: string(pane.Type.Canonical()),
		Metadata:  pane,
		Current: statuspkg.StateObservation{
			Status: statuspkg.AgentStatus{
				PaneID: pane.ID, PaneName: pane.Title, AgentType: string(pane.Type.Canonical()),
				State: state, UpdatedAt: now,
			},
			ObservedAt: now,
			Freshness:  statuspkg.FreshnessFresh,
			Confidence: confidence,
		},
		RawOutput: rawOutput,
	}
}

func testSpawnSessionObservation(now time.Time, panes ...statuspkg.PaneObservation) statuspkg.SessionObservation {
	return statuspkg.SessionObservation{
		Session: "spawn-test", ObservedAt: now, Complete: true,
		Panes: panes, Failures: []statuspkg.ObservationFailure{},
	}
}

func TestNewSpawnState(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 3)

	if state.BatchID != "batch-123" {
		t.Errorf("expected BatchID 'batch-123', got %s", state.BatchID)
	}
	if state.StaggerSeconds != 90 {
		t.Errorf("expected StaggerSeconds 90, got %d", state.StaggerSeconds)
	}
	if state.TotalAgents != 3 {
		t.Errorf("expected TotalAgents 3, got %d", state.TotalAgents)
	}
	if state.StartedAt.IsZero() {
		t.Error("expected non-zero StartedAt")
	}
}

func TestSpawnStateAddPrompt(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 3)
	scheduledAt := time.Now().Add(90 * time.Second)

	state.AddPrompt("proj__cc_1", "pane-1", 1, scheduledAt)

	if len(state.Prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(state.Prompts))
	}

	p := state.Prompts[0]
	if p.Pane != "proj__cc_1" {
		t.Errorf("expected pane 'proj__cc_1', got %s", p.Pane)
	}
	if p.PaneID != "pane-1" {
		t.Errorf("expected pane ID 'pane-1', got %s", p.PaneID)
	}
	if p.Order != 1 {
		t.Errorf("expected order 1, got %d", p.Order)
	}
	if p.Sent {
		t.Error("expected sent to be false")
	}
}

func TestSpawnStateMarkSent(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 2)
	now := time.Now()

	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(90*time.Second))

	// Mark first prompt as sent
	state.MarkSent("pane-1")

	if !state.Prompts[0].Sent {
		t.Error("expected first prompt to be marked as sent")
	}
	if state.Prompts[0].SentAt.IsZero() {
		t.Error("expected SentAt to be set")
	}
	if state.Prompts[1].Sent {
		t.Error("expected second prompt to not be sent yet")
	}

	// Mark second prompt as sent - should complete the spawn
	state.MarkSent("pane-2")

	if !state.Prompts[1].Sent {
		t.Error("expected second prompt to be marked as sent")
	}
	if state.CompletedAt.IsZero() {
		t.Error("expected CompletedAt to be set when all prompts sent")
	}
}

func TestSpawnStatePendingCount(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 3)
	now := time.Now()

	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(90*time.Second))
	state.AddPrompt("proj__cc_3", "pane-3", 3, now.Add(180*time.Second))

	if state.PendingCount() != 3 {
		t.Errorf("expected 3 pending, got %d", state.PendingCount())
	}

	state.MarkSent("pane-1")
	if state.PendingCount() != 2 {
		t.Errorf("expected 2 pending, got %d", state.PendingCount())
	}

	state.MarkSent("pane-2")
	state.MarkSent("pane-3")
	if state.PendingCount() != 0 {
		t.Errorf("expected 0 pending, got %d", state.PendingCount())
	}
}

func TestSpawnStateSaveAndLoad(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create and populate spawn state
	state := NewSpawnState("batch-test", 60, 2)
	now := time.Now()
	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(60*time.Second))
	state.MarkSent("pane-1")

	// Save state
	if err := state.Save(tmpDir); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	// Verify file exists
	path := filepath.Join(tmpDir, ".ntm", "spawn-state.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("spawn state file not created")
	}

	// Load state
	loaded, err := LoadSpawnState(tmpDir)
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state is nil")
	}

	// Verify loaded state
	if loaded.BatchID != "batch-test" {
		t.Errorf("expected BatchID 'batch-test', got %s", loaded.BatchID)
	}
	if loaded.StaggerSeconds != 60 {
		t.Errorf("expected StaggerSeconds 60, got %d", loaded.StaggerSeconds)
	}
	if len(loaded.Prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(loaded.Prompts))
	}
	if !loaded.Prompts[0].Sent {
		t.Error("expected first prompt to be sent")
	}
	if loaded.Prompts[1].Sent {
		t.Error("expected second prompt to not be sent")
	}
}

func TestLoadSpawnStateNotExists(t *testing.T) {
	tmpDir := t.TempDir()

	state, err := LoadSpawnState(tmpDir)
	if err != nil {
		t.Errorf("expected no error for missing file, got %v", err)
	}
	if state != nil {
		t.Error("expected nil state for missing file")
	}
}

func TestLoadSpawnState_ExpiresCompletedStateAfterGracePeriod(t *testing.T) {
	tmpDir := t.TempDir()

	state := NewSpawnState("batch-test", 60, 1)
	state.MarkComplete()
	state.CompletedAt = time.Now().Add(-(spawnStateCompletionGracePeriod + time.Second))
	if err := state.Save(tmpDir); err != nil {
		t.Fatalf("failed to save expired state: %v", err)
	}

	loaded, err := LoadSpawnState(tmpDir)
	if err != nil {
		t.Fatalf("LoadSpawnState() error = %v", err)
	}
	if loaded != nil {
		t.Fatalf("LoadSpawnState() = %#v, want nil for expired state", loaded)
	}
	if SpawnStateExists(tmpDir) {
		t.Fatal("expected expired spawn state file to be removed")
	}
}

func TestClearSpawnState(t *testing.T) {
	tmpDir := t.TempDir()

	// Create state file
	state := NewSpawnState("batch-test", 60, 1)
	if err := state.Save(tmpDir); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	// Verify file exists
	if !SpawnStateExists(tmpDir) {
		t.Fatal("spawn state should exist")
	}

	// Clear state
	if err := ClearSpawnState(tmpDir); err != nil {
		t.Fatalf("failed to clear state: %v", err)
	}

	// Verify file is gone
	if SpawnStateExists(tmpDir) {
		t.Error("spawn state should not exist after clear")
	}
}

func TestSpawnStateIsComplete(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 1)
	state.AddPrompt("proj__cc_1", "pane-1", 1, time.Now())

	if state.IsComplete() {
		t.Error("expected incomplete before marking sent")
	}

	state.MarkSent("pane-1")

	if !state.IsComplete() {
		t.Error("expected complete after marking all sent")
	}
}

func TestSpawnStateMarkComplete(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 2)
	state.AddPrompt("proj__cc_1", "pane-1", 1, time.Now())
	state.AddPrompt("proj__cc_2", "pane-2", 2, time.Now())

	if state.IsComplete() {
		t.Error("expected incomplete before MarkComplete")
	}

	state.MarkComplete()

	if !state.IsComplete() {
		t.Error("expected complete after MarkComplete")
	}
}

func TestTimeUntilNextPrompt(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 2)
	now := time.Now()

	// All sent - should return 0
	state.AddPrompt("proj__cc_1", "pane-1", 1, now.Add(-10*time.Second)) // Already past
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(30*time.Second))  // 30s from now

	state.MarkSent("pane-1") // Mark first as sent

	// Second prompt is still pending, 30s from now
	remaining := state.TimeUntilNextPrompt()
	if remaining <= 0 || remaining > 31*time.Second {
		t.Errorf("expected remaining ~30s, got %v", remaining)
	}

	state.MarkSent("pane-2")

	// All sent - should return 0
	remaining = state.TimeUntilNextPrompt()
	if remaining != 0 {
		t.Errorf("expected 0 when all sent, got %v", remaining)
	}
}

func TestGetPromptStatuses(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 2)
	now := time.Now()

	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(60*time.Second))

	statuses := state.GetPromptStatuses()

	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	// Verify it's a copy
	state.MarkSent("pane-1")
	if statuses[0].Sent {
		t.Error("copy should not be affected by original changes")
	}
}

func TestWaitForAgentsReadyWithObserverEvidence(t *testing.T) {
	now := time.Now().UTC()
	pane := tmux.Pane{ID: "%41", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "demo__cc_1"}

	t.Run("fresh idle succeeds", func(t *testing.T) {
		observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{
			testSpawnSessionObservation(now, testSpawnPaneObservation(now, pane, statuspkg.StateIdle)),
		}}
		ready, err := waitForAgentsReadyWithObserver(t.Context(), "demo", 0, time.Millisecond, observer)
		if err != nil || ready != 1 {
			t.Fatalf("ready=%d err=%v, want 1,nil", ready, err)
		}
	})

	t.Run("empty capture remains unknown and times out", func(t *testing.T) {
		observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{
			testSpawnSessionObservation(now, testSpawnPaneObservation(now, pane, statuspkg.StateUnknown)),
		}}
		ready, err := waitForAgentsReadyWithObserver(t.Context(), "demo", 0, time.Millisecond, observer)
		if ready != 0 || err == nil || !strings.Contains(err.Error(), "state=unknown") {
			t.Fatalf("ready=%d err=%v, want explicit unknown-state timeout", ready, err)
		}
	})

	t.Run("capture failure is retained in timeout", func(t *testing.T) {
		failed := testSpawnPaneObservation(now, pane, statuspkg.StateUnknown)
		failed.Current.Freshness = statuspkg.FreshnessUnavailable
		failed.Current.Confidence = 0
		failed.Current.Error = "capture pipe closed"
		observation := testSpawnSessionObservation(now, failed)
		observation.Complete = false
		observation.Failures = []statuspkg.ObservationFailure{{PaneID: pane.ID, Stage: "capture", Error: "capture pipe closed"}}
		observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{observation}}
		ready, err := waitForAgentsReadyWithObserver(t.Context(), "demo", 0, time.Millisecond, observer)
		if ready != 0 || err == nil || !strings.Contains(err.Error(), "capture pipe closed") {
			t.Fatalf("ready=%d err=%v, want explicit capture failure", ready, err)
		}
	})
}

func TestReadyAgentPanesRejectsIdleShellBeforeAgentProcessStarts(t *testing.T) {
	now := time.Now().UTC()
	pane := tmux.Pane{
		ID: "%45", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude,
		Title: "demo__cc_1", Command: "zsh",
	}
	observation := testSpawnSessionObservation(now, testSpawnPaneObservation(now, pane, statuspkg.StateIdle))
	observation.Panes[0].RawOutput = "testhost%"
	ready, agents := readyAgentPanesFromObservation(observation)
	if agents != 1 || len(ready) != 0 {
		t.Fatalf("ready=%v agents=%d, want idle shell rejected for one agent pane", ready, agents)
	}
	issues := readinessIssuesForAgentPanes(observation)
	if len(issues) != 1 || !strings.Contains(issues[0], "has not replaced shell") {
		t.Fatalf("readiness issues = %v, want explicit shell-startup evidence", issues)
	}
}

func TestDispatchSpawnPromptSequencePreservesOrderAndCanonicalReceipts(t *testing.T) {
	now := time.Now().UTC()
	pane := tmux.Pane{ID: "%42", WindowIndex: 1, Index: 3, Type: tmux.AgentClaude, Title: "demo__cc_3"}
	idle := testSpawnSessionObservation(now, testSpawnPaneObservation(now, pane, statuspkg.StateIdle))
	observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{idle, idle, idle}}
	dispatcher := &recordingSpawnDispatcher{}
	steps := []spawnPromptStep{
		{Kind: "cass_context", Message: "cass"},
		{Kind: "recovery_context", Message: "recovery"},
		{Kind: "user_prompt", Message: "user"},
	}

	receipts, err := dispatchSpawnPromptSequence(
		t.Context(), "demo", pane.ID, steps, observer, dispatcher, 0, time.Millisecond,
	)
	if err != nil {
		t.Fatalf("dispatchSpawnPromptSequence() error = %v", err)
	}
	if got := strings.Join(dispatcher.messages, ","); got != "cass,recovery,user" {
		t.Fatalf("dispatch order = %q, want cass,recovery,user", got)
	}
	if len(receipts) != 3 {
		t.Fatalf("receipts = %d, want 3", len(receipts))
	}
	for i, receipt := range receipts {
		if receipt.Status != dispatchsvc.ReceiptDelivered || receipt.Target.Ref.StableKey() != pane.ID {
			t.Fatalf("receipt[%d] = %+v, want delivered target %s", i, receipt, pane.ID)
		}
	}
}

func TestDispatchSpawnPromptSequenceStopsAfterFailedRevalidation(t *testing.T) {
	now := time.Now().UTC()
	pane := tmux.Pane{ID: "%43", WindowIndex: 0, Index: 2, Type: tmux.AgentClaude, Title: "demo__cc_2"}
	idle := testSpawnSessionObservation(now, testSpawnPaneObservation(now, pane, statuspkg.StateIdle))
	failedPane := testSpawnPaneObservation(now, pane, statuspkg.StateUnknown)
	failedPane.Current.Freshness = statuspkg.FreshnessUnavailable
	failedPane.Current.Confidence = 0
	failedPane.Current.Error = "capture failed after first send"
	failed := testSpawnSessionObservation(now, failedPane)
	failed.Complete = false
	failed.Failures = []statuspkg.ObservationFailure{{PaneID: pane.ID, Stage: "capture", Error: failedPane.Current.Error}}
	observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{idle, failed}}
	dispatcher := &recordingSpawnDispatcher{}

	receipts, err := dispatchSpawnPromptSequence(
		t.Context(), "demo", pane.ID,
		[]spawnPromptStep{{Kind: "cass_context", Message: "first"}, {Kind: "recovery_context", Message: "must-not-send"}},
		observer, dispatcher, 0, time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "capture failed after first send") {
		t.Fatalf("error = %v, want revalidation capture failure", err)
	}
	if len(receipts) != 1 || len(dispatcher.messages) != 1 || dispatcher.messages[0] != "first" {
		t.Fatalf("receipts=%d messages=%v, want exactly first dispatch", len(receipts), dispatcher.messages)
	}
}

func TestDispatchSpawnPromptSequenceCancellationStopsBlockedObserverWithoutDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	observer := &blockingSpawnObserver{entered: make(chan struct{})}
	dispatcher := &recordingSpawnDispatcher{}
	done := make(chan struct{})
	var receipts []dispatchsvc.Receipt
	var dispatchErr error
	go func() {
		defer close(done)
		receipts, dispatchErr = dispatchSpawnPromptSequence(
			ctx, "demo", "%44",
			[]spawnPromptStep{{Kind: "user_prompt", Message: "must-not-send"}},
			observer, dispatcher, time.Minute, time.Millisecond,
		)
	}()

	select {
	case <-observer.entered:
	case <-time.After(time.Second):
		t.Fatal("observer did not enter its blocking observation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("prompt sequence did not stop after cancellation")
	}
	if !errors.Is(dispatchErr, context.Canceled) {
		t.Fatalf("dispatch error = %v, want context cancellation", dispatchErr)
	}
	if len(receipts) != 0 || len(dispatcher.messages) != 0 {
		t.Fatalf("receipts=%d messages=%v, want zero delivery after cancellation", len(receipts), dispatcher.messages)
	}
}

func TestWaitForAgentsReadyCancellationStopsBlockingObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	observer := &blockingSpawnObserver{entered: make(chan struct{})}
	done := make(chan struct{})
	var ready int
	var waitErr error
	go func() {
		defer close(done)
		ready, waitErr = waitForAgentsReadyWithObserver(ctx, "demo", time.Minute, time.Millisecond, observer)
	}()
	select {
	case <-observer.entered:
	case <-time.After(time.Second):
		t.Fatal("readiness observer did not enter its blocking observation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("readiness wait did not stop after cancellation")
	}
	if ready != 0 || !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("ready=%d error=%v, want zero and context cancellation", ready, waitErr)
	}
}

func TestSendInitPromptCancellationStopsBlockingObservationWithoutDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	observer := &blockingSpawnObserver{entered: make(chan struct{})}
	dispatcher := &recordingSpawnDispatcher{}
	done := make(chan struct{})
	var receipts []dispatchsvc.Receipt
	var initErr error
	go func() {
		defer close(done)
		receipts, initErr = sendInitPromptToReadyAgentsWith(
			ctx, "", "demo", "must-not-send", false, observer, dispatcher,
		)
	}()
	select {
	case <-observer.entered:
	case <-time.After(time.Second):
		t.Fatal("init observer did not enter its blocking observation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("init dispatch did not stop after cancellation")
	}
	if !errors.Is(initErr, context.Canceled) {
		t.Fatalf("init error = %v, want context cancellation", initErr)
	}
	if len(receipts) != 0 || len(dispatcher.messages) != 0 {
		t.Fatalf("receipts=%d messages=%v, want zero delivery after cancellation", len(receipts), dispatcher.messages)
	}
}

func TestWaitForSpawnPromptWorkersCancelsAndJoinsBeforeReturning(t *testing.T) {
	setupCtx, cancelSetup := context.WithCancel(t.Context())
	defer cancelSetup()
	setupDone := make(chan struct{})
	workerCanceled := make(chan struct{})
	releaseWorker := make(chan struct{})
	go func() {
		<-setupCtx.Done()
		close(workerCanceled)
		<-releaseWorker
		close(setupDone)
	}()

	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	result := make(chan error, 1)
	go func() {
		result <- waitForSpawnPromptWorkers(context.Background(), setupDone, signals, true, cancelSetup)
	}()

	select {
	case <-workerCanceled:
	case <-time.After(time.Second):
		t.Fatal("setup worker did not observe cancellation")
	}
	select {
	case err := <-result:
		t.Fatalf("setup wait returned before worker exit: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseWorker)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("setup wait error = %v, want interruption", err)
		}
	case <-time.After(time.Second):
		t.Fatal("setup wait did not return after worker exit")
	}
}

func TestSpawnPromptWorkerCleanupBlocksUntilWorkerExit(t *testing.T) {
	setupCtx, cancelSetup := context.WithCancel(t.Context())
	defer cancelSetup()
	workerCanceled := make(chan struct{})
	releaseWorker := make(chan struct{})
	var setupWg sync.WaitGroup
	setupWg.Add(1)
	go func() {
		defer setupWg.Done()
		<-setupCtx.Done()
		close(workerCanceled)
		<-releaseWorker
	}()

	joined := make(chan struct{})
	go func() {
		cancelSetup()
		setupWg.Wait()
		close(joined)
	}()
	select {
	case <-workerCanceled:
	case <-time.After(time.Second):
		t.Fatal("spawn worker did not observe deferred lifecycle cancellation")
	}
	select {
	case <-joined:
		t.Fatal("spawn lifecycle returned before its prompt worker exited")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseWorker)
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("spawn lifecycle did not return after its prompt worker exited")
	}
}

// bd-zz717: the per-pane readiness verdict is rendered as one line per pane,
// naming the pane, the canonical agent type, and the verdict. The
// no-classifier verdict names what the operator must do — send the prompt by
// hand.
func TestFormatSpawnReadinessVerdict(t *testing.T) {
	tests := []struct {
		name      string
		agentType AgentType
		verdict   string
		want      string
	}{
		{
			name:      "checked-and-ready",
			agentType: AgentTypeClaude,
			verdict:   string(tmux.VerdictCheckedAndReady),
			want:      "✓ pane %1 (cc): checked-and-ready",
		},
		{
			name:      "no-classifier names manual send",
			agentType: AgentTypeGemini,
			verdict:   string(tmux.VerdictNoClassifier),
			want:      "⚠ pane %1 (gmi): no-classifier — no composer classifier for this agent type; send the prompt by hand",
		},
		{
			name:      "delivery-not-implemented",
			agentType: AgentTypeGrok,
			verdict:   spawnVerdictDeliveryNotImplemented,
			want:      "⚠ pane %1 (grok): delivery-not-implemented — automated prompt delivery is not implemented for this agent type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatSpawnReadinessVerdict("%1", tt.agentType, tt.verdict); got != tt.want {
				t.Fatalf("formatSpawnReadinessVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSpawnPaneReadinessVerdict(t *testing.T) {
	tests := []struct {
		name      string
		agentType AgentType
		want      string
	}{
		{
			name:      "grok delivery not implemented",
			agentType: AgentTypeGrok,
			want:      spawnVerdictDeliveryNotImplemented,
		},
		{
			name:      "claude checked-and-ready",
			agentType: AgentTypeClaude,
			want:      string(tmux.VerdictCheckedAndReady),
		},
		{
			name:      "codex checked-and-ready",
			agentType: AgentTypeCodex,
			want:      string(tmux.VerdictCheckedAndReady),
		},
		{
			name:      "gemini no-classifier",
			agentType: AgentTypeGemini,
			want:      string(tmux.VerdictNoClassifier),
		},
		{
			name:      "antigravity no-classifier",
			agentType: AgentTypeAntigravity,
			want:      string(tmux.VerdictNoClassifier),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := spawnPaneReadinessVerdict(tt.agentType); got != tt.want {
				t.Fatalf("spawnPaneReadinessVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDispatchSpawnPromptSequenceReadinessTimeout(t *testing.T) {
	now := time.Now().UTC()
	pane := tmux.Pane{ID: "%46", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "demo__cc_1"}
	notReady := testSpawnPaneObservation(now, pane, statuspkg.StateUnknown)
	notReady.Current.Freshness = statuspkg.FreshnessUnavailable
	notReady.Current.Confidence = 0
	observation := testSpawnSessionObservation(now, notReady)
	observation.Complete = false
	observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{observation}}
	dispatcher := &recordingSpawnDispatcher{}

	_, err := dispatchSpawnPromptSequence(
		t.Context(), "demo", pane.ID,
		[]spawnPromptStep{{Kind: "user_prompt", Message: "must-not-send"}},
		observer, dispatcher, 10*time.Millisecond, time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "timeout waiting for pane") {
		t.Fatalf("error = %v, want readiness timeout", err)
	}
	if len(dispatcher.messages) != 0 {
		t.Fatalf("messages = %v, want no dispatch on timeout", dispatcher.messages)
	}
}

// TestSpawnReadinessVerdictPerPane simulates a four-pane spawn and asserts one
// readiness verdict line per pane (bd-zz717). The observer/dispatcher seams
// stand in for tmux; the composer verdict is supplied directly because
// ComposerDeliveryVerdict requires a live tmux capture.
func TestSpawnReadinessVerdictPerPane(t *testing.T) {
	now := time.Now().UTC()
	panes := []tmux.Pane{
		{ID: "%1", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "demo__cc_1"},
		{ID: "%2", WindowIndex: 0, Index: 2, Type: tmux.AgentCodex, Title: "demo__cod_2"},
		{ID: "%3", WindowIndex: 0, Index: 3, Type: tmux.AgentGemini, Title: "demo__gmi_3"},
		{ID: "%4", WindowIndex: 0, Index: 4, Type: tmux.AgentGrok, Title: "demo__grok_4"},
	}
	obs := make([]statuspkg.PaneObservation, 0, len(panes))
	for _, p := range panes {
		obs = append(obs, testSpawnPaneObservation(now, p, statuspkg.StateIdle))
	}
	observer := &scriptedSpawnObserver{observations: []statuspkg.SessionObservation{testSpawnSessionObservation(now, obs...)}}
	dispatcher := &recordingSpawnDispatcher{}

	lines := make([]string, 0, len(panes))
	for _, p := range panes {
		agentType := AgentType(p.Type)
		verdict := spawnPaneReadinessVerdict(agentType)
		if verdict != spawnVerdictDeliveryNotImplemented {
			steps := buildSpawnPromptSequenceForAgent(agentType, "", "", "work", 0)
			if _, err := dispatchSpawnPromptSequence(t.Context(), "demo", p.ID, steps, observer, dispatcher, 0, time.Millisecond); err != nil {
				t.Fatalf("dispatch pane %s: %v", p.ID, err)
			}
		}
		lines = append(lines, formatSpawnReadinessVerdict(p.ID, agentType, verdict))
	}

	if len(lines) != len(panes) {
		t.Fatalf("verdict lines = %d, want one per pane (%d)", len(lines), len(panes))
	}
	for i, p := range panes {
		if !strings.Contains(lines[i], p.ID) {
			t.Fatalf("verdict line[%d] = %q, want pane %s named", i, lines[i], p.ID)
		}
	}
	if !strings.Contains(lines[0], "checked-and-ready") || !strings.Contains(lines[1], "checked-and-ready") {
		t.Fatalf("claude/codex verdicts = %q, %q, want checked-and-ready", lines[0], lines[1])
	}
	if !strings.Contains(lines[2], "no-classifier") {
		t.Fatalf("gemini verdict = %q, want no-classifier", lines[2])
	}
	if !strings.Contains(lines[3], "delivery-not-implemented") {
		t.Fatalf("grok verdict = %q, want delivery-not-implemented", lines[3])
	}
}

// TestSpawnReadinessVerdictEndToEnd drives the real spawn lifecycle and asserts
// that the per-pane delivery-readiness verdict reaches both output surfaces:
// the human-readable verdict line (text mode) and the PaneResponse
// ReadinessVerdict field (JSON mode). Unlike TestSpawnReadinessVerdictPerPane,
// which re-implements the production loop, this test exercises recordSetupVerdict,
// the post-setup print loop, and the JSON assignment inside spawnSessionLogicContext.
func TestSpawnReadinessVerdictEndToEnd(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpDir := t.TempDir()

	oldCfg := cfg
	oldJSON := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJSON
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	configureSessionTemplateFakeAgents(cfg)

	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1},
		{Type: AgentTypeCodex, Index: 1},
		{Type: AgentTypeGemini, Index: 1},
	}
	spawnOpts := func(sessionName string) SpawnOptions {
		return SpawnOptions{
			Session:  sessionName,
			Agents:   agents,
			CCCount:  1,
			CodCount: 1,
			GmiCount: 1,
			UserPane: true,
		}
	}

	t.Run("text", func(t *testing.T) {
		jsonOutput = false
		sessionName := fmt.Sprintf("ntm-verdict-text-%d", time.Now().UnixNano())
		defer func() { _ = tmux.KillSession(sessionName) }()
		projectDir := filepath.Join(tmpDir, sessionName)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		stdout, err := captureStdout(t, func() error {
			return spawnSessionLogicContext(t.Context(), spawnOpts(sessionName))
		})
		if err != nil {
			t.Fatalf("spawnSessionLogicContext: %v", err)
		}
		assertSpawnVerdictLines(t, sessionName, stdout)
	})

	t.Run("json", func(t *testing.T) {
		jsonOutput = true
		sessionName := fmt.Sprintf("ntm-verdict-json-%d", time.Now().UnixNano())
		defer func() { _ = tmux.KillSession(sessionName) }()
		projectDir := filepath.Join(tmpDir, sessionName)
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		stdout, err := captureStdout(t, func() error {
			return spawnSessionLogicContext(t.Context(), spawnOpts(sessionName))
		})
		if err != nil {
			t.Fatalf("spawnSessionLogicContext: %v", err)
		}
		assertSpawnVerdictJSON(t, stdout)
	})
}

func assertSpawnVerdictLines(t *testing.T, sessionName, stdout string) {
	t.Helper()
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("GetPanes: %v", err)
	}
	paneByType := make(map[tmux.AgentType]string, len(panes))
	for _, p := range panes {
		paneByType[p.Type] = p.ID
	}
	wants := []struct {
		agentType AgentType
		verdict   string
	}{
		{AgentTypeClaude, string(tmux.VerdictCheckedAndReady)},
		{AgentTypeCodex, string(tmux.VerdictCheckedAndReady)},
		{AgentTypeGemini, string(tmux.VerdictNoClassifier)},
	}
	for _, w := range wants {
		paneID := paneByType[tmux.AgentType(w.agentType)]
		if paneID == "" {
			t.Fatalf("no pane for agent type %s", w.agentType)
		}
		want := formatSpawnReadinessVerdict(paneID, w.agentType, w.verdict)
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing verdict line %q; got:\n%s", want, stdout)
		}
	}
}

func assertSpawnVerdictJSON(t *testing.T, stdout string) {
	t.Helper()
	var resp output.SpawnResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode spawn response: %v; stdout=%q", err, stdout)
	}
	byType := make(map[string]string, len(resp.Panes))
	for _, p := range resp.Panes {
		byType[p.Type] = p.ReadinessVerdict
	}
	wants := map[string]string{
		"claude": string(tmux.VerdictCheckedAndReady),
		"codex":  string(tmux.VerdictCheckedAndReady),
		"gemini": string(tmux.VerdictNoClassifier),
	}
	for typ, want := range wants {
		if got := byType[typ]; got != want {
			t.Fatalf("ReadinessVerdict for %s = %q, want %q", typ, got, want)
		}
	}
}

// pi captures, read off a live pi 0.84.2 pane (mirrors the fixtures in
// internal/status/unified_test.go). The idle capture is a pane waiting at its
// status line; the working capture is the same pane mid-turn with the braille
// spinner and "Working..." in the live tail.
const piIdleAtStatusLineCapture = ` pi v0.84.2
 escape interrupt · ctrl+c/ctrl+d clear/exit · / commands · ! bash · ctrl+o more
 Press ctrl+o to show full startup help and loaded resources.

────────────────────────────────────────────────────────────────

────────────────────────────────────────────────────────────────
/home/gabriel/repositories/daytrace
0.0%/262k (auto)                              (litellm) kimi-k2.7`

const piWorkingCapture = `with its own shell process, working directory, and scrollback history. You can
split a window horizontally or vertically to create panes.

 ⠴ Working...
────────────────────────────────────────────────────────────────

────────────────────────────────────────────────────────────────
/home/gabriel/repositories/daytrace
↑100k ↓958 13.0%/262k (auto)                  (litellm) kimi-k2.7`

// newPiSessionObserver builds a SessionObserver whose topology and capture are
// pinned to a single pi pane, so the full chain — title-derived Pane.Type,
// determineStateAt's pi arm, and observationConfidence — is exercised rather
// than a hand-built PaneObservation.
func newPiSessionObserver(observedAt time.Time, capture string) *statuspkg.SessionObserver {
	detector := statuspkg.NewDetector()
	return statuspkg.NewSessionObserverWithDependencies(
		detector,
		statuspkg.DefaultSessionObserverConfig(detector.Config()),
		statuspkg.SessionObserverDependencies{
			ListPanes: func(context.Context, string) ([]tmux.PaneActivity, error) {
				return []tmux.PaneActivity{{
					Pane:         tmux.Pane{ID: "%1", Index: 1, Title: "demo__pi_1", Type: tmux.AgentPi},
					LastActivity: observedAt.Add(-time.Minute),
				}}, nil
			},
			CapturePane: func(_ context.Context, _ string, _ int) (string, error) {
				return capture, nil
			},
			Now: func() time.Time { return observedAt },
		},
	)
}

// TestSpawnPaneObservationSafeToDispatchPi is the assertion the suite was
// missing: the readiness gate (spawnPaneObservationSafeToDispatch) succeeding
// for a non-cc/cod agent type. It exercises the full chain from a real pi
// capture, so a future classifier that stops resolving pi to idle fails here
// rather than silently timing out every pi spawn.
func TestSpawnPaneObservationSafeToDispatchPi(t *testing.T) {
	observedAt := time.Now().UTC()

	t.Run("idle pi prompt is dispatchable", func(t *testing.T) {
		observer := newPiSessionObserver(observedAt, piIdleAtStatusLineCapture)
		observation, err := observer.Observe(context.Background(), "demo")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		pane, ok := observation.PaneByID("%1")
		if !ok {
			t.Fatal("observed pane missing")
		}
		if pane.AgentType != "pi" {
			t.Fatalf("AgentType = %q, want pi", pane.AgentType)
		}
		if pane.Current.Status.State != statuspkg.StateIdle {
			t.Fatalf("state = %q, want idle", pane.Current.Status.State)
		}
		if !spawnPaneObservationSafeToDispatch(pane) {
			t.Fatalf("spawnPaneObservationSafeToDispatch = false for idle pi (state=%s confidence=%.2f)",
				pane.Current.Status.State, pane.Current.Confidence)
		}
	})

	t.Run("mid-work pi pane is not dispatchable", func(t *testing.T) {
		observer := newPiSessionObserver(observedAt, piWorkingCapture)
		observation, err := observer.Observe(context.Background(), "demo")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		pane, ok := observation.PaneByID("%1")
		if !ok {
			t.Fatal("observed pane missing")
		}
		if pane.Current.Status.State != statuspkg.StateWorking {
			t.Fatalf("state = %q, want working", pane.Current.Status.State)
		}
		if spawnPaneObservationSafeToDispatch(pane) {
			t.Fatal("spawnPaneObservationSafeToDispatch = true for mid-work pi, want false")
		}
	})
}

// TestWaitForSpawnPaneReadyPi drives the readiness poll against a fake
// observer that yields a live pi observation, asserting it returns before the
// deadline. The existing timeout test covers only AgentClaude and only the
// failing direction.
func TestWaitForSpawnPaneReadyPi(t *testing.T) {
	observedAt := time.Now().UTC()
	observer := newPiSessionObserver(observedAt, piIdleAtStatusLineCapture)

	start := time.Now()
	err := waitForSpawnPaneReady(t.Context(), "demo", "%1", 5*time.Second, time.Millisecond, observer)
	if err != nil {
		t.Fatalf("waitForSpawnPaneReady = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("waitForSpawnPaneReady took %v, want return before deadline", elapsed)
	}
}
