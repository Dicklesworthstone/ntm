package agentmail

// Reservation readback is shared by lock inspection, assignment, and recovery.
// Agent Mail's resource rows are project-scoped but omit project_id, and its
// grant response omits both project_id and agent_name (GH#328). Ownership must
// come from independent server reads, never from echoing the request fields.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Agent Mail's resource default is 20 rows; subsequent requests pin it
	// explicitly. Keep the initial URI stable for existing resource servers.
	reservationPageSize = 20
	// A bound is an error, not permission to return an incomplete lock set.
	maxReservationReadbackRows = 10000
)

type reservationResourceRow struct {
	ID          int       `json:"id"`
	ProjectID   *int      `json:"project_id"`
	Agent       string    `json:"agent"`
	AgentName   string    `json:"agent_name"`
	PathPattern string    `json:"path_pattern"`
	Exclusive   bool      `json:"exclusive"`
	Reason      string    `json:"reason"`
	CreatedTS   FlexTime  `json:"created_ts"`
	ExpiresTS   FlexTime  `json:"expires_ts"`
	ReleasedTS  *FlexTime `json:"released_ts,omitempty"`
}

func reservationResourceText(raw json.RawMessage) ([]byte, error) {
	var envelope struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode reservation resource envelope: %w", err)
	}
	if len(envelope.Contents) != 1 {
		return nil, fmt.Errorf("reservation resource requires one content item, got %d", len(envelope.Contents))
	}
	text := bytes.TrimSpace([]byte(envelope.Contents[0].Text))
	if len(text) == 0 || bytes.Equal(text, []byte("null")) {
		return nil, fmt.Errorf("reservation resource returned no data; an explicit JSON value is required")
	}
	return text, nil
}

// readReservationProject resolves numeric identity via a read-only resource.
// In particular this must not use ensure_project: inspection and failed
// ownership verification must not create or repair a server-side project.
func (c *Client) readReservationProject(ctx context.Context, projectKey string) (*Project, error) {
	key := strings.TrimSpace(projectKey)
	if key == "" {
		return nil, fmt.Errorf("reservation readback requires a project key")
	}
	uri := "resource://project/" + url.PathEscape(key)
	raw, err := c.ReadResource(ctx, uri)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("read reservation project identity: %w", err)
	}
	text, err := reservationResourceText(raw)
	if err != nil {
		return nil, err
	}
	var project Project
	if err := json.Unmarshal(text, &project); err != nil {
		return nil, fmt.Errorf("decode reservation project identity: %w", err)
	}
	if project.ID <= 0 || strings.TrimSpace(project.HumanKey) == "" {
		return nil, fmt.Errorf("reservation project resource has no durable identity")
	}
	if key != project.Slug && !ProjectKeysEquivalent(key, project.HumanKey) {
		return nil, fmt.Errorf("reservation project identity mismatch: got %q (%q), want %q", project.HumanKey, project.Slug, key)
	}
	return &project, nil
}

func (c *Client) readReservationPages(ctx context.Context, projectKey, agentName string, allAgents bool, uri string, first json.RawMessage) ([]FileReservation, error) {
	rows := make([]reservationResourceRow, 0)
	seen := make(map[int]struct{})
	raw := first
	needsProject := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, err := reservationResourceText(raw)
		if err != nil {
			return nil, err
		}
		var page []reservationResourceRow
		if err := json.Unmarshal(text, &page); err != nil {
			return nil, fmt.Errorf("decode reservation page at offset %d: %w", len(rows), err)
		}
		if len(rows)+len(page) > maxReservationReadbackRows {
			return nil, fmt.Errorf("reservation listing exceeds %d rows; refusing an incomplete readback", maxReservationReadbackRows)
		}
		for _, row := range page {
			if row.ID <= 0 || row.PathPattern == "" || (row.Agent == "" && row.AgentName == "") {
				return nil, fmt.Errorf("reservation page contains a row without a durable ID, path, or owner")
			}
			if _, duplicate := seen[row.ID]; duplicate {
				return nil, fmt.Errorf("reservation listing repeated ID %d; pagination did not produce a consistent readback", row.ID)
			}
			if row.Agent != "" && row.AgentName != "" && row.Agent != row.AgentName {
				return nil, fmt.Errorf("reservation %d has conflicting owner fields", row.ID)
			}
			seen[row.ID] = struct{}{}
			needsProject = needsProject || row.ProjectID == nil
		}
		rows = append(rows, page...)
		if len(page) < reservationPageSize {
			break
		}
		// Never filter by agent before advancing the server's offset. An
		// agent's first reservation may be on the last project-wide page.
		nextURI := fmt.Sprintf("%s&limit=%d&offset=%d", uri, reservationPageSize, len(rows))
		raw, err = c.ReadResource(ctx, nextURI)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("read reservation page at offset %d: %w", len(rows), err)
		}
	}

	var project *Project
	if needsProject {
		var err error
		project, err = c.readReservationProject(ctx, projectKey)
		if err != nil {
			return nil, err
		}
	}
	reservations := make([]FileReservation, 0, len(rows))
	for _, row := range rows {
		projectID := 0
		if row.ProjectID != nil {
			projectID = *row.ProjectID
			if project != nil && projectID != project.ID {
				return nil, fmt.Errorf("reservation %d project mismatch: got %d, want %d", row.ID, projectID, project.ID)
			}
		} else {
			// The numeric ID is from a validated project resource, and this
			// row was independently read from that project's reservation URI.
			projectID = project.ID
		}
		name := row.Agent
		if name == "" {
			name = row.AgentName
		}
		if agentName != "" && !allAgents && name != agentName {
			continue
		}
		reservations = append(reservations, FileReservation{
			ID: row.ID, ProjectID: projectID, AgentName: name,
			PathPattern: row.PathPattern, Exclusive: row.Exclusive, Reason: row.Reason,
			CreatedTS: row.CreatedTS, ExpiresTS: row.ExpiresTS, ReleasedTS: row.ReleasedTS,
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return reservations, nil
}

// ReservePaths requests file path reservations.
func (c *Client) ReservePaths(ctx context.Context, opts FileReservationOptions) (*ReservationResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("reservation request requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// One budget includes the mutation and any independent ownership reads.
	ctx, cancel := context.WithTimeout(ctx, LongTimeout)
	defer cancel()
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"agent_name":  opts.AgentName,
		"paths":       opts.Paths,
	}
	if opts.TTLSeconds > 0 {
		args["ttl_seconds"] = opts.TTLSeconds
	}
	if opts.Exclusive {
		args["exclusive"] = true
	}
	if opts.Reason != "" {
		args["reason"] = opts.Reason
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "file_reservation_paths", args)
	if err != nil {
		return nil, err
	}

	reservationResult, err := decodeReservationReply(result)
	if err != nil {
		return reservationResult, NewAPIError("file_reservation_paths", 0, err)
	}

	var conflictErr error
	if len(reservationResult.Conflicts) > 0 {
		conflictErr = fmt.Errorf("%w: %d conflicts", ErrReservationConflict, len(reservationResult.Conflicts))
	}
	if err := c.completeReservationGrantOwnership(ctx, opts, result, reservationResult); err != nil {
		return reservationResult, errors.Join(conflictErr, NewAPIError("file_reservation_paths", 0, err))
	}
	return reservationResult, conflictErr
}

// decodeReservationReply decodes rows independently. A bad timestamp or
// conflict record must not discard lease IDs from an otherwise valid JSON
// mutation receipt. Recovered handles remain unverified and accompany an error.
func decodeReservationReply(raw json.RawMessage) (*ReservationResult, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("reservation server returned null instead of a result")
	}
	var wire struct {
		Granted   []json.RawMessage `json:"granted"`
		Conflicts json.RawMessage   `json:"conflicts"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	result := &ReservationResult{Granted: make([]FileReservation, len(wire.Granted))}
	var decodeErrors []error
	for i, rawGrant := range wire.Granted {
		if err := json.Unmarshal(rawGrant, &result.Granted[i]); err != nil {
			decodeErrors = append(decodeErrors, fmt.Errorf("decode grant %d: %w", i, err))
			var handle struct {
				ID          int    `json:"id"`
				PathPattern string `json:"path_pattern"`
			}
			if handleErr := json.Unmarshal(rawGrant, &handle); handleErr == nil {
				result.Granted[i].ID = handle.ID
				result.Granted[i].PathPattern = handle.PathPattern
			}
		}
	}
	if len(wire.Conflicts) > 0 {
		if err := json.Unmarshal(wire.Conflicts, &result.Conflicts); err != nil {
			decodeErrors = append(decodeErrors, fmt.Errorf("decode reservation conflicts: %w", err))
		}
	}
	return result, errors.Join(decodeErrors...)
}

// completeReservationGrantOwnership fills only omitted ownership fields, and
// only after exact-ID verification against independent, live server reads.
// Explicit zero/empty/wrong values are not silently repaired. All grants stay
// untouched if any check fails, preserving the original recovery evidence.
func (c *Client) completeReservationGrantOwnership(ctx context.Context, opts FileReservationOptions, raw json.RawMessage, result *ReservationResult) error {
	var wire struct {
		Granted []struct {
			ProjectID json.RawMessage `json:"project_id"`
			AgentName json.RawMessage `json:"agent_name"`
		} `json:"granted"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return fmt.Errorf("decode grant ownership fields: %w", err)
	}
	needsReadback := false
	for _, grant := range wire.Granted {
		needsReadback = needsReadback || len(grant.ProjectID) == 0 || len(grant.AgentName) == 0
	}
	if !needsReadback {
		return ctx.Err()
	}
	if strings.TrimSpace(opts.AgentName) == "" {
		return fmt.Errorf("grant ownership readback requires an agent name")
	}
	project, err := c.readReservationProject(ctx, opts.ProjectKey)
	if err != nil {
		return err
	}
	reservations, err := c.ListReservations(ctx, opts.ProjectKey, "", true)
	if err != nil {
		return fmt.Errorf("read back granted reservations: %w", err)
	}
	byID := make(map[int]FileReservation, len(reservations))
	for _, reservation := range reservations {
		if _, duplicate := byID[reservation.ID]; duplicate {
			return fmt.Errorf("readback repeated reservation ID %d", reservation.ID)
		}
		byID[reservation.ID] = reservation
	}
	wantedPaths := make(map[string]bool, len(opts.Paths))
	for _, path := range opts.Paths {
		wantedPaths[path] = true
	}
	verified := make([]FileReservation, len(result.Granted))
	seen := make(map[int]bool, len(result.Granted))
	now := time.Now()
	for i, grant := range result.Granted {
		if grant.ID <= 0 || seen[grant.ID] {
			return fmt.Errorf("grant has missing or repeated durable reservation ID %d", grant.ID)
		}
		seen[grant.ID] = true
		row, found := byID[grant.ID]
		if !found {
			return fmt.Errorf("granted reservation %d is absent from active readback", grant.ID)
		}
		if row.ProjectID != project.ID || row.AgentName != opts.AgentName {
			return fmt.Errorf("reservation %d ownership mismatch: got project=%d agent=%q, want project=%d agent=%q", grant.ID, row.ProjectID, row.AgentName, project.ID, opts.AgentName)
		}
		if len(wire.Granted[i].ProjectID) != 0 && grant.ProjectID != row.ProjectID {
			return fmt.Errorf("reservation %d explicit grant project ID disagrees with readback", grant.ID)
		}
		if len(wire.Granted[i].AgentName) != 0 && grant.AgentName != row.AgentName {
			return fmt.Errorf("reservation %d explicit grant owner disagrees with readback", grant.ID)
		}
		if !wantedPaths[grant.PathPattern] || row.PathPattern != grant.PathPattern || row.Reason != grant.Reason || (opts.Reason != "" && row.Reason != opts.Reason) {
			return fmt.Errorf("reservation %d path or reason does not match the requested grant", grant.ID)
		}
		if row.Exclusive != grant.Exclusive || (opts.Exclusive && !row.Exclusive) {
			return fmt.Errorf("reservation %d exclusivity does not match the requested grant", grant.ID)
		}
		if row.ReleasedTS != nil || grant.ReleasedTS != nil || !row.ExpiresTS.After(now) || !grant.ExpiresTS.After(now) {
			return fmt.Errorf("reservation %d is released or expired", grant.ID)
		}
		// Keep the original grant's path and reason. Use the earlier expiry:
		// neither a renewal nor a shortened lease may overstate its validity.
		verified[i] = grant
		if row.ExpiresTS.Before(grant.ExpiresTS.Time) {
			verified[i].ExpiresTS = row.ExpiresTS
		}
		verified[i].ProjectID = row.ProjectID
		verified[i].AgentName = row.AgentName
		verified[i].CreatedTS = row.CreatedTS
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result.Granted = verified
	return nil
}
