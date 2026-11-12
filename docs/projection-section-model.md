# Projection Section Model

> **Status: SUPERSEDED BY [`docs/robot-section-model.md`](robot-section-model.md).**
> Retained in-place for historical `bd-j9jo3.1.2` design provenance per `AGENTS.md` Rule 1.

**Status:** SUPERSEDED
**Bead:** bd-j9jo3.1.2
**Created:** 2026-03-22
**Part of:** bd-j9jo3 (robot-redesign epic)
**Depends on:** robot-surface-taxonomy.md (bd-j9jo3.1.1)

---

## 1. Design Principles

### 1.1 One Model, Many Projections

All robot surfaces project from a single runtime model:

```go
type RuntimeState struct {
    Summary      SummarySection
    Sessions     []SessionSection
    Work         WorkSection
    Coordination CoordinationSection
    Quota        QuotaSection
    Alerts       []AlertSection
    Incidents    []IncidentSection
    Attention    AttentionSection
    SourceHealth map[string]SourceHealthEntry
    NextActions  []NextAction
}
```

Different surfaces render different subsets:
- **snapshot**: All sections (bootstrap)
- **status**: Summary, Sessions (headers only), Alerts (counts)
- **digest**: Attention, NextActions (prioritized changes)
- **attention**: Attention, Incidents (most urgent item)

### 1.2 Section vs Field

A concept becomes a **section** when:
1. Multiple surfaces need it
2. It has stable internal structure
3. It can be rendered independently
4. Operators reason about it as a unit

Fields within sections can vary by surface. Sections themselves are stable.

### 1.3 Global vs Session-Scoped

| Scope | Contains | Examples |
|-------|----------|----------|
| Global | System-wide state | Work, Quota, Incidents, SourceHealth |
| Session | Per-session state | Sessions[].Agents, Sessions[].Alerts |
| Pane | Per-pane state | Agents[].State, Agents[].Context |

---

## 2. Section Definitions

### 2.1 Summary Section

**Purpose:** Cheap glance metrics. Fits in ~50 tokens.

```go
type SummarySection struct {
    // Session counts
    SessionCount  int    `json:"session_count"`
    AgentCount    int    `json:"agent_count"`

    // Agent state breakdown
    AgentsByState map[string]int `json:"agents_by_state"` // idle, busy, error, compacting
    AgentsByType  map[string]int `json:"agents_by_type"`  // claude, codex, antigravity, gemini (legacy)

    // Work state
    ReadyWork     int    `json:"ready_work"`      // Beads ready to claim
    InProgress    int    `json:"in_progress"`     // Beads being worked

    // Health
    HealthScore   float64 `json:"health_score"`   // 0.0-1.0
    HealthStatus  string  `json:"health_status"`  // healthy, degraded, critical
    AlertsActive  int     `json:"alerts_active"`  // Count of unresolved alerts

    // Mail
    MailUnread    int     `json:"mail_unread"`
    MailUrgent    int     `json:"mail_urgent"`
}
```

**Surface usage:**
| Surface | Fields Used |
|---------|-------------|
| snapshot | All |
| status | All |
| terse | SessionCount, AgentCount, ReadyWork, HealthStatus |
| digest | ReadyWork, AlertsActive, MailUrgent |

---

### 2.2 Sessions Section

**Purpose:** Per-session state with nested agent information.

```go
type SessionSection struct {
    // Identity
    Name      string `json:"name"`
    Label     string `json:"label,omitempty"` // Multi-session label
    Project   string `json:"project"`
    ProjectDir string `json:"project_dir"`

    // State
    Attached  bool   `json:"attached"`
    CreatedAt string `json:"created_at"` // RFC3339

    // Layout
    WindowCount int `json:"window_count"`
    PaneCount   int `json:"pane_count"`

    // Agents
    Agents []AgentSection `json:"agents"`

    // Session-level health
    Health SessionHealth `json:"health"`

    // Session-level alerts (subset of global alerts)
    Alerts []AlertRef `json:"alerts,omitempty"`
}

type AgentSection struct {
    // Identity
    PaneID    string `json:"pane_id"`     // window.pane format
    PaneIndex int    `json:"pane_index"`
    Type      string `json:"type"`        // claude, codex, antigravity, gemini (legacy), user

    // State
    State       string `json:"state"`       // idle, busy, error, compacting, unknown
    StateReason string `json:"state_reason,omitempty"`
    StateSince  string `json:"state_since"` // RFC3339

    // Context window
    Context *ContextInfo `json:"context,omitempty"`

    // Current work (if any)
    CurrentBead string `json:"current_bead,omitempty"`

    // Activity
    LastOutput    string `json:"last_output,omitempty"`     // Truncated recent output
    LastOutputAt  string `json:"last_output_at,omitempty"`  // RFC3339
}

type SessionHealth struct {
    Score    float64  `json:"score"`    // 0.0-1.0
    Status   string   `json:"status"`   // healthy, degraded, critical
    Issues   []string `json:"issues,omitempty"`
}

type ContextInfo struct {
    UsedTokens   int     `json:"used_tokens"`
    MaxTokens    int     `json:"max_tokens"`
    UsagePercent float64 `json:"usage_percent"`
    Compacting   bool    `json:"compacting"`
}
```

**Surface usage:**
| Surface | Fields Used |
|---------|-------------|
| snapshot | All sections, full agent detail |
| status | Headers only: Name, AgentCount, Attached, Health.Status |
| inspect-session | Full detail for one session |
| tail | AgentSection.LastOutput expanded |

---

### 2.3 Work Section

**Purpose:** Beads/task state. Global scope (not per-session).

```go
type WorkSection struct {
    // Counts
    Total      int `json:"total"`
    Open       int `json:"open"`
    Ready      int `json:"ready"`      // No blockers, claimable
    InProgress int `json:"in_progress"`
    Blocked    int `json:"blocked"`

    // Ready queue (top N)
    ReadyQueue []BeadRef `json:"ready_queue"` // Limit 5

    // In-flight work
    InFlight []InFlightWork `json:"in_flight,omitempty"`

    // Recent activity
    RecentlyClosed int `json:"recently_closed"` // Last 24h

    // Health
    StaleCount   int `json:"stale_count"`   // In-progress > threshold
    CycleCount   int `json:"cycle_count"`   // Dependency cycles
}

type BeadRef struct {
    ID       string `json:"id"`
    Title    string `json:"title"`
    Priority int    `json:"priority"`
    Type     string `json:"type"` // task, bug, feature, epic
}

type InFlightWork struct {
    BeadID     string `json:"bead_id"`
    BeadTitle  string `json:"bead_title"`
    Agent      string `json:"agent,omitempty"` // session:pane if known
    StartedAt  string `json:"started_at"`
    DurationSec int   `json:"duration_sec"`
}
```

**Surface usage:**
| Surface | Fields Used |
|---------|-------------|
| snapshot | All |
| status | Counts only |
| digest | ReadyQueue, InFlight changes |
| attention | Ready items becoming stale, blocked items becoming ready |

---

### 2.4 Coordination Section

**Purpose:** Agent coordination state (mail, reservations).

```go
type CoordinationSection struct {
    // Mail
    Mail MailSummary `json:"mail"`

    // File reservations
    Reservations []ReservationInfo `json:"reservations,omitempty"`

    // Conflicts (if any)
    Conflicts []ConflictInfo `json:"conflicts,omitempty"`
}

type MailSummary struct {
    Unread      int    `json:"unread"`
    Urgent      int    `json:"urgent"`
    AckRequired int    `json:"ack_required"`
    ThreadCount int    `json:"thread_count"`

    // Recent messages (top N)
    Recent []MailRef `json:"recent,omitempty"` // Limit 3
}

type MailRef struct {
    ID      string `json:"id"`
    From    string `json:"from"`
    Subject string `json:"subject"`
    Urgent  bool   `json:"urgent"`
    ReceivedAt string `json:"received_at"`
}

type ReservationInfo struct {
    Agent    string   `json:"agent"`
    Patterns []string `json:"patterns"`
    Exclusive bool    `json:"exclusive"`
    ExpiresAt string  `json:"expires_at"`
    Reason   string   `json:"reason,omitempty"` // Usually bead ID
}

type ConflictInfo struct {
    ID        string   `json:"id"`
    Type      string   `json:"type"` // file_conflict, reservation_conflict
    Files     []string `json:"files,omitempty"`
    Agents    []string `json:"agents"`
    DetectedAt string  `json:"detected_at"`
    Resolved  bool     `json:"resolved"`
}
```

**Surface usage:**
| Surface | Fields Used |
|---------|-------------|
| snapshot | All |
| status | Mail counts, Conflict count |
| digest | New mail, new conflicts |
| attention | Urgent mail, active conflicts |

---

### 2.5 Quota Section

**Purpose:** API rate limit and token budget state.

```go
type QuotaSection struct {
    // Per-provider quota
    Providers map[string]ProviderQuota `json:"providers"` // anthropic, openai, google

    // Aggregate
    LowestRemaining float64 `json:"lowest_remaining"` // 0.0-1.0
    WarnThreshold   float64 `json:"warn_threshold"`   // Default 0.2

    // Alerts
    QuotaWarnings []string `json:"quota_warnings,omitempty"`
}

type ProviderQuota struct {
    Name         string  `json:"name"`
    Remaining    float64 `json:"remaining"`    // 0.0-1.0
    ResetsAt     string  `json:"resets_at,omitempty"` // RFC3339
    TokensUsed   int     `json:"tokens_used,omitempty"`
    TokensLimit  int     `json:"tokens_limit,omitempty"`
    RequestsUsed int     `json:"requests_used,omitempty"`
    RequestsLimit int    `json:"requests_limit,omitempty"`
}
```

**Surface usage:**
| Surface | Fields Used |
|---------|-------------|
| snapshot | All |
| status | LowestRemaining, QuotaWarnings count |
| digest | Quota changes |
| attention | Approaching limits |

---

### 2.6 Alerts Section

**Purpose:** Active and recent alerts.

```go
type AlertSection struct {
    // Identity
    ID       string `json:"id"`
    Type     string `json:"type"`     // agent_error, session_unhealthy, quota_warning, etc.
    Severity string `json:"severity"` // info, warning, error, critical

    // Content
    Summary  string `json:"summary"`
    Details  string `json:"details,omitempty"`

    // Scope
    Session  string `json:"session,omitempty"`
    Pane     string `json:"pane,omitempty"`
    BeadID   string `json:"bead_id,omitempty"`

    // Lifecycle
    RaisedAt   string `json:"raised_at"`   // RFC3339
    ClearedAt  string `json:"cleared_at,omitempty"`
    AckedAt    string `json:"acked_at,omitempty"`
    AckedBy    string `json:"acked_by,omitempty"`

    // Actions
    NextActions []NextAction `json:"next_actions,omitempty"`
}

type AlertRef struct {
    ID       string `json:"id"`
    Type     string `json:"type"`
    Severity string `json:"severity"`
    Summary  string `json:"summary"`
}
```

**Surface usage:**
| Surface | Fields Used |
|---------|-------------|
| snapshot | All active alerts |
| status | Counts by severity |
| digest | New and cleared alerts |
| attention | Unacked critical/error alerts |
| inspect-alert | Full detail for one alert |

---

### 2.7 Incidents Section

**Purpose:** Recurring or escalated issues that persist beyond individual alerts.

```go
type IncidentSection struct {
    // Identity
    ID          string `json:"id"`
    Fingerprint string `json:"fingerprint"` // Dedup key

    // Classification
    Type      string `json:"type"`     // agent_crash_loop, persistent_error, etc.
    Severity  string `json:"severity"` // warning, error, critical

    // Content
    Summary   string `json:"summary"`
    Pattern   string `json:"pattern,omitempty"` // What keeps happening

    // Scope
    Session   string   `json:"session,omitempty"`
    Panes     []string `json:"panes,omitempty"`

    // Lifecycle
    OpenedAt    string `json:"opened_at"`    // RFC3339
    LastSeenAt  string `json:"last_seen_at"` // Most recent occurrence
    OccurrenceCount int `json:"occurrence_count"`
    ResolvedAt  string `json:"resolved_at,omitempty"`

    // Related alerts (this incident rolled up from)
    RelatedAlerts []string `json:"related_alerts,omitempty"` // Alert IDs

    // Actions
    NextActions []NextAction `json:"next_actions,omitempty"`
}
```

**Surface usage:**
| Surface | Fields Used |
|---------|-------------|
| snapshot | All open incidents |
| status | Incident count |
| digest | New incidents, escalations |
| attention | Active incidents |
| inspect-incident | Full detail for one incident |

**Incident vs Alert:**
- **Alert:** Single occurrence, clears when condition clears
- **Incident:** Pattern of recurring issues, persists until explicitly resolved

---

### 2.8 Attention Section

**Purpose:** Prioritized queue of items needing operator attention.

```go
type AttentionSection struct {
    // Queue
    Items []AttentionItem `json:"items"`

    // Metadata
    TotalCount     int    `json:"total_count"`      // Full queue size
    FilteredCount  int    `json:"filtered_count"`   // After filtering
    CutoffScore    float64 `json:"cutoff_score"`    // Min score shown

    // State
    LastUpdated    string `json:"last_updated"`     // RFC3339
    CursorPosition int64  `json:"cursor_position"`  // For replay
}

type AttentionItem struct {
    // Identity
    ID    string `json:"id"`
    Cursor int64 `json:"cursor"` // Monotonic position in attention stream

    // Classification
    Category      string `json:"category"`      // agent, bead, alert, mail, conflict
    Type          string `json:"type"`          // Category-specific type
    Actionability string `json:"actionability"` // background, interesting, action_required
    Severity      string `json:"severity"`      // info, warning, error, critical

    // Content
    Summary string `json:"summary"`

    // Scope
    Session string `json:"session,omitempty"`
    Pane    string `json:"pane,omitempty"`

    // Scoring
    Score     float64 `json:"score"`     // Attention priority score
    Factors   []string `json:"factors,omitempty"` // Why this score

    // Timing
    OccurredAt string `json:"occurred_at"` // RFC3339
    Age        int    `json:"age_sec"`     // Seconds since occurred

    // Follow-up
    NextActions []NextAction `json:"next_actions"`

    // State
    Acked    bool   `json:"acked"`
    Snoozed  bool   `json:"snoozed"`
    Pinned   bool   `json:"pinned"`
}
```

**Surface usage:**
| Surface | Fields Used |
|---------|-------------|
| snapshot | AttentionSummary (compact: counts, top item) |
| digest | Full Items array (grouped by actionability) |
| attention | Single top item with full NextActions |
| events | Raw events with cursor (before scoring/filtering) |

---

### 2.9 SourceHealth Section

**Purpose:** Data freshness and degradation metadata per source.

```go
type SourceHealthEntry struct {
    // Identity
    Source string `json:"source"` // tmux, beads, mail, quota, etc.

    // Freshness
    Status        string `json:"status"`         // fresh, stale, unavailable, unknown
    CollectedAt   string `json:"collected_at"`   // RFC3339
    FreshnessSec  int    `json:"freshness_sec"`  // Seconds since collection
    StaleAfterSec int    `json:"stale_after_sec"` // Threshold for staleness

    // Degradation
    DegradedFeatures []string `json:"degraded_features,omitempty"` // What's unreliable
    LastError        string   `json:"last_error,omitempty"`
    LastErrorAt      string   `json:"last_error_at,omitempty"`

    // Provenance
    Provenance string `json:"provenance"` // live, cached, derived
}
```

**Status Values:**

| Status | Meaning |
|--------|---------|
| fresh | Collected within `stale_after_sec` |
| stale | Older than threshold, data may be outdated |
| unavailable | Collection failed, showing last known |
| unknown | Never successfully collected |

**Surface usage:**
| Surface | Fields Used |
|---------|-------------|
| snapshot | Full SourceHealth map |
| status | Degraded sources only (non-fresh) |
| digest | Source degradation changes |

---

### 2.10 NextActions Section

**Purpose:** Mechanical follow-up commands for operators.

```go
type NextAction struct {
    // Identity
    Label    string `json:"label"`    // Human-readable action name

    // Command
    Command  string `json:"command"`  // Exact CLI command to run

    // Classification
    Category string `json:"category"` // inspect, act, resync

    // Metadata
    Rationale string `json:"rationale,omitempty"` // Why this action
    Urgent    bool   `json:"urgent,omitempty"`
}
```

**Surface usage:**
- Embedded in AttentionItem, AlertSection, IncidentSection
- Top-level NextActions in snapshot for bootstrap orientation

---

## 3. Section Composition by Surface

### 3.1 Snapshot (Bootstrap)

```json
{
  "success": true,
  "timestamp": "...",
  "schema_version": "ntm.robot.snapshot.v2",

  "summary": { /* SummarySection */ },
  "sessions": [ /* SessionSection[] */ ],
  "work": { /* WorkSection */ },
  "coordination": { /* CoordinationSection */ },
  "quota": { /* QuotaSection */ },
  "alerts": [ /* AlertSection[] */ ],
  "incidents": [ /* IncidentSection[] */ ],
  "attention": { /* AttentionSection (compact) */ },
  "source_health": { /* map[string]SourceHealthEntry */ },
  "next_actions": [ /* NextAction[] */ ],

  "cursor": 1234567890,
  "replay_window": { /* ReplayWindowInfo */ }
}
```

### 3.2 Status (Summarize)

```json
{
  "success": true,
  "timestamp": "...",
  "schema_version": "ntm.robot.status.v2",

  "summary": { /* SummarySection */ },
  "sessions": [
    { "name": "...", "agent_count": 4, "health": { "status": "healthy" } }
  ],
  "alert_counts": { "critical": 0, "error": 1, "warning": 2 },
  "degraded_sources": [ "beads" ]
}
```

### 3.3 Digest (Triage)

```json
{
  "success": true,
  "timestamp": "...",
  "schema_version": "ntm.robot.digest.v1",

  "attention": {
    "action_required": [ /* AttentionItem[] */ ],
    "interesting": [ /* AttentionItem[] */ ],
    "background": [ /* AttentionItem[] (truncated) */ ]
  },
  "next_actions": [ /* NextAction[] */ ],
  "cursor": 1234567890
}
```

### 3.4 Attention (Single Item)

```json
{
  "success": true,
  "timestamp": "...",
  "schema_version": "ntm.robot.attention.v1",

  "top_item": { /* AttentionItem or null */ },
  "queue_depth": 5,
  "cursor": 1234567890
}
```

---

## 4. Implementation Notes

### 4.1 Go Struct Location

```
internal/robot/projection/
├── model.go        // RuntimeState and section types
├── builder.go      // Assembles RuntimeState from sources
├── renderer.go     // Projects RuntimeState to surface outputs
└── sections.go     // Section-specific logic
```

### 4.2 SQLite Schema Derivation

Each section maps to SQLite tables:

| Section | Table(s) |
|---------|----------|
| Sessions | sessions, agents, panes |
| Work | (external: .beads) |
| Coordination | mail_messages, reservations, conflicts |
| Alerts | alerts |
| Incidents | incidents, incident_alerts |
| Attention | attention_events |
| SourceHealth | source_health |

### 4.3 Freshness Thresholds

| Source | Stale After |
|--------|-------------|
| tmux | 5 seconds |
| beads | 30 seconds |
| mail | 60 seconds |
| quota | 300 seconds |

---

## 5. Success Criteria

This model is successful when:

1. All surfaces render from RuntimeState without additional state assembly
2. New sections can be added without breaking existing surfaces
3. Source degradation is visible in every surface that uses the source
4. Operators understand section boundaries without reading this doc

---

## References

- [Robot Surface Taxonomy](robot-surface-taxonomy.md) (bd-j9jo3.1.1)
- [Attention Feed Contract](attention-feed-contract.md) (br-aa0nj)
- vibe_cockpit vc_cli/src/robot.rs (inspiration)

---

## Changelog

| Date | Change |
|------|--------|
| 2026-03-22 | Initial section model ratified (bd-j9jo3.1.2) |
