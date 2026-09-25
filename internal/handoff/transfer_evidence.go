package handoff

import "github.com/Dicklesworthstone/ntm/internal/agentmail"

// Copy replies before issuing another operation. A client may reuse a reply
// buffer, and an error receipt can contain reference-valued recovery evidence.
// Never fill missing identity fields from the request or discard invalid IDs.
func cloneTransferGrants(grants []agentmail.FileReservation) []agentmail.FileReservation {
	if grants == nil {
		return nil
	}
	out := make([]agentmail.FileReservation, len(grants))
	copy(out, grants)
	for i := range out {
		if out[i].ReleasedTS != nil {
			released := *out[i].ReleasedTS
			out[i].ReleasedTS = &released
		}
	}
	return out
}

func cloneTransferConflicts(conflicts []agentmail.ReservationConflict) []agentmail.ReservationConflict {
	if conflicts == nil {
		return nil
	}
	out := make([]agentmail.ReservationConflict, len(conflicts))
	copy(out, conflicts)
	for i := range out {
		if conflicts[i].Holders != nil {
			out[i].Holders = append([]string{}, conflicts[i].Holders...)
		}
	}
	return out
}
