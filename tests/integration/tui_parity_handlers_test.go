package integration

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// These tests verify root.go handler wiring for robot flags.
// Test scenarios:
// 1. Flag parsing correctness
// 2. Handler dispatch to correct Print function
// 3. Error output format
// 4. Exit codes (0 success, 1 error, 2 unavailable)

// =============================================================================
// --robot-help tests
// =============================================================================

func TestRobotHelpFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-help")
	logger.Log("Output length: %d bytes", len(out))

	// robot-help outputs formatted text help, not JSON
	outputStr := string(out)
	if outputStr == "" {
		t.Errorf("expected non-empty help output")
	}

	// Should contain key sections
	if !strings.Contains(outputStr, "robot-status") {
		t.Errorf("help should mention --robot-status")
	}
	if !strings.Contains(outputStr, "AI Agent") {
		t.Errorf("help should mention AI agents")
	}
}

// =============================================================================
// --robot-version tests
// =============================================================================

func TestRobotVersionFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-version")

	var payload struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildDate string `json:"build_date"`
		BuiltBy   string `json:"built_by"`
		GoVersion string `json:"go_version"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.Version == "" {
		t.Errorf("expected version field")
	}
	if payload.GoVersion == "" {
		t.Errorf("expected go_version field")
	}
	if payload.OS == "" {
		t.Errorf("expected os field")
	}
	if payload.Arch == "" {
		t.Errorf("expected arch field")
	}
}

// =============================================================================
// --robot-status tests
// =============================================================================

func TestRobotStatusFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-status")

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		Sessions    []struct {
			Name   string `json:"name"`
			Panes  int    `json:"panes"`
			Agents []struct {
				Type string `json:"type"`
				Pane string `json:"pane"`
			} `json:"agents"`
		} `json:"sessions"`
		Summary struct {
			TotalSessions int `json:"total_sessions"`
			TotalAgents   int `json:"total_agents"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.GeneratedAt == "" {
		t.Errorf("expected generated_at field")
	}
	// Sessions can be empty if no tmux sessions exist
	if payload.Sessions == nil {
		t.Errorf("sessions should be an array, not nil")
	}
}

// =============================================================================
// --robot-snapshot tests
// =============================================================================

func TestRobotSnapshotFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-snapshot")

	// Find JSON start (may have warning messages before)
	jsonStart := strings.Index(string(out), "{")
	if jsonStart == -1 {
		t.Fatalf("no JSON found in output")
	}
	jsonBytes := out[jsonStart:]

	var payload struct {
		TS       string `json:"ts"`
		Sessions []struct {
			Name     string `json:"name"`
			Attached bool   `json:"attached"`
			Agents   []struct {
				Pane  string `json:"pane"`
				Type  string `json:"type"`
				State string `json:"state"`
			} `json:"agents"`
		} `json:"sessions"`
		BeadsSummary struct {
			Available bool   `json:"available"`
			Reason    string `json:"reason,omitempty"`
		} `json:"beads_summary"`
		AgentMail struct {
			Available bool   `json:"available"`
			Reason    string `json:"reason,omitempty"`
		} `json:"agent_mail"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.TS == "" {
		t.Errorf("expected ts field")
	}
	// Sessions array should always be present (even if empty)
	if payload.Sessions == nil {
		t.Errorf("sessions should be an array, not nil")
	}
}

func TestRobotSnapshotWithSinceFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-snapshot", "--since=2025-01-01T00:00:00Z")

	// Find JSON start (may have warning messages before)
	jsonStart := strings.Index(string(out), "{")
	if jsonStart == -1 {
		t.Fatalf("no JSON found in output")
	}
	jsonBytes := out[jsonStart:]

	var payload struct {
		TS      string `json:"ts"`
		IsDelta bool   `json:"is_delta"`
		Since   string `json:"since"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// With --since, the response contains 'since' field and 'changes' array
	if payload.Since == "" {
		t.Errorf("expected since field when --since is provided")
	}
}

func TestRobotSnapshotInvalidSinceFormat(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	cmd := exec.Command("ntm", "--robot-snapshot", "--since=invalid-timestamp")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail with exit code 1 due to invalid timestamp
	if err == nil {
		t.Errorf("expected command to fail with invalid --since timestamp")
	}

	// Error should mention expected format
	if !strings.Contains(string(out), "RFC3339") && !strings.Contains(string(out), "ISO8601") {
		t.Errorf("error message should mention expected timestamp format")
	}
}

// =============================================================================
// --robot-tail tests (requires session argument)
// =============================================================================

func TestRobotTailMissingSession(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Without session value, Cobra returns a text error
	cmd := exec.Command("ntm", "--robot-tail")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail since flag requires an argument
	if err == nil {
		t.Errorf("expected command to fail when --robot-tail has no value")
	}
	if !strings.Contains(string(out), "robot-tail") {
		t.Errorf("error output should mention the flag name")
	}
}

func TestRobotTailWithNonexistentSession(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)

	cmd := exec.Command("ntm", "--robot-tail=nonexistent-session-12345")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail with exit code 1 for nonexistent session
	if err == nil {
		t.Logf("command succeeded (session may exist)")
		return
	}

	// Should return JSON error or text error
	outputStr := string(out)
	jsonStart := strings.Index(outputStr, "{")
	if jsonStart != -1 {
		// Extract JSON portion (may have warnings before it)
		jsonBytes := []byte(outputStr[jsonStart:])
		var payload struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(jsonBytes, &payload) == nil {
			if payload.Success {
				t.Errorf("expected success=false for nonexistent session")
			}
		}
	}
}

// =============================================================================
// --robot-send tests (requires session and --msg)
// =============================================================================

func TestRobotSendMissingMsg(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// --robot-send requires --msg
	cmd := exec.Command("ntm", "--robot-send=testsession")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Should fail because --msg is required
	if err == nil {
		t.Errorf("expected command to fail when --msg is not provided")
	}
	if !strings.Contains(string(out), "msg") {
		t.Errorf("error should mention missing --msg flag")
	}
}

// =============================================================================
// --robot-graph tests
// =============================================================================

func TestRobotGraphFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-graph")

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		Available   bool   `json:"available"`
		Insights    struct {
			Bottlenecks []struct {
				ID    string  `json:"ID"`
				Value float64 `json:"Value"`
			} `json:"Bottlenecks"`
		} `json:"insights,omitempty"`
		Error string `json:"error,omitempty"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.GeneratedAt == "" {
		t.Errorf("expected generated_at field")
	}
	// May return error if bv not available - that's okay
	if !payload.Available && payload.Error != "" {
		t.Logf("robot-graph returned error (expected if bv not available): %s", payload.Error)
	}
}

// =============================================================================
// --robot-health tests
// =============================================================================

func TestRobotHealthFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	// With empty value, shows TUI help. Test with a session name instead.
	// Use a non-existent session to test error handling
	cmd := exec.Command("ntm", "--robot-health=nonexistent-session")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Command should succeed (exit 0) but return success=false for nonexistent session
	if err != nil {
		t.Fatalf("expected command to exit 0, got error: %v", err)
	}

	var payload struct {
		Success   bool   `json:"success"`
		Session   string `json:"session"`
		CheckedAt string `json:"checked_at"`
		Error     string `json:"error,omitempty"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// For nonexistent session, success should be false
	if payload.Success {
		t.Errorf("expected success=false for nonexistent session")
	}
	if payload.Session != "nonexistent-session" {
		t.Errorf("expected session='nonexistent-session', got %q", payload.Session)
	}
	if payload.CheckedAt == "" {
		t.Errorf("expected checked_at field")
	}
}

// =============================================================================
// --robot-recipes tests
// =============================================================================

func TestRobotRecipesFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-recipes")

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		Count       int    `json:"count"`
		Recipes     []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"recipes"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.GeneratedAt == "" {
		t.Errorf("expected generated_at field")
	}
	if payload.Recipes == nil {
		t.Errorf("recipes should be an array")
	}
	if payload.Count == 0 && len(payload.Recipes) > 0 {
		t.Errorf("count should match recipes length")
	}
}

// =============================================================================
// --robot-schema tests
// =============================================================================

func TestRobotSchemaFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Test various schema types
	schemaTypes := []string{"status", "send", "spawn", "interrupt", "tail", "ack", "snapshot", "all"}
	for _, schemaType := range schemaTypes {
		t.Run(schemaType, func(t *testing.T) {
			out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-schema="+schemaType)

			// Should return valid JSON Schema
			var payload map[string]interface{}
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("invalid JSON for schema %s: %v", schemaType, err)
			}

			// Check for common JSON Schema fields
			if _, ok := payload["$schema"]; !ok {
				// May have definitions or properties instead
				if _, hasDefs := payload["definitions"]; !hasDefs {
					if _, hasProps := payload["properties"]; !hasProps {
						t.Logf("schema %s may use non-standard structure", schemaType)
					}
				}
			}
		})
	}
}

func TestRobotSchemaInvalidType(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	cmd := exec.Command("ntm", "--robot-schema=invalid-schema-type")
	out, _ := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	// Command returns exit 0 with JSON error body
	// This is valid behavior - check the JSON response
	var payload struct {
		Success   bool   `json:"success"`
		Error     string `json:"error"`
		ErrorCode string `json:"error_code"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.Success {
		t.Errorf("expected success=false for invalid schema type")
	}
	if payload.ErrorCode != "INVALID_FLAG" {
		t.Errorf("expected error_code=INVALID_FLAG, got %s", payload.ErrorCode)
	}
}

// =============================================================================
// --robot-terse tests
// =============================================================================

func TestRobotTerseFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-terse")

	// Terse output is single-line encoded state, not JSON
	// Format: S:session|A:ready/total|W:working|I:idle|B:beads|M:mail|!:alerts
	outputStr := strings.TrimSpace(string(out))
	if outputStr == "" {
		t.Errorf("expected non-empty terse output")
	}

	// Terse output uses pipe separators
	if !strings.Contains(outputStr, "|") {
		t.Logf("terse output may be empty or different format: %q", outputStr)
	}
}

// =============================================================================
// --robot-markdown tests
// =============================================================================

func TestRobotMarkdownFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-markdown")

	// Markdown output should contain markdown formatting
	outputStr := string(out)
	if outputStr == "" {
		t.Errorf("expected non-empty markdown output")
	}

	// Should contain some markdown elements (headers, tables, etc)
	if !strings.Contains(outputStr, "#") && !strings.Contains(outputStr, "|") {
		t.Logf("markdown output may be empty: %q", outputStr)
	}
}

func TestRobotMarkdownWithSectionsFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-markdown", "--md-sections=sessions,beads")

	outputStr := string(out)
	if outputStr == "" {
		t.Errorf("expected non-empty markdown output")
	}
}

// =============================================================================
// --robot-dashboard tests
// =============================================================================

func TestRobotDashboardFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-dashboard")

	// Without --json, outputs markdown
	outputStr := string(out)
	if outputStr == "" {
		t.Errorf("expected non-empty dashboard output")
	}
}

func TestRobotDashboardWithJSONFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-dashboard", "--json")

	// Find JSON start (may have warning messages before)
	jsonStart := strings.Index(string(out), "{")
	if jsonStart == -1 {
		t.Fatalf("no JSON found in output")
	}
	jsonBytes := out[jsonStart:]

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		Fleet       string `json:"fleet"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.GeneratedAt == "" {
		t.Errorf("expected generated_at field")
	}
}

// =============================================================================
// Exit code tests
// =============================================================================

func TestRobotFlagsExitCodeSuccess(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// These flags should exit with code 0
	successFlags := []string{
		"--robot-help",
		"--robot-version",
		"--robot-recipes",
	}

	for _, flag := range successFlags {
		t.Run(flag, func(t *testing.T) {
			cmd := exec.Command("ntm", flag)
			out, err := cmd.CombinedOutput()
			logger.Log("%s output: %s", flag, string(out))

			if err != nil {
				t.Errorf("%s should exit with code 0, got error: %v", flag, err)
			}
		})
	}
}

func TestRobotFlagsExitCodeMissingArg(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Flags that require arguments should fail without them
	flagsRequiringArgs := []string{
		"--robot-tail",
		"--robot-send",
		"--robot-health",
		"--robot-schema",
		"--robot-context",
	}

	for _, flag := range flagsRequiringArgs {
		t.Run(flag, func(t *testing.T) {
			cmd := exec.Command("ntm", flag)
			out, err := cmd.CombinedOutput()
			logger.Log("%s output: %s", flag, string(out))

			if err == nil {
				t.Errorf("%s should exit with non-zero code when missing required argument", flag)
			}
		})
	}
}

// =============================================================================
// Error output format tests
// =============================================================================

func TestRobotErrorOutputToStderr(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)

	// Command that will fail
	cmd := exec.Command("ntm", "--robot-tail=nonexistent-session-xyz")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	logger.Log("stdout: %s", stdout.String())
	logger.Log("stderr: %s", stderr.String())

	if err == nil {
		t.Logf("command succeeded (session may exist)")
		return
	}

	// Error output should go to stderr
	stderrStr := stderr.String()
	if stderrStr == "" {
		// Some errors may be in JSON to stdout
		t.Logf("no stderr output, checking stdout for JSON error")
	}
}

// =============================================================================
// Flag combinations tests
// =============================================================================

func TestRobotSnapshotWithBeadLimit(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-snapshot", "--bead-limit=10")

	// Find JSON start (may have warning messages before)
	jsonStart := strings.Index(string(out), "{")
	if jsonStart == -1 {
		t.Fatalf("no JSON found in output")
	}
	jsonBytes := out[jsonStart:]

	var payload struct {
		TS           string `json:"ts"`
		BeadsSummary struct {
			Available bool   `json:"available"`
			Reason    string `json:"reason,omitempty"`
		} `json:"beads_summary"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Bead limit flag affects output when beads are available
	if payload.TS == "" {
		t.Errorf("expected ts field")
	}
}

func TestRobotMarkdownCompactFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-markdown", "--compact")

	outputStr := string(out)
	if outputStr == "" {
		t.Errorf("expected non-empty compact markdown output")
	}
}

// =============================================================================
// Handler dispatch verification tests
// =============================================================================

func TestRobotMailHandlerDispatch(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-mail")

	// robot-mail returns JSON with session and inbox/outbox info
	var payload map[string]interface{}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify it returns a valid JSON object (not empty)
	// The response structure varies based on mail server availability,
	// but should always contain some fields
	if len(payload) == 0 {
		t.Errorf("expected non-empty JSON object, got empty map")
	}
}

func TestRobotPlanHandlerDispatch(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-plan")

	var payload struct {
		GeneratedAt    string `json:"generated_at"`
		Recommendation string `json:"recommendation"`
		Actions        []struct {
			Priority int    `json:"priority"`
			Command  string `json:"command"`
		} `json:"actions"`
		Warnings []string `json:"warnings"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.GeneratedAt == "" {
		t.Errorf("expected generated_at field")
	}
	// Actions and Recommendation are optional - may be empty if no sessions exist
}

func TestRobotTokensHandlerDispatch(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-tokens")

	var payload struct {
		Timestamp string `json:"timestamp"`
		Success   bool   `json:"success"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.Timestamp == "" {
		t.Errorf("expected timestamp field")
	}
}

func TestRobotCassStatusHandlerDispatch(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-cass-status")

	var payload struct {
		CassAvailable bool `json:"cass_available"`
		Healthy       bool `json:"healthy"`
		Index         struct {
			Exists bool `json:"exists"`
		} `json:"index"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify it has expected structure
	// CassAvailable indicates whether CASS is available
	// The specific value may vary based on environment
}

// =============================================================================
// --robot-files tests (ntm-zei3: E2E test with session filter and time window)
// =============================================================================

func TestRobotFilesFlagParsing(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	// Test with a session name (doesn't need to exist - returns empty changes)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-files=test-session")

	var payload struct {
		Success    bool   `json:"success"`
		Session    string `json:"session"`
		TimeWindow string `json:"time_window"`
		Count      int    `json:"count"`
		Changes    []struct {
			Timestamp string   `json:"timestamp"`
			Path      string   `json:"path"`
			Operation string   `json:"operation"`
			Agents    []string `json:"agents"`
			Session   string   `json:"session"`
		} `json:"changes"`
		Summary struct {
			TotalChanges    int            `json:"total_changes"`
			UniqueFiles     int            `json:"unique_files"`
			ByAgent         map[string]int `json:"by_agent"`
			ByOperation     map[string]int `json:"by_operation"`
			MostActiveAgent string         `json:"most_active_agent,omitempty"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	if payload.Session != "test-session" {
		t.Errorf("expected session='test-session', got %q", payload.Session)
	}
	if payload.TimeWindow == "" {
		t.Errorf("expected time_window field to be set")
	}
	// Changes array should exist (even if empty)
	if payload.Changes == nil {
		t.Errorf("changes should be an array, not nil")
	}
}

func TestRobotFilesTimeWindowFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)

	testCases := []struct {
		name           string
		args           []string
		expectedWindow string
	}{
		{
			name:           "default window",
			args:           []string{"--robot-files=test"},
			expectedWindow: "15m",
		},
		{
			name:           "5m window",
			args:           []string{"--robot-files=test", "--window=5m"},
			expectedWindow: "5m",
		},
		{
			name:           "1h window",
			args:           []string{"--robot-files=test", "--window=1h"},
			expectedWindow: "1h",
		},
		{
			name:           "all window",
			args:           []string{"--robot-files=test", "--window=all"},
			expectedWindow: "all",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger := testutil.NewTestLoggerStdout(t)
			out := testutil.AssertCommandSuccess(t, logger, "ntm", tc.args...)

			var payload struct {
				TimeWindow string `json:"time_window"`
			}

			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}

			if payload.TimeWindow != tc.expectedWindow {
				t.Errorf("expected time_window=%q, got %q", tc.expectedWindow, payload.TimeWindow)
			}
		})
	}
}

func TestRobotFilesLimitFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	// Test with explicit limit (verifies flag is parsed)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-files=test", "--files-limit=50")

	var payload struct {
		Success bool `json:"success"`
		Count   int  `json:"count"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Count should be <= limit (can't verify exact limit without actual file changes)
	if payload.Count > 50 {
		t.Errorf("count (%d) exceeded limit (50)", payload.Count)
	}
}

func TestRobotFilesEmptySession(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	// Non-existent session should still return valid JSON with empty changes
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-files=nonexistent-session-xyz")

	var payload struct {
		Success bool   `json:"success"`
		Session string `json:"session"`
		Count   int    `json:"count"`
		Changes []struct {
			Path string `json:"path"`
		} `json:"changes"`
		Summary struct {
			TotalChanges int `json:"total_changes"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Non-existent session should have no changes
	if payload.Count != 0 {
		t.Errorf("expected count=0 for non-existent session, got %d", payload.Count)
	}
	if len(payload.Changes) != 0 {
		t.Errorf("expected empty changes for non-existent session, got %d", len(payload.Changes))
	}
	if payload.Summary.TotalChanges != 0 {
		t.Errorf("expected summary.total_changes=0, got %d", payload.Summary.TotalChanges)
	}
}

func TestRobotFilesSummaryStructure(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-files=test")

	var payload struct {
		Summary struct {
			TotalChanges    int            `json:"total_changes"`
			UniqueFiles     int            `json:"unique_files"`
			ByAgent         map[string]int `json:"by_agent"`
			ByOperation     map[string]int `json:"by_operation"`
			MostActiveAgent string         `json:"most_active_agent,omitempty"`
			Conflicts       []struct {
				Path     string   `json:"path"`
				Agents   []string `json:"agents"`
				Severity string   `json:"severity"`
			} `json:"conflicts,omitempty"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// ByAgent and ByOperation should always be maps (may be empty)
	if payload.Summary.ByAgent == nil {
		t.Errorf("summary.by_agent should be a map, not nil")
	}
	if payload.Summary.ByOperation == nil {
		t.Errorf("summary.by_operation should be a map, not nil")
	}
}

func TestRobotFilesAgentHints(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-files=test")

	var payload struct {
		AgentHints *struct {
			Summary string   `json:"summary"`
			Notes   []string `json:"notes"`
		} `json:"_agent_hints,omitempty"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// AgentHints may be present when file tracking not initialized
	// If present, should have summary field
	if payload.AgentHints != nil {
		if payload.AgentHints.Summary == "" {
			t.Errorf("_agent_hints.summary should not be empty when hints present")
		}
	}
}

// =============================================================================
// --robot-inspect-pane tests (ntm-ux4w: E2E test with state detection)
// =============================================================================

func TestRobotInspectPaneNonExistentSession(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	logger := testutil.NewTestLoggerStdout(t)
	// Non-existent session should return error JSON with exit code 1
	cmd := exec.Command("ntm", "--robot-inspect-pane=nonexistent-session-xyz")
	out, err := cmd.CombinedOutput()
	logger.Log("Output: %s", string(out))

	if err == nil {
		t.Fatalf("expected command to fail for nonexistent session")
	}

	// Find JSON in output (there may be stderr text after)
	jsonStart := strings.Index(string(out), "{")
	jsonEnd := strings.LastIndex(string(out), "}") + 1
	if jsonStart == -1 || jsonEnd <= jsonStart {
		t.Fatalf("no valid JSON found in output")
	}
	jsonBytes := out[jsonStart:jsonEnd]

	var payload struct {
		Success   bool   `json:"success"`
		ErrorCode string `json:"error_code"`
		Error     string `json:"error"`
		Hint      string `json:"hint"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON error response: %v\nOutput: %s", err, string(out))
	}

	if payload.Success {
		t.Errorf("expected success=false for nonexistent session")
	}
	if payload.ErrorCode != "SESSION_NOT_FOUND" {
		t.Errorf("expected error_code='SESSION_NOT_FOUND', got %q", payload.ErrorCode)
	}
	if payload.Error == "" {
		t.Errorf("expected error message to be non-empty")
	}
}

func TestRobotInspectPanePaneNotFound(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	// First we need a real session to test PANE_NOT_FOUND
	// Create a temporary session for this test
	sessionName := fmt.Sprintf("ntm-test-inspect-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	// Try to inspect a pane that doesn't exist (e.g., index 99)
	cmd := exec.Command("ntm", "--robot-inspect-pane="+sessionName, "--index=99")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected command to fail for nonexistent pane index")
	}

	// Find JSON in output (there may be stderr text after)
	jsonStart := strings.Index(string(out), "{")
	jsonEnd := strings.LastIndex(string(out), "}") + 1
	if jsonStart == -1 || jsonEnd <= jsonStart {
		t.Fatalf("no valid JSON found in output")
	}
	jsonBytes := out[jsonStart:jsonEnd]

	var payload struct {
		Success   bool   `json:"success"`
		ErrorCode string `json:"error_code"`
		Error     string `json:"error"`
		Hint      string `json:"hint"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	if payload.Success {
		t.Errorf("expected success=false for nonexistent pane")
	}
	if payload.ErrorCode != "PANE_NOT_FOUND" {
		t.Errorf("expected error_code='PANE_NOT_FOUND', got %q", payload.ErrorCode)
	}
}

func TestRobotInspectPaneSuccessStructure(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	// Create a test session with a single pane
	sessionName := fmt.Sprintf("ntm-test-inspect-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-inspect-pane="+sessionName)

	var payload struct {
		Success   bool   `json:"success"`
		Timestamp string `json:"timestamp"`
		Session   string `json:"session"`
		PaneIndex int    `json:"pane_index"`
		PaneID    string `json:"pane_id"`
		Agent     struct {
			Type            string  `json:"type"`
			Title           string  `json:"title"`
			State           string  `json:"state"`
			StateConfidence float64 `json:"state_confidence"`
			ProcessRunning  bool    `json:"process_running"`
		} `json:"agent"`
		Output struct {
			Lines       int      `json:"lines"`
			Characters  int      `json:"characters"`
			LastLines   []string `json:"last_lines"`
			ErrorsFound []string `json:"errors_found,omitempty"`
		} `json:"output"`
		Context struct {
			RecentFiles []string `json:"recent_files,omitempty"`
			PendingMail int      `json:"pending_mail"`
		} `json:"context"`
		AgentHints *struct {
			Summary string `json:"summary"`
		} `json:"_agent_hints,omitempty"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	if !payload.Success {
		t.Errorf("expected success=true for valid session")
	}
	if payload.Session != sessionName {
		t.Errorf("expected session=%q, got %q", sessionName, payload.Session)
	}
	if payload.PaneID == "" {
		t.Errorf("expected pane_id to be non-empty")
	}
	if payload.Agent.Type == "" {
		t.Errorf("expected agent.type to be set")
	}
	// Output may or may not have lines depending on pane content
	// Just verify the output structure is valid - LastLines can be null if no output
}

func TestRobotInspectPaneInspectLinesFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-test-inspect-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	logger := testutil.NewTestLoggerStdout(t)
	// Test with explicit lines limit
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-inspect-pane="+sessionName, "--inspect-lines=200")

	var payload struct {
		Success bool `json:"success"`
		Output  struct {
			Lines int `json:"lines"`
		} `json:"output"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Errorf("expected success=true")
	}
	// Lines should be within the requested limit (can't verify exact because
	// output depends on pane history)
}

func TestRobotInspectPaneInspectCodeFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-test-inspect-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	logger := testutil.NewTestLoggerStdout(t)
	// Test with --inspect-code flag
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-inspect-pane="+sessionName, "--inspect-code")

	var payload struct {
		Success bool `json:"success"`
		Output  struct {
			CodeBlocks []struct {
				Language  string `json:"language,omitempty"`
				LineStart int    `json:"line_start"`
				LineEnd   int    `json:"line_end"`
			} `json:"code_blocks,omitempty"`
		} `json:"output"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Errorf("expected success=true")
	}
	// Code blocks may or may not be present depending on pane content
	// Just verifying the structure is valid
}

func TestRobotInspectPaneAgentHints(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-test-inspect-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-inspect-pane="+sessionName)

	var payload struct {
		AgentHints *struct {
			Summary          string   `json:"summary"`
			Warnings         []string `json:"warnings,omitempty"`
			SuggestedActions []struct {
				Action   string `json:"action"`
				Target   string `json:"target"`
				Reason   string `json:"reason"`
				Priority int    `json:"priority"`
			} `json:"suggested_actions,omitempty"`
		} `json:"_agent_hints,omitempty"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// AgentHints should be present with at least a summary
	if payload.AgentHints == nil {
		t.Errorf("expected _agent_hints to be present")
	} else if payload.AgentHints.Summary == "" {
		t.Errorf("expected _agent_hints.summary to be non-empty")
	}
}

// =============================================================================
// --robot-metrics tests (ntm-z7ks: E2E test for metrics and replay commands)
// =============================================================================

func TestRobotMetricsWithValidSession(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	// Create a test session
	sessionName := fmt.Sprintf("ntm-test-metrics-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-metrics="+sessionName)

	var payload struct {
		Success    bool   `json:"success"`
		Session    string `json:"session"`
		Period     string `json:"period"`
		TokenUsage struct {
			ByAgent map[string]int64 `json:"by_agent"`
			ByModel map[string]int64 `json:"by_model"`
		} `json:"token_usage"`
		AgentStats   map[string]interface{} `json:"agent_stats"`
		SessionStats struct {
			TotalAgents  int `json:"total_agents"`
			ActiveAgents int `json:"active_agents"`
			FilesChanged int `json:"files_changed"`
		} `json:"session_stats"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	if !payload.Success {
		t.Errorf("expected success=true for valid session")
	}
	if payload.Session != sessionName {
		t.Errorf("expected session=%q, got %q", sessionName, payload.Session)
	}
	if payload.Period == "" {
		t.Errorf("expected period to be set")
	}
}

func TestRobotMetricsPeriodFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-test-metrics-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	testCases := []struct {
		name           string
		period         string
		expectedPeriod string
	}{
		{"default period", "", "24h"},
		{"1h period", "1h", "1h"},
		{"7d period", "7d", "7d"},
		{"all period", "all", "all"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger := testutil.NewTestLoggerStdout(t)
			args := []string{"--robot-metrics=" + sessionName}
			if tc.period != "" {
				args = append(args, "--period="+tc.period)
			}
			out := testutil.AssertCommandSuccess(t, logger, "ntm", args...)

			var payload struct {
				Period string `json:"period"`
			}

			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}

			if payload.Period != tc.expectedPeriod {
				t.Errorf("expected period=%q, got %q", tc.expectedPeriod, payload.Period)
			}
		})
	}
}

func TestRobotMetricsNonExistentSession(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	// Non-existent session should return error
	cmd := exec.Command("ntm", "--robot-metrics=nonexistent-session-xyz")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected command to fail for nonexistent session")
	}

	// Find JSON in output
	jsonStart := strings.Index(string(out), "{")
	jsonEnd := strings.LastIndex(string(out), "}") + 1
	if jsonStart == -1 || jsonEnd <= jsonStart {
		t.Fatalf("no valid JSON found in output: %s", string(out))
	}
	jsonBytes := out[jsonStart:jsonEnd]

	var payload struct {
		Success   bool   `json:"success"`
		ErrorCode string `json:"error_code"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	if payload.Success {
		t.Errorf("expected success=false for nonexistent session")
	}
	if payload.ErrorCode != "SESSION_NOT_FOUND" {
		t.Errorf("expected error_code='SESSION_NOT_FOUND', got %q", payload.ErrorCode)
	}
}

func TestRobotMetricsAgentHints(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-test-metrics-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-metrics="+sessionName)

	var payload struct {
		AgentHints *struct {
			Summary string   `json:"summary"`
			Notes   []string `json:"notes,omitempty"`
		} `json:"_agent_hints,omitempty"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.AgentHints == nil {
		t.Errorf("expected _agent_hints to be present")
	} else if payload.AgentHints.Summary == "" {
		t.Errorf("expected _agent_hints.summary to be non-empty")
	}
}

// =============================================================================
// --robot-replay tests (ntm-z7ks)
// =============================================================================

func TestRobotReplayMissingHistoryID(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-test-replay-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	// Replay without --replay-id should fail or return error
	// (depends on implementation - might return history not found)
	cmd := exec.Command("ntm", "--robot-replay="+sessionName)
	out, err := cmd.CombinedOutput()

	if err == nil {
		// If it succeeds, check if it's an error response
		var payload struct {
			Success   bool   `json:"success"`
			ErrorCode string `json:"error_code,omitempty"`
		}
		jsonStart := strings.Index(string(out), "{")
		if jsonStart != -1 {
			if jsonErr := json.Unmarshal(out[jsonStart:], &payload); jsonErr == nil {
				// Should either fail or return error in JSON
				if payload.Success && payload.ErrorCode == "" {
					// This is unexpected - replay without ID shouldn't fully succeed
					t.Logf("Warning: replay without --replay-id returned success")
				}
			}
		}
		return
	}

	// Find JSON in output
	jsonStart := strings.Index(string(out), "{")
	jsonEnd := strings.LastIndex(string(out), "}") + 1
	if jsonStart == -1 || jsonEnd <= jsonStart {
		t.Fatalf("no valid JSON found in error output: %s", string(out))
	}
	jsonBytes := out[jsonStart:jsonEnd]

	var payload struct {
		Success   bool   `json:"success"`
		ErrorCode string `json:"error_code"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	if payload.Success {
		t.Errorf("expected success=false without --replay-id")
	}
}

func TestRobotReplayNonExistentHistoryID(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-test-replay-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	// Replay with non-existent history ID should fail
	cmd := exec.Command("ntm", "--robot-replay="+sessionName, "--replay-id=nonexistent-id-xyz")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected command to fail for nonexistent history ID")
	}

	// Find JSON in output
	jsonStart := strings.Index(string(out), "{")
	jsonEnd := strings.LastIndex(string(out), "}") + 1
	if jsonStart == -1 || jsonEnd <= jsonStart {
		t.Fatalf("no valid JSON found in output: %s", string(out))
	}
	jsonBytes := out[jsonStart:jsonEnd]

	var payload struct {
		Success   bool   `json:"success"`
		ErrorCode string `json:"error_code"`
		Error     string `json:"error"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	if payload.Success {
		t.Errorf("expected success=false for nonexistent history ID")
	}
	// Error code might be INVALID_FLAG or DEPENDENCY_MISSING depending on history availability
}

func TestRobotReplayDryRunFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-test-replay-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	// Dry run with non-existent ID should still fail
	cmd := exec.Command("ntm", "--robot-replay="+sessionName, "--replay-id=nonexistent-id", "--replay-dry-run")
	out, err := cmd.CombinedOutput()

	if err == nil {
		// If it succeeds, check the response structure
		var payload struct {
			Success  bool `json:"success"`
			DryRun   bool `json:"dry_run,omitempty"`
			Replayed bool `json:"replayed"`
		}
		jsonStart := strings.Index(string(out), "{")
		if jsonStart != -1 {
			if jsonErr := json.Unmarshal(out[jsonStart:], &payload); jsonErr == nil {
				// In dry run, replayed should be false
				if payload.Replayed {
					t.Errorf("expected replayed=false in dry run mode")
				}
			}
		}
		return
	}

	// Find JSON in output - error case
	jsonStart := strings.Index(string(out), "{")
	jsonEnd := strings.LastIndex(string(out), "}") + 1
	if jsonStart == -1 || jsonEnd <= jsonStart {
		t.Fatalf("no valid JSON found in output: %s", string(out))
	}
	jsonBytes := out[jsonStart:jsonEnd]

	var payload struct {
		Success bool `json:"success"`
	}

	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	// Even dry run should fail for nonexistent ID
	if payload.Success {
		t.Errorf("expected success=false for nonexistent history ID even in dry run")
	}
}

// =============================================================================
// --robot-palette tests (ntm-yyvm: E2E test with category and search filters)
// =============================================================================

func TestRobotPaletteBasic(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-palette")

	var payload struct {
		Success   bool   `json:"success"`
		Timestamp string `json:"timestamp"`
		Session   string `json:"session,omitempty"`
		Commands  []struct {
			Key      string `json:"key"`
			Label    string `json:"label"`
			Category string `json:"category"`
			Prompt   string `json:"prompt"`
		} `json:"commands"`
		Favorites []string `json:"favorites"`
		Pinned    []string `json:"pinned"`
		Recent    []struct {
			Key     string `json:"key"`
			UsedAt  string `json:"used_at"`
			Session string `json:"session"`
			Success bool   `json:"success"`
		} `json:"recent"`
		Categories []string `json:"categories"`
		AgentHints *struct {
			Summary string   `json:"summary"`
			Notes   []string `json:"notes,omitempty"`
		} `json:"_agent_hints,omitempty"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput: %s", err, string(out))
	}

	if !payload.Success {
		t.Errorf("expected success=true")
	}
	// Commands should be an array (may be empty if no palette configured)
	if payload.Commands == nil {
		t.Errorf("commands should be an array, not nil")
	}
	// Favorites and Recent should be arrays
	if payload.Favorites == nil {
		t.Errorf("favorites should be an array, not nil")
	}
	if payload.Recent == nil {
		t.Errorf("recent should be an array, not nil")
	}
	if payload.Categories == nil {
		t.Errorf("categories should be an array, not nil")
	}
}

func TestRobotPaletteCategoryFilter(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	// Test with a category filter - may or may not find commands (using canonical --category flag)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-palette", "--category=quick")

	var payload struct {
		Success  bool `json:"success"`
		Commands []struct {
			Category string `json:"category"`
		} `json:"commands"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Errorf("expected success=true")
	}
	// If there are commands, they should all match the category filter
	for _, cmd := range payload.Commands {
		if cmd.Category != "quick" {
			t.Errorf("expected category='quick', got %q", cmd.Category)
		}
	}
}

func TestRobotPaletteSearchFilter(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	// Test with a search filter (using canonical --search flag)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-palette", "--search=test")

	var payload struct {
		Success  bool `json:"success"`
		Commands []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		} `json:"commands"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Errorf("expected success=true")
	}
	// If there are commands, they should match the search term in key or label
	for _, cmd := range payload.Commands {
		keyMatch := strings.Contains(strings.ToLower(cmd.Key), "test")
		labelMatch := strings.Contains(strings.ToLower(cmd.Label), "test")
		if !keyMatch && !labelMatch {
			t.Errorf("command %q/%q doesn't match search term 'test'", cmd.Key, cmd.Label)
		}
	}
}

func TestRobotPaletteSessionFilter(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	sessionName := fmt.Sprintf("ntm-test-palette-%d", time.Now().UnixNano())
	createCmd := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("Could not create test session: %v", err)
	}
	defer func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
	}()

	logger := testutil.NewTestLoggerStdout(t)
	// Use canonical --session flag (--palette-session is deprecated)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-palette", "--session="+sessionName)

	var payload struct {
		Success bool   `json:"success"`
		Session string `json:"session"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Errorf("expected success=true")
	}
	if payload.Session != sessionName {
		t.Errorf("expected session=%q, got %q", sessionName, payload.Session)
	}
}

func TestRobotPaletteAgentHints(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-palette")

	var payload struct {
		AgentHints *struct {
			Summary string   `json:"summary"`
			Notes   []string `json:"notes,omitempty"`
		} `json:"_agent_hints,omitempty"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.AgentHints == nil {
		t.Errorf("expected _agent_hints to be present")
	} else if payload.AgentHints.Summary == "" {
		t.Errorf("expected _agent_hints.summary to be non-empty")
	}
}

func TestRobotPaletteCombinedFilters(t *testing.T) {
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	// Test with both category and search filters (using canonical flags)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-palette", "--category=quick", "--search=fix")

	var payload struct {
		Success  bool `json:"success"`
		Commands []struct {
			Key      string `json:"key"`
			Label    string `json:"label"`
			Category string `json:"category"`
		} `json:"commands"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if !payload.Success {
		t.Errorf("expected success=true")
	}
	// If there are commands, they should match both filters
	for _, cmd := range payload.Commands {
		if cmd.Category != "quick" {
			t.Errorf("expected category='quick', got %q", cmd.Category)
		}
		keyMatch := strings.Contains(strings.ToLower(cmd.Key), "fix")
		labelMatch := strings.Contains(strings.ToLower(cmd.Label), "fix")
		if !keyMatch && !labelMatch {
			t.Errorf("command %q/%q doesn't match search term 'fix'", cmd.Key, cmd.Label)
		}
	}
}
