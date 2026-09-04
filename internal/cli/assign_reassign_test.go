package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestAssignmentEntryPointsRejectCanceledContextBeforeSideEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for name, run := range map[string]func() error{
		"retry":    func() error { return runRetryAssignments(ctx, "unused") },
		"reassign": func() error { return runReassignment(ctx, "unused") },
		"direct": func() error {
			return runDirectPaneAssignment(ctx, &AssignCommandOptions{Session: "unused", BeadIDs: []string{"bd-cancel"}, PaneSelector: "%1"})
		},
		"plan": func() error {
			_, err := getAssignOutputEnhanced(ctx, &AssignCommandOptions{Session: "unused"})
			return err
		},
		"execute": func() error {
			return executeAssignmentsEnhanced(ctx, "unused", &AssignOutputEnhanced{}, &AssignCommandOptions{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

type assignGlobalsSnapshot struct {
	cfg                *config.Config
	jsonOutput         bool
	assignReassign     string
	assignRetry        string
	assignRetryFailed  bool
	assignToPane       string
	assignToType       string
	assignForce        bool
	assignPrompt       string
	assignTemplate     string
	assignTemplateFile string
	assignTimeout      time.Duration
	assignQuiet        bool
	assignVerbose      bool
	assignRepoPath     string
	assignReserveFiles bool
}

func captureAssignGlobals() assignGlobalsSnapshot {
	return assignGlobalsSnapshot{
		cfg:                cfg,
		jsonOutput:         jsonOutput,
		assignReassign:     assignReassign,
		assignRetry:        assignRetry,
		assignRetryFailed:  assignRetryFailed,
		assignToPane:       assignToPane,
		assignToType:       assignToType,
		assignForce:        assignForce,
		assignPrompt:       assignPrompt,
		assignTemplate:     assignTemplate,
		assignTemplateFile: assignTemplateFile,
		assignTimeout:      assignTimeout,
		assignQuiet:        assignQuiet,
		assignVerbose:      assignVerbose,
		assignRepoPath:     assignRepoPath,
		assignReserveFiles: assignReserveFiles,
	}
}

func (s assignGlobalsSnapshot) restore() {
	cfg = s.cfg
	jsonOutput = s.jsonOutput
	assignReassign = s.assignReassign
	assignRetry = s.assignRetry
	assignRetryFailed = s.assignRetryFailed
	assignToPane = s.assignToPane
	assignToType = s.assignToType
	assignForce = s.assignForce
	assignPrompt = s.assignPrompt
	assignTemplate = s.assignTemplate
	assignTemplateFile = s.assignTemplateFile
	assignTimeout = s.assignTimeout
	assignQuiet = s.assignQuiet
	assignVerbose = s.assignVerbose
	assignRepoPath = s.assignRepoPath
	assignReserveFiles = s.assignReserveFiles
}

func setupReassignSession(t *testing.T, tmpDir string) (string, tmux.Pane, tmux.Pane) {
	t.Helper()

	sessionName := fmt.Sprintf("ntm-test-reassign-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = tmux.KillSession(sessionName)
	})

	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1, Model: "test-model"},
		{Type: AgentTypeCodex, Index: 1, Model: "test-model"},
	}
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		CodCount: 1,
		UserPane: true,
	}
	if err := spawnSessionLogicContext(t.Context(), opts); err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	if err := testutil.WaitForSession(sessionName, 5*time.Second); err != nil {
		t.Fatalf("WaitForSession failed: %v", err)
	}

	claudePane, codexPane, err := waitForAgentPanes(sessionName, 5*time.Second)
	if err != nil {
		t.Fatalf("waitForAgentPanes failed: %v", err)
	}

	return sessionName, claudePane, codexPane
}

func agentTypeLabel(pane tmux.Pane) string {
	switch pane.Type {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "codex"
	case tmux.AgentGemini:
		return "gemini"
	default:
		return "unknown"
	}
}

func waitForAgentPanes(sessionName string, timeout time.Duration) (tmux.Pane, tmux.Pane, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		panes, err := tmux.GetPanes(sessionName)
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}

		var claudePane *tmux.Pane
		var codexPane *tmux.Pane
		for i := range panes {
			switch panes[i].Type {
			case tmux.AgentClaude:
				claudePane = &panes[i]
			case tmux.AgentCodex:
				codexPane = &panes[i]
			}
		}

		if claudePane != nil && codexPane != nil {
			return *claudePane, *codexPane, nil
		}

		time.Sleep(100 * time.Millisecond)
	}

	if lastErr != nil {
		return tmux.Pane{}, tmux.Pane{}, fmt.Errorf("last tmux error: %w", lastErr)
	}
	return tmux.Pane{}, tmux.Pane{}, fmt.Errorf("timed out waiting for claude+codex panes in %s", sessionName)
}

func persistCanonicalAssignmentFixture(t *testing.T, store *assignment.AssignmentStore, beadID string, pane tmux.Pane, claimActor string) {
	t.Helper()
	record := store.Assignments[beadID]
	if record == nil {
		t.Fatalf("assignment fixture %s is missing", beadID)
	}
	record.DispatchTarget = pane.ID
	record.OccupancyKey = pane.ID
	record.IdempotencyKey = "fixture-" + beadID
	record.ClaimActor = claimActor
	record.ClaimState = assignment.ClaimClaimed
	record.DispatchState = assignment.DispatchSent
	if err := store.Save(); err != nil {
		t.Fatalf("persist canonical assignment fixture %s: %v", beadID, err)
	}
}

func TestRunReassignment_ToPane_Success(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()
	previousClaim := claimBeadForAssignmentWithPolicy
	previousStatus := getBeadStatusForAssignment
	previousDetails := getBeadAssignmentDetailsForAssignment
	claimBeadForAssignmentWithPolicy = func(_ context.Context, _ string, beadID, actor string, _ []string) (bv.BeadClaimResult, error) {
		return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	}
	getBeadStatusForAssignment = func(_ context.Context, _ string, _ string) (string, error) { return "in_progress", nil }
	getBeadAssignmentDetailsForAssignment = func(_ context.Context, _ string, beadID string) (*bv.BeadAssignmentDetails, error) {
		return &bv.BeadAssignmentDetails{ID: beadID, Status: "in_progress", Assignee: "LegacyClaude"}, nil
	}
	t.Cleanup(func() {
		claimBeadForAssignmentWithPolicy = previousClaim
		getBeadStatusForAssignment = previousStatus
		getBeadAssignmentDetailsForAssignment = previousDetails
	})

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	cfg.Agents.Gemini = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-123", "Test bead", claudePane.Index, "claude", "LegacyClaude", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-123", claudePane, "LegacyClaude")
	if err := store.MarkWorking("bd-123"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	assignReassign = "bd-123"
	assignToPane = fmt.Sprintf("%d", codexPane.Index)
	assignToType = ""
	assignForce = true
	assignPrompt = "Continue work on bd-123"
	assignTemplate = ""
	assignTemplateFile = ""
	assignQuiet = true
	assignVerbose = false
	assignReserveFiles = false

	output, err := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runReassignment failed: %v", err)
	}

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}
	if !envelope.Success || envelope.Data == nil {
		t.Fatalf("expected success envelope, got: %+v\nraw: %s", envelope, output)
	}
	if envelope.Data.Pane != codexPane.Index {
		t.Fatalf("expected pane %d, got %d", codexPane.Index, envelope.Data.Pane)
	}
	if envelope.Data.AgentType != agentTypeLabel(codexPane) {
		t.Fatalf("expected agent type %q, got %q", agentTypeLabel(codexPane), envelope.Data.AgentType)
	}
	if !envelope.Data.PromptSent {
		t.Fatalf("expected prompt to be sent")
	}
	if envelope.Data.PreviousStatus != string(assignment.StatusWorking) {
		t.Fatalf("expected previous status %q, got %q", assignment.StatusWorking, envelope.Data.PreviousStatus)
	}

	storeAfter, _ := assignment.LoadStore(sessionName)
	assignmentAfter := storeAfter.Get("bd-123")
	if assignmentAfter == nil {
		t.Fatalf("expected assignment to exist after reassignment")
	}
	if assignmentAfter.Pane != codexPane.Index {
		t.Fatalf("expected reassigned pane %d, got %d", codexPane.Index, assignmentAfter.Pane)
	}
	if assignmentAfter.AgentType != agentTypeLabel(codexPane) {
		t.Fatalf("expected reassigned agent type %q, got %q", agentTypeLabel(codexPane), assignmentAfter.AgentType)
	}
	if assignmentAfter.PromptSent != assignPrompt {
		t.Fatalf("expected persisted prompt %q, got %q", assignPrompt, assignmentAfter.PromptSent)
	}
	if assignmentAfter.ClaimActor != "LegacyClaude" || assignmentAfter.IdempotencyKey == "" || assignmentAfter.DispatchState != assignment.DispatchSent {
		t.Fatalf("expected atomic reassignment metadata with reused actor: %+v", assignmentAfter)
	}

	time.Sleep(400 * time.Millisecond)
	promptOutput, err := tmux.CapturePaneOutput(codexPane.ID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if !strings.Contains(promptOutput, assignPrompt) {
		t.Fatalf("expected prompt to be delivered, output:\n%s", promptOutput)
	}
}

func TestRunRetryAssignments_PreservesPreviousFailReasonAndMetadata(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()
	previousClaim := claimBeadForAssignmentWithPolicy
	previousStatus := getBeadStatusForAssignment
	previousDetails := getBeadAssignmentDetailsForAssignment
	claimBeadForAssignmentWithPolicy = func(_ context.Context, _ string, beadID, actor string, _ []string) (bv.BeadClaimResult, error) {
		return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	}
	getBeadStatusForAssignment = func(_ context.Context, _ string, _ string) (string, error) { return "open", nil }
	getBeadAssignmentDetailsForAssignment = func(_ context.Context, _ string, beadID string) (*bv.BeadAssignmentDetails, error) {
		return &bv.BeadAssignmentDetails{ID: beadID, Status: "open", Assignee: "RetryClaude"}, nil
	}
	t.Cleanup(func() {
		claimBeadForAssignmentWithPolicy = previousClaim
		getBeadStatusForAssignment = previousStatus
		getBeadAssignmentDetailsForAssignment = previousDetails
	})

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	cfg.Agents.Gemini = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)
	previousObserver := newAssignSessionObserver
	observedAt := time.Now().UTC()
	newAssignSessionObserver = func() assignSessionObserver {
		return fixedAssignSessionObserver{observation: statuspkg.SessionObservation{
			Session: sessionName, ObservedAt: observedAt, Complete: true,
			Panes: []statuspkg.PaneObservation{{
				Pane: tmux.PaneRef{ID: codexPane.ID, WindowIndex: codexPane.WindowIndex, PaneIndex: codexPane.Index},
				Current: statuspkg.StateObservation{
					Status:     statuspkg.AgentStatus{State: statuspkg.StateIdle},
					ObservedAt: observedAt,
					Freshness:  statuspkg.FreshnessFresh,
					Confidence: 0.99,
				},
			}},
		}}
	}
	t.Cleanup(func() { newAssignSessionObserver = previousObserver })

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-131", "Test bead 131", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-131", claudePane, "RetryClaude")
	if err := store.MarkWorking("bd-131"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}
	if err := store.MarkFailed("bd-131", "Agent crashed"); err != nil {
		t.Fatalf("MarkFailed failed: %v", err)
	}

	assignRetry = "bd-131"
	assignRetryFailed = false
	assignReserveFiles = false
	assignToPane = fmt.Sprintf("%d", codexPane.Index)
	assignToType = ""
	assignTemplate = "impl"
	assignTemplateFile = ""
	assignQuiet = true
	assignVerbose = false

	output, err := captureStdout(t, func() error { return runRetryAssignments(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runRetryAssignments failed: %v", err)
	}

	var envelope AssignEnvelope[RetryData]
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}
	if !envelope.Success || envelope.Data == nil {
		t.Fatalf("expected success envelope (run error %v), got: %+v\nraw: %s", err, envelope, output)
	}
	if len(envelope.Data.Retried) != 1 {
		t.Fatalf("expected exactly 1 retried item, got %d", len(envelope.Data.Retried))
	}

	item := envelope.Data.Retried[0]
	if item.PreviousFailReason != "Agent crashed" {
		t.Fatalf("expected previous fail reason %q, got %q", "Agent crashed", item.PreviousFailReason)
	}
	if item.RetryCount != 1 {
		t.Fatalf("expected retry count 1, got %d", item.RetryCount)
	}
	if !item.PromptSent {
		t.Fatalf("expected prompt to be sent")
	}

	expectedPrompt := expandPromptTemplate("bd-131", "Test bead 131", assignTemplate, assignTemplateFile)
	storeAfter, _ := assignment.LoadStore(sessionName)
	assignmentAfter := storeAfter.Get("bd-131")
	if assignmentAfter == nil {
		t.Fatalf("expected assignment to exist after retry")
	}
	if assignmentAfter.Pane != codexPane.Index {
		t.Fatalf("expected retried pane %d, got %d", codexPane.Index, assignmentAfter.Pane)
	}
	if assignmentAfter.RetryCount != 1 {
		t.Fatalf("expected persisted retry count 1, got %d", assignmentAfter.RetryCount)
	}
	if assignmentAfter.PromptSent != expectedPrompt {
		t.Fatalf("expected persisted prompt %q, got %q", expectedPrompt, assignmentAfter.PromptSent)
	}
}

func TestRunRetryAssignments_TargetedPendingMissingPhysicalPaneFailsAndPreservesLedger(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	cfg.Agents.Gemini = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, _ := setupReassignSession(t, tmpDir)
	const beadID = "ntm-pending-missing-pane"
	store := assignment.NewStore(sessionName)
	store.Assignments[beadID] = &assignment.Assignment{
		BeadID:         beadID,
		BeadTitle:      "Pending retry",
		Pane:           claudePane.Index,
		AgentType:      "claude",
		AgentName:      "BlueLake",
		Status:         assignment.StatusClaimed,
		AssignedAt:     time.Now().UTC(),
		IdempotencyKey: "pending-key",
		ClaimActor:     "BlueLake",
		ClaimState:     assignment.ClaimClaimed,
		DispatchState:  assignment.DispatchPending,
		DispatchTarget: "%999999",
		OccupancyKey:   "%999999",
		PendingPrompt:  "do not transfer",
	}
	if err := store.Save(); err != nil {
		t.Fatalf("save pending fixture: %v", err)
	}

	assignRetry = beadID
	assignRetryFailed = false
	assignToPane = ""
	assignToType = ""
	assignReserveFiles = false
	assignRepoPath = tmpDir
	assignQuiet = true
	assignVerbose = false

	output, err := captureStdout(t, func() error { return runRetryAssignments(t.Context(), sessionName) })
	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("targeted retry error = %v, want JSON failure exit", err)
	}
	var envelope AssignEnvelope[RetryData]
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode retry envelope: %v\noutput=%s", err, output)
	}
	if envelope.Success || envelope.Error == nil || envelope.Error.Code != "RETRY_SKIPPED" || envelope.Data == nil {
		t.Fatalf("targeted retry envelope = %+v", envelope)
	}
	if envelope.Data.Summary.RetriedCount != 0 || envelope.Data.Summary.SkippedCount != 1 || len(envelope.Data.Skipped) != 1 ||
		!strings.Contains(envelope.Data.Skipped[0].Reason, "%999999 is unavailable") {
		t.Fatalf("targeted retry data = %+v", envelope.Data)
	}

	reloaded, err := assignment.LoadStoreStrict(sessionName)
	if err != nil {
		t.Fatalf("reload pending ledger: %v", err)
	}
	pending := reloaded.Get(beadID)
	if pending == nil || pending.Status != assignment.StatusClaimed || pending.DispatchState != assignment.DispatchPending || pending.OccupancyKey != "%999999" {
		t.Fatalf("targeted retry changed pending ledger: %+v", pending)
	}
}

func TestRunReassignment_AlreadyAssigned(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	sessionName, claudePane, _ := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-124", "Test bead 124", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-124", claudePane, "ExistingClaude")
	if err := store.MarkWorking("bd-124"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	assignReassign = "bd-124"
	assignToPane = fmt.Sprintf("%d", claudePane.Index)
	assignToType = ""
	assignForce = true
	assignPrompt = "noop"

	output, err := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runReassignment failed: %v", err)
	}

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}
	if envelope.Success || envelope.Error == nil {
		t.Fatalf("expected error envelope, got: %+v", envelope)
	}
	if envelope.Error.Code != "ALREADY_ASSIGNED" {
		t.Fatalf("expected error code ALREADY_ASSIGNED, got %q", envelope.Error.Code)
	}
}

func TestRunReassignment_NoIdleAgentForType(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	sessionName, claudePane, _ := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-125", "Test bead 125", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-125", claudePane, "BusyClaude")
	if err := store.MarkWorking("bd-125"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	assignReassign = "bd-125"
	assignToPane = ""
	assignToType = "gemini"
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runReassignment failed: %v", err)
	}

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}
	if envelope.Success || envelope.Error == nil {
		t.Fatalf("expected error envelope, got: %+v", envelope)
	}
	if envelope.Error.Code != "NO_IDLE_AGENT" {
		t.Fatalf("expected error code NO_IDLE_AGENT, got %q", envelope.Error.Code)
	}
}

func TestRunReassignment_TargetBusyWithoutForce(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-126", "Test bead 126", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-126", claudePane, "BusyTargetClaude")
	if err := store.MarkWorking("bd-126"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	// Make the target pane appear busy: the fake codex (cat) echoes whatever it
	// receives, so echoing Codex's in-flight footer is what the status observer
	// classifies as working (a plain word leaves the composer idle).
	targetPaneID := codexPane.ID
	_ = tmux.SendKeys(targetPaneID, "• Working (esc to interrupt)", true)
	time.Sleep(200 * time.Millisecond)

	assignReassign = "bd-126"
	assignToPane = fmt.Sprintf("%d", codexPane.Index)
	assignToType = ""
	assignForce = false

	output, err := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runReassignment failed: %v", err)
	}

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}
	if envelope.Success || envelope.Error == nil {
		t.Fatalf("expected error envelope, got: %+v", envelope)
	}
	if envelope.Error.Code != "TARGET_BUSY" {
		t.Fatalf("expected error code TARGET_BUSY, got %q", envelope.Error.Code)
	}
}

func TestRunReassignment_NotAssigned(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	sessionName, _, codexPane := setupReassignSession(t, tmpDir)

	assignReassign = "bd-missing"
	assignToPane = fmt.Sprintf("%d", codexPane.Index)
	assignToType = ""
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runReassignment failed: %v", err)
	}

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}
	if envelope.Success || envelope.Error == nil {
		t.Fatalf("expected error envelope, got: %+v", envelope)
	}
	if envelope.Error.Code != "NOT_ASSIGNED" {
		t.Fatalf("expected error code NOT_ASSIGNED, got %q", envelope.Error.Code)
	}
}

func TestRunReassignment_ToPaneNotFound(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	sessionName, claudePane, _ := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-127", "Test bead 127", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-127", claudePane, "MissingTargetClaude")
	if err := store.MarkWorking("bd-127"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	t.Logf("TEST: %s - starting with bead bd-127, targeting non-existent pane 999", t.Name())

	assignReassign = "bd-127"
	assignToPane = "999" // Non-existent pane
	assignToType = ""
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runReassignment failed: %v", err)
	}

	t.Logf("TEST: %s - got output: %s", t.Name(), output)

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	t.Logf("TEST: %s - assertion: expect error envelope with PANE_NOT_FOUND", t.Name())
	if envelope.Success || envelope.Error == nil {
		t.Fatalf("expected error envelope, got: %+v", envelope)
	}
	if envelope.Error.Code != "PANE_NOT_FOUND" {
		t.Fatalf("expected error code PANE_NOT_FOUND, got %q", envelope.Error.Code)
	}
}

func TestRunReassignment_CompletedBead(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-128", "Test bead 128", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-128", claudePane, "CompletedClaude")
	if err := store.MarkWorking("bd-128"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}
	if err := store.MarkCompleted("bd-128"); err != nil {
		t.Fatalf("MarkCompleted failed: %v", err)
	}

	t.Logf("TEST: %s - starting with completed bead bd-128", t.Name())

	assignReassign = "bd-128"
	assignToPane = fmt.Sprintf("%d", codexPane.Index)
	assignToType = ""
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runReassignment failed: %v", err)
	}

	t.Logf("TEST: %s - got output: %s", t.Name(), output)

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	t.Logf("TEST: %s - assertion: expect error envelope with INVALID_STATE and status detail", t.Name())
	if envelope.Success || envelope.Error == nil {
		t.Fatalf("expected error envelope, got: %+v", envelope)
	}
	if envelope.Error.Code != "INVALID_STATE" {
		t.Fatalf("expected error code INVALID_STATE, got %q", envelope.Error.Code)
	}
	// Verify the details include current_status
	if envelope.Error.Details == nil {
		t.Fatalf("expected error details, got nil")
	}
	status, ok := envelope.Error.Details["current_status"].(string)
	if !ok || status != "completed" {
		t.Fatalf("expected current_status='completed' in details, got %v", envelope.Error.Details["current_status"])
	}
}

func TestRunReassignment_FailedBead(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-129", "Test bead 129", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-129", claudePane, "FailedClaude")
	if err := store.MarkWorking("bd-129"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}
	if err := store.MarkFailed("bd-129", "Agent crashed"); err != nil {
		t.Fatalf("MarkFailed failed: %v", err)
	}

	t.Logf("TEST: %s - starting with failed bead bd-129", t.Name())

	assignReassign = "bd-129"
	assignToPane = fmt.Sprintf("%d", codexPane.Index)
	assignToType = ""
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runReassignment failed: %v", err)
	}

	t.Logf("TEST: %s - got output: %s", t.Name(), output)

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	t.Logf("TEST: %s - assertion: expect early invalid-state rejection", t.Name())
	if envelope.Success {
		t.Fatalf("expected error envelope, got success: %+v", envelope)
	}
	if envelope.Error == nil {
		t.Fatalf("expected error envelope, got: %+v", envelope)
	}
	if envelope.Error.Code != "INVALID_STATE" {
		t.Fatalf("expected error code INVALID_STATE, got %q", envelope.Error.Code)
	}
	status, ok := envelope.Error.Details["current_status"].(string)
	if !ok || status != string(assignment.StatusFailed) {
		t.Fatalf("expected current_status=%q, got %v", assignment.StatusFailed, envelope.Error.Details["current_status"])
	}
}

func TestRunReassignment_ReservationRequiredFailsClosedWhenAgentMailUnavailable(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	// Point to non-existent Agent Mail to test graceful degradation
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-130", "Test bead with file reservations", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-130", claudePane, "ReservedClaude")
	if err := store.MarkWorking("bd-130"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	t.Logf("TEST: %s - starting with bead bd-130, Agent Mail disabled", t.Name())

	assignReassign = "bd-130"
	assignToPane = fmt.Sprintf("%d", codexPane.Index)
	assignToType = ""
	assignForce = true
	assignPrompt = "Continue work on bd-130"
	assignQuiet = true

	output, err := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if err != nil && !errors.Is(err, errJSONFailure) {
		t.Fatalf("runReassignment failed: %v", err)
	}

	t.Logf("TEST: %s - got output: %s", t.Name(), output)

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	if envelope.Success || envelope.Error == nil || envelope.Error.Code != "RESERVATION_REQUIRED" {
		t.Fatalf("expected reservation-required failure envelope, got: %+v", envelope)
	}
	reloaded, err := assignment.LoadStoreStrict(sessionName)
	if err != nil {
		t.Fatalf("reload assignment: %v", err)
	}
	durable := reloaded.Get("bd-130")
	if durable == nil || durable.Status != assignment.StatusWorking || durable.ClearState != assignment.ClearStateNone || durable.Pane != claudePane.Index {
		t.Fatalf("reservation preflight changed the original assignment: %+v", durable)
	}
}

func TestRunReassignment_ForceDoesNotBypassDurableTargetOccupancy(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)
	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-moving", "Moving bead", claudePane.Index, "claude", "LegacyClaude", "old prompt"); err != nil {
		t.Fatalf("assign moving bead: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-moving", claudePane, "LegacyClaude")
	if err := store.MarkWorking("bd-moving"); err != nil {
		t.Fatalf("mark moving bead working: %v", err)
	}
	occupied, err := store.Assign("bd-occupied", "Occupied bead", codexPane.Index, "codex", "ExistingCodex", "occupied prompt")
	if err != nil {
		t.Fatalf("assign occupied bead: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, occupied.BeadID, codexPane, "ExistingCodex")

	assignReassign = "bd-moving"
	assignToPane = codexPane.ID
	assignToType = ""
	assignForce = true
	assignReserveFiles = false
	output, runErr := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if !errors.Is(runErr, errJSONFailure) {
		t.Fatalf("runReassignment error = %v, want JSON failure", runErr)
	}
	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode reassignment output: %v\n%s", err, output)
	}
	if envelope.Success || envelope.Error == nil || envelope.Error.Code != "TARGET_BUSY" {
		t.Fatalf("force occupancy envelope = %+v", envelope)
	}
	reloaded, err := assignment.LoadStoreStrict(sessionName)
	if err != nil {
		t.Fatalf("reload assignment store: %v", err)
	}
	moving := reloaded.Get("bd-moving")
	if moving == nil || moving.Status != assignment.StatusWorking || moving.ClearState != assignment.ClearStateNone || moving.Pane != claudePane.Index {
		t.Fatalf("occupied target changed source assignment: %+v", moving)
	}
}

func TestRunReassignment_RedactionBlockLeavesRecoverableHandoffBarrier(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	cfg.Redaction.Mode = string(redaction.ModeBlock)
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)
	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-redaction", "Redaction bead", claudePane.Index, "claude", "LegacyClaude", "old prompt"); err != nil {
		t.Fatalf("assign redaction bead: %v", err)
	}
	persistCanonicalAssignmentFixture(t, store, "bd-redaction", claudePane, "LegacyClaude")
	if err := store.MarkWorking("bd-redaction"); err != nil {
		t.Fatalf("mark redaction bead working: %v", err)
	}
	before := store.Get("bd-redaction")
	if before == nil {
		t.Fatal("redaction assignment fixture is missing")
	}

	assignReassign = "bd-redaction"
	assignToPane = codexPane.ID
	assignToType = ""
	assignForce = true
	assignReserveFiles = false
	assignPrompt = "password=hunter2hunter2"
	output, runErr := captureStdout(t, func() error { return runReassignment(t.Context(), sessionName) })
	if !errors.Is(runErr, errJSONFailure) {
		t.Fatalf("runReassignment error = %v, want JSON failure", runErr)
	}
	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode reassignment output: %v\n%s", err, output)
	}
	if envelope.Success || envelope.Error == nil || envelope.Error.Code != "REDACTION_BLOCKED" {
		t.Fatalf("redaction envelope = %+v", envelope)
	}
	reloaded, err := assignment.LoadStoreStrict(sessionName)
	if err != nil {
		t.Fatalf("reload assignment store: %v", err)
	}
	durable := reloaded.Get("bd-redaction")
	if durable == nil ||
		durable.Status != before.Status ||
		durable.ClearState != before.ClearState ||
		durable.Pane != before.Pane ||
		durable.AgentType != before.AgentType ||
		durable.AgentName != before.AgentName ||
		durable.IdempotencyKey != before.IdempotencyKey ||
		durable.ClaimActor != before.ClaimActor ||
		durable.ClaimState != before.ClaimState ||
		durable.DispatchState != before.DispatchState ||
		durable.DispatchTarget != before.DispatchTarget ||
		durable.OccupancyKey != before.OccupancyKey ||
		durable.PromptSent != before.PromptSent ||
		durable.PendingPrompt != before.PendingPrompt {
		t.Fatalf("redaction failure changed the source before preflight completed: %+v", durable)
	}
	outputPane, err := tmux.CapturePaneOutput(codexPane.ID, 20)
	if err != nil {
		t.Fatalf("capture target pane: %v", err)
	}
	if strings.Contains(outputPane, "hunter2hunter2") {
		t.Fatalf("blocked reassignment leaked prompt to target pane: %q", outputPane)
	}
}
