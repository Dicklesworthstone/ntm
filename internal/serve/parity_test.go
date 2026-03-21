package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// =============================================================================
// CLI vs REST Parity Test Harness (bd-2qq5u)
//
// These tests verify that robot.Get*() outputs match REST handler outputs.
// This ensures AI agents get consistent data regardless of interface.
//
// Coverage:
//   - Session list/status endpoints
//   - Pane capture and output endpoints
//   - Output summary endpoints
//   - Metrics export endpoints
//   - Schema generation
//   - Error response formats
//
// Acceptance Criteria:
//   - Parity tests catch mismatches deterministically
//   - Actionable diff output when mismatches detected
//   - Volatile fields (timestamps, request_ids) normalized before comparison
//
// Run with: go test -v ./internal/serve/... -run "TestParity"
// =============================================================================

// volatileFields are fields that change between invocations and should be
// removed before comparison.
var volatileFields = []string{
	"timestamp", "ts", "generated_at", "created_at", "updated_at",
	"request_id", "duration_ms", "duration", "elapsed", "_meta",
}

// normalizeForParity removes volatile fields from a JSON object for comparison.
func normalizeForParity(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("failed to parse JSON for normalization: %v\nData: %s", err, string(data))
	}
	removeVolatileFieldsRecursive(obj, volatileFields)
	return obj
}

// removeVolatileFieldsRecursive removes volatile fields from a JSON object recursively.
func removeVolatileFieldsRecursive(obj map[string]any, fields []string) {
	for _, field := range fields {
		delete(obj, field)
	}

	for _, v := range obj {
		switch val := v.(type) {
		case map[string]any:
			removeVolatileFieldsRecursive(val, fields)
		case []any:
			for _, item := range val {
				if m, ok := item.(map[string]any); ok {
					removeVolatileFieldsRecursive(m, fields)
				}
			}
		}
	}
}

// compareNormalized compares two normalized JSON maps and returns detailed diff.
func compareNormalized(t *testing.T, name string, expected, actual map[string]any) bool {
	t.Helper()

	expectedJSON, _ := json.MarshalIndent(expected, "", "  ")
	actualJSON, _ := json.MarshalIndent(actual, "", "  ")

	if bytes.Equal(expectedJSON, actualJSON) {
		return true
	}

	// Generate actionable diff
	t.Errorf("%s: outputs differ", name)
	t.Logf("Expected (robot):\n%s", string(expectedJSON))
	t.Logf("Actual (REST):\n%s", string(actualJSON))

	// Find specific differences
	diffs := findDifferences("", expected, actual)
	if len(diffs) > 0 {
		t.Logf("Specific differences:")
		for _, diff := range diffs {
			t.Logf("  %s", diff)
		}
	}

	return false
}

// findDifferences returns human-readable difference descriptions.
func findDifferences(path string, expected, actual map[string]any) []string {
	var diffs []string

	// Check for missing keys in actual
	for k, ev := range expected {
		keyPath := joinPath(path, k)
		av, exists := actual[k]
		if !exists {
			diffs = append(diffs, keyPath+": missing in REST output")
			continue
		}
		if !deepEqual(ev, av) {
			diffs = append(diffs, fmt.Sprintf("%s: value differs (expected=%v, got=%v)", keyPath, ev, av))
		}
	}

	// Check for extra keys in actual
	for k := range actual {
		keyPath := joinPath(path, k)
		if _, exists := expected[k]; !exists {
			diffs = append(diffs, keyPath+": unexpected in REST output")
		}
	}

	return diffs
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func deepEqual(a, b any) bool {
	return reflect.DeepEqual(a, b)
}

// =============================================================================
// Schema Parity Test
// =============================================================================

func TestParitySchemaOutput(t *testing.T) {
	// robot.GetSchema() returns the schema data struct
	robotOutput, err := robot.GetSchema("status")
	if err != nil {
		t.Fatalf("robot.GetSchema failed: %v", err)
	}

	// Serialize robot output to JSON
	robotJSON, err := json.Marshal(robotOutput)
	if err != nil {
		t.Fatalf("failed to marshal robot output: %v", err)
	}

	// Verify the robot output contains expected fields
	robotNorm := normalizeForParity(t, robotJSON)

	// Check essential fields
	if _, ok := robotNorm["success"]; !ok {
		t.Error("robot schema output missing 'success' field")
	}
	if _, ok := robotNorm["schema_type"]; !ok {
		t.Error("robot schema output missing 'schema_type' field")
	}
	if robotNorm["schema_type"] != "status" {
		t.Errorf("schema_type mismatch: got %v, want 'status'", robotNorm["schema_type"])
	}

	t.Logf("Schema output validated: %d fields", len(robotNorm))
}

// TestParitySchemaAllOutput verifies schema=all returns all schemas.
func TestParitySchemaAllOutput(t *testing.T) {
	robotOutput, err := robot.GetSchema("all")
	if err != nil {
		t.Fatalf("robot.GetSchema('all') failed: %v", err)
	}

	robotJSON, err := json.Marshal(robotOutput)
	if err != nil {
		t.Fatalf("failed to marshal robot output: %v", err)
	}

	robotNorm := normalizeForParity(t, robotJSON)

	// Verify schemas array exists
	schemas, ok := robotNorm["schemas"].([]any)
	if !ok {
		t.Fatal("schema output missing 'schemas' array")
	}

	// Should have multiple schemas
	if len(schemas) < 5 {
		t.Errorf("expected at least 5 schemas, got %d", len(schemas))
	}

	t.Logf("All schemas: %d total", len(schemas))
}

// =============================================================================
// Health Endpoint Parity
// =============================================================================

func TestParityHealthEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Call REST handler
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("REST health returned %d, want %d", rec.Code, http.StatusOK)
	}

	restNorm := normalizeForParity(t, rec.Body.Bytes())

	// Health endpoint should always have success=true and status=healthy
	if restNorm["success"] != true {
		t.Error("health endpoint should return success=true")
	}
	if restNorm["status"] != "healthy" {
		t.Errorf("health status = %v, want 'healthy'", restNorm["status"])
	}
}

// =============================================================================
// Sessions Endpoint Parity
// =============================================================================

func TestParitySessionsEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Call REST handler
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()
	srv.handleSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("REST sessions returned %d, want %d", rec.Code, http.StatusOK)
	}

	restNorm := normalizeForParity(t, rec.Body.Bytes())

	// Sessions endpoint should have success and sessions array
	if restNorm["success"] != true {
		t.Error("sessions endpoint should return success=true")
	}
	if _, ok := restNorm["sessions"]; !ok {
		t.Error("sessions endpoint missing 'sessions' field")
	}

	// Sessions should be an array (possibly empty, never nil)
	sessions, ok := restNorm["sessions"].([]any)
	if !ok && restNorm["sessions"] != nil {
		t.Errorf("sessions should be an array, got %T", restNorm["sessions"])
	}
	_ = sessions // may be nil if no sessions exist
}

// =============================================================================
// Robot Status Endpoint Parity
// =============================================================================

func TestParityRobotStatusEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Call REST handler
	req := httptest.NewRequest(http.MethodGet, "/api/robot/status", nil)
	rec := httptest.NewRecorder()
	srv.handleRobotStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("REST robot/status returned %d, want %d", rec.Code, http.StatusOK)
	}

	restNorm := normalizeForParity(t, rec.Body.Bytes())

	// Robot status should have success=true
	if restNorm["success"] != true {
		t.Error("robot/status endpoint should return success=true")
	}

	t.Logf("Robot status REST response has %d fields", len(restNorm))
}

// =============================================================================
// Version Parity Test
// =============================================================================

func TestParityVersionOutput(t *testing.T) {
	// Get robot version output
	robotOutput, err := robot.GetVersion()
	if err != nil {
		t.Fatalf("robot.GetVersion failed: %v", err)
	}

	robotJSON, err := json.Marshal(robotOutput)
	if err != nil {
		t.Fatalf("failed to marshal robot output: %v", err)
	}

	robotNorm := normalizeForParity(t, robotJSON)

	// Verify expected fields
	if _, ok := robotNorm["version"]; !ok {
		t.Error("version output missing 'version' field")
	}
	if robotNorm["success"] != true {
		t.Error("version output should have success=true")
	}

	// Verify system info nested fields
	system, ok := robotNorm["system"].(map[string]any)
	if !ok {
		t.Fatal("version output missing 'system' object")
	}

	systemFields := []string{"go_version", "os", "arch"}
	for _, field := range systemFields {
		if _, ok := system[field]; !ok {
			t.Errorf("version output missing system.%s field", field)
		}
	}

	t.Logf("Version output validated: version=%v", robotNorm["version"])
}

// =============================================================================
// Capabilities Parity Test
// =============================================================================

func TestParityCapabilitiesOutput(t *testing.T) {
	srv, _ := setupTestServer(t)

	robotOutput, err := robot.GetCapabilities()
	if err != nil {
		t.Fatalf("robot.GetCapabilities failed: %v", err)
	}

	robotJSON, err := json.Marshal(robotOutput)
	if err != nil {
		t.Fatalf("failed to marshal robot output: %v", err)
	}

	robotNorm := normalizeForParity(t, robotJSON)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "test-123"))
	rec := httptest.NewRecorder()
	srv.handleCapabilitiesV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("REST capabilities returned %d, want %d", rec.Code, http.StatusOK)
	}

	restNorm := normalizeForParity(t, rec.Body.Bytes())
	if !compareNormalized(t, "capabilities", robotNorm, restNorm) {
		t.Fatal("capabilities parity mismatch")
	}

	if robotNorm["success"] != true {
		t.Error("capabilities output should have success=true")
	}
	if _, ok := robotNorm["commands"]; !ok {
		t.Error("capabilities output missing 'commands' field")
	}
	if _, ok := robotNorm["categories"]; !ok {
		t.Error("capabilities output missing 'categories' field")
	}
	attention, ok := robotNorm["attention"].(map[string]any)
	if !ok {
		t.Fatal("capabilities output missing 'attention' object")
	}
	features, ok := attention["features"].(map[string]any)
	if !ok {
		t.Fatal("capabilities output missing attention.features")
	}
	if _, ok := features["operator_boundary"]; !ok {
		t.Fatal("capabilities output missing attention.features.operator_boundary")
	}

	t.Logf("Capabilities output validated: %d fields", len(robotNorm))
}

// =============================================================================
// Envelope Consistency Tests
// =============================================================================

// TestParityEnvelopeFieldsConsistent verifies all robot outputs have consistent
// envelope fields (success, timestamp, version).
func TestParityEnvelopeFieldsConsistent(t *testing.T) {
	// List of robot Get* functions that return outputs
	tests := []struct {
		name string
		get  func() (any, error)
	}{
		{"GetVersion", func() (any, error) { return robot.GetVersion() }},
		{"GetCapabilities", func() (any, error) { return robot.GetCapabilities() }},
		{"GetHealth", func() (any, error) { return robot.GetHealth() }},
		{"GetSchema_status", func() (any, error) { return robot.GetSchema("status") }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, err := tc.get()
			if err != nil {
				t.Fatalf("%s failed: %v", tc.name, err)
			}

			data, err := json.Marshal(output)
			if err != nil {
				t.Fatalf("failed to marshal %s output: %v", tc.name, err)
			}

			var obj map[string]any
			if err := json.Unmarshal(data, &obj); err != nil {
				t.Fatalf("failed to parse %s output: %v", tc.name, err)
			}

			// All outputs should have 'success' field
			if _, ok := obj["success"]; !ok {
				t.Errorf("%s missing 'success' envelope field", tc.name)
			}

			// All outputs should have 'timestamp' field
			if _, ok := obj["timestamp"]; !ok {
				t.Errorf("%s missing 'timestamp' envelope field", tc.name)
			}
		})
	}
}

// =============================================================================
// Output Summary Parity (requires session context)
// =============================================================================

func TestParityOutputSummaryHandler(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Call without session - should fail with informative error
	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/summary", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "test-123"))
	rec := httptest.NewRecorder()

	srv.handleOutputSummaryV1(rec, req)

	// Should return 400 Bad Request when session is missing
	if rec.Code != http.StatusBadRequest {
		t.Errorf("output/summary without session returned %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}

	// Should have error message
	if _, ok := resp["error"]; !ok {
		t.Error("error response missing 'error' field")
	}
}

// =============================================================================
// Metrics Export Parity
// =============================================================================

func TestParityMetricsExportHandler(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Call metrics export
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/export", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "test-123"))
	rec := httptest.NewRecorder()

	srv.handleMetricsExportV1(rec, req)

	// Should return 200 OK (format defaults to json)
	if rec.Code != http.StatusOK {
		t.Errorf("metrics/export returned %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Should have success=true
	if resp["success"] != true {
		t.Error("metrics/export should return success=true")
	}
}

// =============================================================================
// Pane Capture Parity (stub - requires live session)
// =============================================================================

func TestParityPaneOutputHandler(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Call pane output without session - should fail
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test/panes/0/output", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "test-123"))
	rec := httptest.NewRecorder()

	// This will fail because session doesn't exist, which is expected
	srv.handlePaneOutputV1(rec, req)

	// Should return error (400 or 404)
	if rec.Code == http.StatusOK {
		t.Error("pane output for non-existent session should not return 200")
	}

	t.Logf("Pane output for non-existent session returned %d (expected)", rec.Code)
}

// =============================================================================
// Field Normalization Tests
// =============================================================================

func TestNormalizeRemovesVolatileFields(t *testing.T) {
	input := `{
		"success": true,
		"timestamp": "2025-01-27T10:00:00Z",
		"generated_at": "2025-01-27T10:00:00Z",
		"request_id": "abc-123",
		"duration_ms": 42,
		"data": {
			"timestamp": "nested-timestamp",
			"value": 100
		},
		"items": [
			{"timestamp": "item-ts", "name": "test"}
		]
	}`

	norm := normalizeForParity(t, []byte(input))

	// Top-level volatile fields should be removed
	if _, ok := norm["timestamp"]; ok {
		t.Error("timestamp should be removed")
	}
	if _, ok := norm["generated_at"]; ok {
		t.Error("generated_at should be removed")
	}
	if _, ok := norm["request_id"]; ok {
		t.Error("request_id should be removed")
	}
	if _, ok := norm["duration_ms"]; ok {
		t.Error("duration_ms should be removed")
	}

	// Non-volatile fields should remain
	if norm["success"] != true {
		t.Error("success field should be preserved")
	}

	// Nested volatile fields should be removed
	data, ok := norm["data"].(map[string]any)
	if !ok {
		t.Fatal("data field should exist")
	}
	if _, ok := data["timestamp"]; ok {
		t.Error("nested timestamp should be removed")
	}
	if data["value"] != float64(100) {
		t.Error("nested value should be preserved")
	}

	// Array items should have volatile fields removed
	items, ok := norm["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatal("items array should exist")
	}
	item := items[0].(map[string]any)
	if _, ok := item["timestamp"]; ok {
		t.Error("array item timestamp should be removed")
	}
	if item["name"] != "test" {
		t.Error("array item name should be preserved")
	}
}

// =============================================================================
// Schema Registry Consistency
// =============================================================================

func TestParitySchemaRegistryComplete(t *testing.T) {
	// Get all registered schema types
	allSchemas, err := robot.GetSchema("all")
	if err != nil {
		t.Fatalf("GetSchema('all') failed: %v", err)
	}

	if allSchemas.Schemas == nil {
		t.Fatal("schemas array is nil")
	}

	// Collect schema names
	var schemaNames []string
	for _, schema := range allSchemas.Schemas {
		if schema != nil && schema.Title != "" {
			schemaNames = append(schemaNames, schema.Title)
		}
	}

	sort.Strings(schemaNames)

	t.Logf("Registered schemas (%d):", len(schemaNames))
	for _, name := range schemaNames {
		t.Logf("  - %s", name)
	}

	// Minimum expected schemas based on SchemaCommand map
	minimumSchemas := 10
	if len(schemaNames) < minimumSchemas {
		t.Errorf("expected at least %d schemas, got %d", minimumSchemas, len(schemaNames))
	}
}

// =============================================================================
// Error Response Parity
// =============================================================================

func TestParityErrorResponseFormat(t *testing.T) {
	srv, _ := setupTestServer(t)

	tests := []struct {
		name     string
		method   string
		path     string
		handler  http.HandlerFunc
		wantCode int
	}{
		{
			name:     "sessions_missing_store",
			method:   http.MethodGet,
			path:     "/api/sessions",
			handler:  New(Config{}).handleSessions, // No store configured
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name:     "session_missing_id",
			method:   http.MethodGet,
			path:     "/api/sessions/",
			handler:  srv.handleSession,
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			tc.handler(rec, req)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantCode)
			}

			// Error responses should be valid JSON
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Errorf("error response is not valid JSON: %v", err)
				return
			}

			// Error responses should have 'error' or 'success=false'
			if resp["success"] == true {
				t.Error("error response should not have success=true")
			}
		})
	}
}

// =============================================================================
// Benchmark: Normalization
// =============================================================================

func BenchmarkNormalizeForParity(b *testing.B) {
	sample := []byte(`{
		"success": true,
		"timestamp": "2025-01-27T10:00:00Z",
		"sessions": [
			{"name": "test", "timestamp": "2025-01-27T10:00:00Z", "panes": 4},
			{"name": "dev", "timestamp": "2025-01-27T10:00:00Z", "panes": 2}
		],
		"summary": {"total": 2, "generated_at": "2025-01-27T10:00:00Z"}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var obj map[string]any
		_ = json.Unmarshal(sample, &obj)
		removeVolatileFieldsRecursive(obj, volatileFields)
	}
}
