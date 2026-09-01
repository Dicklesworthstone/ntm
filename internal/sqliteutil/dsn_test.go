package sqliteutil

import (
	"net/url"
	"strings"
	"testing"
)

func TestFileURIPathWindowsAbsoluteDrive(t *testing.T) {
	got := fileURIPath(`C:\Users\ntm user\.config\ntm\state.db`, "windows")
	if want := "/C:/Users/ntm user/.config/ntm/state.db"; got != want {
		t.Fatalf("fileURIPath() = %q, want %q", got, want)
	}
}

func TestFileURIPathWindowsRelativeAndUNC(t *testing.T) {
	tests := map[string]string{
		`state.db`:                "state.db",
		`data\state.db`:           "data/state.db",
		`C:relative\state.db`:     "C:relative/state.db",
		`\\server\share\state.db`: "//server/share/state.db",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			if got := fileURIPath(input, "windows"); got != want {
				t.Fatalf("fileURIPath() = %q, want %q", got, want)
			}
		})
	}
}

func TestFileURIPathNonWindowsPreservesPath(t *testing.T) {
	const input = `C:\literal-backslashes\state.db`
	if got := fileURIPath(input, "linux"); got != input {
		t.Fatalf("fileURIPath() = %q, want %q", got, input)
	}
}

func TestWindowsFileDSNHasNoDriveAuthority(t *testing.T) {
	q := pragmaQuery("busy_timeout(5000)")
	dsn := fileDSNForOS(`C:\Users\ntm user\.config\ntm\state.db`, q, "windows")

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	if parsed.Scheme != "file" || parsed.Host != "" {
		t.Fatalf("DSN scheme/authority = %q/%q, want file with empty authority", parsed.Scheme, parsed.Host)
	}
	if want := "/C:/Users/ntm user/.config/ntm/state.db"; parsed.Path != want {
		t.Fatalf("DSN path = %q, want %q", parsed.Path, want)
	}
	if !strings.Contains(parsed.RawQuery, "_pragma=busy_timeout%285000%29") {
		t.Fatalf("DSN lost pragma: %q", dsn)
	}
}

func TestImmediateTransactionFileDSNRetainsTxLock(t *testing.T) {
	dsn := ImmediateTransactionFileDSN("state.db", "foreign_keys(ON)")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	if got := parsed.Query().Get("_txlock"); got != "immediate" {
		t.Fatalf("_txlock = %q, want immediate", got)
	}
}
