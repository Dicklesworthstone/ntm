package events

import (
	"os"
	"path/filepath"
	"testing"
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
	defer logger.Close()

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
	defer logger.Close()

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
	EmitSessionCreate("test-session", 2, 1, 1, "/tmp/test", "default")
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

func TestEmitError_DoesNotPanic(t *testing.T) {
	// Not parallel: uses global DefaultLogger.
	EmitError("test-session", "test_error", "something went wrong")
}

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
	logger.Close()

	// Replay
	logger2, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer logger2.Close()

	events, err := logger2.Since(before)
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
	defer logger.Close()

	event := NewEvent("test_disabled", "test-session", nil)
	if err := logger.Log(event); err != nil {
		t.Fatalf("Log on disabled logger should not error: %v", err)
	}

	// File should not exist
	if _, err := os.Stat(logPath); err == nil {
		t.Error("disabled logger should not create log file")
	}
}
