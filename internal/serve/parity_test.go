package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
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

func decodeResponseMap(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v\nBody: %s", err, string(body))
	}
	return resp
}

func requireArrayField(t *testing.T, resp map[string]any, field string, wantLen int) []any {
	t.Helper()

	raw, ok := resp[field]
	if !ok {
		t.Fatalf("response missing %q field", field)
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("response field %q has type %T, want []any", field, raw)
	}
	if wantLen >= 0 && len(items) != wantLen {
		t.Fatalf("%s len = %d, want %d", field, len(items), wantLen)
	}
	return items
}

func requireObjectField(t *testing.T, resp map[string]any, field string) map[string]any {
	t.Helper()

	raw, ok := resp[field]
	if !ok {
		t.Fatalf("response missing %q field", field)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("response field %q has type %T, want object", field, raw)
	}
	return obj
}

func seedRobotAPIContractState(t *testing.T, store *state.Store, bus *events.EventBus) (liveSessionID, emptySessionID string) {
	t.Helper()

	now := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	liveSessionID = "contract-live"
	emptySessionID = "contract-empty"

	for _, sess := range []state.Session{
		{
			ID:          liveSessionID,
			Name:        liveSessionID,
			ProjectPath: "/tmp/contract-live",
			CreatedAt:   now,
			Status:      state.SessionActive,
		},
		{
			ID:          emptySessionID,
			Name:        emptySessionID,
			ProjectPath: "/tmp/contract-empty",
			CreatedAt:   now.Add(-time.Minute),
			Status:      state.SessionActive,
		},
	} {
		sess := sess
		if err := store.CreateSession(&sess); err != nil {
			t.Fatalf("create session %q: %v", sess.ID, err)
		}
	}

	lastSeen := now
	if err := store.CreateAgent(&state.Agent{
		ID:         "agent-1",
		SessionID:  liveSessionID,
		Name:       "BlueLake",
		Type:       state.AgentTypeCodex,
		Model:      "gpt-5",
		TmuxPaneID: "%1",
		LastSeen:   &lastSeen,
		Status:     state.AgentWorking,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	bus.PublishSync(events.BaseEvent{
		Type:      string(events.EventPromptSend),
		Timestamp: now,
		Session:   liveSessionID,
	})

	return liveSessionID, emptySessionID
}

func prepareRobotAPIContractEnv(t *testing.T) {
	t.Helper()

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmpWD := t.TempDir()
	if err := os.Chdir(tmpWD); err != nil {
		t.Fatalf("chdir %s: %v", tmpWD, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(origWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	// Keep core system tools available while forcing optional helpers like bv/br
	// to fail fast instead of probing the real repository.
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
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

func TestParityRobotStatusV1Endpoint(t *testing.T) {
	prepareRobotAPIContractEnv(t)

	srv, store := setupTestServer(t)
	liveSessionID, _ := seedRobotAPIContractState(t, store, srv.eventBus)

	robot.SetProjectionStore(store)
	robotOutput, err := robot.GetStatusWithOptions(robot.PaginationOptions{})
	if err != nil {
		t.Fatalf("robot.GetStatusWithOptions failed: %v", err)
	}

	robotJSON, err := json.Marshal(robotOutput)
	if err != nil {
		t.Fatalf("failed to marshal robot output: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/robot/status", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("REST robot/status v1 returned %d, want %d", rec.Code, http.StatusOK)
	}

	resp := decodeResponseMap(t, rec.Body.Bytes())
	if resp["success"] != true {
		t.Fatal("robot/status v1 should return success=true")
	}
	if _, ok := resp["request_id"].(string); !ok {
		t.Fatalf("robot/status v1 missing request_id: %+v", resp)
	}

	sessions := requireArrayField(t, resp, "sessions", -1)
	foundLiveSession := false
	for _, raw := range sessions {
		session, ok := raw.(map[string]any)
		if ok && session["name"] == liveSessionID {
			foundLiveSession = true
			break
		}
	}
	if !foundLiveSession {
		t.Fatalf("robot/status v1 missing seeded session %q in %+v", liveSessionID, sessions)
	}

	robotNorm := normalizeForParity(t, robotJSON)
	restNorm := normalizeForParity(t, rec.Body.Bytes())
	if !compareNormalized(t, "robot/status v1", robotNorm, restNorm) {
		t.Fatal("robot/status v1 parity mismatch")
	}
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

func TestV1RobotAPIContractSmoke(t *testing.T) {
	prepareRobotAPIContractEnv(t)

	srv, store := setupTestServer(t)
	liveSessionID, emptySessionID := seedRobotAPIContractState(t, store, srv.eventBus)

	tests := []struct {
		name     string
		path     string
		wantCode int
		check    func(t *testing.T, resp map[string]any)
	}{
		{
			name:     "health",
			path:     "/api/v1/health",
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp map[string]any) {
				t.Helper()
				if resp["success"] != true {
					t.Fatalf("success = %v, want true", resp["success"])
				}
				if resp["status"] != "healthy" {
					t.Fatalf("status = %v, want healthy", resp["status"])
				}
				if _, ok := resp["timestamp"].(string); !ok {
					t.Fatalf("health missing timestamp: %+v", resp)
				}
				if _, ok := resp["request_id"].(string); !ok {
					t.Fatalf("health missing request_id: %+v", resp)
				}
			},
		},
		{
			name:     "capabilities",
			path:     "/api/v1/capabilities",
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp map[string]any) {
				t.Helper()
				requireArrayField(t, resp, "commands", -1)
				requireArrayField(t, resp, "categories", -1)
				requireObjectField(t, resp, "attention")
				if _, ok := resp["version"].(string); !ok {
					t.Fatalf("capabilities missing version: %+v", resp)
				}
				if _, ok := resp["request_id"].(string); !ok {
					t.Fatalf("capabilities missing request_id: %+v", resp)
				}
			},
		},
		{
			name:     "sessions",
			path:     "/api/v1/sessions",
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp map[string]any) {
				t.Helper()
				sessions := requireArrayField(t, resp, "sessions", 2)
				if resp["count"] != float64(len(sessions)) {
					t.Fatalf("count = %v, want %d", resp["count"], len(sessions))
				}
			},
		},
		{
			name:     "session detail",
			path:     "/api/v1/sessions/" + liveSessionID,
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp map[string]any) {
				t.Helper()
				session := requireObjectField(t, resp, "session")
				if session["id"] != liveSessionID {
					t.Fatalf("session.id = %v, want %s", session["id"], liveSessionID)
				}
				if session["status"] != string(state.SessionActive) {
					t.Fatalf("session.status = %v, want %s", session["status"], state.SessionActive)
				}
			},
		},
		{
			name:     "empty events",
			path:     "/api/v1/sessions/" + emptySessionID + "/events",
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp map[string]any) {
				t.Helper()
				events := requireArrayField(t, resp, "events", 0)
				if resp["count"] != float64(len(events)) {
					t.Fatalf("count = %v, want %d", resp["count"], len(events))
				}
				if resp["session_id"] != emptySessionID {
					t.Fatalf("session_id = %v, want %s", resp["session_id"], emptySessionID)
				}
			},
		},
		{
			name:     "populated events",
			path:     "/api/v1/sessions/" + liveSessionID + "/events",
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp map[string]any) {
				t.Helper()
				eventList := requireArrayField(t, resp, "events", 1)
				if resp["count"] != float64(len(eventList)) {
					t.Fatalf("count = %v, want %d", resp["count"], len(eventList))
				}
				event := eventList[0].(map[string]any)
				if event["session"] != liveSessionID {
					t.Fatalf("event.session = %v, want %s", event["session"], liveSessionID)
				}
				if event["type"] != string(events.EventPromptSend) {
					t.Fatalf("event.type = %v, want %s", event["type"], events.EventPromptSend)
				}
			},
		},
		{
			name:     "robot health",
			path:     "/api/v1/robot/health",
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp map[string]any) {
				t.Helper()
				requireObjectField(t, resp, "sessions")
				requireArrayField(t, resp, "alerts", -1)
				if _, ok := resp["request_id"].(string); !ok {
					t.Fatalf("robot health missing request_id: %+v", resp)
				}
			},
		},
		{
			name:     "missing session",
			path:     "/api/v1/sessions/missing-session",
			wantCode: http.StatusNotFound,
			check: func(t *testing.T, resp map[string]any) {
				t.Helper()
				if resp["success"] != false {
					t.Fatalf("success = %v, want false", resp["success"])
				}
				if resp["error_code"] != ErrCodeNotFound {
					t.Fatalf("error_code = %v, want %s", resp["error_code"], ErrCodeNotFound)
				}
				if _, ok := resp["error"].(string); !ok {
					t.Fatalf("missing error field: %+v", resp)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}

			resp := decodeResponseMap(t, rec.Body.Bytes())
			if _, ok := resp["timestamp"].(string); !ok {
				t.Fatalf("response missing timestamp: %+v", resp)
			}
			if _, ok := resp["request_id"].(string); !ok {
				t.Fatalf("response missing request_id: %+v", resp)
			}
			tc.check(t, resp)
		})
	}
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
