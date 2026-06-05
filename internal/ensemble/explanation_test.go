package ensemble

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewExplanationTracker(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	if tracker == nil {
		t.Fatal("NewExplanationTracker returned nil")
	}
	if tracker.conclusions == nil {
		t.Error("conclusions should be initialized")
	}
	if tracker.modeWeights == nil {
		t.Error("modeWeights should be initialized")
	}

	t.Logf("TEST: %s - assertion: tracker created with defaults", t.Name())
}

func TestExplanationTracker_RecordConclusion(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	tracker.RecordConclusion("c1", ConclusionFinding, "Test finding", []string{"mode-a"}, 0.8)

	if len(tracker.conclusions) != 1 {
		t.Errorf("conclusions = %d, want 1", len(tracker.conclusions))
	}

	c := tracker.conclusions["c1"]
	if c == nil {
		t.Fatal("conclusion c1 not found")
	}
	if c.Type != ConclusionFinding {
		t.Errorf("Type = %s, want finding", c.Type)
	}
	if c.Text != "Test finding" {
		t.Errorf("Text = %s, want 'Test finding'", c.Text)
	}
	if c.Confidence != 0.8 {
		t.Errorf("Confidence = %v, want 0.8", c.Confidence)
	}

	t.Logf("TEST: %s - assertion: conclusion recorded correctly", t.Name())
}

func TestExplanationTracker_AddSourceFinding(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	tracker.RecordConclusion("c1", ConclusionFinding, "Test", []string{"mode-a"}, 0.8)
	tracker.AddSourceFinding("c1", "f1")
	tracker.AddSourceFinding("c1", "f2")

	c := tracker.conclusions["c1"]
	if len(c.SourceFindings) != 2 {
		t.Errorf("SourceFindings = %d, want 2", len(c.SourceFindings))
	}

	t.Logf("TEST: %s - assertion: source findings added", t.Name())
}

func TestExplanationTracker_SetConfidenceBasis(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	tracker.RecordConclusion("c1", ConclusionFinding, "Test", []string{"mode-a"}, 0.8)
	tracker.SetConfidenceBasis("c1", "Confirmed by multiple modes")

	c := tracker.conclusions["c1"]
	if c.ConfidenceBasis != "Confirmed by multiple modes" {
		t.Errorf("ConfidenceBasis = %s, want 'Confirmed by multiple modes'", c.ConfidenceBasis)
	}

	t.Logf("TEST: %s - assertion: confidence basis set", t.Name())
}

func TestExplanationTracker_AddEvidence(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	tracker.RecordConclusion("c1", ConclusionFinding, "Test", []string{"mode-a"}, 0.8)
	tracker.AddSupportingEvidence("c1", "Evidence 1")
	tracker.AddCounterEvidence("c1", "Counter 1")

	c := tracker.conclusions["c1"]
	if len(c.SupportingEvidence) != 1 {
		t.Errorf("SupportingEvidence = %d, want 1", len(c.SupportingEvidence))
	}
	if len(c.CounterEvidence) != 1 {
		t.Errorf("CounterEvidence = %d, want 1", len(c.CounterEvidence))
	}

	t.Logf("TEST: %s - assertion: evidence added", t.Name())
}

func TestExplanationTracker_RecordConflictResolution(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	positions := []PositionSummary{
		{ModeID: "mode-a", Position: "Position A", Strength: 0.8},
		{ModeID: "mode-b", Position: "Position B", Strength: 0.6},
	}
	tracker.RecordConflictResolution("Test topic", positions, "Went with A", ResolutionConsensus)

	if len(tracker.conflicts) != 1 {
		t.Errorf("conflicts = %d, want 1", len(tracker.conflicts))
	}

	cr := tracker.conflicts[0]
	if cr.Topic != "Test topic" {
		t.Errorf("Topic = %s, want 'Test topic'", cr.Topic)
	}
	if cr.Method != ResolutionConsensus {
		t.Errorf("Method = %s, want consensus", cr.Method)
	}
	if len(cr.Positions) != 2 {
		t.Errorf("Positions = %d, want 2", len(cr.Positions))
	}

	t.Logf("TEST: %s - assertion: conflict resolution recorded", t.Name())
}

func TestExplanationTracker_SetStrategyRationale(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	tracker.SetStrategyRationale("Using consensus strategy for broad agreement")

	if tracker.strategyRationale != "Using consensus strategy for broad agreement" {
		t.Errorf("strategyRationale = %s", tracker.strategyRationale)
	}

	t.Logf("TEST: %s - assertion: strategy rationale set", t.Name())
}

func TestExplanationTracker_SetModeWeight(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	tracker.SetModeWeight("mode-a", 0.6)
	tracker.SetModeWeight("mode-b", 0.4)

	if tracker.modeWeights["mode-a"] != 0.6 {
		t.Errorf("mode-a weight = %f, want 0.6", tracker.modeWeights["mode-a"])
	}
	if tracker.modeWeights["mode-b"] != 0.4 {
		t.Errorf("mode-b weight = %f, want 0.4", tracker.modeWeights["mode-b"])
	}

	t.Logf("TEST: %s - assertion: mode weights set", t.Name())
}

func TestExplanationTracker_GenerateLayer(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	tracker.SetStrategyRationale("Test rationale")
	tracker.SetModeWeight("mode-a", 0.5)
	tracker.RecordConclusion("c1", ConclusionFinding, "Finding 1", []string{"mode-a"}, 0.8)
	tracker.RecordConflictResolution("Topic 1", []PositionSummary{
		{ModeID: "mode-a", Position: "A", Strength: 0.8},
	}, "Resolved", ResolutionManual)

	layer := tracker.GenerateLayer()

	if layer == nil {
		t.Fatal("GenerateLayer returned nil")
	}
	if layer.StrategyRationale != "Test rationale" {
		t.Errorf("StrategyRationale = %s", layer.StrategyRationale)
	}
	if len(layer.Conclusions) != 1 {
		t.Errorf("Conclusions = %d, want 1", len(layer.Conclusions))
	}
	if len(layer.ConflictsResolved) != 1 {
		t.Errorf("ConflictsResolved = %d, want 1", len(layer.ConflictsResolved))
	}
	if layer.ModeWeights["mode-a"] != 0.5 {
		t.Errorf("ModeWeights[mode-a] = %f, want 0.5", layer.ModeWeights["mode-a"])
	}
	if layer.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should be set")
	}

	t.Logf("TEST: %s - assertion: layer generated correctly", t.Name())
}

func TestExplanationTracker_NilSafe(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	var tracker *ExplanationTracker

	// All methods should be nil-safe
	tracker.RecordConclusion("c1", ConclusionFinding, "Test", []string{"mode-a"}, 0.8)
	tracker.AddSourceFinding("c1", "f1")
	tracker.SetConfidenceBasis("c1", "basis")
	tracker.AddSupportingEvidence("c1", "evidence")
	tracker.AddCounterEvidence("c1", "counter")
	tracker.SetReasoning("c1", "reasoning")
	tracker.RecordConflictResolution("topic", nil, "resolved", ResolutionManual)
	tracker.SetStrategyRationale("rationale")
	tracker.SetModeWeight("mode-a", 0.5)

	layer := tracker.GenerateLayer()
	if layer != nil {
		t.Error("GenerateLayer on nil should return nil")
	}

	t.Logf("TEST: %s - assertion: nil receiver is safe", t.Name())
}

func TestExplanation_SingleConclusion(t *testing.T) {
	input := map[string]any{"id": "c-single", "type": ConclusionFinding}
	logTestStartExplanation(t, input)

	tracker := NewExplanationTracker(nil)
	tracker.RecordConclusion("c-single", ConclusionFinding, "Single conclusion", []string{"mode-a"}, 0.7)
	layer := tracker.GenerateLayer()
	logTestResultExplanation(t, layer)

	assertTrueExplanation(t, "layer generated", layer != nil)
	assertEqualExplanation(t, "conclusion count", len(layer.Conclusions), 1)
	assertEqualExplanation(t, "conclusion id", layer.Conclusions[0].ConclusionID, "c-single")
}

func TestExplanation_ConflictResolution(t *testing.T) {
	input := map[string]any{"topic": "conflict topic"}
	logTestStartExplanation(t, input)

	tracker := NewExplanationTracker(nil)
	tracker.RecordConflictResolution("conflict topic", []PositionSummary{{ModeID: "mode-a", Position: "A", Strength: 0.7}}, "resolved", ResolutionConsensus)
	layer := tracker.GenerateLayer()
	logTestResultExplanation(t, layer)

	assertEqualExplanation(t, "conflicts count", len(layer.ConflictsResolved), 1)
	assertEqualExplanation(t, "resolution method", layer.ConflictsResolved[0].Method, ResolutionConsensus)
}

func TestExplanation_ConfidenceDerivation(t *testing.T) {
	input := map[string]any{"id": "c-confidence"}
	logTestStartExplanation(t, input)

	tracker := NewExplanationTracker(nil)
	tracker.RecordConclusion("c-confidence", ConclusionFinding, "Confidence text", []string{"mode-a"}, 0.9)
	tracker.SetConfidenceBasis("c-confidence", "Multiple modes agree")
	tracker.AddSupportingEvidence("c-confidence", "Evidence A")
	layer := tracker.GenerateLayer()
	logTestResultExplanation(t, layer)

	assertEqualExplanation(t, "confidence basis", layer.Conclusions[0].ConfidenceBasis, "Multiple modes agree")
	assertEqualExplanation(t, "supporting evidence count", len(layer.Conclusions[0].SupportingEvidence), 1)
}

func logTestStartExplanation(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultExplanation(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertTrueExplanation(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func assertEqualExplanation(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}

func TestFormatExplanation(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	tracker.SetStrategyRationale("Using consensus for agreement")
	tracker.SetModeWeight("mode-a", 0.5)
	tracker.RecordConclusion("c1", ConclusionFinding, "Test finding", []string{"mode-a", "mode-b"}, 0.85)
	tracker.SetConfidenceBasis("c1", "Confirmed by both modes")
	tracker.RecordConflictResolution("Topic", []PositionSummary{
		{ModeID: "mode-a", Position: "Position A", Strength: 0.7},
	}, "Resolved", ResolutionConsensus)

	layer := tracker.GenerateLayer()
	output := FormatExplanation(layer)

	t.Logf("TEST: %s - formatted output:\n%s", t.Name(), output)

	if !strings.Contains(output, "Synthesis Explanation") {
		t.Error("Output should contain title")
	}
	if !strings.Contains(output, "Strategy Rationale") {
		t.Error("Output should contain strategy rationale")
	}
	if !strings.Contains(output, "Mode Weights") {
		t.Error("Output should contain mode weights")
	}
	if !strings.Contains(output, "Conclusion Explanations") {
		t.Error("Output should contain conclusions")
	}
	if !strings.Contains(output, "Conflicts Resolved") {
		t.Error("Output should contain conflicts")
	}

	t.Logf("TEST: %s - assertion: formatting produces readable output", t.Name())
}

func TestFormatExplanation_Nil(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	output := FormatExplanation(nil)

	if output != "No explanation available" {
		t.Errorf("FormatExplanation(nil) = %q, want 'No explanation available'", output)
	}

	t.Logf("TEST: %s - assertion: nil handling works", t.Name())
}

func TestExplanationLayer_JSON(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)
	tracker.RecordConclusion("c1", ConclusionFinding, "Test", []string{"mode-a"}, 0.8)
	layer := tracker.GenerateLayer()

	data, err := layer.JSON()
	if err != nil {
		t.Fatalf("JSON error: %v", err)
	}

	t.Logf("TEST: %s - JSON: %s", t.Name(), string(data))

	// Verify valid JSON
	var decoded ExplanationLayer
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("JSON should be valid: %v", err)
	}

	t.Logf("TEST: %s - assertion: JSON round-trips correctly", t.Name())
}

func TestBuildExplanationFromMerge(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)

	merged := &MergedOutput{
		Findings: []MergedFinding{
			{
				Finding:     Finding{Finding: "Shared finding", Impact: ImpactHigh, Confidence: 0.9},
				SourceModes: []string{"mode-a", "mode-b"},
			},
			{
				Finding:     Finding{Finding: "Unique finding", Impact: ImpactMedium, Confidence: 0.8},
				SourceModes: []string{"mode-a"},
			},
		},
		Risks: []MergedRisk{
			{
				Risk:        Risk{Risk: "Risk 1", Impact: ImpactHigh, Likelihood: 0.7},
				SourceModes: []string{"mode-a"},
			},
		},
		Recommendations: []MergedRecommendation{
			{
				Recommendation: Recommendation{Recommendation: "Rec 1", Priority: ImpactHigh},
				SourceModes:    []string{"mode-b"},
			},
		},
	}

	strategy := &StrategyConfig{
		Name:        "consensus",
		Description: "Builds consensus from modes",
		BestFor:     []string{"analysis", "review"},
	}

	BuildExplanationFromMerge(tracker, merged, strategy)

	t.Logf("TEST: %s - built explanation from merge", t.Name())

	// Should have conclusions for findings, risks, and recommendations
	if len(tracker.conclusions) != 4 {
		t.Errorf("conclusions = %d, want 4", len(tracker.conclusions))
	}

	if tracker.strategyRationale == "" {
		t.Error("strategyRationale should be set")
	}

	t.Logf("TEST: %s - assertion: merge explanations tracked", t.Name())
}

func TestBuildExplanationFromMerge_EmptyFindingSourceModes(t *testing.T) {
	tracker := NewExplanationTracker(nil)
	merged := &MergedOutput{
		Findings: []MergedFinding{
			{
				Finding: Finding{Finding: "Finding without source metadata", Impact: ImpactMedium, Confidence: 0.8},
			},
		},
	}

	BuildExplanationFromMerge(tracker, merged, nil)

	if len(tracker.conclusions) != 1 {
		t.Fatalf("conclusions = %d, want 1", len(tracker.conclusions))
	}

	var got *ConclusionExplanation
	for _, conclusion := range tracker.conclusions {
		got = conclusion
	}
	if got == nil {
		t.Fatal("expected a recorded conclusion")
	}
	if got.ConfidenceBasis != "No source mode metadata provided" {
		t.Fatalf("ConfidenceBasis = %q, want missing-source explanation", got.ConfidenceBasis)
	}
}

func TestBuildExplanationFromConflicts(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewExplanationTracker(nil)

	audit := &AuditReport{
		Conflicts: []DetailedConflict{
			{
				Topic:    "Architecture decision",
				Severity: ConflictHigh,
				Positions: []ConflictPosition{
					{ModeID: "mode-a", Position: "Use microservices", Confidence: 0.8},
					{ModeID: "mode-b", Position: "Use monolith", Confidence: 0.7},
				},
				ResolutionPath: "Consensus suggests microservices",
			},
		},
	}

	BuildExplanationFromConflicts(tracker, audit)

	t.Logf("TEST: %s - built explanation from conflicts", t.Name())

	if len(tracker.conflicts) != 1 {
		t.Errorf("conflicts = %d, want 1", len(tracker.conflicts))
	}

	cr := tracker.conflicts[0]
	if cr.Topic != "Architecture decision" {
		t.Errorf("Topic = %s", cr.Topic)
	}
	if cr.Method != ResolutionConsensus {
		t.Errorf("Method = %s, want consensus", cr.Method)
	}

	t.Logf("TEST: %s - assertion: conflict explanations tracked", t.Name())
}

func TestGenerateConflictID(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	positions := []PositionSummary{
		{ModeID: "mode-a", Position: "A", Strength: 0.8},
		{ModeID: "mode-b", Position: "B", Strength: 0.6},
	}

	id1 := GenerateConflictID("Test Topic", positions)
	id2 := GenerateConflictID("Test Topic", positions)

	if id1 != id2 {
		t.Errorf("IDs should be deterministic: %s != %s", id1, id2)
	}

	id3 := GenerateConflictID("Different Topic", positions)
	if id1 == id3 {
		t.Errorf("Different topics should have different IDs")
	}

	t.Logf("TEST: %s - assertion: conflict IDs are deterministic", t.Name())
}

func TestInferResolutionMethod(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tests := []struct {
		path   string
		method ResolutionMethod
	}{
		{"Resolved by consensus", ResolutionConsensus},
		{"Majority vote selected A", ResolutionMajority},
		{"Weighted combination used", ResolutionWeighted},
		{"Deferred for later", ResolutionDeferred},
		{"Manual override applied", ResolutionManual},
		{"Unknown path", ResolutionManual},
	}

	for _, tc := range tests {
		got := inferResolutionMethod(tc.path)
		if got != tc.method {
			t.Errorf("inferResolutionMethod(%q) = %s, want %s", tc.path, got, tc.method)
		}
	}

	t.Logf("TEST: %s - assertion: resolution methods inferred correctly", t.Name())
}

// =============================================================================
// sanitizeID
// =============================================================================

func TestSanitizeID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "hello", "hello"},
		{"uppercase", "HELLO", "hello"},
		{"spaces replaced", "hello world", "hello-world"},
		{"long truncated", "this is a very long string that goes beyond twenty", "this-is-a-very-long-"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeID(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
