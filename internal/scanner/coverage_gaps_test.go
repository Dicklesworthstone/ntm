package scanner

import (
	"strings"
	"testing"
)

// =============================================================================
// analysis.go: AnalyzeImpact - test RecommendedOrder filtering branches
// =============================================================================

func TestAnalyzeImpactRecommendedOrderAllLowScore(t *testing.T) {
	t.Parallel()

	// When all scores are below threshold (5.0) and no blocks, RecommendedOrder
	// should still include top items (up to 10).
	result := &ScanResult{
		Findings: []Finding{
			{File: "a.go", Line: 1, Severity: SeverityInfo, Message: "Info 1"},
			{File: "b.go", Line: 2, Severity: SeverityInfo, Message: "Info 2"},
			{File: "c.go", Line: 3, Severity: SeverityInfo, Message: "Info 3"},
		},
	}

	analysis, err := AnalyzeImpact(result, nil)
	if err != nil {
		t.Fatalf("AnalyzeImpact error: %v", err)
	}

	// All have ImpactScore=1.0 (info) which is below 5.0 threshold
	// So RecommendedOrder should be populated with fallback (top N)
	if len(analysis.RecommendedOrder) == 0 {
		t.Error("RecommendedOrder should not be empty when findings exist")
	}
	if len(analysis.RecommendedOrder) > 10 {
		t.Errorf("RecommendedOrder should cap at 10, got %d", len(analysis.RecommendedOrder))
	}
}

func TestAnalyzeImpactEmptyFindings(t *testing.T) {
	t.Parallel()

	result := &ScanResult{
		Findings: []Finding{},
	}

	analysis, err := AnalyzeImpact(result, nil)
	if err != nil {
		t.Fatalf("AnalyzeImpact error: %v", err)
	}

	if analysis.TotalFindings != 0 {
		t.Errorf("expected 0 total findings, got %d", analysis.TotalFindings)
	}
	if len(analysis.HighImpactFindings) != 0 {
		t.Errorf("expected 0 high impact findings, got %d", len(analysis.HighImpactFindings))
	}
	if len(analysis.RecommendedOrder) != 0 {
		t.Errorf("expected 0 recommended order, got %d", len(analysis.RecommendedOrder))
	}
}

func TestAnalyzeImpactMixedSeverities(t *testing.T) {
	t.Parallel()

	result := &ScanResult{
		Findings: []Finding{
			{File: "a.go", Line: 1, Severity: SeverityCritical, Message: "Critical"},
			{File: "b.go", Line: 2, Severity: SeverityWarning, Message: "Warning"},
			{File: "c.go", Line: 3, Severity: SeverityInfo, Message: "Info"},
		},
	}

	analysis, err := AnalyzeImpact(result, nil)
	if err != nil {
		t.Fatalf("AnalyzeImpact error: %v", err)
	}

	// Critical (10.0) and Warning (5.0) meet the >= 5.0 threshold
	// Info (1.0) does not
	// But without bv, the fallback path populates all findings into RecommendedOrder
	// (since skipBVIntegration=true in tests, the non-graph path is taken)
	if len(analysis.RecommendedOrder) == 0 {
		t.Error("RecommendedOrder should not be empty with findings present")
	}

	// Should be sorted critical first
	if len(analysis.HighImpactFindings) > 0 {
		if analysis.HighImpactFindings[0].Finding.Severity != SeverityCritical {
			t.Errorf("expected critical first, got %s", analysis.HighImpactFindings[0].Finding.Severity)
		}
	}
}

func TestAnalyzeImpactHotspotsGenerated(t *testing.T) {
	t.Parallel()

	result := &ScanResult{
		Findings: []Finding{
			{File: "hotfile.go", Line: 1, Severity: SeverityCritical, Message: "bug 1"},
			{File: "hotfile.go", Line: 5, Severity: SeverityWarning, Message: "bug 2"},
			{File: "hotfile.go", Line: 10, Severity: SeverityInfo, Message: "note"},
			{File: "cold.go", Line: 1, Severity: SeverityInfo, Message: "minor"},
		},
	}

	analysis, err := AnalyzeImpact(result, nil)
	if err != nil {
		t.Fatalf("AnalyzeImpact error: %v", err)
	}

	if len(analysis.Hotspots) != 2 {
		t.Fatalf("expected 2 hotspots, got %d", len(analysis.Hotspots))
	}

	// hotfile.go should be first (highest impact)
	if analysis.Hotspots[0].File != "hotfile.go" {
		t.Errorf("expected hotfile.go first, got %s", analysis.Hotspots[0].File)
	}
	if analysis.Hotspots[0].FindingCount != 3 {
		t.Errorf("expected 3 findings in hotfile.go, got %d", analysis.Hotspots[0].FindingCount)
	}
}

// =============================================================================
// analysis.go: computeHotspots with file centrality
// =============================================================================

func TestComputeHotspotsWithFileCentrality(t *testing.T) {
	t.Parallel()

	findings := []Finding{
		{File: "a.go", Severity: SeverityCritical},
		{File: "b.go", Severity: SeverityWarning},
	}

	fileCentrality := map[string]float64{
		"a.go": 8.5,
	}

	hotspots := computeHotspots(findings, fileCentrality)

	if len(hotspots) != 2 {
		t.Fatalf("expected 2 hotspots, got %d", len(hotspots))
	}

	if hotspots[0].File != "a.go" {
		t.Fatalf("expected a.go first, got %s", hotspots[0].File)
	}
	if hotspots[0].Centrality != 8.5 {
		t.Errorf("expected a.go centrality 8.5, got %f", hotspots[0].Centrality)
	}
	if hotspots[1].Centrality != 0.0 {
		t.Errorf("expected unmatched file centrality 0, got %f", hotspots[1].Centrality)
	}
}

func TestComputeHotspotsEmpty(t *testing.T) {
	t.Parallel()

	hotspots := computeHotspots(nil, nil)
	if len(hotspots) != 0 {
		t.Errorf("expected 0 hotspots for nil findings, got %d", len(hotspots))
	}
}

// =============================================================================
// analysis.go: estimateFileCentrality
// =============================================================================

func TestEstimateFileCentralityPure(t *testing.T) {
	t.Parallel()

	result := estimateFileCentrality("any.go", map[string]float64{"any.go": 5.0})
	if result != 5.0 {
		t.Errorf("estimateFileCentrality expected 5.0, got %f", result)
	}
}

func TestBuildFileCentrality(t *testing.T) {
	t.Parallel()

	low := Finding{File: "core.go", Line: 10, RuleID: "ubs-low"}
	high := Finding{File: "core.go", Line: 20, RuleID: "ubs-high"}
	other := Finding{File: "other.go", Line: 5, RuleID: "ubs-other"}

	existingBeads := map[string]string{
		FindingSignature(low):   "bd-low",
		FindingSignature(high):  "bd-high",
		FindingSignature(other): "bd-other",
	}
	keystones := map[string]float64{
		"bd-low":   2.0,
		"bd-high":  7.5,
		"bd-other": 3.0,
	}

	result := buildFileCentrality([]Finding{low, high, other}, existingBeads, keystones)
	if result["core.go"] != 7.5 {
		t.Errorf("core.go centrality = %f, want 7.5", result["core.go"])
	}
	if result["other.go"] != 3.0 {
		t.Errorf("other.go centrality = %f, want 3.0", result["other.go"])
	}

	if got := buildFileCentrality([]Finding{low}, nil, keystones); got != nil {
		t.Errorf("expected nil without existing bead IDs, got %#v", got)
	}
}

// =============================================================================
// priority.go: ComputePriorities - additional branch coverage
// =============================================================================

func TestComputePrioritiesEmptyFindings(t *testing.T) {
	t.Parallel()

	result := &ScanResult{Findings: []Finding{}}
	report, err := ComputePriorities(result, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.Findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(report.Findings))
	}
	if strings.Compare(report.Summary, "No findings to prioritize") != 0 {
		t.Errorf("expected empty summary message, got %q", report.Summary)
	}
}

func TestComputePrioritiesMultipleSeverities(t *testing.T) {
	t.Parallel()

	result := &ScanResult{
		Findings: []Finding{
			{File: "a.go", Severity: SeverityInfo, Message: "info"},
			{File: "b.go", Severity: SeverityCritical, Message: "critical"},
			{File: "c.go", Severity: SeverityWarning, Message: "warning"},
			{File: "d.go", Severity: "other", Message: "unknown"},
		},
	}

	report, err := ComputePriorities(result, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.Findings) != 4 {
		t.Fatalf("expected 4 findings, got %d", len(report.Findings))
	}

	// Should be sorted by priority (P0 first, then P1, P2, P3)
	if report.Findings[0].AdjustedPriority != 0 {
		t.Errorf("first finding should be P0, got P%d", report.Findings[0].AdjustedPriority)
	}

	// Summary should mention all priority levels
	if !strings.Contains(report.Summary, "critical (P0)") {
		t.Errorf("summary should mention P0: %q", report.Summary)
	}
	if !strings.Contains(report.Summary, "high (P1)") {
		t.Errorf("summary should mention P1: %q", report.Summary)
	}
}

func TestComputePrioritiesWithBeadIDNoGraph(t *testing.T) {
	t.Parallel()

	result := &ScanResult{
		Findings: []Finding{
			{File: "a.go", Line: 10, Severity: SeverityCritical, Category: "security", Message: "SQL injection"},
		},
	}

	// existingBeadIDs maps finding signatures to bead IDs
	sig := FindingSignature(result.Findings[0])
	beadIDs := map[string]string{
		sig: "bd-12345",
	}

	report, err := ComputePriorities(result, beadIDs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(report.Findings))
	}

	pf := report.Findings[0]
	// BeadID should be set
	if strings.Compare(pf.BeadID, "bd-12345") != 0 {
		t.Errorf("expected bead ID bd-12345, got %q", pf.BeadID)
	}

	// Security category should boost priority: P0 critical - 1 = P0 (already at 0, can't go lower)
	// But the security boost should still be attempted
	if pf.AdjustedPriority != 0 {
		t.Errorf("expected P0 for critical security issue, got P%d", pf.AdjustedPriority)
	}
}

func TestComputePrioritiesSecurityBoostFromP1(t *testing.T) {
	t.Parallel()

	result := &ScanResult{
		Findings: []Finding{
			{File: "a.go", Severity: SeverityWarning, Category: "security", Message: "XSS vuln"},
		},
	}

	report, err := ComputePriorities(result, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pf := report.Findings[0]
	// Warning = P1 base, security boost = P0
	if pf.AdjustedPriority != 0 {
		t.Errorf("expected P0 for warning+security, got P%d", pf.AdjustedPriority)
	}

	// Should have security reasoning
	found := false
	for _, r := range pf.Reasoning {
		if strings.Contains(r, "Security") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected security reasoning in findings")
	}
}

// =============================================================================
// priority.go: FormatPriorityReport - additional branches
// =============================================================================

func TestFormatPriorityReportGraphAvailable(t *testing.T) {
	t.Parallel()

	report := &PriorityReport{
		Findings:       []PrioritizedFinding{},
		GraphAvailable: true,
		Summary:        "No findings to prioritize",
	}

	output := FormatPriorityReport(report)
	if strings.Contains(output, "bv not available") {
		t.Error("should not mention bv unavailable when graph is available")
	}
}

func TestFormatPriorityReportPriorityChange(t *testing.T) {
	t.Parallel()

	report := &PriorityReport{
		Findings: []PrioritizedFinding{
			{
				Finding: Finding{
					File:     "vuln.go",
					Line:     42,
					Severity: SeverityWarning,
					Message:  "Security issue found",
				},
				BasePriority:     1,
				AdjustedPriority: 0,
				ImpactScore:      5.0,
				Reasoning:        []string{"Base priority P1 from severity warning", "Security issue: +1 priority"},
			},
		},
		GraphAvailable: false,
		Summary:        "1 findings prioritized, 1 critical (P0)",
	}

	output := FormatPriorityReport(report)
	// When AdjustedPriority != BasePriority, reasoning is shown
	if !strings.Contains(output, "Security issue") {
		t.Error("should show reasoning for priority change")
	}
}

// =============================================================================
// analysis.go: FormatImpactReport - additional branches
// =============================================================================

func TestFormatImpactReportGraphAvailableFlag(t *testing.T) {
	t.Parallel()

	result := &AnalysisResult{
		GraphAvailable: true,
		RecommendedOrder: []ImpactAnalysis{
			{Finding: Finding{File: "test.go", Line: 1, Severity: SeverityCritical, Message: "bug"}, ImpactScore: 10.0},
		},
	}

	output := FormatImpactReport(result)
	if strings.Contains(output, "bv not available") {
		t.Error("should not mention bv unavailable when graph is available")
	}
}

func TestFormatImpactReportNoFindings(t *testing.T) {
	t.Parallel()

	result := &AnalysisResult{
		GraphAvailable: false,
	}

	output := FormatImpactReport(result)
	if strings.Contains(output, "High-Impact") {
		t.Error("should not show High-Impact section when no findings")
	}
	if strings.Contains(output, "Recommended Fix Order") {
		t.Error("should not show Recommended Fix Order when no findings")
	}
}
