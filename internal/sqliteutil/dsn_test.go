package sqliteutil

import (
	"net/url"
	"testing"
)

// TestSqliteURIPathWindows covers the Windows path shapes: absolute drive
// paths gain a leading slash (empty URI authority), everything else only has
// separators normalized. Regression coverage for native Windows state opens
// failing with `invalid uri authority: C:%5C...`.
func TestSqliteURIPathWindows(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		`C:\Users\ntm user\.config\ntm\state.db`: "/C:/Users/ntm user/.config/ntm/state.db",
		`d:\data\state.db`:                       "/d:/data/state.db",
		`state.db`:                               "state.db",
		`data\state.db`:                          "data/state.db",
		`C:relative\state.db`:                    "C:relative/state.db", // drive-relative: no authority ambiguity
		`\\server\share\state.db`:                "//server/share/state.db",
	}
	for input, want := range tests {
		if got := sqliteURIPath(input, "windows"); got != want {
			t.Errorf("sqliteURIPath(%q, windows) = %q, want %q", input, got, want)
		}
	}
}

// TestSqliteURIPathNonWindowsUntouched: backslash is a legal filename byte on
// POSIX; the path must pass through byte-for-byte.
func TestSqliteURIPathNonWindowsUntouched(t *testing.T) {
	t.Parallel()

	const input = `C:\literal\backslashes.db`
	for _, goos := range []string{"linux", "darwin"} {
		if got := sqliteURIPath(input, goos); got != input {
			t.Errorf("sqliteURIPath(%q, %s) = %q, want untouched", input, goos, got)
		}
	}
}

// TestFileDSNWindowsDriveHasEmptyAuthority proves the rendered DSN keeps the
// drive letter out of the URI authority and preserves pragmas.
func TestFileDSNWindowsDriveHasEmptyAuthority(t *testing.T) {
	t.Parallel()

	dsn := fileDSN(`C:\Users\ntm user\.config\ntm\state.db`, pragmaQuery("busy_timeout(5000)"), "windows")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse %q: %v", dsn, err)
	}
	if parsed.Scheme != "file" || parsed.Host != "" {
		t.Fatalf("scheme/authority = %q/%q, want file with empty authority (dsn %q)", parsed.Scheme, parsed.Host, dsn)
	}
	if want := "/C:/Users/ntm user/.config/ntm/state.db"; parsed.Path != want {
		t.Fatalf("path = %q, want %q", parsed.Path, want)
	}
	if got := parsed.Query().Get("_pragma"); got != "busy_timeout(5000)" {
		t.Fatalf("pragma = %q, want busy_timeout(5000)", got)
	}
}

// TestImmediateTransactionFileDSNKeepsTxLock guards the refactor: the shared
// fileDSN helper must not drop the _txlock=immediate marker.
func TestImmediateTransactionFileDSNKeepsTxLock(t *testing.T) {
	t.Parallel()

	parsed, err := url.Parse(ImmediateTransactionFileDSN("state.db", "foreign_keys(ON)"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := parsed.Query().Get("_txlock"); got != "immediate" {
		t.Fatalf("_txlock = %q, want immediate", got)
	}
}
