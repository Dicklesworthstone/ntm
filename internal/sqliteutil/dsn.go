package sqliteutil

import (
	"net/url"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

const DriverName = "sqlite"

func FileDSN(path string, pragmas ...string) string {
	q := pragmaQuery(pragmas...)
	return fileDSN(path, q)
}

// ImmediateTransactionFileDSN acquires SQLite's write reservation when a
// transaction begins. Use it for read-check-write compare-and-set operations
// whose preconditions must not change between the read and update statements.
func ImmediateTransactionFileDSN(path string, pragmas ...string) string {
	q := pragmaQuery(pragmas...)
	q.Set("_txlock", "immediate")
	return fileDSN(path, q)
}

func fileDSN(path string, query url.Values) string {
	return fileDSNForOS(path, query, runtime.GOOS)
}

func fileDSNForOS(path string, query url.Values, goos string) string {
	return (&url.URL{Scheme: "file", Path: fileURIPath(path, goos), RawQuery: query.Encode()}).String()
}

func fileURIPath(path, goos string) string {
	if goos != "windows" {
		return path
	}

	path = strings.ReplaceAll(path, `\`, "/")
	if len(path) >= 3 && isASCIIAlpha(path[0]) && path[1] == ':' && path[2] == '/' {
		return "/" + path
	}
	return path
}

func isASCIIAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
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
