package ensemble

import (
	"testing"
	"time"
)

func TestGenerateFindingID_Deterministic(t *testing.T) {
	first := GenerateFindingID("mode-a", "Same finding")
	second := GenerateFindingID("mode-a", "Same finding")
	if first != second {
		t.Fatalf("GenerateFindingID should be deterministic, got %q and %q", first, second)
	}

	other := GenerateFindingID("mode-b", "Same finding")
	if other == first {
		t.Fatalf("GenerateFindingID should differ across modes, got %q for both", other)
	}
}

func TestMergeOutputsWithProvenance_RecordsMerge(t *testing.T) {
	tracker := NewProvenanceTracker("question", []string{"mode-a", "mode-b"})
	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Thesis:     "A",
			Confidence: 0.9,
			TopFindings: []Finding{
				{Finding: "Shared finding", Impact: ImpactHigh, Confidence: 0.9},
			},
		},
		{
			ModeID:     "mode-b",
			Thesis:     "B",
			Confidence: 0.8,
			TopFindings: []Finding{
				{Finding: "Shared finding", Impact: ImpactHigh, Confidence: 0.8},
			},
		},
	}

	merged := MergeOutputsWithProvenance(outputs, DefaultMergeConfig(), tracker)
	if len(merged.Findings) != 1 {
		t.Fatalf("expected 1 merged finding, got %d", len(merged.Findings))
	}

	primaryID := GenerateFindingID("mode-a", "Shared finding")
	secondaryID := GenerateFindingID("mode-b", "Shared finding")

	primary, ok := tracker.GetChain(primaryID)
	if !ok {
		t.Fatalf("expected primary chain %s", primaryID)
	}
	if !containsStringProvenance(primary.MergedFrom, secondaryID) {
		t.Fatalf("expected primary chain to merge %s, got %+v", secondaryID, primary.MergedFrom)
	}

	mergedChain, ok := tracker.GetChain(secondaryID)
	if !ok {
		t.Fatalf("expected merged chain %s", secondaryID)
	}
	if mergedChain.MergedInto != primaryID {
		t.Fatalf("expected merged chain to point to %s, got %s", primaryID, mergedChain.MergedInto)
	}

	if merged.Findings[0].ProvenanceID != primaryID {
		t.Fatalf("expected merged provenance id %s, got %s", primaryID, merged.Findings[0].ProvenanceID)
	}
}

func TestSynthesizer_RecordsSynthesisCitations(t *testing.T) {
	tracker := NewProvenanceTracker("question", []string{"mode-a"})
	synth, err := NewSynthesizer(SynthesisConfig{Strategy: StrategyManual})
	if err != nil {
		t.Fatalf("NewSynthesizer error: %v", err)
	}

	outputs := []ModeOutput{
		{
			ModeID:     "mode-a",
			Thesis:     "A",
			Confidence: 0.9,
			TopFindings: []Finding{
				{Finding: "Cited finding", Impact: ImpactHigh, Confidence: 0.9},
			},
		},
	}

	_, err = synth.Synthesize(&SynthesisInput{
		Outputs:          outputs,
		OriginalQuestion: "question",
		Provenance:       tracker,
	})
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}

	findingID := GenerateFindingID("mode-a", "Cited finding")
	chain, ok := tracker.GetChain(findingID)
	if !ok {
		t.Fatalf("expected chain %s", findingID)
	}
	if len(chain.SynthesisCitations) == 0 {
		t.Fatalf("expected synthesis citations to be recorded")
	}
}

func containsStringProvenance(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func TestProvenanceChain_IsActive(t *testing.T) {
	t.Parallel()

	active := &ProvenanceChain{FindingID: "f1", MergedInto: ""}
	if !active.IsActive() {
		t.Error("chain with empty MergedInto should be active")
	}

	merged := &ProvenanceChain{FindingID: "f2", MergedInto: "f1"}
	if merged.IsActive() {
		t.Error("chain with non-empty MergedInto should not be active")
	}
}

func TestProvenanceTracker_ContextHash(t *testing.T) {
	t.Parallel()

	tracker := NewProvenanceTracker("test question", []string{"mode-a", "mode-b"})
	hash := tracker.ContextHash()
	if hash == "" {
		t.Error("ContextHash() should not be empty")
	}
	if len(hash) != 16 {
		t.Errorf("ContextHash() length = %d, want 16", len(hash))
	}

	// Same input should produce same hash
	tracker2 := NewProvenanceTracker("test question", []string{"mode-a", "mode-b"})
	if tracker.ContextHash() != tracker2.ContextHash() {
		t.Error("same inputs should produce same context hash")
	}

	// Different input should produce different hash
	tracker3 := NewProvenanceTracker("different question", []string{"mode-a"})
	if tracker.ContextHash() == tracker3.ContextHash() {
		t.Error("different inputs should produce different context hash")
	}
}

func TestProvenanceTracker_CountAndActiveCount(t *testing.T) {
	t.Parallel()

	tracker := NewProvenanceTracker("q", []string{"m1", "m2"})

	// Empty tracker
	if tracker.Count() != 0 {
		t.Errorf("Count() = %d, want 0", tracker.Count())
	}
	if tracker.ActiveCount() != 0 {
		t.Errorf("ActiveCount() = %d, want 0", tracker.ActiveCount())
	}

	// Add some chains via RecordDiscovery
	tracker.RecordDiscovery("m1", Finding{Finding: "Finding A", Impact: ImpactHigh, Confidence: 0.9})
	tracker.RecordDiscovery("m2", Finding{Finding: "Finding B", Impact: ImpactMedium, Confidence: 0.8})
	if tracker.Count() != 2 {
		t.Errorf("Count() = %d, want 2", tracker.Count())
	}
	if tracker.ActiveCount() != 2 {
		t.Errorf("ActiveCount() = %d, want 2", tracker.ActiveCount())
	}
}

func TestProvenanceTracker_Stats(t *testing.T) {
	t.Parallel()

	tracker := NewProvenanceTracker("q", []string{"m1", "m2"})

	tracker.RecordDiscovery("m1", Finding{Finding: "Finding A", Impact: ImpactHigh, Confidence: 0.9})
	tracker.RecordDiscovery("m2", Finding{Finding: "Finding B", Impact: ImpactMedium, Confidence: 0.8})

	stats := tracker.Stats()
	if stats.TotalFindings != 2 {
		t.Errorf("TotalFindings = %d, want 2", stats.TotalFindings)
	}
	if stats.ActiveFindings != 2 {
		t.Errorf("ActiveFindings = %d, want 2", stats.ActiveFindings)
	}
	if stats.MergedFindings != 0 {
		t.Errorf("MergedFindings = %d, want 0", stats.MergedFindings)
	}
	if stats.ModeBreakdown["m1"] != 1 {
		t.Errorf("ModeBreakdown[m1] = %d, want 1", stats.ModeBreakdown["m1"])
	}
	if stats.ModeBreakdown["m2"] != 1 {
		t.Errorf("ModeBreakdown[m2] = %d, want 1", stats.ModeBreakdown["m2"])
	}
}

func TestProvenanceTracker_Export(t *testing.T) {
	t.Parallel()

	tracker := NewProvenanceTracker("q", []string{"m1"})
	tracker.RecordDiscovery("m1", Finding{Finding: "Finding", Impact: ImpactHigh, Confidence: 0.9})

	data, err := tracker.Export()
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Export() should not return empty data")
	}
	// Verify it's valid JSON containing expected keys
	json := string(data)
	if !containsStringProvenance([]string{json}, "context_hash") {
		// Use a simpler check
		found := false
		for i := 0; i <= len(json)-12; i++ {
			if json[i:i+12] == "context_hash" {
				found = true
				break
			}
		}
		if !found {
			t.Error("exported JSON should contain context_hash")
		}
	}
}

func TestProvenance_ModeDiscovery(t *testing.T) {
	input := map[string]any{"mode": "mode-a", "finding": "Discovery finding"}
	logTestStartProvenance(t, input)

	tracker := NewProvenanceTracker("question", []string{"mode-a"})
	findingID := tracker.RecordDiscovery("mode-a", Finding{Finding: "Discovery finding", Impact: ImpactHigh, Confidence: 0.8})
	chain, ok := tracker.GetChain(findingID)
	logTestResultProvenance(t, chain)

	assertTrueProvenance(t, "chain exists", ok)
	assertEqualProvenance(t, "source mode", chain.SourceMode, "mode-a")
	assertTrueProvenance(t, "has steps", len(chain.Steps) > 0)
}

func TestProvenance_SynthesisTransform(t *testing.T) {
	input := map[string]any{"mode": "mode-a", "finding": "Transform finding"}
	logTestStartProvenance(t, input)

	tracker := NewProvenanceTracker("question", []string{"mode-a"})
	findingID := tracker.RecordDiscovery("mode-a", Finding{Finding: "Transform finding", Impact: ImpactMedium, Confidence: 0.7})
	_ = tracker.RecordSynthesisCitation(findingID, "synthesis:summary")
	chain, _ := tracker.GetChain(findingID)
	logTestResultProvenance(t, chain)

	assertTrueProvenance(t, "citation recorded", len(chain.SynthesisCitations) == 1)
	assertTrueProvenance(t, "steps include synthesis", containsStage(chain.Steps, "synthesis"))
}

func TestProvenance_FullChain(t *testing.T) {
	input := map[string]any{"mode": "mode-a", "finding": "Full chain"}
	logTestStartProvenance(t, input)

	tracker := NewProvenanceTracker("question", []string{"mode-a", "mode-b"})
	findingID := tracker.RecordDiscovery("mode-a", Finding{Finding: "Full chain", Impact: ImpactHigh, Confidence: 0.9})
	_ = tracker.RecordTextChange(findingID, "Full chain updated", "normalized")
	_ = tracker.RecordSynthesisCitation(findingID, "synthesis:findings")
	chain, _ := tracker.GetChain(findingID)
	logTestResultProvenance(t, chain)

	assertEqualProvenance(t, "current text updated", chain.CurrentText, "Full chain updated")
	assertTrueProvenance(t, "has multiple steps", len(chain.Steps) >= 3)
	assertTrueProvenance(t, "has synthesis citation", len(chain.SynthesisCitations) == 1)
}

func logTestStartProvenance(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultProvenance(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertTrueProvenance(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func assertEqualProvenance(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}

func containsStage(steps []ProvenanceStep, stage string) bool {
	for _, step := range steps {
		if step.Stage == stage {
			return true
		}
	}
	return false
}

func TestProvenanceTracker_RecordFilter(t *testing.T) {
	t.Parallel()
	tracker := NewProvenanceTracker("q", []string{"m1"})

	id := tracker.RecordDiscovery("m1", Finding{Finding: "filterable", Impact: ImpactLow, Confidence: 0.3})

	if err := tracker.RecordFilter(id, "low confidence"); err != nil {
		t.Fatalf("RecordFilter: %v", err)
	}

	chain, ok := tracker.GetChain(id)
	if !ok {
		t.Fatal("chain not found after filter")
	}
	if !containsStage(chain.Steps, "filter") {
		t.Error("expected filter stage in steps")
	}

	// Filter on nonexistent should error
	if err := tracker.RecordFilter("nonexistent", "reason"); err == nil {
		t.Error("expected error for nonexistent finding")
	}
}

func TestProvenanceTracker_RecordMerge_Error(t *testing.T) {
	t.Parallel()
	tracker := NewProvenanceTracker("q", []string{"m1"})

	// Merge with nonexistent primary
	err := tracker.RecordMerge("nonexistent", []string{"also-nonexistent"}, 0.9)
	if err == nil {
		t.Error("expected error for nonexistent primary")
	}
}

func TestProvenanceTracker_RecordTextChange(t *testing.T) {
	t.Parallel()
	tracker := NewProvenanceTracker("q", []string{"m1"})

	id := tracker.RecordDiscovery("m1", Finding{Finding: "original text", Impact: ImpactMedium, Confidence: 0.8})

	if err := tracker.RecordTextChange(id, "updated text", "normalize"); err != nil {
		t.Fatalf("RecordTextChange: %v", err)
	}

	chain, _ := tracker.GetChain(id)
	if chain.CurrentText != "updated text" {
		t.Errorf("CurrentText = %q, want %q", chain.CurrentText, "updated text")
	}
	if !containsStage(chain.Steps, "transform") {
		t.Error("expected transform stage in steps")
	}

	// TextChange on nonexistent should error
	if err := tracker.RecordTextChange("nonexistent", "text", "reason"); err == nil {
		t.Error("expected error for nonexistent finding")
	}
}

func TestProvenanceTracker_RecordSynthesisCitation_Error(t *testing.T) {
	t.Parallel()
	tracker := NewProvenanceTracker("q", []string{"m1"})

	if err := tracker.RecordSynthesisCitation("nonexistent", "loc"); err == nil {
		t.Error("expected error for nonexistent finding")
	}
}

func TestProvenanceTracker_ListChains(t *testing.T) {
	t.Parallel()
	tracker := NewProvenanceTracker("q", []string{"m1", "m2"})

	// Empty tracker
	chains := tracker.ListChains()
	if len(chains) != 0 {
		t.Errorf("empty tracker: ListChains() = %d, want 0", len(chains))
	}

	// Add findings
	tracker.RecordDiscovery("m1", Finding{Finding: "finding A", Impact: ImpactHigh, Confidence: 0.9})
	tracker.RecordDiscovery("m2", Finding{Finding: "finding B", Impact: ImpactMedium, Confidence: 0.7})

	chains = tracker.ListChains()
	if len(chains) != 2 {
		t.Errorf("ListChains() = %d, want 2", len(chains))
	}

	// Verify sorted by creation time (both created nearly simultaneously, but order should be stable)
	if chains[0].CreatedAt.After(chains[1].CreatedAt) {
		t.Error("ListChains should be sorted by CreatedAt")
	}
}

func TestProvenanceTracker_ListActiveChains(t *testing.T) {
	t.Parallel()
	tracker := NewProvenanceTracker("q", []string{"m1", "m2"})

	id1 := tracker.RecordDiscovery("m1", Finding{Finding: "active finding", Impact: ImpactHigh, Confidence: 0.9})
	id2 := tracker.RecordDiscovery("m2", Finding{Finding: "merged finding", Impact: ImpactMedium, Confidence: 0.7})

	// Merge id2 into id1
	_ = tracker.RecordMerge(id1, []string{id2}, 0.85)

	active := tracker.ListActiveChains()
	if len(active) != 1 {
		t.Fatalf("ListActiveChains() = %d, want 1", len(active))
	}
	if active[0].FindingID != id1 {
		t.Errorf("active chain ID = %q, want %q", active[0].FindingID, id1)
	}
}

func TestProvenanceTracker_FindByText(t *testing.T) {
	t.Parallel()
	tracker := NewProvenanceTracker("q", []string{"m1"})

	tracker.RecordDiscovery("m1", Finding{Finding: "SQL injection vulnerability in login form", Impact: ImpactHigh, Confidence: 0.9})
	tracker.RecordDiscovery("m1", Finding{Finding: "XSS vulnerability in search page", Impact: ImpactMedium, Confidence: 0.8})
	tracker.RecordDiscovery("m1", Finding{Finding: "Missing CSRF token", Impact: ImpactLow, Confidence: 0.6})

	// Search for "vulnerability"
	matches := tracker.FindByText("vulnerability")
	if len(matches) != 2 {
		t.Errorf("FindByText(vulnerability) = %d matches, want 2", len(matches))
	}

	// Search for "CSRF"
	matches = tracker.FindByText("csrf") // case-insensitive via normalizeText
	if len(matches) != 1 {
		t.Errorf("FindByText(csrf) = %d matches, want 1", len(matches))
	}

	// Search for something nonexistent
	matches = tracker.FindByText("buffer overflow")
	if len(matches) != 0 {
		t.Errorf("FindByText(buffer overflow) = %d matches, want 0", len(matches))
	}
}

func TestFormatProvenance_Nil(t *testing.T) {
	t.Parallel()
	result := FormatProvenance(nil)
	if result != "No provenance found" {
		t.Errorf("FormatProvenance(nil) = %q", result)
	}
}

func TestFormatProvenance_ActiveChain(t *testing.T) {
	t.Parallel()
	tracker := NewProvenanceTracker("q", []string{"m1"})
	id := tracker.RecordDiscovery("m1", Finding{Finding: "test finding", Impact: ImpactHigh, Confidence: 0.9})
	chain, _ := tracker.GetChain(id)

	result := FormatProvenance(chain)

	// Verify key sections are present
	checks := []string{
		"Finding:",
		"Source:",
		"Impact:",
		"Status: Active",
		"Timeline:",
		"discovery",
	}
	for _, check := range checks {
		found := false
		for i := 0; i <= len(result)-len(check); i++ {
			if result[i:i+len(check)] == check {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("FormatProvenance missing %q", check)
		}
	}
}

func TestFormatProvenance_MergedChain(t *testing.T) {
	t.Parallel()
	chain := &ProvenanceChain{
		FindingID:    "abc123",
		SourceMode:   "m1",
		ContextHash:  "ctx123",
		OriginalText: "original",
		CurrentText:  "original",
		Impact:       ImpactHigh,
		Confidence:   0.9,
		MergedInto:   "def456",
		Steps:        []ProvenanceStep{{Stage: "dedupe", Action: "absorbed", Timestamp: testTimeProvenance()}},
	}

	result := FormatProvenance(chain)
	if !strContains(result, "Merged into def456") {
		t.Error("expected merged status in output")
	}
}

func TestFormatProvenance_WithMergedFrom(t *testing.T) {
	t.Parallel()
	chain := &ProvenanceChain{
		FindingID:    "abc123",
		SourceMode:   "m1",
		ContextHash:  "ctx123",
		OriginalText: "original",
		CurrentText:  "original",
		Impact:       ImpactHigh,
		Confidence:   0.9,
		MergedFrom:   []string{"id1", "id2"},
		Steps:        []ProvenanceStep{{Stage: "dedupe", Action: "merged", Timestamp: testTimeProvenance()}},
	}

	result := FormatProvenance(chain)
	if !strContains(result, "merged 2 findings") {
		t.Error("expected merged-from count in output")
	}
}

func TestFormatProvenance_WithCitations(t *testing.T) {
	t.Parallel()
	chain := &ProvenanceChain{
		FindingID:          "abc123",
		SourceMode:         "m1",
		ContextHash:        "ctx123",
		OriginalText:       "original",
		CurrentText:        "updated",
		Impact:             ImpactMedium,
		Confidence:         0.8,
		SynthesisCitations: []string{"synthesis:summary", "synthesis:findings"},
		Steps:              []ProvenanceStep{{Stage: "discovery", Action: "discovered", Timestamp: testTimeProvenance()}},
	}

	result := FormatProvenance(chain)
	if !strContains(result, "Synthesis Citations") {
		t.Error("expected citations section")
	}
	if !strContains(result, "synthesis:summary") {
		t.Error("expected citation entry")
	}
	// When CurrentText != OriginalText, should show both
	if !strContains(result, "Current:") {
		t.Error("expected Current text when different from Original")
	}
}

func TestProvenanceIndex_Full(t *testing.T) {
	t.Parallel()

	tracker := NewProvenanceTracker("q", []string{"m1", "m2"})
	id1 := tracker.RecordDiscovery("m1", Finding{Finding: "finding A", Impact: ImpactHigh, Confidence: 0.9})
	id2 := tracker.RecordDiscovery("m2", Finding{Finding: "finding B", Impact: ImpactMedium, Confidence: 0.8})

	idx := NewProvenanceIndex()
	idx.Index(tracker)

	// Lookup
	chain, ok := idx.Lookup(id1)
	if !ok {
		t.Fatalf("Lookup(%s) not found", id1)
	}
	if chain.SourceMode != "m1" {
		t.Errorf("Lookup source mode = %q, want m1", chain.SourceMode)
	}

	// Lookup nonexistent
	_, ok = idx.Lookup("nonexistent")
	if ok {
		t.Error("Lookup should return false for nonexistent")
	}

	// ByMode
	m1Chains := idx.ByMode("m1")
	if len(m1Chains) != 1 {
		t.Errorf("ByMode(m1) = %d chains, want 1", len(m1Chains))
	}

	m2Chains := idx.ByMode("m2")
	if len(m2Chains) != 1 {
		t.Errorf("ByMode(m2) = %d chains, want 1", len(m2Chains))
	}

	// ByMode nonexistent
	empty := idx.ByMode("nonexistent")
	if len(empty) != 0 {
		t.Errorf("ByMode(nonexistent) = %d, want 0", len(empty))
	}

	// ByContext
	ctxChains := idx.ByContext(tracker.ContextHash())
	if len(ctxChains) != 2 {
		t.Errorf("ByContext() = %d chains, want 2", len(ctxChains))
	}

	_ = id2 // used above in RecordDiscovery
}

func TestGenerateReport_Nil(t *testing.T) {
	t.Parallel()
	report := GenerateReport(nil)
	if report != nil {
		t.Error("GenerateReport(nil) should return nil")
	}
}

func TestGenerateReport_Populated(t *testing.T) {
	t.Parallel()
	tracker := NewProvenanceTracker("q", []string{"m1", "m2"})

	id1 := tracker.RecordDiscovery("m1", Finding{Finding: "primary finding", Impact: ImpactHigh, Confidence: 0.9})
	id2 := tracker.RecordDiscovery("m2", Finding{Finding: "secondary finding", Impact: ImpactMedium, Confidence: 0.8})
	_ = tracker.RecordMerge(id1, []string{id2}, 0.85)

	report := GenerateReport(tracker)
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if report.ContextHash != tracker.ContextHash() {
		t.Errorf("ContextHash = %q, want %q", report.ContextHash, tracker.ContextHash())
	}
	if report.Stats.TotalFindings != 2 {
		t.Errorf("Stats.TotalFindings = %d, want 2", report.Stats.TotalFindings)
	}
	if len(report.ActiveChains) != 1 {
		t.Errorf("ActiveChains = %d, want 1", len(report.ActiveChains))
	}
	if len(report.MergeGraph) != 1 {
		t.Errorf("MergeGraph = %d entries, want 1", len(report.MergeGraph))
	}
	if report.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should not be zero")
	}
}

func TestProvenanceChain_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		chain   ProvenanceChain
		wantErr bool
	}{
		{
			"valid",
			ProvenanceChain{
				FindingID:    "f1",
				SourceMode:   "m1",
				OriginalText: "text",
				Steps:        []ProvenanceStep{{Stage: "discovery", Action: "found"}},
			},
			false,
		},
		{
			"missing finding_id",
			ProvenanceChain{SourceMode: "m1", OriginalText: "text", Steps: []ProvenanceStep{{Stage: "s"}}},
			true,
		},
		{
			"missing source_mode",
			ProvenanceChain{FindingID: "f1", OriginalText: "text", Steps: []ProvenanceStep{{Stage: "s"}}},
			true,
		},
		{
			"missing original_text",
			ProvenanceChain{FindingID: "f1", SourceMode: "m1", Steps: []ProvenanceStep{{Stage: "s"}}},
			true,
		},
		{
			"missing steps",
			ProvenanceChain{FindingID: "f1", SourceMode: "m1", OriginalText: "text"},
			true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.chain.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestTruncateText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		text   string
		maxLen int
		want   string
	}{
		{"short text", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncated", "hello world", 8, "hello..."},
		{"very short max", "hello world", 4, "h..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateText(tc.text, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateText(%q, %d) = %q, want %q", tc.text, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestProvenanceChain_AddStep(t *testing.T) {
	t.Parallel()
	chain := &ProvenanceChain{FindingID: "f1"}

	chain.AddStep("discovery", "found", "details", "related-1", "related-2")

	if len(chain.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(chain.Steps))
	}
	step := chain.Steps[0]
	if step.Stage != "discovery" {
		t.Errorf("Stage = %q", step.Stage)
	}
	if step.Action != "found" {
		t.Errorf("Action = %q", step.Action)
	}
	if step.Details != "details" {
		t.Errorf("Details = %q", step.Details)
	}
	if len(step.RelatedIDs) != 2 {
		t.Errorf("RelatedIDs = %d, want 2", len(step.RelatedIDs))
	}
	if chain.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

// testTimeProvenance returns a fixed time for deterministic test output.
func testTimeProvenance() time.Time {
	return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
}

// strContains is a simple string containment check for tests.
func strContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
