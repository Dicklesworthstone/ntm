package agentmail

import (
	"crypto/sha1" // nolint:gosec // Not cryptographic; path namespace only.
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The contract must match the mcp-agent-mail Rust reference implementation in
// `crates/mcp-agent-mail-core/src/pane_identity.rs`. These tests lock down
// the canonical format so any future divergence fails loudly.

func TestCanonicalIdentityPathMatchesRustReference(t *testing.T) {
	// Force the config base dir. os.UserConfigDir honours XDG_CONFIG_HOME on
	// Linux but derives from $HOME on macOS ($HOME/Library/Application Support),
	// so we set both and resolve the expected base via os.UserConfigDir to
	// stay portable. Cannot use t.Parallel() alongside t.Setenv.
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}

	projectKey := "/data/projects/backend"
	paneID := "%3"

	got := CanonicalIdentityPath(projectKey, paneID)

	// Compute expected sha1[:12] independently to catch any drift.
	h := sha1.Sum([]byte(projectKey))
	expectedHash := hex.EncodeToString(h[:])[:12]
	expected := filepath.Join(base, "agent-mail", "identity", expectedHash, "3")

	if got != expected {
		t.Fatalf("canonical path mismatch:\n got: %s\nwant: %s", got, expected)
	}
}

func TestCanonicalIdentityPathCompositePaneKey(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	got := CanonicalIdentityPath("/p", "main:0:2")
	if !strings.HasSuffix(got, "/main-0-2") {
		t.Fatalf("expected composite pane key %q to become 'main-0-2', got: %s", "main:0:2", got)
	}
}

func TestSanitizePaneID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"%0", "0"},
		{"%123", "123"},
		{"42", "42"},
		{"%foo/bar", "foo_bar"},
		{"", "unknown"},
		{"%", "unknown"},
		{"main:0:2", "main-0-2"},
		{"my_session:1:0", "my_session-1-0"},
	}
	for _, c := range cases {
		if got := sanitizePaneID(c.in); got != c.want {
			t.Errorf("sanitizePaneID(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestWriteIdentityAtomicRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	projectKey := "/a/b/c"
	paneID := "%42"
	path, err := WriteIdentity(projectKey, paneID, "BlueLake  \n")
	if err != nil {
		t.Fatalf("WriteIdentity: %v", err)
	}
	// File must exist and contain the trimmed name plus a trailing newline.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "BlueLake\n" {
		t.Fatalf("unexpected content: %q", string(data))
	}

	name, foundPath := ResolveIdentity(projectKey, paneID)
	if name != "BlueLake" {
		t.Fatalf("expected BlueLake, got %q (path=%s)", name, foundPath)
	}
	if foundPath != path {
		t.Fatalf("expected ResolveIdentity to return canonical path %s, got %s", path, foundPath)
	}
}

func TestWriteIdentityRejectsEmptyName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	if _, err := WriteIdentity("/p", "%0", "   "); err == nil {
		t.Fatal("expected error for empty/whitespace agent name")
	}
}

func TestResolveIdentityLegacyPathsInPriorityOrder(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	projectKey := "/test/proj"
	paneID := "%9"

	// Write the OLD NTM sha256/state-dir file that ntm <=1.13 used. This
	// must be read correctly so upgraders do not lose their state.
	oldPath := oldNtmStatePath(projectKey, paneID)
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(oldPath, []byte("LegacyName\n"), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	name, foundPath := ResolveIdentity(projectKey, paneID)
	if name != "LegacyName" {
		t.Fatalf("expected legacy name LegacyName, got %q (path=%s)", name, foundPath)
	}
	if foundPath != oldPath {
		t.Fatalf("expected legacy state path %s, got %s", oldPath, foundPath)
	}
}

func TestMigrateLegacyIdentityIfNeeded(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	projectKey := "/migrate/proj"
	paneID := "%5"

	// Seed a legacy NTM state file; canonical location must not exist yet.
	oldPath := oldNtmStatePath(projectKey, paneID)
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(oldPath, []byte("GreenOwl\n"), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	got := MigrateLegacyIdentityIfNeeded(projectKey, paneID)
	if got != "GreenOwl" {
		t.Fatalf("expected GreenOwl, got %q", got)
	}

	// After migration the canonical path must exist with the same content.
	canonical := CanonicalIdentityPath(projectKey, paneID)
	data, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("canonical read: %v", err)
	}
	if strings.TrimSpace(string(data)) != "GreenOwl" {
		t.Fatalf("canonical content mismatch: %q", string(data))
	}
}

func TestResolveIdentityReturnsEmptyWhenMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	name, path := ResolveIdentity("/nowhere", "%0")
	if name != "" || path != "" {
		t.Fatalf("expected empty result, got name=%q path=%q", name, path)
	}
}

// Guarantees #107 regression: ntm-written identities must be discoverable at
// the exact path the mcp-agent-mail Rust reference computes. We recompute
// the reference path independently (sha1[:12] of project_key under
// `~/.config/agent-mail/identity/<hash>/<sanitized>`) and assert byte
// equality with CanonicalIdentityPath. If this ever drifts — e.g. someone
// swaps sha1 for sha256, or adds a `.` before the pane id — this test
// fails loudly.
func TestRustContractCompatibility(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}

	type vec struct {
		projectKey string
		paneID     string
		want       string
	}
	cases := []vec{
		{"/project/alpha", "%0", "0"},
		{"/project/beta", "%42", "42"},
		{"/project/gamma", "main:0:2", "main-0-2"},
	}

	for _, c := range cases {
		got := CanonicalIdentityPath(c.projectKey, c.paneID)
		h := sha1.Sum([]byte(c.projectKey))
		expectedHash := hex.EncodeToString(h[:])[:12]
		expected := filepath.Join(base, "agent-mail", "identity", expectedHash, c.want)
		if got != expected {
			t.Fatalf("Rust contract drift for (%q, %q):\n got: %s\nwant: %s",
				c.projectKey, c.paneID, got, expected)
		}

		// Round-trip: the name we write must be readable by ResolveIdentity
		// at the same path.
		agentName := fmt.Sprintf("agent-%s", c.want)
		if _, err := WriteIdentity(c.projectKey, c.paneID, agentName); err != nil {
			t.Fatalf("WriteIdentity: %v", err)
		}
		resolved, path := ResolveIdentity(c.projectKey, c.paneID)
		if resolved != agentName {
			t.Fatalf("resolve mismatch: got %q want %q", resolved, agentName)
		}
		if path != expected {
			t.Fatalf("resolve path mismatch: got %q want %q", path, expected)
		}
	}
}
