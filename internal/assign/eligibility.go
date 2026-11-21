package assign

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SkippedBead describes a work item excluded by assignment eligibility or
// dependency checks.
type SkippedBead struct {
	BeadID       string   `json:"bead_id"`
	BeadTitle    string   `json:"bead_title"`
	Reason       string   `json:"reason"`
	BlockedByIDs []string `json:"blocked_by_ids,omitempty"`
}

// CandidateWorkItem captures the normalized tracker metadata evaluated during
// assignment planning and live mutation preflight.
type CandidateWorkItem struct {
	ID         string
	Title      string
	Type       string
	Status     string
	Priority   int
	Labels     []string
	BlockedBy  []string
	DeferUntil *time.Time
	Pinned     bool
	Ephemeral  bool
	Template   bool
	Wisp       bool
}

// DependencyStatus represents one blocking dependency's ID and status.
type DependencyStatus struct {
	ID     string
	Status string
}

// NormalizeBeadStatus folds case and delimiter variation ("in-progress",
// "In_Progress", "IN PROGRESS") into a canonical lowercase underscore form.
func NormalizeBeadStatus(status string) string {
	canonical := strings.ToLower(strings.TrimSpace(status))
	canonical = strings.ReplaceAll(canonical, "-", "_")
	canonical = strings.ReplaceAll(canonical, " ", "_")
	return canonical
}

// ClassifyWorkItem checks whether a triage/plan work item is ineligible for
// assignment, returning nil when the item is assignable.
func ClassifyWorkItem(
	item CandidateWorkItem,
	activeAssignments map[string]struct{},
	isContainerType func(string) bool,
	operatorGated func(string) bool,
) *SkippedBead {
	if len(item.BlockedBy) > 0 {
		return &SkippedBead{
			BeadID:       item.ID,
			BeadTitle:    item.Title,
			Reason:       "blocked_by_dependency",
			BlockedByIDs: item.BlockedBy,
		}
	}

	// Container types (epic) are grouping nodes, not implementation work: an
	// open unlabeled epic must never be dispatched to a worker pane.
	if isContainerType != nil && isContainerType(item.Type) {
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "container_issue_type"}
	}

	if operatorGated != nil {
		for _, label := range item.Labels {
			if operatorGated(label) {
				return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "operator_gated"}
			}
		}
	}

	switch NormalizeBeadStatus(item.Status) {
	case "open", "ready":
		// Assignable status; fall through to active-assignment check.
	case "":
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "not_open_status"}
	case "in_progress":
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "already_in_progress"}
	case "blocked":
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "blocked_status"}
	case "closed", "resolved", "done":
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "closed_status"}
	default:
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "not_open_status"}
	}

	if _, ok := activeAssignments[item.ID]; ok {
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "already_assigned"}
	}

	return nil
}

// ClassifyLiveWorkItem checks live tracker details right before mutation,
// adding deferral, pin, ephemeral, template, and wisp guards on top of
// ClassifyWorkItem.
func ClassifyLiveWorkItem(
	item CandidateWorkItem,
	activeAssignments map[string]struct{},
	isContainerType func(string) bool,
	operatorGated func(string) bool,
	now time.Time,
) *SkippedBead {
	if skip := ClassifyWorkItem(item, nil, isContainerType, operatorGated); skip != nil {
		return skip
	}
	if item.DeferUntil != nil && item.DeferUntil.After(now) {
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "deferred"}
	}
	if item.Pinned {
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "pinned"}
	}
	if item.Ephemeral {
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "ephemeral"}
	}
	if item.Template {
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "template"}
	}
	if item.Wisp || strings.Contains(strings.ToLower(item.ID), "-wisp-") {
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "wisp"}
	}
	if _, ok := activeAssignments[item.ID]; ok {
		return &SkippedBead{BeadID: item.ID, BeadTitle: item.Title, Reason: "already_assigned"}
	}
	return nil
}

// DependencyRelationToCompleted inspects blocking dependencies for a dependent
// bead, reporting whether it referenced completedBeadID and listing any
// non-terminal blocking dependency IDs sorted deterministically.
func DependencyRelationToCompleted(states []DependencyStatus, completedBeadID string) (bool, []string) {
	related := false
	unresolved := make([]string, 0)
	for _, st := range states {
		if st.ID == completedBeadID {
			related = true
		}
		switch NormalizeBeadStatus(st.Status) {
		case "closed", "tombstone":
		default:
			unresolved = append(unresolved, st.ID)
		}
	}
	sort.Strings(unresolved)
	return related, unresolved
}

// ValidateCycles filters raw dependency cycles, rejecting malformed cycles with
// fewer than 2 nodes or duplicate nodes and returning diagnostic warnings.
func ValidateCycles(cycles [][]string) ([][]string, []string) {
	var validCycles [][]string
	var warnings []string
	for i, cycle := range cycles {
		if len(cycle) < 2 {
			warnings = append(warnings, fmt.Sprintf("invalid cycle %d with < 2 nodes: %v", i+1, cycle))
			continue
		}
		seen := make(map[string]bool, len(cycle))
		valid := true
		for _, node := range cycle {
			if seen[node] {
				warnings = append(warnings, fmt.Sprintf("cycle %d has duplicate node %s: %v", i+1, node, cycle))
				valid = false
				break
			}
			seen[node] = true
		}
		if valid {
			validCycles = append(validCycles, cycle)
		}
	}
	return validCycles, warnings
}

// IsBeadInCycle reports whether beadID appears in any detected dependency cycle.
func IsBeadInCycle(beadID string, cycles [][]string) bool {
	for _, cycle := range cycles {
		for _, id := range cycle {
			if id == beadID {
				return true
			}
		}
	}
	return false
}
