// Package scanner provides UBS integration with deduplication support.
package scanner

import (
	"sync"
)

// DedupIndex maintains an index of existing findings for deduplication.
// All methods are safe for concurrent use.
type DedupIndex struct {
	mu sync.RWMutex
	// BySignature maps finding signatures to bead IDs
	BySignature map[string]string
	// ByFile maps file paths to finding signatures in that file
	ByFile map[string][]string
	// Total count of indexed beads
	Total int
}

// DedupStats returns statistics about the dedup index.
type DedupStats struct {
	TotalBeads  int `json:"total_beads"`
	UniqueFiles int `json:"unique_files"`
}

// FindDuplicatesInFindings identifies findings that already have beads.
type DuplicateInfo struct {
	Finding Finding `json:"finding"`
	BeadID  string  `json:"bead_id"`
}
