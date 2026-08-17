package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	pathpkg "path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/events"
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
	mu         sync.Mutex
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

	reservations, err := d.mailClient.ListReservations(ctx, d.projectKey, "", true)
	if err != nil {
		return nil, fmt.Errorf("listing reservations: %w", err)
	}

	conflicts := detectReservationConflictsAt(reservations, time.Now())
	d.mu.Lock()
	// Prune old conflicts to prevent unbounded growth
	now := time.Now()
	for id, c := range d.conflicts {
		if now.Sub(c.DetectedAt) > time.Hour {
			delete(d.conflicts, id)
		}
	}
	for i := range conflicts {
		conflict := conflicts[i]
		d.conflicts[conflict.ID] = &conflict
	}
	d.mu.Unlock()

	return conflicts, nil
}

func detectReservationConflictsAt(reservations []agentmail.FileReservation, now time.Time) []Conflict {
	type aggregate struct {
		conflict Conflict
		seen     map[string]bool
	}

	active := make([]agentmail.FileReservation, 0, len(reservations))
	for _, r := range reservations {
		if !reservationActiveAt(r, now) {
			continue
		}
		active = append(active, r)
	}

	groups := make(map[string]*aggregate)
	for i := 0; i < len(active); i++ {
		for j := i + 1; j < len(active); j++ {
			a := active[i]
			b := active[j]
			if !reservationsConflict(a, b) {
				continue
			}

			pattern := conflictPatternLabel(a.PathPattern, b.PathPattern)
			group := groups[pattern]
			if group == nil {
				group = &aggregate{
					conflict: Conflict{
						ID:         generateConflictID(pattern),
						Pattern:    pattern,
						DetectedAt: now,
					},
					seen: make(map[string]bool),
				}
				groups[pattern] = group
			}

			for _, reservation := range []agentmail.FileReservation{a, b} {
				if group.seen[reservation.AgentName] {
					continue
				}
				group.seen[reservation.AgentName] = true
				group.conflict.Holders = append(group.conflict.Holders, Holder{
					AgentName:  reservation.AgentName,
					ReservedAt: reservation.CreatedTS.Time,
					ExpiresAt:  reservation.ExpiresTS.Time,
					Reason:     reservation.Reason,
				})
			}
		}
	}

	conflicts := make([]Conflict, 0, len(groups))
	for _, group := range groups {
		sortHolders(group.conflict.Holders)
		conflicts = append(conflicts, group.conflict)
	}

	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].Pattern < conflicts[j].Pattern
	})

	return conflicts
}

func reservationActiveAt(r agentmail.FileReservation, now time.Time) bool {
	if r.ReleasedTS != nil {
		return false
	}
	// A zero expiry means the server sent no expires_ts, or sent one this
	// client could not parse (agentmail.FlexTime maps "" to the zero time).
	// Comparing against it made every such reservation look EXPIRED, so
	// DetectConflicts and CheckPathConflict skipped it entirely and reported
	// "no conflict" for a path that is exclusively held — a fail-OPEN in the
	// layer whose whole job is stopping two agents from editing the same file.
	//
	// Treat unknown as ACTIVE: an unexpired-looking lease we cannot date is a
	// reason to coordinate, not to ignore. internal/assign/reservation.go
	// already refuses a zero expiry outright ("has no future expiry"), so
	// failing closed here is also the consistent choice.
	if r.ExpiresTS.Time.IsZero() {
		return true
	}
	return !now.After(r.ExpiresTS.Time)
}

func reservationsConflict(a, b agentmail.FileReservation) bool {
	if a.AgentName == b.AgentName {
		return false
	}
	if !a.Exclusive && !b.Exclusive {
		return false
	}
	return reservationPatternsOverlap(a.PathPattern, b.PathPattern)
}

func reservationPatternsOverlap(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return matchesPattern(a, b) || matchesPattern(b, a)
}

func conflictPatternLabel(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return a
	}
	if a > b {
		a, b = b, a
	}
	return a + " <-> " + b
}

func sortHolders(holders []Holder) {
	sort.SliceStable(holders, func(i, j int) bool {
		if holders[i].AgentName != holders[j].AgentName {
			return holders[i].AgentName < holders[j].AgentName
		}
		if !holders[i].ReservedAt.Equal(holders[j].ReservedAt) {
			return holders[i].ReservedAt.Before(holders[j].ReservedAt)
		}
		return holders[i].ExpiresAt.Before(holders[j].ExpiresAt)
	})
}

// --- RunCycle wiring (bd-ws2-wire-or-delete-ykmcz.1) -----------------------
//
// The negotiation engine below was fully implemented and tested but never
// invoked: `enable conflict-negotiate` persisted a flag that nothing read.
// runConflictCycle is the wire — RunCycle calls it every tick, it consults the
// persisted ConflictNotify / ConflictNegotiate flags, and it publishes a
// negotiation OUTCOME (naming the conflicting holder pair and the engine's
// resolution decision) on the coordinator event envelope, the event bus, and
// the cycle log.

const (
	// maxConflictOutcomesPerCycle bounds per-tick conflict work so a
	// pathological reservation set cannot stall the monitor loop.
	maxConflictOutcomesPerCycle = 5
	// conflictOutcomeCooldown suppresses re-negotiating/re-notifying the same
	// conflicting pair on every poll tick.
	conflictOutcomeCooldown = 5 * time.Minute
)

// ConflictOutcome records what the coordinator did about one detected
// conflict during a cycle. It names the specific conflicting holder pair and
// the engine's resolution decision — not merely that "negotiation ran".
type ConflictOutcome struct {
	ConflictID     string   `json:"conflict_id"`
	Pattern        string   `json:"pattern"`
	Holders        []string `json:"holders"`
	Mode           string   `json:"mode"` // "negotiate" or "notify"
	Requester      string   `json:"requester,omitempty"`
	AskedToRelease string   `json:"asked_to_release,omitempty"`
	Resolution     string   `json:"resolution"`
	Error          string   `json:"error,omitempty"`
}

// runConflictCycle performs one bounded conflict detection + resolution pass.
// DEFAULT BEHAVIOR PRESERVED: ConflictNegotiate defaults to false, so
// negotiation only runs for sessions that persisted `enable
// conflict-negotiate`; with ConflictNotify also disabled (or no Agent Mail
// client) the engine is never invoked at all.
func (c *SessionCoordinator) runConflictCycle(ctx context.Context) []ConflictOutcome {
	c.mu.Lock()
	notify := c.config.ConflictNotify
	negotiate := c.config.ConflictNegotiate
	if !notify && !negotiate {
		c.mu.Unlock()
		return nil
	}
	detect := c.detectConflictsFn
	if detect == nil {
		if c.conflictDetector == nil {
			c.conflictDetector = NewConflictDetector(c.mailClient, c.projectKey)
		}
		detect = c.conflictDetector.DetectConflicts
	}
	if c.lastConflictOutcome == nil {
		c.lastConflictOutcome = make(map[string]time.Time)
	}
	c.mu.Unlock()

	conflicts, err := detect(ctx)
	if err != nil {
		slog.Warn("coordinator conflict detection failed",
			"session", c.session, "error", err)
		return nil
	}
	if len(conflicts) == 0 {
		return nil
	}

	now := time.Now()
	mode := "notify"
	if negotiate {
		mode = "negotiate"
	}

	var outcomes []ConflictOutcome
	// DetectConflicts returns conflicts sorted by pattern, so the per-cycle
	// bound selects a deterministic subset.
	for i := range conflicts {
		if len(outcomes) >= maxConflictOutcomesPerCycle {
			slog.Info("coordinator conflict cycle reached per-tick bound",
				"session", c.session, "bound", maxConflictOutcomesPerCycle,
				"remaining", len(conflicts)-i)
			break
		}
		conflict := &conflicts[i]
		names := holderNames(conflict.Holders)
		key := conflictCooldownKey(conflict.Pattern, names)

		c.mu.Lock()
		last, seen := c.lastConflictOutcome[key]
		if seen && now.Sub(last) < conflictOutcomeCooldown {
			c.mu.Unlock()
			continue
		}
		c.lastConflictOutcome[key] = now
		// Prune stale cooldown entries to keep the map bounded.
		for k, ts := range c.lastConflictOutcome {
			if now.Sub(ts) > 2*conflictOutcomeCooldown {
				delete(c.lastConflictOutcome, k)
			}
		}
		c.mu.Unlock()

		outcome := ConflictOutcome{
			ConflictID: conflict.ID,
			Pattern:    conflict.Pattern,
			Holders:    names,
			Mode:       mode,
		}

		if negotiate {
			requester, target := prioritizeHolders(conflict.Holders)
			outcome.Requester = requester
			outcome.AskedToRelease = target
			if err := c.NegotiateConflict(ctx, conflict, requester); err != nil {
				outcome.Error = err.Error()
				outcome.Resolution = fmt.Sprintf(
					"negotiation between %s failed: %v",
					strings.Join(names, " and "), err)
			} else {
				outcome.Resolution = fmt.Sprintf(
					"requested %s release %q in favor of %s (earliest reservation wins)",
					target, conflict.Pattern, requester)
			}
		} else {
			if err := c.NotifyConflict(ctx, conflict); err != nil {
				outcome.Error = err.Error()
				outcome.Resolution = fmt.Sprintf(
					"notification to %s failed: %v",
					strings.Join(names, " and "), err)
			} else {
				outcome.Resolution = fmt.Sprintf(
					"notified %s of the reservation conflict on %q (notify-only: no release requested)",
					strings.Join(names, " and "), conflict.Pattern)
			}
		}

		c.publishConflictOutcome(outcome)
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

// publishConflictOutcome surfaces one outcome on the cycle log, the
// coordinator event envelope, and the process event bus.
func (c *SessionCoordinator) publishConflictOutcome(outcome ConflictOutcome) {
	slog.Info("coordinator conflict outcome",
		"session", c.session,
		"conflict_id", outcome.ConflictID,
		"pattern", outcome.Pattern,
		"holders", strings.Join(outcome.Holders, ","),
		"mode", outcome.Mode,
		"resolution", outcome.Resolution,
		"error", outcome.Error)

	eventType := EventConflictDetected
	if outcome.Mode == "negotiate" && outcome.Error == "" {
		eventType = EventConflictResolved
	}
	now := time.Now().UTC()
	details := map[string]any{
		"conflict_id": outcome.ConflictID,
		"pattern":     outcome.Pattern,
		"holders":     outcome.Holders,
		"mode":        outcome.Mode,
		"resolution":  outcome.Resolution,
	}
	if outcome.Requester != "" {
		details["requester"] = outcome.Requester
	}
	if outcome.AskedToRelease != "" {
		details["asked_to_release"] = outcome.AskedToRelease
	}
	if outcome.Error != "" {
		details["error"] = outcome.Error
	}

	select {
	case c.events <- CoordinatorEvent{
		Type:      eventType,
		Timestamp: now,
		Details:   details,
	}:
	default:
		// Channel full, drop event (matches emitEvent policy).
	}

	events.Publish(busCoordinatorEvent{
		BaseEvent: events.BaseEvent{
			Type:      "coordinator.conflict",
			Timestamp: now,
			Session:   c.session,
		},
		Details: details,
	})
}

// holderNames extracts holder agent names preserving the engine's
// deterministic holder ordering.
func holderNames(holders []Holder) []string {
	names := make([]string, 0, len(holders))
	for _, h := range holders {
		names = append(names, h.AgentName)
	}
	return names
}

// conflictCooldownKey identifies a conflicting pattern+holder-set so a
// persisting conflict is acted on once per cooldown window, not once per
// tick. Conflict IDs are freshly generated on every detection pass and so
// cannot be used for this.
func conflictCooldownKey(pattern string, names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return pattern + "|" + strings.Join(sorted, ",")
}

// prioritizeHolders deterministically assigns negotiation priorities:
// earliest reservation (ties broken by agent name) wins and becomes the
// requester; the latest holder is the one the engine asks to release. It
// mutates Holder.Priority in place so NegotiateConflict selects the same
// target it reports.
func prioritizeHolders(holders []Holder) (requester, target string) {
	if len(holders) == 0 {
		return "", ""
	}
	order := make([]*Holder, 0, len(holders))
	for i := range holders {
		order = append(order, &holders[i])
	}
	sort.SliceStable(order, func(i, j int) bool {
		if !order[i].ReservedAt.Equal(order[j].ReservedAt) {
			return order[i].ReservedAt.Before(order[j].ReservedAt)
		}
		return order[i].AgentName < order[j].AgentName
	})
	for rank, h := range order {
		h.Priority = rank
	}
	return order[0].AgentName, order[len(order)-1].AgentName
}

// NegotiateConflict attempts to resolve a conflict by requesting release from lower-priority holders.
func (c *SessionCoordinator) NegotiateConflict(ctx context.Context, conflict *Conflict, requester string) error {
	if conflict == nil {
		return fmt.Errorf("conflict is nil")
	}
	if requester == "" {
		return fmt.Errorf("requester is empty")
	}
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
	if conflict == nil {
		return fmt.Errorf("conflict is nil")
	}
	if c.mailClient == nil {
		return nil
	}

	// Notify all holders
	var recipients []string
	for _, h := range conflict.Holders {
		recipients = append(recipients, h.AgentName)
	}
	if len(recipients) == 0 {
		return nil // No one to notify
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

// conflictIDCounter provides monotonically increasing IDs to avoid collisions
// when multiple conflicts are generated in the same nanosecond.
var conflictIDCounter uint64

// generateConflictID generates a unique ID for a conflict.
func generateConflictID(pattern string) string {
	seq := atomic.AddUint64(&conflictIDCounter, 1)
	return fmt.Sprintf("conflict-%d-%d-%s", time.Now().UnixNano(), seq, sanitizeForID(pattern))
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
// - Combined with wildcard suffix: "src/**/*.go" matches "src/foo/bar/test.go"
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

		// If suffix contains wildcards, we need pattern matching, not literal matching
		if strings.Contains(suffix, "*") {
			// The suffix is a pattern - check if the trailing part of remaining matches it
			// For "*.go", we check if the last path segment matches
			// For "foo/*.go", we check if the last two segments match
			return matchesSuffixPattern(remaining, suffix)
		}

		// bd-eebvt: a literal suffix must occupy a whole basename, not a
		// fragment of one — `**/test.go` should match files named test.go
		// at any depth, NOT files like "mytest.go" whose basename merely
		// ends with the byte sequence "test.go". Require the suffix to
		// either equal `remaining` exactly OR be preceded by a path
		// separator.
		if !strings.HasSuffix(remaining, suffix) {
			return false
		}
		if remaining == suffix {
			return true
		}
		cut := len(remaining) - len(suffix)
		return cut > 0 && remaining[cut-1] == '/'
	}

	// Handle single * patterns (match single path segment)
	if strings.Contains(pattern, "*") {
		return matchesWildcardPattern(path, pattern)
	}

	// Prefix match (pattern is a directory)
	return strings.HasPrefix(path, pattern+"/")
}

// matchesWildcardPattern checks if path matches a pattern with single * wildcards.
func matchesWildcardPattern(path, pattern string) bool {
	// A basename-only pattern is intentionally recursive: reserving "*.go"
	// covers Go files anywhere in the project. Directory-qualified patterns use
	// path.Match below, so their wildcards remain confined to one segment.
	if !strings.Contains(pattern, "/") {
		matched, err := pathpkg.Match(pattern, pathpkg.Base(path))
		return err == nil && matched
	}

	// path.Match gives glob semantics for slash-delimited reservation paths:
	// '*' matches one path segment, never a directory separator. The previous
	// substring-based matcher let "src/*.go" match "src/nested/file.go",
	// creating false conflict reports for unreserved nested files.
	matched, err := pathpkg.Match(pattern, path)
	return err == nil && matched
}

// matchesSuffixPattern checks if the trailing portion of path matches a suffix pattern.
// For example, "foo/bar/test.go" matches suffix pattern "*.go" (last segment matches).
// "foo/bar/main.go" matches suffix pattern "bar/*.go" (last two segments match).
func matchesSuffixPattern(path, suffixPattern string) bool {
	// Count segments in the suffix pattern
	suffixSegments := strings.Count(suffixPattern, "/") + 1

	// Get the last N segments from path
	pathParts := strings.Split(path, "/")
	if len(pathParts) < suffixSegments {
		return false
	}

	// Extract the last N segments
	trailingPath := strings.Join(pathParts[len(pathParts)-suffixSegments:], "/")

	// Now match the trailing path against the suffix pattern
	return matchesWildcardPattern(trailingPath, suffixPattern)
}
