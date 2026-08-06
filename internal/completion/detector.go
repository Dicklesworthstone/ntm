// Package completion provides detection for when agents complete their assigned work.
package completion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// DetectionMethod describes how completion was detected
type DetectionMethod string

const (
	// MethodBeadClosed indicates the bead was detected as closed in br
	MethodBeadClosed DetectionMethod = "bead_closed"
	// MethodPatternMatch indicates completion phrase was found in output
	MethodPatternMatch DetectionMethod = "pattern_match"
	// MethodIdle indicates no activity for threshold duration
	MethodIdle DetectionMethod = "idle"
	// MethodAgentMail indicates agent sent completion message
	MethodAgentMail DetectionMethod = "agent_mail"
	// MethodPaneLost indicates the pane no longer exists
	MethodPaneLost DetectionMethod = "pane_lost"
)

// CompletionEvent represents a detected completion
type CompletionEvent struct {
	EventID       string          `json:"event_id,omitempty"`
	ConsumerToken string          `json:"-"`
	LeaseDuration time.Duration   `json:"-"`
	Pane          int             `json:"pane"`
	PaneID        string          `json:"pane_id,omitempty"`
	PaneTarget    string          `json:"pane_target,omitempty"`
	AgentType     string          `json:"agent_type"`
	BeadID        string          `json:"bead_id"`
	Method        DetectionMethod `json:"method"`
	Timestamp     time.Time       `json:"timestamp"`
	Duration      time.Duration   `json:"duration"`    // How long agent worked
	Output        string          `json:"output"`      // Last N lines (for debugging)
	IsFailed      bool            `json:"is_failed"`   // True if failure detected
	FailReason    string          `json:"fail_reason"` // Reason for failure
}

// TerminalReconciler releases the durable external handles recorded on a
// pending terminal assignment and completes its local terminal transition.
// It must return applied=true only after the ledger row is terminal.
type TerminalReconciler func(context.Context, *assignment.Assignment) (bool, error)

// DetectionConfig configures the detector behavior
type DetectionConfig struct {
	PollInterval            time.Duration // Interval for bead status polling (default 5s)
	IdleThreshold           time.Duration // Duration of inactivity to consider complete (default 120s)
	RetryOnError            bool          // Retry failed checks (default true)
	RetryInterval           time.Duration // Time between retries (default 10s)
	MaxRetries              int           // Max retries before giving up (default 3)
	DedupWindow             time.Duration // Prevent duplicate events (default 5s)
	CompletionLeaseDuration time.Duration // Durable single-consumer lease (default 30s)
	GracefulDegrading       bool          // Fall back to lesser methods (default true)
	CaptureLines            int           // Lines to capture for pattern matching (default 50)
}

// DefaultConfig returns sensible default configuration
func DefaultConfig() DetectionConfig {
	return DetectionConfig{
		PollInterval:            5 * time.Second,
		IdleThreshold:           120 * time.Second,
		RetryOnError:            true,
		RetryInterval:           10 * time.Second,
		MaxRetries:              3, // Canonical default: config.RetryConfig.Completion.MaxAttempts
		DedupWindow:             5 * time.Second,
		CompletionLeaseDuration: 30 * time.Second,
		GracefulDegrading:       true,
		CaptureLines:            50,
	}
}

// CompletionDetector monitors agents for work completion
type CompletionDetector struct {
	Session     string
	Config      DetectionConfig
	Store       *assignment.AssignmentStore
	Patterns    []*regexp.Regexp // Completion patterns
	FailPattern []*regexp.Regexp // Failure patterns

	mu                 sync.RWMutex
	activityTracker    map[string]*activityState // durable pane target -> activity state
	recentEvents       map[string]time.Time      // assignment attempt key -> last event time (for dedup)
	brAvailable        *bool                     // nil = unknown, cached after first check
	observer           *statuspkg.SessionObserver
	terminalReconciler TerminalReconciler
	consumerToken      string
}

// activityState tracks output activity per pane
type activityState struct {
	assignmentKey  string
	lastOutputTime time.Time
	lastOutput     string
	burstStarted   time.Time
	burstActive    bool
}

// Default completion patterns (case-insensitive)
var defaultCompletionPatterns = []string{
	`(?i)bead\s+\S+\s+complete`,
	`(?i)task\s+\S+\s+(done|finished|complete)`,
	`(?i)closing\s+bead`,
	`(?i)br\s+(close|update.*closed)`,
	`(?i)marked\s+as\s+complete`,
	`(?i)successfully\s+completed`,
	`(?i)work\s+complete`,
	`(?i)finished\s+working`,
}

var completionConsumerFallbackCounter uint64

// Default failure patterns (case-insensitive)
var defaultFailurePatterns = []string{
	`(?i)unable\s+to\s+complete`,
	`(?i)cannot\s+proceed`,
	`(?i)blocked\s+by`,
	`(?i)giving\s+up`,
	`(?i)need\s+help`,
	`(?i)failed\s+to`,
	`(?i)error:.*fatal`,
	`(?i)aborting`,
}

// New creates a new CompletionDetector with default configuration
func New(session string, store *assignment.AssignmentStore) *CompletionDetector {
	return NewWithConfig(session, store, DefaultConfig())
}

// NewWithConfig creates a new CompletionDetector with custom configuration
func NewWithConfig(session string, store *assignment.AssignmentStore, cfg DetectionConfig) *CompletionDetector {
	defaults := DefaultConfig()
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaults.PollInterval
	}
	if cfg.IdleThreshold <= 0 {
		cfg.IdleThreshold = defaults.IdleThreshold
	}
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = defaults.RetryInterval
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = defaults.MaxRetries
	}
	if cfg.DedupWindow <= 0 {
		cfg.DedupWindow = defaults.DedupWindow
	}
	if cfg.CompletionLeaseDuration <= 0 {
		cfg.CompletionLeaseDuration = defaults.CompletionLeaseDuration
	}
	if cfg.CaptureLines <= 0 {
		cfg.CaptureLines = defaults.CaptureLines
	}
	if raw := strings.TrimSpace(os.Getenv("NTM_TEST_COMPLETION_LEASE_DURATION")); raw != "" {
		if duration, err := time.ParseDuration(raw); err == nil && duration > 0 {
			cfg.CompletionLeaseDuration = duration
		} else {
			slog.Warn("ignoring invalid completion lease E2E override", "value", raw)
		}
	}
	d := &CompletionDetector{
		Session:         session,
		Config:          cfg,
		Store:           store,
		activityTracker: make(map[string]*activityState),
		recentEvents:    make(map[string]time.Time),
		observer:        statuspkg.NewSessionObserver(statuspkg.NewDetector()),
		consumerToken:   newCompletionConsumerToken(),
	}

	// Compile default patterns
	for _, p := range defaultCompletionPatterns {
		if re, err := regexp.Compile(p); err == nil {
			d.Patterns = append(d.Patterns, re)
		}
	}
	for _, p := range defaultFailurePatterns {
		if re, err := regexp.Compile(p); err == nil {
			d.FailPattern = append(d.FailPattern, re)
		}
	}

	return d
}

func newCompletionConsumerToken() string {
	token, err := assignment.NewAssignmentIdempotencyKey()
	if err == nil {
		return "completion/" + token
	}
	return fmt.Sprintf("completion/%d/%d/%d", os.Getpid(), time.Now().UTC().UnixNano(), atomic.AddUint64(&completionConsumerFallbackCounter, 1))
}

// SetTerminalReconciler configures the external release boundary used after a
// completion event has durably established its exact-generation barrier.
func (d *CompletionDetector) SetTerminalReconciler(reconciler TerminalReconciler) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.terminalReconciler = reconciler
	d.mu.Unlock()
}

// AddPattern adds a custom completion pattern
func (d *CompletionDetector) AddPattern(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	d.mu.Lock()
	d.Patterns = append(d.Patterns, re)
	d.mu.Unlock()
	return nil
}

// AddFailurePattern adds a custom failure pattern
func (d *CompletionDetector) AddFailurePattern(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	d.mu.Lock()
	d.FailPattern = append(d.FailPattern, re)
	d.mu.Unlock()
	return nil
}

// Watch starts continuous monitoring and returns a channel of completion events.
// The channel is closed when the context is cancelled.
func (d *CompletionDetector) Watch(ctx context.Context) <-chan CompletionEvent {
	events := make(chan CompletionEvent, 10)

	go func() {
		defer close(events)

		ticker := time.NewTicker(d.Config.PollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.checkAll(ctx, events)
			}
		}
	}()

	return events
}

// checkAll checks all active assignments for completion
func (d *CompletionDetector) checkAll(ctx context.Context, events chan<- CompletionEvent) {
	if d.Store == nil {
		return
	}

	// Reload the store from disk before scanning. The detector is handed the
	// watch loop's store instance at construction, but post-startup dispatches
	// are recorded by a SEPARATE store instance in
	// internal/cli/assign.go::executeAssignmentsEnhanced (which does its own
	// LoadStore + Assign + Save). Without reloading, the detector's in-memory
	// view is frozen at startup: it never observes anything dispatched later,
	// so it never marks those beads completed and never releases their panes —
	// stale `assigned` records pile up and permanently mark every pane busy,
	// killing the autonomous loop after ~(pane count) dispatches. Reloading
	// here makes each tick observe the current on-disk assignment state. A
	// reload failure is non-fatal — fall back to the existing in-memory view.
	if err := d.Store.Load(); err != nil {
		slog.Warn("completion detector failed to reload assignment store; using in-memory view", "session", d.Session, "error", err)
	}
	for _, terminal := range d.Store.ListPendingCompletionEvents() {
		event, eventErr := completionEventFromDurableTerminal(terminal, nil)
		if eventErr != nil {
			slog.Warn("completion detector rejected pending completion event", "bead", terminal.BeadID, "error", eventErr)
			continue
		}
		if !d.emitCompletionEvent(ctx, terminal, event, events) {
			return
		}
	}

	assignments := d.Store.ListActive()
	for _, a := range assignments {
		if assignmentHasPendingTerminalOutcome(a) {
			event := completionEventFromPendingTerminal(a)
			applied, reconcileErr := d.reconcileTerminalBarrier(ctx, a)
			if reconcileErr != nil {
				slog.Warn("failed to resume assignment terminal reconciliation", "bead", a.BeadID, "error", reconcileErr)
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if applied {
				terminal := d.Store.Get(a.BeadID)
				event, reconcileErr = completionEventFromDurableTerminal(terminal, event)
				if reconcileErr != nil {
					slog.Warn("completion detector rejected reconciled completion event", "bead", a.BeadID, "error", reconcileErr)
					continue
				}
				if !d.emitCompletionEvent(ctx, terminal, event, events) {
					return
				}
			}
			continue
		}
		if !assignmentEligibleForCompletionScan(a) {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
			event, checkErr := d.checkAssignment(ctx, a)
			if checkErr != nil {
				slog.Warn("completion detector rejected assignment", "bead", a.BeadID, "error", checkErr)
				continue
			}
			if event != nil {
				applied, persistErr := d.persistCompletionEvent(ctx, a, event)
				if persistErr != nil {
					slog.Warn("failed to persist assignment completion", "bead", a.BeadID, "error", persistErr)
					if ctx.Err() != nil {
						return
					}
					continue
				}
				if !applied {
					continue
				}

				terminal := d.Store.Get(a.BeadID)
				event, persistErr = completionEventFromDurableTerminal(terminal, event)
				if persistErr != nil {
					slog.Warn("completion detector rejected persisted completion event", "bead", a.BeadID, "error", persistErr)
					continue
				}
				if !d.emitCompletionEvent(ctx, terminal, event, events) {
					return
				}
			}
		}
	}
}

func (d *CompletionDetector) persistCompletionEvent(ctx context.Context, observed *assignment.Assignment, event *CompletionEvent) (bool, error) {
	if d == nil || d.Store == nil || observed == nil || event == nil {
		return false, nil
	}
	terminalStatus := assignment.StatusCompleted
	reason := ""
	if event.IsFailed {
		terminalStatus = assignment.StatusFailed
		reason = event.FailReason
	}
	barrier, applied, err := d.Store.BeginTerminalReconciliationWithCompletionEventIfCurrent(ctx, observed, terminalStatus, reason)
	if err != nil || !applied {
		return false, err
	}
	if barrier != nil && barrier.ClearState == assignment.ClearStateNone && barrier.Status == terminalStatus && strings.TrimSpace(barrier.PendingCompletionEventID) != "" {
		// Cleanup may have completed in an earlier process before it could create
		// the outbox entry. The store has now durably backfilled that event; there
		// is no external lease/claim work to repeat.
		return true, nil
	}
	return d.reconcileTerminalBarrier(ctx, barrier)
}

func (d *CompletionDetector) reconcileTerminalBarrier(ctx context.Context, barrier *assignment.Assignment) (bool, error) {
	if d == nil || d.Store == nil || !assignmentHasPendingTerminalOutcome(barrier) {
		return false, nil
	}
	d.mu.RLock()
	reconciler := d.terminalReconciler
	d.mu.RUnlock()
	if reconciler != nil {
		applied, err := reconciler(ctx, barrier)
		if err != nil || !applied {
			return false, err
		}
		return d.terminalReconciliationFinished(barrier.BeadID)
	}
	if terminalReconciliationNeedsExternalCleanup(barrier) {
		return false, nil
	}
	if _, err := d.Store.RecordClearLeasesReleased(ctx, barrier.BeadID); err != nil {
		return false, err
	}
	if _, err := d.Store.RecordTerminalClaimReleased(ctx, barrier.BeadID); err != nil {
		return false, err
	}
	if err := d.Store.CompleteTerminalReconciliation(ctx, barrier.BeadID, barrier.PendingTerminalStatus, barrier.PendingTerminalReason); err != nil {
		return false, err
	}
	return d.terminalReconciliationFinished(barrier.BeadID)
}

func (d *CompletionDetector) terminalReconciliationFinished(beadID string) (bool, error) {
	if err := d.Store.LoadStrict(); err != nil {
		return false, fmt.Errorf("verify terminal reconciliation for %s: %w", beadID, err)
	}
	current := d.Store.Get(beadID)
	return current != nil && (current.Status == assignment.StatusCompleted || current.Status == assignment.StatusFailed) && current.ClearState == assignment.ClearStateNone, nil
}

func terminalReconciliationNeedsExternalCleanup(current *assignment.Assignment) bool {
	if current == nil {
		return false
	}
	return strings.TrimSpace(current.ClaimActor) != "" || current.ReservationRequired ||
		len(current.ReservationIDs) > 0 || len(current.ReservedPaths) > 0 ||
		current.ReservationState == assignment.ReservationReserving || current.ReservationState == assignment.ReservationReserved ||
		current.ReservationState == assignment.ReservationUnknown
}

func assignmentHasPendingTerminalOutcome(current *assignment.Assignment) bool {
	return current != nil && current.ClearState != assignment.ClearStateNone &&
		(current.PendingTerminalStatus == assignment.StatusCompleted || current.PendingTerminalStatus == assignment.StatusFailed)
}

func completionEventFromPendingTerminal(current *assignment.Assignment) *CompletionEvent {
	startTime := current.AssignedAt
	if current.StartedAt != nil {
		startTime = *current.StartedAt
	}
	return &CompletionEvent{
		EventID:       current.PendingCompletionEventID,
		ConsumerToken: strings.TrimSpace(current.CompletionConsumerToken),
		Pane:          current.Pane,
		PaneID:        strings.TrimSpace(current.OccupancyKey),
		PaneTarget:    strings.TrimSpace(current.DispatchTarget),
		AgentType:     current.AgentType,
		BeadID:        current.BeadID,
		Timestamp:     time.Now(),
		Duration:      time.Since(startTime),
		IsFailed:      current.PendingTerminalStatus == assignment.StatusFailed,
		FailReason:    current.PendingTerminalReason,
	}
}

func completionEventFromDurableTerminal(current *assignment.Assignment, event *CompletionEvent) (*CompletionEvent, error) {
	if current == nil {
		return nil, nil
	}
	if event == nil {
		event = &CompletionEvent{}
	}
	startTime := current.AssignedAt
	if current.StartedAt != nil {
		startTime = *current.StartedAt
	}
	paneID, err := assignment.CanonicalPaneIdentity(current)
	if err != nil {
		return nil, err
	}
	event.EventID = strings.TrimSpace(current.PendingCompletionEventID)
	event.ConsumerToken = strings.TrimSpace(current.CompletionConsumerToken)
	event.Pane = current.Pane
	event.PaneID = paneID
	event.PaneTarget = strings.TrimSpace(current.DispatchTarget)
	event.AgentType = current.AgentType
	event.BeadID = current.BeadID
	event.IsFailed = current.Status == assignment.StatusFailed
	event.FailReason = current.FailReason
	detectedAt := event.Timestamp
	if current.CompletionDetectedAt != nil {
		detectedAt = *current.CompletionDetectedAt
	} else if detectedAt.IsZero() {
		detectedAt = time.Now()
	}
	event.Timestamp = detectedAt
	event.Duration = detectedAt.Sub(startTime)
	if event.Duration < 0 {
		event.Duration = 0
	}
	return event, nil
}

func (d *CompletionDetector) emitCompletionEvent(ctx context.Context, observed *assignment.Assignment, event *CompletionEvent, events chan<- CompletionEvent) bool {
	if event == nil || observed == nil || strings.TrimSpace(event.EventID) == "" ||
		(observed.Status != assignment.StatusCompleted && observed.Status != assignment.StatusFailed) ||
		strings.TrimSpace(observed.PendingCompletionEventID) != strings.TrimSpace(event.EventID) {
		return true
	}
	now := time.Now()
	d.mu.Lock()
	recentlyEmitted := d.eventRecordedRecentlyLocked(observed, now)
	d.mu.Unlock()
	if recentlyEmitted {
		return true
	}
	claimed, acquired, claimErr := d.Store.ClaimPendingCompletionEvent(
		ctx, observed.BeadID, event.EventID, d.consumerToken, d.Config.CompletionLeaseDuration,
	)
	if claimErr != nil {
		if ctx.Err() != nil {
			return false
		}
		slog.Warn("completion detector failed to claim pending event", "bead", observed.BeadID, "event_id", event.EventID, "error", claimErr)
		return true
	}
	if !acquired {
		return true
	}
	observed = claimed
	event.ConsumerToken = d.consumerToken
	event.LeaseDuration = d.Config.CompletionLeaseDuration
	d.mu.Lock()
	if !d.recordEventLocked(observed, now) {
		d.mu.Unlock()
		return true
	}
	delete(d.activityTracker, assignmentTargetKey(observed))
	d.mu.Unlock()

	select {
	case events <- *event:
		return true
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.recentEvents, assignmentAttemptKey(observed))
		d.mu.Unlock()
		return false
	}
}

func assignmentEligibleForCompletionScan(a *assignment.Assignment) bool {
	if a == nil || a.ClearState != assignment.ClearStateNone {
		return false
	}
	return a.Status == assignment.StatusAssigned || a.Status == assignment.StatusWorking
}

// checkAssignment checks a single assignment for completion
func (d *CompletionDetector) checkAssignment(ctx context.Context, a *assignment.Assignment) (*CompletionEvent, error) {
	if _, err := assignment.CanonicalPaneIdentity(a); err != nil {
		return nil, err
	}
	startTime := a.AssignedAt
	if a.StartedAt != nil {
		startTime = *a.StartedAt
	}

	// 1. Check if pane exists
	paneActivities, err := tmux.GetPanesWithActivityContext(ctx, d.Session)
	if err != nil {
		return nil, nil // Can't check, try later
	}
	panes := make([]tmux.Pane, 0, len(paneActivities))
	for _, activity := range paneActivities {
		panes = append(panes, activity.Pane)
	}

	pane, err := resolveAssignmentPane(d.Session, a, panes)
	if err != nil {
		if errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) {
			return nil, err
		}
		return &CompletionEvent{
			Pane:       a.Pane,
			PaneTarget: strings.TrimSpace(a.DispatchTarget),
			AgentType:  a.AgentType,
			BeadID:     a.BeadID,
			Method:     MethodPaneLost,
			Timestamp:  time.Now(),
			Duration:   time.Since(startTime),
			IsFailed:   true,
			FailReason: fmt.Sprintf("pane target cannot be resolved safely: %v", err),
		}, nil
	}
	target := pane.ID
	if target == "" {
		target = fmt.Sprintf("%s:%s", d.Session, pane.Ref().Physical())
	}

	// 2. Check bead status via br (most reliable)
	if d.isBrAvailable() {
		if closed, err := d.checkBeadClosed(ctx, a.BeadID); err == nil && closed {
			output, _ := tmux.CapturePaneOutputContext(ctx, target, d.Config.CaptureLines)
			return &CompletionEvent{
				Pane:       a.Pane,
				PaneID:     pane.ID,
				PaneTarget: pane.Ref().Physical(),
				AgentType:  a.AgentType,
				BeadID:     a.BeadID,
				Method:     MethodBeadClosed,
				Timestamp:  time.Now(),
				Duration:   time.Since(startTime),
				Output:     truncateOutput(output, 500),
			}, nil
		}
	}

	// 3. Capture pane output for pattern/idle detection
	output, err := tmux.CapturePaneOutputContext(ctx, target, d.Config.CaptureLines)
	if err != nil {
		// Can't capture, rely on bead polling
		return nil, nil
	}
	paneActivity := tmux.PaneActivity{Pane: pane}
	for _, activity := range paneActivities {
		if activity.Pane.ID == pane.ID {
			paneActivity = activity
			break
		}
	}
	paneObservation := d.observer.ObservePaneCapture(d.Session, paneActivity, output, nil)
	if assignmentObservationShowsWorking(a, paneObservation) {
		applied, err := d.Store.MarkWorkingIfCurrent(ctx, a)
		if err != nil {
			slog.Warn("completion detector failed to mark assignment working", "bead", a.BeadID, "pane_id", pane.ID, "error", err)
		} else if applied {
			a.Status = assignment.StatusWorking
		}
	}

	// 4. Check for failure patterns
	if reason := d.matchFailurePatterns(output); reason != "" {
		return &CompletionEvent{
			Pane:       a.Pane,
			PaneID:     pane.ID,
			PaneTarget: pane.Ref().Physical(),
			AgentType:  a.AgentType,
			BeadID:     a.BeadID,
			Method:     MethodPatternMatch,
			Timestamp:  time.Now(),
			Duration:   time.Since(startTime),
			Output:     truncateOutput(output, 500),
			IsFailed:   true,
			FailReason: reason,
		}, nil
	}

	// 5. Check for completion patterns — but ONLY as a SUCCESS when an
	// authoritative bead-closed check confirms it. The completion patterns
	// (e.g. `br\s+close`) also match the dispatch prompt's OWN ECHO: the
	// prompt text tells the agent to run `br close <id>`, so a crashed or slow
	// agent whose pane still shows the un-acted-on prompt would otherwise be
	// declared complete — silently dropping its work and (because completed
	// beads are suppressed from re-dispatch) stranding the bead forever. We
	// require br to report the bead actually closed before trusting the
	// pattern. When br is unavailable we cannot confirm, so we do NOT mark a
	// pattern-only match complete (a false completion is far worse than a
	// late one — the bead keeps getting watched and eventually times out).
	if d.matchCompletionPatterns(output) {
		if d.isBrAvailable() {
			if closed, err := d.checkBeadClosed(ctx, a.BeadID); err == nil && closed {
				return &CompletionEvent{
					Pane:       a.Pane,
					PaneID:     pane.ID,
					PaneTarget: pane.Ref().Physical(),
					AgentType:  a.AgentType,
					BeadID:     a.BeadID,
					Method:     MethodPatternMatch,
					Timestamp:  time.Now(),
					Duration:   time.Since(startTime),
					Output:     truncateOutput(output, 500),
				}, nil
			}
		}
		// Pattern matched but the bead is not (yet) closed: treat as not-done.
		// This is the prompt-echo case — keep watching rather than completing.
	}

	// 6. Check idle detection. An idle/stalled timeout is NOT a success: it
	// means the agent stopped producing output. If its bead is genuinely
	// closed, step 2 (or step 5) would already have reported a success this
	// tick, so reaching here with an idle timeout means the work is NOT done.
	// Re-confirm against br to be safe (the bead may have closed between the
	// step-2 check and now), then report FAILED so the pane is released and
	// the bead can be reassigned instead of being silently marked complete.
	if event := d.checkIdleWhenSafe(a, output, startTime, paneObservation); event != nil {
		event.PaneID = pane.ID
		event.PaneTarget = pane.Ref().Physical()
		if d.isBrAvailable() {
			if closed, err := d.checkBeadClosed(ctx, a.BeadID); err == nil && closed {
				event.Method = MethodBeadClosed
				event.IsFailed = false
				event.FailReason = ""
				return event, nil
			}
		}
		event.IsFailed = true
		event.FailReason = fmt.Sprintf("agent idle for %s with bead %s still open (stalled or crashed)", d.Config.IdleThreshold, a.BeadID)
		return event, nil
	}

	return nil, nil
}

func assignmentObservationShowsWorking(a *assignment.Assignment, observation statuspkg.PaneObservation) bool {
	return a != nil && a.Status == assignment.StatusAssigned &&
		observation.Current.Freshness == statuspkg.FreshnessFresh &&
		observation.Current.Error == "" &&
		observation.Current.Status.State == statuspkg.StateWorking &&
		statuspkg.ObservationConfidenceIsActionable(observation.Current.Confidence)
}

// checkIdleWhenSafe starts and advances the inactivity timer only while the
// canonical current observation is fresh, confident, and idle. Active,
// unknown, stale, and weak-heuristic output clears the timer so unchanged
// scrollback can never fail work that SessionObserver still classifies busy.
func (d *CompletionDetector) checkIdleWhenSafe(a *assignment.Assignment, output string, startTime time.Time, observation statuspkg.PaneObservation) *CompletionEvent {
	if !observation.SafeToDispatch() {
		d.mu.Lock()
		delete(d.activityTracker, assignmentTargetKey(a))
		d.mu.Unlock()
		return nil
	}
	return d.checkIdle(a, output, startTime)
}

func assignmentAttemptKey(a *assignment.Assignment) string {
	if a == nil {
		return ""
	}
	if idempotencyKey := strings.TrimSpace(a.IdempotencyKey); idempotencyKey != "" {
		return fmt.Sprintf("%s:%s:%s", a.BeadID, assignmentTargetKey(a), idempotencyKey)
	}

	timestamp := a.AssignedAt
	if timestamp.IsZero() {
		if a.StartedAt != nil {
			timestamp = *a.StartedAt
		} else {
			return fmt.Sprintf("%s:%s", a.BeadID, assignmentTargetKey(a))
		}
	}
	return fmt.Sprintf("%s:%s:%s", a.BeadID, assignmentTargetKey(a), timestamp.UTC().Format(time.RFC3339Nano))
}

func assignmentTargetKey(a *assignment.Assignment) string {
	target, _ := assignment.CanonicalPaneIdentity(a)
	return target
}

func resolveAssignmentPane(session string, a *assignment.Assignment, panes []tmux.Pane) (tmux.Pane, error) {
	if a == nil {
		return tmux.Pane{}, fmt.Errorf("assignment is required")
	}
	target, err := assignment.CanonicalPaneIdentity(a)
	if err != nil {
		return tmux.Pane{}, err
	}
	if prefix := strings.TrimSpace(session) + ":"; target != "" && strings.HasPrefix(target, prefix) {
		target = strings.TrimPrefix(target, prefix)
	}
	resolved, err := tmux.ResolvePaneSelectors(panes, []string{target}, true)
	if err != nil {
		return tmux.Pane{}, err
	}
	return resolved[0], nil
}

func (d *CompletionDetector) pruneExpiredRecentEventsLocked(now time.Time) {
	if len(d.recentEvents) == 0 {
		return
	}
	for key, lastEvent := range d.recentEvents {
		if now.Sub(lastEvent) >= d.Config.DedupWindow {
			delete(d.recentEvents, key)
		}
	}
}

func (d *CompletionDetector) recordEventLocked(a *assignment.Assignment, now time.Time) bool {
	d.pruneExpiredRecentEventsLocked(now)
	key := assignmentAttemptKey(a)
	lastEvent, exists := d.recentEvents[key]
	if exists && now.Sub(lastEvent) < d.Config.DedupWindow {
		return false
	}
	d.recentEvents[key] = now
	return true
}

func (d *CompletionDetector) eventRecordedRecentlyLocked(a *assignment.Assignment, now time.Time) bool {
	d.pruneExpiredRecentEventsLocked(now)
	lastEvent, exists := d.recentEvents[assignmentAttemptKey(a)]
	return exists && now.Sub(lastEvent) < d.Config.DedupWindow
}

// CheckNow performs an immediate check for one canonical pane identity.
func (d *CompletionDetector) CheckNow(target string) (*CompletionEvent, error) {
	if d.Store == nil {
		return nil, fmt.Errorf("no assignment store configured")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("canonical pane target is required")
	}

	// Find the assignment for this exact durable pane identity.
	var targets []*assignment.Assignment
	for _, a := range d.Store.ListActive() {
		if !assignmentEligibleForCompletionScan(a) {
			continue
		}
		identity, err := assignment.CanonicalPaneIdentity(a)
		if err != nil {
			return nil, err
		}
		if identity == target {
			targets = append(targets, a)
		}
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("no active assignment for pane %s", target)
	}
	if len(targets) > 1 {
		return nil, fmt.Errorf("canonical pane %s has %d active assignments", target, len(targets))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return d.checkAssignment(ctx, targets[0])
}

// isBrAvailable checks if the br CLI is available (cached)
func (d *CompletionDetector) isBrAvailable() bool {
	d.mu.RLock()
	if d.brAvailable != nil {
		result := *d.brAvailable
		d.mu.RUnlock()
		return result
	}
	d.mu.RUnlock()

	// Check availability
	d.mu.Lock()
	defer d.mu.Unlock()

	// Double-check after acquiring write lock
	if d.brAvailable != nil {
		return *d.brAvailable
	}

	_, err := exec.LookPath("br")
	available := err == nil
	d.brAvailable = &available

	if !available && d.Config.GracefulDegrading {
		slog.Debug("completion detector using fallback detection because br CLI is unavailable", "session", d.Session)
	}

	return available
}

// checkBeadClosed uses br CLI to check if a bead is closed
func (d *CompletionDetector) checkBeadClosed(ctx context.Context, beadID string) (bool, error) {
	maxRetries := 0
	if d.Config.RetryOnError {
		maxRetries = d.Config.MaxRetries
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		closed, err := checkBeadClosedOnce(ctx, beadID)
		if err == nil {
			return closed, nil
		}
		lastErr = err
		if attempt == maxRetries {
			break
		}
		if err := waitForRetry(ctx, d.Config.RetryInterval); err != nil {
			return false, err
		}
	}

	return false, lastErr
}

func checkBeadClosedOnce(ctx context.Context, beadID string) (bool, error) {
	cmd := exec.CommandContext(ctx, "br", "show", beadID, "--json")
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}

	// `br show --json` includes related records such as dependents, each with
	// its own status. Searching the whole JSON text made an open assignment look
	// complete whenever any dependent was closed. Decode the returned records
	// and inspect only the requested bead's top-level record.
	var records []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(output, &records); err != nil {
		return false, fmt.Errorf("decode br show response for %q: %w", beadID, err)
	}
	for _, record := range records {
		if record.ID == beadID {
			return record.Status == "closed", nil
		}
	}
	return false, fmt.Errorf("br show response did not contain requested bead %q", beadID)
}

func waitForRetry(ctx context.Context, interval time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if interval <= 0 {
		return nil
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// matchCompletionPatterns checks output against completion patterns
func (d *CompletionDetector) matchCompletionPatterns(output string) bool {
	d.mu.RLock()
	patterns := d.Patterns
	d.mu.RUnlock()

	for _, re := range patterns {
		if re.MatchString(output) {
			return true
		}
	}
	return false
}

// matchFailurePatterns checks output against failure patterns, returns matched reason
func (d *CompletionDetector) matchFailurePatterns(output string) string {
	d.mu.RLock()
	patterns := d.FailPattern
	d.mu.RUnlock()

	for _, re := range patterns {
		if match := re.FindString(output); match != "" {
			return match
		}
	}
	return ""
}

// checkIdle detects completion via inactivity
func (d *CompletionDetector) checkIdle(a *assignment.Assignment, output string, startTime time.Time) *CompletionEvent {
	d.mu.Lock()
	defer d.mu.Unlock()

	key := assignmentAttemptKey(a)
	targetKey := assignmentTargetKey(a)
	state, exists := d.activityTracker[targetKey]
	if !exists || state.assignmentKey != key {
		state = &activityState{
			assignmentKey:  key,
			lastOutputTime: time.Now(),
			lastOutput:     output,
			burstActive:    true, // Start active so we can detect if it never outputs anything
			burstStarted:   time.Now(),
		}
		d.activityTracker[targetKey] = state
		return nil
	}

	// Check if output changed
	if output != state.lastOutput {
		// Activity detected
		state.lastOutput = output
		state.lastOutputTime = time.Now()

		// Check for activity burst start
		if !state.burstActive {
			state.burstActive = true
			state.burstStarted = time.Now()
		}
		return nil
	}

	// Output unchanged - check for idle timeout after burst
	if state.burstActive && time.Since(state.lastOutputTime) >= d.Config.IdleThreshold {
		// Reset state
		state.burstActive = false

		return &CompletionEvent{
			Pane:       a.Pane,
			PaneTarget: strings.TrimSpace(a.DispatchTarget),
			AgentType:  a.AgentType,
			BeadID:     a.BeadID,
			Method:     MethodIdle,
			Timestamp:  time.Now(),
			Duration:   time.Since(startTime),
			Output:     truncateOutput(output, 500),
		}
	}

	return nil
}

// truncateOutput limits output to maxLen characters
func truncateOutput(output string, maxLen int) string {
	if len(output) <= maxLen {
		return output
	}
	return "..." + output[len(output)-maxLen:]
}
