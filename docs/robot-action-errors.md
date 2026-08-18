# Robot Action Handoff, Remediation, and Error Semantics Contract

> **Authoritative reference** for follow-up actions, recoverable errors, and retry semantics.
> All robot surfaces MUST follow this contract.

**Status:** AUTHORITATIVE
**Bead:** bd-j9jo3.1.7
**Created:** 2026-03-22

---

## 1. Purpose

This document defines how robot surfaces communicate machine-actionable follow-up steps and recoverable failure modes. The goal is to make recovery, drill-down, and actuation handoff explicit so agents don't reverse-engineer what to do next from prose.

**Design Goals:**
1. Every response includes mechanical next steps
2. Errors are classified with explicit recovery paths
3. Retry vs resync decisions are unambiguous
4. Follow-up semantics are stable across transports

---

## 2. Next Actions

### 2.1 Next Actions Shape

Every robot response MAY include a `next_actions` array:

```json
{
  "success": true,
  "next_actions": [
    {
      "id": "drill_down_agent",
      "label": "Inspect agent details",
      "command": "ntm --robot-inspect-pane --ref=\"agent:ntm/myproject/0.2\"",
      "priority": 1,
      "category": "inspect",
      "applicable_when": "agent_state_changed"
    },
    {
      "id": "send_task",
      "label": "Send next task",
      "command": "ntm --robot-send --target=\"agent:ntm/myproject/0.2\" --msg=\"...\"",
      "priority": 2,
      "category": "actuate",
      "requires_input": ["msg"]
    }
  ]
}
```

### 2.2 Action Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Stable identifier for this action type |
| `label` | string | Yes | Human-readable description |
| `command` | string | Yes | Complete ntm command to execute |
| `priority` | int | Yes | Suggested order (1 = highest) |
| `category` | string | Yes | Action category (see §2.3) |
| `applicable_when` | string | No | Condition under which action applies |
| `requires_input` | []string | No | Parameters consumer must supply |
| `destructive` | bool | No | If true, action has side effects |
| `idempotent` | bool | No | If true, safe to retry |

### 2.3 Action Categories

| Category | Description | Examples |
|----------|-------------|----------|
| `inspect` | Drill down for more detail | `inspect-pane`, `tail`, `diagnose` |
| `actuate` | Take action on entity | `send`, `interrupt`, `restart` |
| `paginate` | Get more results | continuation commands |
| `resync` | Re-establish baseline | `snapshot` after cursor expiry |
| `retry` | Repeat failed operation | same command, idempotent |
| `escalate` | Human intervention needed | alert, manual action |

### 2.4 Context-Specific Actions

Actions vary by response context:

**On Success:**
```json
{
  "success": true,
  "next_actions": [
    { "category": "inspect", "label": "View details", ... },
    { "category": "actuate", "label": "Take action", ... }
  ]
}
```

**On Truncation:**
```json
{
  "truncated": true,
  "next_actions": [
    { "category": "paginate", "label": "Get more", "command": "... --offset=20" }
  ]
}
```

**On Error:**
```json
{
  "success": false,
  "next_actions": [
    { "category": "resync", "label": "Resync state", "command": "ntm --robot-snapshot" }
  ]
}
```

---

## 3. Error Taxonomy

### 3.1 Error Categories

| Category | Code Prefix | Recoverable | Description |
|----------|-------------|-------------|-------------|
| **Cursor** | `CURSOR_*` | Yes | Event stream position issues |
| **Entity** | `ENTITY_*` | Varies | Target entity issues |
| **Source** | `SOURCE_*` | Yes | Data source availability |
| **Request** | `REQUEST_*` | No | Malformed request |
| **Transport** | `TRANSPORT_*` | Yes | Connection issues |
| **Quota** | `QUOTA_*` | Varies | Rate limit issues |
| **Internal** | `INTERNAL_*` | No | Unexpected errors |

### 3.2 Cursor Errors

| Code | Message | Recovery |
|------|---------|----------|
| `CURSOR_EXPIRED` | Events before cursor have been garbage-collected | Resync via `--robot-snapshot` |
| `CURSOR_INVALID` | Cursor format is malformed | Fix cursor format |
| `CURSOR_FUTURE` | Cursor is ahead of latest event | Use `latest_cursor` from response |
| `CURSOR_MISSING` | Required cursor not provided | Get cursor from snapshot |

**Example Response:**
```json
{
  "success": false,
  "error_code": "CURSOR_EXPIRED",
  "error": "Events before cursor evt_20260321010000 have been garbage-collected",
  "hint": "Call --robot-snapshot to resync full state and obtain a fresh cursor",
  "recovery": {
    "type": "resync",
    "command": "ntm --robot-snapshot"
  },
  "next_actions": [
    {
      "id": "resync",
      "category": "resync",
      "command": "ntm --robot-snapshot",
      "priority": 1
    }
  ]
}
```

### 3.3 Entity Errors

| Code | Message | Recovery |
|------|---------|----------|
| `ENTITY_NOT_FOUND` | Referenced entity does not exist | Check reference, list entities |
| `ENTITY_STALE` | Entity was recently removed | Use last-known data or refresh |
| `ENTITY_BUSY` | Entity is locked or in use | Wait and retry |
| `ENTITY_INVALID` | Entity reference is malformed | Fix reference format |

**Example Response:**
```json
{
  "success": false,
  "error_code": "ENTITY_NOT_FOUND",
  "error": "Session 'deleted-project' does not exist",
  "invalid_ref": "session:ntm/deleted-project",
  "hint": "Session may have been killed. Use --robot-status to list current sessions.",
  "recovery": {
    "type": "list",
    "command": "ntm --robot-status"
  },
  "next_actions": [
    {
      "id": "list_sessions",
      "category": "inspect",
      "command": "ntm --robot-status",
      "priority": 1
    }
  ]
}
```

### 3.4 Source Errors

| Code | Message | Recovery |
|------|---------|----------|
| `SOURCE_UNAVAILABLE` | Data source is not reachable | Retry or use cached data |
| `SOURCE_DEGRADED` | Source is partially available | Proceed with degraded data |
| `SOURCE_TIMEOUT` | Source did not respond in time | Retry with longer timeout |
| `SOURCE_STALE` | Source data is outdated | Refresh source |

**Example Response:**
```json
{
  "success": true,
  "source_health": {
    "beads": { "available": false, "reason": "bv not installed" },
    "mail": { "available": true, "degraded": true, "reason": "server slow" }
  },
  "_degraded": true,
  "_degraded_sources": ["beads", "mail"],
  "next_actions": [
    {
      "id": "install_bv",
      "category": "escalate",
      "label": "Install bv for bead support",
      "priority": 1
    }
  ]
}
```

### 3.5 Request Errors

| Code | Message | Recovery |
|------|---------|----------|
| `REQUEST_INVALID_FLAG` | Flag value is malformed | Fix flag value |
| `REQUEST_MISSING_PARAM` | Required parameter missing | Add required parameter |
| `REQUEST_CONFLICT` | Conflicting parameters | Remove conflict |
| `REQUEST_UNSUPPORTED` | Feature not available | Use alternative |

**Example Response:**
```json
{
  "success": false,
  "error_code": "REQUEST_MISSING_PARAM",
  "error": "--robot-send requires --msg parameter",
  "hint": "Add --msg=\"your message\" to the command",
  "recovery": {
    "type": "fix_request",
    "missing_params": ["msg"]
  }
}
```

### 3.6 Quota Errors

| Code | Message | Recovery |
|------|---------|----------|
| `QUOTA_EXCEEDED` | Rate limit exceeded | Wait for reset or switch account |
| `QUOTA_WARNING` | Approaching rate limit | Reduce request rate |
| `QUOTA_SUSPENDED` | Account suspended | Use different account |

---

## 4. Recovery Paths

### 4.1 Recovery Decision Tree

```
ERROR
├─ CURSOR_* ─────────────────────────────────────────────┐
│   └─ Resync: ntm --robot-snapshot                      │
├─ ENTITY_NOT_FOUND ─────────────────────────────────────┤
│   └─ List: ntm --robot-status or --robot-bead-list     │
├─ ENTITY_STALE ─────────────────────────────────────────┤
│   └─ Use last-known OR Refresh: same command           │
├─ ENTITY_BUSY ──────────────────────────────────────────┤
│   └─ Wait + Retry: same command, exponential backoff   │
├─ SOURCE_UNAVAILABLE ───────────────────────────────────┤
│   └─ Retry OR Skip: use degraded data                  │
├─ SOURCE_TIMEOUT ───────────────────────────────────────┤
│   └─ Retry: same command with --timeout=60s            │
├─ REQUEST_* ────────────────────────────────────────────┤
│   └─ Fix Request: correct parameters                   │
├─ QUOTA_EXCEEDED ───────────────────────────────────────┤
│   └─ Wait: check reset_at OR switch account            │
└─ INTERNAL_* ───────────────────────────────────────────┤
    └─ Escalate: report bug, manual intervention         │
```

### 4.2 Retry vs Resync

| Condition | Action | Rationale |
|-----------|--------|-----------|
| Transient network error | Retry same command | Idempotent, may succeed |
| Cursor expired | Resync via snapshot | Lost event stream position |
| Entity not found | List then retry | Target may have moved |
| Source unavailable | Retry or skip | Source may recover |
| Request invalid | Fix and retry | User error |
| Quota exceeded | Wait then retry | Rate limit resets |
| Internal error | Escalate | Bug, needs investigation |

### 4.3 Recovery Object

Errors include a structured `recovery` object:

```json
{
  "recovery": {
    "type": "resync",           // resync, retry, fix_request, wait, escalate
    "command": "ntm --robot-snapshot",  // Command to run
    "idempotent": true,         // Safe to repeat
    "wait_seconds": 60,         // For wait type
    "missing_params": ["msg"],  // For fix_request type
    "retryable": true,          // Can retry same command
    "max_retries": 3            // Suggested retry limit
  }
}
```

---

## 5. Retry Semantics

### 5.1 Idempotency

Commands are classified by idempotency:

| Command Type | Idempotent | Safe to Retry |
|--------------|------------|---------------|
| Read commands | Yes | Always |
| Send with same content | No | Only on timeout |
| Spawn existing session | Yes | Always (no-op) |
| Interrupt | No | Avoid (cumulative effect) |
| Close bead | Yes | Always (no-op if closed) |

### 5.2 Retry Guidance

Errors include retry guidance:

```json
{
  "success": false,
  "error_code": "SOURCE_TIMEOUT",
  "retry": {
    "safe": true,
    "backoff": "exponential",
    "initial_delay_ms": 1000,
    "max_retries": 3,
    "max_delay_ms": 30000
  }
}
```

### 5.3 Request Correlation

For non-idempotent commands, include a durable operation ID (`--op-id`):

```bash
ntm --robot-send=myproject --msg="Fix auth" --op-id="deploy-42"
```

The response includes the recorded operation:
```json
{
  "success": true,
  "operation": {
    "operation_id": "deploy-42",
    "status": "completed",
    "payload_sha256": "...",
    "payload_bytes": 8
  }
}
```

An identical retry replays the recorded outcome (`"replayed": true` in
`operation`); reusing the same `--op-id` with a different payload is rejected.
Query the durable receipt later with:

```bash
ntm --robot-send-receipt=deploy-42
```

---

## 6. Drill-Down Handoff

### 6.1 Summary to Detail

When a summary surface identifies something interesting, it includes drill-down actions:

```json
{
  "alerts": [
    {
      "ref": "alert:ntm/alert-xyz",
      "type": "error",
      "message": "Agent crashed",
      "drill_down": {
        "command": "ntm --robot-diagnose=myproject --panes=2",
        "returns": "Full diagnostic with root cause analysis"
      }
    }
  ]
}
```

### 6.2 Inspect to Actuate

When inspection reveals actionable state, it includes actuation options:

```json
{
  "agent": {
    "ref": "agent:ntm/myproject/0.2",
    "state": "error",
    "error_type": "rate_limited"
  },
  "next_actions": [
    {
      "id": "restart_agent",
      "category": "actuate",
      "command": "ntm --robot-smart-restart=myproject --panes=2",
      "label": "Restart with state recovery"
    },
    {
      "id": "switch_account",
      "category": "actuate",
      "command": "ntm --robot-switch-account --to=backup",
      "label": "Switch to backup account"
    }
  ]
}
```

### 6.3 Actuation to Verification

When actuation completes, it includes verification actions:

```json
{
  "action": "send",
  "status": "completed",
  "next_actions": [
    {
      "id": "wait_response",
      "category": "wait",
      "command": "ntm --robot-wait=myproject --condition=any_output --timeout=60s",
      "label": "Wait for agent response"
    },
    {
      "id": "check_status",
      "category": "inspect",
      "command": "ntm --robot-activity=myproject",
      "label": "Check agent activity"
    }
  ]
}
```

---

## 7. Transport Consistency

### 7.1 Error Consistency

All transports return the same error structure:

**CLI JSON:**
```json
{ "success": false, "error_code": "CURSOR_EXPIRED", ... }
```

**REST:**
```json
{ "success": false, "error_code": "CURSOR_EXPIRED", ... }
```

**SSE:**
```
event: error
data: { "error_code": "CURSOR_EXPIRED", ... }
```

**WebSocket:**
```json
{ "type": "error", "error_code": "CURSOR_EXPIRED", ... }
```

### 7.2 Action Consistency

Actions use same commands regardless of transport:

```json
{
  "next_actions": [
    {
      "command": "ntm --robot-snapshot",  // Always CLI format
      "rest_endpoint": "GET /api/robot/snapshot",  // REST equivalent
      "sse_action": "subscribe",  // SSE action type
      "ws_message": { "type": "snapshot" }  // WebSocket message
    }
  ]
}
```

---

## 8. Go Implementation

### 8.1 Action Types

```go
type NextAction struct {
    ID             string   `json:"id"`
    Label          string   `json:"label"`
    Command        string   `json:"command"`
    Priority       int      `json:"priority"`
    Category       string   `json:"category"`  // inspect, actuate, paginate, resync, retry, escalate
    ApplicableWhen string   `json:"applicable_when,omitempty"`
    RequiresInput  []string `json:"requires_input,omitempty"`
    Destructive    bool     `json:"destructive,omitempty"`
    Idempotent     bool     `json:"idempotent,omitempty"`
}

type Recovery struct {
    Type          string   `json:"type"`  // resync, retry, fix_request, wait, escalate
    Command       string   `json:"command,omitempty"`
    Idempotent    bool     `json:"idempotent,omitempty"`
    WaitSeconds   int      `json:"wait_seconds,omitempty"`
    MissingParams []string `json:"missing_params,omitempty"`
    Retryable     bool     `json:"retryable,omitempty"`
    MaxRetries    int      `json:"max_retries,omitempty"`
}

type RetryGuidance struct {
    Safe           bool   `json:"safe"`
    Backoff        string `json:"backoff"`  // none, linear, exponential
    InitialDelayMs int    `json:"initial_delay_ms"`
    MaxRetries     int    `json:"max_retries"`
    MaxDelayMs     int    `json:"max_delay_ms"`
}
```

### 8.2 Error Builders

```go
func CursorExpiredError(cursor string) *RobotResponse {
    return &RobotResponse{
        Success:   false,
        ErrorCode: "CURSOR_EXPIRED",
        Error:     fmt.Sprintf("Events before cursor %s have been garbage-collected", cursor),
        Hint:      "Call --robot-snapshot to resync full state and obtain a fresh cursor",
        Recovery: &Recovery{
            Type:       "resync",
            Command:    "ntm --robot-snapshot",
            Idempotent: true,
        },
        NextActions: []NextAction{
            {
                ID:       "resync",
                Category: "resync",
                Command:  "ntm --robot-snapshot",
                Priority: 1,
                Label:    "Resync state",
            },
        },
    }
}
```

---

## 9. Non-Goals

1. **Natural language recovery** — Recovery is mechanical, not prose
2. **Auto-recovery** — Consumers decide when to recover
3. **Complex predicates** — Actions are enumerated, not computed
4. **Cross-session recovery** — Recovery is per-request

---

## 10. References

- [Robot Surface Taxonomy](robot-surface-taxonomy.md) — Lane definitions
- [Robot Resource References](robot-resource-references.md) — Entity references
- [Robot Ordering/Pagination](robot-ordering-pagination.md) — Truncation handling

---

## Appendix: Changelog

- **2026-03-22:** Initial action/error contract (bd-j9jo3.1.7)

---

*Reference: bd-j9jo3.1.7*
