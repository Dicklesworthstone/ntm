package sqliteutil

import (
	"net/url"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

const DriverName = "sqlite"

func FileDSN(path string, pragmas ...string) string {
	return fileDSN(path, pragmaQuery(pragmas...), runtime.GOOS)
}

// ImmediateTransactionFileDSN acquires SQLite's write reservation when a
// transaction begins. Use it for read-check-write compare-and-set operations
// whose preconditions must not change between the read and update statements.
func ImmediateTransactionFileDSN(path string, pragmas ...string) string {
	q := pragmaQuery(pragmas...)
	q.Set("_txlock", "immediate")
	return fileDSN(path, q, runtime.GOOS)
}

// fileDSN builds a file: URI DSN for the given database path. goos is
// threaded through (rather than read inline) so tests can exercise the
// Windows path shape from any host.
func fileDSN(path string, q url.Values, goos string) string {
	return (&url.URL{Scheme: "file", Path: sqliteURIPath(path, goos), RawQuery: q.Encode()}).String()
}

// sqliteURIPath converts an OS path into the path component of an SQLite
// file: URI. On Windows an absolute drive path like C:\Users\me\state.db must
// become /C:/Users/me/state.db (empty authority, leading slash): handing the
// raw path to url.URL would either put the drive letter where a URI authority
// belongs or keep backslashes, and the driver rejects both (seen in the field
// as `invalid uri authority: C:%5C...`). Non-Windows paths pass through
// untouched — a backslash is a legal filename byte there.
func sqliteURIPath(path, goos string) string {
	if goos != "windows" {
		return path
	}
	path = strings.ReplaceAll(path, `\`, "/")
	if len(path) >= 3 && path[1] == ':' && path[2] == '/' && isDriveLetter(path[0]) {
		return "/" + path
	}
	return path
}

func isDriveLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func MemoryDSN(pragmas ...string) string {
	q := pragmaQuery(pragmas...)
	if encoded := q.Encode(); encoded != "" {
		return "file::memory:?" + encoded
	}
	return "file::memory:"
}

func pragmaQuery(pragmas ...string) url.Values {
	q := url.Values{}
	q.Set("_time_format", "sqlite")
	q.Set("_timezone", "UTC")
	for _, pragma := range pragmas {
		pragma = strings.TrimSpace(pragma)
		if pragma == "" {
			continue
		}
		q.Add("_pragma", pragma)
	}
	return q
}
