// Package scanner provides smart prioritization combining severity and graph position.
package scanner

import (
	"fmt"
	"sort"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// PrioritizedFinding represents a finding with smart priority.
type PrioritizedFinding struct {
	Finding          Finding  `json:"finding"`
	BasePriority     int      `json:"base_priority"`     // From severity
	AdjustedPriority int      `json:"adjusted_priority"` // After graph analysis
	ImpactScore      float64  `json:"impact_score"`
	Reasoning        []string `json:"reasoning"`
	BeadID           string   `json:"bead_id,omitempty"`
}

// PriorityReport contains prioritized findings with reasoning.
type PriorityReport struct {
	Findings       []PrioritizedFinding `json:"findings"`
	GraphAvailable bool                 `json:"graph_available"`
	Summary        string               `json:"summary"`
}

// ComputePriorities calculates smart priorities combining severity and graph position.
func ComputePriorities(result *ScanResult, existingBeadIDs map[string]string) (*PriorityReport, error) {
	report := &PriorityReport{
		Findings: make([]PrioritizedFinding, 0, len(result.Findings)),
	}

	// Try to get bv insights
	var insights *bv.InsightsResponse
	if bv.IsInstalled() {
		var err error
		insights, err = bv.GetInsights("")
		if err == nil {
			report.GraphAvailable = true
		}
	}

	// Build lookup maps if graph available
	bottleneckMap := make(map[string]float64)
	keystoneMap := make(map[string]float64)
	if insights != nil {
		for _, b := range insights.Bottlenecks {
			bottleneckMap[b.ID] = b.Value
		}
		for _, k := range insights.Keystones {
			keystoneMap[k.ID] = k.Value
		}
	}

	// Process each finding
	for _, f := range result.Findings {
		pf := PrioritizedFinding{
			Finding:      f,
			BasePriority: severityToPriority(f.Severity),
			Reasoning:    make([]string, 0),
		}

		// Start with base priority
		pf.AdjustedPriority = pf.BasePriority
		pf.ImpactScore = severityToScore(f.Severity)

		// Add severity reasoning
		pf.Reasoning = append(pf.Reasoning, fmt.Sprintf("Base priority %s from severity %s",
			priorityString(pf.BasePriority), f.Severity))

		// Check for corresponding bead
		sig := FindingSignature(f)
		if beadID, ok := existingBeadIDs[sig]; ok {
			pf.BeadID = beadID

			// Check if this bead is a bottleneck
			if score, exists := bottleneckMap[beadID]; exists {
				// Boost priority for bottlenecks
				if score > 10 {
					pf.AdjustedPriority = max(0, pf.AdjustedPriority-1) // Increase priority (lower number)
					pf.ImpactScore += score * 2.0
					pf.Reasoning = append(pf.Reasoning,
						fmt.Sprintf("Bottleneck (+1 priority): blocks %.0f downstream paths", score))
				} else if score > 5 {
					pf.ImpactScore += score * 1.5
					pf.Reasoning = append(pf.Reasoning,
						fmt.Sprintf("Moderate bottleneck: blocks %.0f downstream paths", score))
				}
			}

			// Check if this bead is a keystone
			if score, exists := keystoneMap[beadID]; exists {
				pf.ImpactScore += score * 1.0
				pf.Reasoning = append(pf.Reasoning,
					fmt.Sprintf("Keystone: high centrality (%.2f)", score))
			}
		}

		// Adjust priority based on category
		if f.Category == "security" || f.Category == "vulnerability" {
			if pf.AdjustedPriority > 0 {
				pf.AdjustedPriority--
				pf.Reasoning = append(pf.Reasoning, "Security issue: +1 priority")
			}
		}

		report.Findings = append(report.Findings, pf)
	}

	// Sort by adjusted priority (ascending) then by impact score (descending)
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].AdjustedPriority != report.Findings[j].AdjustedPriority {
			return report.Findings[i].AdjustedPriority < report.Findings[j].AdjustedPriority
		}
		return report.Findings[i].ImpactScore > report.Findings[j].ImpactScore
	})

	// Generate summary
	report.Summary = generatePrioritySummary(report)

	return report, nil
}

// severityToPriority converts severity to beads priority (0-3).
func severityToPriority(sev Severity) int {
	switch sev {
	case SeverityCritical:
		return 0 // P0
	case SeverityWarning:
		return 1 // P1
	case SeverityInfo:
		return 3 // P3
	default:
		return 2 // P2
	}
}

// priorityString returns the priority as a string.
func priorityString(p int) string {
	return fmt.Sprintf("P%d", p)
}

// generatePrioritySummary creates a summary of the priority report.
func generatePrioritySummary(report *PriorityReport) string {
	if len(report.Findings) == 0 {
		return "No findings to prioritize"
	}

	// Count by adjusted priority
	counts := make(map[int]int)
	for _, f := range report.Findings {
		counts[f.AdjustedPriority]++
	}

	summary := fmt.Sprintf("%d findings prioritized", len(report.Findings))
	if counts[0] > 0 {
		summary += fmt.Sprintf(", %d critical (P0)", counts[0])
	}
	if counts[1] > 0 {
		summary += fmt.Sprintf(", %d high (P1)", counts[1])
	}
	if counts[2] > 0 {
		summary += fmt.Sprintf(", %d medium (P2)", counts[2])
	}
	if counts[3] > 0 {
		summary += fmt.Sprintf(", %d low (P3)", counts[3])
	}

	return summary
}

// FormatPriorityReport generates a human-readable priority report.
func FormatPriorityReport(report *PriorityReport) string {
	var s string

	s += "Priority Report\n"
	s += "════════════════════════════════════════════════════\n\n"

	if !report.GraphAvailable {
		s += "Note: bv not available, priorities based on severity only\n\n"
	}

	s += fmt.Sprintf("%s\n\n", report.Summary)

	// Group by priority
	grouped := make(map[int][]PrioritizedFinding)
	for _, f := range report.Findings {
		grouped[f.AdjustedPriority] = append(grouped[f.AdjustedPriority], f)
	}

	for p := 0; p <= 3; p++ {
		findings := grouped[p]
		if len(findings) == 0 {
			continue
		}

		priorityLabel := []string{"P0 (Critical)", "P1 (High)", "P2 (Medium)", "P3 (Low)"}[p]
		s += fmt.Sprintf("%s\n", priorityLabel)
		s += "────────────────────────────────────────────────────\n"

		for i, f := range findings {
			if i >= 10 {
				s += fmt.Sprintf("  ... and %d more\n", len(findings)-10)
				break
			}

			s += fmt.Sprintf("  %s:%d - %s\n",
				shortenPath(f.Finding.File),
				f.Finding.Line,
				truncate(f.Finding.Message, 50))

			// Show reasoning for priority changes
			if f.AdjustedPriority != f.BasePriority {
				for _, r := range f.Reasoning[1:] { // Skip base priority reasoning
					s += fmt.Sprintf("    → %s\n", r)
				}
			}
		}
		s += "\n"
	}

	return s
}

// GetTopPriority returns the top N findings by adjusted priority.
func GetTopPriority(report *PriorityReport, n int) []PrioritizedFinding {
	if n >= len(report.Findings) {
		return report.Findings
	}
	return report.Findings[:n]
}

// max returns the larger of two ints.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
