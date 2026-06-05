package tracker

import (
	"sort"
	"time"
)

const (
	// CriticalConflictAgentCount is the number of agents touching a file to trigger critical severity.
	CriticalConflictAgentCount = 3
	// CriticalConflictWindow is the time window within which multiple edits trigger critical severity.
	CriticalConflictWindow = 10 * time.Minute
)

// Conflict represents a detected file conflict
type Conflict struct {
	Path     string               `json:"path"`
	Changes  []RecordedFileChange `json:"changes,omitempty"`
	Severity string               `json:"severity,omitempty"` // "warning", "critical"
	Agents   []string             `json:"agents,omitempty"`
	LastAt   time.Time            `json:"last_at,omitempty"`
}

// DetectConflicts analyzes a set of changes for conflicts.
func DetectConflicts(changes []RecordedFileChange) []Conflict {
	// Group by file path
	byPath := make(map[string][]RecordedFileChange)
	for _, change := range changes {
		// Consider all change types for conflict detection
		// Creation, modification, and deletion are all relevant
		switch change.Change.Type {
		case FileModified, FileAdded, FileDeleted:
			byPath[change.Change.Path] = append(byPath[change.Change.Path], change)
		}
	}

	var conflicts []Conflict
	for path, pathChanges := range byPath {
		if len(pathChanges) > 1 {
			allAgents := make(map[string]bool)
			for _, pc := range pathChanges {
				for _, agent := range pc.Agents {
					allAgents[agent] = true
				}
			}

			if len(allAgents) <= 1 {
				continue
			}

			agentList := make([]string, 0, len(allAgents))
			var last time.Time
			for agent := range allAgents {
				agentList = append(agentList, agent)
			}
			// bd-rfzj1: agentList is built from map iteration, which is
			// non-deterministic. Sort so the per-conflict Agents field
			// is byte-stable across calls — this slice flows directly
			// into --robot-status / work-coordination JSON output.
			sort.Strings(agentList)
			for _, pc := range pathChanges {
				if pc.Timestamp.After(last) {
					last = pc.Timestamp
				}
			}

			conflicts = append(conflicts, Conflict{
				Path:     path,
				Changes:  pathChanges,
				Severity: conflictSeverity(pathChanges, len(allAgents)),
				Agents:   agentList,
				LastAt:   last,
			})
		}
	}
	// bd-rfzj1: outer loop iterates byPath (a map), so the conflicts
	// slice is appended in non-deterministic order. Sort by Path so two
	// calls with identical inputs produce identical JSON. LastAt and
	// Severity are tie-breakers for the rare case of identical Path
	// (shouldn't happen because byPath is keyed by Path, but defensive).
	sort.SliceStable(conflicts, func(i, j int) bool {
		if conflicts[i].Path != conflicts[j].Path {
			return conflicts[i].Path < conflicts[j].Path
		}
		if !conflicts[i].LastAt.Equal(conflicts[j].LastAt) {
			return conflicts[i].LastAt.Before(conflicts[j].LastAt)
		}
		return conflicts[i].Severity < conflicts[j].Severity
	})
	return conflicts
}

// DetectConflictsRecent analyzes global file changes within the given window.
func DetectConflictsRecent(window time.Duration) []Conflict {
	changes := GlobalFileChanges.Since(time.Now().Add(-window))
	return DetectConflicts(changes)
}

// ConflictsSince returns files changed by more than one agent since the timestamp.
func ConflictsSince(ts time.Time, session string) []Conflict {
	changes := GlobalFileChanges.Since(ts)
	var filtered []RecordedFileChange
	for _, c := range changes {
		if session != "" && c.Session != session {
			continue
		}
		filtered = append(filtered, c)
	}
	return DetectConflicts(filtered)
}

// conflictSeverity classifies severity using simple heuristics:
// - critical if three or more agents touched the file
// - critical if edits occurred within a 10-minute window
// otherwise warning.
func conflictSeverity(pathChanges []RecordedFileChange, agentCount int) string {
	if agentCount >= CriticalConflictAgentCount {
		return "critical"
	}
	var minT, maxT time.Time
	for i, c := range pathChanges {
		if i == 0 || c.Timestamp.Before(minT) {
			minT = c.Timestamp
		}
		if c.Timestamp.After(maxT) {
			maxT = c.Timestamp
		}
	}
	if maxT.Sub(minT) <= CriticalConflictWindow {
		return "critical"
	}
	return "warning"
}
