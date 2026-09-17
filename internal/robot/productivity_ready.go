package robot

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// decodeReadyBeadCount distinguishes a known empty queue from an unavailable
// queue. json.Unmarshal(null, &slice) succeeds, but null is not evidence that
// there is no ready work and must never enable a convergence decision.
func decodeReadyBeadCount(raw []byte) (int, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		var envelope struct {
			Issues json.RawMessage `json:"issues"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return 0, fmt.Errorf("decode ready beads: %w", err)
		}
		raw = bytes.TrimSpace(envelope.Issues)
	}
	if len(raw) == 0 || raw[0] != '[' {
		return 0, fmt.Errorf("ready beads must be an array or an issues envelope")
	}
	var issues []*struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &issues); err != nil {
		return 0, fmt.Errorf("decode ready beads: %w", err)
	}
	for _, issue := range issues {
		if issue == nil || issue.ID == "" {
			return 0, fmt.Errorf("ready bead is missing its identity")
		}
	}
	return len(issues), nil
}
