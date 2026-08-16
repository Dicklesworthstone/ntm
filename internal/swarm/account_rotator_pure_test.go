package swarm

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------- parseCAAMAccounts ----------

func TestParseCAAMAccounts_EmptyInput(t *testing.T) {
	t.Parallel()
	accounts, err := parseCAAMAccounts("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("expected 0 accounts, got %d", len(accounts))
	}
}

func TestParseCAAMAccounts_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := parseCAAMAccounts("not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseCAAMAccounts_ArrayFormat(t *testing.T) {
	t.Parallel()
	input := `[{"id":"claude-a","provider":"claude","email":"a@example.com","active":true,"rate_limited":false},{"id":"claude-b","provider":"claude","email":"b@example.com","active":false,"rate_limited":true}]`
	accounts, err := parseCAAMAccounts(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}
	if accounts[0].ID != "claude-a" {
		t.Errorf("accounts[0].ID = %q, want claude-a", accounts[0].ID)
	}
	if !accounts[0].Active {
		t.Error("accounts[0].Active = false, want true")
	}
	if !accounts[1].RateLimited {
		t.Error("accounts[1].RateLimited = false, want true")
	}
}

func TestParseCAAMAccounts_WrapperFormat(t *testing.T) {
	t.Parallel()
	input := `{"accounts":[{"id":"openai-1","provider":"openai","active":true},{"id":"openai-2","provider":"openai","active":false}]}`
	accounts, err := parseCAAMAccounts(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}
	if accounts[0].ID != "openai-1" {
		t.Errorf("accounts[0].ID = %q, want openai-1", accounts[0].ID)
	}
	if accounts[1].Active {
		t.Error("accounts[1].Active = true, want false")
	}
}

func TestParseCAAMAccounts_EmptyArray(t *testing.T) {
	t.Parallel()
	accounts, err := parseCAAMAccounts("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("expected 0 accounts, got %d", len(accounts))
	}
}

func TestParseCAAMAccounts_WrapperEmptyAccounts(t *testing.T) {
	t.Parallel()
	accounts, err := parseCAAMAccounts(`{"accounts":[]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("expected 0 accounts, got %d", len(accounts))
	}
}

func TestParseCAAMAccounts_InvalidWrapperJSON(t *testing.T) {
	t.Parallel()
	// Valid JSON but not an array and not a wrapper object with "accounts" key
	_, err := parseCAAMAccounts(`{"foo":"bar"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------- AccountRotationHistory edge cases ----------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestAccountRotationHistory_RecordRotation_EmptySessionPane(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	err := h.RecordRotation(RotationRecord{
		Provider:    "claude",
		AgentType:   "cc",
		FromAccount: "a",
		ToAccount:   "b",
		RotatedAt:   time.Now(),
		SessionPane: "", // empty — should be a no-op
		TriggeredBy: "limit_hit",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No records should be stored
	h.mu.RLock()
	count := len(h.history)
	h.mu.RUnlock()
	if count != 0 {
		t.Errorf("expected 0 history entries for empty SessionPane, got %d", count)
	}
}

func TestAccountRotationHistory_RecordRotation_ZeroRotatedAt(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	err := h.RecordRotation(RotationRecord{
		Provider:    "claude",
		AgentType:   "cc",
		FromAccount: "a",
		ToAccount:   "b",
		SessionPane: "sess:1.1",
		TriggeredBy: "manual",
		// RotatedAt is zero — should be set to time.Now()
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	records := h.RecordsForPane("sess:1.1", 0)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].RotatedAt.IsZero() {
		t.Error("RotatedAt should have been set to non-zero when zero was provided")
	}
}

func TestAccountRotationHistory_RecordRotation_NilLogger(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", nil)

	// Should not panic even with nil logger
	err := h.RecordRotation(RotationRecord{
		Provider:    "claude",
		AgentType:   "cc",
		FromAccount: "a",
		ToAccount:   "b",
		RotatedAt:   time.Now(),
		SessionPane: "sess:1.1",
		TriggeredBy: "limit_hit",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAccountRotationHistory_RecordsForPane_EmptyHistory(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	records := h.RecordsForPane("nonexistent:1.1", 10)
	if len(records) != 0 {
		t.Fatalf("expected 0 records for nonexistent pane, got %d", len(records))
	}
}

func TestAccountRotationHistory_RecordsForPane_LimitBoundary(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pane := "sess:1.1"

	// Add 5 records
	for i := 0; i < 5; i++ {
		_ = h.RecordRotation(RotationRecord{
			Provider:    "claude",
			AgentType:   "cc",
			FromAccount: "a",
			ToAccount:   "b",
			RotatedAt:   base.Add(time.Duration(i) * time.Minute),
			SessionPane: pane,
			TriggeredBy: "limit_hit",
		})
	}

	// Limit 0 → all records
	all := h.RecordsForPane(pane, 0)
	if len(all) != 5 {
		t.Errorf("limit 0: expected 5 records, got %d", len(all))
	}

	// Negative limit → all records
	neg := h.RecordsForPane(pane, -1)
	if len(neg) != 5 {
		t.Errorf("limit -1: expected 5 records, got %d", len(neg))
	}

	// Limit 3 → last 3 records
	limited := h.RecordsForPane(pane, 3)
	if len(limited) != 3 {
		t.Errorf("limit 3: expected 3 records, got %d", len(limited))
	}

	// Limit exceeds count → all records
	big := h.RecordsForPane(pane, 100)
	if len(big) != 5 {
		t.Errorf("limit 100: expected 5 records, got %d", len(big))
	}
}

func TestAccountRotationHistory_GetRotationStats_NoMatchingAgent(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	_ = h.RecordRotation(RotationRecord{
		Provider:    "claude",
		AgentType:   "cc",
		FromAccount: "a",
		ToAccount:   "b",
		RotatedAt:   time.Now(),
		SessionPane: "sess:1.1",
		TriggeredBy: "limit_hit",
	})

	stats := h.GetRotationStats("gmi")
	if stats.TotalRotations != 0 {
		t.Errorf("TotalRotations = %d, want 0 for non-matching agent type", stats.TotalRotations)
	}
	if stats.UniquePanes != 0 {
		t.Errorf("UniquePanes = %d, want 0", stats.UniquePanes)
	}
}

func TestAccountRotationHistory_GetRotationStats_MultiplePanes(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, pane := range []string{"sess:1.1", "sess:1.2", "sess:2.1"} {
		_ = h.RecordRotation(RotationRecord{
			Provider:    "claude",
			AgentType:   "cc",
			FromAccount: "x",
			ToAccount:   "y",
			RotatedAt:   base.Add(time.Duration(i) * time.Minute),
			SessionPane: pane,
			TriggeredBy: "limit_hit",
		})
	}

	stats := h.GetRotationStats("cc")
	if stats.TotalRotations != 3 {
		t.Errorf("TotalRotations = %d, want 3", stats.TotalRotations)
	}
	if stats.UniquePanes != 3 {
		t.Errorf("UniquePanes = %d, want 3", stats.UniquePanes)
	}
}

// ---------- LoadFromDir / SaveToDir edge cases ----------

func TestAccountRotationHistory_LoadFromDir_EmptyDir(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	// Empty dir param and empty dataDir → no-op
	err := h.LoadFromDir("")
	if err != nil {
		t.Fatalf("unexpected error for empty dir: %v", err)
	}
}

func TestAccountRotationHistory_LoadFromDir_NonExistentFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := NewAccountRotationHistory(dir, discardLogger())

	// No .ntm/rotation_history.json exists → should be no-op
	err := h.LoadFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error for nonexistent history file: %v", err)
	}
}

func TestAccountRotationHistory_LoadFromDir_InvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ntmDir := filepath.Join(dir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ntmDir, "rotation_history.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	h := NewAccountRotationHistory(dir, discardLogger())
	err := h.LoadFromDir(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON history file")
	}
}

func TestAccountRotationHistory_LoadFromDir_NullHistory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ntmDir := filepath.Join(dir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write valid JSON with null history
	if err := os.WriteFile(filepath.Join(ntmDir, "rotation_history.json"), []byte(`{"history":null}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	h := NewAccountRotationHistory(dir, discardLogger())
	err := h.LoadFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should initialize empty history
	h.mu.RLock()
	count := len(h.history)
	h.mu.RUnlock()
	if count != 0 {
		t.Errorf("expected 0 history entries after loading null, got %d", count)
	}
}

func TestAccountRotationHistory_LoadFromDir_UsesDataDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Seed data
	seed := NewAccountRotationHistory(dir, discardLogger())
	_ = seed.RecordRotation(RotationRecord{
		Provider:    "claude",
		AgentType:   "cc",
		FromAccount: "x",
		ToAccount:   "y",
		RotatedAt:   time.Now(),
		SessionPane: "sess:1.1",
		TriggeredBy: "manual",
	})

	// Load with empty dir param → falls back to dataDir
	h := NewAccountRotationHistory(dir, discardLogger())
	err := h.LoadFromDir("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	records := h.RecordsForPane("sess:1.1", 0)
	if len(records) != 1 {
		t.Fatalf("expected 1 record loaded from dataDir, got %d", len(records))
	}
}

func TestAccountRotationHistory_SaveToDir_EmptyDir(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	// Empty dir and empty dataDir → no-op
	err := h.SaveToDir("")
	if err != nil {
		t.Fatalf("unexpected error for empty dir: %v", err)
	}
}

func TestAccountRotationHistory_SaveToDir_UsesDataDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := NewAccountRotationHistory(dir, discardLogger())

	h.mu.Lock()
	h.history["sess:1.1"] = []RotationRecord{{
		Provider:    "claude",
		AgentType:   "cc",
		FromAccount: "a",
		ToAccount:   "b",
		RotatedAt:   time.Now(),
		SessionPane: "sess:1.1",
		TriggeredBy: "manual",
	}}
	h.mu.Unlock()

	// Empty dir param → falls back to dataDir
	err := h.SaveToDir("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := filepath.Join(dir, ".ntm", "rotation_history.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected file to be written: %v", err)
	}

	if !json.Valid(data) {
		t.Fatal("expected valid JSON in saved file")
	}
}

// ---------- AccountRotationHistory WithLogger / SetDataDir ----------

func TestAccountRotationHistory_WithLogger(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	newLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.WithLogger(newLogger)

	h.mu.RLock()
	got := h.logger
	h.mu.RUnlock()
	if got != newLogger {
		t.Error("WithLogger did not update logger")
	}
}

func TestAccountRotationHistory_SetDataDir(t *testing.T) {
	t.Parallel()
	h := NewAccountRotationHistory("", discardLogger())

	h.SetDataDir("/new/dir")

	h.mu.RLock()
	got := h.dataDir
	h.mu.RUnlock()
	if got != "/new/dir" {
		t.Errorf("SetDataDir: dataDir = %q, want /new/dir", got)
	}
}

// ---------- AccountRotator logger helper ----------

func TestAccountRotatorLoggerNilFallback(t *testing.T) {
	t.Parallel()
	rotator := NewAccountRotator()
	rotator.Logger = nil

	l := rotator.logger()
	if l == nil {
		t.Fatal("logger() should return slog.Default() when Logger is nil")
	}
}

// ---------- AccountRotator EnableRotationHistory ----------

func TestAccountRotator_EnableRotationHistory_EmptyDataDir(t *testing.T) {
	t.Parallel()
	rotator := NewAccountRotator()

	err := rotator.EnableRotationHistory("")
	if err == nil {
		t.Fatal("expected error for empty dataDir")
	}
}

func TestAccountRotator_EnableRotationHistory_CreatesStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rotator := NewAccountRotator()
	// Clear the default store to test creation path
	rotator.rotationHistoryStore = nil

	err := rotator.EnableRotationHistory(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rotator.rotationHistoryStore == nil {
		t.Fatal("expected rotationHistoryStore to be created")
	}
}

// ---------- AccountRotator SwitchToAccount with fake caam ----------

func TestAccountRotator_SwitchToAccount_UsesCorrectProvider(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state")
	if err := os.WriteFile(stateFile, []byte("claude-a"), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	caamPath := writeFakeCAAM(t, dir, stateFile)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithLogger(discardLogger())

	record, err := rotator.SwitchToAccount("cc", "claude-b")
	if err != nil {
		t.Fatalf("SwitchToAccount error: %v", err)
	}
	if record.ToAccount != "claude-b" {
		t.Errorf("ToAccount = %q, want claude-b", record.ToAccount)
	}
	if record.TriggeredBy != "manual" {
		t.Errorf("TriggeredBy = %q, want manual", record.TriggeredBy)
	}
	if record.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", record.Provider)
	}

	// Should appear in rotation history
	if rotator.RotationCount() != 1 {
		t.Errorf("RotationCount = %d, want 1", rotator.RotationCount())
	}
}

func TestAccountRotator_SwitchToAccount_CaamUnavailable(t *testing.T) {
	t.Parallel()
	rotator := NewAccountRotator().
		WithCaamPath("/nonexistent/caam").
		WithLogger(discardLogger())

	_, err := rotator.SwitchToAccount("cc", "some-account")
	if err == nil {
		t.Fatal("expected error when caam is unavailable")
	}
}

func TestValidateCaamAccountOperand(t *testing.T) {
	t.Parallel()
	valid := []string{
		"jeff141421@gmail.com", // real caam names are emails
		"_backup_20260501_025439",
		"_original",
		"work account 2", // internal spaces are fine
		"  padded  ",     // plain leading/trailing spaces are fine
	}
	for _, name := range valid {
		if err := validateCaamAccountOperand(name); err != nil {
			t.Errorf("validateCaamAccountOperand(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{
		"",
		"   ",
		"--auto",   // flag-shaped
		"  --auto", // flag-shaped after trim
		"-x",
		"foo\x00bar", // interior control byte
		"foo\x1b[31m",
		"foo\x7f",
		"foo\n", // control byte hiding in trailing whitespace still reaches exec raw
		"\tfoo", // ... or leading whitespace
	}
	for _, name := range invalid {
		if err := validateCaamAccountOperand(name); err == nil {
			t.Errorf("validateCaamAccountOperand(%q) = nil, want error", name)
		}
	}
}
