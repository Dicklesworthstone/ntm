package tracker

// persist.go — durable backing for the file change ledger.
//
// GlobalFileChanges is a per-process ring buffer. The process that observes
// changes is the session monitor, and every surface that reports them —
// `ntm changes`, `ntm conflicts`, --robot-status, the dashboard Files panel,
// the work-coordination adapter — is a different process, so an in-memory ring
// could never carry a single change from the observer to a reader. These
// helpers put the ledger in the shared state.db that every ntm process already
// opens, keeping the in-memory ring as the same-process fallback.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// fileChangeBackend is the slice of *state.Store this package needs, kept as an
// interface so tests can exercise the ledger without touching the user's real
// state.db.
type fileChangeBackend interface {
	AppendFileChanges([]state.FileChangeRow) error
	FileChangesSince(projectDir string, since time.Time) ([]state.FileChangeRow, error)
}

// realOpenBackend opens the shared state store, following this codebase's
// open-on-demand idiom for state.db rather than a wired-in singleton — there is
// nothing to forget to wire, which is how the readers came to be reading an
// empty store in the first place.
//
// Migrate runs on every open rather than behind a sync.Once: it is idempotent
// and cheap, and a process-wide latch would skip migrating any store but the
// first one it happened to see.
func realOpenBackend() (fileChangeBackend, func(), error) {
	store, err := state.Open("")
	if err != nil {
		return nil, nil, err
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return store, func() { _ = store.Close() }, nil
}

// openBackend is the indirection tests replace so a test run never writes to the
// user's real ~/.config/ntm/state.db.
var openBackend = realOpenBackend

// NormalizeProjectDir resolves a project directory to the single spelling the
// ledger is keyed by.
//
// Writers know the project from a session manifest while readers derive it from
// the working directory, and the two must agree exactly or every lookup misses.
// Symlink resolution is the part that bites: on macOS a manifest recorded under
// /var/... and a reader standing in /private/var/... name the same directory.
func NormalizeProjectDir(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs)
}

// projectDirCache memoizes the working-directory resolution. It runs a
// `git rev-parse` subprocess, and callers on a refresh loop would otherwise pay
// for one on every tick; the answer only changes if the process chdirs, so the
// cache is keyed by the working directory rather than held as a single value.
var (
	projectDirCacheMu sync.Mutex
	projectDirCache   = map[string]string{}
)

// CurrentProjectDir resolves the project a reader is standing in: the git
// working tree root, or the working directory when that is not a repository.
func CurrentProjectDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	projectDirCacheMu.Lock()
	cached, ok := projectDirCache[cwd]
	projectDirCacheMu.Unlock()
	if ok {
		return cached
	}

	resolved := resolveProjectDir(cwd)

	projectDirCacheMu.Lock()
	projectDirCache[cwd] = resolved
	projectDirCacheMu.Unlock()
	return resolved
}

func resolveProjectDir(cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	if out, err := cmd.Output(); err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return NormalizeProjectDir(root)
		}
	}
	return NormalizeProjectDir(cwd)
}

// persistChanges writes a batch to the durable ledger. Failures are reported to
// the caller, which treats recording as best-effort telemetry.
func persistChanges(projectDir string, entries []RecordedFileChange) error {
	if projectDir == "" || len(entries) == 0 {
		return nil
	}
	backend, closeBackend, err := openBackend()
	if err != nil {
		return err
	}
	if closeBackend != nil {
		defer closeBackend()
	}

	rows := make([]state.FileChangeRow, 0, len(entries))
	for _, entry := range entries {
		agent := ""
		if len(entry.Agents) > 0 {
			agent = entry.Agents[0]
		}
		rows = append(rows, state.FileChangeRow{
			ProjectDir: projectDir,
			Session:    entry.Session,
			Agent:      agent,
			Path:       entry.Change.Path,
			ChangeType: string(entry.Change.Type),
			CreatedAt:  entry.Timestamp,
		})
	}
	return backend.AppendFileChanges(rows)
}

// durableChangesSince reads the ledger for the project the caller stands in.
//
// The stat signatures (FileState before/after) are deliberately not persisted:
// they exist only so the recorder can tell a second edit from a file that
// merely stayed dirty, and no reader consumes them.
func durableChangesForProject(projectDir string, since time.Time) ([]RecordedFileChange, bool) {
	projectDir = NormalizeProjectDir(projectDir)
	if projectDir == "" {
		return nil, false
	}
	backend, closeBackend, err := openBackend()
	if err != nil {
		return nil, false
	}
	if closeBackend != nil {
		defer closeBackend()
	}

	rows, err := backend.FileChangesSince(projectDir, since)
	if err != nil {
		return nil, false
	}

	changes := make([]RecordedFileChange, 0, len(rows))
	for _, row := range rows {
		entry := RecordedFileChange{
			Timestamp: row.CreatedAt,
			Session:   row.Session,
			Change: FileChange{
				Path: row.Path,
				Type: FileChangeType(row.ChangeType),
			},
		}
		if row.Agent != "" {
			entry.Agents = []string{row.Agent}
		}
		changes = append(changes, entry)
	}
	return changes, true
}
