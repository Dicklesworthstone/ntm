package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/state"
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

	expected := []string{"show", "search", "verify", "export", "list", "history", "actor", "trace", "sources"}
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

// newTestSearcher builds an audit.Searcher rooted at dir. The dedicated
// NewSearcherWithPath constructor was removed as dead code, so redirect the
// default constructor's home-derived path ($HOME/.local/share/ntm/audit)
// at dir via HOME plus a symlink.
func newTestSearcher(t *testing.T, dir string) *audit.Searcher {
	t.Helper()
	home := t.TempDir()
	parent := filepath.Join(home, ".local", "share", "ntm")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir audit parent: %v", err)
	}
	if err := os.Symlink(dir, filepath.Join(parent, "audit")); err != nil {
		t.Fatalf("symlink audit dir: %v", err)
	}
	t.Setenv("HOME", home)
	s, err := audit.NewSearcher()
	if err != nil {
		t.Fatalf("NewSearcher: %v", err)
	}
	return s
}

func withTestSearcher(t *testing.T, dir string) {
	t.Helper()
	searcher := newTestSearcher(t, dir)
	origFunc := newAuditSearcherFunc
	newAuditSearcherFunc = func() (*audit.Searcher, error) {
		return searcher, nil
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

type auditFailureWriter struct {
	err error
}

func (w auditFailureWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestRunAuditVerify_JSONAggregatesAllFilesAndFailsOnCorruption(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "aggregate", 3)
	corruptPath := filepath.Join(tmpDir, "aggregate-2000-01-01.jsonl")
	if err := os.WriteFile(corruptPath, []byte("{not-json}\n"), 0o644); err != nil {
		t.Fatalf("write corrupt audit fixture: %v", err)
	}
	withTestSearcher(t, tmpDir)

	originalJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = originalJSON })

	var buf bytes.Buffer
	err := runAuditVerifyTo(&buf, "aggregate")
	if !errors.Is(err, errJSONFailure) {
		t.Fatalf("runAuditVerifyTo error = %v, want terminal JSON failure", err)
	}

	var result auditVerifyResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("decode one audit verification document: %v; output=%q", err, buf.String())
	}
	if result.Success || result.Verified {
		t.Fatalf("audit verification result = %+v, want explicit failure", result)
	}
	if len(result.Files) != 2 {
		t.Fatalf("audit verification files = %d, want both matched files", len(result.Files))
	}
	failed := 0
	passed := 0
	for _, file := range result.Files {
		if file.Verified {
			passed++
		} else {
			failed++
		}
	}
	if passed != 1 || failed != 1 {
		t.Fatalf("audit verification aggregate = %+v, want one pass and one failure", result.Files)
	}
}

func TestRunAuditVerify_JSONAggregatesSuccessfulFiles(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "verified", 2)
	currentPath := filepath.Join(tmpDir, fmt.Sprintf("verified-%s.jsonl", time.Now().Format("2006-01-02")))
	data, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("read valid audit fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "verified-2000-01-01.jsonl"), data, 0o644); err != nil {
		t.Fatalf("write second valid audit fixture: %v", err)
	}
	withTestSearcher(t, tmpDir)

	originalJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = originalJSON })

	var buf bytes.Buffer
	if err := runAuditVerifyTo(&buf, "verified"); err != nil {
		t.Fatalf("runAuditVerifyTo error = %v", err)
	}
	var result auditVerifyResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("decode one audit verification document: %v; output=%q", err, buf.String())
	}
	if !result.Success || !result.Verified || len(result.Files) != 2 {
		t.Fatalf("audit verification result = %+v, want two-file success", result)
	}
	for _, file := range result.Files {
		if !file.Verified || file.Status != "PASS" || file.Error != "" {
			t.Fatalf("audit verification file = %+v, want clean pass", file)
		}
	}
}

func TestRunAuditVerify_JSONSurfacesEncoderError(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "encode", 1)
	withTestSearcher(t, tmpDir)

	originalJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = originalJSON })

	writeErr := errors.New("audit writer closed")
	err := runAuditVerifyTo(auditFailureWriter{err: writeErr}, "encode")
	if !errors.Is(err, writeErr) {
		t.Fatalf("runAuditVerifyTo error = %v, want encoder error", err)
	}
	if errors.Is(err, errJSONFailure) {
		t.Fatalf("runAuditVerifyTo error = %v, must not claim a written JSON result", err)
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

	err := runAuditShow("show_test", "", "", "", 10, 0)
	if err != nil {
		t.Errorf("show should not error: %v", err)
	}
}

func TestRunAuditShow_WithTimeFilter(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "time_test", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditShow("time_test", "1h", "", "", 10, 0)
	if err != nil {
		t.Errorf("show with --since should not error: %v", err)
	}
}

func TestRunAuditShow_WithTypeFilter(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "filter_test", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditShow("filter_test", "", "", "command", 10, 0)
	if err != nil {
		t.Errorf("show with type filter should not error: %v", err)
	}
}

func TestRunAuditSearch_Pattern(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "search_test", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditSearch("target", "", "", "", "", 30, 50, 0)
	if err != nil {
		t.Errorf("search should not error: %v", err)
	}
}

func TestRunAuditSearch_NoResults(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "noresult_test", 3)
	withTestSearcher(t, tmpDir)

	err := runAuditSearch("xyznonexistent999", "", "", "", "", 30, 50, 0)
	if err != nil {
		t.Errorf("search with no results should not error: %v", err)
	}
}

func TestRunAuditSearch_WithFilters(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestAuditLog(t, tmpDir, "filtered_search", 5)
	withTestSearcher(t, tmpDir)

	err := runAuditSearch("target", "filtered_search", "command", "user", "target_*", 30, 50, 0)
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

func TestAuditStateSubcommands_HistoryActorTraceAndSources(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seed, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := seed.Migrate(); err != nil {
		_ = seed.Close()
		t.Fatalf("seed.Migrate: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := seed.UpsertAuditActor(&state.AuditActor{
		ActorType:    "user",
		ActorID:      "bob",
		DisplayName:  "Bob Operator",
		FirstSeenAt:  now.Add(-10 * time.Minute),
		LastSeenAt:   now,
		EventCount:   2,
		KnownOrigins: `["cli"]`,
	}); err != nil {
		_ = seed.Close()
		t.Fatalf("UpsertAuditActor: %v", err)
	}

	for i, step := range []struct {
		eventType string
		summary   string
	}{
		{"session.spawned", "spawned session alpha"},
		{"session.scaled", "scaled session alpha to 3 agents"},
	} {
		if _, err := seed.RecordAuditEvent(&state.AuditEvent{
			Ts:            now.Add(time.Duration(i) * time.Second),
			ActorType:     "user",
			ActorID:       "bob",
			ActorOrigin:   "cli",
			RequestID:     "req-trace-77",
			CorrelationID: "corr-77",
			Category:      "actuation",
			EventType:     step.eventType,
			Severity:      state.SeverityInfo,
			EntityType:    "session",
			EntityID:      "alpha",
			ChangeSummary: step.summary,
			Reason:        "operator workflow",
		}); err != nil {
			_ = seed.Close()
			t.Fatalf("RecordAuditEvent(%d): %v", i, err)
		}
	}

	if err := seed.UpsertSourceHealth(&state.SourceHealth{
		SourceName:  "tmux",
		Available:   true,
		Healthy:     true,
		LastCheckAt: now,
	}); err != nil {
		_ = seed.Close()
		t.Fatalf("UpsertSourceHealth(tmux): %v", err)
	}
	if err := seed.UpsertSourceHealth(&state.SourceHealth{
		SourceName:          "mail",
		Available:           false,
		Healthy:             false,
		Reason:              "connection refused",
		ConsecutiveFailures: 3,
		LastError:           "dial tcp 127.0.0.1:8765: connect: connection refused",
		LastCheckAt:         now,
	}); err != nil {
		_ = seed.Close()
		t.Fatalf("UpsertSourceHealth(mail): %v", err)
	}
	_ = seed.Close()

	origOpen := openAuditStateStoreFunc
	openAuditStateStoreFunc = func() (*state.Store, error) {
		st, openErr := state.Open(dbPath)
		if openErr != nil {
			return nil, openErr
		}
		if migErr := st.Migrate(); migErr != nil {
			_ = st.Close()
			return nil, migErr
		}
		return st, nil
	}
	t.Cleanup(func() { openAuditStateStoreFunc = origOpen })

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	// 1. `ntm audit history session alpha`
	histRaw, err := captureStdout(t, func() error {
		cmd := newAuditCmd()
		cmd.SetArgs([]string{"history", "session", "alpha", "--limit=10"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("audit history failed: %v", err)
	}
	var histOut AuditStateEventsOutput
	if err := json.Unmarshal([]byte(histRaw), &histOut); err != nil {
		t.Fatalf("decode audit history: %v (raw=%q)", err, histRaw)
	}
	if !histOut.Success || histOut.Count != 2 || histOut.EntityType != "session" || histOut.EntityID != "alpha" {
		t.Fatalf("unexpected audit history output: %+v", histOut)
	}

	// 2. `ntm audit actor user bob`
	actorRaw, err := captureStdout(t, func() error {
		cmd := newAuditCmd()
		cmd.SetArgs([]string{"actor", "user", "bob", "--since=1h"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("audit actor failed: %v", err)
	}
	var actorOut AuditActorOutput
	if err := json.Unmarshal([]byte(actorRaw), &actorOut); err != nil {
		t.Fatalf("decode audit actor: %v (raw=%q)", err, actorRaw)
	}
	if !actorOut.Success || actorOut.Actor == nil || actorOut.Actor.DisplayName != "Bob Operator" || actorOut.Count != 2 {
		t.Fatalf("unexpected audit actor output: %+v", actorOut)
	}

	// 3. `ntm audit trace req-trace-77`
	traceRaw, err := captureStdout(t, func() error {
		cmd := newAuditCmd()
		cmd.SetArgs([]string{"trace", "req-trace-77"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("audit trace failed: %v", err)
	}
	var traceOut AuditStateEventsOutput
	if err := json.Unmarshal([]byte(traceRaw), &traceOut); err != nil {
		t.Fatalf("decode audit trace: %v (raw=%q)", err, traceRaw)
	}
	if !traceOut.Success || traceOut.Count != 2 || traceOut.Events[0].EventType != "session.spawned" || traceOut.Events[1].EventType != "session.scaled" {
		t.Fatalf("unexpected audit trace chronological output: %+v", traceOut)
	}

	// 4. `ntm audit sources --degraded` and `ntm audit sources tmux`
	degRaw, err := captureStdout(t, func() error {
		cmd := newAuditCmd()
		cmd.SetArgs([]string{"sources", "--degraded"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("audit sources --degraded failed: %v", err)
	}
	var degOut AuditSourcesOutput
	if err := json.Unmarshal([]byte(degRaw), &degOut); err != nil {
		t.Fatalf("decode audit sources --degraded: %v (raw=%q)", err, degRaw)
	}
	if !degOut.Success || !degOut.DegradedOnly || degOut.Count != 1 || degOut.Sources[0].SourceName != "mail" {
		t.Fatalf("unexpected degraded sources output: %+v", degOut)
	}

	singleRaw, err := captureStdout(t, func() error {
		cmd := newAuditCmd()
		cmd.SetArgs([]string{"sources", "tmux"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("audit sources tmux failed: %v", err)
	}
	var singleOut AuditSourcesOutput
	if err := json.Unmarshal([]byte(singleRaw), &singleOut); err != nil {
		t.Fatalf("decode audit sources tmux: %v (raw=%q)", err, singleRaw)
	}
	if !singleOut.Success || singleOut.Count != 1 || singleOut.Sources[0].SourceName != "tmux" || !singleOut.Sources[0].Healthy {
		t.Fatalf("unexpected single source output: %+v", singleOut)
	}
}
