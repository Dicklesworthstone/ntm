package ensemble

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProvenanceStep represents a transformation in a finding's lifecycle.
type ProvenanceStep struct {
	// Stage is the processing stage (discovery, dedupe, synthesis).
	Stage string `json:"stage"`

	// Action describes what happened (e.g., "merged", "filtered", "cited").
	Action string `json:"action"`

	// Details provides additional context about the transformation.
	Details string `json:"details,omitempty"`

	// Timestamp is when this step occurred.
	Timestamp time.Time `json:"timestamp"`

	// RelatedIDs lists other finding IDs involved (e.g., for merges).
	RelatedIDs []string `json:"related_ids,omitempty"`
}

// ProvenanceChain tracks the full lifecycle of a finding.
type ProvenanceChain struct {
	// FindingID is the stable hash identifying this finding.
	FindingID string `json:"finding_id"`

	// SourceMode is the original mode that discovered this finding.
	SourceMode string `json:"source_mode"`

	// ContextHash is a hash of the ensemble context (question + modes).
	ContextHash string `json:"context_hash"`

	// OriginalText is the finding text as first discovered.
	OriginalText string `json:"original_text"`

	// CurrentText is the current finding text (may differ after transforms).
	CurrentText string `json:"current_text"`

	// Impact is the finding's impact level.
	Impact ImpactLevel `json:"impact"`

	// Confidence is the finding's confidence score.
	Confidence Confidence `json:"confidence"`

	// Steps records all transformations in order.
	Steps []ProvenanceStep `json:"steps"`

	// CreatedAt is when the finding was first discovered.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the provenance was last updated.
	UpdatedAt time.Time `json:"updated_at"`

	// MergedFrom lists finding IDs that were merged into this one.
	MergedFrom []string `json:"merged_from,omitempty"`

	// MergedInto is the finding ID this was merged into (if applicable).
	MergedInto string `json:"merged_into,omitempty"`

	// SynthesisCitations tracks where this finding was cited in synthesis.
	SynthesisCitations []string `json:"synthesis_citations,omitempty"`
}

// AddStep appends a transformation step to the chain.
func (p *ProvenanceChain) AddStep(stage, action, details string, relatedIDs ...string) {
	step := ProvenanceStep{
		Stage:      stage,
		Action:     action,
		Details:    details,
		Timestamp:  time.Now(),
		RelatedIDs: relatedIDs,
	}
	p.Steps = append(p.Steps, step)
	p.UpdatedAt = step.Timestamp
}

// IsActive returns true if this finding wasn't merged into another.
func (p *ProvenanceChain) IsActive() bool {
	return p.MergedInto == ""
}

// ProvenanceTracker manages provenance chains for an ensemble run.
type ProvenanceTracker struct {
	mu          sync.RWMutex
	chains      map[string]*ProvenanceChain
	contextHash string
}

// NewProvenanceTracker creates a tracker for an ensemble run.
func NewProvenanceTracker(question string, modeIDs []string) *ProvenanceTracker {
	// Create context hash from question and modes
	h := sha256.New()
	h.Write([]byte(question))
	for _, m := range modeIDs {
		h.Write([]byte(m))
	}
	contextHash := hex.EncodeToString(h.Sum(nil))[:16]

	return &ProvenanceTracker{
		chains:      make(map[string]*ProvenanceChain),
		contextHash: contextHash,
	}
}

// GenerateFindingID creates a stable hash for a finding.
// The ID is based on the source mode and finding text.
func GenerateFindingID(modeID, findingText string) string {
	h := sha256.New()
	h.Write([]byte(modeID))
	h.Write([]byte(normalizeText(findingText)))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// RecordDiscovery tracks a finding being discovered by a mode.
func (t *ProvenanceTracker) RecordDiscovery(modeID string, finding Finding) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	findingID := GenerateFindingID(modeID, finding.Finding)

	chain := &ProvenanceChain{
		FindingID:    findingID,
		SourceMode:   modeID,
		ContextHash:  t.contextHash,
		OriginalText: finding.Finding,
		CurrentText:  finding.Finding,
		Impact:       finding.Impact,
		Confidence:   finding.Confidence,
		Steps:        make([]ProvenanceStep, 0, 4),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	chain.AddStep("discovery", "discovered", fmt.Sprintf("Found by mode %s with confidence %.2f", modeID, finding.Confidence))

	t.chains[findingID] = chain
	return findingID
}

// RecordMerge tracks findings being merged during deduplication.
func (t *ProvenanceTracker) RecordMerge(primaryID string, mergedIDs []string, similarity float64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	primary, ok := t.chains[primaryID]
	if !ok {
		return fmt.Errorf("primary finding %s not found", primaryID)
	}

	// Record merge in primary
	primary.AddStep("dedupe", "merged", fmt.Sprintf("Merged %d similar findings (similarity=%.2f)", len(mergedIDs), similarity), mergedIDs...)
	primary.MergedFrom = append(primary.MergedFrom, mergedIDs...)

	// Mark merged findings as inactive
	for _, id := range mergedIDs {
		if merged, ok := t.chains[id]; ok {
			merged.AddStep("dedupe", "absorbed", fmt.Sprintf("Merged into %s (similarity=%.2f)", primaryID, similarity), primaryID)
			merged.MergedInto = primaryID
		}
	}

	return nil
}

// RecordFilter tracks a finding being filtered out.
func (t *ProvenanceTracker) RecordFilter(findingID, reason string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	chain, ok := t.chains[findingID]
	if !ok {
		return fmt.Errorf("finding %s not found", findingID)
	}

	chain.AddStep("filter", "filtered", reason)
	return nil
}

// RecordSynthesisCitation tracks a finding being cited in synthesis output.
func (t *ProvenanceTracker) RecordSynthesisCitation(findingID, synthesisLocation string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	chain, ok := t.chains[findingID]
	if !ok {
		return fmt.Errorf("finding %s not found", findingID)
	}

	chain.AddStep("synthesis", "cited", fmt.Sprintf("Cited in %s", synthesisLocation), synthesisLocation)
	chain.SynthesisCitations = append(chain.SynthesisCitations, synthesisLocation)
	return nil
}

// GetChain retrieves the provenance chain for a finding.
func (t *ProvenanceTracker) GetChain(findingID string) (*ProvenanceChain, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	chain, ok := t.chains[findingID]
	if !ok {
		return nil, false
	}
	// Return a copy to avoid race conditions
	cpy := *chain
	cpy.Steps = make([]ProvenanceStep, len(chain.Steps))
	copy(cpy.Steps, chain.Steps)
	return &cpy, true
}

// ListChains returns all provenance chains.
func (t *ProvenanceTracker) ListChains() []*ProvenanceChain {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]*ProvenanceChain, 0, len(t.chains))
	for _, chain := range t.chains {
		cpy := *chain
		cpy.Steps = make([]ProvenanceStep, len(chain.Steps))
		copy(cpy.Steps, chain.Steps)
		result = append(result, &cpy)
	}

	// Sort by creation time
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	return result
}

// Count returns the total number of tracked findings.
func (t *ProvenanceTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.chains)
}

// ActiveCount returns the number of non-merged findings.
func (t *ProvenanceTracker) ActiveCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	count := 0
	for _, chain := range t.chains {
		if chain.IsActive() {
			count++
		}
	}
	return count
}

// ProvenanceStats provides summary statistics.
type ProvenanceStats struct {
	TotalFindings  int            `json:"total_findings"`
	ActiveFindings int            `json:"active_findings"`
	MergedFindings int            `json:"merged_findings"`
	FilteredCount  int            `json:"filtered_count"`
	CitedCount     int            `json:"cited_count"`
	ModeBreakdown  map[string]int `json:"mode_breakdown"`
}

// Stats returns provenance statistics.
func (t *ProvenanceTracker) Stats() ProvenanceStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.computeStatsLocked()
}

func (t *ProvenanceTracker) computeStatsLocked() ProvenanceStats {
	stats := ProvenanceStats{
		ModeBreakdown: make(map[string]int),
	}

	for _, chain := range t.chains {
		stats.TotalFindings++
		stats.ModeBreakdown[chain.SourceMode]++

		if chain.IsActive() {
			stats.ActiveFindings++
		} else {
			stats.MergedFindings++
		}

		if len(chain.SynthesisCitations) > 0 {
			stats.CitedCount++
		}

		// Count filtered findings
		for _, step := range chain.Steps {
			if step.Action == "filtered" {
				stats.FilteredCount++
				break
			}
		}
	}

	return stats
}

// FormatProvenance formats a chain for human-readable output.
func FormatProvenance(chain *ProvenanceChain) string {
	if chain == nil {
		return "No provenance found"
	}

	var b strings.Builder

	// Header
	fmt.Fprintf(&b, "Finding: %s\n", chain.FindingID)
	fmt.Fprintf(&b, "Source:  %s\n", chain.SourceMode)
	fmt.Fprintf(&b, "Context: %s\n", chain.ContextHash)
	fmt.Fprintf(&b, "Impact:  %s | Confidence: %s\n", chain.Impact, chain.Confidence)
	b.WriteString("\n")

	// Text
	fmt.Fprintf(&b, "Original: %s\n", truncateText(chain.OriginalText, 100))
	if chain.CurrentText != chain.OriginalText {
		fmt.Fprintf(&b, "Current:  %s\n", truncateText(chain.CurrentText, 100))
	}
	b.WriteString("\n")

	// Status
	if chain.MergedInto != "" {
		fmt.Fprintf(&b, "Status: Merged into %s\n\n", chain.MergedInto)
	} else if len(chain.MergedFrom) > 0 {
		fmt.Fprintf(&b, "Status: Active (merged %d findings)\n\n", len(chain.MergedFrom))
	} else {
		b.WriteString("Status: Active\n\n")
	}

	// Timeline
	b.WriteString("Timeline:\n")
	for i, step := range chain.Steps {
		marker := "├─"
		if i == len(chain.Steps)-1 {
			marker = "└─"
		}
		fmt.Fprintf(&b, "  %s [%s] %s: %s\n",
			marker,
			step.Timestamp.Format("15:04:05"),
			step.Stage,
			step.Action,
		)
		if step.Details != "" {
			indent := "  │ "
			if i == len(chain.Steps)-1 {
				indent = "    "
			}
			fmt.Fprintf(&b, "%s  %s\n", indent, step.Details)
		}
	}

	// Citations
	if len(chain.SynthesisCitations) > 0 {
		b.WriteString("\nSynthesis Citations:\n")
		for _, cite := range chain.SynthesisCitations {
			fmt.Fprintf(&b, "  • %s\n", cite)
		}
	}

	return b.String()
}

// truncateText shortens text to maxLen characters with ellipsis.
func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen-3] + "..."
}

// Validate checks that a provenance chain is valid.
func (p *ProvenanceChain) Validate() error {
	if p.FindingID == "" {
		return errors.New("finding_id is required")
	}
	if p.SourceMode == "" {
		return errors.New("source_mode is required")
	}
	if p.OriginalText == "" {
		return errors.New("original_text is required")
	}
	if len(p.Steps) == 0 {
		return errors.New("at least one step is required")
	}
	return nil
}
