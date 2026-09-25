package serve

import (
	"context"
	"errors"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// This internal kind is deliberately NOT in implementedJobTypes. The existing
// POST /beads/{id}/close route checks PermWriteBeads; exposing it through the
// generic jobs allowlist would bypass that operation-specific permission.
const jobTypeBeadClose = "bead_close"

// Execution identity travels outside public params and durable fingerprints.
type jobExecutionIDKey struct{}

type beadCloseJobRequest struct {
	BeadID string `json:"bead_id"`
}

func decodeBeadCloseJob(params map[string]interface{}) (beadCloseJobRequest, error) {
	var req beadCloseJobRequest
	if err := decodeJobParams(params, &req); err != nil {
		return req, err
	}
	if !beadIDPattern.MatchString(req.BeadID) {
		return req, errors.New("bead_id must be a prefixed bead identifier")
	}
	return req, nil
}

// executeBeadCloseJob is an engine adapter, not a second job lifecycle. Shared
// admission/dispatch own capacity, shutdown, cancellation, panic recovery, and
// durable receipts. This execution view has the admission-time project and the
// event publisher, never the original server's mutable configuration or locks.
func (s *Server) executeBeadCloseJob(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	req, err := decodeBeadCloseJob(params)
	if err != nil {
		return nil, err
	}
	dir := s.projectDirSnapshot()
	result := map[string]interface{}{"bead_id": req.BeadID, "project_dir": dir}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Refuse to touch the tracker if its recovery identity cannot be saved.
	if err := reportJobProgress(ctx, result); err != nil {
		return result, err
	}
	result, err = runBeadClose(ctx, dir, req.BeadID, bv.RunBdContext)
	alreadyClosed := result["already_closed"] == true
	if err == nil && !alreadyClosed && s.wsHub != nil {
		s.wsHub.Publish("beads:*", "bead.closed", map[string]interface{}{
			"id": req.BeadID, "bead": result["bead"],
		})
	}
	jobID, _ := ctx.Value(jobExecutionIDKey{}).(string)
	s.publishAsyncBeadCloseAttention(jobID, req.BeadID, alreadyClosed, err)
	return result, err
}
