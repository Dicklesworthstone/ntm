package coordinator

import (
	"sort"
	"time"
)

// WaitEdge models a single "Waiter is waiting for Holder on Resource"
// relationship in the swarm wait-for graph. Several edges with the
// same (Waiter, Holder) pair are folded by DeadlockDetector — the
// edge with the smallest Since timestamp wins for cycle ordering, but
// all resources are surfaced in the diagnostic.
type WaitEdge struct {
	Waiter   string    `json:"waiter"`
	Holder   string    `json:"holder"`
	Resource string    `json:"resource,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Since    time.Time `json:"since"`
}

// DeadlockCycle is one detected cycle, in canonical (rotated) order
// so two structurally identical cycles always render the same JSON.
type DeadlockCycle struct {
	Participants []string  `json:"participants"`
	Resources    []string  `json:"resources,omitempty"`
	Reasons      []string  `json:"reasons,omitempty"`
	OldestSince  time.Time `json:"oldest_since"`
	Suggestion   string    `json:"suggestion,omitempty"`
}

// DeadlockReport is the stable robot-readable envelope produced by
// DetectDeadlocks. It is safe to JSON-encode directly.
type DeadlockReport struct {
	Success   bool            `json:"success"`
	Timestamp string          `json:"timestamp"`
	Cycles    []DeadlockCycle `json:"cycles"`
	NodeCount int             `json:"node_count"`
	EdgeCount int             `json:"edge_count"`
	Sources   []SourceStatus  `json:"sources,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`
}

// SourceStatus tracks per-source availability so callers know whether
// a missing data source contributed to the analysis. Mirrors the
// pattern used by --robot-causality.
type SourceStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Edges     int    `json:"edges,omitempty"`
	Error     string `json:"error,omitempty"`
}

// DetectDeadlockOptions configures DetectDeadlocks.
type DetectDeadlockOptions struct {
	// Now is overridable for deterministic tests.
	Now func() time.Time
	// Sources records the data-source availability that produced the
	// edges; surfaced verbatim into the report.
	Sources []SourceStatus
	// Warnings is a list of human-readable degradation notes (e.g.
	// "agentmail unavailable: ..."). Emitted into the report.
	Warnings []string
}

// DetectDeadlocks returns every elementary cycle in the wait-for graph
// implied by `edges`. The result is sorted with a deterministic
// tie-break so the output JSON is stable across runs.
//
// Algorithm: build adjacency, then DFS from every node lexicographically.
// Each cycle is rotated so it starts at its smallest participant, and
// the report sorts cycles by participant tuple. We use Johnson-style
// "elementary cycle" semantics on this small graph rather than Tarjan
// SCCs because deadlock chains are typically short and operators want
// the actual rotation, not just an SCC.
func DetectDeadlocks(edges []WaitEdge, opts DetectDeadlockOptions) DeadlockReport {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	// Build canonical adjacency list. Multiple edges (Waiter, Holder)
	// fold; metadata is collected on the side for use in diagnostics.
	adj := map[string]map[string]struct{}{}
	meta := map[[2]string]*edgeMeta{}
	nodeSet := map[string]struct{}{}
	for _, e := range edges {
		if e.Waiter == "" || e.Holder == "" {
			continue
		}
		nodeSet[e.Waiter] = struct{}{}
		nodeSet[e.Holder] = struct{}{}
		if _, ok := adj[e.Waiter]; !ok {
			adj[e.Waiter] = map[string]struct{}{}
		}
		adj[e.Waiter][e.Holder] = struct{}{}
		k := [2]string{e.Waiter, e.Holder}
		m, ok := meta[k]
		if !ok {
			m = &edgeMeta{oldest: e.Since}
			meta[k] = m
		}
		if !e.Since.IsZero() && (m.oldest.IsZero() || e.Since.Before(m.oldest)) {
			m.oldest = e.Since
		}
		if e.Resource != "" {
			m.resources = appendUnique(m.resources, e.Resource)
		}
		if e.Reason != "" {
			m.reasons = appendUnique(m.reasons, e.Reason)
		}
	}

	nodes := make([]string, 0, len(nodeSet))
	for n := range nodeSet {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	// Find every elementary cycle by DFS. We bound paths to
	// len(nodes) to avoid pathological re-traversal; each cycle is
	// rotated to start at its smallest member and de-duplicated.
	seen := map[string]struct{}{}
	var cycles []DeadlockCycle
	var dfs func(start, cur string, path []string, onPath map[string]bool)
	dfs = func(start, cur string, path []string, onPath map[string]bool) {
		neighbors := sortedKeys(adj[cur])
		for _, next := range neighbors {
			switch {
			case next == start && len(path) >= 1:
				cycle := append([]string(nil), path...)
				key, rotated := canonicalCycle(cycle)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				cycles = append(cycles, buildCycle(rotated, meta))
			case onPath[next]:
				continue
			case len(path) < len(nodes):
				path = append(path, next)
				onPath[next] = true
				dfs(start, next, path, onPath)
				path = path[:len(path)-1]
				delete(onPath, next)
			}
		}
	}
	for _, start := range nodes {
		onPath := map[string]bool{start: true}
		dfs(start, start, []string{start}, onPath)
	}

	// Final sort over canonical participant tuple for stable JSON.
	sort.Slice(cycles, func(i, j int) bool {
		return cycleKey(cycles[i].Participants) < cycleKey(cycles[j].Participants)
	})
	if cycles == nil {
		// Arrays-never-null contract: the acyclic report must encode as
		// "cycles": [] on the robot surface, never "cycles": null.
		cycles = []DeadlockCycle{}
	}

	return DeadlockReport{
		Success:   true,
		Timestamp: now().UTC().Format(time.RFC3339Nano),
		Cycles:    cycles,
		NodeCount: len(nodeSet),
		EdgeCount: len(meta),
		Sources:   opts.Sources,
		Warnings:  opts.Warnings,
	}
}

// EdgesFromConflicts is a small adapter for callers that already have
// per-resource Conflict rows (e.g. ConflictDetector). It produces one
// wait edge per (waiter, holder) pair where any other holder is
// blocking the would-be waiter on that resource. The waiter is
// identified separately via `waiterByConflictID` so the caller can
// thread its own pending-claim source.
func EdgesFromConflicts(conflicts []Conflict, waiterByConflictID map[string]string) []WaitEdge {
	out := make([]WaitEdge, 0, len(conflicts))
	for _, c := range conflicts {
		waiter := waiterByConflictID[c.ID]
		if waiter == "" {
			continue
		}
		for _, h := range c.Holders {
			if h.AgentName == "" || h.AgentName == waiter {
				continue
			}
			out = append(out, WaitEdge{
				Waiter:   waiter,
				Holder:   h.AgentName,
				Resource: c.FilePath,
				Reason:   h.Reason,
				Since:    c.DetectedAt,
			})
		}
	}
	return out
}

// canonicalCycle rotates a cycle so it begins with its lexicographically
// smallest participant, and returns a stable string key that
// uniquely identifies the rotated cycle.
func canonicalCycle(path []string) (string, []string) {
	if len(path) == 0 {
		return "", nil
	}
	smallest := 0
	for i := 1; i < len(path); i++ {
		if path[i] < path[smallest] {
			smallest = i
		}
	}
	rotated := make([]string, len(path))
	for i := range path {
		rotated[i] = path[(smallest+i)%len(path)]
	}
	return cycleKey(rotated), rotated
}

func cycleKey(path []string) string {
	if len(path) == 0 {
		return ""
	}
	out := path[0]
	for _, p := range path[1:] {
		out += "->" + p
	}
	return out
}

// buildCycle assembles a DeadlockCycle from a rotated participant
// list, looking up edge metadata in `meta` for resources/reasons.
func buildCycle(participants []string, meta map[[2]string]*edgeMeta) DeadlockCycle {
	c := DeadlockCycle{Participants: participants}
	for i := range participants {
		from := participants[i]
		to := participants[(i+1)%len(participants)]
		m, ok := meta[[2]string{from, to}]
		if !ok {
			continue
		}
		for _, r := range m.resources {
			c.Resources = appendUnique(c.Resources, r)
		}
		for _, r := range m.reasons {
			c.Reasons = appendUnique(c.Reasons, r)
		}
		if !m.oldest.IsZero() && (c.OldestSince.IsZero() || m.oldest.Before(c.OldestSince)) {
			c.OldestSince = m.oldest
		}
	}
	sort.Strings(c.Resources)
	sort.Strings(c.Reasons)
	c.Suggestion = suggestResolution(c, meta)
	return c
}

// edgeMeta is held package-private inside this file so it can be
// referenced by both DetectDeadlocks and buildCycle.
type edgeMeta struct {
	resources []string
	reasons   []string
	oldest    time.Time
}

// suggestResolution chooses a stable suggestion string based on cycle
// shape (bd-6yomt):
//   - self-loop (1 participant) → "release self-held reservation: X".
//   - short cycle (2–3 participants) → names the longest-waiting
//     holder, i.e., the participant whose incoming edge in the cycle
//     has the smallest (oldest) .Since timestamp. That is the
//     upstream blocker whose release breaks the cycle most cleanly.
//     Falls through to the alphabetically-first participant when no
//     usable edge metadata exists, preserving canonical determinism.
//   - longer cycle (4+ participants) → names the alphabetically-first
//     participant. canonicalCycle has already rotated the cycle so
//     Participants[0] is that name; the suggestion stays stable
//     across runs and across structurally identical cycle rotations.
func suggestResolution(c DeadlockCycle, meta map[[2]string]*edgeMeta) string {
	if len(c.Participants) == 0 {
		return ""
	}
	if len(c.Participants) == 1 {
		return "release self-held reservation: " + c.Participants[0]
	}
	if len(c.Participants) <= 3 {
		if longest := pickLongestWaitingHolder(c.Participants, meta); longest != "" {
			return "ask " + longest + " to release reservations first"
		}
	}
	return "ask " + c.Participants[0] + " to release reservations first"
}

// pickLongestWaitingHolder walks the (participants[i] → participants[i+1])
// edges of a canonicalized cycle, looks up each edge's oldest .Since in
// meta, and returns the holder ("to") of the edge with the smallest
// oldest timestamp — i.e., the participant that has been blocking the
// longest. Returns "" when meta is nil or no edge in the cycle has a
// non-zero oldest timestamp; callers fall back to the alphabetically-
// first participant in that case.
func pickLongestWaitingHolder(participants []string, meta map[[2]string]*edgeMeta) string {
	if len(participants) == 0 || meta == nil {
		return ""
	}
	var bestHolder string
	var bestOldest time.Time
	for i := range participants {
		from := participants[i]
		to := participants[(i+1)%len(participants)]
		m, ok := meta[[2]string{from, to}]
		if !ok || m.oldest.IsZero() {
			continue
		}
		if bestHolder == "" || m.oldest.Before(bestOldest) {
			bestHolder = to
			bestOldest = m.oldest
		}
	}
	return bestHolder
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func appendUnique(in []string, v string) []string {
	for _, s := range in {
		if s == v {
			return in
		}
	}
	return append(in, v)
}
