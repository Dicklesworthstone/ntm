// Package watcher provides file watching with debouncing using fsnotify.
// file_reservation.go implements automatic file reservation based on pane output detection.
package watcher

import (
	"context"
	"errors"
	"fmt"
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

// AgentNameResolver resolves a registered Agent Mail identity for a tmux pane.
// Implementations should return the empty string to indicate that no identity
// is available and the watcher should skip the pane (rather than fabricate a
// name like the session name, which would not be a registered Agent Mail
// identity and would cause the Agent Mail server to reject reservation calls).
type AgentNameResolver func(paneID string) string

// FileReservationWatcher monitors pane output and automatically reserves files.
type FileReservationWatcher struct {
	client             *agentmail.Client
	projectDir         string
	agentName          string // legacy fallback only; real name comes from agentNameResolver
	sessionFilter      string
	pollInterval       time.Duration
	idleTimeout        time.Duration
	reservationTTL     time.Duration
	captureLines       int
	listAllPanes       func(ctx context.Context) (map[string][]tmux.Pane, error)
	capturePaneOutput  func(ctx context.Context, target string, lines int) (string, error)
	agentNameResolver  AgentNameResolver           // paneID -> registered agent name, or "" to skip
	paneOutputs        map[string]string           // paneID -> last captured output
	activeReservations map[string]*PaneReservation // paneID -> reservation
	lifecycleMu        sync.Mutex
	mu                 sync.Mutex
	stopCh             chan struct{}
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

// WithAgentName sets the legacy fallback agent name for reservations. Most
// callers should prefer WithAgentNameResolver; this option exists for tests
// and for the rare case where a single agent name is appropriate for every
// pane. When both are set, the resolver takes precedence; the fallback is
// only used if the resolver returns "".
func WithAgentName(name string) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.agentName = name
	}
}

// WithAgentNameResolver sets a per-pane resolver that returns the registered
// Agent Mail identity for a given tmux pane ID. The watcher calls this
// resolver before every reservation attempt. If the resolver returns "", the
// reservation is skipped (rather than being sent with a non-registered name,
// which would cause the Agent Mail server to respond with an error and drive
// the operator's error metrics red).
//
// See issue #107 for the original bug.
func WithAgentNameResolver(resolver AgentNameResolver) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.agentNameResolver = resolver
	}
}

// WithSessionFilter limits pane scanning to a single tmux session.
func WithSessionFilter(session string) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.sessionFilter = strings.TrimSpace(session)
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
		listAllPanes:       tmux.DefaultClient.GetAllPanesContext,
		capturePaneOutput:  tmux.CapturePaneOutputContext,
		paneOutputs:        make(map[string]string),
		activeReservations: make(map[string]*PaneReservation),
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// Start begins the file reservation watcher in a background goroutine.
func (w *FileReservationWatcher) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()

	w.mu.Lock()
	if w.stopCh != nil {
		w.mu.Unlock()
		if w.debug {
			log.Printf("[FileReservationWatcher] Start called while already running")
		}
		return
	}

	stopCh := make(chan struct{})
	w.stopCh = stopCh
	w.wg.Add(1) // Must be inside the lock to prevent race with Stop()+wg.Wait()
	w.mu.Unlock()

	go w.run(ctx, stopCh)

	if w.debug {
		log.Printf("[FileReservationWatcher] Started with pollInterval=%v idleTimeout=%v", w.pollInterval, w.idleTimeout)
	}
}

// Stop halts the file reservation watcher and releases all reservations.
func (w *FileReservationWatcher) Stop() {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()

	w.mu.Lock()
	stopCh := w.stopCh
	w.stopCh = nil
	w.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	w.wg.Wait()

	// Release all reservations on stop
	w.releaseAllReservations()

	if w.debug {
		log.Printf("[FileReservationWatcher] Stopped")
	}
}

// run is the main polling loop.
func (w *FileReservationWatcher) run(ctx context.Context, stopCh <-chan struct{}) {
	defer w.wg.Done()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
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
	listAllPanes := w.listAllPanes
	if listAllPanes == nil {
		listAllPanes = tmux.DefaultClient.GetAllPanesContext
	}

	// ListSessions only returns session metadata; it does not populate Session.Panes.
	// The watcher needs the pane-native API to actually inspect agent output.
	panesBySession, err := listAllPanes(ctx)
	if err != nil {
		if w.debug {
			log.Printf("[FileReservationWatcher] Error listing panes: %v", err)
		}
		return
	}

	for sessionName, panes := range panesBySession {
		if w.sessionFilter != "" && sessionName != w.sessionFilter {
			continue
		}
		for _, pane := range panes {
			// Ignore shell/user panes; all agent panes are eligible for edit detection.
			if pane.Type == tmux.AgentUser {
				continue
			}

			w.checkPaneForFileEdits(ctx, sessionName, pane)
		}
	}
}

// checkPaneForFileEdits checks a single pane for file edits and reserves files.
func (w *FileReservationWatcher) checkPaneForFileEdits(ctx context.Context, sessionName string, pane tmux.Pane) {
	capturePaneOutput := w.capturePaneOutput
	if capturePaneOutput == nil {
		capturePaneOutput = tmux.CapturePaneOutputContext
	}

	// Capture recent output
	output, err := capturePaneOutput(ctx, pane.ID, w.captureLines)
	if err != nil {
		if w.debug {
			log.Printf("[FileReservationWatcher] Error capturing output from pane %s: %v", pane.ID, err)
		}
		return
	}

	now := time.Now()
	if !w.recordPaneOutput(pane.ID, output, now) {
		return
	}

	// Detect file edits using local extraction (avoiding import cycle with robot package)
	agentType := mapAgentTypeToPatternAgent(pane.Type)
	files := extractEditedFiles(output, agentType)

	if len(files) > 0 {
		w.onFileEdit(ctx, sessionName, pane, output, files, now)
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
	w.onFileEdit(ctx, sessionName, pane, "", files, time.Now())
}

func (w *FileReservationWatcher) onFileEdit(
	ctx context.Context,
	sessionName string,
	pane tmux.Pane,
	output string,
	files []string,
	now time.Time,
) {
	if w.client == nil || w.projectDir == "" {
		return
	}

	agentName, newFiles := w.prepareReservationAttempt(pane.ID, sessionName, output, files, now)
	if agentName == "" || len(newFiles) == 0 {
		// Either we have no registered identity for this pane yet (skip to
		// avoid a noisy 'unknown agent_name' server error) or we already
		// hold reservations covering every detected file.
		return
	}

	// Reserve new files
	opts := agentmail.FileReservationOptions{
		ProjectKey: w.projectDir,
		AgentName:  agentName,
		Paths:      newFiles,
		TTLSeconds: int(w.reservationTTL.Seconds()),
		Exclusive:  true,
		Reason:     "Auto-reserved by FileReservationWatcher: detected file edit",
	}

	result, err := w.client.ReservePaths(ctx, opts)
	if err != nil && !agentmail.IsReservationConflict(err) {
		if w.debug {
			log.Printf("[FileReservationWatcher] Reservation error for pane %s: %v", pane.ID, err)
		}
		return
	}

	w.recordReservationResult(pane.ID, result, now)

	if w.debug && result != nil && len(result.Granted) > 0 {
		log.Printf("[FileReservationWatcher] Reserved %d files for pane %s: %v",
			len(result.Granted), pane.ID, newFiles)
	}

	// Emit conflicts to callback
	if result != nil && len(result.Conflicts) > 0 {
		if w.debug {
			log.Printf("[FileReservationWatcher] Conflicts for pane %s: %v", pane.ID, result.Conflicts)
		}

		if w.conflictCallback != nil {
			for _, conflict := range w.buildConflictNotifications(ctx, sessionName, pane.ID, agentName, result.Conflicts) {
				w.conflictCallback(conflict)
			}
		}
	}
}

func (w *FileReservationWatcher) recordPaneOutput(paneID string, output string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if lastOutput, exists := w.paneOutputs[paneID]; exists && lastOutput == output {
		return false
	}
	w.paneOutputs[paneID] = output

	if reservation, exists := w.activeReservations[paneID]; exists {
		reservation.LastOutput = output
		reservation.LastActivity = now
	}

	return true
}

// releaseIdleReservations releases reservations for panes that have been idle.
func (w *FileReservationWatcher) releaseIdleReservations(ctx context.Context) {
	if w.client == nil {
		return
	}

	now := time.Now()
	var idleReservations []PaneReservation

	w.mu.Lock()
	for _, reservation := range w.activeReservations {
		if now.Sub(reservation.LastActivity) > w.idleTimeout {
			idleReservations = append(idleReservations, clonePaneReservation(*reservation))
		}
	}
	w.mu.Unlock()

	for _, reservation := range idleReservations {
		if len(reservation.ReservationID) == 0 {
			w.removeTrackedReservation(reservation)
			continue
		}
		releaseResult, err := w.client.ReleaseReservations(ctx, w.projectDir, reservation.AgentName, reservation.Files, reservation.ReservationID)
		if err != nil {
			if w.debug {
				log.Printf("[FileReservationWatcher] Error releasing reservations for pane %s: %v", reservation.PaneID, err)
			}
			continue
		}
		if !reservationReleaseComplete(reservation, releaseResult) {
			if w.debug {
				releasedCount := 0
				if releaseResult != nil {
					releasedCount = releaseResult.Released
				}
				log.Printf("[FileReservationWatcher] Incomplete release for idle pane %s: released %d of %d",
					reservation.PaneID, releasedCount, len(reservation.ReservationID))
			}
			continue
		}

		w.removeTrackedReservation(reservation)
		if w.debug {
			releasedCount := len(reservation.ReservationID)
			if releaseResult != nil && releaseResult.Released > 0 {
				releasedCount = releaseResult.Released
			}
			log.Printf("[FileReservationWatcher] Released %d reservations for idle pane %s",
				releasedCount, reservation.PaneID)
		}
	}
}

// releaseAllReservations releases all tracked reservations.
func (w *FileReservationWatcher) releaseAllReservations() {
	if w.client == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	w.mu.Lock()
	reservations := make([]PaneReservation, 0, len(w.activeReservations))
	for _, reservation := range w.activeReservations {
		reservations = append(reservations, clonePaneReservation(*reservation))
	}
	w.mu.Unlock()

	for _, reservation := range reservations {
		if len(reservation.ReservationID) == 0 {
			w.removeTrackedReservation(reservation)
			continue
		}

		releaseResult, err := w.client.ReleaseReservations(ctx, w.projectDir, reservation.AgentName, reservation.Files, reservation.ReservationID)
		if err != nil {
			if w.debug {
				log.Printf("[FileReservationWatcher] Error releasing reservations for pane %s: %v", reservation.PaneID, err)
			}
			continue
		}
		if !reservationReleaseComplete(reservation, releaseResult) {
			if w.debug {
				releasedCount := 0
				if releaseResult != nil {
					releasedCount = releaseResult.Released
				}
				log.Printf("[FileReservationWatcher] Incomplete release for pane %s during stop: released %d of %d",
					reservation.PaneID, releasedCount, len(reservation.ReservationID))
			}
			continue
		}

		w.removeTrackedReservation(reservation)
	}
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
	if w.client == nil {
		return nil
	}

	extendSeconds := int(w.reservationTTL.Seconds())

	w.mu.Lock()
	reservations := make([]PaneReservation, 0, len(w.activeReservations))
	for _, reservation := range w.activeReservations {
		if len(reservation.ReservationID) > 0 {
			reservations = append(reservations, clonePaneReservation(*reservation))
		}
	}
	w.mu.Unlock()

	var renewErrs []error
	for _, reservation := range reservations {
		if len(reservation.ReservationID) > 0 {
			renewResult, err := w.client.RenewReservations(ctx, agentmail.RenewReservationsOptions{
				ProjectKey:     w.projectDir,
				AgentName:      reservation.AgentName,
				ExtendSeconds:  extendSeconds,
				ReservationIDs: reservation.ReservationID,
			})
			if err != nil {
				if w.debug {
					log.Printf("[FileReservationWatcher] Error renewing reservations for pane %s: %v",
						reservation.PaneID, err)
				}
				renewErrs = append(renewErrs, fmt.Errorf("pane %s: %w", reservation.PaneID, err))
				continue
			}
			if renewResult == nil || renewResult.Renewed < len(reservation.ReservationID) {
				renewedCount := 0
				if renewResult != nil {
					renewedCount = renewResult.Renewed
				}
				err := fmt.Errorf("renewed %d of %d reservations for pane %s", renewedCount, len(reservation.ReservationID), reservation.PaneID)
				if w.debug {
					log.Printf("[FileReservationWatcher] %v", err)
				}
				renewErrs = append(renewErrs, err)
			}
		}
	}
	return errors.Join(renewErrs...)
}

// resolveAgentName looks up the registered Agent Mail identity for a pane,
// consulting (in order): (a) any existing reservation we already opened under
// that pane (cached canonical name), (b) the configured AgentNameResolver,
// (c) the legacy w.agentName fallback. Returns "" if no identity is known,
// in which case callers MUST skip the reservation attempt.
func (w *FileReservationWatcher) resolveAgentName(paneID string) string {
	// If we already opened a reservation for this pane, prefer the cached
	// canonical agent name rather than re-reading the identity file on
	// every poll cycle.
	w.mu.Lock()
	if existing, ok := w.activeReservations[paneID]; ok && existing != nil && existing.AgentName != "" {
		cached := existing.AgentName
		w.mu.Unlock()
		// If we also have a resolver, let it refresh the name (e.g. when a
		// pane respawns with a new identity). Otherwise return the cached
		// name immediately.
		if w.agentNameResolver != nil {
			if resolved := strings.TrimSpace(w.agentNameResolver(paneID)); resolved != "" {
				return resolved
			}
		}
		return cached
	}
	w.mu.Unlock()

	if w.agentNameResolver != nil {
		if resolved := strings.TrimSpace(w.agentNameResolver(paneID)); resolved != "" {
			return resolved
		}
	}
	return strings.TrimSpace(w.agentName)
}

func (w *FileReservationWatcher) prepareReservationAttempt(
	paneID string,
	sessionName string,
	output string,
	files []string,
	now time.Time,
) (string, []string) {
	// Resolve the registered Agent Mail identity for this pane OUTSIDE the
	// lock (the resolver may touch the filesystem). If no identity is
	// available we skip entirely instead of fabricating a name; sending a
	// reservation with an unregistered agent_name is what drove #107's red
	// error metrics in the first place.
	resolvedName := w.resolveAgentName(paneID)

	w.mu.Lock()
	defer w.mu.Unlock()

	reservation, exists := w.activeReservations[paneID]
	if !exists {
		if resolvedName == "" {
			// No registered identity yet and no legacy fallback — skip this
			// reservation attempt. The pane will be retried on the next
			// poll cycle once its identity file is written.
			if w.debug {
				log.Printf("[FileReservationWatcher] Skipping pane %s in session %s: no registered Agent Mail identity yet",
					paneID, sessionName)
			}
			return "", nil
		}
		reservation = &PaneReservation{
			PaneID:       paneID,
			AgentName:    resolvedName,
			LastActivity: now,
			LastOutput:   output,
		}
		w.activeReservations[paneID] = reservation
	} else if resolvedName != "" && reservation.AgentName != resolvedName {
		// Identity was (re)registered after a previous reservation was
		// created under a fallback name; adopt the canonical value going
		// forward so future calls carry the right agent_name.
		if w.debug {
			log.Printf("[FileReservationWatcher] Updating agent name for pane %s: %q -> %q",
				paneID, reservation.AgentName, resolvedName)
		}
		reservation.AgentName = resolvedName
	}

	existingFiles := make(map[string]bool, len(reservation.Files))
	for _, f := range reservation.Files {
		existingFiles[f] = true
	}

	newFiles := make([]string, 0, len(files))
	for _, f := range files {
		if !existingFiles[f] {
			newFiles = append(newFiles, f)
			existingFiles[f] = true
		}
	}

	reservation.LastActivity = now
	if output != "" {
		reservation.LastOutput = output
	}
	return reservation.AgentName, newFiles
}

func (w *FileReservationWatcher) recordReservationResult(paneID string, result *agentmail.ReservationResult, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()

	reservation, exists := w.activeReservations[paneID]
	if !exists {
		return
	}
	reservation.LastActivity = now

	if result == nil {
		return
	}

	existingFiles := make(map[string]bool, len(reservation.Files))
	for _, file := range reservation.Files {
		existingFiles[file] = true
	}
	existingIDs := make(map[int]bool, len(reservation.ReservationID))
	for _, id := range reservation.ReservationID {
		existingIDs[id] = true
	}

	for _, granted := range result.Granted {
		if !existingFiles[granted.PathPattern] {
			reservation.Files = append(reservation.Files, granted.PathPattern)
			existingFiles[granted.PathPattern] = true
		}
		if !existingIDs[granted.ID] {
			reservation.ReservationID = append(reservation.ReservationID, granted.ID)
			existingIDs[granted.ID] = true
		}
	}
}

func (w *FileReservationWatcher) buildConflictNotifications(
	ctx context.Context,
	sessionName string,
	paneID string,
	agentName string,
	conflicts []agentmail.ReservationConflict,
) []FileConflict {
	notifications := make([]FileConflict, 0, len(conflicts))
	reservationsByPath := make(map[string][]agentmail.FileReservation)

	if hasConflictHolders(conflicts) {
		reservations, err := w.client.ListReservations(ctx, w.projectDir, "", true)
		if err == nil {
			for _, reservation := range reservations {
				reservationsByPath[reservation.PathPattern] = append(reservationsByPath[reservation.PathPattern], reservation)
			}
		}
	}

	for _, conflict := range conflicts {
		fc := FileConflict{
			Path:           conflict.Path,
			RequestorAgent: agentName,
			RequestorPane:  paneID,
			SessionName:    sessionName,
			Holders:        append([]string(nil), conflict.Holders...),
			DetectedAt:     time.Now(),
		}

		for _, reservation := range reservationsByPath[conflict.Path] {
			for _, holder := range conflict.Holders {
				if reservation.AgentName != holder {
					continue
				}
				reservedSince := reservation.CreatedTS.Time
				fc.ReservedSince = &reservedSince
				expiresAt := reservation.ExpiresTS.Time
				fc.ExpiresAt = &expiresAt
				fc.HolderReservationIDs = append(fc.HolderReservationIDs, reservation.ID)
				break
			}
		}

		notifications = append(notifications, fc)
	}

	return notifications
}

func hasConflictHolders(conflicts []agentmail.ReservationConflict) bool {
	for _, conflict := range conflicts {
		if len(conflict.Holders) > 0 {
			return true
		}
	}
	return false
}

func clonePaneReservation(reservation PaneReservation) PaneReservation {
	reservation.Files = append([]string(nil), reservation.Files...)
	reservation.ReservationID = append([]int(nil), reservation.ReservationID...)
	return reservation
}

func reservationReleaseComplete(reservation PaneReservation, result *agentmail.ReleaseReservationsResult) bool {
	if len(reservation.ReservationID) == 0 {
		return true
	}
	if result == nil {
		return false
	}
	return result.Released >= len(reservation.ReservationID)
}

func (w *FileReservationWatcher) removeTrackedReservation(snapshot PaneReservation) {
	w.mu.Lock()
	defer w.mu.Unlock()

	current, ok := w.activeReservations[snapshot.PaneID]
	if !ok {
		return
	}

	if len(snapshot.ReservationID) == 0 {
		if len(current.ReservationID) == 0 && !current.LastActivity.After(snapshot.LastActivity) {
			delete(w.activeReservations, snapshot.PaneID)
		}
		return
	}

	removeIDs := make(map[int]struct{}, len(snapshot.ReservationID))
	for _, id := range snapshot.ReservationID {
		removeIDs[id] = struct{}{}
	}
	filteredIDs := make([]int, 0, len(current.ReservationID))
	for _, id := range current.ReservationID {
		if _, remove := removeIDs[id]; !remove {
			filteredIDs = append(filteredIDs, id)
		}
	}
	current.ReservationID = filteredIDs

	removeFiles := make(map[string]struct{}, len(snapshot.Files))
	for _, file := range snapshot.Files {
		removeFiles[file] = struct{}{}
	}
	filteredFiles := make([]string, 0, len(current.Files))
	for _, file := range current.Files {
		if _, remove := removeFiles[file]; !remove {
			filteredFiles = append(filteredFiles, file)
		}
	}
	current.Files = filteredFiles

	if len(current.ReservationID) == 0 && len(current.Files) == 0 {
		delete(w.activeReservations, snapshot.PaneID)
	}
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
