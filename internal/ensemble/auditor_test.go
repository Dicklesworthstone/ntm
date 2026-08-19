package ensemble

import (
	"testing"
	"time"
)

func TestDisagreementAuditor_AuditConflicts(t *testing.T) {
	outputs := []ModeOutput{
		{
			ModeID:          "deductive",
			Thesis:          "Root cause is a missing nil check in handler X.",
			TopFindings:     []Finding{{Finding: "Nil deref in handler", Impact: ImpactHigh, Confidence: 0.7}},
			Risks:           []Risk{{Risk: "Crash on startup", Impact: ImpactCritical, Likelihood: 0.6}},
			Recommendations: []Recommendation{{Recommendation: "Add nil guard", Priority: ImpactHigh}},
			Confidence:      0.8,
			GeneratedAt:     time.Now().UTC(),
		},
		{
			ModeID:          "counterfactual",
			Thesis:          "Root cause is an upstream config mismatch.",
			TopFindings:     []Finding{{Finding: "Config mismatch", Impact: ImpactHigh, Confidence: 0.6}},
			Risks:           []Risk{{Risk: "Silent misconfig", Impact: ImpactMedium, Likelihood: 0.4}},
			Recommendations: []Recommendation{{Recommendation: "Validate config", Priority: ImpactMedium}},
			Confidence:      0.6,
			GeneratedAt:     time.Now().UTC(),
		},
	}

	auditor := NewDisagreementAuditor(outputs, nil)
	report, err := auditor.Audit()
	if err != nil {
		t.Fatalf("Audit error: %v", err)
	}
	if report == nil {
		t.Fatal("expected report, got nil")
	}
	if len(report.Conflicts) == 0 {
		t.Fatal("expected conflicts, got none")
	}
	if len(report.ModeDisagreements) == 0 {
		t.Fatal("expected mode disagreements map")
	}
	if len(report.ResolutionSuggestions) == 0 {
		t.Fatal("expected resolution suggestions")
	}
}

func TestDisagreementAuditor_EmptyOutputs(t *testing.T) {
	auditor := NewDisagreementAuditor(nil, nil)
	if _, err := auditor.Audit(); err == nil {
		t.Fatal("expected error for empty outputs")
	}
}

func TestPositionsDiverge_SimilarPositions(t *testing.T) {
	positions := []ConflictPosition{
		{ModeID: "a", Position: "Root cause is missing nil check"},
		{ModeID: "b", Position: "Root cause: missing nil check"},
	}
	if positionsDiverge(positions) {
		t.Fatal("expected positions not to diverge")
	}
}

func TestResolveModeByCode(t *testing.T) {
	catalog := testModeCatalog(t)
	mode, err := resolveModeByCode("A1", catalog)
	if err != nil {
		t.Fatalf("resolveModeByCode error: %v", err)
	}
	if mode == nil || mode.ID != "deductive" {
		t.Fatalf("resolveModeByCode got %v, want deductive", mode)
	}
	if _, err := resolveModeByCode("A9", catalog); err == nil {
		t.Fatal("expected error for out-of-range code")
	}
}

func TestParseModeCode_Invalid(t *testing.T) {
	if _, _, ok := parseModeCode("invalid"); ok {
		t.Fatal("expected parseModeCode to fail")
	}
	if _, _, ok := parseModeCode("A"); ok {
		t.Fatal("expected parseModeCode to fail on short code")
	}
}

// =============================================================================
// Pure helper function tests
// =============================================================================

func TestTokenize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  int // expected number of tokens
	}{
		{"simple words", "hello world", 2},
		{"single word", "word", 1},
		{"empty string", "", 0},
		{"only whitespace", "   ", 0},
		{"multiple spaces", "one  two   three", 3},
		{"tabs and newlines", "a\tb\nc", 3},
		{"repeated words", "word word word", 1}, // Set only keeps unique
		{"mixed case", "Word WORD word", 3},     // Case sensitive
		{"with punctuation", "hello, world!", 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tokens := tokenize(tc.input)
			if len(tokens) != tc.want {
				t.Errorf("tokenize(%q) got %d tokens, want %d", tc.input, len(tokens), tc.want)
			}
		})
	}
}

func TestJaccardSimilarity_EdgeCases(t *testing.T) {
	t.Parallel()

	// Test edge cases not covered in similarity_test.go
	tests := []struct {
		name string
		a    map[string]struct{}
		b    map[string]struct{}
		want float64
	}{
		{
			name: "both empty returns 1",
			a:    map[string]struct{}{},
			b:    map[string]struct{}{},
			want: 1.0,
		},
		{
			name: "one empty one single",
			a:    map[string]struct{}{},
			b:    map[string]struct{}{"x": {}},
			want: 0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := jaccardSimilarity(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("jaccardSimilarity(%v, %v) = %f, want %f", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestUniqueStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  int
	}{
		{"no duplicates", []string{"a", "b", "c"}, 3},
		{"all duplicates", []string{"a", "a", "a"}, 1},
		{"some duplicates", []string{"a", "b", "a", "c", "b"}, 3},
		{"empty slice", []string{}, 0},
		{"single element", []string{"x"}, 1},
		{"empty strings skipped", []string{"", "", ""}, 0}, // Empty strings are filtered out
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := uniqueStrings(tc.input)
			if len(got) != tc.want {
				t.Errorf("uniqueStrings(%v) returned %d items, want %d", tc.input, len(got), tc.want)
			}
		})
	}
}
