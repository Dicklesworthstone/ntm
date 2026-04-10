package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
)

// --- Pure function tests (safe to run in parallel) ---

func TestParseTimeArg_RFC3339(t *testing.T) {

	input := "2026-01-15T10:30:00Z"
	got, err := parseTimeArg(input)
	if err != nil {
		t.Fatalf("parseTimeArg(%q) error: %v", input, err)
	}
	want, _ := time.Parse(time.RFC3339, input)
	if !got.Equal(want) {
		t.Errorf("parseTimeArg(%q) = %v, want %v", input, got, want)
	}
}

func TestParseTimeArg_RelativeMinutes(t *testing.T) {

	before := time.Now()
	got, err := parseTimeArg("30m")
	after := time.Now()
	if err != nil {
		t.Fatalf("parseTimeArg(30m) error: %v", err)
	}

	expectedLow := before.Add(-30 * time.Minute)
	expectedHigh := after.Add(-30 * time.Minute)
	if got.Before(expectedLow) || got.After(expectedHigh) {
		t.Errorf("parseTimeArg(30m) = %v, expected between %v and %v", got, expectedLow, expectedHigh)
	}
}

func TestParseTimeArg_RelativeHours(t *testing.T) {

	before := time.Now()
	got, err := parseTimeArg("2h")
	if err != nil {
		t.Fatalf("parseTimeArg(2h) error: %v", err)
	}

	expected := before.Add(-2 * time.Hour)
	diff := got.Sub(expected).Abs()
	if diff > time.Second {
		t.Errorf("parseTimeArg(2h) off by %v", diff)
	}
}

func TestParseTimeArg_RelativeDays(t *testing.T) {

	before := time.Now()
	got, err := parseTimeArg("7d")
	if err != nil {
		t.Fatalf("parseTimeArg(7d) error: %v", err)
	}

	expected := before.AddDate(0, 0, -7)
	diff := got.Sub(expected).Abs()
	if diff > time.Second {
		t.Errorf("parseTimeArg(7d) off by %v", diff)
	}
}

func TestParseTimeArg_InvalidInput(t *testing.T) {

	tests := []struct {
		name  string
		input string
	}{
		{"too short", "x"},
		{"invalid unit", "5x"},
		{"not a number", "abch"},
		{"empty string", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTimeArg(tc.input)
			if err == nil {
				t.Errorf("parseTimeArg(%q) should error", tc.input)
			}
		})
	}
}

// --- Command structure tests (safe to run in parallel) ---

func TestNewAuditCmd_SubcommandRegistration(t *testing.T) {

	cmd := newAuditCmd()
	if cmd.Use != "audit" {
		t.Errorf("Use = %q, want %q", cmd.Use, "audit")
	}

	subcommands := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	expected := []string{"show", "search", "verify", "export", "list"}
	for _, name := range expected {
		if !subcommands[name] {
			t.Errorf("missing subcommand %q", name)
		}
	}
}

func TestNewAuditShowCmd_Flags(t *testing.T) {

	cmd := newAuditShowCmd()
	if cmd.Use != "show <session>" {
		t.Errorf("Use = %q, want %q", cmd.Use, "show <session>")
	}

	flags := []string{"since", "until", "type", "limit"}
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag --%s", name)
		}
	}
}

func TestNewAuditSearchCmd_Flags(t *testing.T) {

	cmd := newAuditSearchCmd()
	flags := []string{"sessions", "type", "actor", "target", "days", "limit"}
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag --%s", name)
		}
	}
}

func TestNewAuditExportCmd_Flags(t *testing.T) {

	cmd := newAuditExportCmd()
	flags := []string{"format", "output"}
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag --%s", name)
		}
	}
}

// --- Integration tests that override newAuditSearcherFunc (NOT parallel) ---

func withTestSearcher(t *testing.T, dir string) {
	t.Helper()
	origFunc := newAuditSearcherFunc
	newAuditSearcherFunc = func() (*audit.Searcher, error) {
		return audit.NewSearcherWithPath(dir), nil
	}
	t.Cleanup(func() { newAuditSearcherFunc = origFunc })
}

func TestRunAuditVerify_NoLogs(t *testing.T) {
	tmpDir := t.TempDir()
	withTestSearcher(t, tmpDir)

	err := runAuditVerify("nonexistent_session")
	if err == nil {
		t.Error("expected error for missing logs")
	}
	if !strings.Contains(err.Error(), "no audit logs found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunAuditVerify_ValidLog(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "test_session", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditVerify("test_session")
	if err != nil {
		t.Errorf("verify should pass for valid log: %v", err)
	}
}

func TestRunAuditList_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	withTestSearcher(t, tmpDir)

	err := runAuditList()
	if err != nil {
		t.Errorf("list should not error on empty dir: %v", err)
	}
}

func TestRunAuditList_WithLogs(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "sess_a", 3)
	writeTestAuditLog(t, tmpDir, "sess_b", 2)
	withTestSearcher(t, tmpDir)

	err := runAuditList()
	if err != nil {
		t.Errorf("list should not error: %v", err)
	}
}

func TestRunAuditExport_JSON(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "export_test", 3)
	withTestSearcher(t, tmpDir)

	outFile := filepath.Join(tmpDir, "export.json")
	err := runAuditExport("export_test", "json", outFile)
	if err != nil {
		t.Fatalf("export json failed: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}

	var entries []audit.AuditEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

func TestRunAuditExport_CSV(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "csv_test", 2)
	withTestSearcher(t, tmpDir)

	outFile := filepath.Join(tmpDir, "export.csv")
	err := runAuditExport("csv_test", "csv", outFile)
	if err != nil {
		t.Fatalf("export csv failed: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 { // header + 2 entries
		t.Errorf("expected 3 lines (header + 2), got %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], "timestamp,") {
		t.Errorf("expected CSV header, got %q", lines[0])
	}
}

func TestRunAuditExport_InvalidFormat(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "invalid_fmt", 1)
	withTestSearcher(t, tmpDir)

	err := runAuditExport("invalid_fmt", "xml", "")
	if err == nil {
		t.Error("expected error for unsupported format")
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunAuditShow_WithEntries(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "show_test", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditShow("show_test", "", "", "", 10)
	if err != nil {
		t.Errorf("show should not error: %v", err)
	}
}

func TestRunAuditShow_WithTimeFilter(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "time_test", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditShow("time_test", "1h", "", "", 10)
	if err != nil {
		t.Errorf("show with --since should not error: %v", err)
	}
}

func TestRunAuditShow_WithTypeFilter(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "filter_test", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditShow("filter_test", "", "", "command", 10)
	if err != nil {
		t.Errorf("show with type filter should not error: %v", err)
	}
}

func TestRunAuditSearch_Pattern(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "search_test", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditSearch("target", "", "", "", "", 30, 50)
	if err != nil {
		t.Errorf("search should not error: %v", err)
	}
}

func TestRunAuditSearch_NoResults(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "noresult_test", 3)
	withTestSearcher(t, tmpDir)

	err := runAuditSearch("xyznonexistent999", "", "", "", "", 30, 50)
	if err != nil {
		t.Errorf("search with no results should not error: %v", err)
	}
}

func TestRunAuditSearch_WithFilters(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "filtered_search", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditSearch("target", "filtered_search", "command", "user", "target_*", 30, 50)
	if err != nil {
		t.Errorf("search with filters should not error: %v", err)
	}
}

// --- Test helpers ---

func writeTestAuditLog(t *testing.T, dir, session string, count int) {
	t.Helper()

	filename := fmt.Sprintf("%s-%s.jsonl", session, time.Now().Format("2006-01-02"))
	fpath := filepath.Join(dir, filename)

	f, err := os.Create(fpath)
	if err != nil {
		t.Fatalf("create test log: %v", err)
	}
	defer f.Close()

	eventTypes := []audit.EventType{
		audit.EventTypeCommand, audit.EventTypeSpawn, audit.EventTypeSend,
		audit.EventTypeResponse, audit.EventTypeError,
	}

	var prevHash string
	for i := 0; i < count; i++ {
		entry := audit.AuditEntry{
			Timestamp:   time.Now().Add(-time.Duration(count-i) * time.Minute),
			SessionID:   session,
			EventType:   eventTypes[i%len(eventTypes)],
			Actor:       audit.ActorUser,
			Target:      fmt.Sprintf("target_%d", i),
			Payload:     map[string]interface{}{"action": fmt.Sprintf("action_%d", i)},
			Metadata:    map[string]interface{}{"test": true},
			PrevHash:    prevHash,
			SequenceNum: uint64(i + 1),
		}

		// Compute checksum matching VerifyIntegrity's algorithm
		entryForHash := entry
		entryForHash.Checksum = ""
		hashData, _ := json.Marshal(entryForHash)
		hash := sha256.Sum256(hashData)
		entry.Checksum = hex.EncodeToString(hash[:])
		prevHash = entry.Checksum

		line, _ := json.Marshal(entry)
		fmt.Fprintln(f, string(line))
	}
}
