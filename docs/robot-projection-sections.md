# Canonical Projection Section Model

> **Status: SUPERSEDED BY [`docs/robot-section-model.md`](robot-section-model.md).**
> Retained in-place for historical `bd-j9jo3.1.2` design provenance per `AGENTS.md` Rule 1.

**Status:** SUPERSEDED
**Bead:** bd-j9jo3.1.2
**Created:** 2026-03-22

---

## 1. Purpose

This document defines the canonical sections that robot surfaces share. By projecting from a unified model:

1. **Consistency** — The same concept has the same shape across surfaces
2. **Composability** — Surfaces can include/exclude sections without ad-hoc composition
3. **Evolvability** — Schema changes propagate to all surfaces through the model
4. **Testability** — One section definition, one validation suite

**Anti-Goal:** Ad-hoc composition where each surface invents its own representation.

---

## 2. Section Map

```
┌─────────────────────────────────────────────────────────────────────────┐
│                       CANONICAL SECTIONS                                 │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐    │
│  │   SUMMARY   │  │  SESSIONS   │  │    WORK     │  │ COORDINATION│    │
│  │             │  │             │  │             │  │             │    │
│  │ counts      │  │ sessions[]  │  │ beads       │  │ mail        │    │
│  │ health      │  │   agents[]  │  │ triage      │  │ threads     │    │
│  │ cursor      │  │   panes[]   │  │ graph       │  │ reservations│    │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘    │
│                                                                          │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐    │
│  │    QUOTA    │  │   ALERTS    │  │  INCIDENTS  │  │  ATTENTION  │    │
│  │             │  │             │  │             │  │             │    │
│  │ accounts[]  │  │ active[]    │  │ active[]    │  │ items[]     │    │
│  │ limits      │  │ summary     │  │ history     │  │ cursor      │    │
│  │ usage       │  │ by_severity │  │ by_severity │  │ counts      │    │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘    │
│                                                                          │
│  ┌─────────────┐  ┌─────────────┐                                       │
│  │SOURCE_HEALTH│  │NEXT_ACTIONS │                                       │
│  │             │  │             │                                       │
│  │ sources{}   │  │ actions[]   │                                       │
│  │ degraded[]  │  │ commands    │                                       │
│  │ freshness   │  │ priorities  │                                       │
│  └─────────────┘  └─────────────┘                                       │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Section Definitions

### 3.1 Summary Section

**Purpose:** High-level counts and health indicators for quick orientation.

**Schema:**
```go
type SummarySection struct {
    // Counts provide aggregate numbers
    SessionCount    int `json:"session_count"`
    AgentCount      int `json:"agent_count"`
    IdleAgentCount  int `json:"idle_agent_count"`
    BusyAgentCount  int `json:"busy_agent_count"`
    ErrorAgentCount int `json:"error_agent_count"`

    // Health provides system-level indicators
    OverallHealth   HealthLevel `json:"overall_health"` // healthy, degraded, critical
    HealthReasons   []string    `json:"health_reasons,omitempty"`

    // Cursor provides replay position
    LatestCursor    int64  `json:"latest_cursor"`
    CursorTimestamp string `json:"cursor_timestamp"`

    // Profile indicates active configuration
    SafetyProfile string `json:"safety_profile,omitempty"`
}

type HealthLevel string
const (
    HealthLevelHealthy  HealthLevel = "healthy"
    HealthLevelDegraded HealthLevel = "degraded"
    HealthLevelCritical HealthLevel = "critical"
)
```

**Surface usage:**
- `snapshot`: INCLUDE (full)
- `status`: INCLUDE (full)
- `terse`: INCLUDE (counts only)
- `attention`: INCLUDE (cursor only)
- `events`: EXCLUDE

---

### 3.2 Sessions Section

**Purpose:** Enumerate sessions, agents, and panes.

**Schema:**
```go
type SessionsSection struct {
    Sessions []SessionInfo `json:"sessions"`
    Total    int           `json:"total"`
}

type SessionInfo struct {
    Name      string      `json:"name"`
    Attached  bool        `json:"attached"`
    Created   string      `json:"created,omitempty"`   // RFC3339
    LastActive string     `json:"last_active,omitempty"`
    Agents    []AgentInfo `json:"agents"`
    Panes     []PaneInfo  `json:"panes,omitempty"` // Non-agent panes
}

type AgentInfo struct {
    Pane           string   `json:"pane"`           // "window.pane" format
    Type           string   `json:"type"`           // claude, codex, antigravity, gemini (legacy)
    Variant        string   `json:"variant,omitempty"` // Model variant
    State          string   `json:"state"`          // idle, busy, error, compacting
    StateTimestamp string   `json:"state_timestamp"` // When state last changed
    Confidence     float64  `json:"confidence"`     // Detection confidence
    DetectionMethod string  `json:"detection_method"` // How agent was detected

    // Work association
    CurrentBead    *string `json:"current_bead,omitempty"`
    CurrentTask    string  `json:"current_task,omitempty"`

    // Context
    ContextUsage   float64 `json:"context_usage,omitempty"` // 0.0-1.0
    OutputAgeMs    int64   `json:"output_age_ms"`
    PendingMail    int     `json:"pending_mail,omitempty"`
}

type PaneInfo struct {
    ID          string `json:"id"`          // "window.pane" format
    Title       string `json:"title,omitempty"`
    CurrentDir  string `json:"current_dir,omitempty"`
    RunningCmd  string `json:"running_cmd,omitempty"`
}
```

**Surface usage:**
- `snapshot`: INCLUDE (full)
- `status`: INCLUDE (agents only)
- `terse`: EXCLUDE
- `attention`: EXCLUDE (use session references in attention items)
- `tail`: INCLUDE (single session)
- `inspect-pane`: INCLUDE (single pane)

---

### 3.3 Work Section

**Purpose:** Beads, triage, and dependency graph state.

**Schema:**
```go
type WorkSection struct {
    Beads     *BeadsSummary   `json:"beads,omitempty"`
    Triage    *TriageSummary  `json:"triage,omitempty"`
    Graph     *GraphSummary   `json:"graph,omitempty"`
    Available bool            `json:"available"`
    Reason    string          `json:"reason,omitempty"` // Why unavailable
}

type BeadsSummary struct {
    Total       int `json:"total"`
    Open        int `json:"open"`
    InProgress  int `json:"in_progress"`
    Closed      int `json:"closed"`
    Ready       int `json:"ready"`       // Open with no blockers
    Blocked     int `json:"blocked"`     // Open with blockers

    ByPriority  map[string]int `json:"by_priority,omitempty"`
    ByType      map[string]int `json:"by_type,omitempty"`
    ByLabel     map[string]int `json:"by_label,omitempty"`
}

type TriageSummary struct {
    TopRecommendation *BeadRecommendation `json:"top_recommendation,omitempty"`
    ReadyCount        int                 `json:"ready_count"`
    QuickWinsCount    int                 `json:"quick_wins_count"`
    BlockersCount     int                 `json:"blockers_count"` // Issues blocking others
}

type BeadRecommendation struct {
    ID         string   `json:"id"`
    Title      string   `json:"title"`
    Priority   int      `json:"priority"`
    Score      float64  `json:"score"`
    Reasons    []string `json:"reasons"`
    Unblocks   int      `json:"unblocks"` // How many downstream issues
}

type GraphSummary struct {
    TotalNodes     int     `json:"total_nodes"`
    TotalEdges     int     `json:"total_edges"`
    CycleCount     int     `json:"cycle_count"`
    CriticalPath   int     `json:"critical_path"` // Longest dependency chain
    AvgPageRank    float64 `json:"avg_pagerank,omitempty"`
}
```

**Surface usage:**
- `snapshot`: INCLUDE (summary only)
- `status`: EXCLUDE
- `triage`: INCLUDE (full)
- `plan`: INCLUDE (full)
- `bead-show`: INCLUDE (single bead detail)

---

### 3.4 Coordination Section

**Purpose:** Agent Mail state for multi-agent coordination.

**Schema:**
```go
type CoordinationSection struct {
    Mail         *MailSummary        `json:"mail,omitempty"`
    Threads      *ThreadsSummary     `json:"threads,omitempty"`
    Reservations *ReservationsSummary `json:"reservations,omitempty"`
    Available    bool                `json:"available"`
    Reason       string              `json:"reason,omitempty"`
}

type MailSummary struct {
    TotalUnread   int                    `json:"total_unread"`
    UrgentUnread  int                    `json:"urgent_unread"`
    ByAgent       map[string]AgentMailStats `json:"by_agent,omitempty"`
    LatestMessage string                 `json:"latest_message,omitempty"` // RFC3339
}

type AgentMailStats struct {
    Unread  int    `json:"unread"`
    Pending int    `json:"pending"` // Awaiting ack
    Pane    string `json:"pane,omitempty"`
}

type ThreadsSummary struct {
    Active    int      `json:"active"`
    Stale     int      `json:"stale"` // No activity in 24h
    TopThreads []string `json:"top_threads,omitempty"` // Most active thread IDs
}

type ReservationsSummary struct {
    Active     int      `json:"active"`
    Expiring   int      `json:"expiring"` // <1h TTL remaining
    Conflicts  int      `json:"conflicts"`
    ByAgent    map[string]int `json:"by_agent,omitempty"`
}
```

**Surface usage:**
- `snapshot`: INCLUDE (summary)
- `status`: INCLUDE (counts only)
- `mail-check`: INCLUDE (full)
- `attention`: INCLUDE (when mail events present)

---

### 3.5 Quota Section

**Purpose:** API rate limits and account usage across providers.

**Schema:**
```go
type QuotaSection struct {
    Accounts   []AccountQuota `json:"accounts"`
    Summary    *QuotaSummary  `json:"summary,omitempty"`
    Available  bool           `json:"available"`
    Reason     string         `json:"reason,omitempty"`
}

type AccountQuota struct {
    ID          string `json:"id"`
    Provider    string `json:"provider"` // anthropic, openai, google
    Model       string `json:"model,omitempty"`

    // Usage
    TokensUsed     int64  `json:"tokens_used"`
    TokensLimit    int64  `json:"tokens_limit,omitempty"`
    RequestsUsed   int    `json:"requests_used"`
    RequestsLimit  int    `json:"requests_limit,omitempty"`

    // Health
    Status         string `json:"status"` // ok, warning, exceeded, suspended
    ResetAt        string `json:"reset_at,omitempty"` // RFC3339
    EstimatedExhaustion string `json:"estimated_exhaustion,omitempty"` // RFC3339

    // Flags
    IsActive       bool   `json:"is_active"`
    IsPrimary      bool   `json:"is_primary"`
}

type QuotaSummary struct {
    TotalAccounts   int `json:"total_accounts"`
    HealthyAccounts int `json:"healthy_accounts"`
    WarningAccounts int `json:"warning_accounts"`
    ExceededAccounts int `json:"exceeded_accounts"`

    LowestTokensRemaining int64 `json:"lowest_tokens_remaining,omitempty"`
    NextReset             string `json:"next_reset,omitempty"` // RFC3339
}
```

**Surface usage:**
- `snapshot`: INCLUDE (summary)
- `quota-status`: INCLUDE (full)
- `account-status`: INCLUDE (single account)
- `attention`: INCLUDE (when quota events present)

---

### 3.6 Alerts Section

**Purpose:** Active alerts requiring attention.

**Schema:**
```go
type AlertsSection struct {
    Active     []AlertItem     `json:"active"`
    Summary    *AlertsSummary  `json:"summary,omitempty"`
    RecentlyCleared []string   `json:"recently_cleared,omitempty"` // Last 5 cleared alert IDs
}

type AlertItem struct {
    ID         string                 `json:"id"`
    Type       string                 `json:"type"`     // error, warning, quota, conflict, health
    Severity   string                 `json:"severity"` // info, warning, error, critical
    Message    string                 `json:"message"`

    // Scope
    Session    string                 `json:"session,omitempty"`
    Pane       string                 `json:"pane,omitempty"`
    Agent      string                 `json:"agent,omitempty"`
    BeadID     string                 `json:"bead_id,omitempty"`

    // Context
    Details    map[string]interface{} `json:"details,omitempty"`
    Count      int                    `json:"count"` // Repeat count

    // Timing
    CreatedAt  string                 `json:"created_at"`  // RFC3339
    UpdatedAt  string                 `json:"updated_at"`  // RFC3339
    DurationMs int64                  `json:"duration_ms"` // Since creation

    // Actions
    Dismissable bool                  `json:"dismissable"`
    AutoClears  bool                  `json:"auto_clears"` // Will clear when condition resolves
}

type AlertsSummary struct {
    TotalActive int            `json:"total_active"`
    BySeverity  map[string]int `json:"by_severity"`
    ByType      map[string]int `json:"by_type"`
    OldestAlert string         `json:"oldest_alert,omitempty"` // RFC3339
}
```

**Surface usage:**
- `snapshot`: INCLUDE (full)
- `status`: INCLUDE (summary only)
- `attention`: INCLUDE (action_required alerts)
- `alerts`: INCLUDE (full with filters)

---

### 3.7 Incidents Section

**Purpose:** Active and historical incidents requiring investigation or remediation.

**Schema:**
```go
type IncidentsSection struct {
    Active    []IncidentItem   `json:"active"`
    History   []IncidentItem   `json:"history,omitempty"` // Recently resolved
    Summary   *IncidentsSummary `json:"summary,omitempty"`
}

type IncidentItem struct {
    ID          string `json:"id"`
    Type        string `json:"type"`     // crash, stuck, quota_exceeded, conflict
    Severity    string `json:"severity"` // P0, P1, P2
    Title       string `json:"title"`
    Description string `json:"description,omitempty"`

    // Scope
    Session     string   `json:"session,omitempty"`
    Panes       []string `json:"panes,omitempty"`
    Agents      []string `json:"agents,omitempty"`

    // Lifecycle
    Status      string `json:"status"` // investigating, mitigating, resolved
    CreatedAt   string `json:"created_at"`
    UpdatedAt   string `json:"updated_at"`
    ResolvedAt  string `json:"resolved_at,omitempty"`

    // Attribution
    DetectedBy  string `json:"detected_by"` // health_monitor, user, alert_escalation
    AssignedTo  string `json:"assigned_to,omitempty"`

    // Resolution
    Resolution  string `json:"resolution,omitempty"` // What fixed it
    RootCause   string `json:"root_cause,omitempty"`

    // Related
    RelatedAlerts   []string `json:"related_alerts,omitempty"`
    RelatedBeads    []string `json:"related_beads,omitempty"`
}

type IncidentsSummary struct {
    TotalActive   int            `json:"total_active"`
    BySeverity    map[string]int `json:"by_severity"`
    ByType        map[string]int `json:"by_type"`
    ByStatus      map[string]int `json:"by_status"`
    MttrMinutes   float64        `json:"mttr_minutes,omitempty"` // Mean time to resolve (30d)
}
```

**Surface usage:**
- `snapshot`: INCLUDE (active summary)
- `diagnose`: INCLUDE (full with history)
- `attention`: INCLUDE (active incidents as action_required)

---

### 3.8 Attention Section

**Purpose:** Prioritized items requiring operator attention.

**Schema:**
```go
type AttentionSection struct {
    Items      []AttentionItem `json:"items"`
    Cursor     int64           `json:"cursor"`
    HasMore    bool            `json:"has_more"`
    Counts     *AttentionCounts `json:"counts,omitempty"`
}

type AttentionItem struct {
    Cursor         int64    `json:"cursor"`
    Timestamp      string   `json:"ts"`       // RFC3339Nano
    Category       string   `json:"category"` // agent, bead, mail, alert, incident
    Type           string   `json:"type"`     // state_change, ready, received, raised
    Actionability  string   `json:"actionability"` // background, interesting, action_required
    Severity       string   `json:"severity"` // debug, info, warning, error, critical

    // Scope
    Session        string   `json:"session,omitempty"`
    Pane           string   `json:"pane,omitempty"`
    EntityID       string   `json:"entity_id,omitempty"` // bead ID, alert ID, etc.

    // Content
    Summary        string   `json:"summary"`
    Details        map[string]interface{} `json:"details,omitempty"`

    // Actions
    NextActions    []ActionSuggestion `json:"next_actions,omitempty"`
}

type AttentionCounts struct {
    Total           int            `json:"total"`
    ActionRequired  int            `json:"action_required"`
    Interesting     int            `json:"interesting"`
    Background      int            `json:"background"`
    ByCategory      map[string]int `json:"by_category"`
}

type ActionSuggestion struct {
    Label       string `json:"label"`
    Command     string `json:"command"` // ntm command to run
    Priority    int    `json:"priority"` // 1 = highest
    Destructive bool   `json:"destructive,omitempty"` // Requires confirmation
}
```

**Surface usage:**
- `snapshot`: INCLUDE (summary counts)
- `attention`: INCLUDE (full, blocking)
- `digest`: INCLUDE (summary with top items)
- `events`: EXCLUDE (use raw events instead)

---

### 3.9 Source Health Section

**Purpose:** Freshness and availability of data sources.

**Schema:**
```go
type SourceHealthSection struct {
    Sources   map[string]SourceInfo `json:"sources"`
    Degraded  []string              `json:"degraded,omitempty"` // List of degraded source names
    AllFresh  bool                  `json:"all_fresh"`
}

type SourceInfo struct {
    Name      string `json:"name"`      // tmux, beads, mail, quota, cass
    Available bool   `json:"available"`
    Fresh     bool   `json:"fresh"`

    // Timing
    AgeMs     int64  `json:"age_ms,omitempty"`     // Time since last successful fetch
    UpdatedAt string `json:"updated_at,omitempty"` // RFC3339

    // Degradation
    Degraded      bool   `json:"degraded,omitempty"`
    DegradedSince string `json:"degraded_since,omitempty"` // RFC3339
    DegradedReason string `json:"degraded_reason,omitempty"`

    // Recovery
    LastError     string `json:"last_error,omitempty"`
    RetryingAt    string `json:"retrying_at,omitempty"` // RFC3339
}
```

**Surface usage:**
- `snapshot`: INCLUDE (full)
- `status`: INCLUDE (degraded only)
- `diagnose`: INCLUDE (full with history)
- All surfaces: EMBED (as `_source_health` when any source is degraded)

---

### 3.10 Next Actions Section

**Purpose:** Mechanically suggested follow-up commands.

**Schema:**
```go
type NextActionsSection struct {
    Actions    []ActionSuggestion `json:"actions"`
    Context    string             `json:"context,omitempty"` // Why these actions are suggested
}

// ActionSuggestion defined in Attention Section (3.8)
```

**Surface usage:**
- `snapshot`: INCLUDE (bootstrap actions)
- `diagnose`: INCLUDE (remediation actions)
- `attention`: INCLUDE (per-item actions)
- `triage`: INCLUDE (work actions)

---

## 4. Section Inclusion Matrix

| Surface | Summary | Sessions | Work | Coordination | Quota | Alerts | Incidents | Attention | Source Health | Next Actions |
|---------|---------|----------|------|--------------|-------|--------|-----------|-----------|---------------|--------------|
| `snapshot` | FULL | FULL | SUMMARY | SUMMARY | SUMMARY | FULL | SUMMARY | SUMMARY | FULL | INCLUDE |
| `status` | FULL | AGENTS | - | COUNTS | - | SUMMARY | - | - | DEGRADED | - |
| `terse` | COUNTS | - | - | - | - | - | - | - | - | - |
| `events` | CURSOR | - | - | - | - | - | - | - | - | - |
| `attention` | CURSOR | - | - | WHEN_PRESENT | WHEN_PRESENT | ACTION_REQUIRED | ACTIVE | FULL | WHEN_DEGRADED | INCLUDE |
| `digest` | COUNTS | - | - | COUNTS | COUNTS | COUNTS | COUNTS | TOP_ITEMS | WHEN_DEGRADED | - |
| `diagnose` | FULL | SINGLE | SUMMARY | FULL | FULL | FULL | FULL | - | FULL | INCLUDE |
| `tail` | - | SINGLE | - | - | - | - | - | - | - | - |
| `quota-status` | - | - | - | - | FULL | - | - | - | - | - |
| `alerts` | - | - | - | - | - | FULL | - | - | - | - |

**Legend:**
- `FULL`: Complete section data
- `SUMMARY`: Aggregate counts and top items
- `COUNTS`: Numbers only
- `SINGLE`: One entity (filtered by parameters)
- `WHEN_PRESENT`: Include when relevant events exist
- `WHEN_DEGRADED`: Include when sources are degraded
- `-`: Not included

---

## 5. Section Ownership

Each section has exactly one authoritative source:

| Section | Owner | Updates Via |
|---------|-------|-------------|
| Summary | Runtime aggregation | Computed on demand |
| Sessions | tmux adapter | Session/pane changes |
| Work | bv/beads adapter | Bead state changes |
| Coordination | mail adapter | Mail events |
| Quota | quota adapter | Account polling |
| Alerts | alert manager | Alert raise/clear |
| Incidents | incident manager | Incident lifecycle |
| Attention | attention journal | All event sources |
| Source Health | health monitor | Source polling |
| Next Actions | context-specific | Per-surface logic |

---

## 6. Go Implementation Guidelines

### 6.1 Section Structs

All section structs should be defined in `internal/robot/sections/` with one file per section:

```
internal/robot/sections/
├── summary.go
├── sessions.go
├── work.go
├── coordination.go
├── quota.go
├── alerts.go
├── incidents.go
├── attention.go
├── source_health.go
└── next_actions.go
```

### 6.2 Projection Builder

A `ProjectionBuilder` assembles sections into surface outputs:

```go
type ProjectionBuilder struct {
    summary      *SummarySection
    sessions     *SessionsSection
    work         *WorkSection
    coordination *CoordinationSection
    quota        *QuotaSection
    alerts       *AlertsSection
    incidents    *IncidentsSection
    attention    *AttentionSection
    sourceHealth *SourceHealthSection
    nextActions  *NextActionsSection
}

func (b *ProjectionBuilder) WithSummary(level SummaryLevel) *ProjectionBuilder
func (b *ProjectionBuilder) WithSessions(filter SessionFilter) *ProjectionBuilder
func (b *ProjectionBuilder) Build() *Projection
```

### 6.3 Surface Rendering

Each surface calls the builder with appropriate configuration:

```go
func RenderSnapshot() (*SnapshotOutput, error) {
    projection := NewProjectionBuilder().
        WithSummary(SummaryLevelFull).
        WithSessions(SessionFilterAll).
        WithWork(WorkLevelSummary).
        WithCoordination(CoordinationLevelSummary).
        WithQuota(QuotaLevelSummary).
        WithAlerts(AlertsLevelFull).
        WithIncidents(IncidentsLevelSummary).
        WithAttention(AttentionLevelSummary).
        WithSourceHealth(SourceHealthLevelFull).
        WithNextActions().
        Build()

    return projection.ToSnapshot()
}
```

---

## 7. Schema Versioning

Each section carries its own version:

```go
type VersionedSection struct {
    SchemaID      string `json:"schema_id"`      // e.g., "ntm:sessions:v1"
    SchemaVersion string `json:"schema_version"` // e.g., "1.0.0"
}
```

Surfaces inherit section versions in their envelope.

---

## 8. Non-Goals

1. **Free-form fields** — Every field must be typed and documented
2. **Section nesting** — Sections are flat; use references for relationships
3. **Computed aggregations in sections** — Aggregations live in Summary; sections are raw data
4. **Per-surface section variants** — One definition, multiple inclusion levels

---

## 9. References

- [Robot Surface Taxonomy](robot-surface-taxonomy.md) — Lane definitions and surface ownership
- [Attention Feed Contract](attention-feed-contract.md) — Event envelope and cursor mechanics
- [Robot API Design](robot-api-design.md) — Naming conventions and output envelope

---

## Appendix: Changelog

- **2026-03-22:** Initial section model (bd-j9jo3.1.2)

---

*Reference: bd-j9jo3.1.2*
