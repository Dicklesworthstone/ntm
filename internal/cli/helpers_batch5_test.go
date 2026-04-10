package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// agent_spec.go: TotalCount, ByType, Flatten
// =============================================================================

func TestAgentSpecsTotalCount(t *testing.T) {

	tests := []struct {
		name  string
		specs AgentSpecs
		want  int
	}{
		{"nil", nil, 0},
		{"empty", AgentSpecs{}, 0},
		{"single", AgentSpecs{{Count: 3}}, 3},
		{"multiple", AgentSpecs{{Count: 2}, {Count: 5}, {Count: 1}}, 8},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.specs.TotalCount(); got != tc.want {
				t.Errorf("TotalCount() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAgentSpecsByType(t *testing.T) {

	specs := AgentSpecs{
		{Type: AgentTypeClaude, Count: 2},
		{Type: AgentTypeCodex, Count: 3},
		{Type: AgentTypeClaude, Count: 1, Model: "opus"},
	}

	t.Run("filter claude", func(t *testing.T) {
		got := specs.ByType(AgentTypeClaude)
		if len(got) != 2 {
			t.Errorf("ByType(cc) returned %d specs, want 2", len(got))
		}
	})

	t.Run("filter codex", func(t *testing.T) {
		got := specs.ByType(AgentTypeCodex)
		if len(got) != 1 || got[0].Count != 3 {
			t.Errorf("ByType(cod) = %+v, want 1 spec with count 3", got)
		}
	})

	t.Run("filter gemini empty", func(t *testing.T) {
		got := specs.ByType(AgentTypeGemini)
		if len(got) != 0 {
			t.Errorf("ByType(gmi) returned %d specs, want 0", len(got))
		}
	})
}

func TestAgentSpecsFlatten(t *testing.T) {

	t.Run("empty", func(t *testing.T) {
		var specs AgentSpecs
		got := specs.Flatten()
		if len(got) != 0 {
			t.Errorf("Flatten() returned %d agents, want 0", len(got))
		}
	})

	t.Run("single type", func(t *testing.T) {
		specs := AgentSpecs{{Type: AgentTypeClaude, Count: 3, Model: "opus"}}
		got := specs.Flatten()
		if len(got) != 3 {
			t.Fatalf("Flatten() returned %d agents, want 3", len(got))
		}
		for i, a := range got {
			if a.Type != AgentTypeClaude {
				t.Errorf("agent[%d].Type = %q, want cc", i, a.Type)
			}
			if a.Index != i+1 {
				t.Errorf("agent[%d].Index = %d, want %d", i, a.Index, i+1)
			}
			if a.Model != "opus" {
				t.Errorf("agent[%d].Model = %q, want opus", i, a.Model)
			}
		}
	})

	t.Run("mixed types", func(t *testing.T) {
		specs := AgentSpecs{
			{Type: AgentTypeClaude, Count: 2},
			{Type: AgentTypeCodex, Count: 1},
		}
		got := specs.Flatten()
		if len(got) != 3 {
			t.Fatalf("Flatten() returned %d agents, want 3", len(got))
		}
		// First codex should have Index=1 (independent numbering per type)
		if got[2].Type != AgentTypeCodex || got[2].Index != 1 {
			t.Errorf("agent[2] = %+v, want codex with Index=1", got[2])
		}
	})
}

// =============================================================================
// persona_spec.go: PersonaSpecs.Set, Type, TotalCount, ParsePersonaSpec
// =============================================================================

func TestPersonaSpecsSetAndType(t *testing.T) {

	t.Run("Set appends", func(t *testing.T) {
		var specs PersonaSpecs
		if err := specs.Set("reviewer"); err != nil {
			t.Fatalf("Set(reviewer) error: %v", err)
		}
		if len(specs) != 1 || specs[0].Name != "reviewer" || specs[0].Count != 1 {
			t.Errorf("after Set(reviewer): specs = %+v", specs)
		}
		if err := specs.Set("coder:3"); err != nil {
			t.Fatalf("Set(coder:3) error: %v", err)
		}
		if len(specs) != 2 || specs[1].Count != 3 {
			t.Errorf("after Set(coder:3): specs = %+v", specs)
		}
	})

	t.Run("Set invalid", func(t *testing.T) {
		var specs PersonaSpecs
		if err := specs.Set(""); err == nil {
			t.Error("expected error for empty spec")
		}
		if err := specs.Set("name:abc"); err == nil {
			t.Error("expected error for non-numeric count")
		}
		if err := specs.Set("name:0"); err == nil {
			t.Error("expected error for zero count")
		}
	})

	t.Run("Type", func(t *testing.T) {
		var specs PersonaSpecs
		if got := specs.Type(); got != "name[:count]" {
			t.Errorf("Type() = %q, want name[:count]", got)
		}
	})

	t.Run("TotalCount", func(t *testing.T) {
		specs := PersonaSpecs{
			{Name: "a", Count: 2},
			{Name: "b", Count: 3},
		}
		if got := specs.TotalCount(); got != 5 {
			t.Errorf("TotalCount() = %d, want 5", got)
		}
	})

	t.Run("String", func(t *testing.T) {
		specs := PersonaSpecs{
			{Name: "a", Count: 1},
			{Name: "b", Count: 3},
		}
		got := specs.String()
		if got != "a,b:3" {
			t.Errorf("String() = %q, want a,b:3", got)
		}
	})
}

// =============================================================================
// send.go: intsToStrings, shuffledPermutation, boolToStr,
//          matchesSendTarget, permutePanes
// =============================================================================

func TestIntsToStrings(t *testing.T) {

	tests := []struct {
		name string
		ints []int
		want []string
	}{
		{"nil", nil, []string{}},
		{"empty", []int{}, []string{}},
		{"single", []int{42}, []string{"42"}},
		{"multiple", []int{1, 2, 3}, []string{"1", "2", "3"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := intsToStrings(tc.ints)
			if len(got) != len(tc.want) {
				t.Fatalf("intsToStrings(%v) len = %d, want %d", tc.ints, len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("intsToStrings(%v)[%d] = %q, want %q", tc.ints, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestShuffledPermutation(t *testing.T) {

	t.Run("deterministic with seed", func(t *testing.T) {
		seed1, perm1 := shuffledPermutation(5, 12345)
		seed2, perm2 := shuffledPermutation(5, 12345)
		if seed1 != seed2 {
			t.Errorf("seeds differ: %d vs %d", seed1, seed2)
		}
		if len(perm1) != len(perm2) {
			t.Fatalf("perm lengths differ: %d vs %d", len(perm1), len(perm2))
		}
		for i := range perm1 {
			if perm1[i] != perm2[i] {
				t.Errorf("perm[%d] = %d vs %d", i, perm1[i], perm2[i])
			}
		}
	})

	t.Run("valid permutation", func(t *testing.T) {
		_, perm := shuffledPermutation(10, 99)
		seen := make(map[int]bool)
		for _, v := range perm {
			if v < 0 || v >= 10 {
				t.Errorf("out of range: %d", v)
			}
			if seen[v] {
				t.Errorf("duplicate: %d", v)
			}
			seen[v] = true
		}
	})

	t.Run("n=0", func(t *testing.T) {
		_, perm := shuffledPermutation(0, 1)
		if len(perm) != 0 {
			t.Errorf("expected empty perm, got %v", perm)
		}
	})

	t.Run("n=1", func(t *testing.T) {
		_, perm := shuffledPermutation(1, 1)
		if len(perm) != 1 || perm[0] != 0 {
			t.Errorf("expected [0], got %v", perm)
		}
	})

	t.Run("zero seed returns nonzero", func(t *testing.T) {
		seed, _ := shuffledPermutation(5, 0)
		if seed == 0 {
			t.Error("expected non-zero seed when input seed is 0")
		}
	})
}

func TestBoolToStr(t *testing.T) {
	if got := boolToStr(true); got != "true" {
		t.Errorf("boolToStr(true) = %q", got)
	}
	if got := boolToStr(false); got != "false" {
		t.Errorf("boolToStr(false) = %q", got)
	}
}

func TestMatchesSendTarget(t *testing.T) {

	tests := []struct {
		name   string
		pane   tmux.Pane
		target SendTarget
		want   bool
	}{
		{
			"type matches no variant",
			tmux.Pane{Type: tmux.AgentClaude},
			SendTarget{Type: AgentTypeClaude},
			true,
		},
		{
			"type matches with variant match",
			tmux.Pane{Type: tmux.AgentClaude, Variant: "opus"},
			SendTarget{Type: AgentTypeClaude, Variant: "opus"},
			true,
		},
		{
			"type matches but variant mismatch",
			tmux.Pane{Type: tmux.AgentClaude, Variant: "sonnet"},
			SendTarget{Type: AgentTypeClaude, Variant: "opus"},
			false,
		},
		{
			"type mismatch",
			tmux.Pane{Type: tmux.AgentCodex},
			SendTarget{Type: AgentTypeClaude},
			false,
		},
		{
			"alias pane matches canonical target",
			tmux.Pane{Type: tmux.AgentType("openai-codex")},
			SendTarget{Type: AgentTypeCodex},
			true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesSendTarget(tc.pane, tc.target)
			if got != tc.want {
				t.Errorf("matchesSendTarget(%v, %v) = %v, want %v", tc.pane, tc.target, got, tc.want)
			}
		})
	}
}

func TestPermutePanes(t *testing.T) {

	t.Run("valid permutation", func(t *testing.T) {
		panes := []tmux.Pane{
			{Index: 0, Type: tmux.AgentClaude},
			{Index: 1, Type: tmux.AgentCodex},
			{Index: 2, Type: tmux.AgentGemini},
		}
		perm := []int{2, 0, 1}
		got := permutePanes(panes, perm)
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		if got[0].Index != 2 || got[1].Index != 0 || got[2].Index != 1 {
			t.Errorf("permutePanes reordered incorrectly: %v", got)
		}
	})

	t.Run("length mismatch returns original", func(t *testing.T) {
		panes := []tmux.Pane{{Index: 0}}
		perm := []int{0, 1}
		got := permutePanes(panes, perm)
		if len(got) != 1 || got[0].Index != 0 {
			t.Errorf("expected original panes on mismatch")
		}
	})

	t.Run("out of range index returns original", func(t *testing.T) {
		panes := []tmux.Pane{{Index: 0}, {Index: 1}}
		perm := []int{0, 99}
		got := permutePanes(panes, perm)
		// Out of range causes len(out) != len(panes), fallback to original
		if len(got) != 2 || got[0].Index != 0 {
			t.Errorf("expected original panes on out-of-range, got %v", got)
		}
	})
}

// =============================================================================
// persona_spec.go: ToAgentSpecs
// =============================================================================

func TestToAgentSpecs(t *testing.T) {

	t.Run("empty", func(t *testing.T) {
		specs, names := ToAgentSpecs(nil)
		if len(specs) != 0 {
			t.Errorf("expected empty specs, got %+v", specs)
		}
		if len(names) != 0 {
			t.Errorf("expected empty names, got %+v", names)
		}
	})

	t.Run("groups by type and model", func(t *testing.T) {
		resolved := []ResolvedPersonaAgent{
			{Type: AgentTypeClaude, Model: "opus"},
			{Type: AgentTypeClaude, Model: "opus"},
			{Type: AgentTypeCodex, Model: "gpt4"},
		}
		specs, _ := ToAgentSpecs(resolved)
		totalCount := 0
		for _, s := range specs {
			totalCount += s.Count
		}
		if totalCount != 3 {
			t.Errorf("total count = %d, want 3", totalCount)
		}
	})
}

// =============================================================================
// table.go: StyledTable builder, message helpers, text style helpers
// =============================================================================

func TestStyledTableBuilderOps(t *testing.T) {

	t.Run("basic construction", func(t *testing.T) {
		tbl := NewStyledTable("Name", "Age", "City")
		if tbl.RowCount() != 0 {
			t.Errorf("RowCount() = %d, want 0", tbl.RowCount())
		}
		tbl.AddRow("Alice", "30", "NYC")
		tbl.AddRow("Bob", "25", "SF")
		if tbl.RowCount() != 2 {
			t.Errorf("RowCount() = %d, want 2", tbl.RowCount())
		}
	})

	t.Run("builder methods return receiver", func(t *testing.T) {
		tbl := NewStyledTable("A", "B")
		ret := tbl.WithTitle("Test Title")
		if ret != tbl {
			t.Error("WithTitle did not return receiver")
		}
		ret = tbl.WithFooter("footer")
		if ret != tbl {
			t.Error("WithFooter did not return receiver")
		}
		ret = tbl.WithStyle(TableStyleSimple)
		if ret != tbl {
			t.Error("WithStyle did not return receiver")
		}
	})

	t.Run("render non-empty", func(t *testing.T) {
		tbl := NewStyledTable("Key", "Value")
		tbl.AddRow("a", "1")
		rendered := tbl.Render()
		if rendered == "" {
			t.Error("Render() returned empty string")
		}
		if tbl.String() != rendered {
			t.Error("String() != Render()")
		}
	})

	t.Run("render empty headers", func(t *testing.T) {
		tbl := NewStyledTable()
		if got := tbl.Render(); got != "" {
			t.Errorf("Render() with no headers = %q, want empty", got)
		}
	})

	t.Run("render all styles", func(t *testing.T) {
		for _, style := range []TableStyle{TableStyleRounded, TableStyleSimple, TableStyleMinimal} {
			tbl := NewStyledTable("A", "B").WithStyle(style)
			tbl.AddRow("x", "y")
			if tbl.Render() == "" {
				t.Errorf("style %d rendered empty", style)
			}
		}
	})
}

func TestMessageAndStyleHelpers(t *testing.T) {

	// Test that message helpers return non-empty strings containing the message
	helpers := map[string]func(string) string{
		"SuccessMessage": SuccessMessage,
		"ErrorMessage":   ErrorMessage,
		"WarningMessage": WarningMessage,
		"InfoMessage":    InfoMessage,
		"SubtleText":     SubtleText,
		"BoldText":       BoldText,
		"AccentText":     AccentText,
	}

	for name, fn := range helpers {
		t.Run(name, func(t *testing.T) {
			got := fn("test_content")
			if got == "" {
				t.Errorf("%s returned empty string", name)
			}
			// The content should be preserved (possibly with ANSI codes)
			if stripped := stripANSI(got); stripped == "" {
				t.Errorf("%s stripped to empty", name)
			}
		})
	}
}

func TestMatchesLegacySendTypeFilterCanonicalizesAliases(t *testing.T) {

	tests := []struct {
		name      string
		pane      tmux.Pane
		targetCC  bool
		targetCod bool
		targetGmi bool
		want      bool
	}{
		{name: "claude alias", pane: tmux.Pane{Type: tmux.AgentType("claude_code")}, targetCC: true, want: true},
		{name: "codex alias", pane: tmux.Pane{Type: tmux.AgentType("openai-codex")}, targetCod: true, want: true},
		{name: "gemini alias", pane: tmux.Pane{Type: tmux.AgentType("google-gemini")}, targetGmi: true, want: true},
		{name: "cursor unsupported by legacy flags", pane: tmux.Pane{Type: tmux.AgentCursor}, targetCC: true, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesLegacySendTypeFilter(tc.pane, tc.targetCC, tc.targetCod, tc.targetGmi); got != tc.want {
				t.Fatalf("matchesLegacySendTypeFilter(%v, %v, %v, %v) = %v, want %v", tc.pane, tc.targetCC, tc.targetCod, tc.targetGmi, got, tc.want)
			}
		})
	}
}

func TestIsInterruptibleAgentPane(t *testing.T) {

	tests := []struct {
		name string
		pane tmux.Pane
		want bool
	}{
		{name: "claude alias", pane: tmux.Pane{Type: tmux.AgentType("claude_code")}, want: true},
		{name: "codex alias", pane: tmux.Pane{Type: tmux.AgentType("openai-codex")}, want: true},
		{name: "gemini alias", pane: tmux.Pane{Type: tmux.AgentType("google-gemini")}, want: true},
		{name: "cursor", pane: tmux.Pane{Type: tmux.AgentCursor}, want: true},
		{name: "windsurf alias", pane: tmux.Pane{Type: tmux.AgentType("ws")}, want: true},
		{name: "aider", pane: tmux.Pane{Type: tmux.AgentAider}, want: true},
		{name: "ollama", pane: tmux.Pane{Type: tmux.AgentOllama}, want: true},
		{name: "user", pane: tmux.Pane{Type: tmux.AgentUser}, want: false},
		{name: "unknown", pane: tmux.Pane{Type: tmux.AgentUnknown}, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := isInterruptibleAgentPane(tc.pane); got != tc.want {
				t.Fatalf("isInterruptibleAgentPane(%v) = %v, want %v", tc.pane, got, tc.want)
			}
		})
	}
}
