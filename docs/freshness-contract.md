# Freshness, Degraded-State, and Provenance Contract

> **Status: SUPERSEDED BY [`docs/freshness-degraded-state-contract.md`](freshness-degraded-state-contract.md).**
> Retained in-place for historical `bd-j9jo3.1.4` design provenance per `AGENTS.md` Rule 1.

**Status:** SUPERSEDED
**Bead:** bd-j9jo3.1.4
**Created:** 2026-03-22
**Part of:** bd-j9jo3 (robot-redesign epic)
**Depends on:** projection-section-model.md (bd-j9jo3.1.2), robot-surface-taxonomy.md (bd-j9jo3.1.1)

---

## 1. Purpose

This document defines how robot surfaces communicate data quality to operators. An operator must be able to answer:

1. **"Is this data fresh or stale?"** - When was it collected?
2. **"Is any source degraded?"** - What failed to load?
3. **"Is this live data or cached?"** - What is the provenance?

Without explicit quality markers, operators cannot reason about partial failures or make informed decisions during the operator loop.

**Design Goal:** Per-source (not per-field) quality reporting. Strong enough semantics that all surfaces can express partial failure without inventing bespoke warnings.

**Anti-Goal:** Noisy per-field provenance that clutters payloads and overwhelms consumers.

---

## 2. Core Concepts

### 2.1 Source

A **source** is a distinct data provider that ntm queries. Sources have independent failure modes and freshness windows.

| Source ID | Description | Example Failure |
|-----------|-------------|-----------------|
| `tmux` | Tmux server state | tmux not running |
| `beads` | Beads issue tracker | SQLite locked |
| `mail` | Agent Mail server | MCP server unreachable |
| `cass` | CASS search index | Index stale |
| `bv` | BV graph analyzer | bv binary missing |
| `quota` | Provider quotas | API rate limited |
| `git` | Git repository state | Not a git repo |
| `files` | File system state | Permission denied |
| `process` | Process state | /proc unavailable |

### 2.2 Provenance Mode

Provenance describes how data was obtained:

| Mode | Meaning | When Used |
|------|---------|-----------|
| `live` | Direct query at request time | Normal operation |
| `cached` | From prior query, within TTL | Performance optimization |
| `stale` | From prior query, TTL expired | Source temporarily unavailable |
| `derived` | Computed from other sources | Aggregations, projections |
| `unavailable` | Source completely failed | Bootstrap, permanent failure |

### 2.3 Freshness

Freshness is expressed as:
- **`collected_at`**: RFC3339 timestamp when data was fetched
- **`freshness_sec`**: Seconds since collection (computed at render time)

Freshness thresholds by source type:

| Source Type | Fresh | Stale Warning | Critical |
|-------------|-------|---------------|----------|
| tmux | <5s | 5-30s | >30s |
| beads/bv | <60s | 60-300s | >300s |
| mail | <30s | 30-120s | >120s |
| quota | <300s | 300-900s | >900s |
| cass | <3600s | 3600-86400s | >86400s |

---

## 3. Type Definitions

### 3.1 SourceHealthEntry

Each source reports its health via this structure:

```go
type SourceHealthEntry struct {
    // Identity
    SourceID  string `json:"source_id"`  // e.g., "tmux", "beads", "mail"

    // Health status
    Status    string `json:"status"`     // healthy, degraded, unavailable
    Error     string `json:"error,omitempty"`
    ErrorCode string `json:"error_code,omitempty"` // Machine-parseable

    // Freshness
    Provenance  string `json:"provenance"`    // live, cached, stale, derived, unavailable
    CollectedAt string `json:"collected_at"`  // RFC3339
    FreshnessSec int   `json:"freshness_sec"` // Computed at render time

    // Degraded features (what's missing)
    DegradedFeatures []string `json:"degraded_features,omitempty"`

    // Recovery hints
    RetryAfterSec int    `json:"retry_after_sec,omitempty"`
    Hint          string `json:"hint,omitempty"`
}
```

### 3.2 SourceHealth Map

The RuntimeState includes a map of all source health:

```go
type RuntimeState struct {
    // ... other sections ...

    // Source health (keyed by source_id)
    SourceHealth map[string]SourceHealthEntry `json:"source_health"`

    // Overall health derived from SourceHealth
    OverallStatus string `json:"overall_status"` // healthy, degraded, critical
}
```

### 3.3 Section-Level Provenance

Each major section includes provenance markers:

```go
type SectionProvenance struct {
    Sources     []string `json:"sources"`      // Contributing source IDs
    Provenance  string   `json:"provenance"`   // Worst-case of contributing sources
    CollectedAt string   `json:"collected_at"` // Oldest collection time
    FreshnessSec int     `json:"freshness_sec"`
}

// Example: WorkSection includes provenance
type WorkSection struct {
    // ... existing fields ...

    // Provenance marker
    Provenance SectionProvenance `json:"_provenance"`
}
```

---

## 4. Semantics

### 4.1 Status Determination

Source status is determined by:

```
healthy     := source responded successfully within timeout
degraded    := source responded with partial data OR is using cached data
unavailable := source failed to respond OR returned error
```

### 4.2 Provenance Propagation

When rendering a section that depends on multiple sources:

1. Collect provenance from all contributing sources
2. Use **worst-case** provenance for the section:
   - `unavailable` > `stale` > `cached` > `derived` > `live`
3. Use **oldest** `collected_at` for the section
4. Union all `degraded_features`

### 4.3 Degraded Features

When a source is degraded, report which capabilities are affected:

| Source | Degraded Features When Unavailable |
|--------|-----------------------------------|
| tmux | `session_state`, `agent_state`, `pane_output` |
| beads | `ready_queue`, `bead_details`, `dependency_graph` |
| bv | `triage`, `plan`, `graph_metrics` |
| mail | `inbox`, `threads`, `reservations` |
| cass | `search`, `context` |
| quota | `rate_limits`, `usage_stats` |

### 4.4 Consumer Behavior

Operators MUST handle quality markers:

1. **Check `overall_status`** before acting on data
2. **Check section `_provenance`** before using section data
3. **Display freshness warnings** when `freshness_sec` exceeds thresholds
4. **Skip degraded features** when planning actions

Example operator logic:
```
if response.overall_status == "unavailable":
    fallback to last known good state
elif response.source_health["beads"].provenance == "stale":
    warn user, continue with caveat
elif response.work._provenance.freshness_sec > 60:
    refresh before making decisions
```

---

## 5. Surface Requirements

### 5.1 Snapshot

MUST include:
- Full `source_health` map with all sources queried
- `overall_status` summary
- `_provenance` on each major section

### 5.2 Status

MUST include:
- `overall_status` (derived from contributing sources)
- Abbreviated `source_health` (only degraded/unavailable sources)
- No section-level provenance (too verbose for cheap surface)

### 5.3 Digest / Attention

MUST include:
- `source_health` for sources relevant to attention items
- Items MUST NOT be marked `action_required` if based on stale data
- `_provenance` on attention items

### 5.4 Events

MUST include:
- `source` field on each event (already present)
- Events from degraded sources include `degraded: true` marker

---

## 6. Failure Behavior

### 6.1 Source Unavailable at Bootstrap

If a source is unavailable during snapshot:

1. Include source in `source_health` with `status: "unavailable"`
2. Include `error` and `error_code`
3. Include `hint` for recovery
4. Set section `_provenance.provenance` to `unavailable`
5. Populate section with empty defaults (empty arrays, zero counts)

### 6.2 Source Degraded Mid-Session

If a source becomes degraded after initial bootstrap:

1. Continue using cached data up to TTL
2. Set `provenance` to `cached` then `stale`
3. Raise alert when `freshness_sec` exceeds critical threshold
4. Add to `degraded_features` for affected sections

### 6.3 Recovery

When a degraded source recovers:

1. Clear `error` and `error_code`
2. Set `status` to `healthy`
3. Set `provenance` to `live`
4. Clear `degraded_features`
5. Emit recovery event

---

## 7. JSON Examples

### 7.1 Healthy State

```json
{
  "source_health": {
    "tmux": {
      "source_id": "tmux",
      "status": "healthy",
      "provenance": "live",
      "collected_at": "2026-03-22T03:30:00Z",
      "freshness_sec": 2
    },
    "beads": {
      "source_id": "beads",
      "status": "healthy",
      "provenance": "live",
      "collected_at": "2026-03-22T03:30:01Z",
      "freshness_sec": 1
    }
  },
  "overall_status": "healthy"
}
```

### 7.2 Degraded State

```json
{
  "source_health": {
    "tmux": {
      "source_id": "tmux",
      "status": "healthy",
      "provenance": "live",
      "collected_at": "2026-03-22T03:30:00Z",
      "freshness_sec": 2
    },
    "mail": {
      "source_id": "mail",
      "status": "degraded",
      "error": "MCP server connection timeout",
      "error_code": "CONNECTION_TIMEOUT",
      "provenance": "stale",
      "collected_at": "2026-03-22T03:25:00Z",
      "freshness_sec": 302,
      "degraded_features": ["inbox", "threads", "reservations"],
      "retry_after_sec": 30,
      "hint": "Check mcp-agent-mail server status"
    }
  },
  "overall_status": "degraded"
}
```

### 7.3 Section Provenance

```json
{
  "work": {
    "total": 150,
    "open": 25,
    "ready": 5,
    "_provenance": {
      "sources": ["beads", "bv"],
      "provenance": "live",
      "collected_at": "2026-03-22T03:30:01Z",
      "freshness_sec": 1
    }
  }
}
```

---

## 8. Implementation Notes

### 8.1 Collection Order

Collect sources in dependency order:
1. `tmux` (no dependencies)
2. `git`, `files` (no dependencies)
3. `beads` (depends on file system)
4. `bv` (depends on beads)
5. `mail` (depends on MCP server)
6. `cass` (depends on index)
7. `quota` (depends on external APIs)

### 8.2 Timeout Handling

Each source has a collection timeout:
- `tmux`: 2s
- `beads`, `bv`: 5s
- `mail`: 3s
- `cass`: 10s
- `quota`: 15s

On timeout, mark source as `degraded` with `error_code: "TIMEOUT"`.

### 8.3 Caching Strategy

For cheap surfaces (status, terse):
- Cache all source data with 5s TTL
- Only query tmux live (it's fastest)
- Report cached data with appropriate provenance

For expensive surfaces (snapshot):
- Query all sources live
- Cache result for 30s
- Report live provenance

---

## 9. Migration Path

### 9.1 Existing Code

Current robot outputs do not include quality markers. Migration:

1. Add `source_health` to `RobotResponse` base type
2. Add `_provenance` to section types
3. Wrap source queries with health tracking
4. Update renderers to include provenance

### 9.2 Consumer Updates

Consumers (AI agents) should:
1. Check `overall_status` field exists before assuming healthy
2. Fall back to no-quality-marker behavior if field absent
3. Log warnings when consuming stale data

---

## 10. Non-Goals

This contract explicitly rejects:

1. **Per-field provenance**: Too verbose, overwhelms consumers
2. **Automatic healing**: Operators decide recovery actions
3. **Quality scoring**: Binary healthy/degraded/unavailable is sufficient
4. **Historical provenance**: Only current state matters
5. **Cross-session aggregation**: Each response is independent

---

## 11. Related Documents

- **robot-surface-taxonomy.md**: Defines which surfaces exist and their responsibilities
- **projection-section-model.md**: Defines section structure this contract annotates
- **attention-feed-contract.md**: Defines event semantics (includes source field)
- **robot-api-design.md**: Defines output envelope and error codes
