// Package watcher provides file watching with debouncing using fsnotify.
// file_reservation.go implements automatic file reservation based on pane output detection.
package watcher

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
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
	// leases retains the verified binding and expiry behind each owned ID.
	// A path alone must not suppress acquisition after its lease disappears.
	leases         map[int]agentmail.FileReservation
	renewalPending bool
}

type pendingReservationEdits struct {
	files      []string
	observedAt time.Time
	agentName  string // Empty only while the observed pane has no identity yet.
	// An uncertain mutation must not be blindly replayed, even when its
	// output frame is unchanged. Only unattempted or verified conflict-only
	// work is automatically retried.
	blocked bool
}

// AgentNameResolver resolves a registered Agent Mail identity for a tmux pane.
// Implementations should return the empty string to indicate that no identity
// is available and the watcher should skip the pane (rather than fabricate a
// name like the session name, which would not be a registered Agent Mail
// identity and would cause the Agent Mail server to reject reservation calls).
type AgentNameResolver func(paneID string) string

// reservationWatcherClient keeps the watcher's ownership transitions testable
// independently of the transport. Production uses the shared Agent Mail client,
// including its independent grant-ownership verification.
type reservationWatcherClient interface {
	ReservePaths(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error)
	ListReservations(context.Context, string, string, bool) ([]agentmail.FileReservation, error)
	ReleaseReservations(context.Context, string, string, []string, []int) (*agentmail.ReleaseReservationsResult, error)
	RenewReservations(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error)
	ReadActiveReservations(context.Context, string) ([]agentmail.FileReservation, error)
}

// FileReservationWatcher monitors pane output and automatically reserves files.
type FileReservationWatcher struct {
	client             reservationWatcherClient
	projectDir         string
	agentName          string // legacy fallback only; real name comes from agentNameResolver
	sessionFilter      string
	pollInterval       time.Duration
	idleTimeout        time.Duration
	reservationTTL     time.Duration
	captureLines       int
	autoReserve        bool
	extendOnActivity   bool
	listAllPanes       func(ctx context.Context) (map[string][]tmux.Pane, error)
	capturePaneOutput  func(ctx context.Context, target string, lines int) (string, error)
	agentNameResolver  AgentNameResolver // paneID -> registered agent name, or "" to skip
	paneOutputs        map[string]string // paneID -> last captured output
	pendingEdits       map[string]*pendingReservationEdits
	activeReservations map[string]*PaneReservation // paneID -> reservation
	lifecycleMu        sync.Mutex
	mu                 sync.Mutex
	stopCh             chan struct{}
	cancel             context.CancelFunc // Cancels in-flight polling I/O before Stop joins.
	wg                 sync.WaitGroup
	debug              bool
	conflictCallback   ConflictCallback // Called when conflicts are detected
}

// FileReservationWatcherOption configures a FileReservationWatcher.
type FileReservationWatcherOption func(*FileReservationWatcher)

// WithWatcherClient sets the Agent Mail client.
func WithWatcherClient(client *agentmail.Client) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		// Do not store a typed nil in the interface: the nil-client gate must
		// continue to suppress every network-facing reservation operation.
		w.client = nil
		if client != nil {
			w.client = client
		}
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

// WithAutoReserve controls whether detected file edits create reservations.
func WithAutoReserve(enabled bool) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.autoReserve = enabled
	}
}

// WithExtendOnActivity controls whether changed pane output renews existing reservations.
func WithExtendOnActivity(enabled bool) FileReservationWatcherOption {
	return func(w *FileReservationWatcher) {
		w.extendOnActivity = enabled
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
		autoReserve:        true,
		extendOnActivity:   true,
		listAllPanes:       tmux.DefaultClient.GetAllPanesContext,
		capturePaneOutput:  tmux.CapturePaneOutputContext,
		paneOutputs:        make(map[string]string),
		pendingEdits:       make(map[string]*pendingReservationEdits),
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
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.wg.Add(1) // Must be inside the lock to prevent race with Stop()+wg.Wait()
	w.mu.Unlock()

	go w.run(runCtx, stopCh)

	if w.debug {
		log.Printf("[FileReservationWatcher] Started with pollInterval=%v idleTimeout=%v", w.pollInterval, w.idleTimeout)
	}
}

// Stop halts the file reservation watcher and releases all reservations.
func (w *FileReservationWatcher) Stop() {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()

	w.mu.Lock()
	stopCh, cancel := w.stopCh, w.cancel
	w.stopCh = nil
	w.cancel = nil
	w.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if cancel != nil {
		cancel()
	}
	w.wg.Wait()
	w.mu.Lock()
	clear(w.pendingEdits)
	clear(w.paneOutputs)
	w.mu.Unlock()

	// A started worker owns exit cleanup, including parent cancellation.
	// Without a worker, retry any retained handles from earlier failed cleanup.
	if stopCh == nil {
		w.releaseAllReservations()
	}

	if w.debug {
		log.Printf("[FileReservationWatcher] Stopped")
	}
}

// run is the main polling loop.
func (w *FileReservationWatcher) run(ctx context.Context, stopCh <-chan struct{}) {
	defer w.wg.Done()
	// Cleanup uses its own bounded context, not the cancelled polling context.
	// Join includes cleanup so Stop never returns before owned handles settle.
	defer w.releaseAllReservations()

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
	if ctx.Err() != nil {
		return
	}
	listAllPanes := w.listAllPanes
	if listAllPanes == nil {
		listAllPanes = tmux.DefaultClient.GetAllPanesContext
	}

	// ListSessions only returns session metadata; it does not populate Session.Panes.
	// The watcher needs the pane-native API to actually inspect agent output.
	panesBySession, err := listAllPanes(ctx)
	if ctx.Err() != nil {
		return // Cancelled topology is not evidence that panes disappeared.
	}
	if err != nil {
		if w.debug {
			log.Printf("[FileReservationWatcher] Error listing panes: %v", err)
		}
		return
	}

	livePanes := make(map[string]bool)
	for sessionName, panes := range panesBySession {
		if w.sessionFilter != "" && sessionName != w.sessionFilter {
			continue
		}
		for _, pane := range panes {
			if ctx.Err() != nil {
				return
			}
			// Ignore shell/user panes; all agent panes are eligible for edit detection.
			if pane.Type == tmux.AgentUser {
				continue
			}
			livePanes[pane.ID] = true

			w.checkPaneForFileEdits(ctx, sessionName, pane)
		}
	}
	// Only a successful topology read can retire pending observations. A
	// failed read above is not evidence that all panes disappeared.
	w.mu.Lock()
	for paneID := range w.pendingEdits {
		if !livePanes[paneID] {
			delete(w.pendingEdits, paneID)
		}
	}
	for paneID := range w.paneOutputs {
		if !livePanes[paneID] {
			delete(w.paneOutputs, paneID)
		}
	}
	w.mu.Unlock()
}

// checkPaneForFileEdits checks a single pane for file edits and reserves files.
func (w *FileReservationWatcher) checkPaneForFileEdits(ctx context.Context, sessionName string, pane tmux.Pane) {
	if ctx.Err() != nil {
		return
	}
	capturePaneOutput := w.capturePaneOutput
	if capturePaneOutput == nil {
		capturePaneOutput = tmux.CapturePaneOutputContext
	}

	// Capture recent output
	output, err := capturePaneOutput(ctx, pane.ID, w.captureLines)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if w.debug {
			log.Printf("[FileReservationWatcher] Error capturing output from pane %s: %v", pane.ID, err)
		}
		return
	}

	now := time.Now()
	changed := w.recordPaneOutput(pane.ID, output, now)
	w.mu.Lock()
	needsRenewal := false
	if tracked := w.activeReservations[pane.ID]; tracked != nil {
		needsRenewal = tracked.renewalPending && now.Sub(tracked.LastActivity) <= w.idleTimeout
	}
	w.mu.Unlock()
	if w.extendOnActivity && (changed || needsRenewal) {
		w.renewPaneReservation(ctx, pane.ID)
	}

	if !w.autoReserve || w.client == nil || w.projectDir == "" || ctx.Err() != nil {
		return
	}
	currentName := w.resolveAgentName(pane.ID)
	if changed {
		// Only new pane evidence advances activity. Retrying an acquisition
		// must not keep an idle pane's leases alive indefinitely.
		files := extractEditedFiles(output, mapAgentTypeToPatternAgent(pane.Type), w.projectDir)
		w.mu.Lock()
		w.queueReservationEditsLocked(pane.ID, files, currentName, now)
		w.mu.Unlock()
	}
	if !w.reconcileExpiredReservations(ctx, pane.ID, now) {
		return
	}

	w.mu.Lock()
	pending := w.pendingEdits[pane.ID]
	if pending == nil || pending.blocked {
		w.mu.Unlock()
		return
	}
	if pending.agentName != "" && currentName != "" && pending.agentName != currentName {
		// A verified conflict is retryable only for its original observer,
		// not for an unrelated agent later launched into the same pane.
		delete(w.pendingEdits, pane.ID)
		w.mu.Unlock()
		return
	}
	if pending.agentName == "" {
		pending.agentName = currentName
	}
	if now.Sub(pending.observedAt) > w.idleTimeout {
		delete(w.pendingEdits, pane.ID)
		w.mu.Unlock()
		return
	}
	files := append([]string(nil), pending.files...)
	observedAt := pending.observedAt
	w.mu.Unlock()

	retry := w.onFileEdit(ctx, sessionName, pane, output, files, observedAt)
	currentName = w.resolveAgentName(pane.ID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if pending = w.pendingEdits[pane.ID]; pending != nil {
		held := make(map[string]bool)
		if tracked := w.activeReservations[pane.ID]; tracked != nil && tracked.AgentName == currentName {
			for _, file := range tracked.Files {
				held[file] = true
			}
		}
		remaining := pending.files[:0]
		for _, file := range pending.files {
			if !held[file] {
				remaining = append(remaining, file)
			}
		}
		pending.files = remaining
		pending.blocked = !retry
		if !retry {
			log.Printf("[FileReservationWatcher] Reservation outcome for pane %s is unresolved; automatic acquisition retry withheld; inspect ntm locks before restarting the watcher", pane.ID)
		}
		if len(remaining) == 0 {
			delete(w.pendingEdits, pane.ID)
		}
	}
}

// queueReservationEditsLocked retains edits even when they scroll out of the
// next capture before identity registration or a conflict has been resolved.
// The caller holds w.mu. A retry never supplies a new observation timestamp.
func (w *FileReservationWatcher) queueReservationEditsLocked(paneID string, files []string, agentName string, observedAt time.Time) {
	if w.pendingEdits == nil {
		w.pendingEdits = make(map[string]*pendingReservationEdits)
	}
	pending := w.pendingEdits[paneID]
	if pending != nil && pending.agentName != "" && agentName != "" && pending.agentName != agentName {
		if pending.blocked {
			return // A replacement identity does not settle an uncertain mutation.
		}
		delete(w.pendingEdits, paneID)
		pending = nil
	}
	if pending == nil {
		if len(files) == 0 {
			return
		}
		pending = &pendingReservationEdits{observedAt: observedAt, agentName: agentName}
		w.pendingEdits[paneID] = pending
	}
	if pending.agentName == "" {
		pending.agentName = agentName
	}
	if observedAt.After(pending.observedAt) {
		pending.observedAt = observedAt
	}
	seen := make(map[string]bool, len(pending.files)+len(files))
	for _, file := range pending.files {
		seen[file] = true
	}
	for _, file := range files {
		if file != "" && !seen[file] {
			pending.files = append(pending.files, file)
			seen[file] = true
		}
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
	case tmux.AgentAntigravity:
		return "antigravity"
	default:
		return "*"
	}
}

func (w *FileReservationWatcher) onFileEdit(
	ctx context.Context,
	sessionName string,
	pane tmux.Pane,
	output string,
	files []string,
	now time.Time,
) bool {
	if ctx.Err() != nil || !w.autoReserve || w.client == nil || w.projectDir == "" {
		return true // No mutation was attempted.
	}

	agentName, newFiles := w.prepareReservationAttempt(pane.ID, sessionName, output, files, now)
	if agentName == "" || len(newFiles) == 0 {
		// Either we have no registered identity for this pane yet (skip to
		// avoid a noisy 'unknown agent_name' server error) or we already
		// hold reservations covering every detected file.
		return true
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
	if err != nil && (!agentmail.IsReservationConflict(err) || errors.Is(err, agentmail.ErrReservationUnverified)) {
		// A response can carry BOTH conflicts and failed ownership readback.
		// Those handles are inspection evidence, never authority to renew or
		// release. Only a verified partial grant may enter our owned set.
		if w.debug {
			log.Printf("[FileReservationWatcher] Reservation error for pane %s: %v", pane.ID, err)
		}
		return false
	}
	if result == nil {
		return false // No receipt is not proof that an acquisition failed.
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
	return true
}

// renewPaneReservation extends the lease for a pane that produced new output.
func (w *FileReservationWatcher) renewPaneReservation(ctx context.Context, paneID string) {
	if w.client == nil || ctx.Err() != nil {
		return
	}

	w.mu.Lock()
	reservation, ok := w.activeReservations[paneID]
	if !ok || len(reservation.ReservationID) == 0 {
		w.mu.Unlock()
		return
	}
	copy := clonePaneReservation(*reservation)
	w.mu.Unlock()
	if currentName := w.resolveAgentName(paneID); currentName != "" && currentName != copy.AgentName {
		return // A replacement identity cannot extend its predecessor's leases.
	}

	result, err := w.client.RenewReservations(ctx, agentmail.RenewReservationsOptions{
		ProjectKey:     w.projectDir,
		AgentName:      copy.AgentName,
		ExtendSeconds:  int(w.reservationTTL.Seconds()),
		ReservationIDs: copy.ReservationID,
	})
	w.mu.Lock()
	if current := w.activeReservations[paneID]; current != nil && current.AgentName == copy.AgentName {
		current.renewalPending = err != nil || result == nil || result.Renewed < len(copy.ReservationID)
	}
	w.mu.Unlock()
	if err != nil || result == nil || result.Renewed < len(copy.ReservationID) {
		if w.debug {
			if err != nil {
				log.Printf("[FileReservationWatcher] Error renewing reservations for active pane %s: %v", paneID, err)
			} else {
				renewed := 0
				if result != nil {
					renewed = result.Renewed
				}
				log.Printf("[FileReservationWatcher] Renewed %d of %d reservations for active pane %s", renewed, len(copy.ReservationID), paneID)
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

// reconcileExpiredReservations refreshes only leases whose last verified
// lifetime ended. Missing/expired rows are no longer coverage; an independent
// read may instead prove that a renewal succeeded despite a lost reply. Never
// reacquire merely because a renewal count was short or the read failed.
func (w *FileReservationWatcher) reconcileExpiredReservations(ctx context.Context, paneID string, now time.Time) bool {
	w.mu.Lock()
	tracked := w.activeReservations[paneID]
	if tracked == nil || len(tracked.leases) == 0 {
		w.mu.Unlock()
		return true
	}
	snapshot := clonePaneReservation(*tracked)
	w.mu.Unlock()

	expired := make(map[int]agentmail.FileReservation)
	for id, lease := range snapshot.leases {
		if !lease.ExpiresTS.After(now) {
			expired[id] = lease
		}
	}
	if len(expired) == 0 {
		return true
	}
	rows, err := w.client.ReadActiveReservations(ctx, w.projectDir)
	if err != nil || ctx.Err() != nil {
		if w.debug {
			log.Printf("[FileReservationWatcher] Cannot verify expired leases for pane %s: %v", paneID, err)
		}
		return false
	}
	byID := make(map[int]agentmail.FileReservation, len(rows))
	for _, row := range rows {
		if _, duplicate := byID[row.ID]; duplicate {
			return false // Incomplete/inconsistent evidence is not absence.
		}
		byID[row.ID] = row
	}
	checkedAt := time.Now()
	lost := make(map[int]bool)
	for id, lease := range expired {
		row, found := byID[id]
		if !found {
			lost[id] = true
			continue
		}
		// Compare against the original VERIFIED binding, not against a new
		// request or a newly registered pane identity. A mismatched row must
		// not authorize release, renewal or replacement acquisition.
		if row.AgentName != lease.AgentName || row.ProjectID != lease.ProjectID ||
			row.PathPattern != lease.PathPattern || row.Reason != lease.Reason ||
			row.Exclusive != lease.Exclusive || row.ExpiresTS.IsZero() {
			return false
		}
		lost[id] = row.ReleasedTS != nil || !row.ExpiresTS.After(checkedAt)
	}
	currentName := w.resolveAgentName(paneID)

	w.mu.Lock()
	defer w.mu.Unlock()
	current := w.activeReservations[paneID]
	if current == nil || current.AgentName != snapshot.AgentName {
		return false
	}
	// Do not let a delayed read overwrite a newer grant/renewal observation.
	for id, lease := range expired {
		live, ok := current.leases[id]
		if !ok || !live.ExpiresTS.Equal(lease.ExpiresTS.Time) || live.PathPattern != lease.PathPattern {
			return false
		}
	}
	lostPaths := make([]string, 0, len(lost))
	for _, id := range current.ReservationID {
		if lost[id] {
			lostPaths = append(lostPaths, current.leases[id].PathPattern)
			delete(current.leases, id)
		} else if _, checked := expired[id]; checked {
			current.leases[id] = byID[id]
		}
	}
	ids := current.ReservationID[:0]
	for _, id := range current.ReservationID {
		if !lost[id] {
			ids = append(ids, id)
		}
	}
	current.ReservationID = ids
	remainingPaths := make(map[string]bool)
	for _, lease := range current.leases {
		remainingPaths[lease.PathPattern] = true
	}
	noLongerHeld := make(map[string]bool)
	for _, path := range lostPaths {
		if !remainingPaths[path] {
			noLongerHeld[path] = true
		}
	}
	files := current.Files[:0]
	for _, path := range current.Files {
		if !noLongerHeld[path] {
			files = append(files, path)
		}
	}
	current.Files = files
	if len(lostPaths) > 0 && currentName == snapshot.AgentName && checkedAt.Sub(current.LastActivity) <= w.idleTimeout {
		w.queueReservationEditsLocked(paneID, lostPaths, snapshot.AgentName, current.LastActivity)
	}
	return true
}

// releaseIdleReservations releases reservations for panes that have been idle.
func (w *FileReservationWatcher) releaseIdleReservations(ctx context.Context) {
	if w.client == nil || ctx.Err() != nil {
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
		// Paths can also name manually acquired or subsequently replaced
		// leases. Cleanup owns only the exact IDs we verified and recorded.
		releaseResult, err := w.client.ReleaseReservations(ctx, w.projectDir, reservation.AgentName, nil, reservation.ReservationID)
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

		releaseResult, err := w.client.ReleaseReservations(ctx, w.projectDir, reservation.AgentName, nil, reservation.ReservationID)
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
		if len(reservation.ReservationID) > 0 {
			// Respawning a pane changes its identity, not the owner of its
			// existing server-side leases. Never relabel those handles and
			// later renew/release them as the replacement agent.
			if w.debug {
				log.Printf("[FileReservationWatcher] Deferring reservation for pane %s: leases still belong to %q, current identity is %q",
					paneID, reservation.AgentName, resolvedName)
			}
			return "", nil
		}
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

	if now.After(reservation.LastActivity) {
		reservation.LastActivity = now
	}
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
	if now.After(reservation.LastActivity) {
		reservation.LastActivity = now
	}

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
		if reservation.leases == nil {
			reservation.leases = make(map[int]agentmail.FileReservation)
		}
		reservation.leases[granted.ID] = granted
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
	if reservation.leases != nil {
		leases := make(map[int]agentmail.FileReservation, len(reservation.leases))
		for id, lease := range reservation.leases {
			leases[id] = lease
		}
		reservation.leases = leases
	}
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
	if !ok || current.AgentName != snapshot.AgentName {
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
		delete(current.leases, id)
	}
	filteredIDs := make([]int, 0, len(current.ReservationID))
	removed := false
	for _, id := range current.ReservationID {
		if _, remove := removeIDs[id]; !remove {
			filteredIDs = append(filteredIDs, id)
		} else {
			removed = true
		}
	}
	if !removed {
		return // A newer lease may own the same path under a different ID.
	}
	current.ReservationID = filteredIDs

	removeFiles := make(map[string]struct{}, len(snapshot.Files))
	for _, file := range snapshot.Files {
		removeFiles[file] = struct{}{}
	}
	for _, lease := range current.leases {
		delete(removeFiles, lease.PathPattern)
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
	// The line-anchored patterns below need (?m): extractEditedFiles is called
	// with a whole multi-line pane capture, so without it `^` only ever matched
	// the very first line of the capture and these rules were dead.
	"gemini": {
		regexp.MustCompile(`(?im)^Writing:\s*(.+)$`),
		regexp.MustCompile(`(?im)^Editing:\s*(.+)$`),
		regexp.MustCompile(`(?im)^Created:\s*(.+)$`),
		regexp.MustCompile(`(?i)(?:edited|modified)\s+(?:file:?\s+)?([^\s,]+\.\w+)`),
	},
	"*": {
		// Generic patterns as fallback
		regexp.MustCompile(`(?im)^(?:✓\s*)?(?:edited|modified):?\s+([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?im)^(?:✓\s*)?created:?\s+([^\s,]+\.\w+)`),
		regexp.MustCompile(`(?im)^(?:✓\s*)?wrote:?\s+([^\s,]+\.\w+)`),
		// Path-like patterns (match absolute or relative paths ending in file extension)
		regexp.MustCompile(`(?:^|[\s:"'])((?:/[^/\s]+)+\.\w+)`),
		regexp.MustCompile(`(?:^|[\s:"'])(\./[^\s]+\.\w+)`),
		regexp.MustCompile(`(?:^|[\s:"'])([a-zA-Z_][a-zA-Z0-9_/-]*\.\w+)`),
	},
}

// extractEditedFiles extracts file paths from agent output.
// It returns a list of files that appear to have been edited/written by the agent.
func extractEditedFiles(output string, agentType string, projectDir string) []string {
	seen := make(map[string]bool)
	var files []string

	collect := func(patterns []*regexp.Regexp) {
		for _, re := range patterns {
			matches := re.FindAllStringSubmatch(output, -1)
			for _, match := range matches {
				if len(match) <= 1 {
					continue
				}
				path := cleanFilePathForReservation(match[1])
				if !isValidFilePathForReservation(path) || seen[path] {
					continue
				}
				if !namesProjectFile(path, projectDir) {
					continue
				}
				seen[path] = true
				files = append(files, path)
			}
		}
	}

	// Get patterns for specific agent type
	if patterns, ok := filePathPatterns[agentType]; ok {
		collect(patterns)
	}

	// Also try generic patterns
	if agentType != "*" {
		collect(filePathPatterns["*"])
	}

	return files
}

// namesProjectFile reports whether a candidate extracted from pane output is
// plausibly a file rather than prose.
//
// The generic catch-all pattern matches any "word.word" token, so a sentence
// like "moving Object.assign into node.js utils" yields "e.g", "Object.assign",
// and "node.js" — each of which would otherwise become an exclusive 15-minute
// Agent Mail reservation against a path that does not exist. Candidates that
// already carry a path separator are taken at face value; a bare name has to
// resolve to a real file inside the project.
func namesProjectFile(path, projectDir string) bool {
	if strings.ContainsRune(path, '/') {
		return true
	}
	if projectDir == "" {
		// Without a project root there is nothing to verify against, and an
		// over-broad exclusive reservation is worse than a missed one.
		return false
	}
	info, err := os.Stat(filepath.Join(projectDir, path))
	return err == nil && !info.IsDir()
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
