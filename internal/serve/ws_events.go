// Package serve provides WebSocket event persistence, resume, and backpressure handling.
package serve

import (
	"container/ring"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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

	// Load highest seq from database if available
	if db != nil {
		var maxSeq sql.NullInt64
		err := db.QueryRow("SELECT MAX(seq) FROM ws_events").Scan(&maxSeq)
		if err == nil && maxSeq.Valid {
			store.seq = maxSeq.Int64
			log.Printf("ws_events: initialized seq from db seq=%d", store.seq)
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

	affected, _ := result.RowsAffected()
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

	event := &WSStoredEvent{
		Seq:       s.nextSeq(),
		Topic:     topic,
		EventType: eventType,
		Data:      string(jsonData),
		CreatedAt: time.Now(),
	}

	// Store in ring buffer
	s.bufferMu.Lock()
	s.buffer.Value = event
	s.buffer = s.buffer.Next()
	s.bufferMu.Unlock()

	// Persist to database
	if s.db != nil {
		_, err = s.db.Exec(
			"INSERT INTO ws_events (seq, topic, event_type, data, created_at) VALUES (?, ?, ?, ?, ?)",
			event.Seq, event.Topic, event.EventType, event.Data, event.CreatedAt,
		)
		if err != nil {
			log.Printf("ws_events: persist error seq=%d: %v", event.Seq, err)
			// Continue even if persist fails - event is in memory
		}
	}

	return event, nil
}

// GetSince retrieves events after the given sequence number.
// First tries the ring buffer, falls back to database if needed.
// Returns events and a boolean indicating if a cursor reset is needed.
func (s *WSEventStore) GetSince(since int64, topic string, limit int) ([]WSStoredEvent, bool, error) {
	if limit <= 0 {
		limit = 1000
	}

	// Try ring buffer first
	events, found := s.getFromBuffer(since, topic, limit)
	if found {
		return events, false, nil
	}

	// Fall back to database
	if s.db == nil {
		// No database - cursor is too old, return reset signal
		return nil, true, nil
	}

	return s.getFromDB(since, topic, limit)
}

// getFromBuffer retrieves events from the ring buffer.
// Returns events and whether the since cursor was found in the buffer.
func (s *WSEventStore) getFromBuffer(since int64, topic string, limit int) ([]WSStoredEvent, bool) {
	s.bufferMu.RLock()
	defer s.bufferMu.RUnlock()

	// Find the oldest event in the buffer to check if since is still valid
	var oldestSeq int64 = -1
	s.buffer.Do(func(v interface{}) {
		if ev, ok := v.(*WSStoredEvent); ok && ev != nil {
			if oldestSeq == -1 || ev.Seq < oldestSeq {
				oldestSeq = ev.Seq
			}
		}
	})

	// If since is older than our oldest buffered event, buffer can't satisfy
	if oldestSeq == -1 || since < oldestSeq-1 {
		return nil, false
	}

	// Collect matching events
	var events []WSStoredEvent
	s.buffer.Do(func(v interface{}) {
		if ev, ok := v.(*WSStoredEvent); ok && ev != nil {
			if ev.Seq > since && (topic == "" || matchTopic(topic, ev.Topic)) {
				if len(events) < limit {
					events = append(events, *ev)
				}
			}
		}
	})

	return events, true
}

// getFromDB retrieves events from the database.
// Returns events and whether a reset is needed (cursor too old for DB too).
func (s *WSEventStore) getFromDB(since int64, topic string, limit int) ([]WSStoredEvent, bool, error) {
	var rows *sql.Rows
	var err error

	if topic == "" {
		rows, err = s.db.Query(
			"SELECT seq, topic, event_type, data, created_at FROM ws_events WHERE seq > ? ORDER BY seq LIMIT ?",
			since, limit,
		)
	} else {
		// For topic filtering, we need to handle wildcards in Go
		rows, err = s.db.Query(
			"SELECT seq, topic, event_type, data, created_at FROM ws_events WHERE seq > ? ORDER BY seq LIMIT ?",
			since, limit*10, // Fetch more to account for filtering
		)
	}
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

		// Apply topic filter
		if topic != "" && !matchTopic(topic, ev.Topic) {
			continue
		}

		events = append(events, ev)
		if len(events) >= limit {
			break
		}
	}

	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate events: %w", err)
	}

	// Check if since cursor was too old (no events and since > 0)
	if len(events) == 0 && since > 0 {
		// Check if there are ANY events in the database
		var minSeq sql.NullInt64
		if err := s.db.QueryRow("SELECT MIN(seq) FROM ws_events").Scan(&minSeq); err != nil && err != sql.ErrNoRows {
			return nil, false, fmt.Errorf("query min seq: %w", err)
		}
		if minSeq.Valid && since < minSeq.Int64-1 {
			// Cursor is too old even for database
			return nil, true, nil
		}
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

// GetDroppedStats gets dropped event statistics for a client.
func (s *WSEventStore) GetDroppedStats(clientID string, since time.Time) ([]WSDroppedInfo, error) {
	if s.db == nil {
		return nil, nil
	}

	rows, err := s.db.Query(`
		SELECT topic, client_id, SUM(dropped_count), MIN(first_dropped_seq), MAX(last_dropped_seq), reason
		FROM ws_dropped_events
		WHERE client_id = ? AND created_at > ?
		GROUP BY topic, reason
		ORDER BY MAX(created_at) DESC`,
		clientID, since,
	)
	if err != nil {
		return nil, fmt.Errorf("query dropped stats: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Printf("ws_events: dropped rows close error: %v", closeErr)
		}
	}()

	var stats []WSDroppedInfo
	for rows.Next() {
		var info WSDroppedInfo
		var firstSeq, lastSeq sql.NullInt64
		if err := rows.Scan(&info.Topic, &info.ClientID, &info.DroppedCount, &firstSeq, &lastSeq, &info.Reason); err != nil {
			return nil, fmt.Errorf("scan dropped info: %w", err)
		}
		if firstSeq.Valid {
			info.FirstDroppedSeq = firstSeq.Int64
		}
		if lastSeq.Valid {
			info.LastDroppedSeq = lastSeq.Int64
		}
		stats = append(stats, info)
	}

	return stats, rows.Err()
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
