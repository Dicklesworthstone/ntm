package scanner

import (
	"strings"
	"testing"
)

func TestSeverityToPriority(t *testing.T) {
	tests := []struct {
		severity Severity
		want     int
	}{
		{SeverityCritical, 0},
		{SeverityWarning, 1},
		{SeverityInfo, 3},
		{"unknown", 2},
	}

	for _, tt := range tests {
		got := severityToPriority(tt.severity)
		if got != tt.want {
			t.Errorf("severityToPriority(%s) = %d, want %d", tt.severity, got, tt.want)
		}
	}
}

func TestPriorityString(t *testing.T) {
	tests := []struct {
		priority int
		want     string
	}{
		{0, "P0"},
		{1, "P1"},
		{2, "P2"},
		{3, "P3"},
	}

	for _, tt := range tests {
		got := priorityString(tt.priority)
		if got != tt.want {
			t.Errorf("priorityString(%d) = %s, want %s", tt.priority, got, tt.want)
		}
	}
}

func TestComputePrioritiesBasic(t *testing.T) {
	result := &ScanResult{
		Findings: []Finding{
			{File: "test.go", Line: 10, Severity: SeverityCritical, Message: "Critical bug"},
			{File: "test.go", Line: 20, Severity: SeverityWarning, Message: "Warning"},
			{File: "test.go", Line: 30, Severity: SeverityInfo, Message: "Info"},
		},
	}

	report, err := ComputePriorities(result, nil)
	if err != nil {
		t.Fatalf("ComputePriorities error: %v", err)
	}

	if len(report.Findings) != 3 {
		t.Errorf("Expected 3 findings, got %d", len(report.Findings))
	}

	// Should be sorted by priority (P0 first)
	if len(report.Findings) > 0 {
		first := report.Findings[0]
		if first.AdjustedPriority != 0 {
			t.Errorf("First finding should have P0, got P%d", first.AdjustedPriority)
		}
	}
}

func TestComputePrioritiesSecurityBoost(t *testing.T) {
	result := &ScanResult{
		Findings: []Finding{
			{
				File:     "test.go",
				Line:     10,
				Severity: SeverityWarning,
				Category: "security",
				Message:  "Security warning",
			},
		},
	}

	report, err := ComputePriorities(result, nil)
	if err != nil {
		t.Fatalf("ComputePriorities error: %v", err)
	}

	if len(report.Findings) == 0 {
		t.Fatal("Expected at least 1 finding")
	}

	finding := report.Findings[0]
	// Security issues should get a boost (P1 -> P0)
	if finding.AdjustedPriority != 0 {
		t.Errorf("Security finding should be P0, got P%d", finding.AdjustedPriority)
	}

	// Should have reasoning about security
	hasSecurityReason := false
	for _, r := range finding.Reasoning {
		if strings.Contains(r, "Security") {
			hasSecurityReason = true
			break
		}
	}
	if !hasSecurityReason {
		t.Error("Should have reasoning about security boost")
	}
}

func TestGeneratePrioritySummary(t *testing.T) {
	report := &PriorityReport{
		Findings: []PrioritizedFinding{
			{AdjustedPriority: 0},
			{AdjustedPriority: 0},
			{AdjustedPriority: 1},
			{AdjustedPriority: 2},
		},
	}

	summary := generatePrioritySummary(report)

	if !strings.Contains(summary, "4 findings") {
		t.Errorf("Summary should mention 4 findings: %s", summary)
	}
	if !strings.Contains(summary, "2 critical") {
		t.Errorf("Summary should mention 2 critical: %s", summary)
	}
}

func TestGeneratePrioritySummaryEmpty(t *testing.T) {
	report := &PriorityReport{
		Findings: []PrioritizedFinding{},
	}

	summary := generatePrioritySummary(report)

	if summary != "No findings to prioritize" {
		t.Errorf("Expected empty summary message, got: %s", summary)
	}
}

func TestFormatPriorityReport(t *testing.T) {
	report := &PriorityReport{
		Findings: []PrioritizedFinding{
			{
				Finding:          Finding{File: "test.go", Line: 10, Message: "Bug"},
				BasePriority:     1,
				AdjustedPriority: 0,
				ImpactScore:      15.0,
				Reasoning:        []string{"Base priority P1", "Security issue: +1 priority"},
			},
		},
		GraphAvailable: false,
		Summary:        "1 findings prioritized, 1 critical (P0)",
	}

	output := FormatPriorityReport(report)

	if !strings.Contains(output, "Priority Report") {
		t.Error("Report should contain 'Priority Report' header")
	}
	if !strings.Contains(output, "test.go") {
		t.Error("Report should contain filename")
	}
	if !strings.Contains(output, "P0 (Critical)") {
		t.Error("Report should contain priority section header")
	}
}

func TestMax(t *testing.T) {
	if max(5, 3) != 5 {
		t.Error("max(5, 3) should be 5")
	}
	if max(3, 5) != 5 {
		t.Error("max(3, 5) should be 5")
	}
	if max(5, 5) != 5 {
		t.Error("max(5, 5) should be 5")
	}
}
