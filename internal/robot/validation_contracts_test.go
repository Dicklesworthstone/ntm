package robot

// validation_contracts_test.go implements unit tests for the contract layer.
//
// These tests verify schema ID/versioning rules, required/optional field semantics,
// deterministic ordering requirements, action-handoff shapes, request identity,
// operator attention-state rules, and error semantics.
//
// Bead: bd-j9jo3.9.7

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// =============================================================================
// Schema ID and Versioning Rules
// =============================================================================

func TestContract_SchemaIDFormat(t *testing.T) {
	t.Parallel()

	// Schema IDs must follow the format: ntm:robot:<surface>:v<n>
	schemaIDPattern := regexp.MustCompile(`^ntm:robot:[a-z][a-z0-9_-]*:v\d+$`)

	registry := GetRobotRegistry()
	for _, surface := range registry.Surfaces {
		if surface.SchemaID == "" {
			t.Errorf("surface %q has empty schema_id", surface.Name)
			continue
		}
		if !schemaIDPattern.MatchString(surface.SchemaID) {
			t.Errorf("surface %q schema_id %q does not match pattern %s",
				surface.Name, surface.SchemaID, schemaIDPattern.String())
		}
	}

	// Verify sections also have valid schema IDs
	// Section format is: ntm:section:<name>:v<n>
	sectionPattern := regexp.MustCompile(`^ntm:section:[a-z][a-z0-9_-]*:v\d+$`)
	for _, section := range registry.Sections {
		if section.SchemaID == "" {
			t.Errorf("section %q has empty schema_id", section.Name)
			continue
		}
		if !sectionPattern.MatchString(section.SchemaID) {
			t.Errorf("section %q schema_id %q does not match pattern %s",
				section.Name, section.SchemaID, sectionPattern.String())
		}
	}
}

func TestContract_AttentionContractVersion(t *testing.T) {
	t.Parallel()

	// Version must be semver format
	semverPattern := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	if !semverPattern.MatchString(AttentionContractVersion) {
		t.Errorf("AttentionContractVersion %q is not valid semver", AttentionContractVersion)
	}
}

// =============================================================================
// Event Category and Type Constants
// =============================================================================

func TestContract_EventCategories(t *testing.T) {
	t.Parallel()

	// All event categories must be non-empty lowercase strings
	categories := []EventCategory{
		EventCategorySession,
		EventCategoryPane,
		EventCategoryAgent,
		EventCategoryActuation,
		EventCategoryFile,
		EventCategoryMail,
		EventCategoryBead,
		EventCategorySystem,
		EventCategoryAlert,
		EventCategoryIncident,
		EventCategoryHealth,
	}

	seen := make(map[EventCategory]bool)
	for _, cat := range categories {
		if cat == "" {
			t.Error("empty event category constant")
		}
		if string(cat) != strings.ToLower(string(cat)) {
			t.Errorf("event category %q should be lowercase", cat)
		}
		if seen[cat] {
			t.Errorf("duplicate event category: %q", cat)
		}
		seen[cat] = true
	}
}

func TestContract_EventTypes(t *testing.T) {
	t.Parallel()

	// Event types must follow format: category.action
	eventTypePattern := regexp.MustCompile(`^[a-z]+\.[a-z_]+$`)

	eventTypes := []EventType{
		// Session
		EventTypeSessionCreated, EventTypeSessionDestroyed,
		EventTypeSessionAttached, EventTypeSessionDetached,
		// Pane
		EventTypePaneCreated, EventTypePaneDestroyed,
		EventTypePaneOutput, EventTypePaneResized,
		// Agent
		EventTypeAgentStarted, EventTypeAgentStopped,
		EventTypeAgentStateChange, EventTypeAgentError,
		EventTypeAgentHandoff, EventTypeAgentStalled,
		EventTypeAgentRecovered, EventTypeAgentCompacted,
		EventTypeAgentIdle, EventTypeAgentPromptWait,
		// Actuation
		EventTypeActuationRequested, EventTypeActuationOutcome,
		EventTypeActuationVerified,
		// File
		EventTypeFileChanged, EventTypeFileConflict,
		EventTypeFileReserved, EventTypeFileReleased,
		// Mail
		EventTypeMailReceived, EventTypeMailAckRequired,
		EventTypeMailAcknowledged, EventTypeMailUnread,
		// Bead
		EventTypeBeadCreated, EventTypeBeadUpdated,
		EventTypeBeadClosed, EventTypeBeadUnblocked,
		// System
		EventTypeSystemStartup, EventTypeSystemShutdown,
		EventTypeSystemHealthChange, EventTypeSystemCursorReset,
		EventTypeSpawn,
		// Alert
		EventTypeAlertAttentionRequired, EventTypeAlertWarning,
		EventTypeAlertInfo, EventTypeAlert, EventTypeHealthChange,
		// Incident
		EventTypeIncidentOpened, EventTypeIncidentPromoted,
		EventTypeIncidentRecurred, EventTypeIncidentResolved,
		EventTypeIncidentMuted,
	}

	seen := make(map[EventType]bool)
	for _, et := range eventTypes {
		if et == "" {
			t.Error("empty event type constant")
			continue
		}
		if !eventTypePattern.MatchString(string(et)) {
			t.Errorf("event type %q does not match pattern %s", et, eventTypePattern.String())
		}
		if seen[et] {
			t.Errorf("duplicate event type: %q", et)
		}
		seen[et] = true
	}
}

// =============================================================================
// Severity and Actionability Constants
// =============================================================================

func TestContract_SeverityValues(t *testing.T) {
	t.Parallel()

	severities := []Severity{
		SeverityDebug, SeverityInfo, SeverityWarning,
		SeverityCritical, SeverityError,
	}

	seen := make(map[Severity]bool)
	for _, sev := range severities {
		if sev == "" {
			t.Error("empty severity constant")
		}
		if string(sev) != strings.ToLower(string(sev)) {
			t.Errorf("severity %q should be lowercase", sev)
		}
		if seen[sev] {
			t.Errorf("duplicate severity: %q", sev)
		}
		seen[sev] = true
	}
}

func TestContract_ActionabilityValues(t *testing.T) {
	t.Parallel()

	actionabilities := []Actionability{
		ActionabilityBackground,
		ActionabilityInteresting,
		ActionabilityActionRequired,
	}

	seen := make(map[Actionability]bool)
	for _, act := range actionabilities {
		if act == "" {
			t.Error("empty actionability constant")
		}
		if string(act) != strings.ToLower(string(act)) {
			t.Errorf("actionability %q should be lowercase", act)
		}
		if seen[act] {
			t.Errorf("duplicate actionability: %q", act)
		}
		seen[act] = true
	}
}

// =============================================================================
// Error Code Stability
// =============================================================================

func TestContract_ErrorCodeValues(t *testing.T) {
	t.Parallel()

	// Error codes must be uppercase with underscores
	errCodePattern := regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

	errorCodes := []string{
		ErrCodeSessionNotFound, ErrCodePaneNotFound,
		ErrCodeInvalidFlag, ErrCodeTimeout,
		ErrCodeNotImplemented, ErrCodeDependencyMissing,
		ErrCodeInternalError, ErrCodeNotFound,
		ErrCodePermissionDenied, ErrCodeResourceBusy,
		ErrCodeSoftExitFailed, ErrCodeHardKillFailed,
		ErrCodeShellNotReturned, ErrCodeCCLaunchFailed,
		ErrCodeCCInitTimeout, ErrCodeBeadNotFound,
		ErrCodePromptSendFailed,
	}

	seen := make(map[string]bool)
	for _, code := range errorCodes {
		if code == "" {
			t.Error("empty error code constant")
			continue
		}
		if !errCodePattern.MatchString(code) {
			t.Errorf("error code %q does not match pattern %s", code, errCodePattern.String())
		}
		if seen[code] {
			t.Errorf("duplicate error code: %q", code)
		}
		seen[code] = true
	}
}

// =============================================================================
// RobotResponse Contract
// =============================================================================

func TestContract_RobotResponse_SuccessFields(t *testing.T) {
	t.Parallel()

	resp := NewRobotResponse(true)

	// Success response must have these fields set
	if !resp.Success {
		t.Error("Success should be true")
	}
	if resp.Timestamp == "" {
		t.Error("Timestamp should be set")
	}
	if resp.Version == "" {
		t.Error("Version should be set")
	}

	// Error fields should be empty
	if resp.Error != "" {
		t.Error("Error should be empty for success response")
	}
	if resp.ErrorCode != "" {
		t.Error("ErrorCode should be empty for success response")
	}
}

func TestContract_RobotResponse_ErrorFields(t *testing.T) {
	t.Parallel()

	resp := NewErrorResponse(
		testError("test error"),
		ErrCodeInternalError,
		"test hint",
	)

	// Error response must have these fields set
	if resp.Success {
		t.Error("Success should be false")
	}
	if resp.Error == "" {
		t.Error("Error should be set")
	}
	if resp.ErrorCode == "" {
		t.Error("ErrorCode should be set")
	}
	if resp.Hint == "" {
		t.Error("Hint should be set")
	}
	if resp.Timestamp == "" {
		t.Error("Timestamp should be set for error response")
	}
}

func TestContract_RobotResponse_JSONSerialization(t *testing.T) {
	t.Parallel()

	resp := NewRobotResponse(true)

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Required fields must be present in JSON
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp"}
	for _, field := range requiredFields {
		if _, ok := m[field]; !ok {
			t.Errorf("missing required JSON field: %s", field)
		}
	}
}

// testError is a simple error for testing
type testError string

func (e testError) Error() string { return string(e) }

// =============================================================================
// AttentionEvent Contract
// =============================================================================

func TestContract_AttentionEvent_RequiredFields(t *testing.T) {
	t.Parallel()

	event := AttentionEvent{
		Cursor:        100,
		Ts:            "2026-01-01T00:00:00Z",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Severity:      SeverityInfo,
		Actionability: ActionabilityBackground,
		Session:       "test-session",
		Pane:          1,
		Summary:       "Test event",
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Check required fields
	requiredFields := []string{"cursor", "ts", "category", "type", "severity", "actionability", "summary"}
	for _, field := range requiredFields {
		if _, ok := m[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

// =============================================================================
// Surface Metadata Contract
// =============================================================================

func TestContract_SurfaceDescriptor_RequiredFields(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	for _, surface := range registry.Surfaces {
		// Every surface must have these fields
		if surface.Name == "" {
			t.Errorf("surface missing name")
		}
		if surface.Flag == "" {
			t.Errorf("surface %q missing flag", surface.Name)
		}
		if surface.Category == "" {
			t.Errorf("surface %q missing category", surface.Name)
		}
		if surface.Description == "" {
			t.Errorf("surface %q missing description", surface.Name)
		}
		if surface.SchemaID == "" {
			t.Errorf("surface %q missing schema_id", surface.Name)
		}

		// Every surface must have at least CLI transport
		if len(surface.Transports) == 0 {
			t.Errorf("surface %q has no transports", surface.Name)
		}
		hasCLI := false
		for _, tr := range surface.Transports {
			if tr.Type == "cli" {
				hasCLI = true
				break
			}
		}
		if !hasCLI {
			t.Errorf("surface %q missing CLI transport", surface.Name)
		}
	}
}

func TestContract_SurfaceDescriptor_CategoryOrder(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Verify surfaces are sorted by category then name
	for i := 1; i < len(registry.Surfaces); i++ {
		prev := registry.Surfaces[i-1]
		curr := registry.Surfaces[i]

		prevIdx := categoryIndex(prev.Category)
		currIdx := categoryIndex(curr.Category)

		if prevIdx > currIdx {
			t.Errorf("surfaces out of category order: %q (%s) before %q (%s)",
				prev.Name, prev.Category, curr.Name, curr.Category)
		}
		if prevIdx == currIdx && prev.Name > curr.Name {
			t.Errorf("surfaces out of name order within %s: %q before %q",
				prev.Category, prev.Name, curr.Name)
		}
	}
}

// =============================================================================
// Pagination Contract
// =============================================================================

func TestContract_PaginationInfo_Fields(t *testing.T) {
	t.Parallel()

	info := &PaginationInfo{
		Total:   100,
		Offset:  0,
		Limit:   50,
		HasMore: true,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// All pagination fields should be present
	fields := []string{"total", "offset", "limit", "has_more"}
	for _, field := range fields {
		if _, ok := m[field]; !ok {
			t.Errorf("missing pagination field: %s", field)
		}
	}
}

// =============================================================================
// Action Hint Contract
// =============================================================================

func TestContract_NextAction_Structure(t *testing.T) {
	t.Parallel()

	action := NextAction{
		Action: "robot-send",
		Args:   "--session test-session --pane 1 --prompt 'continue'",
		Reason: "Agent is idle and waiting for input",
	}

	data, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Required fields: action and args
	if _, ok := m["action"]; !ok {
		t.Error("missing action field")
	}
	if _, ok := m["args"]; !ok {
		t.Error("missing args field")
	}

	// Action must be non-empty
	if action.Action == "" {
		t.Error("action should not be empty")
	}
	if action.Args == "" {
		t.Error("args should not be empty")
	}
}

func TestContract_NextAction_ReasonOptional(t *testing.T) {
	t.Parallel()

	// Reason is optional but recommended
	action := NextAction{
		Action: "robot-tail",
		Args:   "--session test-session --pane 1",
	}

	data, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Should serialize without reason
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// reason should be omitted when empty
	if _, ok := m["reason"]; ok {
		t.Error("reason should be omitted when empty")
	}
}

// =============================================================================
// Registry Metadata Consistency
// =============================================================================

func TestContract_Registry_CategoriesExist(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	if len(registry.Categories) == 0 {
		t.Fatal("registry has no categories")
	}

	// Categories should be unique
	seen := make(map[string]bool)
	for _, cat := range registry.Categories {
		if cat == "" {
			t.Error("empty category in registry")
		}
		if seen[cat] {
			t.Errorf("duplicate category: %q", cat)
		}
		seen[cat] = true
	}

	// All surface categories should be in the registry categories
	catSet := make(map[string]bool)
	for _, cat := range registry.Categories {
		catSet[cat] = true
	}
	for _, surface := range registry.Surfaces {
		if !catSet[surface.Category] {
			t.Errorf("surface %q has category %q not in registry.Categories",
				surface.Name, surface.Category)
		}
	}
}

func TestContract_Registry_SchemaTypesMatchSchemaCommand(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// SchemaTypes should cover all SchemaCommand entries
	typeSet := make(map[string]bool)
	for _, st := range registry.SchemaTypes {
		typeSet[st] = true
	}

	for name := range SchemaCommand {
		if !typeSet[name] {
			t.Errorf("SchemaCommand %q not in registry.SchemaTypes", name)
		}
	}

	// And vice versa
	for _, st := range registry.SchemaTypes {
		if _, ok := SchemaCommand[st]; !ok {
			t.Errorf("registry.SchemaTypes %q not in SchemaCommand", st)
		}
	}
}

func TestContract_Registry_SectionsMatchSurfaces(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// All sections referenced by surfaces should exist
	sectionSet := make(map[string]bool)
	for _, sec := range registry.Sections {
		sectionSet[sec.Name] = true
	}

	for _, surface := range registry.Surfaces {
		for _, secName := range surface.Sections {
			if !sectionSet[secName] {
				t.Errorf("surface %q references unknown section %q",
					surface.Name, secName)
			}
		}
	}
}

// =============================================================================
// Consumer Metadata Contract
// =============================================================================

func TestContract_CoreSurfaces_HaveConsumerMetadata(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Core surfaces should have consumer guidance
	coreSurfaces := []string{"snapshot", "status", "attention", "digest"}
	for _, name := range coreSurfaces {
		surface, ok := registry.Surface(name)
		if !ok {
			continue // Surface might not exist yet
		}
		if surface.ConsumerGuidance == nil {
			t.Logf("core surface %q missing consumer_guidance (recommended)", name)
		}
	}
}

func TestContract_InspectSurfaces_HaveFollowUp(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Inspect surfaces should have follow_up metadata
	inspectSurfaces := []string{
		"inspect-session", "inspect-agent", "inspect-work",
		"inspect-quota", "inspect-incident",
	}

	for _, name := range inspectSurfaces {
		surface, ok := registry.Surface(name)
		if !ok {
			continue
		}
		if surface.FollowUp == nil {
			t.Logf("inspect surface %q missing follow_up metadata (recommended)", name)
		}
	}
}

// =============================================================================
// Deterministic Ordering
// =============================================================================

func TestContract_SchemaTypesAreSorted(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	types := registry.SchemaTypes

	sorted := make([]string, len(types))
	copy(sorted, types)
	sort.Strings(sorted)

	for i := range types {
		if types[i] != sorted[i] {
			t.Errorf("schema_types not sorted: index %d has %q, want %q",
				i, types[i], sorted[i])
			break
		}
	}
}

func TestContract_CategoriesAreSorted(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	cats := registry.Categories

	// Categories should be in categoryOrder, not alphabetical
	for i := 1; i < len(cats); i++ {
		prevIdx := categoryIndex(cats[i-1])
		currIdx := categoryIndex(cats[i])
		if prevIdx > currIdx {
			t.Errorf("categories not in expected order: %q (idx %d) before %q (idx %d)",
				cats[i-1], prevIdx, cats[i], currIdx)
		}
	}
}
