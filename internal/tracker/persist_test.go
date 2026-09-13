package tracker

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// TestMain installs a fake ledger for the whole package.
//
// persistChanges and durableChangesSince open state.Open(""), which is the
// user's real ~/.config/ntm/state.db. A test run must never write there, so the
// default backend is replaced before any test runs and individual tests opt into
// a real store explicitly.
func TestMain(m *testing.M) {
	fake := newFakeBackend()
	openBackend = func() (fileChangeBackend, func(), error) { return fake, nil, nil }
	os.Exit(m.Run())
}

// errFakeBackend stands in for a ledger that cannot be reached.
var errFakeBackend = errors.New("fake ledger unavailable")

// fakeBackend is an in-memory stand-in for the state.db ledger.
type fakeBackend struct {
	mu        sync.Mutex
	rows      []state.FileChangeRow
	appendErr error
	queryErr  error
}

func newFakeBackend() *fakeBackend { return &fakeBackend{} }

func (f *fakeBackend) AppendFileChanges(rows []state.FileChangeRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	f.rows = append(f.rows, rows...)
	return nil
}

func (f *fakeBackend) FileChangesSince(projectDir string, since time.Time) ([]state.FileChangeRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	var out []state.FileChangeRow
	for _, row := range f.rows {
		if row.ProjectDir == projectDir && row.CreatedAt.After(since) {
			out = append(out, row)
		}
	}
	return out, nil
}

// installFakeBackend points the package at a fresh fake for one test.
func installFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	previous := openBackend
	fake := newFakeBackend()
	openBackend = func() (fileChangeBackend, func(), error) { return fake, nil, nil }
	t.Cleanup(func() { openBackend = previous })
	return fake
}

// The whole point of the ledger: what one process records, another can read.
func TestRecordedChangesCrossProcessViaLedger(t *testing.T) {
	fake := installFakeBackend(t)
	repo := newGitRepo(t)

	// Stand in the project so the reader resolves the same key the writer used.
	chdir(t, repo)

	writer := NewGitRecorder(GitRecorderConfig{
		Root: repo, ProjectDir: repo, Session: "sess", Identity: "cc-1",
		Store: NewFileChangeStore(50),
	})
	sample(t, writer)
	writeFile(t, repo, "tracked.txt", "changed by another process")
	sample(t, writer)

	if len(fake.rows) == 0 {
		t.Fatal("recorder wrote nothing to the durable ledger")
	}

	// A reader with an empty in-memory ring must still see the change.
	GlobalFileChanges = NewFileChangeStore(500)
	changes := RecordedChangesSince(time.Now().Add(-time.Hour))
	if len(changes) == 0 {
		t.Fatal("reader with an empty in-memory ring saw no changes; the ledger did not carry them")
	}
	if changes[0].Change.Path != "tracked.txt" {
		t.Errorf("path = %q, want tracked.txt", changes[0].Change.Path)
	}
	if len(changes[0].Agents) != 1 || changes[0].Agents[0] != "cc-1" {
		t.Errorf("agents = %v, want [cc-1]", changes[0].Agents)
	}
}

// state.db is machine-wide, so a reader must never be shown another repo's rows.
func TestLedgerIsProjectScoped(t *testing.T) {
	fake := installFakeBackend(t)
	other := t.TempDir()
	fake.rows = append(fake.rows, state.FileChangeRow{
		ProjectDir: NormalizeProjectDir(other),
		Session:    "other-sess",
		Agent:      "cc-9",
		Path:       "somewhere/else.go",
		ChangeType: string(FileModified),
		CreatedAt:  time.Now(),
	})

	repo := newGitRepo(t)
	chdir(t, repo)

	GlobalFileChanges = NewFileChangeStore(500)
	if changes := RecordedChangesSince(time.Now().Add(-time.Hour)); len(changes) != 0 {
		t.Errorf("reader in one project saw %d changes from another: %+v", len(changes), changes)
	}
}

// Writers key the ledger from a session manifest and readers from the working
// directory; on macOS those differ by /var vs /private/var for the same
// directory, and an unnormalized key would silently match nothing.
func TestNormalizeProjectDirResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got, want := NormalizeProjectDir(link), NormalizeProjectDir(real); got != want {
		t.Errorf("NormalizeProjectDir(symlink) = %q, want %q", got, want)
	}
	if NormalizeProjectDir("") != "" {
		t.Error("empty input should stay empty")
	}
}

// Recording is telemetry: a ledger that cannot be written must surface an error
// to the caller, which logs and carries on, never taking down the monitor.
func TestSampleReportsLedgerFailure(t *testing.T) {
	fake := installFakeBackend(t)
	fake.appendErr = errFakeBackend

	repo := newGitRepo(t)
	r := NewGitRecorder(GitRecorderConfig{
		Root: repo, ProjectDir: repo, Session: "sess", Identity: "cc-1",
		Store: NewFileChangeStore(50),
	})
	if _, err := r.Sample(t.Context()); err != nil {
		t.Fatalf("baseline sample: %v", err)
	}
	writeFile(t, repo, "tracked.txt", "changed")
	if _, err := r.Sample(t.Context()); err == nil {
		t.Error("a failing ledger write should be reported to the caller")
	}
}

// A reader whose ledger is unavailable falls back to whatever this process holds
// rather than erroring at the user.
func TestReaderFallsBackToInMemoryRing(t *testing.T) {
	fake := installFakeBackend(t)
	fake.queryErr = errFakeBackend

	GlobalFileChanges = NewFileChangeStore(500)
	GlobalFileChanges.Add(RecordedFileChange{
		Timestamp: time.Now(),
		Session:   "sess",
		Agents:    []string{"cc-1"},
		Change:    FileChange{Path: "in/memory.go", Type: FileModified},
	})

	changes := RecordedChangesSince(time.Now().Add(-time.Hour))
	if len(changes) != 1 || changes[0].Change.Path != "in/memory.go" {
		t.Errorf("expected the in-memory fallback, got %+v", changes)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}
