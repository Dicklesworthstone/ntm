-- WebSocket event persistence for cursor resume and replay
-- Enables reliable reconnection and event history retrieval

-- Store recent WebSocket events for replay
CREATE TABLE ws_events (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    topic TEXT NOT NULL,
    event_type TEXT NOT NULL,
    data TEXT NOT NULL,  -- JSON-encoded event data
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index for efficient cursor queries (replay events since a sequence number)
CREATE INDEX idx_ws_events_seq ON ws_events(seq);

-- Index for topic-based queries
CREATE INDEX idx_ws_events_topic_seq ON ws_events(topic, seq);

-- Index for retention cleanup (delete old events)
CREATE INDEX idx_ws_events_created_at ON ws_events(created_at);

-- Track dropped events for backpressure reporting
CREATE TABLE ws_dropped_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    topic TEXT NOT NULL,
    client_id TEXT NOT NULL,
    dropped_count INTEGER NOT NULL DEFAULT 1,
    first_dropped_seq INTEGER,
    last_dropped_seq INTEGER,
    reason TEXT,  -- buffer_full, slow_client, rate_limited
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index for recent drops per client
CREATE INDEX idx_ws_dropped_client ON ws_dropped_events(client_id, created_at);
