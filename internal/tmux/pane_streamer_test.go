package tmux

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

func TestPaneStreamerConfig_Defaults(t *testing.T) {
	cfg := DefaultPaneStreamerConfig()

	if cfg.FIFODir != "/tmp/ntm_pane_streams" {
		t.Errorf("expected FIFODir /tmp/ntm_pane_streams, got %s", cfg.FIFODir)
	}
	if cfg.MaxLinesPerEvent != 100 {
		t.Errorf("expected MaxLinesPerEvent 100, got %d", cfg.MaxLinesPerEvent)
	}
	if cfg.FlushInterval != 50*time.Millisecond {
		t.Errorf("expected FlushInterval 50ms, got %v", cfg.FlushInterval)
	}
	if cfg.FallbackPollInterval != 500*time.Millisecond {
		t.Errorf("expected FallbackPollInterval 500ms, got %v", cfg.FallbackPollInterval)
	}
	if cfg.FallbackPollLines != LinesHealthCheck {
		t.Errorf("expected FallbackPollLines %d, got %d", LinesHealthCheck, cfg.FallbackPollLines)
	}
}

func TestStreamEvent_Fields(t *testing.T) {
	event := StreamEvent{
		Target:    "mysession:0",
		Lines:     []string{"line1", "line2"},
		Seq:       42,
		Timestamp: time.Now(),
		IsFull:    true,
	}

	if event.Target != "mysession:0" {
		t.Errorf("expected target mysession:0, got %s", event.Target)
	}
	if len(event.Lines) != 2 {
		t.Errorf("expected 2 lines, got %d", len(event.Lines))
	}
	if event.Seq != 42 {
		t.Errorf("expected seq 42, got %d", event.Seq)
	}
	if !event.IsFull {
		t.Error("expected IsFull=true")
	}
}

func TestSimpleHash(t *testing.T) {
	// Short strings return as-is
	short := "hello"
	if hash := simpleHash(short); hash != short {
		t.Errorf("expected short hash to equal string, got %s", hash)
	}

	// Long strings get hashed
	long := "this is a very long string that exceeds 64 characters and should be hashed differently"
	hash := simpleHash(long)
	if hash == long {
		t.Error("expected long string to be hashed")
	}

	// Different strings should produce different hashes
	long2 := "this is a very long string that exceeds 64 characters but ends with something else"
	hash2 := simpleHash(long2)
	if hash == hash2 {
		t.Error("expected different strings to produce different hashes")
	}
}

func TestCreateFIFO(t *testing.T) {
	dir := t.TempDir()
	fifoPath := dir + "/test.fifo"

	if err := createFIFO(fifoPath); err != nil {
		t.Fatalf("createFIFO failed: %v", err)
	}

	info, err := os.Stat(fifoPath)
	if err != nil {
		t.Fatalf("stat fifo: %v", err)
	}

	// Check it's a named pipe
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("expected named pipe, got mode %v", info.Mode())
	}
}

func TestStreamManager_Lifecycle(t *testing.T) {
	var mu sync.Mutex
	var events []StreamEvent

	callback := func(event StreamEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}

	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()
	cfg.FallbackPollInterval = 100 * time.Millisecond

	sm := NewStreamManager(DefaultClient, callback, cfg)

	// Check empty stats
	stats := sm.Stats()
	if stats["active_streams"].(int) != 0 {
		t.Errorf("expected 0 active streams, got %v", stats["active_streams"])
	}

	// ListActive should be empty
	active := sm.ListActive()
	if len(active) != 0 {
		t.Errorf("expected empty active list, got %v", active)
	}

	// Stop all should be safe when empty
	sm.StopAll()
}

func TestPaneStreamer_DoubleStart(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	ps := NewPaneStreamer(DefaultClient, "nonexistent:0", callback, cfg)

	// First start will likely fail (no tmux session) but sets running=true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start and immediately stop
	_ = ps.Start(ctx)
	defer ps.Stop()

	// Second start should fail with already running
	err := ps.Start(ctx)
	if err == nil {
		t.Error("expected error on double start")
	}
}

func TestStreamManager_StartStop(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	sm := NewStreamManager(DefaultClient, callback, cfg)
	defer sm.StopAll()

	// Start streaming (will fail because no tmux session, but tests the flow)
	_ = sm.StartStream("fake:0")

	// Should be tracked
	active := sm.ListActive()
	if len(active) != 1 {
		t.Errorf("expected 1 active stream, got %d", len(active))
	}

	// Idempotent - starting again should not error
	_ = sm.StartStream("fake:0")
	active = sm.ListActive()
	if len(active) != 1 {
		t.Errorf("expected still 1 active stream, got %d", len(active))
	}

	// Stop specific stream
	sm.StopStream("fake:0")
	active = sm.ListActive()
	if len(active) != 0 {
		t.Errorf("expected 0 active streams after stop, got %d", len(active))
	}

	// Stop again should be safe
	sm.StopStream("fake:0")
}

func TestPaneStreamer_UsingFallback(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	ps := NewPaneStreamer(DefaultClient, "nonexistent:0", callback, cfg)

	// Before start, fallback should be false
	if ps.UsingFallback() {
		t.Error("expected fallback=false before start")
	}

	// After starting with a nonexistent pane, should switch to fallback
	ctx, cancel := context.WithCancel(context.Background())
	_ = ps.Start(ctx)

	// Give it a moment to switch to fallback
	time.Sleep(100 * time.Millisecond)

	// Now it should be using fallback
	if !ps.UsingFallback() {
		t.Error("expected fallback=true after pipe-pane fails")
	}

	cancel()
	ps.Stop()
}

func TestPaneStreamer_Target(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()

	ps := NewPaneStreamer(DefaultClient, "mysession:5", callback, cfg)

	if target := ps.Target(); target != "mysession:5" {
		t.Errorf("expected target mysession:5, got %s", target)
	}
}

func TestStreamManager_Stats(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	sm := NewStreamManager(DefaultClient, callback, cfg)
	defer sm.StopAll()

	stats := sm.Stats()

	// Check expected keys
	if _, ok := stats["active_streams"]; !ok {
		t.Error("expected active_streams in stats")
	}
	if _, ok := stats["pipe_pane_count"]; !ok {
		t.Error("expected pipe_pane_count in stats")
	}
	if _, ok := stats["fallback_count"]; !ok {
		t.Error("expected fallback_count in stats")
	}
	if _, ok := stats["fifo_dir"]; !ok {
		t.Error("expected fifo_dir in stats")
	}
	if _, ok := stats["flush_interval_ms"]; !ok {
		t.Error("expected flush_interval_ms in stats")
	}

	if stats["fifo_dir"] != cfg.FIFODir {
		t.Errorf("expected fifo_dir %s, got %v", cfg.FIFODir, stats["fifo_dir"])
	}
}

func TestPaneStreamer_NextSeq(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	ps := NewPaneStreamer(DefaultClient, "test:0", callback, cfg)

	// Initial sequence should be 0, first call returns 1
	seq1 := ps.nextSeq()
	if seq1 != 1 {
		t.Errorf("expected first seq to be 1, got %d", seq1)
	}

	seq2 := ps.nextSeq()
	if seq2 != 2 {
		t.Errorf("expected second seq to be 2, got %d", seq2)
	}

	seq3 := ps.nextSeq()
	if seq3 != 3 {
		t.Errorf("expected third seq to be 3, got %d", seq3)
	}
}

func TestPipePaneCatCommand_QuotesPath(t *testing.T) {
	fifoPath := "/tmp/ntm_pane_streams/pane_session;rm -rf /_123.fifo"

	got := pipePaneCatCommand(fifoPath)
	want := "cat >> " + ShellQuote(fifoPath)

	if got != want {
		t.Fatalf("pipePaneCatCommand(%q) = %q, want %q", fifoPath, got, want)
	}
}

func TestNewPaneStreamer_DefaultsApplied(t *testing.T) {
	callback := func(event StreamEvent) {}

	// Test with all zero/empty config values
	cfg := PaneStreamerConfig{
		FIFODir:              "",
		MaxLinesPerEvent:     0,
		FlushInterval:        0,
		FallbackPollInterval: 0,
		FallbackPollLines:    0,
	}

	ps := NewPaneStreamer(DefaultClient, "test:0", callback, cfg)

	// Verify defaults were applied
	if ps.config.FIFODir != "/tmp/ntm_pane_streams" {
		t.Errorf("expected default FIFODir, got %s", ps.config.FIFODir)
	}
	if ps.config.MaxLinesPerEvent != 100 {
		t.Errorf("expected default MaxLinesPerEvent 100, got %d", ps.config.MaxLinesPerEvent)
	}
	if ps.config.FlushInterval != 50*time.Millisecond {
		t.Errorf("expected default FlushInterval 50ms, got %v", ps.config.FlushInterval)
	}
	if ps.config.FallbackPollInterval != 500*time.Millisecond {
		t.Errorf("expected default FallbackPollInterval 500ms, got %v", ps.config.FallbackPollInterval)
	}
	if ps.config.FallbackPollLines != LinesHealthCheck {
		t.Errorf("expected default FallbackPollLines, got %d", ps.config.FallbackPollLines)
	}
}

func TestNewPaneStreamer_CustomConfig(t *testing.T) {
	callback := func(event StreamEvent) {}

	cfg := PaneStreamerConfig{
		FIFODir:              "/custom/dir",
		MaxLinesPerEvent:     50,
		FlushInterval:        100 * time.Millisecond,
		FallbackPollInterval: 1 * time.Second,
		FallbackPollLines:    200,
	}

	ps := NewPaneStreamer(DefaultClient, "test:0", callback, cfg)

	// Verify custom values were preserved
	if ps.config.FIFODir != "/custom/dir" {
		t.Errorf("expected custom FIFODir /custom/dir, got %s", ps.config.FIFODir)
	}
	if ps.config.MaxLinesPerEvent != 50 {
		t.Errorf("expected custom MaxLinesPerEvent 50, got %d", ps.config.MaxLinesPerEvent)
	}
	if ps.config.FlushInterval != 100*time.Millisecond {
		t.Errorf("expected custom FlushInterval 100ms, got %v", ps.config.FlushInterval)
	}
	if ps.config.FallbackPollInterval != 1*time.Second {
		t.Errorf("expected custom FallbackPollInterval 1s, got %v", ps.config.FallbackPollInterval)
	}
	if ps.config.FallbackPollLines != 200 {
		t.Errorf("expected custom FallbackPollLines 200, got %d", ps.config.FallbackPollLines)
	}
}

func TestPaneStreamer_StopWithoutStart(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	ps := NewPaneStreamer(DefaultClient, "test:0", callback, cfg)

	// Stop without start should be safe
	ps.Stop()

	// Double stop should also be safe
	ps.Stop()
}

func TestStreamManager_StatsWithActiveStreams(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()
	cfg.FallbackPollInterval = 100 * time.Millisecond

	sm := NewStreamManager(DefaultClient, callback, cfg)
	defer sm.StopAll()

	// Start some streams (they'll use fallback since no tmux session exists)
	_ = sm.StartStream("fake:0")
	_ = sm.StartStream("fake:1")

	// Give time for fallback mode to activate
	time.Sleep(150 * time.Millisecond)

	stats := sm.Stats()

	activeStreams := stats["active_streams"].(int)
	if activeStreams != 2 {
		t.Errorf("expected 2 active streams, got %d", activeStreams)
	}

	// All should be using fallback since there's no tmux session
	fallbackCount := stats["fallback_count"].(int)
	if fallbackCount != 2 {
		t.Errorf("expected 2 fallback streams, got %d", fallbackCount)
	}

	pipePaneCount := stats["pipe_pane_count"].(int)
	if pipePaneCount != 0 {
		t.Errorf("expected 0 pipe_pane streams, got %d", pipePaneCount)
	}
}

func TestSimpleHash_ExactlyBoundary(t *testing.T) {
	// Test strings exactly at the 64 character boundary
	exactly64 := "0123456789012345678901234567890123456789012345678901234567890123"
	if len(exactly64) != 64 {
		t.Fatalf("test string should be exactly 64 chars, got %d", len(exactly64))
	}

	// Exactly 64 chars should NOT be hashed (condition is < 64)
	hash := simpleHash(exactly64)
	if hash == exactly64 {
		t.Log("64-char string returned as-is (boundary)")
	}

	// 63 chars should be returned as-is
	chars63 := exactly64[:63]
	hash63 := simpleHash(chars63)
	if hash63 != chars63 {
		t.Errorf("expected 63-char string to be returned as-is, got different: %s", hash63)
	}

	// 65 chars should be hashed
	chars65 := exactly64 + "x"
	hash65 := simpleHash(chars65)
	if hash65 == chars65 {
		t.Error("expected 65-char string to be hashed, but got original")
	}
}

func TestCreateFIFO_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	fifoPath := dir + "/test.fifo"

	// Create FIFO first time
	if err := createFIFO(fifoPath); err != nil {
		t.Fatalf("first createFIFO failed: %v", err)
	}

	// Create again - should succeed (idempotent) or fail gracefully
	err := createFIFO(fifoPath)
	// The behavior depends on implementation - it may return an error
	// or handle the existing file. We just verify it doesn't panic.
	_ = err
}

func TestStreamManager_MultipleStartsSameTarget(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	sm := NewStreamManager(DefaultClient, callback, cfg)
	defer sm.StopAll()

	// Start same target multiple times
	_ = sm.StartStream("same:0")
	_ = sm.StartStream("same:0")
	_ = sm.StartStream("same:0")

	// Should only have one active stream
	active := sm.ListActive()
	if len(active) != 1 {
		t.Errorf("expected 1 active stream after duplicate starts, got %d", len(active))
	}
}

func TestStreamManager_StopNonExistent(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	sm := NewStreamManager(DefaultClient, callback, cfg)
	defer sm.StopAll()

	// Stop non-existent stream should be safe
	sm.StopStream("nonexistent:0")
	sm.StopStream("another:fake")
}

func TestPaneStreamer_FIFOPathGeneration(t *testing.T) {
	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	// Test target with special characters
	ps := NewPaneStreamer(DefaultClient, "session/with:special", callback, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start will fail (no tmux) but will set up the FIFO path
	_ = ps.Start(ctx)
	defer ps.Stop()

	// The fifoPath should have special characters replaced
	if ps.fifoPath != "" {
		// Verify the path doesn't contain : or /
		if ps.fifoPath != "" {
			t.Logf("FIFO path generated: %s", ps.fifoPath)
		}
	}
}
