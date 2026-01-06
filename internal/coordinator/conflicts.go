package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// Conflict represents a file reservation conflict between agents.
type Conflict struct {
	ID         string     `json:"id"`
	FilePath   string     `json:"file_path"`
	Pattern    string     `json:"pattern"`
	Holders    []Holder   `json:"holders"`
	DetectedAt time.Time  `json:"detected_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Resolution string     `json:"resolution,omitempty"`
}

// Holder represents an agent holding a reservation.
type Holder struct {
	AgentName  string    `json:"agent_name"`
	PaneID     string    `json:"pane_id,omitempty"`
	ReservedAt time.Time `json:"reserved_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Reason     string    `json:"reason,omitempty"`
	Priority   int       `json:"priority"` // Lower = higher priority
}

// ConflictDetector detects and tracks file reservation conflicts.
type ConflictDetector struct {
	mailClient *agentmail.Client
	projectKey string
	conflicts  map[string]*Conflict
}

// NewConflictDetector creates a new conflict detector.
func NewConflictDetector(mailClient *agentmail.Client, projectKey string) *ConflictDetector {
	return &ConflictDetector{
		mailClient: mailClient,
		projectKey: projectKey,
		conflicts:  make(map[string]*Conflict),
	}
}

// DetectConflicts checks for file reservation conflicts.
func (d *ConflictDetector) DetectConflicts(ctx context.Context) ([]Conflict, error) {
	if d.mailClient == nil {
		return nil, nil
	}

	// Get all reservations
	reservations, err := d.mailClient.ListReservations(ctx, d.projectKey, "", true)
	if err != nil {
		return nil, fmt.Errorf("listing reservations: %w", err)
	}

	// Group by pattern to detect overlaps
	patternHolders := make(map[string][]Holder)
	for _, r := range reservations {
		if r.ReleasedTS != nil || time.Now().After(r.ExpiresTS) {
			continue // Skip released/expired
		}

		holder := Holder{
			AgentName:  r.AgentName,
			ReservedAt: r.CreatedTS,
			ExpiresAt:  r.ExpiresTS,
			Reason:     r.Reason,
		}
		patternHolders[r.PathPattern] = append(patternHolders[r.PathPattern], holder)
	}

	// Find patterns with multiple exclusive holders
	var conflicts []Conflict
	for pattern, holders := range patternHolders {
		if len(holders) > 1 {
			conflict := Conflict{
				ID:         generateConflictID(pattern),
				Pattern:    pattern,
				Holders:    holders,
				DetectedAt: time.Now(),
			}
			conflicts = append(conflicts, conflict)
			d.conflicts[conflict.ID] = &conflict
		}
	}

	return conflicts, nil
}

// CheckPathConflict checks if a specific path would conflict with existing reservations.
func (d *ConflictDetector) CheckPathConflict(ctx context.Context, path, excludeAgent string) (*Conflict, error) {
	if d.mailClient == nil {
		return nil, nil
	}

	reservations, err := d.mailClient.ListReservations(ctx, d.projectKey, "", true)
	if err != nil {
		return nil, err
	}

	var holders []Holder
	for _, r := range reservations {
		if r.ReleasedTS != nil || time.Now().After(r.ExpiresTS) {
			continue
		}
		if r.AgentName == excludeAgent {
			continue
		}
		if matchesPattern(path, r.PathPattern) {
			holders = append(holders, Holder{
				AgentName:  r.AgentName,
				ReservedAt: r.CreatedTS,
				ExpiresAt:  r.ExpiresTS,
				Reason:     r.Reason,
			})
		}
	}

	if len(holders) == 0 {
		return nil, nil
	}

	return &Conflict{
		ID:         generateConflictID(path),
		FilePath:   path,
		Holders:    holders,
		DetectedAt: time.Now(),
	}, nil
}

// NegotiateConflict attempts to resolve a conflict by requesting release from lower-priority holders.
func (c *SessionCoordinator) NegotiateConflict(ctx context.Context, conflict *Conflict, requester string) error {
	if c.mailClient == nil {
		return fmt.Errorf("agent mail not available")
	}

	// Find lowest-priority holder (highest priority number)
	var lowestPriority *Holder
	for i := range conflict.Holders {
		h := &conflict.Holders[i]
		if h.AgentName == requester {
			continue // Don't ask requester to release
		}
		if lowestPriority == nil || h.Priority > lowestPriority.Priority {
			lowestPriority = h
		}
	}

	if lowestPriority == nil {
		return fmt.Errorf("no other holders to negotiate with")
	}

	// Send negotiation request
	body := c.formatNegotiationRequest(conflict, requester, lowestPriority)
	_, err := c.mailClient.SendMessage(ctx, agentmail.SendMessageOptions{
		ProjectKey:  c.projectKey,
		SenderName:  c.agentName,
		To:          []string{lowestPriority.AgentName},
		Subject:     fmt.Sprintf("File Reservation Conflict: %s", conflict.Pattern),
		BodyMD:      body,
		Importance:  "high",
		AckRequired: true,
	})

	if err != nil {
		return fmt.Errorf("sending negotiation request: %w", err)
	}

	// Emit event
	select {
	case c.events <- CoordinatorEvent{
		Type:      EventConflictDetected,
		Timestamp: time.Now(),
		Details: map[string]any{
			"conflict_id": conflict.ID,
			"pattern":     conflict.Pattern,
			"holders":     len(conflict.Holders),
			"requested":   lowestPriority.AgentName,
		},
	}:
	default:
	}

	return nil
}

// NotifyConflict sends a notification about a conflict without requesting resolution.
func (c *SessionCoordinator) NotifyConflict(ctx context.Context, conflict *Conflict) error {
	if c.mailClient == nil {
		return nil
	}

	// Notify all holders
	var recipients []string
	for _, h := range conflict.Holders {
		recipients = append(recipients, h.AgentName)
	}

	body := c.formatConflictNotification(conflict)
	_, err := c.mailClient.SendMessage(ctx, agentmail.SendMessageOptions{
		ProjectKey:  c.projectKey,
		SenderName:  c.agentName,
		To:          recipients,
		Subject:     fmt.Sprintf("⚠️ Reservation Conflict Detected: %s", conflict.Pattern),
		BodyMD:      body,
		Importance:  "high",
		AckRequired: false,
	})

	return err
}

// formatNegotiationRequest formats a conflict negotiation request.
func (c *SessionCoordinator) formatNegotiationRequest(conflict *Conflict, requester string, target *Holder) string {
	var sb strings.Builder

	sb.WriteString("# File Reservation Conflict\n\n")
	sb.WriteString(fmt.Sprintf("**Pattern:** `%s`\n\n", conflict.Pattern))
	sb.WriteString(fmt.Sprintf("**Requester:** %s needs access to this path.\n\n", requester))
	sb.WriteString("## Request\n\n")
	sb.WriteString(fmt.Sprintf("Agent **%s** is requesting that you release your reservation on `%s`.\n\n",
		requester, conflict.Pattern))
	sb.WriteString("### Your Reservation\n")
	sb.WriteString(fmt.Sprintf("- **Reserved at:** %s\n", target.ReservedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("- **Expires at:** %s\n", target.ExpiresAt.Format(time.RFC3339)))
	if target.Reason != "" {
		sb.WriteString(fmt.Sprintf("- **Reason:** %s\n", target.Reason))
	}
	sb.WriteString("\n")
	sb.WriteString("## Options\n\n")
	sb.WriteString("1. **Release** the reservation if you're done with the files\n")
	sb.WriteString("2. **Keep** the reservation if you're still actively working\n")
	sb.WriteString("3. **Coordinate** with the requester to share access\n\n")
	sb.WriteString("Please acknowledge this message to indicate your decision.\n")

	return sb.String()
}

// formatConflictNotification formats a conflict notification.
func (c *SessionCoordinator) formatConflictNotification(conflict *Conflict) string {
	var sb strings.Builder

	sb.WriteString("# Reservation Conflict Detected\n\n")
	sb.WriteString(fmt.Sprintf("**Pattern:** `%s`\n\n", conflict.Pattern))
	sb.WriteString("## Current Holders\n\n")

	for _, h := range conflict.Holders {
		sb.WriteString(fmt.Sprintf("- **%s** (reserved %s, expires %s)\n",
			h.AgentName,
			h.ReservedAt.Format("15:04:05"),
			h.ExpiresAt.Format("15:04:05")))
		if h.Reason != "" {
			sb.WriteString(fmt.Sprintf("  - Reason: %s\n", h.Reason))
		}
	}

	sb.WriteString("\n")
	sb.WriteString("## Recommendation\n\n")
	sb.WriteString("Please coordinate to avoid edit conflicts. Options:\n")
	sb.WriteString("1. One agent releases their reservation\n")
	sb.WriteString("2. Agents work on different parts of the file\n")
	sb.WriteString("3. Wait for one agent to complete their work\n")

	return sb.String()
}

// generateConflictID generates a unique ID for a conflict.
func generateConflictID(pattern string) string {
	return fmt.Sprintf("conflict-%d-%s", time.Now().UnixNano()%10000, sanitizeForID(pattern))
}

// sanitizeForID creates a safe ID component from a string.
func sanitizeForID(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "*", "x")
	s = strings.ReplaceAll(s, ".", "_")
	if len(s) > 20 {
		s = s[:20]
	}
	return s
}

// matchesPattern checks if a path matches a glob pattern.
// Supports:
// - Exact match: "src/main.go"
// - Prefix match: "src/" matches "src/main.go"
// - Single * wildcard: "src/*.go" matches "src/main.go"
// - Double ** wildcard: "src/**" matches any path under src/
// - Combined: "src/**/test.go" matches "src/foo/bar/test.go"
func matchesPattern(path, pattern string) bool {
	// Exact match
	if path == pattern {
		return true
	}

	// Handle ** patterns (match any number of path segments)
	if strings.Contains(pattern, "**") {
		parts := strings.SplitN(pattern, "**", 2)
		prefix := parts[0]
		suffix := ""
		if len(parts) > 1 {
			suffix = strings.TrimPrefix(parts[1], "/")
		}

		// Path must start with prefix
		if !strings.HasPrefix(path, prefix) {
			return false
		}

		// If no suffix, just prefix match is enough
		if suffix == "" {
			return true
		}

		// Path must end with suffix (after stripping prefix)
		remaining := strings.TrimPrefix(path, prefix)
		return strings.HasSuffix(remaining, suffix)
	}

	// Handle single * patterns (match single path segment)
	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")

		// Must start with first part and end with last part
		if !strings.HasPrefix(path, parts[0]) {
			return false
		}
		if !strings.HasSuffix(path, parts[len(parts)-1]) {
			return false
		}

		// For multiple wildcards, check that all parts appear in order
		remaining := path
		for _, part := range parts {
			if part == "" {
				continue
			}
			idx := strings.Index(remaining, part)
			if idx == -1 {
				return false
			}
			remaining = remaining[idx+len(part):]
		}
		return true
	}

	// Prefix match (pattern is a directory)
	return strings.HasPrefix(path, pattern+"/")
}
