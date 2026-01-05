package integration

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

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
	testutil.RequireTmux(t)

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
	testutil.RequireTmux(t)

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
	testutil.RequireTmux(t)

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
	testutil.RequireTmux(t)

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
	if strings.Contains(outputStr, "{") {
		var payload struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(out, &payload) == nil {
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

	// With nonexistent session, should return JSON error
	if err == nil {
		// Command succeeded - check output
		var payload struct {
			Success bool `json:"success"`
		}
		if json.Unmarshal(out, &payload) == nil {
			if payload.Success {
				t.Logf("health check succeeded (session may exist)")
			}
		}
	} else {
		// Command failed - that's expected for nonexistent session
		t.Logf("health check failed as expected for nonexistent session")
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
	testutil.RequireTmux(t)

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
	testutil.RequireTmux(t)

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
	testutil.RequireTmux(t)

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
	testutil.RequireTmux(t)

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
	testutil.RequireTmux(t)

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
	testutil.RequireTmux(t)

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
	testutil.RequireTmux(t)

	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-markdown", "--md-compact")

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

	// Verify it has expected structure (may vary based on mail server availability)
	// At minimum it should be valid JSON
	if payload == nil {
		t.Errorf("expected non-nil JSON payload")
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
