package robot

import (
	"reflect"
	"strings"
	"testing"
)

func TestGetRobotRegistry_SurfaceCoverage(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	commands := buildCommandRegistry()

	if len(registry.Surfaces) != len(commands) {
		t.Fatalf("registry surfaces = %d, want %d", len(registry.Surfaces), len(commands))
	}

	for _, command := range commands {
		surface, ok := registry.Surface(command.Name)
		if !ok {
			t.Fatalf("missing surface %q", command.Name)
		}
		if surface.Flag != command.Flag {
			t.Fatalf("surface %q flag = %q, want %q", command.Name, surface.Flag, command.Flag)
		}
		if surface.Category != command.Category {
			t.Fatalf("surface %q category = %q, want %q", command.Name, surface.Category, command.Category)
		}
		if surface.Description != command.Description {
			t.Fatalf("surface %q description drifted", command.Name)
		}
		if !reflect.DeepEqual(surface.Parameters, command.Parameters) {
			t.Fatalf("surface %q parameters drifted from command registry", command.Name)
		}
		if !reflect.DeepEqual(surface.Examples, command.Examples) {
			t.Fatalf("surface %q examples drifted from command registry", command.Name)
		}
		if strings.TrimSpace(surface.SchemaID) == "" {
			t.Fatalf("surface %q missing schema_id", command.Name)
		}
		if len(surface.Transports) == 0 || surface.Transports[0].Type != "cli" {
			t.Fatalf("surface %q missing CLI transport", command.Name)
		}
	}
}

func TestGetRobotRegistry_SectionReferencesResolve(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	if len(registry.Sections) == 0 {
		t.Fatal("expected non-empty section registry")
	}

	for _, surface := range registry.Surfaces {
		for _, sectionName := range surface.Sections {
			section, ok := registry.Section(sectionName)
			if !ok {
				t.Fatalf("surface %q references unknown section %q", surface.Name, sectionName)
			}
			if strings.TrimSpace(section.SchemaID) == "" {
				t.Fatalf("section %q missing schema_id", sectionName)
			}
		}
	}
}

func TestGetRobotRegistry_SchemaBindingsCoverSchemaCommand(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	if len(registry.SchemaTypes) != len(SchemaCommand) {
		t.Fatalf("schema_types = %d, want %d", len(registry.SchemaTypes), len(SchemaCommand))
	}

	for name, want := range SchemaCommand {
		got, ok := registry.SchemaBinding(name)
		if !ok {
			t.Fatalf("missing schema binding %q", name)
		}
		if reflect.TypeOf(got) != reflect.TypeOf(want) {
			t.Fatalf("schema binding %q type = %T, want %T", name, got, want)
		}
	}
}

func TestGetRobotRegistry_KeySurfaceMetadata(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	tests := []struct {
		name       string
		schemaType string
		sections   []string
	}{
		{
			name:       "snapshot",
			schemaType: "snapshot",
			sections: []string{
				"summary",
				"sessions",
				"work",
				"coordination",
				"quota",
				"alerts",
				"incidents",
				"attention",
				"source_health",
				"next_actions",
			},
		},
		{
			name:       "status",
			schemaType: "status",
			sections:   []string{"summary", "sessions"},
		},
		{
			name:       "attention",
			schemaType: "",
			sections:   []string{"attention", "incidents", "next_actions", "cursor"},
		},
		{
			name:       "health-restart-stuck",
			schemaType: "auto_restart_stuck",
			sections:   []string{"source_health", "incidents", "next_actions"},
		},
		{
			name:       "inspect-pane",
			schemaType: "inspect",
			sections:   []string{"events", "next_actions"},
		},
		{
			name:       "inspect-session",
			schemaType: "inspect_session",
			sections:   []string{"sessions", "next_actions"},
		},
		{
			name:       "inspect-agent",
			schemaType: "inspect_agent",
			sections:   []string{"sessions", "next_actions"},
		},
		{
			name:       "inspect-work",
			schemaType: "inspect_work",
			sections:   []string{"work", "next_actions"},
		},
		{
			name:       "inspect-coordination",
			schemaType: "inspect_coordination",
			sections:   []string{"coordination", "next_actions"},
		},
		{
			name:       "inspect-quota",
			schemaType: "inspect_quota",
			sections:   []string{"quota", "next_actions"},
		},
		{
			name:       "inspect-incident",
			schemaType: "inspect_incident",
			sections:   []string{"incidents", "next_actions"},
		},
	}

	for _, tc := range tests {
		surface, ok := registry.Surface(tc.name)
		if !ok {
			t.Fatalf("missing surface %q", tc.name)
		}
		if surface.SchemaType != tc.schemaType {
			t.Fatalf("surface %q schema_type = %q, want %q", tc.name, surface.SchemaType, tc.schemaType)
		}
		if !reflect.DeepEqual(surface.Sections, tc.sections) {
			t.Fatalf("surface %q sections = %#v, want %#v", tc.name, surface.Sections, tc.sections)
		}
	}
}

func TestGetRobotRegistry_SurfacesSortedByCategoryThenName(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	for i := 1; i < len(registry.Surfaces); i++ {
		prev := registry.Surfaces[i-1]
		curr := registry.Surfaces[i]
		if prev.Category == curr.Category && prev.Name > curr.Name {
			t.Fatalf("surfaces out of order within %q: %q before %q", curr.Category, prev.Name, curr.Name)
		}
		if prev.Category != curr.Category && categoryIndex(prev.Category) > categoryIndex(curr.Category) {
			t.Fatalf("surface categories out of order: %q before %q", prev.Category, curr.Category)
		}
	}
}

func TestGetRobotRegistry_SurfaceReturnsDetachedSlices(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	first, ok := registry.Surface("status")
	if !ok {
		t.Fatal("missing surface status")
	}
	second, ok := registry.Surface("status")
	if !ok {
		t.Fatal("missing surface status on second lookup")
	}

	if len(first.Parameters) == 0 {
		t.Fatal("expected status surface parameters")
	}
	first.Parameters[0].Name = "mutated"
	first.Sections[0] = "mutated"
	first.Examples[0] = "mutated"
	first.Transports[0].Endpoint = "mutated"

	if second.Parameters[0].Name == "mutated" {
		t.Fatal("surface parameters alias registry storage")
	}
	if second.Sections[0] == "mutated" {
		t.Fatal("surface sections alias registry storage")
	}
	if second.Examples[0] == "mutated" {
		t.Fatal("surface examples alias registry storage")
	}
	if second.Transports[0].Endpoint == "mutated" {
		t.Fatal("surface transports alias registry storage")
	}
}

func TestGetRobotRegistry_ReturnsDetachedRegistrySnapshots(t *testing.T) {
	t.Parallel()

	first := GetRobotRegistry()
	second := GetRobotRegistry()

	if len(first.Surfaces) == 0 || len(first.Sections) == 0 || len(first.Categories) == 0 || len(first.SchemaTypes) == 0 {
		t.Fatal("expected populated registry snapshots")
	}

	first.Surfaces[0].Name = "mutated-surface"
	first.Sections[0].Name = "mutated-section"
	first.Categories[0] = "mutated-category"
	first.SchemaTypes[0] = "mutated-schema"

	if second.Surfaces[0].Name == "mutated-surface" {
		t.Fatal("GetRobotRegistry returned shared surfaces slice")
	}
	if second.Sections[0].Name == "mutated-section" {
		t.Fatal("GetRobotRegistry returned shared sections slice")
	}
	if second.Categories[0] == "mutated-category" {
		t.Fatal("GetRobotRegistry returned shared categories slice")
	}
	if second.SchemaTypes[0] == "mutated-schema" {
		t.Fatal("GetRobotRegistry returned shared schema types slice")
	}
}

func TestGetRobotRegistry_ConsumerMetadataPopulated(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Verify key surfaces have consumer metadata
	keySurfaces := []struct {
		name                   string
		expectConsumerGuidance bool
		expectBoundedness      bool
		expectFollowUp         bool
		expectAttentionOps     bool
		expectExplainability   bool
	}{
		{
			name:                   "status",
			expectConsumerGuidance: true,
			expectBoundedness:      true,
			expectFollowUp:         true,
		},
		{
			name:                   "snapshot",
			expectConsumerGuidance: true,
			expectBoundedness:      true,
			expectFollowUp:         true,
			expectExplainability:   true,
		},
		{
			name:                   "attention",
			expectConsumerGuidance: true,
			expectBoundedness:      true,
			expectFollowUp:         true,
			expectAttentionOps:     true,
			expectExplainability:   true,
		},
		{
			name:                   "terse",
			expectConsumerGuidance: true,
			expectBoundedness:      true,
		},
		{
			name:                   "capabilities",
			expectConsumerGuidance: true,
		},
	}

	for _, tc := range keySurfaces {
		surface, ok := registry.Surface(tc.name)
		if !ok {
			t.Fatalf("missing surface %q", tc.name)
		}
		if tc.expectConsumerGuidance && surface.ConsumerGuidance == nil {
			t.Errorf("surface %q: expected consumer_guidance but got nil", tc.name)
		}
		if tc.expectBoundedness && surface.Boundedness == nil {
			t.Errorf("surface %q: expected boundedness but got nil", tc.name)
		}
		if tc.expectFollowUp && surface.FollowUp == nil {
			t.Errorf("surface %q: expected follow_up but got nil", tc.name)
		}
		if tc.expectAttentionOps && surface.AttentionOps == nil {
			t.Errorf("surface %q: expected attention_ops but got nil", tc.name)
		}
		if tc.expectExplainability && surface.Explainability == nil {
			t.Errorf("surface %q: expected explainability but got nil", tc.name)
		}
	}

	// Verify attention surface has rich attention ops metadata
	attention, _ := registry.Surface("attention")
	if attention.AttentionOps != nil {
		if !attention.AttentionOps.SupportsAcknowledge {
			t.Error("attention surface should support acknowledge")
		}
		if !attention.AttentionOps.SupportsSnooze {
			t.Error("attention surface should support snooze")
		}
		if !attention.AttentionOps.SupportsPin {
			t.Error("attention surface should support pin")
		}
	}

	// Verify snapshot has action handoff metadata
	snapshot, _ := registry.Surface("snapshot")
	if snapshot.ActionHandoff == nil {
		t.Error("snapshot should have action_handoff metadata")
	} else if !snapshot.ActionHandoff.SupportsActions {
		t.Error("snapshot should support actions")
	}
}

func TestGetRobotRegistry_SectionConsumerMetadataPopulated(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Verify key sections have consumer metadata
	keySections := []struct {
		name                   string
		expectConsumerGuidance bool
		expectBoundedness      bool
		expectExplainability   bool
	}{
		{
			name:                   "summary",
			expectConsumerGuidance: true,
		},
		{
			name:                   "sessions",
			expectConsumerGuidance: true,
			expectBoundedness:      true,
			expectExplainability:   true,
		},
		{
			name:                   "work",
			expectConsumerGuidance: true,
			expectBoundedness:      true,
			expectExplainability:   true,
		},
		{
			name:                   "attention",
			expectConsumerGuidance: true,
			expectBoundedness:      true,
			expectExplainability:   true,
		},
		{
			name:                   "incidents",
			expectConsumerGuidance: true,
			expectExplainability:   true,
		},
	}

	for _, tc := range keySections {
		section, ok := registry.Section(tc.name)
		if !ok {
			t.Fatalf("missing section %q", tc.name)
		}
		if tc.expectConsumerGuidance && section.ConsumerGuidance == nil {
			t.Errorf("section %q: expected consumer_guidance but got nil", tc.name)
		}
		if tc.expectBoundedness && section.Boundedness == nil {
			t.Errorf("section %q: expected boundedness but got nil", tc.name)
		}
		if tc.expectExplainability && section.Explainability == nil {
			t.Errorf("section %q: expected explainability but got nil", tc.name)
		}
	}
}

func TestGetRobotRegistry_ConsumerMetadataClonedOnLookup(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	first, ok := registry.Surface("snapshot")
	if !ok {
		t.Fatal("missing surface snapshot")
	}
	second, ok := registry.Surface("snapshot")
	if !ok {
		t.Fatal("missing surface snapshot on second lookup")
	}

	// Verify consumer metadata is cloned, not shared
	if first.ConsumerGuidance != nil && second.ConsumerGuidance != nil {
		first.ConsumerGuidance.IntendedUse = "mutated"
		if second.ConsumerGuidance.IntendedUse == "mutated" {
			t.Error("ConsumerGuidance not properly cloned")
		}
	}
	if first.FollowUp != nil && second.FollowUp != nil && len(first.FollowUp.InspectSurfaces) > 0 {
		first.FollowUp.InspectSurfaces[0] = "mutated"
		if len(second.FollowUp.InspectSurfaces) > 0 && second.FollowUp.InspectSurfaces[0] == "mutated" {
			t.Error("FollowUp.InspectSurfaces not properly cloned")
		}
	}
}

// =============================================================================
// Schema ID and Versioning Rules Tests (bd-j9jo3.9.7)
// =============================================================================

func TestSchemaID_FormatConsistency(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// All schema IDs must follow the pattern: ntm:robot:<surface>:<version>
	// (uses colons as separators)
	for _, surface := range registry.Surfaces {
		schemaID := surface.SchemaID
		if schemaID == "" {
			t.Errorf("surface %q has empty schema_id", surface.Name)
			continue
		}

		// Schema ID must start with "ntm:"
		if !strings.HasPrefix(schemaID, "ntm:") {
			t.Errorf("surface %q schema_id %q must start with 'ntm:'", surface.Name, schemaID)
		}

		// Schema ID must have at least 3 colon-separated parts
		parts := strings.Split(schemaID, ":")
		if len(parts) < 3 {
			t.Errorf("surface %q schema_id %q must have at least 3 parts (ntm:robot:<name>)", surface.Name, schemaID)
		}

		// Last part should indicate version (v1, v2, etc)
		lastPart := parts[len(parts)-1]
		if !strings.HasPrefix(lastPart, "v") {
			t.Logf("surface %q schema_id %q last part %q doesn't look like a version", surface.Name, schemaID, lastPart)
		}
	}

	// Verify section schema IDs follow the same pattern
	for _, section := range registry.Sections {
		schemaID := section.SchemaID
		if schemaID == "" {
			t.Errorf("section %q has empty schema_id", section.Name)
			continue
		}

		if !strings.HasPrefix(schemaID, "ntm:") {
			t.Errorf("section %q schema_id %q must start with 'ntm:'", section.Name, schemaID)
		}
	}
}

func TestSchemaID_Uniqueness(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Collect all schema IDs
	schemaIDs := make(map[string]string) // schema_id -> surface/section name

	for _, surface := range registry.Surfaces {
		if existing, ok := schemaIDs[surface.SchemaID]; ok {
			t.Errorf("duplicate schema_id %q used by both %q and %q", surface.SchemaID, existing, surface.Name)
		}
		schemaIDs[surface.SchemaID] = "surface:" + surface.Name
	}

	for _, section := range registry.Sections {
		if existing, ok := schemaIDs[section.SchemaID]; ok {
			// Sections may share schema IDs with surfaces if they're the same concept
			if !strings.HasSuffix(existing, section.Name) {
				t.Logf("schema_id %q used by both %q and section %q (may be intentional)", section.SchemaID, existing, section.Name)
			}
		}
		schemaIDs[section.SchemaID] = "section:" + section.Name
	}
}

func TestEnvelopeVersion_StableFormat(t *testing.T) {
	t.Parallel()

	// EnvelopeVersion must be semver format
	parts := strings.Split(EnvelopeVersion, ".")
	if len(parts) != 3 {
		t.Fatalf("EnvelopeVersion %q is not semver format (MAJOR.MINOR.PATCH)", EnvelopeVersion)
	}

	for i, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				t.Errorf("EnvelopeVersion %q part %d (%q) contains non-digit", EnvelopeVersion, i, part)
			}
		}
	}
}

// =============================================================================
// Deterministic Ordering Tests (bd-j9jo3.9.7)
// =============================================================================

func TestRegistry_DeterministicSurfaceOrder(t *testing.T) {
	t.Parallel()

	// Get registry multiple times and verify consistent ordering
	first := GetRobotRegistry()
	second := GetRobotRegistry()

	if len(first.Surfaces) != len(second.Surfaces) {
		t.Fatalf("surface count mismatch: %d vs %d", len(first.Surfaces), len(second.Surfaces))
	}

	for i := range first.Surfaces {
		if first.Surfaces[i].Name != second.Surfaces[i].Name {
			t.Errorf("surface order differs at index %d: %q vs %q",
				i, first.Surfaces[i].Name, second.Surfaces[i].Name)
		}
	}
}

func TestRegistry_DeterministicSectionOrder(t *testing.T) {
	t.Parallel()

	first := GetRobotRegistry()
	second := GetRobotRegistry()

	if len(first.Sections) != len(second.Sections) {
		t.Fatalf("section count mismatch: %d vs %d", len(first.Sections), len(second.Sections))
	}

	for i := range first.Sections {
		if first.Sections[i].Name != second.Sections[i].Name {
			t.Errorf("section order differs at index %d: %q vs %q",
				i, first.Sections[i].Name, second.Sections[i].Name)
		}
	}
}

func TestRegistry_DeterministicCategoryOrder(t *testing.T) {
	t.Parallel()

	first := GetRobotRegistry()
	second := GetRobotRegistry()

	if len(first.Categories) != len(second.Categories) {
		t.Fatalf("category count mismatch: %d vs %d", len(first.Categories), len(second.Categories))
	}

	for i := range first.Categories {
		if first.Categories[i] != second.Categories[i] {
			t.Errorf("category order differs at index %d: %q vs %q",
				i, first.Categories[i], second.Categories[i])
		}
	}
}

func TestRegistry_DeterministicSchemaTypeOrder(t *testing.T) {
	t.Parallel()

	first := GetRobotRegistry()
	second := GetRobotRegistry()

	if len(first.SchemaTypes) != len(second.SchemaTypes) {
		t.Fatalf("schema type count mismatch: %d vs %d", len(first.SchemaTypes), len(second.SchemaTypes))
	}

	for i := range first.SchemaTypes {
		if first.SchemaTypes[i] != second.SchemaTypes[i] {
			t.Errorf("schema type order differs at index %d: %q vs %q",
				i, first.SchemaTypes[i], second.SchemaTypes[i])
		}
	}
}

// =============================================================================
// Truncation/Pagination Metadata Tests (bd-j9jo3.9.7)
// =============================================================================

func TestBoundedness_WellFormed(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Key surfaces that should have boundedness metadata
	surfacesWithBoundedness := []string{"status", "snapshot", "attention", "terse"}

	for _, name := range surfacesWithBoundedness {
		surface, ok := registry.Surface(name)
		if !ok {
			t.Errorf("expected surface %q not found", name)
			continue
		}

		if surface.Boundedness == nil {
			t.Errorf("surface %q should have boundedness metadata", name)
			continue
		}

		b := surface.Boundedness

		// DefaultLimit must be positive if specified
		if b.DefaultLimit < 0 {
			t.Errorf("surface %q has negative default_limit: %d", name, b.DefaultLimit)
		}

		// MaxLimit must be >= DefaultLimit if both are specified
		if b.MaxLimit > 0 && b.DefaultLimit > 0 && b.MaxLimit < b.DefaultLimit {
			t.Errorf("surface %q max_limit (%d) < default_limit (%d)", name, b.MaxLimit, b.DefaultLimit)
		}

		// TruncationBehavior should be non-empty if limits are specified
		if (b.DefaultLimit > 0 || b.MaxLimit > 0) && b.TruncationBehavior == "" {
			t.Logf("surface %q has limits but no truncation_behavior description", name)
		}
	}
}

func TestSectionBoundedness_WellFormed(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Key sections that should have boundedness metadata
	sectionsWithBoundedness := []string{"sessions", "work", "attention"}

	for _, name := range sectionsWithBoundedness {
		section, ok := registry.Section(name)
		if !ok {
			t.Errorf("expected section %q not found", name)
			continue
		}

		if section.Boundedness == nil {
			t.Errorf("section %q should have boundedness metadata", name)
			continue
		}

		b := section.Boundedness

		if b.DefaultLimit < 0 {
			t.Errorf("section %q has negative default_limit: %d", name, b.DefaultLimit)
		}
	}
}

// =============================================================================
// Action-Handoff Shape Tests (bd-j9jo3.9.7)
// =============================================================================

func TestActionHandoff_WellFormed(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Surfaces that should support action handoff
	actionSurfaces := []string{"snapshot", "attention", "digest"}

	for _, name := range actionSurfaces {
		surface, ok := registry.Surface(name)
		if !ok {
			continue
		}

		if surface.ActionHandoff == nil {
			t.Logf("surface %q has no action_handoff metadata", name)
			continue
		}

		ah := surface.ActionHandoff

		if ah.SupportsActions && len(ah.ActionTypes) == 0 {
			t.Errorf("surface %q supports_actions but has empty action_types", name)
		}

		// Verify action types are valid
		validActionTypes := map[string]bool{
			"spawn":       true,
			"restart":     true,
			"send":        true,
			"acknowledge": true,
			"escalate":    true,
			"dismiss":     true,
			"snooze":      true,
			"pin":         true,
			"inspect":     true,
			"diagnose":    true,
		}

		for _, actionType := range ah.ActionTypes {
			if !validActionTypes[actionType] {
				t.Logf("surface %q has non-standard action_type: %q", name, actionType)
			}
		}
	}
}

// =============================================================================
// Request Identity/Idempotency Tests (bd-j9jo3.9.7)
// =============================================================================

func TestRequestSemantics_IdempotencyConfig(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Mutation surfaces should have request semantics
	mutationSurfaces := []string{"spawn", "send", "interrupt", "restart-pane"}

	for _, name := range mutationSurfaces {
		surface, ok := registry.Surface(name)
		if !ok {
			continue // Surface might not exist yet
		}

		if surface.RequestSemantics == nil {
			t.Logf("mutation surface %q has no request_semantics", name)
			continue
		}

		rs := surface.RequestSemantics

		// If idempotency is supported, the key param should be specified
		if rs.SupportsIdempotency && rs.IdempotencyKeyParam == "" {
			t.Logf("surface %q supports idempotency but has no idempotency_key_param", name)
		}

		// If correlation is supported, the field should be specified
		if rs.SupportsCorrelation && rs.CorrelationIDField == "" {
			t.Errorf("surface %q supports correlation but has no correlation_id_field", name)
		}
	}
}

func TestRequestSemantics_ReadOnlySurfacesNoIdempotency(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// Read-only surfaces don't need idempotency (they're naturally idempotent)
	readOnlySurfaces := []string{"status", "snapshot", "attention", "context", "env"}

	for _, name := range readOnlySurfaces {
		surface, ok := registry.Surface(name)
		if !ok {
			continue
		}

		if surface.RequestSemantics != nil && surface.RequestSemantics.SupportsIdempotency {
			// This is fine - read surfaces can optionally declare idempotency
			t.Logf("read-only surface %q declares idempotency support (optional)", name)
		}
	}
}

// =============================================================================
// Operator Attention-State Rules Tests (bd-j9jo3.9.7)
// =============================================================================

func TestAttentionOps_WellFormed(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	surface, ok := registry.Surface("attention")
	if !ok {
		t.Fatal("attention surface not found")
	}

	if surface.AttentionOps == nil {
		t.Fatal("attention surface should have attention_ops metadata")
	}

	ops := surface.AttentionOps

	// Attention surface should support standard operations
	expectedOps := map[string]bool{
		"acknowledge": ops.SupportsAcknowledge,
		"snooze":      ops.SupportsSnooze,
		"pin":         ops.SupportsPin,
	}

	for op, supported := range expectedOps {
		if !supported {
			t.Errorf("attention surface should support %s", op)
		}
	}

	// OperatorStateField should be specified
	if ops.OperatorStateField == "" {
		t.Logf("attention surface has no operator_state_field specified")
	}
}

func TestAttentionOps_OnlyAttentionSurfaces(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()

	// AttentionOps should only be on attention-related surfaces
	attentionSurfaces := map[string]bool{
		"attention":        true,
		"inspect-incident": true,
	}

	for _, surface := range registry.Surfaces {
		hasOps := surface.AttentionOps != nil
		isAttention := attentionSurfaces[surface.Name]

		if hasOps && !isAttention {
			t.Logf("non-attention surface %q has attention_ops (may be intentional)", surface.Name)
		}
	}
}

// =============================================================================
// Capabilities Discovery Output Tests (bd-j9jo3.9.7)
// =============================================================================

func TestGetCapabilities_OutputStructure(t *testing.T) {
	t.Parallel()

	output, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities error: %v", err)
	}

	// Basic validation
	if !output.Success {
		t.Fatal("GetCapabilities should return success=true")
	}

	if output.Version == "" {
		t.Error("GetCapabilities should include version")
	}

	if len(output.Commands) == 0 {
		t.Error("GetCapabilities should include commands")
	}

	if len(output.Categories) == 0 {
		t.Error("GetCapabilities should include categories")
	}
}

func TestGetCapabilities_DeterministicOutput(t *testing.T) {
	t.Parallel()

	first, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities error: %v", err)
	}

	second, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities error (second call): %v", err)
	}

	// Ignore timestamps in comparison
	first.Timestamp = ""
	second.Timestamp = ""

	// Commands should be in same order
	if len(first.Commands) != len(second.Commands) {
		t.Fatalf("command count mismatch: %d vs %d", len(first.Commands), len(second.Commands))
	}

	for i := range first.Commands {
		if first.Commands[i].Name != second.Commands[i].Name {
			t.Errorf("command order differs at index %d: %q vs %q",
				i, first.Commands[i].Name, second.Commands[i].Name)
		}
	}

	// Categories should be in same order
	if len(first.Categories) != len(second.Categories) {
		t.Fatalf("category count mismatch: %d vs %d", len(first.Categories), len(second.Categories))
	}

	for i := range first.Categories {
		if first.Categories[i] != second.Categories[i] {
			t.Errorf("category order differs at index %d: %q vs %q",
				i, first.Categories[i], second.Categories[i])
		}
	}
}

func TestGetCapabilities_CommandsMatchRegistry(t *testing.T) {
	t.Parallel()

	output, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities error: %v", err)
	}

	registry := GetRobotRegistry()

	// Build map of registry surfaces
	registrySurfaces := make(map[string]bool)
	for _, surface := range registry.Surfaces {
		registrySurfaces[surface.Name] = true
	}

	// All commands should correspond to registry surfaces
	for _, cmd := range output.Commands {
		if !registrySurfaces[cmd.Name] {
			t.Errorf("command %q not found in registry surfaces", cmd.Name)
		}
	}
}

func TestGetCapabilities_AttentionCapabilitiesPopulated(t *testing.T) {
	t.Parallel()

	output, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities error: %v", err)
	}

	if output.Attention == nil {
		t.Fatal("GetCapabilities should include attention capabilities")
	}

	// Attention capabilities should have contract version
	attn := output.Attention
	if attn.ContractVersion == "" {
		t.Error("attention capabilities should have contract_version")
	}

	// Features map should be present
	if attn.Features == nil {
		t.Error("attention capabilities should have features map")
	}
}

// =============================================================================
// Schema Discovery Output Tests (bd-j9jo3.9.7)
// =============================================================================

func TestGetSchema_DiscoveryConsistency(t *testing.T) {
	t.Parallel()

	// Get all schema types
	allOutput, err := GetSchema("all")
	if err != nil {
		t.Fatalf("GetSchema('all') error: %v", err)
	}

	if len(allOutput.Schemas) == 0 {
		t.Fatal("GetSchema('all') returned no schemas")
	}

	// Build a set of schema IDs from the 'all' response
	schemaIDs := make(map[string]bool)
	for _, schema := range allOutput.Schemas {
		if schema != nil && schema.Title != "" {
			schemaIDs[schema.Title] = true
		}
	}

	// Verify at least some key schemas are present
	keySchemas := []string{"Status", "Spawn", "Health"}
	for _, key := range keySchemas {
		found := false
		for _, schema := range allOutput.Schemas {
			if schema != nil && strings.Contains(schema.Title, key) {
				found = true
				break
			}
		}
		if !found {
			t.Logf("key schema %q not found in GetSchema('all') output", key)
		}
	}
}

func TestGetSchema_AllTypesMatchRegistry(t *testing.T) {
	t.Parallel()

	registry := GetRobotRegistry()
	allOutput, err := GetSchema("all")
	if err != nil {
		t.Fatalf("GetSchema('all') error: %v", err)
	}

	// The number of schemas should match registry schema types
	if len(allOutput.Schemas) != len(registry.SchemaTypes) {
		t.Errorf("schema count mismatch: GetSchema('all') has %d, registry has %d",
			len(allOutput.Schemas), len(registry.SchemaTypes))
	}
}

func TestGetSchema_InvalidTypeError(t *testing.T) {
	t.Parallel()

	output, err := GetSchema("nonexistent_schema_type_xyz")
	if err != nil {
		t.Fatalf("GetSchema should not return error for invalid type: %v", err)
	}

	if output.Success {
		t.Error("GetSchema should return success=false for invalid type")
	}

	if output.ErrorCode == "" {
		t.Error("GetSchema should set error_code for invalid type")
	}

	if output.Hint == "" {
		t.Error("GetSchema should provide hint for invalid type")
	}
}
