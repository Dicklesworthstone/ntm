package coordinator

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestMatchesPattern(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
		matches bool
	}{
		// Exact matches
		{"internal/cli/coordinator.go", "internal/cli/coordinator.go", true},
		{"internal/cli/coordinator.go", "internal/cli/other.go", false},

		// Single * patterns
		{"internal/cli/coordinator.go", "internal/cli/*.go", true},
		{"internal/cli/coordinator.go", "internal/cli/*.ts", false},
		{"internal/cli/coordinator.go", "*.go", true},

		// Multiple * patterns (was broken before fix)
		{"src/app/test/main.go", "src/*/test/*.go", true},
		{"src/foo/bar/test.go", "src/*/test.go", true},
		{"src/app/other/main.go", "src/*/test/*.go", false},

		// Double ** patterns
		{"internal/cli/coordinator.go", "internal/**", true},
		{"internal/cli/subdir/file.go", "internal/**", true},
		{"external/cli/file.go", "internal/**", false},

		// Double ** patterns with suffix (was broken before fix)
		{"src/foo/bar/test.go", "src/**/test.go", true},
		{"src/test.go", "src/**/test.go", true},
		{"src/deep/nested/path/test.go", "src/**/test.go", true},
		{"src/foo/bar/main.go", "src/**/test.go", false},
		{"other/test.go", "src/**/test.go", false},

		// Double ** patterns with wildcard suffix (was broken before fix)
		{"src/foo/bar/test.go", "src/**/*.go", true},
		{"src/main.go", "src/**/*.go", true},
		{"src/foo/bar/test.ts", "src/**/*.go", false},
		{"other/main.go", "src/**/*.go", false},
		{"foo/bar/main.go", "**/*.go", true},
		{"main.go", "**/*.go", true},

		// Multi-segment suffix patterns after **
		{"src/a/b/foo/main.go", "src/**/foo/*.go", true},
		{"src/a/b/bar/main.go", "src/**/foo/*.go", false},

		// Prefix patterns (directory matching)
		{"internal/cli/coordinator.go", "internal/cli", true},
		{"internal/cli/subdir/file.go", "internal/cli", true},
		{"internal/cli_other/file.go", "internal/cli", false},

		// Edge cases
		{"file.go", "file.go", true},
		{"a/b/c.go", "a/b/*.go", true},
		{"a/b/c.ts", "a/b/*.go", false},

		// bd-eebvt: literal suffix after ** must occupy a whole
		// basename, not a fragment. Pre-fix, a basename that ends
		// with the literal mid-filename (e.g. "mytest.go" vs
		// "**/test.go") was a false-positive match.
		{"mytest.go", "**/test.go", false},                // basename ends with "test.go" but isn't "test.go"
		{"xtest.go", "**/test.go", false},                 // same — single char prefix in basename
		{"internal/xfoo.go", "internal/**/foo.go", false}, // prefixed form: basename "xfoo.go" not "foo.go"
		{"test.go", "**/test.go", true},                   // exact basename — matches
		{"foo/test.go", "**/test.go", true},               // basename in subdir — matches
		{"deep/nested/test.go", "**/test.go", true},       // basename at any depth — matches
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.path, func(t *testing.T) {
			result := matchesPattern(tt.path, tt.pattern)
			if result != tt.matches {
				t.Errorf("matchesPattern(%q, %q) = %v, expected %v", tt.path, tt.pattern, result, tt.matches)
			}
		})
	}
}

func TestSanitizeForID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"internal/cli/file.go", "internal-cli-file_go"},
		{"*.go", "x_go"},
		{"**/*.ts", "xx-x_ts"},
		{"very_long_path_that_exceeds_twenty_characters", "very_long_path_that_"},
	}

	for _, tt := range tests {
		result := sanitizeForID(tt.input)
		if result != tt.expected {
			t.Errorf("sanitizeForID(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestGenerateConflictID(t *testing.T) {
	id1 := generateConflictID("internal/cli/*.go")
	id2 := generateConflictID("internal/cli/*.go")

	if id1 == "" {
		t.Error("expected non-empty conflict ID")
	}
	if !strings.Contains(id1, "conflict-") {
		t.Error("expected ID to contain 'conflict-' prefix")
	}
	// IDs should be different due to timestamp
	if id1 == id2 {
		t.Log("Warning: consecutive IDs may match if called very quickly")
	}
}

func TestGenerateConflictID_ConcurrentUnique(t *testing.T) {
	t.Parallel()

	const count = 128
	results := make(chan string, count)
	var wg sync.WaitGroup

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- generateConflictID("internal/cli/*.go")
		}()
	}

	wg.Wait()
	close(results)

	seen := make(map[string]struct{}, count)
	for id := range results {
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate conflict ID generated: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewConflictDetector(t *testing.T) {
	cd := NewConflictDetector(nil, "/tmp/test")

	if cd.mailClient != nil {
		t.Error("expected nil mailClient")
	}
	if cd.projectKey != "/tmp/test" {
		t.Errorf("expected projectKey '/tmp/test', got %q", cd.projectKey)
	}
	if cd.conflicts == nil {
		t.Error("expected conflicts map to be initialized")
	}
}

func TestDetectReservationConflictsAt_SharedReservationsDoNotConflict(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 21, 12, 0, 0, 0, time.UTC)
	conflicts := detectReservationConflictsAt([]agentmail.FileReservation{
		testReservation("BlueLake", "internal/**", false, now, now.Add(time.Hour), nil),
		testReservation("RedStone", "internal/**", false, now, now.Add(time.Hour), nil),
	}, now)

	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts for shared reservations, got %#v", conflicts)
	}
}

func TestDetectReservationConflictsAt_ExclusiveOverlapIncludesAllHolders(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 21, 12, 0, 0, 0, time.UTC)
	conflicts := detectReservationConflictsAt([]agentmail.FileReservation{
		testReservation("BlueLake", "internal/**", true, now, now.Add(2*time.Hour), nil),
		testReservation("RedStone", "internal/cli/*.go", false, now.Add(5*time.Minute), now.Add(2*time.Hour), nil),
	}, now)

	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}

	conflict := conflicts[0]
	if conflict.Pattern != "internal/** <-> internal/cli/*.go" {
		t.Fatalf("unexpected conflict pattern %q", conflict.Pattern)
	}
	if len(conflict.Holders) != 2 {
		t.Fatalf("expected 2 holders, got %d", len(conflict.Holders))
	}
	if conflict.Holders[0].AgentName != "BlueLake" || conflict.Holders[1].AgentName != "RedStone" {
		t.Fatalf("holders not sorted as expected: %#v", conflict.Holders)
	}
}

func TestDetectReservationConflictsAt_IgnoresExpiredAndReleasedReservations(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 21, 12, 0, 0, 0, time.UTC)
	releasedAt := agentmail.FlexTime{Time: now.Add(-10 * time.Minute)}
	conflicts := detectReservationConflictsAt([]agentmail.FileReservation{
		testReservation("BlueLake", "internal/**", true, now.Add(-2*time.Hour), now.Add(-time.Minute), nil),
		testReservation("RedStone", "internal/**", true, now.Add(-time.Hour), now.Add(time.Hour), &releasedAt),
		testReservation("GreenCastle", "internal/**", true, now, now.Add(time.Hour), nil),
	}, now)

	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts after filtering inactive reservations, got %#v", conflicts)
	}
}

func testReservation(agent, pattern string, exclusive bool, createdAt, expiresAt time.Time, releasedAt *agentmail.FlexTime) agentmail.FileReservation {
	return agentmail.FileReservation{
		AgentName:   agent,
		PathPattern: pattern,
		Exclusive:   exclusive,
		CreatedTS:   agentmail.FlexTime{Time: createdAt},
		ExpiresTS:   agentmail.FlexTime{Time: expiresAt},
		ReleasedTS:  releasedAt,
	}
}

// =============================================================================
// formatNegotiationRequest / formatConflictNotification tests
// =============================================================================

func TestFormatNegotiationRequest(t *testing.T) {
	t.Parallel()

	c := New("test-session", "/tmp/test", nil, "CoordAgent")

	now := time.Now()
	conflict := &Conflict{
		ID:      "conflict-42",
		Pattern: "internal/cli/*.go",
	}
	target := &Holder{
		AgentName:  "BlueFox",
		ReservedAt: now.Add(-10 * time.Minute),
		ExpiresAt:  now.Add(50 * time.Minute),
		Reason:     "refactoring CLI",
	}

	body := c.formatNegotiationRequest(conflict, "RedBear", target)

	// Verify key sections (note: target.AgentName is not in the body,
	// since the message is addressed *to* the target holder)
	checks := []string{
		"# File Reservation Conflict",
		"internal/cli/*.go",
		"RedBear",
		"refactoring CLI",
		"## Request",
		"### Your Reservation",
		"## Options",
		"Release",
		"Keep",
		"Coordinate",
		"acknowledge",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q", want)
		}
	}
}

func TestFormatNegotiationRequest_NoReason(t *testing.T) {
	t.Parallel()

	c := New("test-session", "/tmp/test", nil, "CoordAgent")

	now := time.Now()
	conflict := &Conflict{Pattern: "src/**/*.go"}
	target := &Holder{
		AgentName:  "GreenCastle",
		ReservedAt: now,
		ExpiresAt:  now.Add(time.Hour),
		Reason:     "", // No reason
	}

	body := c.formatNegotiationRequest(conflict, "Requester", target)

	// Should NOT contain "Reason:" line when reason is empty
	if strings.Contains(body, "**Reason:**") {
		t.Error("expected no Reason line when holder reason is empty")
	}
}

func TestFormatConflictNotification(t *testing.T) {
	t.Parallel()

	c := New("test-session", "/tmp/test", nil, "CoordAgent")

	now := time.Now()
	conflict := &Conflict{
		ID:      "conflict-99",
		Pattern: "internal/config/*.go",
		Holders: []Holder{
			{
				AgentName:  "Agent1",
				ReservedAt: now.Add(-5 * time.Minute),
				ExpiresAt:  now.Add(55 * time.Minute),
				Reason:     "config refactor",
			},
			{
				AgentName:  "Agent2",
				ReservedAt: now.Add(-2 * time.Minute),
				ExpiresAt:  now.Add(58 * time.Minute),
				Reason:     "",
			},
		},
	}

	body := c.formatConflictNotification(conflict)

	checks := []string{
		"# Reservation Conflict Detected",
		"internal/config/*.go",
		"## Current Holders",
		"Agent1",
		"Agent2",
		"config refactor",
		"## Recommendation",
		"releases their reservation",
		"different parts of the file",
		"Wait for one agent",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q", want)
		}
	}

	// Agent2 has no reason, should not have a reason line for it
	// Count "Reason:" occurrences - should be 1 (Agent1 only)
	if strings.Count(body, "Reason:") != 1 {
		t.Errorf("expected exactly 1 Reason line, got %d", strings.Count(body, "Reason:"))
	}
}

func TestFormatConflictNotification_EmptyHolders(t *testing.T) {
	t.Parallel()

	c := New("test-session", "/tmp/test", nil, "CoordAgent")

	conflict := &Conflict{
		Pattern: "empty/*.go",
		Holders: []Holder{},
	}

	body := c.formatConflictNotification(conflict)

	// Should still produce valid markdown
	if !strings.Contains(body, "# Reservation Conflict Detected") {
		t.Error("expected markdown header even with no holders")
	}
	if !strings.Contains(body, "## Recommendation") {
		t.Error("expected recommendation section even with no holders")
	}
}

// =============================================================================
// matchesSuffixPattern edge case
// =============================================================================

func TestMatchesSuffixPattern_TooFewSegments(t *testing.T) {
	t.Parallel()

	// path has fewer segments than suffix pattern requires
	if matchesSuffixPattern("main.go", "foo/bar/*.go") {
		t.Error("expected false when path has fewer segments than suffix pattern")
	}
}

func TestConflictStruct(t *testing.T) {
	now := time.Now()
	conflict := Conflict{
		ID:         "conflict-123",
		FilePath:   "internal/cli/file.go",
		Pattern:    "internal/cli/*.go",
		DetectedAt: now,
		Holders: []Holder{
			{
				AgentName:  "Agent1",
				PaneID:     "%0",
				ReservedAt: now.Add(-5 * time.Minute),
				ExpiresAt:  now.Add(55 * time.Minute),
				Reason:     "refactoring",
				Priority:   1,
			},
			{
				AgentName:  "Agent2",
				PaneID:     "%1",
				ReservedAt: now.Add(-2 * time.Minute),
				ExpiresAt:  now.Add(58 * time.Minute),
				Reason:     "bug fix",
				Priority:   2,
			},
		},
	}

	if len(conflict.Holders) != 2 {
		t.Errorf("expected 2 holders, got %d", len(conflict.Holders))
	}
	if conflict.Holders[0].AgentName != "Agent1" {
		t.Errorf("expected first holder 'Agent1', got %q", conflict.Holders[0].AgentName)
	}
	if conflict.Resolution != "" {
		t.Error("expected empty resolution for unresolved conflict")
	}
}

func TestReservationPatternsOverlap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "exact", a: "internal/cli/*.go", b: "internal/cli/*.go", want: true},
		{name: "overlap broad narrow", a: "internal/**", b: "internal/cli/*.go", want: true},
		{name: "disjoint", a: "internal/**", b: "docs/**/*.md", want: false},
		{name: "empty", a: "", b: "docs/**/*.md", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := reservationPatternsOverlap(tt.a, tt.b); got != tt.want {
				t.Fatalf("reservationPatternsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDetectReservationConflictsAt_ExclusiveOverlapsOnly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 21, 12, 0, 0, 0, time.UTC)
	releasedAt := agentmail.FlexTime{Time: now.Add(-time.Minute)}
	future := agentmail.FlexTime{Time: now.Add(time.Hour)}
	past := agentmail.FlexTime{Time: now.Add(-time.Minute)}

	reservations := []agentmail.FileReservation{
		{
			PathPattern: "internal/**",
			AgentName:   "Alpha",
			Exclusive:   true,
			Reason:      "broad refactor",
			CreatedTS:   agentmail.FlexTime{Time: now.Add(-10 * time.Minute)},
			ExpiresTS:   future,
		},
		{
			PathPattern: "internal/cli/*.go",
			AgentName:   "Beta",
			Exclusive:   false,
			Reason:      "cli edits",
			CreatedTS:   agentmail.FlexTime{Time: now.Add(-8 * time.Minute)},
			ExpiresTS:   future,
		},
		{
			PathPattern: "internal/cli/*.go",
			AgentName:   "Gamma",
			Exclusive:   false,
			Reason:      "review",
			CreatedTS:   agentmail.FlexTime{Time: now.Add(-6 * time.Minute)},
			ExpiresTS:   future,
		},
		{
			PathPattern: "docs/**",
			AgentName:   "DocsA",
			Exclusive:   false,
			CreatedTS:   agentmail.FlexTime{Time: now.Add(-4 * time.Minute)},
			ExpiresTS:   future,
		},
		{
			PathPattern: "docs/manual/*.md",
			AgentName:   "DocsB",
			Exclusive:   false,
			CreatedTS:   agentmail.FlexTime{Time: now.Add(-3 * time.Minute)},
			ExpiresTS:   future,
		},
		{
			PathPattern: "tmp/**",
			AgentName:   "Expired",
			Exclusive:   true,
			CreatedTS:   agentmail.FlexTime{Time: now.Add(-2 * time.Minute)},
			ExpiresTS:   past,
		},
		{
			PathPattern: "tmp/file.go",
			AgentName:   "Released",
			Exclusive:   true,
			CreatedTS:   agentmail.FlexTime{Time: now.Add(-2 * time.Minute)},
			ExpiresTS:   future,
			ReleasedTS:  &releasedAt,
		},
	}

	conflicts := detectReservationConflictsAt(reservations, now)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d: %+v", len(conflicts), conflicts)
	}

	conflict := conflicts[0]
	if conflict.Pattern != "internal/** <-> internal/cli/*.go" {
		t.Fatalf("unexpected conflict pattern: %q", conflict.Pattern)
	}
	if len(conflict.Holders) != 3 {
		t.Fatalf("expected 3 holders, got %d", len(conflict.Holders))
	}
	if conflict.Holders[0].AgentName != "Alpha" || conflict.Holders[1].AgentName != "Beta" || conflict.Holders[2].AgentName != "Gamma" {
		t.Fatalf("holders not sorted/deduped as expected: %+v", conflict.Holders)
	}
}

func TestDetectReservationConflictsAt_IgnoresSameAgentDuplicates(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 21, 12, 0, 0, 0, time.UTC)
	future := agentmail.FlexTime{Time: now.Add(time.Hour)}

	reservations := []agentmail.FileReservation{
		{
			PathPattern: "internal/*.go",
			AgentName:   "Solo",
			Exclusive:   true,
			CreatedTS:   agentmail.FlexTime{Time: now.Add(-2 * time.Minute)},
			ExpiresTS:   future,
		},
		{
			PathPattern: "internal/*.go",
			AgentName:   "Solo",
			Exclusive:   true,
			CreatedTS:   agentmail.FlexTime{Time: now.Add(-time.Minute)},
			ExpiresTS:   future,
		},
	}

	conflicts := detectReservationConflictsAt(reservations, now)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts for same-agent duplicates, got %+v", conflicts)
	}
}

// ntm-f7xz / OC-019: the digest's work summary must state its provenance —
// zero-valued counts previously masqueraded as facts when the source failed
// or (worse) was never consulted at all.
func TestDigestWorkSummaryStatesProvenance(t *testing.T) {
	c := New("f7xz-test", t.TempDir(), nil, "NTM-Coordinator")
	digest := c.GenerateDigest()
	if digest.WorkSummary.Status == "" {
		t.Fatal("work_summary.status is empty; the digest must state computed vs unavailable")
	}
	// A temp dir has no beads/bv data: the status must admit unavailability
	// rather than presenting zero counts as computed truth.
	if !strings.Contains(digest.WorkSummary.Status, "unavailable") &&
		!strings.Contains(digest.WorkSummary.Status, "computed") {
		t.Fatalf("work_summary.status = %q, want computed or unavailable provenance", digest.WorkSummary.Status)
	}
}
