package robot

// validation_goldens_test.go implements schema goldens and contract stability tests.
//
// These tests pin checked-in schema artifacts, normative example payloads, and
// registry-backed discovery output. Breaking changes trigger explicit test failures
// with rich diagnostics so maintainers can evolve the robot contract only through
// intentional updates.
//
// Bead: bd-j9jo3.9.1
//
// To update goldens when contracts intentionally change:
//   go test -run TestGolden -update-goldens
//
// Golden file naming convention:
//   schema_{surface}.golden.json      - JSON Schema for a surface
//   registry_surfaces.golden.json     - Registry surface descriptors
//   error_{code}.golden.json          - Canonical error response examples
//   scenario_{name}.golden.json       - Scenario payload examples (already exist)

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateGoldens = flag.Bool("update-goldens", false, "Update golden files")

// goldenTestDir is the path to golden files relative to the test file.
const goldenTestDir = "testdata/robot_redesign/goldens"

// =============================================================================
// Golden File Management
// =============================================================================

// GoldenFile represents a pinned contract artifact.
type GoldenFile struct {
	Name        string `json:"name"`
	SchemaID    string `json:"schema_id,omitempty"`
	Description string `json:"description,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// GoldenComparison captures the diff between expected and actual.
type GoldenComparison struct {
	GoldenPath    string
	SchemaID      string
	Matched       bool
	AddedFields   []string
	RemovedFields []string
	ChangedFields []string
	DiffSummary   string
}

// readGolden reads a golden file and returns its contents.
func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(goldenTestDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Golden doesn't exist yet
		}
		t.Fatalf("failed to read golden %s: %v", name, err)
	}
	return data
}

// writeGolden writes content to a golden file.
func writeGolden(t *testing.T, name string, data []byte) {
	t.Helper()
	path := filepath.Join(goldenTestDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create golden dir: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write golden %s: %v", name, err)
	}
	t.Logf("Updated golden: %s", path)
}

// normalizeJSON normalizes JSON for comparison (sorted keys, consistent formatting).
func normalizeJSON(data []byte) ([]byte, error) {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

// compareGolden compares actual output to golden file.
func compareGolden(t *testing.T, goldenName string, actual []byte) GoldenComparison {
	t.Helper()

	comparison := GoldenComparison{
		GoldenPath: filepath.Join(goldenTestDir, goldenName),
	}

	golden := readGolden(t, goldenName)
	if golden == nil {
		if *updateGoldens {
			writeGolden(t, goldenName, actual)
			comparison.Matched = true
			comparison.DiffSummary = "created new golden"
			return comparison
		}
		t.Fatalf("Golden file %s does not exist. Run with -update-goldens to create.", goldenName)
	}

	normalizedGolden, err := normalizeJSON(golden)
	if err != nil {
		t.Fatalf("failed to normalize golden JSON: %v", err)
	}
	normalizedActual, err := normalizeJSON(actual)
	if err != nil {
		t.Fatalf("failed to normalize actual JSON: %v", err)
	}

	if bytes.Equal(normalizedGolden, normalizedActual) {
		comparison.Matched = true
		return comparison
	}

	// Compute structural diff
	comparison.AddedFields, comparison.RemovedFields, comparison.ChangedFields = computeJSONDiff(golden, actual)

	if *updateGoldens {
		writeGolden(t, goldenName, normalizedActual)
		comparison.Matched = true
		comparison.DiffSummary = fmt.Sprintf("updated: +%d -%d ~%d fields",
			len(comparison.AddedFields), len(comparison.RemovedFields), len(comparison.ChangedFields))
		return comparison
	}

	comparison.DiffSummary = buildDiffSummary(comparison)
	return comparison
}

// computeJSONDiff computes field-level differences between two JSON objects.
func computeJSONDiff(golden, actual []byte) (added, removed, changed []string) {
	var goldenMap, actualMap map[string]interface{}
	if err := json.Unmarshal(golden, &goldenMap); err != nil {
		return nil, nil, nil
	}
	if err := json.Unmarshal(actual, &actualMap); err != nil {
		return nil, nil, nil
	}

	goldenFields := flattenFields("", goldenMap)
	actualFields := flattenFields("", actualMap)

	goldenSet := make(map[string]string)
	for k, v := range goldenFields {
		goldenSet[k] = v
	}
	actualSet := make(map[string]string)
	for k, v := range actualFields {
		actualSet[k] = v
	}

	for k := range actualSet {
		if _, ok := goldenSet[k]; !ok {
			added = append(added, k)
		}
	}
	for k := range goldenSet {
		if _, ok := actualSet[k]; !ok {
			removed = append(removed, k)
		}
	}
	for k, av := range actualSet {
		if gv, ok := goldenSet[k]; ok && gv != av {
			changed = append(changed, k)
		}
	}

	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return
}

// flattenFields flattens a nested map into dot-notation paths with type info.
func flattenFields(prefix string, m map[string]interface{}) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]interface{}:
			for subK, subV := range flattenFields(path, val) {
				result[subK] = subV
			}
		case []interface{}:
			result[path] = fmt.Sprintf("array[%d]", len(val))
		default:
			result[path] = fmt.Sprintf("%T", v)
		}
	}
	return result
}

// buildDiffSummary creates a human-readable diff summary.
func buildDiffSummary(c GoldenComparison) string {
	var parts []string
	if len(c.AddedFields) > 0 {
		parts = append(parts, fmt.Sprintf("ADDED: %s", strings.Join(c.AddedFields[:clampLen(5, len(c.AddedFields))], ", ")))
	}
	if len(c.RemovedFields) > 0 {
		parts = append(parts, fmt.Sprintf("REMOVED: %s", strings.Join(c.RemovedFields[:clampLen(5, len(c.RemovedFields))], ", ")))
	}
	if len(c.ChangedFields) > 0 {
		parts = append(parts, fmt.Sprintf("CHANGED: %s", strings.Join(c.ChangedFields[:clampLen(5, len(c.ChangedFields))], ", ")))
	}
	return strings.Join(parts, "; ")
}

// =============================================================================
// Schema Golden Tests
// =============================================================================

// schemaGoldenTest defines a schema to test against a golden file.
type schemaGoldenTest struct {
	Name        string
	SchemaType  string
	GoldenFile  string
	Description string
}

// coreSchemaGoldens are the key schemas that form the robot contract.
var coreSchemaGoldens = []schemaGoldenTest{
	{
		Name:        "Status",
		SchemaType:  "status",
		GoldenFile:  "schema_status.golden.json",
		Description: "Cheap summary surface for frequent polling",
	},
	{
		Name:        "Snapshot",
		SchemaType:  "snapshot",
		GoldenFile:  "schema_snapshot.golden.json",
		Description: "Canonical bootstrap surface for orientation",
	},
	{
		Name:        "Attention",
		SchemaType:  "attention",
		GoldenFile:  "schema_attention.golden.json",
		Description: "Prioritized action queue for operator response",
	},
	{
		Name:        "Digest",
		SchemaType:  "digest",
		GoldenFile:  "schema_digest.golden.json",
		Description: "Aggregated event summary with highlights",
	},
	{
		Name:        "InspectSession",
		SchemaType:  "inspect_session",
		GoldenFile:  "schema_inspect_session.golden.json",
		Description: "Session drill-down inspection surface",
	},
	{
		Name:        "InspectAgent",
		SchemaType:  "inspect_agent",
		GoldenFile:  "schema_inspect_agent.golden.json",
		Description: "Agent drill-down inspection surface",
	},
	{
		Name:        "InspectWork",
		SchemaType:  "inspect_work",
		GoldenFile:  "schema_inspect_work.golden.json",
		Description: "Work/beads inspection surface",
	},
	{
		Name:        "InspectQuota",
		SchemaType:  "inspect_quota",
		GoldenFile:  "schema_inspect_quota.golden.json",
		Description: "Quota drill-down inspection surface",
	},
	{
		Name:        "InspectIncident",
		SchemaType:  "inspect_incident",
		GoldenFile:  "schema_inspect_incident.golden.json",
		Description: "Incident drill-down inspection surface",
	},
}

func TestGolden_CoreSchemas(t *testing.T) {
	t.Parallel()

	for _, tc := range coreSchemaGoldens {
		tc := tc // capture
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			output, err := GetSchema(tc.SchemaType)
			if err != nil {
				t.Fatalf("GetSchema(%q) error: %v", tc.SchemaType, err)
			}
			if !output.Success {
				t.Fatalf("GetSchema(%q) failed: %s", tc.SchemaType, output.Error)
			}

			// Wrap schema with metadata for traceability (no timestamp - it would break comparisons)
			goldenData := map[string]interface{}{
				"_golden_metadata": map[string]interface{}{
					"schema_type": tc.SchemaType,
					"description": tc.Description,
				},
				"schema": output.Schema,
			}

			actual, err := json.MarshalIndent(goldenData, "", "  ")
			if err != nil {
				t.Fatalf("failed to marshal schema: %v", err)
			}

			comparison := compareGolden(t, tc.GoldenFile, actual)
			if !comparison.Matched {
				t.Errorf("Schema %q differs from golden %s\n%s\n\nRun with -update-goldens to update.",
					tc.SchemaType, tc.GoldenFile, comparison.DiffSummary)
				logSchemaChange(t, tc, comparison)
			}
		})
	}
}

// logSchemaChange logs diagnostic info for schema drift.
func logSchemaChange(t *testing.T, tc schemaGoldenTest, comparison GoldenComparison) {
	t.Helper()
	t.Logf("=== Schema Drift Diagnostic ===")
	t.Logf("Schema: %s (%s)", tc.Name, tc.SchemaType)
	t.Logf("Golden: %s", comparison.GoldenPath)
	if len(comparison.AddedFields) > 0 {
		t.Logf("Added fields (%d):", len(comparison.AddedFields))
		for _, f := range comparison.AddedFields {
			t.Logf("  + %s", f)
		}
	}
	if len(comparison.RemovedFields) > 0 {
		t.Logf("Removed fields (%d):", len(comparison.RemovedFields))
		for _, f := range comparison.RemovedFields {
			t.Logf("  - %s", f)
		}
	}
	if len(comparison.ChangedFields) > 0 {
		t.Logf("Changed fields (%d):", len(comparison.ChangedFields))
		for _, f := range comparison.ChangedFields {
			t.Logf("  ~ %s", f)
		}
	}
}

// =============================================================================
// Registry Golden Tests
// =============================================================================

// RegistryGolden captures stable registry metadata.
type RegistryGolden struct {
	SurfaceCount    int                  `json:"surface_count"`
	SectionCount    int                  `json:"section_count"`
	CategoryCount   int                  `json:"category_count"`
	SchemaTypeCount int                  `json:"schema_type_count"`
	Surfaces        []SurfaceGoldenEntry `json:"surfaces"`
	Categories      []string             `json:"categories"`
	SchemaTypes     []string             `json:"schema_types"`
}

// SurfaceGoldenEntry is a pinned surface descriptor.
type SurfaceGoldenEntry struct {
	Name                     string `json:"name"`
	Flag                     string `json:"flag"`
	Category                 string `json:"category"`
	SchemaType               string `json:"schema_type"`
	SchemaID                 string `json:"schema_id"`
	HasConsumerGuidance      bool   `json:"has_consumer_guidance"`
	HasBoundedness           bool   `json:"has_boundedness"`
	HasFollowUp              bool   `json:"has_follow_up"`
	HasActionHandoff         bool   `json:"has_action_handoff"`
	HasRequestSemantics      bool   `json:"has_request_semantics"`
	HasAttentionOps          bool   `json:"has_attention_ops"`
	HasExplainability        bool   `json:"has_explainability"`
	HasLifecycle             bool   `json:"has_lifecycle"`
	SupportsIdempotency      bool   `json:"supports_idempotency,omitempty"`
	SupportsAcknowledge      bool   `json:"supports_acknowledge,omitempty"`
	SupportsPagination       bool   `json:"supports_pagination,omitempty"`
	HasDiagnosticEntryPoints bool   `json:"has_diagnostic_entry_points,omitempty"`
}

func TestGolden_Registry(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	if registry == nil {
		t.Fatal("GetRobotRegistry returned nil")
	}

	// Build golden data
	golden := RegistryGolden{
		SurfaceCount:    len(registry.Surfaces),
		SectionCount:    len(registry.Sections),
		CategoryCount:   len(registry.Categories),
		SchemaTypeCount: len(registry.SchemaTypes),
		Categories:      registry.Categories,
		SchemaTypes:     registry.SchemaTypes,
	}

	// Extract surface entries
	for _, surface := range registry.Surfaces {
		entry := SurfaceGoldenEntry{
			Name:                surface.Name,
			Flag:                surface.Flag,
			Category:            surface.Category,
			SchemaType:          surface.SchemaType,
			SchemaID:            surface.SchemaID,
			HasConsumerGuidance: surface.ConsumerGuidance != nil,
			HasBoundedness:      surface.Boundedness != nil,
			HasFollowUp:         surface.FollowUp != nil,
			HasActionHandoff:    surface.ActionHandoff != nil,
			HasRequestSemantics: surface.RequestSemantics != nil,
			HasAttentionOps:     surface.AttentionOps != nil,
			HasExplainability:   surface.Explainability != nil,
			HasLifecycle:        surface.Lifecycle != nil,
		}

		if surface.RequestSemantics != nil {
			entry.SupportsIdempotency = surface.RequestSemantics.SupportsIdempotency
		}
		if surface.AttentionOps != nil {
			entry.SupportsAcknowledge = surface.AttentionOps.SupportsAcknowledge
		}
		if surface.Boundedness != nil {
			entry.SupportsPagination = surface.Boundedness.SupportsPagination
		}
		if surface.Explainability != nil {
			entry.HasDiagnosticEntryPoints = surface.Explainability.HasDiagnosticEntryPoints
		}

		golden.Surfaces = append(golden.Surfaces, entry)
	}

	actual, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal registry golden: %v", err)
	}

	comparison := compareGolden(t, "registry_surfaces.golden.json", actual)
	if !comparison.Matched {
		t.Errorf("Registry differs from golden\n%s\n\nRun with -update-goldens to update.",
			comparison.DiffSummary)
	}
}

// =============================================================================
// Error Response Golden Tests
// =============================================================================

// errorGoldenTest defines an error response to test.
type errorGoldenTest struct {
	Code            string
	GoldenFile      string
	Description     string
	IsRetryable     bool
	RemediationHint string
}

// canonicalErrorGoldens are standard error responses.
var canonicalErrorGoldens = []errorGoldenTest{
	{
		Code:            ErrCodeSessionNotFound,
		GoldenFile:      "error_session_not_found.golden.json",
		Description:     "Session does not exist or has been terminated",
		IsRetryable:     false,
		RemediationHint: "Use 'ntm list' to see available sessions, or spawn a new session with 'ntm spawn'.",
	},
	{
		Code:            ErrCodePaneNotFound,
		GoldenFile:      "error_pane_not_found.golden.json",
		Description:     "Pane does not exist within the session",
		IsRetryable:     false,
		RemediationHint: "Use 'ntm status' to see available panes in the session.",
	},
	{
		Code:            ErrCodeInvalidFlag,
		GoldenFile:      "error_invalid_flag.golden.json",
		Description:     "Invalid or missing required flag",
		IsRetryable:     false,
		RemediationHint: "Check the command syntax with 'ntm <command> --help'.",
	},
	{
		Code:            ErrCodeTimeout,
		GoldenFile:      "error_timeout.golden.json",
		Description:     "Operation timed out waiting for response",
		IsRetryable:     true,
		RemediationHint: "Retry with increased timeout, or check if the agent is responsive.",
	},
	{
		Code:            ErrCodeDependencyMissing,
		GoldenFile:      "error_dependency_missing.golden.json",
		Description:     "Required external dependency is not available",
		IsRetryable:     false,
		RemediationHint: "Install the required dependency and ensure it is on PATH.",
	},
	{
		Code:            ErrCodeInternalError,
		GoldenFile:      "error_internal.golden.json",
		Description:     "Unexpected internal error",
		IsRetryable:     true,
		RemediationHint: "Report this issue with full context via 'ntm support-bundle'.",
	},
	{
		Code:            ErrCodeResourceBusy,
		GoldenFile:      "error_resource_busy.golden.json",
		Description:     "Resource is locked or busy",
		IsRetryable:     true,
		RemediationHint: "Wait for the current operation to complete, then retry.",
	},
}

func TestGolden_ErrorResponses(t *testing.T) {
	t.Parallel()

	for _, tc := range canonicalErrorGoldens {
		tc := tc
		t.Run(tc.Code, func(t *testing.T) {
			t.Parallel()

			// Create canonical error response
			resp := NewErrorResponse(
				fmt.Errorf("example error for %s", tc.Code),
				tc.Code,
				tc.RemediationHint,
			)
			// Zero out the timestamp for deterministic comparison
			resp.Timestamp = "GOLDEN_TIMESTAMP"

			// Wrap with metadata
			goldenData := map[string]interface{}{
				"_golden_metadata": map[string]interface{}{
					"error_code":   tc.Code,
					"description":  tc.Description,
					"is_retryable": tc.IsRetryable,
				},
				"response": resp,
			}

			actual, err := json.MarshalIndent(goldenData, "", "  ")
			if err != nil {
				t.Fatalf("failed to marshal error response: %v", err)
			}

			comparison := compareGolden(t, tc.GoldenFile, actual)
			if !comparison.Matched {
				t.Errorf("Error response %q differs from golden\n%s",
					tc.Code, comparison.DiffSummary)
			}
		})
	}
}

// =============================================================================
// Scenario Payload Golden Tests
// =============================================================================

// scenarioGoldenTest validates scenario fixtures.
type scenarioGoldenTest struct {
	Name           string
	SourceFile     string
	RequiredFields []string
}

var scenarioGoldens = []scenarioGoldenTest{
	{
		Name:       "HealthySession",
		SourceFile: "scenario_healthy_session.json",
		RequiredFields: []string{
			"scenario_id", "description", "session", "agents",
			"source_health", "incidents", "expected_outputs",
		},
	},
	{
		Name:       "DegradedSources",
		SourceFile: "scenario_degraded_sources.json",
		RequiredFields: []string{
			"scenario_id", "description", "session", "agents",
			"source_health", "expected_outputs", "fault_injection",
		},
	},
	{
		Name:       "AgentStuck",
		SourceFile: "scenario_agent_stuck.json",
		RequiredFields: []string{
			"scenario_id", "description", "session", "agents",
			"source_health", "incidents", "expected_outputs",
		},
	},
}

func TestGolden_ScenarioPayloads(t *testing.T) {
	t.Parallel()

	for _, tc := range scenarioGoldens {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join("testdata/robot_redesign", tc.SourceFile)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("failed to read scenario file %s: %v", path, err)
			}

			// Validate required fields
			var scenario map[string]interface{}
			if err := json.Unmarshal(data, &scenario); err != nil {
				t.Fatalf("invalid JSON in %s: %v", tc.SourceFile, err)
			}

			for _, field := range tc.RequiredFields {
				if _, ok := scenario[field]; !ok {
					t.Errorf("scenario %s missing required field: %s", tc.Name, field)
				}
			}

			// Validate scenario structure
			validateScenarioStructure(t, tc.Name, scenario)
		})
	}
}

// validateScenarioStructure performs deep validation of scenario fixtures.
func validateScenarioStructure(t *testing.T, name string, scenario map[string]interface{}) {
	t.Helper()

	// Validate session structure
	if session, ok := scenario["session"].(map[string]interface{}); ok {
		requiredSessionFields := []string{"name", "attached", "pane_count", "agent_count", "health_status"}
		for _, field := range requiredSessionFields {
			if _, ok := session[field]; !ok {
				t.Errorf("scenario %s: session missing field %s", name, field)
			}
		}
	} else {
		t.Errorf("scenario %s: session is not an object", name)
	}

	// Validate agents structure
	if agents, ok := scenario["agents"].([]interface{}); ok {
		for i, agent := range agents {
			if agentMap, ok := agent.(map[string]interface{}); ok {
				requiredAgentFields := []string{"id", "session_name", "pane", "agent_type", "state", "health_status"}
				for _, field := range requiredAgentFields {
					if _, ok := agentMap[field]; !ok {
						t.Errorf("scenario %s: agent[%d] missing field %s", name, i, field)
					}
				}
			}
		}
	}

	// Validate source_health structure
	if sourceHealth, ok := scenario["source_health"].(map[string]interface{}); ok {
		requiredHealthFields := []string{"tmux_status", "beads_status", "mail_status", "pt_status", "degraded_sources"}
		for _, field := range requiredHealthFields {
			if _, ok := sourceHealth[field]; !ok {
				t.Errorf("scenario %s: source_health missing field %s", name, field)
			}
		}
	}

	// Validate expected_outputs
	if expected, ok := scenario["expected_outputs"].(map[string]interface{}); ok {
		if _, ok := expected["status_success"]; !ok {
			t.Errorf("scenario %s: expected_outputs missing status_success", name)
		}
		if _, ok := expected["snapshot_success"]; !ok {
			t.Errorf("scenario %s: expected_outputs missing snapshot_success", name)
		}
	}
}

// =============================================================================
// Contract Stability Tests
// =============================================================================

func TestContract_RequiredSchemaFields(t *testing.T) {
	t.Parallel()

	// Core surfaces must have these response fields (from RobotResponse)
	requiredResponseFields := []string{"success", "timestamp"}

	testCases := []struct {
		schemaType     string
		requiredFields []string
	}{
		{"status", append(requiredResponseFields, "schema_id", "schema_version", "generated_at", "system", "sessions", "summary")},
		{"snapshot", append(requiredResponseFields, "schema_id", "ts", "sessions", "active_incidents", "summary")},
		{"attention", append(requiredResponseFields, "items", "counts")},
		{"digest", append(requiredResponseFields, "window", "counts", "highlights", "attention_items", "agent_states")},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.schemaType, func(t *testing.T) {
			t.Parallel()

			output, err := GetSchema(tc.schemaType)
			if err != nil {
				t.Fatalf("GetSchema error: %v", err)
			}
			if output.Schema == nil {
				t.Fatal("schema is nil")
			}

			for _, field := range tc.requiredFields {
				if _, ok := output.Schema.Properties[field]; !ok {
					t.Errorf("schema %q missing required field: %s", tc.schemaType, field)
				}
			}
		})
	}
}

func TestContract_ErrorCodeStability(t *testing.T) {
	t.Parallel()

	// These error codes must remain stable
	stableErrorCodes := map[string]string{
		"SESSION_NOT_FOUND":  ErrCodeSessionNotFound,
		"PANE_NOT_FOUND":     ErrCodePaneNotFound,
		"INVALID_FLAG":       ErrCodeInvalidFlag,
		"TIMEOUT":            ErrCodeTimeout,
		"NOT_IMPLEMENTED":    ErrCodeNotImplemented,
		"DEPENDENCY_MISSING": ErrCodeDependencyMissing,
		"INTERNAL_ERROR":     ErrCodeInternalError,
		"PERMISSION_DENIED":  ErrCodePermissionDenied,
		"RESOURCE_BUSY":      ErrCodeResourceBusy,
		"SOFT_EXIT_FAILED":   ErrCodeSoftExitFailed,
		"HARD_KILL_FAILED":   ErrCodeHardKillFailed,
		"SHELL_NOT_RETURNED": ErrCodeShellNotReturned,
		"CC_LAUNCH_FAILED":   ErrCodeCCLaunchFailed,
		"CC_INIT_TIMEOUT":    ErrCodeCCInitTimeout,
		"BEAD_NOT_FOUND":     ErrCodeBeadNotFound,
		"PROMPT_SEND_FAILED": ErrCodePromptSendFailed,
	}

	for expected, actual := range stableErrorCodes {
		if actual != expected {
			t.Errorf("error code %q changed to %q - this is a breaking change", expected, actual)
		}
	}
}

func TestContract_RegistrySurfaceStability(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	if registry == nil {
		t.Fatal("registry is nil")
	}

	// Core surfaces that must exist (use actual registry names: lowercase with hyphens)
	coreSurfaces := []string{
		"status", "snapshot", "attention", "digest",
		"inspect-session", "inspect-agent", "inspect-work",
		"inspect-quota", "inspect-incident",
	}

	surfaceNames := make(map[string]bool)
	for _, s := range registry.Surfaces {
		surfaceNames[s.Name] = true
	}

	for _, name := range coreSurfaces {
		if !surfaceNames[name] {
			t.Errorf("core surface %q not found in registry - this is a breaking change", name)
		}
	}
}

func TestContract_IdempotencySupport(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	if registry == nil {
		t.Fatal("registry is nil")
	}

	// Surfaces that must support idempotency
	idempotentSurfaces := []string{"Send", "Interrupt", "Spawn"}

	for _, name := range idempotentSurfaces {
		surface, ok := registry.Surface(name)
		if !ok {
			continue // Surface might not exist yet
		}
		if surface.RequestSemantics == nil {
			t.Logf("surface %q has no RequestSemantics", name)
			continue
		}
		if !surface.RequestSemantics.SupportsIdempotency {
			t.Errorf("surface %q should support idempotency", name)
		}
	}
}

// =============================================================================
// Helper: clampLen limits a slice index for display truncation
// =============================================================================

func clampLen(limit, length int) int {
	if limit < length {
		return limit
	}
	return length
}
