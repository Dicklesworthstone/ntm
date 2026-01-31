package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewArchiver(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("requires session name", func(t *testing.T) {
		_, err := NewArchiver(ArchiverOptions{
			OutputDir: tmpDir,
		})
		if err == nil {
			t.Error("expected error for empty session name")
		}
	})

	t.Run("creates archiver with defaults", func(t *testing.T) {
		a, err := NewArchiver(ArchiverOptions{
			SessionName: "test-session",
			OutputDir:   tmpDir,
		})
		if err != nil {
			t.Fatalf("NewArchiver() error: %v", err)
		}
		defer a.Close()

		if a.sessionName != "test-session" {
			t.Errorf("sessionName = %q, want %q", a.sessionName, "test-session")
		}
		if a.interval != DefaultInterval {
			t.Errorf("interval = %v, want %v", a.interval, DefaultInterval)
		}
		if a.linesPerCapture != DefaultLinesPerCapture {
			t.Errorf("linesPerCapture = %d, want %d", a.linesPerCapture, DefaultLinesPerCapture)
		}
	})

	t.Run("creates output directory", func(t *testing.T) {
		subDir := filepath.Join(tmpDir, "nested", "archive")
		a, err := NewArchiver(ArchiverOptions{
			SessionName: "test",
			OutputDir:   subDir,
		})
		if err != nil {
			t.Fatalf("NewArchiver() error: %v", err)
		}
		defer a.Close()

		if _, err := os.Stat(subDir); os.IsNotExist(err) {
			t.Error("output directory not created")
		}
	})

	t.Run("creates archive file", func(t *testing.T) {
		a, err := NewArchiver(ArchiverOptions{
			SessionName: "myproject",
			OutputDir:   tmpDir,
		})
		if err != nil {
			t.Fatalf("NewArchiver() error: %v", err)
		}
		defer a.Close()

		// Check file exists
		pattern := filepath.Join(tmpDir, "myproject_*.jsonl")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("Glob() error: %v", err)
		}
		if len(matches) == 0 {
			t.Error("archive file not created")
		}
	})
}

func TestArchiver_WriteRecord(t *testing.T) {
	tmpDir := t.TempDir()

	var records []*ArchiveRecord
	a, err := NewArchiver(ArchiverOptions{
		SessionName: "test-session",
		OutputDir:   tmpDir,
		OnRecord: func(r *ArchiveRecord) {
			records = append(records, r)
		},
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Write a record
	record := &ArchiveRecord{
		Session:   "test-session",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Model:     "opus-4.5",
		Timestamp: time.Now().UTC(),
		Content:   "Hello, world!",
		Lines:     1,
		Sequence:  1,
	}

	if err := a.writeRecord(record); err != nil {
		t.Fatalf("writeRecord() error: %v", err)
	}

	// Verify callback was called
	if len(records) != 1 {
		t.Errorf("callback received %d records, want 1", len(records))
	}

	// Verify stats
	stats := a.Stats()
	if stats.TotalRecords != 1 {
		t.Errorf("TotalRecords = %d, want 1", stats.TotalRecords)
	}

	// Flush and read back
	a.flush()

	// Find the archive file
	pattern := filepath.Join(tmpDir, "test-session_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("archive file not found")
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading archive: %v", err)
	}

	var readRecord ArchiveRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("unmarshaling record: %v", err)
	}

	if readRecord.Session != "test-session" {
		t.Errorf("Session = %q, want %q", readRecord.Session, "test-session")
	}
	if readRecord.Agent != "cc" {
		t.Errorf("Agent = %q, want %q", readRecord.Agent, "cc")
	}
	if readRecord.Content != "Hello, world!" {
		t.Errorf("Content = %q, want %q", readRecord.Content, "Hello, world!")
	}
}

func TestArchiver_Stats(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "stats-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	stats := a.Stats()

	if stats.Session != "stats-test" {
		t.Errorf("Session = %q, want %q", stats.Session, "stats-test")
	}
	if stats.TotalRecords != 0 {
		t.Errorf("TotalRecords = %d, want 0", stats.TotalRecords)
	}
	if stats.Duration < 0 {
		t.Errorf("Duration = %v, should be >= 0", stats.Duration)
	}
}

func TestArchiver_RunContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "cancel-test",
		OutputDir:   tmpDir,
		Interval:    100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Run should exit when context is cancelled
	start := time.Now()
	err = a.Run(ctx)
	elapsed := time.Since(start)

	if err != context.DeadlineExceeded {
		t.Errorf("Run() error = %v, want context.DeadlineExceeded", err)
	}

	// Should have exited quickly
	if elapsed > 200*time.Millisecond {
		t.Errorf("Run() took %v, expected < 200ms", elapsed)
	}
}

func TestFindNewContent(t *testing.T) {
	tests := []struct {
		name     string
		previous string
		current  string
		want     string
	}{
		{
			name:     "empty previous",
			previous: "",
			current:  "line1\nline2",
			want:     "line1\nline2",
		},
		{
			name:     "empty current",
			previous: "line1",
			current:  "",
			want:     "",
		},
		{
			name:     "no overlap",
			previous: "old1\nold2",
			current:  "new1\nnew2",
			want:     "new1\nnew2",
		},
		{
			name:     "partial overlap at end",
			previous: "line1\nline2\nline3",
			current:  "line3\nline4\nline5",
			want:     "line4\nline5",
		},
		{
			name:     "complete overlap",
			previous: "line1\nline2",
			current:  "line1\nline2",
			want:     "",
		},
		{
			name:     "scrolling buffer",
			previous: "line1\nline2\nline3\nline4\nline5",
			current:  "line4\nline5\nline6\nline7",
			want:     "line6\nline7",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findNewContent(tc.previous, tc.current)
			if got != tc.want {
				t.Errorf("findNewContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSimpleHash(t *testing.T) {
	// Same input should give same hash
	h1 := simpleHash("hello world")
	h2 := simpleHash("hello world")
	if h1 != h2 {
		t.Errorf("same input gave different hashes: %d != %d", h1, h2)
	}

	// Different input should give different hash
	h3 := simpleHash("hello world!")
	if h1 == h3 {
		t.Errorf("different input gave same hash: %d", h1)
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"single", 1},
		{"two\nlines", 2},
		{"three\nlines\nhere", 3},
		{"trailing\n", 2},
	}

	for _, tc := range tests {
		got := countLines(tc.input)
		if got != tc.want {
			t.Errorf("countLines(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestSplitLines(t *testing.T) {
	lines := splitLines("one\ntwo\nthree")
	if len(lines) != 3 {
		t.Errorf("len(lines) = %d, want 3", len(lines))
	}
	if lines[0] != "one" || lines[1] != "two" || lines[2] != "three" {
		t.Errorf("lines = %v, want [one two three]", lines)
	}
}

func TestStartsWithLines(t *testing.T) {
	lines := []string{"a", "b", "c", "d"}

	if !startsWithLines(lines, []string{"a", "b"}) {
		t.Error("should start with [a, b]")
	}
	if startsWithLines(lines, []string{"b", "c"}) {
		t.Error("should not start with [b, c]")
	}
	if startsWithLines(lines, []string{"a", "b", "c", "d", "e"}) {
		t.Error("prefix longer than lines should return false")
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		input string
		want  string
	}{
		{"~/test", filepath.Join(home, "test")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tc := range tests {
		got := expandPath(tc.input)
		if got != tc.want {
			t.Errorf("expandPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestArchiveRecord_JSONFormat(t *testing.T) {
	record := ArchiveRecord{
		Session:   "myproject",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Model:     "opus-4.5",
		Timestamp: time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC),
		Content:   "Test content\nwith newlines",
		Lines:     2,
		Sequence:  5,
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	// Verify required CASS fields are present
	jsonStr := string(data)
	requiredFields := []string{"session", "pane", "agent", "timestamp", "content"}
	for _, field := range requiredFields {
		if !strings.Contains(jsonStr, `"`+field+`"`) {
			t.Errorf("JSON missing required field: %s", field)
		}
	}

	// Verify round-trip
	var decoded ArchiveRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	if decoded.Session != record.Session {
		t.Errorf("Session = %q, want %q", decoded.Session, record.Session)
	}
	if decoded.Agent != record.Agent {
		t.Errorf("Agent = %q, want %q", decoded.Agent, record.Agent)
	}
	if decoded.Content != record.Content {
		t.Errorf("Content = %q, want %q", decoded.Content, record.Content)
	}
}

func TestDefaultArchiverOptions(t *testing.T) {
	opts := DefaultArchiverOptions("test")

	if opts.SessionName != "test" {
		t.Errorf("SessionName = %q, want %q", opts.SessionName, "test")
	}
	if opts.Interval != DefaultInterval {
		t.Errorf("Interval = %v, want %v", opts.Interval, DefaultInterval)
	}
	if opts.LinesPerCapture != DefaultLinesPerCapture {
		t.Errorf("LinesPerCapture = %d, want %d", opts.LinesPerCapture, DefaultLinesPerCapture)
	}
}

func TestPaneState(t *testing.T) {
	state := &PaneState{
		LastHash:    12345,
		LastCapture: time.Now(),
		Sequence:    3,
		TotalLines:  100,
		LastContent: "old content",
	}

	// Verify state fields are accessible
	if state.Sequence != 3 {
		t.Errorf("Sequence = %d, want 3", state.Sequence)
	}
	if state.TotalLines != 100 {
		t.Errorf("TotalLines = %d, want 100", state.TotalLines)
	}
}

// ============================================================================
// Additional tests for bd-s2l4: Output archive capture and storage
// ============================================================================

func TestArchiver_ANSIEscapeContent(t *testing.T) {
	tmpDir := t.TempDir()

	var records []*ArchiveRecord
	a, err := NewArchiver(ArchiverOptions{
		SessionName: "ansi-test",
		OutputDir:   tmpDir,
		OnRecord: func(r *ArchiveRecord) {
			records = append(records, r)
		},
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Content with ANSI escape sequences (colors, formatting)
	ansiContent := "\033[32mGreen text\033[0m\n\033[1;31mBold red\033[0m\n\033[4mUnderline\033[0m"
	record := &ArchiveRecord{
		Session:   "ansi-test",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Timestamp: time.Now().UTC(),
		Content:   ansiContent,
		Lines:     3,
		Sequence:  1,
	}

	if err := a.writeRecord(record); err != nil {
		t.Fatalf("ARCHIVE_TEST: ANSI content | writeRecord error: %v", err)
	}

	a.flush()

	// Read back and verify ANSI sequences are preserved
	pattern := filepath.Join(tmpDir, "ansi-test_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("ARCHIVE_TEST: ANSI content | archive file not found")
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: ANSI content | reading archive: %v", err)
	}

	var readRecord ArchiveRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("ARCHIVE_TEST: ANSI content | unmarshaling: %v", err)
	}

	if readRecord.Content != ansiContent {
		t.Errorf("ARCHIVE_TEST: ANSI content | Content mismatch, ANSI sequences not preserved")
	}
	t.Logf("ARCHIVE_TEST: ANSI content | Size=%d | Content preserved correctly", len(readRecord.Content))
}

func TestArchiver_LargeScrollbackContent(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "large-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Generate large content (10000 lines)
	var builder strings.Builder
	lineCount := 10000
	for i := 0; i < lineCount; i++ {
		builder.WriteString("Line ")
		builder.WriteString(string(rune('0' + (i % 10))))
		builder.WriteString(": This is line number ")
		builder.WriteString(strings.Repeat("x", 80)) // ~80 chars per line
		builder.WriteString("\n")
	}
	largeContent := builder.String()
	originalSize := len(largeContent)

	record := &ArchiveRecord{
		Session:   "large-test",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Timestamp: time.Now().UTC(),
		Content:   largeContent,
		Lines:     lineCount,
		Sequence:  1,
	}

	if err := a.writeRecord(record); err != nil {
		t.Fatalf("ARCHIVE_TEST: Large scrollback | writeRecord error: %v", err)
	}

	a.flush()

	// Read back and verify
	pattern := filepath.Join(tmpDir, "large-test_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("ARCHIVE_TEST: Large scrollback | archive file not found")
	}

	info, err := os.Stat(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: Large scrollback | stat error: %v", err)
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: Large scrollback | reading archive: %v", err)
	}

	var readRecord ArchiveRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("ARCHIVE_TEST: Large scrollback | unmarshaling: %v", err)
	}

	if readRecord.Content != largeContent {
		t.Error("ARCHIVE_TEST: Large scrollback | content mismatch")
	}

	t.Logf("ARCHIVE_TEST: Large scrollback | Size=%d | Lines=%d | FileSize=%d | Path=%s",
		originalSize, lineCount, info.Size(), matches[0])
}

func TestArchiver_UnicodeContent(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "unicode-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Content with various Unicode characters
	unicodeContent := "Hello 世界\n" +
		"日本語テスト\n" +
		"Emoji: 🚀 💻 🎉 ✅ ❌\n" +
		"Mathematical: ∑ ∫ π √ ≠\n" +
		"Currency: € £ ¥ ₹ ₿\n" +
		"Arrows: → ← ↑ ↓ ⇒\n" +
		"Greek: α β γ δ ε"

	record := &ArchiveRecord{
		Session:   "unicode-test",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "cc",
		Timestamp: time.Now().UTC(),
		Content:   unicodeContent,
		Lines:     7,
		Sequence:  1,
	}

	if err := a.writeRecord(record); err != nil {
		t.Fatalf("ARCHIVE_TEST: Unicode content | writeRecord error: %v", err)
	}

	a.flush()

	// Read back and verify
	pattern := filepath.Join(tmpDir, "unicode-test_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("ARCHIVE_TEST: Unicode content | archive file not found")
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: Unicode content | reading archive: %v", err)
	}

	var readRecord ArchiveRecord
	if err := json.Unmarshal(data, &readRecord); err != nil {
		t.Fatalf("ARCHIVE_TEST: Unicode content | unmarshaling: %v", err)
	}

	if readRecord.Content != unicodeContent {
		t.Errorf("ARCHIVE_TEST: Unicode content | Content mismatch\nGot: %q\nWant: %q",
			readRecord.Content, unicodeContent)
	}
	t.Logf("ARCHIVE_TEST: Unicode content | Size=%d | Runes=%d", len(unicodeContent), len([]rune(unicodeContent)))
}

func TestArchiver_MultipleRecordsAppend(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "append-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Write multiple records
	numRecords := 5
	for i := 1; i <= numRecords; i++ {
		record := &ArchiveRecord{
			Session:   "append-test",
			Pane:      "cc_2",
			PaneIndex: 2,
			Agent:     "cc",
			Timestamp: time.Now().UTC(),
			Content:   strings.Repeat("content ", i*10),
			Lines:     1,
			Sequence:  i,
		}
		if err := a.writeRecord(record); err != nil {
			t.Fatalf("ARCHIVE_TEST: Append record %d | writeRecord error: %v", i, err)
		}
	}

	a.flush()

	// Verify stats
	stats := a.Stats()
	if stats.TotalRecords != numRecords {
		t.Errorf("ARCHIVE_TEST: Append | TotalRecords = %d, want %d", stats.TotalRecords, numRecords)
	}

	// Read back and count records (JSONL format = one record per line)
	pattern := filepath.Join(tmpDir, "append-test_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("ARCHIVE_TEST: Append | archive file not found")
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ARCHIVE_TEST: Append | reading archive: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != numRecords {
		t.Errorf("ARCHIVE_TEST: Append | file has %d lines, want %d", len(lines), numRecords)
	}

	// Verify each line is valid JSON
	for i, line := range lines {
		var record ArchiveRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("ARCHIVE_TEST: Append | line %d invalid JSON: %v", i, err)
		}
		if record.Sequence != i+1 {
			t.Errorf("ARCHIVE_TEST: Append | line %d Sequence = %d, want %d", i, record.Sequence, i+1)
		}
	}

	t.Logf("ARCHIVE_TEST: Append | Records=%d | Lines=%d | FileSize=%d",
		numRecords, len(lines), len(data))
}

func TestFindNewContent_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		previous string
		current  string
		want     string
	}{
		{
			name:     "single line overlap",
			previous: "a",
			current:  "a\nb",
			want:     "b",
		},
		{
			name:     "multi-line exact overlap",
			previous: "a\nb\nc",
			current:  "a\nb\nc",
			want:     "",
		},
		{
			name:     "complete content replacement",
			previous: "old content here",
			current:  "completely new content",
			want:     "completely new content",
		},
		{
			name:     "whitespace handling",
			previous: "line1\n   \nline2",
			current:  "line2\nline3\n   ",
			want:     "line3\n   ",
		},
		{
			name:     "long overlap with small new content",
			previous: "a\nb\nc\nd\ne\nf\ng\nh\ni\nj",
			current:  "i\nj\nk",
			want:     "k",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findNewContent(tc.previous, tc.current)
			if got != tc.want {
				t.Errorf("findNewContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSimpleHash_Collisions(t *testing.T) {
	// Test that slightly different inputs produce different hashes
	inputs := []string{
		"hello world",
		"hello world!",
		"Hello world",
		" hello world",
		"hello world ",
		"helloworld",
	}

	hashes := make(map[uint64]string)
	for _, input := range inputs {
		h := simpleHash(input)
		if prev, exists := hashes[h]; exists {
			t.Errorf("Hash collision: %q and %q both hash to %d", prev, input, h)
		}
		hashes[h] = input
	}
}

func TestCountLines_EdgeCases(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"\n", 2},        // Empty line + trailing
		{"\n\n", 3},      // Two empty lines + trailing
		{"a\n\nb", 3},    // Line with empty middle
		{"a\nb\nc\n", 4}, // Three lines with trailing newline
		{"\na\nb\n", 4},  // Leading newline
		{"   ", 1},       // Whitespace only, no newline
		{"   \n   ", 2},  // Whitespace with newline
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := countLines(tc.input)
			if got != tc.want {
				t.Errorf("countLines(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestArchiver_FileNamingConvention(t *testing.T) {
	tmpDir := t.TempDir()

	sessionName := "my-project-2026"
	a, err := NewArchiver(ArchiverOptions{
		SessionName: sessionName,
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Check file naming follows expected pattern: {session}_{date}.jsonl
	pattern := filepath.Join(tmpDir, sessionName+"_*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("Glob() error: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("Expected 1 archive file, found %d", len(matches))
	}

	// Verify date format in filename (YYYY-MM-DD)
	filename := filepath.Base(matches[0])
	expectedPrefix := sessionName + "_"
	if !strings.HasPrefix(filename, expectedPrefix) {
		t.Errorf("Filename %q should start with %q", filename, expectedPrefix)
	}

	datePart := strings.TrimSuffix(strings.TrimPrefix(filename, expectedPrefix), ".jsonl")
	if len(datePart) != 10 { // YYYY-MM-DD
		t.Errorf("Date part %q should be 10 characters (YYYY-MM-DD)", datePart)
	}

	t.Logf("ARCHIVE_TEST: File naming | Session=%s | Filename=%s | DatePart=%s",
		sessionName, filename, datePart)
}

func TestArchiver_CloseIdempotent(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "close-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}

	// Close multiple times should not panic or error
	if err := a.Close(); err != nil {
		t.Errorf("First Close() error: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Second Close() error: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Third Close() error: %v", err)
	}
}

// =============================================================================
// Additional Pure Function Tests for Coverage Improvement
// =============================================================================

func TestMin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 1},
		{2, 1, 1},
		{0, 0, 0},
		{-1, 1, -1},
		{100, 50, 50},
		{-100, -50, -100},
	}

	for _, tc := range tests {
		got := min(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("min(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestFindOverlap_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		prev []string
		curr []string
		want int
	}{
		{
			name: "empty prev",
			prev: []string{},
			curr: []string{"a", "b"},
			want: 0,
		},
		{
			name: "empty curr",
			prev: []string{"a", "b"},
			curr: []string{},
			want: 0,
		},
		{
			name: "no overlap at all",
			prev: []string{"x", "y", "z"},
			curr: []string{"a", "b", "c"},
			want: 0,
		},
		{
			name: "last line of prev in middle of curr",
			prev: []string{"a", "b", "c"},
			curr: []string{"x", "c", "d"},
			want: 2,
		},
		{
			name: "single line prev matches first of curr",
			prev: []string{"x"},
			curr: []string{"x", "y", "z"},
			want: 1,
		},
		{
			name: "complete match",
			prev: []string{"a", "b"},
			curr: []string{"a", "b"},
			want: 2,
		},
		{
			name: "suffix of prev at start of curr",
			prev: []string{"1", "2", "3", "4", "5"},
			curr: []string{"4", "5", "6", "7"},
			want: 2,
		},
		{
			name: "longer than maxCheck lines",
			prev: make([]string, 100), // 100 lines
			curr: make([]string, 10),
			want: 0, // No overlap since slices are empty strings
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findOverlap(tc.prev, tc.curr)
			if got != tc.want {
				t.Errorf("findOverlap() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSplitLines_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  int // Number of lines expected
	}{
		{"empty", "", 0},
		{"single line no newline", "hello", 1},
		{"single line with newline", "hello\n", 1},
		{"two lines", "a\nb", 2},
		{"empty lines", "\n\n\n", 3},
		{"mixed empty", "a\n\nb", 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitLines(tc.input)
			if len(got) != tc.want {
				t.Errorf("splitLines(%q) = %d lines, want %d", tc.input, len(got), tc.want)
			}
		})
	}
}

func TestFlush_NilFile(t *testing.T) {
	a := &Archiver{
		file: nil, // No file set
	}

	// flush with nil file should not panic and return nil
	err := a.flush()
	if err != nil {
		t.Errorf("flush() with nil file should return nil, got %v", err)
	}
}

func TestStats_WithPaneStates(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := NewArchiver(ArchiverOptions{
		SessionName: "stats-panes-test",
		OutputDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("NewArchiver() error: %v", err)
	}
	defer a.Close()

	// Manually add pane states
	a.mu.Lock()
	a.paneStates[2] = &PaneState{TotalLines: 100}
	a.paneStates[3] = &PaneState{TotalLines: 200}
	a.paneStates[4] = &PaneState{TotalLines: 50}
	a.mu.Unlock()

	stats := a.Stats()

	if stats.PanesTracked != 3 {
		t.Errorf("PanesTracked = %d, want 3", stats.PanesTracked)
	}
	if stats.TotalLines != 350 {
		t.Errorf("TotalLines = %d, want 350", stats.TotalLines)
	}
}

func TestStartsWithLines_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		lines  []string
		prefix []string
		want   bool
	}{
		{
			name:   "empty prefix",
			lines:  []string{"a", "b"},
			prefix: []string{},
			want:   true,
		},
		{
			name:   "empty lines",
			lines:  []string{},
			prefix: []string{"a"},
			want:   false,
		},
		{
			name:   "both empty",
			lines:  []string{},
			prefix: []string{},
			want:   true,
		},
		{
			name:   "single element match",
			lines:  []string{"x"},
			prefix: []string{"x"},
			want:   true,
		},
		{
			name:   "single element no match",
			lines:  []string{"x"},
			prefix: []string{"y"},
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := startsWithLines(tc.lines, tc.prefix)
			if got != tc.want {
				t.Errorf("startsWithLines() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExpandPath_NoHome(t *testing.T) {
	// Test with path that doesn't start with ~
	result := expandPath("/absolute/path")
	if result != "/absolute/path" {
		t.Errorf("expandPath(/absolute/path) = %q, want /absolute/path", result)
	}

	result = expandPath("relative/path")
	if result != "relative/path" {
		t.Errorf("expandPath(relative/path) = %q, want relative/path", result)
	}

	result = expandPath("")
	if result != "" {
		t.Errorf("expandPath('') = %q, want ''", result)
	}
}
