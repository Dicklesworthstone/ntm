# Freshness and Degraded-State Contract

> **Authoritative specification for data freshness, degraded sources, and provenance semantics.**
> This contract defines how robot surfaces describe data quality so operators can trust what they see.

**Status:** RATIFIED
**Bead:** bd-j9jo3.1.4
**Created:** 2026-03-22
**Part of:** bd-j9jo3 (robot-redesign epic)
**Depends on:** projection-section-model.md (bd-j9jo3.1.2)
**Inspired by:** vibe_cockpit staleness patterns

---

## 1. Design Principles

### 1.1 Why This Matters

When an operator sees "3 agents idle", they need to know:
- Is this data from 1 second ago or 5 minutes ago?
- Did the tmux query fail, and we're showing stale data?
- Are some fields derived/computed vs. directly observed?

Without freshness metadata, operators cannot distinguish between:
- Agents actually idle vs. data too old to reflect current state
- No alerts vs. alert system failed to collect
- Zero mail vs. mail service unreachable

### 1.2 Core Invariants

1. **Every source has freshness metadata** - No implicit "assume it's fresh"
2. **Degradation is explicit** - Failed collection shows in `source_health`
3. **Per-source, not per-field** - Avoid noisy field-level provenance
4. **Fail-visible** - Stale/unavailable sources are surfaced, not hidden

---

## 2. Source Health Model

### 2.1 Source Definitions

A **source** is a data origin that can independently succeed or fail. ntm has these sources:

| Source | Collects | Stale After |
|--------|----------|-------------|
| `tmux` | Sessions, panes, window state | 5 seconds |
| `agent_detector` | Agent types, states, prompts | 5 seconds |
| `context_tracker` | Context window estimates | 30 seconds |
| `beads` | Bead state from .beads/ | 30 seconds |
| `mail` | Agent Mail inbox, threads | 60 seconds |
| `quota` | API rate limit state | 300 seconds |
| `alerts` | Alert rule evaluations | 10 seconds |
| `health` | System health computations | 10 seconds |
| `events` | Attention event journal | 1 second |

### 2.2 Source Health Entry

Every robot output includes a `source_health` section:

```go
type SourceHealthEntry struct {
    // Identity
    Source string `json:"source"` // tmux, beads, mail, etc.

    // Status (enum)
    Status string `json:"status"` // fresh, stale, unavailable, unknown

    // Timing
    CollectedAt   string `json:"collected_at"`    // RFC3339 - when data was collected
    FreshnessSec  int    `json:"freshness_sec"`   // Seconds since collection
    StaleAfterSec int    `json:"stale_after_sec"` // Threshold for staleness

    // Degradation
    DegradedFeatures []string `json:"degraded_features,omitempty"` // What's unreliable
    LastError        string   `json:"last_error,omitempty"`
    LastErrorAt      string   `json:"last_error_at,omitempty"`

    // Provenance
    Provenance string `json:"provenance"` // live, cached, derived
}
```

### 2.3 Status Values

| Status | Meaning | Operator Behavior |
|--------|---------|-------------------|
| `fresh` | Data collected within `stale_after_sec` | Trust the data |
| `stale` | Older than threshold, but the returned data is explicitly marked stale | Note uncertainty, may need resync |
| `unavailable` | Collection failed, no useful data | Check `last_error`, may need intervention |
| `unknown` | Source never collected successfully | Check configuration |

### 2.4 Example Output

```json
{
  "source_health": {
    "tmux": {
      "source": "tmux",
      "status": "fresh",
      "collected_at": "2026-03-22T10:30:45Z",
      "freshness_sec": 2,
      "stale_after_sec": 5,
      "provenance": "live"
    },
    "beads": {
      "source": "beads",
      "status": "stale",
      "collected_at": "2026-03-22T10:29:15Z",
      "freshness_sec": 92,
      "stale_after_sec": 30,
      "degraded_features": ["ready_count_may_be_outdated", "in_progress_may_differ"],
      "provenance": "cached"
    },
    "mail": {
      "source": "mail",
      "status": "unavailable",
      "collected_at": "2026-03-22T10:25:00Z",
      "freshness_sec": 347,
      "stale_after_sec": 60,
      "last_error": "connection refused",
      "last_error_at": "2026-03-22T10:30:00Z",
      "provenance": "cached"
    }
  }
}
```

---

## 3. Degraded Features

### 3.1 What Gets Degraded

When a source is stale or unavailable, specific features become unreliable:

| Source | Degraded Features When Stale |
|--------|------------------------------|
| `tmux` | `agent_states`, `pane_output`, `session_list` |
| `agent_detector` | `agent_types`, `current_state`, `is_busy` |
| `context_tracker` | `token_estimates`, `compaction_state` |
| `beads` | `ready_count`, `in_progress_list`, `blocked_count` |
| `mail` | `unread_count`, `urgent_threads`, `recent_messages` |
| `quota` | `remaining_percent`, `resets_at`, `warning_state` |
| `alerts` | `active_alerts`, `alert_counts` |

### 3.2 Feature Strings

Degraded features are identified by stable string keys that downstream consumers can match:

```go
const (
    DegradedAgentStates     = "agent_states"
    DegradedPaneOutput      = "pane_output"
    DegradedSessionList     = "session_list"
    DegradedTokenEstimates  = "token_estimates"
    DegradedReadyCount      = "ready_count"
    DegradedInProgressList  = "in_progress_list"
    DegradedUnreadCount     = "unread_count"
    DegradedQuotaRemaining  = "remaining_percent"
    DegradedActiveAlerts    = "active_alerts"
    // ... etc
)
```

### 3.3 Consumer Behavior

When a feature is degraded, consumers SHOULD:
1. Use only the data returned by the source; do not infer a prior state when none is present
2. Display a visual indicator that the data may be stale
3. Consider the data "possibly incorrect" in decision-making
4. Periodically attempt to refresh

Consumers MUST NOT:
- Hide the section entirely (show stale data with warning)
- Treat stale data as authoritative for destructive actions
- Ignore `source_health` when automating decisions

---

## 4. Provenance Mode

### 4.1 Provenance Values

| Mode | Meaning | Example |
|------|---------|---------|
| `live` | Directly observed just now | tmux list-sessions output |
| `cached` | Last known good value, age tracked | Beads state from 30s ago |
| `derived` | Computed from other data | Health score from multiple sources |

### 4.2 Provenance Rules

1. **live sources** have `freshness_sec < stale_after_sec`
2. **cached sources** have `freshness_sec >= stale_after_sec` and valid data
3. **derived sources** are computed from other sources; they inherit the worst provenance of their inputs

### 4.3 Derived Source Example

The `health` source computes health scores from tmux, agents, and alerts:

```json
{
  "source_health": {
    "health": {
      "source": "health",
      "status": "stale",
      "provenance": "derived",
      "degraded_features": ["health_score_stale"],
      "derived_from": ["tmux", "agent_detector", "alerts"],
      "worst_input_status": "stale",
      "worst_input_source": "alerts"
    }
  }
}
```

---

## 5. Surface Integration

### 5.1 Snapshot (Bootstrap)

Snapshot MUST include full `source_health` for all sources, regardless of status.

```json
{
  "success": true,
  "source_health": {
    "tmux": { "status": "fresh", ... },
    "beads": { "status": "fresh", ... },
    "mail": { "status": "stale", ... },
    "quota": { "status": "unavailable", ... },
    ...
  },
  "sessions": [...],
  ...
}
```

### 5.2 Status (Summarize)

Status includes only degraded sources (non-fresh) to keep output compact:

```json
{
  "success": true,
  "degraded_sources": ["mail", "quota"],
  "degraded_warnings": [
    "mail: stale (92s), unread_count may be outdated",
    "quota: unavailable, connection refused"
  ],
  ...
}
```

### 5.3 Digest (Triage)

Digest includes `source_health` entries that affect triaged items:

```json
{
  "success": true,
  "attention": {
    "items": [...]
  },
  "source_health": {
    "beads": { "status": "stale", ... }
  },
  "data_quality_warning": "beads source is stale; ready_count may be outdated"
}
```

### 5.4 Attention (Single Item)

Attention output includes `source_caveats` on the item itself when relevant:

```json
{
  "top_item": {
    "id": "...",
    "summary": "3 beads now ready",
    "source_caveats": ["beads: stale (45s)"]
  }
}
```

---

## 6. Collection Failure Handling

### 6.1 Failure Modes

| Failure | Result | Recovery |
|---------|--------|----------|
| Collection timeout | Source marked `unavailable` | Auto-retry next poll |
| Connection refused | Source marked `unavailable` | Check service health |
| Parse error | Source marked `unavailable`, last good value retained | Log error, continue |
| Partial data | Source marked `stale` with specific `degraded_features` | Show partial data |

### 6.2 Error Propagation

Errors are captured in `last_error` and `last_error_at`:

```json
{
  "source": "quota",
  "status": "unavailable",
  "last_error": "timeout after 5s: api.anthropic.com unreachable",
  "last_error_at": "2026-03-22T10:30:00Z",
  "provenance": "cached"
}
```

### 6.3 Retry Semantics

- Sources retry on next poll cycle
- No exponential backoff (simple fixed interval)
- Consecutive failures don't change behavior
- Recovery auto-detected on next successful collection

---

## 7. Operator Loop Integration

### 7.1 Bootstrap Decision

When calling `--robot-snapshot`, check `source_health` before trusting data:

```python
response = ntm_snapshot()
for source, health in response['source_health'].items():
    if health['status'] == 'unavailable':
        log_warning(f"{source} unavailable: {health.get('last_error')}")
    elif health['status'] == 'stale':
        log_info(f"{source} stale by {health['freshness_sec']}s")
```

### 7.2 Triage Decision

When deciding actions from `--robot-digest`, weight stale data appropriately:

```python
for item in response['attention']['items']:
    if 'source_caveats' in item:
        # Stale data - don't take destructive action
        confidence = 'low'
    else:
        confidence = 'high'
```

### 7.3 Resync Triggers

Operators SHOULD trigger a full resync when:
- Multiple critical sources are `unavailable`
- A source has been `stale` for > 10x its `stale_after_sec`
- Decisions are blocked by data uncertainty

---

## 8. Implementation Notes

### 8.1 Go Types Location

```
internal/robot/projection/
├── freshness.go     // SourceHealthEntry, freshness computation
├── degradation.go   // Degraded feature constants and mapping
└── collection.go    // Collection timing and failure handling
```

### 8.2 SQLite Schema

```sql
CREATE TABLE source_health (
    source TEXT PRIMARY KEY,
    status TEXT NOT NULL,           -- fresh, stale, unavailable, unknown
    collected_at TEXT NOT NULL,     -- RFC3339
    stale_after_sec INTEGER NOT NULL,
    provenance TEXT NOT NULL,       -- live, cached, derived
    degraded_features TEXT,         -- JSON array
    last_error TEXT,
    last_error_at TEXT
);
```

### 8.3 Update Frequency

Source health is recomputed:
- On every collection attempt (success or failure)
- On every robot command output (freshness recalculated)
- NOT persisted between ntm restarts (recomputed from collectors)

---

## 9. Success Criteria

This contract is successful when:

1. Every robot output includes `source_health` or `degraded_sources`
2. Operators can distinguish fresh vs. stale data without external docs
3. Collection failures are visible in output, not hidden
4. No surface silently uses stale data without marking it

---

## References

- [Projection Section Model](projection-section-model.md) (bd-j9jo3.1.2)
- [Robot Surface Taxonomy](robot-surface-taxonomy.md) (bd-j9jo3.1.1)
- vibe_cockpit `RobotEnvelope.staleness` pattern

---

## Changelog

| Date | Change |
|------|--------|
| 2026-03-22 | Initial freshness/degraded-state contract ratified (bd-j9jo3.1.4) |
