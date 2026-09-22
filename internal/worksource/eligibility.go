package worksource

import (
	"sort"
	"strings"
	"time"
)

// EligibilityPolicy supplies project policy and external ownership evidence.
// Missing maps do not claim that those external systems were checked. Callers
// must retain their live reservation/claim gates before dispatching work.
type EligibilityPolicy struct {
	GatedLabels   []string
	ProgramLabels []string // Optional allow-list of exact program:* labels.
	OwnedBeads    map[string]string
	ReservedBeads map[string][]string
	HeldMutexes   map[string]string
	Now           time.Time
}

// Exclusion deliberately carries no title, description, or private task text.
type Exclusion struct {
	ID        string   `json:"id"`
	Reasons   []string `json:"reasons"`
	BlockedBy []string `json:"blocked_by,omitempty"`
}

type Eligibility struct {
	EligibleIDs []string    `json:"eligible_ids"`
	Excluded    []Exclusion `json:"excluded"`
	ReasonCode  string      `json:"reason_code,omitempty"`
}

// Filter checks candidates against the canonical export rather than trusting
// BV scores or cached readiness counts. Candidate order survives; duplicate IDs
// are removed; exclusions/reasons are deterministic and independent of map order.
// Eligible candidates form a conflict-free mutex batch in rank order. This is
// planning evidence, not a lease: independent calls must still claim/reserve
// their work atomically and revalidate ownership before dispatch.
func (s *Snapshot) Filter(candidates []string, policy EligibilityPolicy) Eligibility {
	result := Eligibility{EligibleIDs: []string{}, Excluded: []Exclusion{}}
	if s == nil || s.issues == nil {
		result.ReasonCode = StaleCode
		return result
	}
	if policy.Now.IsZero() {
		policy.Now = time.Now()
	}
	gates := normalizedSet(policy.GatedLabels)
	programs := normalizedSet(policy.ProgramLabels)
	mutexes := make(map[string]bool)
	for key := range policy.HeldMutexes {
		mutexes[normalized(key)] = true
	}
	for id, row := range s.issues {
		// A claim/reservation can exist before the tracker says in_progress,
		// and its worker may still be unwinding after tracker closure. Live
		// external ownership wins over lifecycle; an old assignee retained on
		// a closed/tombstoned row alone does not keep the mutex forever.
		externalOwner := policy.OwnedBeads[id] != "" || len(policy.ReservedBeads[id]) > 0
		terminal := row.Status == "closed" || row.Status == "tombstone"
		assigned := !terminal && strings.TrimSpace(row.Assignee) != ""
		if row.Status != "in_progress" && !externalOwner && !assigned {
			continue
		}
		for _, label := range row.Labels {
			if key, ok := mutexKey(label); ok {
				mutexes[key] = true
			}
		}
	}
	// Existing holders and choices made by this call are different evidence.
	// Keep them separate so a skipped alternative is not reported as claimed.
	selectedMutexes := make(map[string]bool)
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate)
		if seen[id] {
			continue
		}
		seen[id] = true
		exclusion := Exclusion{ID: id, Reasons: []string{}}
		row, found := s.issues[id]
		if !found {
			exclusion.Reasons = append(exclusion.Reasons, "source_missing")
		} else {
			if row.Status != "open" {
				exclusion.Reasons = append(exclusion.Reasons, "lifecycle_not_open")
			}
			if strings.TrimSpace(row.Assignee) != "" || policy.OwnedBeads[id] != "" {
				exclusion.Reasons = append(exclusion.Reasons, "assigned")
			}
			if normalized(row.Type) == "epic" {
				exclusion.Reasons = append(exclusion.Reasons, "container")
			}
			if normalized(row.Type) == "wisp" || row.Ephemeral || row.IsTemplate || row.Pinned {
				exclusion.Reasons = append(exclusion.Reasons, "non_dispatchable")
			}
			if row.DeferUntil != "" {
				until, err := time.Parse(time.RFC3339, row.DeferUntil)
				if err != nil || until.After(policy.Now) {
					exclusion.Reasons = append(exclusion.Reasons, "deferred")
				}
			}
			if len(policy.ReservedBeads[id]) > 0 {
				exclusion.Reasons = append(exclusion.Reasons, "reserved")
			}
			programAllowed := len(programs) == 0
			for _, value := range row.Labels {
				label := normalized(value)
				if gates[label] {
					exclusion.Reasons = append(exclusion.Reasons, "operator_gated")
				}
				if label == "secret" || label == "secrets" || label == "private" || strings.HasPrefix(label, "secret:") || strings.HasPrefix(label, "secrets:") || strings.HasPrefix(label, "private:") {
					exclusion.Reasons = append(exclusion.Reasons, "private")
				}
				if programs[label] {
					programAllowed = true
				}
				if key, ok := mutexKey(label); ok && mutexes[key] {
					exclusion.Reasons = append(exclusion.Reasons, "mutex_held")
				}
			}
			if !programAllowed {
				exclusion.Reasons = append(exclusion.Reasons, "program_scope")
			}
			for _, dep := range row.Dependencies {
				switch normalized(dep.Type) {
				case "blocks", "conditional-blocks", "waits-for", "":
					blockerID := strings.TrimSpace(dep.ID)
					blocker, exists := s.issues[blockerID]
					if !exists || (blocker.Status != "closed" && blocker.Status != "tombstone") {
						exclusion.BlockedBy = append(exclusion.BlockedBy, blockerID)
					}
				}
			}
			if len(exclusion.BlockedBy) > 0 {
				exclusion.Reasons = append(exclusion.Reasons, "blocked")
				exclusion.BlockedBy = sortedUnique(exclusion.BlockedBy)
			}
		}
		if len(exclusion.Reasons) == 0 {
			for _, label := range row.Labels {
				if key, ok := mutexKey(label); ok && selectedMutexes[key] {
					exclusion.Reasons = append(exclusion.Reasons, "mutex_conflict")
					break
				}
			}
		}
		if len(exclusion.Reasons) == 0 {
			result.EligibleIDs = append(result.EligibleIDs, id)
			// Acquire the entire label set only after every eligibility and
			// conflict check passes. A rejected multi-mutex candidate must not
			// consume its uncontended groups and starve independent work.
			for _, label := range row.Labels {
				if key, ok := mutexKey(label); ok {
					selectedMutexes[key] = true
				}
			}
		} else {
			exclusion.Reasons = sortedUnique(exclusion.Reasons)
			result.Excluded = append(result.Excluded, exclusion)
		}
	}
	sort.Slice(result.Excluded, func(i, j int) bool { return result.Excluded[i].ID < result.Excluded[j].ID })
	if len(result.EligibleIDs) == 0 {
		result.ReasonCode = NoClaimableCode
	}
	return result
}

func normalizedSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if value = normalized(value); value != "" {
			result[value] = true
		}
	}
	return result
}

func mutexKey(label string) (string, bool) {
	value, ok := strings.CutPrefix(normalized(label), "mutex:")
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func sortedUnique(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
