package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// send.go: SendTargets.String()
// =============================================================================

func TestSendTargets_String_Empty(t *testing.T) {
	var s SendTargets
	if got := s.String(); got != "" {
		t.Errorf("empty SendTargets.String() = %q, want %q", got, "")
	}
}

func TestSendTargets_String_SingleNoVariant(t *testing.T) {
	s := SendTargets{{Type: AgentTypeClaude}}
	if got := s.String(); got != "cc" {
		t.Errorf("SendTargets.String() = %q, want %q", got, "cc")
	}
}

func TestSendTargets_String_SingleWithVariant(t *testing.T) {
	s := SendTargets{{Type: AgentTypeClaude, Variant: "opus"}}
	if got := s.String(); got != "cc:opus" {
		t.Errorf("SendTargets.String() = %q, want %q", got, "cc:opus")
	}
}

func TestSendTargets_String_Multiple(t *testing.T) {
	s := SendTargets{
		{Type: AgentTypeClaude},
		{Type: AgentTypeCodex, Variant: "mini"},
		{Type: AgentTypeGemini},
	}
	want := "cc,cod:mini,gmi"
	if got := s.String(); got != want {
		t.Errorf("SendTargets.String() = %q, want %q", got, want)
	}
}

func TestSendTargets_String_NilReceiver(t *testing.T) {
	var s *SendTargets
	if got := s.String(); got != "" {
		t.Errorf("nil SendTargets.String() = %q, want %q", got, "")
	}
}

// =============================================================================
// send.go: SendTargets.Set()
// =============================================================================

func TestSendTargets_Set_NoVariant(t *testing.T) {
	var s SendTargets
	err := s.Set("cc")
	if err != nil {
		t.Fatalf("Set() unexpected error: %v", err)
	}
	if len(s) != 1 {
		t.Fatalf("expected 1 target, got %d", len(s))
	}
	// Note: Set only parses variants, Type is set by flag registration
	if s[0].Variant != "" {
		t.Errorf("variant should be empty, got %q", s[0].Variant)
	}
}

func TestSendTargets_Set_WithVariant(t *testing.T) {
	var s SendTargets
	err := s.Set("cc:opus")
	if err != nil {
		t.Fatalf("Set() unexpected error: %v", err)
	}
	if len(s) != 1 {
		t.Fatalf("expected 1 target, got %d", len(s))
	}
	if s[0].Variant != "opus" {
		t.Errorf("variant = %q, want %q", s[0].Variant, "opus")
	}
}

func TestSendTargets_Set_Multiple(t *testing.T) {
	var s SendTargets
	_ = s.Set("cc")
	_ = s.Set("cc:opus")
	if len(s) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(s))
	}
}

// =============================================================================
// send.go: SendTargets.HasTargetsForType()
// =============================================================================

func TestHasTargetsForType_Found(t *testing.T) {
	s := SendTargets{
		{Type: AgentTypeClaude},
		{Type: AgentTypeCodex},
	}
	if !s.HasTargetsForType(AgentTypeClaude) {
		t.Error("expected HasTargetsForType(cc) = true")
	}
}

func TestHasTargetsForType_NotFound(t *testing.T) {
	s := SendTargets{
		{Type: AgentTypeClaude},
	}
	if s.HasTargetsForType(AgentTypeGemini) {
		t.Error("expected HasTargetsForType(gmi) = false")
	}
}

func TestHasTargetsForType_Empty(t *testing.T) {
	var s SendTargets
	if s.HasTargetsForType(AgentTypeClaude) {
		t.Error("expected HasTargetsForType on empty = false")
	}
}

// =============================================================================
// send.go: SendTargets.MatchesPane() + matchesSendTarget()
// =============================================================================

func TestMatchesPane_TypeMatch(t *testing.T) {
	s := SendTargets{{Type: AgentTypeClaude}}
	pane := tmux.Pane{Index: 1, Type: tmux.AgentClaude}
	if !s.MatchesPane(pane) {
		t.Error("expected MatchesPane = true for matching type")
	}
}

func TestMatchesPane_TypeMismatch(t *testing.T) {
	s := SendTargets{{Type: AgentTypeClaude}}
	pane := tmux.Pane{Index: 1, Type: tmux.AgentCodex}
	if s.MatchesPane(pane) {
		t.Error("expected MatchesPane = false for mismatched type")
	}
}

func TestMatchesPane_VariantMatch(t *testing.T) {
	s := SendTargets{{Type: AgentTypeClaude, Variant: "opus"}}
	pane := tmux.Pane{Index: 1, Type: tmux.AgentClaude, Variant: "opus"}
	if !s.MatchesPane(pane) {
		t.Error("expected MatchesPane = true for matching type+variant")
	}
}

func TestMatchesPane_VariantMismatch(t *testing.T) {
	s := SendTargets{{Type: AgentTypeClaude, Variant: "opus"}}
	pane := tmux.Pane{Index: 1, Type: tmux.AgentClaude, Variant: "sonnet"}
	if s.MatchesPane(pane) {
		t.Error("expected MatchesPane = false for mismatched variant")
	}
}

func TestMatchesPane_NoVariantMatchesAll(t *testing.T) {
	s := SendTargets{{Type: AgentTypeClaude}}
	pane := tmux.Pane{Index: 1, Type: tmux.AgentClaude, Variant: "anything"}
	if !s.MatchesPane(pane) {
		t.Error("expected MatchesPane = true when target has no variant filter")
	}
}

func TestMatchesSendTarget_Direct(t *testing.T) {
	tests := []struct {
		name   string
		pane   tmux.Pane
		target SendTarget
		want   bool
	}{
		{
			"exact type match",
			tmux.Pane{Type: tmux.AgentClaude},
			SendTarget{Type: AgentTypeClaude},
			true,
		},
		{
			"type mismatch",
			tmux.Pane{Type: tmux.AgentCodex},
			SendTarget{Type: AgentTypeClaude},
			false,
		},
		{
			"type+variant match",
			tmux.Pane{Type: tmux.AgentClaude, Variant: "opus"},
			SendTarget{Type: AgentTypeClaude, Variant: "opus"},
			true,
		},
		{
			"variant mismatch",
			tmux.Pane{Type: tmux.AgentClaude, Variant: "haiku"},
			SendTarget{Type: AgentTypeClaude, Variant: "opus"},
			false,
		},
		{
			"no variant filter accepts any variant",
			tmux.Pane{Type: tmux.AgentCodex, Variant: "mini"},
			SendTarget{Type: AgentTypeCodex},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesSendTarget(tt.pane, tt.target)
			if got != tt.want {
				t.Errorf("matchesSendTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}

// =============================================================================
// send.go: permuteBatchPrompts()
// =============================================================================

func TestPermuteBatchPrompts_Basic(t *testing.T) {
	prompts := []BatchPrompt{
		{Text: "A", Priority: 0},
		{Text: "B", Priority: 1},
		{Text: "C", Priority: 2},
	}
	perm := []int{2, 0, 1}
	result := permuteBatchPrompts(prompts, perm)
	if len(result) != 3 {
		t.Fatalf("expected 3 prompts, got %d", len(result))
	}
	if result[0].Text != "C" {
		t.Errorf("result[0] = %q, want %q", result[0].Text, "C")
	}
	if result[1].Text != "A" {
		t.Errorf("result[1] = %q, want %q", result[1].Text, "A")
	}
	if result[2].Text != "B" {
		t.Errorf("result[2] = %q, want %q", result[2].Text, "B")
	}
}

func TestPermuteBatchPrompts_LengthMismatch(t *testing.T) {
	prompts := []BatchPrompt{{Text: "A"}, {Text: "B"}}
	perm := []int{0} // wrong length
	result := permuteBatchPrompts(prompts, perm)
	// Should return original on mismatch
	if len(result) != 2 {
		t.Fatalf("expected 2 prompts on mismatch, got %d", len(result))
	}
	if result[0].Text != "A" {
		t.Errorf("should return original order on mismatch")
	}
}

func TestPermuteBatchPrompts_Identity(t *testing.T) {
	prompts := []BatchPrompt{{Text: "X"}, {Text: "Y"}}
	perm := []int{0, 1}
	result := permuteBatchPrompts(prompts, perm)
	if result[0].Text != "X" || result[1].Text != "Y" {
		t.Error("identity permutation should preserve order")
	}
}

func TestPermuteBatchPrompts_InvalidIndex(t *testing.T) {
	prompts := []BatchPrompt{{Text: "A"}, {Text: "B"}}
	perm := []int{0, 5} // out of bounds
	result := permuteBatchPrompts(prompts, perm)
	// Should fall back to original
	if len(result) != 2 {
		t.Fatalf("expected 2 on invalid perm, got %d", len(result))
	}
}

// =============================================================================
// send.go: paneAgentLabel()
// =============================================================================

func TestPaneAgentLabel_WithTypeAndIndex(t *testing.T) {
	p := tmux.Pane{Index: 1, Type: tmux.AgentClaude, NTMIndex: 2}
	got := paneAgentLabel(p)
	if got != "cc_2" {
		t.Errorf("paneAgentLabel() = %q, want %q", got, "cc_2")
	}
}

func TestPaneAgentLabel_UserPane(t *testing.T) {
	p := tmux.Pane{Index: 0, Type: tmux.AgentUser}
	got := paneAgentLabel(p)
	if got != "user" {
		t.Errorf("paneAgentLabel() = %q, want %q", got, "user")
	}
}

func TestPaneAgentLabel_TitleWithPrefix(t *testing.T) {
	p := tmux.Pane{Index: 3, Title: "__cc_1"}
	got := paneAgentLabel(p)
	if got != "cc_1" {
		t.Errorf("paneAgentLabel() = %q, want %q", got, "cc_1")
	}
}

func TestPaneAgentLabel_TitleWithEmbeddedDoubleUnderscoreSession(t *testing.T) {
	p := tmux.Pane{Index: 3, Title: "my__project__cc_1"}
	got := paneAgentLabel(p)
	if got != "cc_1" {
		t.Errorf("paneAgentLabel() = %q, want %q", got, "cc_1")
	}
}

func TestTimelineAgentIDFromPane_TitleWithEmbeddedDoubleUnderscoreSession(t *testing.T) {
	p := tmux.Pane{Index: 3, Title: "my__project__cod_2"}
	got := timelineAgentIDFromPane(p)
	if got != "cod_2" {
		t.Errorf("timelineAgentIDFromPane() = %q, want %q", got, "cod_2")
	}
}

func TestPaneAgentLabel_TitleWithoutPrefix(t *testing.T) {
	p := tmux.Pane{Index: 3, Title: "custom_title"}
	got := paneAgentLabel(p)
	if got != "custom_title" {
		t.Errorf("paneAgentLabel() = %q, want %q", got, "custom_title")
	}
}

func TestPaneAgentLabel_NoTypeNoTitle(t *testing.T) {
	p := tmux.Pane{Index: 7}
	got := paneAgentLabel(p)
	if got != "pane_7" {
		t.Errorf("paneAgentLabel() = %q, want %q", got, "pane_7")
	}
}

// =============================================================================
// send.go: buildSendDryRunEntries()
// =============================================================================

func TestBuildSendDryRunEntries_Basic(t *testing.T) {
	panes := []tmux.Pane{
		{Index: 1, Type: tmux.AgentClaude, NTMIndex: 1},
		{Index: 2, Type: tmux.AgentCodex, NTMIndex: 1},
	}
	entries := buildSendDryRunEntries(panes, "review this code", "manual")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Pane != 1 {
		t.Errorf("entry[0].Pane = %d, want 1", entries[0].Pane)
	}
	if entries[0].Agent != "cc_1" {
		t.Errorf("entry[0].Agent = %q, want %q", entries[0].Agent, "cc_1")
	}
	if entries[0].Prompt != "review this code" {
		t.Errorf("entry[0].Prompt = %q, want full prompt", entries[0].Prompt)
	}
	if entries[0].Source != "manual" {
		t.Errorf("entry[0].Source = %q, want %q", entries[0].Source, "manual")
	}
	if entries[1].Pane != 2 {
		t.Errorf("entry[1].Pane = %d, want 2", entries[1].Pane)
	}
}

func TestBuildSendDryRunEntries_Empty(t *testing.T) {
	entries := buildSendDryRunEntries(nil, "test", "manual")
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for nil panes, got %d", len(entries))
	}
}

// (boolToStr, hasNonClaudeTargets, isNonClaudeAgent, normalizeCommandLine
// already tested in helpers_batch3_test.go, helpers_batch5_test.go, cli_helpers_test.go)

// =============================================================================
// send.go: sendTargetValue methods
// =============================================================================

func TestSendTargetValue_SetTrue(t *testing.T) {
	var targets SendTargets
	v := newSendTargetValue(AgentTypeClaude, &targets)
	err := v.Set("true")
	if err != nil {
		t.Fatalf("Set(true) error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Type != AgentTypeClaude {
		t.Errorf("type = %q, want %q", targets[0].Type, AgentTypeClaude)
	}
	if targets[0].Variant != "" {
		t.Errorf("variant = %q, want empty", targets[0].Variant)
	}
}

func TestSendTargetValue_SetFalse(t *testing.T) {
	var targets SendTargets
	v := newSendTargetValue(AgentTypeClaude, &targets)
	err := v.Set("false")
	if err != nil {
		t.Fatalf("Set(false) error: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("expected 0 targets for false, got %d", len(targets))
	}
}

func TestSendTargetValue_SetVariant(t *testing.T) {
	var targets SendTargets
	v := newSendTargetValue(AgentTypeCodex, &targets)
	err := v.Set("mini")
	if err != nil {
		t.Fatalf("Set(mini) error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Type != AgentTypeCodex {
		t.Errorf("type = %q, want %q", targets[0].Type, AgentTypeCodex)
	}
	if targets[0].Variant != "mini" {
		t.Errorf("variant = %q, want %q", targets[0].Variant, "mini")
	}
}

func TestSendTargetValue_SetEmpty(t *testing.T) {
	var targets SendTargets
	v := newSendTargetValue(AgentTypeClaude, &targets)
	err := v.Set("")
	if err != nil {
		t.Fatalf("Set('') error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Variant != "" {
		t.Errorf("variant = %q, want empty", targets[0].Variant)
	}
}

func TestSendTargetValue_IsBoolFlag(t *testing.T) {
	var targets SendTargets
	v := newSendTargetValue(AgentTypeClaude, &targets)
	if !v.IsBoolFlag() {
		t.Error("IsBoolFlag() should return true")
	}
}

func TestSendTargetValue_Type(t *testing.T) {
	var targets SendTargets
	v := newSendTargetValue(AgentTypeClaude, &targets)
	if got := v.Type(); got != "[variant]" {
		t.Errorf("Type() = %q, want %q", got, "[variant]")
	}
}

// =============================================================================
// scale.go: printScalePlan() - doesn't panic
// =============================================================================

func TestPrintScalePlan_NoPanic(t *testing.T) {
	// Ensure printScalePlan doesn't panic with various inputs
	printScalePlan("test", map[string]int{"cc": 1}, map[string]int{"cc": 3}, []ScaleAction{
		{ActionType: "spawn", AgentType: "cc", Count: 2},
	})
}

func TestPrintScalePlan_ScaleDown(t *testing.T) {
	printScalePlan("test",
		map[string]int{"cc": 5, "cod": 3},
		map[string]int{"cc": 2, "cod": 3},
		[]ScaleAction{
			{ActionType: "kill", AgentType: "cc", Count: 3, Agents: []string{"cc_3", "cc_4", "cc_5"}},
		},
	)
}

func TestPrintScalePlan_Empty(t *testing.T) {
	printScalePlan("test", map[string]int{}, map[string]int{}, nil)
}

// =============================================================================
// rebalance.go: printRebalanceReport() - doesn't panic
// =============================================================================

func TestPrintRebalanceReport_NoPanic(t *testing.T) {
	resp := RebalanceResponse{
		Session:        "test",
		ImbalanceScore: 0.5,
		Recommendation: "moderate",
		Workloads: []RebalanceWorkload{
			{Pane: 1, AgentType: "claude", TaskCount: 5, IsHealthy: true},
			{Pane: 2, AgentType: "codex", TaskCount: 1, IsHealthy: true, IsIdle: true},
		},
		Transfers: []RebalanceTransfer{
			{BeadID: "bd-123", BeadTitle: "Fix auth", FromPane: 1, FromAgent: "claude", ToPane: 2, ToAgent: "codex", Reason: "imbalance"},
		},
		After: map[int]int{1: 4, 2: 2},
	}
	printRebalanceReport(resp)
}

func TestPrintRebalanceReport_NoTransfers(t *testing.T) {
	resp := RebalanceResponse{
		Session:        "test",
		ImbalanceScore: 0.1,
		Recommendation: "balanced",
		Workloads: []RebalanceWorkload{
			{Pane: 1, AgentType: "claude", TaskCount: 2, IsHealthy: true},
		},
		After: map[int]int{},
	}
	printRebalanceReport(resp)
}

func TestPrintRebalanceReport_HighImbalance(t *testing.T) {
	resp := RebalanceResponse{
		Session:        "test",
		ImbalanceScore: 0.9,
		Recommendation: "critical",
		Workloads: []RebalanceWorkload{
			{Pane: 1, AgentType: "claude", TaskCount: 10, IsHealthy: false},
		},
		After: map[int]int{1: 10},
	}
	printRebalanceReport(resp)
}

func TestPrintRebalanceReport_EmptyWorkloads(t *testing.T) {
	resp := RebalanceResponse{
		Session:        "test",
		ImbalanceScore: 0.0,
		Recommendation: "none",
		Workloads:      []RebalanceWorkload{},
		After:          map[int]int{},
	}
	printRebalanceReport(resp)
}

// =============================================================================
// rebalance.go: outputRebalanceJSON() - round-trip
// =============================================================================

func TestOutputRebalanceJSON_Success(t *testing.T) {
	// Just ensure it doesn't error on valid input
	err := outputRebalanceJSON(map[string]string{"test": "value"})
	if err != nil {
		t.Errorf("outputRebalanceJSON() error: %v", err)
	}
}

func TestOutputRebalanceJSON_InvalidInput(t *testing.T) {
	// Channels can't be marshaled
	ch := make(chan int)
	err := outputRebalanceJSON(ch)
	if err == nil {
		t.Error("expected error for unmarshalable input")
	}
}

// =============================================================================
// review_queue.go: outputReviewQueueJSON() - round-trip
// =============================================================================

func TestOutputReviewQueueJSON_Success(t *testing.T) {
	err := outputReviewQueueJSON(map[string]int{"count": 5})
	if err != nil {
		t.Errorf("outputReviewQueueJSON() error: %v", err)
	}
}

func TestOutputReviewQueueJSON_InvalidInput(t *testing.T) {
	ch := make(chan int)
	err := outputReviewQueueJSON(ch)
	if err == nil {
		t.Error("expected error for unmarshalable input")
	}
}
