// Package scanner provides UBS integration with deduplication support.
package scanner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// DedupIndex maintains an index of existing findings for deduplication.
type DedupIndex struct {
	// BySignature maps finding signatures to bead IDs
	BySignature map[string]string
	// ByFile maps file paths to finding signatures in that file
	ByFile map[string][]string
	// Total count of indexed beads
	Total int
}

// NewDedupIndex creates a new deduplication index from existing beads.
func NewDedupIndex() (*DedupIndex, error) {
	idx := &DedupIndex{
		BySignature: make(map[string]string),
		ByFile:      make(map[string][]string),
	}

	if err := idx.Refresh(); err != nil {
		return nil, err
	}

	return idx, nil
}

// Refresh reloads the index from the beads database.
func (idx *DedupIndex) Refresh() error {
	// Query all open UBS beads
	output, err := bv.RunBd("", "list", "--json", "--labels=ubs-scan", "--status=open,in_progress")
	if err != nil {
		// bd might not be installed or no beads exist
		return nil
	}

	if output == "" {
		return nil
	}

	var beads []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(output), &beads); err != nil {
		return fmt.Errorf("parsing beads: %w", err)
	}

	// Clear existing index
	idx.BySignature = make(map[string]string)
	idx.ByFile = make(map[string][]string)
	idx.Total = 0

	for _, b := range beads {
		sig, file := parseBeadForDedup(b.Title, b.Description)
		if sig == "" {
			continue
		}

		idx.BySignature[sig] = b.ID
		if file != "" {
			idx.ByFile[file] = append(idx.ByFile[file], sig)
		}
		idx.Total++
	}

	return nil
}

// Exists returns true if a finding already exists as a bead.
func (idx *DedupIndex) Exists(f Finding) bool {
	sig := FindingSignature(f)
	_, exists := idx.BySignature[sig]
	return exists
}

// GetBeadID returns the bead ID for a finding if it exists.
func (idx *DedupIndex) GetBeadID(f Finding) (string, bool) {
	sig := FindingSignature(f)
	id, exists := idx.BySignature[sig]
	return id, exists
}

// Add registers a new finding in the index (after creating a bead).
func (idx *DedupIndex) Add(f Finding, beadID string) {
	sig := FindingSignature(f)
	idx.BySignature[sig] = beadID
	idx.ByFile[f.File] = append(idx.ByFile[f.File], sig)
	idx.Total++
}

// FindingsForFile returns signatures of all indexed findings in a file.
func (idx *DedupIndex) FindingsForFile(file string) []string {
	return idx.ByFile[file]
}

// parseBeadForDedup extracts signature and file from bead title/description.
func parseBeadForDedup(title, desc string) (signature, file string) {
	// Extract file:line from description
	lines := strings.Split(desc, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "**File:**") {
			start := strings.Index(line, "`")
			end := strings.LastIndex(line, "`")
			if start != -1 && end > start {
				fileLine := line[start+1 : end]
				// Split by : to get file path
				parts := strings.SplitN(fileLine, ":", 3)
				if len(parts) >= 2 {
					file = parts[0]
					// Reconstruct signature: file:line:rule or file:line:ubs
					signature = fileLine + ":ubs"
				}
			}
			break
		}
	}

	// Try to extract rule from title: [SEVERITY] rule_id: message
	if strings.HasPrefix(title, "[") {
		bracketEnd := strings.Index(title, "]")
		if bracketEnd > 0 {
			rest := strings.TrimSpace(title[bracketEnd+1:])
			colonIdx := strings.Index(rest, ":")
			if colonIdx > 0 {
				rule := strings.TrimSpace(rest[:colonIdx])
				if !strings.Contains(rule, " ") && file != "" {
					// Looks like a rule ID
					parts := strings.SplitN(signature, ":", 3)
					if len(parts) >= 2 {
						signature = parts[0] + ":" + parts[1] + ":" + rule
					}
				}
			}
		}
	}

	return signature, file
}

// DedupStats returns statistics about the dedup index.
type DedupStats struct {
	TotalBeads  int `json:"total_beads"`
	UniqueFiles int `json:"unique_files"`
}

// Stats returns statistics about the current index.
func (idx *DedupIndex) Stats() DedupStats {
	return DedupStats{
		TotalBeads:  idx.Total,
		UniqueFiles: len(idx.ByFile),
	}
}

// FindDuplicatesInFindings identifies findings that already have beads.
type DuplicateInfo struct {
	Finding Finding `json:"finding"`
	BeadID  string  `json:"bead_id"`
}

// CheckFindings returns which findings are duplicates of existing beads.
func (idx *DedupIndex) CheckFindings(findings []Finding) (new []Finding, duplicates []DuplicateInfo) {
	for _, f := range findings {
		if id, exists := idx.GetBeadID(f); exists {
			duplicates = append(duplicates, DuplicateInfo{
				Finding: f,
				BeadID:  id,
			})
		} else {
			new = append(new, f)
		}
	}
	return new, duplicates
}
