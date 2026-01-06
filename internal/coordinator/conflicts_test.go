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
