// Package assign provides bead-to-agent assignment functionality.
package assign

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
)

// FileReservationClient is the Agent Mail surface used by file reservations.
type FileReservationClient interface {
	EnsureProject(context.Context, string) (*agentmail.Project, error)
	ReservePaths(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error)
	ListReservations(context.Context, string, string, bool) ([]agentmail.FileReservation, error)
	ReleaseReservations(context.Context, string, string, []string, []int) (*agentmail.ReleaseReservationsResult, error)
	RenewReservations(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error)
}

// FileReservationManager handles file path reservations for bead assignments.
type FileReservationManager struct {
	client     FileReservationClient
	projectKey string
	ttlSeconds int
}

// FileReservationResult contains the result of a file reservation attempt.
type FileReservationResult struct {
	BeadID         string                          `json:"bead_id"`
	AgentName      string                          `json:"agent_name"`
	RequestedPaths []string                        `json:"requested_paths"`
	GrantedPaths   []string                        `json:"granted_paths"`
	Conflicts      []agentmail.ReservationConflict `json:"conflicts,omitempty"`
	ReservationIDs []int                           `json:"reservation_ids"`
	ExpiresAt      *time.Time                      `json:"expires_at,omitempty"`
	Success        bool                            `json:"success"`
	Error          string                          `json:"error,omitempty"`
}

// NewFileReservationManager creates a new file reservation manager.
//
// projectKey is canonicalized through symlinks: Agent Mail resolves project
// paths to their real directory (e.g. /data/projects/X -> /home/user/X), so
// every downstream reservation read/release keyed by the raw symlinked path
// would miss the project Agent Mail actually registered (GH#239).
func NewFileReservationManager(client FileReservationClient, projectKey string) *FileReservationManager {
	return &FileReservationManager{
		client:     client,
		projectKey: agentmail.CanonicalProjectKey(projectKey),
		ttlSeconds: 3600, // Default 1 hour
	}
}

// SetTTL sets the TTL for reservations in seconds.
func (m *FileReservationManager) SetTTL(seconds int) {
	if seconds > 0 {
		m.ttlSeconds = seconds
	}
}

// Pre-compiled regexes for file path extraction (avoid recompilation per call).
// Use lookahead-like logic by not consuming the trailing boundary to avoid overlap.
// A path may open after a line start, whitespace, an opening bracket or quote,
// or a Markdown code-span backtick (ntm#336).
var (
	filePathRegex = regexp.MustCompile("(?m)(?:^|\\s|[(\\[\"'`])([a-zA-Z0-9_./-]+(?:\\.[a-zA-Z0-9]+)+)(?:\\:\\d+(?::\\d+)?)?")
	dotfileRegex  = regexp.MustCompile("(?m)(?:^|\\s|[(\\[\"'`])(\\.[a-zA-Z][a-zA-Z0-9_]*(?:\\.[a-zA-Z0-9]+)*)")
	dirPathRegex  = regexp.MustCompile("(?m)(?:^|\\s|[(\\[\"'`])([a-zA-Z0-9_-]+(?:/[a-zA-Z0-9_-]+)+)")
	globRegex     = regexp.MustCompile("(?m)(?:^|\\s|[(\\[\"'`])([a-zA-Z0-9_./*-]+\\*[a-zA-Z0-9_./*-]*)")
	// isValidPath regexes - pre-compiled for performance
	versionLikeRegex = regexp.MustCompile(`^\d+\.\d+`)
	validExtRegex    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]{0,9}$`)
)

// ExtractFilePaths extracts file paths from a bead title and description.
// Patterns detected:
// - Explicit paths: src/api/handler.go, lib/utils.ts
// - Glob patterns: internal/**/*.go, *.json
// - Package references: internal/cli, pkg/api
func ExtractFilePaths(title, description string) []string {
	combined := title + "\n" + description

	var paths []string
	seen := make(map[string]bool)

	// Extract file paths first
	for _, match := range filePathRegex.FindAllStringSubmatch(combined, -1) {
		if len(match) > 1 && isValidPath(match[1]) && !seen[match[1]] {
			paths = append(paths, match[1])
			seen[match[1]] = true
		}
	}

	// Extract dotfiles
	for _, match := range dotfileRegex.FindAllStringSubmatch(combined, -1) {
		if len(match) > 1 && !seen[match[1]] {
			paths = append(paths, match[1])
			seen[match[1]] = true
		}
	}

	// Extract directory paths. A match that runs straight into ".ext" is the
	// directory part of a file name (docs/notes.md), not a directory the
	// bead names, so it must not widen into a docs/notes/**/* glob.
	for _, loc := range dirPathRegex.FindAllStringSubmatchIndex(combined, -1) {
		if len(loc) >= 4 && loc[2] >= 0 {
			if end := loc[3]; end+1 < len(combined) && combined[end] == '.' && isPathWordByte(combined[end+1]) {
				continue
			}
			dir := combined[loc[2]:loc[3]]
			// Trim trailing slash if present
			dir = strings.TrimSuffix(dir, "/")
			if isValidPath(dir) && !seen[dir] {
				glob := dir + "/**/*"
				if !seen[glob] {
					paths = append(paths, glob)
					seen[glob] = true
				}
				seen[dir] = true
			}
		}
	}

	// Extract glob patterns
	for _, match := range globRegex.FindAllStringSubmatch(combined, -1) {
		if len(match) > 1 && !seen[match[1]] {
			paths = append(paths, match[1])
			seen[match[1]] = true
		}
	}

	return paths
}

func isPathWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// Owned-files sections in a bead description (ntm#336). A section starts at
// a Markdown ATX heading or a standalone label line ("Owned outputs:",
// "**Files to modify**"). A section opened by a heading runs to the next
// heading of the same or a higher level, so its own sub-labels ("Primary:",
// "Tests:") and sub-headings stay inside it. A section opened by a label line
// runs to the next heading or label line. Fenced code blocks are content,
// never headings: a "# comment" in a shell snippet does not end a section.
var (
	markdownHeadingRegex   = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.+?)[\s#]*$`)
	markdownLabelTextRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z /()-]{0,60}$`)
	ownedSectionTitleRegex = regexp.MustCompile(`(?i)^(?:owned\b.*|.*\bowned (?:outputs?|files|paths)\b.*|outputs?|output files|deliverables|files to (?:edit|change|modify|create|touch|write)|write set|edit surface)$`)
)

// ReservationPathsForBead returns the paths an assignment should reserve for
// a bead. When the description declares the bead's owned files under a
// heading such as "## Owned outputs" (or a "Files to modify:" label line),
// only the paths in that section are returned: inputs the bead merely reads
// must not become exclusive reservations. Otherwise every path in the title
// and description is returned, as ExtractFilePaths finds them. A declared
// section that names no path yields nil, so a reservation-required
// assignment fails closed instead of reserving the wrong files (ntm#336);
// DeclaresOwnedPaths tells that case apart from a bead that names no files.
func ReservationPathsForBead(title, description string) []string {
	if section, declared := ownedPathsSection(description); declared {
		return ExtractFilePaths("", section)
	}
	return ExtractFilePaths(title, description)
}

// DeclaresOwnedPaths reports whether a bead description has an owned-files
// section, whose paths ReservationPathsForBead then reserves exclusively.
func DeclaresOwnedPaths(description string) bool {
	_, declared := ownedPathsSection(description)
	return declared
}

// ErrNoReservationPaths rejects a reservation-required assignment whose bead
// names no files, before the bead is claimed (ntm#336). The opt-out it names
// is spelled the same on `ntm assign` and `ntm --robot-bulk-assign`.
var ErrNoReservationPaths = errors.New("no file paths to reserve in the bead title or description; " +
	"list the files it will change (for example under an \"## Owned outputs\" heading) or pass --reserve-files=false")

// ErrOwnedSectionHasNoPaths rejects a bead whose description declares an
// owned-files section ("## Owned outputs", "## Deliverables", "Files to
// modify:") that names no file. Paths elsewhere in the description are read
// as inputs, so "no file paths" alone would be misleading.
var ErrOwnedSectionHasNoPaths = errors.New("the bead description has an owned-files section " +
	"(such as \"## Owned outputs\", \"## Deliverables\" or \"Files to modify:\") that names no file paths; " +
	"paths elsewhere in the description are treated as inputs and are not reserved. " +
	"List the files it will change in that section or pass --reserve-files=false")

// ReservationPathsForAssignment resolves the files a reservation-required
// assignment must reserve from the live bead's title and description, before
// anything is claimed. It fails with ErrOwnedSectionHasNoPaths when an
// owned-files section is declared but empty, and with ErrNoReservationPaths
// when the bead names no files, so such a bead is refused unclaimed instead of
// being claimed and then failing its reservation (ntm#336). Every assignment
// surface that reserves by default (`ntm assign`, `--robot-bulk-assign`) uses
// it so they agree on what a bead's file scope is.
func ReservationPathsForAssignment(title, description string) ([]string, error) {
	paths := ReservationPathsForBead(title, description)
	if len(paths) == 0 {
		if DeclaresOwnedPaths(description) {
			return nil, ErrOwnedSectionHasNoPaths
		}
		return nil, ErrNoReservationPaths
	}
	return paths, nil
}

// BeadTextReader reads one bead's live title and description from a project.
type BeadTextReader func(ctx context.Context, projectDir, beadID string) (title, description string, err error)

// DiscoverReservationPaths finds the reservation surface for a reservation
// request that names no paths: a durable intent recorded with discovery, or a
// caller that defers path selection to the reservation port. It reads the live
// bead so paths listed only in the description count, not just those in the
// title (ntm#336); when the bead cannot be read, the request's title still
// counts. Reservation ports call it from both Reserve and ReconcileReservation
// so the two always see the same path set.
func DiscoverReservationPaths(ctx context.Context, projectDir string, req assignmentstore.ReservationRequest, read BeadTextReader) []string {
	title := req.BeadTitle
	description := ""
	if strings.TrimSpace(projectDir) != "" && read != nil {
		if liveTitle, liveDescription, err := read(ctx, projectDir, req.BeadID); err == nil {
			if strings.TrimSpace(title) == "" {
				title = liveTitle
			}
			description = liveDescription
		}
	}
	return ReservationPathsForBead(title, description)
}

// ownedPathsSection returns the text of every owned-files section in a
// Markdown description, including the text of each opening heading so a path
// written on it ("## Owned outputs: `a.go`") counts.
func ownedPathsSection(description string) (string, bool) {
	var (
		section   []string
		declared  bool
		inside    bool
		openLevel int // ATX level of the heading that opened the section; 0 for a label line
		fence     string
	)
	for _, line := range strings.Split(description, "\n") {
		if marker := codeFenceMarker(line); marker != "" {
			switch {
			case fence == "":
				fence = marker
			case strings.HasPrefix(marker, fence):
				fence = ""
			}
			if inside {
				section = append(section, line)
			}
			continue
		}
		if fence == "" {
			if title, level, isHeading := sectionTitle(line); isHeading {
				if inside && openLevel > 0 && (level == 0 || level > openLevel) {
					// A sub-label or deeper heading inside the open
					// heading-level owned section belongs to it. Its text,
					// not the raw line: "**Tests**" is not a glob.
					section = append(section, title)
					continue
				}
				inside = ownedSectionTitleRegex.MatchString(title)
				openLevel = level
				declared = declared || inside
				if inside && level > 0 {
					section = append(section, title)
				}
				continue
			}
		}
		if inside {
			section = append(section, line)
		}
	}
	return strings.Join(section, "\n"), declared
}

// codeFenceMarker returns the run of backticks or tildes (three or more) that
// opens or closes a fenced code block on this line, or "".
func codeFenceMarker(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return ""
	}
	for _, ch := range []byte{'`', '~'} {
		n := 0
		for n < len(trimmed) && trimmed[n] == ch {
			n++
		}
		if n >= 3 {
			return trimmed[:n]
		}
	}
	return ""
}

// sectionTitle reports whether line is a Markdown ATX heading or a
// standalone label line, returning its text without emphasis markers or a
// trailing colon, and its heading level (0 for a label line). A label line
// is bold, or ends in a colon, and holds only words: list items and
// sentences are never labels.
func sectionTitle(line string) (string, int, bool) {
	if m := markdownHeadingRegex.FindStringSubmatch(line); m != nil {
		return cleanSectionTitle(m[2]), len(m[1]), true
	}
	trimmed := strings.TrimSpace(line)
	bold := len(trimmed) > 4 && (strings.HasPrefix(trimmed, "**") || strings.HasPrefix(trimmed, "__"))
	colon := strings.HasSuffix(strings.TrimRight(trimmed, "*_ "), ":")
	if !bold && !colon {
		return "", 0, false
	}
	title := cleanSectionTitle(trimmed)
	if !markdownLabelTextRegex.MatchString(title) {
		return "", 0, false
	}
	return title, 0, true
}

func cleanSectionTitle(text string) string {
	text = strings.TrimSpace(text)
	for {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.Trim(text, "*_ "), ":"))
		if trimmed == text {
			return text
		}
		text = trimmed
	}
}

// isValidPath checks if a path looks valid (not a URL, version, etc.)
func isValidPath(path string) bool {
	// Exclude URLs and domain names
	if strings.Contains(path, "://") || strings.HasPrefix(path, "www.") {
		return false
	}

	// Exclude version-like strings (e.g., 1.2.3, v1.2.3)
	if versionLikeRegex.MatchString(path) || strings.HasPrefix(strings.ToLower(path), "v") && versionLikeRegex.MatchString(path[1:]) {
		return false
	}

	// Exclude common non-path patterns and domain-like patterns
	excludePatterns := []string{
		"e.g.", "i.e.", "etc.", "fig.", "ref.", "http", "https", ".com", ".net", ".org", ".io", ".gov", ".edu",
	}
	lowerPath := strings.ToLower(path)
	for _, pattern := range excludePatterns {
		if lowerPath == pattern || strings.HasSuffix(lowerPath, pattern) || strings.HasPrefix(lowerPath, pattern) && strings.Contains(pattern, "http") {
			return false
		}
	}

	// Exclude obvious non-paths (e.g. sentences ending in dot)
	if strings.HasSuffix(path, ".") || strings.HasPrefix(path, ".") && !strings.Contains(path, "/") && len(path) < 3 {
		// Matches "." or ".." but allows ".github"
		if path == "." || path == ".." {
			return false
		}
	}

	// Check for file extension (must have content before and after dot)
	if strings.Contains(path, ".") {
		parts := strings.Split(path, ".")
		if len(parts) >= 2 {
			// Last part should be a valid extension with at least one letter
			// This excludes things like "fig.1" while allowing "config.json"
			ext := parts[len(parts)-1]
			if validExtRegex.MatchString(ext) {
				// First part should have content or be a dotfile
				if len(parts[0]) > 0 || strings.Contains(path, "/") {
					// Additional check: exclude common top-level domains that look like extensions
					tlds := []string{"com", "net", "org", "io", "gov", "edu", "me", "ai", "app", "dev"}
					isTLD := false
					for _, tld := range tlds {
						if ext == tld && !strings.Contains(path, "/") {
							isTLD = true
							break
						}
					}
					if !isTLD {
						return true
					}
				}
			}
		}
	}

	// Paths with slashes are valid
	return strings.Contains(path, "/") && !strings.Contains(path, " ")
}

// ReserveForBead reserves file paths mentioned in a bead for an agent.
func (m *FileReservationManager) ReserveForBead(ctx context.Context, beadID, beadTitle, beadDescription, agentName string) (*FileReservationResult, error) {
	return m.reservePathsForBead(ctx, beadID, agentName, ExtractFilePaths(beadTitle, beadDescription))
}

// ReservePathsForBead reserves the caller's exact durable path set. Assignment
// recovery and reassignment use this instead of rediscovering a potentially
// different set from a title-only projection.
func (m *FileReservationManager) ReservePathsForBead(ctx context.Context, beadID, agentName string, requestedPaths []string) (*FileReservationResult, error) {
	paths := make([]string, 0, len(requestedPaths))
	seen := make(map[string]struct{}, len(requestedPaths))
	for _, rawPath := range requestedPaths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return m.reservePathsForBead(ctx, beadID, agentName, paths)
}

func (m *FileReservationManager) reservePathsForBead(ctx context.Context, beadID, agentName string, paths []string) (*FileReservationResult, error) {
	result := &FileReservationResult{
		BeadID: beadID, AgentName: agentName,
		RequestedPaths: append([]string(nil), paths...), Success: false,
	}

	if len(paths) == 0 {
		result.Success = true
		return result, nil
	}

	if m.client == nil {
		result.Error = "agent mail client not configured"
		return result, nil
	}
	expectedProjectID, err := m.ensureProject(ctx)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}

	// Reserve paths via Agent Mail
	reservationResult, err := m.client.ReservePaths(ctx, agentmail.FileReservationOptions{
		ProjectKey: m.projectKey,
		AgentName:  agentName,
		Paths:      paths,
		TTLSeconds: m.ttlSeconds,
		Exclusive:  true,
		Reason:     fmt.Sprintf("bead assignment: %s", beadID),
	})

	if err != nil {
		// Check if it's a conflict error with partial results
		if reservationResult != nil {
			result.Conflicts = reservationResult.Conflicts
			if validationErr := collectGrantedReservations(result, reservationResult.Granted, paths, agentName, fmt.Sprintf("bead assignment: %s", beadID), expectedProjectID, time.Now().UTC(), false); validationErr != nil {
				result.Error = validationErr.Error()
				return result, validationErr
			}
			result.Error = fmt.Sprintf("conflicts detected: %v", err)
			return result, nil
		}
		result.Error = err.Error()
		return result, err
	}

	if reservationResult == nil {
		result.Error = "agent mail returned no reservation result"
		return result, errors.New(result.Error)
	}
	// Process successful reservations
	if validationErr := collectGrantedReservations(result, reservationResult.Granted, paths, agentName, fmt.Sprintf("bead assignment: %s", beadID), expectedProjectID, time.Now().UTC(), false); validationErr != nil {
		result.Error = validationErr.Error()
		return result, validationErr
	}
	result.Success = true

	return result, nil
}

// ReconcileForBead lists active Agent Mail leases created for one bead and
// returns the durable handles that match the original agent and requested
// paths. An empty successful result proves no matching lease remains.
func (m *FileReservationManager) ReconcileForBead(ctx context.Context, beadID, agentName string, requestedPaths []string) (*FileReservationResult, error) {
	result := &FileReservationResult{
		BeadID:         beadID,
		AgentName:      agentName,
		RequestedPaths: append([]string(nil), requestedPaths...),
	}
	if m == nil || m.client == nil {
		result.Error = "agent mail client not configured"
		return result, errors.New(result.Error)
	}
	expectedProjectID, err := m.ensureProject(ctx)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	wanted := make(map[string]struct{}, len(requestedPaths))
	for _, raw := range requestedPaths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		wanted[path] = struct{}{}
	}
	if len(wanted) == 0 {
		result.Error = "reservation reconciliation requires the original requested paths"
		return result, errors.New(result.Error)
	}
	reservations, err := m.client.ListReservations(ctx, m.projectKey, agentName, true)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	reason := fmt.Sprintf("bead assignment: %s", beadID)
	seen := make(map[string]struct{}, len(wanted))
	seenIDs := make(map[int]struct{})
	var validationErrors []error
	for _, reservation := range reservations {
		if reservation.ReleasedTS != nil || reservation.AgentName != agentName || reservation.Reason != reason {
			continue
		}
		path := strings.TrimSpace(reservation.PathPattern)
		if _, ok := wanted[path]; !ok {
			continue
		}
		if reservation.ID <= 0 {
			validationErrors = append(validationErrors, fmt.Errorf("active reservation for %s has no durable ID", path))
			continue
		}
		if _, duplicate := seenIDs[reservation.ID]; duplicate {
			validationErrors = append(validationErrors, fmt.Errorf("duplicate active reservation ID %d", reservation.ID))
			continue
		}
		seenIDs[reservation.ID] = struct{}{}
		if _, duplicate := seen[path]; !duplicate {
			seen[path] = struct{}{}
			result.GrantedPaths = append(result.GrantedPaths, path)
		}
		result.ReservationIDs = append(result.ReservationIDs, reservation.ID)
		if reservation.ProjectID != expectedProjectID {
			validationErrors = append(validationErrors, fmt.Errorf("active reservation %d for %s belongs to project %d, want %d", reservation.ID, path, reservation.ProjectID, expectedProjectID))
		}
		if !reservation.Exclusive {
			validationErrors = append(validationErrors, fmt.Errorf("active reservation %d for %s is not exclusive", reservation.ID, path))
		}
		expiresAt := reservation.ExpiresTS.Time
		if expiresAt.IsZero() || !expiresAt.After(time.Now().UTC()) {
			validationErrors = append(validationErrors, fmt.Errorf("active reservation %d for %s has no future expiry", reservation.ID, path))
		} else if result.ExpiresAt == nil || expiresAt.Before(*result.ExpiresAt) {
			result.ExpiresAt = &expiresAt
		}
	}
	sort.Strings(result.GrantedPaths)
	sort.Ints(result.ReservationIDs)
	result.Success = len(seen) == len(wanted) && len(validationErrors) == 0
	if len(seen) > 0 && !result.Success {
		result.Error = fmt.Sprintf("found %d of %d requested reservations", len(seen), len(wanted))
	}
	if validationErr := errors.Join(validationErrors...); validationErr != nil {
		result.Error = validationErr.Error()
		return result, validationErr
	}
	return result, nil
}

func (m *FileReservationManager) ensureProject(ctx context.Context) (int, error) {
	if m == nil || m.client == nil {
		return 0, errors.New("agent mail client not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	project, err := m.client.EnsureProject(ctx, m.projectKey)
	if err != nil {
		return 0, fmt.Errorf("ensure Agent Mail project %q: %w", m.projectKey, err)
	}
	if project == nil || project.ID <= 0 {
		return 0, fmt.Errorf("ensure Agent Mail project %q returned no durable project ID", m.projectKey)
	}
	expectedKey := strings.TrimSpace(m.projectKey)
	if expectedKey == "" {
		return 0, errors.New("agent-mail project binding requires a non-empty project key")
	}
	// Symlink-equivalent compare: Agent Mail canonicalizes project paths, so
	// the returned human key may be the resolved form of a symlinked key
	// (GH#239). Fail-closed semantics are preserved for genuinely different
	// directories.
	if humanKey := strings.TrimSpace(project.HumanKey); !agentmail.ProjectKeysEquivalent(humanKey, expectedKey) {
		return 0, fmt.Errorf("agent-mail project binding mismatch: got %q, want %q", humanKey, m.projectKey)
	}
	return project.ID, nil
}

func collectGrantedReservations(result *FileReservationResult, granted []agentmail.FileReservation, requested []string, agentName, expectedReason string, expectedProjectID int, now time.Time, allowDuplicatePaths bool) error {
	wanted := make(map[string]struct{}, len(requested))
	for _, raw := range requested {
		if path := strings.TrimSpace(raw); path != "" {
			wanted[path] = struct{}{}
		}
	}
	seenPaths := make(map[string]struct{}, len(wanted))
	seenIDs := make(map[int]struct{}, len(granted))
	var validationErrors []error
	for _, reservation := range granted {
		path := strings.TrimSpace(reservation.PathPattern)
		validHandle := true
		if reservation.ID <= 0 {
			validationErrors = append(validationErrors, fmt.Errorf("reservation for %q has no durable ID", path))
			validHandle = false
		} else if _, duplicate := seenIDs[reservation.ID]; duplicate {
			validationErrors = append(validationErrors, fmt.Errorf("duplicate reservation ID %d", reservation.ID))
			validHandle = false
		} else {
			seenIDs[reservation.ID] = struct{}{}
		}
		if reservation.ProjectID != expectedProjectID {
			validationErrors = append(validationErrors, fmt.Errorf("reservation %d for %q belongs to project %d, want %d", reservation.ID, path, reservation.ProjectID, expectedProjectID))
		}
		if reservation.ReleasedTS != nil {
			validationErrors = append(validationErrors, fmt.Errorf("reservation %d for %q is already released", reservation.ID, path))
		}
		if !reservation.Exclusive {
			validationErrors = append(validationErrors, fmt.Errorf("reservation %d for %q is not exclusive", reservation.ID, path))
		}
		if strings.TrimSpace(reservation.AgentName) != strings.TrimSpace(agentName) {
			validationErrors = append(validationErrors, fmt.Errorf("reservation %d agent mismatch: got %q, want %q", reservation.ID, reservation.AgentName, agentName))
		}
		if strings.TrimSpace(reservation.Reason) != strings.TrimSpace(expectedReason) {
			validationErrors = append(validationErrors, fmt.Errorf("reservation %d reason mismatch: got %q, want %q", reservation.ID, reservation.Reason, expectedReason))
		}
		expiresAt := reservation.ExpiresTS.Time
		if expiresAt.IsZero() || !expiresAt.After(now) {
			validationErrors = append(validationErrors, fmt.Errorf("reservation %d for %q has no future expiry", reservation.ID, path))
		}
		if _, requestedPath := wanted[path]; !requestedPath {
			validationErrors = append(validationErrors, fmt.Errorf("reservation %d returned unexpected path %q", reservation.ID, path))
		} else if _, duplicate := seenPaths[path]; duplicate && !allowDuplicatePaths {
			validationErrors = append(validationErrors, fmt.Errorf("multiple reservations returned for path %q", path))
		} else {
			seenPaths[path] = struct{}{}
		}
		if validHandle {
			recordGrantedReservation(result, reservation)
		}
	}
	for path := range wanted {
		if _, ok := seenPaths[path]; !ok {
			validationErrors = append(validationErrors, fmt.Errorf("reservation response omitted requested path %q", path))
		}
	}
	sort.Strings(result.GrantedPaths)
	sort.Ints(result.ReservationIDs)
	return errors.Join(validationErrors...)
}

func recordGrantedReservation(result *FileReservationResult, granted agentmail.FileReservation) {
	result.GrantedPaths = append(result.GrantedPaths, granted.PathPattern)
	result.ReservationIDs = append(result.ReservationIDs, granted.ID)
	expiresAt := granted.ExpiresTS.Time
	if !expiresAt.IsZero() && (result.ExpiresAt == nil || expiresAt.Before(*result.ExpiresAt)) {
		result.ExpiresAt = &expiresAt
	}
}

// ReleaseExactForBead releases active leases only after a durable assignment
// barrier prevents a newer generation for the same bead. Paths are used to
// recover a unique exact-binding lease whose Agent Mail ID drifted; ambiguous
// matches fail closed. Post-release active listings are authoritative.
func (m *FileReservationManager) ReleaseExactForBead(ctx context.Context, barrier *assignmentstore.Assignment, reservationIDs []int, paths []string) ([]string, error) {
	if m == nil || m.client == nil {
		return nil, errors.New("agent mail client not configured")
	}
	beadID, agentName, err := exactReleaseBarrierBinding(barrier)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	expectedProjectID, err := m.ensureProject(ctx)
	if err != nil {
		return nil, err
	}
	reason := fmt.Sprintf("bead assignment: %s", strings.TrimSpace(beadID))
	wantedIDs, err := normalizeReservationIDs(reservationIDs)
	if err != nil {
		return nil, err
	}
	wantedPaths := normalizeReservationPathSet(paths)
	if len(wantedIDs) == 0 && len(wantedPaths) == 0 {
		return nil, nil
	}

	releasedPaths := make(map[string]struct{})
	const reconciliationLimit = 3
	for attempt := 0; attempt < reconciliationLimit; attempt++ {
		active, listErr := m.client.ListReservations(ctx, m.projectKey, agentName, false)
		if listErr != nil {
			return nil, fmt.Errorf("list active reservations for exact release: %w", listErr)
		}
		selectedIDs, selectedPaths, selectErr := selectExactReleaseReservations(
			active, expectedProjectID, beadID, agentName, reason, wantedIDs, wantedPaths,
		)
		if selectErr != nil {
			return nil, selectErr
		}
		if len(selectedIDs) == 0 {
			return mapKeysSorted(releasedPaths), nil
		}
		for path := range selectedPaths {
			releasedPaths[path] = struct{}{}
		}
		// A release response may be lost after the server commits. Always
		// reconcile from the next authoritative active listing.
		_, _ = m.client.ReleaseReservations(ctx, m.projectKey, agentName, nil, sortedReservationIDs(selectedIDs))
	}

	remaining, err := m.client.ListReservations(ctx, m.projectKey, agentName, false)
	if err != nil {
		return nil, fmt.Errorf("verify exact reservation release: %w", err)
	}
	selectedIDs, _, err := selectExactReleaseReservations(
		remaining, expectedProjectID, beadID, agentName, reason, wantedIDs, wantedPaths,
	)
	if err != nil {
		return nil, err
	}
	if len(selectedIDs) > 0 {
		return nil, fmt.Errorf("%d exact reservations for bead %s remain active after %d release attempts", len(selectedIDs), beadID, reconciliationLimit)
	}
	return mapKeysSorted(releasedPaths), nil
}

func exactReleaseBarrierBinding(barrier *assignmentstore.Assignment) (string, string, error) {
	if barrier == nil {
		return "", "", errors.New("durable assignment release barrier is required")
	}
	if barrier.ClearState != assignmentstore.ClearStateReservationReleasing && barrier.ClearState != assignmentstore.ClearStateLeasesReleased {
		return "", "", fmt.Errorf("assignment %s has no durable release barrier", barrier.BeadID)
	}
	beadID := strings.TrimSpace(barrier.BeadID)
	agentName := strings.TrimSpace(barrier.ReservationAgent)
	if agentName == "" {
		agentName = strings.TrimSpace(barrier.AgentName)
	}
	if beadID == "" || agentName == "" {
		return "", "", errors.New("release barrier requires bead and reservation agent identity")
	}
	return beadID, agentName, nil
}

func selectExactReleaseReservations(
	active []agentmail.FileReservation,
	expectedProjectID int,
	beadID, agentName, reason string,
	wantedIDs map[int]struct{},
	wantedPaths map[string]struct{},
) (map[int]struct{}, map[string]struct{}, error) {
	selectedIDs := make(map[int]struct{})
	selectedPaths := make(map[string]struct{})
	matchesByPath := make(map[string][]int, len(wantedPaths))
	for _, reservation := range active {
		if reservation.ReleasedTS != nil {
			continue
		}
		path := strings.TrimSpace(reservation.PathPattern)
		_, requestedByID := wantedIDs[reservation.ID]
		_, requestedByPath := wantedPaths[path]
		exactBinding := reservation.ID > 0 && reservation.ProjectID == expectedProjectID &&
			strings.TrimSpace(reservation.AgentName) == agentName && strings.TrimSpace(reservation.Reason) == reason
		if requestedByID && !exactBinding {
			return nil, nil, fmt.Errorf("reservation %d is not authoritatively bound to bead %s", reservation.ID, beadID)
		}
		if requestedByID && len(wantedPaths) > 0 && !requestedByPath {
			return nil, nil, fmt.Errorf("reservation %d path %q is not authoritatively bound to bead %s durable paths", reservation.ID, path, beadID)
		}
		if requestedByPath && reservation.ProjectID == expectedProjectID &&
			strings.TrimSpace(reservation.AgentName) == agentName && strings.TrimSpace(reservation.Reason) == reason && reservation.ID <= 0 {
			return nil, nil, fmt.Errorf("active reservation for path %q has no durable ID", path)
		}
		if exactBinding && requestedByPath {
			matchesByPath[path] = append(matchesByPath[path], reservation.ID)
		}
		if requestedByID && exactBinding && len(wantedPaths) == 0 {
			selectedIDs[reservation.ID] = struct{}{}
			if path != "" {
				selectedPaths[path] = struct{}{}
			}
		}
	}
	for path := range wantedPaths {
		matches := matchesByPath[path]
		switch len(matches) {
		case 0:
			continue
		case 1:
			if _, duplicateID := selectedIDs[matches[0]]; duplicateID {
				return nil, nil, fmt.Errorf("reservation %d is bound to multiple requested paths", matches[0])
			}
			selectedIDs[matches[0]] = struct{}{}
			selectedPaths[path] = struct{}{}
		default:
			return nil, nil, fmt.Errorf("exact reservation cleanup for %q is ambiguous: found %d active matching leases", path, len(matches))
		}
	}
	return selectedIDs, selectedPaths, nil
}

func normalizeReservationIDs(ids []int) (map[int]struct{}, error) {
	result := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("invalid reservation ID %d", id)
		}
		if _, duplicate := result[id]; duplicate {
			return nil, fmt.Errorf("duplicate reservation ID %d", id)
		}
		result[id] = struct{}{}
	}
	return result, nil
}

func normalizeReservationPathSet(paths []string) map[string]struct{} {
	result := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		if path := strings.TrimSpace(rawPath); path != "" {
			result[path] = struct{}{}
		}
	}
	return result
}

func sortedReservationIDs(ids map[int]struct{}) []int {
	result := make([]int, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Ints(result)
	return result
}

func mapKeysSorted(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
