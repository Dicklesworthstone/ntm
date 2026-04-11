// Package robot provides machine-readable output for AI agents.
// attention_feed.go implements the attention feed runtime components.
package robot

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ntmevents "github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/integrations/pt"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

// AttentionStore defines the interface for durable attention event storage.
// This interface allows the AttentionFeed to persist events to SQLite.
type AttentionStore interface {
	// AppendAttentionEvent inserts an attention event and returns its cursor.
	AppendAttentionEvent(event *state.StoredAttentionEvent) (int64, error)
	// GetAttentionEventsSince returns events with cursor > sinceCursor.
	GetAttentionEventsSince(sinceCursor int64, limit int) ([]state.StoredAttentionEvent, error)
	// GetAttentionEventsInTimeRange returns events inside a bounded time window.
	GetAttentionEventsInTimeRange(start, end time.Time, limit int) ([]state.StoredAttentionEvent, error)
	// GetAttentionItemStatesForCursors returns durable operator state keyed by cursor.
	GetAttentionItemStatesForCursors(cursors []int64) (map[int64]state.AttentionItemState, error)
	// GetAttentionReplayWindow returns the currently replayable cursor range.
	GetAttentionReplayWindow() (state.AttentionReplayWindow, error)
	// GetLatestEventCursor returns the most recent event cursor.
	GetLatestEventCursor() (int64, error)
	// GetEventsForIncident returns attention events linked to an incident.
	GetEventsForIncident(incidentID string, limit int) ([]state.StoredAttentionEvent, error)
	// GetIncident loads an incident row by ID.
	GetIncident(id string) (*state.Incident, error)
	// GCExpiredEvents deletes events past their expiration time.
	GCExpiredEvents() (int64, error)
}

type ReconstructionConfidence string

const (
	ReconstructionConfidenceHigh        ReconstructionConfidence = "high"
	ReconstructionConfidenceMedium      ReconstructionConfidence = "medium"
	ReconstructionConfidenceLow         ReconstructionConfidence = "low"
	ReconstructionConfidenceUnavailable ReconstructionConfidence = "unavailable"
)

// ReconstructionMeta describes how a bounded historical response was reconstructed.
type ReconstructionMeta struct {
	RequestedAt    string                   `json:"requested_at,omitempty"`
	Method         string                   `json:"method"`
	Source         string                   `json:"source"`
	EventsReplayed int                      `json:"events_replayed"`
	GapsDetected   int                      `json:"gaps_detected"`
	Interpolations int                      `json:"interpolations"`
	StartedAt      string                   `json:"started_at"`
	CompletedAt    string                   `json:"completed_at"`
	DurationMs     int64                    `json:"duration_ms"`
	Confidence     ReconstructionConfidence `json:"confidence"`
	Warnings       []string                 `json:"warnings,omitempty"`
	ActualStart    string                   `json:"actual_start,omitempty"`
	ActualEnd      string                   `json:"actual_end,omitempty"`
	Partial        bool                     `json:"partial,omitempty"`
}

// =============================================================================
// Configuration
// =============================================================================

// AttentionFeedConfig controls the behavior of the attention feed.
type AttentionFeedConfig struct {
	// JournalSize is the maximum number of events retained for replay.
	// Events beyond this limit are garbage-collected.
	// Default: 10000
	JournalSize int

	// RetentionPeriod is the minimum time events are retained.
	// Events older than this may be garbage-collected even if within JournalSize.
	// Default: 1 hour
	RetentionPeriod time.Duration

	// HeartbeatInterval is how often heartbeat events are emitted.
	// Set to 0 to disable heartbeats.
	// Default: 30 seconds
	HeartbeatInterval time.Duration
}

// DefaultAttentionFeedConfig returns sensible defaults.
func DefaultAttentionFeedConfig() AttentionFeedConfig {
	return AttentionFeedConfig{
		JournalSize:       10000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 30 * time.Second,
	}
}

// =============================================================================
// Cursor Allocator
// =============================================================================

// CursorAllocator generates monotonically increasing cursors.
// It is safe for concurrent use.
type CursorAllocator struct {
	counter atomic.Int64
}

// NewCursorAllocator creates a new cursor allocator starting from 1.
// Cursor 0 is reserved to mean "no cursor" or "start from beginning".
func NewCursorAllocator() *CursorAllocator {
	return &CursorAllocator{}
}

// Next returns the next cursor value.
// Cursors are guaranteed to be strictly monotonically increasing.
func (c *CursorAllocator) Next() int64 {
	return c.counter.Add(1)
}

// Current returns the most recently allocated cursor.
// Returns 0 if no cursors have been allocated.
func (c *CursorAllocator) Current() int64 {
	return c.counter.Load()
}

// =============================================================================
// Journal Entry
// =============================================================================

// journalEntry wraps an event with metadata for the journal.
type journalEntry struct {
	event     AttentionEvent
	timestamp time.Time
}

// =============================================================================
// Attention Journal
// =============================================================================

// AttentionJournal is a bounded, thread-safe buffer for attention events.
// It supports replay from a cursor and automatic garbage collection.
type AttentionJournal struct {
	mu        sync.Mutex
	entries   []journalEntry
	size      int
	oldest    int64
	newest    int64
	lastEvent time.Time
	retention time.Duration

	// Metrics for observability
	totalAppended   atomic.Int64
	totalEvicted    atomic.Int64
	replayRequests  atomic.Int64
	expiredRequests atomic.Int64
}

// NewAttentionJournal creates a new journal with the specified capacity.
func NewAttentionJournal(size int, retention time.Duration) *AttentionJournal {
	if size < 1 {
		size = 1000
	}
	if retention <= 0 {
		retention = time.Hour
	}
	return &AttentionJournal{
		entries:   make([]journalEntry, 0, size),
		size:      size,
		retention: retention,
	}
}

// Append adds an event to the journal.
// The event must already have a cursor assigned.
func (j *AttentionJournal) Append(event AttentionEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()

	now := time.Now()
	j.pruneLocked(now)

	entry := journalEntry{
		event:     cloneAttentionEvent(event),
		timestamp: now,
	}

	if len(j.entries) >= j.size {
		overflow := len(j.entries) - j.size + 1
		j.entries = j.entries[overflow:]
		j.totalEvicted.Add(int64(overflow))
	}

	j.entries = append(j.entries, entry)
	j.oldest = j.entries[0].event.Cursor
	j.newest = event.Cursor
	j.lastEvent = now
	j.totalAppended.Add(1)
}

// Replay returns events with cursor > sinceCursor.
// If sinceCursor is expired, returns ErrCursorExpired.
// If sinceCursor is 0, returns all available events.
// If sinceCursor is -1, returns no events (caller wants to start from "now").
func (j *AttentionJournal) Replay(sinceCursor int64, limit int) ([]AttentionEvent, int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.replayRequests.Add(1)
	j.pruneLocked(time.Now())

	// Handle special cursor values
	if sinceCursor == -1 {
		// Start from "now" - return newest cursor, no events
		return []AttentionEvent{}, j.newest, nil
	}

	if limit <= 0 {
		limit = 100
	}

	// Check if cursor is expired
	if sinceCursor > 0 && j.cursorExpiredLocked(sinceCursor) {
		j.expiredRequests.Add(1)
		return nil, 0, &CursorExpiredError{
			RequestedCursor: sinceCursor,
			EarliestCursor:  j.earliestReplayCursorLocked(),
			RetentionPeriod: j.retention,
		}
	}

	events := make([]AttentionEvent, 0, minInt(limit, len(j.entries)))
	for _, entry := range j.entries {
		if entry.event.Cursor <= sinceCursor {
			continue
		}
		events = append(events, cloneAttentionEvent(entry.event))
		if len(events) == limit {
			break
		}
	}

	return events, j.newest, nil
}

// Stats returns current journal statistics.
func (j *AttentionJournal) Stats() JournalStats {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.pruneLocked(time.Now())

	var lastEventTime *time.Time
	if !j.lastEvent.IsZero() {
		ts := j.lastEvent.UTC()
		lastEventTime = &ts
	}

	return JournalStats{
		Size:            j.size,
		Count:           len(j.entries),
		OldestCursor:    j.oldest,
		NewestCursor:    j.newest,
		LastEventTime:   lastEventTime,
		RetentionPeriod: j.retention,
		TotalAppended:   j.totalAppended.Load(),
		TotalEvicted:    j.totalEvicted.Load(),
		ReplayRequests:  j.replayRequests.Load(),
		ExpiredRequests: j.expiredRequests.Load(),
	}
}

// RangeStats returns retained journal stats for entries with cursor >= minCursor.
func (j *AttentionJournal) RangeStats(minCursor int64) (count int, oldest, newest int64) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.pruneLocked(time.Now())

	for _, entry := range j.entries {
		if entry.event.Cursor < minCursor {
			continue
		}
		if count == 0 {
			oldest = entry.event.Cursor
		}
		count++
		newest = entry.event.Cursor
	}

	return count, oldest, newest
}

func (j *AttentionJournal) pruneLocked(now time.Time) {
	if len(j.entries) == 0 {
		j.oldest = 0
		return
	}

	cutoff := now.Add(-j.retention)
	trim := 0
	for trim < len(j.entries) && j.entries[trim].timestamp.Before(cutoff) {
		trim++
	}
	if trim > 0 {
		j.entries = j.entries[trim:]
		j.totalEvicted.Add(int64(trim))
	}

	if len(j.entries) == 0 {
		j.oldest = 0
		return
	}
	j.oldest = j.entries[0].event.Cursor
}

func (j *AttentionJournal) cursorExpiredLocked(sinceCursor int64) bool {
	if sinceCursor <= 0 || j.newest == 0 {
		return false
	}
	if len(j.entries) == 0 {
		return sinceCursor < j.newest
	}
	earliest := j.entries[0].event.Cursor
	return sinceCursor < earliest-1
}

func (j *AttentionJournal) earliestReplayCursorLocked() int64 {
	if len(j.entries) > 0 {
		return j.entries[0].event.Cursor
	}
	return j.newest
}

// JournalStats contains observability metrics for the journal.
type JournalStats struct {
	Size            int           `json:"size"`
	Count           int           `json:"count"`
	OldestCursor    int64         `json:"oldest_cursor"`
	NewestCursor    int64         `json:"newest_cursor"`
	LastEventTime   *time.Time    `json:"last_event_time,omitempty"`
	RetentionPeriod time.Duration `json:"retention_period"`
	TotalAppended   int64         `json:"total_appended"`
	TotalEvicted    int64         `json:"total_evicted"`
	ReplayRequests  int64         `json:"replay_requests"`
	ExpiredRequests int64         `json:"expired_requests"`
}

// =============================================================================
// Cursor Expired Error
// =============================================================================

// CursorExpiredError indicates a cursor references garbage-collected events.
type CursorExpiredError struct {
	RequestedCursor int64
	EarliestCursor  int64
	RetentionPeriod time.Duration
}

func (e *CursorExpiredError) Error() string {
	return fmt.Sprintf("cursor %d has expired (earliest available: %d, retention: %s)",
		e.RequestedCursor, e.EarliestCursor, e.RetentionPeriod)
}

// ToDetails converts the error to CursorExpiredDetails for JSON output.
func (e *CursorExpiredError) ToDetails() CursorExpiredDetails {
	return CursorExpiredDetails{
		RequestedCursor: e.RequestedCursor,
		EarliestCursor:  e.EarliestCursor,
		RetentionPeriod: e.RetentionPeriod.String(),
		ResyncCommand:   "ntm --robot-snapshot",
	}
}

// =============================================================================
// Subscription
// =============================================================================

// AttentionHandler is called for each event in the feed.
type AttentionHandler func(AttentionEvent)

// subscription wraps a handler with an ID for management.
type subscription struct {
	id      uint64
	handler AttentionHandler
}

// =============================================================================
// Attention Feed Service
// =============================================================================

// AttentionFeed is the main service for the attention feed system.
// It manages cursor allocation, event journaling, and subscriptions.
// When a store is configured, events are persisted to SQLite for restart-safe replay.
type AttentionFeed struct {
	config  AttentionFeedConfig
	cursor  *CursorAllocator
	journal *AttentionJournal
	store   AttentionStore // Optional SQLite store for durable persistence

	appendMu            sync.Mutex
	ephemeralFromCursor atomic.Int64 // First cursor that exists only in-memory after store write failure
	pendingEvents       []AttentionEvent
	drainingEvents      bool
	dedupMu             sync.Mutex
	recentDedup         map[string]attentionDedupEntry

	subMu     sync.RWMutex
	subNextID atomic.Uint64
	subs      map[uint64]subscription

	stopOnce sync.Once
	stopCh   chan struct{}
	stopWg   sync.WaitGroup
}

type attentionDedupEntry struct {
	surfacedAt       time.Time
	suppressedCount  int
	suppressedSince  time.Time
	lastSuppressedAt time.Time
}

// ActuationStage identifies where an actuation event sits in the closed loop.
type ActuationStage string

const (
	ActuationStageRequest      ActuationStage = "request"
	ActuationStageOutcome      ActuationStage = "outcome"
	ActuationStageVerification ActuationStage = "verification"
)

// ActuationRecord is a high-level description of an operator-triggered action
// that should be published into the durable attention stream.
type ActuationRecord struct {
	Session        string
	Action         string
	Stage          ActuationStage
	Source         string
	Method         string
	RequestID      string
	CorrelationID  string
	IdempotencyKey string
	Targets        []string
	Successful     []string
	Failed         []map[string]any
	Confirmations  []map[string]any
	Pending        []string
	MessagePreview string
	Result         string
	Verification   string
	ErrorCode      string
	Error          string
	Blocked        bool
	TimedOut       bool
	ReasonCode     string
	Summary        string
	Actionability  Actionability
	Severity       Severity
}

// AttentionFeedOption is a functional option for configuring AttentionFeed.
type AttentionFeedOption func(*AttentionFeed)

// WithAttentionStore configures a durable SQLite store for attention events.
// When set, events are persisted to survive restarts and support cursor-based replay.
func WithAttentionStore(store AttentionStore) AttentionFeedOption {
	return func(f *AttentionFeed) {
		f.store = store
	}
}

// NewAttentionFeed creates a new attention feed service.
func NewAttentionFeed(config AttentionFeedConfig, opts ...AttentionFeedOption) *AttentionFeed {
	if config.JournalSize == 0 {
		config = DefaultAttentionFeedConfig()
	}

	feed := &AttentionFeed{
		config:      config,
		cursor:      NewCursorAllocator(),
		journal:     NewAttentionJournal(config.JournalSize, config.RetentionPeriod),
		recentDedup: make(map[string]attentionDedupEntry),
		subs:        make(map[uint64]subscription),
		stopCh:      make(chan struct{}),
	}

	// Apply options
	for _, opt := range opts {
		opt(feed)
	}

	// Sync cursor from store if available
	feed.syncCursorFromStore()

	// Start heartbeat if configured
	if config.HeartbeatInterval > 0 {
		feed.stopWg.Add(1)
		go feed.heartbeatLoop()
	}

	return feed
}

func (f *AttentionFeed) enqueuePendingEventLocked(event AttentionEvent) bool {
	f.pendingEvents = append(f.pendingEvents, cloneAttentionEvent(event))
	if f.drainingEvents {
		return false
	}
	f.drainingEvents = true
	return true
}

// Append allocates a cursor, stores the event, and notifies subscribers.
// The caller provides an event without a cursor; this method assigns one.
// When a store is configured, events are persisted to SQLite for durability.
func (f *AttentionFeed) Append(event AttentionEvent) AttentionEvent {
	event.NextActions = sanitizeNextActions(event.NextActions)

	// Ensure timestamp if not set
	if event.Ts == "" {
		event.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	}

	f.appendMu.Lock()

	// Persist to store if available (store assigns cursor via AUTOINCREMENT).
	// If a store write fails once, switch new events to journal-only mode so
	// cursor allocation remains monotonic and replay stays internally consistent.
	if f.store != nil && f.ephemeralFromCursor.Load() == 0 {
		stored := attentionEventToStored(event, f.config.RetentionPeriod)
		cursor, err := f.store.AppendAttentionEvent(&stored)
		if err == nil {
			event.Cursor = cursor
			f.cursor.counter.Store(cursor)
		} else {
			event.Cursor = f.cursor.Next()
			f.ephemeralFromCursor.CompareAndSwap(0, event.Cursor)
		}
	} else {
		event.Cursor = f.cursor.Next()
		if f.store != nil {
			f.ephemeralFromCursor.CompareAndSwap(0, event.Cursor)
		}
	}

	// Store in journal (also serves as in-memory cache)
	f.journal.Append(event)
	startDrain := f.enqueuePendingEventLocked(event)
	f.appendMu.Unlock()

	if startDrain {
		f.drainPendingEvents()
	}

	return event
}

// PublishEphemeral allocates a cursor and notifies subscribers without
// persisting or journaling the event. This is used for live-only control
// signals such as watch heartbeats that should not appear in replay.
func (f *AttentionFeed) PublishEphemeral(event AttentionEvent) AttentionEvent {
	event.NextActions = sanitizeNextActions(event.NextActions)

	if event.Ts == "" {
		event.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	}

	f.appendMu.Lock()
	event.Cursor = f.cursor.Next()
	startDrain := f.enqueuePendingEventLocked(event)
	f.appendMu.Unlock()

	if startDrain {
		f.drainPendingEvents()
	}

	return event
}

func (f *AttentionFeed) appendDeduplicated(event AttentionEvent, window time.Duration) (AttentionEvent, bool) {
	key := strings.TrimSpace(event.DedupKey)
	if key == "" || window <= 0 {
		return f.Append(event), true
	}

	ts := parseAttentionEventTime(event.Ts)
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	cutoff := ts.Add(-window)

	f.dedupMu.Lock()
	for existingKey, entry := range f.recentDedup {
		lastSeen := entry.surfacedAt
		if entry.lastSuppressedAt.After(lastSeen) {
			lastSeen = entry.lastSuppressedAt
		}
		if lastSeen.Before(cutoff) {
			delete(f.recentDedup, existingKey)
		}
	}
	if entry, ok := f.recentDedup[key]; ok && !entry.surfacedAt.Before(cutoff) {
		if entry.suppressedCount == 0 {
			entry.suppressedSince = ts
		}
		entry.suppressedCount++
		entry.lastSuppressedAt = ts
		f.recentDedup[key] = entry
		f.dedupMu.Unlock()
		return AttentionEvent{}, false
	}
	if entry, ok := f.recentDedup[key]; ok && entry.suppressedCount > 0 {
		event = annotateAttentionDedupResurfacing(event, window, entry)
	}
	f.recentDedup[key] = attentionDedupEntry{surfacedAt: ts}
	f.dedupMu.Unlock()

	return f.Append(event), true
}

const (
	attentionDetailItemKey                 = "attention_item_key"
	attentionDetailState                   = "attention_state"
	attentionDetailStoredState             = "attention_stored_state"
	attentionDetailPreviousState           = "attention_previous_state"
	attentionDetailFingerprint             = "attention_fingerprint"
	attentionDetailStoredFingerprint       = "attention_stored_fingerprint"
	attentionDetailPreviousFingerprint     = "attention_previous_fingerprint"
	attentionDetailAcknowledgedAt          = "attention_acknowledged_at"
	attentionDetailAcknowledgedBy          = "attention_acknowledged_by"
	attentionDetailSnoozedUntil            = "attention_snoozed_until"
	attentionDetailDismissedAt             = "attention_dismissed_at"
	attentionDetailDismissedBy             = "attention_dismissed_by"
	attentionDetailPinned                  = "attention_pinned"
	attentionDetailMuted                   = "attention_muted"
	attentionDetailOverridePriority        = "attention_override_priority"
	attentionDetailOverrideReason          = "attention_override_reason"
	attentionDetailOverrideExpiresAt       = "attention_override_expires_at"
	attentionDetailResurfacingPolicy       = "attention_resurfacing_policy"
	attentionDetailHiddenReason            = "attention_hidden_reason"
	attentionDetailExplanationCode         = "attention_explanation_code"
	attentionDetailResurfaced              = "resurfaced"
	attentionDetailResurfaceReason         = "resurface_reason"
	attentionDetailCooldownWindowMS        = "cooldown_window_ms"
	attentionDetailCooldownSuppressedCount = "cooldown_suppressed_count"
	attentionDetailCooldownSuppressedSince = "cooldown_suppressed_since"
	attentionDetailCooldownLastSuppressed  = "cooldown_last_suppressed_at"

	attentionHiddenReasonAcknowledged = "acknowledged"
	attentionHiddenReasonSnoozed      = "snoozed"
	attentionHiddenReasonDismissed    = "dismissed"
	attentionHiddenReasonMuted        = "muted"

	attentionExplainNew        = "EXPLAIN_ATT_NEW"
	attentionExplainAck        = "EXPLAIN_ATT_ACK"
	attentionExplainSnoozed    = "EXPLAIN_ATT_SNOOZED"
	attentionExplainDismissed  = "EXPLAIN_ATT_DISMISSED"
	attentionExplainPinned     = "EXPLAIN_ATT_PINNED"
	attentionExplainMuted      = "EXPLAIN_ATT_MUTED"
	attentionExplainEscalated  = "EXPLAIN_ATT_ESCALATED"
	attentionExplainResurfaced = "EXPLAIN_ATT_RESURFACED"

	attentionResurfaceReasonCooldownExpired   = "cooldown_expired"
	attentionResurfaceReasonFingerprintChange = "fingerprint_changed"
	attentionResurfaceReasonSnoozeExpired     = "snooze_expired"
	attentionResurfaceReasonSeverityEscalated = "severity_escalation"
)

func annotateAttentionDedupResurfacing(event AttentionEvent, window time.Duration, entry attentionDedupEntry) AttentionEvent {
	if entry.suppressedCount <= 0 {
		return event
	}
	if event.Details == nil {
		event.Details = map[string]any{}
	}
	event.Details[attentionDetailResurfaced] = true
	event.Details[attentionDetailResurfaceReason] = attentionResurfaceReasonCooldownExpired
	event.Details[attentionDetailCooldownWindowMS] = window.Milliseconds()
	event.Details[attentionDetailCooldownSuppressedCount] = entry.suppressedCount
	if !entry.suppressedSince.IsZero() {
		event.Details[attentionDetailCooldownSuppressedSince] = entry.suppressedSince.UTC().Format(time.RFC3339Nano)
	}
	if !entry.lastSuppressedAt.IsZero() {
		event.Details[attentionDetailCooldownLastSuppressed] = entry.lastSuppressedAt.UTC().Format(time.RFC3339Nano)
	}
	return event
}

func attentionItemKeyForStoredEvent(event state.StoredAttentionEvent) string {
	if key := strings.TrimSpace(event.DedupKey); key != "" {
		return "dedup:" + key
	}
	return fmt.Sprintf("cursor:%d", event.Cursor)
}

func attentionItemKeyForReplayEvent(event AttentionEvent) string {
	if key := strings.TrimSpace(event.DedupKey); key != "" {
		return "dedup:" + key
	}
	return fmt.Sprintf("cursor:%d", event.Cursor)
}

func attentionEventFingerprint(event AttentionEvent) string {
	payload := map[string]any{
		"session":       strings.TrimSpace(event.Session),
		"pane":          event.Pane,
		"category":      string(event.Category),
		"type":          string(event.Type),
		"source":        strings.TrimSpace(event.Source),
		"actionability": string(event.Actionability),
		"severity":      string(event.Severity),
		"reason_code":   strings.TrimSpace(event.ReasonCode),
		"summary":       strings.TrimSpace(event.Summary),
	}
	if details := attentionMaterialDetails(event.Details); len(details) > 0 {
		payload["details"] = details
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("fp-%x", sum[:8])
}

func attentionMaterialDetails(details map[string]any) map[string]any {
	cloned := cloneAnyMap(details)
	if len(cloned) == 0 {
		return nil
	}
	for key := range cloned {
		switch {
		case strings.HasPrefix(key, "attention_"):
			delete(cloned, key)
		case strings.HasPrefix(key, "digest_"):
			delete(cloned, key)
		case key == attentionDetailResurfaced,
			key == attentionDetailResurfaceReason,
			key == attentionDetailCooldownWindowMS,
			key == attentionDetailCooldownSuppressedCount,
			key == attentionDetailCooldownSuppressedSince,
			key == attentionDetailCooldownLastSuppressed:
			delete(cloned, key)
		}
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func attentionPriorityOverrideActive(itemState *state.AttentionItemState, now time.Time) bool {
	if itemState == nil || strings.TrimSpace(itemState.OverridePriority) == "" {
		return false
	}
	if itemState.OverrideExpiresAt != nil && now.After(itemState.OverrideExpiresAt.UTC()) {
		return false
	}
	return true
}

func applyAttentionPriorityOverride(event AttentionEvent, override string) AttentionEvent {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case "critical":
		event.Severity = SeverityCritical
		event.Actionability = ActionabilityActionRequired
	case "error", "high", "urgent":
		if attentionSeverityRank(event.Severity) < attentionSeverityRank(SeverityError) {
			event.Severity = SeverityError
		}
		event.Actionability = ActionabilityActionRequired
	case "warning", "medium", "interesting":
		if attentionSeverityRank(event.Severity) < attentionSeverityRank(SeverityWarning) {
			event.Severity = SeverityWarning
		}
		if attentionActionabilityRank(event.Actionability) < attentionActionabilityRank(ActionabilityInteresting) {
			event.Actionability = ActionabilityInteresting
		}
	default:
		if attentionSeverityRank(event.Severity) < attentionSeverityRank(SeverityInfo) {
			event.Severity = SeverityInfo
		}
		if attentionActionabilityRank(event.Actionability) < attentionActionabilityRank(ActionabilityInteresting) {
			event.Actionability = ActionabilityInteresting
		}
	}
	return event
}

func annotateAttentionOperatorState(event AttentionEvent, itemKey string, itemState *state.AttentionItemState) AttentionEvent {
	if event.Details == nil {
		event.Details = map[string]any{}
	}
	event.Details[attentionDetailItemKey] = itemKey
	if fingerprint := attentionEventFingerprint(event); fingerprint != "" {
		event.Details[attentionDetailFingerprint] = fingerprint
	}
	if itemState == nil {
		event.Details[attentionDetailState] = state.AttentionStateNew
		event.Details[attentionDetailExplanationCode] = attentionExplainNew
		return event
	}

	now := time.Now().UTC()
	storedState := strings.TrimSpace(string(itemState.State))
	if storedState == "" {
		storedState = string(state.AttentionStateNew)
	}
	event.Details[attentionDetailStoredState] = storedState
	if itemState.Fingerprint != "" {
		event.Details[attentionDetailStoredFingerprint] = itemState.Fingerprint
	}
	if itemState.AcknowledgedAt != nil {
		event.Details[attentionDetailAcknowledgedAt] = itemState.AcknowledgedAt.UTC().Format(time.RFC3339Nano)
	}
	if itemState.AcknowledgedBy != "" {
		event.Details[attentionDetailAcknowledgedBy] = itemState.AcknowledgedBy
	}
	if itemState.SnoozedUntil != nil {
		event.Details[attentionDetailSnoozedUntil] = itemState.SnoozedUntil.UTC().Format(time.RFC3339Nano)
	}
	if itemState.DismissedAt != nil {
		event.Details[attentionDetailDismissedAt] = itemState.DismissedAt.UTC().Format(time.RFC3339Nano)
	}
	if itemState.DismissedBy != "" {
		event.Details[attentionDetailDismissedBy] = itemState.DismissedBy
	}
	if itemState.Pinned {
		event.Details[attentionDetailPinned] = true
	}
	if itemState.Muted {
		event.Details[attentionDetailMuted] = true
	}
	overrideActive := attentionPriorityOverrideActive(itemState, now)
	if overrideActive {
		event.Details[attentionDetailOverridePriority] = itemState.OverridePriority
		event = applyAttentionPriorityOverride(event, itemState.OverridePriority)
		if event.Details[attentionDetailExplanationCode] == nil {
			event.Details[attentionDetailExplanationCode] = attentionExplainEscalated
		}
	}
	if itemState.OverrideReason != "" {
		event.Details[attentionDetailOverrideReason] = itemState.OverrideReason
	}
	if itemState.OverrideExpiresAt != nil {
		event.Details[attentionDetailOverrideExpiresAt] = itemState.OverrideExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if itemState.ResurfacingPolicy != "" {
		event.Details[attentionDetailResurfacingPolicy] = itemState.ResurfacingPolicy
	}
	if itemState.Pinned && attentionActionabilityRank(event.Actionability) < attentionActionabilityRank(ActionabilityInteresting) {
		event.Actionability = ActionabilityInteresting
	}

	effectiveState := storedState
	fingerprintChanged := itemState.Fingerprint != "" &&
		attentionStringDetail(event.Details, attentionDetailFingerprint) != "" &&
		itemState.Fingerprint != attentionStringDetail(event.Details, attentionDetailFingerprint) &&
		!strings.EqualFold(itemState.ResurfacingPolicy, "never")

	switch {
	case fingerprintChanged && storedState != string(state.AttentionStateNew):
		effectiveState = string(state.AttentionStateNew)
		event.Details[attentionDetailPreviousState] = storedState
		event.Details[attentionDetailPreviousFingerprint] = itemState.Fingerprint
		event.Details[attentionDetailResurfaced] = true
		event.Details[attentionDetailResurfaceReason] = attentionResurfaceReasonFingerprintChange
		event.Details[attentionDetailExplanationCode] = attentionExplainResurfaced
	case storedState == string(state.AttentionStateSnoozed) && itemState.SnoozedUntil != nil && !now.Before(itemState.SnoozedUntil.UTC()):
		effectiveState = string(state.AttentionStateNew)
		event.Details[attentionDetailPreviousState] = storedState
		event.Details[attentionDetailResurfaced] = true
		event.Details[attentionDetailResurfaceReason] = attentionResurfaceReasonSnoozeExpired
		event.Details[attentionDetailExplanationCode] = attentionExplainResurfaced
	case storedState == string(state.AttentionStateSnoozed) && event.Severity == SeverityCritical:
		effectiveState = string(state.AttentionStateNew)
		event.Details[attentionDetailPreviousState] = storedState
		event.Details[attentionDetailResurfaced] = true
		event.Details[attentionDetailResurfaceReason] = attentionResurfaceReasonSeverityEscalated
		event.Details[attentionDetailExplanationCode] = attentionExplainResurfaced
	}

	event.Details[attentionDetailState] = effectiveState

	if !itemState.Pinned && !overrideActive {
		hiddenReason := ""
		explanationCode := ""
		switch {
		case itemState.Muted:
			hiddenReason = attentionHiddenReasonMuted
			explanationCode = attentionExplainMuted
		case effectiveState == string(state.AttentionStateAcknowledged):
			hiddenReason = attentionHiddenReasonAcknowledged
			explanationCode = attentionExplainAck
		case effectiveState == string(state.AttentionStateSnoozed):
			hiddenReason = attentionHiddenReasonSnoozed
			explanationCode = attentionExplainSnoozed
		case effectiveState == string(state.AttentionStateDismissed):
			hiddenReason = attentionHiddenReasonDismissed
			explanationCode = attentionExplainDismissed
		}
		if hiddenReason != "" {
			event.Details[attentionDetailHiddenReason] = hiddenReason
			event.Details[attentionDetailExplanationCode] = explanationCode
		}
	} else if itemState.Pinned {
		event.Details[attentionDetailExplanationCode] = attentionExplainPinned
	}

	return event
}

func (f *AttentionFeed) drainPendingEvents() {
	for {
		f.appendMu.Lock()
		if len(f.pendingEvents) == 0 {
			f.drainingEvents = false
			f.appendMu.Unlock()
			return
		}
		event := f.pendingEvents[0]
		f.pendingEvents = f.pendingEvents[1:]
		f.appendMu.Unlock()

		// Notify subscribers outside appendMu so handlers can emit follow-on
		// attention events. Those appends requeue behind the current event and
		// are drained in cursor order after the current delivery completes.
		f.notifySubscribers(event)
	}
}

// Replay returns events since the given cursor.
// Use cursor=0 to get all available events.
// Use cursor=-1 to get no events and just the current cursor.
// When a store is configured, replays from SQLite for durable cursor semantics.
func (f *AttentionFeed) Replay(sinceCursor int64, limit int) ([]AttentionEvent, int64, error) {
	if f.store != nil {
		return f.replayFromStore(sinceCursor, limit)
	}
	return f.journal.Replay(sinceCursor, limit)
}

// ReplayForIncident returns replayable attention events around an incident's timeline.
func (f *AttentionFeed) ReplayForIncident(incidentID string, windowBefore, windowAfter time.Duration) ([]AttentionEvent, int64, error) {
	if f.store == nil {
		return nil, 0, fmt.Errorf("attention replay store unavailable")
	}

	incident, err := f.store.GetIncident(incidentID)
	if err != nil {
		return nil, 0, fmt.Errorf("load incident: %w", err)
	}
	if incident == nil {
		return nil, 0, fmt.Errorf("incident %q not found", incidentID)
	}

	if windowBefore < 0 {
		windowBefore = 0
	}
	if windowAfter < 0 {
		windowAfter = 0
	}

	// incidentEventPadding provides extra events beyond the incident count to capture
	// surrounding context (~30s at typical event rates).
	// minIncidentQueryLimit ensures enough events for meaningful analysis even for
	// small incidents.
	// maxIncidentQueryLimit caps memory usage for very large incidents.
	const (
		incidentEventPadding  = 64
		minIncidentQueryLimit = 128
		maxIncidentQueryLimit = 2048
	)
	limit := minInt(maxInt(incident.EventCount+incidentEventPadding, minIncidentQueryLimit), maxIncidentQueryLimit)
	linkedEvents, err := f.store.GetEventsForIncident(incidentID, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("load linked incident events: %w", err)
	}

	replayEnd := incident.LastEventAt.UTC()
	if len(linkedEvents) > 0 {
		lastLinked := linkedEvents[len(linkedEvents)-1].Ts.UTC()
		if !lastLinked.IsZero() && (replayEnd.IsZero() || lastLinked.Before(replayEnd)) {
			replayEnd = lastLinked
		}
	}
	start := incident.StartedAt.UTC().Add(-windowBefore)
	end := replayEnd.Add(windowAfter)
	if end.Before(start) {
		end = start
	}

	rangeEvents, err := f.store.GetAttentionEventsInTimeRange(start, end, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("load incident replay window: %w", err)
	}
	rangeEvents = filterStoredAttentionEventsByTimeRange(rangeEvents, start, end)

	merged := mergeStoredAttentionEvents(rangeEvents, linkedEvents)
	return f.hydrateStoredAttentionEvents(merged), maxStoredAttentionCursor(merged, f.cursor.Current()), nil
}

// ReconstructAsOf returns the most recent bounded replayable attention context at or before a timestamp.
func (f *AttentionFeed) ReconstructAsOf(timestamp time.Time, limit int) ([]AttentionEvent, *ReconstructionMeta, error) {
	if f.store == nil {
		return nil, nil, fmt.Errorf("attention replay store unavailable")
	}

	requestedAt := timestamp.UTC()
	if requestedAt.IsZero() {
		requestedAt = time.Now().UTC()
	}
	if limit <= 0 {
		limit = 100
	}

	startedAt := time.Now().UTC()
	lookback := f.config.RetentionPeriod
	if lookback <= 0 {
		lookback = time.Hour
	}
	// queryOverfetchFactor accounts for filtering that reduces the result set post-query.
	// absoluteMaxQueryLimit is the hard ceiling on any single time-range query.
	const (
		queryOverfetchFactor  = 4
		absoluteMaxQueryLimit = 4096
	)
	queryLimit := minInt(maxInt(limit*queryOverfetchFactor, limit), absoluteMaxQueryLimit)

	storedEvents, err := f.store.GetAttentionEventsInTimeRange(requestedAt.Add(-lookback), requestedAt, queryLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("reconstruct attention as-of: %w", err)
	}

	confidence := ReconstructionConfidenceHigh
	warnings := []string{"RECONSTRUCTED"}
	partial := false
	if len(storedEvents) > limit {
		storedEvents = storedEvents[len(storedEvents)-limit:]
		confidence = ReconstructionConfidenceMedium
		warnings = append(warnings, "PARTIAL_DATA")
		partial = true
	}
	if len(storedEvents) == 0 {
		confidence = ReconstructionConfidenceUnavailable
		warnings = append(warnings, "PARTIAL_DATA")
	}

	events := f.hydrateStoredAttentionEvents(storedEvents)
	completedAt := time.Now().UTC()
	meta := &ReconstructionMeta{
		RequestedAt:    requestedAt.Format(time.RFC3339Nano),
		Method:         "event_replay",
		Source:         "attention_store",
		EventsReplayed: len(events),
		GapsDetected:   0,
		Interpolations: 0,
		StartedAt:      startedAt.Format(time.RFC3339Nano),
		CompletedAt:    completedAt.Format(time.RFC3339Nano),
		DurationMs:     completedAt.Sub(startedAt).Milliseconds(),
		Confidence:     confidence,
		Warnings:       warnings,
		Partial:        partial,
	}
	if len(events) > 0 {
		meta.ActualStart = events[0].Ts
		meta.ActualEnd = events[len(events)-1].Ts
	}

	return events, meta, nil
}

// CurrentCursor returns the most recently allocated cursor.
func (f *AttentionFeed) CurrentCursor() int64 {
	return f.cursor.Current()
}

// Stats returns journal statistics for observability.
// When a store is configured, returns stats based on durable storage.
func (f *AttentionFeed) Stats() JournalStats {
	if f.store != nil {
		stats, err := f.storeBackedStats()
		if err == nil {
			return stats
		}
	}
	return f.journal.Stats()
}

// =============================================================================
// Digest Engine
// =============================================================================

// AttentionDigestOptions controls how the digest engine filters and coalesces
// events before they are surfaced to operator-facing commands.
type AttentionDigestOptions struct {
	Session             string
	Categories          []EventCategory
	Types               []EventType
	ExcludeTypes        []EventType
	MinSeverity         Severity
	MinActionability    Actionability
	ActionRequiredLimit int
	InterestingLimit    int
	BackgroundLimit     int
	IncludeTrace        bool
}

// AttentionDigest is the token-efficient delta view built from the raw feed.
// It preserves cursor boundaries, counts, and representative event details so
// higher-level robot commands can summarize "what changed?" without forcing a
// full snapshot or full event replay each time.
type AttentionDigest struct {
	CursorStart     int64                      `json:"cursor_start"`
	CursorEnd       int64                      `json:"cursor_end"`
	PeriodStart     string                     `json:"period_start,omitempty"`
	PeriodEnd       string                     `json:"period_end,omitempty"`
	EventCount      int                        `json:"event_count"`
	ByCategory      map[EventCategory]int      `json:"by_category"`
	ByActionability map[Actionability]int      `json:"by_actionability"`
	Buckets         AttentionDigestBuckets     `json:"buckets"`
	Suppressed      AttentionDigestSuppression `json:"suppressed"`
	Summary         string                     `json:"summary"`
	Trace           []AttentionDigestDecision  `json:"trace,omitempty"`

	// PrioritizedQueue is a flattened, pre-ordered list of digest items for
	// operator loops that want a single iteration target. Items appear in
	// actionability order (action_required first, then interesting, then
	// background) and are capped at 10 total.
	PrioritizedQueue []AttentionDigestItem `json:"prioritized_queue,omitempty"`
}

// AttentionDigestBuckets groups representative digest items by urgency so
// operators can see the most important changes first.
type AttentionDigestBuckets struct {
	ActionRequired []AttentionDigestItem `json:"action_required,omitempty"`
	Interesting    []AttentionDigestItem `json:"interesting,omitempty"`
	Background     []AttentionDigestItem `json:"background,omitempty"`
}

// AttentionDigestItem represents one surfaced digest entry. It preserves the
// representative event plus the cursor span and source-event count that produced
// the item so follow-up inspection can stay targeted.
type AttentionDigestItem struct {
	Event             AttentionEvent `json:"event"`
	CursorStart       int64          `json:"cursor_start"`
	CursorEnd         int64          `json:"cursor_end"`
	SourceEventCount  int            `json:"source_event_count"`
	SuppressedCount   int            `json:"suppressed_count,omitempty"`
	SuppressionReason string         `json:"suppression_reason,omitempty"`
}

// AttentionDigestSuppression summarizes how much raw feed noise was suppressed
// and why.
type AttentionDigestSuppression struct {
	Total    int            `json:"total"`
	ByReason map[string]int `json:"by_reason,omitempty"`
}

// AttentionDigestDecision captures the deterministic surface/coalesce/suppress
// decision made for a source event. Tests use this to print high-signal traces
// when digest expectations fail.
type AttentionDigestDecision struct {
	Cursor               int64         `json:"cursor"`
	Summary              string        `json:"summary"`
	Bucket               Actionability `json:"bucket,omitempty"`
	Decision             string        `json:"decision"`
	Reason               string        `json:"reason,omitempty"`
	RepresentativeCursor int64         `json:"representative_cursor,omitempty"`
}

const (
	attentionDigestSuppressionHeartbeat       = "heartbeat_noise"
	attentionDigestSuppressionPaneOutputBurst = "pane_output_burst"
	attentionDigestSuppressionLifecycleNoise  = "lifecycle_noise"
	attentionDigestSuppressionDuplicateAlert  = "duplicate_alert"
	attentionDigestSuppressionBucketLimit     = "bucket_limit"
)

type attentionDigestCandidate struct {
	item    AttentionDigestItem
	members []AttentionEvent
}

// DefaultAttentionDigestOptions returns conservative defaults that keep the
// surfaced digest compact while preserving the most important signals.
func DefaultAttentionDigestOptions() AttentionDigestOptions {
	return AttentionDigestOptions{
		MinSeverity:         SeverityInfo,
		MinActionability:    ActionabilityBackground,
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
	}
}

// Digest builds a token-efficient digest from all replayable events newer than
// sinceCursor. It reuses replay cursor semantics so callers can chain digest
// calls without inventing a second cursor model.
func (f *AttentionFeed) Digest(sinceCursor int64, opts AttentionDigestOptions) (*AttentionDigest, error) {
	limit := f.Stats().Count
	if limit < 1 {
		limit = 1
	}

	events, newest, err := f.Replay(sinceCursor, limit)
	if err != nil {
		return nil, err
	}

	return BuildAttentionDigest(events, sinceCursor, newest, opts), nil
}

// BuildAttentionDigest reduces a set of replayed events into a compact summary
// that preserves cursor boundaries and representative event details.
func BuildAttentionDigest(events []AttentionEvent, sinceCursor, cursorEnd int64, opts AttentionDigestOptions) *AttentionDigest {
	options := normalizeAttentionDigestOptions(opts)

	filtered := make([]AttentionEvent, 0, len(events))
	for _, event := range events {
		if matchesAttentionDigestFilters(event, options) {
			filtered = append(filtered, cloneAttentionEvent(event))
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Cursor != filtered[j].Cursor {
			return filtered[i].Cursor < filtered[j].Cursor
		}
		return filtered[i].Ts < filtered[j].Ts
	})

	digest := &AttentionDigest{
		CursorStart:     cursorEnd,
		CursorEnd:       cursorEnd,
		EventCount:      len(filtered),
		ByCategory:      map[EventCategory]int{},
		ByActionability: map[Actionability]int{},
		Buckets: AttentionDigestBuckets{
			ActionRequired: []AttentionDigestItem{},
			Interesting:    []AttentionDigestItem{},
			Background:     []AttentionDigestItem{},
		},
		Suppressed: AttentionDigestSuppression{
			ByReason: map[string]int{},
		},
	}
	if digest.CursorStart < 0 {
		digest.CursorStart = 0
	}
	if len(filtered) > 0 {
		digest.CursorStart = filtered[0].Cursor
		digest.PeriodStart = filtered[0].Ts
		digest.PeriodEnd = filtered[len(filtered)-1].Ts
	} else if sinceCursor >= 0 {
		digest.CursorStart = sinceCursor
	}

	candidates := buildAttentionDigestCandidates(filtered, options, digest)
	actionRequired, interesting, background := partitionAttentionDigestCandidates(candidates)

	digest.Buckets.ActionRequired = surfaceAttentionDigestBucket(actionRequired, ActionabilityActionRequired, options.ActionRequiredLimit, options, digest)
	digest.Buckets.Interesting = surfaceAttentionDigestBucket(interesting, ActionabilityInteresting, options.InterestingLimit, options, digest)
	digest.Buckets.Background = surfaceAttentionDigestBucket(background, ActionabilityBackground, options.BackgroundLimit, options, digest)

	// Build flattened prioritized queue for operator loops (capped at 10)
	digest.PrioritizedQueue = buildAttentionDigestPrioritizedQueue(digest.Buckets, 10)

	digest.Summary = buildAttentionDigestSummary(digest)

	if len(digest.Suppressed.ByReason) == 0 {
		digest.Suppressed.ByReason = nil
	}
	if !options.IncludeTrace {
		digest.Trace = nil
	}

	return digest
}

func normalizeAttentionDigestOptions(opts AttentionDigestOptions) AttentionDigestOptions {
	if opts.MinSeverity == "" {
		opts.MinSeverity = SeverityInfo
	}
	if opts.MinActionability == "" {
		opts.MinActionability = ActionabilityBackground
	}
	if opts.ActionRequiredLimit <= 0 {
		opts.ActionRequiredLimit = 5
	}
	if opts.InterestingLimit <= 0 {
		opts.InterestingLimit = 4
	}
	if opts.BackgroundLimit <= 0 {
		opts.BackgroundLimit = 3
	}
	return opts
}

func matchesAttentionDigestFilters(event AttentionEvent, opts AttentionDigestOptions) bool {
	if opts.Session != "" && event.Session != opts.Session {
		return false
	}
	if len(opts.Categories) > 0 {
		matched := false
		for _, category := range opts.Categories {
			if event.Category == category {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(opts.Types) > 0 {
		matched := false
		for _, eventType := range opts.Types {
			if event.Type == eventType {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(opts.ExcludeTypes) > 0 {
		for _, eventType := range opts.ExcludeTypes {
			if event.Type == eventType {
				return false
			}
		}
	}
	if !attentionEventHasVisibilityOverride(event) {
		if attentionSeverityRank(event.Severity) < attentionSeverityRank(opts.MinSeverity) {
			return false
		}
		if attentionActionabilityRank(event.Actionability) < attentionActionabilityRank(opts.MinActionability) {
			return false
		}
	}
	return true
}

func buildAttentionDigestCandidates(events []AttentionEvent, opts AttentionDigestOptions, digest *AttentionDigest) []*attentionDigestCandidate {
	candidates := make([]*attentionDigestCandidate, 0, len(events))
	grouped := make(map[string]int)

	for _, event := range events {
		digest.ByCategory[event.Category]++
		digest.ByActionability[event.Actionability]++

		if reason, drop := attentionDigestDropReason(event); drop {
			recordAttentionDigestSuppression(digest, reason)
			recordAttentionDigestDecision(digest, opts, event, event.Actionability, "suppressed", reason, 0)
			continue
		}

		if key, reason := attentionDigestGroupKey(event); key != "" {
			if idx, ok := grouped[key]; ok {
				coalesceAttentionDigestCandidate(candidates[idx], event, reason, digest)
				continue
			}
			grouped[key] = len(candidates)
			candidates = append(candidates, newAttentionDigestCandidate(event, opts))
			continue
		}

		candidates = append(candidates, newAttentionDigestCandidate(event, opts))
	}

	return candidates
}

func partitionAttentionDigestCandidates(candidates []*attentionDigestCandidate) (actionRequired, interesting, background []*attentionDigestCandidate) {
	for _, candidate := range candidates {
		switch candidate.item.Event.Actionability {
		case ActionabilityActionRequired:
			actionRequired = append(actionRequired, candidate)
		case ActionabilityInteresting:
			interesting = append(interesting, candidate)
		default:
			background = append(background, candidate)
		}
	}
	return actionRequired, interesting, background
}

func surfaceAttentionDigestBucket(candidates []*attentionDigestCandidate, bucket Actionability, limit int, opts AttentionDigestOptions, digest *AttentionDigest) []AttentionDigestItem {
	if len(candidates) == 0 {
		return []AttentionDigestItem{}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i].item.Event
		right := candidates[j].item.Event
		if attentionBoolDetail(left.Details, attentionDetailPinned) != attentionBoolDetail(right.Details, attentionDetailPinned) {
			return attentionBoolDetail(left.Details, attentionDetailPinned)
		}
		if attentionSeverityRank(left.Severity) != attentionSeverityRank(right.Severity) {
			return attentionSeverityRank(left.Severity) > attentionSeverityRank(right.Severity)
		}
		if attentionDigestRecurrencePriority(left) != attentionDigestRecurrencePriority(right) {
			return attentionDigestRecurrencePriority(left) > attentionDigestRecurrencePriority(right)
		}
		if candidates[i].item.CursorEnd != candidates[j].item.CursorEnd {
			return candidates[i].item.CursorEnd > candidates[j].item.CursorEnd
		}
		if left.Category != right.Category {
			return left.Category < right.Category
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		return left.Summary < right.Summary
	})

	surfaced := make([]AttentionDigestItem, 0, minInt(limit, len(candidates)))
	for idx, candidate := range candidates {
		if idx < limit {
			surfaced = append(surfaced, candidate.item)
			recordAttentionDigestCandidateTrace(digest, opts, candidate, bucket, false)
			continue
		}

		recordAttentionDigestSuppression(digest, attentionDigestSuppressionBucketLimit)
		recordAttentionDigestCandidateTrace(digest, opts, candidate, bucket, true)
	}

	return surfaced
}

func newAttentionDigestCandidate(event AttentionEvent, opts AttentionDigestOptions) *attentionDigestCandidate {
	item := AttentionDigestItem{
		Event:            cloneAttentionEvent(event),
		CursorStart:      event.Cursor,
		CursorEnd:        event.Cursor,
		SourceEventCount: 1,
	}
	candidate := &attentionDigestCandidate{item: item}
	if opts.IncludeTrace {
		candidate.members = []AttentionEvent{cloneAttentionEvent(event)}
	}
	return candidate
}

func coalesceAttentionDigestCandidate(candidate *attentionDigestCandidate, event AttentionEvent, reason string, digest *AttentionDigest) {
	candidate.item.SourceEventCount++
	candidate.item.SuppressedCount++
	candidate.item.SuppressionReason = reason
	if candidate.item.CursorStart == 0 || event.Cursor < candidate.item.CursorStart {
		candidate.item.CursorStart = event.Cursor
	}
	if event.Cursor > candidate.item.CursorEnd {
		candidate.item.CursorEnd = event.Cursor
	}
	if shouldReplaceAttentionDigestRepresentative(candidate.item.Event, event) {
		candidate.item.Event = cloneAttentionEvent(event)
	}
	if candidate.members != nil {
		candidate.members = append(candidate.members, cloneAttentionEvent(event))
	}

	annotateAttentionDigestRepresentative(&candidate.item)
	recordAttentionDigestSuppression(digest, reason)
}

func shouldReplaceAttentionDigestRepresentative(current, next AttentionEvent) bool {
	if attentionSeverityRank(next.Severity) != attentionSeverityRank(current.Severity) {
		return attentionSeverityRank(next.Severity) > attentionSeverityRank(current.Severity)
	}
	if attentionActionabilityRank(next.Actionability) != attentionActionabilityRank(current.Actionability) {
		return attentionActionabilityRank(next.Actionability) > attentionActionabilityRank(current.Actionability)
	}
	return next.Cursor >= current.Cursor
}

func annotateAttentionDigestRepresentative(item *AttentionDigestItem) {
	if item == nil {
		return
	}
	if item.Event.Details == nil {
		item.Event.Details = map[string]any{}
	}
	item.Event.Details["digest_cursor_start"] = item.CursorStart
	item.Event.Details["digest_cursor_end"] = item.CursorEnd
	item.Event.Details["digest_source_event_count"] = item.SourceEventCount
	if item.SuppressedCount > 0 {
		item.Event.Details["digest_suppressed_count"] = item.SuppressedCount
		item.Event.Details["digest_suppression_reason"] = item.SuppressionReason
	}
	switch item.SuppressionReason {
	case attentionDigestSuppressionPaneOutputBurst:
		item.Event.Summary = attentionDigestOutputSummary(item.Event, item.SourceEventCount)
	case attentionDigestSuppressionLifecycleNoise, attentionDigestSuppressionDuplicateAlert:
		item.Event.Summary = attentionDigestRepeatedSummary(item.Event.Summary, item.SourceEventCount)
	}
}

func attentionDigestOutputSummary(event AttentionEvent, count int) string {
	if count <= 1 {
		return event.Summary
	}
	paneRef := attentionEventPaneRef(event)
	switch {
	case event.Session != "" && paneRef != "":
		return fmt.Sprintf("%d output updates in %s pane %s", count, event.Session, paneRef)
	case event.Session != "":
		return fmt.Sprintf("%d output updates in %s", count, event.Session)
	default:
		return fmt.Sprintf("%d output updates", count)
	}
}

func attentionDigestRepeatedSummary(summary string, count int) string {
	if summary == "" || count <= 1 {
		return summary
	}
	return fmt.Sprintf("%s (%dx)", summary, count)
}

func attentionDigestDropReason(event AttentionEvent) (string, bool) {
	switch event.Type {
	case EventType(DefaultTransportLiveness.HeartbeatType):
		return attentionDigestSuppressionHeartbeat, true
	case EventTypePaneResized, EventTypeSessionAttached, EventTypeSessionDetached:
		return attentionDigestSuppressionLifecycleNoise, true
	default:
		if hiddenReason := attentionEventHiddenReason(event); hiddenReason != "" {
			return "attention_state_" + hiddenReason, true
		}
		return "", false
	}
}

func attentionDigestGroupKey(event AttentionEvent) (string, string) {
	if event.Type == EventTypePaneOutput {
		return fmt.Sprintf("output:%s:%s", event.Session, attentionEventPaneRef(event)), attentionDigestSuppressionPaneOutputBurst
	}
	if isAttentionDigestDuplicateAlertCandidate(event) {
		return fmt.Sprintf("alert:%s:%s:%d:%s", event.Type, event.Session, event.Pane, strings.ToLower(strings.TrimSpace(event.Summary))), attentionDigestSuppressionDuplicateAlert
	}
	if isAttentionDigestLifecycleCandidate(event) {
		return fmt.Sprintf("lifecycle:%s:%s:%d:%s", event.Type, event.Session, event.Pane, attentionStringDetail(event.Details, "signal")), attentionDigestSuppressionLifecycleNoise
	}
	return "", ""
}

func isAttentionDigestDuplicateAlertCandidate(event AttentionEvent) bool {
	if event.Category != EventCategoryAlert {
		return false
	}
	return strings.TrimSpace(event.Summary) != ""
}

func isAttentionDigestLifecycleCandidate(event AttentionEvent) bool {
	if event.Actionability == ActionabilityActionRequired {
		return false
	}
	switch event.Type {
	case EventTypeSessionCreated,
		EventTypeSessionDestroyed,
		EventTypePaneCreated,
		EventTypePaneDestroyed,
		EventTypeAgentStarted,
		EventTypeAgentStopped,
		EventTypeAgentStateChange,
		EventTypeAgentRecovered,
		EventTypeAgentCompacted:
		return true
	default:
		return false
	}
}

func recordAttentionDigestSuppression(digest *AttentionDigest, reason string) {
	if digest == nil || reason == "" {
		return
	}
	digest.Suppressed.Total++
	if digest.Suppressed.ByReason == nil {
		digest.Suppressed.ByReason = map[string]int{}
	}
	digest.Suppressed.ByReason[reason]++
}

func recordAttentionDigestDecision(digest *AttentionDigest, opts AttentionDigestOptions, event AttentionEvent, bucket Actionability, decision, reason string, representativeCursor int64) {
	if digest == nil || !opts.IncludeTrace {
		return
	}
	digest.Trace = append(digest.Trace, AttentionDigestDecision{
		Cursor:               event.Cursor,
		Summary:              event.Summary,
		Bucket:               bucket,
		Decision:             decision,
		Reason:               reason,
		RepresentativeCursor: representativeCursor,
	})
}

func recordAttentionDigestCandidateTrace(digest *AttentionDigest, opts AttentionDigestOptions, candidate *attentionDigestCandidate, bucket Actionability, bucketSuppressed bool) {
	if digest == nil || candidate == nil || !opts.IncludeTrace {
		return
	}

	representative := candidate.item.Event
	representativeCursor := representative.Cursor
	surfaceReason := attentionDigestSurfaceReason(representative)
	if len(candidate.members) == 0 {
		decision := "surfaced"
		reason := surfaceReason
		if bucketSuppressed {
			decision = "suppressed"
			reason = attentionDigestSuppressionBucketLimit
		} else if candidate.item.SuppressedCount > 0 {
			decision = "coalesced"
			reason = candidate.item.SuppressionReason
		}
		recordAttentionDigestDecision(digest, opts, representative, bucket, decision, reason, 0)
		return
	}

	for _, member := range candidate.members {
		switch {
		case member.Cursor == representativeCursor && bucketSuppressed:
			recordAttentionDigestDecision(digest, opts, member, bucket, "suppressed", attentionDigestSuppressionBucketLimit, 0)
		case member.Cursor == representativeCursor && candidate.item.SuppressedCount > 0:
			recordAttentionDigestDecision(digest, opts, member, bucket, "coalesced", candidate.item.SuppressionReason, representativeCursor)
		case member.Cursor == representativeCursor:
			recordAttentionDigestDecision(digest, opts, member, bucket, "surfaced", surfaceReason, 0)
		default:
			recordAttentionDigestDecision(digest, opts, member, bucket, "suppressed", candidate.item.SuppressionReason, representativeCursor)
		}
	}
}

func attentionDigestRecurrencePriority(event AttentionEvent) float64 {
	score := attentionFloatDetail(event.Details, attentionDetailCooldownSuppressedCount)
	if score <= 0 {
		return 0
	}
	if attentionDigestSurfaceReason(event) != "" {
		score += 1
	}
	return score
}

func attentionDigestSurfaceReason(event AttentionEvent) string {
	return attentionStringDetail(event.Details, attentionDetailResurfaceReason)
}

// buildAttentionDigestPrioritizedQueue flattens the three actionability buckets
// into a single ordered list for operator loops. Items maintain their bucket
// order (action_required first, then interesting, then background) and are
// capped at the specified limit.
func buildAttentionDigestPrioritizedQueue(buckets AttentionDigestBuckets, limit int) []AttentionDigestItem {
	if limit <= 0 {
		limit = 10
	}

	total := len(buckets.ActionRequired) + len(buckets.Interesting) + len(buckets.Background)
	if total == 0 {
		return nil
	}

	queue := make([]AttentionDigestItem, 0, minInt(total, limit))
	for _, items := range [][]AttentionDigestItem{
		buckets.ActionRequired,
		buckets.Interesting,
		buckets.Background,
	} {
		if len(queue) >= limit {
			break
		}
		remaining := limit - len(queue)
		if remaining > len(items) {
			remaining = len(items)
		}
		queue = append(queue, items[:remaining]...)
	}

	return queue
}

func buildAttentionDigestSummary(digest *AttentionDigest) string {
	if digest == nil {
		return "no matching changes"
	}

	actionRequired := len(digest.Buckets.ActionRequired)
	interesting := len(digest.Buckets.Interesting)
	background := len(digest.Buckets.Background)
	countSummary := fmt.Sprintf("%d action_required, %d interesting, %d background", actionRequired, interesting, background)

	switch {
	case digest.EventCount == 0:
		return "no matching changes"
	case actionRequired == 0 && interesting == 0 && background == 0:
		return fmt.Sprintf("no surfaced items; %d suppressed from %d events", digest.Suppressed.Total, digest.EventCount)
	}

	lead := attentionDigestLeadSummary(digest)
	if digest.Suppressed.Total > 0 {
		countSummary = fmt.Sprintf("%s; %d suppressed from %d events", countSummary, digest.Suppressed.Total, digest.EventCount)
	} else {
		countSummary = fmt.Sprintf("%s from %d events", countSummary, digest.EventCount)
	}
	if lead == "" {
		return countSummary
	}
	return fmt.Sprintf("%s; %s", lead, countSummary)
}

func attentionDigestLeadSummary(digest *AttentionDigest) string {
	if digest == nil {
		return ""
	}
	for _, items := range [][]AttentionDigestItem{
		digest.Buckets.ActionRequired,
		digest.Buckets.Interesting,
		digest.Buckets.Background,
	} {
		if len(items) == 0 {
			continue
		}
		return items[0].Event.Summary
	}
	return ""
}

// PublishTrackerChange normalizes a state tracker change and appends it to the feed.
func (f *AttentionFeed) PublishTrackerChange(change tracker.StateChange) AttentionEvent {
	return f.Append(NewTrackerEvent(change))
}

// PublishTrackerChanges normalizes and appends tracker changes in order.
func (f *AttentionFeed) PublishTrackerChanges(changes []tracker.StateChange) []AttentionEvent {
	if len(changes) == 0 {
		return nil
	}
	published := make([]AttentionEvent, 0, len(changes))
	for _, change := range changes {
		published = append(published, f.PublishTrackerChange(change))
	}
	return published
}

// PublishLoggedEvent normalizes a logged analytics event and appends it to the feed.
// Suppressed logged events return ok=false and are not appended.
func (f *AttentionFeed) PublishLoggedEvent(event ntmevents.Event) (published AttentionEvent, ok bool) {
	normalized, ok := NewLoggedAttentionEvent(event)
	if !ok {
		return AttentionEvent{}, false
	}
	return f.Append(normalized), true
}

// PublishLoggedEvents normalizes and appends logged events in order, skipping suppressed entries.
func (f *AttentionFeed) PublishLoggedEvents(events []ntmevents.Event) []AttentionEvent {
	if len(events) == 0 {
		return nil
	}
	published := make([]AttentionEvent, 0, len(events))
	for _, event := range events {
		if normalized, ok := f.PublishLoggedEvent(event); ok {
			published = append(published, normalized)
		}
	}
	return published
}

// PublishBusEvent normalizes an event-bus event and appends it to the feed.
// Unsupported bus events return ok=false and are not appended.
func (f *AttentionFeed) PublishBusEvent(event ntmevents.BusEvent) (published AttentionEvent, ok bool) {
	normalized, ok := NewBusAttentionEvent(event)
	if !ok {
		return AttentionEvent{}, false
	}
	return f.Append(normalized), true
}

// PublishBusEvents normalizes and appends event-bus events in order, skipping unsupported entries.
func (f *AttentionFeed) PublishBusEvents(events []ntmevents.BusEvent) []AttentionEvent {
	if len(events) == 0 {
		return nil
	}
	published := make([]AttentionEvent, 0, len(events))
	for _, event := range events {
		if normalized, ok := f.PublishBusEvent(event); ok {
			published = append(published, normalized)
		}
	}
	return published
}

// PublishBusHistory replays event-bus history into the feed oldest-first so cursors
// reflect the original event chronology.
func (f *AttentionFeed) PublishBusHistory(bus *ntmevents.EventBus, limit int) []AttentionEvent {
	if bus == nil {
		bus = ntmevents.DefaultBus
	}
	history := bus.History(limit)
	if len(history) == 0 {
		return nil
	}
	published := make([]AttentionEvent, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		if normalized, ok := f.PublishBusEvent(history[i]); ok {
			published = append(published, normalized)
		}
	}
	return published
}

// SubscribeEventBus forwards live event-bus events into the feed using the shared
// normalization logic.
func (f *AttentionFeed) SubscribeEventBus(bus *ntmevents.EventBus) func() {
	if bus == nil {
		bus = ntmevents.DefaultBus
	}
	return bus.SubscribeAll(func(event ntmevents.BusEvent) {
		f.PublishBusEvent(event)
	})
}

// AppendDeduplicated appends a pre-normalized attention event with a suppression window.
// Higher orchestration layers own source-specific translation and can use this to
// publish durable events without duplicating feed-local dedup behavior.
func (f *AttentionFeed) AppendDeduplicated(event AttentionEvent, window time.Duration) (AttentionEvent, bool) {
	return f.appendDeduplicated(event, window)
}

const ptAttentionSource = "pt.monitor"

// PublishPTStateChange normalizes a PT classification transition and appends it
// to the durable attention feed with light suppression for duplicate bridge emissions.
func (f *AttentionFeed) PublishPTStateChange(change pt.ClassificationStateChange) (AttentionEvent, bool) {
	event, ok := NewPTStateChangeAttentionEvent(change)
	if !ok {
		return AttentionEvent{}, false
	}
	return f.AppendDeduplicated(event, 30*time.Second)
}

// PublishPTAlert normalizes a PT alert and appends it to the durable attention
// feed with a wider suppression window to avoid repeated threshold floods.
func (f *AttentionFeed) PublishPTAlert(alert pt.Alert) (AttentionEvent, bool) {
	event, ok := NewPTAlertAttentionEvent(alert)
	if !ok {
		return AttentionEvent{}, false
	}
	return f.AppendDeduplicated(event, ptAlertDedupWindow(alert.Type))
}

// NewPTStateChangeAttentionEvent converts a PT classification transition into a
// normalized attention event. Benign steady-state classifications are suppressed.
func NewPTStateChangeAttentionEvent(change pt.ClassificationStateChange) (AttentionEvent, bool) {
	session := ptAttentionSession(change.Session, change.Pane)
	paneRef := strings.TrimSpace(change.Pane)
	ts := attentionTimestamp(change.Event.Timestamp)

	details := map[string]any{
		"monitor":                 "pt",
		"pid":                     change.PID,
		"pane_ref":                paneRef,
		"previous_classification": strings.TrimSpace(string(change.Previous)),
		"current_classification":  strings.TrimSpace(string(change.Current)),
		"confidence":              change.Event.Confidence,
		"initial":                 change.Initial,
		"consecutive_count":       change.ConsecutiveCount,
	}
	if !change.Since.IsZero() {
		details["since"] = change.Since.UTC().Format(time.RFC3339Nano)
	}
	if reason := strings.TrimSpace(change.Event.Reason); reason != "" {
		details["reason"] = reason
	}
	if change.Event.NetworkActive {
		details["network_active"] = true
	}

	build := func(category EventCategory, eventType EventType, actionability Actionability, severity Severity, reasonCode, summary string, nextActions []NextAction) (AttentionEvent, bool) {
		return annotateAttentionSignal(AttentionEvent{
			Ts:            ts.Format(time.RFC3339Nano),
			Session:       session,
			Pane:          attentionPaneIndex(paneRef),
			Category:      category,
			Type:          eventType,
			Source:        ptAttentionSource,
			Actionability: actionability,
			Severity:      severity,
			ReasonCode:    reasonCode,
			Summary:       attentionSummary(session, paneRef, summary),
			Details:       details,
			NextActions:   nextActions,
			DedupKey:      ptStateChangeDedupKey(session, paneRef, change.Previous, change.Current),
		}), true
	}

	switch change.Current {
	case pt.ClassStuck:
		return build(
			EventCategoryAgent,
			EventTypeAgentStalled,
			ActionabilityInteresting,
			SeverityWarning,
			"pt:state:stuck",
			"agent classified as stuck",
			attentionTailOrStatusActions(session, paneRef, "Inspect the stalled agent output"),
		)
	case pt.ClassZombie:
		return build(
			EventCategoryAgent,
			EventTypeAgentError,
			ActionabilityActionRequired,
			SeverityError,
			"pt:state:zombie",
			"agent classified as zombie",
			attentionTailOrStatusActions(session, paneRef, "Inspect the zombie agent output"),
		)
	default:
		if ptStateIsDegraded(change.Previous) && !ptStateIsDegraded(change.Current) {
			return build(
				EventCategoryAgent,
				EventTypeAgentRecovered,
				ActionabilityInteresting,
				SeverityInfo,
				"pt:state:recovered",
				fmt.Sprintf("agent recovered from %s to %s", change.Previous, change.Current),
				[]NextAction{attentionStatusNextAction("Inspect current PT health state")},
			)
		}
		return AttentionEvent{}, false
	}
}

// NewPTAlertAttentionEvent converts a PT alert into a normalized attention
// event. Alert thresholds remain actionable even when the PT alert channel is full.
func NewPTAlertAttentionEvent(alert pt.Alert) (AttentionEvent, bool) {
	session := ptAttentionSession(alert.Session, alert.Pane)
	paneRef := strings.TrimSpace(alert.Pane)
	ts := attentionTimestamp(alert.Timestamp)
	details := map[string]any{
		"monitor":          "pt",
		"alert_type":       strings.TrimSpace(string(alert.Type)),
		"state":            strings.TrimSpace(string(alert.State)),
		"pid":              alert.PID,
		"pane_ref":         paneRef,
		"duration":         alert.Duration.String(),
		"duration_seconds": int(alert.Duration.Round(time.Second) / time.Second),
	}
	if message := strings.TrimSpace(alert.Message); message != "" {
		details["message"] = message
	}

	build := func(category EventCategory, eventType EventType, actionability Actionability, severity Severity, reasonCode, summary string, nextActions []NextAction) (AttentionEvent, bool) {
		return annotateAttentionSignal(AttentionEvent{
			Ts:            ts.Format(time.RFC3339Nano),
			Session:       session,
			Pane:          attentionPaneIndex(paneRef),
			Category:      category,
			Type:          eventType,
			Source:        ptAttentionSource,
			Actionability: actionability,
			Severity:      severity,
			ReasonCode:    reasonCode,
			Summary:       attentionSummary(session, paneRef, summary),
			Details:       details,
			NextActions:   nextActions,
			DedupKey:      ptAlertDedupKey(session, paneRef, alert.Type),
		}), true
	}

	switch alert.Type {
	case pt.AlertStuck:
		return build(
			EventCategoryAlert,
			EventTypeAlertWarning,
			ActionabilityActionRequired,
			SeverityWarning,
			"pt:alert:stuck",
			fmt.Sprintf("agent stuck for %s", alert.Duration.Round(time.Second)),
			attentionTailOrStatusActions(session, paneRef, "Inspect the stalled agent output"),
		)
	case pt.AlertZombie:
		return build(
			EventCategoryAlert,
			EventTypeAlertAttentionRequired,
			ActionabilityActionRequired,
			SeverityError,
			"pt:alert:zombie",
			"agent is a zombie process",
			attentionTailOrStatusActions(session, paneRef, "Inspect the zombie agent output"),
		)
	case pt.AlertIdle:
		return build(
			EventCategoryAgent,
			EventTypeAgentIdle,
			ActionabilityInteresting,
			SeverityWarning,
			"pt:alert:idle",
			fmt.Sprintf("agent idle for %s", alert.Duration.Round(time.Second)),
			[]NextAction{attentionStatusNextAction("Inspect current PT health state")},
		)
	default:
		return AttentionEvent{}, false
	}
}

func ptStateIsDegraded(classification pt.Classification) bool {
	switch classification {
	case pt.ClassStuck, pt.ClassZombie:
		return true
	default:
		return false
	}
}

func ptAlertDedupWindow(alertType pt.AlertType) time.Duration {
	switch alertType {
	case pt.AlertIdle:
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}

func ptStateChangeDedupKey(session, pane string, previous, current pt.Classification) string {
	return fmt.Sprintf("pt|state|%s|%s|%s|%s", strings.TrimSpace(session), strings.TrimSpace(pane), strings.TrimSpace(string(previous)), strings.TrimSpace(string(current)))
}

func ptAlertDedupKey(session, pane string, alertType pt.AlertType) string {
	return fmt.Sprintf("pt|alert|%s|%s|%s", strings.TrimSpace(session), strings.TrimSpace(pane), strings.TrimSpace(string(alertType)))
}

func ptAttentionSession(session, pane string) string {
	if trimmed := strings.TrimSpace(session); trimmed != "" {
		return trimmed
	}
	pane = strings.TrimSpace(pane)
	if pane == "" {
		return ""
	}
	if candidate := strings.TrimSpace(tmux.PaneTitleSession(pane)); candidate != "" {
		return candidate
	}
	return ""
}

// PublishMailPending creates and appends a mail_pending signal for unread mail.
func (f *AttentionFeed) PublishMailPending(projectKey, from, to, subject string, messageID int, threadID string) AttentionEvent {
	event := AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Category:      EventCategoryMail,
		Type:          EventTypeMailReceived,
		Source:        "agent_mail",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       fmt.Sprintf("New mail from %s: %s", from, subject),
		Details: map[string]any{
			"project_key": projectKey,
			"from":        from,
			"to":          to,
			"subject":     subject,
			"message_id":  messageID,
			"thread_id":   threadID,
		},
		NextActions: attentionMailCheckActions(projectKey, to, threadID, true, "Inspect the unread message"),
	}
	return f.Append(annotateAttentionSignal(event))
}

// PublishMailAckRequired creates and appends a mail_ack_required signal for messages needing acknowledgment.
func (f *AttentionFeed) PublishMailAckRequired(projectKey, from, to, subject string, messageID int, threadID string) AttentionEvent {
	event := AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Category:      EventCategoryMail,
		Type:          EventTypeMailAckRequired,
		Source:        "agent_mail",
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       fmt.Sprintf("Ack required from %s: %s", from, subject),
		Details: map[string]any{
			"project_key":  projectKey,
			"from":         from,
			"to":           to,
			"subject":      subject,
			"message_id":   messageID,
			"thread_id":    threadID,
			"ack_required": true,
		},
		NextActions: attentionMailCheckActions(projectKey, to, threadID, false, "Inspect the message before acknowledging it"),
	}
	return f.Append(annotateAttentionSignal(event))
}

// PublishActuation appends a first-class actuation event to the durable feed.
func (f *AttentionFeed) PublishActuation(record ActuationRecord) AttentionEvent {
	action := strings.TrimSpace(record.Action)
	if action == "" {
		action = "actuation"
	}
	if record.Source == "" {
		record.Source = "robot.actuation"
	}
	if record.Actionability == "" {
		record.Actionability = ActionabilityInteresting
	}
	if record.Severity == "" {
		record.Severity = SeverityInfo
	}

	eventType := EventTypeActuationOutcome
	switch record.Stage {
	case ActuationStageRequest:
		eventType = EventTypeActuationRequested
	case ActuationStageVerification:
		eventType = EventTypeActuationVerified
	}

	targets := append([]string(nil), record.Targets...)
	targetRef := actuationTargetRef(record.Session, targets)
	paneRef := actuationPaneRef(targets)

	details := map[string]any{
		"action":       action,
		"stage":        string(record.Stage),
		"target_count": len(targets),
		"target_ref":   targetRef,
	}
	if len(targets) > 0 {
		details["targets"] = targets
		details["target_refs"] = actuationTargetRefs(record.Session, targets)
	}
	if paneRef != "" {
		details["pane_ref"] = paneRef
	}
	if record.Method != "" {
		details["method"] = record.Method
	}
	if record.RequestID != "" {
		details["request_id"] = record.RequestID
	}
	if record.CorrelationID != "" {
		details["correlation_id"] = record.CorrelationID
	}
	if record.IdempotencyKey != "" {
		details["idempotency_key"] = record.IdempotencyKey
	}
	if record.MessagePreview != "" {
		details["message_preview"] = record.MessagePreview
	}
	if record.Result != "" {
		details["result"] = record.Result
	}
	if record.Verification != "" {
		details["verification_state"] = record.Verification
	}
	if len(record.Successful) > 0 {
		details["successful"] = append([]string(nil), record.Successful...)
		details["successful_count"] = len(record.Successful)
	}
	if len(record.Failed) > 0 {
		details["failed"] = cloneAnyMapSlice(record.Failed)
		details["failed_count"] = len(record.Failed)
	}
	if len(record.Confirmations) > 0 {
		details["confirmations"] = cloneAnyMapSlice(record.Confirmations)
		details["confirmation_count"] = len(record.Confirmations)
	}
	if len(record.Pending) > 0 {
		details["pending"] = append([]string(nil), record.Pending...)
		details["pending_count"] = len(record.Pending)
	}
	if record.ErrorCode != "" {
		details["error_code"] = record.ErrorCode
	}
	if record.Error != "" {
		details["error"] = record.Error
	}
	if record.Blocked {
		details["blocked"] = true
	}
	if record.TimedOut {
		details["timed_out"] = true
	}

	reason := "Inspect the actuation state"
	switch record.Stage {
	case ActuationStageOutcome:
		reason = "Inspect the actuation outcome and resulting agent output"
	case ActuationStageVerification:
		reason = "Inspect the verification state before proceeding"
	}

	summary := strings.TrimSpace(record.Summary)
	if summary == "" {
		switch record.Stage {
		case ActuationStageRequest:
			summary = fmt.Sprintf("%s requested", action)
		case ActuationStageVerification:
			state := robotFirstNonEmpty(record.Verification, "pending")
			summary = fmt.Sprintf("%s verification %s", action, state)
		default:
			state := robotFirstNonEmpty(record.Result, "recorded")
			summary = fmt.Sprintf("%s outcome %s", action, state)
		}
	}

	reasonCode := strings.TrimSpace(record.ReasonCode)
	if reasonCode == "" {
		switch record.Stage {
		case ActuationStageRequest:
			reasonCode = "actuation_requested"
		case ActuationStageVerification:
			reasonCode = "actuation_verification"
		default:
			reasonCode = "actuation_outcome"
		}
	}

	return f.Append(AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Session:       record.Session,
		Pane:          attentionPaneIndex(paneRef),
		Category:      EventCategoryActuation,
		Type:          eventType,
		Source:        record.Source,
		Actionability: record.Actionability,
		Severity:      record.Severity,
		ReasonCode:    reasonCode,
		Summary:       attentionSummary(record.Session, paneRef, summary),
		Details:       details,
		NextActions:   actuationNextActions(record.Session, targets, reason),
	})
}

// Subscribe registers a handler to receive events.
// Returns an unsubscribe function.
func (f *AttentionFeed) Subscribe(handler AttentionHandler) func() {
	f.subMu.Lock()
	defer f.subMu.Unlock()

	id := f.subNextID.Add(1)
	f.subs[id] = subscription{id: id, handler: handler}

	return func() {
		f.subMu.Lock()
		defer f.subMu.Unlock()
		delete(f.subs, id)
	}
}

// Stop shuts down the feed gracefully.
func (f *AttentionFeed) Stop() {
	f.stopOnce.Do(func() {
		close(f.stopCh)
	})
	f.stopWg.Wait()
}

// notifySubscribers sends an event to all registered handlers.
func (f *AttentionFeed) notifySubscribers(event AttentionEvent) {
	f.subMu.RLock()
	handlers := make([]AttentionHandler, 0, len(f.subs))
	for _, sub := range f.subs {
		handlers = append(handlers, sub.handler)
	}
	f.subMu.RUnlock()

	for _, h := range handlers {
		// Run handlers synchronously to preserve ordering guarantees.
		// Handlers should be fast; slow handlers will block the feed.
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("attention feed: handler panic recovered: %v", r)
				}
			}()
			h(cloneAttentionEvent(event))
		}()
	}
}

func (f *AttentionFeed) subscriberCount() int {
	f.subMu.RLock()
	defer f.subMu.RUnlock()
	return len(f.subs)
}

// SubscriberCount returns the number of active feed subscriptions.
func (f *AttentionFeed) SubscriberCount() int {
	return f.subscriberCount()
}

// heartbeatLoop emits periodic heartbeat events.
func (f *AttentionFeed) heartbeatLoop() {
	defer f.stopWg.Done()
	if f.config.HeartbeatInterval <= 0 {
		return
	}
	timer := time.NewTimer(f.config.HeartbeatInterval)
	defer timer.Stop()

	for {
		select {
		case <-f.stopCh:
			return
		case t := <-timer.C:
			nextInterval := f.config.HeartbeatInterval
			if f.subscriberCount() == 0 {
				timer.Reset(nextInterval)
				continue
			}
			journalStats := f.journal.Stats()
			if journalStats.LastEventTime != nil && !journalStats.LastEventTime.IsZero() {
				idle := t.Sub(*journalStats.LastEventTime)
				if idle < f.config.HeartbeatInterval {
					nextInterval = f.config.HeartbeatInterval - idle
					if nextInterval < time.Millisecond {
						nextInterval = time.Millisecond
					}
					timer.Reset(nextInterval)
					continue
				}
			}
			details := map[string]any{
				"journal_stats":    journalStats,
				"subscriber_count": f.subscriberCount(),
			}
			if journalStats.LastEventTime != nil && !journalStats.LastEventTime.IsZero() {
				lastEventTime := journalStats.LastEventTime.UTC()
				details["last_event_time"] = lastEventTime.Format(time.RFC3339Nano)
				details["idle_ms"] = t.Sub(lastEventTime).Milliseconds()
			}
			f.PublishEphemeral(AttentionEvent{
				Ts:            t.UTC().Format(time.RFC3339Nano),
				Category:      EventCategorySystem,
				Type:          EventType(DefaultTransportLiveness.HeartbeatType),
				Source:        "attention_feed",
				Actionability: ActionabilityBackground,
				Severity:      SeverityDebug,
				Summary:       "Heartbeat",
				Details:       details,
			})
			timer.Reset(nextInterval)
		}
	}
}

// =============================================================================
// Event Builder Helpers
// =============================================================================

var supportedAttentionActionNames = map[string]struct{}{
	"robot-attention":            {},
	"robot-bead-show":            {},
	"robot-context":              {},
	"robot-diff":                 {},
	"robot-digest":               {},
	"robot-events":               {},
	"robot-graph":                {},
	"robot-inspect-coordination": {},
	"robot-mail-check":           {},
	"robot-plan":                 {},
	"robot-send":                 {},
	"robot-snapshot":             {},
	"robot-status":               {},
	"robot-tail":                 {},
	"robot-watch-bead":           {},
}

var suppressedLoggedAttentionReasons = map[ntmevents.EventType]string{
	ntmevents.EventPromptSend:      "prompt send events are high-volume control traffic; inspect pane output or token reports instead",
	ntmevents.EventPromptBroadcast: "prompt broadcast events are high-volume control traffic; inspect pane output or token reports instead",
	ntmevents.EventTemplateUse:     "template selection is configuration metadata and does not belong in the high-level operator feed",
}

const (
	attentionSignalSessionChanged      = "session_changed"
	attentionSignalPaneChanged         = "pane_changed"
	attentionSignalAgentStateChanged   = "agent_state_changed"
	attentionSignalStalled             = "stalled"
	attentionSignalContextHot          = "context_hot"
	attentionSignalRateLimited         = "rate_limited"
	attentionSignalAlertRaised         = "alert_raised"
	attentionSignalReservationConflict = "reservation_conflict"
	attentionSignalFileConflict        = "file_conflict"

	attentionContextHotActionThreshold = 90.0
)

// NewTrackerEvent converts a legacy state tracker change into a normalized
// attention event so existing tracker-based flows can feed the cursored journal.
func NewTrackerEvent(change tracker.StateChange) AttentionEvent {
	ts := change.Timestamp.UTC()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	details := cloneAnyMap(change.Details)
	if details == nil {
		details = map[string]any{}
	}
	if change.Pane != "" {
		details["pane_ref"] = change.Pane
	}

	paneIdx := attentionPaneIndex(change.Pane)
	event := AttentionEvent{
		Ts:            ts.Format(time.RFC3339Nano),
		Session:       change.Session,
		Pane:          paneIdx,
		Source:        "state_tracker",
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Details:       details,
		Summary:       fmt.Sprintf("%s changed", change.Type),
	}

	switch change.Type {
	case tracker.ChangeAgentOutput:
		event.Category = EventCategoryPane
		event.Type = EventTypePaneOutput
		event.Actionability = ActionabilityInteresting
		event.Summary = attentionSummary(change.Session, change.Pane, "agent output detected")
		if change.Session != "" && change.Pane != "" {
			event.NextActions = []NextAction{{
				Action: "robot-tail",
				Args:   fmt.Sprintf("--robot-tail=%s --panes=%s --lines=50", change.Session, change.Pane),
				Reason: "Inspect the new pane output",
			}}
		}
	case tracker.ChangeAgentState:
		event.Category = EventCategoryAgent
		event.Type = EventTypeAgentStateChange
		if state, _ := details["state"].(string); state == "error" {
			event.Severity = SeverityError
			event.Actionability = ActionabilityActionRequired
			event.Summary = attentionSummary(change.Session, change.Pane, "agent entered error state")
		} else {
			event.Actionability = ActionabilityInteresting
			event.Summary = attentionSummary(change.Session, change.Pane, "agent state changed")
		}
	case tracker.ChangeBeadUpdate:
		event.Category = EventCategoryBead
		event.Type = EventTypeBeadUpdated
		event.Actionability = ActionabilityInteresting
		event.Summary = attentionSummary(change.Session, change.Pane, "bead updated")
	case tracker.ChangeMailReceived:
		event.Category = EventCategoryMail
		event.Type = EventTypeMailReceived
		event.Actionability = ActionabilityInteresting
		event.Summary = attentionSummary(change.Session, change.Pane, "mail received")
	case tracker.ChangeAlert:
		event.Category = EventCategoryAlert
		event.Type = EventTypeAlertWarning
		event.Actionability = ActionabilityActionRequired
		event.Severity = SeverityWarning
		event.Summary = attentionSummary(change.Session, change.Pane, "alert raised")
	case tracker.ChangePaneCreated:
		event.Category = EventCategoryPane
		event.Type = EventTypePaneCreated
		event.Summary = attentionSummary(change.Session, change.Pane, "pane created")
	case tracker.ChangePaneRemoved:
		event.Category = EventCategoryPane
		event.Type = EventTypePaneDestroyed
		event.Actionability = ActionabilityInteresting
		event.Summary = attentionSummary(change.Session, change.Pane, "pane removed")
	case tracker.ChangeSessionCreated:
		event.Category = EventCategorySession
		event.Type = EventTypeSessionCreated
		event.Summary = attentionSummary(change.Session, "", "session created")
	case tracker.ChangeSessionRemoved:
		event.Category = EventCategorySession
		event.Type = EventTypeSessionDestroyed
		event.Actionability = ActionabilityInteresting
		event.Summary = attentionSummary(change.Session, "", "session removed")
	case tracker.ChangeFileChange:
		event.Category = EventCategoryFile
		event.Type = EventTypeFileChanged
		event.Actionability = ActionabilityInteresting
		event.Summary = attentionSummary(change.Session, change.Pane, "file changed")
	default:
		event.Category = EventCategorySystem
		event.Type = EventTypeSystemHealthChange
		event.Summary = attentionSummary(change.Session, change.Pane, fmt.Sprintf("%s observed", change.Type))
	}

	return annotateAttentionSignal(event)
}

// SuppressedLoggedAttentionReason documents which legacy analytics events are
// intentionally omitted from the attention feed and why.
func SuppressedLoggedAttentionReason(eventType ntmevents.EventType) string {
	return suppressedLoggedAttentionReasons[eventType]
}

// NewLoggedAttentionEvent converts a legacy analytics/logged event into the
// normalized attention envelope. It returns false when the source event is
// intentionally suppressed from the high-level operator feed.
func NewLoggedAttentionEvent(event ntmevents.Event) (AttentionEvent, bool) {
	if SuppressedLoggedAttentionReason(event.Type) != "" {
		return AttentionEvent{}, false
	}

	ts := event.Timestamp.UTC()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	details := cloneAnyMap(event.Data)
	if details == nil {
		details = map[string]any{}
	}
	if event.AgentName != "" {
		details["agent_name"] = event.AgentName
	}
	if event.CorrelationID != "" {
		details["correlation_id"] = event.CorrelationID
	}

	pane := attentionPaneFromDetails(details)
	result := AttentionEvent{
		Ts:            ts.Format(time.RFC3339Nano),
		Session:       event.Session,
		Pane:          pane,
		Source:        "event_log",
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Details:       details,
		Summary:       attentionSummary(event.Session, "", fmt.Sprintf("%s recorded", attentionHumanize(string(event.Type)))),
	}

	switch event.Type {
	case ntmevents.EventSessionCreate:
		result.Category = EventCategorySession
		result.Type = EventTypeSessionCreated
		result.Actionability = ActionabilityInteresting
		result.Summary = attentionSummary(event.Session, "", "session created")
		result.NextActions = []NextAction{attentionStatusNextAction("Inspect active sessions and panes")}
	case ntmevents.EventSessionKill:
		result.Category = EventCategorySession
		result.Type = EventTypeSessionDestroyed
		result.Actionability = ActionabilityInteresting
		result.Severity = SeverityWarning
		result.Summary = attentionSummary(event.Session, "", "session ended")
	case ntmevents.EventSessionAttach:
		result.Category = EventCategorySession
		result.Type = EventTypeSessionAttached
		result.Summary = attentionSummary(event.Session, "", "session attached")
	case ntmevents.EventAgentSpawn, ntmevents.EventAgentAdd:
		result.Category = EventCategoryAgent
		result.Type = EventTypeAgentStarted
		result.Actionability = ActionabilityInteresting
		result.Summary = attentionSummary(event.Session, attentionPaneRef(details), attentionAgentSummary("agent started", event.AgentName, details))
	case ntmevents.EventAgentCrash:
		result.Category = EventCategoryAgent
		result.Type = EventTypeAgentError
		result.Actionability = ActionabilityActionRequired
		result.Severity = SeverityError
		result.Summary = attentionSummary(event.Session, attentionPaneRef(details), attentionAgentSummary("agent crashed", event.AgentName, details))
		result.NextActions = attentionTailOrStatusActions(event.Session, attentionEventPaneRef(result), "Inspect the rate-limited agent output")
	case ntmevents.EventAgentRestart:
		result.Category = EventCategoryAgent
		result.Type = EventTypeAgentRecovered
		result.Actionability = ActionabilityInteresting
		result.Summary = attentionSummary(event.Session, attentionPaneRef(details), attentionAgentSummary("agent restarted", event.AgentName, details))
	case ntmevents.EventInterrupt:
		result.Category = EventCategoryAgent
		result.Type = EventTypeAgentStateChange
		result.Actionability = ActionabilityInteresting
		result.Summary = attentionSummary(event.Session, attentionPaneRef(details), attentionAgentSummary("agent interrupted", event.AgentName, details))
	case ntmevents.EventCheckpointCreate:
		result.Category = EventCategorySystem
		result.Type = EventTypeSystemHealthChange
		result.Actionability = ActionabilityInteresting
		result.Summary = attentionSummary(event.Session, "", "checkpoint created")
	case ntmevents.EventCheckpointRestore:
		result.Category = EventCategorySystem
		result.Type = EventTypeSystemHealthChange
		result.Actionability = ActionabilityInteresting
		result.Summary = attentionSummary(event.Session, "", "checkpoint restored")
		result.NextActions = []NextAction{attentionStatusNextAction("Verify restored session state")}
	case ntmevents.EventSessionSave:
		result.Category = EventCategorySystem
		result.Type = EventTypeSystemHealthChange
		result.Summary = attentionSummary(event.Session, "", "session saved")
	case ntmevents.EventSessionRestore:
		result.Category = EventCategorySystem
		result.Type = EventTypeSystemHealthChange
		result.Actionability = ActionabilityInteresting
		result.Summary = attentionSummary(event.Session, "", "session restored")
		result.NextActions = []NextAction{attentionStatusNextAction("Inspect restored session state")}
	case ntmevents.EventError:
		result.Category = EventCategoryAlert
		result.Type = EventTypeAlertWarning
		result.Actionability = ActionabilityActionRequired
		result.Severity = SeverityError
		result.Summary = attentionSummary(event.Session, "", attentionMessageSummary("error recorded", details))
		result.NextActions = []NextAction{attentionStatusNextAction("Inspect the current robot state")}
	default:
		result.Category = EventCategorySystem
		result.Type = EventTypeSystemHealthChange
	}

	return annotateAttentionSignal(result), true
}

// NewBusAttentionEvent converts an event-bus event into the normalized
// attention envelope. It returns false when the source event is intentionally
// suppressed from the high-level operator feed.
func NewBusAttentionEvent(event ntmevents.BusEvent) (AttentionEvent, bool) {
	switch e := event.(type) {
	case ntmevents.ProfileAssignedEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.profile", attentionDetailsFromStruct(e), EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, fmt.Sprintf("profile %s assigned to %s", e.Profile, e.AgentID), []NextAction{attentionStatusNextAction("Inspect the updated agent profile")}), true
	case ntmevents.ProfileSwitchedEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.profile", attentionDetailsFromStruct(e), EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, fmt.Sprintf("profile switched for %s from %s to %s", e.AgentID, e.OldProfile, e.NewProfile), []NextAction{attentionStatusNextAction("Inspect the updated agent profile")}), true
	case ntmevents.ContextWarningEvent:
		actionability := ActionabilityInteresting
		if e.UsagePercent >= 90 {
			actionability = ActionabilityActionRequired
		}
		return attentionFromBusStruct(e.BaseEvent, "event_bus.context", attentionDetailsFromStruct(e), EventCategoryAlert, EventTypeAlertWarning, actionability, SeverityWarning, fmt.Sprintf("context usage high for %s (%.1f%%)", e.AgentID, e.UsagePercent), attentionContextActions(e.Session, "Inspect context pressure before the agent stalls")), true
	case ntmevents.RotationStartedEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.rotation", attentionDetailsFromStruct(e), EventCategoryAgent, EventTypeAgentCompacted, ActionabilityInteresting, SeverityInfo, fmt.Sprintf("rotation started for %s", e.AgentID), attentionContextActions(e.Session, "Inspect context pressure during rotation")), true
	case ntmevents.RotationCompletedEvent:
		eventType := EventTypeAgentRecovered
		actionability := ActionabilityInteresting
		severity := SeverityInfo
		oldAgentID := strings.TrimSpace(e.OldAgentID)
		newAgentID := strings.TrimSpace(e.NewAgentID)
		summary := "rotation completed"
		switch {
		case oldAgentID != "" && newAgentID != "":
			summary = fmt.Sprintf("rotation completed from %s to %s", oldAgentID, newAgentID)
		case oldAgentID != "":
			summary = fmt.Sprintf("rotation completed for %s", oldAgentID)
		case newAgentID != "":
			summary = fmt.Sprintf("rotation completed to %s", newAgentID)
		}
		nextActions := []NextAction{attentionStatusNextAction("Inspect rotated agents")}
		if !e.Success {
			eventType = EventTypeAgentError
			actionability = ActionabilityActionRequired
			severity = SeverityError
			failedAgentID := oldAgentID
			if failedAgentID == "" {
				failedAgentID = newAgentID
			}
			if failedAgentID == "" {
				failedAgentID = "unknown agent"
			}
			summary = fmt.Sprintf("rotation failed for %s", failedAgentID)
		}
		return attentionFromBusStruct(e.BaseEvent, "event_bus.rotation", attentionDetailsFromStruct(e), EventCategoryAgent, eventType, actionability, severity, summary, nextActions), true
	case ntmevents.CheckpointCreatedEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.checkpoint", attentionDetailsFromStruct(e), EventCategorySystem, EventTypeSystemHealthChange, ActionabilityInteresting, SeverityInfo, fmt.Sprintf("checkpoint %s created", e.Name), nil), true
	case ntmevents.CheckpointRestoredEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.checkpoint", attentionDetailsFromStruct(e), EventCategorySystem, EventTypeSystemHealthChange, ActionabilityInteresting, SeverityInfo, fmt.Sprintf("checkpoint %s restored", e.Name), []NextAction{attentionStatusNextAction("Inspect restored agent state")}), true
	case ntmevents.WorkflowStartedEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.workflow", attentionDetailsFromStruct(e), EventCategorySystem, EventTypeSystemHealthChange, ActionabilityInteresting, SeverityInfo, fmt.Sprintf("workflow %s started", e.Workflow), nil), true
	case ntmevents.StageTransitionEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.workflow", attentionDetailsFromStruct(e), EventCategorySystem, EventTypeSystemHealthChange, ActionabilityInteresting, SeverityInfo, fmt.Sprintf("workflow %s moved from %s to %s", e.Workflow, e.FromStage, e.ToStage), nil), true
	case ntmevents.WorkflowPausedEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.workflow", attentionDetailsFromStruct(e), EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, fmt.Sprintf("workflow %s paused: %s", e.Workflow, e.Reason), []NextAction{attentionStatusNextAction("Inspect paused workflow state")}), true
	case ntmevents.WorkflowCompletedEvent:
		category := EventCategorySystem
		eventType := EventTypeSystemHealthChange
		actionability := ActionabilityInteresting
		severity := SeverityInfo
		summary := fmt.Sprintf("workflow %s completed", e.Workflow)
		nextActions := []NextAction(nil)
		if !e.Success {
			category = EventCategoryAlert
			eventType = EventTypeAlertWarning
			actionability = ActionabilityActionRequired
			severity = SeverityError
			summary = fmt.Sprintf("workflow %s failed", e.Workflow)
			nextActions = []NextAction{attentionStatusNextAction("Inspect failed workflow state")}
		}
		return attentionFromBusStruct(e.BaseEvent, "event_bus.workflow", attentionDetailsFromStruct(e), category, eventType, actionability, severity, summary, nextActions), true
	case ntmevents.AgentStallEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.agent", attentionDetailsFromStruct(e), EventCategoryAgent, EventTypeAgentStalled, ActionabilityActionRequired, SeverityWarning, fmt.Sprintf("agent %s stalled for %.0fs", e.AgentID, e.StallDuration), attentionContextActions(e.Session, "Inspect context pressure for the stalled agent")), true
	case ntmevents.AgentErrorEvent:
		return attentionFromBusStruct(e.BaseEvent, "event_bus.agent", attentionDetailsFromStruct(e), EventCategoryAgent, EventTypeAgentError, ActionabilityActionRequired, SeverityError, fmt.Sprintf("agent %s error: %s", e.AgentID, e.Message), attentionTailOrStatusActions(e.Session, "", "Inspect the failing agent output")), true
	case ntmevents.AlertEvent:
		eventType := EventTypeAlertInfo
		actionability := ActionabilityInteresting
		severity := SeverityInfo
		switch strings.ToLower(e.Severity) {
		case "critical":
			eventType = EventTypeAlertAttentionRequired
			actionability = ActionabilityActionRequired
			severity = SeverityCritical
		case "error":
			eventType = EventTypeAlertAttentionRequired
			actionability = ActionabilityActionRequired
			severity = SeverityError
		case "warning":
			eventType = EventTypeAlertWarning
			actionability = ActionabilityActionRequired
			severity = SeverityWarning
		}
		return attentionFromBusStruct(e.BaseEvent, "event_bus.alert", attentionDetailsFromStruct(e), EventCategoryAlert, eventType, actionability, severity, e.Message, []NextAction{attentionStatusNextAction("Inspect the active alerts")}), true
	case ntmevents.ReservationConflictEvent:
		holders := strings.Join(e.Holders, ", ")
		summary := fmt.Sprintf("reservation conflict on %s: %s blocked by [%s]", e.Path, e.RequestorAgent, holders)
		details := map[string]any{
			"path":            e.Path,
			"requestor_agent": e.RequestorAgent,
			"requestor_pane":  e.RequestorPane,
			"holders":         e.Holders,
			"conflict_kind":   "reservation",
		}
		nextActions := attentionReservationConflictActions(e.Session, details, "Inspect the conflicting reservation state")
		return attentionFromBusStruct(e.BaseEvent, "event_bus.conflict", details, EventCategoryFile, EventTypeFileConflict, ActionabilityActionRequired, SeverityWarning, summary, nextActions), true
	case ntmevents.FileConflictEvent:
		agents := strings.Join(e.Agents, ", ")
		summary := fmt.Sprintf("file conflict on %s: agents [%s] editing concurrently", e.Path, agents)
		details := map[string]any{
			"path":          e.Path,
			"agents":        e.Agents,
			"conflict_kind": "file",
		}
		nextActions := attentionConflictActions(e.Session, e.Path, "Compare agent outputs for conflict resolution")
		return attentionFromBusStruct(e.BaseEvent, "event_bus.conflict", details, EventCategoryFile, EventTypeFileConflict, ActionabilityActionRequired, SeverityWarning, summary, nextActions), true
	case ntmevents.WebhookEvent:
		return attentionFromWebhookEvent(e), true
	case ntmevents.BaseEvent:
		return attentionFromBusStruct(e, "event_bus", nil, EventCategorySystem, EventTypeSystemHealthChange, ActionabilityBackground, SeverityInfo, attentionHumanize(e.Type), nil), true
	default:
		return AttentionEvent{}, false
	}
}

// NewAgentStateChangeEvent creates an agent state change event.
func NewAgentStateChangeEvent(session string, pane int, agentID, fromState, toState, source string) AttentionEvent {
	actionability := ActionabilityBackground
	if toState == "idle" || toState == "error" {
		actionability = ActionabilityInteresting
	}

	severity := SeverityInfo
	if toState == "error" {
		severity = SeverityError
		actionability = ActionabilityActionRequired
	}

	return annotateAttentionSignal(AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Session:       session,
		Pane:          pane,
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Source:        source,
		Actionability: actionability,
		Severity:      severity,
		Summary:       fmt.Sprintf("Agent %s transitioned from %s to %s", agentID, fromState, toState),
		Details: map[string]any{
			"agent_id":   agentID,
			"from_state": fromState,
			"to_state":   toState,
		},
	})
}

// NewBeadEvent creates a bead-related event.
func NewBeadEvent(eventType EventType, beadID, title string, details map[string]any) AttentionEvent {
	actionability := ActionabilityBackground
	severity := SeverityInfo

	switch eventType {
	case EventTypeBeadUnblocked:
		actionability = ActionabilityInteresting
		severity = SeverityInfo
	case EventTypeBeadClosed:
		actionability = ActionabilityBackground
		severity = SeverityInfo
	}

	summary := fmt.Sprintf("Bead %s: %s", beadID, title)
	if eventType == EventTypeBeadUnblocked {
		summary = fmt.Sprintf("Bead %s became ready: %s", beadID, title)
	}

	return AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Category:      EventCategoryBead,
		Type:          eventType,
		Source:        "bead_tracker",
		Actionability: actionability,
		Severity:      severity,
		Summary:       summary,
		Details:       details,
		NextActions: []NextAction{
			{
				Action: "robot-bead-show",
				Args:   fmt.Sprintf("--robot-bead-show=%s", beadID),
				Reason: "View bead details",
			},
		},
	}
}

// NewMailEvent creates a mail-related event.
func NewMailEvent(eventType EventType, from, to, subject string, ackRequired bool) AttentionEvent {
	actionability := ActionabilityInteresting
	if ackRequired {
		actionability = ActionabilityActionRequired
	}

	return AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Category:      EventCategoryMail,
		Type:          eventType,
		Source:        "agent_mail",
		Actionability: actionability,
		Severity:      SeverityInfo,
		Summary:       fmt.Sprintf("Mail from %s to %s: %s", from, to, subject),
		Details: map[string]any{
			"from":         from,
			"to":           to,
			"subject":      subject,
			"ack_required": ackRequired,
		},
	}
}

// NewFileConflictEvent creates a file conflict event.
func NewFileConflictEvent(session string, filePath string, agents []string) AttentionEvent {
	return annotateAttentionSignal(AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Session:       session,
		Category:      EventCategoryFile,
		Type:          EventTypeFileConflict,
		Source:        "conflict_detector",
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityError,
		Summary:       fmt.Sprintf("File conflict: %s modified by %v", filePath, agents),
		Details: map[string]any{
			"file":   filePath,
			"agents": agents,
		},
		NextActions: []NextAction{
			{
				Action: "robot-diff",
				Args:   fmt.Sprintf("--robot-diff=%s", session),
				Reason: "Compare agent changes",
			},
		},
	})
}

// NewReservationConflictEvent creates an attention event for a concrete
// reservation conflict observed by the file reservation watcher.
func NewReservationConflictEvent(conflict watcher.FileConflict) (AttentionEvent, bool) {
	path := strings.TrimSpace(conflict.Path)
	holders := compactStringSlice(conflict.Holders)
	if path == "" || len(holders) == 0 {
		return AttentionEvent{}, false
	}

	ts := attentionTimestamp(conflict.DetectedAt)
	details := map[string]any{
		"path":            path,
		"requestor_agent": strings.TrimSpace(conflict.RequestorAgent),
		"requestor_pane":  strings.TrimSpace(conflict.RequestorPane),
		"holders":         holders,
	}
	if len(conflict.HolderReservationIDs) > 0 {
		details["holder_reservation_ids"] = append([]int(nil), conflict.HolderReservationIDs...)
	}
	if conflict.ReservedSince != nil && !conflict.ReservedSince.IsZero() {
		details["reserved_since"] = conflict.ReservedSince.UTC().Format(time.RFC3339Nano)
	}
	if conflict.ExpiresAt != nil && !conflict.ExpiresAt.IsZero() {
		details["expires_at"] = conflict.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}

	summary := fmt.Sprintf("reservation conflict on %s", path)
	if requestor := strings.TrimSpace(conflict.RequestorAgent); requestor != "" {
		summary = fmt.Sprintf("reservation conflict on %s for %s", path, requestor)
	}

	return annotateAttentionSignal(AttentionEvent{
		Ts:            ts.Format(time.RFC3339Nano),
		Session:       conflict.SessionName,
		Pane:          attentionPaneIndex(conflict.RequestorPane),
		Category:      EventCategoryFile,
		Type:          EventTypeFileConflict,
		Source:        "watcher.file_reservation",
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       attentionSummary(conflict.SessionName, conflict.RequestorPane, summary),
		Details:       details,
		NextActions:   attentionReservationConflictActions(conflict.SessionName, details, "Inspect the conflicting reservation state"),
	}), true
}

// NewTrackedFileConflictEvent creates an attention event for a concrete file
// overlap observed from tracker conflict analysis.
func NewTrackedFileConflictEvent(session string, conflict tracker.Conflict) (AttentionEvent, bool) {
	path := strings.TrimSpace(conflict.Path)
	agents := compactStringSlice(conflict.Agents)
	if path == "" || len(agents) < 2 {
		return AttentionEvent{}, false
	}

	severity := SeverityWarning
	if strings.EqualFold(conflict.Severity, "critical") {
		severity = SeverityCritical
	}

	details := map[string]any{
		"path":             path,
		"agents":           agents,
		"change_count":     len(conflict.Changes),
		"tracker_severity": strings.TrimSpace(conflict.Severity),
		"last_at":          attentionTimestamp(conflict.LastAt).Format(time.RFC3339Nano),
	}

	return annotateAttentionSignal(AttentionEvent{
		Ts:            attentionTimestamp(conflict.LastAt).Format(time.RFC3339Nano),
		Session:       session,
		Category:      EventCategoryFile,
		Type:          EventTypeFileConflict,
		Source:        "tracker.conflicts",
		Actionability: ActionabilityActionRequired,
		Severity:      severity,
		Summary:       attentionSummary(session, "", fmt.Sprintf("file conflict on %s across %d agents", path, len(agents))),
		Details:       details,
		NextActions:   attentionConflictActions(session, path, "Compare conflicting file edits"),
	}), true
}

// =============================================================================
// Global Feed Instance
// =============================================================================

// globalFeed is the default attention feed instance.
var globalFeed *AttentionFeed
var globalFeedMu sync.Mutex

// GetAttentionFeed returns the global attention feed instance.
// The feed is lazily initialized with default configuration and can later be
// replaced with a store-backed instance during application startup.
func GetAttentionFeed() *AttentionFeed {
	globalFeedMu.Lock()
	defer globalFeedMu.Unlock()
	if globalFeed == nil {
		globalFeed = NewAttentionFeed(DefaultAttentionFeedConfig())
	}
	return globalFeed
}

// PeekAttentionFeed returns the global attention feed if it has already been
// initialized. Unlike GetAttentionFeed, it never creates a new feed instance.
func PeekAttentionFeed() *AttentionFeed {
	globalFeedMu.Lock()
	defer globalFeedMu.Unlock()
	return globalFeed
}

// SetAttentionFeed sets a custom global feed (for testing).
// Production startup uses this to replace the default in-memory feed with a
// durable store-backed instance. Tests also use it to install controlled feeds.
func SetAttentionFeed(feed *AttentionFeed) {
	globalFeedMu.Lock()
	defer globalFeedMu.Unlock()
	globalFeed = feed
}

// NOTE: --robot-events command implementation lives in events.go (br-kpvhy).
// The EventsOptions, EventsResponse, GetEvents, filterEvents, and toStringSetForEvents
// are all defined there. This comment preserves the bead reference.

func cloneAttentionEvent(event AttentionEvent) AttentionEvent {
	cloned := event
	cloned.Details = cloneAnyMap(event.Details)
	if event.NextActions != nil {
		cloned.NextActions = append([]NextAction(nil), event.NextActions...)
	}
	return cloned
}

func sanitizeNextActions(actions []NextAction) []NextAction {
	if len(actions) == 0 {
		// Normalize nil to empty slice so JSON always emits [] not null.
		return []NextAction{}
	}

	filtered := make([]NextAction, 0, len(actions))
	for _, action := range actions {
		if action.Action == "" {
			continue
		}
		if _, ok := supportedAttentionActionNames[action.Action]; !ok {
			continue
		}
		filtered = append(filtered, action)
	}
	if len(filtered) == 0 {
		return []NextAction{}
	}
	return filtered
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	raw, err := json.Marshal(src)
	if err != nil {
		dst := make(map[string]any, len(src))
		for k, v := range src {
			dst[k] = v
		}
		return dst
	}
	var dst map[string]any
	if err := json.Unmarshal(raw, &dst); err != nil {
		dst = make(map[string]any, len(src))
		for k, v := range src {
			dst[k] = v
		}
	}
	return dst
}

func attentionDetailsFromStruct(src any) map[string]any {
	raw, err := json.Marshal(src)
	if err != nil {
		return map[string]any{}
	}

	var details map[string]any
	if err := json.Unmarshal(raw, &details); err != nil {
		return map[string]any{}
	}

	delete(details, "type")
	delete(details, "timestamp")
	delete(details, "session")
	return details
}

func attentionFromBusStruct(base ntmevents.BaseEvent, source string, details map[string]any, category EventCategory, eventType EventType, actionability Actionability, severity Severity, summary string, actions []NextAction) AttentionEvent {
	paneRef := attentionPaneRef(details)
	return annotateAttentionSignal(AttentionEvent{
		Ts:            attentionTimestamp(base.Timestamp).Format(time.RFC3339Nano),
		Session:       base.Session,
		Pane:          attentionPaneIndex(paneRef),
		Category:      category,
		Type:          eventType,
		Source:        source,
		Actionability: actionability,
		Severity:      severity,
		Summary:       attentionSummary(base.Session, paneRef, summary),
		Details:       details,
		NextActions:   actions,
	})
}

func attentionFromWebhookEvent(event ntmevents.WebhookEvent) AttentionEvent {
	details := attentionDetailsFromStruct(event)
	paneRef := event.Pane
	if paneRef != "" {
		details["pane_ref"] = paneRef
	}
	base := ntmevents.BaseEvent{
		Type:      event.Type,
		Timestamp: event.Timestamp,
		Session:   event.Session,
	}

	switch event.Type {
	case ntmevents.WebhookSessionCreated:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategorySession, EventTypeSessionCreated, ActionabilityInteresting, SeverityInfo, "session created", []NextAction{attentionStatusNextAction("Inspect active sessions and panes")})
	case ntmevents.WebhookSessionKilled, ntmevents.WebhookSessionEnded:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategorySession, EventTypeSessionDestroyed, ActionabilityInteresting, SeverityWarning, "session ended", nil)
	case ntmevents.WebhookAgentStarted:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAgent, EventTypeAgentStarted, ActionabilityInteresting, SeverityInfo, attentionAgentSummary("agent started", event.Agent, details), nil)
	case ntmevents.WebhookAgentStopped:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAgent, EventTypeAgentStopped, ActionabilityInteresting, SeverityInfo, attentionAgentSummary("agent stopped", event.Agent, details), nil)
	case ntmevents.WebhookAgentError, ntmevents.WebhookAgentCrashed:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAgent, EventTypeAgentError, ActionabilityActionRequired, SeverityError, attentionAgentSummary("agent error", event.Agent, details), attentionTailOrStatusActions(event.Session, paneRef, "Inspect the failing agent output"))
	case ntmevents.WebhookAgentRestarted:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAgent, EventTypeAgentRecovered, ActionabilityInteresting, SeverityInfo, attentionAgentSummary("agent restarted", event.Agent, details), nil)
	case ntmevents.WebhookAgentIdle:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, attentionAgentSummary("agent became idle", event.Agent, details), nil)
	case ntmevents.WebhookAgentBusy:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAgent, EventTypeAgentStateChange, ActionabilityBackground, SeverityInfo, attentionAgentSummary("agent became busy", event.Agent, details), nil)
	case ntmevents.WebhookAgentRateLimit:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, attentionAgentSummary("agent hit a rate limit", event.Agent, details), attentionTailOrStatusActions(event.Session, paneRef, "Inspect the rate-limited agent output"))
	case ntmevents.WebhookAgentCompleted:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, attentionAgentSummary("agent completed work", event.Agent, details), nil)
	case ntmevents.WebhookRotationNeeded:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAlert, EventTypeAlertAttentionRequired, ActionabilityActionRequired, SeverityWarning, "agent rotation needed", attentionContextActions(event.Session, "Inspect context pressure before rotating the agent"))
	case ntmevents.WebhookHealthDegraded:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "system health degraded", []NextAction{attentionStatusNextAction("Inspect the current robot state")})
	case ntmevents.WebhookBeadAssigned:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryBead, EventTypeBeadUpdated, ActionabilityInteresting, SeverityInfo, "bead assigned", attentionBeadActions(attentionBeadID(details), "Inspect bead details"))
	case ntmevents.WebhookBeadCompleted:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryBead, EventTypeBeadClosed, ActionabilityBackground, SeverityInfo, "bead completed", attentionBeadActions(attentionBeadID(details), "Inspect bead details"))
	case ntmevents.WebhookBeadFailed:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityError, "bead failed", attentionBeadActions(attentionBeadID(details), "Inspect the failed bead"))
	default:
		return attentionFromBusStruct(base, "event_bus.webhook", details, EventCategorySystem, EventTypeSystemHealthChange, ActionabilityBackground, SeverityInfo, attentionHumanize(event.Type), nil)
	}
}

func attentionPaneIndex(raw string) int {
	pane := strings.TrimSpace(raw)
	if pane == "" {
		return 0
	}
	pane = strings.TrimPrefix(pane, "%")
	if dot := strings.LastIndex(pane, "."); dot >= 0 {
		pane = pane[dot+1:]
	}
	idx, err := strconv.Atoi(pane)
	if err != nil || idx < 0 {
		return 0
	}
	return idx
}

func attentionSummary(session, pane, action string) string {
	switch {
	case session != "" && pane != "":
		return fmt.Sprintf("%s in %s pane %s", action, session, pane)
	case session != "":
		return fmt.Sprintf("%s in %s", action, session)
	default:
		return action
	}
}

func attentionTimestamp(ts time.Time) time.Time {
	if ts.IsZero() {
		return time.Now().UTC()
	}
	return ts.UTC()
}

func attentionHumanize(raw string) string {
	return strings.ReplaceAll(strings.ReplaceAll(raw, "_", " "), ".", " ")
}

func attentionPaneFromDetails(details map[string]any) int {
	if details == nil {
		return 0
	}
	for _, key := range []string{"pane_ref", "pane", "pane_index"} {
		value, ok := details[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			return attentionPaneIndex(v)
		case int:
			if v > 0 {
				return v
			}
		case int32:
			if v > 0 {
				return int(v)
			}
		case int64:
			if v > 0 {
				return int(v)
			}
		case float64:
			if v > 0 {
				return int(v)
			}
		}
	}
	return 0
}

func attentionPaneRef(details map[string]any) string {
	if details == nil {
		return ""
	}
	for _, key := range []string{"pane_ref", "pane", "pane_index"} {
		value, ok := details[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			return strings.TrimSpace(v)
		case int:
			return strconv.Itoa(v)
		case int32:
			return strconv.Itoa(int(v))
		case int64:
			return strconv.FormatInt(v, 10)
		case float64:
			return strconv.Itoa(int(v))
		}
	}
	return ""
}

func attentionAgentSummary(prefix, fallback string, details map[string]any) string {
	for _, key := range []string{"agent_id", "agent_name", "agent"} {
		if value, ok := details[key]; ok {
			if label := strings.TrimSpace(fmt.Sprint(value)); label != "" && label != "<nil>" {
				return fmt.Sprintf("%s for %s", prefix, label)
			}
		}
	}
	if label := strings.TrimSpace(fallback); label != "" {
		return fmt.Sprintf("%s for %s", prefix, label)
	}
	return prefix
}

func attentionMessageSummary(prefix string, details map[string]any) string {
	for _, key := range []string{"message", "error_type", "name"} {
		if value, ok := details[key]; ok {
			if label := strings.TrimSpace(fmt.Sprint(value)); label != "" && label != "<nil>" {
				return fmt.Sprintf("%s: %s", prefix, label)
			}
		}
	}
	return prefix
}

func annotateAttentionSignal(event AttentionEvent) AttentionEvent {
	signal, reason, metadata := deriveAttentionSignal(event)
	if signal == "" {
		return event
	}
	event = applyAttentionSignalPolicy(event, signal)
	if event.Details == nil {
		event.Details = map[string]any{}
	} else {
		event.Details = cloneAnyMap(event.Details)
	}
	event.Details["signal"] = signal
	event.Details["signal_reason"] = reason
	for key, value := range metadata {
		if _, exists := event.Details[key]; !exists {
			event.Details[key] = value
		}
	}
	return event
}

func applyAttentionSignalPolicy(event AttentionEvent, signal string) AttentionEvent {
	switch signal {
	case attentionSignalSessionChanged:
		event.Actionability = maxAttentionActionability(event.Actionability, ActionabilityInteresting)
		event.Severity = maxAttentionSeverity(event.Severity, SeverityInfo)
		if len(event.NextActions) == 0 {
			event.NextActions = []NextAction{attentionStatusNextAction("Inspect the updated session state")}
		}
	case attentionSignalPaneChanged:
		event.Actionability = maxAttentionActionability(event.Actionability, ActionabilityInteresting)
		event.Severity = maxAttentionSeverity(event.Severity, SeverityInfo)
		if len(event.NextActions) == 0 {
			event.NextActions = []NextAction{attentionStatusNextAction("Inspect the updated pane layout")}
		}
	case attentionSignalAgentStateChanged:
		event.Actionability = maxAttentionActionability(event.Actionability, ActionabilityInteresting)
		event.Severity = maxAttentionSeverity(event.Severity, SeverityInfo)
		if len(event.NextActions) == 0 {
			event.NextActions = []NextAction{attentionStatusNextAction("Inspect the updated agent state")}
		}
	case attentionSignalStalled:
		event.Actionability = maxAttentionActionability(event.Actionability, ActionabilityActionRequired)
		event.Severity = maxAttentionSeverity(event.Severity, SeverityWarning)
		if len(event.NextActions) == 0 {
			event.NextActions = attentionContextActions(event.Session, "Inspect context pressure for the stalled agent")
		}
	case attentionSignalContextHot:
		event.Actionability = maxAttentionActionability(event.Actionability, attentionContextHotActionability(event))
		event.Severity = maxAttentionSeverity(event.Severity, SeverityWarning)
		if len(event.NextActions) == 0 {
			event.NextActions = attentionContextActions(event.Session, "Inspect context pressure before the agent stalls")
		}
	case attentionSignalRateLimited:
		event.Actionability = maxAttentionActionability(event.Actionability, ActionabilityActionRequired)
		event.Severity = maxAttentionSeverity(event.Severity, SeverityWarning)
		if len(event.NextActions) == 0 {
			event.NextActions = attentionTailOrStatusActions(event.Session, attentionEventPaneRef(event), "Inspect the rate-limited agent output")
		}
	case attentionSignalAlertRaised:
		event.Actionability = maxAttentionActionability(event.Actionability, attentionAlertActionability(event))
		event.Severity = maxAttentionSeverity(event.Severity, SeverityInfo)
		if len(event.NextActions) == 0 {
			event.NextActions = []NextAction{attentionStatusNextAction("Inspect the active alerts")}
		}
	case attentionSignalReservationConflict:
		event.Actionability = maxAttentionActionability(event.Actionability, ActionabilityActionRequired)
		event.Severity = maxAttentionSeverity(event.Severity, SeverityWarning)
		if len(event.NextActions) == 0 {
			event.NextActions = attentionReservationConflictActions(event.Session, event.Details, "Inspect the conflicting reservation state")
		}
	case attentionSignalFileConflict:
		event.Actionability = maxAttentionActionability(event.Actionability, ActionabilityActionRequired)
		event.Severity = maxAttentionSeverity(event.Severity, SeverityWarning)
		if len(event.NextActions) == 0 {
			event.NextActions = attentionConflictActions(event.Session, attentionConflictPath(event), "Compare conflicting file edits")
		}
	}
	return event
}

func deriveAttentionSignal(event AttentionEvent) (string, string, map[string]any) {
	switch event.Type {
	case EventTypeSessionCreated, EventTypeSessionDestroyed, EventTypeSessionAttached, EventTypeSessionDetached:
		return attentionSignalSessionChanged, "session lifecycle changed", nil
	case EventTypePaneCreated, EventTypePaneDestroyed, EventTypePaneResized:
		return attentionSignalPaneChanged, "pane lifecycle or geometry changed", nil
	case EventTypeAgentStarted, EventTypeAgentStopped, EventTypeAgentStateChange, EventTypeAgentRecovered, EventTypeAgentCompacted:
		return attentionSignalAgentStateChanged, "agent lifecycle or state changed", nil
	case EventTypeAgentStalled:
		return attentionSignalStalled, fmt.Sprintf("agent exceeded the %ds stall heuristic", int(DefaultStallThreshold/time.Second)), map[string]any{
			"signal_threshold_seconds":   int(DefaultStallThreshold / time.Second),
			"signal_threshold_rationale": "stalling is inferred with the activity classifier's default 30s heuristic",
		}
	case EventTypeAlertAttentionRequired, EventTypeAlertWarning, EventTypeAlertInfo:
		if isContextHotAttentionEvent(event) {
			usage := attentionFloatDetail(event.Details, "usage_percent")
			reason := "context pressure warning emitted by the event bus"
			if usage > 0 {
				reason = fmt.Sprintf("context usage %.1f%% crossed the operator warning heuristic", usage)
				if usage >= attentionContextHotActionThreshold {
					reason = fmt.Sprintf("context usage %.1f%% is at or above the %.0f%% action threshold", usage, attentionContextHotActionThreshold)
				}
			}
			return attentionSignalContextHot, reason, map[string]any{
				"signal_threshold_percent":   attentionContextHotActionThreshold,
				"signal_threshold_rationale": "context warnings stay interesting below 90% usage and become action_required at 90%",
			}
		}
		if isRateLimitedAttentionEvent(event) {
			return attentionSignalRateLimited, "matched explicit rate-limit telemetry or rate-limit text", map[string]any{
				"signal_threshold_rationale": "rate-limit signals come from explicit webhook/events or known rate-limit text patterns",
			}
		}
		return attentionSignalAlertRaised, "alert emitted by ntm monitoring", nil
	case EventTypeFileConflict:
		return deriveConflictSignal(event)
	default:
		return "", "", nil
	}
}

func deriveConflictSignal(event AttentionEvent) (string, string, map[string]any) {
	if isReservationConflictAttentionEvent(event) {
		holders := attentionStringSliceDetail(event.Details, "holders")
		path := attentionConflictPath(event)
		reason := "active reservation holders blocked another reservation request"
		if len(holders) > 0 && path != "" {
			reason = fmt.Sprintf("%d active holder(s) blocked a reservation on %s", len(holders), path)
		}
		return attentionSignalReservationConflict, reason, map[string]any{
			"conflict_holder_count": len(holders),
			"conflict_kind":         "reservation",
		}
	}
	if isFileConflictAttentionEvent(event) {
		agents := attentionStringSliceDetail(event.Details, "agents")
		path := attentionConflictPath(event)
		reason := "multiple agents touched the same file"
		if len(agents) > 0 && path != "" {
			reason = fmt.Sprintf("%d agent(s) touched %s", len(agents), path)
		}
		return attentionSignalFileConflict, reason, map[string]any{
			"conflict_agent_count": len(agents),
			"conflict_kind":        "file",
		}
	}
	return "", "", nil
}

func isContextHotAttentionEvent(event AttentionEvent) bool {
	if event.Source == "event_bus.context" {
		return true
	}
	return attentionFloatDetail(event.Details, "usage_percent") > 0 &&
		strings.Contains(strings.ToLower(event.Summary), "context usage")
}

func isRateLimitedAttentionEvent(event AttentionEvent) bool {
	values := []string{
		event.Summary,
		event.Source,
		attentionStringDetail(event.Details, "alert_type"),
		attentionStringDetail(event.Details, "error_type"),
		attentionStringDetail(event.Details, "message"),
		attentionStringDetail(event.Details, "reason"),
		attentionStringDetail(event.Details, "status"),
	}
	for _, value := range values {
		lower := strings.ToLower(strings.TrimSpace(value))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "rate limit") ||
			strings.Contains(lower, "rate_limit") ||
			strings.Contains(lower, "rate-limit") ||
			strings.Contains(lower, "ratelimit") ||
			strings.Contains(lower, "too many requests") ||
			strings.Contains(lower, "quota exceeded") ||
			strings.Contains(lower, "429") {
			return true
		}
	}
	return false
}

func isReservationConflictAttentionEvent(event AttentionEvent) bool {
	path := attentionConflictPath(event)
	holders := attentionStringSliceDetail(event.Details, "holders")
	if path == "" || len(holders) == 0 {
		return false
	}
	if event.Source == "watcher.file_reservation" {
		return true
	}
	return attentionStringDetail(event.Details, "requestor_agent") != "" ||
		len(attentionStringSliceDetail(event.Details, "holder_reservation_ids")) > 0
}

func isFileConflictAttentionEvent(event AttentionEvent) bool {
	path := attentionConflictPath(event)
	agents := attentionStringSliceDetail(event.Details, "agents")
	if path == "" || len(agents) < 2 {
		return false
	}
	if event.Source == "tracker.conflicts" || event.Source == "conflict_detector" {
		return true
	}
	return attentionStringDetail(event.Details, "tracker_severity") != "" ||
		attentionFloatDetail(event.Details, "change_count") >= 2
}

func attentionStringDetail(details map[string]any, key string) string {
	if details == nil {
		return ""
	}
	value, ok := details[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func attentionStringSliceDetail(details map[string]any, key string) []string {
	if details == nil {
		return nil
	}
	value, ok := details[key]
	if !ok {
		return nil
	}
	switch v := value.(type) {
	case []string:
		return compactStringSlice(v)
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			label := strings.TrimSpace(fmt.Sprint(item))
			if label != "" && label != "<nil>" {
				items = append(items, label)
			}
		}
		return items
	case []int:
		items := make([]string, 0, len(v))
		for _, item := range v {
			items = append(items, strconv.Itoa(item))
		}
		return items
	}
	return nil
}

func attentionFloatDetail(details map[string]any, key string) float64 {
	if details == nil {
		return 0
	}
	value, ok := details[key]
	if !ok {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func attentionBoolDetail(details map[string]any, key string) bool {
	if details == nil {
		return false
	}
	value, ok := details[key]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		normalized := strings.TrimSpace(strings.ToLower(v))
		return normalized == "true" || normalized == "1" || normalized == "yes"
	case int:
		return v != 0
	case int32:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	}
	return false
}

func attentionEventHasVisibilityOverride(event AttentionEvent) bool {
	return attentionBoolDetail(event.Details, attentionDetailPinned) ||
		attentionStringDetail(event.Details, attentionDetailOverridePriority) != ""
}

func attentionEventHiddenReason(event AttentionEvent) string {
	if attentionEventHasVisibilityOverride(event) {
		return ""
	}
	return attentionStringDetail(event.Details, attentionDetailHiddenReason)
}

func filterVisibleAttentionEvents(events []AttentionEvent) []AttentionEvent {
	filtered := make([]AttentionEvent, 0, len(events))
	for _, event := range events {
		if attentionEventHiddenReason(event) != "" {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func attentionContextHotActionability(event AttentionEvent) Actionability {
	if attentionFloatDetail(event.Details, "usage_percent") >= attentionContextHotActionThreshold {
		return ActionabilityActionRequired
	}
	return ActionabilityInteresting
}

func attentionAlertActionability(event AttentionEvent) Actionability {
	if event.Severity == SeverityDebug || event.Severity == SeverityInfo {
		return ActionabilityInteresting
	}
	return ActionabilityActionRequired
}

func attentionEventPaneRef(event AttentionEvent) string {
	if paneRef := attentionPaneRef(event.Details); paneRef != "" {
		return paneRef
	}
	if event.Pane > 0 {
		return strconv.Itoa(event.Pane)
	}
	return ""
}

func maxAttentionActionability(current, required Actionability) Actionability {
	if attentionActionabilityRank(current) >= attentionActionabilityRank(required) {
		return current
	}
	return required
}

func attentionActionabilityRank(level Actionability) int {
	switch level {
	case ActionabilityActionRequired:
		return 2
	case ActionabilityInteresting:
		return 1
	default:
		return 0
	}
}

func maxAttentionSeverity(current, required Severity) Severity {
	if attentionSeverityRank(current) >= attentionSeverityRank(required) {
		return current
	}
	return required
}

func attentionSeverityRank(level Severity) int {
	switch level {
	case SeverityCritical:
		return 4
	case SeverityError:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}

// attentionConflictActions builds NextActions for file conflict events.
// path is currently unused (robot-diff takes only a session) but kept in the
// signature so callers remain ready when per-file diff filtering is added.
func attentionConflictActions(session, path, reason string) []NextAction { //nolint:unparam
	if session == "" {
		return []NextAction{attentionStatusNextAction(reason)}
	}
	return []NextAction{{
		Action: "robot-diff",
		Args:   fmt.Sprintf("--robot-diff=%s", session),
		Reason: reason,
	}}
}

func attentionReservationConflictActions(session string, details map[string]any, reason string) []NextAction {
	actions := []NextAction{attentionStatusNextAction(reason)}
	if agentName := attentionReservationConflictAgent(details); agentName != "" {
		actions = append(actions, NextAction{
			Action: "robot-inspect-coordination",
			Args:   fmt.Sprintf("--robot-inspect-coordination=%s", agentName),
			Reason: "Inspect the agent coordination state behind the reservation conflict",
		})
	}
	if session != "" {
		actions = append(actions, attentionConflictActions(session, attentionStringDetail(details, "path"), "Inspect related session activity")...)
	}
	return actions
}

func attentionMailCheckActions(projectKey, agentName, threadID string, unreadOnly bool, reason string) []NextAction {
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return []NextAction{attentionStatusNextAction(reason)}
	}

	args := []string{
		"--robot-mail-check",
		fmt.Sprintf("--mail-project=%s", attentionCommandArgValue(projectKey)),
	}
	if unreadOnly {
		args = append(args, "--mail-status=unread")
	}
	if agentName = strings.TrimSpace(agentName); agentName != "" {
		args = append(args, fmt.Sprintf("--mail-agent=%s", attentionCommandArgValue(agentName)))
	}
	if threadID = strings.TrimSpace(threadID); threadID != "" {
		args = append(args, fmt.Sprintf("--thread=%s", attentionCommandArgValue(threadID)))
	}

	return []NextAction{{
		Action: "robot-mail-check",
		Args:   strings.Join(args, " "),
		Reason: reason,
	}}
}

func attentionCommandArgValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, " \t\n\"'") {
		return strconv.Quote(value)
	}
	return value
}

func attentionReservationConflictAgent(details map[string]any) string {
	if agentName := attentionStringDetail(details, "requestor_agent"); agentName != "" {
		return agentName
	}
	holders := attentionStringSliceDetail(details, "holders")
	if len(holders) > 0 {
		return holders[0]
	}
	return ""
}

func attentionConflictPath(event AttentionEvent) string {
	for _, key := range []string{"path", "file", "pattern", "path_pattern"} {
		if value := attentionStringDetail(event.Details, key); value != "" {
			return value
		}
	}
	return ""
}

func compactStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		label := strings.TrimSpace(value)
		if label != "" {
			out = append(out, label)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func attentionStatusNextAction(reason string) NextAction {
	return NextAction{
		Action: "robot-status",
		Args:   "--robot-status",
		Reason: reason,
	}
}

func actuationNextActions(session string, targets []string, reason string) []NextAction {
	if paneRef := actuationPaneRef(targets); paneRef != "" {
		return attentionTailOrStatusActions(session, paneRef, reason)
	}
	return []NextAction{attentionStatusNextAction(reason)}
}

func actuationPaneRef(targets []string) string {
	if len(targets) != 1 {
		return ""
	}
	return strings.TrimSpace(targets[0])
}

func actuationTargetRef(session string, targets []string) string {
	session = strings.TrimSpace(session)
	if session == "" {
		return ""
	}
	if paneRef := actuationPaneRef(targets); paneRef != "" {
		return fmt.Sprintf("session:%s:%s", session, paneRef)
	}
	if len(targets) > 0 {
		return fmt.Sprintf("session:%s:multi", session)
	}
	return fmt.Sprintf("session:%s", session)
}

func actuationTargetRefs(session string, targets []string) []string {
	if strings.TrimSpace(session) == "" || len(targets) == 0 {
		return nil
	}
	refs := make([]string, 0, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		refs = append(refs, fmt.Sprintf("session:%s:%s", session, target))
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

func cloneAnyMapSlice(values []map[string]any) []map[string]any {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]map[string]any, 0, len(values))
	for _, value := range values {
		cloned = append(cloned, cloneAnyMap(value))
	}
	return cloned
}

func attentionContextActions(session, reason string) []NextAction {
	if session == "" {
		return []NextAction{attentionStatusNextAction(reason)}
	}
	return []NextAction{{
		Action: "robot-context",
		Args:   fmt.Sprintf("--robot-context=%s", session),
		Reason: reason,
	}}
}

func attentionTailOrStatusActions(session, paneRef, reason string) []NextAction {
	if session == "" || paneRef == "" {
		return []NextAction{attentionStatusNextAction(reason)}
	}
	return []NextAction{{
		Action: "robot-tail",
		Args:   fmt.Sprintf("--robot-tail=%s --panes=%s --lines=50", session, paneRef),
		Reason: reason,
	}}
}

func attentionBeadActions(beadID, reason string) []NextAction {
	if beadID == "" {
		return nil
	}
	return []NextAction{{
		Action: "robot-bead-show",
		Args:   fmt.Sprintf("--robot-bead-show=%s", beadID),
		Reason: reason,
	}}
}

func attentionBeadID(details map[string]any) string {
	if details == nil {
		return ""
	}
	for _, key := range []string{"bead_id", "bead", "id"} {
		if value, ok := details[key]; ok {
			if label := strings.TrimSpace(fmt.Sprint(value)); label != "" && label != "<nil>" {
				return label
			}
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// NOTE: buildSnapshotAttentionSummary is defined in robot.go (br-slg9g)

// NOTE: EventsOptions, filterEventsForRobot, and toStringSetForEvents are
// defined in events.go (br-kpvhy: --robot-events command implementation)

// =============================================================================
// Store-backed Operations (bd-j9jo3.4.1)
// =============================================================================

// syncCursorFromStore initializes the cursor allocator from durable storage.
func (f *AttentionFeed) syncCursorFromStore() {
	if f.store == nil {
		return
	}

	latestCursor, err := f.store.GetLatestEventCursor()
	if err == nil && latestCursor > 0 {
		f.cursor.counter.Store(latestCursor)
	}
}

// replayFromStore reads events from SQLite with cursor expiration handling.
func (f *AttentionFeed) replayFromStore(sinceCursor int64, limit int) ([]AttentionEvent, int64, error) {
	window, err := f.store.GetAttentionReplayWindow()
	if err != nil {
		// Fallback to in-memory journal
		events, newestCursor, replayErr := f.journal.Replay(sinceCursor, limit)
		if replayErr != nil {
			return nil, 0, replayErr
		}
		return f.hydrateReplayEvents(events), newestCursor, nil
	}

	// Handle cursor=-1 (start from now)
	if sinceCursor == -1 {
		return []AttentionEvent{}, maxCursor(window.NewestCursor, f.cursor.Current()), nil
	}

	if limit <= 0 {
		limit = 100
	}

	// Check cursor expiration
	if window.CursorExpired(sinceCursor) {
		return nil, 0, &CursorExpiredError{
			RequestedCursor: sinceCursor,
			EarliestCursor:  window.OldestCursor,
			RetentionPeriod: f.config.RetentionPeriod,
		}
	}

	// GC expired events periodically
	_, _ = f.store.GCExpiredEvents()

	storedEvents, err := f.store.GetAttentionEventsSince(sinceCursor, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("replay attention events from store: %w", err)
	}
	events := f.hydrateStoredAttentionEvents(storedEvents)

	if tailStart := f.ephemeralFromCursor.Load(); tailStart > 0 && len(events) < limit {
		journalSince := sinceCursor
		if journalSince < tailStart-1 {
			journalSince = tailStart - 1
		}
		tail, journalCursor, err := f.journal.Replay(journalSince, limit-len(events))
		if err != nil {
			return nil, 0, err
		}
		events = append(events, f.hydrateReplayEvents(tail)...)
		return events, maxCursor(window.NewestCursor, journalCursor), nil
	}

	return events, maxCursor(window.NewestCursor, f.cursor.Current()), nil
}

func (f *AttentionFeed) hydrateStoredAttentionEvents(storedEvents []state.StoredAttentionEvent) []AttentionEvent {
	itemStates := map[int64]state.AttentionItemState{}
	if len(storedEvents) > 0 {
		cursors := make([]int64, 0, len(storedEvents))
		for _, stored := range storedEvents {
			cursors = append(cursors, stored.Cursor)
		}
		if loaded, err := f.store.GetAttentionItemStatesForCursors(cursors); err == nil {
			itemStates = loaded
		}
	}

	events := make([]AttentionEvent, 0, len(storedEvents))
	for _, stored := range storedEvents {
		event := attentionEventFromStored(stored)
		itemKey := attentionItemKeyForStoredEvent(stored)
		if itemState, ok := itemStates[stored.Cursor]; ok {
			itemStateCopy := itemState
			event = annotateAttentionOperatorState(event, itemKey, &itemStateCopy)
		} else {
			event = annotateAttentionOperatorState(event, itemKey, nil)
		}
		events = append(events, event)
	}
	return events
}

func (f *AttentionFeed) hydrateReplayEvents(events []AttentionEvent) []AttentionEvent {
	if f.store == nil || len(events) == 0 {
		return events
	}

	cursors := make([]int64, 0, len(events))
	for _, event := range events {
		if event.Cursor > 0 {
			cursors = append(cursors, event.Cursor)
		}
	}

	itemStates := map[int64]state.AttentionItemState{}
	if len(cursors) > 0 {
		if loaded, err := f.store.GetAttentionItemStatesForCursors(cursors); err == nil {
			itemStates = loaded
		}
	}

	hydrated := make([]AttentionEvent, 0, len(events))
	for _, event := range events {
		itemKey := attentionItemKeyForReplayEvent(event)
		if itemState, ok := itemStates[event.Cursor]; ok {
			itemStateCopy := itemState
			event = annotateAttentionOperatorState(event, itemKey, &itemStateCopy)
		} else {
			event = annotateAttentionOperatorState(event, itemKey, nil)
		}
		hydrated = append(hydrated, event)
	}
	return hydrated
}

func mergeStoredAttentionEvents(groups ...[]state.StoredAttentionEvent) []state.StoredAttentionEvent {
	byCursor := make(map[int64]state.StoredAttentionEvent)
	for _, group := range groups {
		for _, event := range group {
			byCursor[event.Cursor] = event
		}
	}
	cursors := make([]int64, 0, len(byCursor))
	for cursor := range byCursor {
		cursors = append(cursors, cursor)
	}
	sort.Slice(cursors, func(i, j int) bool {
		return cursors[i] < cursors[j]
	})
	merged := make([]state.StoredAttentionEvent, 0, len(cursors))
	for _, cursor := range cursors {
		merged = append(merged, byCursor[cursor])
	}
	return merged
}

func filterStoredAttentionEventsByTimeRange(events []state.StoredAttentionEvent, start, end time.Time) []state.StoredAttentionEvent {
	start = start.UTC()
	end = end.UTC()
	filtered := make([]state.StoredAttentionEvent, 0, len(events))
	for _, event := range events {
		ts := event.Ts.UTC()
		if ts.Before(start) || ts.After(end) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func maxStoredAttentionCursor(events []state.StoredAttentionEvent, fallback int64) int64 {
	if len(events) == 0 {
		return fallback
	}
	return maxCursor(events[len(events)-1].Cursor, fallback)
}

// storeBackedStats returns journal stats from durable storage.
func (f *AttentionFeed) storeBackedStats() (JournalStats, error) {
	window, err := f.store.GetAttentionReplayWindow()
	if err != nil {
		return JournalStats{}, err
	}

	latestCursor := window.NewestCursor
	if latestCursor > f.cursor.Current() {
		f.cursor.counter.Store(latestCursor)
	}

	stats := f.journal.Stats()
	stats.Size = f.config.JournalSize
	stats.Count = window.EventCount
	stats.OldestCursor = window.OldestCursor
	stats.NewestCursor = maxCursor(latestCursor, f.cursor.Current())
	if window.LastEventAt != nil && (stats.LastEventTime == nil || window.LastEventAt.After(*stats.LastEventTime)) {
		ts := window.LastEventAt.UTC()
		stats.LastEventTime = &ts
	}
	stats.RetentionPeriod = f.config.RetentionPeriod

	if tailStart := f.ephemeralFromCursor.Load(); tailStart > 0 {
		tailCount, tailOldest, tailNewest := f.journal.RangeStats(tailStart)
		stats.Count += tailCount
		if stats.OldestCursor == 0 && tailCount > 0 {
			stats.OldestCursor = tailOldest
		}
		stats.NewestCursor = maxCursor(stats.NewestCursor, tailNewest)
	}

	return stats, nil
}

func maxCursor(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// attentionEventToStored converts an AttentionEvent to StoredAttentionEvent for persistence.
func attentionEventToStored(event AttentionEvent, retention time.Duration) state.StoredAttentionEvent {
	ts := parseAttentionEventTime(event.Ts)
	var expiresAt *time.Time
	if retention > 0 {
		expiry := ts.Add(retention)
		expiresAt = &expiry
	}

	var details string
	if len(event.Details) > 0 {
		if raw, err := json.Marshal(event.Details); err == nil {
			details = string(raw)
		}
	}

	var nextActions string
	if len(event.NextActions) > 0 {
		if raw, err := json.Marshal(event.NextActions); err == nil {
			nextActions = string(raw)
		}
	}

	stored := state.StoredAttentionEvent{
		Ts:            ts,
		SessionName:   event.Session,
		Category:      string(event.Category),
		EventType:     string(event.Type),
		Source:        event.Source,
		Actionability: state.Actionability(event.Actionability),
		Severity:      state.Severity(event.Severity),
		ReasonCode:    event.ReasonCode,
		Summary:       event.Summary,
		Details:       details,
		NextActions:   nextActions,
		DedupKey:      event.DedupKey,
		DedupCount:    1,
		ExpiresAt:     expiresAt,
	}
	if event.Pane > 0 {
		stored.Pane = strconv.Itoa(event.Pane)
	}
	return stored
}

// attentionEventFromStored converts a StoredAttentionEvent back to AttentionEvent.
func attentionEventFromStored(event state.StoredAttentionEvent) AttentionEvent {
	result := AttentionEvent{
		Cursor:        event.Cursor,
		Ts:            event.Ts.UTC().Format(time.RFC3339Nano),
		Session:       event.SessionName,
		Category:      EventCategory(event.Category),
		Type:          EventType(event.EventType),
		Source:        event.Source,
		Actionability: Actionability(event.Actionability),
		Severity:      Severity(event.Severity),
		ReasonCode:    event.ReasonCode,
		Summary:       event.Summary,
		NextActions:   []NextAction{},
		DedupKey:      event.DedupKey,
	}
	if event.Pane != "" {
		if pane, err := strconv.Atoi(event.Pane); err == nil {
			result.Pane = pane
		}
	}
	if event.Details != "" {
		var details map[string]any
		if err := json.Unmarshal([]byte(event.Details), &details); err == nil {
			result.Details = details
		}
	}
	if event.NextActions != "" {
		var actions []NextAction
		if err := json.Unmarshal([]byte(event.NextActions), &actions); err == nil {
			result.NextActions = sanitizeNextActions(actions)
		}
	}
	return result
}

// parseAttentionEventTime parses the timestamp from an attention event.
func parseAttentionEventTime(ts string) time.Time {
	if ts == "" {
		return time.Now().UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}
