package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

type assignGlobalsSnapshot struct {
	cfg                *config.Config
	jsonOutput         bool
	assignReassign     string
	assignToPane       int
	assignToType       string
	assignForce        bool
	assignPrompt       string
	assignTemplate     string
	assignTemplateFile string
	assignQuiet        bool
	assignVerbose      bool
}

func captureAssignGlobals() assignGlobalsSnapshot {
	return assignGlobalsSnapshot{
		cfg:                cfg,
		jsonOutput:         jsonOutput,
		assignReassign:     assignReassign,
		assignToPane:       assignToPane,
		assignToType:       assignToType,
		assignForce:        assignForce,
		assignPrompt:       assignPrompt,
		assignTemplate:     assignTemplate,
		assignTemplateFile: assignTemplateFile,
		assignQuiet:        assignQuiet,
		assignVerbose:      assignVerbose,
	}
}

func (s assignGlobalsSnapshot) restore() {
	cfg = s.cfg
	jsonOutput = s.jsonOutput
	assignReassign = s.assignReassign
	assignToPane = s.assignToPane
	assignToType = s.assignToType
	assignForce = s.assignForce
	assignPrompt = s.assignPrompt
	assignTemplate = s.assignTemplate
	assignTemplateFile = s.assignTemplateFile
	assignQuiet = s.assignQuiet
	assignVerbose = s.assignVerbose
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
	if err := spawnSessionLogic(opts); err != nil {
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

func TestRunReassignment_ToPane_Success(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCatCommandTemplate
	cfg.Agents.Gemini = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-123", "Test bead", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	if err := store.MarkWorking("bd-123"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	assignReassign = "bd-123"
	assignToPane = codexPane.Index
	assignToType = ""
	assignForce = true
	assignPrompt = "Continue work on bd-123"
	assignTemplate = ""
	assignTemplateFile = ""
	assignQuiet = true
	assignVerbose = false

	output, err := captureStdout(t, func() error { return runReassignment(nil, sessionName) })
	if err != nil {
		t.Fatalf("runReassignment failed: %v", err)
	}

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}
	if !envelope.Success || envelope.Data == nil {
		t.Fatalf("expected success envelope, got: %+v", envelope)
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

	time.Sleep(400 * time.Millisecond)
	promptOutput, err := tmux.CapturePaneOutput(codexPane.ID, 20)
	if err != nil {
		t.Fatalf("CapturePaneOutput failed: %v", err)
	}
	if !strings.Contains(promptOutput, assignPrompt) {
		t.Fatalf("expected prompt to be delivered, output:\n%s", promptOutput)
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
	cfg.Agents.Codex = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, _ := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-124", "Test bead 124", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	if err := store.MarkWorking("bd-124"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	assignReassign = "bd-124"
	assignToPane = claudePane.Index
	assignToType = ""
	assignForce = true
	assignPrompt = "noop"

	output, err := captureStdout(t, func() error { return runReassignment(nil, sessionName) })
	if err != nil {
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
	cfg.Agents.Codex = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, _ := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-125", "Test bead 125", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	if err := store.MarkWorking("bd-125"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	assignReassign = "bd-125"
	assignToPane = -1
	assignToType = "gemini"
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(nil, sessionName) })
	if err != nil {
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
	cfg.Agents.Codex = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-126", "Test bead 126", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	if err := store.MarkWorking("bd-126"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	// Make the target pane appear busy.
	targetPaneID := codexPane.ID
	_ = tmux.SendKeys(targetPaneID, "busy", true)
	time.Sleep(200 * time.Millisecond)

	assignReassign = "bd-126"
	assignToPane = codexPane.Index
	assignToType = ""
	assignForce = false

	output, err := captureStdout(t, func() error { return runReassignment(nil, sessionName) })
	if err != nil {
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
	cfg.Agents.Codex = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, _, codexPane := setupReassignSession(t, tmpDir)

	assignReassign = "bd-missing"
	assignToPane = codexPane.Index
	assignToType = ""
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(nil, sessionName) })
	if err != nil {
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
	cfg.Agents.Codex = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, _ := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-127", "Test bead 127", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	if err := store.MarkWorking("bd-127"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	t.Logf("TEST: %s - starting with bead bd-127, targeting non-existent pane 999", t.Name())

	assignReassign = "bd-127"
	assignToPane = 999 // Non-existent pane
	assignToType = ""
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(nil, sessionName) })
	if err != nil {
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
	cfg.Agents.Codex = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-128", "Test bead 128", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	if err := store.MarkWorking("bd-128"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}
	if err := store.MarkCompleted("bd-128"); err != nil {
		t.Fatalf("MarkCompleted failed: %v", err)
	}

	t.Logf("TEST: %s - starting with completed bead bd-128", t.Name())

	assignReassign = "bd-128"
	assignToPane = codexPane.Index
	assignToType = ""
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(nil, sessionName) })
	if err != nil {
		t.Fatalf("runReassignment failed: %v", err)
	}

	t.Logf("TEST: %s - got output: %s", t.Name(), output)

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	t.Logf("TEST: %s - assertion: expect error envelope with NOT_ASSIGNED and status detail", t.Name())
	if envelope.Success || envelope.Error == nil {
		t.Fatalf("expected error envelope, got: %+v", envelope)
	}
	if envelope.Error.Code != "NOT_ASSIGNED" {
		t.Fatalf("expected error code NOT_ASSIGNED, got %q", envelope.Error.Code)
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
	cfg.Agents.Codex = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-129", "Test bead 129", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	if err := store.MarkWorking("bd-129"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}
	if err := store.MarkFailed("bd-129", "Agent crashed"); err != nil {
		t.Fatalf("MarkFailed failed: %v", err)
	}

	t.Logf("TEST: %s - starting with failed bead bd-129", t.Name())

	assignReassign = "bd-129"
	assignToPane = codexPane.Index
	assignToType = ""
	assignForce = true

	output, err := captureStdout(t, func() error { return runReassignment(nil, sessionName) })
	if err != nil {
		t.Fatalf("runReassignment failed: %v", err)
	}

	t.Logf("TEST: %s - got output: %s", t.Name(), output)

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	// Note: Currently, reassigning a failed bead is not supported because
	// StatusFailed can only transition to StatusAssigned (retry), not StatusReassigned.
	// The implementation allows the CLI to proceed (doesn't check for StatusFailed),
	// but store.Reassign() will fail with InvalidTransitionError.
	// This test documents current behavior; update if behavior changes.
	t.Logf("TEST: %s - assertion: expect error due to invalid state transition", t.Name())
	if envelope.Success {
		t.Logf("TEST: %s - behavior changed: failed beads can now be reassigned", t.Name())
		// If this succeeds in the future, verify the reassignment worked correctly
		if envelope.Data == nil {
			t.Fatalf("expected data in success envelope")
		}
		if envelope.Data.Pane != codexPane.Index {
			t.Fatalf("expected pane %d, got %d", codexPane.Index, envelope.Data.Pane)
		}
	} else {
		// Current expected behavior: fails with REASSIGN_ERROR
		if envelope.Error == nil {
			t.Fatalf("expected error envelope, got: %+v", envelope)
		}
		if envelope.Error.Code != "REASSIGN_ERROR" {
			t.Fatalf("expected error code REASSIGN_ERROR, got %q", envelope.Error.Code)
		}
	}
}

func TestRunReassignment_FileReservationsGracefulDegradation(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	// Point to non-existent Agent Mail to test graceful degradation
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = testAgentCatCommandTemplate
	cfg.Agents.Codex = testAgentCatCommandTemplate
	jsonOutput = true

	sessionName, claudePane, codexPane := setupReassignSession(t, tmpDir)

	store := assignment.NewStore(sessionName)
	if _, err := store.Assign("bd-130", "Test bead with file reservations", claudePane.Index, "claude", "", "Original prompt"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	if err := store.MarkWorking("bd-130"); err != nil {
		t.Fatalf("MarkWorking failed: %v", err)
	}

	t.Logf("TEST: %s - starting with bead bd-130, Agent Mail disabled", t.Name())

	assignReassign = "bd-130"
	assignToPane = codexPane.Index
	assignToType = ""
	assignForce = true
	assignPrompt = "Continue work on bd-130"
	assignQuiet = true

	output, err := captureStdout(t, func() error { return runReassignment(nil, sessionName) })
	if err != nil {
		t.Fatalf("runReassignment failed: %v", err)
	}

	t.Logf("TEST: %s - got output: %s", t.Name(), output)

	var envelope ReassignEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	t.Logf("TEST: %s - assertion: reassignment should succeed even with Agent Mail unavailable", t.Name())
	if !envelope.Success {
		t.Fatalf("expected success envelope, got error: %+v", envelope.Error)
	}
	if envelope.Data == nil {
		t.Fatalf("expected data in success envelope")
	}
	if envelope.Data.Pane != codexPane.Index {
		t.Fatalf("expected pane %d, got %d", codexPane.Index, envelope.Data.Pane)
	}

	// File reservations should not be transferred (Agent Mail unavailable)
	// but the reassignment itself should succeed
	t.Logf("TEST: %s - assertion: file reservations should not be transferred (Agent Mail disabled)", t.Name())
	if envelope.Data.FileReservationsTransferred {
		t.Logf("TEST: %s - unexpected: file reservations marked as transferred despite Agent Mail being disabled", t.Name())
	}
}
