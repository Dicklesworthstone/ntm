package adapters

import (
	"context"
	"errors"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

const workReservationBudget = 1500 * time.Millisecond

type workReservationReader func(context.Context, string) (*agentmail.WorkReservationSnapshot, error)

// WorkReservationVerification distinguishes an observed empty set from a
// failed or unattempted read. These counts cover the project, not just the
// visible preview. Arbitrary path reservations cannot be mapped to a bead.
type WorkReservationVerification struct {
	State       string `json:"state"`
	ProjectID   int    `json:"project_id,omitempty"`
	ObservedAt  string `json:"observed_at,omitempty"`
	Active      int    `json:"active"`
	MappedBeads int    `json:"mapped_beads"`
	Unmapped    int    `json:"unmapped"`
	Reason      string `json:"reason,omitempty"`
}

// Optional Agent Mail failures must not turn a readable Beads backlog into
// "queue dry" or pretend that ownership was checked. A parent cancellation,
// however, ends the whole collection. The nested budget includes identity,
// every reservation page and the final project-identity recheck.
func collectWorkReservationEvidence(ctx context.Context, project string, read workReservationReader, budget time.Duration) (map[string][]string, *WorkReservationVerification, error) {
	if ctx == nil {
		return nil, nil, errors.New("work reservation evidence requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if read == nil {
		return nil, &WorkReservationVerification{State: "not_checked", Reason: "reservation reader unavailable"}, nil
	}
	if budget <= 0 {
		budget = workReservationBudget
	}
	readCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	snapshot, err := read(readCtx, project)
	if parentErr := ctx.Err(); parentErr != nil {
		return nil, nil, parentErr
	}
	if err == nil && readCtx.Err() != nil {
		err = readCtx.Err()
	}
	if err == nil && (snapshot == nil || snapshot.ProjectID <= 0 || snapshot.ObservedAt.IsZero() || snapshot.ByBead == nil || !agentmail.ProjectKeysEquivalent(project, snapshot.ProjectKey)) {
		err = errors.New("reservation reader returned no verified project snapshot")
	}
	if err != nil {
		// Apply the normal disclosure/redaction policy to errors. Do not
		// publish reservation rows or their arbitrary reason text here.
		reason, _ := NormalizeDisclosureText(err.Error())
		return nil, &WorkReservationVerification{State: "unavailable", Reason: reason}, nil
	}
	return snapshot.ByBead, &WorkReservationVerification{
		State: "observed", ProjectID: snapshot.ProjectID,
		ObservedAt: snapshot.ObservedAt.UTC().Format(time.RFC3339Nano),
		Active:     snapshot.Active, MappedBeads: len(snapshot.ByBead), Unmapped: snapshot.Unmapped,
	}, nil
}
