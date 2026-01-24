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

	time.Sleep(400 * time.Millisecond)

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
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
	if claudePane == nil || codexPane == nil {
		t.Fatalf("expected claude and codex panes, found claude=%v codex=%v", claudePane != nil, codexPane != nil)
	}

	return sessionName, *claudePane, *codexPane
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

func TestRunReassignment_ToPane_Success(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	snapshot := captureAssignGlobals()
	defer snapshot.restore()

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1")

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	cfg.AgentMail.Enabled = false
	cfg.Agents.Claude = "cat"
	cfg.Agents.Codex = "cat"
	cfg.Agents.Gemini = "cat"
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

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	cfg.AgentMail.Enabled = false
	cfg.Agents.Claude = "cat"
	cfg.Agents.Codex = "cat"
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

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	cfg.AgentMail.Enabled = false
	cfg.Agents.Claude = "cat"
	cfg.Agents.Codex = "cat"
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

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	cfg.AgentMail.Enabled = false
	cfg.Agents.Claude = "cat"
	cfg.Agents.Codex = "cat"
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
	targetPaneID := fmt.Sprintf("%s:%d", sessionName, codexPane.Index)
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

	cfg = config.Default()
	cfg.ProjectsBase = tmpDir
	cfg.AgentMail.Enabled = false
	cfg.Agents.Claude = "cat"
	cfg.Agents.Codex = "cat"
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
