// Package robot provides machine-readable output for AI agents.
// attention_feed.go implements the attention feed runtime components.
package robot

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ntmevents "github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

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

	return JournalStats{
		Size:            j.size,
		Count:           len(j.entries),
		OldestCursor:    j.oldest,
		NewestCursor:    j.newest,
		RetentionPeriod: j.retention,
		TotalAppended:   j.totalAppended.Load(),
		TotalEvicted:    j.totalEvicted.Load(),
		ReplayRequests:  j.replayRequests.Load(),
		ExpiredRequests: j.expiredRequests.Load(),
	}
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
type AttentionFeed struct {
	config  AttentionFeedConfig
	cursor  *CursorAllocator
	journal *AttentionJournal

	subMu     sync.RWMutex
	subNextID atomic.Uint64
	subs      map[uint64]subscription

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewAttentionFeed creates a new attention feed service.
func NewAttentionFeed(config AttentionFeedConfig) *AttentionFeed {
	if config.JournalSize == 0 {
		config = DefaultAttentionFeedConfig()
	}

	feed := &AttentionFeed{
		config:  config,
		cursor:  NewCursorAllocator(),
		journal: NewAttentionJournal(config.JournalSize, config.RetentionPeriod),
		subs:    make(map[uint64]subscription),
		stopCh:  make(chan struct{}),
	}

	// Start heartbeat if configured
	if config.HeartbeatInterval > 0 {
		go feed.heartbeatLoop()
	}

	return feed
}

// Append allocates a cursor, stores the event, and notifies subscribers.
// The caller provides an event without a cursor; this method assigns one.
func (f *AttentionFeed) Append(event AttentionEvent) AttentionEvent {
	event.NextActions = sanitizeNextActions(event.NextActions)

	// Allocate cursor
	event.Cursor = f.cursor.Next()

	// Ensure timestamp if not set
	if event.Ts == "" {
		event.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	}

	// Store in journal
	f.journal.Append(event)

	// Notify subscribers
	f.notifySubscribers(event)

	return event
}

// Replay returns events since the given cursor.
// Use cursor=0 to get all available events.
// Use cursor=-1 to get no events and just the current cursor.
func (f *AttentionFeed) Replay(sinceCursor int64, limit int) ([]AttentionEvent, int64, error) {
	return f.journal.Replay(sinceCursor, limit)
}

// CurrentCursor returns the most recently allocated cursor.
func (f *AttentionFeed) CurrentCursor() int64 {
	return f.cursor.Current()
}

// Stats returns journal statistics for observability.
func (f *AttentionFeed) Stats() JournalStats {
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
	if attentionSeverityRank(event.Severity) < attentionSeverityRank(opts.MinSeverity) {
		return false
	}
	if attentionActionabilityRank(event.Actionability) < attentionActionabilityRank(opts.MinActionability) {
		return false
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
		if attentionSeverityRank(left.Severity) != attentionSeverityRank(right.Severity) {
			return attentionSeverityRank(left.Severity) > attentionSeverityRank(right.Severity)
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
	if len(candidate.members) == 0 {
		decision := "surfaced"
		reason := ""
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
			recordAttentionDigestDecision(digest, opts, member, bucket, "surfaced", "", 0)
		default:
			recordAttentionDigestDecision(digest, opts, member, bucket, "suppressed", candidate.item.SuppressionReason, representativeCursor)
		}
	}
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

// PublishMailPending creates and appends a mail_pending signal for unread mail.
func (f *AttentionFeed) PublishMailPending(from, to, subject string, messageID int, threadID string) AttentionEvent {
	event := AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Category:      EventCategoryMail,
		Type:          EventTypeMailReceived,
		Source:        "agent_mail",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       fmt.Sprintf("New mail from %s: %s", from, subject),
		Details: map[string]any{
			"from":       from,
			"to":         to,
			"subject":    subject,
			"message_id": messageID,
			"thread_id":  threadID,
		},
		NextActions: []NextAction{
			{
				Action: "robot-mail-read",
				Args:   fmt.Sprintf("--message-id=%d", messageID),
				Reason: "Read message",
			},
		},
	}
	return f.Append(annotateAttentionSignal(event))
}

// PublishMailAckRequired creates and appends a mail_ack_required signal for messages needing acknowledgment.
func (f *AttentionFeed) PublishMailAckRequired(from, to, subject string, messageID int, threadID string) AttentionEvent {
	event := AttentionEvent{
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Category:      EventCategoryMail,
		Type:          EventTypeMailAckRequired,
		Source:        "agent_mail",
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       fmt.Sprintf("Ack required from %s: %s", from, subject),
		Details: map[string]any{
			"from":         from,
			"to":           to,
			"subject":      subject,
			"message_id":   messageID,
			"thread_id":    threadID,
			"ack_required": true,
		},
		NextActions: []NextAction{
			{
				Action: "robot-mail-ack",
				Args:   fmt.Sprintf("--message-id=%d", messageID),
				Reason: "Acknowledge message",
			},
			{
				Action: "robot-mail-read",
				Args:   fmt.Sprintf("--message-id=%d", messageID),
				Reason: "Read message first",
			},
		},
	}
	return f.Append(annotateAttentionSignal(event))
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
				// Recover from panics in handlers to prevent feed disruption
				_ = recover()
			}()
			h(cloneAttentionEvent(event))
		}()
	}
}

// heartbeatLoop emits periodic heartbeat events.
func (f *AttentionFeed) heartbeatLoop() {
	ticker := time.NewTicker(f.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-f.stopCh:
			return
		case t := <-ticker.C:
			f.Append(AttentionEvent{
				Ts:            t.UTC().Format(time.RFC3339Nano),
				Category:      EventCategorySystem,
				Type:          EventType(DefaultTransportLiveness.HeartbeatType),
				Source:        "attention_feed",
				Actionability: ActionabilityBackground,
				Severity:      SeverityDebug,
				Summary:       "Heartbeat",
				Details: map[string]any{
					"journal_stats": f.journal.Stats(),
				},
			})
		}
	}
}

// =============================================================================
// Event Builder Helpers
// =============================================================================

var supportedAttentionActionNames = map[string]struct{}{
	"robot-attention":  {},
	"robot-bead-show":  {},
	"robot-context":    {},
	"robot-diff":       {},
	"robot-digest":     {},
	"robot-events":     {},
	"robot-graph":      {},
	"robot-plan":       {},
	"robot-snapshot":   {},
	"robot-status":     {},
	"robot-tail":       {},
	"robot-watch-bead": {},
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
		result.NextActions = attentionTailOrStatusActions(event.Session, attentionPaneRef(details), "Inspect the failing agent output")
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
		summary := fmt.Sprintf("rotation completed from %s to %s", e.OldAgentID, e.NewAgentID)
		nextActions := []NextAction{attentionStatusNextAction("Inspect rotated agents")}
		if !e.Success {
			eventType = EventTypeAgentError
			actionability = ActionabilityActionRequired
			severity = SeverityError
			summary = fmt.Sprintf("rotation failed for %s", e.OldAgentID)
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
		nextActions := []NextAction{
			attentionStatusNextAction("Inspect file reservations with --robot-locks"),
			{Action: "robot-locks", Args: fmt.Sprintf("%s --all-agents --json", e.Session), Reason: "View all active file reservations"},
		}
		return attentionFromBusStruct(e.BaseEvent, "event_bus.conflict", details, EventCategoryFile, EventTypeFileConflict, ActionabilityActionRequired, SeverityWarning, summary, nextActions), true
	case ntmevents.FileConflictEvent:
		agents := strings.Join(e.Agents, ", ")
		summary := fmt.Sprintf("file conflict on %s: agents [%s] editing concurrently", e.Path, agents)
		details := map[string]any{
			"path":          e.Path,
			"agents":        e.Agents,
			"conflict_kind": "file",
		}
		nextActions := []NextAction{
			attentionStatusNextAction("Inspect which agents are editing this file"),
			{Action: "robot-diff", Args: e.Session, Reason: "Compare agent outputs for conflict resolution"},
		}
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
				Args:   fmt.Sprintf("--session=%s --file=%s", session, filePath),
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
		NextActions:   attentionConflictActions(conflict.SessionName, path, "Inspect the conflicting reservation state"),
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
var globalFeedOnce sync.Once

// GetAttentionFeed returns the global attention feed instance.
// The feed is lazily initialized with default configuration.
func GetAttentionFeed() *AttentionFeed {
	globalFeedOnce.Do(func() {
		globalFeed = NewAttentionFeed(DefaultAttentionFeedConfig())
	})
	return globalFeed
}

// PeekAttentionFeed returns the global attention feed if it has already been
// initialized. Unlike GetAttentionFeed, it never creates a new feed instance.
func PeekAttentionFeed() *AttentionFeed {
	return globalFeed
}

// SetAttentionFeed sets a custom global feed (for testing).
// Must be called before any calls to GetAttentionFeed.
func SetAttentionFeed(feed *AttentionFeed) {
	globalFeed = feed
}

// NOTE: --robot-events command implementation lives in events.go (br-kpvhy).
// The EventsOptions, EventsResponse, GetEvents, filterEvents, and PrintEvents
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
			event.NextActions = attentionConflictActions(event.Session, attentionConflictPath(event), "Inspect the conflicting reservation state")
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

func attentionConflictActions(session, path, reason string) []NextAction {
	path = strings.TrimSpace(path)
	if session != "" && path != "" && !strings.ContainsAny(path, "*?[") {
		return []NextAction{{
			Action: "robot-diff",
			Args:   fmt.Sprintf("--session=%s --file=%s", session, path),
			Reason: reason,
		}}
	}
	return []NextAction{attentionStatusNextAction(reason)}
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

// NOTE: buildSnapshotAttentionSummary is defined in robot.go (br-slg9g)
