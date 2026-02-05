package cli

import (
	"testing"
	"time"
)

func TestMatchesNTMPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Lifecycle directories
		{name: "lifecycle pattern", input: "ntm-lifecycle-abc123", expected: "ntm-lifecycle-*"},
		{name: "lifecycle with uuid", input: "ntm-lifecycle-550e8400-e29b-41d4-a716-446655440000", expected: "ntm-lifecycle-*"},

		// Description files
		{name: "desc md file", input: "ntm-myproject-desc.md", expected: "ntm-*-desc.md"},
		{name: "desc md with numbers", input: "ntm-123-desc.md", expected: "ntm-*-desc.md"},

		// Graph files
		{name: "graph html exact", input: "ntm-graph.html", expected: "ntm-graph.html"},

		// Test directories
		{name: "test-ntm dir", input: "test-ntm-spawn", expected: "test-ntm-*"},
		{name: "test-ntm with timestamp", input: "test-ntm-20260205", expected: "test-ntm-*"},

		// Atomic files
		{name: "atomic file", input: "ntm-atomic-config.json", expected: "ntm-atomic-*"},

		// Prompt files
		{name: "prompt md file", input: "ntm-prompt-abc.md", expected: "ntm-prompt-*.md"},

		// Mail files
		{name: "mail md file", input: "ntm-mail-xyz.md", expected: "ntm-mail-*.md"},

		// Handoff files
		{name: "handoff tmp", input: ".handoff-myproject.tmp", expected: ".handoff-*.tmp"},

		// Events rotate
		{name: "events rotate", input: "events-rotate-12345.jsonl", expected: "events-rotate-*.jsonl"},

		// Rotation tmp
		{name: "rotation tmp", input: "rotation-abc.tmp", expected: "rotation-*.tmp"},

		// Pending tmp
		{name: "pending tmp", input: "pending-123.tmp", expected: "pending-*.tmp"},

		// Non-matching
		{name: "unrelated file", input: "some-other-file.txt", expected: ""},
		{name: "partial match no ext", input: "ntm-prompt-abc", expected: ""},
		{name: "wrong prefix", input: "foo-lifecycle-abc", expected: ""},
		{name: "empty string", input: "", expected: ""},
		{name: "just ntm", input: "ntm", expected: ""},
		{name: "graph wrong ext", input: "ntm-graph.json", expected: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := matchesNTMPattern(tc.input)
			if result != tc.expected {
				t.Errorf("matchesNTMPattern(%q) = %q; want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestFormatCleanupDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		// Less than 1 minute (rounds to nearest minute)
		{name: "0 seconds", duration: 0, expected: "0m"},
		{name: "30 seconds", duration: 30 * time.Second, expected: "0m"}, // 0.5m rounds to 0 (banker's rounding)
		{name: "59 seconds", duration: 59 * time.Second, expected: "1m"}, // 0.98m rounds to 1

		// Less than 1 hour
		{name: "1 minute", duration: 1 * time.Minute, expected: "1m"},
		{name: "30 minutes", duration: 30 * time.Minute, expected: "30m"},
		{name: "59 minutes", duration: 59 * time.Minute, expected: "59m"},

		// Less than 24 hours
		{name: "1 hour", duration: 1 * time.Hour, expected: "1.0h"},
		{name: "1.5 hours", duration: 90 * time.Minute, expected: "1.5h"},
		{name: "12 hours", duration: 12 * time.Hour, expected: "12.0h"},
		{name: "23.9 hours", duration: 23*time.Hour + 54*time.Minute, expected: "23.9h"},

		// 24+ hours
		{name: "24 hours", duration: 24 * time.Hour, expected: "1.0d"},
		{name: "36 hours", duration: 36 * time.Hour, expected: "1.5d"},
		{name: "48 hours", duration: 48 * time.Hour, expected: "2.0d"},
		{name: "72 hours", duration: 72 * time.Hour, expected: "3.0d"},
		{name: "1 week", duration: 7 * 24 * time.Hour, expected: "7.0d"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := formatCleanupDuration(tc.duration)
			if result != tc.expected {
				t.Errorf("formatCleanupDuration(%v) = %q; want %q", tc.duration, result, tc.expected)
			}
		})
	}
}

func TestGetFileType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		isDir    bool
		expected string
	}{
		{name: "directory", isDir: true, expected: "directory"},
		{name: "file", isDir: false, expected: "file"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := getFileType(tc.isDir)
			if result != tc.expected {
				t.Errorf("getFileType(%v) = %q; want %q", tc.isDir, result, tc.expected)
			}
		})
	}
}
