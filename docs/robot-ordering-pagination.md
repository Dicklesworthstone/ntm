# Robot Ordering, Truncation, Pagination, and Payload Budget Contract

> **Authoritative reference** for deterministic ordering, bounded output, and payload management.
> All robot surfaces MUST follow this contract.

**Status:** AUTHORITATIVE
**Bead:** bd-j9jo3.1.6
**Created:** 2026-03-22

---

## 1. Purpose

This document defines how robot surfaces stay deterministic, bounded, and token-efficient as the underlying system grows. The goal is to make boundedness, determinism, and explainable omission part of the product contract.

**Design Goals:**
1. Output is deterministic — same input produces same output order
2. Output is bounded — no surface explodes in size under routine use
3. Truncation is explicit — consumers know when data is omitted
4. Pagination is safe — large result sets can be continued
5. Budgets are documented — regression in size/latency is detectable

---

## 2. Deterministic Ordering

### 2.1 Ordering Principle

Every collection in robot output has a **stable ordering key**. The key is:
1. **Deterministic** — Same data produces same order
2. **Documented** — Consumers know what to expect
3. **Stable across runs** — No random/timestamp-based reordering

### 2.2 Ordering Keys by Collection

| Collection | Primary Key | Secondary Key | Order |
|------------|-------------|---------------|-------|
| `sessions` | `name` | - | Ascending |
| `agents` (within session) | `pane` (numeric) | - | Ascending |
| `panes` | `id` (window.pane) | - | Ascending |
| `beads` | `priority` | `created_at` | Asc priority, Asc created |
| `alerts` | `severity` | `created_at` | Desc severity, Desc created |
| `incidents` | `severity` | `created_at` | Desc severity, Desc created |
| `events` | `cursor` | - | Ascending |
| `attention_items` | `actionability` | `cursor` | Desc actionability, Desc cursor |
| `accounts` | `provider` | `id` | Ascending |
| `tools` | `name` | - | Ascending |
| `threads` | `updated_at` | - | Descending |
| `mail` | `created_ts` | - | Descending |

### 2.3 Severity and Actionability Order

**Severity (descending):**
```
critical > error > warning > info > debug
```

**Actionability (descending):**
```
action_required > interesting > background
```

### 2.4 Ordering in Go

```go
// Sort sessions by name
sort.Slice(sessions, func(i, j int) bool {
    return sessions[i].Name < sessions[j].Name
})

// Sort alerts by severity (desc), then created_at (desc)
sort.Slice(alerts, func(i, j int) bool {
    if alerts[i].SeverityRank() != alerts[j].SeverityRank() {
        return alerts[i].SeverityRank() > alerts[j].SeverityRank()
    }
    return alerts[i].CreatedAt.After(alerts[j].CreatedAt)
})
```

---

## 3. Truncation

### 3.1 Truncation Principle

When output exceeds bounds, truncation is:
1. **Explicit** — `truncated: true` marker in output
2. **Counted** — `total_count` shows full count
3. **Continued** — `continuation` shows how to get more
4. **Deterministic** — Same limit produces same truncation

### 3.2 Truncation Markers

```json
{
  "sessions": [...],
  "pagination": {
    "total_count": 47,
    "returned_count": 20,
    "truncated": true,
    "offset": 0,
    "limit": 20,
    "continuation": "--offset=20 --limit=20"
  }
}
```

### 3.3 Truncation Signals

| Signal | Meaning |
|--------|---------|
| `truncated: true` | More items exist beyond returned set |
| `truncated: false` | All items returned |
| `total_count` | Total items matching filters |
| `returned_count` | Items in this response |
| `continuation` | Command/parameters to get next page |
| `has_more` | Boolean shorthand for truncated |

### 3.4 Per-Section Truncation

Each section can be independently truncated:

```json
{
  "sessions": {
    "items": [...],
    "truncated": true,
    "total_count": 50,
    "returned_count": 10
  },
  "alerts": {
    "items": [...],
    "truncated": false,
    "total_count": 3,
    "returned_count": 3
  }
}
```

---

## 4. Pagination

### 4.1 Pagination Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `--limit` | int | Surface-specific | Max items to return |
| `--offset` | int | 0 | Items to skip |
| `--cursor` | string | - | Cursor-based pagination (events) |
| `--since` | timestamp | - | Time-based filtering |

### 4.1a Paginated Surfaces (exact scope)

Offset/limit pagination is NOT list-wide. It is implemented on exactly the
surfaces flagged `paginated: true` in the robot registry (the WS0-G6
single-declaration map `SchemaPagination` in
`internal/robot/schema_pagination.go`, exposed per surface via
`--robot-capabilities` as `paginated`/`paginated_reason` and enforced by
registry-walk conformance tests):

| Surface | Command | Contract |
|---------|---------|----------|
| `status` | `ntm --robot-status --limit/--robot-offset` | `pagination` + `_agent_hints.next_offset` |
| `snapshot` | `ntm --robot-snapshot --limit/--robot-offset` | `pagination` + `_agent_hints.next_offset` |
| `history` (robot) | `ntm --robot-history=<s> --limit/--offset` | `pagination` + `_agent_hints.next_offset` |
| `ensemble_modes` | `ntm --robot-ensemble-modes --limit/--offset` | `pagination` + `_agent_hints.next_offset` |
| `session_list` | `ntm list --json --limit/--offset` | `count`/`total_matches`/`has_more` + hints |
| `history_list` | `ntm history --json --limit/--offset` | offset counts back from newest |
| `audit_query` | `ntm audit show/search --json --limit/--offset` | `count`/`total_matches`/`has_more` + hints |
| `checkpoint_list` | `ntm checkpoint list [session] --json --limit/--offset` | `count`/`total_matches`/`has_more` + hints |
| `approvals_list` | `ntm approve list --json --limit/--offset` | `count`/`total_matches`/`has_more` + hints |

Every other list-shaped surface is **UNPAGINATED BY DESIGN** (bounded
cardinality, per-request result echoes, or cursor-based continuation like
`events`/`attention`); each carries its reason in the registry flag. An
unflagged list surface fails the conformance test, so this table's scope
cannot silently drift.

### 4.2 Offset-Based Pagination

For the offset-paginated collections above (e.g. status/snapshot sessions):

```bash
# First page
ntm --robot-status --limit=20

# Second page
ntm --robot-status --limit=20 --offset=20

# Third page
ntm --robot-status --limit=20 --offset=40
```

### 4.3 Cursor-Based Pagination

For event streams where offset is unstable:

```bash
# Initial fetch
ntm --robot-events

# Continue from cursor
ntm --robot-events --since-cursor=42
```

Response includes:
```json
{
  "events": [...],
  "cursor": 42,
  "has_more": true
}
```

### 4.4 When to Use Which

| Use Case | Pagination Type | Reason |
|----------|-----------------|--------|
| Sessions/agents | Offset | Stable ordering by name |
| Beads list | Offset | Stable ordering by priority/created |
| Event stream | Cursor | Events can be inserted between fetches |
| Alert history | Cursor | New alerts can arrive between pages |
| Current alerts | Offset | Snapshot at a point in time |

### 4.5 Pagination Consistency

During pagination:
- **Offset pagination** may skip or duplicate items if underlying data changes
- **Cursor pagination** guarantees no duplicates, may miss items created before cursor

Consumers should:
- Prefer cursor pagination for event-like data
- Accept eventual consistency for list pagination
- Use snapshot for point-in-time consistency

---

## 5. Payload Budgets

### 5.1 Budget Principle

Every surface has explicit budgets for:
1. **Token count** — Approximate LLM tokens
2. **Byte size** — JSON payload size
3. **Item count** — Maximum items per collection
4. **Latency** — Target response time

### 5.2 Surface Budgets

| Surface | Token Budget | Byte Budget | Item Limits | Latency Target |
|---------|--------------|-------------|-------------|----------------|
| `snapshot` | 2000 | 50KB | sessions:20, agents:100, alerts:50 | <500ms |
| `status` | 500 | 15KB | sessions:50, agents:200 | <100ms |
| `terse` | 50 | 1KB | n/a (single line) | <50ms |
| `events` | 1000 | 30KB | events:100 | <200ms |
| `attention` | 500 | 15KB | items:20 | <200ms |
| `digest` | 300 | 10KB | counts only | <100ms |
| `tail` | 2000 | 50KB | lines:100 per pane | <200ms |
| `diagnose` | 3000 | 75KB | full detail | <1000ms |

### 5.3 Default Limits

```go
const (
    // Snapshot defaults
    SnapshotSessionsLimit     = 20
    SnapshotAgentsPerSession  = 10
    SnapshotAlertsLimit       = 50
    SnapshotBeadsSummaryOnly  = true

    // Status defaults
    StatusSessionsLimit = 50
    StatusAgentsLimit   = 200

    // Events defaults
    EventsLimit         = 100
    EventsMaxAgeMinutes = 60

    // Attention defaults
    AttentionItemsLimit = 20

    // Tail defaults
    TailLinesDefault    = 20
    TailLinesMax        = 500
)
```

### 5.4 Budget Enforcement

When budget is exceeded:
1. Truncate to limit
2. Set `truncated: true`
3. Include `continuation` for more
4. Add `_budget_note` explaining omission

```json
{
  "sessions": [...],
  "truncated": true,
  "_budget_note": "20 of 47 sessions returned. Use --limit=50 for more or --robot-status for full list."
}
```

### 5.5 Budget Overrides

Consumers can request larger payloads:

```bash
# Request more sessions
ntm --robot-snapshot --limit=100

# Request full output (use with caution)
ntm --robot-status --limit=0
```

`--limit=0` means "no limit" and should only be used for small systems or specific debugging.

---

## 6. Detail Levels

### 6.1 Detail Level Principle

Surfaces support multiple detail levels to trade verbosity for tokens:

| Level | Description | Use Case |
|-------|-------------|----------|
| `minimal` | IDs and counts only | Quick health check |
| `summary` | Key fields, no nested detail | Dashboard display |
| `standard` | Default output | Normal operation |
| `full` | All available fields | Debugging |
| `verbose` | Full + internal metadata | Deep debugging |

### 6.2 Specifying Detail Level

The shipped CLI knob is `--robot-verbosity` (env `NTM_ROBOT_VERBOSITY`), whose
profiles are `terse`, `default`, and `debug`; the five-level ladder above is
the conceptual model those profiles collapse into.

```bash
ntm --robot-status --robot-verbosity=terse
ntm --robot-snapshot --robot-verbosity=debug
```

### 6.3 Detail Level Defaults

| Surface | Default Level |
|---------|--------------|
| `snapshot` | standard |
| `status` | summary |
| `terse` | minimal |
| `events` | standard |
| `attention` | standard |
| `diagnose` | full |

### 6.4 Field Inclusion by Level

Example for `SessionInfo`:

| Field | minimal | summary | standard | full | verbose |
|-------|---------|---------|----------|------|---------|
| `name` | Y | Y | Y | Y | Y |
| `attached` | - | Y | Y | Y | Y |
| `agent_count` | Y | Y | Y | Y | Y |
| `agents` | - | - | Y | Y | Y |
| `agents[].state` | - | - | Y | Y | Y |
| `agents[].context_usage` | - | - | - | Y | Y |
| `agents[].detection_method` | - | - | - | - | Y |

---

## 7. Omission Signaling

### 7.1 Omission Types

| Type | Signal | Example |
|------|--------|---------|
| Truncation | `truncated: true` | List exceeded limit |
| Compression | `compressed: true` | Section summarized |
| Redaction | `redacted: true` | Sensitive data removed |
| Unavailable | `available: false` | Source not reachable |
| Excluded | `excluded_sections` | Section not requested |

### 7.2 Omission in JSON

```json
{
  "sessions": {
    "items": [...],
    "truncated": true,
    "truncation_reason": "limit_exceeded"
  },
  "alerts": {
    "available": false,
    "reason": "alert_manager_unreachable"
  },
  "beads": {
    "compressed": true,
    "compression_reason": "summary_only",
    "drill_down": "ntm --robot-bead-list"
  }
}
```

### 7.3 Omission in TOON Format

```
SESSIONS (20 of 47, truncated)
├─ myproject (attached, 3 agents)
├─ other (detached, 2 agents)
└─ ... 45 more (use --limit=50)

ALERTS (unavailable: alert manager unreachable)
```

### 7.4 Omission in Terse Format

```
3s/12a/ok [alerts:unavail] [sessions:20/47]
```

---

## 8. Transport Consistency

### 8.1 Consistency Across Transports

All transports (JSON, TOON, markdown, terse, REST, streaming) must:
1. Use same ordering keys
2. Apply same limits
3. Signal same omissions
4. Return same data for same parameters

### 8.2 Transport-Specific Budgets

Some transports have tighter budgets:

| Transport | Token Multiplier | Notes |
|-----------|------------------|-------|
| JSON | 1.0x | Baseline |
| TOON | 0.7x | More efficient encoding |
| Markdown | 1.2x | Table headers add overhead |
| Terse | 0.1x | Single-line summary |
| Streaming | 1.0x | Same as JSON |

### 8.3 Streaming Considerations

For streaming output:
- Events are ordered by cursor
- No pagination within a stream session
- Backpressure applies server-side limits
- Client disconnect terminates stream

---

## 9. Regression Detection

### 9.1 Size Regression Tests

CI should verify:
```go
func TestSnapshotPayloadSize(t *testing.T) {
    output := generateSnapshot(testFixture)
    json := marshalJSON(output)

    if len(json) > SnapshotMaxBytes {
        t.Errorf("Snapshot exceeds budget: %d > %d bytes",
            len(json), SnapshotMaxBytes)
    }
}
```

### 9.2 Ordering Regression Tests

CI should verify determinism:
```go
func TestSnapshotOrderingDeterminism(t *testing.T) {
    a := generateSnapshot(testFixture)
    b := generateSnapshot(testFixture)

    if !reflect.DeepEqual(a, b) {
        t.Error("Snapshot not deterministic across runs")
    }
}
```

### 9.3 Golden File Tests

Check-in golden output files and diff:
```bash
ntm --robot-snapshot > testdata/golden/snapshot.json
git diff testdata/golden/snapshot.json
```

---

## 10. Implementation Guidelines

### 10.1 Apply Limits Early

```go
func buildSessions(all []Session, limit int) []SessionInfo {
    // Apply limit BEFORE expensive transforms
    limited := all
    if limit > 0 && len(all) > limit {
        limited = all[:limit]
    }

    // Transform only the limited set
    result := make([]SessionInfo, len(limited))
    for i, s := range limited {
        result[i] = transformSession(s)
    }
    return result
}
```

### 10.2 Include Pagination Info

```go
func paginationInfo(total, offset, limit int) *PaginationInfo {
    returned := min(total-offset, limit)
    return &PaginationInfo{
        TotalCount:    total,
        ReturnedCount: returned,
        Offset:        offset,
        Limit:         limit,
        Truncated:     total > offset+returned,
        Continuation:  fmt.Sprintf("--offset=%d --limit=%d", offset+returned, limit),
    }
}
```

### 10.3 Sort Before Return

```go
func (b *ProjectionBuilder) Build() *Projection {
    // Sort all collections BEFORE returning
    sortSessions(b.sessions)
    sortAlerts(b.alerts)
    sortEvents(b.events)

    return &Projection{
        Sessions: b.sessions,
        Alerts:   b.alerts,
        Events:   b.events,
    }
}
```

---

## 11. Non-Goals

1. **Compression algorithms** — This contract is about logical truncation, not wire compression
2. **Caching** — Caching is a separate concern
3. **Rate limiting** — Rate limits are API-level, not payload-level
4. **Streaming chunking** — Chunk size is transport-specific

---

## 12. References

- [Robot Surface Taxonomy](robot-surface-taxonomy.md) — Lane definitions
- [Robot Projection Sections](robot-projection-sections.md) — Section schemas
- [Robot Schema Versioning](robot-schema-versioning.md) — Schema identity

---

## Appendix: Changelog

- **2026-03-22:** Initial ordering/pagination contract (bd-j9jo3.1.6)

---

*Reference: bd-j9jo3.1.6*
