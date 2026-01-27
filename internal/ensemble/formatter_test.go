package ensemble

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestNewSynthesisFormatter(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)

	if f == nil {
		t.Fatal("NewSynthesisFormatter returned nil")
	}
	if f.Format != FormatMarkdown {
		t.Errorf("Format = %v, want markdown", f.Format)
	}
	if f.IncludeRaw {
		t.Error("IncludeRaw should default to false")
	}
	if !f.IncludeAudit {
		t.Error("IncludeAudit should default to true")
	}
	if f.Verbose {
		t.Error("Verbose should default to false")
	}
}

func TestSynthesisFormatter_FormatResult_Markdown(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)

	result := &SynthesisResult{
		Summary:     "Executive summary of findings",
		Confidence:  0.85,
		GeneratedAt: time.Now(),
		Findings: []Finding{
			{Finding: "Critical security vulnerability", Impact: ImpactCritical, Confidence: 0.95, EvidencePointer: "auth.go:42"},
			{Finding: "Performance issue", Impact: ImpactMedium, Confidence: 0.80},
		},
		Risks: []Risk{
			{Risk: "Data breach potential", Impact: ImpactHigh, Likelihood: 0.7, Mitigation: "Implement rate limiting"},
		},
		Recommendations: []Recommendation{
			{Recommendation: "Upgrade authentication", Priority: ImpactCritical, Rationale: "Security is paramount"},
		},
		QuestionsForUser: []Question{
			{Question: "Should we prioritize security over features?", Context: "Given limited resources"},
		},
	}

	audit := &AuditReport{
		Conflicts: []DetailedConflict{
			{Topic: "Priority", Severity: ConflictMedium, Positions: []ConflictPosition{
				{ModeID: "mode-a", Position: "Focus on security", Confidence: 0.8},
				{ModeID: "mode-b", Position: "Focus on performance", Confidence: 0.7},
			}},
		},
		ResolutionSuggestions: []string{"Consider phased approach"},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, audit)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	output := buf.String()

	// Check key sections present
	if !strings.Contains(output, "# Ensemble Synthesis Report") {
		t.Error("Missing report header")
	}
	if !strings.Contains(output, "## Executive Summary") {
		t.Error("Missing executive summary section")
	}
	if !strings.Contains(output, "Executive summary of findings") {
		t.Error("Missing summary content")
	}
	if !strings.Contains(output, "85%") {
		t.Error("Missing confidence percentage")
	}
	if !strings.Contains(output, "## Key Findings") {
		t.Error("Missing key findings section")
	}
	if !strings.Contains(output, "Critical security vulnerability") {
		t.Error("Missing finding content")
	}
	if !strings.Contains(output, "`auth.go:42`") {
		t.Error("Missing evidence pointer")
	}
	if !strings.Contains(output, "## Identified Risks") {
		t.Error("Missing risks section")
	}
	if !strings.Contains(output, "Data breach potential") {
		t.Error("Missing risk content")
	}
	if !strings.Contains(output, "## Recommendations") {
		t.Error("Missing recommendations section")
	}
	if !strings.Contains(output, "Upgrade authentication") {
		t.Error("Missing recommendation content")
	}
	if !strings.Contains(output, "## Questions for User") {
		t.Error("Missing questions section")
	}
	if !strings.Contains(output, "Should we prioritize security") {
		t.Error("Missing question content")
	}
	if !strings.Contains(output, "## Mode Disagreements") {
		t.Error("Missing disagreements section")
	}
	if !strings.Contains(output, "Focus on security") {
		t.Error("Missing conflict position")
	}
}

func TestSynthesisFormatter_FormatResult_JSON(t *testing.T) {
	f := NewSynthesisFormatter(FormatJSON)
	f.IncludeAudit = true

	result := &SynthesisResult{
		Summary:     "Test summary",
		Confidence:  0.75,
		GeneratedAt: time.Now(),
		Findings: []Finding{
			{Finding: "Test finding", Impact: ImpactMedium, Confidence: 0.8},
		},
	}

	audit := &AuditReport{
		Conflicts: []DetailedConflict{
			{Topic: "Test conflict", Severity: ConflictLow},
		},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, audit)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	// Verify valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON output: %v", err)
	}

	if parsed["synthesis"] == nil {
		t.Error("JSON should contain synthesis key")
	}
	if parsed["audit"] == nil {
		t.Error("JSON should contain audit key when IncludeAudit is true")
	}
}

func TestSynthesisFormatter_FormatResult_JSON_NoAudit(t *testing.T) {
	f := NewSynthesisFormatter(FormatJSON)
	f.IncludeAudit = false

	result := &SynthesisResult{
		Summary:     "Test summary",
		Confidence:  0.75,
		GeneratedAt: time.Now(),
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, &AuditReport{})
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	if parsed["audit"] != nil {
		t.Error("JSON should not contain audit when IncludeAudit is false")
	}
}

func TestSynthesisFormatter_FormatResult_YAML(t *testing.T) {
	f := NewSynthesisFormatter(FormatYAML)

	result := &SynthesisResult{
		Summary:     "YAML test summary",
		Confidence:  0.65,
		GeneratedAt: time.Now(),
		Findings: []Finding{
			{Finding: "YAML finding", Impact: ImpactLow, Confidence: 0.7},
		},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, nil)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	// Verify valid YAML
	var parsed map[string]interface{}
	if err := yaml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid YAML output: %v", err)
	}

	if parsed["synthesis"] == nil {
		t.Error("YAML should contain synthesis key")
	}
}

func TestSynthesisFormatter_FormatResult_DefaultFormat(t *testing.T) {
	f := &SynthesisFormatter{Format: OutputFormat("unknown")}

	result := &SynthesisResult{
		Summary:     "Test",
		GeneratedAt: time.Now(),
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, nil)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	// Should default to markdown
	output := buf.String()
	if !strings.Contains(output, "# Ensemble Synthesis Report") {
		t.Error("Unknown format should default to markdown")
	}
}

func TestSynthesisFormatter_FormatResult_NilFormatter(t *testing.T) {
	var f *SynthesisFormatter

	var buf bytes.Buffer
	err := f.FormatResult(&buf, &SynthesisResult{}, nil)
	if err == nil {
		t.Error("Expected error for nil formatter")
	}
}

func TestSynthesisFormatter_FormatResult_NilWriter(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)

	err := f.FormatResult(nil, &SynthesisResult{}, nil)
	if err == nil {
		t.Error("Expected error for nil writer")
	}
}

func TestSynthesisFormatter_FormatResult_NilResult(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)

	var buf bytes.Buffer
	err := f.FormatResult(&buf, nil, nil)
	if err == nil {
		t.Error("Expected error for nil result in markdown")
	}
}

func TestSynthesisFormatter_FormatResult_Verbose(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)
	f.Verbose = true

	result := &SynthesisResult{
		Summary:     "Summary",
		Confidence:  0.8,
		GeneratedAt: time.Now(),
		Findings: []Finding{
			{Finding: "Test", Impact: ImpactMedium, Confidence: 0.8, Reasoning: "Detailed reasoning here"},
		},
		Recommendations: []Recommendation{
			{Recommendation: "Do this", Priority: ImpactHigh, Rationale: "Because it's important"},
		},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, nil)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "Detailed reasoning here") {
		t.Error("Verbose mode should include finding reasoning")
	}
	if !strings.Contains(output, "Because it's important") {
		t.Error("Verbose mode should include recommendation rationale")
	}
}

func TestSynthesisFormatter_FormatMergedOutput(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)

	merged := &MergedOutput{
		Findings: []MergedFinding{
			{Finding: Finding{Finding: "Merged finding", Impact: ImpactHigh, Confidence: 0.9}, SourceModes: []string{"mode-a", "mode-b"}, MergeScore: 0.85},
		},
		Risks: []MergedRisk{
			{Risk: Risk{Risk: "Merged risk", Impact: ImpactMedium, Likelihood: 0.6}, SourceModes: []string{"mode-a"}, MergeScore: 0.7},
		},
		Recommendations: []MergedRecommendation{
			{Recommendation: Recommendation{Recommendation: "Merged rec", Priority: ImpactHigh}, SourceModes: []string{"mode-b"}, MergeScore: 0.8},
		},
		SourceModes: []string{"mode-a", "mode-b"},
		Stats: MergeStats{
			InputCount:      2,
			TotalFindings:   3,
			DedupedFindings: 1,
			TotalRisks:      2,
			DedupedRisks:    1,
			MergeTime:       100 * time.Millisecond,
		},
	}

	var buf bytes.Buffer
	err := f.FormatMergedOutput(&buf, merged)
	if err != nil {
		t.Fatalf("FormatMergedOutput error: %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "# Merged Output Report") {
		t.Error("Missing merged report header")
	}
	if !strings.Contains(output, "## Merge Statistics") {
		t.Error("Missing merge statistics section")
	}
	if !strings.Contains(output, "mode-a, mode-b") {
		t.Error("Missing source modes list")
	}
	if !strings.Contains(output, "Merged finding") {
		t.Error("Missing merged finding")
	}
	if !strings.Contains(output, "score: 0.85") {
		t.Error("Missing merge score")
	}
}

func TestSynthesisFormatter_FormatMergedOutput_JSON(t *testing.T) {
	f := NewSynthesisFormatter(FormatJSON)

	merged := &MergedOutput{
		SourceModes: []string{"mode-a"},
		Stats:       MergeStats{InputCount: 1},
	}

	var buf bytes.Buffer
	err := f.FormatMergedOutput(&buf, merged)
	if err != nil {
		t.Fatalf("FormatMergedOutput error: %v", err)
	}

	var parsed MergedOutput
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}
}

func TestSynthesisFormatter_FormatMergedOutput_YAML(t *testing.T) {
	f := NewSynthesisFormatter(FormatYAML)

	merged := &MergedOutput{
		SourceModes: []string{"mode-a"},
		Stats:       MergeStats{InputCount: 1},
	}

	var buf bytes.Buffer
	err := f.FormatMergedOutput(&buf, merged)
	if err != nil {
		t.Fatalf("FormatMergedOutput error: %v", err)
	}

	var parsed MergedOutput
	if err := yaml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid YAML: %v", err)
	}
}

func TestSynthesisFormatter_FormatMergedOutput_NilFormatter(t *testing.T) {
	var f *SynthesisFormatter

	var buf bytes.Buffer
	err := f.FormatMergedOutput(&buf, &MergedOutput{})
	if err == nil {
		t.Error("Expected error for nil formatter")
	}
}

func TestSynthesisFormatter_FormatMergedOutput_NilWriter(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)

	err := f.FormatMergedOutput(nil, &MergedOutput{})
	if err == nil {
		t.Error("Expected error for nil writer")
	}
}

func TestSynthesisFormatter_FormatMergedOutput_NilMerged(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)

	var buf bytes.Buffer
	err := f.FormatMergedOutput(&buf, nil)
	if err == nil {
		t.Error("Expected error for nil merged output")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10c", 10, "exactly10c"},
		{"this is a longer string", 10, "this is..."},
		{"   spaced   ", 10, "spaced"},
		{"", 10, ""},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestPriorityEmoji(t *testing.T) {
	tests := []struct {
		priority ImpactLevel
		want     string
	}{
		{ImpactCritical, "🔴"},
		{ImpactHigh, "🟠"},
		{ImpactMedium, "🟡"},
		{ImpactLow, "🟢"},
		{ImpactLevel("unknown"), "⚪"},
	}

	for _, tt := range tests {
		got := priorityEmoji(tt.priority)
		if got != tt.want {
			t.Errorf("priorityEmoji(%v) = %q, want %q", tt.priority, got, tt.want)
		}
	}
}

func TestFormatResult_EmptyCollections(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)

	result := &SynthesisResult{
		Summary:          "Minimal result",
		Confidence:       0.5,
		GeneratedAt:      time.Now(),
		Findings:         []Finding{},
		Risks:            []Risk{},
		Recommendations:  []Recommendation{},
		QuestionsForUser: []Question{},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, nil)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	output := buf.String()

	// Should still have headers but not the section content headers
	if !strings.Contains(output, "# Ensemble Synthesis Report") {
		t.Error("Missing main header")
	}
	if !strings.Contains(output, "## Executive Summary") {
		t.Error("Missing executive summary")
	}
	// Empty collections should not generate section headers
	if strings.Contains(output, "## Key Findings") {
		t.Error("Should not have Key Findings section when empty")
	}
	if strings.Contains(output, "## Identified Risks") {
		t.Error("Should not have Risks section when empty")
	}
}

func TestFormatResult_RiskTable(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)

	result := &SynthesisResult{
		Summary:     "Risk analysis",
		Confidence:  0.7,
		GeneratedAt: time.Now(),
		Risks: []Risk{
			{Risk: "Very long risk description that should be truncated in the table view", Impact: ImpactHigh, Likelihood: 0.8, Mitigation: ""},
			{Risk: "Short risk", Impact: ImpactLow, Likelihood: 0.3, Mitigation: "Apply patch"},
		},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, nil)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	output := buf.String()

	// Should have table structure
	if !strings.Contains(output, "| Risk | Impact | Likelihood | Mitigation |") {
		t.Error("Missing risk table header")
	}
	if !strings.Contains(output, "|------|--------|------------|------------|") {
		t.Error("Missing table separator")
	}
	// Check likelihood as percentage
	if !strings.Contains(output, "80%") {
		t.Error("Risk likelihood should be shown as percentage")
	}
	// Empty mitigation should show dash
	if !strings.Contains(output, "| - |") {
		t.Error("Empty mitigation should show dash")
	}
}

func TestFormatResult_DisagreementsSection(t *testing.T) {
	f := NewSynthesisFormatter(FormatMarkdown)
	f.IncludeAudit = true

	result := &SynthesisResult{
		Summary:     "Summary",
		Confidence:  0.6,
		GeneratedAt: time.Now(),
	}

	audit := &AuditReport{
		Conflicts: []DetailedConflict{
			{
				Topic:    "Architecture Decision",
				Severity: ConflictHigh,
				Positions: []ConflictPosition{
					{ModeID: "mode-a", Position: "Use microservices", Confidence: 0.9},
					{ModeID: "mode-b", Position: "Use monolith", Confidence: 0.85},
				},
				ResolutionPath: "Consult with architect",
			},
		},
		ResolutionSuggestions: []string{
			"Review requirements again",
			"Consider hybrid approach",
		},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, audit)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "## Mode Disagreements") {
		t.Error("Missing disagreements section")
	}
	if !strings.Contains(output, "1 areas of disagreement") {
		t.Error("Missing conflict count")
	}
	if !strings.Contains(output, "### Architecture Decision (high)") {
		t.Error("Missing conflict topic header")
	}
	if !strings.Contains(output, "**mode-a** (90% confidence)") {
		t.Error("Missing mode position")
	}
	if !strings.Contains(output, "Use microservices") {
		t.Error("Missing position text")
	}
	if !strings.Contains(output, "Consult with architect") {
		t.Error("Missing resolution path")
	}
	if !strings.Contains(output, "### Resolution Suggestions") {
		t.Error("Missing resolution suggestions header")
	}
}

func TestOutputFormat_Constants(t *testing.T) {
	if FormatMarkdown != "markdown" {
		t.Errorf("FormatMarkdown = %q, want markdown", FormatMarkdown)
	}
	if FormatJSON != "json" {
		t.Errorf("FormatJSON = %q, want json", FormatJSON)
	}
	if FormatYAML != "yaml" {
		t.Errorf("FormatYAML = %q, want yaml", FormatYAML)
	}
}

func TestFormatResult_ContributionsSection(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	f := NewSynthesisFormatter(FormatMarkdown)
	f.IncludeContributions = true

	result := &SynthesisResult{
		Summary:     "Test summary",
		Confidence:  0.8,
		GeneratedAt: time.Now(),
		Contributions: &ContributionReport{
			GeneratedAt:     time.Now(),
			TotalFindings:   10,
			DedupedFindings: 7,
			OverlapRate:     0.3,
			DiversityScore:  0.65,
			Scores: []ContributionScore{
				{
					ModeID:               "mode-a",
					ModeName:             "Deductive Logic",
					FindingsCount:        4,
					OriginalFindings:     5,
					UniqueInsights:       2,
					CitationCount:        3,
					RisksCount:           1,
					RecommendationsCount: 1,
					Score:                55.0,
					Rank:                 1,
					HighlightFindings:    []string{"Key insight from deductive analysis"},
				},
				{
					ModeID:               "mode-b",
					ModeName:             "Bayesian Reasoning",
					FindingsCount:        3,
					OriginalFindings:     5,
					UniqueInsights:       1,
					CitationCount:        2,
					RisksCount:           0,
					RecommendationsCount: 0,
					Score:                35.0,
					Rank:                 2,
				},
			},
		},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, nil)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: %s - output:\n%s", t.Name(), output)

	// Check contributions section header
	if !strings.Contains(output, "## Mode Contributions") {
		t.Error("Missing Mode Contributions section")
	}

	// Check summary stats
	if !strings.Contains(output, "Total findings: 10 (deduped: 7)") {
		t.Error("Missing total/deduped findings summary")
	}
	if !strings.Contains(output, "Overlap: 30%") {
		t.Error("Missing overlap rate")
	}
	if !strings.Contains(output, "Diversity: 0.65") {
		t.Error("Missing diversity score")
	}

	// Check table headers
	if !strings.Contains(output, "| Rank | Mode | Score |") {
		t.Error("Missing contribution table header")
	}

	// Check mode data in table
	if !strings.Contains(output, "Deductive Logic") {
		t.Error("Missing Deductive Logic mode name")
	}
	if !strings.Contains(output, "Bayesian Reasoning") {
		t.Error("Missing Bayesian Reasoning mode name")
	}
	if !strings.Contains(output, "#1") {
		t.Error("Missing rank #1")
	}
	if !strings.Contains(output, "#2") {
		t.Error("Missing rank #2")
	}

	t.Logf("TEST: %s - assertion: contributions section rendered correctly", t.Name())
}

func TestFormatResult_ContributionsSection_Verbose(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	f := NewSynthesisFormatter(FormatMarkdown)
	f.IncludeContributions = true
	f.Verbose = true

	result := &SynthesisResult{
		Summary:     "Test summary",
		Confidence:  0.8,
		GeneratedAt: time.Now(),
		Contributions: &ContributionReport{
			GeneratedAt:     time.Now(),
			TotalFindings:   5,
			DedupedFindings: 4,
			Scores: []ContributionScore{
				{
					ModeID:            "mode-a",
					ModeName:          "Test Mode",
					FindingsCount:     3,
					OriginalFindings:  3,
					UniqueInsights:    1,
					Score:             60.0,
					Rank:              1,
					HighlightFindings: []string{"Important unique finding"},
				},
			},
		},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, nil)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: %s - output:\n%s", t.Name(), output)

	// Verbose mode should show highlights
	if !strings.Contains(output, "Test Mode highlights") {
		t.Error("Verbose mode should show highlights header")
	}
	if !strings.Contains(output, "Important unique finding") {
		t.Error("Verbose mode should show highlight findings")
	}

	t.Logf("TEST: %s - assertion: verbose mode shows highlights", t.Name())
}

func TestFormatResult_ContributionsSection_JSON(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	f := NewSynthesisFormatter(FormatJSON)

	result := &SynthesisResult{
		Summary:     "Test summary",
		Confidence:  0.8,
		GeneratedAt: time.Now(),
		Contributions: &ContributionReport{
			GeneratedAt:     time.Now(),
			TotalFindings:   5,
			DedupedFindings: 4,
			Scores: []ContributionScore{
				{
					ModeID:           "mode-a",
					FindingsCount:    3,
					OriginalFindings: 3,
					Score:            60.0,
					Rank:             1,
				},
			},
		},
	}

	var buf bytes.Buffer
	err := f.FormatResult(&buf, result, nil)
	if err != nil {
		t.Fatalf("FormatResult error: %v", err)
	}

	output := buf.String()
	t.Logf("TEST: %s - output:\n%s", t.Name(), output)

	// JSON output should include contributions
	if !strings.Contains(output, "\"contributions\"") {
		t.Error("JSON output should include contributions field")
	}
	if !strings.Contains(output, "\"total_findings\": 5") {
		t.Error("JSON output should include total_findings")
	}
	if !strings.Contains(output, "\"scores\"") {
		t.Error("JSON output should include scores array")
	}

	t.Logf("TEST: %s - assertion: JSON includes contributions", t.Name())
}
