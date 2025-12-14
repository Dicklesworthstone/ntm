// Package scanner provides UBS integration with bv graph analysis.
package scanner

import (
	"fmt"
	"sort"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// ImpactAnalysis represents a finding's impact in the dependency graph.
type ImpactAnalysis struct {
	Finding       Finding           `json:"finding"`
	BeadID        string            `json:"bead_id,omitempty"`        // If created as a bead
	GraphPosition *bv.GraphPosition `json:"graph_position,omitempty"` // Position in dependency graph
	BlocksCount   int               `json:"blocks_count"`             // Number of tasks blocked by this
	ImpactScore   float64           `json:"impact_score"`             // Combined severity + graph centrality
}

// Hotspot represents a file with multiple findings and high centrality.
type Hotspot struct {
	File         string    `json:"file"`
	FindingCount int       `json:"finding_count"`
	Critical     int       `json:"critical"`
	Warning      int       `json:"warning"`
	Info         int       `json:"info"`
	Centrality   float64   `json:"centrality"` // Max centrality of related beads
	ImpactScore  float64   `json:"impact_score"`
	Findings     []Finding `json:"findings,omitempty"`
}

// AnalysisResult contains impact and hotspot analysis results.
type AnalysisResult struct {
	HighImpactFindings []ImpactAnalysis `json:"high_impact_findings"`
	Hotspots           []Hotspot        `json:"hotspots"`
	RecommendedOrder   []ImpactAnalysis `json:"recommended_order"`
	TotalFindings      int              `json:"total_findings"`
	GraphAvailable     bool             `json:"graph_available"`
}

// AnalyzeImpact analyzes scan findings against the beads dependency graph.
// Returns findings sorted by their impact on project progress.
func AnalyzeImpact(result *ScanResult, existingBeadIDs map[string]string) (*AnalysisResult, error) {
	ar := &AnalysisResult{
		HighImpactFindings: make([]ImpactAnalysis, 0),
		Hotspots:           make([]Hotspot, 0),
		RecommendedOrder:   make([]ImpactAnalysis, 0),
		TotalFindings:      len(result.Findings),
	}

	// Check if bv is available
	if !bv.IsInstalled() {
		ar.GraphAvailable = false
		// Still provide basic analysis without graph
		for _, f := range result.Findings {
			ar.HighImpactFindings = append(ar.HighImpactFindings, ImpactAnalysis{
				Finding:     f,
				ImpactScore: severityToScore(f.Severity),
			})
		}
		sortByImpact(ar.HighImpactFindings)
		ar.RecommendedOrder = ar.HighImpactFindings
		ar.Hotspots = computeHotspots(result.Findings, nil)
		return ar, nil
	}

	ar.GraphAvailable = true

	// Get bv insights for graph analysis
	insights, err := bv.GetInsights(result.Project)
	if err != nil {
		// Fall back to basic analysis
		ar.GraphAvailable = false
		for _, f := range result.Findings {
			ar.HighImpactFindings = append(ar.HighImpactFindings, ImpactAnalysis{
				Finding:     f,
				ImpactScore: severityToScore(f.Severity),
			})
		}
		sortByImpact(ar.HighImpactFindings)
		ar.RecommendedOrder = ar.HighImpactFindings
		ar.Hotspots = computeHotspots(result.Findings, nil)
		return ar, nil
	}

	// Build lookup maps for graph metrics
	bottleneckMap := make(map[string]float64)
	for _, b := range insights.Bottlenecks {
		bottleneckMap[b.ID] = b.Value
	}

	keystoneMap := make(map[string]float64)
	for _, k := range insights.Keystones {
		keystoneMap[k.ID] = k.Value
	}

	// Analyze each finding
	for _, f := range result.Findings {
		analysis := ImpactAnalysis{
			Finding:     f,
			ImpactScore: severityToScore(f.Severity),
		}

		// Check if this finding has a corresponding bead
		sig := FindingSignature(f)
		if beadID, ok := existingBeadIDs[sig]; ok {
			analysis.BeadID = beadID

			// Get graph position for this bead
			pos, err := bv.GetGraphPosition(result.Project, beadID)
			if err == nil {
				analysis.GraphPosition = pos

				// Enhance impact score with graph metrics
				if pos.IsBottleneck {
					analysis.BlocksCount = int(pos.BottleneckScore)
					analysis.ImpactScore += pos.BottleneckScore * 2.0 // Bottlenecks are critical
				}
				if pos.IsKeystone {
					analysis.ImpactScore += pos.KeystoneScore * 1.5
				}
			}
		}

		ar.HighImpactFindings = append(ar.HighImpactFindings, analysis)
	}

	// Sort by impact
	sortByImpact(ar.HighImpactFindings)

	// Filter for actually high-impact findings (top 50% or score > 5)
	threshold := 5.0
	for _, a := range ar.HighImpactFindings {
		if a.ImpactScore >= threshold || a.BlocksCount > 0 {
			ar.RecommendedOrder = append(ar.RecommendedOrder, a)
		}
	}
	if len(ar.RecommendedOrder) == 0 && len(ar.HighImpactFindings) > 0 {
		// Include at least the top items
		limit := len(ar.HighImpactFindings)
		if limit > 10 {
			limit = 10
		}
		ar.RecommendedOrder = ar.HighImpactFindings[:limit]
	}

	// Compute hotspots with graph centrality
	ar.Hotspots = computeHotspots(result.Findings, keystoneMap)

	return ar, nil
}

// severityToScore converts severity to a base impact score.
func severityToScore(sev Severity) float64 {
	switch sev {
	case SeverityCritical:
		return 10.0
	case SeverityWarning:
		return 5.0
	case SeverityInfo:
		return 1.0
	default:
		return 2.0
	}
}

// sortByImpact sorts findings by impact score descending.
func sortByImpact(findings []ImpactAnalysis) {
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].ImpactScore > findings[j].ImpactScore
	})
}

// computeHotspots groups findings by file and calculates hotspot scores.
func computeHotspots(findings []Finding, keystoneMap map[string]float64) []Hotspot {
	// Group findings by file
	byFile := make(map[string]*Hotspot)

	for _, f := range findings {
		h, ok := byFile[f.File]
		if !ok {
			h = &Hotspot{
				File:     f.File,
				Findings: make([]Finding, 0),
			}
			byFile[f.File] = h
		}

		h.FindingCount++
		h.Findings = append(h.Findings, f)

		switch f.Severity {
		case SeverityCritical:
			h.Critical++
		case SeverityWarning:
			h.Warning++
		case SeverityInfo:
			h.Info++
		}
	}

	// Convert to slice and calculate impact scores
	hotspots := make([]Hotspot, 0, len(byFile))
	for _, h := range byFile {
		// Base score from findings
		h.ImpactScore = float64(h.Critical)*10.0 + float64(h.Warning)*5.0 + float64(h.Info)*1.0

		// Add centrality bonus if we have keystone data
		// Note: keystoneMap keys are bead IDs, not files, so this is approximate
		// A more sophisticated approach would track file→bead relationships
		if keystoneMap != nil {
			h.Centrality = estimateFileCentrality(h.File, keystoneMap)
			h.ImpactScore += h.Centrality * 3.0
		}

		hotspots = append(hotspots, *h)
	}

	// Sort by impact score
	sort.Slice(hotspots, func(i, j int) bool {
		return hotspots[i].ImpactScore > hotspots[j].ImpactScore
	})

	return hotspots
}

// estimateFileCentrality estimates file centrality from keystone data.
// This is a heuristic - in practice, we'd need file→bead mappings.
func estimateFileCentrality(file string, keystoneMap map[string]float64) float64 {
	// For now, return 0 as we don't have direct file→bead mapping
	// A future improvement would track this relationship
	return 0.0
}

// FormatImpactReport generates a human-readable impact report.
func FormatImpactReport(ar *AnalysisResult) string {
	var s string

	s += fmt.Sprintf("Scan Impact Analysis\n")
	s += fmt.Sprintf("════════════════════════════════════════════════════\n\n")

	if !ar.GraphAvailable {
		s += fmt.Sprintf("Note: bv not available, showing severity-based analysis only\n\n")
	}

	// High-impact findings
	if len(ar.RecommendedOrder) > 0 {
		s += fmt.Sprintf("High-Impact Findings (by downstream blockers):\n")
		for i, a := range ar.RecommendedOrder {
			if i >= 10 {
				s += fmt.Sprintf("  ... and %d more\n", len(ar.RecommendedOrder)-10)
				break
			}
			blocksInfo := ""
			if a.BlocksCount > 0 {
				blocksInfo = fmt.Sprintf(" - blocks %d tasks", a.BlocksCount)
			}
			s += fmt.Sprintf("  %d. [%s] %s in %s:%d%s\n",
				i+1,
				formatPriority(a.Finding.Severity),
				truncate(a.Finding.Message, 40),
				shortenPath(a.Finding.File),
				a.Finding.Line,
				blocksInfo)
		}
		s += "\n"
	}

	// Hotspots
	if len(ar.Hotspots) > 0 {
		s += fmt.Sprintf("Quality Hotspots:\n")
		for i, h := range ar.Hotspots {
			if i >= 5 {
				break
			}
			centralityInfo := ""
			if h.Centrality > 0 {
				centralityInfo = fmt.Sprintf(", centrality: %.2f", h.Centrality)
			}
			s += fmt.Sprintf("  %d. %s - %d findings%s\n",
				i+1,
				h.File,
				h.FindingCount,
				centralityInfo)
		}
		s += "\n"
	}

	// Recommended fix order
	if len(ar.RecommendedOrder) > 0 {
		s += fmt.Sprintf("Recommended Fix Order:\n")
		for i, a := range ar.RecommendedOrder {
			if i >= 5 {
				break
			}
			s += fmt.Sprintf("  %d. %s:%d (impact score: %.1f)\n",
				i+1,
				shortenPath(a.Finding.File),
				a.Finding.Line,
				a.ImpactScore)
		}
	}

	return s
}

// formatPriority returns a priority string from severity.
func formatPriority(sev Severity) string {
	switch sev {
	case SeverityCritical:
		return "P0"
	case SeverityWarning:
		return "P1"
	case SeverityInfo:
		return "P3"
	default:
		return "P2"
	}
}

// truncate truncates a string to maxLen with ellipsis.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
