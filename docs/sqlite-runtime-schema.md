# SQLite Runtime Schema Design

> **Status: SUPERSEDED BY [`internal/state/migrations/`](../internal/state/migrations/) (executable DDL truth) and [`docs/runtime-schema-design.md`](runtime-schema-design.md).**
> Retained in-place for historical `bd-j9jo3.2.1` design provenance per `AGENTS.md` Rule 1.

**Status:** SUPERSEDED
**Bead:** bd-j9jo3.2.1
**Created:** 2026-03-22
**Part of:** bd-j9jo3 (robot-redesign epic)
**Depends on:**
- projection-section-model.md (bd-j9jo3.1.2)
- freshness-degraded-state-contract.md (bd-j9jo3.1.4)

---

## 1. Design Principles

### 1.1 Source of Truth vs. Runtime Projection

ntm already has durable source-of-truth tables:
- `sessions`, `agents`, `tasks` - Orchestration state
- `reservations`, `approvals` - Coordination state
- `event_log` - Raw event history

The **runtime projection layer** adds:
- Derived state for fast robot output
- Freshness metadata for degradation handling
- Attention events for the operator loop
- Incidents for recurring issue tracking
- Output watermarks for restart-safe deltas

### 1.2 Naming Convention

All new tables are prefixed with `rt_` (runtime) to distinguish from source-of-truth:

```
rt_source_health      -- Data freshness metadata
rt_attention_events   -- Attention journal entries
rt_incidents          -- Recurring issue patterns
rt_output_watermarks  -- Cursor/baseline tracking
rt_projections        -- Cached section renders
```

### 1.3 Lifecycle

Runtime tables are:
- **Rebuilt on startup** from source tables + collectors
- **Updated incrementally** during operation
- **Pruned automatically** based on retention policies
- **Safe to delete** without losing source-of-truth

---

## 2. Table Definitions

### 2.1 rt_source_health

Tracks freshness and degradation state per data source.

```sql
-- Migration: 007_runtime_projection.sql
CREATE TABLE IF NOT EXISTS rt_source_health (
    source TEXT PRIMARY KEY,           -- tmux, beads, mail, quota, etc.
    status TEXT NOT NULL,               -- fresh, stale, unavailable, unknown
    collected_at TEXT NOT NULL,         -- RFC3339 timestamp
    stale_after_sec INTEGER NOT NULL,   -- Threshold for staleness
    provenance TEXT NOT NULL,           -- live, cached, derived
    degraded_features TEXT,             -- JSON array of affected features
    last_error TEXT,
    last_error_at TEXT,
    derived_from TEXT,                  -- JSON array for derived sources
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_rt_source_health_status ON rt_source_health(status);
CREATE INDEX IF NOT EXISTS idx_rt_source_health_updated_at ON rt_source_health(updated_at);
```

**Usage:**
- Updated on every collection attempt (success or failure)
- Read by all robot surfaces to populate `source_health` section
- Never persisted across restarts (rebuilt from collectors)

### 2.2 rt_attention_events

Stores attention journal entries for the replay/triage lane.

```sql
CREATE TABLE IF NOT EXISTS rt_attention_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    cursor INTEGER NOT NULL UNIQUE,      -- Monotonic attention cursor
    ts TEXT NOT NULL,                    -- RFC3339Nano occurred_at
    session_name TEXT,                   -- Session scope (nullable for global)
    pane_id TEXT,                        -- Pane scope (window.pane format)
    category TEXT NOT NULL,              -- agent, bead, alert, mail, conflict, health, system
    event_type TEXT NOT NULL,            -- Category-specific type
    source TEXT NOT NULL,                -- Generating component
    actionability TEXT NOT NULL,         -- background, interesting, action_required
    severity TEXT NOT NULL,              -- debug, info, warning, error, critical
    summary TEXT NOT NULL,               -- One-line human-readable
    details TEXT,                        -- JSON object with event-specific data
    next_actions TEXT,                   -- JSON array of NextAction
    score REAL,                          -- Attention priority score (nullable)
    acked_at TEXT,                       -- When acknowledged (nullable)
    acked_by TEXT,                       -- Who acknowledged (nullable)
    snoozed_until TEXT,                  -- Snooze expiry (nullable)
    pinned INTEGER NOT NULL DEFAULT 0,   -- 0 = not pinned, 1 = pinned
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_rt_attention_cursor ON rt_attention_events(cursor);
CREATE INDEX IF NOT EXISTS idx_rt_attention_ts ON rt_attention_events(ts);
CREATE INDEX IF NOT EXISTS idx_rt_attention_session ON rt_attention_events(session_name);
CREATE INDEX IF NOT EXISTS idx_rt_attention_category ON rt_attention_events(category);
CREATE INDEX IF NOT EXISTS idx_rt_attention_actionability ON rt_attention_events(actionability);
CREATE INDEX IF NOT EXISTS idx_rt_attention_severity ON rt_attention_events(severity);
CREATE INDEX IF NOT EXISTS idx_rt_attention_acked ON rt_attention_events(acked_at);
CREATE INDEX IF NOT EXISTS idx_rt_attention_created_at ON rt_attention_events(created_at);
```

**Usage:**
- Written by event publishers (activity detector, health monitor, etc.)
- Read by `--robot-events` (cursor-based replay)
- Filtered/scored by `--robot-digest` and `--robot-attention`
- Pruned based on retention (default: 24 hours)

### 2.3 rt_incidents

Tracks recurring or escalated issues that persist beyond individual alerts.

```sql
CREATE TABLE IF NOT EXISTS rt_incidents (
    id TEXT PRIMARY KEY,                 -- UUID
    fingerprint TEXT NOT NULL UNIQUE,    -- Dedup key (hash of pattern)
    incident_type TEXT NOT NULL,         -- agent_crash_loop, persistent_error, etc.
    severity TEXT NOT NULL,              -- warning, error, critical
    summary TEXT NOT NULL,
    pattern TEXT,                        -- Description of recurring pattern
    session_name TEXT,                   -- Session scope (nullable for global)
    panes TEXT,                          -- JSON array of affected pane IDs
    opened_at TEXT NOT NULL,             -- RFC3339 first occurrence
    last_seen_at TEXT NOT NULL,          -- RFC3339 most recent occurrence
    occurrence_count INTEGER NOT NULL DEFAULT 1,
    resolved_at TEXT,                    -- RFC3339 when resolved (nullable)
    resolved_by TEXT,                    -- Who resolved (nullable)
    resolution_notes TEXT,               -- How it was resolved (nullable)
    related_alerts TEXT,                 -- JSON array of alert IDs
    next_actions TEXT,                   -- JSON array of NextAction
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_rt_incidents_fingerprint ON rt_incidents(fingerprint);
CREATE INDEX IF NOT EXISTS idx_rt_incidents_session ON rt_incidents(session_name);
CREATE INDEX IF NOT EXISTS idx_rt_incidents_severity ON rt_incidents(severity);
CREATE INDEX IF NOT EXISTS idx_rt_incidents_opened_at ON rt_incidents(opened_at);
CREATE INDEX IF NOT EXISTS idx_rt_incidents_resolved_at ON rt_incidents(resolved_at);
```

**Usage:**
- Created when alert pattern is detected (same fingerprint > N times in window)
- Updated on each occurrence (`occurrence_count++`, `last_seen_at` updated)
- Resolved manually or when condition clears for threshold duration
- Never auto-deleted (historical record)

### 2.4 rt_incident_alerts

Junction table linking incidents to their constituent alerts.

```sql
CREATE TABLE IF NOT EXISTS rt_incident_alerts (
    incident_id TEXT NOT NULL REFERENCES rt_incidents(id) ON DELETE CASCADE,
    alert_id TEXT NOT NULL,              -- References alerts in existing alert system
    added_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (incident_id, alert_id)
);

CREATE INDEX IF NOT EXISTS idx_rt_incident_alerts_alert ON rt_incident_alerts(alert_id);
```

### 2.5 rt_output_watermarks

Tracks output/activity baselines for restart-safe delta computation.

```sql
CREATE TABLE IF NOT EXISTS rt_output_watermarks (
    session_name TEXT NOT NULL,
    pane_id TEXT NOT NULL,
    watermark_type TEXT NOT NULL,        -- output_position, activity_state, context_estimate
    watermark_value TEXT NOT NULL,       -- Type-specific value (line count, state, tokens)
    computed_at TEXT NOT NULL,           -- RFC3339 when computed
    PRIMARY KEY (session_name, pane_id, watermark_type)
);

CREATE INDEX IF NOT EXISTS idx_rt_watermarks_session ON rt_output_watermarks(session_name);
CREATE INDEX IF NOT EXISTS idx_rt_watermarks_computed_at ON rt_output_watermarks(computed_at);
```

**Usage:**
- Written after each pane output capture
- Read on restart to compute deltas without full re-scan
- Pruned when session is deleted

### 2.6 rt_projections

Caches rendered section projections for fast repeated reads.

```sql
CREATE TABLE IF NOT EXISTS rt_projections (
    projection_key TEXT PRIMARY KEY,     -- e.g., "summary", "sessions:myproject"
    projection_type TEXT NOT NULL,       -- section type: summary, sessions, work, etc.
    scope TEXT,                          -- Session name for scoped projections
    data TEXT NOT NULL,                  -- JSON rendered projection
    computed_at TEXT NOT NULL,           -- RFC3339 when computed
    valid_until TEXT,                    -- RFC3339 cache expiry (nullable = no expiry)
    dependencies TEXT                    -- JSON array of source names that invalidate
);

CREATE INDEX IF NOT EXISTS idx_rt_projections_type ON rt_projections(projection_type);
CREATE INDEX IF NOT EXISTS idx_rt_projections_scope ON rt_projections(scope);
CREATE INDEX IF NOT EXISTS idx_rt_projections_computed_at ON rt_projections(computed_at);
```

**Usage:**
- Written by projection builder after computing sections
- Read by robot surfaces for fast response
- Invalidated when source_health changes for a dependency
- Optional (can be disabled for always-fresh projections)

### 2.7 rt_cursor_state

Tracks active cursors for connected consumers.

```sql
CREATE TABLE IF NOT EXISTS rt_cursor_state (
    cursor_id TEXT PRIMARY KEY,          -- Consumer-provided or auto-generated
    consumer_type TEXT NOT NULL,         -- cli, rest, websocket
    last_cursor INTEGER NOT NULL,        -- Last consumed attention cursor
    last_seen_at TEXT NOT NULL,          -- RFC3339 consumer last polled
    metadata TEXT                        -- JSON consumer metadata
);

CREATE INDEX IF NOT EXISTS idx_rt_cursor_consumer ON rt_cursor_state(consumer_type);
CREATE INDEX IF NOT EXISTS idx_rt_cursor_last_seen ON rt_cursor_state(last_seen_at);
```

**Usage:**
- Tracks which cursor each consumer has seen
- Used for cursor expiry warnings
- Pruned after consumer timeout (default: 1 hour)

---

## 3. Migration Plan

### 3.1 Migration File

Create `internal/state/migrations/007_runtime_projection.sql`:

```sql
-- NTM State Store: Runtime Projection Tables
-- Version: 007
-- Description: Adds runtime projection layer for robot mode surfaces

-- Source health tracking
CREATE TABLE IF NOT EXISTS rt_source_health ( ... );

-- Attention event journal
CREATE TABLE IF NOT EXISTS rt_attention_events ( ... );

-- Incident tracking
CREATE TABLE IF NOT EXISTS rt_incidents ( ... );
CREATE TABLE IF NOT EXISTS rt_incident_alerts ( ... );

-- Output watermarks
CREATE TABLE IF NOT EXISTS rt_output_watermarks ( ... );

-- Projection cache
CREATE TABLE IF NOT EXISTS rt_projections ( ... );

-- Cursor state
CREATE TABLE IF NOT EXISTS rt_cursor_state ( ... );
```

### 3.2 Migration Safety

- All tables are `CREATE TABLE IF NOT EXISTS` for idempotency
- No modifications to existing tables
- No foreign keys to existing tables (loose coupling)
- Safe to run multiple times

### 3.3 Rollback

Runtime tables can be dropped without affecting source-of-truth:

```sql
DROP TABLE IF EXISTS rt_cursor_state;
DROP TABLE IF EXISTS rt_projections;
DROP TABLE IF EXISTS rt_output_watermarks;
DROP TABLE IF EXISTS rt_incident_alerts;
DROP TABLE IF EXISTS rt_incidents;
DROP TABLE IF EXISTS rt_attention_events;
DROP TABLE IF EXISTS rt_source_health;
```

---

## 4. Retention and Pruning

### 4.1 Retention Policies

| Table | Retention | Pruning Trigger |
|-------|-----------|-----------------|
| rt_source_health | Always current | Rebuilt on startup |
| rt_attention_events | 24 hours | Background goroutine |
| rt_incidents | Indefinite | Manual cleanup |
| rt_output_watermarks | Session lifetime | Session delete |
| rt_projections | 5 minutes | Cache invalidation |
| rt_cursor_state | 1 hour idle | Background goroutine |

### 4.2 Pruning Implementation

```go
// PruneRuntimeTables removes expired runtime data
func (s *Store) PruneRuntimeTables(ctx context.Context) error {
    now := time.Now().UTC()

    // Prune old attention events (24h retention)
    _, err := s.db.ExecContext(ctx, `
        DELETE FROM rt_attention_events
        WHERE created_at < datetime('now', '-24 hours')
        AND pinned = 0
    `)
    if err != nil {
        return fmt.Errorf("prune attention events: %w", err)
    }

    // Prune idle cursor state (1h timeout)
    _, err = s.db.ExecContext(ctx, `
        DELETE FROM rt_cursor_state
        WHERE last_seen_at < datetime('now', '-1 hour')
    `)
    if err != nil {
        return fmt.Errorf("prune cursor state: %w", err)
    }

    // Prune expired projections
    _, err = s.db.ExecContext(ctx, `
        DELETE FROM rt_projections
        WHERE valid_until IS NOT NULL
        AND valid_until < datetime('now')
    `)
    if err != nil {
        return fmt.Errorf("prune projections: %w", err)
    }

    return nil
}
```

---

## 5. Store APIs

### 5.1 Source Health API

```go
// UpdateSourceHealth records collection result for a source
func (s *Store) UpdateSourceHealth(ctx context.Context, entry SourceHealthEntry) error

// GetSourceHealth returns current health for all sources
func (s *Store) GetSourceHealth(ctx context.Context) (map[string]SourceHealthEntry, error)

// GetDegradedSources returns only non-fresh sources
func (s *Store) GetDegradedSources(ctx context.Context) ([]SourceHealthEntry, error)
```

### 5.2 Attention Events API

```go
// PublishAttentionEvent writes a new attention event
func (s *Store) PublishAttentionEvent(ctx context.Context, event AttentionEvent) (cursor int64, error)

// GetAttentionEvents returns events since cursor
func (s *Store) GetAttentionEvents(ctx context.Context, sinceCursor int64, limit int) ([]AttentionEvent, error)

// GetLatestCursor returns the most recent attention cursor
func (s *Store) GetLatestCursor(ctx context.Context) (int64, error)

// AcknowledgeEvent marks an event as acknowledged
func (s *Store) AcknowledgeEvent(ctx context.Context, cursor int64, by string) error

// SnoozeEvent marks an event as snoozed until time
func (s *Store) SnoozeEvent(ctx context.Context, cursor int64, until time.Time) error

// PinEvent pins/unpins an event
func (s *Store) PinEvent(ctx context.Context, cursor int64, pinned bool) error
```

### 5.3 Incidents API

```go
// CreateOrUpdateIncident creates new or updates existing incident by fingerprint
func (s *Store) CreateOrUpdateIncident(ctx context.Context, incident Incident) error

// GetOpenIncidents returns all unresolved incidents
func (s *Store) GetOpenIncidents(ctx context.Context) ([]Incident, error)

// ResolveIncident marks an incident as resolved
func (s *Store) ResolveIncident(ctx context.Context, id, by, notes string) error

// LinkAlertToIncident associates an alert with an incident
func (s *Store) LinkAlertToIncident(ctx context.Context, incidentID, alertID string) error
```

### 5.4 Watermarks API

```go
// SetOutputWatermark records output position for a pane
func (s *Store) SetOutputWatermark(ctx context.Context, session, pane, value string) error

// GetOutputWatermark returns last recorded position
func (s *Store) GetOutputWatermark(ctx context.Context, session, pane string) (string, error)
```

---

## 6. Success Criteria

This schema is successful when:

1. Robot surfaces read from rt_* tables without hitting source tables repeatedly
2. Restart recovers projection state from rt_* tables within 1 second
3. Pruning keeps database size bounded (<100MB for typical use)
4. No foreign key violations between runtime and source-of-truth tables

---

## References

- [Projection Section Model](projection-section-model.md) (bd-j9jo3.1.2)
- [Freshness Contract](freshness-degraded-state-contract.md) (bd-j9jo3.1.4)
- Existing schema: `internal/state/migrations/001_initial.sql`

---

## Changelog

| Date | Change |
|------|--------|
| 2026-03-22 | Initial runtime schema design ratified (bd-j9jo3.2.1) |
