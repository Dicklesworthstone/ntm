package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func isAgentMailMissingAgentError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, agentmail.ErrAgentNotRegistered) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "agent '") && strings.Contains(msg, "' not found")
}

func refreshReservationSessionAgent(ctx context.Context, client *agentmail.Client, session, projectKey string, cause error) (*agentmail.SessionAgentInfo, bool, error) {
	if client == nil || strings.TrimSpace(session) == "" || !isAgentMailMissingAgentError(cause) {
		return nil, false, nil
	}
	info, err := client.RegisterSessionAgent(ctx, session, projectKey)
	if err != nil {
		return nil, true, fmt.Errorf("refreshing stale Agent Mail identity: %w", err)
	}
	if info == nil {
		return nil, true, fmt.Errorf("refreshing stale Agent Mail identity: Agent Mail server unavailable")
	}
	return info, true, nil
}

func releaseMatchingReservationIDs(ctx context.Context, client *agentmail.Client, projectKey, agentName string, patterns []string) (*agentmail.ReleaseReservationsResult, error) {
	if client == nil || len(patterns) == 0 {
		return nil, nil
	}
	reservations, err := client.ListReservations(ctx, projectKey, "", true)
	if err != nil {
		return nil, err
	}
	ids := matchingReservationIDs(reservations, agentName, patterns)
	if len(ids) == 0 {
		return nil, nil
	}
	return client.ReleaseReservations(ctx, projectKey, agentName, nil, ids)
}

func matchingReservationIDs(reservations []agentmail.FileReservation, agentName string, patterns []string) []int {
	seen := make(map[int]bool)
	var ids []int
	for _, reservation := range reservations {
		if reservation.ID <= 0 {
			continue
		}
		if agentName != "" && reservation.AgentName != agentName {
			continue
		}
		for _, pattern := range patterns {
			if reservationUnlockPatternMatches(pattern, reservation.PathPattern) {
				if !seen[reservation.ID] {
					seen[reservation.ID] = true
					ids = append(ids, reservation.ID)
				}
				break
			}
		}
	}
	return ids
}

func reservationUnlockPatternMatches(requested, held string) bool {
	requested = strings.TrimSpace(requested)
	held = strings.TrimSpace(held)
	if requested == "" || held == "" {
		return false
	}
	return requested == held || locksCheckPathMatches(requested, held) || locksCheckPathMatches(held, requested)
}
