// Package archive provides background archiving of agent output for CASS indexing.
// The Archiver captures pane content incrementally and writes CASS-compatible JSONL.
package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
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

	// DefaultMaxFileBytes bounds each archive chunk. A single larger record
	// is kept intact in its own chunk rather than splitting a JSONL record.
	DefaultMaxFileBytes int64 = 64 * 1024 * 1024
)

var archiveFilenamePattern = regexp.MustCompile(`^(.+)_(\d{4}-\d{2}-\d{2})(?:_(\d{6}\.\d{9})-\d+)?\.jsonl$`)

// ParseFilename identifies both historical daily archives and bounded chunks.
// The date/time suffix is anchored at the end so date-like session names keep
// their full identity. Timestamps use the writer's local calendar convention.
func ParseFilename(name string) (string, time.Time, bool) {
	matches := archiveFilenamePattern.FindStringSubmatch(name)
	if len(matches) != 4 {
		return "", time.Time{}, false
	}
	layout, value := "2006-01-02", matches[2]
	if matches[3] != "" {
		layout += "_150405.000000000"
		value += "_" + matches[3]
	}
	ts, err := time.ParseInLocation(layout, value, time.Local)
	if err != nil {
		return "", time.Time{}, false
	}
	return matches[1], ts, true
}

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
	fileDate        string
	fileBytes       int64
	maxFileBytes    int64
	writeErr        error
	started         time.Time
	totalRecords    int
	onRecord        func(*ArchiveRecord) // Optional callback for testing
	getPanes        func(context.Context, string) ([]tmux.Pane, error)
	writeFile       func(*os.File, []byte) (int, error)
}

// ArchiverOptions configures the Archiver.
type ArchiverOptions struct {
	SessionName     string
	OutputDir       string
	Interval        time.Duration
	LinesPerCapture int
	MaxFileBytes    int64                // Zero uses DefaultMaxFileBytes; negative values are invalid.
	OnRecord        func(*ArchiveRecord) // Callback when record is written
}

// DefaultArchiverOptions returns sensible defaults.
func DefaultArchiverOptions(sessionName string) ArchiverOptions {
	return ArchiverOptions{
		SessionName:     sessionName,
		OutputDir:       util.ExpandPath(DefaultOutputDir),
		Interval:        DefaultInterval,
		LinesPerCapture: DefaultLinesPerCapture,
		MaxFileBytes:    DefaultMaxFileBytes,
	}
}

// NewArchiver creates a new Archiver.
func NewArchiver(opts ArchiverOptions) (*Archiver, error) {
	if strings.TrimSpace(opts.SessionName) == "" {
		return nil, fmt.Errorf("session name required")
	}
	if opts.OutputDir == "" {
		opts.OutputDir = util.ExpandPath(DefaultOutputDir)
	}
	if opts.Interval < 0 {
		return nil, fmt.Errorf("interval must be non-negative")
	}
	if opts.LinesPerCapture < 0 {
		return nil, fmt.Errorf("lines per capture must be non-negative")
	}
	if opts.MaxFileBytes < 0 {
		return nil, fmt.Errorf("maximum archive file bytes must be non-negative")
	}
	if opts.Interval == 0 {
		opts.Interval = DefaultInterval
	}
	if opts.LinesPerCapture == 0 {
		opts.LinesPerCapture = DefaultLinesPerCapture
	}
	if opts.MaxFileBytes == 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}

	// Ensure output directory exists
	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("creating archive directory: %w", err)
	}

	a := &Archiver{
		sessionName:     opts.SessionName,
		outputDir:       opts.OutputDir,
		interval:        opts.Interval,
		linesPerCapture: opts.LinesPerCapture,
		paneStates:      make(map[int]*PaneState),
		maxFileBytes:    opts.MaxFileBytes,
		started:         time.Now(),
		onRecord:        opts.OnRecord,
		getPanes:        tmux.GetPanesContext,
	}
	file, err := a.openChunk(a.started)
	if err != nil {
		return nil, fmt.Errorf("opening archive file: %w", err)
	}
	a.file = file
	a.fileDate = a.started.Format("2006-01-02")
	return a, nil
}

// Run starts the archive loop. It blocks until the context is cancelled.
func (a *Archiver) Run(ctx context.Context) error {
	if a.interval <= 0 {
		return fmt.Errorf("archive interval must be positive")
	}

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
	if err := tmux.ValidateSessionName(a.sessionName); err != nil {
		return fmt.Errorf("validating session name: %w", err)
	}

	panes, err := a.getPanes(ctx, a.sessionName)
	if err != nil {
		return fmt.Errorf("getting session panes: %w", err)
	}

	for _, pane := range panes {
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
	target := fmt.Sprintf("%s:%d.%d", a.sessionName, pane.WindowIndex, pane.Index)

	// Capture content
	content, err := tmux.DefaultClient.CapturePaneOutputContext(ctx, target, a.linesPerCapture)
	if err != nil {
		return fmt.Errorf("capturing pane: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

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
		Sequence:  state.Sequence + 1,
	}

	// Write record
	if err := a.writeRecord(record); err != nil {
		return fmt.Errorf("writing record: %w", err)
	}
	// A failed write or rotation must not consume this capture. The next
	// interval retries the still-unarchived content with the same sequence.
	state.LastHash = contentHash
	state.LastContent = content
	state.LastCapture = record.Timestamp
	state.Sequence = record.Sequence
	state.TotalLines += record.Lines

	return nil
}

// openChunk creates a file owned exclusively by this writer. Never append to
// an earlier chunk: monitor restarts must not make a retired file live again.
func (a *Archiver) openChunk(now time.Time) (*os.File, error) {
	pattern := fmt.Sprintf("%s_%s-*.jsonl", util.SanitizeFilename(a.sessionName), now.Format("2006-01-02_150405.000000000"))
	return os.CreateTemp(a.outputDir, pattern)
}

// rotateChunk hands the writer over at a complete-record boundary. The old
// file stays open if preparing the successor fails. Once the handoff is
// logged, no further write by this archiver can reach the previous file.
// The caller holds a.mu.
func (a *Archiver) rotateChunk(now time.Time) error {
	if err := a.file.Sync(); err != nil {
		return fmt.Errorf("syncing previous archive chunk: %w", err)
	}
	next, err := a.openChunk(now)
	if err != nil {
		return fmt.Errorf("opening next archive chunk: %w", err)
	}
	previous := a.file
	previousBytes := a.fileBytes
	closeErr := previous.Close()
	// Close invalidates the previous descriptor even when it returns an error.
	// Keep the prepared successor so the next capture can retry safely.
	a.file = next
	a.fileDate = now.Format("2006-01-02")
	a.fileBytes = 0
	if closeErr != nil {
		return fmt.Errorf("closing previous archive chunk %s: %w", previous.Name(), closeErr)
	}
	slog.Info("archive file rotated", "session", a.sessionName,
		"closed_path", previous.Name(), "closed_bytes", previousBytes, "active_path", next.Name())
	return nil
}

// writeRecord writes one complete JSONL record, rotating before it would
// exceed the current chunk's budget. The capture loop holds a.mu.
func (a *Archiver) writeRecord(record *ArchiveRecord) error {
	if a.file == nil {
		return os.ErrClosed
	}
	if a.writeErr != nil {
		return a.writeErr
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	now := time.Now()
	if now.Format("2006-01-02") != a.fileDate ||
		(a.fileBytes > 0 && int64(len(data)) > a.maxFileBytes-a.fileBytes) {
		if err := a.rotateChunk(now); err != nil {
			return err
		}
	}
	var n int
	if a.writeFile != nil {
		n, err = a.writeFile(a.file, data)
	} else {
		n, err = a.file.Write(data)
	}
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		if n > 0 {
			// A partial append (for example ENOSPC) is not a JSONL record.
			// Only this writer owns the unique chunk, so remove just this
			// uncommitted attempt before allowing the capture to retry.
			truncateErr := a.file.Truncate(a.fileBytes)
			_, seekErr := a.file.Seek(a.fileBytes, io.SeekStart)
			if rollbackErr := errors.Join(truncateErr, seekErr); rollbackErr != nil {
				// Never append another record to an unrepairable partial line.
				a.writeErr = errors.Join(err, fmt.Errorf("restoring archive record boundary: %w", rollbackErr))
				return a.writeErr
			}
		}
		return err
	}
	a.fileBytes += int64(n)
	a.totalRecords++

	// Call optional callback
	if a.onRecord != nil {
		a.onRecord(record)
	}

	return nil
}

// flush syncs the archive file.
func (a *Archiver) flush() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.flushLocked()
}

func (a *Archiver) flushLocked() error {
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
		flushErr := a.flushLocked()
		closeErr := a.file.Close()
		a.file = nil
		return errors.Join(flushErr, closeErr)
	}
	return nil
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
	// Set max line size for large terminal outputs (10MB), start with 64KB
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// findOverlap finds the index in curr where new content starts after prev.
func findOverlap(prev, curr []string) int {
	if len(prev) == 0 || len(curr) == 0 {
		return 0
	}

	// Look for the largest suffix of prev anywhere in curr. Incremental tmux
	// captures usually place the overlap at curr[0], but a partially redrawn
	// prompt or status line can precede the repeated tail.
	maxCheck := min(len(prev), 50) // Don't check too many lines
	for suffixLen := maxCheck; suffixLen > 0; suffixLen-- {
		suffix := prev[len(prev)-suffixLen:]
		if idx := indexOfLines(curr, suffix); idx >= 0 {
			return idx + suffixLen
		}
	}

	return 0 // All content is new
}

func indexOfLines(lines, needle []string) int {
	if len(needle) == 0 || len(lines) < len(needle) {
		return -1
	}
	for start := 0; start <= len(lines)-len(needle); start++ {
		if startsWithLines(lines[start:], needle) {
			return start
		}
	}
	return -1
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
