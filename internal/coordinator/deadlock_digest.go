package coordinator

// deadlock_digest.go — C7 wiring seam (bd-ws2-wire-or-delete-ykmcz.8).
//
// This file surfaces DetectDeadlocks (deadlock.go) through the two
// coordinator-side integration points the bead names — reservation
// wait-edge construction shared with `ntm locks list --check-deadlocks`,
// and the coordinator digest line — WITHOUT touching C1's concurrent
// territory (coordinator.go, conflicts.go). It only CALLS package
// helpers defined there (detectReservationConflictsAt,
// EdgesFromConflicts); it never redefines or edits them.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// WaitEdgesFromReservations derives a wait-for graph from raw Agent Mail
// file reservations. Two active reservations by different agents whose
// patterns overlap (with at least one exclusive) contest the same
// resource; the reservation reserved strictly LATER is modeled as
// waiting on every earlier holder in that conflict group. A pair whose
// timestamps tie carries no usable ordering, so it contributes no edge —
// better to miss an edge than to invent a false 2-cycle from a single
// contested path.
//
// A genuine reservation deadlock therefore shows up as mutual overlap in
// opposite creation order — A grabbed X first and now overlaps B's Y,
// while B grabbed Y first and now overlaps A's X — which DetectDeadlocks
// reports as the cycle A -> B -> A.
func WaitEdgesFromReservations(reservations []agentmail.FileReservation, now time.Time) []WaitEdge {
	conflicts := detectReservationConflictsAt(reservations, now)
	waiters := make(map[string]string, len(conflicts))
	for i := range conflicts {
		c := &conflicts[i]
		// detectReservationConflictsAt labels the contested resource in
		// Pattern; EdgesFromConflicts reads FilePath. Bridge the two so
		// cycle diagnostics name the contested pattern.
		if c.FilePath == "" {
			c.FilePath = c.Pattern
		}
		if w := latestReservedHolder(c.Holders); w != "" {
			waiters[c.ID] = w
		}
	}
	return EdgesFromConflicts(conflicts, waiters)
}

// latestReservedHolder returns the holder with the strictly latest
// ReservedAt — the agent that queued behind everyone else in the
// conflict group. Returns "" when the latest timestamp is tied or zero:
// an unorderable group yields no waiter and thus no edge.
func latestReservedHolder(holders []Holder) string {
	var (
		latest    string
		latestAt  time.Time
		ambiguous bool
	)
	for _, h := range holders {
		if h.AgentName == "" || h.ReservedAt.IsZero() {
			continue
		}
		switch {
		case latest == "" || h.ReservedAt.After(latestAt):
			latest = h.AgentName
			latestAt = h.ReservedAt
			ambiguous = false
		case h.ReservedAt.Equal(latestAt):
			ambiguous = true
		}
	}
	if ambiguous {
		return ""
	}
	return latest
}

// DeadlockDigestLine renders a DeadlockReport as a single digest alert
// line naming every detected cycle, or "" when the graph is acyclic so
// the digest carries no false-positive noise.
func DeadlockDigestLine(report DeadlockReport) string {
	if len(report.Cycles) == 0 {
		return ""
	}
	parts := make([]string, 0, len(report.Cycles))
	for _, c := range report.Cycles {
		cycle := strings.Join(c.Participants, " -> ")
		if len(c.Participants) > 1 {
			cycle += " -> " + c.Participants[0]
		}
		if c.Suggestion != "" {
			cycle += " (" + c.Suggestion + ")"
		}
		parts = append(parts, cycle)
	}
	// Hedged wording (bd-izuqq.4): the cycle comes from a creation-order
	// heuristic over advisory reservations — it flags a POSSIBLE deadlock,
	// with known false positives and negatives, not a proven one.
	return fmt.Sprintf("Possible reservation deadlock (%d cycle(s), advisory heuristic): %s",
		len(report.Cycles), strings.Join(parts, "; "))
}

// populateDeadlockAlerts checks the project's active file reservations
// for wait-for cycles and, when one exists, appends the deadlock digest
// line to digest.Alerts and attaches the full robot-readable report.
// Silent no-op when no mail client is configured or Agent Mail is
// unreachable — the digest must degrade, not fail, and an unknown state
// must not fabricate a deadlock line.
func (c *SessionCoordinator) populateDeadlockAlerts(ctx context.Context, digest *DigestSummary) {
	if c == nil || digest == nil || c.mailClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reservations, err := c.mailClient.ListReservations(ctx, c.mailProjectKey, "", true)
	if err != nil {
		return
	}
	now := time.Now()
	edges := WaitEdgesFromReservations(reservations, now)
	report := DetectDeadlocks(edges, DetectDeadlockOptions{
		Now: func() time.Time { return now },
		Sources: []SourceStatus{{
			Name:      "agentmail_reservations",
			Available: true,
			Edges:     len(edges),
		}},
	})
	if len(report.Cycles) == 0 {
		return
	}
	digest.Deadlocks = report.Cycles
	if line := DeadlockDigestLine(report); line != "" {
		digest.Alerts = append(digest.Alerts, line)
	}
}
