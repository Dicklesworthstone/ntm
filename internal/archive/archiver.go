// Package archive provides background archiving of agent output for CASS indexing.
// The Archiver captures pane content incrementally and writes CASS-compatible JSONL.
package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	// DefaultInterval is the default capture interval.
	DefaultInterval = 30 * time.Second

	// DefaultOutputDir is the default archive output directory.
	DefaultOutputDir = "~/.ntm/archive"

	// DefaultLinesPerCapture is how many lines to capture per interval.
	DefaultLinesPerCapture = 500
)

// ArchiveRecord represents a single CASS-compatible archive entry.
type ArchiveRecord struct {
	Session   string    `json:"session"`
	Pane      string    `json:"pane"`
	PaneIndex int       `json:"pane_index"`
	Agent     string    `json:"agent"`
	Model     string    `json:"model,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
	Lines     int       `json:"lines"`
	Sequence  int       `json:"sequence"` // Monotonic sequence number per pane
}

// PaneState tracks the state of a single pane for incremental capture.
type PaneState struct {
	LastHash    uint64 // Hash of last captured content for deduplication
	LastCapture time.Time
	Sequence    int
	TotalLines  int
	LastContent string // Last captured content for diff
}

// Archiver captures agent output for CASS indexing.
type Archiver struct {
	sessionName     string
	outputDir       string
	interval        time.Duration
	linesPerCapture int
	paneStates      map[int]*PaneState // Keyed by pane index
	mu              sync.RWMutex
	file            *os.File
	encoder         *json.Encoder
	started         time.Time
	totalRecords    int
	onRecord        func(*ArchiveRecord) // Optional callback for testing
}

// ArchiverOptions configures the Archiver.
type ArchiverOptions struct {
	SessionName     string
	OutputDir       string
	Interval        time.Duration
	LinesPerCapture int
	OnRecord        func(*ArchiveRecord) // Callback when record is written
}

// DefaultArchiverOptions returns sensible defaults.
func DefaultArchiverOptions(sessionName string) ArchiverOptions {
	return ArchiverOptions{
		SessionName:     sessionName,
		OutputDir:       util.ExpandPath(DefaultOutputDir),
		Interval:        DefaultInterval,
		LinesPerCapture: DefaultLinesPerCapture,
	}
}

// NewArchiver creates a new Archiver.
func NewArchiver(opts ArchiverOptions) (*Archiver, error) {
	if opts.SessionName == "" {
		return nil, fmt.Errorf("session name required")
	}
	if opts.OutputDir == "" {
		opts.OutputDir = util.ExpandPath(DefaultOutputDir)
	}
	if opts.Interval == 0 {
		opts.Interval = DefaultInterval
	}
	if opts.LinesPerCapture == 0 {
		opts.LinesPerCapture = DefaultLinesPerCapture
	}

	// Ensure output directory exists
	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("creating archive directory: %w", err)
	}

	// Create archive file (one per session, append mode)
	filename := fmt.Sprintf("%s_%s.jsonl", opts.SessionName, time.Now().Format("2006-01-02"))
	filePath := filepath.Join(opts.OutputDir, filename)

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening archive file: %w", err)
	}

	return &Archiver{
		sessionName:     opts.SessionName,
		outputDir:       opts.OutputDir,
		interval:        opts.Interval,
		linesPerCapture: opts.LinesPerCapture,
		paneStates:      make(map[int]*PaneState),
		file:            f,
		encoder:         json.NewEncoder(f),
		started:         time.Now(),
		onRecord:        opts.OnRecord,
	}, nil
}

// Run starts the archive loop. It blocks until the context is cancelled.
func (a *Archiver) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	// Initial capture
	if err := a.archiveNewContent(ctx); err != nil {
		// Log but continue - don't fail on first capture
		slog.Warn("archive initial capture error", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			// Final flush on shutdown
			if err := a.flush(); err != nil {
				slog.Warn("archive flush on shutdown error", "session", a.sessionName, "error", err)
			}
			return ctx.Err()
		case <-ticker.C:
			if err := a.archiveNewContent(ctx); err != nil {
				// Log but continue
				slog.Warn("archive capture error", "error", err)
			}
		}
	}
}

// archiveNewContent captures new content from all panes.
func (a *Archiver) archiveNewContent(ctx context.Context) error {
	// Get session info with panes
	session, err := tmux.GetSession(a.sessionName)
	if err != nil {
		return fmt.Errorf("getting session info: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, pane := range session.Panes {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Skip control pane (pane 1)
		if pane.Index == 1 {
			continue
		}

		// Skip user panes (no agent output)
		if pane.Type == tmux.AgentUser {
			continue
		}

		if err := a.capturePane(ctx, pane); err != nil {
			// Log but continue with other panes
			slog.Warn("archive pane capture error", "pane", pane.Index, "error", err)
		}
	}

	return nil
}

// capturePane captures new content from a single pane.
func (a *Archiver) capturePane(ctx context.Context, pane tmux.Pane) error {
	target := fmt.Sprintf("%s:1.%d", a.sessionName, pane.Index)

	// Capture content
	content, err := tmux.DefaultClient.CapturePaneOutputContext(ctx, target, a.linesPerCapture)
	if err != nil {
		return fmt.Errorf("capturing pane: %w", err)
	}

	// Get or create pane state
	state, ok := a.paneStates[pane.Index]
	if !ok {
		state = &PaneState{}
		a.paneStates[pane.Index] = state
	}

	// Check for new content using simple hash
	contentHash := simpleHash(content)
	if contentHash == state.LastHash && state.LastCapture.Add(a.interval*2).After(time.Now()) {
		// No new content and recent capture - skip
		return nil
	}

	// Find new content by diffing
	newContent := findNewContent(state.LastContent, content)
	if newContent == "" {
		// Update hash but no new record needed
		state.LastHash = contentHash
		state.LastContent = content
		return nil
	}

	// Update state
	state.LastHash = contentHash
	state.LastContent = content
	state.LastCapture = time.Now()
	state.Sequence++
	state.TotalLines += countLines(newContent)

	// Create record
	paneName := fmt.Sprintf("%s_%d", pane.Type, pane.Index)
	record := &ArchiveRecord{
		Session:   a.sessionName,
		Pane:      paneName,
		PaneIndex: pane.Index,
		Agent:     string(pane.Type),
		Model:     pane.Variant,
		Timestamp: time.Now().UTC(),
		Content:   newContent,
		Lines:     countLines(newContent),
		Sequence:  state.Sequence,
	}

	// Write record
	if err := a.writeRecord(record); err != nil {
		return fmt.Errorf("writing record: %w", err)
	}

	return nil
}

// writeRecord writes an archive record to the JSONL file.
func (a *Archiver) writeRecord(record *ArchiveRecord) error {
	if err := a.encoder.Encode(record); err != nil {
		return err
	}
	a.totalRecords++

	// Call optional callback
	if a.onRecord != nil {
		a.onRecord(record)
	}

	return nil
}

// flush syncs the archive file.
func (a *Archiver) flush() error {
	if a.file != nil {
		return a.file.Sync()
	}
	return nil
}

// Close closes the archiver and its file.
func (a *Archiver) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.file != nil {
		if err := a.flush(); err != nil {
			// Log but continue to close file
			slog.Warn("archive flush on close error", "session", a.sessionName, "error", err)
		}
		err := a.file.Close()
		a.file = nil
		return err
	}
	return nil
}

// Stats returns archive statistics.
func (a *Archiver) Stats() ArchiverStats {
	a.mu.RLock()
	defer a.mu.RUnlock()

	panesTracked := len(a.paneStates)
	totalLines := 0
	for _, state := range a.paneStates {
		totalLines += state.TotalLines
	}

	return ArchiverStats{
		Session:      a.sessionName,
		OutputDir:    a.outputDir,
		Started:      a.started,
		Duration:     time.Since(a.started),
		TotalRecords: a.totalRecords,
		PanesTracked: panesTracked,
		TotalLines:   totalLines,
	}
}

// ArchiverStats contains archiver statistics.
type ArchiverStats struct {
	Session      string        `json:"session"`
	OutputDir    string        `json:"output_dir"`
	Started      time.Time     `json:"started"`
	Duration     time.Duration `json:"duration"`
	TotalRecords int           `json:"total_records"`
	PanesTracked int           `json:"panes_tracked"`
	TotalLines   int           `json:"total_lines"`
}

// Helper functions

// simpleHash computes a simple hash of a string for change detection.
func simpleHash(s string) uint64 {
	var hash uint64 = 5381
	for _, c := range s {
		hash = ((hash << 5) + hash) + uint64(c)
	}
	return hash
}

// countLines counts lines in a string.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	count := 1
	for _, c := range s {
		if c == '\n' {
			count++
		}
	}
	return count
}

// findNewContent finds content in 'current' that wasn't in 'previous'.
// Uses a simple suffix-based approach: find the longest suffix of previous
// that is a prefix of current, and return everything after that.
func findNewContent(previous, current string) string {
	if previous == "" {
		return current
	}
	if current == "" {
		return ""
	}

	// Split into lines for comparison
	prevLines := splitLines(previous)
	currLines := splitLines(current)

	if len(currLines) == 0 {
		return ""
	}

	// Find overlap: look for where previous lines appear at the start of current
	// This handles scrolling terminal output
	overlapStart := findOverlap(prevLines, currLines)

	if overlapStart >= len(currLines) {
		return "" // No new content
	}

	return strings.Join(currLines[overlapStart:], "\n")
}

// splitLines splits a string into lines.
func splitLines(s string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// findOverlap finds the index in curr where new content starts after prev.
func findOverlap(prev, curr []string) int {
	if len(prev) == 0 {
		return 0
	}

	// Look for the last few lines of prev appearing at the start of curr
	// Check increasingly larger suffixes of prev
	maxCheck := min(len(prev), 50) // Don't check too many lines
	for suffixLen := maxCheck; suffixLen > 0; suffixLen-- {
		suffix := prev[len(prev)-suffixLen:]
		if startsWithLines(curr, suffix) {
			return suffixLen
		}
	}

	// No overlap found - check if curr is completely different
	// If first line of prev appears anywhere in curr, find new content after it
	for i, line := range curr {
		if line == prev[len(prev)-1] {
			return i + 1
		}
	}

	return 0 // All content is new
}

// startsWithLines checks if lines starts with prefix.
func startsWithLines(lines, prefix []string) bool {
	if len(lines) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if lines[i] != p {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
