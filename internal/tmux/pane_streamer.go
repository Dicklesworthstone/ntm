// Package tmux provides pane output streaming using pipe-pane with polling fallback.
package tmux

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// StreamEvent represents a pane output event.
type StreamEvent struct {
	Target    string    // Pane target (e.g., "session:0")
	Lines     []string  // Output lines
	Seq       int64     // Sequence number
	Timestamp time.Time // When the event was captured
	IsFull    bool      // True if this is a full capture (polling), false if incremental (pipe)
	// ContinuesLine joins the first fragment to the preceding event's last
	// unterminated line. Partial means this event's last line has no newline.
	ContinuesLine bool
	Partial       bool
	// SnapshotLines is the bounded stream tail for line-oriented consumers
	// when this event contains or completes an unterminated line.
	// It includes only output observed since this reader started, not earlier
	// capture-pane backfill; restarting a reader begins a new tail.
	SnapshotLines []string
}

// StreamCallback is called when new output is available.
type StreamCallback func(event StreamEvent)

// PaneStreamerConfig configures the pane streamer.
type PaneStreamerConfig struct {
	// FIFODir is where named pipes are created (default: secure user-specific dir)
	FIFODir string

	// MaxLinesPerEvent limits lines per WebSocket event (default: 100)
	MaxLinesPerEvent int

	// MaxBytesPerEvent bounds buffered output, including newline delimiters.
	// Long lines are emitted as fragments rather than retained until a newline.
	MaxBytesPerEvent int

	// FlushInterval is the max time to buffer before flushing (default: 50ms)
	FlushInterval time.Duration

	// FallbackPollInterval is the polling interval when pipe-pane fails (default: 500ms)
	FallbackPollInterval time.Duration

	// FallbackPollLines is the number of lines to capture in polling mode (default: 50)
	FallbackPollLines int
}

// DefaultPaneStreamerConfig returns sensible defaults.
func DefaultPaneStreamerConfig() PaneStreamerConfig {
	return PaneStreamerConfig{
		FIFODir:              defaultFIFODir(),
		MaxLinesPerEvent:     100,
		MaxBytesPerEvent:     64 * 1024,
		FlushInterval:        50 * time.Millisecond,
		FallbackPollInterval: 500 * time.Millisecond,
		FallbackPollLines:    LinesHealthCheck,
	}
}

func defaultFIFODir() string {
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			stateDir = filepath.Join(home, ".local", "state")
		} else {
			// Fallback if no home dir
			return filepath.Join(os.TempDir(), fmt.Sprintf("ntm_streams_%d", os.Getuid()))
		}
	}
	return filepath.Join(stateDir, "ntm", "streams")
}

// PaneStreamer streams output from a single pane.
type PaneStreamer struct {
	client   *Client
	target   string // e.g., "session:0"
	config   PaneStreamerConfig
	callback StreamCallback

	lifecycleMu sync.Mutex
	ctx         context.Context
	stopCh      chan struct{}
	wg          sync.WaitGroup

	fifoPath     string
	fifoMu       sync.Mutex
	fifo         *os.File
	seq          int64
	useFallback  atomic.Bool
	ownsPipePane bool

	mu       sync.Mutex
	running  bool
	lastHash string // Hash of last captured output for deduplication
}

// fifoPathSequence distinguishes concurrent streamers for the same pane in a
// single process. A target plus PID alone is not unique when separate stream
// managers stream the same pane: the later streamer would unlink the earlier
// streamer's live FIFO.
var fifoPathSequence atomic.Uint64

type paneStreamKey struct {
	client *Client
	target string
}

var paneStreamOwners = struct {
	sync.Mutex
	owners map[paneStreamKey]*PaneStreamer
}{owners: make(map[paneStreamKey]*PaneStreamer)}

// NewPaneStreamer creates a streamer for the given pane target.
func NewPaneStreamer(client *Client, target string, callback StreamCallback, cfg PaneStreamerConfig) *PaneStreamer {
	if client == nil {
		client = DefaultClient
	}
	if cfg.FIFODir == "" {
		cfg.FIFODir = defaultFIFODir()
	}
	if cfg.MaxLinesPerEvent <= 0 {
		cfg.MaxLinesPerEvent = 100
	}
	if cfg.MaxBytesPerEvent <= 0 {
		cfg.MaxBytesPerEvent = 64 * 1024
	} else if cfg.MaxBytesPerEvent < utf8.UTFMax {
		// Every valid UTF-8 rune must fit without being split across frames.
		cfg.MaxBytesPerEvent = utf8.UTFMax
	}

	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 50 * time.Millisecond
	}
	if cfg.FallbackPollInterval <= 0 {
		cfg.FallbackPollInterval = 500 * time.Millisecond
	}
	if cfg.FallbackPollLines <= 0 {
		cfg.FallbackPollLines = LinesHealthCheck
	}

	return &PaneStreamer{
		client:   client,
		target:   target,
		config:   cfg,
		callback: callback,
	}
}

// Start begins streaming pane output.
func (ps *PaneStreamer) Start(ctx context.Context) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	ps.lifecycleMu.Lock()
	defer ps.lifecycleMu.Unlock()

	ps.mu.Lock()
	if ps.running {
		ps.mu.Unlock()
		return fmt.Errorf("streamer already running for %s", ps.target)
	}
	ps.running = true
	ps.ctx = ctx
	ps.stopCh = make(chan struct{})
	ps.useFallback.Store(false)
	ps.lastHash = ""
	ps.fifoPath = ""
	ps.mu.Unlock()

	defer func() {
		if err == nil {
			return
		}

		ps.mu.Lock()
		ps.running = false
		stopCh := ps.stopCh
		ps.ctx = nil
		ps.stopCh = nil
		ps.mu.Unlock()

		if stopCh != nil {
			close(stopCh)
		}
	}()

	// pipe-pane opens its output on the tmux server's host. A remote tmux
	// server cannot write our local FIFO. Poll through the same SSH client
	// that owns the pane, without touching either host's pipe-pane.
	if ps.client.Remote != "" {
		ps.useFallback.Store(true)
		ps.wg.Add(1)
		go ps.runPollingLoop()
		return nil
	}

	// Ensure FIFO directory exists
	if err = os.MkdirAll(ps.config.FIFODir, 0755); err != nil {
		return fmt.Errorf("create fifo dir: %w", err)
	}

	// Try pipe-pane first
	if err := ps.startPipePaneStreaming(); err != nil {
		log.Printf("pipe-pane: failed for %s, falling back to polling: %v", ps.target, err)
		ps.useFallback.Store(true)
		ps.wg.Add(1)
		go ps.runPollingLoop()
	}

	return nil
}

// Stop stops streaming and cleans up.
func (ps *PaneStreamer) Stop() {
	ps.lifecycleMu.Lock()
	defer ps.lifecycleMu.Unlock()

	ps.mu.Lock()
	if !ps.running {
		ps.mu.Unlock()
		return
	}
	ps.running = false
	stopCh := ps.stopCh
	ps.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}

	// Stop pipe-pane before closing the FIFO. Closing the descriptor wakes an
	// idle reader, and runFIFOReader joins it before Stop returns.
	if ps.ownsPipePane {
		_ = ps.client.RunSilent("pipe-pane", "-t", ExactTarget(ps.target))
		paneStreamOwners.Lock()
		delete(paneStreamOwners.owners, paneStreamKey{client: ps.client, target: ps.target})
		paneStreamOwners.Unlock()
		ps.ownsPipePane = false
	}
	if ps.fifoPath != "" {
		_ = os.Remove(ps.fifoPath)
	}
	ps.closeFIFO()

	ps.wg.Wait()

	ps.mu.Lock()
	ps.ctx = nil
	ps.stopCh = nil
	ps.mu.Unlock()
}

// UsingFallback returns true if polling mode is active.
func (ps *PaneStreamer) UsingFallback() bool {
	return ps.useFallback.Load()
}

// nextSeq returns the next sequence number.
func (ps *PaneStreamer) nextSeq() int64 {
	return atomic.AddInt64(&ps.seq, 1)
}

func pipePaneCatCommand(fifoPath string) string {
	// pipe-pane runs the command via a shell, so the FIFO path must be quoted.
	return fmt.Sprintf("cat >> %s", ShellQuote(fifoPath))
}

// startPipePaneStreaming sets up pipe-pane streaming via a FIFO.
func (ps *PaneStreamer) startPipePaneStreaming() error {
	key := paneStreamKey{client: ps.client, target: ps.target}
	paneStreamOwners.Lock()
	defer paneStreamOwners.Unlock()
	if _, exists := paneStreamOwners.owners[key]; exists {
		return fmt.Errorf("pipe-pane already owned for %s", ps.target)
	}

	ps.fifoPath = paneStreamerFIFOPath(ps.config.FIFODir, ps.target)

	// Create FIFO (named pipe)
	if err := createFIFO(ps.fifoPath); err != nil {
		// No FIFO was created, so Stop must not treat this path as ours. In
		// particular, createFIFO refuses an existing regular file and Stop must
		// never remove that file while cleaning up a fallback stream.
		ps.fifoPath = ""
		return fmt.Errorf("create fifo: %w", err)
	}

	// Start the pipe-pane command
	// Note: pipe-pane runs the command in a shell, output goes to the command's stdin
	catCmd := pipePaneCatCommand(ps.fifoPath)
	if err := ps.client.RunSilent("pipe-pane", "-t", ExactTarget(ps.target), catCmd); err != nil {
		os.Remove(ps.fifoPath)
		ps.fifoPath = ""
		return fmt.Errorf("pipe-pane: %w", err)
	}
	paneStreamOwners.owners[key] = ps
	ps.ownsPipePane = true

	log.Printf("pipe-pane: attached to %s via %s", ps.target, ps.fifoPath)

	// Start reader goroutine
	ps.wg.Add(1)
	go ps.runFIFOReader()

	return nil
}

func paneStreamerFIFOPath(dir, target string) string {
	safeTarget := strings.ReplaceAll(target, ":", "_")
	safeTarget = strings.ReplaceAll(safeTarget, "/", "_")
	return filepath.Join(dir, fmt.Sprintf("pane_%s_%d_%d.fifo", safeTarget, os.Getpid(), fifoPathSequence.Add(1)))
}

// runFIFOReader reads from the FIFO and emits events.
func (ps *PaneStreamer) runFIFOReader() {
	defer ps.wg.Done()

	ctx := ps.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	stopCh := ps.stopCh

	// Open FIFO with O_RDWR to prevent blocking on open when no writer.
	// With O_RDONLY, the open() syscall blocks until a writer opens the FIFO.
	// O_RDWR allows the reader to open immediately, and we use SetReadDeadline
	// to implement non-blocking reads in the loop below.
	fifo, err := os.OpenFile(ps.fifoPath, os.O_RDWR, os.ModeNamedPipe)
	if err != nil {
		log.Printf("pipe-pane: failed to open fifo %s: %v, switching to fallback", ps.fifoPath, err)
		ps.useFallback.Store(true)
		ps.wg.Add(1)
		go ps.runPollingLoop()
		return
	}
	ps.fifoMu.Lock()
	ps.fifo = fifo
	ps.fifoMu.Unlock()
	defer func() {
		ps.fifoMu.Lock()
		if ps.fifo == fifo {
			ps.fifo = nil
		}
		ps.fifoMu.Unlock()
		_ = fifo.Close()
	}()

	// Stop can race with opening the FIFO. If it already closed the stop
	// channel, do not enter a blocking read after publishing the descriptor.
	select {
	case <-stopCh:
		return
	case <-ctx.Done():
		return
	default:
	}

	err = ps.readOutputStream(ctx, stopCh, fifo)
	if err != nil && err != io.EOF && !strings.Contains(err.Error(), "file already closed") {
		log.Printf("pipe-pane: read error for %s: %v", ps.target, err)
	}
	select {
	case <-stopCh:
		return
	case <-ctx.Done():
		return
	default:
		// Unexpected stream termination, fallback to a full polling snapshot.
		log.Printf("pipe-pane: stream ended unexpectedly for %s, switching to fallback", ps.target)
		ps.useFallback.Store(true)
		ps.wg.Add(1)
		go ps.runPollingLoop()
	}
}

// readOutputStream reads bounded chunks, not newline-terminated strings: agent
// composers and progress indicators routinely write without a newline. The
// timer must be able to publish those bytes while the producer remains idle.
// The reader is closed and joined on every exit, including cancellation.
func (ps *PaneStreamer) readOutputStream(ctx context.Context, stopCh <-chan struct{}, reader io.ReadCloser) error {
	type readResult struct {
		data []byte
		err  error
	}
	readCh := make(chan readResult)
	readerDone := make(chan struct{})
	readerCtx, cancelReader := context.WithCancel(ctx)
	go func() {
		defer close(readerDone)
		chunk := make([]byte, 4096)
		for {
			n, err := reader.Read(chunk)
			if n > 0 || err != nil {
				result := readResult{data: append([]byte(nil), chunk[:n]...), err: err}
				select {
				case readCh <- result:
				case <-readerCtx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		cancelReader()
		_ = reader.Close()
		<-readerDone
	}()

	history := paneOutputHistory{
		maxBytes: ps.config.MaxBytesPerEvent,
		maxLines: ps.config.FallbackPollLines,
	}
	buffer := paneOutputBuffer{
		maxBytes: ps.config.MaxBytesPerEvent,
		maxLines: ps.config.MaxLinesPerEvent,
		emit: func(lines []string, continuesLine, partial bool) {
			history.append(lines, continuesLine)
			event := StreamEvent{
				Target: ps.target, Lines: lines, Seq: ps.nextSeq(), Timestamp: time.Now(),
				ContinuesLine: continuesLine, Partial: partial,
			}
			if partial || continuesLine {
				event.SnapshotLines = append([]string(nil), history.lines...)
			}
			ps.callback(event)
		},
	}
	defer buffer.flush(true)
	flushTicker := time.NewTicker(ps.config.FlushInterval)
	defer flushTicker.Stop()
	for {
		select {
		case <-stopCh:
			return nil
		case <-ctx.Done():
			return nil
		case <-flushTicker.C:
			buffer.flush(false)
		case result := <-readCh:
			buffer.write(result.data)
			if result.err != nil {
				return result.err
			}
		}
	}
}

// paneOutputBuffer preserves line boundaries while limiting both event bytes
// and lines. At most three incomplete UTF-8 bytes survive a timed flush; they
// are joined with the next read so JSON encoding cannot replace a split rune.
type paneOutputBuffer struct {
	data          []byte
	maxBytes      int
	maxLines      int
	newlines      int
	continuesLine bool
	emit          func(lines []string, continuesLine, partial bool)
}

func (b *paneOutputBuffer) write(data []byte) {
	for len(data) > 0 {
		n := min(len(data), b.maxBytes-len(b.data))
		for i, ch := range data[:n] {
			if ch == '\n' {
				b.newlines++
				if b.newlines == b.maxLines {
					n = i + 1
					break
				}
			}
		}
		b.data = append(b.data, data[:n]...)
		data = data[n:]
		if len(b.data) == b.maxBytes || b.newlines == b.maxLines {
			b.flush(false)
		}
	}
}

func (b *paneOutputBuffer) flush(final bool) {
	n := len(b.data)
	if n == 0 {
		return
	}
	if !final {
		start := n - 1
		for start > 0 && !utf8.RuneStart(b.data[start]) {
			start--
		}
		if !utf8.FullRune(b.data[start:]) {
			n = start
		}
	}
	if n == 0 {
		return
	}
	partial := b.data[n-1] != '\n'
	lines := strings.Split(string(b.data[:n]), "\n")
	if !partial {
		lines = lines[:len(lines)-1]
	}
	b.emit(lines, b.continuesLine, partial)
	b.continuesLine = partial
	copy(b.data, b.data[n:])
	b.data = b.data[:len(b.data)-n]
	b.newlines = 0 // Only an incomplete UTF-8 rune can remain.
}

// paneOutputHistory keeps a bounded tail for snapshot publication. It belongs
// to one read lifecycle, so restarting a stream cannot retain another stream's
// partial line. Fragments still use the same reader and ordering as full lines.
type paneOutputHistory struct {
	lines    []string
	maxBytes int
	maxLines int
}

func (h *paneOutputHistory) append(lines []string, continuesLine bool) {
	if len(lines) == 0 {
		return
	}
	if continuesLine && len(h.lines) > 0 {
		h.lines[len(h.lines)-1] += lines[0]
		lines = lines[1:]
	}
	h.lines = append(h.lines, lines...)
	trimmed := false
	if len(h.lines) > h.maxLines {
		h.lines = h.lines[len(h.lines)-h.maxLines:]
		trimmed = true
	}

	remaining := h.maxBytes
	for i := len(h.lines) - 1; i >= 0; i-- {
		if len(h.lines[i]) > remaining {
			start := len(h.lines[i]) - remaining
			for start < len(h.lines[i]) && !utf8.RuneStart(h.lines[i][start]) {
				start++
			}
			h.lines[i] = h.lines[i][start:]
			h.lines = h.lines[i:]
			trimmed = true
			break
		}
		remaining -= len(h.lines[i])
		if i > 0 {
			if remaining == 0 {
				h.lines = h.lines[i:]
				trimmed = true
				break
			}
			remaining-- // Newline separating this line from the preceding one.
		}
	}
	if trimmed {
		// Split lines and truncated suffixes share their source allocation.
		// Release dropped bytes as well as dropped slice entries so a tiny
		// retained tail cannot keep a much larger event or joined line alive.
		retained := make([]string, len(h.lines))
		for i, line := range h.lines {
			retained[i] = strings.Clone(line)
		}
		h.lines = retained
	}
}

// closeFIFO unblocks the reader during shutdown. The file is also closed by
// runFIFOReader, so closing here intentionally tolerates os.ErrClosed.
func (ps *PaneStreamer) closeFIFO() {
	ps.fifoMu.Lock()
	fifo := ps.fifo
	ps.fifoMu.Unlock()
	if fifo != nil {
		_ = fifo.Close()
	}
}

// runPollingLoop polls capture-pane as a fallback.
func (ps *PaneStreamer) runPollingLoop() {
	defer ps.wg.Done()

	ctx := ps.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	stopCh := ps.stopCh

	ticker := time.NewTicker(ps.config.FallbackPollInterval)
	defer ticker.Stop()

	log.Printf("pipe-pane: fallback polling started for %s (interval=%v)", ps.target, ps.config.FallbackPollInterval)

	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			captureCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			output, err := ps.client.CapturePaneOutputContext(captureCtx, ps.target, ps.config.FallbackPollLines)
			cancel()
			if err != nil {
				select {
				case <-stopCh:
					return
				default:
				}
				if ctx.Err() != nil {
					return
				}
				log.Printf("pipe-pane: capture-pane error for %s: %v", ps.target, err)
				continue
			}

			// Simple deduplication: skip if output hasn't changed
			hash := simpleHash(output)
			if hash == ps.lastHash {
				continue
			}
			ps.lastHash = hash

			lines := strings.Split(output, "\n")
			ps.callback(StreamEvent{
				Target:    ps.target,
				Lines:     lines,
				Seq:       ps.nextSeq(),
				Timestamp: time.Now(),
				IsFull:    true,
			})
		}
	}
}

// simpleHash computes a robust hash for deduplication.
func simpleHash(s string) string {
	h := fnv.New64a()
	h.Write([]byte(s))
	return fmt.Sprintf("%016x", h.Sum64())
}

// StreamManager manages multiple pane streamers.
type StreamManager struct {
	client    *Client
	config    PaneStreamerConfig
	callback  StreamCallback
	streamers map[string]*PaneStreamer
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewStreamManager creates a new stream manager.
func NewStreamManager(client *Client, callback StreamCallback, cfg PaneStreamerConfig) *StreamManager {
	if client == nil {
		client = DefaultClient
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &StreamManager{
		client:    client,
		config:    cfg,
		callback:  callback,
		streamers: make(map[string]*PaneStreamer),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// StartStream starts streaming for a pane. Idempotent.
func (sm *StreamManager) StartStream(target string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, exists := sm.streamers[target]; exists {
		return nil // Already streaming
	}

	streamer := NewPaneStreamer(sm.client, target, sm.callback, sm.config)
	if err := streamer.Start(sm.ctx); err != nil {
		return err
	}

	sm.streamers[target] = streamer
	log.Printf("stream_manager: started streaming for %s", target)
	return nil
}

// StopStream stops streaming for a pane.
func (sm *StreamManager) StopStream(target string) {
	sm.mu.Lock()
	streamer, exists := sm.streamers[target]
	if exists {
		delete(sm.streamers, target)
	}
	sm.mu.Unlock()

	if exists {
		streamer.Stop()
		log.Printf("stream_manager: stopped streaming for %s", target)
	}
}

// StopAll stops all streamers. The manager remains usable for later streams.
func (sm *StreamManager) StopAll() {
	sm.mu.Lock()
	cancel := sm.cancel
	streamers := make([]*PaneStreamer, 0, len(sm.streamers))
	for _, s := range sm.streamers {
		streamers = append(streamers, s)
	}
	sm.streamers = make(map[string]*PaneStreamer)
	sm.ctx, sm.cancel = context.WithCancel(context.Background())
	sm.mu.Unlock()

	cancel()

	for _, s := range streamers {
		s.Stop()
	}
	log.Printf("stream_manager: stopped all streamers")
}

// ListActive returns targets of all active streamers.
func (sm *StreamManager) ListActive() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	targets := make([]string, 0, len(sm.streamers))
	for target := range sm.streamers {
		targets = append(targets, target)
	}
	return targets
}

// Stats returns streaming statistics.
func (sm *StreamManager) Stats() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	active := len(sm.streamers)
	pipePaneCount := 0
	fallbackCount := 0

	for _, s := range sm.streamers {
		if s.UsingFallback() {
			fallbackCount++
		} else {
			pipePaneCount++
		}
	}

	return map[string]interface{}{
		"active_streams":    active,
		"pipe_pane_count":   pipePaneCount,
		"fallback_count":    fallbackCount,
		"fifo_dir":          sm.config.FIFODir,
		"flush_interval_ms": sm.config.FlushInterval.Milliseconds(),
	}
}
