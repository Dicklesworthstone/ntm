package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
