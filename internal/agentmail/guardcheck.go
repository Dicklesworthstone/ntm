package agentmail

// guardcheck.go — pre-commit guard reservation predicate (bd-ws1-truth-safety-l5ddi.1).
//
// CheckStagedReservations is the predicate behind `ntm guards check --staged`,
// which the fallback pre-commit hook invokes. It intentionally differs from
// CheckConflicts: CheckConflicts detects MUTUAL conflicts between pairs of
// reservations, while a pre-commit hook must block when ANY active exclusive
// reservation covers a staged path — a single holder is enough, because the
// committer (a human or an unidentified agent) is not the reservation holder.

import (
	"context"
	"sort"
	"time"
)

// StagedReservationConflict describes an active exclusive file reservation
// that overlaps one staged path.
type StagedReservationConflict struct {
	Path          string    `json:"path"`
	ReservationID int       `json:"reservation_id"`
	Holder        string    `json:"holder"`
	PathPattern   string    `json:"path_pattern"`
	ExpiresTS     time.Time `json:"expires_ts"`
}

// CheckStagedReservations returns every active exclusive reservation that
// overlaps any of the given staged paths. Reservations held by selfAgent
// (when non-empty) are skipped so an agent can commit its own reserved files.
func (c *Client) CheckStagedReservations(ctx context.Context, projectKey, selfAgent string, paths []string) ([]StagedReservationConflict, error) {
	reservations, err := c.ListReservations(ctx, projectKey, "", true)
	if err != nil {
		return nil, err
	}

	return stagedReservationConflicts(reservations, selfAgent, paths, time.Now()), nil
}

// stagedReservationConflicts evaluates one collected reservation snapshot.
// Transport errors are handled by the caller, never interpreted as no locks.
func stagedReservationConflicts(reservations []FileReservation, selfAgent string, paths []string, now time.Time) []StagedReservationConflict {
	var conflicts []StagedReservationConflict
	for _, path := range paths {
		// No TrimSpace: filenames with leading/trailing whitespace are legal
		// on Unix, and trimming them would silently fail-open against their
		// exact-path reservations.
		if path == "" {
			continue
		}
		for _, reservation := range reservations {
			if !reservation.Exclusive {
				continue
			}
			if !reservationActiveAt(reservation, now) {
				continue
			}
			if selfAgent != "" && reservation.AgentName == selfAgent {
				continue
			}
			// A staged path is a concrete filename, not a second reservation
			// glob or a subtree. Preserve its metacharacters and whitespace.
			if !matchesReservationPattern(path, reservation.PathPattern) {
				continue
			}
			conflicts = append(conflicts, StagedReservationConflict{
				Path:          path,
				ReservationID: reservation.ID,
				Holder:        reservation.AgentName,
				PathPattern:   reservation.PathPattern,
				ExpiresTS:     reservation.ExpiresTS.Time,
			})
		}
	}

	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Path != conflicts[j].Path {
			return conflicts[i].Path < conflicts[j].Path
		}
		return conflicts[i].ReservationID < conflicts[j].ReservationID
	})
	return conflicts
}
