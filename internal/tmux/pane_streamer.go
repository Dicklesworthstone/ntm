// Package tmux provides pane output streaming using pipe-pane with polling fallback.
package tmux

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StreamEvent represents a pane output event.
type StreamEvent struct {
	Target    string    // Pane target (e.g., "session:0")
	Lines     []string  // Output lines
	Seq       int64     // Sequence number
	Timestamp time.Time // When the event was captured
	IsFull    bool      // True if this is a full capture (polling), false if incremental (pipe)
}

// StreamCallback is called when new output is available.
type StreamCallback func(event StreamEvent)

// PaneStreamerConfig configures the pane streamer.
type PaneStreamerConfig struct {
	// FIFODir is where named pipes are created (default: /tmp/ntm_pane_streams)
	FIFODir string

	// MaxLinesPerEvent limits lines per WebSocket event (default: 100)
	MaxLinesPerEvent int

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
		FIFODir:              "/tmp/ntm_pane_streams",
		MaxLinesPerEvent:     100,
		FlushInterval:        50 * time.Millisecond,
		FallbackPollInterval: 500 * time.Millisecond,
		FallbackPollLines:    LinesHealthCheck,
	}
}

// PaneStreamer streams output from a single pane.
type PaneStreamer struct {
	client   *Client
	target   string // e.g., "session:0"
	config   PaneStreamerConfig
	callback StreamCallback

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	fifoPath    string
	seq         int64
	useFallback atomic.Bool

	mu       sync.Mutex
	running  bool
	lastHash string // Hash of last captured output for deduplication
}

// NewPaneStreamer creates a streamer for the given pane target.
func NewPaneStreamer(client *Client, target string, callback StreamCallback, cfg PaneStreamerConfig) *PaneStreamer {
	if cfg.FIFODir == "" {
		cfg.FIFODir = "/tmp/ntm_pane_streams"
	}
	if cfg.MaxLinesPerEvent <= 0 {
		cfg.MaxLinesPerEvent = 100
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
func (ps *PaneStreamer) Start(ctx context.Context) error {
	ps.mu.Lock()
	if ps.running {
		ps.mu.Unlock()
		return fmt.Errorf("streamer already running for %s", ps.target)
	}
	ps.running = true
	ps.mu.Unlock()

	ps.ctx, ps.cancel = context.WithCancel(ctx)

	// Ensure FIFO directory exists
	if err := os.MkdirAll(ps.config.FIFODir, 0755); err != nil {
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
	ps.mu.Lock()
	if !ps.running {
		ps.mu.Unlock()
		return
	}
	ps.running = false
	ps.mu.Unlock()

	if ps.cancel != nil {
		ps.cancel()
	}

	// Stop pipe-pane
	if ps.fifoPath != "" {
		_ = ps.client.RunSilent("pipe-pane", "-t", ps.target)
		_ = os.Remove(ps.fifoPath)
	}

	ps.wg.Wait()
}

// Target returns the pane target.
func (ps *PaneStreamer) Target() string {
	return ps.target
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
	// Create unique FIFO path
	safeTarget := strings.ReplaceAll(ps.target, ":", "_")
	safeTarget = strings.ReplaceAll(safeTarget, "/", "_")
	ps.fifoPath = filepath.Join(ps.config.FIFODir, fmt.Sprintf("pane_%s_%d.fifo", safeTarget, os.Getpid()))

	// Create FIFO (named pipe)
	if err := createFIFO(ps.fifoPath); err != nil {
		return fmt.Errorf("create fifo: %w", err)
	}

	// Start the pipe-pane command
	// Note: pipe-pane runs the command in a shell, output goes to the command's stdin
	catCmd := pipePaneCatCommand(ps.fifoPath)
	if err := ps.client.RunSilent("pipe-pane", "-t", ps.target, catCmd); err != nil {
		os.Remove(ps.fifoPath)
		return fmt.Errorf("pipe-pane: %w", err)
	}

	log.Printf("pipe-pane: attached to %s via %s", ps.target, ps.fifoPath)

	// Start reader goroutine
	ps.wg.Add(1)
	go ps.runFIFOReader()

	return nil
}

// runFIFOReader reads from the FIFO and emits events.
func (ps *PaneStreamer) runFIFOReader() {
	defer ps.wg.Done()

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
	defer fifo.Close()

	reader := bufio.NewReader(fifo)
	var lineBuf []string
	flushTicker := time.NewTicker(ps.config.FlushInterval)
	defer flushTicker.Stop()

	flushLines := func() {
		if len(lineBuf) == 0 {
			return
		}
		ps.callback(StreamEvent{
			Target:    ps.target,
			Lines:     lineBuf,
			Seq:       ps.nextSeq(),
			Timestamp: time.Now(),
			IsFull:    false,
		})
		lineBuf = nil
	}

	for {
		select {
		case <-ps.ctx.Done():
			flushLines()
			return
		case <-flushTicker.C:
			flushLines()
		default:
			// Non-blocking read with short timeout
			fifo.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF || os.IsTimeout(err) {
					continue
				}
				// Check if context is done
				if ps.ctx.Err() != nil {
					flushLines()
					return
				}
				log.Printf("pipe-pane: read error for %s: %v", ps.target, err)
				continue
			}

			lineBuf = append(lineBuf, strings.TrimSuffix(line, "\n"))

			// Flush if buffer is full
			if len(lineBuf) >= ps.config.MaxLinesPerEvent {
				flushLines()
			}
		}
	}
}

// runPollingLoop polls capture-pane as a fallback.
func (ps *PaneStreamer) runPollingLoop() {
	defer ps.wg.Done()

	ticker := time.NewTicker(ps.config.FallbackPollInterval)
	defer ticker.Stop()

	log.Printf("pipe-pane: fallback polling started for %s (interval=%v)", ps.target, ps.config.FallbackPollInterval)

	for {
		select {
		case <-ps.ctx.Done():
			return
		case <-ticker.C:
			output, err := ps.client.CapturePaneOutputContext(ps.ctx, ps.target, ps.config.FallbackPollLines)
			if err != nil {
				if ps.ctx.Err() != nil {
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

// simpleHash computes a simple hash for deduplication.
func simpleHash(s string) string {
	// Use length + first 32 chars + last 32 chars as a quick hash
	if len(s) < 64 {
		return s
	}
	return fmt.Sprintf("%d:%s:%s", len(s), s[:32], s[len(s)-32:])
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

// StopAll stops all streamers.
func (sm *StreamManager) StopAll() {
	sm.cancel()

	sm.mu.Lock()
	streamers := make([]*PaneStreamer, 0, len(sm.streamers))
	for _, s := range sm.streamers {
		streamers = append(streamers, s)
	}
	sm.streamers = make(map[string]*PaneStreamer)
	sm.mu.Unlock()

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
