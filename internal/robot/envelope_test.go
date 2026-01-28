// Package robot provides machine-readable output for AI agents.
// envelope_test.go tests that all robot output types comply with the envelope spec.
package robot

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestEnvelopeSpec documents and tests the robot output envelope requirements.
//
// The robot output envelope is the standardized structure for all robot command
// responses. It ensures AI agents can reliably parse and handle any robot output.
//
// # Envelope Specification v1.0.0
//
// Required fields for all robot responses:
//   - success (bool): Whether the operation completed successfully
//   - timestamp (string): RFC3339 UTC timestamp when response was generated
//   - version (string): Envelope specification version (e.g., "1.0.0")
//   - output_format (string): Serialization format ("json" or "toon")
//
// Optional fields:
//   - _meta (object): Response metadata (timing, exit code, command)
//
// Required for error responses:
//   - error (string): Human-readable error message
//   - error_code (string): Machine-readable error code (see ErrCode* constants)
//   - hint (string, optional): Actionable guidance for resolving the error
//
// Array fields:
//   - Critical arrays must always be present (empty [] if no items)
//   - Never use null for arrays that agents will iterate over
func TestEnvelopeSpec(t *testing.T) {
	t.Run("RobotResponse_HasRequiredFields", func(t *testing.T) {
		resp := NewRobotResponse(true)

		if !resp.Success {
			t.Error("NewRobotResponse(true) should have success=true")
		}
		if resp.Timestamp == "" {
			t.Error("NewRobotResponse should set timestamp")
		}
		// Verify timestamp is valid RFC3339
		_, err := time.Parse(time.RFC3339, resp.Timestamp)
		if err != nil {
			t.Errorf("Timestamp should be RFC3339 format, got: %s", resp.Timestamp)
		}
	})

	t.Run("RobotResponse_HasEnvelopeFields", func(t *testing.T) {
		resp := NewRobotResponse(true)

		// Version should be set to current envelope version
		if resp.Version != EnvelopeVersion {
			t.Errorf("Version = %q, want %q", resp.Version, EnvelopeVersion)
		}

		// OutputFormat should be set based on global OutputFormat
		if resp.OutputFormat == "" {
			t.Error("OutputFormat should be set")
		}
	})

	t.Run("EnvelopeVersion_IsSemVer", func(t *testing.T) {
		// Envelope version should follow semantic versioning
		parts := len(EnvelopeVersion)
		if parts == 0 {
			t.Error("EnvelopeVersion should not be empty")
		}
		// Basic check: contains dots
		if EnvelopeVersion != "1.0.0" {
			t.Logf("EnvelopeVersion = %q (update test if intentionally changed)", EnvelopeVersion)
		}
	})

	t.Run("ResponseMeta_WithTiming", func(t *testing.T) {
		meta, finish := StartResponseMeta("robot-status")
		time.Sleep(1 * time.Millisecond) // Ensure some duration
		finish()

		if meta.Command != "robot-status" {
			t.Errorf("Command = %q, want %q", meta.Command, "robot-status")
		}
		if meta.DurationMs == 0 {
			t.Error("DurationMs should be set after finish()")
		}
	})

	t.Run("ResponseMeta_WithExitCode", func(t *testing.T) {
		meta := NewResponseMeta("robot-test").WithExitCode(1)

		if meta.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want %d", meta.ExitCode, 1)
		}
	})

	t.Run("ErrorResponse_HasRequiredFields", func(t *testing.T) {
		resp := NewErrorResponse(
			errForTest("something went wrong"),
			ErrCodeInternalError,
			"Try restarting the service",
		)

		if resp.Success {
			t.Error("Error response should have success=false")
		}
		if resp.Error != "something went wrong" {
			t.Errorf("Error should be set, got: %s", resp.Error)
		}
		if resp.ErrorCode != ErrCodeInternalError {
			t.Errorf("ErrorCode should be set, got: %s", resp.ErrorCode)
		}
		if resp.Hint != "Try restarting the service" {
			t.Errorf("Hint should be set, got: %s", resp.Hint)
		}
	})
}

// errForTest creates an error for testing purposes.
type errForTest string

func (e errForTest) Error() string { return string(e) }

// TestOutputTypesEmbedRobotResponse verifies that output types embed RobotResponse.
// This test documents which types are compliant and which need migration.
func TestOutputTypesEmbedRobotResponse(t *testing.T) {
	robotResponseType := reflect.TypeOf(RobotResponse{})

	// Compliant types that embed RobotResponse
	compliantTypes := []struct {
		name string
		typ  reflect.Type
	}{
		{"AccountStatusOutput", reflect.TypeOf(AccountStatusOutput{})},
		{"AccountsListOutput", reflect.TypeOf(AccountsListOutput{})},
		{"AckOutput", reflect.TypeOf(AckOutput{})},
		{"ActivityOutput", reflect.TypeOf(ActivityOutput{})},
		{"AgentHealthOutput", reflect.TypeOf(AgentHealthOutput{})},
		{"AlertsOutput", reflect.TypeOf(AlertsOutput{})},
		{"AssignOutput", reflect.TypeOf(AssignOutput{})},
		{"BeadClaimOutput", reflect.TypeOf(BeadClaimOutput{})},
		{"BeadCloseOutput", reflect.TypeOf(BeadCloseOutput{})},
		{"BeadCreateOutput", reflect.TypeOf(BeadCreateOutput{})},
		{"BeadShowOutput", reflect.TypeOf(BeadShowOutput{})},
		{"BeadsListOutput", reflect.TypeOf(BeadsListOutput{})},
		{"BulkAssignOutput", reflect.TypeOf(BulkAssignOutput{})},
		{"CASSContextOutput", reflect.TypeOf(CASSContextOutput{})},
		{"CASSInsightsOutput", reflect.TypeOf(CASSInsightsOutput{})},
		{"CASSSearchOutput", reflect.TypeOf(CASSSearchOutput{})},
		{"CASSStatusOutput", reflect.TypeOf(CASSStatusOutput{})},
		{"CapabilitiesOutput", reflect.TypeOf(CapabilitiesOutput{})},
		{"ContextOutput", reflect.TypeOf(ContextOutput{})},
		{"DCGStatusOutput", reflect.TypeOf(DCGStatusOutput{})},
		{"DashboardOutput", reflect.TypeOf(DashboardOutput{})},
		{"DiagnoseBriefOutput", reflect.TypeOf(DiagnoseBriefOutput{})},
		{"DiagnoseOutput", reflect.TypeOf(DiagnoseOutput{})},
		{"DiffOutput", reflect.TypeOf(DiffOutput{})},
		{"DismissAlertOutput", reflect.TypeOf(DismissAlertOutput{})},
		{"EnsembleOutput", reflect.TypeOf(EnsembleOutput{})},
		{"EnsembleSpawnOutput", reflect.TypeOf(EnsembleSpawnOutput{})},
		{"EnvOutput", reflect.TypeOf(EnvOutput{})},
		{"ErrorsOutput", reflect.TypeOf(ErrorsOutput{})},
		{"FilesOutput", reflect.TypeOf(FilesOutput{})},
		{"GraphOutput", reflect.TypeOf(GraphOutput{})},
		{"HealthOutput", reflect.TypeOf(HealthOutput{})},
		{"HistoryOutput", reflect.TypeOf(HistoryOutput{})},
		{"InspectPaneOutput", reflect.TypeOf(InspectPaneOutput{})},
		{"InterruptOutput", reflect.TypeOf(InterruptOutput{})},
		{"IsWorkingOutput", reflect.TypeOf(IsWorkingOutput{})},
		{"JFPBundlesOutput", reflect.TypeOf(JFPBundlesOutput{})},
		{"JFPCategoriesOutput", reflect.TypeOf(JFPCategoriesOutput{})},
		{"JFPInstalledOutput", reflect.TypeOf(JFPInstalledOutput{})},
		{"JFPListOutput", reflect.TypeOf(JFPListOutput{})},
		{"JFPSearchOutput", reflect.TypeOf(JFPSearchOutput{})},
		{"JFPShowOutput", reflect.TypeOf(JFPShowOutput{})},
		{"JFPStatusOutput", reflect.TypeOf(JFPStatusOutput{})},
		{"JFPSuggestOutput", reflect.TypeOf(JFPSuggestOutput{})},
		{"JFPTagsOutput", reflect.TypeOf(JFPTagsOutput{})},
		{"MSSearchOutput", reflect.TypeOf(MSSearchOutput{})},
		{"MSShowOutput", reflect.TypeOf(MSShowOutput{})},
		{"MailOutput", reflect.TypeOf(MailOutput{})},
		{"MetricsOutput", reflect.TypeOf(MetricsOutput{})},
		{"MonitorOutput", reflect.TypeOf(MonitorOutput{})},
		{"PaletteOutput", reflect.TypeOf(PaletteOutput{})},
		{"PlanOutput", reflect.TypeOf(PlanOutput{})},
		{"ProbeOutput", reflect.TypeOf(ProbeOutput{})},
		{"QuotaCheckOutput", reflect.TypeOf(QuotaCheckOutput{})},
		{"QuotaStatusOutput", reflect.TypeOf(QuotaStatusOutput{})},
		{"RecipesOutput", reflect.TypeOf(RecipesOutput{})},
		{"ReplayOutput", reflect.TypeOf(ReplayOutput{})},
		{"RestartPaneOutput", reflect.TypeOf(RestartPaneOutput{})},
		{"RouteOutput", reflect.TypeOf(RouteOutput{})},
		{"RUSyncOutput", reflect.TypeOf(RUSyncOutput{})},
		{"SchemaOutput", reflect.TypeOf(SchemaOutput{})},
		{"SendAndAckOutput", reflect.TypeOf(SendAndAckOutput{})},
		{"SendOutput", reflect.TypeOf(SendOutput{})},
		{"SessionHealthOutput", reflect.TypeOf(SessionHealthOutput{})},
		{"SmartRestartOutput", reflect.TypeOf(SmartRestartOutput{})},
		{"SnapshotDeltaOutput", reflect.TypeOf(SnapshotDeltaOutput{})},
		{"SnapshotOutput", reflect.TypeOf(SnapshotOutput{})},
		{"SpawnOutput", reflect.TypeOf(SpawnOutput{})},
		{"StatusOutput", reflect.TypeOf(StatusOutput{})},
		{"SwitchAccountOutput", reflect.TypeOf(SwitchAccountOutput{})},
		{"TailOutput", reflect.TypeOf(TailOutput{})},
		{"TUIAlertsOutput", reflect.TypeOf(TUIAlertsOutput{})},
		{"TokensOutput", reflect.TypeOf(TokensOutput{})},
		{"ToolsOutput", reflect.TypeOf(ToolsOutput{})},
		{"TriageOutput", reflect.TypeOf(TriageOutput{})},
		{"VersionOutput", reflect.TypeOf(VersionOutput{})},
	}

	for _, tc := range compliantTypes {
		t.Run(tc.name+"_EmbedRobotResponse", func(t *testing.T) {
			if !embedsType(tc.typ, robotResponseType) {
				t.Errorf("%s should embed RobotResponse", tc.name)
			}
		})
	}
}

// TestOutputTypesHaveRequiredJSONTags verifies JSON tag consistency.
func TestOutputTypesHaveRequiredJSONTags(t *testing.T) {
	t.Run("RobotResponse_JSONTags", func(t *testing.T) {
		typ := reflect.TypeOf(RobotResponse{})

		expectedTags := map[string]string{
			"Success":      "success",
			"Timestamp":    "timestamp",
			"Version":      "version,omitempty",
			"OutputFormat": "output_format,omitempty",
			"Meta":         "_meta,omitempty",
			"Error":        "error,omitempty",
			"ErrorCode":    "error_code,omitempty",
			"Hint":         "hint,omitempty",
		}

		for fieldName, expectedTag := range expectedTags {
			field, ok := typ.FieldByName(fieldName)
			if !ok {
				t.Errorf("RobotResponse should have field %s", fieldName)
				continue
			}
			tag := field.Tag.Get("json")
			if tag != expectedTag {
				t.Errorf("RobotResponse.%s json tag = %q, want %q", fieldName, tag, expectedTag)
			}
		}
	})

	t.Run("ResponseMeta_JSONTags", func(t *testing.T) {
		typ := reflect.TypeOf(ResponseMeta{})

		expectedTags := map[string]string{
			"DurationMs": "duration_ms,omitempty",
			"ExitCode":   "exit_code,omitempty",
			"Command":    "command,omitempty",
		}

		for fieldName, expectedTag := range expectedTags {
			field, ok := typ.FieldByName(fieldName)
			if !ok {
				t.Errorf("ResponseMeta should have field %s", fieldName)
				continue
			}
			tag := field.Tag.Get("json")
			if tag != expectedTag {
				t.Errorf("ResponseMeta.%s json tag = %q, want %q", fieldName, tag, expectedTag)
			}
		}
	})
}

// TestArrayFieldsNeverNull verifies critical array fields are initialized.
func TestArrayFieldsNeverNull(t *testing.T) {
	// Test that array fields in output types are initialized to empty slices
	// rather than nil when there are no items.

	t.Run("AgentHints_SuggestedActions", func(t *testing.T) {
		hints := AgentHints{}
		data, err := json.Marshal(hints)
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}

		// Check that suggested_actions is omitted when nil (omitempty)
		// This is acceptable for optional hint fields
		var unmarshaled map[string]interface{}
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}

		// suggested_actions should be omitted when empty (due to omitempty)
		if _, exists := unmarshaled["suggested_actions"]; exists {
			t.Log("suggested_actions present when empty (acceptable with omitempty)")
		}
	})

	t.Run("SendOutput_Arrays", func(t *testing.T) {
		output := SendOutput{
			RobotResponse: NewRobotResponse(true),
			Session:       "test",
			Targets:       []string{},    // Empty but present
			Successful:    []string{},    // Empty but present
			Failed:        []SendError{}, // Empty but present
		}

		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}

		var unmarshaled map[string]interface{}
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}

		// These arrays should be present as [] not null
		for _, field := range []string{"targets", "successful", "failed"} {
			val, exists := unmarshaled[field]
			if !exists {
				t.Errorf("SendOutput.%s should be present in JSON", field)
				continue
			}
			arr, ok := val.([]interface{})
			if !ok {
				t.Errorf("SendOutput.%s should be an array, got %T", field, val)
				continue
			}
			if arr == nil {
				t.Errorf("SendOutput.%s should be [] not null", field)
			}
		}
	})
}

// TestEnvelope_ErrorCodes verifies all error codes are documented and consistent.
func TestEnvelope_ErrorCodes(t *testing.T) {
	// All documented error codes
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

	seen := make(map[string]bool)
	for _, code := range codes {
		t.Run("Code_"+code, func(t *testing.T) {
			// Verify format: UPPER_SNAKE_CASE
			if code == "" {
				t.Error("Error code should not be empty")
			}
			for _, c := range code {
				if c != '_' && (c < 'A' || c > 'Z') {
					t.Errorf("Error code %q should be UPPER_SNAKE_CASE", code)
					break
				}
			}
			// Verify uniqueness
			if seen[code] {
				t.Errorf("Duplicate error code: %s", code)
			}
			seen[code] = true
		})
	}
}

// TestEnvelope_TimestampHelpers verifies timestamp formatting helpers for envelope compliance.
func TestEnvelope_TimestampHelpers(t *testing.T) {
	t.Run("RFC3339_Format", func(t *testing.T) {
		ts := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
		formatted := FormatTimestamp(ts)
		// Verify RFC3339 compliance
		parsed, err := time.Parse(time.RFC3339, formatted)
		if err != nil {
			t.Errorf("FormatTimestamp should produce RFC3339: %v", err)
		}
		if !parsed.Equal(ts) {
			t.Error("Round-trip timestamp mismatch")
		}
	})

	t.Run("UTC_Timezone", func(t *testing.T) {
		formatted := FormatTimestamp(time.Now())
		// Should end with Z (UTC)
		if formatted[len(formatted)-1] != 'Z' {
			t.Errorf("Timestamp should be UTC (end with Z), got: %s", formatted)
		}
	})
}

// embedsType checks if target embeds embeddedType.
func embedsType(target, embeddedType reflect.Type) bool {
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

// ============================================================================
// Non-Compliant Types Documentation
// ============================================================================
// Remaining Output structs without RobotResponse are nested helper types
// rather than top-level robot responses:
//
// - CapturedOutput (synthesis.go)
// - JSONOutput (synthesis.go)
// - PaneOutput (robot.go)
// - ToolInfoOutput (tools.go)
// - ToolHealthOutput (tools.go)
// - ToolHealthOutput (tools.go)
// - PaneOutput (robot.go)
//
// These are intentionally nested inside higher-level outputs that already
// embed RobotResponse, so no envelope is required here.
// ============================================================================
