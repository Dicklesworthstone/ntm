package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// ============================================================================
// WebSocket Hub + Subscription Protocol
// ============================================================================

// WSMessageType defines WebSocket message types.
type WSMessageType string

const (
	WSMsgSubscribe   WSMessageType = "subscribe"
	WSMsgUnsubscribe WSMessageType = "unsubscribe"
	WSMsgEvent       WSMessageType = "event"
	WSMsgError       WSMessageType = "error"
	WSMsgAck         WSMessageType = "ack"
	WSMsgPing        WSMessageType = "ping"
	WSMsgPong        WSMessageType = "pong"
)

// WSMessage is the base WebSocket message envelope.
type WSMessage struct {
	Type      WSMessageType          `json:"type"`
	Timestamp string                 `json:"ts"`
	RequestID string                 `json:"request_id,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// WSSubscribeRequest is sent by clients to subscribe to topics.
// Since is the replay cursor: the seq of the last event frame the client
// processed. When present, persisted events with seq > Since matching the
// requested topics are replayed before live delivery. See the replay contract
// in ws_events.go.
type WSSubscribeRequest struct {
	Topics []string `json:"topics"`
	Since  int64    `json:"since,omitempty"` // Cursor for replay (event seq)
}

// WSEvent is an event pushed to clients.
// Seq is the resume cursor: clients persist the highest seq they have
// processed and pass it back as `since` on re-subscribe. Replay is set only on
// frames re-delivered from the event store during cursor replay (additive
// field; absent on live frames).
type WSEvent struct {
	Type      WSMessageType `json:"type"`
	Timestamp string        `json:"ts"`
	Seq       int64         `json:"seq"`
	Topic     string        `json:"topic"`
	EventType string        `json:"event_type"`
	Data      interface{}   `json:"data"`
	Replay    bool          `json:"replay,omitempty"`
}

// WSError represents a WebSocket error frame.
type WSError struct {
	Type      WSMessageType `json:"type"`
	Timestamp string        `json:"ts"`
	RequestID string        `json:"request_id,omitempty"`
	Code      string        `json:"code"`
	Message   string        `json:"message"`
}

// WSAttentionSub tracks attention-specific subscription state for a WebSocket client.
// This enables durable attention semantics: cursor replay, cursor expiration,
// operator state filtering, and transport parity with SSE.
type WSAttentionSub struct {
	// Active indicates whether attention streaming is active for this client.
	Active bool `json:"active"`

	// Cursor is the last event cursor delivered to this client.
	// Used for replay deduplication and heartbeat reporting.
	Cursor int64 `json:"cursor"`

	// SinceCursor is the cursor from which streaming started.
	SinceCursor int64 `json:"since_cursor"`

	// Session filters events to a specific session (empty = all).
	Session string `json:"session,omitempty"`

	// Profile is the attention profile name (e.g., "operator", "minimal").
	Profile string `json:"profile,omitempty"`

	// CategoryFilter limits events to these categories.
	CategoryFilter []string `json:"category_filter,omitempty"`

	// ActionabilityFilter limits events to these actionability levels.
	ActionabilityFilter []string `json:"actionability_filter,omitempty"`

	// ExcludeMuted excludes items with muted operator state.
	ExcludeMuted bool `json:"exclude_muted"`

	// ExcludeSnoozed excludes items that are currently snoozed.
	ExcludeSnoozed bool `json:"exclude_snoozed"`

	// StartedAt is when this subscription started.
	StartedAt time.Time `json:"started_at"`

	// EventCount is the number of events delivered since subscription started.
	EventCount int64 `json:"event_count"`

	// DroppedCount is events dropped due to backpressure.
	DroppedCount uint64 `json:"dropped_count"`

	// unsubscribe is the function to stop the feed subscription.
	unsubscribe func()
	topics      []string
}

// WSClient represents a connected WebSocket client.
type WSClient struct {
	id         string
	conn       *websocket.Conn
	hub        *WSHub
	send       chan []byte
	topics     map[string]struct{}
	topicsMu   sync.RWMutex
	authClaims map[string]interface{}
	closeOnce  sync.Once

	// Attention subscription state for durable attention semantics.
	attentionSub   *WSAttentionSub
	attentionSubMu sync.Mutex
}

// WSHub manages WebSocket connections and topic routing.
type WSHub struct {
	clients      map[*WSClient]struct{}
	clientsMu    sync.RWMutex
	register     chan *WSClient
	unregister   chan *WSClient
	broadcast    chan *WSEvent
	dropped      atomic.Int64
	seq          int64
	seqMu        sync.Mutex
	done         chan struct{}
	stopOnce     sync.Once
	redactionCfg *RedactionConfig
	redactionMu  sync.RWMutex

	// store is the optional persistence/replay backend (see the replay
	// contract in ws_events.go). Nil pointer = live streaming only.
	store atomic.Pointer[WSEventStore]
	// replayCh carries subscribe-with-cursor requests into the Run loop so
	// replay and live delivery are serialized (gap-free, duplicate-free).
	replayCh chan *wsReplayRequest
	// pendingDrops coalesces per-client dropped-event ranges. Owned
	// exclusively by the Run goroutine; no locking.
	pendingDrops map[*WSClient]map[string]*wsDropRange
	// pendingResets blocks later events until a backpressured client receives
	// the explicit resync signal. Owned by the Run goroutine.
	pendingResets map[*WSClient][]byte
}

// NewWSHub creates a new WebSocket hub.
func NewWSHub() *WSHub {
	return &WSHub{
		clients:       make(map[*WSClient]struct{}),
		register:      make(chan *WSClient),
		unregister:    make(chan *WSClient),
		broadcast:     make(chan *WSEvent, 256),
		done:          make(chan struct{}),
		replayCh:      make(chan *wsReplayRequest),
		pendingDrops:  make(map[*WSClient]map[string]*wsDropRange),
		pendingResets: make(map[*WSClient][]byte),
	}
}

// Run starts the hub's main event loop.
func (h *WSHub) Run() {
	for {
		select {
		case <-h.done:
			// Persist any drop ranges still pending so slow-client gaps are
			// visible in ws_dropped_events across a restart. Best-effort.
			h.flushAllDrops()
			return
		case client := <-h.register:
			h.clientsMu.Lock()
			h.clients[client] = struct{}{}
			total := len(h.clients)
			h.clientsMu.Unlock()
			log.Printf("ws client connected id=%s total=%d", client.id, total)
		case client := <-h.unregister:
			h.flushClientDrops(client, false)
			delete(h.pendingResets, client)
			h.clientsMu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			total := len(h.clients)
			h.clientsMu.Unlock()
			log.Printf("ws client disconnected id=%s total=%d", client.id, total)
		case req := <-h.replayCh:
			h.handleReplay(req)
		case event := <-h.broadcast:
			h.broadcastEvent(event)
		}
	}
}

// Stop shuts down the hub.
func (h *WSHub) Stop() {
	h.stopOnce.Do(func() {
		close(h.done)
		h.clientsMu.Lock()
		defer h.clientsMu.Unlock()
		for client := range h.clients {
			delete(h.clients, client)
			close(client.send)
		}
	})
}

// nextSeq returns the next sequence number.
func (h *WSHub) nextSeq() int64 {
	h.seqMu.Lock()
	defer h.seqMu.Unlock()
	h.seq++
	return h.seq
}

// broadcastEvent sends an event to all subscribed clients.
// Runs on the hub's Run goroutine: the bounded broadcast channel (Publish
// drops on overflow instead of blocking) keeps the persistence insert below
// off the publication hot path.
func (h *WSHub) broadcastEvent(event *WSEvent) {
	// Apply redaction first so the persisted copy matches the wire form and
	// replay cannot leak content that live delivery would have redacted.
	if err := h.redactEvent(event); err != nil {
		// No cursor has been allocated and no data has been persisted. Signal
		// the loss without copying untrusted payload, metadata or errors into
		// the reset frame or logs.
		log.Printf("ws redaction: event withheld; stream resynchronization required")
		cursor := h.currentSeq()
		if store := h.eventStore(); store != nil {
			cursor = store.CurrentSeq()
		}
		h.clientsMu.RLock()
		for client := range h.clients {
			if client.isSubscribed(event.Topic) {
				h.trySendReset(client, "", "redaction_failed", cursor, 0)
			}
		}
		h.clientsMu.RUnlock()
		return
	}

	// Assign the frame cursor. When an event store is attached the persisted
	// seq is authoritative: it survives restarts and is what clients pass back
	// as `since` to resume (see the replay contract in ws_events.go).
	if store := h.eventStore(); store != nil {
		if stored, err := store.Store(event.Topic, event.EventType, event.Data); err == nil {
			event.Seq = stored.Seq
		} else {
			// A separate ephemeral counter would rewind or collide with the
			// durable cursor and conceal a gap from reconnecting clients. Make
			// the loss explicit so consumers can refresh their state instead.
			log.Printf("ws_events: store failed, resetting stream topic=%s: %v", event.Topic, err)
			h.clientsMu.RLock()
			for client := range h.clients {
				if client.isSubscribed(event.Topic) {
					h.trySendReset(client, event.Topic, "persistence_failed", store.CurrentSeq(), 0)
				}
			}
			h.clientsMu.RUnlock()
			return
		}
	} else {
		event.Seq = h.nextSeq()
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("ws marshal error: %v", err)
		return
	}

	h.clientsMu.RLock()
	defer h.clientsMu.RUnlock()

	for client := range h.clients {
		if !h.flushClientReset(client) {
			if client.isSubscribed(event.Topic) {
				h.noteClientDrop(client, event.Topic, event.Seq)
			}
			continue
		}
		if client.isSubscribed(event.Topic) {
			select {
			case client.send <- data:
				// Delivery succeeded again: report and persist any gap this
				// client accumulated while it was slow.
				h.flushClientDrops(client, true)
			default:
				dropped := h.dropped.Add(1)
				h.noteClientDrop(client, event.Topic, event.Seq)
				log.Printf("ws client buffer full id=%s surface=websocket session= pane= queue_depth=%d dropped_count=%d latency_ms=0 decision=coalesce reason_codes=queue_depth,dropped_output", client.id, len(client.send), dropped)
			}
		}
	}
}

// redactEvent protects each publication boundary, including historical replay
// and attention frames that do not pass through broadcastEvent. Routing keys
// must retain their identity; refuse a sensitive one instead of rewriting it.
func (h *WSHub) redactEvent(event *WSEvent) error {
	h.redactionMu.RLock()
	cfg := h.redactionCfg
	h.redactionMu.RUnlock()
	if cfg == nil || !cfg.Enabled || cfg.Config.Mode == redaction.ModeOff || cfg.Config.Mode == redaction.ModeWarn {
		return nil
	}

	metadata, err := json.Marshal(struct {
		Topic     string `json:"topic"`
		EventType string `json:"event_type"`
	}{Topic: event.Topic, EventType: event.EventType})
	if err != nil {
		return errors.New("cannot encode event metadata")
	}
	metadataCfg := cfg.Config
	metadataCfg.Mode = redaction.ModeBlock
	result := scanServerJSON(string(metadata), metadataCfg)
	if result.LimitExceeded {
		return errRedactionInspectionLimit
	}
	if result.Blocked {
		return errors.New("event metadata contains sensitive content")
	}
	data, err := redactWSEventData(event.Data, cfg.Config)
	if err != nil {
		return err
	}
	event.Data = data
	return nil
}

// redactWSEventData scans the complete JSON representation, preserving field
// names as detection context and covering structs, typed maps/slices and raw
// JSON alike. Freeze the encoded payload once: custom marshalers must not
// produce different data for persistence and live delivery. Raw JSON also
// preserves exact numbers without a float64 round trip.
func redactWSEventData(data interface{}, cfg redaction.Config) (interface{}, error) {
	if cfg.Mode == redaction.ModeOff || cfg.Mode == redaction.ModeWarn {
		return data, nil
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		// MarshalJSON errors may themselves contain credentials.
		return nil, errors.New("cannot encode event data for redaction")
	}
	output, _, err := redactJSONBytes(encoded, cfg)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(output), nil
}

// Publish publishes an event to a topic.
func (h *WSHub) Publish(topic, eventType string, data interface{}) {
	event := &WSEvent{
		Type:      WSMsgEvent,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Topic:     topic,
		EventType: eventType,
		Data:      data,
	}
	select {
	case <-h.done:
		return
	case h.broadcast <- event:
	default:
		dropped := h.dropped.Add(1)
		// The queue overflow occurs before redactEvent can inspect metadata.
		log.Printf("ws broadcast buffer full, dropping event surface=websocket session= pane= queue_depth=%d dropped_count=%d latency_ms=0 decision=coalesce reason_codes=queue_depth,dropped_output", len(h.broadcast), dropped)
	}
}

func (h *WSHub) RegisterClient(client *WSClient) bool {
	if client == nil {
		return false
	}
	select {
	case <-h.done:
		return false
	case h.register <- client:
		return true
	}
}

func (h *WSHub) UnregisterClient(client *WSClient) {
	if client == nil {
		return
	}
	select {
	case <-h.done:
		return
	case h.unregister <- client:
	}
}

// isSubscribed checks if a client is subscribed to a topic.
func (c *WSClient) isSubscribed(topic string) bool {
	c.topicsMu.RLock()
	defer c.topicsMu.RUnlock()

	// Check exact match
	if _, ok := c.topics[topic]; ok {
		return true
	}

	// Check wildcard patterns
	// "global" matches all global.* topics
	// "sessions:*" matches all session topics
	// "panes:*" matches all pane topics
	for pattern := range c.topics {
		if matchTopic(pattern, topic) {
			return true
		}
	}
	return false
}

// matchTopic checks if a pattern matches a topic.
// Supports:
//   - "*" matches everything
//   - "prefix:*" matches prefix:anything
//   - exact match
func matchTopic(pattern, topic string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, ":*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(topic, prefix)
	}
	return pattern == topic
}

// Subscribe adds topics to the client's subscription.
func (c *WSClient) Subscribe(topics []string) {
	c.topicsMu.Lock()
	defer c.topicsMu.Unlock()
	for _, topic := range topics {
		c.topics[topic] = struct{}{}
	}
	log.Printf("ws client subscribed id=%s topic_count=%d", c.id, len(topics))
}

// Unsubscribe removes topics from the client's subscription.
func (c *WSClient) Unsubscribe(topics []string) {
	c.topicsMu.Lock()
	defer c.topicsMu.Unlock()
	for _, topic := range topics {
		delete(c.topics, topic)
	}
	log.Printf("ws client unsubscribed id=%s topic_count=%d", c.id, len(topics))
}

// Topics returns the client's subscribed topics.
func (c *WSClient) Topics() []string {
	c.topicsMu.RLock()
	defer c.topicsMu.RUnlock()
	topics := make([]string, 0, len(c.topics))
	for t := range c.topics {
		topics = append(topics, t)
	}
	return topics
}

// WebSocket upgrader configuration.
// Note: CheckOrigin always returns true here because origin validation
// is performed in handleWebSocket using Server.checkWSOrigin() which has
// access to the configured allowed origins. This is necessary because
// CORS middleware does NOT apply to WebSocket upgrade requests.
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Origin validation is performed in handleWebSocket
		return true
	},
}

// WebSocket timeouts.
const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second
	wsPingPeriod     = (wsPongWait * 9) / 10
	wsMaxMessageSize = 4096
)

// =============================================================================
// Streaming API Handlers
// =============================================================================

// handleStartPaneStreamV1 handles POST /api/v1/sessions/{sessionId}/panes/{paneIdx}/stream.
func (s *Server) handleStartPaneStreamV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	target, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}
	topic := streamTopicForTarget(target)

	if err := s.streamManager.StartStream(target); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"target":  target,
		"topic":   topic,
		"message": "streaming started",
	}, reqID)
}

// handleStopPaneStreamV1 handles DELETE /api/v1/sessions/{sessionId}/panes/{paneIdx}/stream.
func (s *Server) handleStopPaneStreamV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	target, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}
	s.streamManager.StopStream(target)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"target":  target,
		"message": "streaming stopped",
	}, reqID)
}

func streamTopicForTarget(target string) string {
	if strings.HasPrefix(target, "panes:") {
		return target
	}
	return "panes:" + target
}

// handleStreamingStatsV1 handles GET /api/v1/streaming/stats.
func (s *Server) handleStreamingStatsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	stats := s.streamManager.Stats()
	stats["active_targets"] = s.streamManager.ListActive()

	writeSuccessResponse(w, http.StatusOK, stats, reqID)
}

// ============================================================================
// WebSocket Handler
// ============================================================================

// checkWSOrigin validates the Origin header for WebSocket connections.
//
// The allowlist applies in every auth mode, including local. WebSocket
// handshakes are exempt from CORS, and the REST CSRF defence is the CORS
// middleware's 403-on-bad-Origin, which never runs for an upgrade — so accepting
// any origin in local mode let any page the operator happened to visit open a
// socket to 127.0.0.1 and subscribe to the attention journal, pane output, and
// mail. Local mode is the default, so that was the default posture.
//
// An absent Origin header is still allowed: only browsers send Origin, and only
// browsers are the CSWSH vector, so requiring it would break agents and curl
// without adding protection.
func (s *Server) checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No origin header - allow for non-browser clients
		return true
	}

	// Parse the origin URL to reject malformed values before matching.
	originURL, err := url.Parse(origin)
	if err != nil {
		log.Printf("ws: invalid origin URL %q: %v", origin, err)
		return false
	}

	// Reject malformed origins (e.g., "//example.com" or "https://")
	if originURL.Scheme == "" || originURL.Host == "" {
		log.Printf("ws: malformed origin %q (missing scheme or host)", origin)
		return false
	}

	// Match with the same predicate the CORS middleware uses. The two must not
	// diverge: this used to compare Host (port-sensitive) while CORS compares
	// Hostname, so the default localhost allowlist — which carries no ports —
	// would have rejected a browser at http://localhost:7337 here while allowing
	// it there.
	s.mu.RLock()
	origins := s.corsAllowedOrigins
	s.mu.RUnlock()
	if len(origins) == 0 {
		// Config documents an empty allowlist as "default localhost only".
		// applyDefaults normally materializes that, but honoring it here too
		// means a Server built without defaults enforces the documented policy
		// rather than rejecting every browser origin.
		origins = defaultLocalOrigins()
	}
	if originAllowed(origin, origins) {
		return true
	}

	log.Printf("ws: rejected origin %q (allowed: %v)", origin, origins)
	return false
}

// handleWebSocket handles WebSocket connections at /api/v1/ws.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Validate origin to prevent WebSocket CSRF attacks
	// Note: CORS middleware does NOT apply to WebSocket upgrades
	if !s.checkWSOrigin(r) {
		reqID := requestIDFromContext(r.Context())
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "origin not allowed", nil, reqID)
		return
	}

	// Upgrade HTTP connection to WebSocket
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade failed: %v", err)
		return
	}

	// Generate client ID
	clientID := generateRequestID()

	// Create client
	client := &WSClient{
		id:         clientID,
		conn:       conn,
		hub:        s.wsHub,
		send:       make(chan []byte, 256),
		topics:     make(map[string]struct{}),
		authClaims: extractAuthClaims(r),
	}

	// Wire persistence before the hub loop starts so seq assignment is
	// durable from the very first broadcast (mirrors ensureWSHubRunning for
	// routers exercised without Start()).
	s.ensureWSEventStore()
	s.ensureWSHubRunning()

	// Register client with hub
	if !s.wsHub.RegisterClient(client) {
		if err := conn.Close(); err != nil {
			log.Printf("ws close after failed register id=%s: %v", clientID, err)
		}
		return
	}

	// Start read and write pumps
	go client.writePump()
	go client.readPump()
}

// readPump reads messages from the WebSocket connection.
func (c *WSClient) readPump() {
	defer func() {
		// Cancel attention subscription before unregistering
		c.cancelAttentionSubscription()
		c.hub.UnregisterClient(c)
		c.closeOnce.Do(func() {
			if err := c.conn.Close(); err != nil {
				log.Printf("ws close error id=%s: %v", c.id, err)
			}
		})
	}()

	c.conn.SetReadLimit(wsMaxMessageSize)
	if err := c.conn.SetReadDeadline(time.Now().Add(wsPongWait)); err != nil {
		log.Printf("ws set read deadline error id=%s: %v", c.id, err)
		return
	}
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("ws read error id=%s: %v", c.id, err)
			}
			break
		}

		c.handleMessage(message)
	}
}

// writePump writes messages to the WebSocket connection.
func (c *WSClient) writePump() {
	ticker := time.NewTicker(wsPingPeriod)
	defer func() {
		ticker.Stop()
		c.closeOnce.Do(func() {
			if err := c.conn.Close(); err != nil {
				log.Printf("ws close error id=%s: %v", c.id, err)
			}
		})
	}()

	for {
		select {
		case message, ok := <-c.send:
			if err := c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
				log.Printf("ws set write deadline error id=%s: %v", c.id, err)
				return
			}
			if !ok {
				// Hub closed the channel
				if err := c.conn.WriteMessage(websocket.CloseMessage, []byte{}); err != nil {
					log.Printf("ws close frame error id=%s: %v", c.id, err)
				}
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			if _, err := w.Write(message); err != nil {
				return
			}

			// Drain queued messages
			n := len(c.send)
			for i := 0; i < n; i++ {
				if _, err := w.Write([]byte{'\n'}); err != nil {
					return
				}
				if _, err := w.Write(<-c.send); err != nil {
					return
				}
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
				log.Printf("ws ping deadline error id=%s: %v", c.id, err)
				return
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage processes an incoming WebSocket message.
func (c *WSClient) handleMessage(data []byte) {
	var msg WSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		c.sendError("", "parse_error", "invalid JSON message")
		return
	}

	switch msg.Type {
	case WSMsgSubscribe:
		c.handleSubscribe(msg)
	case WSMsgUnsubscribe:
		c.handleUnsubscribe(msg)
	case WSMsgPing:
		c.sendPong(msg.RequestID)
	default:
		c.sendError(msg.RequestID, "unknown_type", fmt.Sprintf("unknown message type: %s", msg.Type))
	}
}

// handleSubscribe processes a subscribe request.
func (c *WSClient) handleSubscribe(msg WSMessage) {
	// Extract topics from data
	topicsRaw, ok := msg.Data["topics"]
	if !ok {
		c.sendError(msg.RequestID, "missing_topics", "subscribe requires topics array")
		return
	}

	topicsSlice, ok := topicsRaw.([]interface{})
	if !ok {
		c.sendError(msg.RequestID, "invalid_topics", "topics must be an array")
		return
	}

	topics := make([]string, 0, len(topicsSlice))
	for _, t := range topicsSlice {
		if str, ok := t.(string); ok {
			// Validate topic format
			if !isValidTopic(str) {
				c.sendError(msg.RequestID, "invalid_topic", fmt.Sprintf("invalid topic: %s", str))
				return
			}
			topics = append(topics, str)
		}
	}

	if len(topics) == 0 {
		c.sendError(msg.RequestID, "empty_topics", "at least one topic required")
		return
	}

	// Check RBAC for topics
	for _, topic := range topics {
		if !c.canSubscribe(topic) {
			c.sendError(msg.RequestID, "unauthorized", fmt.Sprintf("not authorized for topic: %s", topic))
			return
		}
	}

	// Optional replay cursor (see the replay contract in ws_events.go).
	// Presence of "since" requests replay of persisted events with seq > since
	// for the regular (non-attention) topics before live delivery. Attention
	// topics use their own durable cursor field, since_cursor.
	since := int64(-1)
	if sinceRaw, hasSince := msg.Data["since"]; hasSince {
		f, ok := sinceRaw.(float64)
		if !ok || f < 0 {
			c.sendError(msg.RequestID, "invalid_since", "since must be a non-negative integer event seq")
			return
		}
		since = int64(f)
	}

	// Check for attention topic subscriptions with durable semantics
	attentionTopics, regularTopics := partitionAttentionTopics(topics)

	// Subscribe attention topics first. A cursor-expired or unavailable feed
	// cannot satisfy the requested subscription, so do not acknowledge the
	// complete request (or partially subscribe its regular topics) as though it
	// had succeeded.
	var attentionResult map[string]interface{}
	var attentionSub *WSAttentionSub
	if len(attentionTopics) > 0 {
		attentionResult = c.handleAttentionSubscribe(msg, attentionTopics)
		if errorCode, failed := attentionResult["error_code"].(string); failed {
			message, _ := attentionResult["error"].(string)
			if message == "" {
				message = "attention subscription failed"
			}
			c.sendError(msg.RequestID, errorCode, message)
			return
		}
		c.attentionSubMu.Lock()
		attentionSub = c.attentionSub
		c.attentionSubMu.Unlock()
	}

	response := map[string]interface{}{"subscribed": topics}
	if attentionResult != nil {
		response["attention"] = attentionResult
	}
	sendSubscriptionAck := func() bool {
		if len(attentionTopics) == 0 {
			return c.sendAck(msg.RequestID, response)
		}
		// A live callback can fail while replay or the regular-topic
		// subscription is being prepared. Commit the acknowledgment under the
		// same lock as delivery so no failed subscription is acknowledged.
		c.attentionSubMu.Lock()
		defer c.attentionSubMu.Unlock()
		if attentionSub == nil || c.attentionSub != attentionSub || !attentionSub.Active {
			return c.sendError(msg.RequestID, "REDACTION_FAILED", "attention subscription is no longer active; resynchronize the subscription")
		}
		return c.sendAck(msg.RequestID, response)
	}

	// Regular topics are only committed after any durable attention request has
	// been accepted, keeping a single subscribe message all-or-nothing.
	if len(regularTopics) > 0 {
		if since >= 0 {
			// Queue the completion ack on the hub loop too: live delivery
			// must not overtake it or consume the slot reserved for it.
			c.hub.SubscribeWithReplay(c, regularTopics, since, func(res wsReplayResult) bool {
				response["total"] = len(c.Topics())
				response["replay"] = map[string]interface{}{
					"since":    since,
					"replayed": res.replayed,
					"reset":    res.reset,
					"cursor":   res.cursor,
				}
				return sendSubscriptionAck()
			})
			return
		} else {
			c.Subscribe(regularTopics)
		}
	}

	response["total"] = len(c.Topics())
	sendSubscriptionAck()
}

// handleUnsubscribe processes an unsubscribe request.
func (c *WSClient) handleUnsubscribe(msg WSMessage) {
	topicsRaw, ok := msg.Data["topics"]
	if !ok {
		c.sendError(msg.RequestID, "missing_topics", "unsubscribe requires topics array")
		return
	}

	topicsSlice, ok := topicsRaw.([]interface{})
	if !ok {
		c.sendError(msg.RequestID, "invalid_topics", "topics must be an array")
		return
	}

	topics := make([]string, 0, len(topicsSlice))
	for _, t := range topicsSlice {
		if str, ok := t.(string); ok {
			topics = append(topics, str)
		}
	}

	// Check if any attention topics are being unsubscribed
	attentionTopics, _ := partitionAttentionTopics(topics)
	if len(attentionTopics) > 0 {
		c.removeAttentionTopics(attentionTopics)
	}

	c.Unsubscribe(topics)
	c.sendAck(msg.RequestID, map[string]interface{}{
		"unsubscribed": topics,
		"total":        len(c.Topics()),
	})
}

// isValidTopic checks if a topic string is valid.
//
// Note: This is intentionally permissive for topic *values* and primarily
// validates known topic namespaces, not the full shape of each topic string.
func isValidTopic(topic string) bool {
	if topic == "" {
		return false
	}
	if topic == "*" || topic == "global" || topic == "global:*" || topic == "scanner" || topic == "memory" {
		return true
	}
	// sessions:* or sessions:{name}
	if strings.HasPrefix(topic, "sessions:") {
		return true
	}
	// panes:* or panes:{session}:{idx}
	if strings.HasPrefix(topic, "panes:") {
		return true
	}
	// agent:{type}
	if strings.HasPrefix(topic, "agent:") {
		return true
	}
	// tool systems
	if strings.HasPrefix(topic, "beads:") ||
		strings.HasPrefix(topic, "mail:") ||
		strings.HasPrefix(topic, "reservations:") ||
		strings.HasPrefix(topic, "pipelines:") ||
		strings.HasPrefix(topic, "approvals:") ||
		strings.HasPrefix(topic, "accounts:") ||
		strings.HasPrefix(topic, "attention:") {
		return true
	}
	// attention topic without prefix (for main feed)
	if topic == "attention" {
		return true
	}
	return false
}

// canSubscribe checks if the client is authorized to subscribe to a topic.
func (c *WSClient) canSubscribe(topic string) bool {
	// Deliberate: every authenticated client may subscribe to any topic.
	// ntm serve is a single-operator surface (auth gates the connection, not
	// individual topics); per-topic RBAC is not part of the current design.
	return true
}

// partitionAttentionTopics separates attention topics from regular topics.
// Attention topics ("attention", "attention:*") get special durable semantics.
func partitionAttentionTopics(topics []string) (attention, regular []string) {
	for _, topic := range topics {
		if isAttentionTopic(topic) {
			attention = append(attention, topic)
		} else {
			regular = append(regular, topic)
		}
	}
	return attention, regular
}

// isAttentionTopic checks if a topic is an attention feed topic.
func isAttentionTopic(topic string) bool {
	return topic == "attention" || strings.HasPrefix(topic, "attention:")
}

// handleAttentionSubscribe processes attention topic subscriptions with durable semantics.
// Supports cursor-based replay, filtering, and operator attention state.
//
// Subscription options in msg.Data:
//   - since_cursor: int64 - Replay from cursor (0 = beginning, -1 = now)
//   - session: string - Filter to session
//   - profile: string - Attention profile (operator, minimal, debug, alerts)
//   - category: []string - Filter by event categories
//   - actionability: []string - Filter by actionability levels
//   - exclude_muted: bool - Skip muted items
//   - exclude_snoozed: bool - Skip snoozed items
func (c *WSClient) handleAttentionSubscribe(msg WSMessage, topics []string) map[string]interface{} {
	return c.handleAttentionSubscribeWithFeed(robot.GetAttentionFeed(), msg, topics)
}

// handleAttentionSubscribeWithFeed establishes a durable attention subscription.
// The feed parameter keeps the cursor-validation and replay path testable at the
// point where subscription state is committed.
func (c *WSClient) handleAttentionSubscribeWithFeed(feed attentionStreamFeed, msg WSMessage, topics []string) map[string]interface{} {
	// Extract subscription options
	sinceCursor := int64(0)
	if sc, ok := msg.Data["since_cursor"]; ok {
		switch v := sc.(type) {
		case float64:
			sinceCursor = int64(v)
		case int64:
			sinceCursor = v
		case int:
			sinceCursor = int64(v)
		}
	}

	session := ""
	if s, ok := msg.Data["session"].(string); ok {
		session = s
	}

	profile := ""
	if p, ok := msg.Data["profile"].(string); ok {
		profile = p
	}

	categoryFilter := extractStringSlice(msg.Data["category"])
	actionabilityFilter := extractStringSlice(msg.Data["actionability"])

	excludeMuted := false
	if em, ok := msg.Data["exclude_muted"].(bool); ok {
		excludeMuted = em
	}

	excludeSnoozed := false
	if es, ok := msg.Data["exclude_snoozed"].(bool); ok {
		excludeSnoozed = es
	}

	if feed == nil {
		return map[string]interface{}{
			"error":      "attention_feed_unavailable",
			"error_code": "ATTENTION_UNAVAILABLE",
		}
	}

	// Check for cursor expiration before subscribing
	stats := feed.Stats()
	if expired, earliest := attentionCursorExpired(sinceCursor, stats); expired {
		return map[string]interface{}{
			"error":          "cursor_expired",
			"error_code":     robot.ErrCodeCursorExpired,
			"oldest_cursor":  earliest,
			"newest_cursor":  stats.NewestCursor,
			"resync_hint":    "Resync via --robot-snapshot, then resubscribe with since_cursor=-1",
			"resync_command": "ntm --robot-snapshot",
		}
	}

	// Replace the previous durable subscription and its hub memberships.
	c.cancelAttentionSubscription()

	// Create new attention subscription
	sub := &WSAttentionSub{
		Active:              true,
		Cursor:              sinceCursor,
		SinceCursor:         sinceCursor,
		Session:             session,
		Profile:             profile,
		CategoryFilter:      categoryFilter,
		ActionabilityFilter: actionabilityFilter,
		ExcludeMuted:        excludeMuted,
		ExcludeSnoozed:      excludeSnoozed,
		StartedAt:           time.Now(),
		topics:              append([]string(nil), topics...),
	}

	// Subscribe to the feed with filtering
	sub.unsubscribe = feed.Subscribe(func(event robot.AttentionEvent) {
		c.deliverAttentionEvent(event, sub)
	})

	// Store subscription
	c.attentionSubMu.Lock()
	c.attentionSub = sub
	c.attentionSubMu.Unlock()

	// Retain the requested topics for replacement and explicit unsubscribe.
	// Live attention events use the durable feed callback above, so Start does
	// not bridge attention events back through wsHub.
	c.Subscribe(topics)

	// Perform replay if cursor is not "start from now"
	var replayCount int
	if sinceCursor >= 0 {
		events, _, err := feed.Replay(sinceCursor, stats.Count)
		if err != nil {
			// The cursor can fall out of the bounded replay window after the
			// preflight above and before Replay acquires the feed's journal lock.
			// Do not acknowledge a subscription which missed its requested
			// history: remove the live callback before
			// reporting the failure to the caller.
			c.cancelAttentionSubscription()
			c.Unsubscribe(topics)

			var cursorErr *robot.CursorExpiredError
			if errors.As(err, &cursorErr) {
				currentStats := feed.Stats()
				return map[string]interface{}{
					"error":          "cursor_expired",
					"error_code":     robot.ErrCodeCursorExpired,
					"oldest_cursor":  cursorErr.EarliestCursor,
					"newest_cursor":  currentStats.NewestCursor,
					"resync_hint":    "Resync via --robot-snapshot, then resubscribe with since_cursor=-1",
					"resync_command": "ntm --robot-snapshot",
				}
			}
			return map[string]interface{}{
				"error":      fmt.Sprintf("replay attention events: %v", err),
				"error_code": "ATTENTION_REPLAY_FAILED",
			}
		} else {
			// Deliver replayed events
			var replayErr error
			replayCount, replayErr = c.deliverAttentionReplay(events, topics, sub)
			if replayErr != nil {
				c.cancelAttentionSubscription()
				return map[string]interface{}{
					"error":      "attention replay could not be disclosed safely; resynchronize the subscription",
					"error_code": "REDACTION_FAILED",
				}
			}
		}
	}

	// Live delivery may have failed while replay was in progress, including
	// an empty replay. Never report that inactive subscription as accepted.
	c.attentionSubMu.Lock()
	active := c.attentionSub == sub && sub.Active
	c.attentionSubMu.Unlock()
	if !active {
		c.cancelAttentionSubscription()
		return map[string]interface{}{
			"error":      "attention subscription is no longer active; resynchronize the subscription",
			"error_code": "REDACTION_FAILED",
		}
	}

	// Build response
	result := map[string]interface{}{
		"subscribed":    true,
		"topics":        topics,
		"since_cursor":  sinceCursor,
		"oldest_cursor": attentionReplayEarliestCursor(stats),
		"newest_cursor": stats.NewestCursor,
		"replay_count":  replayCount,
	}
	if session != "" {
		result["session_filter"] = session
	}
	if profile != "" {
		result["profile"] = profile
	}
	return result
}

// cancelAttentionSubscription stops any existing attention subscription.
func (c *WSClient) cancelAttentionSubscription() {
	c.attentionSubMu.Lock()
	sub := c.attentionSub
	c.attentionSub = nil
	c.attentionSubMu.Unlock()

	if sub != nil && sub.unsubscribe != nil {
		sub.unsubscribe()
	}
	if sub != nil && len(sub.topics) > 0 {
		c.Unsubscribe(sub.topics)
	}
}

func (c *WSClient) removeAttentionTopics(topics []string) {
	c.attentionSubMu.Lock()
	sub := c.attentionSub
	if sub == nil {
		c.attentionSubMu.Unlock()
		return
	}
	remaining := make([]string, 0, len(sub.topics))
	for _, subscribed := range sub.topics {
		remove := false
		for _, requested := range topics {
			if subscribed == requested {
				remove = true
				break
			}
		}
		if !remove {
			remaining = append(remaining, subscribed)
		}
	}
	sub.topics = remaining
	if len(remaining) != 0 {
		c.attentionSubMu.Unlock()
		return
	}
	c.attentionSub = nil
	c.attentionSubMu.Unlock()
	if sub.unsubscribe != nil {
		sub.unsubscribe()
	}
}

// deliverAttentionEvent delivers a single attention event to the client.
// Applies subscription filters and tracks cursor position.
func (c *WSClient) deliverAttentionEvent(event robot.AttentionEvent, expected *WSAttentionSub) {
	// Snapshot the filter criteria AND the cursor under the lock. Copying only
	// the pointer and then reading sub.Cursor/sub.Active raced the locked
	// cursor writes below and in deliverAttentionReplay: AttentionFeed invokes
	// subscriber handlers synchronously on the publishing goroutine, so any
	// event published during the subscribe→replay window runs both at once,
	// which either re-delivered an event or rolled the cursor backwards.
	c.attentionSubMu.Lock()
	sub := c.attentionSub
	if sub == nil || sub != expected || !sub.Active {
		c.attentionSubMu.Unlock()
		return
	}
	criteria := *sub
	c.attentionSubMu.Unlock()

	// Skip events already sent during replay
	if event.Cursor <= criteria.Cursor {
		return
	}

	// Skip transport heartbeat events (handled separately)
	if event.Type == robot.EventType(robot.DefaultTransportLiveness.HeartbeatType) {
		return
	}

	// Apply session filter
	if criteria.Session != "" && event.Session != criteria.Session {
		return
	}

	// Apply category/actionability filters (session already checked above)
	if !matchesAttentionFilters(event, criteria.CategoryFilter, "", criteria.ActionabilityFilter) {
		return
	}

	// Determine target topic
	topic := "attention"
	if event.Session != "" {
		sessionTopic := "attention:" + event.Session
		for _, t := range criteria.topics {
			if t == sessionTopic {
				topic = sessionTopic
				break
			}
		}
	}

	// Build event envelope
	wsEvent := &WSEvent{
		Type:      WSMsgEvent,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Seq:       event.Cursor, // Use attention cursor as seq for correlation
		Topic:     topic,
		EventType: string(event.Type),
		Data:      event,
	}

	if c.hub != nil {
		if err := c.hub.redactEvent(wsEvent); err != nil {
			// Do not let a later cursor conceal the withheld event. The next
			// subscribe replaces this inactive subscription and releases its
			// feed callback; an in-flight callback for a replaced subscription
			// must not stop the new one.
			c.attentionSubMu.Lock()
			current := c.attentionSub == expected && expected.Active
			if current {
				expected.Active = false
				c.sendError("", "REDACTION_FAILED", "attention event could not be disclosed safely; resynchronize the subscription")
			}
			c.attentionSubMu.Unlock()
			return
		}
	}
	data, err := json.Marshal(wsEvent)
	if err != nil {
		return
	}

	// Redaction can take time, so a replay failure or replacement may have
	// invalidated the snapshot. Sending and committing the cursor must be one
	// operation with respect to those changes.
	c.attentionSubMu.Lock()
	if c.attentionSub != expected || !expected.Active || event.Cursor <= expected.Cursor {
		c.attentionSubMu.Unlock()
		return
	}
	sent := c.trySend(data, nil)
	if sent {
		expected.Cursor = event.Cursor
		expected.EventCount++
	} else {
		expected.DroppedCount++
	}
	c.attentionSubMu.Unlock()
	if !sent {
		log.Printf("ws attention: dropped event cursor=%d client=%s", event.Cursor, c.id)
	}
}

// deliverAttentionReplay delivers replayed attention events to the client.
// Returns the count of events delivered and refuses an unsafe replay so the
// caller cannot acknowledge a partial subscription as successful.
func (c *WSClient) deliverAttentionReplay(events []robot.AttentionEvent, topics []string, sub *WSAttentionSub) (int, error) {
	if sub == nil {
		return 0, nil
	}

	delivered := 0
	for _, event := range events {
		// Skip events before cursor
		if event.Cursor <= sub.SinceCursor {
			continue
		}

		// Skip transport heartbeats
		if event.Type == robot.EventType(robot.DefaultTransportLiveness.HeartbeatType) {
			continue
		}

		// Apply filters
		if sub.Session != "" && event.Session != sub.Session {
			continue
		}
		// Session already checked above
		if !matchesAttentionFilters(event, sub.CategoryFilter, "", sub.ActionabilityFilter) {
			continue
		}

		// Determine topic
		topic := "attention"
		if event.Session != "" {
			sessionTopic := "attention:" + event.Session
			for _, t := range topics {
				if t == sessionTopic {
					topic = sessionTopic
					break
				}
			}
		}

		// Build replay event
		wsEvent := &WSEvent{
			Type:      WSMsgEvent,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Seq:       event.Cursor,
			Topic:     topic,
			EventType: string(event.Type),
			Data:      event,
		}

		if c.hub != nil {
			if err := c.hub.redactEvent(wsEvent); err != nil {
				c.attentionSubMu.Lock()
				if c.attentionSub == sub {
					sub.Active = false
				}
				c.attentionSubMu.Unlock()
				return delivered, err
			}
		}
		data, err := json.Marshal(wsEvent)
		if err != nil {
			continue
		}

		c.attentionSubMu.Lock()
		if c.attentionSub != sub || !sub.Active {
			c.attentionSubMu.Unlock()
			return delivered, errors.New("attention subscription changed during replay")
		}
		if c.trySend(data, nil) {
			delivered++
			if event.Cursor > sub.Cursor {
				sub.Cursor = event.Cursor
			}
		}
		c.attentionSubMu.Unlock()
	}

	return delivered, nil
}

// extractStringSlice extracts a string slice from an interface{}.
func extractStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	switch arr := v.(type) {
	case []string:
		return arr
	case []interface{}:
		result := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

func (c *WSClient) trySend(data []byte, onDrop func()) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	select {
	case c.send <- data:
		return true
	default:
		if onDrop != nil {
			onDrop()
		}
		return false
	}
}

// sendError sends a WebSocket error frame.
func (c *WSClient) sendError(requestID, code, message string) bool {
	errMsg := WSError{
		Type:      WSMsgError,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		RequestID: requestID,
		Code:      code,
		Message:   message,
	}
	data, err := json.Marshal(errMsg)
	if err != nil {
		return false
	}
	return c.trySend(data, func() {
		log.Printf("ws client buffer full, dropping error id=%s", c.id)
	})
}

// sendAck sends a WebSocket acknowledgment frame.
func (c *WSClient) sendAck(requestID string, data map[string]interface{}) bool {
	ack := WSMessage{
		Type:      WSMsgAck,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		RequestID: requestID,
		Data:      data,
	}
	msg, err := json.Marshal(ack)
	if err != nil {
		return false
	}
	return c.trySend(msg, func() {
		log.Printf("ws client buffer full, dropping ack id=%s", c.id)
	})
}

// sendPong sends a WebSocket pong response.
func (c *WSClient) sendPong(requestID string) {
	pong := WSMessage{
		Type:      WSMsgPong,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		RequestID: requestID,
	}
	data, err := json.Marshal(pong)
	if err != nil {
		return
	}
	c.trySend(data, func() {
		// Buffer full, skip
	})
}
