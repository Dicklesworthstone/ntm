package serve

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

type attentionHeartbeatSourceSummary struct {
	healthy         int
	degraded        int
	unavailable     int
	degradedReasons []string
}

// Attention heartbeat intervals control how frequently the attention feed
// polls for updates in different states.
// These serve as defaults; override via [serve.attention] in config.toml.
const (
	// attentionHeartbeatIdleInterval is the poll interval when no events are occurring.
	attentionHeartbeatIdleInterval = 5 * time.Second
	// attentionHeartbeatHighActivityInterval is used during sustained high event rates
	// to reduce CPU overhead from frequent polling.
	attentionHeartbeatHighActivityInterval = 30 * time.Second
	// attentionHeartbeatRecoveryInterval is used when catching up after a gap in data,
	// polling more frequently to quickly reconstruct state.
	attentionHeartbeatRecoveryInterval = time.Second
)

const sseMaxFrameBytes = 10 << 20

// writeSSEEvent protects a complete frame before writing any of its bytes.
// Streaming middleware cannot safely scan arbitrary Write/Flush fragments, and
// mutating the shared feed would corrupt replay for other subscribers. Snapshot
// configuration for each frame so a long-lived stream observes policy changes.
func (s *Server) writeSSEEvent(w io.Writer, eventType string, payload any) (err error) {
	defer func() {
		if err != nil {
			// All errors below are static: marshaler and transport errors can
			// contain source data and must not be copied into server logs.
			log.Printf("SSE: terminating stream: %v", err)
		}
	}()

	if len(eventType) > sseMaxFrameBytes || strings.ContainsAny(eventType, "\r\n\x00") {
		return errors.New("invalid SSE event type")
	}
	s.mu.RLock()
	rcfg := s.redactionCfg
	s.mu.RUnlock()
	cfg := redaction.Config{Mode: redaction.ModeOff}
	if rcfg != nil && rcfg.Enabled {
		cfg = rcfg.Config
	}

	// The event type is routing metadata. Replacing it would silently route
	// the event elsewhere; refuse a sensitive type in either enforcing mode.
	metadataCfg := cfg
	if metadataCfg.Mode == redaction.ModeRedact {
		metadataCfg.Mode = redaction.ModeBlock
	}
	metadata := scanServerText(eventType, metadataCfg)
	if metadata.LimitExceeded {
		return errRedactionInspectionLimit
	}
	if metadata.Blocked {
		return errors.New("sensitive SSE routing metadata")
	}

	data, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return errors.New("failed to encode SSE event")
	}
	frameOverhead := len("event: \ndata: \n\n") + len(eventType)
	if len(data) > sseMaxFrameBytes-frameOverhead {
		return errors.New("SSE event exceeds the frame size limit")
	}
	data, result, redactErr := redactJSONBytes(data, cfg)
	if redactErr != nil {
		return redactErr
	}
	if len(data) > sseMaxFrameBytes-frameOverhead || bytes.ContainsAny(data, "\r\n") {
		return errors.New("unsafe SSE event frame")
	}
	if len(metadata.Findings) > 0 || len(result.Findings) > 0 {
		log.Printf("SSE redaction: metadata_findings=%d payload_findings=%d",
			len(metadata.Findings), len(result.Findings))
	}
	// A frame is one write, with the JSON payload and routing label already
	// protected. Preserve caller-controlled replay batching and flush timing.
	n, writeErr := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
	if writeErr != nil || n != frameOverhead+len(data) {
		return errors.New("failed to write SSE event")
	}
	return nil
}

// handleEventStream handles SSE event streaming at /events.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	// Create client channel
	clientCh := make(chan events.BusEvent, 100)
	s.addSSEClient(clientCh)
	defer s.removeSSEClient(clientCh)

	// Get flusher for streaming
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Send initial connection event
	if err := s.writeSSEEvent(w, "connected", map[string]string{
		"status": "connected",
		"time":   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return
	}
	flusher.Flush()

	// Stream events
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-clientCh:
			if err := s.writeSSEEvent(w, event.EventType(), map[string]interface{}{
				"type":      event.EventType(),
				"timestamp": event.EventTimestamp().Format(time.RFC3339),
				"session":   event.EventSession(),
			}); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// addSSEClient adds a client to the SSE broadcast list.
func (s *Server) addSSEClient(ch chan events.BusEvent) {
	s.sseClientsMu.Lock()
	defer s.sseClientsMu.Unlock()
	s.sseClients[ch] = struct{}{}
}

// removeSSEClient removes a client from the SSE broadcast list.
func (s *Server) removeSSEClient(ch chan events.BusEvent) {
	s.sseClientsMu.Lock()
	defer s.sseClientsMu.Unlock()
	delete(s.sseClients, ch)
	close(ch)
}

// broadcastEvent sends an event to all SSE clients.
func (s *Server) broadcastEvent(event events.BusEvent) {
	s.sseClientsMu.RLock()
	defer s.sseClientsMu.RUnlock()

	for ch := range s.sseClients {
		select {
		case ch <- event:
		default:
			// Client buffer full, skip
		}
	}
}

// =============================================================================
// Attention Feed Handlers
// =============================================================================

type attentionStreamFeed interface {
	Stats() robot.JournalStats
	Replay(sinceCursor int64, limit int) ([]robot.AttentionEvent, int64, error)
	Subscribe(robot.AttentionHandler) func()
}

type preparedAttentionStream struct {
	stats          robot.JournalStats
	replayBoundary int64
	replayEvents   []robot.AttentionEvent
	eventCh        chan robot.AttentionEvent
	unsubscribe    func()
	droppedCount   atomic.Uint64
}

func (p *preparedAttentionStream) takeDroppedCount() uint64 {
	return p.droppedCount.Swap(0)
}

func prepareAttentionStream(feed attentionStreamFeed, sinceCursor int64, bufferSize int) (*preparedAttentionStream, error) {
	if feed == nil {
		return nil, errors.New("attention feed unavailable")
	}
	if bufferSize <= 0 {
		bufferSize = 100
	}

	eventCh := make(chan robot.AttentionEvent, bufferSize)
	prepared := &preparedAttentionStream{
		eventCh: eventCh,
	}
	unsubscribe := feed.Subscribe(func(event robot.AttentionEvent) {
		select {
		case eventCh <- event:
		default:
			prepared.droppedCount.Add(1)
		}
	})

	stats := feed.Stats()
	if expired, earliest := attentionCursorExpired(sinceCursor, stats); expired {
		unsubscribe()
		return nil, &robot.CursorExpiredError{
			RequestedCursor: sinceCursor,
			EarliestCursor:  earliest,
			RetentionPeriod: stats.RetentionPeriod,
		}
	}

	prepared.stats = stats
	prepared.replayBoundary = stats.NewestCursor
	prepared.replayEvents = []robot.AttentionEvent{}
	prepared.unsubscribe = unsubscribe
	if sinceCursor < 0 {
		return prepared, nil
	}

	replayLimit := stats.Count
	if replayLimit <= 0 {
		replayLimit = 1
	}
	events, _, err := feed.Replay(sinceCursor, replayLimit)
	if err != nil {
		unsubscribe()
		return nil, err
	}

	prepared.replayEvents = filterAttentionReplayBoundary(events, prepared.replayBoundary)
	return prepared, nil
}

func attentionCursorExpiredPayload(cursorErr *robot.CursorExpiredError, newestCursor int64) map[string]interface{} {
	details := cursorErr.ToDetails()
	return map[string]interface{}{
		"error_code":     robot.ErrCodeCursorExpired,
		"message":        "cursor has expired, resync required",
		"oldest_cursor":  details.EarliestCursor,
		"newest_cursor":  newestCursor,
		"resync_command": details.ResyncCommand,
	}
}

func filterAttentionReplayBoundary(events []robot.AttentionEvent, maxCursor int64) []robot.AttentionEvent {
	if len(events) == 0 || maxCursor <= 0 {
		return []robot.AttentionEvent{}
	}

	filtered := make([]robot.AttentionEvent, 0, len(events))
	for _, event := range events {
		if event.Cursor > maxCursor {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func (s *Server) writeAttentionReplay(
	w io.Writer,
	flusher http.Flusher,
	events []robot.AttentionEvent,
	categoryFilter []string,
	sessionFilter string,
	actionabilityFilter []string,
	replayCursor int64,
) (int64, int, error) {
	delivered := 0
	for _, event := range events {
		if event.Type == robot.EventType(robot.DefaultTransportLiveness.HeartbeatType) ||
			!matchesAttentionFilters(event, categoryFilter, sessionFilter, actionabilityFilter) {
			// Match the live stream path: transport heartbeat events are not emitted as attention items.
			replayCursor = max(replayCursor, event.Cursor)
			continue
		}
		if err := s.writeSSEEvent(w, "attention", event); err != nil {
			return replayCursor, delivered, err
		}
		replayCursor = max(replayCursor, event.Cursor)
		delivered++
	}
	if flusher != nil {
		flusher.Flush()
	}
	return replayCursor, delivered, nil
}

// handleAttentionStreamV1 handles SSE streaming at /api/v1/attention/stream.
// Query params:
//   - since_cursor: replay from this cursor (0 = start from beginning, -1 = from now)
//   - category: filter by event category (comma-separated)
//   - session: filter by session name
//   - actionability: filter by actionability level (comma-separated)
//   - heartbeat: heartbeat interval in seconds (default adaptive, 0 to disable)
func (s *Server) handleAttentionStreamV1(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	sinceCursor := int64(0)
	if sc := r.URL.Query().Get("since_cursor"); sc != "" {
		parsed, err := strconv.ParseInt(sc, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since_cursor: "+err.Error())
			return
		}
		sinceCursor = parsed
	}

	categoryFilter := parseCSVParam(r.URL.Query().Get("category"))
	sessionFilter := r.URL.Query().Get("session")
	actionabilityFilter := parseCSVParam(r.URL.Query().Get("actionability"))

	heartbeatInterval := attentionHeartbeatIdleInterval
	heartbeatOverride := false
	if hb := r.URL.Query().Get("heartbeat"); hb != "" {
		parsed, err := strconv.Atoi(hb)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid heartbeat: "+err.Error())
			return
		}
		heartbeatInterval = time.Duration(parsed) * time.Second
		heartbeatOverride = true
	}

	feed := robot.GetAttentionFeed()
	prepared, err := prepareAttentionStream(feed, sinceCursor, 100)
	if err != nil {
		var cursorErr *robot.CursorExpiredError
		if !errors.As(err, &cursorErr) {
			writeError(w, http.StatusInternalServerError, "attention stream setup failed: "+err.Error())
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}

		cursorExpiredEvent := attentionCursorExpiredPayload(cursorErr, feed.Stats().NewestCursor)
		if err := s.writeSSEEvent(w, "error", cursorExpiredEvent); err != nil {
			return
		}
		flusher.Flush()
		return
	}
	defer prepared.unsubscribe()

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Send connection event
	connEvent := map[string]interface{}{
		"status":        "connected",
		"time":          time.Now().UTC().Format(time.RFC3339),
		"since_cursor":  sinceCursor,
		"oldest_cursor": attentionReplayEarliestCursor(prepared.stats),
		"newest_cursor": prepared.stats.NewestCursor,
	}
	if err := s.writeSSEEvent(w, "connected", connEvent); err != nil {
		return
	}
	flusher.Flush()

	// Stream bookkeeping spans replayed and live-delivered events.
	streamStart := time.Now()
	streamID := fmt.Sprintf("watch_%s", streamStart.UTC().Format("20060102T150405Z"))

	// Replay events from cursor if requested
	replayCursor := prepared.replayBoundary
	replayCursor, eventsSinceStart, err := s.writeAttentionReplay(
		w,
		flusher,
		prepared.replayEvents,
		categoryFilter,
		sessionFilter,
		actionabilityFilter,
		replayCursor,
	)
	if err != nil {
		return
	}

	// Stream events
	ctx := r.Context()
	recoveryMode := sinceCursor > 0 || len(prepared.replayEvents) > 0
	deliveredSinceHeartbeat := 0
	filteredSinceHeartbeat := 0
	var heartbeatTimer *time.Timer
	var heartbeatCh <-chan time.Time
	nextHeartbeatInterval := attentionHeartbeatInterval(
		time.Since(streamStart),
		deliveredSinceHeartbeat,
		recoveryMode,
		attentionHeartbeatSourceSummary{},
		heartbeatInterval,
		heartbeatOverride,
	)
	if nextHeartbeatInterval > 0 {
		heartbeatTimer = time.NewTimer(nextHeartbeatInterval)
		heartbeatCh = heartbeatTimer.C
		defer heartbeatTimer.Stop()
	}
	resetHeartbeatTimer := func(nextInterval time.Duration) {
		if heartbeatTimer == nil {
			return
		}
		if !heartbeatTimer.Stop() {
			select {
			case <-heartbeatTimer.C:
			default:
			}
		}
		heartbeatTimer.Reset(nextInterval)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-prepared.eventCh:
			if event.Cursor <= replayCursor {
				// Already sent during replay
				continue
			}
			if event.Type == robot.EventType(robot.DefaultTransportLiveness.HeartbeatType) {
				// SSE uses its own transport heartbeat framing instead of replay/feed events.
				continue
			}
			if !matchesAttentionFilters(event, categoryFilter, sessionFilter, actionabilityFilter) {
				filteredSinceHeartbeat++
				continue
			}
			if err := s.writeSSEEvent(w, "attention", event); err != nil {
				return
			}
			flusher.Flush()
			replayCursor = event.Cursor
			eventsSinceStart++
			deliveredSinceHeartbeat++
			if heartbeatTimer != nil {
				sourceSummary := s.attentionHeartbeatSourceSummary()
				nextHeartbeatInterval = attentionHeartbeatInterval(
					time.Since(streamStart),
					deliveredSinceHeartbeat,
					recoveryMode,
					sourceSummary,
					heartbeatInterval,
					heartbeatOverride,
				)
				resetHeartbeatTimer(nextHeartbeatInterval)
			}
		case <-heartbeatCh:
			currentStats := feed.Stats()
			sourceSummary := s.attentionHeartbeatSourceSummary()
			nextHeartbeatInterval = attentionHeartbeatInterval(
				time.Since(streamStart),
				deliveredSinceHeartbeat,
				recoveryMode,
				sourceSummary,
				heartbeatInterval,
				heartbeatOverride,
			)
			heartbeat := map[string]interface{}{
				"type":                        "heartbeat",
				"stream_id":                   streamID,
				"time":                        time.Now().UTC().Format(time.RFC3339),
				"oldest_cursor":               attentionReplayEarliestCursor(currentStats),
				"newest_cursor":               currentStats.NewestCursor,
				"cursor_position":             replayCursor,
				"event_count":                 currentStats.Count,
				"subscriber_count":            feed.SubscriberCount(),
				"uptime_ms":                   time.Since(streamStart).Milliseconds(),
				"events_since_start":          eventsSinceStart,
				"next_heartbeat_ms":           nextHeartbeatInterval.Milliseconds(),
				"events_since_last_heartbeat": deliveredSinceHeartbeat,
				"filtered_since_last":         filteredSinceHeartbeat,
				"dropped_since_last":          prepared.takeDroppedCount(),
				"sources_healthy":             sourceSummary.healthy,
				"sources_degraded":            sourceSummary.degraded,
				"sources_unavailable":         sourceSummary.unavailable,
				"degraded_reasons":            sourceSummary.degradedReasons,
				"subscription_active":         true,
			}
			if currentStats.LastEventTime != nil && !currentStats.LastEventTime.IsZero() {
				lastEventTime := currentStats.LastEventTime.UTC()
				heartbeat["last_event_time"] = lastEventTime.Format(time.RFC3339Nano)
				heartbeat["idle_ms"] = time.Since(lastEventTime).Milliseconds()
			}
			if err := s.writeSSEEvent(w, "heartbeat", heartbeat); err != nil {
				return
			}
			flusher.Flush()
			deliveredSinceHeartbeat = 0
			filteredSinceHeartbeat = 0
			recoveryMode = false
			if heartbeatTimer != nil && nextHeartbeatInterval > 0 {
				resetHeartbeatTimer(nextHeartbeatInterval)
			}
		}
	}
}

func attentionHeartbeatInterval(
	streamAge time.Duration,
	deliveredSinceHeartbeat int,
	recoveryMode bool,
	sourceSummary attentionHeartbeatSourceSummary,
	baseInterval time.Duration,
	override bool,
) time.Duration {
	if baseInterval <= 0 {
		return 0
	}
	if override {
		return baseInterval
	}
	if recoveryMode && deliveredSinceHeartbeat == 0 && streamAge < attentionHeartbeatIdleInterval {
		return attentionHeartbeatRecoveryInterval
	}
	if sourceSummary.degraded > 0 {
		return attentionHeartbeatIdleInterval
	}
	if deliveredSinceHeartbeat > 0 {
		return attentionHeartbeatHighActivityInterval
	}
	return attentionHeartbeatIdleInterval
}

func (s *Server) attentionHeartbeatSourceSummary() attentionHeartbeatSourceSummary {
	summary := attentionHeartbeatSourceSummary{
		degradedReasons: []string{},
	}
	if s == nil || s.stateStore == nil {
		return summary
	}
	rows, err := s.stateStore.GetAllSourceHealth()
	if err != nil {
		return summary
	}
	return summarizeAttentionHeartbeatSources(robot.SourceHealthSectionFromRows(rows))
}

func summarizeAttentionHeartbeatSources(section *adapters.SourceHealthSection) attentionHeartbeatSourceSummary {
	summary := attentionHeartbeatSourceSummary{
		degradedReasons: []string{},
	}
	if section == nil {
		return summary
	}
	reasonSet := make(map[string]struct{}, len(section.Degraded))
	for _, source := range section.Sources {
		if source.Fresh {
			summary.healthy++
		}
		if source.Degraded {
			summary.degraded++
		}
		if !source.Available {
			summary.unavailable++
		}
		if !source.Degraded {
			continue
		}
		reason := strings.TrimSpace(string(source.ReasonCode))
		if reason == "" {
			reason = strings.TrimSpace(source.DegradedReason)
		}
		if reason == "" {
			reason = strings.TrimSpace(source.LastError)
		}
		if reason == "" {
			continue
		}
		if _, exists := reasonSet[reason]; exists {
			continue
		}
		reasonSet[reason] = struct{}{}
		summary.degradedReasons = append(summary.degradedReasons, reason)
	}
	sort.Strings(summary.degradedReasons)
	return summary
}

// handleAttentionEventsV1 handles HTTP replay at /api/v1/attention/events.
// Query params:
//   - since_cursor: replay from this cursor (required)
//   - incident_id: bounded replay around a durable incident
//   - as_of: reconstruct bounded attention context at or before an RFC3339 timestamp
//   - window_before_ms/window_after_ms: incident replay context bounds
//   - category: filter by event category (comma-separated)
//   - session: filter by session name
//   - actionability: filter by actionability level (comma-separated)
//   - limit: max events to return (default 100)
func (s *Server) handleAttentionEventsV1(w http.ResponseWriter, r *http.Request) {
	opts := robot.EventsOptions{
		Limit:               100,
		Session:             r.URL.Query().Get("session"),
		CategoryFilter:      parseCSVParam(r.URL.Query().Get("category")),
		ActionabilityFilter: parseCSVParam(r.URL.Query().Get("actionability")),
		IncidentID:          strings.TrimSpace(r.URL.Query().Get("incident_id")),
		Store:               s.stateStore,
		ExplicitStore:       true,
	}

	if sc := r.URL.Query().Get("since_cursor"); sc != "" {
		parsed, err := strconv.ParseInt(sc, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since_cursor: "+err.Error())
			return
		}
		opts.SinceCursor = parsed
	}

	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid limit: "+err.Error())
			return
		}
		if parsed > 0 && parsed < 10000 {
			limit = parsed
		}
	}
	opts.Limit = limit

	if raw := strings.TrimSpace(r.URL.Query().Get("as_of")); raw != "" {
		asOf, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid as_of: "+err.Error())
			return
		}
		opts.AsOf = asOf
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("window_before_ms")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid window_before_ms")
			return
		}
		opts.WindowBefore = time.Duration(parsed) * time.Millisecond
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("window_after_ms")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid window_after_ms")
			return
		}
		opts.WindowAfter = time.Duration(parsed) * time.Millisecond
	}

	output, status := robot.BuildEventsOutput(opts)
	if !output.Success && output.ErrorCode == robot.ErrCodeCursorExpired {
		oldestCursor := int64(0)
		retention := time.Duration(0)
		if output.ReplayWindow != nil {
			oldestCursor = output.ReplayWindow.OldestCursor
			if parsed, err := time.ParseDuration(output.ReplayWindow.RetentionPeriod); err == nil {
				retention = parsed
			}
		}
		writeJSON(w, http.StatusConflict, attentionCursorExpiredPayload(&robot.CursorExpiredError{
			RequestedCursor: opts.SinceCursor,
			EarliestCursor:  oldestCursor,
			RetentionPeriod: retention,
		}, output.NextCursor))
		return
	}
	payload := map[string]interface{}{
		"success":       output.Success,
		"timestamp":     output.Timestamp,
		"version":       output.Version,
		"output_format": output.OutputFormat,
		"events":        output.Events,
		"since_cursor":  opts.SinceCursor,
		"next_cursor":   output.NextCursor,
		"newest_cursor": output.NextCursor,
		"oldest_cursor": int64(0),
		"event_count":   len(output.Events),
		"truncated":     output.HasMore,
	}
	if output.ReplayWindow != nil {
		payload["newest_cursor"] = output.ReplayWindow.LatestCursor
		payload["oldest_cursor"] = output.ReplayWindow.OldestCursor
	}
	if output.ReplayTarget != nil {
		payload["replay_target"] = output.ReplayTarget
	}
	if output.Reconstruction != nil {
		payload["reconstruction"] = output.Reconstruction
	}
	if output.Boundedness != nil {
		payload["boundedness"] = output.Boundedness
	}
	if output.Incident != nil {
		payload["incident"] = output.Incident
	}
	if output.Error != "" {
		payload["error"] = output.Error
	}
	if output.ErrorCode != "" {
		payload["error_code"] = output.ErrorCode
	}
	if output.Hint != "" {
		payload["hint"] = output.Hint
	}
	writeJSON(w, status, payload)
}

// handleAttentionDigestV1 handles digest at /api/v1/attention/digest.
// Query params:
//   - since_cursor: aggregate events from this cursor
//   - category: filter by event category (comma-separated)
//   - session: filter by session name
//   - action_required_limit: max action_required items (default 5)
//   - interesting_limit: max interesting items (default 10)
//   - background_limit: max background items (default 5)
//   - trace: include decision trace (default false)
func (s *Server) handleAttentionDigestV1(w http.ResponseWriter, r *http.Request) {
	sinceCursor := int64(0)
	if sc := r.URL.Query().Get("since_cursor"); sc != "" {
		parsed, err := strconv.ParseInt(sc, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since_cursor: "+err.Error())
			return
		}
		sinceCursor = parsed
	}

	opts := robot.DefaultAttentionDigestOptions()

	if arl := r.URL.Query().Get("action_required_limit"); arl != "" {
		if parsed, err := strconv.Atoi(arl); err == nil && parsed >= 0 && parsed <= 1000 {
			opts.ActionRequiredLimit = parsed
		}
	}
	if il := r.URL.Query().Get("interesting_limit"); il != "" {
		if parsed, err := strconv.Atoi(il); err == nil && parsed >= 0 && parsed <= 1000 {
			opts.InterestingLimit = parsed
		}
	}
	if bl := r.URL.Query().Get("background_limit"); bl != "" {
		if parsed, err := strconv.Atoi(bl); err == nil && parsed >= 0 && parsed <= 1000 {
			opts.BackgroundLimit = parsed
		}
	}
	if trace := r.URL.Query().Get("trace"); trace == "true" || trace == "1" {
		opts.IncludeTrace = true
	}

	categoryFilter := parseCSVParam(r.URL.Query().Get("category"))
	if len(categoryFilter) > 0 {
		categories := make([]robot.EventCategory, 0, len(categoryFilter))
		for _, cat := range categoryFilter {
			categories = append(categories, robot.EventCategory(cat))
		}
		opts.Categories = categories
	}

	sessionFilter := r.URL.Query().Get("session")
	if sessionFilter != "" {
		opts.Session = sessionFilter
	}

	feed := robot.GetAttentionFeed()
	stats := feed.Stats()

	// Check for cursor expiration using the same boundary as the underlying journal.
	if expired, earliest := attentionCursorExpired(sinceCursor, stats); expired {
		writeJSON(w, http.StatusConflict, attentionCursorExpiredPayload(&robot.CursorExpiredError{
			RequestedCursor: sinceCursor,
			EarliestCursor:  earliest,
			RetentionPeriod: stats.RetentionPeriod,
		}, stats.NewestCursor))
		return
	}

	digest, err := feed.Digest(sinceCursor, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "digest failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, digest)
}

type AttentionItemStateRequest struct {
	Action            string `json:"action"`
	Reason            string `json:"reason,omitempty"`
	Until             string `json:"until,omitempty"`
	OverridePriority  string `json:"override_priority,omitempty"`
	OverrideExpiresAt string `json:"override_expires_at,omitempty"`
	ResurfacingPolicy string `json:"resurfacing_policy,omitempty"`
}

func attentionMaterialDetailsForServe(details map[string]any) map[string]any {
	if len(details) == 0 {
		return nil
	}
	filtered := make(map[string]any, len(details))
	for key, value := range details {
		switch {
		case strings.HasPrefix(key, "attention_"):
			continue
		case strings.HasPrefix(key, "digest_"):
			continue
		case key == "resurfaced",
			key == "resurface_reason",
			key == "cooldown_window_ms",
			key == "cooldown_suppressed_count",
			key == "cooldown_suppressed_since",
			key == "cooldown_last_suppressed":
			continue
		default:
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func attentionEventFingerprintForServe(event state.StoredAttentionEvent) string {
	pane := 0
	if event.Pane != "" {
		if parsed, err := strconv.Atoi(event.Pane); err == nil {
			pane = parsed
		}
	}
	payload := map[string]any{
		"session":       strings.TrimSpace(event.SessionName),
		"pane":          pane,
		"category":      strings.TrimSpace(event.Category),
		"type":          strings.TrimSpace(event.EventType),
		"source":        strings.TrimSpace(event.Source),
		"actionability": strings.TrimSpace(string(event.Actionability)),
		"severity":      strings.TrimSpace(string(event.Severity)),
		"reason_code":   strings.TrimSpace(event.ReasonCode),
		"summary":       strings.TrimSpace(event.Summary),
	}
	if event.Details != "" {
		var details map[string]any
		if err := json.Unmarshal([]byte(event.Details), &details); err == nil {
			if filtered := attentionMaterialDetailsForServe(details); len(filtered) > 0 {
				payload["details"] = filtered
			}
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("fp-%x", sum[:8])
}

func (s *Server) attentionStoredEventByCursor(cursor int64) (*state.StoredAttentionEvent, error) {
	if s.stateStore == nil {
		return nil, nil
	}
	events, err := s.stateStore.GetAttentionEventsSince(cursor-1, 1)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 || events[0].Cursor != cursor {
		return nil, nil
	}
	event := events[0]
	return &event, nil
}

func attentionActorFromRequest(r *http.Request) (actorType, actorID, actorOrigin, actorLabel string) {
	rc := RoleFromContext(r.Context())
	if rc == nil {
		return "api", "anonymous", "api", "anonymous"
	}
	actorType = "user"
	actorOrigin = string(rc.Role)
	actorID = strings.TrimSpace(rc.UserID)
	if actorID == "" {
		actorID = actorOrigin
	}
	if actorID == "" {
		actorID = "anonymous"
	}
	return actorType, actorID, actorOrigin, actorID
}

func attentionCanonicalState(itemKey, dedupKey, fingerprint string, prior *state.AttentionItemState) state.AttentionItemState {
	if prior != nil {
		next := *prior
		if itemKey != "" {
			next.ItemKey = itemKey
		}
		if dedupKey != "" {
			next.DedupKey = dedupKey
		}
		if fingerprint != "" {
			next.Fingerprint = fingerprint
		}
		if next.State == "" {
			next.State = state.AttentionStateNew
		}
		if next.ResurfacingPolicy == "" {
			next.ResurfacingPolicy = "on_change"
		}
		return next
	}
	return state.AttentionItemState{
		ItemKey:           itemKey,
		DedupKey:          dedupKey,
		State:             state.AttentionStateNew,
		Fingerprint:       fingerprint,
		ResurfacingPolicy: "on_change",
	}
}

func attentionStateJSON(item state.AttentionItemState) (string, error) {
	raw, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func attentionStateLabel(item state.AttentionItemState) string {
	if strings.TrimSpace(string(item.State)) == "" {
		return string(state.AttentionStateNew)
	}
	return string(item.State)
}

func applyAttentionItemAction(item *state.AttentionItemState, action string, req AttentionItemStateRequest, actorLabel string, now time.Time) error {
	if item == nil {
		return fmt.Errorf("attention item state is nil")
	}

	action = strings.ToLower(strings.TrimSpace(action))
	if policy := strings.TrimSpace(req.ResurfacingPolicy); policy != "" {
		item.ResurfacingPolicy = policy
	}

	switch action {
	case "ack", "acknowledge":
		item.State = state.AttentionStateAcknowledged
		item.AcknowledgedAt = &now
		item.AcknowledgedBy = actorLabel
		item.SnoozedUntil = nil
		item.DismissedAt = nil
		item.DismissedBy = ""
	case "snooze":
		until := strings.TrimSpace(req.Until)
		if until == "" {
			return fmt.Errorf("until is required for snooze")
		}
		parsed, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return fmt.Errorf("invalid until: %w", err)
		}
		parsed = parsed.UTC()
		if !parsed.After(now) {
			return fmt.Errorf("until must be in the future")
		}
		item.State = state.AttentionStateSnoozed
		item.SnoozedUntil = &parsed
		item.DismissedAt = nil
		item.DismissedBy = ""
	case "dismiss":
		item.State = state.AttentionStateDismissed
		item.DismissedAt = &now
		item.DismissedBy = actorLabel
		item.SnoozedUntil = nil
	case "restore":
		item.State = state.AttentionStateNew
		item.AcknowledgedAt = nil
		item.AcknowledgedBy = ""
		item.SnoozedUntil = nil
		item.DismissedAt = nil
		item.DismissedBy = ""
	case "pin":
		item.Pinned = true
		item.PinnedAt = &now
		item.PinnedBy = actorLabel
	case "unpin":
		item.Pinned = false
		item.PinnedAt = nil
		item.PinnedBy = ""
	case "mute":
		item.Muted = true
		item.MutedAt = &now
		item.MutedBy = actorLabel
	case "unmute":
		item.Muted = false
		item.MutedAt = nil
		item.MutedBy = ""
	case "escalate":
		override := strings.TrimSpace(req.OverridePriority)
		if override == "" {
			override = "critical"
		}
		item.OverridePriority = override
		item.OverrideReason = strings.TrimSpace(req.Reason)
		if expires := strings.TrimSpace(req.OverrideExpiresAt); expires != "" {
			parsed, err := time.Parse(time.RFC3339, expires)
			if err != nil {
				return fmt.Errorf("invalid override_expires_at: %w", err)
			}
			parsed = parsed.UTC()
			item.OverrideExpiresAt = &parsed
		} else {
			item.OverrideExpiresAt = nil
		}
	default:
		return fmt.Errorf("unsupported action %q", req.Action)
	}

	return nil
}

// handleAttentionItemStateV1 handles POST /api/v1/attention/items/{cursor}/state.
func (s *Server) handleAttentionItemStateV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	cursor, err := strconv.ParseInt(strings.TrimSpace(chi.URLParam(r, "cursor")), 10, 64)
	if err != nil || cursor <= 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "valid attention cursor required", nil, reqID)
		return
	}

	var req AttentionItemStateRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	if strings.TrimSpace(req.Action) == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "action required", nil, reqID)
		return
	}

	storedEvent, err := s.attentionStoredEventByCursor(cursor)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load attention event: "+err.Error(), nil, reqID)
		return
	}
	if storedEvent == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "attention item not found", nil, reqID)
		return
	}

	itemKey, err := s.stateStore.ResolveAttentionItemKey(cursor)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to resolve attention item key: "+err.Error(), nil, reqID)
		return
	}
	if itemKey == "" {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "attention item key not found", nil, reqID)
		return
	}

	priorState, err := s.stateStore.GetAttentionItemState(itemKey)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load attention item state: "+err.Error(), nil, reqID)
		return
	}

	actorType, actorID, actorOrigin, actorLabel := attentionActorFromRequest(r)
	fingerprint := attentionEventFingerprintForServe(*storedEvent)
	now := time.Now().UTC()
	nextState := attentionCanonicalState(itemKey, storedEvent.DedupKey, fingerprint, priorState)
	if err := applyAttentionItemAction(&nextState, req.Action, req, actorLabel, now); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	nextState.Fingerprint = fingerprint

	previous := attentionCanonicalState(itemKey, storedEvent.DedupKey, fingerprint, priorState)
	previousJSON, err := attentionStateJSON(previous)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize previous state", nil, reqID)
		return
	}
	newJSON, err := attentionStateJSON(nextState)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize new state", nil, reqID)
		return
	}

	var (
		auditEventID    int64
		auditDecisionID int64
	)

	err = s.stateStore.Transaction(func(tx *state.Tx) error {
		if err := tx.UpsertAttentionItemState(&nextState); err != nil {
			return err
		}

		evidenceJSON, marshalErr := json.Marshal(map[string]any{
			"cursor":      cursor,
			"dedup_key":   storedEvent.DedupKey,
			"session":     storedEvent.SessionName,
			"pane":        storedEvent.Pane,
			"summary":     storedEvent.Summary,
			"fingerprint": fingerprint,
		})
		if marshalErr != nil {
			return fmt.Errorf("marshal attention evidence: %w", marshalErr)
		}

		eventID, err := tx.RecordAuditEvent(&state.AuditEvent{
			Ts:              now,
			ActorType:       actorType,
			ActorID:         actorID,
			ActorOrigin:     actorOrigin,
			RequestID:       reqID,
			CorrelationID:   r.Header.Get("Idempotency-Key"),
			Category:        "attention",
			EventType:       strings.ToLower(strings.TrimSpace(req.Action)),
			Severity:        storedEvent.Severity,
			EntityType:      "attention_item",
			EntityID:        itemKey,
			PreviousState:   previousJSON,
			NewState:        newJSON,
			ChangeSummary:   fmt.Sprintf("%s attention item %s", strings.ToLower(strings.TrimSpace(req.Action)), itemKey),
			Reason:          strings.TrimSpace(req.Reason),
			Evidence:        string(evidenceJSON),
			DisclosureState: "visible",
			RetentionClass:  state.RetentionClassStandard,
		})
		if err != nil {
			return err
		}
		auditEventID = eventID

		decisionID, err := tx.RecordAuditDecision(&state.AuditDecision{
			DecisionType: strings.ToLower(strings.TrimSpace(req.Action)),
			DecisionAt:   now,
			ActorType:    actorType,
			ActorID:      actorID,
			EntityType:   "attention_item",
			EntityID:     itemKey,
			Reason:       strings.TrimSpace(req.Reason),
			ExpiresAt:    nextState.OverrideExpiresAt,
			AuditEventID: &auditEventID,
		})
		if err != nil {
			return err
		}
		auditDecisionID = decisionID
		return nil
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to apply attention action: "+err.Error(), nil, reqID)
		return
	}

	SetAuditResource(r, "attention", itemKey)
	SetAuditSession(r, storedEvent.SessionName, storedEvent.Pane, "")
	SetAuditAction(r, AuditActionUpdate)
	SetAuditDetails(r, fmt.Sprintf("%s attention item %s", strings.ToLower(strings.TrimSpace(req.Action)), itemKey))

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"target": map[string]interface{}{
			"cursor":    cursor,
			"item_key":  itemKey,
			"dedup_key": storedEvent.DedupKey,
			"session":   storedEvent.SessionName,
			"pane":      storedEvent.Pane,
		},
		"result": map[string]interface{}{
			"action":          strings.ToLower(strings.TrimSpace(req.Action)),
			"previous_state":  attentionStateLabel(previous),
			"new_state":       attentionStateLabel(nextState),
			"attention_state": nextState,
		},
		"audit": map[string]interface{}{
			"event_id":    auditEventID,
			"decision_id": auditDecisionID,
			"actor_type":  actorType,
			"actor_id":    actorID,
		},
	}, reqID)
}

func attentionReplayEarliestCursor(stats robot.JournalStats) int64 {
	if stats.Count == 0 {
		return stats.NewestCursor
	}
	return stats.OldestCursor
}

func attentionCursorExpired(sinceCursor int64, stats robot.JournalStats) (bool, int64) {
	earliest := attentionReplayEarliestCursor(stats)
	if sinceCursor <= 0 || stats.NewestCursor == 0 {
		return false, earliest
	}
	if stats.Count == 0 {
		return sinceCursor < stats.NewestCursor, earliest
	}
	return sinceCursor < earliest-1, earliest
}

// matchesAttentionFilters checks if an event matches the specified filters.
func matchesAttentionFilters(event robot.AttentionEvent, categoryFilter []string, sessionFilter string, actionabilityFilter []string) bool {
	// Category filter
	if len(categoryFilter) > 0 {
		matched := false
		for _, cat := range categoryFilter {
			if string(event.Category) == cat {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Session filter
	if sessionFilter != "" && event.Session != sessionFilter {
		return false
	}

	// Actionability filter
	if len(actionabilityFilter) > 0 {
		matched := false
		for _, act := range actionabilityFilter {
			if string(event.Actionability) == act {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}
