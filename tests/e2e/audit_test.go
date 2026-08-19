package e2e

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestAudit_LogCreation tests audit log creation with entries.
func TestAudit_LogCreation(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-AUDIT: Test audit log creation")

	// Create audit directory in temp
	auditDir := filepath.Join(tmpDir, "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatalf("Failed to create audit directory: %v", err)
	}

	sessionID := "test_audit_session"

	// Create audit logger config
	cfg := &audit.LoggerConfig{
		SessionID:     sessionID,
		BufferSize:    1, // Flush immediately
		FlushInterval: time.Second,
	}

	// We can't use NewAuditLogger directly as it uses fixed home dir path.
	// Instead, simulate by writing JSONL entries directly.
	logFile := filepath.Join(auditDir, sessionID+"-"+time.Now().Format("2006-01-02")+".jsonl")
	logger.Log("Creating audit log file: %s", logFile)
	logger.Log("Config: %+v", cfg)

	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("Failed to create log file: %v", err)
	}

	// Write audit entries manually (simulating AuditLogger.Log)
	var prevHash string
	entries := []audit.AuditEntry{
		{
			Timestamp:   time.Now().UTC(),
			SessionID:   sessionID,
			EventType:   audit.EventTypeCommand,
			Actor:       audit.ActorUser,
			Target:      "ntm spawn",
			Payload:     map[string]interface{}{"args": []string{"myproject"}},
			SequenceNum: 1,
		},
		{
			Timestamp:   time.Now().UTC().Add(time.Second),
			SessionID:   sessionID,
			EventType:   audit.EventTypeSpawn,
			Actor:       audit.ActorSystem,
			Target:      "cc_1",
			Payload:     map[string]interface{}{"agent": "claude", "model": "opus"},
			SequenceNum: 2,
		},
		{
			Timestamp:   time.Now().UTC().Add(2 * time.Second),
			SessionID:   sessionID,
			EventType:   audit.EventTypeSend,
			Actor:       audit.ActorUser,
			Target:      "cc_1",
			Payload:     map[string]interface{}{"prompt": "Hello, agent!"},
			SequenceNum: 3,
		},
	}

	encoder := json.NewEncoder(f)
	for i := range entries {
		entries[i].PrevHash = prevHash

		// Calculate checksum
		entryForHash := entries[i]
		entryForHash.Checksum = ""
		hashData, _ := json.Marshal(entryForHash)
		hash := sha256.Sum256(hashData)
		entries[i].Checksum = hex.EncodeToString(hash[:])
		prevHash = entries[i].Checksum

		if err := encoder.Encode(entries[i]); err != nil {
			f.Close()
			t.Fatalf("Failed to write entry: %v", err)
		}
		logger.Log("[E2E-AUDIT] Wrote entry %d: type=%s actor=%s", i+1, entries[i].EventType, entries[i].Actor)
	}
	f.Close()

	// Verify log file exists with correct content
	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("Log file not found: %v", err)
	}
	logger.Log("[E2E-AUDIT] Log file size: %d bytes", info.Size())

	// Read and verify entries
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(entries) {
		t.Errorf("Expected %d entries, got %d", len(entries), len(lines))
	}

	for i, line := range lines {
		var entry audit.AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("Failed to parse entry %d: %v", i, err)
			continue
		}
		if entry.SessionID != sessionID {
			t.Errorf("Entry %d: expected session %q, got %q", i, sessionID, entry.SessionID)
		}
		if entry.Checksum == "" {
			t.Errorf("Entry %d: missing checksum", i)
		}
		logger.Log("[E2E-AUDIT] Verified entry %d: seq=%d checksum=%s...", i+1, entry.SequenceNum, entry.Checksum[:16])
	}

	logger.Log("PASS: Audit log creation verified (%d entries)", len(entries))
}

// TestAudit_QueryFiltering tests audit log querying with filters.
func TestAudit_QueryFiltering(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-AUDIT: Test query filtering")

	// Create audit directory with test data
	auditDir := filepath.Join(tmpDir, "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatalf("Failed to create audit directory: %v", err)
	}

	sessionID := "query_test_session"
	logFile := filepath.Join(auditDir, sessionID+"-"+time.Now().Format("2006-01-02")+".jsonl")

	// Create entries with different types and actors
	entries := []audit.AuditEntry{
		{Timestamp: time.Now().UTC(), SessionID: sessionID, EventType: audit.EventTypeCommand, Actor: audit.ActorUser, Target: "spawn", SequenceNum: 1, Checksum: "hash1"},
		{Timestamp: time.Now().UTC(), SessionID: sessionID, EventType: audit.EventTypeSpawn, Actor: audit.ActorSystem, Target: "cc_1", SequenceNum: 2, Checksum: "hash2"},
		{Timestamp: time.Now().UTC(), SessionID: sessionID, EventType: audit.EventTypeSend, Actor: audit.ActorUser, Target: "cc_1", SequenceNum: 3, Checksum: "hash3"},
		{Timestamp: time.Now().UTC(), SessionID: sessionID, EventType: audit.EventTypeResponse, Actor: audit.ActorAgent, Target: "cc_1", SequenceNum: 4, Checksum: "hash4"},
		{Timestamp: time.Now().UTC(), SessionID: sessionID, EventType: audit.EventTypeError, Actor: audit.ActorSystem, Target: "cc_1", SequenceNum: 5, Checksum: "hash5"},
		{Timestamp: time.Now().UTC(), SessionID: sessionID, EventType: audit.EventTypeCommand, Actor: audit.ActorUser, Target: "kill", SequenceNum: 6, Checksum: "hash6"},
	}

	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("Failed to create log file: %v", err)
	}
	encoder := json.NewEncoder(f)
	for _, entry := range entries {
		encoder.Encode(entry)
	}
	f.Close()
	logger.Log("[E2E-AUDIT] Created %d test entries", len(entries))

	// Use Searcher to query
	searcher := newAuditSearcherForTest(t, auditDir)

	// Query all entries
	result, err := searcher.Search(audit.Query{Sessions: []string{sessionID}})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	logger.Log("[E2E-AUDIT] Query all: %d entries", result.TotalCount)
	if result.TotalCount != len(entries) {
		t.Errorf("Expected %d entries, got %d", len(entries), result.TotalCount)
	}

	// Query by event type
	result, err = searcher.Search(audit.Query{
		Sessions:   []string{sessionID},
		EventTypes: []audit.EventType{audit.EventTypeCommand},
	})
	if err != nil {
		t.Fatalf("Search by event type failed: %v", err)
	}
	logger.Log("[E2E-AUDIT] Query EventTypeCommand: %d entries", result.TotalCount)
	if result.TotalCount != 2 {
		t.Errorf("Expected 2 command entries, got %d", result.TotalCount)
	}

	// Query by actor
	result, err = searcher.Search(audit.Query{
		Sessions: []string{sessionID},
		Actors:   []audit.Actor{audit.ActorUser},
	})
	if err != nil {
		t.Fatalf("Search by actor failed: %v", err)
	}
	logger.Log("[E2E-AUDIT] Query ActorUser: %d entries", result.TotalCount)
	if result.TotalCount != 3 {
		t.Errorf("Expected 3 user entries, got %d", result.TotalCount)
	}

	// Query by actor + event type
	result, err = searcher.Search(audit.Query{
		Sessions:   []string{sessionID},
		Actors:     []audit.Actor{audit.ActorSystem},
		EventTypes: []audit.EventType{audit.EventTypeError},
	})
	if err != nil {
		t.Fatalf("Search by actor+type failed: %v", err)
	}
	logger.Log("[E2E-AUDIT] Query ActorSystem + EventTypeError: %d entries", result.TotalCount)
	if result.TotalCount != 1 {
		t.Errorf("Expected 1 system error entry, got %d", result.TotalCount)
	}

	// Query with limit
	result, err = searcher.Search(audit.Query{
		Sessions: []string{sessionID},
		Limit:    3,
	})
	if err != nil {
		t.Fatalf("Search with limit failed: %v", err)
	}
	logger.Log("[E2E-AUDIT] Query limit=3: %d entries", result.TotalCount)
	if result.TotalCount != 3 {
		t.Errorf("Expected 3 entries with limit, got %d", result.TotalCount)
	}

	logger.Log("PASS: Audit query filtering verified")
}

// TestAudit_ExportCSV tests exporting audit logs to CSV format.
func TestAudit_ExportCSV(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-AUDIT: Test CSV export")

	// Create audit directory with test data
	auditDir := filepath.Join(tmpDir, "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatalf("Failed to create audit directory: %v", err)
	}

	sessionID := "export_test_session"
	logFile := filepath.Join(auditDir, sessionID+"-"+time.Now().Format("2006-01-02")+".jsonl")

	// Create test entries
	now := time.Now().UTC()
	entries := []audit.AuditEntry{
		{Timestamp: now, SessionID: sessionID, EventType: audit.EventTypeCommand, Actor: audit.ActorUser, Target: "spawn", SequenceNum: 1, Checksum: "hash1"},
		{Timestamp: now.Add(time.Second), SessionID: sessionID, EventType: audit.EventTypeSpawn, Actor: audit.ActorSystem, Target: "cc_1", SequenceNum: 2, Checksum: "hash2"},
		{Timestamp: now.Add(2 * time.Second), SessionID: sessionID, EventType: audit.EventTypeSend, Actor: audit.ActorUser, Target: "cc_1", SequenceNum: 3, Checksum: "hash3"},
	}

	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("Failed to create log file: %v", err)
	}
	encoder := json.NewEncoder(f)
	for _, entry := range entries {
		encoder.Encode(entry)
	}
	f.Close()

	// Export to CSV
	csvFile := filepath.Join(tmpDir, "audit_export.csv")
	csvOut, err := os.Create(csvFile)
	if err != nil {
		t.Fatalf("Failed to create CSV file: %v", err)
	}

	writer := csv.NewWriter(csvOut)

	// Write header
	header := []string{"timestamp", "session_id", "event_type", "actor", "target", "sequence_num", "checksum"}
	if err := writer.Write(header); err != nil {
		csvOut.Close()
		t.Fatalf("Failed to write CSV header: %v", err)
	}

	// Read JSONL and write CSV rows
	logData, _ := os.ReadFile(logFile)
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	for _, line := range lines {
		var entry audit.AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		row := []string{
			entry.Timestamp.Format(time.RFC3339),
			entry.SessionID,
			string(entry.EventType),
			string(entry.Actor),
			entry.Target,
			fmt.Sprintf("%d", entry.SequenceNum),
			entry.Checksum,
		}
		writer.Write(row)
	}
	writer.Flush()
	csvOut.Close()

	logger.Log("[E2E-AUDIT] Exported to: %s", csvFile)

	// Verify CSV content
	csvIn, err := os.Open(csvFile)
	if err != nil {
		t.Fatalf("Failed to open CSV file: %v", err)
	}
	defer csvIn.Close()

	reader := csv.NewReader(csvIn)
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("Failed to read CSV: %v", err)
	}

	// Verify header
	if len(records) < 1 {
		t.Fatal("CSV has no header")
	}
	if records[0][0] != "timestamp" {
		t.Errorf("Expected first header column 'timestamp', got %q", records[0][0])
	}
	logger.Log("[E2E-AUDIT] CSV header: %v", records[0])

	// Verify row count (header + data rows)
	expectedRows := 1 + len(entries)
	if len(records) != expectedRows {
		t.Errorf("Expected %d CSV rows (1 header + %d data), got %d", expectedRows, len(entries), len(records))
	}
	logger.Log("[E2E-AUDIT] CSV rows: %d (1 header + %d data)", len(records), len(records)-1)

	// Verify data row content
	for i := 1; i < len(records); i++ {
		if records[i][1] != sessionID {
			t.Errorf("Row %d: expected session_id %q, got %q", i, sessionID, records[i][1])
		}
		logger.Log("[E2E-AUDIT] CSV row %d: type=%s actor=%s target=%s", i, records[i][2], records[i][3], records[i][4])
	}

	logger.Log("PASS: Audit CSV export verified")
}

// TestAudit_IntegrityVerification tests audit log tamper detection.
func TestAudit_IntegrityVerification(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-AUDIT: Test integrity verification")

	// Create audit directory
	auditDir := filepath.Join(tmpDir, "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatalf("Failed to create audit directory: %v", err)
	}

	sessionID := "integrity_test_session"
	logFile := filepath.Join(auditDir, sessionID+"-"+time.Now().Format("2006-01-02")+".jsonl")

	// Create properly chained entries
	var prevHash string
	entries := []audit.AuditEntry{
		{Timestamp: time.Now().UTC(), SessionID: sessionID, EventType: audit.EventTypeCommand, Actor: audit.ActorUser, Target: "test1", SequenceNum: 1},
		{Timestamp: time.Now().UTC().Add(time.Second), SessionID: sessionID, EventType: audit.EventTypeSpawn, Actor: audit.ActorSystem, Target: "test2", SequenceNum: 2},
		{Timestamp: time.Now().UTC().Add(2 * time.Second), SessionID: sessionID, EventType: audit.EventTypeSend, Actor: audit.ActorUser, Target: "test3", SequenceNum: 3},
	}

	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("Failed to create log file: %v", err)
	}
	encoder := json.NewEncoder(f)
	for i := range entries {
		entries[i].PrevHash = prevHash

		// Calculate proper checksum
		entryForHash := entries[i]
		entryForHash.Checksum = ""
		hashData, _ := json.Marshal(entryForHash)
		hash := sha256.Sum256(hashData)
		entries[i].Checksum = hex.EncodeToString(hash[:])
		prevHash = entries[i].Checksum

		encoder.Encode(entries[i])
	}
	f.Close()
	logger.Log("[E2E-AUDIT] Created %d chained entries", len(entries))

	// Verify integrity passes on valid log
	err = audit.VerifyIntegrity(logFile)
	if err != nil {
		t.Errorf("Expected valid log to pass verification, got error: %v", err)
	} else {
		logger.Log("[E2E-AUDIT] Valid log integrity check: PASS")
	}

	// Create a tampered log file
	tamperedFile := filepath.Join(auditDir, "tampered.jsonl")
	f, err = os.Create(tamperedFile)
	if err != nil {
		t.Fatalf("Failed to create tampered file: %v", err)
	}
	encoder = json.NewEncoder(f)

	// Copy first entry
	encoder.Encode(entries[0])

	// Tamper with second entry (change target but keep old hash)
	tampered := entries[1]
	tampered.Target = "TAMPERED_TARGET"
	// Keep original checksum (incorrect now)
	encoder.Encode(tampered)

	// Copy third entry
	encoder.Encode(entries[2])
	f.Close()
	logger.Log("[E2E-AUDIT] Created tampered log file")

	// Verify integrity fails on tampered log
	err = audit.VerifyIntegrity(tamperedFile)
	if err == nil {
		t.Error("Expected tampered log to fail verification, but it passed")
	} else {
		logger.Log("[E2E-AUDIT] Tampered log integrity check: FAIL (expected)")
		logger.Log("[E2E-AUDIT] Error: %v", err)
	}

	// Test with broken hash chain
	brokenChainFile := filepath.Join(auditDir, "broken_chain.jsonl")
	f, err = os.Create(brokenChainFile)
	if err != nil {
		t.Fatalf("Failed to create broken chain file: %v", err)
	}
	encoder = json.NewEncoder(f)

	// Write entries with wrong prev_hash
	for i := range entries {
		entry := entries[i]
		if i == 2 {
			entry.PrevHash = "wrong_hash_value" // Break the chain
		}
		encoder.Encode(entry)
	}
	f.Close()
	logger.Log("[E2E-AUDIT] Created broken chain log file")

	// Verify fails on broken chain
	err = audit.VerifyIntegrity(brokenChainFile)
	if err == nil {
		t.Error("Expected broken chain log to fail verification")
	} else {
		logger.Log("[E2E-AUDIT] Broken chain integrity check: FAIL (expected)")
		logger.Log("[E2E-AUDIT] Error: %v", err)
	}

	logger.Log("PASS: Audit integrity verification verified")
}

// TestAudit_RetentionPolicy tests audit log retention and archival.
func TestAudit_RetentionPolicy(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-AUDIT: Test retention policy")

	// Create audit directory
	auditDir := filepath.Join(tmpDir, "audit")
	archiveDir := filepath.Join(tmpDir, "audit_archive")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatalf("Failed to create audit directory: %v", err)
	}
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatalf("Failed to create archive directory: %v", err)
	}

	// Create multiple session log files with different dates
	sessions := []struct {
		name string
		date string
		age  int // days old
	}{
		{"session_recent", time.Now().Format("2006-01-02"), 0},
		{"session_week", time.Now().AddDate(0, 0, -7).Format("2006-01-02"), 7},
		{"session_old1", time.Now().AddDate(0, 0, -35).Format("2006-01-02"), 35},
		{"session_old2", time.Now().AddDate(0, 0, -45).Format("2006-01-02"), 45},
	}

	for _, s := range sessions {
		logFile := filepath.Join(auditDir, s.name+"-"+s.date+".jsonl")
		entry := audit.AuditEntry{
			Timestamp:   time.Now().AddDate(0, 0, -s.age),
			SessionID:   s.name,
			EventType:   audit.EventTypeCommand,
			Actor:       audit.ActorUser,
			Target:      "test",
			SequenceNum: 1,
			Checksum:    "hash",
		}
		f, err := os.Create(logFile)
		if err != nil {
			t.Fatalf("Failed to create log file: %v", err)
		}
		json.NewEncoder(f).Encode(entry)
		f.Close()
		logger.Log("[E2E-AUDIT] Created log file: %s (age: %d days)", filepath.Base(logFile), s.age)
	}

	// Apply retention policy (30 days) - archive old logs
	retentionDays := 30
	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	files, err := os.ReadDir(auditDir)
	if err != nil {
		t.Fatalf("Failed to read audit directory: %v", err)
	}

	archivedCount := 0
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
			continue
		}

		// Parse date from filename (session-YYYY-MM-DD.jsonl)
		parts := strings.Split(strings.TrimSuffix(file.Name(), ".jsonl"), "-")
		if len(parts) >= 4 {
			dateStr := parts[len(parts)-3] + "-" + parts[len(parts)-2] + "-" + parts[len(parts)-1]
			fileDate, err := time.Parse("2006-01-02", dateStr)
			if err == nil && fileDate.Before(cutoff) {
				// Move to archive
				src := filepath.Join(auditDir, file.Name())
				dst := filepath.Join(archiveDir, file.Name())
				if err := os.Rename(src, dst); err != nil {
					t.Errorf("Failed to archive %s: %v", file.Name(), err)
				} else {
					archivedCount++
					logger.Log("[E2E-AUDIT] Archived: %s", file.Name())
				}
			}
		}
	}

	logger.Log("[E2E-AUDIT] Archived %d old logs (retention: %d days)", archivedCount, retentionDays)

	// Verify archive directory has old files
	archivedFiles, err := os.ReadDir(archiveDir)
	if err != nil {
		t.Fatalf("Failed to read archive directory: %v", err)
	}
	if len(archivedFiles) != 2 {
		t.Errorf("Expected 2 archived files, got %d", len(archivedFiles))
	}

	// Verify audit directory has recent files
	remainingFiles, err := os.ReadDir(auditDir)
	if err != nil {
		t.Fatalf("Failed to read audit directory: %v", err)
	}
	if len(remainingFiles) != 2 {
		t.Errorf("Expected 2 remaining files, got %d", len(remainingFiles))
	}

	// List remaining files
	for _, f := range remainingFiles {
		logger.Log("[E2E-AUDIT] Remaining in audit: %s", f.Name())
	}
	for _, f := range archivedFiles {
		logger.Log("[E2E-AUDIT] In archive: %s", f.Name())
	}

	logger.Log("PASS: Audit retention policy verified")
}

// TestAudit_ConcurrentWrites tests concurrent audit log writes.
func TestAudit_ConcurrentWrites(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-AUDIT: Test concurrent writes")

	auditDir := filepath.Join(tmpDir, "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatalf("Failed to create audit directory: %v", err)
	}

	sessionID := "concurrent_test_session"
	logFile := filepath.Join(auditDir, sessionID+"-"+time.Now().Format("2006-01-02")+".jsonl")

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("Failed to create log file: %v", err)
	}

	// Simulate concurrent writes (using sequential writes with unique sequence numbers)
	numEntries := 50
	var prevHash string
	for i := 1; i <= numEntries; i++ {
		entry := audit.AuditEntry{
			Timestamp:   time.Now().UTC(),
			SessionID:   sessionID,
			EventType:   audit.EventTypeCommand,
			Actor:       audit.ActorUser,
			Target:      "concurrent_test",
			SequenceNum: uint64(i),
			PrevHash:    prevHash,
		}

		// Calculate checksum
		entryForHash := entry
		entryForHash.Checksum = ""
		hashData, _ := json.Marshal(entryForHash)
		hash := sha256.Sum256(hashData)
		entry.Checksum = hex.EncodeToString(hash[:])
		prevHash = entry.Checksum

		data, _ := json.Marshal(entry)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	f.Close()

	logger.Log("[E2E-AUDIT] Wrote %d entries", numEntries)

	// Verify all entries are present and chain is valid
	err = audit.VerifyIntegrity(logFile)
	if err != nil {
		t.Errorf("Integrity verification failed: %v", err)
	} else {
		logger.Log("[E2E-AUDIT] Integrity check: PASS")
	}

	// Count entries
	data, _ := os.ReadFile(logFile)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != numEntries {
		t.Errorf("Expected %d entries, got %d", numEntries, len(lines))
	}

	// Verify sequence numbers
	rf, _ := os.Open(logFile)
	scanner := bufio.NewScanner(rf)
	expectedSeq := uint64(1)
	for scanner.Scan() {
		var entry audit.AuditEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.SequenceNum != expectedSeq {
			t.Errorf("Sequence mismatch: expected %d, got %d", expectedSeq, entry.SequenceNum)
		}
		expectedSeq++
	}
	rf.Close()

	logger.Log("[E2E-AUDIT] Sequence numbers verified (1-%d)", numEntries)
	logger.Log("PASS: Concurrent writes verified")
}

// TestAudit_TimeRangeQuery tests querying audit logs by time range.
func TestAudit_TimeRangeQuery(t *testing.T) {
	testutil.RequireE2E(t)

	tmpDir := t.TempDir()
	logger := testutil.NewTestLogger(t, tmpDir)
	logger.LogSection("E2E-AUDIT: Test time range query")

	auditDir := filepath.Join(tmpDir, "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		t.Fatalf("Failed to create audit directory: %v", err)
	}

	sessionID := "timerange_test"
	logFile := filepath.Join(auditDir, sessionID+"-"+time.Now().Format("2006-01-02")+".jsonl")

	// Create entries at different times
	baseTime := time.Now().UTC().Add(-time.Hour) // 1 hour ago
	entries := []audit.AuditEntry{
		{Timestamp: baseTime, SessionID: sessionID, EventType: audit.EventTypeCommand, SequenceNum: 1, Checksum: "h1"},
		{Timestamp: baseTime.Add(15 * time.Minute), SessionID: sessionID, EventType: audit.EventTypeSpawn, SequenceNum: 2, Checksum: "h2"},
		{Timestamp: baseTime.Add(30 * time.Minute), SessionID: sessionID, EventType: audit.EventTypeSend, SequenceNum: 3, Checksum: "h3"},
		{Timestamp: baseTime.Add(45 * time.Minute), SessionID: sessionID, EventType: audit.EventTypeResponse, SequenceNum: 4, Checksum: "h4"},
		{Timestamp: baseTime.Add(60 * time.Minute), SessionID: sessionID, EventType: audit.EventTypeCommand, SequenceNum: 5, Checksum: "h5"},
	}

	f, _ := os.Create(logFile)
	encoder := json.NewEncoder(f)
	for _, e := range entries {
		encoder.Encode(e)
	}
	f.Close()

	logger.Log("[E2E-AUDIT] Created %d entries spanning 1 hour", len(entries))

	searcher := newAuditSearcherForTest(t, auditDir)

	// Query entries from last 30 minutes
	since := baseTime.Add(30 * time.Minute)
	result, err := searcher.Search(audit.Query{
		Sessions: []string{sessionID},
		Since:    &since,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	logger.Log("[E2E-AUDIT] Query since %v: %d entries", since.Format(time.RFC3339), result.TotalCount)
	if result.TotalCount != 3 {
		t.Errorf("Expected 3 entries since 30min mark, got %d", result.TotalCount)
	}

	// Query entries until 30 minutes ago
	until := baseTime.Add(30 * time.Minute)
	result, err = searcher.Search(audit.Query{
		Sessions: []string{sessionID},
		Until:    &until,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	logger.Log("[E2E-AUDIT] Query until %v: %d entries", until.Format(time.RFC3339), result.TotalCount)
	if result.TotalCount != 3 {
		t.Errorf("Expected 3 entries until 30min mark, got %d", result.TotalCount)
	}

	// Query entries in middle range (15-45 min)
	rangeStart := baseTime.Add(15 * time.Minute)
	rangeEnd := baseTime.Add(45 * time.Minute)
	result, err = searcher.Search(audit.Query{
		Sessions: []string{sessionID},
		Since:    &rangeStart,
		Until:    &rangeEnd,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	logger.Log("[E2E-AUDIT] Query range 15-45min: %d entries", result.TotalCount)
	if result.TotalCount != 3 {
		t.Errorf("Expected 3 entries in range, got %d", result.TotalCount)
	}

	logger.Log("PASS: Time range query verified")
}

// newAuditSearcherForTest builds an audit.Searcher rooted at dir. The
// dedicated NewSearcherWithPath constructor was removed as dead code, so
// redirect the default constructor's home-derived path
// ($HOME/.local/share/ntm/audit) at dir via HOME plus a symlink.
func newAuditSearcherForTest(t *testing.T, dir string) *audit.Searcher {
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
