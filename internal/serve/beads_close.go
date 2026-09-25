package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var errBeadCloseUnconfirmed = errors.New("bead close outcome could not be confirmed")

// runBeadClose is shared by synchronous requests and asynchronous jobs. An exit
// code alone is not evidence that the requested bead reached the closed state:
// br can return an empty result, or fail after committing the state transition.
// Reconcile ambiguous outcomes with a read in the SAME workspace and context.
// Never retry the mutation here or start fresh work after cancellation.
func runBeadClose(ctx context.Context, dir, beadID string,
	run func(context.Context, string, ...string) (string, error),
) (map[string]interface{}, error) {
	result := map[string]interface{}{"bead_id": beadID, "project_dir": dir}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	output, err := run(ctx, dir, "show", beadID, "--json")
	if cancelErr := ctx.Err(); cancelErr != nil {
		return result, cancelErr
	}
	if err == nil {
		if bead, ok := confirmedClosedBead(output, beadID); ok {
			result["bead"], result["closed"], result["already_closed"] = bead, true, true
			return result, nil
		}
	}

	// Once the command starts, a transport error or cancellation cannot prove
	// that no mutation occurred. Preserve that distinction in recovery data.
	result["outcome_unknown"] = true
	output, closeErr := run(ctx, dir, "close", beadID, "--json")
	if closeErr == nil {
		if bead, ok := confirmedClosedBead(output, beadID); ok {
			result["bead"], result["closed"] = bead, true
			delete(result, "outcome_unknown")
			return result, ctx.Err()
		}
	}
	if cancelErr := ctx.Err(); cancelErr != nil {
		return result, errors.Join(closeErr, cancelErr)
	}

	output, statusErr := run(ctx, dir, "show", beadID, "--json")
	if statusErr == nil {
		if bead, ok := confirmedClosedBead(output, beadID); ok {
			result["bead"], result["closed"], result["reconciled"] = bead, true, true
			delete(result, "outcome_unknown")
			return result, ctx.Err()
		}
	}
	if closeErr != nil {
		return result, fmt.Errorf("close bead %s: %w", beadID, errors.Join(closeErr, statusErr, ctx.Err()))
	}
	return result, fmt.Errorf("%w for %s", errors.Join(errBeadCloseUnconfirmed, statusErr, ctx.Err()), beadID)
}

// confirmedClosedBead accepts only one authoritative record for the requested
// ID. An unrelated closed bead, a success envelope, null, or a multi-bead list
// must never authorize a close receipt. Preserve integer metadata losslessly.
func confirmedClosedBead(output, beadID string) (map[string]interface{}, bool) {
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false
	}
	if beads, ok := value.([]interface{}); ok {
		if len(beads) != 1 {
			return nil, false
		}
		value = beads[0]
	}
	bead, ok := value.(map[string]interface{})
	if !ok {
		return nil, false
	}
	id, _ := bead["id"].(string)
	status, _ := bead["status"].(string)
	return bead, id == beadID && strings.EqualFold(strings.TrimSpace(status), "closed")
}
