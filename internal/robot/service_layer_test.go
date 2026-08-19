// Package robot provides machine-readable output for AI agents.
// service_layer_test.go tests that GetX service functions return deterministic outputs.
package robot

import (
	"encoding/json"
	"reflect"
	"testing"
)

// =============================================================================
// Service Layer Determinism Tests
//
// These tests verify that GetX functions return consistent, deterministic output
// structures as required by bd-2gjig acceptance criteria.
// =============================================================================

// TestServiceLayerOutputStructure verifies GetX functions return properly structured outputs.
func TestServiceLayerOutputStructure(t *testing.T) {
	t.Run("GetVersion_Structure", func(t *testing.T) {
		output, err := GetVersion()
		if err != nil {
			t.Fatalf("GetVersion failed: %v", err)
		}

		// Verify required fields
		if output.Version == "" {
			t.Error("GetVersion should return non-empty version")
		}

		// Verify envelope compliance
		if !output.Success {
			t.Error("GetVersion should have success=true")
		}
		if output.Timestamp == "" {
			t.Error("GetVersion should have timestamp set")
		}

		t.Logf("GetVersion: version=%s success=%v timestamp=%s",
			output.Version, output.Success, output.Timestamp)
	})

	t.Run("GetPlan_Structure", func(t *testing.T) {
		skipSlowRobotShortIntegrationTest(t, "GetPlan_Structure shells into live planning and belongs in longer integration runs")
		output, err := GetPlan()
		if err != nil {
			t.Fatalf("GetPlan failed: %v", err)
		}

		// Verify envelope compliance
		if !output.Success && output.Error == "" {
			t.Error("GetPlan with success=false should have error message")
		}
		if output.Timestamp == "" {
			t.Error("GetPlan should have timestamp set")
		}

		t.Logf("GetPlan: success=%v timestamp=%s", output.Success, output.Timestamp)
	})
}

// TestServiceLayerDeterminism verifies repeated calls return consistent structure.
func TestServiceLayerDeterminism(t *testing.T) {
	t.Run("GetVersion_Deterministic", func(t *testing.T) {
		output1, err := GetVersion()
		if err != nil {
			t.Fatalf("GetVersion first call failed: %v", err)
		}

		output2, err := GetVersion()
		if err != nil {
			t.Fatalf("GetVersion second call failed: %v", err)
		}

		// Version should be the same
		if output1.Version != output2.Version {
			t.Errorf("GetVersion version mismatch: %q vs %q", output1.Version, output2.Version)
		}

		// Both should have timestamps (may differ)
		if output1.Timestamp == "" || output2.Timestamp == "" {
			t.Error("GetVersion should always set timestamp")
		}

		t.Logf("GetVersion deterministic: version1=%s version2=%s",
			output1.Version, output2.Version)
	})
}

// TestServiceLayerJSONCompliance verifies outputs produce valid JSON.
func TestServiceLayerJSONCompliance(t *testing.T) {
	t.Run("GetVersion_JSON", func(t *testing.T) {
		output, err := GetVersion()
		if err != nil {
			t.Fatalf("GetVersion failed: %v", err)
		}

		// Should marshal to valid JSON
		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("GetVersion output should marshal: %v", err)
		}

		// Should unmarshal back
		var unmarshaled VersionOutput
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("GetVersion output should unmarshal: %v", err)
		}

		// Values should match
		if unmarshaled.Version != output.Version {
			t.Errorf("Round-trip version mismatch: %q vs %q",
				unmarshaled.Version, output.Version)
		}

		t.Logf("GetVersion JSON: len=%d bytes", len(data))
	})

	t.Run("GetPlan_JSON", func(t *testing.T) {
		skipSlowRobotShortIntegrationTest(t, "GetPlan_JSON shells into live planning and belongs in longer integration runs")
		output, err := GetPlan()
		if err != nil {
			t.Fatalf("GetPlan failed: %v", err)
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("GetPlan output should marshal: %v", err)
		}

		// Verify it's valid JSON by unmarshaling to generic map
		var unmarshaled map[string]interface{}
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("GetPlan output should produce valid JSON: %v", err)
		}

		// Check required envelope fields
		if _, ok := unmarshaled["success"]; !ok {
			t.Error("GetPlan JSON should have 'success' field")
		}
		if _, ok := unmarshaled["timestamp"]; !ok {
			t.Error("GetPlan JSON should have 'timestamp' field")
		}

		t.Logf("GetPlan JSON: len=%d bytes", len(data))
	})
}

// TestServiceLayerEnvelopeCompliance verifies GetX outputs embed RobotResponse.
func TestServiceLayerEnvelopeCompliance(t *testing.T) {
	robotResponseType := reflect.TypeOf(RobotResponse{})

	outputTypes := []struct {
		name string
		typ  reflect.Type
	}{
		{"VersionOutput", reflect.TypeOf(VersionOutput{})},
		{"PlanOutput", reflect.TypeOf(PlanOutput{})},
		{"StatusOutput", reflect.TypeOf(StatusOutput{})},
		{"SnapshotOutput", reflect.TypeOf(SnapshotOutput{})},
		{"TailOutput", reflect.TypeOf(TailOutput{})},
		{"ContextOutput", reflect.TypeOf(ContextOutput{})},
		{"ActivityOutput", reflect.TypeOf(ActivityOutput{})},
		{"SendOutput", reflect.TypeOf(SendOutput{})},
		{"HealthOutput", reflect.TypeOf(HealthOutput{})},
		{"DiagnoseOutput", reflect.TypeOf(DiagnoseOutput{})},
	}

	for _, tc := range outputTypes {
		t.Run(tc.name+"_Envelope", func(t *testing.T) {
			if !embedsRobotResponse(tc.typ, robotResponseType) {
				t.Errorf("%s should embed RobotResponse for envelope compliance", tc.name)
			}
			t.Logf("%s embeds RobotResponse: true", tc.name)
		})
	}
}

// TestServiceLayerErrorHandling verifies GetX functions handle errors consistently.
func TestServiceLayerErrorHandling(t *testing.T) {
	// Test that error returns are consistent
	t.Run("Error_Response_Format", func(t *testing.T) {
		// Create an error response
		errResp := NewErrorResponse(
			errForTestService("test error"),
			ErrCodeInternalError,
			"Try again",
		)

		// Should have success=false
		if errResp.Success {
			t.Error("Error response should have success=false")
		}

		// Should have error message
		if errResp.Error != "test error" {
			t.Errorf("Error message=%q, want 'test error'", errResp.Error)
		}

		// Should have error code
		if errResp.ErrorCode != ErrCodeInternalError {
			t.Errorf("Error code=%q, want %q", errResp.ErrorCode, ErrCodeInternalError)
		}

		// Should have timestamp
		if errResp.Timestamp == "" {
			t.Error("Error response should have timestamp")
		}

		t.Logf("Error response: success=%v error=%s code=%s",
			errResp.Success, errResp.Error, errResp.ErrorCode)
	})
}

// TestServiceLayerNullSafety verifies array fields are never null in JSON.
func TestServiceLayerNullSafety(t *testing.T) {
	t.Run("SendOutput_EmptyArrays", func(t *testing.T) {
		output := SendOutput{
			RobotResponse: NewRobotResponse(true),
			Session:       "test",
			Blocked:       false,
			Redaction:     RedactionSummary{Mode: "off", Findings: 0, Action: "off"},
			Warnings:      []string{},
			Targets:       []string{},
			Successful:    []string{},
			Failed:        []SendError{},
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}

		// Arrays should be [] not null
		arrayFields := []string{"warnings", "targets", "successful", "failed"}
		for _, field := range arrayFields {
			val, exists := raw[field]
			if !exists {
				t.Errorf("Field %s should be present", field)
				continue
			}
			if val == nil {
				t.Errorf("Field %s should be [] not null", field)
				continue
			}
			arr, ok := val.([]interface{})
			if !ok {
				t.Errorf("Field %s should be array, got %T", field, val)
				continue
			}
			t.Logf("Field %s: type=array len=%d", field, len(arr))
		}
	})

	t.Run("ActivityOutput_EmptyArrays", func(t *testing.T) {
		output := ActivityOutput{
			RobotResponse: NewRobotResponse(true),
			Agents:        []AgentActivityInfo{},
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}

		// Agents array should be present
		if val, exists := raw["agents"]; !exists {
			t.Error("Field 'agents' should be present")
		} else if val == nil {
			t.Error("Field 'agents' should be [] not null")
		} else if arr, ok := val.([]interface{}); !ok {
			t.Errorf("Field 'agents' should be array, got %T", val)
		} else {
			t.Logf("Field agents: type=array len=%d", len(arr))
		}
	})
}

// TestServiceLayerResponseMeta verifies _meta field is properly populated.
func TestServiceLayerResponseMeta(t *testing.T) {
	t.Run("ResponseMeta_Creation", func(t *testing.T) {
		meta := NewResponseMeta("robot-test")

		if meta.Command != "robot-test" {
			t.Errorf("Command=%q, want 'robot-test'", meta.Command)
		}

		t.Logf("ResponseMeta: command=%s", meta.Command)
	})

	t.Run("ResponseMeta_WithExitCode", func(t *testing.T) {
		meta := NewResponseMeta("robot-test").WithExitCode(1)

		if meta.ExitCode != 1 {
			t.Errorf("ExitCode=%d, want 1", meta.ExitCode)
		}

		t.Logf("ResponseMeta: command=%s exit_code=%d", meta.Command, meta.ExitCode)
	})

	t.Run("ResponseMeta_Timing", func(t *testing.T) {
		meta, finish := StartResponseMeta("robot-timed")
		// Do some work
		for i := 0; i < 1000; i++ {
			_ = i * 2
		}
		finish()

		if meta.DurationMs == 0 {
			// Duration might be 0 if very fast, so just verify it's set
			t.Log("DurationMs is 0 (operation was very fast)")
		} else {
			t.Logf("ResponseMeta: command=%s duration_ms=%d", meta.Command, meta.DurationMs)
		}
	})
}

// errForTestService creates an error for testing purposes.
type errForTestService string

func (e errForTestService) Error() string { return string(e) }

// embedsRobotResponse checks if target embeds RobotResponse.
func embedsRobotResponse(target, embeddedType reflect.Type) bool {
	if target.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < target.NumField(); i++ {
		field := target.Field(i)
		if field.Anonymous && field.Type == embeddedType {
			return true
		}
	}
	return false
}
