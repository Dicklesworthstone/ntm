//go:build unix

package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPaneStreamerRealFIFOStreamsUnterminatedAndJoinsIdleShutdown(t *testing.T) {
	t.Parallel()

	fifoPath := filepath.Join(t.TempDir(), "pane_live.fifo")
	if err := createFIFO(fifoPath); err != nil {
		t.Fatalf("createFIFO: %v", err)
	}
	fifo, err := os.OpenFile(fifoPath, os.O_RDWR, os.ModeNamedPipe)
	if err != nil {
		t.Fatalf("open FIFO: %v", err)
	}
	t.Cleanup(func() { _ = fifo.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopCh := make(chan struct{})
	events := make(chan StreamEvent, 64)
	const maxBytes = 64
	ps := NewPaneStreamer(nil, "%99", func(ev StreamEvent) {
		events <- ev
	}, PaneStreamerConfig{
		MaxBytesPerEvent:  maxBytes,
		MaxLinesPerEvent:  8,
		FlushInterval:     10 * time.Millisecond,
		FallbackPollLines: 16,
	})

	done := make(chan error, 1)
	go func() {
		done <- ps.readOutputStream(ctx, stopCh, fifo)
		close(events)
	}()

	writer, err := os.OpenFile(fifoPath, os.O_WRONLY, os.ModeNamedPipe)
	if err != nil {
		t.Fatalf("open FIFO writer: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	// 1. Unterminated prompt appears promptly without a newline.
	if _, err := writer.Write([]byte("agent> ")); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	ev1 := nextOutputEvent(t, events)
	if !ev1.Partial || ev1.ContinuesLine || strings.Join(ev1.Lines, "\n") != "agent> " {
		t.Fatalf("unexpected prompt event: %+v", ev1)
	}

	// 2. CR-based progress without newline continues the line promptly.
	if _, err := writer.Write([]byte("\rprogress 50%\rprogress 100%")); err != nil {
		t.Fatalf("write CR progress: %v", err)
	}
	ev2 := nextOutputEvent(t, events)
	if !ev2.Partial || !ev2.ContinuesLine || !strings.Contains(strings.Join(ev2.SnapshotLines, "\n"), "progress 100%") {
		t.Fatalf("unexpected CR progress event: %+v", ev2)
	}

	// 3. Large unterminated UTF-8 output stays bounded per event and valid UTF-8.
	giant := strings.Repeat("🙂a", 120) // 600 bytes > 9x maxBytes
	if _, err := writer.Write([]byte(giant)); err != nil {
		t.Fatalf("write giant output: %v", err)
	}
	var assembled strings.Builder
	for assembled.Len() < len(giant) {
		ev := nextOutputEvent(t, events)
		frag := strings.Join(ev.Lines, "\n")
		if len(frag) > maxBytes || !utf8.ValidString(frag) {
			t.Fatalf("unbounded or invalid UTF-8 fragment: len=%d valid=%v", len(frag), utf8.ValidString(frag))
		}
		if len(strings.Join(ev.SnapshotLines, "\n")) > maxBytes {
			t.Fatalf("snapshot exceeded maxBytes=%d: len=%d", maxBytes, len(strings.Join(ev.SnapshotLines, "\n")))
		}
		assembled.WriteString(frag)
	}
	if assembled.String() != giant {
		t.Fatalf("giant output mismatch: got %d bytes, want %d bytes", assembled.Len(), len(giant))
	}

	// 4. Planted negative: shutting down an idle real Linux FIFO must unblock
	// the reader goroutine and join promptly without hanging.
	close(stopCh)
	_ = fifo.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("idle FIFO shutdown returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("real Linux FIFO shutdown left reader goroutine blocked")
	}
}

func TestPaneStreamerClampsSubRuneMaxBytesPerEvent(t *testing.T) {
	t.Parallel()
	// Planted negative: sub-UTFMax byte bounds must clamp so multi-byte runes
	// cannot stall flushing.
	ps := NewPaneStreamer(nil, "%100", func(StreamEvent) {}, PaneStreamerConfig{MaxBytesPerEvent: 1})
	if ps.config.MaxBytesPerEvent < utf8.UTFMax {
		t.Fatalf("MaxBytesPerEvent = %d, want >= %d", ps.config.MaxBytesPerEvent, utf8.UTFMax)
	}
}
