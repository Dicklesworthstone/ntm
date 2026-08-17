# Robot Resource References and Cross-Surface Identity Contract

> **Authoritative reference** for stable resource identifiers and cross-surface correlation.
> All robot surfaces MUST use these reference patterns.

**Status:** AUTHORITATIVE
**Bead:** bd-j9jo3.1.9
**Created:** 2026-03-22

---

## 1. Purpose

This document defines stable machine-readable references for operator-facing entities. The goal is to enable AI agents to correlate data across surfaces, transports, and action loops without relying on brittle text matching.

**Design Goals:**
1. Every entity has a canonical machine-readable reference
2. References are stable across surfaces and transports
3. References work across truncation, pagination, and retry
4. Invalid/stale references are explicitly reported
5. Correlation is mechanical, not heuristic

---

## 2. Reference Format

### 2.1 Canonical Reference Structure

All resource references follow this format:

```
<type>:<scope>/<id>[@<version>]

Examples:
  session:ntm/myproject
  pane:ntm/myproject/0.2
  agent:ntm/myproject/0.2
  bead:ntm/bd-abc123
  alert:ntm/alert-xyz789
  incident:ntm/inc-2026032201
  attention:ntm/evt_20260322033000123456
  action:ntm/act-send-20260322033100
```

### 2.2 Reference Components

| Component | Description | Example |
|-----------|-------------|---------|
| `type` | Entity type | `session`, `pane`, `agent`, `bead` |
| `scope` | Namespace (always `ntm` for local) | `ntm` |
| `id` | Entity identifier | `myproject`, `0.2`, `bd-abc123` |
| `version` | Optional version qualifier | `@v2`, `@cursor:1234` |

### 2.3 Type Prefixes

| Type | Prefix | ID Format | Example |
|------|--------|-----------|---------|
| Session | `session:` | session name | `session:ntm/myproject` |
| Pane | `pane:` | window.pane | `pane:ntm/myproject/0.2` |
| Agent | `agent:` | window.pane (same as pane) | `agent:ntm/myproject/0.2` |
| Bead | `bead:` | bead ID | `bead:ntm/bd-abc123` |
| Alert | `alert:` | alert ID | `alert:ntm/alert-xyz789` |
| Incident | `incident:` | incident ID | `incident:ntm/inc-2026032201` |
| Attention | `attention:` | cursor | `attention:ntm/evt_20260322033000` |
| Event | `event:` | cursor | `event:ntm/evt_20260322033000` |
| Thread | `thread:` | thread ID | `thread:ntm/bd-abc123` |
| Reservation | `reservation:` | reservation ID | `reservation:ntm/res-xyz` |
| Action | `action:` | action ID | `action:ntm/act-send-123` |
| Account | `account:` | account ID | `account:ntm/claude-prod-1` |
| Tool | `tool:` | tool name | `tool:ntm/cass` |

---

## 3. Entity References

### 3.1 Session Reference

```json
{
  "ref": "session:ntm/myproject",
  "name": "myproject",
  "display_name": "myproject"
}
```

**Properties:**
- Session names are unique within an ntm instance
- Reference is stable for session lifetime
- After session deletion, reference becomes invalid

### 3.2 Pane Reference

```json
{
  "ref": "pane:ntm/myproject/0.2",
  "session": "myproject",
  "window": 0,
  "pane": 2,
  "display_id": "0.2"
}
```

**Properties:**
- Format: `<window>.<pane>` (tmux convention)
- Pane IDs are unique within a session
- Reference is stable for pane lifetime
- After pane close, reference becomes invalid

### 3.3 Agent Reference

```json
{
  "ref": "agent:ntm/myproject/0.2",
  "pane_ref": "pane:ntm/myproject/0.2",
  "type": "claude",
  "variant": "opus-4.6"
}
```

**Properties:**
- Agent reference is derived from pane reference
- One agent per pane (agents are pane-bound)
- Agent type/variant may change; reference stays stable

### 3.4 Bead Reference

```json
{
  "ref": "bead:ntm/bd-abc123",
  "id": "bd-abc123",
  "title": "Fix auth bug",
  "status": "open"
}
```

**Properties:**
- Bead IDs are globally unique (prefixed)
- Reference is permanent (even after closure)
- Used as thread IDs in Agent Mail

### 3.5 Alert Reference

```json
{
  "ref": "alert:ntm/alert-xyz789",
  "id": "alert-xyz789",
  "type": "error",
  "severity": "warning"
}
```

**Properties:**
- Alert IDs are unique within ntm instance
- Reference valid until alert is dismissed
- May be auto-dismissed when condition clears

### 3.6 Incident Reference

```json
{
  "ref": "incident:ntm/inc-2026032201",
  "id": "inc-2026032201",
  "status": "investigating",
  "severity": "P1"
}
```

**Properties:**
- Incident IDs include date for sortability
- Reference is permanent for audit trail
- Status may change; reference stays stable

### 3.7 Attention Item Reference

```json
{
  "ref": "attention:ntm/evt_20260322033000123456",
  "cursor": "evt_20260322033000123456",
  "category": "agent",
  "type": "state_change"
}
```

**Properties:**
- Cursor-based reference for event stream items
- Reference is immutable (events are append-only)
- May expire based on retention policy

### 3.8 Action Reference

```json
{
  "ref": "action:ntm/act-send-20260322033100-abc",
  "id": "act-send-20260322033100-abc",
  "type": "send",
  "status": "completed"
}
```

**Properties:**
- Action IDs include timestamp and random suffix
- Reference is permanent for audit trail
- Links to triggering attention item and target entity

---

## 4. Cross-Surface Correlation

### 4.1 Correlation Principle

Every surface that references an entity MUST include its canonical reference:

```json
// In snapshot
{
  "sessions": [
    {
      "ref": "session:ntm/myproject",
      "name": "myproject",
      "agents": [
        {
          "ref": "agent:ntm/myproject/0.2",
          "pane": "0.2",
          "type": "claude"
        }
      ]
    }
  ]
}

// In attention
{
  "items": [
    {
      "ref": "attention:ntm/evt_123456",
      "cursor": "evt_123456",
      "entity_ref": "agent:ntm/myproject/0.2",
      "summary": "Agent became idle"
    }
  ]
}

// In inspect
{
  "ref": "agent:ntm/myproject/0.2",
  "details": { ... }
}
```

### 4.2 Reference Resolution

Given a reference, consumers can:

1. **Drill down**: `ntm --robot-inspect --ref="agent:ntm/myproject/0.2"`
2. **Filter**: `ntm --robot-events --entity="session:ntm/myproject"`
3. **Act**: `ntm --robot-send --target="agent:ntm/myproject/0.2"`

### 4.3 Reference in Commands

Commands accept references as targets:

```bash
# Using reference
ntm --robot-send --target="agent:ntm/myproject/0.2" --msg="Hello"

# Equivalent shorthand (session=myproject, panes=2)
ntm --robot-send=myproject --panes=2 --msg="Hello"
```

---

## 5. Reference Lifecycle

### 5.1 Durability Classes

| Class | Description | Examples |
|-------|-------------|----------|
| **Permanent** | Never expires | beads, incidents, actions |
| **Session-bound** | Valid for session lifetime | sessions, panes, agents |
| **Ephemeral** | Valid for limited time | alerts, attention items |
| **Request-scoped** | Valid only in response context | pagination cursors |

### 5.2 Lifecycle States

```
ACTIVE → STALE → INVALID
```

| State | Meaning | API Behavior |
|-------|---------|--------------|
| `ACTIVE` | Entity exists and is accessible | Normal response |
| `STALE` | Entity was recently removed | `ENTITY_STALE` warning, last-known data |
| `INVALID` | Entity doesn't exist | `ENTITY_NOT_FOUND` error |

### 5.3 Staleness Detection

References include optional freshness hint:

```json
{
  "ref": "session:ntm/myproject",
  "ref_valid": true,
  "ref_checked_at": "2026-03-22T03:35:00Z"
}
```

### 5.4 Invalid Reference Response

When referencing a non-existent entity:

```json
{
  "success": false,
  "error_code": "ENTITY_NOT_FOUND",
  "error": "Referenced entity does not exist",
  "invalid_ref": "session:ntm/deleted-session",
  "hint": "Session may have been killed. Use --robot-status to list current sessions."
}
```

---

## 6. Reference in Truncated Output

### 6.1 Reference Preservation

When output is truncated, references are preserved:

```json
{
  "sessions": [
    { "ref": "session:ntm/a", "name": "a", "agents": [...] },
    { "ref": "session:ntm/b", "name": "b", "agents": [...] }
  ],
  "truncated": true,
  "truncated_refs": [
    "session:ntm/c",
    "session:ntm/d",
    "session:ntm/e"
  ],
  "continuation": "--offset=2 --limit=5"
}
```

**Properties:**
- `truncated_refs` lists omitted entity references
- Consumers can fetch specific refs: `--refs=session:ntm/c,session:ntm/d`

### 6.2 Reference-Based Fetching

<!-- ntm-docs: skip -->
```bash
# Fetch specific entities by reference
ntm --robot-inspect --refs="session:ntm/c,session:ntm/d"

# Filter events by entity reference
ntm --robot-events --entity="agent:ntm/myproject/0.2"
```

---

## 7. Action Targeting

### 7.1 Target Resolution

Actions require a target reference:

```json
// Send action request
{
  "action": "send",
  "target": "agent:ntm/myproject/0.2",
  "payload": {
    "msg": "Start work on bd-abc123"
  }
}

// Action response
{
  "action_ref": "action:ntm/act-send-20260322-xyz",
  "target": "agent:ntm/myproject/0.2",
  "status": "completed",
  "result": { "sent": true }
}
```

### 7.2 Broadcast Targets

Special target references for multi-entity actions:

| Target | Meaning |
|--------|---------|
| `session:ntm/myproject/*` | All panes in session |
| `session:ntm/myproject/agents` | All agent panes in session |
| `agents:ntm/claude/*` | All Claude agents across sessions |
| `agents:ntm/idle` | All idle agents |

### 7.3 Target Validation

Before executing an action, validate target:

```go
func ValidateTarget(ref string) (*TargetInfo, error) {
    parsed, err := ParseReference(ref)
    if err != nil {
        return nil, ErrInvalidReference
    }

    entity, exists := LookupEntity(parsed)
    if !exists {
        return nil, ErrEntityNotFound
    }

    return &TargetInfo{
        Ref:    ref,
        Entity: entity,
        Valid:  true,
    }, nil
}
```

---

## 8. Reference in Events

### 8.1 Event Entity Reference

Every event includes the affected entity:

```json
{
  "cursor": "evt_20260322033000123456",
  "category": "agent",
  "type": "state_change",
  "entity_ref": "agent:ntm/myproject/0.2",
  "entity_type": "agent",
  "session_ref": "session:ntm/myproject"
}
```

### 8.2 Related References

Events may reference multiple entities:

```json
{
  "cursor": "evt_20260322033000123457",
  "category": "conflict",
  "type": "file_conflict",
  "entity_refs": [
    "agent:ntm/myproject/0.1",
    "agent:ntm/myproject/0.2"
  ],
  "related_refs": [
    "bead:ntm/bd-abc123",
    "reservation:ntm/res-xyz"
  ]
}
```

### 8.3 Reference Chain

Events can link to causing events:

```json
{
  "cursor": "evt_20260322033000123458",
  "category": "action",
  "type": "send_completed",
  "entity_ref": "agent:ntm/myproject/0.2",
  "caused_by": "attention:ntm/evt_20260322033000123456",
  "action_ref": "action:ntm/act-send-xyz"
}
```

---

## 9. Go Implementation

### 9.1 Reference Type

```go
package robot

type EntityRef struct {
    Type    string `json:"type"`    // session, pane, agent, bead, etc.
    Scope   string `json:"scope"`   // ntm (always for local)
    ID      string `json:"id"`      // Entity-specific ID
    Version string `json:"version,omitempty"` // Optional version
}

func (r EntityRef) String() string {
    s := fmt.Sprintf("%s:%s/%s", r.Type, r.Scope, r.ID)
    if r.Version != "" {
        s += "@" + r.Version
    }
    return s
}

func ParseRef(s string) (EntityRef, error) {
    // Parse "type:scope/id[@version]"
    ...
}
```

### 9.2 Reference Constants

```go
const (
    RefTypeSession     = "session"
    RefTypePane        = "pane"
    RefTypeAgent       = "agent"
    RefTypeBead        = "bead"
    RefTypeAlert       = "alert"
    RefTypeIncident    = "incident"
    RefTypeAttention   = "attention"
    RefTypeEvent       = "event"
    RefTypeAction      = "action"
    RefTypeThread      = "thread"
    RefTypeReservation = "reservation"
    RefTypeAccount     = "account"
    RefTypeTool        = "tool"

    RefScopeLocal = "ntm"
)
```

### 9.3 Reference Factory

```go
func SessionRef(name string) EntityRef {
    return EntityRef{Type: RefTypeSession, Scope: RefScopeLocal, ID: name}
}

func PaneRef(session string, window, pane int) EntityRef {
    return EntityRef{
        Type:  RefTypePane,
        Scope: RefScopeLocal,
        ID:    fmt.Sprintf("%s/%d.%d", session, window, pane),
    }
}

func BeadRef(id string) EntityRef {
    return EntityRef{Type: RefTypeBead, Scope: RefScopeLocal, ID: id}
}
```

### 9.4 Reference in Structs

```go
type SessionInfo struct {
    Ref         string      `json:"ref"` // Always include
    Name        string      `json:"name"`
    Attached    bool        `json:"attached"`
    Agents      []AgentInfo `json:"agents"`
}

func (s *SessionInfo) SetRef() {
    s.Ref = SessionRef(s.Name).String()
}
```

---

## 10. Reference Validation

### 10.1 Format Validation

```go
func ValidateRefFormat(s string) error {
    // Must match: type:scope/id[@version]
    pattern := `^[a-z]+:[a-z]+/[^@]+(@[^@]+)?$`
    if !regexp.MustCompile(pattern).MatchString(s) {
        return fmt.Errorf("invalid reference format: %s", s)
    }
    return nil
}
```

### 10.2 Existence Validation

```go
func ValidateRefExists(ref EntityRef) (bool, error) {
    switch ref.Type {
    case RefTypeSession:
        return SessionExists(ref.ID)
    case RefTypePane:
        return PaneExists(ref.ID)
    case RefTypeBead:
        return BeadExists(ref.ID)
    // ...
    default:
        return false, fmt.Errorf("unknown ref type: %s", ref.Type)
    }
}
```

---

## 11. Non-Goals

1. **URIs/URLs** — References are not web URLs
2. **Cross-instance references** — Scope is always local (`ntm`)
3. **Version negotiation** — Version suffix is informational only
4. **Reference caching** — Caching is implementation detail
5. **Reference compression** — References are human-readable

---

## 12. References

- [Robot Surface Taxonomy](robot-surface-taxonomy.md) — Surface definitions
- [Robot Projection Sections](robot-projection-sections.md) — Entity schemas
- [Robot Ordering/Pagination](robot-ordering-pagination.md) — Truncation behavior

---

## Appendix: Changelog

- **2026-03-22:** Initial resource reference contract (bd-j9jo3.1.9)

---

*Reference: bd-j9jo3.1.9*
