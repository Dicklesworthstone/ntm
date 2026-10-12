package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/policy"
)

// The CLI contributes its Agent Mail read adapter; policy, request identity,
// terminal replay handling and atomic consumption live in the shared gate.
func evaluateForceReleaseGate(ctx context.Context, pol *policy.Policy, engine *approval.Engine, operation approval.ForceReleaseOperation, newApproval string, client *agentmail.Client) (approval.ForceReleaseDecision, error) {
	return engine.EvaluateForceRelease(ctx, pol.ForceReleasePolicy(), operation, newApproval, func(ctx context.Context) (approval.ForceReleaseLease, error) {
		if client == nil {
			return approval.ForceReleaseLease{}, fmt.Errorf("Agent Mail client unavailable")
		}
		reservation, err := client.GetReservation(ctx, operation.ProjectKey, operation.ReservationID)
		if err != nil {
			return approval.ForceReleaseLease{}, err
		}
		if reservation == nil {
			return approval.ForceReleaseLease{}, fmt.Errorf("reservation inspection returned no result")
		}
		return approval.ForceReleaseLease{
			Holder: reservation.AgentName, Path: reservation.PathPattern,
			CreatedAt: reservation.CreatedTS.Time.UTC().Format(time.RFC3339Nano),
			ExpiresAt: reservation.ExpiresTS.Time.UTC().Format(time.RFC3339Nano),
			ProjectID: reservation.ProjectID, Exclusive: reservation.Exclusive,
		}, nil
	})
}
