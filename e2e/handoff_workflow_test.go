//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-HANDOFF] Tests for agent-to-agent handoff workflow.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/handoff"
)

// HandoffTestSuite manages E2E tests for handoff workflows
type HandoffTestSuite struct {
	t        *testing.T
	logger   *TestLogger
	tempDir  string
	cleanup  []func()
	ntmPath  string
	origDir  string
}

// extractJSON extracts JSON from output that may contain log lines
// It finds the first { or [ and tries to parse from there
func extractJSON(output string) string {
	// Try to find start of JSON object or array
	objStart := strings.Index(output, "{")
	arrStart := strings.Index(output, "[")

	start := -1
	if objStart >= 0 && arrStart >= 0 {
		if objStart < arrStart {
			start = objStart
		} else {
			start = arrStart
		}
	} else if objStart >= 0 {
		start = objStart
	} else if arrStart >= 0 {
		start = arrStart
	}

	if start < 0 {
		return output
	}

	return strings.TrimSpace(output[start:])
}

// NewHandoffTestSuite creates a new test suite for handoff E2E tests
func NewHandoffTestSuite(t *testing.T, scenario string) *HandoffTestSuite {
	logger := NewTestLogger(t, scenario)

	// Find ntm binary
	ntmPath, err := exec.LookPath("ntm")
	if err != nil {
		t.Skip("ntm binary not found in PATH")
	}

	return &HandoffTestSuite{
		t:       t,
		logger:  logger,
		ntmPath: ntmPath,
	}
}

// Setup creates temp directory for testing
func (s *HandoffTestSuite) Setup() error {
	s.logger.Log("[E2E-HANDOFF] Setting up handoff test environment")

	tempDir, err := os.MkdirTemp("", "ntm-handoff-e2e-*")
	if err != nil {
		return err
	}
	s.tempDir = tempDir
	s.cleanup = append(s.cleanup, func() { os.RemoveAll(tempDir) })
	s.logger.Log("[E2E-HANDOFF] Created temp directory: %s", tempDir)

	// Change to temp directory
	s.origDir, err = os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(tempDir); err != nil {
		return err
	}
	s.cleanup = append(s.cleanup, func() { os.Chdir(s.origDir) })
	s.logger.Log("[E2E-HANDOFF] Changed to temp directory")

	return nil
}

// Teardown cleans up resources
func (s *HandoffTestSuite) Teardown() {
	s.logger.Log("[E2E-HANDOFF] Running cleanup (%d items)", len(s.cleanup))
	for i := len(s.cleanup) - 1; i >= 0; i-- {
		s.cleanup[i]()
	}
	s.logger.Close()
}

// runNTM executes ntm with arguments and returns output
func (s *HandoffTestSuite) runNTM(args ...string) (string, error) {
	s.logger.Log("[E2E-HANDOFF] Running: ntm %s", strings.Join(args, " "))
	cmd := exec.Command(s.ntmPath, args...)
	output, err := cmd.CombinedOutput()
	result := string(output)
	if err != nil {
		s.logger.Log("[E2E-HANDOFF] Command failed: %v, output: %s", err, result)
		return result, err
	}
	s.logger.Log("[E2E-HANDOFF] Output length: %d bytes", len(output))
	return result, nil
}

// =============================================================================
// Scenario 1: Basic Handoff Create/List/Show
// =============================================================================

func TestHandoffBasicWorkflow(t *testing.T) {
	suite := NewHandoffTestSuite(t, "handoff-basic")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-HANDOFF] Setup failed: %v", err)
	}
	defer suite.Teardown()

	suite.logger.Log("[E2E-HANDOFF] === Scenario 1: Basic Handoff Workflow ===")

	session := "test-session"
	goal := "Implemented authentication system"
	now := "Add unit tests for auth handlers"

	// Create handoff
	suite.logger.Log("[E2E-HANDOFF] Creating handoff")
	createOut, err := suite.runNTM("handoff", "create", session,
		"--goal", goal,
		"--now", now,
		"--format", "json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] handoff create failed: %v, output: %s", err, createOut)
	}

	var createResp struct {
		Success bool   `json:"success"`
		Path    string `json:"path"`
		Session string `json:"session"`
		Goal    string `json:"goal"`
		Now     string `json:"now"`
	}
	if err := json.Unmarshal([]byte(extractJSON(createOut)), &createResp); err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to parse create response: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Handoff created: path=%s", createResp.Path)

	if !createResp.Success {
		t.Fatal("[E2E-HANDOFF] Create response success=false")
	}
	if createResp.Session != session {
		t.Errorf("[E2E-HANDOFF] Session mismatch: got %s, want %s", createResp.Session, session)
	}
	if createResp.Goal != goal {
		t.Errorf("[E2E-HANDOFF] Goal mismatch: got %s, want %s", createResp.Goal, goal)
	}

	// List handoffs
	suite.logger.Log("[E2E-HANDOFF] Listing handoffs")
	listOut, err := suite.runNTM("handoff", "list", session, "--json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] handoff list failed: %v", err)
	}

	var listResp struct {
		Session  string `json:"session"`
		Count    int    `json:"count"`
		Handoffs []struct {
			Path    string `json:"path"`
			Session string `json:"session"`
			Goal    string `json:"goal"`
		} `json:"handoffs"`
	}
	if err := json.Unmarshal([]byte(extractJSON(listOut)), &listResp); err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to parse list response: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Found %d handoffs", listResp.Count)

	if listResp.Count < 1 {
		t.Fatal("[E2E-HANDOFF] No handoffs found")
	}
	if listResp.Handoffs[0].Goal != goal {
		t.Errorf("[E2E-HANDOFF] Listed goal mismatch")
	}

	// Show handoff
	suite.logger.Log("[E2E-HANDOFF] Showing handoff")
	showOut, err := suite.runNTM("handoff", "show", createResp.Path, "--json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] handoff show failed: %v", err)
	}

	var showResp struct {
		Session string `json:"session"`
		Goal    string `json:"goal"`
		Now     string `json:"now"`
	}
	if err := json.Unmarshal([]byte(extractJSON(showOut)), &showResp); err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to parse show response: %v", err)
	}

	if showResp.Goal != goal || showResp.Now != now {
		t.Error("[E2E-HANDOFF] Show response mismatch")
	}

	suite.logger.Log("[E2E-HANDOFF] PASS: Basic handoff workflow works")
}

// =============================================================================
// Scenario 2: Multi-hop Handoff Chain
// =============================================================================

func TestHandoffMultiHopChain(t *testing.T) {
	suite := NewHandoffTestSuite(t, "handoff-multihop")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-HANDOFF] Setup failed: %v", err)
	}
	defer suite.Teardown()

	suite.logger.Log("[E2E-HANDOFF] === Scenario 2: Multi-hop Handoff Chain ===")

	session := "chain-session"

	// Simulate Agent A creating a handoff
	suite.logger.Log("[E2E-HANDOFF] Agent A creates initial handoff")
	_, err := suite.runNTM("handoff", "create", session,
		"--goal", "Phase 1: Set up project structure",
		"--now", "Create main components",
		"--format", "json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Agent A handoff create failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond) // Ensure different timestamps

	// Simulate Agent B resuming and creating a handoff
	suite.logger.Log("[E2E-HANDOFF] Agent B creates second handoff")
	_, err = suite.runNTM("handoff", "create", session,
		"--goal", "Phase 2: Implemented main components",
		"--now", "Add error handling",
		"--format", "json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Agent B handoff create failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Simulate Agent C resuming and creating a handoff
	suite.logger.Log("[E2E-HANDOFF] Agent C creates third handoff")
	_, err = suite.runNTM("handoff", "create", session,
		"--goal", "Phase 3: Added error handling and tests",
		"--now", "Final documentation",
		"--format", "json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Agent C handoff create failed: %v", err)
	}

	// List all handoffs for the session
	suite.logger.Log("[E2E-HANDOFF] Listing all handoffs in chain")
	listOut, err := suite.runNTM("handoff", "list", session, "--json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] handoff list failed: %v", err)
	}

	var listResp struct {
		Count    int `json:"count"`
		Handoffs []struct {
			Goal string `json:"goal"`
		} `json:"handoffs"`
	}
	if err := json.Unmarshal([]byte(extractJSON(listOut)), &listResp); err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to parse list response: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Chain contains %d handoffs", listResp.Count)

	if listResp.Count != 3 {
		t.Errorf("[E2E-HANDOFF] Expected 3 handoffs in chain, got %d", listResp.Count)
	}

	// Verify most recent handoff is listed first (sorted by date desc)
	if len(listResp.Handoffs) >= 1 && !strings.Contains(listResp.Handoffs[0].Goal, "Phase 3") {
		suite.logger.Log("[E2E-HANDOFF] Note: Handoffs may not be sorted by date")
	}

	suite.logger.Log("[E2E-HANDOFF] PASS: Multi-hop handoff chain works")
}

// =============================================================================
// Scenario 3: Handoff with File Changes
// =============================================================================

func TestHandoffWithFileChanges(t *testing.T) {
	suite := NewHandoffTestSuite(t, "handoff-files")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-HANDOFF] Setup failed: %v", err)
	}
	defer suite.Teardown()

	suite.logger.Log("[E2E-HANDOFF] === Scenario 3: Handoff with File Changes ===")

	// Create a handoff using the internal package directly
	projectDir := suite.tempDir
	writer := handoff.NewWriter(projectDir)
	reader := handoff.NewReader(projectDir)

	h := handoff.New("file-changes-session").
		WithGoalAndNow("Refactored database layer", "Add migration scripts").
		WithStatus(handoff.StatusComplete, handoff.OutcomeSucceeded).
		AddTask("Restructured models", "models/user.go", "models/order.go").
		MarkCreated("db/connection.go", "db/migrations.go").
		MarkModified("config/database.yaml").
		MarkDeleted("legacy/old_db.go").
		AddDecision("orm", "Using GORM v2").
		AddFinding("performance", "Query time reduced by 40%")

	path, err := writer.Write(h, "refactor")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to write handoff: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Created handoff at: %s", path)

	// Read back and verify
	restored, err := reader.Read(path)
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to read handoff: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Verifying file changes")
	suite.logger.Log("[E2E-HANDOFF]   Created: %v", restored.Files.Created)
	suite.logger.Log("[E2E-HANDOFF]   Modified: %v", restored.Files.Modified)
	suite.logger.Log("[E2E-HANDOFF]   Deleted: %v", restored.Files.Deleted)

	// Verify file changes
	if len(restored.Files.Created) != 2 {
		t.Errorf("[E2E-HANDOFF] Expected 2 created files, got %d", len(restored.Files.Created))
	}
	if len(restored.Files.Modified) != 1 {
		t.Errorf("[E2E-HANDOFF] Expected 1 modified file, got %d", len(restored.Files.Modified))
	}
	if len(restored.Files.Deleted) != 1 {
		t.Errorf("[E2E-HANDOFF] Expected 1 deleted file, got %d", len(restored.Files.Deleted))
	}

	// Verify decisions and findings
	if restored.Decisions["orm"] != "Using GORM v2" {
		t.Errorf("[E2E-HANDOFF] Decision mismatch: %v", restored.Decisions)
	}
	if restored.Findings["performance"] != "Query time reduced by 40%" {
		t.Errorf("[E2E-HANDOFF] Finding mismatch: %v", restored.Findings)
	}

	// Verify total file changes
	if restored.TotalFileChanges() != 4 {
		t.Errorf("[E2E-HANDOFF] Expected 4 total file changes, got %d", restored.TotalFileChanges())
	}

	suite.logger.Log("[E2E-HANDOFF] PASS: Handoff with file changes works")
}

// =============================================================================
// Scenario 4: Resume Workflow
// =============================================================================

func TestHandoffResumeWorkflow(t *testing.T) {
	suite := NewHandoffTestSuite(t, "handoff-resume")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-HANDOFF] Setup failed: %v", err)
	}
	defer suite.Teardown()

	suite.logger.Log("[E2E-HANDOFF] === Scenario 4: Resume Workflow ===")

	session := "resume-test"
	goal := "Initial implementation"
	now := "Continue with integration"

	// Create initial handoff
	suite.logger.Log("[E2E-HANDOFF] Creating initial handoff")
	_, err := suite.runNTM("handoff", "create", session,
		"--goal", goal,
		"--now", now,
		"--format", "json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] handoff create failed: %v", err)
	}

	// Resume the session
	suite.logger.Log("[E2E-HANDOFF] Resuming session")
	resumeOut, err := suite.runNTM("resume", session, "--json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] resume failed: %v, output: %s", err, resumeOut)
	}

	var resumeResp struct {
		Success bool   `json:"success"`
		Action  string `json:"action"`
		Handoff struct {
			Goal string `json:"goal"`
			Now  string `json:"now"`
		} `json:"handoff"`
	}
	if err := json.Unmarshal([]byte(extractJSON(resumeOut)), &resumeResp); err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to parse resume response: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Resume action: %s", resumeResp.Action)

	if !resumeResp.Success {
		t.Error("[E2E-HANDOFF] Resume was not successful")
	}
	if resumeResp.Action != "display" {
		t.Errorf("[E2E-HANDOFF] Expected action=display, got %s", resumeResp.Action)
	}
	if resumeResp.Handoff.Goal != goal {
		t.Errorf("[E2E-HANDOFF] Resumed goal mismatch: got %s, want %s", resumeResp.Handoff.Goal, goal)
	}
	if resumeResp.Handoff.Now != now {
		t.Errorf("[E2E-HANDOFF] Resumed now mismatch: got %s, want %s", resumeResp.Handoff.Now, now)
	}

	suite.logger.Log("[E2E-HANDOFF] PASS: Resume workflow works")
}

// =============================================================================
// Scenario 5: Handoff Output Formats
// =============================================================================

func TestHandoffOutputFormats(t *testing.T) {
	suite := NewHandoffTestSuite(t, "handoff-formats")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-HANDOFF] Setup failed: %v", err)
	}
	defer suite.Teardown()

	suite.logger.Log("[E2E-HANDOFF] === Scenario 5: Handoff Output Formats ===")

	session := "format-test"
	goal := "Test output formats"
	now := "Verify all formats work"

	// Test JSON output
	suite.logger.Log("[E2E-HANDOFF] Testing JSON format")
	jsonOut, err := suite.runNTM("handoff", "create", session,
		"--goal", goal,
		"--now", now,
		"--output", "-",
		"--format", "json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] JSON format failed: %v", err)
	}

	var jsonResp map[string]interface{}
	if err := json.Unmarshal([]byte(extractJSON(jsonOut)), &jsonResp); err != nil {
		t.Errorf("[E2E-HANDOFF] JSON output is not valid JSON: %v", err)
	} else {
		suite.logger.Log("[E2E-HANDOFF] JSON format: valid")
	}

	// Test markdown output
	suite.logger.Log("[E2E-HANDOFF] Testing markdown format")
	mdOut, err := suite.runNTM("handoff", "create", session,
		"--goal", goal,
		"--now", now,
		"--output", "-",
		"--format", "markdown")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Markdown format failed: %v", err)
	}

	if !strings.Contains(mdOut, "# Handoff:") {
		t.Errorf("[E2E-HANDOFF] Markdown output missing expected header")
	}
	if !strings.Contains(mdOut, "## Goal") {
		t.Errorf("[E2E-HANDOFF] Markdown output missing Goal section")
	}
	suite.logger.Log("[E2E-HANDOFF] Markdown format: valid")

	// Test YAML output (default)
	suite.logger.Log("[E2E-HANDOFF] Testing YAML format")
	yamlOut, err := suite.runNTM("handoff", "create", session,
		"--goal", goal,
		"--now", now,
		"--output", "-",
		"--format", "yaml")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] YAML format failed: %v", err)
	}

	if !strings.Contains(yamlOut, "goal:") || !strings.Contains(yamlOut, "now:") {
		t.Errorf("[E2E-HANDOFF] YAML output missing expected fields")
	}
	suite.logger.Log("[E2E-HANDOFF] YAML format: valid")

	suite.logger.Log("[E2E-HANDOFF] PASS: All output formats work")
}

// =============================================================================
// Scenario 6: Session Listing
// =============================================================================

func TestHandoffSessionListing(t *testing.T) {
	suite := NewHandoffTestSuite(t, "handoff-sessions")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-HANDOFF] Setup failed: %v", err)
	}
	defer suite.Teardown()

	suite.logger.Log("[E2E-HANDOFF] === Scenario 6: Session Listing ===")

	// Create handoffs for multiple sessions
	sessions := []string{"project-alpha", "project-beta", "project-gamma"}

	for _, session := range sessions {
		suite.logger.Log("[E2E-HANDOFF] Creating handoff for session: %s", session)
		_, err := suite.runNTM("handoff", "create", session,
			"--goal", "Work on "+session,
			"--now", "Continue "+session,
			"--format", "json")
		if err != nil {
			t.Fatalf("[E2E-HANDOFF] Failed to create handoff for %s: %v", session, err)
		}
	}

	// List all sessions
	suite.logger.Log("[E2E-HANDOFF] Listing all sessions")
	listOut, err := suite.runNTM("handoff", "list", "--json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] handoff list failed: %v", err)
	}

	var listResp struct {
		Sessions []string `json:"sessions"`
		Count    int      `json:"count"`
	}
	if err := json.Unmarshal([]byte(extractJSON(listOut)), &listResp); err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to parse list response: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Found %d sessions: %v", listResp.Count, listResp.Sessions)

	if listResp.Count < len(sessions) {
		t.Errorf("[E2E-HANDOFF] Expected at least %d sessions, got %d", len(sessions), listResp.Count)
	}

	// Verify all sessions are listed
	sessionSet := make(map[string]bool)
	for _, s := range listResp.Sessions {
		sessionSet[s] = true
	}

	for _, expected := range sessions {
		if !sessionSet[expected] {
			t.Errorf("[E2E-HANDOFF] Session %s not found in list", expected)
		}
	}

	suite.logger.Log("[E2E-HANDOFF] PASS: Session listing works")
}

// =============================================================================
// Scenario 7: Agent Info in Handoff
// =============================================================================

func TestHandoffAgentInfo(t *testing.T) {
	suite := NewHandoffTestSuite(t, "handoff-agent-info")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-HANDOFF] Setup failed: %v", err)
	}
	defer suite.Teardown()

	suite.logger.Log("[E2E-HANDOFF] === Scenario 7: Agent Info in Handoff ===")

	// Create a handoff with agent info using the internal package
	projectDir := suite.tempDir
	writer := handoff.NewWriter(projectDir)
	reader := handoff.NewReader(projectDir)

	h := handoff.New("agent-info-session").
		WithGoalAndNow("Test agent info", "Verify agent metadata").
		SetAgentInfo("cc_1", handoff.AgentTypeClaude, "%42").
		SetTokenInfo(75000, 100000)

	path, err := writer.Write(h, "agent-info")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to write handoff: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Created handoff at: %s", path)

	// Read back and verify
	restored, err := reader.Read(path)
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to read handoff: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Agent info: id=%s type=%s pane=%s",
		restored.AgentID, restored.AgentType, restored.PaneID)
	suite.logger.Log("[E2E-HANDOFF] Token info: used=%d max=%d pct=%.1f%%",
		restored.TokensUsed, restored.TokensMax, restored.TokensPct)

	// Verify agent info
	if restored.AgentID != "cc_1" {
		t.Errorf("[E2E-HANDOFF] AgentID mismatch: got %s, want cc_1", restored.AgentID)
	}
	if restored.AgentType != handoff.AgentTypeClaude {
		t.Errorf("[E2E-HANDOFF] AgentType mismatch: got %s, want %s", restored.AgentType, handoff.AgentTypeClaude)
	}
	if restored.PaneID != "%42" {
		t.Errorf("[E2E-HANDOFF] PaneID mismatch: got %s, want %%42", restored.PaneID)
	}

	// Verify token info
	if restored.TokensUsed != 75000 {
		t.Errorf("[E2E-HANDOFF] TokensUsed mismatch: got %d, want 75000", restored.TokensUsed)
	}
	if restored.TokensMax != 100000 {
		t.Errorf("[E2E-HANDOFF] TokensMax mismatch: got %d, want 100000", restored.TokensMax)
	}
	if restored.TokensPct != 75.0 {
		t.Errorf("[E2E-HANDOFF] TokensPct mismatch: got %.1f, want 75.0", restored.TokensPct)
	}

	suite.logger.Log("[E2E-HANDOFF] PASS: Agent info in handoff works")
}

// =============================================================================
// Scenario 8: Handoff with Blockers
// =============================================================================

func TestHandoffWithBlockers(t *testing.T) {
	suite := NewHandoffTestSuite(t, "handoff-blockers")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-HANDOFF] Setup failed: %v", err)
	}
	defer suite.Teardown()

	suite.logger.Log("[E2E-HANDOFF] === Scenario 8: Handoff with Blockers ===")

	// Create a handoff with blockers using the internal package
	projectDir := suite.tempDir
	writer := handoff.NewWriter(projectDir)
	reader := handoff.NewReader(projectDir)

	h := handoff.New("blocked-session").
		WithGoalAndNow("Partial implementation", "Resolve blockers first").
		WithStatus(handoff.StatusBlocked, handoff.OutcomePartialMinus).
		AddBlocker("Waiting for API review").
		AddBlocker("Need database credentials").
		AddBlocker("Dependency version conflict")

	path, err := writer.Write(h, "blocked")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to write handoff: %v", err)
	}

	// Verify handoff file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("[E2E-HANDOFF] Handoff file not found: %v", err)
	}

	// Read back and verify
	restored, err := reader.Read(path)
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to read handoff: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Status: %s, Outcome: %s", restored.Status, restored.Outcome)
	suite.logger.Log("[E2E-HANDOFF] Blockers: %v", restored.Blockers)

	// Verify blocked status
	if !restored.IsBlocked() {
		t.Error("[E2E-HANDOFF] Expected IsBlocked() to return true")
	}
	if restored.Status != handoff.StatusBlocked {
		t.Errorf("[E2E-HANDOFF] Status mismatch: got %s, want blocked", restored.Status)
	}
	if restored.Outcome != handoff.OutcomePartialMinus {
		t.Errorf("[E2E-HANDOFF] Outcome mismatch: got %s, want partial-", restored.Outcome)
	}

	// Verify blockers
	if len(restored.Blockers) != 3 {
		t.Errorf("[E2E-HANDOFF] Expected 3 blockers, got %d", len(restored.Blockers))
	}

	suite.logger.Log("[E2E-HANDOFF] PASS: Handoff with blockers works")
}

// =============================================================================
// Scenario 9: Handoff Directory Structure
// =============================================================================

func TestHandoffDirectoryStructure(t *testing.T) {
	suite := NewHandoffTestSuite(t, "handoff-dirs")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-HANDOFF] Setup failed: %v", err)
	}
	defer suite.Teardown()

	suite.logger.Log("[E2E-HANDOFF] === Scenario 9: Handoff Directory Structure ===")

	session := "structure-test"

	// Create handoff
	_, err := suite.runNTM("handoff", "create", session,
		"--goal", "Test directory structure",
		"--now", "Verify paths",
		"--format", "json")
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] handoff create failed: %v", err)
	}

	// Verify directory structure
	handoffDir := filepath.Join(suite.tempDir, ".ntm", "handoffs", session)
	suite.logger.Log("[E2E-HANDOFF] Checking directory: %s", handoffDir)

	if _, err := os.Stat(handoffDir); err != nil {
		t.Errorf("[E2E-HANDOFF] Handoff directory not found: %v", err)
	}

	// List files in handoff directory
	entries, err := os.ReadDir(handoffDir)
	if err != nil {
		t.Fatalf("[E2E-HANDOFF] Failed to read handoff directory: %v", err)
	}

	suite.logger.Log("[E2E-HANDOFF] Found %d files in handoff directory", len(entries))

	if len(entries) < 1 {
		t.Error("[E2E-HANDOFF] No handoff files found")
	}

	// Verify file naming convention
	for _, entry := range entries {
		name := entry.Name()
		suite.logger.Log("[E2E-HANDOFF]   File: %s", name)
		if !strings.HasSuffix(name, ".yaml") {
			t.Errorf("[E2E-HANDOFF] Unexpected file extension: %s", name)
		}
	}

	suite.logger.Log("[E2E-HANDOFF] PASS: Directory structure is correct")
}
