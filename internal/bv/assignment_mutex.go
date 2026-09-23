package bv

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

// ErrAssignmentMutexHeld means another claimed task uses a mutex required by
// this assignment. It is an eligibility refusal, not an uncertain claim.
var ErrAssignmentMutexHeld = errors.New("assignment mutex is held")

// AssignmentMutexError names only the requested task's conflicting groups.
// Holder IDs, owners and private task text must not escape through refusals.
type AssignmentMutexError struct {
	BeadID string   `json:"bead_id"`
	Groups []string `json:"mutex_groups"`
}

func (e *AssignmentMutexError) Error() string {
	if e == nil {
		return ErrAssignmentMutexHeld.Error()
	}
	return fmt.Sprintf("%s: %s: mutex groups unavailable: %s", ErrBeadAssignmentIneligible, e.BeadID, strings.Join(e.Groups, ", "))
}

func (e *AssignmentMutexError) Unwrap() []error {
	return []error{ErrAssignmentMutexHeld, ErrBeadAssignmentIneligible}
}

// requireAssignmentMutexes runs inside the SAME BEGIN IMMEDIATE transaction
// that claims the issue. Its caller must not commit or release write ownership
// between this check and updating status/assignee. The claim itself then holds
// every requested group; no second lock table, expiry, or cleanup protocol is
// needed. Other NTM claim processes sharing this database see the committed
// holder before they can pass this gate. Direct tracker edits are not fenced.
func requireAssignmentMutexes(ctx context.Context, tx *sql.Tx, beadID string) error {
	if ctx == nil || tx == nil || strings.TrimSpace(beadID) == "" {
		return errors.New("assignment mutex check requires a context, transaction and bead ID")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT label FROM labels WHERE issue_id = ?", beadID)
	if err != nil {
		return fmt.Errorf("read assignment mutex labels: %w", err)
	}
	groups := make(map[string]bool)
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			rows.Close()
			return fmt.Errorf("read assignment mutex label: %w", err)
		}
		if key, ok := worksource.MutexKey(label); ok {
			groups[key] = true
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return fmt.Errorf("finish assignment mutex labels: %w", err)
	}
	if len(groups) == 0 {
		return ctx.Err()
	}

	// Normalize in Go, using the planner's exact Unicode case/whitespace
	// rules. SQLite LOWER/TRIM alone would let differently spelled copies of
	// one group pass each other. The colon filter is case-independent and
	// excludes only labels that cannot possibly have the mutex: prefix.
	rows, err = tx.QueryContext(ctx, `
		SELECT i.id, i.status, i.assignee, l.label
		FROM labels l
		LEFT JOIN issues i ON i.id = l.issue_id
		WHERE l.issue_id <> ? AND instr(l.label, ':') > 0`, beadID)
	if err != nil {
		return fmt.Errorf("inspect assignment mutex holders: %w", err)
	}
	defer rows.Close()
	conflicts := make(map[string]bool)
	for rows.Next() {
		var id, status, assignee sql.NullString
		var label string
		if err := rows.Scan(&id, &status, &assignee, &label); err != nil {
			return fmt.Errorf("read assignment mutex holder: %w", err)
		}
		key, ok := worksource.MutexKey(label)
		if !ok || !groups[key] {
			continue
		}
		if !id.Valid || !status.Valid || strings.TrimSpace(status.String) == "" {
			return fmt.Errorf("%w: mutex holder lifecycle cannot be verified", ErrBeadAssignmentIneligible)
		}
		// The exact requested issue was excluded by ID, not actor: one actor
		// may own several panes, so two of its different tasks still conflict.
		if worksource.ClaimHoldsMutex(status.String, assignee.String) {
			conflicts[key] = true
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return fmt.Errorf("finish assignment mutex holder check: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(conflicts) == 0 {
		return nil
	}
	held := make([]string, 0, len(conflicts))
	for key := range conflicts {
		held = append(held, key)
	}
	sort.Strings(held)
	return &AssignmentMutexError{BeadID: beadID, Groups: held}
}
