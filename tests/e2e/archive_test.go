package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/archive"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestArchive_CreateAndRetrieve tests basic archive creation and retrieval.
func TestArchive_CreateAndRetrieve(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-ARCHIVE: Test archive creation and retrieval")

	// Create a mock archiver to test the JSONL file format
	sessionName := "test_archive_session"
	archiveDir := filepath.Join(tmpDir, "archive")

	logger.Log("Creating archive directory: %s", archiveDir)
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatalf("Failed to create archive directory: %v", err)
	}

	// Create archive records manually (simulating what the archiver would create)
	records := []archive.ArchiveRecord{
		{
			Session:   sessionName,
			Pane:      "cc_2",
			PaneIndex: 2,
			Agent:     "claude",
			Model:     "opus",
			Timestamp: time.Now().UTC(),
			Content:   "First capture: agent is thinking...\nLine 2 of output",
			Lines:     2,
			Sequence:  1,
		},
		{
			Session:   sessionName,
			Pane:      "cc_2",
			PaneIndex: 2,
			Agent:     "claude",
			Model:     "opus",
			Timestamp: time.Now().UTC().Add(30 * time.Second),
			Content:   "Second capture: implementing solution\nCode block here\nMore output",
			Lines:     3,
			Sequence:  2,
		},
		{
			Session:   sessionName,
			Pane:      "cod_3",
			PaneIndex: 3,
			Agent:     "codex",
			Model:     "o3",
			Timestamp: time.Now().UTC().Add(45 * time.Second),
			Content:   "Codex output: running tests\nAll tests passed",
			Lines:     2,
			Sequence:  1,
		},
	}

	// Write archive file
	archiveFile := filepath.Join(archiveDir, sessionName+"_"+time.Now().Format("2006-01-02")+".jsonl")
	logger.Log("Writing archive file: %s", archiveFile)

	f, err := os.Create(archiveFile)
	if err != nil {
		t.Fatalf("Failed to create archive file: %v", err)
	}

	encoder := json.NewEncoder(f)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			f.Close()
			t.Fatalf("Failed to write record: %v", err)
		}
		logger.Log("[E2E-ARCHIVE] Wrote record: pane=%s seq=%d lines=%d", record.Pane, record.Sequence, record.Lines)
	}
	f.Close()

	// Verify file was created
	info, err := os.Stat(archiveFile)
	if err != nil {
		t.Fatalf("Archive file not found: %v", err)
	}
	logger.Log("[E2E-ARCHIVE] Archive file size: %d bytes", info.Size())

	// Read and verify records
	data, err := os.ReadFile(archiveFile)
	if err != nil {
		t.Fatalf("Failed to read archive file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(records) {
		t.Errorf("Expected %d records, got %d", len(records), len(lines))
	}

	// Parse each record
	for i, line := range lines {
		var readRecord archive.ArchiveRecord
		if err := json.Unmarshal([]byte(line), &readRecord); err != nil {
			t.Errorf("Failed to parse record %d: %v", i, err)
			continue
		}

		if readRecord.Session != sessionName {
			t.Errorf("Record %d: expected session %q, got %q", i, sessionName, readRecord.Session)
		}
		logger.Log("[E2E-ARCHIVE] Verified record %d: pane=%s agent=%s", i+1, readRecord.Pane, readRecord.Agent)
	}

	logger.Log("PASS: Archive creation and retrieval verified")
}

// TestArchive_IncrementalCapture tests that archives capture content incrementally.
func TestArchive_IncrementalCapture(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-ARCHIVE: Test incremental capture")

	// Test the findNewContent function behavior via archive record sequences
	previousContent := "Line 1\nLine 2\nLine 3"
	currentContent := "Line 2\nLine 3\nLine 4\nLine 5"

	// Simulate what archive would extract as "new content"
	// The archiver should identify Line 4 and Line 5 as new
	prevLines := strings.Split(previousContent, "\n")
	currLines := strings.Split(currentContent, "\n")

	// Find overlap
	newStart := 0
	for i := 0; i < len(currLines); i++ {
		found := false
		for j := 0; j < len(prevLines); j++ {
			if currLines[i] == prevLines[j] {
				found = true
				break
			}
		}
		if !found {
			newStart = i
			break
		} else {
			newStart = i + 1
		}
	}

	newContent := strings.Join(currLines[newStart:], "\n")
	logger.Log("[E2E-ARCHIVE] Previous content: %d lines", len(prevLines))
	logger.Log("[E2E-ARCHIVE] Current content: %d lines", len(currLines))
	logger.Log("[E2E-ARCHIVE] New content starts at line %d", newStart)
	logger.Log("[E2E-ARCHIVE] New content: %q", newContent)

	if !strings.Contains(newContent, "Line 4") {
		t.Errorf("Expected new content to contain 'Line 4', got: %q", newContent)
	}
	if !strings.Contains(newContent, "Line 5") {
		t.Errorf("Expected new content to contain 'Line 5', got: %q", newContent)
	}

	logger.Log("PASS: Incremental capture logic verified")
}

// TestArchive_MultiPaneCapture tests archiving from multiple panes.
func TestArchive_MultiPaneCapture(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-ARCHIVE: Test multi-pane capture")

	sessionName := "multi_pane_session"
	archiveDir := filepath.Join(tmpDir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatalf("Failed to create archive directory: %v", err)
	}

	// Simulate multiple panes being archived
	panes := []struct {
		pane  string
		index int
		agent string
	}{
		{"cc_2", 2, "claude"},
		{"cod_3", 3, "codex"},
		{"gmi_4", 4, "gemini"},
	}

	archiveFile := filepath.Join(archiveDir, sessionName+".jsonl")
	f, err := os.Create(archiveFile)
	if err != nil {
		t.Fatalf("Failed to create archive file: %v", err)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)

	// Write records for each pane
	totalRecords := 0
	for _, pane := range panes {
		for seq := 1; seq <= 3; seq++ {
			record := archive.ArchiveRecord{
				Session:   sessionName,
				Pane:      pane.pane,
				PaneIndex: pane.index,
				Agent:     pane.agent,
				Timestamp: time.Now().UTC().Add(time.Duration(seq) * 30 * time.Second),
				Content:   pane.agent + " output sequence " + string(rune('0'+seq)),
				Lines:     1,
				Sequence:  seq,
			}
			if err := encoder.Encode(record); err != nil {
				t.Fatalf("Failed to write record: %v", err)
			}
			totalRecords++
			logger.Log("[E2E-ARCHIVE] Captured: pane=%s seq=%d", pane.pane, seq)
		}
	}

	logger.Log("[E2E-ARCHIVE] Total records written: %d", totalRecords)

	// Verify we can read back all records
	f.Close()
	data, err := os.ReadFile(archiveFile)
	if err != nil {
		t.Fatalf("Failed to read archive: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != totalRecords {
		t.Errorf("Expected %d records, got %d", totalRecords, len(lines))
	}

	// Verify each pane has records
	paneCounts := make(map[string]int)
	for _, line := range lines {
		var record archive.ArchiveRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		paneCounts[record.Pane]++
	}

	for _, pane := range panes {
		if paneCounts[pane.pane] != 3 {
			t.Errorf("Expected 3 records for pane %s, got %d", pane.pane, paneCounts[pane.pane])
		}
		logger.Log("[E2E-ARCHIVE] Pane %s: %d records", pane.pane, paneCounts[pane.pane])
	}

	logger.Log("PASS: Multi-pane capture verified")
}

// TestArchive_CASSIntegration tests that archive format is CASS-compatible.
func TestArchive_CASSIntegration(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-ARCHIVE: Test CASS integration format")

	// Create a sample archive record and verify it has all CASS-required fields
	record := archive.ArchiveRecord{
		Session:   "cass_test_session",
		Pane:      "cc_2",
		PaneIndex: 2,
		Agent:     "claude",
		Model:     "opus",
		Timestamp: time.Now().UTC(),
		Content:   "Sample agent output for CASS indexing\nMultiple lines\nWith code:\n```go\nfunc main() {}\n```",
		Lines:     5,
		Sequence:  1,
	}

	// Serialize to JSON
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Failed to marshal record: %v", err)
	}

	logger.Log("[E2E-ARCHIVE] CASS record size: %d bytes", len(data))

	// Parse back and verify fields
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to parse CASS record: %v", err)
	}

	// Required CASS fields
	requiredFields := []string{"session", "pane", "pane_index", "agent", "timestamp", "content", "lines", "sequence"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("Missing required CASS field: %s", field)
		} else {
			logger.Log("[E2E-ARCHIVE] CASS field present: %s", field)
		}
	}

	// Verify content is searchable text
	content, ok := parsed["content"].(string)
	if !ok {
		t.Error("Content field is not a string")
	} else {
		if !strings.Contains(content, "agent output") {
			t.Error("Content not searchable")
		}
		logger.Log("[E2E-ARCHIVE] Content searchable: yes (%d chars)", len(content))
	}

	logger.Log("PASS: CASS integration format verified")
}

// TestArchive_ListArchives tests listing available archives.
func TestArchive_ListArchives(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-ARCHIVE: Test listing archives")

	archiveDir := filepath.Join(tmpDir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatalf("Failed to create archive directory: %v", err)
	}

	// Create multiple archive files
	sessions := []string{"session_alpha", "session_beta", "session_gamma"}
	for _, session := range sessions {
		filename := filepath.Join(archiveDir, session+"_"+time.Now().Format("2006-01-02")+".jsonl")
		if err := os.WriteFile(filename, []byte(`{"session":"`+session+`"}`+"\n"), 0644); err != nil {
			t.Fatalf("Failed to create archive file: %v", err)
		}
		logger.Log("[E2E-ARCHIVE] Created archive: %s", filepath.Base(filename))
	}

	// List archives
	files, err := filepath.Glob(filepath.Join(archiveDir, "*.jsonl"))
	if err != nil {
		t.Fatalf("Failed to list archives: %v", err)
	}

	if len(files) != len(sessions) {
		t.Errorf("Expected %d archives, found %d", len(sessions), len(files))
	}

	for _, file := range files {
		logger.Log("[E2E-ARCHIVE] Found archive: %s", filepath.Base(file))
	}

	logger.Log("PASS: Archive listing verified (%d archives)", len(files))
}

// TestArchive_ContextCancellation tests that archive gracefully stops on context cancellation.
func TestArchive_ContextCancellation(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-ARCHIVE: Test context cancellation")

	archiveDir := filepath.Join(tmpDir, "archive")

	opts := archive.ArchiverOptions{
		SessionName:     "cancel_test_session",
		OutputDir:       archiveDir,
		Interval:        50 * time.Millisecond,
		LinesPerCapture: 50,
	}

	archiver, err := archive.NewArchiver(opts)
	if err != nil {
		t.Fatalf("Failed to create archiver: %v", err)
	}

	// Create a context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Run archiver in background
	done := make(chan error, 1)
	go func() {
		done <- archiver.Run(ctx)
	}()

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)
	logger.Log("[E2E-ARCHIVE] Archiver running, cancelling context")

	// Cancel the context
	cancel()

	// Wait for archiver to stop
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Expected context.Canceled, got: %v", err)
		}
		logger.Log("[E2E-ARCHIVE] Archiver stopped gracefully")
	case <-time.After(2 * time.Second):
		t.Fatal("Archiver did not stop within timeout")
	}

	// Close should succeed
	if err := archiver.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}

	logger.Log("PASS: Context cancellation verified")
}
