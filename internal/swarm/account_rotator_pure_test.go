package swarm

import (
	"io"
	"log/slog"
	"testing"
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

// ---------- LoadFromDir / SaveToDir edge cases ----------

// ---------- AccountRotationHistory WithLogger / SetDataDir ----------

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

// ---------- AccountRotator SwitchToAccount with fake caam ----------

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
