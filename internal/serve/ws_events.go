// Package serve provides WebSocket event persistence, resume, and backpressure handling.
package serve

import (
	"container/ring"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// WSEventStore provides event persistence and replay for WebSocket connections.
// It maintains an in-memory ring buffer for fast access and persists to SQLite
// for durability across restarts.
type WSEventStore struct {
	db            *sql.DB
	buffer        *ring.Ring
	bufferMu      sync.RWMutex
	bufferSize    int
	retentionSecs int64 // How long to keep events in SQLite
	seq           int64
	seqMu         sync.Mutex
	storeMu       sync.Mutex // serializes durable append and ring publication
	cleanupTicker *time.Ticker
	done          chan struct{}
	stopOnce      sync.Once
	cleanupWG     sync.WaitGroup
}

// WSStoredEvent is an event stored in the ring buffer and database.
type WSStoredEvent struct {
	Seq       int64     `json:"seq"`
	Topic     string    `json:"topic"`
	EventType string    `json:"event_type"`
	Data      string    `json:"data"` // JSON-encoded
	CreatedAt time.Time `json:"created_at"`
}

// WSDroppedInfo tracks dropped events for a client.
type WSDroppedInfo struct {
	Topic           string `json:"topic"`
	ClientID        string `json:"client_id"`
	DroppedCount    int    `json:"dropped_count"`
	FirstDroppedSeq int64  `json:"first_dropped_seq,omitempty"`
	LastDroppedSeq  int64  `json:"last_dropped_seq,omitempty"`
	Reason          string `json:"reason"`
}

// WSSubscriptionOptions configures client subscription behavior.
type WSSubscriptionOptions struct {
	Since          int64  `json:"since,omitempty"`             // Cursor: replay events after this seq
	ThrottleMS     int    `json:"throttle_ms,omitempty"`       // Min ms between messages
	MaxLinesPerMsg int    `json:"max_lines_per_msg,omitempty"` // Max output lines per message
	Mode           string `json:"mode,omitempty"`              // "lines" or "raw"
}

// WSEventStoreConfig configures the event store.
type WSEventStoreConfig struct {
	BufferSize       int           // Number of events in ring buffer (default: 10000)
	RetentionSeconds int64         // How long to keep events in SQLite (default: 3600 = 1 hour)
	CleanupInterval  time.Duration // How often to run cleanup (default: 5 minutes)
}

// DefaultWSEventStoreConfig returns sensible defaults.
func DefaultWSEventStoreConfig() WSEventStoreConfig {
	return WSEventStoreConfig{
		BufferSize:       10000,
		RetentionSeconds: 3600,
		CleanupInterval:  5 * time.Minute,
	}
}

// NewWSEventStore creates a new event store.
// If db is nil, operates in memory-only mode (no persistence).
func NewWSEventStore(db *sql.DB, cfg WSEventStoreConfig) *WSEventStore {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 10000
	}
	if cfg.RetentionSeconds <= 0 {
		cfg.RetentionSeconds = 3600
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}

	store := &WSEventStore{
		db:            db,
		buffer:        ring.New(cfg.BufferSize),
		bufferSize:    cfg.BufferSize,
		retentionSecs: cfg.RetentionSeconds,
		done:          make(chan struct{}),
	}

	// AUTOINCREMENT retains the high-water mark even after retention removes
	// every event. MAX(seq) alone would rewind cursors on an idle restart.
	if db != nil {
		err := db.QueryRow("SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'ws_events'), 0)").Scan(&store.seq)
		if err == nil {
			log.Printf("ws_events: initialized seq from db seq=%d", store.seq)
		} else {
			log.Printf("ws_events: read sequence high-water mark: %v", err)
		}
	}

	// Start cleanup goroutine if we have a database
	if db != nil {
		store.cleanupTicker = time.NewTicker(cfg.CleanupInterval)
		store.cleanupWG.Add(1)
		go store.cleanupLoop()
	}

	return store
}

// Stop stops the event store's background goroutines.
func (s *WSEventStore) Stop() {
	s.stopOnce.Do(func() {
		close(s.done)
		if s.cleanupTicker != nil {
			s.cleanupTicker.Stop()
		}
	})
	s.cleanupWG.Wait()
}

// cleanupLoop periodically removes old events from the database.
func (s *WSEventStore) cleanupLoop() {
	defer s.cleanupWG.Done()
	for {
		select {
		case <-s.done:
			return
		case <-s.cleanupTicker.C:
			if err := s.cleanup(); err != nil {
				log.Printf("ws_events: cleanup error: %v", err)
			}
		}
	}
}

// cleanup removes events older than the retention period.
func (s *WSEventStore) cleanup() error {
	if s.db == nil {
		return nil
	}

	cutoff := time.Now().Add(-time.Duration(s.retentionSecs) * time.Second)
	result, err := s.db.Exec(
		"DELETE FROM ws_events WHERE created_at < ?",
		cutoff,
	)
	if err != nil {
		return fmt.Errorf("delete old events: %w", err)
	}

	affected, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("retention cleanup rows affected: %w", rowsErr)
	}
	if affected > 0 {
		log.Printf("ws_events: retention cleanup removed=%d cutoff=%s", affected, cutoff.Format(time.RFC3339))
	}

	// Also cleanup old dropped event records (keep last 24 hours)
	dropCutoff := time.Now().Add(-24 * time.Hour)
	_, _ = s.db.Exec("DELETE FROM ws_dropped_events WHERE created_at < ?", dropCutoff)

	return nil
}

// nextSeq returns the next sequence number.
func (s *WSEventStore) nextSeq() int64 {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.seq++
	return s.seq
}

// CurrentSeq returns the current sequence number (highest assigned).
func (s *WSEventStore) CurrentSeq() int64 {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return s.seq
}

// Store stores an event in both the ring buffer and database.
func (s *WSEventStore) Store(topic, eventType string, data interface{}) (*WSStoredEvent, error) {
	// Marshal data to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal event data: %w", err)
	}
	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	event := &WSStoredEvent{
		Topic:     topic,
		EventType: eventType,
		Data:      string(jsonData),
		CreatedAt: time.Now(),
	}

	// Let SQLite allocate durable cursors. Explicit locally allocated seqs can
	// collide with another store, and reuse pruned IDs after a restart. Publish
	// only after persistence succeeds; a memory-only success here would claim a
	// resumable cursor for an event that disappears on restart.
	if s.db != nil {
		result, err := s.db.Exec(
			"INSERT INTO ws_events (topic, event_type, data, created_at) VALUES (?, ?, ?, ?)",
			event.Topic, event.EventType, event.Data, event.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("persist event: %w", err)
		}
		event.Seq, err = result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("read persisted event sequence: %w", err)
		}
		s.seqMu.Lock()
		if event.Seq > s.seq {
			s.seq = event.Seq
		}
		s.seqMu.Unlock()
	} else {
		event.Seq = s.nextSeq()
	}

	// Keep append order identical to cursor order for memory-only replay too.
	s.bufferMu.Lock()
	s.buffer.Value = event
	s.buffer = s.buffer.Next()
	s.bufferMu.Unlock()

	return event, nil
}

// GetSince retrieves events after the given sequence number.
// The database is authoritative when configured: a ring can outlive retention
// or miss events written by another store sharing the same runtime database.
// Returns events and a boolean indicating if a cursor reset is needed.
func (s *WSEventStore) GetSince(since int64, topic string, limit int) ([]WSStoredEvent, bool, error) {
	var topics []string
	if topic != "" {
		topics = []string{topic}
	}
	return s.getSinceTopics(since, topics, limit)
}

// getSinceTopics applies every subscription pattern before the result limit.
// An empty list selects all topics, as GetSince's empty topic has always done.
func (s *WSEventStore) getSinceTopics(since int64, topics []string, limit int) ([]WSStoredEvent, bool, error) {
	if limit <= 0 {
		limit = 1000
	}
	if s.db != nil {
		return s.getFromDB(since, topics, limit)
	}

	// Try ring buffer first
	events, found := s.getFromBuffer(since, topics, limit)
	if found {
		return events, false, nil
	}

	return nil, true, nil
}

// getFromBuffer retrieves events from the ring buffer.
// Returns events and whether the since cursor was found in the buffer.
func (s *WSEventStore) getFromBuffer(since int64, topics []string, limit int) ([]WSStoredEvent, bool) {
	s.bufferMu.RLock()
	defer s.bufferMu.RUnlock()

	// ring.Len() walks the whole ring (O(n)), so hoist it out of the loop
	// conditions: with the default 10000-slot buffer, re-evaluating it per
	// iteration made these scans O(n^2) and stalled the hub loop for seconds.
	ringSize := s.buffer.Len()

	// Find the oldest event in the buffer to check if since is still valid
	var oldestSeq int64 = -1
	if ev, ok := s.buffer.Value.(*WSStoredEvent); ok && ev != nil {
		// Buffer is full and has wrapped, current pointer holds the oldest element
		oldestSeq = ev.Seq
	} else {
		// Buffer hasn't wrapped. The oldest element is the very first one inserted.
		curr := s.buffer
		for i := 0; i < ringSize; i++ {
			if ev, ok := curr.Value.(*WSStoredEvent); ok && ev != nil {
				if oldestSeq == -1 || ev.Seq < oldestSeq {
					oldestSeq = ev.Seq
				}
			}
			curr = curr.Next()
		}
	}

	// If since is older than our oldest buffered event, buffer can't satisfy
	if oldestSeq == -1 || since < oldestSeq-1 {
		return nil, false
	}

	// Collect matching events
	var events []WSStoredEvent
	curr := s.buffer
	for i := 0; i < ringSize; i++ {
		if ev, ok := curr.Value.(*WSStoredEvent); ok && ev != nil {
			if ev.Seq > since && (len(topics) == 0 || matchAnyTopic(topics, ev.Topic)) {
				events = append(events, *ev)
				if len(events) >= limit {
					break
				}
			}
		}
		curr = curr.Next()
	}

	return events, true
}

// getFromDB retrieves events from the database.
// Returns events and whether a reset is needed (cursor too old for DB too).
func (s *WSEventStore) getFromDB(since int64, topics []string, limit int) ([]WSStoredEvent, bool, error) {
	// Keep retention validation and selection on one snapshot. Otherwise a
	// concurrent cleanup can erase the requested history between the two reads
	// and turn an expired cursor into a successful empty replay.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("begin replay: %w", err)
	}
	defer tx.Rollback()
	var minSeq sql.NullInt64
	var highWater int64
	if err := tx.QueryRow(`SELECT MIN(seq),
		COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'ws_events'), 0)
		FROM ws_events`).Scan(&minSeq, &highWater); err != nil {
		return nil, false, fmt.Errorf("query replay window: %w", err)
	}
	s.seqMu.Lock()
	if highWater > s.seq {
		s.seq = highWater
	}
	s.seqMu.Unlock()
	if since > 0 && (since > highWater || (minSeq.Valid && since < minSeq.Int64-1) || (!minSeq.Valid && since < highWater)) {
		return nil, true, nil
	}

	query := "SELECT seq, topic, event_type, data, created_at FROM ws_events WHERE seq > ?"
	args := []interface{}{since}
	predicates := make([]string, 0, len(topics))
	matchAll := len(topics) == 0
	for _, topic := range topics {
		if topic == "*" {
			matchAll = true
			break
		}
		if strings.HasSuffix(topic, ":*") {
			// Keep matchTopic's literal, case-sensitive prefix semantics.
			// SQL LIKE/GLOB would reinterpret characters in valid topics.
			prefix := strings.TrimSuffix(topic, "*")
			predicates = append(predicates, "substr(topic, 1, length(?)) = ?")
			args = append(args, prefix, prefix)
		} else {
			predicates = append(predicates, "topic = ?")
			args = append(args, topic)
		}
	}
	if matchAll {
		args = args[:1]
	} else {
		query += " AND (" + strings.Join(predicates, " OR ") + ")"
	}
	query += " ORDER BY seq LIMIT ?"
	args = append(args, limit)
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("query events: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Printf("ws_events: rows close error: %v", closeErr)
		}
	}()

	var events []WSStoredEvent
	for rows.Next() {
		var ev WSStoredEvent
		if err := rows.Scan(&ev.Seq, &ev.Topic, &ev.EventType, &ev.Data, &ev.CreatedAt); err != nil {
			return nil, false, fmt.Errorf("scan event: %w", err)
		}

		events = append(events, ev)
		if len(events) >= limit {
			break
		}
	}

	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate events: %w", err)
	}

	return events, false, nil
}

// RecordDropped records dropped events for a client.
func (s *WSEventStore) RecordDropped(clientID, topic, reason string, firstSeq, lastSeq int64) error {
	if s.db == nil {
		return nil
	}

	count := lastSeq - firstSeq + 1
	if count < 1 {
		count = 1
	}

	_, err := s.db.Exec(`
		INSERT INTO ws_dropped_events (topic, client_id, dropped_count, first_dropped_seq, last_dropped_seq, reason)
		VALUES (?, ?, ?, ?, ?, ?)`,
		topic, clientID, count, firstSeq, lastSeq, reason,
	)
	if err != nil {
		return fmt.Errorf("record dropped: %w", err)
	}

	log.Printf("ws_events: dropped events client=%s topic=%s count=%d reason=%s", clientID, topic, count, reason)
	return nil
}

// BufferStats returns statistics about the ring buffer.
func (s *WSEventStore) BufferStats() (size int, used int, oldestSeq int64, newestSeq int64) {
	s.bufferMu.RLock()
	defer s.bufferMu.RUnlock()

	size = s.bufferSize
	oldestSeq = -1
	newestSeq = -1

	s.buffer.Do(func(v interface{}) {
		if ev, ok := v.(*WSStoredEvent); ok && ev != nil {
			used++
			if oldestSeq == -1 || ev.Seq < oldestSeq {
				oldestSeq = ev.Seq
			}
			if newestSeq == -1 || ev.Seq > newestSeq {
				newestSeq = ev.Seq
			}
		}
	})

	return
}

// ============================================================================
// Hub integration: persistence, replay, and dropped-event accounting
// ============================================================================
//
// REPLAY CONTRACT (live server behavior, wired through WSHub):
//
//   - Every `event` frame carries `seq`, a server-assigned, strictly increasing
//     cursor. When a WSEventStore is attached to the hub (Server wires it from
//     the runtime state store's ws_events table, migration 006_ws_events.sql),
//     the persisted store seq is authoritative and survives server restarts.
//     Without a store the hub falls back to an ephemeral in-process counter.
//   - A client resumes after a disconnect by sending a subscribe message with
//     `data.since = <last seq it processed>` alongside `data.topics`. The
//     server replays persisted events with seq > since that match the
//     requested (non-attention) topics, in ascending seq order, each marked
//     with an additive `"replay": true` field on the standard event frame.
//   - Replay and the transition to live delivery are serialized through the
//     hub's Run loop: the subscription is committed and the replay snapshot is
//     taken in the same loop iteration, so the client sees the gap exactly
//     once — no duplicates, no missing events — followed by live frames.
//   - The subscribe ack is queued after all replay frames; its `data.replay`
//     object ({since, replayed, reset, cursor}) reports the outcome, and its
//     arrival marks the end of replay.
//   - If the cursor is older than retention (or no history is available), the
//     server sends a `stream.reset` frame carrying `current_seq` and
//     `oldest_available` instead of replaying; the client should treat its
//     state as fresh and re-sync out of band.
//   - A replay must fit both the bounded history batch and the client's send
//     queue, including space for the subscribe ack. Larger backlogs produce
//     an explicit reset instead of a successful but incomplete replay.
//   - Events dropped to a slow client are recorded in ws_dropped_events
//     (coalesced per topic into seq ranges, reason "slow_client"); once the
//     client drains and delivery succeeds again, it receives a
//     `pane.output.dropped` frame describing the missed range so it can
//     re-request it via `since`.
//   - Attention topics ("attention", "attention:*") keep their own durable
//     cursor protocol (`since_cursor`) and are not part of this contract.
//
// The publication hot path is protected by the hub's bounded broadcast
// channel: WSHub.Publish never blocks (it drops on overflow), and the
// synchronous SQLite insert in Store() runs on the hub's Run goroutine.

// wsReplayLimit bounds how many persisted events a single subscribe-with-since
// request will replay. Matches the GetSince default page size.
const wsReplayLimit = 1000

// wsReplayRequest asks the hub loop to commit a subscription and replay
// persisted events past a cursor before live delivery begins.
type wsReplayRequest struct {
	client   *WSClient
	topics   []string
	since    int64
	complete func(wsReplayResult) bool
	resp     chan wsReplayResult
}

// wsReplayResult reports the outcome of a replay request.
type wsReplayResult struct {
	replayed int
	reset    bool
	cursor   int64
}

// wsDropRange coalesces consecutive dropped events for one client+topic.
type wsDropRange struct {
	first int64
	last  int64
	count int
}

// SetEventStore attaches the persistence/replay store to the hub. Safe to call
// before or while the hub loop is running.
func (h *WSHub) SetEventStore(store *WSEventStore) {
	h.store.Store(store)
}

// eventStore returns the attached event store, or nil when persistence is not
// wired (degraded mode: live streaming only).
func (h *WSHub) eventStore() *WSEventStore {
	return h.store.Load()
}

// currentSeq returns the hub's ephemeral sequence counter (store-less mode).
func (h *WSHub) currentSeq() int64 {
	h.seqMu.Lock()
	defer h.seqMu.Unlock()
	return h.seq
}

// SubscribeWithReplay commits a subscription for topics and replays persisted
// events with seq > since before live delivery, per the replay contract above.
// It blocks until the hub loop has queued replay frames and called complete,
// when provided, to queue the subscribe acknowledgment before live delivery.
func (h *WSHub) SubscribeWithReplay(client *WSClient, topics []string, since int64, complete func(wsReplayResult) bool) wsReplayResult {
	req := &wsReplayRequest{
		client:   client,
		topics:   topics,
		since:    since,
		complete: complete,
		resp:     make(chan wsReplayResult, 1),
	}
	select {
	case h.replayCh <- req:
	case <-h.done:
		// Hub is shutting down; still record the subscription so a late
		// broadcastEvent (if any) sees consistent state.
		client.Subscribe(topics)
		return wsReplayResult{}
	}
	select {
	case res := <-req.resp:
		return res
	case <-h.done:
		return wsReplayResult{}
	}
}

// handleReplay runs on the hub's Run goroutine. Committing the subscription
// and snapshotting history in the same loop iteration is what makes the
// replay→live handoff gap-free and duplicate-free: every event processed by
// the loop after this call is delivered live, and everything stored before it
// is visible to GetSince here.
func (h *WSHub) handleReplay(req *wsReplayRequest) {
	req.client.Subscribe(req.topics)

	res := wsReplayResult{}
	defer func() {
		// Keep the replay's completion acknowledgment in this same hub turn.
		// Otherwise a live event could consume its reserved queue slot after
		// we respond to the read loop but before that loop queues the ack.
		if req.complete != nil && !req.complete(res) {
			res.reset = true
			h.trySendReset(req.client, "", "replay_overflow", res.cursor, 0)
		}
		req.resp <- res
	}()
	store := h.eventStore()
	if store == nil {
		// No persistence wired: replay is unavailable. Tell the client to
		// treat its state as fresh.
		res.reset = true
		res.cursor = h.currentSeq()
		h.trySendReset(req.client, "", "replay_unavailable", res.cursor, 0)
		return
	}
	res.cursor = store.CurrentSeq()
	if _, pending := h.pendingResets[req.client]; pending {
		h.flushClientReset(req.client)
		res.reset = true
		return
	}

	topicFilter := ""
	if len(req.topics) == 1 {
		topicFilter = req.topics[0]
	}
	// Filter before limiting, and request one extra matching event so a
	// bounded prefix can never masquerade as a complete reconnect history.
	events, needsReset, err := store.getSinceTopics(req.since, req.topics, wsReplayLimit+1)
	res.cursor = store.CurrentSeq()
	if err != nil {
		log.Printf("ws_events: replay query failed client=%s since=%d: %v", req.client.id, req.since, err)
		res.reset = true
		h.trySendReset(req.client, topicFilter, "replay_failed", res.cursor, 0)
		return
	}
	if needsReset {
		_, _, oldestAvail, _ := store.BufferStats()
		res.reset = true
		h.trySendReset(req.client, topicFilter, "cursor_expired", res.cursor, oldestAvail)
		return
	}

	if len(events) > wsReplayLimit {
		res.reset = true
		h.trySendReset(req.client, topicFilter, "replay_limit_exceeded", res.cursor, 0)
		return
	}
	// The completion callback queues the subscribe ack before this hub turn
	// ends. Keep a slot for it, and avoid a partial replay when a slow client
	// cannot accept the whole batch. Concurrent control frames can still fill
	// the queue, so the send loop below also preserves the reset barrier.
	available := cap(req.client.send) - len(req.client.send) - 1
	if len(events) > available {
		res.reset = true
		h.trySendReset(req.client, topicFilter, "replay_overflow", res.cursor, 0)
		return
	}

	frames := make([][]byte, 0, len(events))
	for _, ev := range events {
		frame := &WSEvent{
			Type:      WSMsgEvent,
			Timestamp: ev.CreatedAt.UTC().Format(time.RFC3339Nano),
			Seq:       ev.Seq,
			Topic:     ev.Topic,
			EventType: ev.EventType,
			Data:      json.RawMessage(ev.Data),
			Replay:    true,
		}
		// History may have been recorded while redaction was disabled, or by
		// an older server. Apply the current disclosure policy without changing
		// the stored cursor, original record or routing identity.
		if err := h.redactEvent(frame); err != nil {
			log.Printf("ws_events: replay withheld by redaction seq=%d", ev.Seq)
			res.reset = true
			h.trySendReset(req.client, "", "redaction_failed", res.cursor, 0)
			return
		}
		data, err := json.Marshal(frame)
		if err != nil {
			log.Printf("ws_events: replay marshal error seq=%d: %v", ev.Seq, err)
			res.reset = true
			h.trySendReset(req.client, topicFilter, "replay_failed", res.cursor, 0)
			return
		}
		frames = append(frames, data)
	}
	for i, data := range frames {
		if !req.client.trySend(data, nil) {
			ev := events[i]
			lastSeq := events[len(events)-1].Seq
			if recErr := store.RecordDropped(req.client.id, ev.Topic, "replay_overflow", ev.Seq, lastSeq); recErr != nil {
				log.Printf("ws_events: record replay overflow client=%s: %v", req.client.id, recErr)
			}
			log.Printf("ws_events: replay overflow client=%s dropped=[%d,%d]", req.client.id, ev.Seq, lastSeq)
			res.reset = true
			h.trySendReset(req.client, topicFilter, "replay_overflow", res.cursor, 0)
			break
		}
		res.replayed++
	}
}

// trySendReset queues a stream.reset frame. If the transport is full, retain a
// barrier: no later event may be delivered before the client learns about the
// gap. In particular, a failed durable append has no sequence to expose it.
func (h *WSHub) trySendReset(client *WSClient, topic, reason string, currentSeq, oldestAvail int64) {
	data, err := json.Marshal(NewStreamReset(topic, reason, currentSeq, oldestAvail))
	if err != nil {
		return
	}
	if client.trySend(data, nil) {
		delete(h.pendingResets, client)
	} else {
		if h.pendingResets == nil {
			h.pendingResets = make(map[*WSClient][]byte)
		}
		h.pendingResets[client] = data
	}
}

func (h *WSHub) flushClientReset(client *WSClient) bool {
	data, pending := h.pendingResets[client]
	if !pending {
		return true
	}
	if !client.trySend(data, nil) {
		return false
	}
	delete(h.pendingResets, client)
	return true
}

// matchAnyTopic reports whether topic matches any of the subscription patterns.
func matchAnyTopic(patterns []string, topic string) bool {
	for _, p := range patterns {
		if matchTopic(p, topic) {
			return true
		}
	}
	return false
}

// noteClientDrop coalesces a dropped event into the client's pending drop
// ranges. Owned by the hub Run goroutine; no locking.
func (h *WSHub) noteClientDrop(client *WSClient, topic string, seq int64) {
	byTopic := h.pendingDrops[client]
	if byTopic == nil {
		byTopic = make(map[string]*wsDropRange)
		h.pendingDrops[client] = byTopic
	}
	r := byTopic[topic]
	if r == nil {
		byTopic[topic] = &wsDropRange{first: seq, last: seq, count: 1}
		return
	}
	if seq < r.first {
		r.first = seq
	}
	if seq > r.last {
		r.last = seq
	}
	r.count++
}

// flushClientDrops persists any pending drop ranges for the client via
// RecordDropped and, when notify is true (delivery to the client succeeded
// again), queues a pane.output.dropped frame per range so the client learns
// what it missed and can re-request via `since`. Owned by the hub Run
// goroutine.
func (h *WSHub) flushClientDrops(client *WSClient, notify bool) {
	byTopic := h.pendingDrops[client]
	if len(byTopic) == 0 {
		return
	}
	delete(h.pendingDrops, client)

	store := h.eventStore()
	for topic, r := range byTopic {
		if store != nil {
			if err := store.RecordDropped(client.id, topic, "slow_client", r.first, r.last); err != nil {
				log.Printf("ws_events: record dropped client=%s topic=%s: %v", client.id, topic, err)
			}
		}
		if notify {
			data, err := json.Marshal(NewPaneOutputDropped(topic, r.count, r.first, r.last, "slow_client"))
			if err != nil {
				continue
			}
			client.trySend(data, nil)
		}
	}
}

// flushAllDrops persists every pending drop range; called when the hub loop
// exits. Owned by the hub Run goroutine.
func (h *WSHub) flushAllDrops() {
	for client := range h.pendingDrops {
		h.flushClientDrops(client, false)
	}
}

// WSStreamReset is sent to clients when their cursor has expired.
type WSStreamReset struct {
	Type        WSMessageType `json:"type"`
	Timestamp   string        `json:"ts"`
	Topic       string        `json:"topic,omitempty"`
	Reason      string        `json:"reason"`
	CurrentSeq  int64         `json:"current_seq"`
	OldestAvail int64         `json:"oldest_available,omitempty"`
}

// NewStreamReset creates a stream reset message.
func NewStreamReset(topic, reason string, currentSeq, oldestAvail int64) *WSStreamReset {
	return &WSStreamReset{
		Type:        "stream.reset",
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		Topic:       topic,
		Reason:      reason,
		CurrentSeq:  currentSeq,
		OldestAvail: oldestAvail,
	}
}

// WSPaneOutputDropped is sent when pane output is dropped due to backpressure.
type WSPaneOutputDropped struct {
	Type         WSMessageType `json:"type"`
	Timestamp    string        `json:"ts"`
	Topic        string        `json:"topic"`
	DroppedCount int           `json:"dropped_count"`
	FirstSeq     int64         `json:"first_seq,omitempty"`
	LastSeq      int64         `json:"last_seq,omitempty"`
	Reason       string        `json:"reason"`
}

// NewPaneOutputDropped creates a pane output dropped message.
func NewPaneOutputDropped(topic string, count int, firstSeq, lastSeq int64, reason string) *WSPaneOutputDropped {
	return &WSPaneOutputDropped{
		Type:         "pane.output.dropped",
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		Topic:        topic,
		DroppedCount: count,
		FirstSeq:     firstSeq,
		LastSeq:      lastSeq,
		Reason:       reason,
	}
}
