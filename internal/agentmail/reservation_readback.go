package agentmail

// Reservation readback is shared by lock inspection, assignment, and recovery.
// Agent Mail's resource rows are project-scoped but omit project_id, and its
// grant response omits both project_id and agent_name (GH#328). Ownership must
// come from independent server reads, never from echoing the request fields.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
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

	var reservationResult ReservationResult
	if err := json.Unmarshal(result, &reservationResult); err != nil {
		return nil, NewAPIError("file_reservation_paths", 0, err)
	}

	// Check for conflicts
	if len(reservationResult.Conflicts) > 0 {
		return &reservationResult, fmt.Errorf("%w: %d conflicts", ErrReservationConflict, len(reservationResult.Conflicts))
	}

	return &reservationResult, nil
}
