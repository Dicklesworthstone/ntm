package audit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// Helper to create test audit log directory and populate with test data
func setupTestAuditDir(t *testing.T) (string, func()) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "audit_query_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	auditDir := filepath.Join(tempDir, ".local", "share", "ntm", "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("Failed to create audit dir: %v", err)
	}

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return auditDir, cleanup
}

// Helper to write test entries to a log file
func writeTestEntries(t *testing.T, auditDir string, sessionID string, date string, entries []AuditEntry) {
	t.Helper()

	filename := sessionID + "-" + date + ".jsonl"
	filePath := filepath.Join(auditDir, filename)

	file, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("Failed to create test log file: %v", err)
	}
	defer file.Close()

	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("Failed to marshal entry: %v", err)
		}
		file.Write(data)
		file.WriteString("\n")
	}
}

func TestSearcher_Search_BasicFilters(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	// Create test entries
	now := time.Now().UTC()
	entries := []AuditEntry{
		{
			Timestamp:   now.Add(-1 * time.Hour),
			SessionID:   "test-session",
			EventType:   EventTypeCommand,
			Actor:       ActorUser,
			Target:      "pane-1",
			SequenceNum: 1,
			Checksum:    "abc123",
		},
		{
			Timestamp:   now.Add(-30 * time.Minute),
			SessionID:   "test-session",
			EventType:   EventTypeSend,
			Actor:       ActorSystem,
			Target:      "myproject__cc_1",
			SequenceNum: 2,
			Checksum:    "def456",
		},
		{
			Timestamp:   now.Add(-15 * time.Minute),
			SessionID:   "test-session",
			EventType:   EventTypeError,
			Actor:       ActorAgent,
			Target:      "myproject__cod_1",
			SequenceNum: 3,
			Checksum:    "ghi789",
		},
	}

	writeTestEntries(t, auditDir, "test-session", now.Format("2006-01-02"), entries)

	searcher := NewSearcherWithPath(auditDir)

	// Test: No filters (get all)
	t.Run("NoFilters", func(t *testing.T) {
		result, err := searcher.Search(Query{})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 3 {
			t.Errorf("Expected 3 entries, got %d", len(result.Entries))
		}
	})

	// Test: Filter by event type
	t.Run("FilterByEventType", func(t *testing.T) {
		result, err := searcher.Search(Query{
			EventTypes: []EventType{EventTypeSend},
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 1 {
			t.Errorf("Expected 1 entry, got %d", len(result.Entries))
		}
		if result.Entries[0].EventType != EventTypeSend {
			t.Errorf("Expected EventTypeSend, got %s", result.Entries[0].EventType)
		}
	})

	// Test: Filter by actor
	t.Run("FilterByActor", func(t *testing.T) {
		result, err := searcher.Search(Query{
			Actors: []Actor{ActorUser, ActorAgent},
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 2 {
			t.Errorf("Expected 2 entries, got %d", len(result.Entries))
		}
	})

	// Test: Filter by session
	t.Run("FilterBySession", func(t *testing.T) {
		result, err := searcher.Search(Query{
			Sessions: []string{"test-session"},
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 3 {
			t.Errorf("Expected 3 entries, got %d", len(result.Entries))
		}

		// Non-existent session
		result, err = searcher.Search(Query{
			Sessions: []string{"nonexistent"},
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 0 {
			t.Errorf("Expected 0 entries, got %d", len(result.Entries))
		}
	})
}

func TestSearcher_Search_TimeRange(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	now := time.Now().UTC()
	entries := []AuditEntry{
		{
			Timestamp:   now.Add(-2 * time.Hour),
			SessionID:   "test-session",
			EventType:   EventTypeCommand,
			Actor:       ActorUser,
			Target:      "old-entry",
			SequenceNum: 1,
			Checksum:    "abc",
		},
		{
			Timestamp:   now.Add(-30 * time.Minute),
			SessionID:   "test-session",
			EventType:   EventTypeCommand,
			Actor:       ActorUser,
			Target:      "recent-entry",
			SequenceNum: 2,
			Checksum:    "def",
		},
	}

	writeTestEntries(t, auditDir, "test-session", now.Format("2006-01-02"), entries)

	searcher := NewSearcherWithPath(auditDir)

	// Test: Since filter
	t.Run("SinceFilter", func(t *testing.T) {
		since := now.Add(-1 * time.Hour)
		result, err := searcher.Search(Query{
			Since: &since,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 1 {
			t.Errorf("Expected 1 entry, got %d", len(result.Entries))
		}
		if result.Entries[0].Target != "recent-entry" {
			t.Errorf("Expected recent-entry, got %s", result.Entries[0].Target)
		}
	})

	// Test: Until filter
	t.Run("UntilFilter", func(t *testing.T) {
		until := now.Add(-1 * time.Hour)
		result, err := searcher.Search(Query{
			Until: &until,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 1 {
			t.Errorf("Expected 1 entry, got %d", len(result.Entries))
		}
		if result.Entries[0].Target != "old-entry" {
			t.Errorf("Expected old-entry, got %s", result.Entries[0].Target)
		}
	})

	// Test: Both since and until
	t.Run("TimeRange", func(t *testing.T) {
		since := now.Add(-3 * time.Hour)
		until := now.Add(-1 * time.Hour)
		result, err := searcher.Search(Query{
			Since: &since,
			Until: &until,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 1 {
			t.Errorf("Expected 1 entry, got %d", len(result.Entries))
		}
	})
}

func TestSearcher_Search_TargetPattern(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	now := time.Now().UTC()
	entries := []AuditEntry{
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeSend,
			Actor:       ActorSystem,
			Target:      "myproject__cc_1",
			SequenceNum: 1,
			Checksum:    "a",
		},
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeSend,
			Actor:       ActorSystem,
			Target:      "myproject__cc_2",
			SequenceNum: 2,
			Checksum:    "b",
		},
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeSend,
			Actor:       ActorSystem,
			Target:      "myproject__cod_1",
			SequenceNum: 3,
			Checksum:    "c",
		},
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeSend,
			Actor:       ActorSystem,
			Target:      "other__gmi_1",
			SequenceNum: 4,
			Checksum:    "d",
		},
	}

	writeTestEntries(t, auditDir, "test-session", now.Format("2006-01-02"), entries)

	searcher := NewSearcherWithPath(auditDir)

	// Test: Glob pattern with *
	t.Run("GlobStar", func(t *testing.T) {
		result, err := searcher.Search(Query{
			TargetPattern: "myproject__cc_*",
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 2 {
			t.Errorf("Expected 2 entries, got %d", len(result.Entries))
		}
	})

	// Test: Glob pattern with ?
	t.Run("GlobQuestion", func(t *testing.T) {
		result, err := searcher.Search(Query{
			TargetPattern: "myproject__cc_?",
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 2 {
			t.Errorf("Expected 2 entries, got %d", len(result.Entries))
		}
	})

	// Test: Broader pattern
	t.Run("BroadPattern", func(t *testing.T) {
		result, err := searcher.Search(Query{
			TargetPattern: "*__*_1",
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 3 {
			t.Errorf("Expected 3 entries (cc_1, cod_1, gmi_1), got %d", len(result.Entries))
		}
	})
}

func TestSearcher_Search_GrepPattern(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	now := time.Now().UTC()
	entries := []AuditEntry{
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeError,
			Actor:       ActorAgent,
			Target:      "agent-1",
			Payload:     map[string]interface{}{"error": "authentication failed"},
			SequenceNum: 1,
			Checksum:    "a",
		},
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeCommand,
			Actor:       ActorUser,
			Target:      "agent-1",
			Payload:     map[string]interface{}{"command": "ls -la"},
			SequenceNum: 2,
			Checksum:    "b",
		},
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeError,
			Actor:       ActorAgent,
			Target:      "agent-2",
			Payload:     map[string]interface{}{"error": "timeout exceeded"},
			SequenceNum: 3,
			Checksum:    "c",
		},
	}

	writeTestEntries(t, auditDir, "test-session", now.Format("2006-01-02"), entries)

	searcher := NewSearcherWithPath(auditDir)

	// Test: Grep for specific text
	t.Run("GrepText", func(t *testing.T) {
		result, err := searcher.Search(Query{
			GrepPattern: "authentication",
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 1 {
			t.Errorf("Expected 1 entry, got %d", len(result.Entries))
		}
	})

	// Test: Grep with regex
	t.Run("GrepRegex", func(t *testing.T) {
		result, err := searcher.Search(Query{
			GrepPattern: "error.*",
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 2 {
			t.Errorf("Expected 2 entries, got %d", len(result.Entries))
		}
	})

	// Test: Grep for error type and text
	t.Run("GrepCombined", func(t *testing.T) {
		result, err := searcher.Search(Query{
			EventTypes:  []EventType{EventTypeError},
			GrepPattern: "timeout",
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 1 {
			t.Errorf("Expected 1 entry, got %d", len(result.Entries))
		}
	})
}

func TestSearcher_Search_Pagination(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	now := time.Now().UTC()
	entries := make([]AuditEntry, 10)
	for i := 0; i < 10; i++ {
		entries[i] = AuditEntry{
			Timestamp:   now.Add(time.Duration(i) * time.Minute),
			SessionID:   "test-session",
			EventType:   EventTypeCommand,
			Actor:       ActorUser,
			Target:      "target",
			SequenceNum: uint64(i + 1),
			Checksum:    string(rune('a' + i)),
		}
	}

	writeTestEntries(t, auditDir, "test-session", now.Format("2006-01-02"), entries)

	searcher := NewSearcherWithPath(auditDir)

	// Test: Limit
	t.Run("Limit", func(t *testing.T) {
		result, err := searcher.Search(Query{
			Limit: 3,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 3 {
			t.Errorf("Expected 3 entries, got %d", len(result.Entries))
		}
		if !result.Truncated {
			t.Error("Expected Truncated to be true")
		}
	})

	// Test: Offset
	t.Run("Offset", func(t *testing.T) {
		result, err := searcher.Search(Query{
			Offset: 5,
			Limit:  10,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 5 {
			t.Errorf("Expected 5 entries, got %d", len(result.Entries))
		}
	})

	// Test: Offset + Limit
	t.Run("OffsetAndLimit", func(t *testing.T) {
		result, err := searcher.Search(Query{
			Offset: 2,
			Limit:  3,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(result.Entries) != 3 {
			t.Errorf("Expected 3 entries, got %d", len(result.Entries))
		}
	})
}

func TestSearcher_Count(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	now := time.Now().UTC()
	entries := []AuditEntry{
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeCommand,
			Actor:       ActorUser,
			Target:      "t1",
			SequenceNum: 1,
			Checksum:    "a",
		},
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeSend,
			Actor:       ActorSystem,
			Target:      "t2",
			SequenceNum: 2,
			Checksum:    "b",
		},
		{
			Timestamp:   now,
			SessionID:   "test-session",
			EventType:   EventTypeSend,
			Actor:       ActorSystem,
			Target:      "t3",
			SequenceNum: 3,
			Checksum:    "c",
		},
	}

	writeTestEntries(t, auditDir, "test-session", now.Format("2006-01-02"), entries)

	searcher := NewSearcherWithPath(auditDir)

	// Test: Count all
	t.Run("CountAll", func(t *testing.T) {
		count, err := searcher.Count(Query{})
		if err != nil {
			t.Fatalf("Count failed: %v", err)
		}
		if count != 3 {
			t.Errorf("Expected count 3, got %d", count)
		}
	})

	// Test: Count with filter
	t.Run("CountWithFilter", func(t *testing.T) {
		count, err := searcher.Count(Query{
			EventTypes: []EventType{EventTypeSend},
		})
		if err != nil {
			t.Fatalf("Count failed: %v", err)
		}
		if count != 2 {
			t.Errorf("Expected count 2, got %d", count)
		}
	})
}

func TestSearcher_StreamSearch(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	now := time.Now().UTC()
	entries := make([]AuditEntry, 5)
	for i := 0; i < 5; i++ {
		entries[i] = AuditEntry{
			Timestamp:   now.Add(time.Duration(i) * time.Minute),
			SessionID:   "test-session",
			EventType:   EventTypeCommand,
			Actor:       ActorUser,
			Target:      "target",
			SequenceNum: uint64(i + 1),
			Checksum:    string(rune('a' + i)),
		}
	}

	writeTestEntries(t, auditDir, "test-session", now.Format("2006-01-02"), entries)

	searcher := NewSearcherWithPath(auditDir)

	// Test: Stream all
	t.Run("StreamAll", func(t *testing.T) {
		ctx := context.Background()
		results, err := searcher.StreamSearch(ctx, Query{})
		if err != nil {
			t.Fatalf("StreamSearch failed: %v", err)
		}

		count := 0
		for result := range results {
			if result.Err != nil {
				t.Errorf("Stream error: %v", result.Err)
				break
			}
			count++
		}

		if count != 5 {
			t.Errorf("Expected 5 entries, got %d", count)
		}
	})

	// Test: Stream with limit
	t.Run("StreamWithLimit", func(t *testing.T) {
		ctx := context.Background()
		results, err := searcher.StreamSearch(ctx, Query{Limit: 2})
		if err != nil {
			t.Fatalf("StreamSearch failed: %v", err)
		}

		count := 0
		for result := range results {
			if result.Err != nil {
				t.Errorf("Stream error: %v", result.Err)
				break
			}
			count++
		}

		if count != 2 {
			t.Errorf("Expected 2 entries, got %d", count)
		}
	})

	// Test: Stream with cancellation
	t.Run("StreamWithCancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		results, err := searcher.StreamSearch(ctx, Query{})
		if err != nil {
			t.Fatalf("StreamSearch failed: %v", err)
		}

		count := 0
		for result := range results {
			if result.Err != nil {
				// Expected cancellation error
				break
			}
			count++
			if count == 2 {
				cancel()
			}
		}

		// Should have received at least 2 entries before cancellation
		if count < 2 {
			t.Errorf("Expected at least 2 entries before cancel, got %d", count)
		}
	})
}

func TestSearcher_BuildIndex(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	now := time.Now().UTC()
	entries := []AuditEntry{
		{
			Timestamp:   now,
			SessionID:   "session-a",
			EventType:   EventTypeCommand,
			Actor:       ActorUser,
			Target:      "t1",
			SequenceNum: 1,
			Checksum:    "a",
		},
		{
			Timestamp:   now,
			SessionID:   "session-b",
			EventType:   EventTypeSend,
			Actor:       ActorSystem,
			Target:      "t2",
			SequenceNum: 2,
			Checksum:    "b",
		},
		{
			Timestamp:   now,
			SessionID:   "session-a",
			EventType:   EventTypeError,
			Actor:       ActorAgent,
			Target:      "t3",
			SequenceNum: 3,
			Checksum:    "c",
		},
	}

	writeTestEntries(t, auditDir, "session-a", now.Format("2006-01-02"), entries[:1])
	writeTestEntries(t, auditDir, "session-b", now.Format("2006-01-02"), entries[1:2])
	writeTestEntries(t, auditDir, "session-a", now.Add(24*time.Hour).Format("2006-01-02"), entries[2:])

	searcher := NewSearcherWithPath(auditDir)

	// Build index
	ctx := context.Background()
	err := searcher.BuildIndex(ctx)
	if err != nil {
		t.Fatalf("BuildIndex failed: %v", err)
	}

	// Check index stats
	stats := searcher.IndexStats()

	if stats["indexed"] != true {
		t.Error("Expected indexed to be true")
	}
	if stats["entry_count"] != 3 {
		t.Errorf("Expected entry_count 3, got %v", stats["entry_count"])
	}
	if stats["session_count"] != 2 {
		t.Errorf("Expected session_count 2, got %v", stats["session_count"])
	}

	eventCounts := stats["event_type_counts"].(map[string]int)
	if eventCounts["command"] != 1 {
		t.Errorf("Expected 1 command event, got %d", eventCounts["command"])
	}
	if eventCounts["send"] != 1 {
		t.Errorf("Expected 1 send event, got %d", eventCounts["send"])
	}
	if eventCounts["error"] != 1 {
		t.Errorf("Expected 1 error event, got %d", eventCounts["error"])
	}
}

func TestSearcher_Timeout(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	// Create many entries
	now := time.Now().UTC()
	entries := make([]AuditEntry, 100)
	for i := 0; i < 100; i++ {
		entries[i] = AuditEntry{
			Timestamp:   now.Add(time.Duration(i) * time.Second),
			SessionID:   "test-session",
			EventType:   EventTypeCommand,
			Actor:       ActorUser,
			Target:      "target",
			SequenceNum: uint64(i + 1),
			Checksum:    "x",
		}
	}

	writeTestEntries(t, auditDir, "test-session", now.Format("2006-01-02"), entries)

	searcher := NewSearcherWithPath(auditDir)

	// Test with very short timeout
	t.Run("ShortTimeout", func(t *testing.T) {
		result, err := searcher.Search(Query{
			Timeout: 1 * time.Nanosecond,
		})

		// Should either return some results or context deadline exceeded
		if err != nil && err != context.DeadlineExceeded {
			t.Logf("Search with short timeout returned: %v", err)
		}

		if result != nil && result.Truncated {
			t.Log("Search was truncated due to timeout")
		}
	})
}

func TestGlobToRegex(t *testing.T) {
	tests := []struct {
		glob    string
		input   string
		matches bool
	}{
		{"*", "anything", true},
		{"*.go", "main.go", true},
		{"*.go", "main.txt", false},
		{"test_*", "test_foo", true},
		{"test_*", "testfoo", false},
		{"myproject__cc_?", "myproject__cc_1", true},
		{"myproject__cc_?", "myproject__cc_12", false},
		{"*__*_*", "proj__cc_1", true},
		{"foo.bar", "foo.bar", true},
		{"foo.bar", "fooXbar", false},
		{"[test]", "[test]", true}, // Literal brackets
	}

	for _, tt := range tests {
		t.Run(tt.glob+"_"+tt.input, func(t *testing.T) {
			pattern := globToRegex(tt.glob)
			matched, err := regexp.MatchString(pattern, tt.input)
			if err != nil {
				t.Fatalf("Regex error: %v", err)
			}
			if matched != tt.matches {
				t.Errorf("globToRegex(%q) matching %q: expected %v, got %v (pattern: %s)",
					tt.glob, tt.input, tt.matches, matched, pattern)
			}
		})
	}
}

func TestExtractDateFromFilename(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"test-session-2025-01-15.jsonl", "2025-01-15"},
		{"my-project-2024-12-31.jsonl", "2024-12-31"},
		{"session-2025-01-01.jsonl", "2025-01-01"},
		{"complex-name-with-dashes-2025-06-15.jsonl", "2025-06-15"},
		{"invalid.jsonl", ""},
		{"no-date.jsonl", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := extractDateFromFilename(tt.filename)
			if result != tt.expected {
				t.Errorf("extractDateFromFilename(%q): expected %q, got %q",
					tt.filename, tt.expected, result)
			}
		})
	}
}

func TestSearcher_EmptyDirectory(t *testing.T) {
	auditDir, cleanup := setupTestAuditDir(t)
	defer cleanup()

	searcher := NewSearcherWithPath(auditDir)

	// Search should return empty results, not error
	result, err := searcher.Search(Query{})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Errorf("Expected 0 entries, got %d", len(result.Entries))
	}

	// Count should return 0
	count, err := searcher.Count(Query{})
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected count 0, got %d", count)
	}
}

func TestSearcher_NonexistentDirectory(t *testing.T) {
	searcher := NewSearcherWithPath("/nonexistent/path/to/audit")

	// Should not error, just return empty results
	result, err := searcher.Search(Query{})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Errorf("Expected 0 entries, got %d", len(result.Entries))
	}
}
