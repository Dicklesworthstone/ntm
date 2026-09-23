package bv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrReadyCandidatesIncomplete means that a ready response cannot establish a
// complete candidate set. In particular, null, malformed, and truncated results
// must not be treated as proof that the work queue is empty.
var ErrReadyCandidatesIncomplete = errors.New("WORK_CANDIDATES_INCOMPLETE")

const readyCandidateLimit = 100000
const readyCandidateResponseBytes = 64 << 20

// GetReadyCandidatesContext reads the direct tracker ready set BEFORE applying
// policy or a display limit. The older preview reader uses br's default result
// limit and then truncates again locally; filtering that preview can hide every
// eligible item below its cutoff. A sentinel row detects the bounded full-set
// limit instead of silently presenting a partial result as the whole queue.
//
// This is tracker membership, not authorization. Callers must still verify the
// canonical source and apply eligibility, claim, and reservation checks.
func GetReadyCandidatesContext(ctx context.Context, dir string) ([]BeadPreview, error) {
	return readReadyCandidates(ctx, dir, RunBdContext)
}

func readReadyCandidates(ctx context.Context, dir string, run func(context.Context, string, ...string) (string, error)) ([]BeadPreview, error) {
	if ctx == nil || run == nil {
		return nil, errors.New("ready candidates require a context and tracker runner")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("ready candidates require an explicit project")
	}
	output, err := run(ctx, dir, "ready", "--json", "--limit", strconv.Itoa(readyCandidateLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read complete ready candidates: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := decodeReadyCandidates(output, readyCandidateLimit)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return rows, err
}

func decodeReadyCandidates(output string, limit int) ([]BeadPreview, error) {
	incomplete := func(reason string) ([]BeadPreview, error) {
		return nil, fmt.Errorf("%w: %s", ErrReadyCandidatesIncomplete, reason)
	}
	if len(output) > readyCandidateResponseBytes {
		return incomplete("ready response exceeds 64 MiB")
	}
	payload := json.RawMessage(strings.TrimSpace(output))
	if len(payload) == 0 || string(payload) == "null" {
		return incomplete("missing ready list; expected an explicit JSON array")
	}
	var total *int
	if payload[0] == '{' {
		var envelope struct {
			Issues     json.RawMessage `json:"issues"`
			Success    *bool           `json:"success"`
			Error      json.RawMessage `json:"error"`
			Total      *int            `json:"total"`
			Truncated  bool            `json:"truncated"`
			HasMore    bool            `json:"has_more"`
			NextCursor json.RawMessage `json:"next_cursor"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return incomplete("malformed ready envelope")
		}
		reportedError := strings.TrimSpace(string(envelope.Error))
		if (envelope.Success != nil && !*envelope.Success) || (reportedError != "" && reportedError != "null" && reportedError != `""`) {
			return incomplete("tracker reported a failed ready query")
		}
		cursor := strings.TrimSpace(string(envelope.NextCursor))
		if envelope.Truncated || envelope.HasMore || (cursor != "" && cursor != "null" && cursor != `""`) {
			return incomplete("tracker reported additional ready pages")
		}
		payload = json.RawMessage(strings.TrimSpace(string(envelope.Issues)))
		total = envelope.Total
	}
	if len(payload) == 0 || payload[0] != '[' {
		return incomplete("missing ready array in tracker response")
	}
	var rows []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Priority *int   `json:"priority"`
	}
	if err := json.Unmarshal(payload, &rows); err != nil {
		return incomplete("malformed ready records")
	}
	if limit < 0 || len(rows) > limit {
		return incomplete("ready candidate limit exceeded; no partial candidate set was accepted")
	}
	if total != nil && *total != len(rows) {
		return incomplete("ready envelope total does not match its records")
	}
	result := make([]BeadPreview, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" || row.Priority == nil || *row.Priority < 0 || *row.Priority > 4 {
			return incomplete("ready record has an invalid identity or priority")
		}
		if _, duplicate := seen[id]; duplicate {
			return incomplete("ready response contains duplicate issue identities")
		}
		seen[id] = struct{}{}
		result = append(result, BeadPreview{ID: id, Title: row.Title, Priority: fmt.Sprintf("P%d", *row.Priority)})
	}
	return result, nil
}
