# Robot Projection Section Model

> **Authoritative reference** for the normalized section model shared across ntm robot surfaces.
> All robot outputs MUST project from this model.

**Status:** AUTHORITATIVE
**Bead:** bd-j9jo3.1.2
**Created:** 2026-03-22
**Inspired by:** vibe_cockpit (RobotEnvelope, staleness tracking, DigestSection)

---

## 1. Purpose

This document defines the normalized sections that major robot surfaces share. The goal is to make renderers and APIs project from one section model rather than each inventing ad hoc composition rules.

**Design Goal:** Consistent section boundaries that drive SQLite schema and output schemas downstream.

**Anti-Goal:** Free-floating fields with ambiguous ownership.

---

## 2. The Projection Envelope

Every major robot response is wrapped in a standard envelope:

```go
type RobotEnvelope struct {
    // Schema identification
    SchemaVersion   string `json:"schema_version"`    // e.g., "1.0.0"
    SchemaID        string `json:"schema_id"`         // e.g., "ntm:snapshot:v1"
    
    // Standard envelope
    Success         bool      `json:"success"`
    Timestamp       time.Time `json:"timestamp"`
    Error           string    `json:"error,omitempty"`
    ErrorCode       string    `json:"error_code,omitempty"`
    Hint            string    `json:"hint,omitempty"`
    
    // Source freshness (per-section)
    Sources         SourceHealth `json:"sources,omitempty"`
    
    // Warnings about data quality
    Warnings        []string `json:"warnings,omitempty"`
    
    // Cursor for event-based surfaces
    Cursor          string `json:"cursor,omitempty"`
}
```

---

## 3. Source Health

Every source (tmux, beads, mail, etc.) has explicit freshness tracking:

```go
type SourceHealth struct {
    Sessions    SourceStatus `json:"sessions,omitempty"`
    Work        SourceStatus `json:"work,omitempty"`
    Coordination SourceStatus `json:"coordination,omitempty"`
    Quota       SourceStatus `json:"quota,omitempty"`
    Alerts      SourceStatus `json:"alerts,omitempty"`
    Incidents   SourceStatus `json:"incidents,omitempty"`
}

type SourceStatus struct {
    Fresh          bool      `json:"fresh"`
    AgeMs          int64     `json:"age_ms,omitempty"`
    CollectedAt    time.Time `json:"collected_at,omitempty"`
    Degraded       bool      `json:"degraded,omitempty"`
    DegradedReason string    `json:"degraded_reason,omitempty"`
    DegradedSince  time.Time `json:"degraded_since,omitempty"`
}
```

**Freshness rules:**
- `Fresh=true` if age < 30s
- `Fresh=false` if age >= 30s
- `Degraded=true` if source is unavailable

---

## 4. Canonical Sections

### 4.1 Summary Section

High-level counts for quick assessment. Cheap to compute, suitable for frequent polling.

```go
type SummarySection struct {
    TotalSessions   int `json:"total_sessions"`
    TotalAgents     int `json:"total_agents"`
    AgentsIdle      int `json:"agents_idle"`
    AgentsBusy      int `json:"agents_busy"`
    AgentsError     int `json:"agents_error"`
    
    OpenBeads       int `json:"open_beads"`
    ReadyBeads      int `json:"ready_beads"`
    BlockedBeads    int `json:"blocked_beads"`
    
    OpenAlerts      int `json:"open_alerts"`
    OpenIncidents   int `json:"open_incidents"`
    
    UnreadMail      int `json:"unread_mail"`
    PendingAck      int `json:"pending_ack"`
    
    OverallHealth   string `json:"overall_health"` // "healthy", "degraded", "critical"
}
```

**Used by:** status, terse, dashboard

---

### 4.2 Sessions Section

Full session and agent state. Detailed view for inspection.

```go
type SessionsSection struct {
    Sessions []Session `json:"sessions"`
}

type Session struct {
    Name        string    `json:"name"`
    Window      string    `json:"window"`
    Attached    bool      `json:"attached"`
    CreatedAt   time.Time `json:"created_at"`
    
    Agents      []Agent   `json:"agents"`
    UserPane    *Pane     `json:"user_pane,omitempty"`
}

type Agent struct {
    PaneIndex   int       `json:"pane_index"`
    PaneID      string    `json:"pane_id"`       // "0.1", "0.2", etc.
    Type        string    `json:"type"`          // "claude", "codex", "gemini"
    Model       string    `json:"model,omitempty"`
    State       string    `json:"state"`         // "idle", "busy", "error", "compacting"
    StateAge    string    `json:"state_age"`     // "2m30s"
    ContextPct  float64   `json:"context_pct,omitempty"`
    LastOutput  time.Time `json:"last_output,omitempty"`
}

type Pane struct {
    Index       int    `json:"index"`
    Active      bool   `json:"active"`
    Title       string `json:"title,omitempty"`
}
```

**Used by:** snapshot, status (abbreviated)

---

### 4.3 Work Section

Bead state for task management.

```go
type WorkSection struct {
    Ready    []BeadSummary `json:"ready"`
    Blocked  []BeadSummary `json:"blocked"`
    InProgress []BeadSummary `json:"in_progress"`
    
    GraphHealth GraphHealth `json:"graph_health,omitempty"`
}

type BeadSummary struct {
    ID          string `json:"id"`
    Title       string `json:"title"`
    Priority    int    `json:"priority"`
    Type        string `json:"type"`
    Labels      []string `json:"labels,omitempty"`
    Assignee    string `json:"assignee,omitempty"`
    BlockedBy   []string `json:"blocked_by,omitempty"`
    Unblocks    int    `json:"unblocks,omitempty"`
}

type GraphHealth struct {
    TotalOpen   int     `json:"total_open"`
    Cycles      int     `json:"cycles"`
    MaxDepth    int     `json:"max_depth"`
    Velocity    float64 `json:"velocity,omitempty"` // beads/day
}
```

**Used by:** snapshot, triage, plan

---

### 4.4 Coordination Section

Agent Mail state for multi-agent coordination.

```go
type CoordinationSection struct {
    Inbox       InboxSummary `json:"inbox"`
    Reservations []FileLease `json:"reservations,omitempty"`
    ActiveThreads []ThreadSummary `json:"active_threads,omitempty"`
}

type InboxSummary struct {
    Unread     int `json:"unread"`
    Urgent     int `json:"urgent"`
    PendingAck int `json:"pending_ack"`
}

type FileLease struct {
    Path       string    `json:"path"`
    Agent      string    `json:"agent"`
    Exclusive  bool      `json:"exclusive"`
    ExpiresAt  time.Time `json:"expires_at"`
}

type ThreadSummary struct {
    ID         string    `json:"id"`
    Subject    string    `json:"subject"`
    Messages   int       `json:"messages"`
    LastUpdate time.Time `json:"last_update"`
}
```

**Used by:** snapshot, mail

---

### 4.5 Quota Section

Resource usage and limits.

```go
type QuotaSection struct {
    Providers []ProviderQuota `json:"providers,omitempty"`
    RCH       *RCHQuota       `json:"rch,omitempty"`
    System    *SystemQuota    `json:"system,omitempty"`
}

type ProviderQuota struct {
    Provider    string  `json:"provider"` // "anthropic", "openai", "google"
    UsedPct     float64 `json:"used_pct"`
    ResetsAt    time.Time `json:"resets_at,omitempty"`
    Warning     bool    `json:"warning"`
    Exceeded    bool    `json:"exceeded"`
}

type RCHQuota struct {
    WorkersAvailable int `json:"workers_available"`
    WorkersTotal     int `json:"workers_total"`
    QueuedJobs       int `json:"queued_jobs"`
}

type SystemQuota struct {
    CPUPct     float64 `json:"cpu_pct,omitempty"`
    MemPct     float64 `json:"mem_pct,omitempty"`
    DiskPct    float64 `json:"disk_pct,omitempty"`
}
```

**Used by:** snapshot, diagnose, health

---

### 4.5.1 Resource Pressure Projection

`--robot-snapshot` also exposes a compact `resource_pressure` object for
large-swarm operators. It reuses the pressure governor's stable robot
projection rather than inventing a snapshot-only schema.

```go
type RobotPressure struct {
    Success           bool          `json:"success"`
    Timestamp         string        `json:"timestamp"`
    Mode              string        `json:"mode"` // observe|enforce
    Overall           string        `json:"overall"` // low|normal|elevated|high|critical
    Limiting          []string      `json:"limiting"`
    RecommendedAction string        `json:"recommended_action"`
    Enforcing         bool          `json:"enforcing"`
    Sources           []RobotSource `json:"sources"`
}

type RobotSource struct {
    Source string  `json:"source"` // cpu, memory, load, proc_count, rch_queue, ...
    Value  float64 `json:"value"`
    Unit   string  `json:"unit,omitempty"` // usually ratio
    Level  string  `json:"level"`
}
```

Operators should treat `overall=high|critical` as a reason to defer
non-urgent large spawns or assignment bursts. `proc_count` is normalized
against a host-scaled process budget (`max(CPU*256, 4096)` by default)
so small laptops and 64+ core swarm hosts can use the same level tokens.

**Used by:** snapshot, spawn admission, future assignment/backpressure gates

---

### 4.6 Alerts Section

Active alerts requiring attention.

```go
type AlertsSection struct {
    Active  []Alert `json:"active"`
    Recent  []Alert `json:"recent,omitempty"` // last 24h
}

type Alert struct {
    ID          string    `json:"id"`
    Type        string    `json:"type"`      // "quota", "error", "crash", "conflict"
    Severity    string    `json:"severity"`  // "info", "warning", "error", "critical"
    Session     string    `json:"session,omitempty"`
    Pane        string    `json:"pane,omitempty"`
    Summary     string    `json:"summary"`
    CreatedAt   time.Time `json:"created_at"`
    Actionability string  `json:"actionability"` // "background", "interesting", "action_required"
}
```

**Used by:** snapshot, attention, digest

---

### 4.7 Incidents Section

Durable escalations for recurring operator pain.

```go
type IncidentsSection struct {
    Open    []Incident `json:"open"`
    Recent  []Incident `json:"recent,omitempty"` // last 7 days
}

type Incident struct {
    ID          string    `json:"id"`
    Title       string    `json:"title"`
    Severity    string    `json:"severity"` // "minor", "major", "critical"
    State       string    `json:"state"`    // "open", "acknowledged", "resolved"
    Session     string    `json:"session,omitempty"`
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
    AlertCount  int       `json:"alert_count"`
    Timeline    []IncidentEvent `json:"timeline,omitempty"`
}

type IncidentEvent struct {
    Timestamp   time.Time `json:"timestamp"`
    Type        string    `json:"type"` // "created", "escalated", "acknowledged", "comment", "resolved"
    Actor       string    `json:"actor,omitempty"`
    Details     string    `json:"details,omitempty"`
}
```

**Used by:** snapshot, attention, diagnose

---

### 4.8 Attention Section

Prioritized items currently needing attention.

```go
type AttentionSection struct {
    Items           []AttentionItem `json:"items"`
    TotalActionReq  int             `json:"total_action_required"`
    LastEventAt     time.Time       `json:"last_event_at,omitempty"`
}

type AttentionItem struct {
    Rank        int      `json:"rank"`
    Category    string   `json:"category"`  // "agent", "bead", "alert", "mail", "conflict"
    Type        string   `json:"type"`
    Summary     string   `json:"summary"`
    Severity    string   `json:"severity"`
    AgeSec      int64    `json:"age_sec"`
    Session     string   `json:"session,omitempty"`
    Pane        string   `json:"pane,omitempty"`
    EntityID    string   `json:"entity_id,omitempty"` // bead ID, alert ID, etc.
    NextActions []Action `json:"next_actions,omitempty"`
}

type Action struct {
    Label     string `json:"label"`
    Command   string `json:"command"`
    Rationale string `json:"rationale,omitempty"`
}
```

**Used by:** attention, digest, events

---

### 4.9 Source Health Section

Per-source data freshness (meta-section).

```go
type SourceHealthSection struct {
    Sessions     SourceStatus `json:"sessions"`
    Work         SourceStatus `json:"work"`
    Coordination SourceStatus `json:"coordination"`
    Quota        SourceStatus `json:"quota"`
    Alerts       SourceStatus `json:"alerts"`
    Incidents    SourceStatus `json:"incidents"`
    
    DegradedSources []string `json:"degraded_sources,omitempty"`
}
```

**Used by:** All major surfaces (always present)

---

### 4.10 Next Actions Section

Mechanical follow-up commands (cross-cutting).

```go
type NextActionsSection struct {
    Actions []Action `json:"actions,omitempty"`
}
```

**Used by:** Embedded in AttentionItem, Alert, Incident

---

## 5. Section Composition by Surface

| Surface | summary | sessions | work | coord | quota | alerts | incidents | attention | source_health |
|---------|---------|----------|------|-------|-------|--------|-----------|-----------|---------------|
| snapshot | full | full | full | full | full | full | full | - | full |
| status | full | abbrev | - | abbrev | - | count | count | - | full |
| events | - | - | - | - | - | - | - | raw | - |
| attention | - | - | - | - | - | - | - | full | - |
| digest | brief | - | brief | - | - | brief | brief | top-N | brief |
| terse | counts | - | - | - | - | - | - | - | degraded-only |

**Legend:**
- `full`: Complete section data
- `abbrev`: Abbreviated view (counts + top items)
- `brief`: Summary counts only
- `count`: Just the count
- `top-N`: Top N items
- `raw`: Raw events, not aggregated
- `-`: Not included

---

## 6. Section Ownership

Each section has exactly ONE authoritative source:

| Section | Owner | Source |
|---------|-------|--------|
| sessions | tmux adapter | `tmux list-sessions`, `tmux list-panes`, activity detector |
| work | beads adapter | `.beads/`, `br` CLI |
| coordination | mail adapter | Agent Mail server |
| quota | quota adapter | Provider APIs, caut, rch |
| alerts | alert manager | Internal alert pipeline |
| incidents | incident store | SQLite incidents table |
| attention | attention journal | SQLite attention table |
| source_health | collector health | Per-adapter health checks |

---

## 7. Degraded Section Markers

When a section's source is unavailable, the section includes degraded markers:

```json
{
  "work": {
    "_degraded": true,
    "_degraded_reason": "beads database locked",
    "_degraded_since": "2026-03-22T02:00:00Z",
    "ready": null,
    "blocked": null,
    "in_progress": null
  }
}
```

**Rules:**
1. Degraded sections MUST include `_degraded`, `_degraded_reason`, `_degraded_since`
2. Data fields MUST be null (not empty arrays or defaults)
3. `source_health` section MUST list degraded sources

---

## 8. Section Stability Contract

### 8.1 Field Stability

- **Required fields** are always present and typed consistently
- **Optional fields** (marked `omitempty`) may be absent but never change type
- **Array fields** are never null (empty array if no items)
- **Nested sections** follow same rules recursively

### 8.2 Breaking Changes

These changes require schema version bump:
- Removing a required field
- Changing a field's type
- Changing enum values (state, severity, type)
- Changing field semantics

These changes are additive (no version bump):
- Adding new optional fields
- Adding new enum values
- Adding new sections

---

## 9. SQLite Backing

Each section maps to SQLite tables:

| Section | Tables |
|---------|--------|
| sessions | `robot_sessions`, `robot_agents` |
| work | Backed by `.beads/` (no SQLite) |
| coordination | `robot_mail_cache` (cache only) |
| quota | `robot_quota_samples` |
| alerts | `robot_alerts` |
| incidents | `robot_incidents`, `robot_incident_events` |
| attention | `robot_attention_journal` |
| source_health | `robot_source_health` |

---

## 10. Section Justification

When adding a new section, answer:

1. **Ownership:** What source owns this data?
2. **Boundaries:** What fields belong here vs. another section?
3. **Surfaces:** Which surfaces include this section?
4. **Freshness:** How does staleness affect this section?
5. **Degraded:** How does this section behave when degraded?

If these questions cannot be answered clearly, the data should be fields in an existing section.

---

## Appendix: Changelog

- **2026-03-22:** Initial section model (bd-j9jo3.1.2)

---

*Reference: bd-j9jo3.1.2*
