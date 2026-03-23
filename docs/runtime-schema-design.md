# Runtime Schema Design

> **SQLite schema design for the robot-redesign runtime projection layer.**
> This document defines tables that hold runtime projections, source health, attention events, incidents, and output watermarks.

**Status:** RATIFIED
**Bead:** bd-j9jo3.2.1
**Created:** 2026-03-22
**Part of:** bd-j9jo3 (robot-redesign epic)
**Depends on:** projection-section-model.md (bd-j9jo3.1.2), robot-surface-taxonomy.md (bd-j9jo3.1.1)

---

## 1. Design Principles

### 1.1 Separation of Concerns

The runtime schema separates:
- **Source-of-truth state** (tmux sessions, beads, mail) — lives in external systems
- **Derived runtime state** (projections, health, attention) — lives in these tables

Runtime tables are **ephemeral projections** that can be rebuilt from sources. They are not the authoritative record.

### 1.2 Durable vs Volatile

| Category | Durability | Survives Restart | Example |
|----------|-----------|------------------|---------|
| Runtime projections | Volatile | No (rebuilt) | `runtime_sessions`, `runtime_agents` |
| Source health | Semi-durable | Yes (cached) | `source_health` |
| Attention events | Durable | Yes (append-only) | `attention_events` |
| Incidents | Durable | Yes (lifecycle) | `incidents` |
| Watermarks | Durable | Yes (cursors) | `output_watermarks` |

### 1.3 Migration Strategy

New tables are added via migration `007_runtime.sql`. They coexist with existing tables (`sessions`, `agents`, etc.) and do not replace them. The runtime layer reads from existing tables and external sources to populate projections.

---

## 2. Table Designs

### 2.1 runtime_sessions

**Purpose:** Cached projection of session state for fast robot queries.

```sql
CREATE TABLE IF NOT EXISTS runtime_sessions (
    -- Identity
    name TEXT PRIMARY KEY,
    label TEXT,
    project_path TEXT,

    -- State (from tmux)
    attached INTEGER NOT NULL DEFAULT 0,  -- 0=detached, 1=attached
    window_count INTEGER NOT NULL DEFAULT 0,
    pane_count INTEGER NOT NULL DEFAULT 0,

    -- Agent summary (from live scan)
    agent_count INTEGER NOT NULL DEFAULT 0,
    active_agents INTEGER NOT NULL DEFAULT 0,
    idle_agents INTEGER NOT NULL DEFAULT 0,
    error_agents INTEGER NOT NULL DEFAULT 0,

    -- Health rollup
    health_status TEXT NOT NULL DEFAULT 'unknown',  -- healthy, warning, critical, unknown
    health_reason TEXT,

    -- Timestamps
    created_at TIMESTAMP,
    last_attached_at TIMESTAMP,
    last_activity_at TIMESTAMP,

    -- Freshness
    collected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stale_after TIMESTAMP NOT NULL  -- When this projection expires
);

CREATE INDEX IF NOT EXISTS idx_runtime_sessions_health ON runtime_sessions(health_status);
CREATE INDEX IF NOT EXISTS idx_runtime_sessions_collected ON runtime_sessions(collected_at);
```

**Refresh policy:** Rebuilt on every status/snapshot call or after `stale_after` expires.

---

### 2.2 runtime_agents

**Purpose:** Cached projection of agent state for fast queries and event generation.

```sql
CREATE TABLE IF NOT EXISTS runtime_agents (
    -- Identity
    id TEXT PRIMARY KEY,  -- session:pane format
    session_name TEXT NOT NULL,
    pane TEXT NOT NULL,  -- "0.1", "0.2", etc.

    -- Type detection
    agent_type TEXT NOT NULL,  -- claude, codex, gemini, user, unknown
    variant TEXT,  -- Model alias
    type_confidence REAL NOT NULL DEFAULT 0.0,
    type_method TEXT NOT NULL DEFAULT 'unknown',  -- process, title, output, manual

    -- State
    state TEXT NOT NULL DEFAULT 'unknown',  -- idle, active, busy, error, compacting
    state_reason TEXT,
    previous_state TEXT,
    state_changed_at TIMESTAMP,

    -- Activity
    last_output_at TIMESTAMP,
    last_output_age_sec INTEGER NOT NULL DEFAULT -1,
    output_tail_lines INTEGER NOT NULL DEFAULT 0,

    -- Coordination
    current_bead TEXT,
    pending_mail INTEGER NOT NULL DEFAULT 0,
    agent_mail_name TEXT,  -- Agent Mail identity

    -- Health
    health_status TEXT NOT NULL DEFAULT 'unknown',
    health_reason TEXT,

    -- Freshness
    collected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stale_after TIMESTAMP NOT NULL,

    FOREIGN KEY (session_name) REFERENCES runtime_sessions(name) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_runtime_agents_session ON runtime_agents(session_name);
CREATE INDEX IF NOT EXISTS idx_runtime_agents_type ON runtime_agents(agent_type);
CREATE INDEX IF NOT EXISTS idx_runtime_agents_state ON runtime_agents(state);
CREATE INDEX IF NOT EXISTS idx_runtime_agents_health ON runtime_agents(health_status);
CREATE INDEX IF NOT EXISTS idx_runtime_agents_collected ON runtime_agents(collected_at);
```

**Refresh policy:** Rebuilt on activity scan or state change detection.

---

### 2.3 runtime_work

**Purpose:** Cached projection of bead/work state for robot queries.

```sql
CREATE TABLE IF NOT EXISTS runtime_work (
    -- Identity
    bead_id TEXT PRIMARY KEY,

    -- Content
    title TEXT NOT NULL,
    status TEXT NOT NULL,  -- open, in_progress, closed
    priority INTEGER NOT NULL DEFAULT 2,
    bead_type TEXT NOT NULL DEFAULT 'task',

    -- Assignment
    assignee TEXT,
    claimed_at TIMESTAMP,

    -- Dependencies
    blocked_by_count INTEGER NOT NULL DEFAULT 0,
    unblocks_count INTEGER NOT NULL DEFAULT 0,

    -- Labels
    labels TEXT,  -- JSON array

    -- Triage
    score REAL,
    score_reason TEXT,

    -- Freshness
    collected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stale_after TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_runtime_work_status ON runtime_work(status);
CREATE INDEX IF NOT EXISTS idx_runtime_work_priority ON runtime_work(priority);
CREATE INDEX IF NOT EXISTS idx_runtime_work_assignee ON runtime_work(assignee);
CREATE INDEX IF NOT EXISTS idx_runtime_work_score ON runtime_work(score);
```

**Refresh policy:** Rebuilt from bv/br on bead changes or every 60s.

---

### 2.4 runtime_coordination

**Purpose:** Cached projection of Agent Mail state.

```sql
CREATE TABLE IF NOT EXISTS runtime_coordination (
    -- Identity
    agent_name TEXT PRIMARY KEY,

    -- Pane association
    session_name TEXT,
    pane TEXT,

    -- Mail state
    unread_count INTEGER NOT NULL DEFAULT 0,
    pending_ack_count INTEGER NOT NULL DEFAULT 0,
    urgent_count INTEGER NOT NULL DEFAULT 0,

    -- Last activity
    last_message_at TIMESTAMP,
    last_sent_at TIMESTAMP,
    last_received_at TIMESTAMP,

    -- Freshness
    collected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stale_after TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_runtime_coord_session ON runtime_coordination(session_name);
CREATE INDEX IF NOT EXISTS idx_runtime_coord_unread ON runtime_coordination(unread_count);
CREATE INDEX IF NOT EXISTS idx_runtime_coord_urgent ON runtime_coordination(urgent_count);
```

**Refresh policy:** Rebuilt from Agent Mail on poll or message event.

---

### 2.5 runtime_quota

**Purpose:** Cached projection of account usage and rate limits.

```sql
CREATE TABLE IF NOT EXISTS runtime_quota (
    -- Identity
    provider TEXT NOT NULL,  -- anthropic, openai, google
    account TEXT NOT NULL,

    -- State
    limit_hit INTEGER NOT NULL DEFAULT 0,  -- 0=no, 1=yes
    used_pct REAL NOT NULL DEFAULT 0.0,
    used_pct_known INTEGER NOT NULL DEFAULT 0, -- 0=unknown, 1=safe to interpret
    used_pct_source TEXT NOT NULL DEFAULT 'unknown', -- provider|tokens|requests|unknown
    resets_at TIMESTAMP,

    -- Active account
    is_active INTEGER NOT NULL DEFAULT 0,  -- Currently selected

    -- Health
    healthy INTEGER NOT NULL DEFAULT 1,  -- 0=unhealthy, 1=healthy
    health_reason TEXT,

    -- Freshness
    collected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stale_after TIMESTAMP NOT NULL,

    PRIMARY KEY (provider, account)
);

CREATE INDEX IF NOT EXISTS idx_runtime_quota_provider ON runtime_quota(provider);
CREATE INDEX IF NOT EXISTS idx_runtime_quota_limit_hit ON runtime_quota(limit_hit);
CREATE INDEX IF NOT EXISTS idx_runtime_quota_active ON runtime_quota(is_active);
```

**Refresh policy:** Rebuilt from caut on quota event or every 30s.

`used_pct` is only meaningful when `used_pct_known=1`, and it must still be read together with `used_pct_source`. If caut only supplies a provider-level percentage, persist that provenance instead of pretending ntm measured exact token or request saturation.

---

### 2.6 source_health

**Purpose:** Durable record of upstream data source health.

```sql
CREATE TABLE IF NOT EXISTS source_health (
    -- Identity
    source_name TEXT PRIMARY KEY,  -- beads, cass, mail, caut, tmux, rch

    -- Availability
    available INTEGER NOT NULL DEFAULT 0,  -- 0=unavailable, 1=available
    healthy INTEGER NOT NULL DEFAULT 0,    -- 0=degraded, 1=healthy
    reason TEXT,

    -- Timing
    last_success_at TIMESTAMP,
    last_failure_at TIMESTAMP,
    last_check_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Latency
    latency_ms INTEGER,
    avg_latency_ms INTEGER,

    -- Version
    version TEXT,

    -- Error tracking
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    last_error_code TEXT
);

CREATE INDEX IF NOT EXISTS idx_source_health_available ON source_health(available);
CREATE INDEX IF NOT EXISTS idx_source_health_healthy ON source_health(healthy);
CREATE INDEX IF NOT EXISTS idx_source_health_last_check ON source_health(last_check_at);
```

**Refresh policy:** Updated on every source probe. Survives restart.

---

### 2.7 attention_events

**Purpose:** Append-only log of events for the attention feed.

```sql
CREATE TABLE IF NOT EXISTS attention_events (
    -- Cursor (monotonic)
    cursor INTEGER PRIMARY KEY AUTOINCREMENT,

    -- Timestamp
    ts TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Scope
    session_name TEXT,
    pane TEXT,

    -- Classification
    category TEXT NOT NULL,  -- agent, session, bead, alert, mail, incident
    event_type TEXT NOT NULL,  -- state_change, error_detected, ready, etc.
    source TEXT NOT NULL,      -- activity_detector, health_monitor, etc.

    -- Priority
    actionability TEXT NOT NULL DEFAULT 'background',  -- urgent, action_required, interesting, background
    severity TEXT NOT NULL DEFAULT 'info',  -- debug, info, warning, error, critical

    -- Content
    summary TEXT NOT NULL,
    details TEXT,  -- JSON

    -- Actions
    next_actions TEXT,  -- JSON array of NextAction

    -- Dedup
    dedup_key TEXT,
    dedup_count INTEGER NOT NULL DEFAULT 1,

    -- Retention
    expires_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_attention_events_ts ON attention_events(ts);
CREATE INDEX IF NOT EXISTS idx_attention_events_session ON attention_events(session_name);
CREATE INDEX IF NOT EXISTS idx_attention_events_category ON attention_events(category);
CREATE INDEX IF NOT EXISTS idx_attention_events_actionability ON attention_events(actionability);
CREATE INDEX IF NOT EXISTS idx_attention_events_severity ON attention_events(severity);
CREATE INDEX IF NOT EXISTS idx_attention_events_expires ON attention_events(expires_at);
CREATE INDEX IF NOT EXISTS idx_attention_events_dedup ON attention_events(dedup_key);
```

**Retention policy:** Events older than `expires_at` (default: 24h) are GC'd.

**Dedup:** Events with same `dedup_key` within 60s increment `dedup_count` instead of inserting.

---

### 2.8 incidents

**Purpose:** Durable incident lifecycle tracking.

```sql
CREATE TABLE IF NOT EXISTS incidents (
    -- Identity
    id TEXT PRIMARY KEY,

    -- Title
    title TEXT NOT NULL,

    -- State
    status TEXT NOT NULL DEFAULT 'open',  -- open, investigating, resolved, muted
    severity TEXT NOT NULL DEFAULT 'medium',  -- critical, high, medium, low

    -- Scope
    session_names TEXT,  -- JSON array
    agent_ids TEXT,      -- JSON array

    -- Aggregation
    alert_count INTEGER NOT NULL DEFAULT 0,
    event_count INTEGER NOT NULL DEFAULT 0,
    first_event_cursor INTEGER,
    last_event_cursor INTEGER,

    -- Timeline
    started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_event_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    acknowledged_at TIMESTAMP,
    acknowledged_by TEXT,
    resolved_at TIMESTAMP,
    resolved_by TEXT,
    muted_at TIMESTAMP,
    muted_by TEXT,
    muted_reason TEXT,

    -- Investigation
    root_cause TEXT,
    resolution TEXT,
    notes TEXT
);

CREATE INDEX IF NOT EXISTS idx_incidents_status ON incidents(status);
CREATE INDEX IF NOT EXISTS idx_incidents_severity ON incidents(severity);
CREATE INDEX IF NOT EXISTS idx_incidents_started_at ON incidents(started_at);
CREATE INDEX IF NOT EXISTS idx_incidents_last_event ON incidents(last_event_at);
```

**Lifecycle:** Open -> Investigating -> Resolved | Muted

---

### 2.9 incident_events

**Purpose:** Links attention events to incidents.

```sql
CREATE TABLE IF NOT EXISTS incident_events (
    incident_id TEXT NOT NULL,
    event_cursor INTEGER NOT NULL,
    added_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    PRIMARY KEY (incident_id, event_cursor),
    FOREIGN KEY (incident_id) REFERENCES incidents(id) ON DELETE CASCADE,
    FOREIGN KEY (event_cursor) REFERENCES attention_events(cursor) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_incident_events_incident ON incident_events(incident_id);
CREATE INDEX IF NOT EXISTS idx_incident_events_cursor ON incident_events(event_cursor);
```

---

### 2.10 output_watermarks

**Purpose:** Track cursors and baselines for incremental queries.

```sql
CREATE TABLE IF NOT EXISTS output_watermarks (
    -- Identity
    watermark_type TEXT NOT NULL,  -- events, alerts, incidents
    scope TEXT NOT NULL,           -- global, session:name, agent:id

    -- Cursor
    last_cursor INTEGER NOT NULL DEFAULT 0,
    last_ts TIMESTAMP,

    -- Baseline
    baseline_cursor INTEGER,
    baseline_ts TIMESTAMP,
    baseline_hash TEXT,  -- Hash of baseline state for drift detection

    -- Metadata
    consumer TEXT,  -- Who created this watermark
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    PRIMARY KEY (watermark_type, scope)
);

CREATE INDEX IF NOT EXISTS idx_watermarks_type ON output_watermarks(watermark_type);
CREATE INDEX IF NOT EXISTS idx_watermarks_updated ON output_watermarks(updated_at);
```

---

## 3. Migration Plan

### 3.1 Migration File: 007_runtime.sql

```sql
-- NTM State Store: Runtime Projection Layer
-- Version: 007
-- Description: Creates tables for runtime projections, source health, attention events, and incidents
-- Bead: bd-j9jo3.2.1

-- Runtime session projections
CREATE TABLE IF NOT EXISTS runtime_sessions (
    name TEXT PRIMARY KEY,
    label TEXT,
    project_path TEXT,
    attached INTEGER NOT NULL DEFAULT 0,
    window_count INTEGER NOT NULL DEFAULT 0,
    pane_count INTEGER NOT NULL DEFAULT 0,
    agent_count INTEGER NOT NULL DEFAULT 0,
    active_agents INTEGER NOT NULL DEFAULT 0,
    idle_agents INTEGER NOT NULL DEFAULT 0,
    error_agents INTEGER NOT NULL DEFAULT 0,
    health_status TEXT NOT NULL DEFAULT 'unknown',
    health_reason TEXT,
    created_at TIMESTAMP,
    last_attached_at TIMESTAMP,
    last_activity_at TIMESTAMP,
    collected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stale_after TIMESTAMP NOT NULL
);

-- (... all other CREATE TABLE and CREATE INDEX statements ...)
```

### 3.2 Rollout Strategy

1. **Phase 1:** Add migration, tables created but not populated
2. **Phase 2:** Add adapters that populate tables on scan/probe
3. **Phase 3:** Add robot surfaces that read from tables
4. **Phase 4:** Deprecate inline computation in favor of table reads

### 3.3 Backward Compatibility

- Existing tables (`sessions`, `agents`, `tasks`, etc.) remain unchanged
- Runtime tables are additive, not replacing
- Robot commands fall back to inline computation if runtime tables are empty

---

## 4. Query Patterns

### 4.1 Snapshot Bootstrap

```sql
-- Get full session inventory
SELECT * FROM runtime_sessions WHERE stale_after > datetime('now');

-- Get all agents for a session
SELECT * FROM runtime_agents WHERE session_name = ? AND stale_after > datetime('now');

-- Get attention summary
SELECT actionability, COUNT(*) as count
FROM attention_events
WHERE ts > datetime('now', '-1 hour')
GROUP BY actionability;
```

### 4.2 Incremental Polling

```sql
-- Get events since cursor
SELECT * FROM attention_events
WHERE cursor > ?
ORDER BY cursor ASC
LIMIT 100;

-- Get latest cursor
SELECT MAX(cursor) FROM attention_events;
```

### 4.3 Health Checks

```sql
-- Check all sources healthy
SELECT source_name, available, healthy, reason
FROM source_health
WHERE healthy = 0 OR available = 0;

-- Check freshness
SELECT name, health_status, collected_at,
       (julianday('now') - julianday(stale_after)) * 86400 as seconds_stale
FROM runtime_sessions
WHERE stale_after < datetime('now');
```

---

## 5. Retention and Cleanup

### 5.1 Event GC

```sql
-- Delete expired events (run periodically)
DELETE FROM attention_events WHERE expires_at < datetime('now');

-- Keep at least 1000 events
DELETE FROM attention_events
WHERE cursor < (SELECT MAX(cursor) - 1000 FROM attention_events)
AND expires_at < datetime('now');
```

### 5.2 Projection Rebuild

```sql
-- Clear stale projections only after they have already been unreadable for a grace window
DELETE FROM runtime_sessions WHERE stale_after < datetime('now', '-5 minutes');
DELETE FROM runtime_agents WHERE stale_after < datetime('now', '-5 minutes');
DELETE FROM runtime_work WHERE stale_after < datetime('now', '-5 minutes');
DELETE FROM runtime_coordination WHERE stale_after < datetime('now', '-5 minutes');
DELETE FROM runtime_quota WHERE stale_after < datetime('now', '-5 minutes');
DELETE FROM runtime_handoff WHERE stale_after < datetime('now', '-5 minutes');
DELETE FROM source_health WHERE last_check_at < datetime('now', '-24 hours');
```

### 5.3 Operational Guardrails

- Snapshot tables (`runtime_sessions`, `runtime_agents`, `runtime_work`, `runtime_coordination`, `runtime_handoff`, `runtime_quota`) are read with `stale_after > now` and GC'd only after an additional 5 minute grace window.
- `source_health` is bounded diagnostic history, not an append-only log. Keep 24 hours and prune anything older.
- `attention_events` stay append-only on the hot path and prune strictly by `expires_at`; routine readers should use cursor and timestamp indexes rather than full scans.
- Periodic maintenance should call `RunGC(DefaultRuntimeGCConfig())` about every 5 minutes.
- Projection refreshes should stay keyed-snapshot based: update current rows, delete rows that disappeared from normalized input, and avoid inventing history-table semantics for routine status polling.

---

## 6. References

- [Projection Section Model](projection-section-model.md) — Section definitions
- [Robot Surface Taxonomy](robot-surface-taxonomy.md) — Lane ownership
- [Attention Feed Contract](attention-feed-contract.md) — Event semantics
- `internal/state/migrations/` — Existing migrations

---

## 8. Audit Trail Tables (bd-j9jo3.2.5)

### 8.1 audit_events

**Purpose:** Append-only log of audit-worthy state changes with actor attribution.

```sql
CREATE TABLE IF NOT EXISTS audit_events (
    -- Identity
    id INTEGER PRIMARY KEY AUTOINCREMENT,

    -- Timestamp
    ts TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Actor attribution
    actor_type TEXT NOT NULL,  -- agent, user, system, api
    actor_id TEXT,             -- Agent name, user ID, API key prefix
    actor_origin TEXT,         -- IP, session, pane, request source

    -- Request correlation
    request_id TEXT,           -- UUID for request tracing
    correlation_id TEXT,       -- Cross-system correlation

    -- Event classification
    category TEXT NOT NULL,    -- attention, actuation, incident, disclosure
    event_type TEXT NOT NULL,  -- ack, pin, snooze, override, mute, resolve, etc.
    severity TEXT NOT NULL DEFAULT 'info',  -- info, warning, error

    -- Affected entity
    entity_type TEXT NOT NULL, -- session, agent, bead, alert, incident
    entity_id TEXT NOT NULL,

    -- State change
    previous_state TEXT,       -- JSON of previous state (optional)
    new_state TEXT,            -- JSON of new state (optional)
    change_summary TEXT NOT NULL, -- Human-readable summary

    -- Decision context
    reason TEXT,               -- User-provided reason
    evidence TEXT,             -- JSON of supporting evidence
    disclosure_state TEXT,     -- visible, preview_only, redacted, withheld

    -- Retention
    retention_class TEXT NOT NULL DEFAULT 'standard',  -- standard, extended, permanent
    expires_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_events(ts);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_events(actor_type, actor_id);
CREATE INDEX IF NOT EXISTS idx_audit_request ON audit_events(request_id);
CREATE INDEX IF NOT EXISTS idx_audit_correlation ON audit_events(correlation_id);
CREATE INDEX IF NOT EXISTS idx_audit_category ON audit_events(category);
CREATE INDEX IF NOT EXISTS idx_audit_entity ON audit_events(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_audit_expires ON audit_events(expires_at);
```

**Retention classes:**
- `standard`: 7 days (routine operations)
- `extended`: 30 days (significant state changes)
- `permanent`: Never expires (security-relevant, compliance events)

---

### 8.2 audit_actors

**Purpose:** Actor identity registry for correlation and display.

```sql
CREATE TABLE IF NOT EXISTS audit_actors (
    -- Identity
    actor_type TEXT NOT NULL,  -- agent, user, system, api
    actor_id TEXT NOT NULL,

    -- Display
    display_name TEXT,
    description TEXT,

    -- Metadata
    first_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    event_count INTEGER NOT NULL DEFAULT 0,

    -- Origin tracking
    known_origins TEXT,  -- JSON array of seen origins

    PRIMARY KEY (actor_type, actor_id)
);

CREATE INDEX IF NOT EXISTS idx_audit_actors_last_seen ON audit_actors(last_seen_at);
```

---

### 8.3 audit_decision_log

**Purpose:** Compact summary of operator decisions for quick lookup.

```sql
CREATE TABLE IF NOT EXISTS audit_decision_log (
    -- Identity
    id INTEGER PRIMARY KEY AUTOINCREMENT,

    -- Decision
    decision_type TEXT NOT NULL,  -- ack, pin, snooze, override, mute, resolve, disclose, redact
    decision_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Actor
    actor_type TEXT NOT NULL,
    actor_id TEXT,

    -- Target
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,

    -- Context
    reason TEXT,
    expires_at TIMESTAMP,  -- For time-limited decisions like snooze

    -- Reference to full audit event
    audit_event_id INTEGER,

    FOREIGN KEY (audit_event_id) REFERENCES audit_events(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_decision_type ON audit_decision_log(decision_type);
CREATE INDEX IF NOT EXISTS idx_decision_entity ON audit_decision_log(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_decision_actor ON audit_decision_log(actor_type, actor_id);
CREATE INDEX IF NOT EXISTS idx_decision_at ON audit_decision_log(decision_at);
CREATE INDEX IF NOT EXISTS idx_decision_expires ON audit_decision_log(expires_at);
```

---

### 8.4 Audit API Patterns

```go
// AuditEvent represents a single audit record
type AuditEvent struct {
    ID            int64     `json:"id"`
    Timestamp     time.Time `json:"ts"`
    ActorType     string    `json:"actor_type"`
    ActorID       string    `json:"actor_id,omitempty"`
    ActorOrigin   string    `json:"actor_origin,omitempty"`
    RequestID     string    `json:"request_id,omitempty"`
    CorrelationID string    `json:"correlation_id,omitempty"`
    Category      string    `json:"category"`
    EventType     string    `json:"event_type"`
    Severity      string    `json:"severity"`
    EntityType    string    `json:"entity_type"`
    EntityID      string    `json:"entity_id"`
    PrevState     string    `json:"previous_state,omitempty"`
    NewState      string    `json:"new_state,omitempty"`
    ChangeSummary string    `json:"change_summary"`
    Reason        string    `json:"reason,omitempty"`
    Evidence      string    `json:"evidence,omitempty"`
    Disclosure    string    `json:"disclosure_state,omitempty"`
}

// RecordAuditEvent writes an audit event and returns its ID
func RecordAuditEvent(ctx context.Context, event AuditEvent) (int64, error)

// GetAuditHistory returns audit events for an entity
func GetAuditHistory(ctx context.Context, entityType, entityID string, limit int) ([]AuditEvent, error)

// GetActorActivity returns recent activity for an actor
func GetActorActivity(ctx context.Context, actorType, actorID string, since time.Time) ([]AuditEvent, error)

// GetRequestTrace returns all events for a request ID
func GetRequestTrace(ctx context.Context, requestID string) ([]AuditEvent, error)
```

---

### 8.5 Audit Event Categories

| Category | Events | When Logged |
|----------|--------|-------------|
| `attention` | `view`, `ack`, `pin`, `unpin`, `snooze`, `dismiss` | Operator interacts with attention items |
| `actuation` | `send`, `interrupt`, `spawn`, `restart`, `route`, `assign` | Robot command executed |
| `incident` | `create`, `escalate`, `acknowledge`, `investigate`, `resolve`, `mute` | Incident lifecycle changes |
| `disclosure` | `redact`, `reveal`, `export`, `share` | Sensitive content visibility changes |

---

### 8.6 Retention and Cleanup

```sql
-- Standard cleanup (run periodically)
DELETE FROM audit_events
WHERE retention_class = 'standard' AND expires_at < datetime('now');

-- Extended cleanup
DELETE FROM audit_events
WHERE retention_class = 'extended' AND expires_at < datetime('now');

-- Never delete 'permanent' events automatically

-- Compact decision log (keep last 1000 per entity)
DELETE FROM audit_decision_log
WHERE id NOT IN (
    SELECT id FROM audit_decision_log
    ORDER BY decision_at DESC
    LIMIT 10000
);
```

---

## 9. References

- [Projection Section Model](projection-section-model.md) — Section definitions
- [Robot Surface Taxonomy](robot-surface-taxonomy.md) — Lane ownership
- [Attention Feed Contract](attention-feed-contract.md) — Event semantics
- `internal/state/migrations/` — Existing migrations

---

*End of runtime schema design. Implementation proceeds in bd-j9jo3.2.2 (implement runtime store APIs).*
