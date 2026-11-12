# SQLite Runtime Tables Design

> **Status: SUPERSEDED BY [`internal/state/migrations/`](../internal/state/migrations/) (executable DDL truth) and [`docs/runtime-schema-design.md`](runtime-schema-design.md).**
> Retained in-place for historical `bd-j9jo3.2.1` design provenance per `AGENTS.md` Rule 1.

**Status:** SUPERSEDED
**Bead:** bd-j9jo3.2.1
**Created:** 2026-03-22
**Part of:** bd-j9jo3 (robot-redesign epic)
**Depends on:** projection-section-model.md (bd-j9jo3.1.2), freshness-contract.md (bd-j9jo3.1.4)

---

## 1. Design Principles

### 1.1 Separation of Truth

| Layer | Source of Truth | Persistence |
|-------|-----------------|-------------|
| **Tmux state** | tmux server | Ephemeral (query live) |
| **Runtime projections** | SQLite | Durable (survives restart) |
| **Source data** | External tools | Cached in projections |

Runtime projections are **derived**, not **authoritative**. On restart, projections
are rebuilt from sources. SQLite provides:
- Restart resilience (no cold bootstrap delay)
- Event replay (cursor-based attention feed)
- Incident persistence (promoted alerts survive restart)

### 1.2 Schema Constraints

- **SQLite only**: No external databases (DuckDB, Postgres)
- **Single file**: `~/.local/share/ntm/runtime.db`
- **WAL mode**: Concurrent readers, single writer
- **No triggers**: All logic in Go code
- **Explicit migrations**: Version-tracked schema changes

### 1.3 Retention Boundaries

| Table | Retention | Rationale |
|-------|-----------|-----------|
| `runtime_sessions` | Until session deleted | Track known sessions |
| `runtime_agents` | Until agent removed | Track agent lifecycle |
| `source_health` | 24 hours | Diagnostic history |
| `attention_events` | 1 hour (configurable) | Event replay window |
| `incidents` | 7 days | Operator review window |
| `output_watermarks` | Until session deleted | Baseline tracking |

---

## 2. Table Definitions

### 2.1 runtime_sessions

Projection of known tmux sessions.

```sql
CREATE TABLE runtime_sessions (
    -- Identity
    session_id    TEXT PRIMARY KEY,  -- ntm session name
    label         TEXT,              -- Multi-session label (nullable)
    project_dir   TEXT NOT NULL,

    -- State
    attached      INTEGER NOT NULL DEFAULT 0,  -- Boolean
    window_count  INTEGER NOT NULL DEFAULT 1,
    pane_count    INTEGER NOT NULL DEFAULT 1,

    -- Timestamps
    created_at    TEXT NOT NULL,     -- RFC3339
    last_seen_at  TEXT NOT NULL,     -- RFC3339, updated on each scan
    deleted_at    TEXT,              -- RFC3339, soft delete

    -- Health
    health_score  REAL,              -- 0.0-1.0
    health_status TEXT,              -- healthy, degraded, critical

    -- Provenance
    provenance    TEXT NOT NULL DEFAULT 'live',
    collected_at  TEXT NOT NULL
);

CREATE INDEX idx_sessions_project ON runtime_sessions(project_dir);
CREATE INDEX idx_sessions_deleted ON runtime_sessions(deleted_at) WHERE deleted_at IS NOT NULL;
```

### 2.2 runtime_agents

Projection of agents within sessions.

```sql
CREATE TABLE runtime_agents (
    -- Identity
    agent_id      TEXT PRIMARY KEY,  -- session_id:pane_index
    session_id    TEXT NOT NULL REFERENCES runtime_sessions(session_id) ON DELETE CASCADE,
    pane_id       TEXT NOT NULL,     -- window.pane format
    pane_index    INTEGER NOT NULL,
    agent_type    TEXT NOT NULL,     -- claude, codex, antigravity, gemini (legacy), user

    -- State
    state         TEXT NOT NULL DEFAULT 'unknown',  -- idle, busy, error, compacting, unknown
    state_reason  TEXT,
    state_since   TEXT,              -- RFC3339

    -- Context
    context_used    INTEGER,         -- Estimated tokens used
    context_max     INTEGER,         -- Context limit
    context_percent REAL,            -- Usage percentage
    compacting      INTEGER DEFAULT 0,

    -- Work
    current_bead  TEXT,              -- Bead ID if working

    -- Timestamps
    last_output_at TEXT,             -- RFC3339
    last_seen_at   TEXT NOT NULL,    -- RFC3339

    -- Provenance
    provenance    TEXT NOT NULL DEFAULT 'live',
    collected_at  TEXT NOT NULL
);

CREATE INDEX idx_agents_session ON runtime_agents(session_id);
CREATE INDEX idx_agents_type ON runtime_agents(agent_type);
CREATE INDEX idx_agents_state ON runtime_agents(state);
CREATE INDEX idx_agents_bead ON runtime_agents(current_bead) WHERE current_bead IS NOT NULL;
```

### 2.3 runtime_work

Projection of beads/work state. Cached from br/bv queries.

```sql
CREATE TABLE runtime_work (
    -- Singleton row (only one)
    id            INTEGER PRIMARY KEY CHECK (id = 1),

    -- Counts
    total         INTEGER NOT NULL DEFAULT 0,
    open          INTEGER NOT NULL DEFAULT 0,
    ready         INTEGER NOT NULL DEFAULT 0,
    in_progress   INTEGER NOT NULL DEFAULT 0,
    blocked       INTEGER NOT NULL DEFAULT 0,

    -- Health
    stale_count   INTEGER NOT NULL DEFAULT 0,
    cycle_count   INTEGER NOT NULL DEFAULT 0,
    recently_closed INTEGER NOT NULL DEFAULT 0,

    -- Provenance
    provenance    TEXT NOT NULL DEFAULT 'live',
    collected_at  TEXT NOT NULL
);

-- Ready queue (top N ready items)
CREATE TABLE runtime_work_ready (
    bead_id       TEXT PRIMARY KEY,
    title         TEXT NOT NULL,
    priority      INTEGER NOT NULL,
    bead_type     TEXT NOT NULL,
    position      INTEGER NOT NULL,  -- Order in queue
    collected_at  TEXT NOT NULL
);

CREATE INDEX idx_work_ready_position ON runtime_work_ready(position);

-- In-flight work (claimed items)
CREATE TABLE runtime_work_inflight (
    bead_id       TEXT PRIMARY KEY,
    title         TEXT NOT NULL,
    agent_id      TEXT,              -- session:pane if known
    started_at    TEXT NOT NULL,
    collected_at  TEXT NOT NULL
);
```

### 2.4 runtime_coordination

Projection of mail and reservation state.

```sql
CREATE TABLE runtime_coordination (
    -- Singleton row
    id            INTEGER PRIMARY KEY CHECK (id = 1),

    -- Mail counts
    mail_unread     INTEGER NOT NULL DEFAULT 0,
    mail_urgent     INTEGER NOT NULL DEFAULT 0,
    mail_ack_req    INTEGER NOT NULL DEFAULT 0,
    mail_threads    INTEGER NOT NULL DEFAULT 0,

    -- Conflict counts
    conflict_count  INTEGER NOT NULL DEFAULT 0,

    -- Provenance
    provenance    TEXT NOT NULL DEFAULT 'live',
    collected_at  TEXT NOT NULL
);

-- Recent mail (top N)
CREATE TABLE runtime_mail_recent (
    mail_id       TEXT PRIMARY KEY,
    from_agent    TEXT NOT NULL,
    subject       TEXT NOT NULL,
    urgent        INTEGER NOT NULL DEFAULT 0,
    received_at   TEXT NOT NULL,
    position      INTEGER NOT NULL,
    collected_at  TEXT NOT NULL
);

CREATE INDEX idx_mail_position ON runtime_mail_recent(position);

-- Active reservations
CREATE TABLE runtime_reservations (
    reservation_id TEXT PRIMARY KEY,
    agent         TEXT NOT NULL,
    pattern       TEXT NOT NULL,
    exclusive     INTEGER NOT NULL DEFAULT 0,
    expires_at    TEXT NOT NULL,
    reason        TEXT,
    collected_at  TEXT NOT NULL
);

CREATE INDEX idx_reservations_agent ON runtime_reservations(agent);
CREATE INDEX idx_reservations_expires ON runtime_reservations(expires_at);

-- Active conflicts
CREATE TABLE runtime_conflicts (
    conflict_id   TEXT PRIMARY KEY,
    conflict_type TEXT NOT NULL,     -- file_conflict, reservation_conflict
    files         TEXT,              -- JSON array
    agents        TEXT NOT NULL,     -- JSON array
    detected_at   TEXT NOT NULL,
    resolved      INTEGER NOT NULL DEFAULT 0,
    collected_at  TEXT NOT NULL
);

CREATE INDEX idx_conflicts_resolved ON runtime_conflicts(resolved);
```

### 2.5 runtime_quota

Projection of API quota state.

```sql
CREATE TABLE runtime_quota (
    -- Identity
    provider      TEXT PRIMARY KEY,  -- anthropic, openai, google

    -- Usage
    used_tokens     INTEGER NOT NULL DEFAULT 0,
    limit_tokens    INTEGER,
    usage_percent   REAL,
    reset_at        TEXT,            -- RFC3339

    -- Status
    status        TEXT NOT NULL DEFAULT 'healthy',  -- healthy, warning, exceeded
    remaining_sec INTEGER,

    -- Provenance
    provenance    TEXT NOT NULL DEFAULT 'live',
    collected_at  TEXT NOT NULL
);
```

### 2.6 source_health

Historical source health records (from freshness contract).

```sql
CREATE TABLE source_health (
    -- Identity
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id     TEXT NOT NULL,     -- tmux, beads, mail, etc.

    -- Health
    status        TEXT NOT NULL,     -- healthy, degraded, unavailable
    error         TEXT,
    error_code    TEXT,

    -- Freshness
    provenance    TEXT NOT NULL,     -- live, cached, stale, derived, unavailable
    freshness_sec INTEGER NOT NULL,

    -- Degraded features (JSON array)
    degraded_features TEXT,

    -- Recovery
    retry_after_sec INTEGER,
    hint          TEXT,

    -- Timestamps
    collected_at  TEXT NOT NULL,     -- RFC3339
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_source_health_source ON source_health(source_id);
CREATE INDEX idx_source_health_time ON source_health(collected_at);
CREATE INDEX idx_source_health_status ON source_health(status) WHERE status != 'healthy';
```

### 2.7 attention_events

Event store for attention feed (from attention-feed-contract.md).

```sql
CREATE TABLE attention_events (
    -- Identity
    cursor        TEXT PRIMARY KEY,  -- evt_<timestamp_nanos>

    -- Envelope
    ts            TEXT NOT NULL,     -- RFC3339Nano
    session_id    TEXT,              -- Nullable for global events
    pane_id       TEXT,              -- Nullable for non-pane events

    -- Classification
    category      TEXT NOT NULL,     -- agent, session, bead, mail, alert, conflict, health, system
    event_type    TEXT NOT NULL,     -- state_change, output_detected, etc.
    source        TEXT NOT NULL,     -- activity_detector, health_monitor, etc.

    -- Actionability
    actionability TEXT NOT NULL,     -- background, interesting, action_required
    severity      TEXT NOT NULL,     -- debug, info, warning, error, critical

    -- Content
    summary       TEXT NOT NULL,
    details       TEXT,              -- JSON object
    next_actions  TEXT,              -- JSON array

    -- Timestamps
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_events_ts ON attention_events(ts);
CREATE INDEX idx_events_session ON attention_events(session_id) WHERE session_id IS NOT NULL;
CREATE INDEX idx_events_category ON attention_events(category);
CREATE INDEX idx_events_actionability ON attention_events(actionability);
CREATE INDEX idx_events_created ON attention_events(created_at);
```

### 2.8 incidents

Promoted alerts that require operator attention.

```sql
CREATE TABLE incidents (
    -- Identity
    incident_id   TEXT PRIMARY KEY,  -- inc_<uuid>

    -- Source
    source_type   TEXT NOT NULL,     -- alert, event, manual
    source_id     TEXT,              -- Original alert/event ID

    -- Classification
    category      TEXT NOT NULL,     -- agent, session, quota, conflict, system
    severity      TEXT NOT NULL,     -- warning, error, critical

    -- Content
    title         TEXT NOT NULL,
    description   TEXT,
    details       TEXT,              -- JSON object

    -- State
    state         TEXT NOT NULL DEFAULT 'open',  -- open, acknowledged, resolved, dismissed
    acknowledged_by TEXT,
    acknowledged_at TEXT,
    resolved_by   TEXT,
    resolved_at   TEXT,
    resolution    TEXT,              -- How it was resolved

    -- Scope
    session_id    TEXT,
    agent_id      TEXT,

    -- Related
    related_events TEXT,             -- JSON array of cursor IDs

    -- Timestamps
    detected_at   TEXT NOT NULL,     -- RFC3339
    updated_at    TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_incidents_state ON incidents(state);
CREATE INDEX idx_incidents_severity ON incidents(severity);
CREATE INDEX idx_incidents_session ON incidents(session_id) WHERE session_id IS NOT NULL;
CREATE INDEX idx_incidents_detected ON incidents(detected_at);
```

### 2.9 output_watermarks

Baseline tracking for diff/delta operations.

```sql
CREATE TABLE output_watermarks (
    -- Identity
    agent_id      TEXT PRIMARY KEY,  -- session:pane

    -- Watermark
    last_cursor   TEXT NOT NULL,     -- Last event cursor processed
    last_output_hash TEXT,           -- Hash of last seen output
    last_output_lines INTEGER,       -- Line count at last capture

    -- Baseline
    baseline_cursor TEXT,            -- Cursor at baseline establishment
    baseline_at   TEXT,              -- RFC3339

    -- Timestamps
    updated_at    TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
```

### 2.10 schema_migrations

Migration tracking.

```sql
CREATE TABLE schema_migrations (
    version       INTEGER PRIMARY KEY,
    name          TEXT NOT NULL,
    applied_at    TEXT NOT NULL DEFAULT (datetime('now')),
    checksum      TEXT              -- SHA256 of migration SQL
);
```

---

## 3. Migration Strategy

### 3.1 Migration Naming

Migrations are numbered and named:
```
001_initial_runtime_schema.sql
002_add_incidents_table.sql
003_add_source_health_history.sql
```

### 3.2 Migration Execution

```go
func Migrate(db *sql.DB) error {
    // 1. Create migrations table if not exists
    // 2. Get current version
    // 3. Apply pending migrations in order
    // 4. Record each applied migration
}
```

### 3.3 Rollback Policy

- **No automatic rollbacks**: Failed migrations abort, manual intervention required
- **Forward-only**: Migrations are additive, not reversible
- **Test before deploy**: Migrations tested against copy of production data

### 3.4 Initial Migration

The first migration (`001_initial_runtime_schema.sql`) creates all tables defined above.

---

## 4. Store APIs

### 4.1 Projection Operations

```go
type RuntimeStore interface {
    // Sessions
    UpsertSession(ctx context.Context, s *SessionProjection) error
    GetSession(ctx context.Context, id string) (*SessionProjection, error)
    ListSessions(ctx context.Context) ([]*SessionProjection, error)
    DeleteSession(ctx context.Context, id string) error

    // Agents
    UpsertAgent(ctx context.Context, a *AgentProjection) error
    GetAgent(ctx context.Context, id string) (*AgentProjection, error)
    ListAgentsBySession(ctx context.Context, sessionID string) ([]*AgentProjection, error)

    // Work
    UpdateWorkProjection(ctx context.Context, w *WorkProjection) error
    GetWorkProjection(ctx context.Context) (*WorkProjection, error)

    // Source Health
    RecordSourceHealth(ctx context.Context, h *SourceHealthEntry) error
    GetLatestSourceHealth(ctx context.Context) (map[string]*SourceHealthEntry, error)

    // Events
    InsertEvent(ctx context.Context, e *AttentionEvent) error
    GetEventsSince(ctx context.Context, cursor string, limit int) ([]*AttentionEvent, error)

    // Incidents
    CreateIncident(ctx context.Context, i *Incident) error
    UpdateIncident(ctx context.Context, id string, update IncidentUpdate) error
    ListIncidents(ctx context.Context, filter IncidentFilter) ([]*Incident, error)
}
```

### 4.2 Transaction Patterns

- **Projection updates**: Single transaction per source collection cycle
- **Event writes**: Write-ahead, eventual batch commit
- **Incident updates**: Immediate commit (operator-facing)

---

## 5. Retention Implementation

### 5.1 Pruning Jobs

Run periodically (e.g., every 5 minutes):

```go
func PruneExpired(db *sql.DB) error {
    now := time.Now()

    // Prune events older than 1 hour
    _, err := db.Exec(`
        DELETE FROM attention_events
        WHERE created_at < datetime('now', '-1 hour')
    `)

    // Prune source_health older than 24 hours
    _, err = db.Exec(`
        DELETE FROM source_health
        WHERE created_at < datetime('now', '-24 hours')
    `)

    // Prune resolved incidents older than 7 days
    _, err = db.Exec(`
        DELETE FROM incidents
        WHERE state IN ('resolved', 'dismissed')
        AND resolved_at < datetime('now', '-7 days')
    `)

    return err
}
```

### 5.2 Retention Configuration

```toml
[runtime.retention]
events_hours = 1
source_health_hours = 24
incidents_days = 7
```

---

## 6. Performance Considerations

### 6.1 Write Patterns

| Table | Write Frequency | Strategy |
|-------|-----------------|----------|
| `runtime_sessions` | ~1/min per session | Immediate |
| `runtime_agents` | ~1/min per agent | Immediate |
| `attention_events` | ~10/sec peak | Batch (100ms) |
| `source_health` | ~1/5sec per source | Immediate |
| `incidents` | ~1/hour | Immediate |

### 6.2 Read Patterns

| Operation | Expected Latency | Index Support |
|-----------|------------------|---------------|
| GetSession | <1ms | Primary key |
| ListSessions | <5ms | Full scan (small table) |
| GetEventsSince | <10ms | Cursor index |
| ListIncidents | <5ms | State + severity indexes |

### 6.3 WAL Configuration

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA cache_size = -64000;  -- 64MB cache
```

---

## 7. Relationship to Existing State

### 7.1 Existing Tables (in state.db)

ntm already has persistent state in `~/.local/share/ntm/state.db`:
- Session metadata
- Command history
- Checkpoint data

### 7.2 Separation Strategy

- **runtime.db**: New file for projection layer
- **state.db**: Existing file, unchanged
- **No foreign keys across databases**: All relationships via IDs

### 7.3 Migration Path

1. Create `runtime.db` alongside `state.db`
2. Populate projections from live sources
3. No data migration from `state.db` required

---

## 8. Non-Goals

This schema explicitly rejects:

1. **Event sourcing**: Events are ephemeral, not source of truth
2. **Full history**: Only recent state retained
3. **Cross-session aggregation**: Each session independent
4. **Complex queries**: Simple key lookups preferred
5. **Distributed locking**: Single-writer model sufficient

---

## 9. Related Documents

- **projection-section-model.md**: Defines Go structs this schema persists
- **freshness-contract.md**: Defines source_health semantics
- **attention-feed-contract.md**: Defines event envelope schema
- **robot-surface-taxonomy.md**: Defines which surfaces consume this data
