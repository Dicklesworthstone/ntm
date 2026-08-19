package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditStore_Record(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// Record an audit entry
	rec := &AuditRecord{
		RequestID:  "req-123",
		UserID:     "user-456",
		Role:       RoleOperator,
		Action:     AuditActionCreate,
		Resource:   "session",
		ResourceID: "sess-789",
		Method:     "POST",
		Path:       "/api/v1/sessions",
		StatusCode: 201,
		Duration:   42,
		SessionID:  "sess-789",
		RemoteAddr: "127.0.0.1:54321",
		UserAgent:  "TestAgent/1.0",
	}

	if err := store.Record(rec); err != nil {
		t.Fatalf("Record error: %v", err)
	}

	// Verify JSONL file was written
	data, err := os.ReadFile(cfg.JSONLPath)
	if err != nil {
		t.Fatalf("read jsonl error: %v", err)
	}
	if len(data) == 0 {
		t.Error("jsonl file is empty")
	}

	// Parse the JSONL record
	var parsed AuditRecord
	if err := json.Unmarshal(data[:len(data)-1], &parsed); err != nil {
		t.Fatalf("parse jsonl error: %v", err)
	}
	if parsed.RequestID != "req-123" {
		t.Errorf("parsed.RequestID = %q, want %q", parsed.RequestID, "req-123")
	}
	if parsed.Action != AuditActionCreate {
		t.Errorf("parsed.Action = %q, want %q", parsed.Action, AuditActionCreate)
	}
}

func TestAuditStore_JSONLOnlySchedulesRetentionCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "audit.jsonl")
	store, err := NewAuditStore(AuditStoreConfig{
		JSONLPath:       jsonlPath,
		Retention:       24 * time.Hour,
		CleanupInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// Keep the test small while exercising the periodic cleanup path rather
	// than calling cleanup() directly.
	store.maxBytes = 1
	if err := store.Record(&AuditRecord{Action: AuditActionCreate, Resource: "sessions"}); err != nil {
		t.Fatalf("Record error: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		entries, readErr := os.ReadDir(tmpDir)
		if readErr != nil {
			t.Fatalf("ReadDir: %v", readErr)
		}
		for _, entry := range entries {
			if entry.Name() != "audit.jsonl" && strings.HasPrefix(entry.Name(), "audit.") && strings.HasSuffix(entry.Name(), ".jsonl") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("JSONL-only audit store never rotated its configured audit log")
}

func TestAuditStore_RecordCountsUnavailableConfiguredJSONLSink(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewAuditStore(AuditStoreConfig{
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	store.appendMu.Lock()
	store.mu.Lock()
	if err := store.jsonlFile.Close(); err != nil {
		store.mu.Unlock()
		store.appendMu.Unlock()
		t.Fatalf("close JSONL sink: %v", err)
	}
	store.jsonlFile = nil
	store.mu.Unlock()
	store.appendMu.Unlock()

	if err := store.Record(&AuditRecord{Action: AuditActionCreate, Resource: "sessions"}); err != nil {
		t.Fatalf("Record error: %v", err)
	}
	if got := store.writeFailures.Load(); got != 1 {
		t.Fatalf("WriteFailures() = %d, want 1 for an unavailable configured JSONL sink", got)
	}
}

func TestAuditStore_Retention(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       1 * time.Millisecond, // Very short retention
		CleanupInterval: 24 * time.Hour,       // Don't run auto-cleanup
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// Record an entry
	rec := &AuditRecord{
		Timestamp:  time.Now().Add(-time.Hour), // Old record
		RequestID:  "old-req",
		UserID:     "user",
		Role:       RoleViewer,
		Action:     AuditActionExecute,
		Resource:   "test",
		Method:     "GET",
		Path:       "/test",
		StatusCode: 200,
		Duration:   0,
		RemoteAddr: "127.0.0.1",
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record error: %v", err)
	}

	// Verify record exists
	if got := countAuditRows(t, store); got != 1 {
		t.Fatalf("expected 1 record before cleanup, got %d", got)
	}

	// Run cleanup manually
	store.cleanup()

	// Verify record was removed
	if got := countAuditRows(t, store); got != 0 {
		t.Errorf("expected 0 records after cleanup, got %d", got)
	}
}

func TestInferAction(t *testing.T) {
	tests := []struct {
		method string
		want   AuditAction
	}{
		{"POST", AuditActionCreate},
		{"PUT", AuditActionUpdate},
		{"PATCH", AuditActionUpdate},
		{"DELETE", AuditActionDelete},
		{"GET", AuditActionExecute},
		{"OPTIONS", AuditActionExecute},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got := inferAction(tt.method)
			if got != tt.want {
				t.Errorf("inferAction(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestInferResource(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/api/v1/sessions", "sessions"},
		{"/api/v1/sessions/123", "sessions"},
		{"/api/v1/agents/a1/status", "agents"},
		{"/api/v1/jobs", "jobs"},
		{"/api/sessions", "sessions"},
		{"/api/health", "health"},
		{"/other", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := inferResource(tt.path)
			if got != tt.want {
				t.Errorf("inferResource(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsMutatingMethod(t *testing.T) {
	tests := []struct {
		method string
		want   bool
	}{
		{"GET", false},
		{"HEAD", false},
		{"OPTIONS", false},
		{"POST", true},
		{"PUT", true},
		{"PATCH", true},
		{"DELETE", true},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got := isMutatingMethod(tt.method)
			if got != tt.want {
				t.Errorf("isMutatingMethod(%q) = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{"/api/v1/sessions", []string{"api", "v1", "sessions"}},
		{"/api/v1/sessions/123/agents", []string{"api", "v1", "sessions", "123", "agents"}},
		{"/", nil},
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := splitPath(tt.path)
			if len(got) != len(tt.want) {
				t.Errorf("splitPath(%q) = %v (len %d), want %v (len %d)", tt.path, got, len(got), tt.want, len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitPath(%q)[%d] = %q, want %q", tt.path, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDefaultAuditStoreConfig(t *testing.T) {
	dataDir := "/tmp/test-data"
	cfg := DefaultAuditStoreConfig(dataDir)

	expectedDBPath := filepath.Join(dataDir, "audit.db")
	if cfg.DBPath != expectedDBPath {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, expectedDBPath)
	}

	expectedJSONLPath := filepath.Join(dataDir, "audit.jsonl")
	if cfg.JSONLPath != expectedJSONLPath {
		t.Errorf("JSONLPath = %q, want %q", cfg.JSONLPath, expectedJSONLPath)
	}

	expectedRetention := 90 * 24 * time.Hour
	if cfg.Retention != expectedRetention {
		t.Errorf("Retention = %v, want %v", cfg.Retention, expectedRetention)
	}

	expectedCleanupInterval := 24 * time.Hour
	if cfg.CleanupInterval != expectedCleanupInterval {
		t.Errorf("CleanupInterval = %v, want %v", cfg.CleanupInterval, expectedCleanupInterval)
	}
}

func TestSetAuditAction(t *testing.T) {
	// Create a request with audit context
	req := httptest.NewRequest("POST", "/api/v1/test", nil)

	// Set audit context on the request
	ac := &AuditContext{}
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyAudit, ac))

	// Set action
	SetAuditAction(req, AuditActionApprove)

	if ac.Action != AuditActionApprove {
		t.Errorf("Action = %q, want %q", ac.Action, AuditActionApprove)
	}
}

func TestSetAuditAction_NilContext(t *testing.T) {
	// Create a request without audit context
	req := httptest.NewRequest("GET", "/api/v1/test", nil)

	// Should not panic when context is nil
	SetAuditAction(req, AuditActionExecute)
	// No assertion needed - just verify no panic
}

func TestNewAuditStore_DefaultRetentionValues(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       0,  // Should default to 90 days
		CleanupInterval: -1, // Should default to 24h
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	if store.retention != 90*24*time.Hour {
		t.Errorf("retention = %v, want 90 days", store.retention)
	}
}

func TestNewAuditStore_UsesSingleSQLiteConnection(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	if store.db == nil {
		t.Fatal("expected sqlite db to be configured")
	}

	stats := store.db.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
}

func TestNewAuditStore_DBOnly(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
		// No JSONLPath
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	if store.db == nil {
		t.Error("db should not be nil")
	}
	if store.jsonlFile != nil {
		t.Error("jsonlFile should be nil when no JSONLPath")
	}

	// Recording should still work (just DB, no JSONL)
	rec := &AuditRecord{
		RequestID:  "req-db-only",
		UserID:     "user",
		Role:       RoleViewer,
		Action:     AuditActionExecute,
		Resource:   "test",
		Method:     "GET",
		Path:       "/test",
		StatusCode: 200,
		RemoteAddr: "127.0.0.1",
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record error: %v", err)
	}
	if got := countAuditRows(t, store); got != 1 {
		t.Errorf("expected 1 record, got %d", got)
	}
}

// countAuditRows counts persisted audit records directly; the exported Query
// method was removed as dead code.
func countAuditRows(t *testing.T, store *AuditStore) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM audit_records`).Scan(&n); err != nil {
		t.Fatalf("count audit records: %v", err)
	}
	return n
}

func TestNewAuditStore_JSONLOnly(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
		// No DBPath
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	if store.db != nil {
		t.Error("db should be nil when no DBPath")
	}
	if store.jsonlFile == nil {
		t.Error("jsonlFile should not be nil")
	}

	// Recording should still work (just JSONL, no DB)
	rec := &AuditRecord{
		RequestID:  "req-jsonl-only",
		UserID:     "user",
		Role:       RoleViewer,
		Action:     AuditActionExecute,
		Resource:   "test",
		Method:     "GET",
		Path:       "/test",
		StatusCode: 200,
		RemoteAddr: "127.0.0.1",
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record error: %v", err)
	}

	data, _ := os.ReadFile(cfg.JSONLPath)
	if len(data) == 0 {
		t.Error("JSONL file should have content")
	}
}

func TestAuditStore_CleanupNilDB(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       time.Millisecond,
		CleanupInterval: time.Hour,
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// cleanup with nil db should not panic
	store.cleanup()
}

func TestAuditStore_Close_DBOnly(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}

	// Close with only DB (no JSONL file)
	if err := store.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
}

func TestAuditStore_Close_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("first Close error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close error: %v", err)
	}
}

func TestAuditContextFromRequest_NilContext(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)

	// Should return nil when no audit context is set
	ac := AuditContextFromRequest(req)
	if ac != nil {
		t.Error("Expected nil audit context from request without context")
	}
}

func TestAuditContextFromRequest_WithContext(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	expected := &AuditContext{
		Resource:   "test",
		ResourceID: "123",
	}
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyAudit, expected))

	ac := AuditContextFromRequest(req)
	if ac != expected {
		t.Error("Expected audit context from request")
	}
	if ac.Resource != "test" {
		t.Errorf("Resource = %q, want %q", ac.Resource, "test")
	}
}

// bd-w8uwo: cleanup() returned early when no SQLite DB was configured and only
// ever issued a DELETE against the database, so the JSONL sink was never
// rotated or truncated despite Retention being documented as "how long to keep
// audit records".
func TestAuditStore_RotatesAndPrunesJSONL(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "audit.jsonl")

	store, err := NewAuditStore(AuditStoreConfig{
		JSONLPath: jsonlPath,
		Retention: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Grow the active log past the rotation threshold.
	big := make([]byte, maxJSONLBytes+1024)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(jsonlPath, big, 0644); err != nil {
		t.Fatalf("seed oversized log: %v", err)
	}

	store.cleanupJSONL()

	// The active path must exist again and be small, and a rotated segment
	// must be alongside it.
	info, err := os.Stat(jsonlPath)
	if err != nil {
		t.Fatalf("active audit log missing after rotation: %v", err)
	}
	if info.Size() >= maxJSONLBytes {
		t.Fatalf("active log was not rotated (size=%d)", info.Size())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	rotated := ""
	for _, e := range entries {
		if e.Name() != "audit.jsonl" && strings.HasSuffix(e.Name(), ".jsonl") {
			rotated = e.Name()
		}
	}
	if rotated == "" {
		t.Fatalf("no rotated segment was produced; entries=%v", entries)
	}

	// Auditing must keep working through a rotation.
	if err := store.Record(&AuditRecord{Action: AuditActionCreate, Resource: "sessions"}); err != nil {
		t.Fatalf("Record after rotation: %v", err)
	}
	if store.writeFailures.Load() != 0 {
		t.Fatalf("write failures after rotation = %d, want 0", store.writeFailures.Load())
	}
	after, err := os.ReadFile(jsonlPath)
	if err != nil || len(after) == 0 {
		t.Fatalf("record did not reach the reopened log (len=%d err=%v)", len(after), err)
	}

	// A rotated segment older than the retention window is pruned.
	stale := filepath.Join(dir, rotated)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	store.cleanupJSONL()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("rotated segment past retention survived (err=%v)", err)
	}
	// The active log is never pruned, however old it looks.
	if _, err := os.Stat(jsonlPath); err != nil {
		t.Fatalf("active log was pruned: %v", err)
	}
}

// Unrelated files in the audit directory must not be deleted by pruning.
func TestAuditStore_PruneLeavesUnrelatedFilesAlone(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "audit.jsonl")

	store, err := NewAuditStore(AuditStoreConfig{JSONLPath: jsonlPath, Retention: time.Hour})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	bystander := filepath.Join(dir, "notes.jsonl")
	if err := os.WriteFile(bystander, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write bystander: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(bystander, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	store.cleanupJSONL()

	if _, err := os.Stat(bystander); err != nil {
		t.Fatalf("an unrelated .jsonl file was deleted by audit pruning: %v", err)
	}
}

// Two rotations inside the same second must not collide: the timestamp has
// one-second resolution, so a naive name would make os.Rename OVERWRITE the
// earlier segment and destroy audit records.
func TestRotatedSegmentPath_NeverCollides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	first := rotatedSegmentPath(path, now)
	if err := os.WriteFile(first, []byte("first\n"), 0644); err != nil {
		t.Fatalf("write first segment: %v", err)
	}

	second := rotatedSegmentPath(path, now)
	if second == first {
		t.Fatalf("two rotations in the same second produced the same name %q; the first segment would be overwritten", first)
	}
	if err := os.WriteFile(second, []byte("second\n"), 0644); err != nil {
		t.Fatalf("write second segment: %v", err)
	}

	// The original must still be intact.
	data, err := os.ReadFile(first)
	if err != nil || string(data) != "first\n" {
		t.Fatalf("first segment = (%q, %v), want it untouched", data, err)
	}
}

// Records written concurrently with a rotation must all land somewhere. The
// writer used to snapshot the file handle BEFORE taking appendMu, so a
// rotation could close that handle underneath it and the record was lost.
func TestAuditStore_ConcurrentWritesSurviveRotation(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "audit.jsonl")

	store, err := NewAuditStore(AuditStoreConfig{JSONLPath: jsonlPath, Retention: time.Hour})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const writers = 8
	const perWriter = 25
	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			start.Wait()
			for i := 0; i < perWriter; i++ {
				_ = store.Record(&AuditRecord{
					Action:   AuditActionCreate,
					Resource: "sessions",
					UserID:   fmt.Sprintf("w%d-%d", w, i),
				})
			}
		}(w)
	}

	// Rotate continuously while the writers run. A tiny threshold makes every
	// call rotate, which is the point: maximize the window in which a writer
	// could be holding a handle that rotation is about to close.
	store.maxBytes = 1
	wg.Add(1)
	go func() {
		defer wg.Done()
		start.Wait()
		for i := 0; i < 40; i++ {
			if err := store.rotateJSONLIfLarge(jsonlPath); err != nil {
				t.Errorf("rotate: %v", err)
			}
		}
	}()

	start.Done()
	wg.Wait()

	if got := store.writeFailures.Load(); got != 0 {
		t.Fatalf("write failures = %d, want 0 — records were lost across rotation", got)
	}

	// Every record must be findable across the active log plus all segments.
	total := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) != "" {
				total++
			}
		}
	}
	if want := writers * perWriter; total != want {
		t.Fatalf("found %d audit records across all segments, want %d — records were lost or duplicated by rotation", total, want)
	}
}

// rotatedSegmentPath must terminate even when the directory cannot be read.
// An unbounded search spins forever on a persistent non-NotExist Lstat error —
// while holding both appendMu and mu, which would wedge every request that
// records an audit entry, not just rotation.
func TestRotatedSegmentPath_TerminatesWhenTheDirectoryIsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Remove search permission so Lstat inside it fails with EACCES, not ENOENT.
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	done := make(chan string, 1)
	go func() {
		done <- rotatedSegmentPath(filepath.Join(locked, "audit.jsonl"), time.Now().UTC())
	}()

	select {
	case got := <-done:
		if got == "" {
			t.Fatal("rotatedSegmentPath returned an empty name")
		}
		if !strings.HasSuffix(got, ".jsonl") {
			t.Fatalf("rotatedSegmentPath = %q, want a .jsonl segment name", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rotatedSegmentPath did not terminate on an unreadable directory; it holds appendMu and mu while spinning")
	}
}
