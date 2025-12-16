package robot

import (
	"encoding/json"
	"errors"
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

func TestWithAgentHints(t *testing.T) {
	// Create a response with agent hints
	type StatusResponse struct {
		RobotResponse
		SessionCount int `json:"session_count"`
	}

	resp := StatusResponse{
		RobotResponse: NewRobotResponse(true),
		SessionCount:  3,
	}

	hints := &AgentHints{
		Summary: "3 active sessions",
		SuggestedActions: []RobotAction{
			{Action: "monitor", Reason: "all agents working"},
		},
	}

	wrapped := AddAgentHints(resp, hints)
	data, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Verify original fields are present
	if parsed["success"] != true {
		t.Error("expected success in output")
	}
	if parsed["session_count"] != float64(3) {
		t.Errorf("expected session_count 3, got %v", parsed["session_count"])
	}

	// Verify _agent_hints is present
	hintsData, ok := parsed["_agent_hints"]
	if !ok {
		t.Fatal("expected _agent_hints in output")
	}

	hintsMap, ok := hintsData.(map[string]interface{})
	if !ok {
		t.Fatal("_agent_hints should be an object")
	}

	if hintsMap["summary"] != "3 active sessions" {
		t.Errorf("unexpected summary: %v", hintsMap["summary"])
	}
}

func TestWithAgentHintsNil(t *testing.T) {
	// When hints are nil, should just return the data
	resp := NewRobotResponse(true)
	wrapped := AddAgentHints(resp, nil)

	data, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if _, ok := parsed["_agent_hints"]; ok {
		t.Error("_agent_hints should not be present when nil")
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
	// RobotError should output JSON and return the error
	testErr := errors.New("test error message")

	// Note: In a real test we'd capture stdout to verify JSON output
	// For now, just verify it returns the error correctly
	returnedErr := RobotError(testErr, ErrCodeSessionNotFound, "test hint")
	if returnedErr != testErr {
		t.Errorf("RobotError should return the original error, got %v", returnedErr)
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

func TestFormatUnixMillis(t *testing.T) {
	t.Run("zero returns empty", func(t *testing.T) {
		result := FormatUnixMillis(0)
		if result != "" {
			t.Errorf("FormatUnixMillis(0) = %q, want empty string", result)
		}
	})

	t.Run("valid milliseconds", func(t *testing.T) {
		// 2025-12-15T10:30:00Z in milliseconds
		ms := int64(1765795800000)
		result := FormatUnixMillis(ms)
		// Verify it's valid RFC3339
		_, err := time.Parse(time.RFC3339, result)
		if err != nil {
			t.Errorf("Result is not valid RFC3339: %v", err)
		}
		// Verify it ends with Z (UTC)
		if result[len(result)-1] != 'Z' {
			t.Errorf("Result %q should end with Z", result)
		}
	})
}

func TestFormatUnixSeconds(t *testing.T) {
	t.Run("zero returns empty", func(t *testing.T) {
		result := FormatUnixSeconds(0)
		if result != "" {
			t.Errorf("FormatUnixSeconds(0) = %q, want empty string", result)
		}
	})

	t.Run("valid seconds", func(t *testing.T) {
		// 2025-12-15T10:30:00Z in seconds
		sec := int64(1765795800)
		result := FormatUnixSeconds(sec)
		// Verify it's valid RFC3339
		parsed, err := time.Parse(time.RFC3339, result)
		if err != nil {
			t.Errorf("Result is not valid RFC3339: %v", err)
		}
		// Verify round-trip
		if parsed.Unix() != sec {
			t.Errorf("Round-trip failed: got %d, want %d", parsed.Unix(), sec)
		}
	})
}

func TestTimestampConsistency(t *testing.T) {
	// All timestamp functions should produce consistent RFC3339 format
	now := time.Now()
	nowMs := now.UnixMilli()
	nowSec := now.Unix()

	results := []string{
		FormatTimestamp(now),
		FormatUnixMillis(nowMs),
		FormatUnixSeconds(nowSec),
	}

	for i, result := range results {
		// All should be valid RFC3339
		_, err := time.Parse(time.RFC3339, result)
		if err != nil {
			t.Errorf("Result %d is not valid RFC3339: %v", i, err)
		}
		// All should end with Z
		if result[len(result)-1] != 'Z' {
			t.Errorf("Result %d = %q should end with Z", i, result)
		}
	}
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

		requiredArrays := []string{"targets", "successful", "failed"}
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
