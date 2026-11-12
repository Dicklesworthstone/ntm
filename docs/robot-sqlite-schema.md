# Robot SQLite Schema Design

> **Status: SUPERSEDED BY [`internal/state/migrations/`](../internal/state/migrations/) (executable DDL truth) and [`docs/runtime-schema-design.md`](runtime-schema-design.md).**
> Retained in-place for historical `bd-j9jo3.2.1` design provenance per `AGENTS.md` Rule 1.

**Status:** SUPERSEDED
**Bead:** bd-j9jo3.2.1
**Created:** 2026-03-22
**Prerequisite:** docs/robot-section-model.md (bd-j9jo3.1.2)

---

## 1. Purpose

This document defines the SQLite schema for robot mode runtime state. The goal is to make the persistent layer intentional rather than letting tables accrete around the first implementation.

**Design Goal:** Durable projection substrate that survives restart, replay, and parity work without needing a second database.

**Anti-Goal:** Treating runtime projection state as equivalent to tmux truth.

---

## 2. Schema Architecture

### 2.1 Separation of Concerns

| Layer | Description | Volatility |
|-------|-------------|------------|
| **Source Truth** | Tmux, beads, mail servers | External, not owned by ntm |
| **Runtime Projection** | Cached/derived state from sources | Recomputable on restart |
| **Durable Events** | Attention journal, incidents | Survives restart |
| **Watermarks** | Cursors, baselines, checkpoints | Persists across sessions |

### 2.2 Table Naming Convention

```
robot_<domain>_<purpose>
```

Examples:
- `robot_sessions` - Session projection cache
- `robot_source_health` - Source freshness tracking
- `robot_attention_events` - Durable attention journal
- `robot_incidents` - Incident state
- `robot_watermarks` - Cursors and baselines

---

## 3. Core Tables

### 3.1 robot_sessions

Cached session state projected from tmux. Recomputed on startup.

```sql
CREATE TABLE IF NOT EXISTS robot_sessions (
    id                  TEXT PRIMARY KEY,  -- session name
    window              TEXT NOT NULL,
    attached            INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL,     -- RFC3339
    updated_at          TEXT NOT NULL,     -- RFC3339
    agent_count         INTEGER NOT NULL DEFAULT 0,
    user_pane_active    INTEGER NOT NULL DEFAULT 0,
    raw_json            TEXT               -- full tmux metadata
);

CREATE INDEX idx_robot_sessions_updated ON robot_sessions(updated_at);
```

### 3.2 robot_agents

Cached agent state projected from activity detection.

```sql
CREATE TABLE IF NOT EXISTS robot_agents (
    id                  TEXT PRIMARY KEY,  -- session:pane (e.g., "proj:0.2")
    session_id          TEXT NOT NULL REFERENCES robot_sessions(id) ON DELETE CASCADE,
    pane_index          INTEGER NOT NULL,
    pane_id             TEXT NOT NULL,     -- "0.2"
    agent_type          TEXT NOT NULL,     -- "claude", "codex", "antigravity", "gemini" (legacy)
    model               TEXT,
    state               TEXT NOT NULL,     -- "idle", "busy", "error", "compacting"
    state_changed_at    TEXT NOT NULL,     -- RFC3339
    context_pct         REAL,
    last_output_at      TEXT,              -- RFC3339
    updated_at          TEXT NOT NULL,     -- RFC3339
    raw_json            TEXT
);

CREATE INDEX idx_robot_agents_session ON robot_agents(session_id);
CREATE INDEX idx_robot_agents_state ON robot_agents(state);
CREATE INDEX idx_robot_agents_type ON robot_agents(agent_type);
```

### 3.3 robot_source_health

Tracks freshness and degraded state for each data source.

```sql
CREATE TABLE IF NOT EXISTS robot_source_health (
    source_name         TEXT PRIMARY KEY,  -- "sessions", "work", "coordination", etc.
    fresh               INTEGER NOT NULL DEFAULT 1,
    age_ms              INTEGER,
    collected_at        TEXT,              -- RFC3339
    degraded            INTEGER NOT NULL DEFAULT 0,
    degraded_reason     TEXT,
    degraded_since      TEXT,              -- RFC3339
    updated_at          TEXT NOT NULL      -- RFC3339
);

CREATE INDEX idx_robot_source_health_degraded ON robot_source_health(degraded);
```

### 3.4 robot_attention_events

Durable attention journal. Does NOT get wiped on restart.

```sql
CREATE TABLE IF NOT EXISTS robot_attention_events (
    cursor              TEXT PRIMARY KEY,  -- "evt_<timestamp_nanos>"
    ts                  TEXT NOT NULL,     -- RFC3339Nano
    category            TEXT NOT NULL,     -- "agent", "bead", "alert", "mail", etc.
    type                TEXT NOT NULL,     -- "state_change", "ready", "raised", etc.
    source              TEXT NOT NULL,     -- "activity_detector", "bead_tracker", etc.
    actionability       TEXT NOT NULL,     -- "background", "interesting", "action_required"
    severity            TEXT NOT NULL,     -- "debug", "info", "warning", "error", "critical"
    session_id          TEXT,              -- nullable for global events
    pane_id             TEXT,              -- nullable
    entity_id           TEXT,              -- bead ID, alert ID, etc.
    summary             TEXT NOT NULL,
    details_json        TEXT,              -- structured event-specific data
    next_actions_json   TEXT,              -- suggested follow-up commands
    acknowledged        INTEGER NOT NULL DEFAULT 0,
    acknowledged_at     TEXT,              -- RFC3339
    acknowledged_by     TEXT,              -- agent name or "operator"
    snoozed_until       TEXT,              -- RFC3339
    pinned              INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_attention_events_ts ON robot_attention_events(ts);
CREATE INDEX idx_attention_events_category ON robot_attention_events(category);
CREATE INDEX idx_attention_events_actionability ON robot_attention_events(actionability);
CREATE INDEX idx_attention_events_session ON robot_attention_events(session_id);
CREATE INDEX idx_attention_events_acked ON robot_attention_events(acknowledged);
CREATE INDEX idx_attention_events_snoozed ON robot_attention_events(snoozed_until) WHERE snoozed_until IS NOT NULL;
```

### 3.5 robot_incidents

Durable incident state for recurring operator pain.

```sql
CREATE TABLE IF NOT EXISTS robot_incidents (
    id                  TEXT PRIMARY KEY,  -- "inc_<timestamp>_<hash>"
    title               TEXT NOT NULL,
    severity            TEXT NOT NULL,     -- "minor", "major", "critical"
    state               TEXT NOT NULL,     -- "open", "acknowledged", "resolved"
    session_id          TEXT,
    created_at          TEXT NOT NULL,     -- RFC3339
    updated_at          TEXT NOT NULL,     -- RFC3339
    resolved_at         TEXT,              -- RFC3339
    resolved_by         TEXT,
    alert_count         INTEGER NOT NULL DEFAULT 1,
    first_alert_cursor  TEXT,              -- links to robot_attention_events
    last_alert_cursor   TEXT,
    metadata_json       TEXT
);

CREATE INDEX idx_incidents_state ON robot_incidents(state);
CREATE INDEX idx_incidents_severity ON robot_incidents(severity);
CREATE INDEX idx_incidents_session ON robot_incidents(session_id);
CREATE INDEX idx_incidents_created ON robot_incidents(created_at);
```

### 3.6 robot_incident_events

Timeline events for incident history.

```sql
CREATE TABLE IF NOT EXISTS robot_incident_events (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    incident_id         TEXT NOT NULL REFERENCES robot_incidents(id) ON DELETE CASCADE,
    ts                  TEXT NOT NULL,     -- RFC3339
    type                TEXT NOT NULL,     -- "created", "escalated", "acknowledged", "comment", "resolved"
    actor               TEXT,              -- agent name or "operator"
    details             TEXT,
    alert_cursor        TEXT               -- links to triggering attention event
);

CREATE INDEX idx_incident_events_incident ON robot_incident_events(incident_id);
CREATE INDEX idx_incident_events_ts ON robot_incident_events(ts);
```

### 3.7 robot_watermarks

Cursors, baselines, and checkpoints for replay/resync.

```sql
CREATE TABLE IF NOT EXISTS robot_watermarks (
    key                 TEXT PRIMARY KEY,  -- "latest_cursor", "snapshot_baseline", etc.
    value               TEXT NOT NULL,
    updated_at          TEXT NOT NULL      -- RFC3339
);
```

Standard keys:
- `latest_cursor`: Most recent event cursor
- `snapshot_baseline`: Cursor at last snapshot
- `oldest_cursor`: Oldest event still retained
- `retention_cutoff`: Events older than this may be pruned

### 3.8 robot_digest_cache

Cached digest summaries for token efficiency.

```sql
CREATE TABLE IF NOT EXISTS robot_digest_cache (
    cursor_from         TEXT NOT NULL,
    cursor_to           TEXT NOT NULL,
    window_minutes      INTEGER NOT NULL,
    computed_at         TEXT NOT NULL,     -- RFC3339
    digest_json         TEXT NOT NULL,
    PRIMARY KEY (cursor_from, cursor_to)
);

CREATE INDEX idx_digest_cache_window ON robot_digest_cache(window_minutes);
```

---

## 4. Auxiliary Tables

### 4.1 robot_quota_samples

Point-in-time quota snapshots.

```sql
CREATE TABLE IF NOT EXISTS robot_quota_samples (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    ts                  TEXT NOT NULL,     -- RFC3339
    provider            TEXT NOT NULL,     -- "anthropic", "openai", "google", "rch"
    used_pct            REAL,
    resets_at           TEXT,              -- RFC3339
    warning             INTEGER NOT NULL DEFAULT 0,
    exceeded            INTEGER NOT NULL DEFAULT 0,
    raw_json            TEXT
);

CREATE INDEX idx_quota_samples_ts ON robot_quota_samples(ts);
CREATE INDEX idx_quota_samples_provider ON robot_quota_samples(provider);
```

### 4.2 robot_alert_history

Alert lifecycle for audit trail.

```sql
CREATE TABLE IF NOT EXISTS robot_alert_history (
    id                  TEXT PRIMARY KEY,
    alert_cursor        TEXT NOT NULL REFERENCES robot_attention_events(cursor),
    raised_at           TEXT NOT NULL,     -- RFC3339
    resolved_at         TEXT,              -- RFC3339
    promoted_to_incident TEXT,             -- incident ID if promoted
    suppressed          INTEGER NOT NULL DEFAULT 0,
    suppressed_reason   TEXT
);

CREATE INDEX idx_alert_history_raised ON robot_alert_history(raised_at);
CREATE INDEX idx_alert_history_incident ON robot_alert_history(promoted_to_incident) WHERE promoted_to_incident IS NOT NULL;
```

---

## 5. Migration Strategy

### 5.1 Migration Numbering

```
robot_migrations/
  001_create_sessions.sql
  002_create_agents.sql
  003_create_source_health.sql
  004_create_attention_events.sql
  005_create_incidents.sql
  006_create_watermarks.sql
  007_create_digest_cache.sql
  008_create_quota_samples.sql
  009_create_alert_history.sql
```

### 5.2 Migration Runner

```go
func RunMigrations(db *sql.DB) error {
    // Read existing migration level from robot_schema_version table
    // Apply missing migrations in order
    // Record new migration level
}
```

### 5.3 Schema Version Table

```sql
CREATE TABLE IF NOT EXISTS robot_schema_version (
    version             INTEGER PRIMARY KEY,
    applied_at          TEXT NOT NULL,     -- RFC3339
    description         TEXT
);
```

---

## 6. Data Lifecycle

### 6.1 Recomputable vs. Durable

| Table | Lifecycle | On Restart |
|-------|-----------|------------|
| robot_sessions | Recomputable | Recompute from tmux |
| robot_agents | Recomputable | Recompute from activity detection |
| robot_source_health | Recomputable | Recompute from adapters |
| robot_attention_events | Durable | Preserve |
| robot_incidents | Durable | Preserve |
| robot_incident_events | Durable | Preserve |
| robot_watermarks | Durable | Preserve |
| robot_digest_cache | Derived | Prune stale, recompute on demand |
| robot_quota_samples | Time-series | Retain with policy |
| robot_alert_history | Audit | Retain with policy |

### 6.2 Retention Policies

| Table | Default Retention |
|-------|-------------------|
| robot_attention_events | 7 days |
| robot_incidents | 90 days (resolved) |
| robot_quota_samples | 30 days |
| robot_alert_history | 30 days |
| robot_digest_cache | 24 hours |

### 6.3 Pruning

```sql
-- Run periodically
DELETE FROM robot_attention_events 
WHERE ts < datetime('now', '-7 days')
  AND acknowledged = 1
  AND NOT EXISTS (SELECT 1 FROM robot_incidents WHERE first_alert_cursor = cursor OR last_alert_cursor = cursor);

DELETE FROM robot_quota_samples 
WHERE ts < datetime('now', '-30 days');

DELETE FROM robot_digest_cache 
WHERE computed_at < datetime('now', '-24 hours');
```

---

## 7. Index Strategy

### 7.1 Primary Access Patterns

| Pattern | Index |
|---------|-------|
| Get session by name | `robot_sessions(id)` PK |
| List agents by session | `robot_agents(session_id)` |
| Filter agents by state | `robot_agents(state)` |
| Events by cursor range | `robot_attention_events(cursor)` PK |
| Events by actionability | `robot_attention_events(actionability)` |
| Events by session | `robot_attention_events(session_id)` |
| Open incidents | `robot_incidents(state)` |
| Incidents by session | `robot_incidents(session_id)` |
| Incident timeline | `robot_incident_events(incident_id)` |

### 7.2 Covering Indexes

For hot paths, consider covering indexes:

```sql
-- Attention digest hot path
CREATE INDEX idx_attention_digest ON robot_attention_events(
    actionability, category, ts
) WHERE acknowledged = 0;
```

---

## 8. Relationship to Existing State

ntm already has SQLite state in `~/.config/ntm/state.db`. The robot tables:

1. **Live in the same database** - No separate DB file
2. **Use `robot_` prefix** - Clearly namespaced from existing tables
3. **Do not replace tmux truth** - Sessions/agents are projections, not sources
4. **Coexist with beads** - Beads remain in `.beads/`, not duplicated

---

## 9. API Contract

### 9.1 Store Interface

```go
type RobotStore interface {
    // Sessions
    UpsertSession(session *Session) error
    GetSession(id string) (*Session, error)
    ListSessions() ([]*Session, error)
    
    // Agents
    UpsertAgent(agent *Agent) error
    GetAgent(id string) (*Agent, error)
    ListAgentsBySession(sessionID string) ([]*Agent, error)
    
    // Source Health
    UpdateSourceHealth(source string, status *SourceStatus) error
    GetSourceHealth(source string) (*SourceStatus, error)
    ListDegradedSources() ([]string, error)
    
    // Attention Events
    InsertEvent(event *AttentionEvent) error
    GetEventsByCursor(cursor string, limit int) ([]*AttentionEvent, error)
    GetEventsByActionability(actionability string, limit int) ([]*AttentionEvent, error)
    AcknowledgeEvent(cursor string, by string) error
    SnoozeEvent(cursor string, until time.Time) error
    
    // Incidents
    CreateIncident(incident *Incident) error
    GetIncident(id string) (*Incident, error)
    ListOpenIncidents() ([]*Incident, error)
    UpdateIncidentState(id string, state string) error
    AddIncidentEvent(incidentID string, event *IncidentEvent) error
    
    // Watermarks
    GetWatermark(key string) (string, error)
    SetWatermark(key, value string) error
}
```

### 9.2 Transaction Boundaries

- Event insert + watermark update: Single transaction
- Incident create + first event: Single transaction
- Session/agent batch update: Single transaction
- Source health updates: Individual transactions OK

---

## 10. Testing Requirements

### 10.1 Table Tests

- Create each table in empty database
- Verify all indexes exist
- Test foreign key cascades
- Test unique constraints

### 10.2 Migration Tests

- Apply migrations 1-N on empty database
- Verify final schema matches expected
- Test idempotent re-application

### 10.3 Store API Tests

- CRUD operations for each table
- Transaction atomicity
- Concurrent access
- Retention pruning

---

## Appendix A: Full Schema DDL

```sql
-- See individual table definitions above
-- Combined into robot_migrations/001_full_schema.sql
```

---

## Appendix B: Changelog

- **2026-03-22:** Initial schema design (bd-j9jo3.2.1)

---

*Reference: bd-j9jo3.2.1*
