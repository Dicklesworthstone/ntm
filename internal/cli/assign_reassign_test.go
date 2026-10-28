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
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

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
	assignWithCASS     bool
	assignNoCASS       bool
	assignWithMemory   bool
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
		assignWithCASS:     assignWithCASS,
		assignNoCASS:       assignNoCASS,
		assignWithMemory:   assignWithMemory,
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
	assignWithCASS = s.assignWithCASS
	assignNoCASS = s.assignNoCASS
	assignWithMemory = s.assignWithMemory
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

// --- Assignment-time CASS/CM prompt enrichment (--with-cass / --no-cass) ---

const (
	assignContextCASSMarker = "NTM_ASSIGN_CASS_HISTORY"
	assignContextTitle      = "Add rate limiting middleware"
)

// writeAssignContextStubCass writes a stub cass that logs each invocation's
// arguments and prints one dateless, high-scoring hit, so the relevance and
// age filters keep it.
func writeAssignContextStubCass(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "cass.log")
	payload := `{"query":"rate limiting","total_matches":1,"hits":[{"source_path":"/home/user/.cass/sessions/gateway/session-a.jsonl",` +
		`"line_number":7,"agent":"codex","content":"` + assignContextCASSMarker + ` token bucket per API key fixed the burst","score":0.93}]}`
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\ncat <<'NTM_FIXTURE_EOF'\n" + payload + "\nNTM_FIXTURE_EOF\n"
	binPath := filepath.Join(dir, "cass")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub cass: %v", err)
	}
	return binPath, logPath
}

func readAssignContextStubLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read stub cass log: %v", err)
	}
	return string(data)
}

// assertAssignContextSearches checks one assignment's cass calls. Assignment
// enrichment prefers the bead's own project, so context search runs the global
// query plus one scoped to that workspace, both over the bead's text.
func assertAssignContextSearches(t *testing.T, cassCalls, projectDir string, terms ...string) {
	t.Helper()
	searches, scoped := strings.Split(strings.TrimSpace(cassCalls), "\n"), 0
	for _, call := range searches {
		if !strings.HasPrefix(call, "search ") {
			t.Fatalf("cass calls = %q, want only searches", cassCalls)
		}
		for _, term := range terms {
			if !strings.Contains(call, term) {
				t.Fatalf("cass calls = %q, want every search over the bead (missing %q)", cassCalls, term)
			}
		}
		if strings.Contains(call, " --workspace="+projectDir+" ") {
			scoped++
		}
	}
	if len(searches) != 2 || scoped != 1 {
		t.Fatalf("cass calls = %q, want one global and one workspace-scoped search", cassCalls)
	}
}

// installAssignContextTracker stubs the tracker with a stateful fake: a claim
// makes the bead in_progress and owned by its actor, so a fresh assignment
// and a same-key recovery both pass the atomic eligibility gate.
func installAssignContextTracker(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	owners := map[string]string{}
	previousClaim, previousStatus, previousDetails := claimBeadForAssignmentWithPolicy, getBeadStatusForAssignment, getBeadAssignmentDetailsForAssignment
	claimBeadForAssignmentWithPolicy = func(_ context.Context, _ string, beadID, actor string, _ []string) (bv.BeadClaimResult, error) {
		mu.Lock()
		defer mu.Unlock()
		owners[beadID] = actor
		return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	}
	getBeadStatusForAssignment = func(_ context.Context, _ string, beadID string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if owners[beadID] != "" {
			return "in_progress", nil
		}
		return "open", nil
	}
	getBeadAssignmentDetailsForAssignment = func(_ context.Context, _ string, beadID string) (*bv.BeadAssignmentDetails, error) {
		mu.Lock()
		defer mu.Unlock()
		details := &bv.BeadAssignmentDetails{
			ID: beadID, Title: assignContextTitle, IssueType: "task", Status: "open",
			Labels: []string{"gateway"}, Description: "Throttle bursts with a token bucket keyed by API key.",
		}
		if owner := owners[beadID]; owner != "" {
			details.Status, details.Assignee = "in_progress", owner
		}
		return details, nil
	}
	t.Cleanup(func() {
		claimBeadForAssignmentWithPolicy = previousClaim
		getBeadStatusForAssignment = previousStatus
		getBeadAssignmentDetailsForAssignment = previousDetails
	})
}

// installAssignContextObserver reports pane freshly idle on every observation
// except those refuse selects, which fail before any keystroke is sent.
func installAssignContextObserver(t *testing.T, session string, pane tmux.Pane, refuse func(call int32) bool) {
	t.Helper()
	var calls atomic.Int32
	previous := newAssignSessionObserver
	newAssignSessionObserver = func() assignSessionObserver {
		return fixedAssignSessionObserver{observe: func(context.Context, string) (statuspkg.SessionObservation, error) {
			if call := calls.Add(1); refuse != nil && refuse(call) {
				return statuspkg.SessionObservation{}, errors.New("pane became busy")
			}
			observedAt := time.Now().UTC()
			return statuspkg.SessionObservation{
				Session: session, ObservedAt: observedAt, Complete: true,
				Panes: []statuspkg.PaneObservation{{
					Pane: tmux.PaneRef{ID: pane.ID, WindowIndex: pane.WindowIndex, PaneIndex: pane.Index},
					Current: statuspkg.StateObservation{
						Status:     statuspkg.AgentStatus{State: statuspkg.StateIdle},
						ObservedAt: observedAt,
						Freshness:  statuspkg.FreshnessFresh,
						Confidence: 0.99,
					},
				}},
			}, nil
		}}
	}
	t.Cleanup(func() { newAssignSessionObserver = previous })
}

// setupAssignContextSession spawns a real tmux session whose codex pane runs
// cat, and returns the session, its project directory, and that pane.
func setupAssignContextSession(t *testing.T) (string, string, tmux.Pane) {
	t.Helper()
	testutil.RequireTmuxThrottled(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")
	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCodexCommandTemplate
	cfg.Agents.Gemini = testAgentCatCommandTemplate
	jsonOutput = true // non-interactive spawn creates the project directory
	session, _, codexPane := setupReassignSession(t, tmpDir)
	return session, tmpDir, codexPane
}

func assignContextLedgerRow(t *testing.T, session, beadID string) *assignment.Assignment {
	t.Helper()
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load assignment ledger: %v", err)
	}
	row := store.Get(beadID)
	if row == nil {
		t.Fatalf("%s missing from the assignment ledger", beadID)
	}
	return row
}

func waitForAssignContextPaneText(t *testing.T, paneID, marker string) {
	t.Helper()
	deadline := time.Now().Add(testutil.ScaleTimeout(10 * time.Second))
	for {
		output, err := tmux.CapturePaneOutput(paneID, 120)
		if err == nil && strings.Contains(output, marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s never showed %q (err=%v):\n%s", paneID, marker, err, output)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestExecuteAssignmentsEnhancedEnrichesPromptBeforeRecordingIntent drives the
// `ntm assign --auto` execution path against a real tmux agent pane with a
// stub cass: [cass.context] enabled=true enriches the prompt before the intent
// is recorded (delivered to the pane and persisted in the ledger), --no-cass
// disables it without touching cass, and a missing cass degrades to a
// recorded skip while the bead is still assigned.
func TestExecuteAssignmentsEnhancedEnrichesPromptBeforeRecordingIntent(t *testing.T) {
	snapshot := captureAssignGlobals()
	defer snapshot.restore()
	installAssignContextTracker(t)
	session, projectDir, codexPane := setupAssignContextSession(t)
	installAssignContextObserver(t, session, codexPane, nil)
	cassBin, cassLog := writeAssignContextStubCass(t)
	cfg.CASS.BinaryPath = cassBin
	cfg.CASS.Context.Enabled = true

	execute := func(beadID string, noCASS bool) AssignmentItem {
		t.Helper()
		out := &AssignOutputEnhanced{Assignments: []AssignmentItem{{
			BeadID: beadID, BeadTitle: assignContextTitle, Pane: codexPane.Index,
			PaneTarget: assignmentPaneTarget(codexPane), PaneID: codexPane.ID, AgentType: "codex",
		}}}
		opts := &AssignCommandOptions{
			Session: session, ProjectDir: projectDir, Template: "impl", Quiet: true,
			Timeout: 15 * time.Second, NoCASS: noCASS, policyProject: projectDir,
		}
		if err := executeAssignmentsEnhanced(t.Context(), session, out, opts); err != nil {
			t.Fatalf("assign %s: %v (errors %v)", beadID, err, out.Errors)
		}
		if item := out.Assignments[0]; !item.PromptSent {
			t.Fatalf("assign %s did not deliver: %+v", beadID, item)
		}
		return out.Assignments[0]
	}
	release := func(beadID string) {
		t.Helper()
		store, err := assignment.LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("load ledger: %v", err)
		}
		if err := store.MarkFailed(beadID, "phase complete"); err != nil {
			t.Fatalf("free pane after %s: %v", beadID, err)
		}
		// Restart the fixture agent so the next phase meets a fresh composer
		// rather than the previous prompt's echo.
		if _, err := tmux.DefaultClient.Run("respawn-pane", "-k", "-t", codexPane.ID, `printf '\342\200\272 \n'; exec /bin/cat`); err != nil {
			t.Fatalf("respawn fixture agent: %v", err)
		}
		waitForAssignContextPaneText(t, codexPane.ID, "›")
	}

	// [cass.context] enabled=true with no flag: the history is injected.
	const enrichedBead = "ntm-ctx-config"
	enriched := execute(enrichedBead, false)
	if enriched.CASSInjection == nil || enriched.CASSInjection.ItemsInjected == 0 || enriched.CASSInjection.SkippedReason != "" {
		t.Fatalf("cass_injection = %+v, want injected history", enriched.CASSInjection)
	}
	raw, err := json.Marshal(enriched)
	if err != nil || !strings.Contains(string(raw), `"cass_injection"`) {
		t.Fatalf("assignment JSON lacks cass_injection: %s (%v)", raw, err)
	}
	cassCalls := readAssignContextStubLog(t, cassLog)
	assertAssignContextSearches(t, cassCalls, projectDir, "gateway", "bucket")
	if strings.Contains(cassCalls, "dependencies") {
		t.Fatalf("cass query carried template boilerplate: %q", cassCalls)
	}
	waitForAssignContextPaneText(t, codexPane.ID, assignContextCASSMarker)
	base := expandPromptTemplate(enrichedBead, assignContextTitle, "impl", "")
	row := assignContextLedgerRow(t, session, enrichedBead)
	if row.DispatchState != assignment.DispatchSent || !strings.Contains(row.PromptSent, assignContextCASSMarker) ||
		!strings.HasSuffix(row.PromptSent, "\n---\n\n"+base) {
		t.Fatalf("ledger row = %+v, want the enriched prompt recorded as sent", row)
	}
	if row.BaseIntentSHA256 != assignment.PromptSHA256(base) || row.IntentSHA256 != assignment.PromptSHA256(row.PromptSent) {
		t.Fatalf("ledger checksums base=%q intent=%q, want base over the template and intent over the enriched prompt", row.BaseIntentSHA256, row.IntentSHA256)
	}
	release(enrichedBead)

	// --no-cass overrides [cass.context] enabled=true and never runs cass.
	const disabledBead = "ntm-ctx-no-cass"
	disabled := execute(disabledBead, true)
	if disabled.CASSInjection != nil || disabled.MemoryInjection != nil {
		t.Fatalf("--no-cass assignment reported enrichment: cass=%+v memory=%+v", disabled.CASSInjection, disabled.MemoryInjection)
	}
	if calls := readAssignContextStubLog(t, cassLog); calls != cassCalls {
		t.Fatalf("--no-cass queried cass: %q", calls)
	}
	if row := assignContextLedgerRow(t, session, disabledBead); row.PromptSent != expandPromptTemplate(disabledBead, assignContextTitle, "impl", "") || row.BaseIntentSHA256 != "" {
		t.Fatalf("--no-cass ledger row = %+v, want the bare template prompt and no enrichment record", row)
	}
	release(disabledBead)

	// cass missing: still assigned, with the skip recorded.
	cfg.CASS.BinaryPath = filepath.Join(t.TempDir(), "no-such-cass")
	const degradedBead = "ntm-ctx-missing"
	degraded := execute(degradedBead, false)
	// Context search reports the cass client's ErrNotInstalled.
	if degraded.CASSInjection == nil || degraded.CASSInjection.ItemsInjected != 0 || !strings.Contains(degraded.CASSInjection.SkippedReason, "cass is not installed") {
		t.Fatalf("degraded cass_injection = %+v, want a recorded missing-cass skip", degraded.CASSInjection)
	}
	if row := assignContextLedgerRow(t, session, degradedBead); row.PromptSent != expandPromptTemplate(degradedBead, assignContextTitle, "impl", "") {
		t.Fatalf("degraded ledger prompt = %q, want the bare template prompt", row.PromptSent)
	}
}

// TestDirectPaneAssignWithCASSRecoveryReplaysRecordedPrompt drives
// `ntm assign --pane --with-cass`: the first attempt enriches and records the
// prompt, but the dispatch-time re-observation refuses before any keystroke;
// the same-intent re-run recovers that claim and delivers exactly the
// recorded enriched prompt without querying cass again.
func TestDirectPaneAssignWithCASSRecoveryReplaysRecordedPrompt(t *testing.T) {
	snapshot := captureAssignGlobals()
	defer snapshot.restore()
	installAssignContextTracker(t)
	session, projectDir, codexPane := setupAssignContextSession(t)
	// Observation 1 is the direct preflight gate; observation 2 is the first
	// attempt's dispatch-time re-check.
	installAssignContextObserver(t, session, codexPane, func(call int32) bool { return call == 2 })
	cassBin, cassLog := writeAssignContextStubCass(t)
	cfg.CASS.BinaryPath = cassBin // [cass.context] stays disabled: --with-cass alone drives injection
	jsonOutput = true

	const beadID = "ntm-ctx-direct"
	run := func() (AssignEnvelope[DirectAssignData], error) {
		t.Helper()
		opts := &AssignCommandOptions{
			Session: session, ProjectDir: projectDir, BeadIDs: []string{beadID}, PaneSelector: codexPane.ID,
			Template: "impl", IgnoreDeps: true, Quiet: true, Timeout: 15 * time.Second, WithCASS: true,
			policyProject: projectDir,
		}
		output, runErr := captureStdout(t, func() error { return runDirectPaneAssignment(t.Context(), opts) })
		var envelope AssignEnvelope[DirectAssignData]
		if err := json.Unmarshal([]byte(output), &envelope); err != nil {
			t.Fatalf("decode direct assignment JSON: %v\n%s", err, output)
		}
		return envelope, runErr
	}

	first, err := run()
	if !errors.Is(err, errJSONFailure) || first.Success || first.Data == nil || first.Data.Assignment == nil {
		t.Fatalf("first attempt = %+v (err %v), want a refused dispatch", first, err)
	}
	if cass := first.Data.Assignment.CASSInjection; cass == nil || cass.ItemsInjected == 0 {
		t.Fatalf("first attempt cass_injection = %+v, want injected history", cass)
	}
	cassCalls := readAssignContextStubLog(t, cassLog)
	assertAssignContextSearches(t, cassCalls, projectDir, "gateway")
	pending := assignContextLedgerRow(t, session, beadID)
	if pending.Status != assignment.StatusClaimed || pending.DispatchState != assignment.DispatchPending ||
		!strings.Contains(pending.PendingPrompt, assignContextCASSMarker) ||
		pending.BaseIntentSHA256 != assignment.PromptSHA256(expandPromptTemplate(beadID, assignContextTitle, "impl", "")) {
		t.Fatalf("pending ledger row = %+v, want a claimed, undelivered, enriched intent", pending)
	}

	second, err := run()
	if err != nil || !second.Success || second.Data == nil || second.Data.Assignment == nil || !second.Data.Assignment.PromptSent {
		t.Fatalf("recovery = %+v (err %v), want the recorded intent delivered", second, err)
	}
	if second.Data.Assignment.CASSInjection != nil {
		t.Fatalf("recovery re-ran enrichment: %+v", second.Data.Assignment.CASSInjection)
	}
	if calls := readAssignContextStubLog(t, cassLog); calls != cassCalls {
		t.Fatalf("recovery queried cass again: %q", calls)
	}
	waitForAssignContextPaneText(t, codexPane.ID, assignContextCASSMarker)
	if row := assignContextLedgerRow(t, session, beadID); row.PromptSent != pending.PendingPrompt || row.DispatchState != assignment.DispatchSent ||
		row.IdempotencyKey != pending.IdempotencyKey {
		t.Fatalf("recovered ledger row = %+v, want the recorded prompt sent under key %s", row, pending.IdempotencyKey)
	}
}

// TestResolvePromptContextOptionsMirrorsSendPrecedence pins the flag/config
// precedence the assignment surfaces share with --robot-send, and that every
// assignment command registers the flags.
func TestResolvePromptContextOptionsMirrorsSendPrecedence(t *testing.T) {
	cfgOn := config.Default() // [cass.context] enabled=true, [memory] send_injection=false
	cfgOn.CASS.Context.MaxTokens = 777
	cfgOn.Memory.SendMaxRules = 2

	if opts := resolvePromptContextOptions(false, false, false, cfgOn); !opts.WithCASS || opts.WithMemory || opts.InjectConfig == nil || opts.InjectConfig.MaxTokens != 777 {
		t.Fatalf("config default = %+v, want CASS on with [cass.context] parameters and memory off", opts)
	}
	if opts := resolvePromptContextOptions(true, true, false, cfgOn); opts.WithCASS {
		t.Fatal("--no-cass must override both --with-cass and [cass.context] enabled=true")
	}
	if opts := resolvePromptContextOptions(false, false, true, cfgOn); !opts.WithMemory || opts.MemoryInject == nil || opts.MemoryInject.MaxRules != 2 {
		t.Fatalf("--with-memory = %+v, want memory on with [memory] send_max_rules", opts)
	}
	cfgOn.Memory.SendInjection = true
	if opts := resolvePromptContextOptions(false, false, false, cfgOn); !opts.WithMemory {
		t.Fatal("[memory] send_injection=true must enable memory by default")
	}
	cfgOn.Memory.Enabled = false
	if opts := resolvePromptContextOptions(false, false, true, cfgOn); !opts.WithMemory || opts.MemoryInject.Enabled {
		t.Fatalf("[memory] enabled=false + --with-memory = %+v, want a request that degrades to a recorded skip", opts)
	}
	cfgOff := config.Default()
	cfgOff.CASS.Context.Enabled = false
	if opts := resolvePromptContextOptions(false, false, false, cfgOff); opts.Enabled() {
		t.Fatalf("[cass.context] off without flags = %+v, want nothing enabled", opts)
	}
	if opts := resolvePromptContextOptions(true, false, false, cfgOff); !opts.WithCASS {
		t.Fatal("--with-cass must enable injection when [cass.context] is off")
	}

	for name, cmd := range map[string]*cobra.Command{"assign": newAssignCmd(), "coordinator assign": newCoordinatorAssignCmd()} {
		for _, flag := range []string{"with-cass", "no-cass", "with-memory"} {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("ntm %s missing --%s", name, flag)
			}
		}
	}
}
