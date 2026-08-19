package robot

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewRobotResponse(t *testing.T) {
	t.Run("success response", func(t *testing.T) {
		resp := NewRobotResponse(true)
		if !resp.Success {
			t.Error("expected Success to be true")
		}
		if resp.Timestamp == "" {
			t.Error("expected Timestamp to be set")
		}
		// Verify timestamp is valid RFC3339
		_, err := time.Parse(time.RFC3339, resp.Timestamp)
		if err != nil {
			t.Errorf("Timestamp is not valid RFC3339: %v", err)
		}
	})

	t.Run("failure response", func(t *testing.T) {
		resp := NewRobotResponse(false)
		if resp.Success {
			t.Error("expected Success to be false")
		}
	})
}

func TestNewRobotResponseWithMeta(t *testing.T) {
	t.Run("with valid meta", func(t *testing.T) {
		meta := &ResponseMeta{
			Command:    "test-command",
			DurationMs: 150,
		}
		resp := NewRobotResponseWithMeta(true, meta)
		if !resp.Success {
			t.Error("expected Success to be true")
		}
		if resp.Meta == nil {
			t.Fatal("expected Meta to be set")
		}
		if resp.Meta.Command != "test-command" {
			t.Errorf("expected Command 'test-command', got %q", resp.Meta.Command)
		}
		if resp.Meta.DurationMs != 150 {
			t.Errorf("expected DurationMs 150, got %d", resp.Meta.DurationMs)
		}
	})

	t.Run("with nil meta", func(t *testing.T) {
		resp := NewRobotResponseWithMeta(false, nil)
		if resp.Success {
			t.Error("expected Success to be false")
		}
		if resp.Meta != nil {
			t.Error("expected Meta to be nil")
		}
	})

	t.Run("meta JSON serialization", func(t *testing.T) {
		meta := &ResponseMeta{
			Command:    "robot-status",
			DurationMs: 250,
		}
		resp := NewRobotResponseWithMeta(true, meta)

		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		metaData, ok := parsed["_meta"].(map[string]interface{})
		if !ok {
			t.Fatal("expected _meta in JSON output")
		}
		if metaData["command"] != "robot-status" {
			t.Errorf("unexpected command: %v", metaData["command"])
		}
		if metaData["duration_ms"] != float64(250) {
			t.Errorf("unexpected duration: %v", metaData["duration_ms"])
		}
	})
}

func TestNewErrorResponse(t *testing.T) {
	err := errors.New("session not found")
	resp := NewErrorResponse(err, ErrCodeSessionNotFound, "Use 'ntm list' to see sessions")

	if resp.Success {
		t.Error("expected Success to be false")
	}
	if resp.Error != "session not found" {
		t.Errorf("expected Error 'session not found', got %q", resp.Error)
	}
	if resp.ErrorCode != ErrCodeSessionNotFound {
		t.Errorf("expected ErrorCode %q, got %q", ErrCodeSessionNotFound, resp.ErrorCode)
	}
	if resp.Hint != "Use 'ntm list' to see sessions" {
		t.Errorf("unexpected Hint: %q", resp.Hint)
	}
}

func TestEncodeTerminalRobotOutputFailureForcesJSON(t *testing.T) {
	originalFormat := GetOutputFormat()
	SetOutputFormat(FormatTOON)
	t.Cleanup(func() { SetOutputFormat(originalFormat) })

	output := &SendOutput{
		RobotResponse: NewErrorResponse(errors.New("no target panes"), ErrCodePaneNotFound, "check filters"),
		Targets:       []string{},
		Successful:    []string{},
		Failed:        []SendError{},
		Warnings:      []string{},
	}
	stdout, err := captureStdout(t, func() error {
		return encodeTerminalRobotOutput(output, output.RobotResponse, "send failed")
	})
	var exitErr *ProcessExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !exitErr.JSONWritten() {
		t.Fatalf("terminal error = %T %v, want written exit-1 ProcessExitError", err, err)
	}

	var response SendOutput
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("terminal failure is not JSON: %v\noutput=%q", err, stdout)
	}
	if response.Success || response.ErrorCode != ErrCodePaneNotFound || response.OutputFormat != string(FormatJSON) {
		t.Fatalf("terminal response = %+v, want PANE_NOT_FOUND JSON failure", response.RobotResponse)
	}
}

func TestPrintAttentionResponseFailureExitContract(t *testing.T) {
	originalFormat := GetOutputFormat()
	SetOutputFormat(FormatTOON)
	t.Cleanup(func() { SetOutputFormat(originalFormat) })

	tests := []struct {
		name     string
		code     string
		wantExit int
	}{
		{name: "normal command failure", code: ErrCodeInternalError, wantExit: 1},
		{name: "unavailable functionality", code: ErrCodeNotImplemented, wantExit: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := AttentionResponse{
				RobotResponse: NewErrorResponse(errors.New("attention failed"), tt.code, "retry"),
				WakeReason:    "error",
				Digest:        &AttentionDigest{},
			}
			var exitCode int
			stdout, err := captureStdout(t, func() error {
				exitCode = printAttentionResponse(response, 2)
				return nil
			})
			if err != nil {
				t.Fatalf("capture attention output: %v", err)
			}
			if exitCode != tt.wantExit {
				t.Fatalf("exit code = %d, want %d", exitCode, tt.wantExit)
			}
			var output AttentionResponse
			if err := json.Unmarshal([]byte(stdout), &output); err != nil {
				t.Fatalf("attention failure is not JSON: %v\noutput=%q", err, stdout)
			}
			if output.Success || output.ErrorCode != tt.code || output.OutputFormat != string(FormatJSON) {
				t.Fatalf("attention response = %+v", output.RobotResponse)
			}
		})
	}
}

func TestLegacyRobotOutputRejectsNonzeroSuccessEnvelope(t *testing.T) {
	originalFormat := GetOutputFormat()
	SetOutputFormat(FormatTOON)
	t.Cleanup(func() { SetOutputFormat(originalFormat) })

	output := &AttentionResponse{
		RobotResponse: NewRobotResponse(true),
		WakeReason:    "timeout",
		Digest:        &AttentionDigest{},
	}
	var exitCode int
	stdout, err := captureStdout(t, func() error {
		exitCode = printLegacyRobotOutput(output, output.RobotResponse, 1, "robot test failed")
		return nil
	})
	if err != nil {
		t.Fatalf("capture legacy robot output: %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("legacy output exit = %d, want 1", exitCode)
	}
	var response AttentionResponse
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("legacy contract failure is not JSON: %v\noutput=%q", err, stdout)
	}
	if response.Success || response.ErrorCode != ErrCodeInternalError || response.OutputFormat != string(FormatJSON) {
		t.Fatalf("legacy response = %+v, want INTERNAL_ERROR JSON failure", response.RobotResponse)
	}
}

func TestRobotPrinterFailureMatrixForcesCanonicalJSON(t *testing.T) {
	originalFormat := GetOutputFormat()
	SetOutputFormat(FormatTOON)
	t.Cleanup(func() { SetOutputFormat(originalFormat) })

	missingSession := "ntm-terminal-contract-session-that-does-not-exist"
	tests := []struct {
		name      string
		print     func() error
		errorCode string
	}{
		{name: "docs", print: func() error { return PrintDocs("not-a-topic") }, errorCode: ErrCodeInvalidFlag},
		{name: "schema", print: func() error { return PrintSchema("not-a-schema") }, errorCode: ErrCodeInvalidFlag},
		{name: "environment", print: func() error { return PrintEnv(missingSession) }, errorCode: ErrCodeSessionNotFound},
		{name: "errors", print: func() error {
			return PrintErrors(ErrorsOptions{Session: missingSession})
		}, errorCode: ErrCodeSessionNotFound},
		{name: "probe flags", print: func() error {
			return PrintProbeFlagError(errors.New("invalid probe flags"))
		}, errorCode: ErrCodeInvalidFlag},
		{name: "causality", print: func() error {
			return printCausality(CausalityOptions{}, causalityLoaders{})
		}, errorCode: ErrCodeInternalError},
		{name: "monitor", print: func() error {
			return PrintMonitor(MonitorConfig{Session: missingSession})
		}, errorCode: ErrCodeSessionNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, err := captureStdout(t, tt.print)
			var exitErr *ProcessExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !exitErr.JSONWritten() {
				t.Fatalf("printer error = %T %v, want written exit-1 ProcessExitError", err, err)
			}
			var response RobotResponse
			if err := json.Unmarshal([]byte(stdout), &response); err != nil {
				t.Fatalf("failure is not JSON: %v\noutput=%q", err, stdout)
			}
			if response.Success || response.ErrorCode != tt.errorCode || response.OutputFormat != string(FormatJSON) {
				t.Fatalf("response = %+v, want %s JSON failure", response, tt.errorCode)
			}
			if strings.TrimSpace(response.Error) == "" {
				t.Fatal("failure response is missing required error message")
			}
			if response.Timestamp == "" {
				t.Fatal("failure response is missing timestamp")
			}
		})
	}
}

func TestRobotResponseJSON(t *testing.T) {
	t.Run("success response serialization", func(t *testing.T) {
		resp := NewRobotResponse(true)
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if parsed["success"] != true {
			t.Error("expected success to be true in JSON")
		}
		if _, ok := parsed["timestamp"]; !ok {
			t.Error("expected timestamp in JSON")
		}
		// Error fields should be omitted
		if _, ok := parsed["error"]; ok {
			t.Error("error should be omitted when empty")
		}
		if _, ok := parsed["error_code"]; ok {
			t.Error("error_code should be omitted when empty")
		}
	})

	t.Run("error response serialization", func(t *testing.T) {
		resp := NewErrorResponse(
			errors.New("test error"),
			ErrCodeInternalError,
			"Try again",
		)
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if parsed["success"] != false {
			t.Error("expected success to be false in JSON")
		}
		if parsed["error"] != "test error" {
			t.Errorf("expected error 'test error', got %v", parsed["error"])
		}
		if parsed["error_code"] != ErrCodeInternalError {
			t.Errorf("expected error_code %q, got %v", ErrCodeInternalError, parsed["error_code"])
		}
		if parsed["hint"] != "Try again" {
			t.Errorf("expected hint 'Try again', got %v", parsed["hint"])
		}
	})
}

func TestAgentHints(t *testing.T) {
	hints := AgentHints{
		Summary: "2 sessions, 5 agents",
		SuggestedActions: []RobotAction{
			{Action: "send_prompt", Target: "idle agents", Reason: "2 available"},
			{Action: "wait", Reason: "3 agents busy"},
		},
		Warnings: []string{"Agent in pane 2 at 90% context"},
		Notes:    []string{"Consider spawning more agents"},
	}

	data, err := json.Marshal(hints)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed AgentHints
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if parsed.Summary != hints.Summary {
		t.Errorf("Summary mismatch: got %q", parsed.Summary)
	}
	if len(parsed.SuggestedActions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(parsed.SuggestedActions))
	}
	if parsed.SuggestedActions[0].Action != "send_prompt" {
		t.Errorf("first action should be send_prompt")
	}
	if len(parsed.Warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(parsed.Warnings))
	}
}

func TestErrorCodes(t *testing.T) {
	// Verify error codes are defined as expected strings
	codes := []string{
		ErrCodeSessionNotFound,
		ErrCodePaneNotFound,
		ErrCodeInvalidFlag,
		ErrCodeTimeout,
		ErrCodeNotImplemented,
		ErrCodeDependencyMissing,
		ErrCodeInternalError,
		ErrCodePermissionDenied,
		ErrCodeResourceBusy,
	}

	for _, code := range codes {
		if code == "" {
			t.Errorf("error code should not be empty")
		}
		// Codes should be SCREAMING_SNAKE_CASE
		for _, c := range code {
			if c >= 'a' && c <= 'z' {
				t.Errorf("error code %q should be uppercase", code)
				break
			}
		}
	}
}

func TestRobotError(t *testing.T) {
	testErr := errors.New("test error message")
	stdout, returnedErr := captureStdout(t, func() error {
		return RobotError(testErr, ErrCodeSessionNotFound, "test hint")
	})
	if !errors.Is(returnedErr, testErr) {
		t.Fatalf("RobotError should unwrap to the original error, got %v", returnedErr)
	}
	var exitErr *ProcessExitError
	if !errors.As(returnedErr, &exitErr) || exitErr.ExitCode() != 1 || !exitErr.JSONWritten() {
		t.Fatalf("RobotError result = %T exit=%d json_written=%v", returnedErr, exitErr.ExitCode(), exitErr.JSONWritten())
	}
	var payload RobotResponse
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode RobotError payload: %v\n%s", err, stdout)
	}
	if payload.Success || payload.Error != testErr.Error() || payload.ErrorCode != ErrCodeSessionNotFound || payload.Hint != "test hint" {
		t.Fatalf("RobotError payload = %+v", payload)
	}
}

func TestNotImplementedResponse(t *testing.T) {
	t.Run("response structure", func(t *testing.T) {
		resp := NotImplementedResponse{
			RobotResponse: RobotResponse{
				Success:   false,
				Timestamp: "2025-12-15T10:30:00Z",
				Error:     "Feature not available",
				ErrorCode: ErrCodeNotImplemented,
				Hint:      "Try later",
			},
			Feature:        "robot-assign",
			PlannedVersion: "v1.3",
		}

		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		// Verify required fields
		if parsed["success"] != false {
			t.Error("expected success to be false")
		}
		if parsed["error_code"] != ErrCodeNotImplemented {
			t.Errorf("expected error_code %q, got %v", ErrCodeNotImplemented, parsed["error_code"])
		}
		if parsed["feature"] != "robot-assign" {
			t.Errorf("expected feature 'robot-assign', got %v", parsed["feature"])
		}
		if parsed["planned_version"] != "v1.3" {
			t.Errorf("expected planned_version 'v1.3', got %v", parsed["planned_version"])
		}
	})

	t.Run("omits empty planned_version", func(t *testing.T) {
		resp := NotImplementedResponse{
			RobotResponse: NewErrorResponse(
				errors.New("not available"),
				ErrCodeNotImplemented,
				"",
			),
			Feature: "some-feature",
			// PlannedVersion intentionally empty
		}

		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if _, ok := parsed["planned_version"]; ok {
			t.Error("planned_version should be omitted when empty")
		}
	})
}

// =============================================================================
// Timestamp Helper Tests
// =============================================================================

func TestFormatTimestamp(t *testing.T) {
	// Test with a known time
	knownTime := time.Date(2025, 12, 15, 10, 30, 0, 0, time.UTC)
	result := FormatTimestamp(knownTime)
	expected := "2025-12-15T10:30:00Z"
	if result != expected {
		t.Errorf("FormatTimestamp() = %q, want %q", result, expected)
	}

	// Verify the result is valid RFC3339
	_, err := time.Parse(time.RFC3339, result)
	if err != nil {
		t.Errorf("Result is not valid RFC3339: %v", err)
	}
}

func TestFormatTimestampConvertsToUTC(t *testing.T) {
	// Test that non-UTC times are converted to UTC
	loc, _ := time.LoadLocation("America/New_York")
	localTime := time.Date(2025, 12, 15, 5, 30, 0, 0, loc) // 5:30 AM EST = 10:30 AM UTC
	result := FormatTimestamp(localTime)
	expected := "2025-12-15T10:30:00Z"
	if result != expected {
		t.Errorf("FormatTimestamp() = %q, want %q (should convert to UTC)", result, expected)
	}
}

func TestFormatTimestampPtr(t *testing.T) {
	t.Run("nil time", func(t *testing.T) {
		result := FormatTimestampPtr(nil)
		if result != "" {
			t.Errorf("FormatTimestampPtr(nil) = %q, want empty string", result)
		}
	})

	t.Run("valid time", func(t *testing.T) {
		knownTime := time.Date(2025, 12, 15, 10, 30, 0, 0, time.UTC)
		result := FormatTimestampPtr(&knownTime)
		expected := "2025-12-15T10:30:00Z"
		if result != expected {
			t.Errorf("FormatTimestampPtr() = %q, want %q", result, expected)
		}
	})
}

func TestTailAgentHints(t *testing.T) {
	t.Run("all idle agents", func(t *testing.T) {
		panes := map[string]PaneOutput{
			"0": {Type: "claude", State: "idle"},
			"1": {Type: "codex", State: "idle"},
		}
		hints := generateTailHints(panes)
		if hints == nil {
			t.Fatal("expected hints, got nil")
		}
		if len(hints.IdleAgents) != 2 {
			t.Errorf("expected 2 idle agents, got %d", len(hints.IdleAgents))
		}
		if len(hints.ActiveAgents) != 0 {
			t.Errorf("expected 0 active agents, got %d", len(hints.ActiveAgents))
		}
		// Should have suggestion about all idle
		found := false
		for _, s := range hints.Suggestions {
			if s == "All 2 agents idle - ready for new prompts" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'all idle' suggestion, got %v", hints.Suggestions)
		}
	})

	t.Run("mixed idle and active", func(t *testing.T) {
		panes := map[string]PaneOutput{
			"0": {Type: "claude", State: "idle"},
			"1": {Type: "codex", State: "active"},
			"2": {Type: "gemini", State: "active"},
		}
		hints := generateTailHints(panes)
		if hints == nil {
			t.Fatal("expected hints, got nil")
		}
		if len(hints.IdleAgents) != 1 {
			t.Errorf("expected 1 idle agent, got %d", len(hints.IdleAgents))
		}
		if len(hints.ActiveAgents) != 2 {
			t.Errorf("expected 2 active agents, got %d", len(hints.ActiveAgents))
		}
	})

	t.Run("error state includes suggestion", func(t *testing.T) {
		panes := map[string]PaneOutput{
			"0": {Type: "claude", State: "error"},
		}
		hints := generateTailHints(panes)
		if hints == nil {
			t.Fatal("expected hints, got nil")
		}
		// Should have error suggestion
		found := false
		for _, s := range hints.Suggestions {
			if s == "Pane 0 has an error - check output" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected error pane suggestion, got %v", hints.Suggestions)
		}
	})

	t.Run("unknown states return nil hints", func(t *testing.T) {
		panes := map[string]PaneOutput{
			"0": {Type: "shell", State: "unknown"},
		}
		hints := generateTailHints(panes)
		if hints != nil {
			t.Errorf("expected nil hints for unknown state, got %v", hints)
		}
	})
}

// =============================================================================
// Required Field Tests - Verify critical fields are always present
// =============================================================================
// These tests ensure that critical arrays are never absent from JSON output,
// allowing safe iteration without null checks.

func TestCriticalFieldsAlwaysPresent(t *testing.T) {
	t.Run("InterruptOutput has required arrays", func(t *testing.T) {
		output := InterruptOutput{
			Session:        "test",
			Interrupted:    []string{},
			PreviousStates: map[string]PaneState{},
			ReadyForInput:  []string{},
			Failed:         []InterruptError{},
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		// These fields must always be present (not omitted even when empty)
		requiredArrays := []string{"interrupted", "previous_states", "ready_for_input", "failed"}
		for _, field := range requiredArrays {
			if _, ok := parsed[field]; !ok {
				t.Errorf("required field %q is missing from JSON output", field)
			}
		}
	})

	t.Run("SendOutput has required arrays", func(t *testing.T) {
		output := SendOutput{
			RobotResponse:  NewRobotResponse(true),
			Session:        "test",
			Blocked:        false,
			Redaction:      RedactionSummary{Mode: "off", Findings: 0, Action: "off"},
			Warnings:       []string{},
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{},
			MessagePreview: "",
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		requiredArrays := []string{"warnings", "targets", "successful", "failed"}
		for _, field := range requiredArrays {
			if _, ok := parsed[field]; !ok {
				t.Errorf("required field %q is missing from JSON output", field)
			}
		}
	})

	t.Run("SpawnOutput has required arrays", func(t *testing.T) {
		output := SpawnOutput{
			Session:   "test",
			CreatedAt: FormatTimestamp(time.Now()),
			Agents:    []SpawnedAgent{},
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if _, ok := parsed["agents"]; !ok {
			t.Error("required field 'agents' is missing from JSON output")
		}
	})

	t.Run("TailOutput has required fields", func(t *testing.T) {
		output := TailOutput{
			RobotResponse: NewRobotResponse(true),
			Session:       "test",
			Panes:         map[string]PaneOutput{},
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if _, ok := parsed["panes"]; !ok {
			t.Error("required field 'panes' is missing from JSON output")
		}
	})
}

func TestOptionalFieldsOmitted(t *testing.T) {
	t.Run("error fields absent on success", func(t *testing.T) {
		resp := NewRobotResponse(true)

		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		optionalFields := []string{"error", "error_code", "hint"}
		for _, field := range optionalFields {
			if _, ok := parsed[field]; ok {
				t.Errorf("optional field %q should be absent on success", field)
			}
		}
	})

	t.Run("dry_run absent when not in dry-run mode", func(t *testing.T) {
		output := SendOutput{
			RobotResponse:  NewRobotResponse(true),
			Session:        "test",
			Blocked:        false,
			Redaction:      RedactionSummary{Mode: "off", Findings: 0, Action: "off"},
			Warnings:       []string{},
			Targets:        []string{"1"},
			Successful:     []string{"1"},
			Failed:         []SendError{},
			MessagePreview: "test",
			DryRun:         false, // Not dry-run
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if _, ok := parsed["dry_run"]; ok {
			t.Error("dry_run should be absent when false")
		}
		if _, ok := parsed["would_send_to"]; ok {
			t.Error("would_send_to should be absent in normal mode")
		}
	})

	t.Run("dry_run present when true", func(t *testing.T) {
		output := SendOutput{
			RobotResponse:  NewRobotResponse(true),
			Session:        "test",
			Blocked:        false,
			Redaction:      RedactionSummary{Mode: "off", Findings: 0, Action: "off"},
			Warnings:       []string{},
			Targets:        []string{"1"},
			Successful:     []string{},
			Failed:         []SendError{},
			MessagePreview: "test",
			DryRun:         true,
			WouldSendTo:    []string{"1"},
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if _, ok := parsed["dry_run"]; !ok {
			t.Error("dry_run should be present when true")
		}
		if _, ok := parsed["would_send_to"]; !ok {
			t.Error("would_send_to should be present in dry-run mode")
		}
	})
}

// =============================================================================
// Structured Error Tests (bd-3vc3s)
// =============================================================================
// Tests for StructuredError, ErrorDetails, and related helper functions.

func TestNewStructuredError(t *testing.T) {
	err := NewStructuredError(ErrCodeSoftExitFailed, "Agent did not respond to Ctrl-C")

	if err.Code != ErrCodeSoftExitFailed {
		t.Errorf("expected code %q, got %q", ErrCodeSoftExitFailed, err.Code)
	}
	if err.Message != "Agent did not respond to Ctrl-C" {
		t.Errorf("expected message 'Agent did not respond to Ctrl-C', got %q", err.Message)
	}
	// Optional fields should be empty/nil
	if err.Phase != "" {
		t.Errorf("expected empty phase, got %q", err.Phase)
	}
	if err.Pane != 0 {
		t.Errorf("expected pane 0, got %d", err.Pane)
	}
	if err.Details != nil {
		t.Error("expected nil details")
	}
	if err.RecoveryHint != "" {
		t.Errorf("expected empty recovery hint, got %q", err.RecoveryHint)
	}
}

func TestStructuredErrorBuilderPattern(t *testing.T) {
	details := NewErrorDetails().
		WithChildPID(12345).
		WithAgentType("cc").
		WithAttemptedActions("ctrl-c-1", "ctrl-c-2", "wait-3s")

	err := NewStructuredError(ErrCodeSoftExitFailed, "Agent did not respond to Ctrl-C").
		WithPhase("soft_exit").
		WithPane(4).
		WithDetails(details).
		WithRecoveryHint("Try --robot-restart-pane with automatic kill -9 fallback")

	if err.Phase != "soft_exit" {
		t.Errorf("expected phase 'soft_exit', got %q", err.Phase)
	}
	if err.Pane != 4 {
		t.Errorf("expected pane 4, got %d", err.Pane)
	}
	if err.RecoveryHint != "Try --robot-restart-pane with automatic kill -9 fallback" {
		t.Errorf("unexpected recovery hint: %q", err.RecoveryHint)
	}
	if err.Details == nil {
		t.Fatal("expected details to be set")
	}
	if err.Details.ChildPID != 12345 {
		t.Errorf("expected child PID 12345, got %d", err.Details.ChildPID)
	}
	if err.Details.AgentType != "cc" {
		t.Errorf("expected agent type 'cc', got %q", err.Details.AgentType)
	}
	if len(err.Details.AttemptedActions) != 3 {
		t.Errorf("expected 3 attempted actions, got %d", len(err.Details.AttemptedActions))
	}
}

func TestStructuredErrorInterface(t *testing.T) {
	t.Run("without phase", func(t *testing.T) {
		err := NewStructuredError(ErrCodeTimeout, "Operation timed out")
		msg := err.Error()
		if msg != "Operation timed out" {
			t.Errorf("expected 'Operation timed out', got %q", msg)
		}
	})

	t.Run("with phase", func(t *testing.T) {
		err := NewStructuredError(ErrCodeSoftExitFailed, "Ctrl-C ignored").
			WithPhase("soft_exit")
		msg := err.Error()
		if msg != "soft_exit: Ctrl-C ignored" {
			t.Errorf("expected 'soft_exit: Ctrl-C ignored', got %q", msg)
		}
	})
}

func TestErrorDetails(t *testing.T) {
	details := NewErrorDetails()
	if details == nil {
		t.Fatal("expected non-nil ErrorDetails")
	}

	// Test all builder methods
	details.
		WithChildPID(54321).
		WithProcessState("running").
		WithLastOutput("some output here", 20).
		WithAttemptedActions("action1", "action2").
		WithAgentType("gmi").
		WithExitMethod("escape_then_exit").
		WithDuration(3500).
		SetExtra("custom_field", "custom_value")

	if details.ChildPID != 54321 {
		t.Errorf("expected child PID 54321, got %d", details.ChildPID)
	}
	if details.ProcessState != "running" {
		t.Errorf("expected process state 'running', got %q", details.ProcessState)
	}
	if details.LastOutput != "some output here" {
		t.Errorf("expected last output 'some output here', got %q", details.LastOutput)
	}
	if len(details.AttemptedActions) != 2 {
		t.Errorf("expected 2 attempted actions, got %d", len(details.AttemptedActions))
	}
	if details.AgentType != "gmi" {
		t.Errorf("expected agent type 'gmi', got %q", details.AgentType)
	}
	if details.ExitMethod != "escape_then_exit" {
		t.Errorf("expected exit method 'escape_then_exit', got %q", details.ExitMethod)
	}
	if details.DurationMs != 3500 {
		t.Errorf("expected duration 3500, got %d", details.DurationMs)
	}
	if details.Extra["custom_field"] != "custom_value" {
		t.Errorf("expected custom field value 'custom_value', got %v", details.Extra["custom_field"])
	}
}

func TestErrorDetailsLastOutputTruncation(t *testing.T) {
	details := NewErrorDetails()
	longOutput := "This is a very long output string that exceeds the maximum length"

	details.WithLastOutput(longOutput, 20)

	if len(details.LastOutput) > 35 { // 20 + len("... [truncated]")
		t.Errorf("expected truncated output, got length %d", len(details.LastOutput))
	}
	if details.LastOutput != "This is a very long ... [truncated]" {
		t.Errorf("unexpected truncated output: %q", details.LastOutput)
	}

	// Short output should not be truncated
	details2 := NewErrorDetails()
	details2.WithLastOutput("short", 20)
	if details2.LastOutput != "short" {
		t.Errorf("short output should not be modified, got %q", details2.LastOutput)
	}
}

func TestStructuredErrorJSON(t *testing.T) {
	details := NewErrorDetails().
		WithChildPID(12345).
		WithProcessState("zombie").
		WithAttemptedActions("ctrl-c", "kill-9").
		WithAgentType("cc")

	structErr := NewStructuredError(ErrCodeSoftExitFailed, "Agent stuck").
		WithPhase("soft_exit").
		WithPane(3).
		WithDetails(details).
		WithRecoveryHint("Try manual intervention")

	data, err := json.Marshal(structErr)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Verify required fields
	if parsed["code"] != ErrCodeSoftExitFailed {
		t.Errorf("expected code %q, got %v", ErrCodeSoftExitFailed, parsed["code"])
	}
	if parsed["message"] != "Agent stuck" {
		t.Errorf("expected message 'Agent stuck', got %v", parsed["message"])
	}
	if parsed["phase"] != "soft_exit" {
		t.Errorf("expected phase 'soft_exit', got %v", parsed["phase"])
	}
	if parsed["pane"] != float64(3) {
		t.Errorf("expected pane 3, got %v", parsed["pane"])
	}
	if parsed["recovery_hint"] != "Try manual intervention" {
		t.Errorf("expected recovery_hint, got %v", parsed["recovery_hint"])
	}

	// Verify details
	detailsData, ok := parsed["details"].(map[string]interface{})
	if !ok {
		t.Fatal("expected details to be an object")
	}
	if detailsData["child_pid"] != float64(12345) {
		t.Errorf("expected child_pid 12345, got %v", detailsData["child_pid"])
	}
	if detailsData["process_state"] != "zombie" {
		t.Errorf("expected process_state 'zombie', got %v", detailsData["process_state"])
	}
	if detailsData["agent_type"] != "cc" {
		t.Errorf("expected agent_type 'cc', got %v", detailsData["agent_type"])
	}
}

func TestStructuredErrorJSONOmitsEmptyFields(t *testing.T) {
	// Minimal structured error - optional fields should be omitted
	structErr := NewStructuredError(ErrCodeTimeout, "Timed out")

	data, err := json.Marshal(structErr)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Required fields must be present
	if _, ok := parsed["code"]; !ok {
		t.Error("code should always be present")
	}
	if _, ok := parsed["message"]; !ok {
		t.Error("message should always be present")
	}

	// Optional fields should be omitted
	optionalFields := []string{"phase", "details", "recovery_hint"}
	for _, field := range optionalFields {
		if _, ok := parsed[field]; ok {
			t.Errorf("optional field %q should be omitted when empty", field)
		}
	}

	// Pane=0 should be omitted (omitempty behavior for int)
	if _, ok := parsed["pane"]; ok {
		t.Error("pane should be omitted when 0")
	}
}

func TestRestartErrorCodes(t *testing.T) {
	// Verify the new restart-related error codes are defined correctly
	codes := map[string]string{
		ErrCodeSoftExitFailed:   "SOFT_EXIT_FAILED",
		ErrCodeHardKillFailed:   "HARD_KILL_FAILED",
		ErrCodeShellNotReturned: "SHELL_NOT_RETURNED",
		ErrCodeCCLaunchFailed:   "CC_LAUNCH_FAILED",
		ErrCodeCCInitTimeout:    "CC_INIT_TIMEOUT",
		ErrCodeBeadNotFound:     "BEAD_NOT_FOUND",
		ErrCodePromptSendFailed: "PROMPT_SEND_FAILED",
	}

	for constant, expected := range codes {
		if constant != expected {
			t.Errorf("expected %q, got %q", expected, constant)
		}
		// Codes should be SCREAMING_SNAKE_CASE
		for _, c := range constant {
			if c >= 'a' && c <= 'z' {
				t.Errorf("error code %q should be uppercase", constant)
				break
			}
		}
	}
}

func TestRobotPrintersDoNotReturnRawEnvelopeEncoder(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate robot test source")
	}
	packageDir := filepath.Dir(sourceFile)
	entries, err := os.ReadDir(packageDir)
	if err != nil {
		t.Fatalf("read robot package: %v", err)
	}

	// PrintSessions intentionally emits a bare array and has no RobotResponse
	// envelope from which a terminal exit can be derived.
	allowed := map[string]bool{"PrintSessions": true}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(packageDir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range parsed.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Print") || allowed[fn.Name.Name] {
				continue
			}
			returnsInt := fn.Type.Results != nil && len(fn.Type.Results.List) == 1
			if returnsInt {
				resultType, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
				returnsInt = ok && resultType.Name == "int"
			}
			usesLegacyGuard := false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if returnsInt {
					if call, ok := node.(*ast.CallExpr); ok {
						if ident, ok := call.Fun.(*ast.Ident); ok {
							if ident.Name == "printLegacyRobotOutput" || ident.Name == "printAttentionResponse" {
								usesLegacyGuard = true
							}
							// Delegating to another exported printer also satisfies the
							// guard: that callee is audited by this same test, so the
							// envelope still reaches printLegacyRobotOutput exactly once.
							// Without this, the ctx-aware split (PrintWait ->
							// PrintWaitContext) reads as a bypass when it is a forward.
							if ident.Name != fn.Name.Name && strings.HasPrefix(ident.Name, "Print") {
								usesLegacyGuard = true
							}
							if ident.Name == "encodeJSON" || ident.Name == "outputJSON" || ident.Name == "encodeRobotFailureJSON" {
								position := fset.Position(call.Pos())
								t.Errorf("integer printer %s calls %s directly at %s; use printLegacyRobotOutput", fn.Name.Name, ident.Name, position)
							}
						}
					}
				}
				switch stmt := node.(type) {
				case *ast.ReturnStmt:
					if len(stmt.Results) != 1 {
						return true
					}
					call, ok := stmt.Results[0].(*ast.CallExpr)
					if !ok {
						return true
					}
					ident, ok := call.Fun.(*ast.Ident)
					if ok && (ident.Name == "encodeJSON" || ident.Name == "outputJSON") {
						position := fset.Position(stmt.Pos())
						t.Errorf("%s returns %s directly at %s; use encodeTerminalRobotOutput for RobotResponse envelopes", fn.Name.Name, ident.Name, position)
					}
				case *ast.ExprStmt:
					call, ok := stmt.X.(*ast.CallExpr)
					if !ok {
						return true
					}
					ident, ok := call.Fun.(*ast.Ident)
					if ok && (ident.Name == "encodeJSON" || ident.Name == "outputJSON") {
						position := fset.Position(stmt.Pos())
						t.Errorf("%s ignores %s at %s; terminal printers must propagate encoding and exit failures", fn.Name.Name, ident.Name, position)
					}
				}
				return true
			})
			if returnsInt && !usesLegacyGuard {
				t.Errorf("integer printer %s does not route through the legacy terminal guard", fn.Name.Name)
			}
		}
	}
}

func TestRobotSourcesReserveLiteralExitTwoForNotImplemented(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate robot test source")
	}
	packageDir := filepath.Dir(sourceFile)
	entries, err := os.ReadDir(packageDir)
	if err != nil {
		t.Fatalf("read robot package: %v", err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(packageDir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range parsed.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if strings.HasPrefix(fn.Name.Name, "Print") {
					if stmt, ok := node.(*ast.ReturnStmt); ok && len(stmt.Results) == 1 {
						if literal, ok := stmt.Results[0].(*ast.BasicLit); ok && literal.Kind == token.INT && literal.Value == "2" {
							position := fset.Position(stmt.Pos())
							t.Errorf("%s returns literal exit 2 at %s; derive unavailable status from a NOT_IMPLEMENTED envelope", fn.Name.Name, position)
						}
					}
				}
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 || fn.Name.Name == "PrintRobotUnavailable" {
					return true
				}
				literal, ok := call.Args[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.INT || literal.Value != "2" {
					return true
				}
				called := ""
				switch target := call.Fun.(type) {
				case *ast.Ident:
					called = target.Name
				case *ast.SelectorExpr:
					called = target.Sel.Name
				}
				if called == "WithExitCode" || called == "ExitResultForCode" {
					position := fset.Position(call.Pos())
					t.Errorf("%s uses literal exit 2 at %s; only PrintRobotUnavailable may author exit 2 directly", fn.Name.Name, position)
				}
				return true
			})
		}
	}
}
