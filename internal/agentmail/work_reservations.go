package agentmail

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// WorkReservationSnapshot is a read-only observation of project reservations.
// ByBead includes every owner's live bead-assignment reservation, not only the
// querying agent's reservations. Unmapped reservations still protect their
// paths, but do not establish a bead identity. No lease is acquired or renewed.
type WorkReservationSnapshot struct {
	ProjectKey string
	ProjectID  int
	ObservedAt time.Time
	Active     int
	Unmapped   int
	ByBead     map[string][]string
}

// ReadWorkReservations resolves the project independently of reservation rows
// and verifies it again after the complete paginated read. The second check
// prevents a deleted/recreated project from being attributed to its old ID.
// This must never call ensure_project or any other mutating endpoint.
func (c *Client) ReadWorkReservations(ctx context.Context, projectKey string) (*WorkReservationSnapshot, error) {
	if c == nil {
		return nil, errors.New("work reservation client is unavailable")
	}
	return readWorkReservations(ctx, projectKey, c.readReservationProject,
		func(ctx context.Context, project string) ([]FileReservation, error) {
			return c.ListReservations(ctx, project, "", true)
		}, time.Now)
}

func readWorkReservations(ctx context.Context, projectKey string,
	readProject func(context.Context, string) (*Project, error),
	list func(context.Context, string) ([]FileReservation, error),
	now func() time.Time,
) (*WorkReservationSnapshot, error) {
	if ctx == nil || readProject == nil || list == nil || now == nil {
		return nil, errors.New("work reservation read requires a context and readers")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return nil, errors.New("work reservation read requires an explicit project")
	}
	before, err := readProject(ctx, projectKey)
	if err != nil {
		return nil, fmt.Errorf("resolve work reservation project: %w", err)
	}
	if before == nil || before.ID <= 0 || strings.TrimSpace(before.HumanKey) == "" {
		return nil, errors.New("work reservation project has no durable identity")
	}
	if projectKey != before.Slug && !ProjectKeysEquivalent(projectKey, before.HumanKey) {
		return nil, errors.New("work reservation project does not match requested project")
	}
	projectID, humanKey := before.ID, before.HumanKey
	rows, err := list(ctx, projectKey)
	if err != nil {
		return nil, fmt.Errorf("read work reservations: %w", err)
	}
	if len(rows) > maxReservationReadbackRows {
		return nil, errors.New("work reservation read exceeds the complete readback limit")
	}
	after, err := readProject(ctx, projectKey)
	if err != nil {
		return nil, fmt.Errorf("recheck work reservation project: %w", err)
	}
	if after == nil || after.ID != projectID || !ProjectKeysEquivalent(humanKey, after.HumanKey) {
		return nil, errors.New("work reservation project changed during collection")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	observedAt := now().UTC()
	if observedAt.IsZero() {
		return nil, errors.New("work reservation observation time is unavailable")
	}
	result := &WorkReservationSnapshot{
		ProjectKey: projectKey, ProjectID: projectID, ObservedAt: observedAt,
		ByBead: make(map[string][]string),
	}
	seen := make(map[int]bool, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if row.ID <= 0 || seen[row.ID] || row.ProjectID != projectID || strings.TrimSpace(row.AgentName) == "" || strings.TrimSpace(row.PathPattern) == "" {
			return nil, errors.New("work reservation read contains invalid, duplicate, or foreign ownership")
		}
		seen[row.ID] = true
		if row.ReleasedTS != nil {
			continue
		}
		if row.ExpiresTS.IsZero() {
			return nil, fmt.Errorf("work reservation %d has no expiry evidence", row.ID)
		}
		if !row.ExpiresTS.After(observedAt) {
			continue
		}
		result.Active++
		// This exact reason is written by NTM's CLI, robot and coordinator
		// assignment ports. Do not guess IDs from arbitrary prose, prefixes,
		// path names, or a substring that could refer to another bead.
		beadID, ok := strings.CutPrefix(row.Reason, "bead assignment: ")
		if !ok || beadID == "" || strings.ContainsAny(beadID, " \t\r\n") {
			result.Unmapped++
			continue
		}
		result.ByBead[beadID] = append(result.ByBead[beadID], row.AgentName)
	}
	for id, owners := range result.ByBead {
		sort.Strings(owners)
		unique := owners[:0]
		for _, owner := range owners {
			if len(unique) == 0 || unique[len(unique)-1] != owner {
				unique = append(unique, owner)
			}
		}
		result.ByBead[id] = unique
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
