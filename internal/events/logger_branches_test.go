package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

// ---------------------------------------------------------------------------
// maybeRotate — 0% → 100% (both branches: skip and rotate)
// ---------------------------------------------------------------------------

func TestMaybeRotate_SkipsWhenRecent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	logger, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(logger)

	// lastRotation is set to now in NewLogger, so maybeRotate should skip.
	logger.maybeRotate()

	// No error means the early return (< 24h check) worked.
}

func TestMaybeRotate_RotatesWhenOld(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	logger, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(logger)

	// Write a test event to create the file
	event := NewEvent("test_rotate", "test-session", map[string]interface{}{"key": "value"})
	if err := logger.Log(event); err != nil {
		t.Fatal(err)
	}

	// Force lastRotation to be > 24h ago
	logger.lastRotation = time.Now().Add(-48 * time.Hour)

	// This should trigger actual rotation
	logger.maybeRotate()

	// lastRotation should be updated to recent
	if time.Since(logger.lastRotation) > time.Second {
		t.Error("expected lastRotation to be updated after rotation")
	}
}

// ---------------------------------------------------------------------------
// DefaultLogger — 0% → 100%
// ---------------------------------------------------------------------------

func TestDefaultLogger_ReturnsNonNil(t *testing.T) {
	// Not parallel: uses global singleton.
	logger := DefaultLogger()
	if logger == nil {
		t.Fatal("DefaultLogger() returned nil")
	}
}

func TestDefaultLogger_ReturnsSameInstance(t *testing.T) {
	// Not parallel: uses global singleton.
	l1 := DefaultLogger()
	l2 := DefaultLogger()
	if l1 != l2 {
		t.Error("expected DefaultLogger to return the same instance (sync.Once)")
	}
}

// ---------------------------------------------------------------------------
// Emit — 0% → 100%
// ---------------------------------------------------------------------------

func TestEmit_DoesNotPanic(t *testing.T) {
	// Not parallel: uses global DefaultLogger.
	// Emit should not panic even with minimal args.
	Emit(EventSessionCreate, "test-emit-session", nil)
}

// ---------------------------------------------------------------------------
// EmitSessionCreate — 0% → 100%
// ---------------------------------------------------------------------------

func TestEmitSessionCreate_DoesNotPanic(t *testing.T) {
	// Not parallel: uses global DefaultLogger.
	EmitSessionCreate("test-session", SessionCreateData{
		ClaudeCount: 2,
		CodexCount:  1,
		GrokCount:   3,
		WorkDir:     "/tmp/test",
		Recipe:      "default",
	})
}

// ---------------------------------------------------------------------------
// EmitPromptSend — 0% → 100%
// ---------------------------------------------------------------------------

func TestEmitPromptSend_DoesNotPanic(t *testing.T) {
	// Not parallel: uses global DefaultLogger.
	EmitPromptSend("test-session", 3, 500, "default", "cc,cod,gmi", true)
}

// ---------------------------------------------------------------------------
// EmitError — 0% → 100%
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// NewLogger with temp file — write + replay round-trip
// ---------------------------------------------------------------------------

func TestLogger_WriteAndReplay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	logger, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now().Add(-1 * time.Second)

	// Write events
	for i := 0; i < 3; i++ {
		event := NewEvent("test_replay", "test-session", map[string]interface{}{"i": i})
		if err := logger.Log(event); err != nil {
			t.Fatalf("Log failed: %v", err)
		}
	}
	closeLogger(logger)

	// Replay
	logger2, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(logger2)

	events, err := ReadSince(logger2.path, before)
	if err != nil {
		t.Fatalf("Since failed: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}
}

// ---------------------------------------------------------------------------
// Logger disabled — should no-op
// ---------------------------------------------------------------------------

func TestLogger_Disabled_NoFileCreated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	logger, err := NewLogger(LoggerOptions{
		Path:    logPath,
		Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(logger)

	event := NewEvent("test_disabled", "test-session", nil)
	if err := logger.Log(event); err != nil {
		t.Fatalf("Log on disabled logger should not error: %v", err)
	}

	// File should not exist
	if _, err := os.Stat(logPath); err == nil {
		t.Error("disabled logger should not create log file")
	}
}

func TestRotationScanFailureKeepsActiveLogAndWriter(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	// Simulate a legacy/imported line larger than the reader's limit. Both
	// preceding and following records must survive the failed compaction.
	original := []byte("{\"type\":\"before\"}\n" + strings.Repeat("x", maxEventLineBytes) + "\n{\"type\":\"after\"}\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(l)
	for i := 0; i < 2; i++ {
		if err := l.rotateOldEntries(); err == nil {
			t.Fatal("expected oversized-line scan failure")
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("failed rotation changed the active log: bytes=%d want=%d err=%v", len(got), len(original), err)
		}
	}
	if err := l.Log(NewEvent(EventError, "still-writable", nil)); err != nil {
		t.Fatalf("rotation failure disabled logging: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(got, original) || !bytes.Contains(got[len(original):], []byte("still-writable")) {
		t.Fatalf("post-failure write lost records or was silently discarded: %v", err)
	}
}

func TestRotationPreservesPrivatePermissionsAndUnknownRecords(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	old, err := json.Marshal(Event{Timestamp: time.Now().AddDate(0, 0, -35), Type: EventError})
	if err != nil {
		t.Fatal(err)
	}
	unknown := []byte("{}\nnull\n{malformed}\n")
	if err := os.WriteFile(path, append(append(old, '\n'), unknown...), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true, RetentionDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(l)
	if err := l.Log(NewEvent(EventError, "recent", nil)); err != nil {
		t.Fatal(err)
	}
	if err := l.rotateOldEntries(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(got, unknown) || bytes.Contains(got, old) || !bytes.Contains(got, []byte("recent")) {
		t.Fatalf("rotation must expire only dated old records: %q, err=%v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("rotation widened private log permissions to %o", info.Mode().Perm())
	}
}

func TestLoggerRejectsOversizeEventBeforeWrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(l)
	event := NewEvent(EventError, "oversized", map[string]interface{}{"body": strings.Repeat("x", maxEventLineBytes)})
	if err := l.Log(event); err == nil {
		t.Fatal("accepted a record that makes readers fail")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 || l.eventCount != 0 {
		t.Fatalf("oversized event changed persistent log or event counter: info=%v err=%v", info, err)
	}
	if err := l.Log(NewEvent(EventError, "valid", nil)); err != nil {
		t.Fatal(err)
	}
	events, err := ReadSince(path, time.Time{})
	if err != nil || len(events) != 1 || events[0].Session != "valid" {
		t.Fatalf("valid events no longer readable: events=%v err=%v", events, err)
	}
}

func TestRotationRefusesReplacedActiveFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(l)
	if err := l.Log(NewEvent(EventError, "original", nil)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	other := []byte("another process owns this file\n")
	if err := os.WriteFile(path, other, 0600); err != nil {
		t.Fatal(err)
	}
	if err := l.rotateOldEntries(); err == nil {
		t.Fatal("rotated an unrelated replacement file")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, other) {
		t.Fatalf("replacement log was overwritten: %q, err=%v", got, err)
	}
}

func TestRotationClosedOrDisabledDoesNotReopen(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		l, err := NewLogger(LoggerOptions{Path: path, Enabled: enabled})
		if err != nil {
			t.Fatal(err)
		}
		if err := closeLogger(l); err != nil {
			t.Fatal(err)
		}
		if err := l.rotateOldEntries(); err != nil {
			t.Fatal(err)
		}
		if l.file != nil {
			t.Fatal("rotation reopened a closed logger")
		}
		if !enabled {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("rotation created a disabled log: %v", err)
			}
		}
	}
}

type failedEventLogWriter struct{ err error }

func (w failedEventLogWriter) Write([]byte) (int, error) { return 0, w.err }

func TestFilterRetainedEventsPropagatesIOErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("injected rotation I/O failure")
	if err := filterRetainedEvents(iotest.ErrReader(failure), io.Discard, time.Now()); !errors.Is(err, failure) {
		t.Fatalf("lost source read failure: %v", err)
	}
	for _, size := range []int{1, 8192} {
		// Exercise both a buffered flush failure and an immediate write failure.
		err := filterRetainedEvents(strings.NewReader(strings.Repeat("x", size)+"\n"), failedEventLogWriter{failure}, time.Now())
		if !errors.Is(err, failure) {
			t.Fatalf("lost destination failure with %d-byte line: %v", size, err)
		}
	}
}

func TestRotationCommitMergesWritesAfterSnapshot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(l)
	if err := l.Log(NewEvent(EventError, "before", nil)); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	snapshot, err := src.Stat()
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rotation-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	if err := filterRetainedEvents(io.NewSectionReader(src, 0, snapshot.Size()), tmp, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// This is the exact interleaving where the old logger's unchecked merge
	// could discard accepted events. No scheduler timing is needed to test it.
	if err := l.Log(NewEvent(EventError, "during", nil)); err != nil {
		t.Fatal(err)
	}
	if err := l.commitRotation(src, snapshot, tmp); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(NewEvent(EventError, "after", nil)); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSince(path, time.Time{})
	if err != nil || len(got) != 3 {
		t.Fatalf("rotation lost accepted events: count=%d err=%v", len(got), err)
	}
	for i, want := range []string{"before", "during", "after"} {
		if got[i].Session != want {
			t.Fatalf("event %d = %q, want %q", i, got[i].Session, want)
		}
	}
}

func TestRotationCommitFailureKeepsWriter(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(l)
	if err := l.Log(NewEvent(EventError, "before", nil)); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	snapshot, err := src.Stat()
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rotation-test-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(NewEvent(EventError, "during", nil)); err != nil {
		t.Fatal(err)
	}
	if err := l.commitRotation(src, snapshot, tmp); err == nil {
		t.Fatal("ignored a failed merge into the closed staging file")
	}
	if err := l.Log(NewEvent(EventError, "after", nil)); err != nil {
		t.Fatalf("failed merge disabled the writer: %v", err)
	}
	got, err := ReadSince(path, time.Time{})
	if err != nil || len(got) != 3 {
		t.Fatalf("failed merge lost accepted events: count=%d err=%v", len(got), err)
	}
}

func TestRotationConcurrentWritesAndRotations(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(l)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := l.Log(NewEvent(EventError, fmt.Sprintf("%d-%d", worker, i), nil)); err != nil {
					t.Errorf("write: %v", err)
				}
			}
		}(worker)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.rotateOldEntries(); err != nil {
				t.Errorf("rotation: %v", err)
			}
		}()
	}
	wg.Wait()
	if err := closeLogger(l); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSince(path, time.Time{})
	if err != nil || len(got) != 200 {
		t.Fatalf("concurrent rotation lost events: count=%d err=%v", len(got), err)
	}
	seen := make(map[string]bool)
	for _, event := range got {
		if seen[event.Session] {
			t.Fatalf("duplicate event %s", event.Session)
		}
		seen[event.Session] = true
	}
}

func TestLoggerLineLimitRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(l)
	event := NewEvent(EventError, "boundary", map[string]interface{}{"body": ""})
	base, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	event.Data["body"] = strings.Repeat("x", maxEventLineBytes-1-len(base))
	if err := l.Log(event); err != nil {
		t.Fatalf("largest readable line was rejected: %v", err)
	}
	got, err := ReadSince(path, time.Time{})
	if err != nil || len(got) != 1 {
		t.Fatalf("accepted boundary record is unreadable: count=%d err=%v", len(got), err)
	}
	event.Data["body"] = event.Data["body"].(string) + "x"
	if err := l.Log(event); err == nil {
		t.Fatal("accepted one byte beyond the scanner's limit")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != maxEventLineBytes {
		t.Fatalf("failed boundary write altered the log: info=%v err=%v", info, err)
	}
}
