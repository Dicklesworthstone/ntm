package robot

import (
	"strings"
	"testing"
)

// =============================================================================
// categoryIndex tests
// =============================================================================

func TestCategoryIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cat  string
		want int
	}{
		{"state", "state", 0},
		{"attention", "attention", 1},
		{"ensemble", "ensemble", 2},
		{"control", "control", 3},
		{"spawn", "spawn", 4},
		{"beads", "beads", 5},
		{"bv", "bv", 6},
		{"cass", "cass", 7},
		{"pipeline", "pipeline", 8},
		{"utility", "utility", 9},
		{"unknown category", "nonexistent", len(categoryOrder)},
		{"empty string", "", len(categoryOrder)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := categoryIndex(tc.cat)
			if got != tc.want {
				t.Errorf("categoryIndex(%q) = %d, want %d", tc.cat, got, tc.want)
			}
		})
	}
}

// =============================================================================
// buildCommandRegistry tests
// =============================================================================

func TestBuildCommandRegistry(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()

	if len(commands) == 0 {
		t.Fatal("buildCommandRegistry() returned empty slice")
	}

	// Verify all commands have required fields
	for i, cmd := range commands {
		if cmd.Name == "" {
			t.Errorf("command[%d] has empty Name", i)
		}
		if cmd.Flag == "" {
			t.Errorf("command[%d] (%s) has empty Flag", i, cmd.Name)
		}
		if cmd.Category == "" {
			t.Errorf("command[%d] (%s) has empty Category", i, cmd.Name)
		}
		if cmd.Description == "" {
			t.Errorf("command[%d] (%s) has empty Description", i, cmd.Name)
		}
	}
}

func TestBuildCommandRegistryUniqueNames(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	seen := make(map[string]bool)

	for _, cmd := range commands {
		if seen[cmd.Name] {
			t.Errorf("duplicate command name: %q", cmd.Name)
		}
		seen[cmd.Name] = true
	}
}

func TestBuildCommandRegistryUniqueFlags(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	seen := make(map[string]bool)

	for _, cmd := range commands {
		if seen[cmd.Flag] {
			t.Errorf("duplicate command flag: %q", cmd.Flag)
		}
		seen[cmd.Flag] = true
	}
}

func TestBuildCommandRegistryValidCategories(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	validCategories := make(map[string]bool)
	for _, cat := range categoryOrder {
		validCategories[cat] = true
	}

	for _, cmd := range commands {
		if !validCategories[cmd.Category] {
			t.Errorf("command %q has invalid category %q", cmd.Name, cmd.Category)
		}
	}
}

func TestBuildCommandRegistryExamples(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()

	for _, cmd := range commands {
		if len(cmd.Examples) == 0 {
			t.Errorf("command %q has no examples", cmd.Name)
		}
	}
}

func TestBuildCommandRegistryParameterFields(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()

	for _, cmd := range commands {
		for j, param := range cmd.Parameters {
			if param.Name == "" {
				t.Errorf("command %q param[%d] has empty Name", cmd.Name, j)
			}
			if param.Flag == "" {
				t.Errorf("command %q param[%d] has empty Flag", cmd.Name, j)
			}
			if param.Type == "" {
				t.Errorf("command %q param[%d] has empty Type", cmd.Name, j)
			}
			if param.Description == "" {
				t.Errorf("command %q param[%d] has empty Description", cmd.Name, j)
			}
		}
	}
}

func TestBuildCommandRegistry_IncludesAttentionContractCommands(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	byFlag := make(map[string]RobotCommandInfo, len(commands))
	for _, cmd := range commands {
		byFlag[cmd.Flag] = cmd
	}

	expectedCategories := map[string]string{
		"--robot-snapshot":  "state",
		"--robot-events":    "attention",
		"--robot-digest":    "attention",
		"--robot-wait":      "control",
		"--robot-attention": "attention",
	}

	for _, want := range AttentionCommands {
		cmd, ok := byFlag[want.Name]
		if !ok {
			t.Fatalf("attention contract command %q missing from command registry", want.Name)
		}
		if got, ok := expectedCategories[want.Name]; ok && cmd.Category != got {
			t.Fatalf("command %q category = %q, want %q", want.Name, cmd.Category, got)
		}
		if strings.TrimSpace(cmd.Description) == "" {
			t.Fatalf("command %q should have a registry description", want.Name)
		}
		if len(cmd.Examples) == 0 {
			t.Fatalf("command %q should include at least one example", want.Name)
		}
	}
}

func TestBuildCommandRegistry_IncludesRobotOverlay(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	for _, cmd := range commands {
		if cmd.Flag != "--robot-overlay" {
			continue
		}
		if cmd.Category != "control" {
			t.Fatalf("overlay category = %q, want control", cmd.Category)
		}
		if len(cmd.Parameters) != 3 {
			t.Fatalf("overlay parameter count = %d, want 3", len(cmd.Parameters))
		}
		if strings.TrimSpace(cmd.Description) == "" {
			t.Fatal("overlay description should not be empty")
		}
		if len(cmd.Examples) == 0 {
			t.Fatal("overlay examples should not be empty")
		}
		return
	}

	t.Fatal("missing --robot-overlay in command registry")
}

// =============================================================================
// GetCapabilities tests
// =============================================================================

func TestGetCapabilities(t *testing.T) {
	t.Parallel()

	output, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities() error: %v", err)
	}
	if output == nil {
		t.Fatal("GetCapabilities() returned nil")
	}

	if len(output.Commands) == 0 {
		t.Error("expected non-empty Commands")
	}
	if len(output.Categories) != len(categoryOrder) {
		t.Errorf("Categories length = %d, want %d", len(output.Categories), len(categoryOrder))
	}
	if output.Attention == nil {
		t.Fatal("expected attention capabilities")
	}
	if output.Attention.ContractVersion != AttentionContractVersion {
		t.Errorf("Attention.ContractVersion = %q, want %q", output.Attention.ContractVersion, AttentionContractVersion)
	}
	beadOrphaned, ok := output.Attention.SignalAvailability[AttentionSignalBeadOrphaned]
	if !ok {
		t.Fatalf("expected %q signal availability entry", AttentionSignalBeadOrphaned)
	}
	if beadOrphaned.Status != CapabilityUnavailable {
		t.Errorf("bead_orphaned status = %q, want %q", beadOrphaned.Status, CapabilityUnavailable)
	}
}

func TestGetCapabilitiesSortOrder(t *testing.T) {
	t.Parallel()

	output, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities() error: %v", err)
	}

	// Verify commands are sorted by category then name
	for i := 1; i < len(output.Commands); i++ {
		prev := output.Commands[i-1]
		curr := output.Commands[i]

		prevIdx := categoryIndex(prev.Category)
		currIdx := categoryIndex(curr.Category)

		if prevIdx > currIdx {
			t.Errorf("commands not sorted by category: %q (%s) before %q (%s)",
				prev.Name, prev.Category, curr.Name, curr.Category)
		}
		if prevIdx == currIdx && prev.Name > curr.Name {
			t.Errorf("commands not sorted by name within category %q: %q before %q",
				prev.Category, prev.Name, curr.Name)
		}
	}
}

func TestDefaultAttentionCapabilities_BeadOrphanedUnsupported(t *testing.T) {
	t.Parallel()

	caps := DefaultAttentionCapabilities()
	if caps == nil {
		t.Fatal("DefaultAttentionCapabilities() returned nil")
	}
	if caps.ContractVersion != AttentionContractVersion {
		t.Errorf("ContractVersion = %q, want %q", caps.ContractVersion, AttentionContractVersion)
	}
	signal, ok := caps.SignalAvailability[AttentionSignalBeadOrphaned]
	if !ok {
		t.Fatalf("expected %q in SignalAvailability", AttentionSignalBeadOrphaned)
	}
	if signal.Status != CapabilityUnavailable {
		t.Errorf("Status = %q, want %q", signal.Status, CapabilityUnavailable)
	}
	if signal.Note == "" {
		t.Error("expected unsupported note for bead_orphaned")
	}
}

func TestDefaultAttentionCapabilities_ProfilesDiscoverable(t *testing.T) {
	t.Parallel()

	caps := DefaultAttentionCapabilities()
	if caps == nil {
		t.Fatal("DefaultAttentionCapabilities() returned nil")
	}
	if caps.DefaultProfile != DefaultProfile {
		t.Fatalf("DefaultProfile = %q, want %q", caps.DefaultProfile, DefaultProfile)
	}
	if len(caps.Profiles) == 0 {
		t.Fatal("expected discoverable profiles in capabilities output")
	}
	if _, ok := caps.Features["profile_presets"]; !ok {
		t.Fatal("expected profile_presets feature entry")
	}
	if caps.Features["profile_presets"].Status != CapabilityAvailable {
		t.Fatalf("profile_presets status = %q, want %q", caps.Features["profile_presets"].Status, CapabilityAvailable)
	}

	seen := map[string]bool{}
	for _, profile := range caps.Profiles {
		seen[profile.Name] = true
		if profile.Description == "" {
			t.Fatalf("profile %q should have a description", profile.Name)
		}
	}
	for _, required := range []string{"operator", "debug"} {
		if !seen[required] {
			t.Fatalf("expected %q profile in capabilities output, got %+v", required, caps.Profiles)
		}
	}
}

func TestDefaultAttentionCapabilities_OperatorBoundaryGuardrail(t *testing.T) {
	t.Parallel()

	caps := DefaultAttentionCapabilities()
	if caps == nil {
		t.Fatal("DefaultAttentionCapabilities() returned nil")
	}
	feature, ok := caps.Features["operator_boundary"]
	if !ok {
		t.Fatal("expected operator_boundary feature entry")
	}
	if feature.Status != CapabilityAvailable {
		t.Fatalf("operator_boundary status = %q, want %q", feature.Status, CapabilityAvailable)
	}
	for _, want := range []string{
		"sensing/actuation surface",
		"does not assign work",
		"Agent Mail",
	} {
		if !strings.Contains(feature.Note, want) {
			t.Fatalf("operator_boundary note %q missing %q", feature.Note, want)
		}
	}
}

func TestDefaultAttentionCapabilities_GuardrailsIncluded(t *testing.T) {
	t.Parallel()

	caps := DefaultAttentionCapabilities()
	if caps == nil {
		t.Fatal("DefaultAttentionCapabilities() returned nil")
	}
	if len(caps.Guardrails) == 0 {
		t.Fatal("expected Guardrails to be non-empty")
	}

	required := map[string]string{
		"nervous_system_not_planner":           "must not invent plans",
		"one_obvious_tending_loop":             "--robot-attention",
		"cursor_resync_is_explicit":            "--robot-snapshot",
		"unsupported_conditions_stay_explicit": "bead_orphaned",
	}
	for _, guardrail := range caps.Guardrails {
		wantSnippet, ok := required[guardrail.Name]
		if !ok {
			continue
		}
		if !strings.Contains(guardrail.Rule, wantSnippet) {
			t.Fatalf("guardrail %q rule = %q, want snippet %q", guardrail.Name, guardrail.Rule, wantSnippet)
		}
		delete(required, guardrail.Name)
	}
	for name := range required {
		t.Fatalf("missing guardrail %q", name)
	}
}

func TestGetProfile_NormalizesInput(t *testing.T) {
	t.Parallel()

	profile := GetProfile("  DEBUG ")
	if profile == nil {
		t.Fatal("expected case-insensitive trimmed profile lookup to succeed")
	}
	if profile.Name != "debug" {
		t.Fatalf("profile.Name = %q, want %q", profile.Name, "debug")
	}
}

func TestGetProfile_ReturnsDetachedCopy(t *testing.T) {
	t.Parallel()

	profile := GetProfile("alerts")
	if profile == nil {
		t.Fatal("expected alerts profile")
	}
	if len(profile.Filters.Categories) == 0 {
		t.Fatal("expected alerts profile to expose categories")
	}

	profile.Name = "mutated"
	profile.Filters.Categories[0] = EventCategorySystem

	builtin := GetProfile("alerts")
	if builtin == nil {
		t.Fatal("expected alerts profile on second lookup")
	}
	if builtin.Name != "alerts" {
		t.Fatalf("GetProfile returned shared state; Name = %q, want %q", builtin.Name, "alerts")
	}
	if builtin.Filters.Categories[0] != EventCategoryAlert {
		t.Fatalf("GetProfile returned shared nested slices; Categories[0] = %q, want %q", builtin.Filters.Categories[0], EventCategoryAlert)
	}
}

func TestListProfiles_ReturnsCopy(t *testing.T) {
	t.Parallel()

	profiles := ListProfiles()
	if len(profiles) == 0 {
		t.Fatal("expected non-empty profile list")
	}

	original := BuiltinProfiles[0].Name
	profiles[0].Name = "mutated"
	if BuiltinProfiles[0].Name != original {
		t.Fatalf("ListProfiles returned backing storage; BuiltinProfiles[0].Name = %q, want %q", BuiltinProfiles[0].Name, original)
	}
}

func TestListProfiles_DeepCopiesNestedFilters(t *testing.T) {
	t.Parallel()

	profiles := ListProfiles()
	var alerts *AttentionProfile
	for i := range profiles {
		if profiles[i].Name == "alerts" {
			alerts = &profiles[i]
			break
		}
	}
	if alerts == nil {
		t.Fatal("expected alerts profile in copied list")
	}
	if len(alerts.Filters.Categories) == 0 {
		t.Fatal("expected alerts profile categories")
	}

	alerts.Filters.Categories[0] = EventCategorySystem

	refetched := GetProfile("alerts")
	if refetched == nil {
		t.Fatal("expected alerts profile after mutation")
	}
	if refetched.Filters.Categories[0] != EventCategoryAlert {
		t.Fatalf("ListProfiles leaked nested filter slices; Categories[0] = %q, want %q", refetched.Filters.Categories[0], EventCategoryAlert)
	}
}

func TestResolveEffectiveFilters_ProfileAndExplicitOverrides(t *testing.T) {
	t.Parallel()

	filters := ResolveEffectiveFilters("operator", ProfileFilters{
		MinSeverity:      SeverityError,
		MinActionability: ActionabilityActionRequired,
	})

	if filters.SourceProfile != "operator" {
		t.Fatalf("SourceProfile = %q, want %q", filters.SourceProfile, "operator")
	}
	if filters.MinSeverity != SeverityError {
		t.Fatalf("MinSeverity = %q, want %q", filters.MinSeverity, SeverityError)
	}
	if filters.MinActionability != ActionabilityActionRequired {
		t.Fatalf("MinActionability = %q, want %q", filters.MinActionability, ActionabilityActionRequired)
	}
	if len(filters.ExplicitOverrides) != 2 {
		t.Fatalf("ExplicitOverrides = %v, want 2 explicit fields", filters.ExplicitOverrides)
	}
	if filters.ExplicitOverrides[0] != "min_severity" || filters.ExplicitOverrides[1] != "min_actionability" {
		t.Fatalf("ExplicitOverrides = %v, want min_severity,min_actionability", filters.ExplicitOverrides)
	}
}

func TestSeverityMeetsMinimum_CriticalRanksAboveError(t *testing.T) {
	t.Parallel()

	if !severityMeetsMinimum(SeverityCritical, SeverityError) {
		t.Fatal("critical severity should satisfy an error minimum")
	}
	if !severityMeetsMinimum(SeverityCritical, SeverityCritical) {
		t.Fatal("critical severity should satisfy a critical minimum")
	}
	if severityMeetsMinimum(SeverityWarning, SeverityCritical) {
		t.Fatal("warning severity should not satisfy a critical minimum")
	}
}

func TestDefaultAttentionCapabilities_UnsupportedConditionsIncluded(t *testing.T) {
	t.Parallel()

	caps := DefaultAttentionCapabilities()
	if caps == nil {
		t.Fatal("DefaultAttentionCapabilities() returned nil")
	}
	if len(caps.UnsupportedConditions) == 0 {
		t.Fatal("expected UnsupportedConditions to be non-empty")
	}

	// Verify bead_orphaned is listed with full rationale
	found := false
	for _, uc := range caps.UnsupportedConditions {
		if uc.Name == AttentionSignalBeadOrphaned {
			found = true
			if uc.Status != CapabilityUnavailable {
				t.Errorf("bead_orphaned Status = %q, want %q", uc.Status, CapabilityUnavailable)
			}
			if uc.Reason == "" {
				t.Error("bead_orphaned Reason must not be empty")
			}
			if len(uc.ObservablesAvailable) == 0 {
				t.Error("bead_orphaned ObservablesAvailable must list what ntm CAN see")
			}
			if uc.WhatWouldChange == "" {
				t.Error("bead_orphaned WhatWouldChange must explain prerequisites for support")
			}
		}
	}
	if !found {
		t.Errorf("expected %q in UnsupportedConditions", AttentionSignalBeadOrphaned)
	}
}

func TestGetCapabilities_IncludesAttentionGuardrails(t *testing.T) {
	t.Parallel()

	output, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities() error: %v", err)
	}
	if output.Attention == nil {
		t.Fatal("expected attention capabilities")
	}
	if len(output.Attention.Guardrails) == 0 {
		t.Fatal("expected attention guardrails in capabilities output")
	}
}

func TestUnsupportedConditions_ConsistentWithSignalAvailability(t *testing.T) {
	t.Parallel()

	caps := DefaultAttentionCapabilities()

	// Every unsupported condition must also appear in SignalAvailability
	// with CapabilityUnavailable status — the two surfaces must agree.
	for _, uc := range caps.UnsupportedConditions {
		signal, ok := caps.SignalAvailability[uc.Name]
		if !ok {
			t.Errorf("UnsupportedCondition %q not found in SignalAvailability", uc.Name)
			continue
		}
		if signal.Status != CapabilityUnavailable {
			t.Errorf("SignalAvailability[%q].Status = %q, but UnsupportedConditions marks it unavailable",
				uc.Name, signal.Status)
		}
	}
}

func TestUnsupportedConditions_NotInAllWaitConditions(t *testing.T) {
	t.Parallel()

	// Unsupported conditions must NEVER appear in AllWaitConditions
	waitSet := make(map[string]bool)
	for _, wc := range AllWaitConditions {
		waitSet[wc] = true
	}

	for _, uc := range UnsupportedConditions() {
		if waitSet[uc.Name] {
			t.Errorf("unsupported condition %q must not be in AllWaitConditions", uc.Name)
		}
	}
}

func TestIsUnsupportedWaitCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		condition string
		want      bool
	}{
		{AttentionSignalBeadOrphaned, true},
		{"bead_orphaned", true},        // explicit string match
		{"idle", false},                // valid condition
		{"complete", false},            // valid condition
		{"nonexistent_garbage", false}, // unknown condition
		{"bead_orphaned,idle", true},   // unsupported in composed condition
	}

	for _, tt := range tests {
		got := isUnsupportedWaitCondition(tt.condition)
		if got != tt.want {
			t.Errorf("isUnsupportedWaitCondition(%q) = %v, want %v", tt.condition, got, tt.want)
		}
	}
}

func TestBuildCommandRegistryWaitCommandReflectsExtendedConditions(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()

	var waitCmd *RobotCommandInfo
	for i := range commands {
		if commands[i].Name == "wait" {
			waitCmd = &commands[i]
			break
		}
	}
	if waitCmd == nil {
		t.Fatal("expected wait command in registry")
	}

	if !strings.Contains(waitCmd.Description, "attention-feed conditions") {
		t.Fatalf("wait description %q should mention attention-feed conditions", waitCmd.Description)
	}

	var untilParam *RobotParameter
	var transitionParam *RobotParameter
	for i := range waitCmd.Parameters {
		param := &waitCmd.Parameters[i]
		switch param.Flag {
		case "--wait-until":
			untilParam = param
		case "--wait-transition":
			transitionParam = param
		}
	}
	if untilParam == nil {
		t.Fatal("expected --wait-until parameter")
	}
	if transitionParam == nil {
		t.Fatal("expected --wait-transition parameter")
	}

	for _, cond := range []string{
		"stalled",
		"rate_limited",
		"attention",
		"action_required",
		"mail_pending",
		"mail_ack_required",
		"context_hot",
		"reservation_conflict",
		"file_conflict",
		"session_changed",
		"pane_changed",
		"bead_orphaned",
	} {
		if !strings.Contains(untilParam.Description, cond) {
			t.Errorf("--wait-until description missing %q: %q", cond, untilParam.Description)
		}
	}

	if !strings.Contains(transitionParam.Description, "pane-state conditions") {
		t.Errorf("--wait-transition description should clarify pane-state scope, got %q", transitionParam.Description)
	}

	foundAttentionExample := false
	for _, example := range waitCmd.Examples {
		if strings.Contains(example, "--wait-until=action_required") {
			foundAttentionExample = true
			break
		}
	}
	if !foundAttentionExample {
		t.Error("expected wait command examples to include an action_required attention wait")
	}
}

func TestDocsContentMentionsAttentionWait(t *testing.T) {
	t.Parallel()

	commands := getCommandsContent()
	if commands == nil {
		t.Fatal("getCommandsContent() returned nil")
	}

	foundAgentControl := false
	for _, section := range commands.Sections {
		if section.Heading != "Agent Control" {
			continue
		}
		foundAgentControl = true
		if !strings.Contains(section.Body, "pane state or attention-feed condition") {
			t.Fatalf("Agent Control docs should mention attention-feed wait conditions, got %q", section.Body)
		}
	}
	if !foundAgentControl {
		t.Fatal("Agent Control section not found")
	}

	examples := getExamplesContent()
	if examples == nil {
		t.Fatal("getExamplesContent() returned nil")
	}

	found := false
	for _, example := range examples.Examples {
		if example.Name != "wait_for_attention" {
			continue
		}
		found = true
		if !strings.Contains(example.Command, "--wait-until=action_required") {
			t.Errorf("wait_for_attention command = %q, want action_required wait", example.Command)
		}
		if !strings.Contains(example.Notes, "operator-relevant wakeup") {
			t.Errorf("wait_for_attention notes = %q, want operator wakeup guidance", example.Notes)
		}
	}
	if !found {
		t.Fatal("expected wait_for_attention example")
	}
}

func TestDocsContentMentionsOverlayHandoff(t *testing.T) {
	t.Parallel()

	commands := getCommandsContent()
	if commands == nil {
		t.Fatal("getCommandsContent() returned nil")
	}

	foundAgentControl := false
	for _, section := range commands.Sections {
		if section.Heading != "Agent Control" {
			continue
		}
		foundAgentControl = true
		if !strings.Contains(section.Body, "--robot-overlay") {
			t.Fatalf("Agent Control docs should mention --robot-overlay, got %q", section.Body)
		}
	}
	if !foundAgentControl {
		t.Fatal("Agent Control section not found")
	}

	examples := getExamplesContent()
	if examples == nil {
		t.Fatal("getExamplesContent() returned nil")
	}

	found := false
	for _, example := range examples.Examples {
		if example.Name != "handoff_to_human" {
			continue
		}
		found = true
		if !strings.Contains(example.Command, "--robot-overlay") {
			t.Errorf("handoff_to_human command = %q, want overlay invocation", example.Command)
		}
		if !strings.Contains(example.Command, "--overlay-no-wait") {
			t.Errorf("handoff_to_human command = %q, want explicit no-wait handoff", example.Command)
		}
		if !strings.Contains(example.Notes, "operator") {
			t.Errorf("handoff_to_human notes = %q, want operator guidance", example.Notes)
		}
	}
	if !found {
		t.Fatal("expected handoff_to_human example")
	}
}

// =============================================================================
// categoryOrder tests
// =============================================================================

func TestCategoryOrderCompleteness(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	usedCategories := make(map[string]bool)
	for _, cmd := range commands {
		usedCategories[cmd.Category] = true
	}

	for cat := range usedCategories {
		found := false
		for _, c := range categoryOrder {
			if c == cat {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("category %q used in commands but not in categoryOrder", cat)
		}
	}
}
