package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTempXDGRuntimeDir points the prepared-redaction store at a
// per-test temp directory so the test never touches the real
// $XDG_RUNTIME_DIR/ntm tree (which is shared across CLI invocations).
func withTempXDGRuntimeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	return dir
}

func TestPreparedRedactionRoundTrip_AcrossProcessSimulation(t *testing.T) {
	withTempXDGRuntimeDir(t)

	const raw = "super-secret-token-abcdef"
	const redacted = "[REDACTED]"
	findings := []RedactPreviewFinding{{Category: "test", Redacted: redacted}}

	handle, err := stashPreparedRedaction(raw, redacted, findings)
	if err != nil {
		t.Fatalf("stash: %v", err)
	}
	if !strings.HasPrefix(handle, "rh_") || len(handle) != 3+32 {
		t.Fatalf("handle shape: %q", handle)
	}

	// Cross-process simulation: stash and consume go through disk.
	// A fresh call to consumePreparedRedaction reads the file written
	// by stash, NOT any in-process map. This is the regression test
	// for ntm#126's broken in-process-only design.
	gotRaw, gotRedacted, gotFindings, err := consumePreparedRedaction(handle)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if gotRaw != raw {
		t.Errorf("raw mismatch: got %q, want %q", gotRaw, raw)
	}
	if gotRedacted != redacted {
		t.Errorf("redacted mismatch: got %q, want %q", gotRedacted, redacted)
	}
	if len(gotFindings) != 1 || gotFindings[0].Category != "test" {
		t.Errorf("findings mismatch: got %+v", gotFindings)
	}
}

func TestPreparedRedactionIsConsumedOnce(t *testing.T) {
	withTempXDGRuntimeDir(t)
	handle, err := stashPreparedRedaction("x", "[REDACTED]", nil)
	if err != nil {
		t.Fatalf("stash: %v", err)
	}
	if _, _, _, err := consumePreparedRedaction(handle); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	_, _, _, err = consumePreparedRedaction(handle)
	if err == nil {
		t.Fatalf("second consume should fail; the handle has been used")
	}
}

func TestPreparedRedactionExpired(t *testing.T) {
	dir := withTempXDGRuntimeDir(t)
	handle, err := stashPreparedRedaction("x", "[REDACTED]", nil)
	if err != nil {
		t.Fatalf("stash: %v", err)
	}
	// Backdate the file past the TTL by rewriting it with a stale
	// CreatedAt. We can't rely on os.Chtimes here because TTL is
	// computed against payload.CreatedAt, not file mtime.
	path := filepath.Join(dir, "ntm", "redaction-handles", handle+".json")
	stale := []byte(`{"raw":"x","redacted_body":"[REDACTED]","findings":null,"created_at":"2020-01-01T00:00:00Z"}`)
	if err := os.WriteFile(path, stale, 0o600); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	_, _, _, err = consumePreparedRedaction(handle)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got %v", err)
	}
	// And the stale file must be gone after the failed consume.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("stale handle file should be cleaned up; stat err: %v", statErr)
	}
}

func TestPreparedRedactionHandleValidation(t *testing.T) {
	withTempXDGRuntimeDir(t)
	cases := []struct {
		in      string
		wantErr string
	}{
		{"", "invalid"},
		{"rh_", "invalid"},
		{"rh_xxx", "invalid"},                        // too short
		{"rh_" + strings.Repeat("g", 32), "invalid"}, // non-hex
		{"rh_" + strings.Repeat("a", 31), "invalid"}, // wrong length
		{"rh_../../etc/passwd", "invalid"},           // traversal attempt
		{"rh_" + strings.Repeat("a", 32) + "/../../../etc/passwd", "invalid"}, // traversal w/ valid prefix
		{"rh_" + strings.Repeat("0", 32), "not found"},                        // well-formed but no file
	}
	for _, c := range cases {
		_, _, _, err := consumePreparedRedaction(c.in)
		if err == nil {
			t.Errorf("consumePreparedRedaction(%q): expected error containing %q, got nil", c.in, c.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("consumePreparedRedaction(%q): want error containing %q, got %v", c.in, c.wantErr, err)
		}
	}
}

func TestPreparedRedactionSweepRemovesStaleEntries(t *testing.T) {
	dir := withTempXDGRuntimeDir(t)
	store := filepath.Join(dir, "ntm", "redaction-handles")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Plant a stale file with old mtime.
	stalePath := filepath.Join(store, "rh_"+strings.Repeat("1", 32)+".json")
	if err := os.WriteFile(stalePath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	old := time.Now().Add(-preparedRedactionTTL - time.Hour)
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// Trigger sweep via a fresh stash.
	if _, err := stashPreparedRedaction("y", "[REDACTED]", nil); err != nil {
		t.Fatalf("stash: %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("stale file should have been swept; stat err: %v", err)
	}
}
