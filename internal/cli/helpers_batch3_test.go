package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// assign.go: inferTaskTypeFromBead
// =============================================================================

func TestInferTaskTypeFromBead(t *testing.T) {

	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"bug keyword", "Fix broken login page", "bug"},
		{"error keyword", "Handle error in API response", "bug"},
		{"crash keyword", "Crash on startup with nil pointer", "bug"},
		{"test keyword", "Add unit tests for config package", "testing"},
		{"coverage keyword", "Improve coverage for cli module", "testing"},
		{"doc keyword", "Update README documentation", "documentation"},
		{"refactor keyword", "Refactor auth middleware", "refactor"},
		{"cleanup keyword", "Cleanup deprecated endpoints", "refactor"},
		{"analyze keyword", "Analyze performance bottlenecks", "analysis"},
		{"feature keyword", "Implement dark mode toggle", "feature"},
		{"add keyword", "Add export functionality", "feature"},
		{"no match defaults to task", "Miscellaneous work item", "task"},
		{"empty title", "", "task"},
		{"case insensitive", "FIX LOGIN BUG", "bug"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bead := bv.BeadPreview{Title: tc.title}
			got := inferTaskTypeFromBead(bead)
			if got != tc.want {
				t.Errorf("inferTaskTypeFromBead(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

// =============================================================================
// assign.go: expandPromptTemplate
// =============================================================================

func TestExpandPromptTemplate(t *testing.T) {

	tests := []struct {
		name         string
		beadID       string
		title        string
		templateName string
		wantContains []string
	}{
		{
			"impl template",
			"bd-123", "Fix auth bug", "impl",
			[]string{"bd-123", "Fix auth bug", "br dep tree bd-123"},
		},
		{
			"review template",
			"bd-456", "Add tests", "review",
			[]string{"bd-456", "Add tests", "Review and verify"},
		},
		{
			"default template",
			"bd-789", "New feature", "unknown",
			[]string{"bd-789", "New feature"},
		},
		{
			"custom without file",
			"bd-abc", "Task title", "custom",
			[]string{"bd-abc", "Task title"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := expandPromptTemplate(tc.beadID, tc.title, tc.templateName, "")
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("expandPromptTemplate(%q, %q, %q, \"\") = %q, want to contain %q", tc.beadID, tc.title, tc.templateName, got, want)
				}
			}
		})
	}
}

// =============================================================================
// send.go: permuteBatchPrompts
// =============================================================================

func TestPermuteBatchPrompts(t *testing.T) {

	prompts := []BatchPrompt{
		{Text: "first"},
		{Text: "second"},
		{Text: "third"},
	}

	// Valid permutation reverses order
	got := permuteBatchPrompts(prompts, []int{2, 1, 0})
	if got[0].Text != "third" || got[1].Text != "second" || got[2].Text != "first" {
		t.Errorf("reverse perm: got %v", got)
	}

	// Mismatched length returns original
	got2 := permuteBatchPrompts(prompts, []int{0, 1})
	if len(got2) != 3 || got2[0].Text != "first" {
		t.Errorf("mismatched len: got %v", got2)
	}

	// Out-of-range index returns original
	got3 := permuteBatchPrompts(prompts, []int{0, 1, 5})
	if len(got3) != 3 || got3[0].Text != "first" {
		t.Errorf("out-of-range: got %v", got3)
	}

	// Negative index returns original
	got4 := permuteBatchPrompts(prompts, []int{0, -1, 2})
	if len(got4) != 3 || got4[0].Text != "first" {
		t.Errorf("negative: got %v", got4)
	}

	// Empty input returns original (nil == nil, so len check)
	got5 := permuteBatchPrompts(nil, nil)
	if len(got5) != 0 {
		t.Errorf("nil: got %v, want empty", got5)
	}
}

// =============================================================================
// send.go: isNonClaudeAgent
// =============================================================================

func TestIsNonClaudeAgent(t *testing.T) {

	tests := []struct {
		name     string
		paneType tmux.AgentType
		want     bool
	}{
		{"claude is not non-claude", tmux.AgentClaude, false},
		{"codex is non-claude", tmux.AgentCodex, true},
		{"gemini is non-claude", tmux.AgentGemini, true},
		{"user is not non-claude", tmux.AgentUser, false},
		{"unknown is non-claude", tmux.AgentUnknown, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pane := tmux.Pane{Type: tc.paneType}
			got := isNonClaudeAgent(pane)
			if got != tc.want {
				t.Errorf("isNonClaudeAgent(%v) = %v, want %v", tc.paneType, got, tc.want)
			}
		})
	}
}

// =============================================================================
// send.go: hasNonClaudeTargets
// =============================================================================

func TestHasNonClaudeTargets(t *testing.T) {

	// All claude - no non-claude targets
	allClaude := []tmux.Pane{
		{Type: tmux.AgentClaude},
		{Type: tmux.AgentClaude},
	}
	if hasNonClaudeTargets(allClaude) {
		t.Error("all claude panes should return false")
	}

	// Mix with codex
	mixed := []tmux.Pane{
		{Type: tmux.AgentClaude},
		{Type: tmux.AgentCodex},
	}
	if !hasNonClaudeTargets(mixed) {
		t.Error("mixed panes should return true")
	}

	// Empty
	if hasNonClaudeTargets(nil) {
		t.Error("nil panes should return false")
	}

	// User panes only - not non-claude
	userOnly := []tmux.Pane{
		{Type: tmux.AgentUser},
	}
	if hasNonClaudeTargets(userOnly) {
		t.Error("user-only panes should return false")
	}
}

// =============================================================================
// send.go: sendTargetValue.Set and IsBoolFlag
// =============================================================================

func TestSendTargetValueSetAndIsBoolFlag(t *testing.T) {

	var targets SendTargets
	val := newSendTargetValue(AgentTypeClaude, &targets)

	// IsBoolFlag should return true
	if !val.IsBoolFlag() {
		t.Error("IsBoolFlag() should return true")
	}

	// Set with "true" (bare flag) adds target without variant
	if err := val.Set("true"); err != nil {
		t.Fatalf("Set(true) error: %v", err)
	}
	if len(targets) != 1 || targets[0].Variant != "" {
		t.Errorf("after Set(true): targets = %+v", targets)
	}

	// Set with empty adds target without variant
	if err := val.Set(""); err != nil {
		t.Fatalf("Set(\"\") error: %v", err)
	}
	if len(targets) != 2 {
		t.Errorf("after Set(\"\"): len = %d, want 2", len(targets))
	}

	// Set with "false" is a no-op
	if err := val.Set("false"); err != nil {
		t.Fatalf("Set(false) error: %v", err)
	}
	if len(targets) != 2 {
		t.Errorf("after Set(false): len = %d, want 2", len(targets))
	}

	// Set with variant
	if err := val.Set("opus"); err != nil {
		t.Fatalf("Set(opus) error: %v", err)
	}
	if len(targets) != 3 || targets[2].Variant != "opus" {
		t.Errorf("after Set(opus): targets = %+v", targets)
	}
}

// =============================================================================
// send.go: SendTargets.HasTargetsForType
// =============================================================================

func TestSendTargetsHasTargetsForType(t *testing.T) {

	targets := SendTargets{
		{Type: AgentTypeClaude, Variant: ""},
		{Type: AgentTypeCodex, Variant: "gpt4"},
	}

	if !targets.HasTargetsForType(AgentTypeClaude) {
		t.Error("should have claude targets")
	}
	if !targets.HasTargetsForType(AgentTypeCodex) {
		t.Error("should have codex targets")
	}
	if targets.HasTargetsForType(AgentTypeGemini) {
		t.Error("should not have gemini targets")
	}

	// Empty targets
	var empty SendTargets
	if empty.HasTargetsForType(AgentTypeClaude) {
		t.Error("empty targets should not match")
	}
}
