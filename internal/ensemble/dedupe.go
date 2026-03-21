package ensemble

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// DedupeConfig controls the deduplication behavior.
type DedupeConfig struct {
	// SimilarityThreshold is the minimum similarity for two findings to be
	// considered duplicates (0.0-1.0). Default: 0.7
	SimilarityThreshold float64 `json:"similarity_threshold" yaml:"similarity_threshold"`

	// EvidenceWeight controls how much evidence pointer similarity
	// contributes to overall similarity (0.0-1.0). Default: 0.3
	EvidenceWeight float64 `json:"evidence_weight" yaml:"evidence_weight"`

	// TextWeight controls how much text similarity contributes
	// to overall similarity (0.0-1.0). Default: 0.7
	TextWeight float64 `json:"text_weight" yaml:"text_weight"`

	// PreferHighConfidence keeps the finding with higher confidence
	// when merging duplicates. Default: true
	PreferHighConfidence bool `json:"prefer_high_confidence" yaml:"prefer_high_confidence"`

	// PreserveProvenance tracks the full provenance chain through
	// deduplication. Default: true
	PreserveProvenance bool `json:"preserve_provenance" yaml:"preserve_provenance"`
}

// DefaultDedupeConfig returns sensible defaults for deduplication.
func DefaultDedupeConfig() DedupeConfig {
	return DedupeConfig{
		SimilarityThreshold:  0.7,
		EvidenceWeight:       0.3,
		TextWeight:           0.7,
		PreferHighConfidence: true,
		PreserveProvenance:   true,
	}
}

// FindingCluster represents a group of similar findings.
type FindingCluster struct {
	// ClusterID is a stable identifier derived from cluster content.
	ClusterID string `json:"cluster_id" yaml:"cluster_id"`

	// Canonical is the representative finding for this cluster.
	Canonical Finding `json:"canonical" yaml:"canonical"`

	// Members are all findings in this cluster, including the canonical one.
	Members []ClusterMember `json:"members" yaml:"members"`

	// MemberCount is the number of findings in this cluster.
	MemberCount int `json:"member_count" yaml:"member_count"`

	// SourceModes lists all modes that contributed findings to this cluster.
	SourceModes []string `json:"source_modes" yaml:"source_modes"`

	// AverageConfidence is the mean confidence across all cluster members.
	AverageConfidence Confidence `json:"average_confidence" yaml:"average_confidence"`

	// MaxConfidence is the highest confidence in the cluster.
	MaxConfidence Confidence `json:"max_confidence" yaml:"max_confidence"`

	// ProvenanceIDs tracks provenance chain IDs for all cluster members.
	ProvenanceIDs []string `json:"provenance_ids,omitempty" yaml:"provenance_ids,omitempty"`
}

// ClusterMember represents a finding within a cluster.
type ClusterMember struct {
	Finding      Finding `json:"finding" yaml:"finding"`
	SourceMode   string  `json:"source_mode" yaml:"source_mode"`
	Similarity   float64 `json:"similarity" yaml:"similarity"` // Similarity to canonical
	ProvenanceID string  `json:"provenance_id,omitempty" yaml:"provenance_id,omitempty"`
}

// DedupeResult is the output of the deduplication engine.
type DedupeResult struct {
	// GeneratedAt is when the deduplication was performed.
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`

	// Clusters are groups of deduplicated findings.
	Clusters []FindingCluster `json:"clusters" yaml:"clusters"`

	// Stats provides deduplication statistics.
	Stats DedupeStats `json:"stats" yaml:"stats"`

	// Config records the configuration used.
	Config DedupeConfig `json:"config" yaml:"config"`
}

// DedupeStats provides statistics about the deduplication operation.
type DedupeStats struct {
	// InputFindings is the total number of findings before deduplication.
	InputFindings int `json:"input_findings" yaml:"input_findings"`

	// OutputClusters is the number of clusters produced.
	OutputClusters int `json:"output_clusters" yaml:"output_clusters"`

	// DuplicatesFound is the number of duplicates detected.
	DuplicatesFound int `json:"duplicates_found" yaml:"duplicates_found"`

	// ReductionPercent is the percentage reduction (0-100).
	ReductionPercent float64 `json:"reduction_percent" yaml:"reduction_percent"`

	// LargestCluster is the size of the biggest cluster.
	LargestCluster int `json:"largest_cluster" yaml:"largest_cluster"`

	// AverageSimilarity is the mean similarity within clusters.
	AverageSimilarity float64 `json:"average_similarity" yaml:"average_similarity"`

	// ProcessingTime is how long deduplication took.
	ProcessingTime time.Duration `json:"processing_time" yaml:"processing_time"`
}

// DedupeEngine performs finding deduplication with clustering.
type DedupeEngine struct {
	config  DedupeConfig
	tracker *ProvenanceTracker
}

// dedupeFindingEntry is an internal type for tracking findings during deduplication.
type dedupeFindingEntry struct {
	finding      Finding
	sourceMode   string
	provenanceID string
}

// NewDedupeEngine creates a deduplication engine with the given config.
func NewDedupeEngine(cfg DedupeConfig) *DedupeEngine {
	return &DedupeEngine{
		config: cfg,
	}
}

// NewDedupeEngineWithProvenance creates a deduplication engine that tracks provenance.
func NewDedupeEngineWithProvenance(cfg DedupeConfig, tracker *ProvenanceTracker) *DedupeEngine {
	return &DedupeEngine{
		config:  cfg,
		tracker: tracker,
	}
}

// Dedupe performs deduplication on findings from multiple mode outputs.
func (e *DedupeEngine) Dedupe(outputs []ModeOutput) *DedupeResult {
	start := time.Now()

	slog.Info("dedupe engine starting",
		"input_modes", len(outputs),
		"threshold", e.config.SimilarityThreshold,
	)

	// Collect all findings with metadata
	var allFindings []dedupeFindingEntry

	for _, output := range outputs {
		for _, f := range output.TopFindings {
			provID := ""
			if e.tracker != nil && e.config.PreserveProvenance {
				provID = e.tracker.RecordDiscovery(output.ModeID, f)
			}
			allFindings = append(allFindings, dedupeFindingEntry{
				finding:      f,
				sourceMode:   output.ModeID,
				provenanceID: provID,
			})
		}
	}

	if len(allFindings) == 0 {
		return &DedupeResult{
			GeneratedAt: time.Now(),
			Clusters:    nil,
			Stats: DedupeStats{
				InputFindings:  0,
				OutputClusters: 0,
				ProcessingTime: time.Since(start),
			},
			Config: e.config,
		}
	}

	// Build clusters using greedy clustering
	clusters := e.buildClusters(allFindings)

	// Compute statistics
	inputCount := len(allFindings)
	outputCount := len(clusters)
	duplicates := inputCount - outputCount

	reductionPct := 0.0
	if inputCount > 0 {
		reductionPct = float64(duplicates) / float64(inputCount) * 100
	}

	largestCluster := 0
	var totalSim float64
	var simCount int

	for _, c := range clusters {
		if c.MemberCount > largestCluster {
			largestCluster = c.MemberCount
		}
		for _, m := range c.Members {
			if m.Similarity > 0 && m.Similarity < 1.0 {
				totalSim += m.Similarity
				simCount++
			}
		}
	}

	avgSim := 0.0
	if simCount > 0 {
		avgSim = totalSim / float64(simCount)
	}

	slog.Info("dedupe engine completed",
		"input_findings", inputCount,
		"output_clusters", outputCount,
		"duplicates", duplicates,
		"reduction_percent", fmt.Sprintf("%.1f%%", reductionPct),
		"duration", time.Since(start),
	)

	return &DedupeResult{
		GeneratedAt: time.Now(),
		Clusters:    clusters,
		Stats: DedupeStats{
			InputFindings:     inputCount,
			OutputClusters:    outputCount,
			DuplicatesFound:   duplicates,
			ReductionPercent:  reductionPct,
			LargestCluster:    largestCluster,
			AverageSimilarity: avgSim,
			ProcessingTime:    time.Since(start),
		},
		Config: e.config,
	}
}

// buildClusters groups similar findings into clusters.
func (e *DedupeEngine) buildClusters(findings []dedupeFindingEntry) []FindingCluster {
	if len(findings) == 0 {
		return nil
	}

	// Track which findings have been assigned to a cluster
	assigned := make([]bool, len(findings))
	var clusters []FindingCluster

	for i := 0; i < len(findings); i++ {
		if assigned[i] {
			continue
		}

		// Start a new cluster with this finding
		cluster := e.startCluster(findings[i])
		assigned[i] = true

		// Find similar findings to add to this cluster
		for j := i + 1; j < len(findings); j++ {
			if assigned[j] {
				continue
			}

			sim := e.computeSimilarity(findings[i].finding, findings[j].finding)
			if sim >= e.config.SimilarityThreshold {
				e.addToCluster(&cluster, findings[j], sim)
				assigned[j] = true

				// Record merge in provenance
				if e.tracker != nil && e.config.PreserveProvenance {
					if findings[i].provenanceID != "" && findings[j].provenanceID != "" {
						_ = e.tracker.RecordMerge(
							findings[i].provenanceID,
							[]string{findings[j].provenanceID},
							sim,
						)
					}
				}
			}
		}

		// Finalize cluster
		e.finalizeCluster(&cluster)
		clusters = append(clusters, cluster)
	}

	// Sort clusters deterministically by cluster ID
	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].ClusterID < clusters[j].ClusterID
	})

	return clusters
}

// startCluster creates a new cluster with an initial finding.
func (e *DedupeEngine) startCluster(entry dedupeFindingEntry) FindingCluster {
	member := ClusterMember{
		Finding:      entry.finding,
		SourceMode:   entry.sourceMode,
		Similarity:   1.0, // Self-similarity
		ProvenanceID: entry.provenanceID,
	}

	return FindingCluster{
		Canonical:   entry.finding,
		Members:     []ClusterMember{member},
		SourceModes: []string{entry.sourceMode},
	}
}

// addToCluster adds a finding to an existing cluster.
func (e *DedupeEngine) addToCluster(cluster *FindingCluster, entry dedupeFindingEntry, similarity float64) {
	member := ClusterMember{
		Finding:      entry.finding,
		SourceMode:   entry.sourceMode,
		Similarity:   similarity,
		ProvenanceID: entry.provenanceID,
	}
	cluster.Members = append(cluster.Members, member)

	// Add source mode if not already present
	found := false
	for _, m := range cluster.SourceModes {
		if m == entry.sourceMode {
			found = true
			break
		}
	}
	if !found {
		cluster.SourceModes = append(cluster.SourceModes, entry.sourceMode)
	}

	// Update canonical if this has higher confidence
	if e.config.PreferHighConfidence && entry.finding.Confidence > cluster.Canonical.Confidence {
		cluster.Canonical = entry.finding
	}
}

// finalizeCluster computes derived fields and generates stable cluster ID.
func (e *DedupeEngine) finalizeCluster(cluster *FindingCluster) {
	// Sort members deterministically (by source mode, then finding text)
	sort.Slice(cluster.Members, func(i, j int) bool {
		if cluster.Members[i].SourceMode != cluster.Members[j].SourceMode {
			return cluster.Members[i].SourceMode < cluster.Members[j].SourceMode
		}
		return cluster.Members[i].Finding.Finding < cluster.Members[j].Finding.Finding
	})

	// Sort source modes
	sort.Strings(cluster.SourceModes)

	// Compute member count
	cluster.MemberCount = len(cluster.Members)

	// Compute confidence stats
	var sumConf, maxConf Confidence
	for _, m := range cluster.Members {
		sumConf += m.Finding.Confidence
		if m.Finding.Confidence > maxConf {
			maxConf = m.Finding.Confidence
		}
	}
	if cluster.MemberCount > 0 {
		cluster.AverageConfidence = sumConf / Confidence(cluster.MemberCount)
	}
	cluster.MaxConfidence = maxConf

	// Collect provenance IDs
	for _, m := range cluster.Members {
		if m.ProvenanceID != "" {
			cluster.ProvenanceIDs = append(cluster.ProvenanceIDs, m.ProvenanceID)
		}
	}

	// Generate stable cluster ID from content hash
	cluster.ClusterID = e.generateClusterID(cluster)
}

// generateClusterID creates a stable hash-based ID for the cluster.
func (e *DedupeEngine) generateClusterID(cluster *FindingCluster) string {
	h := sha256.New()

	// Include all member finding texts in sorted order for stability
	texts := make([]string, len(cluster.Members))
	for i, m := range cluster.Members {
		texts[i] = m.Finding.Finding
	}
	sort.Strings(texts)

	for _, t := range texts {
		h.Write([]byte(t))
		h.Write([]byte{0})
	}

	return "clu-" + hex.EncodeToString(h.Sum(nil))[:8]
}

// computeSimilarity calculates similarity between two findings.
func (e *DedupeEngine) computeSimilarity(a, b Finding) float64 {
	// Normalize weights
	totalWeight := e.config.TextWeight + e.config.EvidenceWeight
	if totalWeight == 0 {
		totalWeight = 1.0
	}
	textWeight := e.config.TextWeight / totalWeight
	evidenceWeight := e.config.EvidenceWeight / totalWeight

	// Text similarity using Jaccard
	textA := normalizeText(a.Finding)
	textB := normalizeText(b.Finding)
	tokensA := tokenize(textA)
	tokensB := tokenize(textB)
	textSim := jaccardSimilarity(tokensA, tokensB)

	// Evidence similarity
	evidenceSim := evidenceProximity(a.EvidencePointer, b.EvidencePointer)

	// Combined weighted similarity
	combined := textWeight*textSim + evidenceWeight*evidenceSim

	slog.Debug("dedupe similarity computed",
		"text_sim", fmt.Sprintf("%.3f", textSim),
		"evidence_sim", fmt.Sprintf("%.3f", evidenceSim),
		"combined", fmt.Sprintf("%.3f", combined),
	)

	return combined
}

// GetCanonicalFindings returns just the canonical finding from each cluster.
func (r *DedupeResult) GetCanonicalFindings() []Finding {
	if r == nil {
		return nil
	}
	findings := make([]Finding, len(r.Clusters))
	for i, c := range r.Clusters {
		findings[i] = c.Canonical
	}
	return findings
}

// GetClusterByID returns a cluster by its ID, or nil if not found.
func (r *DedupeResult) GetClusterByID(id string) *FindingCluster {
	if r == nil {
		return nil
	}
	for i := range r.Clusters {
		if r.Clusters[i].ClusterID == id {
			return &r.Clusters[i]
		}
	}
	return nil
}

// Render produces a human-readable summary of deduplication results.
func (r *DedupeResult) Render() string {
	if r == nil {
		return "No deduplication results"
	}

	var b strings.Builder
	b.WriteString("Deduplication Results:\n")
	b.WriteString(strings.Repeat("-", 50) + "\n\n")

	fmt.Fprintf(&b, "Input Findings:    %d\n", r.Stats.InputFindings)
	fmt.Fprintf(&b, "Output Clusters:   %d\n", r.Stats.OutputClusters)
	fmt.Fprintf(&b, "Duplicates Found:  %d\n", r.Stats.DuplicatesFound)
	fmt.Fprintf(&b, "Reduction:         %.1f%%\n", r.Stats.ReductionPercent)
	fmt.Fprintf(&b, "Largest Cluster:   %d members\n", r.Stats.LargestCluster)
	fmt.Fprintf(&b, "Avg Similarity:    %.2f\n", r.Stats.AverageSimilarity)
	fmt.Fprintf(&b, "Processing Time:   %s\n\n", r.Stats.ProcessingTime)

	b.WriteString("Clusters:\n")
	for i, c := range r.Clusters {
		fmt.Fprintf(&b, "\n%d. [%s] (%d members, conf: %.2f)\n",
			i+1, c.ClusterID, c.MemberCount, c.MaxConfidence)
		fmt.Fprintf(&b, "   Canonical: %s\n", truncateDedupText(c.Canonical.Finding, 60))
		if c.Canonical.EvidencePointer != "" {
			fmt.Fprintf(&b, "   Evidence: %s\n", c.Canonical.EvidencePointer)
		}
		fmt.Fprintf(&b, "   Sources: %s\n", strings.Join(c.SourceModes, ", "))
	}

	return b.String()
}

// truncateDedupText limits text length with ellipsis.
// Named differently to avoid conflict with provenance.go.
func truncateDedupText(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return "..."[:maxLen]
	}
	targetLen := maxLen - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	return s[:prevI] + "..."
}

// DedupeFindings is a convenience function for deduplicating findings.
func DedupeFindings(outputs []ModeOutput) *DedupeResult {
	return NewDedupeEngine(DefaultDedupeConfig()).Dedupe(outputs)
}

// DedupeFindingsWithConfig allows custom configuration.
func DedupeFindingsWithConfig(outputs []ModeOutput, cfg DedupeConfig) *DedupeResult {
	return NewDedupeEngine(cfg).Dedupe(outputs)
}

// DedupeFindingsWithProvenance includes provenance tracking.
func DedupeFindingsWithProvenance(outputs []ModeOutput, cfg DedupeConfig, tracker *ProvenanceTracker) *DedupeResult {
	return NewDedupeEngineWithProvenance(cfg, tracker).Dedupe(outputs)
}
