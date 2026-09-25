package handoff

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// ErrTransferPostcondition marks a mutation whose acknowledgement could not be
// confirmed against independent live evidence. It must never be interpreted as
// permission to repeat the mutation, acquire more leases, or discard receipts.
var ErrTransferPostcondition = errors.New("unverified reservation transfer postcondition")

// verifyTransferRenewal reads once after renewal. Count acknowledgements alone
// do not establish which leases remain alive, or that their expiry was extended.
// A long-enough existing lease is valid even when renewal was an effective no-op.
func verifyTransferRenewal(ctx context.Context, client ReservationTransferClient, projectKey string, sources []ReservationSnapshot, minimumExpiry time.Time) (verified []agentmail.FileReservation, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(ErrTransferPostcondition, err)
		}
	}()
	if ctx == nil || len(sources) == 0 || minimumExpiry.IsZero() {
		return nil, errors.New("renewal verification requires context, captured leases, and an expiry bound")
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTransferCleanupTime)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := client.ListReservations(ctx, projectKey, "", true)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return nil, fmt.Errorf("read renewed reservations: %w", err)
	}
	if rows == nil {
		return nil, errors.New("renewal readback returned no listing")
	}
	byID := make(map[int]agentmail.FileReservation, len(rows))
	for _, row := range rows {
		if row.ID <= 0 || row.ProjectID != sources[0].ProjectID {
			return nil, errors.New("renewal readback has missing lease identity or a different project")
		}
		if _, duplicate := byID[row.ID]; duplicate {
			return nil, fmt.Errorf("renewal readback repeated lease ID %d", row.ID)
		}
		byID[row.ID] = row
	}
	now := time.Now()
	verified = make([]agentmail.FileReservation, 0, len(sources))
	for _, source := range sources {
		row, found := byID[source.ID]
		if !found || row.ProjectID != source.ProjectID || row.AgentName != source.AgentName ||
			row.PathPattern != source.PathPattern || row.Exclusive != source.Exclusive ||
			row.Reason != source.Reason || !row.CreatedTS.Time.Equal(source.CreatedAt) ||
			row.ReleasedTS != nil || !row.ExpiresTS.After(now) {
			return nil, fmt.Errorf("renewed lease %d for %q is absent, changed, expired, or released", source.ID, source.PathPattern)
		}
		if row.ExpiresTS.Before(minimumExpiry) {
			return nil, fmt.Errorf("renewed lease %d for %q expires at %s before requested minimum %s", source.ID, source.PathPattern,
				row.ExpiresTS.Format(time.RFC3339Nano), minimumExpiry.Format(time.RFC3339Nano))
		}
		verified = append(verified, row)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return cloneTransferGrants(verified), nil
}

// verifyTransferRelease observes the disappearance of exact lease IDs, not the
// vacancy of reusable paths. An acknowledged count is not enough to authorize
// a retry or compensation. Read project-wide so a changed owner cannot hide a
// surviving ID; even an inactive row returned by an inconsistent active view
// makes the result uncertain. Replacement IDs on the same path are untouched.
func verifyTransferRelease(ctx context.Context, client ReservationTransferClient, projectKey string, projectID int, ids []int) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(ErrTransferPostcondition, err)
		}
	}()
	if ctx == nil || projectID <= 0 || len(ids) == 0 {
		return errors.New("release verification requires context, project identity, and lease IDs")
	}
	wanted := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || wanted[id] {
			return errors.New("release verification requires distinct positive lease IDs")
		}
		wanted[id] = true
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTransferCleanupTime)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	rows, err := client.ListReservations(ctx, projectKey, "", true)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return fmt.Errorf("read released reservations: %w", err)
	}
	if rows == nil {
		return errors.New("release readback returned no listing; an explicit empty listing is required")
	}
	seen := make(map[int]bool, len(rows))
	for _, row := range rows {
		if row.ID <= 0 || row.ProjectID != projectID || seen[row.ID] {
			return errors.New("release readback contains missing, repeated, or foreign-project lease identity")
		}
		seen[row.ID] = true
		if wanted[row.ID] {
			return fmt.Errorf("released lease %d is still present in active readback", row.ID)
		}
	}
	return ctx.Err()
}
