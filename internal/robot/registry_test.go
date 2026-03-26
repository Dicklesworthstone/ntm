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
		name                    string
		expectConsumerGuidance  bool
		expectBoundedness       bool
		expectFollowUp          bool
		expectAttentionOps      bool
		expectExplainability    bool
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
