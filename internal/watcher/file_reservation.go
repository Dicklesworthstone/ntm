// Package watcher provides file watching with debouncing using fsnotify.
// file_reservation.go implements automatic file reservation based on pane output detection.
package watcher

import (
	"context"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	// DefaultPollIntervalReservation is the default polling interval for checking pane output.
	DefaultPollIntervalReservation = 10 * time.Second

	// DefaultIdleTimeout is how long a pane must be idle before releasing reservations.
	DefaultIdleTimeout = 10 * time.Minute

	// DefaultReservationTTL is the TTL for file reservations.
	DefaultReservationTTL = 15 * time.Minute

	// DefaultCaptureLinesReservation is the number of lines to capture for pattern detection.
	DefaultCaptureLinesReservation = 100
)

// PaneReservation tracks reservations made by a pane.
type PaneReservation struct {
	PaneID        string
	AgentName     string
	Files         []string
	ReservationID []int
	LastActivity  time.Time
	LastOutput    string // Hash or truncated output to detect changes
}

// FileReservationWatcher monitors pane output and automatically reserves files.
type FileReservationWatcher struct {
	client             *agentmail.Client
	projectDir         string
	agentName          string
	pollInterval       time.Duration
	idleTimeout        time.Duration
	reservationTTL     time.Duration
	captureLines       int
	activeReservations map[string]*PaneReservation // paneID -> reservation
	mu                 sync.Mutex
	cancelFunc         context.CancelFunc
	wg                 sync.WaitGroup
	debug              bool
	conflictCallback   ConflictCallback // Called when conflicts are detected
}

// FileReservationWatcherOption configures a FileReservationWatcher.
type FileReservationWatcherOption func(*FileReservationWatcher)

// WithWatcherClient sets the Agent Mail client.
func WithWatcherClient(client *agentmail.Client) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.client = client
	}
}

// WithProjectDir sets the project directory.
func WithProjectDir(dir string) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.projectDir = dir
	}
}

// WithAgentName sets the agent name for reservations.
func WithAgentName(name string) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.agentName = name
	}
}

// WithReservationPollInterval sets the polling interval.
func WithReservationPollInterval(d time.Duration) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		if d > 0 {
			w.pollInterval = d
		}
	}
}

// WithIdleTimeout sets the idle timeout for releasing reservations.
func WithIdleTimeout(d time.Duration) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		if d > 0 {
			w.idleTimeout = d
		}
	}
}

// WithReservationTTL sets the TTL for reservations.
func WithReservationTTL(d time.Duration) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		if d > 0 {
			w.reservationTTL = d
		}
	}
}

// WithDebug enables debug logging.
func WithDebug(debug bool) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.debug = debug
	}
}

// WithConflictCallback sets the callback for conflict notifications.
func WithConflictCallback(cb ConflictCallback) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.conflictCallback = cb
	}
}

// WithCaptureLines sets the number of lines to capture for pattern detection.
func WithCaptureLines(lines int) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		if lines > 0 {
			w.captureLines = lines
		}
	}
}

// NewFileReservationWatcher creates a new FileReservationWatcher.
func NewFileReservationWatcher(opts ...FileReservationWatcherOption) *FileReservationWatcher {
	w := &FileReservationWatcher{
		pollInterval:       DefaultPollIntervalReservation,
		idleTimeout:        DefaultIdleTimeout,
		reservationTTL:     DefaultReservationTTL,
		captureLines:       DefaultCaptureLinesReservation,
		activeReservations: make(map[string]*PaneReservation),
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// Start begins the file reservation watcher in a background goroutine.
func (w *FileReservationWatcher) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	w.cancelFunc = cancel

	w.wg.Add(1)
	go w.run(ctx)

	if w.debug {
		log.Printf("[FileReservationWatcher] Started with pollInterval=%v idleTimeout=%v", w.pollInterval, w.idleTimeout)
	}
}

// Stop halts the file reservation watcher and releases all reservations.
func (w *FileReservationWatcher) Stop() {
	if w.cancelFunc != nil {
		w.cancelFunc()
	}
	w.wg.Wait()

	// Release all reservations on stop
	w.releaseAllReservations()

	if w.debug {
		log.Printf("[FileReservationWatcher] Stopped")
	}
}

// run is the main polling loop.
func (w *FileReservationWatcher) run(ctx context.Context) {
	defer w.wg.Done()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkPaneOutputs(ctx)
			w.releaseIdleReservations(ctx)
		}
	}
}

// checkPaneOutputs scans all panes for file edits.
func (w *FileReservationWatcher) checkPaneOutputs(ctx context.Context) {
	// Get all sessions
	sessions, err := tmux.ListSessions()
	if err != nil {
		if w.debug {
			log.Printf("[FileReservationWatcher] Error listing sessions: %v", err)
		}
		return
	}

	for _, session := range sessions {
		for _, pane := range session.Panes {
			// Only monitor agent panes (Claude, Codex, Gemini)
			if pane.Type == tmux.AgentUser {
				continue
			}

			w.checkPaneForFileEdits(ctx, session.Name, pane)
		}
	}
}

// checkPaneForFileEdits checks a single pane for file edits and reserves files.
func (w *FileReservationWatcher) checkPaneForFileEdits(ctx context.Context, sessionName string, pane tmux.Pane) {
	// Capture recent output
	output, err := tmux.CapturePaneOutputContext(ctx, pane.ID, w.captureLines)
	if err != nil {
		if w.debug {
			log.Printf("[FileReservationWatcher] Error capturing output from pane %s: %v", pane.ID, err)
		}
		return
	}

	// Detect file edits using local extraction (avoiding import cycle with robot package)
	agentType := mapAgentTypeToPatternAgent(pane.Type)
	files := extractEditedFiles(output, agentType)

	if len(files) > 0 {
		w.OnFileEdit(ctx, sessionName, pane, files)
	}
}

// mapAgentTypeToPatternAgent converts tmux.AgentType to pattern agent string.
func mapAgentTypeToPatternAgent(agentType tmux.AgentType) string {
	switch agentType {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "codex"
	case tmux.AgentGemini:
		return "gemini"
	default:
		return "*"
	}
}

// OnFileEdit handles detected file edits by reserving files.
func (w *FileReservationWatcher) OnFileEdit(ctx context.Context, sessionName string, pane tmux.Pane, files []string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.client == nil || w.projectDir == "" {
		return
	}

	// Get or create reservation record for this pane
	reservation, exists := w.activeReservations[pane.ID]
	if !exists {
		// Determine agent name for reservations
		agentName := w.agentName
		if agentName == "" {
			agentName = sessionName + "_" + pane.ID
		}
		reservation = &PaneReservation{
			PaneID:       pane.ID,
			AgentName:    agentName,
			Files:        nil,
			LastActivity: time.Now(),
		}
		w.activeReservations[pane.ID] = reservation
	}

	// Find new files not already reserved by this pane
	newFiles := make([]string, 0)
	existingFiles := make(map[string]bool)
	for _, f := range reservation.Files {
		existingFiles[f] = true
	}
	for _, f := range files {
		if !existingFiles[f] {
			newFiles = append(newFiles, f)
		}
	}

	if len(newFiles) == 0 {
		// No new files, just update activity time
		reservation.LastActivity = time.Now()
		return
	}

	// Reserve new files
	opts := agentmail.FileReservationOptions{
		ProjectKey: w.projectDir,
		AgentName:  reservation.AgentName,
		Paths:      newFiles,
		TTLSeconds: int(w.reservationTTL.Seconds()),
		Exclusive:  true,
		Reason:     "Auto-reserved by FileReservationWatcher: detected file edit",
	}

	result, err := w.client.ReservePaths(ctx, opts)
	if err != nil {
		// Reservation conflict is expected when another agent has the file
		if w.debug {
			log.Printf("[FileReservationWatcher] Reservation error for pane %s: %v", pane.ID, err)
		}
		// Still update activity time even on conflict
		reservation.LastActivity = time.Now()
		return
	}

	// Track granted reservations
	for _, granted := range result.Granted {
		reservation.Files = append(reservation.Files, granted.PathPattern)
		reservation.ReservationID = append(reservation.ReservationID, granted.ID)
	}
	reservation.LastActivity = time.Now()

	if w.debug && len(result.Granted) > 0 {
		log.Printf("[FileReservationWatcher] Reserved %d files for pane %s: %v",
			len(result.Granted), pane.ID, newFiles)
	}

	// Emit conflicts to callback
	if len(result.Conflicts) > 0 {
		if w.debug {
			log.Printf("[FileReservationWatcher] Conflicts for pane %s: %v", pane.ID, result.Conflicts)
		}

		if w.conflictCallback != nil {
			for _, conflict := range result.Conflicts {
				fc := FileConflict{
					Path:           conflict.Path,
					RequestorAgent: reservation.AgentName,
					RequestorPane:  pane.ID,
					SessionName:    sessionName,
					Holders:        conflict.Holders,
					DetectedAt:     time.Now(),
				}

				// Try to get additional info about the conflicting reservation
				if w.client != nil && len(conflict.Holders) > 0 {
					reservations, err := w.client.ListReservations(ctx, w.projectDir, "", true)
					if err == nil {
						for _, r := range reservations {
							// Match by path pattern and holder
							if r.PathPattern == conflict.Path {
								for _, holder := range conflict.Holders {
									if r.AgentName == holder {
										reservedSince := r.CreatedTS.Time
										fc.ReservedSince = &reservedSince
										expiresAt := r.ExpiresTS.Time
										fc.ExpiresAt = &expiresAt
										fc.HolderReservationIDs = append(fc.HolderReservationIDs, r.ID)
									}
								}
							}
						}
					}
				}

				w.conflictCallback(fc)
			}
		}
	}
}

// releaseIdleReservations releases reservations for panes that have been idle.
func (w *FileReservationWatcher) releaseIdleReservations(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.client == nil {
		return
	}

	now := time.Now()
	var toDelete []string

	for paneID, reservation := range w.activeReservations {
		if now.Sub(reservation.LastActivity) > w.idleTimeout {
			// Release reservations for this idle pane
			if len(reservation.ReservationID) > 0 {
				err := w.client.ReleaseReservations(ctx, w.projectDir, reservation.AgentName, reservation.Files, reservation.ReservationID)
				if err != nil && w.debug {
					log.Printf("[FileReservationWatcher] Error releasing reservations for pane %s: %v", paneID, err)
				} else if w.debug {
					log.Printf("[FileReservationWatcher] Released %d reservations for idle pane %s",
						len(reservation.ReservationID), paneID)
				}
			}
			toDelete = append(toDelete, paneID)
		}
	}

	// Clean up
	for _, paneID := range toDelete {
		delete(w.activeReservations, paneID)
	}
}

// releaseAllReservations releases all tracked reservations.
func (w *FileReservationWatcher) releaseAllReservations() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.client == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for paneID, reservation := range w.activeReservations {
		if len(reservation.ReservationID) > 0 {
			err := w.client.ReleaseReservations(ctx, w.projectDir, reservation.AgentName, reservation.Files, reservation.ReservationID)
			if err != nil && w.debug {
				log.Printf("[FileReservationWatcher] Error releasing reservations for pane %s: %v", paneID, err)
			}
		}
	}

	w.activeReservations = make(map[string]*PaneReservation)
}

// GetActiveReservations returns a copy of all active reservations.
func (w *FileReservationWatcher) GetActiveReservations() map[string]*PaneReservation {
	w.mu.Lock()
	defer w.mu.Unlock()

	result := make(map[string]*PaneReservation, len(w.activeReservations))
	for k, v := range w.activeReservations {
		// Copy the reservation
		copied := *v
		copied.Files = make([]string, len(v.Files))
		copy(copied.Files, v.Files)
		copied.ReservationID = make([]int, len(v.ReservationID))
		copy(copied.ReservationID, v.ReservationID)
		result[k] = &copied
	}
	return result
}

// RenewReservations extends the TTL of all active reservations.
func (w *FileReservationWatcher) RenewReservations(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.client == nil {
		return nil
	}

	extendSeconds := int(w.reservationTTL.Seconds())
	for _, reservation := range w.activeReservations {
		if len(reservation.ReservationID) > 0 {
			_, err := w.client.RenewReservations(ctx, agentmail.RenewReservationsOptions{
				ProjectKey:    w.projectDir,
				AgentName:     reservation.AgentName,
				ExtendSeconds: extendSeconds,
			})
			if err != nil && w.debug {
				log.Printf("[FileReservationWatcher] Error renewing reservations for pane %s: %v",
					reservation.PaneID, err)
			}
		}
	}
	return nil
}

// =============================================================================
// File Edit Detection (local implementation to avoid import cycle with robot)
// =============================================================================

// filePathPatterns are specialized patterns for extracting file paths from agent output.
var filePathPatterns = map[string][]*regexp.Regexp{
	"claude": {
		// JSON tool call patterns (highest priority)
		regexp.MustCompile(`"file_path"\s*:\s*"([^"]+)"`),
		// Prose patterns
		regexp.MustCompile(`(?i)(?:edited|modified)\s+(?:file:?\s+)?([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?i)created\s+(?:file:?\s+)?([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?i)writing\s+(?:to\s+)?(?:file:?\s+)?([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?i)wrote\s+(?:to\s+)?(?:file:?\s+)?([^\s,]+\.\w+)`),
	},
	"codex": {
		regexp.MustCompile(`(?i)(?:editing|modified)\s+(?:file:?\s+)?([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?i)created\s+(?:file:?\s+)?([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?i)writing\s+(?:to\s+)?(?:file:?\s+)?([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?i)wrote\s+(?:to\s+)?(?:file:?\s+)?([^\s,]+\.\w+)`),
	},
	"gemini": {
		regexp.MustCompile(`(?i)^Writing:\s*(.+)$`),
		regexp.MustCompile(`(?i)^Editing:\s*(.+)$`),
		regexp.MustCompile(`(?i)^Created:\s*(.+)$`),
		regexp.MustCompile(`(?i)(?:edited|modified)\s+(?:file:?\s+)?([^\s,]+\.\w+)`),
	},
	"*": {
		// Generic patterns as fallback
		regexp.MustCompile(`(?i)^(?:✓\s*)?(?:edited|modified):?\s+([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?i)^(?:✓\s*)?created:?\s+([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?i)^(?:✓\s*)?wrote:?\s+([^\s,]+\.\w+)`),
		// Path-like patterns (match absolute or relative paths ending in file extension)
		regexp.MustCompile(`(?:^|[\s:"'])((?:/[^/\s]+)+\.\w+)`),
		regexp.MustCompile(`(?:^|[\s:"'])(\./[^\s]+\.\w+)`),
		regexp.MustCompile(`(?:^|[\s:"'])([a-zA-Z_][a-zA-Z0-9_/-]*\.\w+)`),
	},
}

// extractEditedFiles extracts file paths from agent output.
// It returns a list of files that appear to have been edited/written by the agent.
func extractEditedFiles(output string, agentType string) []string {
	seen := make(map[string]bool)
	var files []string

	// Get patterns for specific agent type
	patterns, ok := filePathPatterns[agentType]
	if ok {
		for _, re := range patterns {
			matches := re.FindAllStringSubmatch(output, -1)
			for _, match := range matches {
				if len(match) > 1 {
					path := cleanFilePathForReservation(match[1])
					if isValidFilePathForReservation(path) && !seen[path] {
						seen[path] = true
						files = append(files, path)
					}
				}
			}
		}
	}

	// Also try generic patterns
	if agentType != "*" {
		genericPatterns := filePathPatterns["*"]
		for _, re := range genericPatterns {
			matches := re.FindAllStringSubmatch(output, -1)
			for _, match := range matches {
				if len(match) > 1 {
					path := cleanFilePathForReservation(match[1])
					if isValidFilePathForReservation(path) && !seen[path] {
						seen[path] = true
						files = append(files, path)
					}
				}
			}
		}
	}

	return files
}

// cleanFilePathForReservation normalizes a file path extracted from output.
func cleanFilePathForReservation(path string) string {
	// Trim surrounding quotes and whitespace
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"'`)
	path = strings.TrimSpace(path)

	// Remove trailing punctuation that might have been captured
	path = strings.TrimRight(path, ".,;:!?")

	return path
}

// isValidFilePathForReservation checks if a path looks like a valid file path.
func isValidFilePathForReservation(path string) bool {
	if path == "" {
		return false
	}

	// Must contain a file extension
	if !strings.Contains(path, ".") {
		return false
	}

	// Check for invalid characters
	invalidChars := []string{"<", ">", "|", "*", "?", "\n", "\r", "\t"}
	for _, c := range invalidChars {
		if strings.Contains(path, c) {
			return false
		}
	}

	// Must end with a valid extension (alphanumeric)
	lastDot := strings.LastIndex(path, ".")
	if lastDot == -1 || lastDot == len(path)-1 {
		return false
	}
	ext := path[lastDot+1:]
	if len(ext) > 10 || len(ext) < 1 {
		return false
	}
	for _, c := range ext {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) {
			return false
		}
	}

	// Avoid matching common false positives
	falsePositives := []string{
		"example.com", "localhost.test", "api.v1", "v1.0", "v2.0",
	}
	for _, fp := range falsePositives {
		if strings.HasSuffix(strings.ToLower(path), fp) && !strings.Contains(path, "/") {
			return false
		}
	}

	return true
}
