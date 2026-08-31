package agentmail

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
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

func TestWriteIdentityRefusesCompositePaneAddress(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	if _, err := WriteIdentity("/p", "main:0:2", "BlueLake"); err == nil {
		t.Fatal("composite pane address must be refused")
	}
}

func TestVerifiedGenerationReceiptAndMirror(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	record := PaneIdentityRecord{Name: "BlueLake", SessionName: "main", PaneID: "%42", PanePID: 123, SocketPath: "/tmp/tmux.sock", WrittenAt: "2026-08-31T00:00:00Z"}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := CanonicalIdentityPath("/source", "%42")
	if err := writeIdentityFile(path, string(data)); err != nil {
		t.Fatal(err)
	}
	_, verified, err := ReadVerifiedIdentity("/source", "%42", "BlueLake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MirrorVerifiedIdentity("/worktree", "%42", verified); err != nil {
		t.Fatal(err)
	}
	mirrored, err := os.ReadFile(CanonicalIdentityPath("/worktree", "%42"))
	if err != nil || string(mirrored) != string(data) {
		t.Fatalf("mirror changed receipt: err=%v got=%s", err, mirrored)
	}
}

func TestPlainNameIsNotGenerationEvidence(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	path := CanonicalIdentityPath("/p", "%42")
	if err := writeIdentityFile(path, "BlueLake\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadVerifiedIdentity("/p", "%42", "BlueLake"); err == nil {
		t.Fatal("plain-name compatibility file must not satisfy generation proof")
	}
}

func TestResolveIdentityIgnoresCanonicalSymlink(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	projectKey := "/symlink/read"
	paneID := "%11"
	path := CanonicalIdentityPath(projectKey, paneID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside-identity")
	if err := os.WriteFile(outsidePath, []byte("SpoofedName\n"), 0o600); err != nil {
		t.Fatalf("write outside identity: %v", err)
	}
	if err := os.Symlink(outsidePath, path); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	name, foundPath := ResolveIdentity(projectKey, paneID)
	if name != "" || foundPath != "" {
		t.Fatalf("expected symlinked identity to be ignored, got name=%q path=%q", name, foundPath)
	}
}

func TestWriteLegacyCompatIdentityDoesNotOverwriteSymlinkTarget(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "agent-mail-name.test.legacy-symlink")

	outsidePath := filepath.Join(t.TempDir(), "outside-identity")
	outsideContent := []byte("OutsideName\n")
	if err := os.WriteFile(outsidePath, outsideContent, 0o600); err != nil {
		t.Fatalf("write outside identity: %v", err)
	}
	if err := os.Symlink(outsidePath, legacyPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	writtenPath := writeLegacyCompatIdentityAtPath(legacyPath, "BlueLake")
	if writtenPath != legacyPath {
		t.Fatalf("legacy path = %q, want %q", writtenPath, legacyPath)
	}
	outside, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatalf("read outside identity: %v", err)
	}
	if string(outside) != string(outsideContent) {
		t.Fatalf("outside identity was overwritten: got %q, want %q", string(outside), string(outsideContent))
	}
	info, err := os.Lstat(legacyPath)
	if err != nil {
		t.Fatalf("lstat legacy identity: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("legacy identity path is still a symlink")
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy identity: %v", err)
	}
	if string(data) != "BlueLake\n" {
		t.Fatalf("legacy identity content = %q, want %q", string(data), "BlueLake\n")
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

// Guarantees #107 regression: bare-pane identities must be discoverable at
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

// The current mcp-agent-mail reference implementation stores a structured
// PaneIdentityRecord JSON object in the canonical identity file (its GH#252
// binding-liveness work); older writers stored a bare name. Resolving must
// decode the structured form to its "name" field — returning the raw blob
// made an entire JSON object the Beads assignee and Agent Mail actor for the
// pane (PR #275 finding 5).
func TestResolveIdentity_StructuredPaneBindingDecodesToName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	projectKey := "/structured/binding"
	paneID := "%25"
	path := CanonicalIdentityPath(projectKey, paneID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	record := `{"name":"BlueLake","session_name":"proj","pane_id":"%25","pane_pid":4242,` +
		`"socket_path":"/tmp/tmux-1000/default","written_at":"2026-08-30T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(record+"\n"), 0o600); err != nil {
		t.Fatalf("write structured identity: %v", err)
	}

	name, foundPath := ResolveIdentity(projectKey, paneID)
	if name != "BlueLake" {
		t.Fatalf("ResolveIdentity name = %q, want BlueLake (a raw JSON blob must never be an identity)", name)
	}
	if foundPath != path {
		t.Fatalf("ResolveIdentity path = %q, want %q", foundPath, path)
	}
}

// Malformed or name-less JSON must fail closed: falling through to "use the
// raw contents" would resurrect the JSON-blob-as-assignee bug for exactly the
// files most likely to be corrupt.
func TestResolveIdentity_MalformedOrNamelessJSONFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"truncated object", `{"name":"Blue`},
		{"object without name", `{"pane_id":"%25"}`},
		{"object with empty name", `{"name":"   "}`},
		{"array", `["BlueLake"]`},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tmp)
			t.Setenv("HOME", tmp)

			projectKey := fmt.Sprintf("/malformed/binding/%d", i)
			paneID := "%26"
			path := CanonicalIdentityPath(projectKey, paneID)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("mkdir identity dir: %v", err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write identity: %v", err)
			}
			if name, foundPath := ResolveIdentity(projectKey, paneID); name != "" || foundPath != "" {
				t.Fatalf("ResolveIdentity = (%q, %q), want no identity for %s", name, foundPath, tc.name)
			}
		})
	}
}

// Plain-text legacy identities must keep resolving exactly as before.
func TestResolveIdentity_PlainTextStillResolves(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	projectKey := "/plain/legacy"
	paneID := "%27"
	path := CanonicalIdentityPath(projectKey, paneID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("SnowyOwl\n"), 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}
	if name, _ := ResolveIdentity(projectKey, paneID); name != "SnowyOwl" {
		t.Fatalf("ResolveIdentity = %q, want SnowyOwl", name)
	}
}
