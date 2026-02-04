package coordinator

import (
	"strings"
	"testing"
	"time"
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
