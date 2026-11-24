package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"
)

func setTestDefaultFIFODir(t *testing.T) string {
	t.Helper()

	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	return filepath.Join(stateDir, "ntm", "streams")
}

func TestPaneStreamerConfig_Defaults(t *testing.T) {
	wantFIFODir := setTestDefaultFIFODir(t)
	cfg := DefaultPaneStreamerConfig()

	if cfg.FIFODir != wantFIFODir {
		t.Errorf("expected FIFODir %s, got %s", wantFIFODir, cfg.FIFODir)
	}
	if cfg.MaxLinesPerEvent != 100 {
		t.Errorf("expected MaxLinesPerEvent 100, got %d", cfg.MaxLinesPerEvent)
	}
	if cfg.MaxBytesPerEvent != 64*1024 {
		t.Errorf("expected MaxBytesPerEvent 65536, got %d", cfg.MaxBytesPerEvent)
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

func startTestOutputStream(t *testing.T, cfg PaneStreamerConfig) (*io.PipeWriter, <-chan StreamEvent, context.CancelFunc, <-chan error) {
	t.Helper()
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan StreamEvent, 32)
	ps := NewPaneStreamer(nil, "%123", func(event StreamEvent) {
		events <- event
	}, cfg)
	done := make(chan error, 1)
	go func() {
		done <- ps.readOutputStream(ctx, nil, reader)
		close(events)
	}()
	t.Cleanup(func() {
		cancel()
		_ = writer.Close()
		// Drain callbacks as well as joining the worker if a failed assertion
		// left output queued; cleanup itself must never strand a reader.
		for range events {
		}
	})
	return writer, events, cancel, done
}

func nextOutputEvent(t *testing.T, events <-chan StreamEvent) StreamEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("stream ended before output arrived")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("output was not flushed while the writer remained open")
		return StreamEvent{}
	}
}

func TestPaneStreamerFlushesPromptAndProgressWithoutNewlines(t *testing.T) {
	writer, events, _, _ := startTestOutputStream(t, PaneStreamerConfig{FlushInterval: 5 * time.Millisecond})
	if _, err := io.WriteString(writer, "ready> "); err != nil {
		t.Fatal(err)
	}
	prompt := nextOutputEvent(t, events)
	if got := strings.Join(prompt.Lines, "\n"); got != "ready> " || !prompt.Partial || prompt.ContinuesLine {
		t.Fatalf("prompt = %+v; want an unterminated initial fragment", prompt)
	}

	if _, err := io.WriteString(writer, "\rworking 50%\x1b[K"); err != nil {
		t.Fatal(err)
	}
	progress := nextOutputEvent(t, events)
	if got := strings.Join(progress.Lines, "\n"); got != "\rworking 50%\x1b[K" || !progress.Partial || !progress.ContinuesLine {
		t.Fatalf("progress = %+v; CR/ANSI fragment must continue the same line", progress)
	}

	if _, err := io.WriteString(writer, "\ndone\nnext> "); err != nil {
		t.Fatal(err)
	}
	ending := nextOutputEvent(t, events)
	if got := strings.Join(ending.Lines, "\n"); got != "\ndone\nnext> " || !ending.Partial || !ending.ContinuesLine {
		t.Fatalf("line boundaries were lost after partial flush: %+v", ending)
	}
	if prompt.Seq != 1 || progress.Seq != 2 || ending.Seq != 3 {
		t.Fatalf("sequence order = %d, %d, %d", prompt.Seq, progress.Seq, ending.Seq)
	}
}

func TestPaneStreamerBoundsGiantUnterminatedOutput(t *testing.T) {
	const maxBytes = 1024
	payload := strings.Repeat("prompt🙂", 20000)
	var reconstructed strings.Builder
	count := 0
	ps := NewPaneStreamer(nil, "%123", func(event StreamEvent) {
		count++
		if !event.Partial || event.ContinuesLine != (count > 1) {
			t.Errorf("incorrect giant-line framing: %+v", event)
		}
		fragment := strings.Join(event.Lines, "\n")
		if len(fragment) > maxBytes || !utf8.ValidString(fragment) {
			t.Errorf("unbounded or invalid UTF-8 fragment: bytes=%d valid=%v", len(fragment), utf8.ValidString(fragment))
		}
		reconstructed.WriteString(fragment)
	}, PaneStreamerConfig{MaxBytesPerEvent: maxBytes, FlushInterval: time.Hour})
	if err := ps.readOutputStream(context.Background(), nil, io.NopCloser(strings.NewReader(payload))); err != io.EOF {
		t.Fatalf("readOutputStream = %v, want EOF", err)
	}
	if count < 100 || reconstructed.String() != payload {
		t.Fatalf("large no-newline output was not preserved in bounded fragments: events=%d got_bytes=%d want_bytes=%d", count, reconstructed.Len(), len(payload))
	}
}

func TestPaneStreamerRetainsSplitUTF8AcrossTimedFlush(t *testing.T) {
	writer, events, _, _ := startTestOutputStream(t, PaneStreamerConfig{FlushInterval: 5 * time.Millisecond})
	runeBytes := []byte("🙂")
	if _, err := writer.Write(append([]byte("ready "), runeBytes[:2]...)); err != nil {
		t.Fatal(err)
	}
	first := nextOutputEvent(t, events)
	if len(first.Lines) != 1 || first.Lines[0] != "ready " || !first.Partial {
		t.Fatalf("timed flush emitted an incomplete UTF-8 rune: %+v", first)
	}
	if _, err := writer.Write(append(runeBytes[2:], '\n')); err != nil {
		t.Fatal(err)
	}
	last := nextOutputEvent(t, events)
	if len(last.Lines) != 1 || last.Lines[0] != "🙂" || last.Partial || !last.ContinuesLine {
		t.Fatalf("split UTF-8 rune or newline was lost: %+v", last)
	}
}

func TestPaneOutputBufferPreservesBoundariesAtByteAndLineLimits(t *testing.T) {
	for _, maxBytes := range []int{4, 5, 16, 64} {
		for _, maxLines := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("bytes_%d_lines_%d", maxBytes, maxLines), func(t *testing.T) {
				const input = "first\n\n🙂\rnext\nlast prompt> "
				var reconstructed strings.Builder
				previousPartial := false
				buffer := paneOutputBuffer{
					maxBytes: maxBytes, maxLines: maxLines,
					emit: func(lines []string, continues, partial bool) {
						if continues != previousPartial || len(lines) > maxLines {
							t.Errorf("invalid framing: lines=%q continues=%v partial=%v", lines, continues, partial)
						}
						fragment := strings.Join(lines, "\n")
						if !partial {
							fragment += "\n"
						}
						if len(fragment) > maxBytes || !utf8.ValidString(fragment) {
							t.Errorf("invalid fragment: %q", fragment)
						}
						reconstructed.WriteString(fragment)
						previousPartial = partial
					},
				}
				// One-byte writes force every possible UTF-8 read split.
				for i := range len(input) {
					buffer.write([]byte(input[i : i+1]))
				}
				buffer.flush(true)
				if reconstructed.String() != input {
					t.Fatalf("got %q, want %q", reconstructed.String(), input)
				}
			})
		}
	}
}

type outputReadError struct {
	*bytes.Reader
	err error
}

func (r outputReadError) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF || r.Reader.Len() == 0 {
		return n, r.err
	}
	return n, err
}

func TestPaneStreamerFlushesDataReturnedWithReadError(t *testing.T) {
	for _, readErr := range []error{io.EOF, errors.New("lost pipe")} {
		t.Run(readErr.Error(), func(t *testing.T) {
			var events []StreamEvent
			ps := NewPaneStreamer(nil, "%123", func(event StreamEvent) {
				events = append(events, event)
			}, PaneStreamerConfig{FlushInterval: time.Hour})
			reader := io.NopCloser(outputReadError{Reader: bytes.NewReader([]byte("line\nprompt> ")), err: readErr})
			if err := ps.readOutputStream(context.Background(), nil, reader); err != readErr {
				t.Fatalf("got error %v, want %v", err, readErr)
			}
			if len(events) != 1 || strings.Join(events[0].Lines, "\n") != "line\nprompt> " || !events[0].Partial {
				t.Fatalf("read error discarded pending output: %+v", events)
			}
		})
	}
}

func TestPaneStreamerCancellationClosesIdleReader(t *testing.T) {
	writer, events, cancel, done := startTestOutputStream(t, PaneStreamerConfig{FlushInterval: 5 * time.Millisecond})
	if _, err := io.WriteString(writer, "ready> "); err != nil {
		t.Fatal(err)
	}
	nextOutputEvent(t, events)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancel returned an error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation left the read goroutine blocked")
	}
	if _, err := io.WriteString(writer, "late output"); err != io.ErrClosedPipe {
		t.Fatalf("reader remained open after cancellation: %v", err)
	}
}

func TestPaneStreamerSnapshotsTrackPartialLineLifecycle(t *testing.T) {
	writer, events, _, _ := startTestOutputStream(t, PaneStreamerConfig{
		FlushInterval: 5 * time.Millisecond,
	})
	write := func(text string) StreamEvent {
		t.Helper()
		if _, err := io.WriteString(writer, text); err != nil {
			t.Fatal(err)
		}
		return nextOutputEvent(t, events)
	}
	first := write("first\n")
	if first.Partial || first.ContinuesLine || first.SnapshotLines != nil {
		t.Fatalf("ordinary complete line unexpectedly needs a snapshot: %+v", first)
	}
	prompt := write("prompt> ")
	if got := strings.Join(prompt.SnapshotLines, "\n"); got != "first\nprompt> " {
		t.Fatalf("initial prompt snapshot = %q", got)
	}
	completed := write("working\n")
	if !completed.ContinuesLine || completed.Partial {
		t.Fatalf("completion lost partial-line boundary: %+v", completed)
	}
	if got := strings.Join(completed.SnapshotLines, "\n"); got != "first\nprompt> working" {
		t.Fatalf("completed prompt snapshot = %q", got)
	}
	last := write("ordinary\n")
	if last.Partial || last.ContinuesLine || last.SnapshotLines != nil ||
		len(last.Lines) != 1 || last.Lines[0] != "ordinary" {
		t.Fatalf("complete lines did not resume incremental delivery: %+v", last)
	}
	// Previously emitted snapshots must not change when later bytes arrive.
	if got := strings.Join(prompt.SnapshotLines, "\n"); got != "first\nprompt> " {
		t.Fatalf("later writes mutated an already published snapshot: %q", got)
	}
}

func TestPaneOutputHistoryBoundsSnapshotsWithoutSplittingUTF8(t *testing.T) {
	for _, maxBytes := range []int{4, 9, 64} {
		for _, maxLines := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("bytes_%d_lines_%d", maxBytes, maxLines), func(t *testing.T) {
				history := paneOutputHistory{maxBytes: maxBytes, maxLines: maxLines}
				history.append([]string{"older", "recent", "prompt🙂"}, false)
				history.append([]string{"🙂tail"}, true)
				const all = "older\nrecent\nprompt🙂🙂tail"
				got := strings.Join(history.lines, "\n")
				if len(got) > maxBytes || len(history.lines) > maxLines || !utf8.ValidString(got) {
					t.Fatalf("invalid bounded snapshot bytes=%d lines=%d value=%q", len(got), len(history.lines), got)
				}
				if got == "" || !strings.HasSuffix(all, got) {
					t.Fatalf("snapshot is not a retained output tail: %q", got)
				}
			})
		}
	}
}

func TestPaneOutputHistoryReleasesDroppedOutput(t *testing.T) {
	t.Run("line bound", func(t *testing.T) {
		source := strings.Split(strings.Repeat("x", 64*1024)+"\nkept\nlast", "\n")
		history := paneOutputHistory{maxBytes: 64 * 1024, maxLines: 2}
		history.append(source, false)
		if strings.Join(history.lines, "\n") != "kept\nlast" {
			t.Fatalf("incorrect retained lines: %q", history.lines)
		}
		for i, line := range history.lines {
			if unsafe.StringData(line) == unsafe.StringData(source[i+1]) {
				t.Fatal("retained line keeps the dropped 64 KiB output allocation alive")
			}
		}
	})
	t.Run("byte bound", func(t *testing.T) {
		source := strings.Repeat("x", 64*1024) + "tail"
		history := paneOutputHistory{maxBytes: 4, maxLines: 2}
		history.append([]string{source}, false)
		if len(history.lines) != 1 || history.lines[0] != "tail" {
			t.Fatalf("incorrect retained suffix: %q", history.lines)
		}
		if unsafe.StringData(history.lines[0]) == unsafe.StringData(source[len(source)-4:]) {
			t.Fatal("retained suffix keeps the dropped 64 KiB output allocation alive")
		}
	})
}

func TestPaneStreamerSnapshotHistoryResetsBetweenReadLifecycles(t *testing.T) {
	var events []StreamEvent
	ps := NewPaneStreamer(nil, "%123", func(event StreamEvent) {
		events = append(events, event)
	}, PaneStreamerConfig{FlushInterval: time.Hour})
	for _, input := range []string{"old prompt> ", "new prompt> "} {
		if err := ps.readOutputStream(context.Background(), nil, io.NopCloser(strings.NewReader(input))); err != io.EOF {
			t.Fatalf("readOutputStream = %v", err)
		}
	}
	// Snapshots describe only the current reader's observed tail. They do not
	// seed or reuse earlier capture-pane backfill or a previous reader's data.
	if len(events) != 2 || strings.Join(events[1].SnapshotLines, "\n") != "new prompt> " || events[1].ContinuesLine {
		t.Fatalf("restarted stream retained old partial output: %+v", events)
	}
}

func TestSimpleHash(t *testing.T) {
	// All strings get hashed to a fixed-length hex string
	short := "hello"
	hash := simpleHash(short)
	if len(hash) != 16 {
		t.Errorf("expected 16-char hex hash, got %d chars: %s", len(hash), hash)
	}

	// Long strings get hashed
	long := "this is a very long string that exceeds 64 characters and should be hashed consistently"
	hashL := simpleHash(long)
	if len(hashL) != 16 {
		t.Errorf("expected 16-char hex hash for long string, got %s", hashL)
	}

	// Different strings should produce different hashes
	long2 := "this is a very long string that exceeds 64 characters but ends with something else"
	hash2 := simpleHash(long2)
	if hashL == hash2 {
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
	acquireGlobalTmuxTestLock(t)

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
	acquireGlobalTmuxTestLock(t)

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

func TestStreamManager_StartAfterStopAll(t *testing.T) {
	acquireGlobalTmuxTestLock(t)

	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()
	sm := NewStreamManager(DefaultClient, func(StreamEvent) {}, cfg)

	sm.StopAll()
	if err := sm.StartStream("fake:0"); err != nil {
		t.Fatalf("StartStream after StopAll: %v", err)
	}
	t.Cleanup(sm.StopAll)

	sm.mu.RLock()
	streamer := sm.streamers["fake:0"]
	sm.mu.RUnlock()
	if streamer == nil {
		t.Fatal("StartStream after StopAll did not retain the new streamer")
	}

	select {
	case <-streamer.ctx.Done():
		t.Fatal("StartStream after StopAll used the manager's cancelled context")
	default:
	}
}

func TestPaneStreamer_UsingFallback(t *testing.T) {
	acquireGlobalTmuxTestLock(t)

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

func TestPaneStreamerFIFOPathIsUniqueForSameTarget(t *testing.T) {
	dir := t.TempDir()
	first := paneStreamerFIFOPath(dir, "project:0.1")
	second := paneStreamerFIFOPath(dir, "project:0.1")

	if first == second {
		t.Fatalf("FIFO paths collide for concurrent streamers: %q", first)
	}
	if filepath.Dir(first) != dir || filepath.Dir(second) != dir {
		t.Fatalf("FIFO paths must remain in configured directory: %q, %q", first, second)
	}
}

func TestPaneStreamer_SecondStreamerDoesNotClaimExistingPipePane(t *testing.T) {
	client := NewClient("")
	first := NewPaneStreamer(client, "shared:0", func(StreamEvent) {}, PaneStreamerConfig{FIFODir: t.TempDir()})
	second := NewPaneStreamer(client, "shared:0", func(StreamEvent) {}, PaneStreamerConfig{FIFODir: t.TempDir()})
	key := paneStreamKey{client: client, target: "shared:0"}

	paneStreamOwners.Lock()
	paneStreamOwners.owners[key] = first
	paneStreamOwners.Unlock()
	t.Cleanup(func() {
		paneStreamOwners.Lock()
		if paneStreamOwners.owners[key] == first {
			delete(paneStreamOwners.owners, key)
		}
		paneStreamOwners.Unlock()
	})

	if err := second.startPipePaneStreaming(); err == nil {
		t.Fatal("second streamer claimed an existing pipe-pane")
	}
	if second.ownsPipePane {
		t.Fatal("second streamer must not own the existing pipe-pane")
	}
	if second.fifoPath != "" {
		t.Fatalf("second streamer created a FIFO despite existing owner: %q", second.fifoPath)
	}
}

func TestPaneStreamerRemotePollsWithoutOpeningAPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SSH fixture uses a POSIX shell")
	}
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	// The stub accepts only a capture through the requested host. Record
	// every invocation so a remote pipe-pane attempt cannot pass by falling
	// back to polling after the command fails.
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_TEST_SSH_CALLS"
case "$*" in
  *stream-host*capture-pane*%37*) printf 'remote prompt>\n' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "ssh-calls")
	t.Setenv("NTM_TEST_SSH_CALLS", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = filepath.Join(dir, "unused-streams")
	cfg.FallbackPollInterval = 10 * time.Millisecond
	events := make(chan StreamEvent, 1)
	ps := NewPaneStreamer(NewClient("stream-host"), "%37", func(event StreamEvent) {
		events <- event
	}, cfg)
	if err := ps.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ps.Stop)
	select {
	case event := <-events:
		if !ps.UsingFallback() || !event.IsFull || event.Target != "%37" || strings.Join(event.Lines, "\n") != "remote prompt>" {
			t.Fatalf("remote stream produced an unexpected snapshot: %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a remote pane snapshot")
	}
	ps.Stop()
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "pipe-pane") {
		t.Fatalf("remote stream modified a pipe: %s", calls)
	}
	if _, err := os.Stat(cfg.FIFODir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote stream touched the local FIFO directory: %v", err)
	}
}

func TestPaneStreamer_Start_RollsBackStateOnFIFODirError(t *testing.T) {
	tmp := t.TempDir()
	fifoDirAsFile := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(fifoDirAsFile, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ps := NewPaneStreamer(nil, "mysession:0", func(StreamEvent) {}, PaneStreamerConfig{FIFODir: fifoDirAsFile})
	if err := ps.Start(context.TODO()); err == nil {
		t.Fatalf("Start() expected error for FIFODir=%q, got nil", fifoDirAsFile)
	}

	ps.mu.Lock()
	running := ps.running
	ps.mu.Unlock()

	if running {
		t.Fatalf("Start() error left ps.running=true")
	}
	if ps.ctx != nil {
		t.Fatalf("Start() error left ps.ctx non-nil")
	}
	if ps.stopCh != nil {
		t.Fatalf("Start() error left ps.stopCh non-nil")
	}
}

func TestPaneStreamer_StartDoesNotRetainBlockedFIFOPath(t *testing.T) {
	dir := t.TempDir()
	target := "mysession:0"
	sequence := fifoPathSequence.Load() + 1
	blockedPath := filepath.Join(dir, fmt.Sprintf("pane_mysession_0_%d_%d.fifo", os.Getpid(), sequence))
	if err := os.WriteFile(blockedPath, []byte("not a fifo"), 0600); err != nil {
		t.Fatalf("write blocking path: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ps := NewPaneStreamer(nil, target, func(StreamEvent) {}, PaneStreamerConfig{FIFODir: dir})
	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ps.mu.Lock()
	fifoPath := ps.fifoPath
	ps.mu.Unlock()
	if fifoPath != "" {
		t.Fatalf("failed FIFO setup retained cleanup path %q", fifoPath)
	}

	cancel()
	done := make(chan struct{})
	go func() {
		ps.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fallback worker did not stop after context cancellation")
	}

	if _, err := os.Stat(blockedPath); err != nil {
		t.Fatalf("blocked path was unexpectedly removed: %v", err)
	}
}

func TestNewPaneStreamer_DefaultsApplied(t *testing.T) {
	callback := func(event StreamEvent) {}
	wantFIFODir := setTestDefaultFIFODir(t)

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
	if ps.config.FIFODir != wantFIFODir {
		t.Errorf("expected default FIFODir %s, got %s", wantFIFODir, ps.config.FIFODir)
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

func TestNewPaneStreamer_NilClientDefaultsToDefaultClient(t *testing.T) {
	ps := NewPaneStreamer(nil, "test:0", func(StreamEvent) {}, PaneStreamerConfig{})

	if ps.client != DefaultClient {
		t.Fatal("expected nil client to default to DefaultClient")
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

func TestPaneStreamer_StartWaitsForConcurrentStop(t *testing.T) {
	acquireGlobalTmuxTestLock(t)

	callback := func(event StreamEvent) {}
	cfg := DefaultPaneStreamerConfig()
	cfg.FIFODir = t.TempDir()

	ps := NewPaneStreamer(DefaultClient, "nonexistent:0", callback, cfg)

	ps.mu.Lock()
	ps.running = true
	ps.stopCh = make(chan struct{})
	ps.mu.Unlock()

	release := make(chan struct{})
	ps.wg.Add(1)
	go func() {
		defer ps.wg.Done()
		<-release
	}()

	stopped := make(chan struct{})
	go func() {
		ps.Stop()
		close(stopped)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		ps.mu.Lock()
		running := ps.running
		ps.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not begin before timeout")
		}
		time.Sleep(time.Millisecond)
	}

	started := make(chan error, 1)
	go func() {
		started <- ps.Start(context.Background())
	}()

	select {
	case err := <-started:
		t.Fatalf("Start returned before Stop completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after worker released")
	}

	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start returned unexpected error after Stop completed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not resume after Stop completed")
	}

	ps.Stop()
}

func TestStreamManager_StatsWithActiveStreams(t *testing.T) {
	acquireGlobalTmuxTestLock(t)

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

func TestNewStreamManager_NilClientDefaultsToDefaultClient(t *testing.T) {
	sm := NewStreamManager(nil, func(StreamEvent) {}, PaneStreamerConfig{})
	defer sm.StopAll()

	if sm.client != DefaultClient {
		t.Fatal("expected nil client to default to DefaultClient")
	}
}

func TestSimpleHash_ExactlyBoundary(t *testing.T) {
	// Test strings exactly at the 64 character boundary
	exactly64 := "0123456789012345678901234567890123456789012345678901234567890123"
	if len(exactly64) != 64 {
		t.Fatalf("test string should be exactly 64 chars, got %d", len(exactly64))
	}

	// All lengths are now hashed consistently
	hash := simpleHash(exactly64)
	if len(hash) != 16 {
		t.Errorf("expected 16-char hex hash, got %s", hash)
	}

	// 63 chars
	chars63 := exactly64[:63]
	hash63 := simpleHash(chars63)
	if len(hash63) != 16 {
		t.Errorf("expected 16-char hex hash for 63-char string, got %s", hash63)
	}

	if hash == hash63 {
		t.Error("expected different hashes for different lengths")
	}
}

func TestCreateFIFO_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	fifoPath := dir + "/test.fifo"

	// Create FIFO first time
	if err := createFIFO(fifoPath); err != nil {
		t.Fatalf("first createFIFO failed: %v", err)
	}

	// Recreating a stale FIFO is safe and remains supported.
	if err := createFIFO(fifoPath); err != nil {
		t.Fatalf("recreate stale FIFO: %v", err)
	}
	info, err := os.Stat(fifoPath)
	if err != nil {
		t.Fatalf("stat recreated FIFO: %v", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("recreated path mode = %v, want named pipe", info.Mode())
	}
}

func TestCreateFIFORefusesToReplaceRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/not-a-fifo"
	const contents = "must not be deleted"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write regular file: %v", err)
	}

	if err := createFIFO(path); err == nil {
		t.Fatal("createFIFO succeeded for a pre-existing regular file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original regular file: %v", err)
	}
	if string(got) != contents {
		t.Fatalf("regular file contents = %q, want %q", got, contents)
	}
}

func TestStreamManager_MultipleStartsSameTarget(t *testing.T) {
	acquireGlobalTmuxTestLock(t)

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
	acquireGlobalTmuxTestLock(t)

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

func TestStreamManagerIndependentViewerLeases(t *testing.T) {
	cfg := DefaultPaneStreamerConfig()
	cfg.FallbackPollInterval = time.Hour
	sm := NewStreamManager(NewClient("lease-test-host"), func(StreamEvent) {}, cfg)
	t.Cleanup(sm.StopAll)
	const target = "%42"
	acquire := func(owner string) {
		t.Helper()
		if err := sm.AcquireStreamLease(target, owner, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	acquire("first")
	worker := sm.streamers[target]
	stale := sm.claims[target]["first"]
	acquire("first")
	acquire("second")
	if err := sm.StartStream(target); err != nil {
		t.Fatal(err)
	}
	sm.StopStream(target)
	sm.releaseStream(target, "first", stale)
	sm.ReleaseStreamLease(target, "second")
	sm.ReleaseStreamLease(target, "second")
	if sm.streamers[target] != worker || len(sm.claims[target]) != 1 {
		t.Fatal("a stale expiry, duplicate release, or legacy stop interrupted the first viewer")
	}
	sm.ReleaseStreamLease(target, "first")
	if got := sm.ListActive(); len(got) != 0 {
		t.Fatalf("worker retained after last release: %v", got)
	}
}

func TestStreamManagerExpiresAbandonedLease(t *testing.T) {
	cfg := DefaultPaneStreamerConfig()
	cfg.FallbackPollInterval = time.Hour
	sm := NewStreamManager(NewClient("lease-test-host"), func(StreamEvent) {}, cfg)
	t.Cleanup(sm.StopAll)
	if err := sm.AcquireStreamLease("%42", "abandoned", 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(sm.ListActive()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("abandoned viewer kept capture worker alive")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStreamManagerCapsNewLeasesAndAllowsRenewal(t *testing.T) {
	cfg := DefaultPaneStreamerConfig()
	cfg.FallbackPollInterval = time.Hour
	sm := NewStreamManager(NewClient("lease-test-host"), func(StreamEvent) {}, cfg)
	t.Cleanup(sm.StopAll)
	for i := 0; i < maxViewerLeasesPerPane; i++ {
		if err := sm.AcquireStreamLease("%42", fmt.Sprint(i), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if err := sm.AcquireStreamLease("%42", "extra", time.Hour); !errors.Is(err, ErrStreamViewerLimit) {
		t.Fatalf("excess viewer error = %v", err)
	}
	if err := sm.AcquireStreamLease("%42", "0", time.Hour); err != nil {
		t.Fatalf("existing viewer could not renew at capacity: %v", err)
	}
	sm.ReleaseStreamLease("%42", "0")
	if err := sm.AcquireStreamLease("%42", "extra", time.Hour); err != nil {
		t.Fatalf("released slot stayed unavailable: %v", err)
	}
}
