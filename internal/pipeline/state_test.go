package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestSaveState(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	state := &ExecutionState{
		RunID:      "test-run-123",
		WorkflowID: "my-workflow",
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Variables: map[string]interface{}{
			"name":  "Alice",
			"count": 42,
		},
		Steps: map[string]StepResult{
			"step1": {
				Status: StatusCompleted,
				Output: "step1 output",
			},
		},
	}

	err := SaveState(tmpDir, state)
	if err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	// Verify file exists
	path := filepath.Join(tmpDir, ".ntm", "pipelines", "test-run-123.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("State file not created at %s", path)
	}

	// Verify file contents are valid JSON
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read state file: %v", err)
	}

	var loaded ExecutionState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("State file is not valid JSON: %v", err)
	}

	if loaded.RunID != "test-run-123" {
		t.Errorf("RunID = %q, want %q", loaded.RunID, "test-run-123")
	}
	if loaded.WorkflowID != "my-workflow" {
		t.Errorf("WorkflowID = %q, want %q", loaded.WorkflowID, "my-workflow")
	}
	if loaded.Status != StatusRunning {
		t.Errorf("Status = %v, want %v", loaded.Status, StatusRunning)
	}
}

func TestSaveState_NilState(t *testing.T) {
	tmpDir := t.TempDir()
	err := SaveState(tmpDir, nil)
	if err == nil {
		t.Error("SaveState(nil) should return error")
	}
}

func TestSaveState_EmptyRunID(t *testing.T) {
	tmpDir := t.TempDir()
	state := &ExecutionState{
		RunID: "", // Empty
	}
	err := SaveState(tmpDir, state)
	if err == nil {
		t.Error("SaveState with empty RunID should return error")
	}
}

func TestSaveState_Overwrite(t *testing.T) {
	tmpDir := t.TempDir()

	// Save initial state
	state1 := &ExecutionState{
		RunID:  "run-1",
		Status: StatusRunning,
	}
	if err := SaveState(tmpDir, state1); err != nil {
		t.Fatalf("First SaveState() error = %v", err)
	}

	// Save updated state with same run ID
	state2 := &ExecutionState{
		RunID:  "run-1",
		Status: StatusCompleted,
	}
	if err := SaveState(tmpDir, state2); err != nil {
		t.Fatalf("Second SaveState() error = %v", err)
	}

	// Load and verify it was overwritten
	loaded, err := LoadState(tmpDir, "run-1")
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if loaded.Status != StatusCompleted {
		t.Errorf("Status = %v, want %v (state should be overwritten)", loaded.Status, StatusCompleted)
	}
}

func TestLoadState(t *testing.T) {
	tmpDir := t.TempDir()

	// Save a state first
	state := &ExecutionState{
		RunID:       "load-test-123",
		WorkflowID:  "test-workflow",
		Status:      StatusCompleted,
		CurrentStep: "step3",
		Variables: map[string]interface{}{
			"result": "success",
		},
		Steps: map[string]StepResult{
			"step1": {Status: StatusCompleted, Output: "out1"},
			"step2": {Status: StatusCompleted, Output: "out2"},
			"step3": {Status: StatusCompleted, Output: "out3"},
		},
	}
	if err := SaveState(tmpDir, state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	// Load the state
	loaded, err := LoadState(tmpDir, "load-test-123")
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	if loaded.RunID != "load-test-123" {
		t.Errorf("RunID = %q, want %q", loaded.RunID, "load-test-123")
	}
	if loaded.WorkflowID != "test-workflow" {
		t.Errorf("WorkflowID = %q, want %q", loaded.WorkflowID, "test-workflow")
	}
	if loaded.Status != StatusCompleted {
		t.Errorf("Status = %v, want %v", loaded.Status, StatusCompleted)
	}
	if loaded.CurrentStep != "step3" {
		t.Errorf("CurrentStep = %q, want %q", loaded.CurrentStep, "step3")
	}
	if len(loaded.Steps) != 3 {
		t.Errorf("len(Steps) = %d, want 3", len(loaded.Steps))
	}
	if loaded.Variables["result"] != "success" {
		t.Errorf("Variables[result] = %v, want 'success'", loaded.Variables["result"])
	}
}

func TestLoadState_EmptyRunID(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := LoadState(tmpDir, "")
	if err == nil {
		t.Error("LoadState with empty runID should return error")
	}
}

func TestLoadState_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := LoadState(tmpDir, "nonexistent-run")
	if err == nil {
		t.Error("LoadState for nonexistent run should return error")
	}
}

func TestLoadState_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory structure
	dir := filepath.Join(tmpDir, ".ntm", "pipelines")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	// Write invalid JSON
	path := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(path, []byte("not valid json {"), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	_, err := LoadState(tmpDir, "invalid")
	if err == nil {
		t.Error("LoadState should return error for invalid JSON")
	}
}

func TestLoadState_EmptyRunIDInFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory structure
	dir := filepath.Join(tmpDir, ".ntm", "pipelines")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	// Write JSON with empty RunID - should be filled from filename
	path := filepath.Join(dir, "from-filename.json")
	data := `{"run_id":"","status":"running"}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	loaded, err := LoadState(tmpDir, "from-filename")
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if loaded.RunID != "from-filename" {
		t.Errorf("RunID = %q, want 'from-filename' (should be filled from filename)", loaded.RunID)
	}
}

func cleanupStateFixture(runID string) *ExecutionState {
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	return &ExecutionState{
		RunID: runID, WorkflowID: "cleanup-fixture", Session: "cleanup-session",
		Status: StatusCompleted, StartedAt: started, UpdatedAt: finished, FinishedAt: finished,
		CommandEvaluationVersion: commandEvaluationVersion, Steps: make(map[string]StepResult),
	}
}

func saveOldCleanupState(t *testing.T, dir string, state *ExecutionState) []byte {
	t.Helper()
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	path := pipelineStatePath(dir, state.RunID)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertCleanupEntry(t *testing.T, result *StateCleanupResult, runID, action, reason string) {
	t.Helper()
	for _, entry := range result.Entries {
		if entry.RunID == runID {
			if entry.Action != action || entry.Reason != reason {
				t.Fatalf("cleanup entry = %+v, want %s/%s", entry, action, reason)
			}
			return
		}
	}
	t.Fatalf("run %q absent from cleanup result: %+v", runID, result)
}

func checkCleanupReceipt(t *testing.T, state *ExecutionState, eligible bool, reason string) {
	t.Helper()
	dir := t.TempDir()
	before := saveOldCleanupState(t, dir, state)
	preview, err := CleanupStates(t.Context(), dir, 24*time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	action := "retained"
	if eligible {
		action, reason = "would_delete", "settled"
	}
	assertCleanupEntry(t, preview, state.RunID, action, reason)
	after, err := os.ReadFile(pipelineStatePath(dir, state.RunID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("preview changed checkpoint: %v", err)
	}
	result, err := CleanupStates(t.Context(), dir, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if eligible {
		assertCleanupEntry(t, result, state.RunID, "deleted", "settled")
		if result.Deleted != 1 || result.Eligible != 1 || result.Retained != 0 || preview.Eligible != 1 {
			t.Fatalf("eligible counts: preview=%+v actual=%+v", preview, result)
		}
		if _, err := os.Stat(pipelineStatePath(dir, state.RunID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("eligible checkpoint remains: %v", err)
		}
	} else {
		assertCleanupEntry(t, result, state.RunID, "retained", reason)
		if result.Deleted != 0 || result.Eligible != 0 || result.Retained != 1 || preview.Eligible != 0 {
			t.Fatalf("retained counts: preview=%+v actual=%+v", preview, result)
		}
		after, err = os.ReadFile(pipelineStatePath(dir, state.RunID))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("retained checkpoint changed: %v", err)
		}
	}
}

func TestCleanupStates(t *testing.T) {
	dir := t.TempDir()
	for _, status := range []ExecutionStatus{StatusCompleted, StatusFailed, StatusCancelled} {
		state := cleanupStateFixture("old-" + string(status))
		state.Status = status
		state.Steps["skipped"] = StepResult{
			StepID: "skipped", Status: StatusSkipped, SkipKind: SkipKindWhenCondition,
			StartedAt: state.StartedAt, FinishedAt: state.FinishedAt,
		}
		if status == StatusCancelled {
			cancelled := state.FinishedAt.Add(-time.Second)
			state.CancelledAt = &cancelled
			state.Errors = []ExecutionError{{Type: "timeout", Message: "global timeout", Fatal: true}}
		} else if status == StatusFailed {
			state.Errors = []ExecutionError{{Type: "execution", Message: "command exited 7", Fatal: true}}
		}
		saveOldCleanupState(t, dir, state)
	}
	recent := cleanupStateFixture("recent")
	if err := SaveState(dir, recent); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(pipelineStateDir(dir), "workflows", "shared.yaml")
	if err := os.MkdirAll(filepath.Dir(snapshot), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot, []byte("shared frozen definition"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupStates(t.Context(), dir, 24*time.Hour, false)
	if err != nil || result.Deleted != 3 || result.Eligible != 3 || result.Retained != 1 {
		t.Fatalf("CleanupStates() = %+v, %v", result, err)
	}
	assertCleanupEntry(t, result, "recent", "retained", "recent")
	for _, status := range []ExecutionStatus{StatusCompleted, StatusFailed, StatusCancelled} {
		runID := "old-" + string(status)
		assertCleanupEntry(t, result, runID, "deleted", "settled")
		prefix, err := runControlPrefix(dir, runID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(prefix + ".lock"); err != nil {
			t.Fatalf("cleanup removed stable ownership lock: %v", err)
		}
		if _, err := os.Stat(prefix + ".owner"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cleanup published an execution owner: %v", err)
		}
	}
	if data, err := os.ReadFile(snapshot); err != nil || string(data) != "shared frozen definition" {
		t.Fatalf("cleanup changed shared workflow: %q, %v", data, err)
	}
}

func TestCleanupStatesInvalidArgumentsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	state := cleanupStateFixture("cancelled-request")
	before := saveOldCleanupState(t, dir, state)
	for _, age := range []time.Duration{0, -time.Hour} {
		if _, err := CleanupStates(t.Context(), dir, age, false); err == nil {
			t.Fatalf("accepted invalid age %v", age)
		}
	}
	if _, err := CleanupStates(t.Context(), "", time.Hour, false); err == nil {
		t.Fatal("accepted an unspecified project")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CleanupStates(ctx, dir, time.Hour, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cleanup: %v", err)
	}
	after, err := os.ReadFile(pipelineStatePath(dir, state.RunID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("cancelled cleanup changed state: %v", err)
	}
	empty, err := CleanupStates(nil, t.TempDir(), time.Hour, false)
	if err != nil || empty.Deleted != 0 || len(empty.Entries) != 0 {
		t.Fatalf("missing state directory: %+v %v", empty, err)
	}
}

func TestCleanupStatesRetainsUnfinishedState(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ExecutionState)
		reason string
	}{
		{"running", func(s *ExecutionState) { s.Status = StatusRunning }, "nonterminal"},
		{"paused", func(s *ExecutionState) { s.Status = StatusPaused }, "nonterminal"},
		{"pending", func(s *ExecutionState) { s.Status = StatusPending }, "nonterminal"},
		{"unknown", func(s *ExecutionState) { s.Status = "future" }, "nonterminal"},
		{"unfinished", func(s *ExecutionState) { s.FinishedAt = time.Time{} }, "unfinished"},
		{"backwards_time", func(s *ExecutionState) { s.FinishedAt = s.StartedAt.Add(-time.Second) }, "unfinished"},
		{"cancel_without_cleanup", func(s *ExecutionState) { s.Status = StatusCancelled }, "unfinished"},
		{"cancel_after_finish", func(s *ExecutionState) { at := s.FinishedAt.Add(time.Second); s.CancelledAt = &at }, "unfinished"},
		{"current_step", func(s *ExecutionState) { s.CurrentStep = "active" }, "in_flight"},
		{"in_flight", func(s *ExecutionState) { s.InFlightSteps = map[string]InFlightStepState{"active": {StepID: "active"}} }, "in_flight"},
		{"parallel", func(s *ExecutionState) {
			s.ParallelState = map[string]ParallelGroupState{"group": {InFlightStepIDs: []string{"active"}}}
		}, "in_flight"},
		{"running_step", func(s *ExecutionState) { s.Steps["active"] = StepResult{Status: StatusRunning} }, "unfinished_step"},
		{"unknown_step", func(s *ExecutionState) { s.Steps["active"] = StepResult{Status: "future"} }, "unfinished_step"},
		{"unfinished_step", func(s *ExecutionState) { s.Steps["active"] = StepResult{Status: StatusCompleted} }, "unfinished_step"},
		{"checkpoint_error", func(s *ExecutionState) {
			s.Errors = []ExecutionError{{Type: "checkpoint", Message: "storage failed", Fatal: true}}
		}, "checkpoint_failure"},
		{"legacy_journal", func(s *ExecutionState) { s.CommandEvaluationVersion = 0 }, "unsupported_journal"},
		{"old_journal", func(s *ExecutionState) { s.CommandEvaluationVersion = 1 }, "unsupported_journal"},
		{"negative_journal", func(s *ExecutionState) { s.CommandEvaluationVersion = -1 }, "unsupported_journal"},
		{"future_journal", func(s *ExecutionState) { s.CommandEvaluationVersion++ }, "unsupported_journal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cleanupStateFixture(test.name)
			test.change(state)
			checkCleanupReceipt(t, state, false, test.reason)
		})
	}
}

func cleanupCommandFixture(state *ExecutionState, status string) CommandExecutionState {
	record := CommandExecutionState{
		Version: commandExecutionVersion, StepID: "command", StepHash: strings.Repeat("a", 64),
		Status: status, ProcessID: 123, StartedAt: state.StartedAt, SettledAt: state.FinishedAt,
		Result: StepResult{StepID: "command", AgentType: "command", Status: StatusFailed,
			StartedAt: state.StartedAt, FinishedAt: state.FinishedAt,
			Error: &StepError{Type: "command", Message: "command exited 7", Timestamp: state.FinishedAt}},
	}
	switch status {
	case commandExecutionStarting:
		record.ProcessID = 0
		fallthrough
	case commandExecutionRunning:
		record.SettledAt, record.Result = time.Time{}, StepResult{}
	case commandExecutionUnresolved:
		record.SettledAt = time.Time{}
	}
	return record
}

func cleanupSelectorFixture(state *ExecutionState, status string) CommandEvaluationState {
	record := CommandEvaluationState{
		Version: commandEvaluationVersion, StepID: "selector", StepHash: strings.Repeat("b", 64),
		InvocationHash: strings.Repeat("c", 64), StdoutLimit: 1024, StderrLimit: 1024,
		Status: status, StartedAt: state.StartedAt,
	}
	if status != commandExecutionStarting {
		record.ProcessID, record.FinishedAt = 123, state.FinishedAt
	}
	if status == commandExecutionUnresolved {
		record.Error = "observation did not prove settlement"
	} else if status == commandExecutionSettled {
		record.Output = []byte("ready")
		sum := sha256.Sum256(record.Output)
		record.OutputHash = hex.EncodeToString(sum[:])
	}
	return record
}

func cleanupAgentFixture(state *ExecutionState, status string) AgentDeliveryState {
	record := AgentDeliveryState{
		Version: agentDeliveryVersion, StepID: "agent", Kind: StepKindPrompt, Session: state.Session,
		Endpoint: "local", PaneID: "%1", PanePID: 123, AgentType: "claude",
		StepHash: strings.Repeat("d", 64), PromptHash: strings.Repeat("e", 64), PromptEchoHash: strings.Repeat("f", 64),
		Status: status, StartedAt: state.StartedAt,
	}
	if status == agentDeliveryDelivered || status == agentDeliveryCompleted {
		record.DeliveredAt = state.StartedAt.Add(time.Second)
	}
	if status == agentDeliveryCompleted {
		record.CompletedAt = state.FinishedAt
	}
	return record
}

func cleanupMailFixture(state *ExecutionState, status string) MailSendState {
	record := MailSendState{
		Version: mailSendVersion, StepID: "mail", StepHash: strings.Repeat("a", 64),
		Status: status, StartedAt: state.StartedAt,
	}
	if status != mailSendSending {
		record.FinishedAt = state.FinishedAt
		record.Result = StepResult{StepID: "mail", Status: StatusCompleted, StartedAt: state.StartedAt, FinishedAt: state.FinishedAt}
		if status == mailSendRejected {
			record.Result.Status = StatusFailed
		}
	}
	return record
}

func cleanupReservationFixture(state *ExecutionState, status string) ReservationStepState {
	record := ReservationStepState{
		Version: reservationStepVersion, StepID: "reserve", StepHash: strings.Repeat("a", 64),
		Kind: StepKindReservationAcquire, Status: status, StartedAt: state.StartedAt, FinishedAt: state.FinishedAt,
		Request: FileReservationPathsStep{ProjectKey: "/project", AgentName: "BlueLake", Paths: StringOrList{"a.go"}},
		Granted: []agentmail.FileReservation{{
			ID: 1, ProjectID: 1, PathPattern: "a.go", AgentName: "BlueLake",
			CreatedTS: agentmail.FlexTime{Time: state.StartedAt}, ExpiresTS: agentmail.FlexTime{Time: state.StartedAt.Add(time.Hour)},
		}},
		Result: StepResult{StepID: "reserve", Status: StatusCompleted, StartedAt: state.StartedAt, FinishedAt: state.FinishedAt},
	}
	switch status {
	case reservationAcquiring:
		record.Granted, record.FinishedAt, record.Result = nil, time.Time{}, StepResult{}
	case reservationUncertain, reservationRejected:
		record.Granted, record.Result.Status = nil, StatusFailed
	case reservationPartial:
		record.Request.Paths = append(record.Request.Paths, "b.go")
		record.Conflicts = []agentmail.ReservationConflict{{Path: "b.go", Holders: []string{"RedFox"}}}
		record.Result.Status = StatusFailed
	case reservationReleasing:
		record.Kind, record.FinishedAt, record.Result = StepKindReservationRelease, time.Time{}, StepResult{}
	case reservationReleased:
		record.ReleasedIDs = []int{1}
	}
	return record
}

func TestCleanupStatesReceiptPhases(t *testing.T) {
	for _, status := range []string{commandExecutionStarting, commandExecutionRunning, commandExecutionUnresolved, commandExecutionSettled} {
		t.Run("command_"+status, func(t *testing.T) {
			state := cleanupStateFixture("command-" + status)
			record := cleanupCommandFixture(state, status)
			if err := validateCommandExecution(record.StepID, record); err != nil {
				t.Fatalf("invalid test receipt: %v", err)
			}
			state.CommandExecutions = map[string]CommandExecutionState{record.StepID: record}
			checkCleanupReceipt(t, state, status == commandExecutionSettled, "unsettled_command")
		})
	}
	for _, status := range []string{commandExecutionStarting, commandExecutionUnresolved, commandExecutionSettled} {
		t.Run("selector_"+status, func(t *testing.T) {
			state := cleanupStateFixture("selector-" + status)
			record := cleanupSelectorFixture(state, status)
			if err := validateCommandEvaluation(record.StepID, record); err != nil {
				t.Fatalf("invalid test receipt: %v", err)
			}
			state.CommandEvaluations = map[string]CommandEvaluationState{record.StepID: record}
			checkCleanupReceipt(t, state, commandEvaluationHasOutcome(record), "unsettled_selector")
		})
	}
	for _, status := range []string{agentDeliveryNotSent, agentDeliverySending, agentDeliveryDelivered, agentDeliveryCompleted} {
		t.Run("agent_"+status, func(t *testing.T) {
			state := cleanupStateFixture("agent-" + status)
			record := cleanupAgentFixture(state, status)
			if err := validateAgentDelivery(record.StepID, record, state.Session); err != nil {
				t.Fatalf("invalid test receipt: %v", err)
			}
			state.AgentDeliveries = map[string]AgentDeliveryState{record.StepID: record}
			checkCleanupReceipt(t, state, status == agentDeliveryNotSent || status == agentDeliveryCompleted, "unfinished_agent_delivery")
		})
	}
	for _, status := range []string{mailSendSending, mailSendSent, mailSendRejected} {
		t.Run("mail_"+status, func(t *testing.T) {
			state := cleanupStateFixture("mail-" + status)
			record := cleanupMailFixture(state, status)
			if err := validateMailSend(record.StepID, record); err != nil {
				t.Fatalf("invalid test receipt: %v", err)
			}
			state.MailSends = map[string]MailSendState{record.StepID: record}
			checkCleanupReceipt(t, state, status != mailSendSending, "unfinished_mail_send")
		})
	}
	for _, status := range []string{reservationAcquiring, reservationUncertain, reservationHeld, reservationPartial, reservationReleasing, reservationReleased, reservationRejected} {
		t.Run("reservation_"+status, func(t *testing.T) {
			state := cleanupStateFixture("reservation-" + status)
			record := cleanupReservationFixture(state, status)
			if err := validateReservationStep(record.StepID, record); err != nil {
				t.Fatalf("invalid test receipt: %v", err)
			}
			state.ReservationSteps = map[string]ReservationStepState{record.StepID: record}
			checkCleanupReceipt(t, state, status == reservationReleased || status == reservationRejected, "unfinished_reservation")
		})
	}
}

func TestCleanupStatesSettledBackgroundAndFailedIterations(t *testing.T) {
	state := cleanupStateFixture("settled-background")
	state.Status = StatusFailed
	record := cleanupCommandFixture(state, commandExecutionSettled)
	record.Background, record.Result.RerunOnResume = true, true
	record.Result.Status = StatusCancelled
	state.CommandExecutions = map[string]CommandExecutionState{record.StepID: record}
	state.Steps[record.StepID] = record.Result
	state.ForeachState = map[string]ForeachIterationState{"each": {StepID: "each", Total: 5, CurrentIteration: 2}}
	if err := validateCommandExecution(record.StepID, record); err != nil {
		t.Fatal(err)
	}
	checkCleanupReceipt(t, state, true, "")
	record.Status, record.SettledAt, record.Result.RerunOnResume = commandExecutionUnresolved, time.Time{}, false
	state.CommandExecutions[record.StepID] = record
	if err := validateCommandExecution(record.StepID, record); err != nil {
		t.Fatal(err)
	}
	checkCleanupReceipt(t, state, false, "unsettled_command")
}

func TestCleanupStatesRequiresParallelSettlement(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ParallelGroupState)
	}{
		{"settled", func(*ParallelGroupState) {}},
		{"missing_proof", func(g *ParallelGroupState) { g.AllSubstepsSettled = false }},
		{"missing_finish", func(g *ParallelGroupState) { g.CompletedAt = time.Time{} }},
		{"backwards_time", func(g *ParallelGroupState) { g.CompletedAt = g.StartedAt.Add(-time.Second) }},
		{"wrong_identity", func(g *ParallelGroupState) { g.StepID = "other" }},
		{"active_child", func(g *ParallelGroupState) { g.InFlightStepIDs = []string{"child"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cleanupStateFixture("parallel-" + test.name)
			state.Status = StatusFailed
			group := ParallelGroupState{
				StepID: "group", Total: 2, StartedAt: state.StartedAt, CompletedAt: state.FinishedAt,
				AllSubstepsSettled: true, CompletedStepIDs: []string{"first"}, FailedStepIDs: []string{"second"},
			}
			test.change(&group)
			state.ParallelState = map[string]ParallelGroupState{"group": group}
			checkCleanupReceipt(t, state, test.name == "settled", "in_flight")
		})
	}
}

func TestCleanupStatesAcceptedReceiptProvenance(t *testing.T) {
	for _, family := range []string{"command", "selector", "agent"} {
		for _, corrupt := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/corrupt_%t", family, corrupt), func(t *testing.T) {
				state := cleanupStateFixture("accepted-" + family)
				hash := sha256.Sum256([]byte("verified output"))
				reason := ""
				switch family {
				case "command":
					record := cleanupCommandFixture(state, commandExecutionSettled)
					token, err := commandExecutionReceiptToken(state, record)
					if err != nil {
						t.Fatal(err)
					}
					record.ResolutionAction, record.ResolutionToken = CommandExecutionResolutionAccept, token
					record.ResolutionReason, record.ResolutionExitCode = "independently verified termination", 7
					record.ResolvedAt, record.ResolutionOutputHash = state.FinishedAt, hex.EncodeToString(hash[:])
					record.Result.Output, record.Result.Error = "verified output", nil
					if _, err := commandExecutionRecoveryEntry(state, record); err != nil {
						t.Fatalf("invalid accepted command: %v", err)
					}
					state.CommandExecutions = map[string]CommandExecutionState{record.StepID: record}
					reason = "invalid_command_receipt"
				case "selector":
					record := cleanupSelectorFixture(state, commandExecutionStarting)
					token, err := commandEvaluationReceiptToken(state, record)
					if err != nil {
						t.Fatal(err)
					}
					record.Status, record.OriginalStatus = commandEvaluationAccepted, commandExecutionStarting
					record.ResolutionAction, record.ResolutionToken = CommandEvaluationResolutionAccept, token
					record.ResolutionReason, record.ResolvedAt = "independently verified termination", state.FinishedAt
					record.Output, record.OutputHash = []byte("verified output"), hex.EncodeToString(hash[:])
					record.ResolutionHash, err = commandEvaluationResolutionHash(record)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := commandEvaluationRecoveryEntry(state, record); err != nil {
						t.Fatalf("invalid accepted selector: %v", err)
					}
					state.CommandEvaluations = map[string]CommandEvaluationState{record.StepID: record}
					reason = "invalid_selector_receipt"
				case "agent":
					record := cleanupAgentFixture(state, agentDeliverySending)
					token, err := agentDeliveryReceiptToken(state, record)
					if err != nil {
						t.Fatal(err)
					}
					boundary := sha256.Sum256([]byte(record.BeforeOutput))
					record.Status, record.DeliveredAt, record.CompletedAt = agentDeliveryCompleted, state.StartedAt.Add(time.Second), state.FinishedAt
					record.ResolutionAction, record.ResolutionToken = AgentDeliveryResolutionAccept, token
					record.ResolutionReason, record.ResolvedAt = "verified original submission", record.DeliveredAt
					record.ResolutionBoundaryHash = hex.EncodeToString(boundary[:])
					if _, err := agentDeliveryRecoveryEntry(state, record); err != nil {
						t.Fatalf("invalid accepted agent: %v", err)
					}
					state.AgentDeliveries = map[string]AgentDeliveryState{record.StepID: record}
					reason = "invalid_agent_receipt"
				}
				if corrupt {
					// Every receipt is structurally valid, but its operator's
					// decision must not be transferable to another run.
					state.RunID += "-copied"
				}
				checkCleanupReceipt(t, state, !corrupt, reason)
			})
		}
	}
}

func cleanupStateWithReceipts(t *testing.T) *ExecutionState {
	t.Helper()
	state := cleanupStateFixture("receipt-integrity")
	state.CommandExecutions = map[string]CommandExecutionState{"command": cleanupCommandFixture(state, commandExecutionSettled)}
	state.CommandEvaluations = map[string]CommandEvaluationState{"selector": cleanupSelectorFixture(state, commandExecutionSettled)}
	state.AgentDeliveries = map[string]AgentDeliveryState{"agent": cleanupAgentFixture(state, agentDeliveryCompleted)}
	state.MailSends = map[string]MailSendState{"mail": cleanupMailFixture(state, mailSendSent)}
	state.ReservationSteps = map[string]ReservationStepState{"reserve": cleanupReservationFixture(state, reservationReleased)}
	if reason := stateCleanupRetentionReason(state); reason != "" {
		t.Fatalf("invalid base receipts: %s", reason)
	}
	return state
}

func TestCleanupStatesReceiptIntegrity(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ExecutionState)
		reason string
	}{
		{"command_key", func(s *ExecutionState) {
			r := s.CommandExecutions["command"]
			s.CommandExecutions = map[string]CommandExecutionState{"wrong": r}
		}, "invalid_command_receipt"},
		{"selector_key", func(s *ExecutionState) {
			r := s.CommandEvaluations["selector"]
			s.CommandEvaluations = map[string]CommandEvaluationState{"wrong": r}
		}, "invalid_selector_receipt"},
		{"agent_key", func(s *ExecutionState) {
			r := s.AgentDeliveries["agent"]
			s.AgentDeliveries = map[string]AgentDeliveryState{"wrong": r}
		}, "invalid_agent_receipt"},
		{"mail_key", func(s *ExecutionState) { r := s.MailSends["mail"]; s.MailSends = map[string]MailSendState{"wrong": r} }, "invalid_mail_receipt"},
		{"reservation_key", func(s *ExecutionState) {
			r := s.ReservationSteps["reserve"]
			s.ReservationSteps = map[string]ReservationStepState{"wrong": r}
		}, "invalid_reservation_receipt"},
		{"command_settlement", func(s *ExecutionState) {
			r := s.CommandExecutions["command"]
			r.SettledAt = time.Time{}
			s.CommandExecutions["command"] = r
		}, "invalid_command_receipt"},
		{"selector_output", func(s *ExecutionState) {
			r := s.CommandEvaluations["selector"]
			r.OutputHash = strings.Repeat("0", 64)
			s.CommandEvaluations["selector"] = r
		}, "invalid_selector_receipt"},
		{"agent_completion", func(s *ExecutionState) {
			r := s.AgentDeliveries["agent"]
			r.CompletedAt = time.Time{}
			s.AgentDeliveries["agent"] = r
		}, "invalid_agent_receipt"},
		{"mail_version", func(s *ExecutionState) { r := s.MailSends["mail"]; r.Version++; s.MailSends["mail"] = r }, "invalid_mail_receipt"},
		{"unreleased_grant", func(s *ExecutionState) {
			r := s.ReservationSteps["reserve"]
			r.ReleasedIDs = nil
			s.ReservationSteps["reserve"] = r
		}, "invalid_reservation_receipt"},
		{"conflicting_lease", func(s *ExecutionState) {
			r := cloneReservationStep(s.ReservationSteps["reserve"])
			r.StepID, r.Result.StepID = "other", "other"
			r.Granted[0].CreatedTS.Time = r.Granted[0].CreatedTS.Add(time.Second)
			s.ReservationSteps["other"] = r
		}, "invalid_reservation_receipt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := cleanupStateWithReceipts(t)
			test.change(state)
			checkCleanupReceipt(t, state, false, test.reason)
		})
	}
}

func TestCleanupStatesRetainsLiveOwnerAndIgnoresStaleOwnerMetadata(t *testing.T) {
	dir := t.TempDir()
	state := cleanupStateFixture("owned")
	control, err := AcquireRunControl(t.Context(), dir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	before := saveOldCleanupState(t, dir, state)
	result, err := CleanupStates(t.Context(), dir, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanupEntry(t, result, state.RunID, "retained", "owned")
	if control.Context().Err() != nil {
		t.Fatal("cleanup cancelled the live executor")
	}
	after, err := os.ReadFile(pipelineStatePath(dir, state.RunID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("cleanup erased active-owner evidence: %v", err)
	}
	control.Close()
	prefix, err := runControlPrefix(dir, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	stale := runControlRecord{Token: strings.Repeat("a", 64), Active: true}
	if err := writeRunControl(prefix+".owner", stale); err != nil {
		t.Fatal(err)
	}
	result, err = CleanupStates(t.Context(), dir, time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanupEntry(t, result, state.RunID, "would_delete", "settled")
	var owner runControlRecord
	if err := readRunControl(prefix+".owner", &owner); err != nil || owner != stale {
		t.Fatalf("preview advertised an executor: %+v %v", owner, err)
	}
	result, err = CleanupStates(t.Context(), dir, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanupEntry(t, result, state.RunID, "deleted", "settled")
	if err := readRunControl(prefix+".owner", &owner); err != nil || owner != stale {
		t.Fatalf("cleanup changed cancellation ownership: %+v %v", owner, err)
	}
	if _, err := os.Stat(prefix + ".lock"); err != nil {
		t.Fatalf("cleanup removed shared lock: %v", err)
	}
}

func TestCleanupStatesRechecksChangedCheckpoint(t *testing.T) {
	for _, sameAge := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_age_%t", sameAge), func(t *testing.T) {
			dir := t.TempDir()
			state := cleanupStateFixture("refreshed")
			saveOldCleanupState(t, dir, state)
			path := pipelineStatePath(dir, state.RunID)
			observed, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			// Replace exactly the same bytes through SaveState, as a
			// concurrent executor does after directory enumeration.
			if err := SaveState(dir, state); err != nil {
				t.Fatal(err)
			}
			if sameAge {
				if err := os.Chtimes(path, observed.ModTime(), observed.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			entry, err := cleanupStateCandidate(t.Context(), dir, state.RunID, observed, time.Now().Add(-time.Hour), false)
			if err != nil || entry.Action != "retained" || entry.Reason != "changed" {
				t.Fatalf("stale inspection deleted replacement: %+v %v", entry, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("replacement changed: %v", err)
			}
		})
	}
}

func TestCleanupStatesPreservesUninspectableFiles(t *testing.T) {
	for _, kind := range []string{"malformed", "future_schema", "negative_schema", "mismatched_id", "oversized", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			state := cleanupStateFixture(kind)
			saveOldCleanupState(t, dir, state)
			path := pipelineStatePath(dir, state.RunID)
			reason := "invalid_state"
			switch kind {
			case "malformed":
				if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
					t.Fatal(err)
				}
			case "future_schema", "negative_schema", "mismatched_id":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]interface{}
				if err := json.Unmarshal(data, &doc); err != nil {
					t.Fatal(err)
				}
				if kind == "mismatched_id" {
					doc["run_id"] = "other-run"
				} else if kind == "future_schema" {
					doc["state_schema_version"] = PipelineStateSchemaVersion + 1
				} else {
					doc["state_schema_version"] = -1
				}
				data, err = json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				file, err := os.OpenFile(path, os.O_WRONLY, 0644)
				if err != nil {
					t.Fatal(err)
				}
				err = file.Truncate(stateCleanupMaxBytes + 1)
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("create oversized checkpoint: %v %v", err, closeErr)
				}
				reason = "too_large"
			case "symlink":
				target := path
				path = pipelineStatePath(dir, "linked")
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				// Keep the target ineligible independently of the link.
				now := time.Now()
				if err := os.Chtimes(target, now, now); err != nil {
					t.Fatal(err)
				}
				state.RunID, reason = "linked", "not_regular"
			}
			if kind != "symlink" {
				old := time.Now().Add(-48 * time.Hour)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			result, err := CleanupStates(t.Context(), dir, time.Hour, false)
			if err != nil || result.Deleted != 0 {
				t.Fatalf("unsafe cleanup: %+v %v", result, err)
			}
			assertCleanupEntry(t, result, state.RunID, "retained", reason)
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() {
				t.Fatalf("uninspectable evidence changed: %v", err)
			}
		})
	}
}

func TestCleanupStatesSkipsOtherFilesAndDirectories(t *testing.T) {
	dir := t.TempDir()
	stateDir := pipelineStateDir(dir)
	if err := os.MkdirAll(filepath.Join(stateDir, "directory.json"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "notes.txt")
	if err := os.WriteFile(path, []byte("retain notes"), 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupStates(t.Context(), dir, time.Hour, false)
	if err != nil || len(result.Entries) != 0 || result.Deleted != 0 {
		t.Fatalf("non-state entries were considered: %+v %v", result, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "directory.json")); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineStateDir(t *testing.T) {
	got := pipelineStateDir("/project")
	want := filepath.Join("/project", ".ntm", "pipelines")
	if got != want {
		t.Errorf("pipelineStateDir() = %q, want %q", got, want)
	}
}

func TestPipelineStatePath(t *testing.T) {
	got := pipelineStatePath("/project", "run-123")
	want := filepath.Join("/project", ".ntm", "pipelines", "run-123.json")
	if got != want {
		t.Errorf("pipelineStatePath() = %q, want %q", got, want)
	}
}

func TestSaveAndLoadState_ComplexState(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a complex state with all fields populated
	now := time.Now().Truncate(time.Second) // Truncate for comparison
	state := &ExecutionState{
		RunID:        "complex-run",
		WorkflowID:   "complex-workflow",
		Status:       StatusFailed,
		CurrentStep:  "step2",
		StartedAt:    now,
		UpdatedAt:    now.Add(time.Minute),
		FinishedAt:   now.Add(2 * time.Minute),
		Session:      "my-session",
		WorkflowFile: "/path/to/workflow.yaml",
		Variables: map[string]interface{}{
			"string_var": "hello",
			"int_var":    float64(42), // JSON unmarshals numbers as float64
			"bool_var":   true,
			"nested": map[string]interface{}{
				"inner": "value",
			},
		},
		Steps: map[string]StepResult{
			"step1": {
				Status:     StatusCompleted,
				Output:     "step1 output",
				StartedAt:  now,
				FinishedAt: now.Add(30 * time.Second),
				PaneUsed:   "pane-1",
			},
			"step2": {
				Status:     StatusFailed,
				Output:     "",
				Error:      &StepError{Type: "agent_error", Message: "Something went wrong"},
				StartedAt:  now.Add(30 * time.Second),
				FinishedAt: now.Add(40 * time.Second),
				Attempts:   3,
			},
		},
		Errors: []ExecutionError{
			{Type: "step_error", Message: "Error in step2", Fatal: false},
			{Type: "retry", Message: "Retry failed", Fatal: true},
		},
	}

	if err := SaveState(tmpDir, state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	loaded, err := LoadState(tmpDir, "complex-run")
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	// Verify all fields
	if loaded.RunID != state.RunID {
		t.Errorf("RunID = %q, want %q", loaded.RunID, state.RunID)
	}
	if loaded.WorkflowID != state.WorkflowID {
		t.Errorf("WorkflowID = %q, want %q", loaded.WorkflowID, state.WorkflowID)
	}
	if loaded.Status != state.Status {
		t.Errorf("Status = %v, want %v", loaded.Status, state.Status)
	}
	if loaded.CurrentStep != state.CurrentStep {
		t.Errorf("CurrentStep = %q, want %q", loaded.CurrentStep, state.CurrentStep)
	}
	if loaded.Session != state.Session {
		t.Errorf("Session = %q, want %q", loaded.Session, state.Session)
	}
	if loaded.WorkflowFile != state.WorkflowFile {
		t.Errorf("WorkflowFile = %q, want %q", loaded.WorkflowFile, state.WorkflowFile)
	}

	// Check variables
	if loaded.Variables["string_var"] != "hello" {
		t.Errorf("Variables[string_var] = %v, want 'hello'", loaded.Variables["string_var"])
	}
	if loaded.Variables["int_var"] != float64(42) {
		t.Errorf("Variables[int_var] = %v, want 42", loaded.Variables["int_var"])
	}
	if loaded.Variables["bool_var"] != true {
		t.Errorf("Variables[bool_var] = %v, want true", loaded.Variables["bool_var"])
	}

	// Check steps
	if len(loaded.Steps) != 2 {
		t.Errorf("len(Steps) = %d, want 2", len(loaded.Steps))
	}
	step1 := loaded.Steps["step1"]
	if step1.Status != StatusCompleted {
		t.Errorf("step1.Status = %v, want %v", step1.Status, StatusCompleted)
	}
	if step1.Output != "step1 output" {
		t.Errorf("step1.Output = %q, want 'step1 output'", step1.Output)
	}

	step2 := loaded.Steps["step2"]
	if step2.Status != StatusFailed {
		t.Errorf("step2.Status = %v, want %v", step2.Status, StatusFailed)
	}
	if step2.Error == nil || step2.Error.Message != "Something went wrong" {
		t.Errorf("step2.Error.Message = %v, want 'Something went wrong'", step2.Error)
	}
	if step2.Attempts != 3 {
		t.Errorf("step2.Attempts = %d, want 3", step2.Attempts)
	}

	// Check errors
	if len(loaded.Errors) != 2 {
		t.Errorf("len(Errors) = %d, want 2", len(loaded.Errors))
	}
}

func TestCleanupStates_MultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, ".ntm", "pipelines")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	// Create 5 files: 3 old, 2 new
	oldTime := time.Now().Add(-72 * time.Hour)
	newTime := time.Now().Add(-1 * time.Hour)

	for i := 1; i <= 3; i++ {
		runID := fmt.Sprintf("old-%d", i)
		path := pipelineStatePath(tmpDir, runID)
		if err := SaveState(tmpDir, cleanupStateFixture(runID)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("Failed to set time: %v", err)
		}
	}

	for i := 1; i <= 2; i++ {
		runID := fmt.Sprintf("new-%d", i)
		path := pipelineStatePath(tmpDir, runID)
		if err := SaveState(tmpDir, cleanupStateFixture(runID)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, newTime, newTime); err != nil {
			t.Fatalf("Failed to set time: %v", err)
		}
	}

	result, err := CleanupStates(t.Context(), tmpDir, 24*time.Hour, false)
	if err != nil {
		t.Fatalf("CleanupStates() error = %v", err)
	}
	if result.Deleted != 3 || result.Eligible != 3 || result.Retained != 2 {
		t.Errorf("cleanup counts = %+v, want 3 deleted and 2 retained", result)
	}

	// Stable control files remain in their own directory after cleanup.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	remaining := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			remaining++
		}
	}
	if remaining != 2 {
		t.Errorf("remaining state files = %d, want 2", remaining)
	}
}
