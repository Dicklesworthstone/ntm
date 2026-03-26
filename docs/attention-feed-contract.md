# Attention Feed Contract

> **Authoritative specification for the ntm attention feed system.**
> This document defines the JSON semantics, event envelopes, cursor mechanics, and operator loop for AI agents consuming ntm's attention feed.

**Status:** IMPLEMENTED
**Bead:** br-aa0nj (initial), bd-j9jo3.9.4 (refresh)
**Created:** 2026-03-21
**Updated:** 2026-03-26

---

## 1. Foundational Principle

**The LLM is the driver; ntm is the nervous system.**

ntm provides:
- **Sensing:** observe agent states, session health, alerts, beads, mail
- **Actuation:** spawn, send, interrupt, wait, route

ntm does NOT provide:
- Planning, scheduling, or mission runtime
- A second source of truth for task/coordination state
- Opaque heuristics that cannot be explained in code/tests

The attention feed is a **read-only event stream** that tells the operator "what changed" so they can decide what to do next. All actions flow through existing robot commands.

---

## 2. The Operator Loop

The canonical operator loop is:

```
1. BOOTSTRAP:  snapshot → establish baseline state, get initial cursor
2. POLL:       events?cursor=X → get new events since cursor
3. TRIAGE:     digest/attention → get prioritized what-changed summary
4. ACT:        robot-send, robot-interrupt, etc. → take action
5. WAIT:       wait?condition=X → block until condition met
6. REPEAT:     goto POLL
```

### 2.1 Cold Start

On first invocation or after context loss, an operator MUST call `--robot-snapshot` to establish baseline state. This returns:
- Full system state (sessions, agents, beads, alerts, mail)
- An initial cursor for subsequent `--robot-events` calls
- Profile/defaults in effect

### 2.2 Cursor Expiration and Resync

If a cursor expires (events have been garbage-collected), the events command returns:

```json
{
  "success": false,
  "error_code": "CURSOR_EXPIRED",
  "error": "Cursor 'evt_2026032102300000' has expired. Events before this cursor have been garbage-collected.",
  "hint": "Call --robot-snapshot to resync full state and obtain a fresh cursor.",
  "resync_required": true
}
```

The operator MUST call `--robot-snapshot` to resync and obtain a fresh cursor.

---

## 3. Event Envelope

All events share a normalized envelope:

```json
{
  "cursor": "evt_20260321023045123456",
  "ts": "2026-03-21T02:30:45.123456Z",
  "session": "myproject",
  "pane": "0.2",
  "category": "agent",
  "type": "state_change",
  "source": "activity_detector",
  "actionability": "interesting",
  "severity": "info",
  "summary": "Agent cc_2 transitioned from busy to idle",
  "details": {
    "agent_type": "claude",
    "previous_state": "busy",
    "current_state": "idle",
    "duration_sec": 342
  },
  "next_actions": [
    {
      "label": "Check output",
      "command": "ntm --robot-tail=myproject --panes=2 --lines=100"
    },
    {
      "label": "Send next task",
      "command": "ntm --robot-send=myproject --panes=2 --msg=\"...\""
    }
  ]
}
```

### 3.1 Field Definitions

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cursor` | string | Yes | Monotonically increasing cursor for replay. Format: `evt_<timestamp_nanos>` |
| `ts` | string | Yes | RFC3339Nano timestamp when event occurred |
| `session` | string | No | Session name if event is session-scoped. Absent for global events. |
| `pane` | string | No | Pane identifier (`window.pane`) if event is pane-scoped. Absent otherwise. |
| `category` | string | Yes | Event category. See §3.2 |
| `type` | string | Yes | Event type within category. See §3.2 |
| `source` | string | Yes | Component that generated event (e.g., `activity_detector`, `health_monitor`, `bead_tracker`) |
| `actionability` | string | Yes | See §4 |
| `severity` | string | Yes | `debug`, `info`, `warning`, `error`, `critical` |
| `summary` | string | Yes | Human-readable one-line summary |
| `details` | object | No | Event-specific structured data. Schema varies by type. |
| `next_actions` | array | No | Mechanical follow-up commands. See §5. |

### 3.2 Event Categories and Types

**Category: `agent`**
| Type | Description |
|------|-------------|
| `state_change` | Agent transitioned between idle/busy/error/compacting |
| `output_detected` | New output appeared in agent pane |
| `error_detected` | Error pattern detected in output |
| `compaction_started` | Agent began context compaction |
| `compaction_completed` | Agent finished context compaction |
| `crash_detected` | Agent process appears to have crashed |
| `restart_required` | Agent needs restart (e.g., rate limit hit) |

**Category: `session`**
| Type | Description |
|------|-------------|
| `created` | New session spawned |
| `attached` | Session attached |
| `detached` | Session detached |
| `killed` | Session terminated |
| `agent_added` | New agent pane added |
| `agent_removed` | Agent pane removed |

**Category: `bead`**
| Type | Description |
|------|-------------|
| `ready` | Bead became ready (blockers cleared) |
| `claimed` | Bead claimed by an agent |
| `closed` | Bead closed |
| `blocked` | Bead became blocked |
| `priority_changed` | Bead priority changed |

**Category: `mail`**
| Type | Description |
|------|-------------|
| `received` | New mail received |
| `ack_requested` | Message requires acknowledgment |
| `thread_updated` | Thread received new message |

**Category: `alert`**
| Type | Description |
|------|-------------|
| `raised` | New alert raised |
| `cleared` | Alert condition cleared |
| `escalated` | Alert escalated in severity |

**Category: `conflict`**
| Type | Description |
|------|-------------|
| `file_conflict` | Multiple agents editing same file |
| `reservation_conflict` | File reservation conflict |

**Category: `health`**
| Type | Description |
|------|-------------|
| `degraded` | System health degraded |
| `recovered` | System health recovered |
| `quota_warning` | API quota approaching limit |
| `quota_exceeded` | API quota exceeded |

**Category: `system`**
| Type | Description |
|------|-------------|
| `heartbeat` | Periodic liveness signal (see §10) |
| `cursor_expiry_warning` | Cursor approaching expiration |

---

## 4. Actionability Classes

Events are classified by how urgently they demand operator attention:

| Class | Meaning | Examples |
|-------|---------|----------|
| `background` | Informational, no action typically needed | Agent became idle, session attached, heartbeat |
| `interesting` | Worth noticing but not urgent | Agent output detected, bead ready, mail received |
| `action_required` | Demands prompt operator decision | Error detected, quota exceeded, conflict, crash |

**Important:** Actionability is NOT the same as severity. A `warning` severity event might be `background` (e.g., quota at 80%), while an `info` severity event might be `action_required` (e.g., explicit approval request).

---

## 5. Next Actions

The `next_actions` array provides mechanical follow-up commands. These are NOT suggestions or heuristics—they are concrete robot commands that make sense as immediate responses.

### 5.1 Rules

1. **Must be executable:** Each action must be a valid ntm robot command.
2. **Must be self-contained:** No placeholders or `...` that require operator inference.
3. **Must point to real surfaces:** Only reference commands that exist in `--robot-capabilities`.
4. **Must be mechanically derivable:** If the action requires judgment, omit it.
5. **No hypothetical commands:** Never suggest commands that might exist in the future.

### 5.2 Structure

```json
{
  "next_actions": [
    {
      "label": "Check agent output",
      "command": "ntm --robot-tail=myproject --panes=2 --lines=50",
      "rationale": "Agent just finished work"
    },
    {
      "label": "Diagnose error",
      "command": "ntm --robot-diagnose=myproject --pane=2",
      "rationale": "Error pattern detected"
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `label` | string | Yes | Short human-readable action name |
| `command` | string | Yes | Complete ntm command to execute |
| `rationale` | string | No | Brief explanation why this action is relevant |

---

## 6. Cursor Semantics

### 6.1 Format

Cursors are opaque strings with the format: `evt_<timestamp_nanos>`

Example: `evt_20260321023045123456789`

### 6.2 Properties

1. **Monotonically increasing:** cursor A < cursor B iff A's timestamp < B's timestamp
2. **Globally ordered:** Cursors are ordered across all event sources
3. **Replay-safe:** Requesting events with cursor X returns all events with cursor > X
4. **Idempotent:** Same cursor always returns same events (modulo garbage collection)

### 6.3 Retention and Expiration

- Events are retained for at least **1 hour** after generation
- Retention may be extended based on system load and storage
- When a cursor expires, `CURSOR_EXPIRED` error is returned (see §2.2)

### 6.4 Cursor in Snapshot

`--robot-snapshot` always returns a `cursor` field representing the current event position:

```json
{
  "success": true,
  "cursor": "evt_20260321023045123456789",
  "ts": "2026-03-21T02:30:45Z",
  "sessions": [...]
}
```

---

## 7. Command Cluster

### 7.1 `--robot-snapshot`

Full state dump with cursor for subsequent event polling.

```bash
ntm --robot-snapshot
ntm --robot-snapshot --since=2026-03-21T00:00:00Z  # Delta since timestamp
```

**Response:**
```json
{
  "success": true,
  "timestamp": "2026-03-21T02:30:45Z",
  "cursor": "evt_20260321023045123456789",
  "ts": "2026-03-21T02:30:45Z",
  "sessions": [...],
  "beads_summary": {...},
  "agent_mail": {...},
  "alerts": [...],
  "alerts_detailed": [...],
  "profile": {
    "name": "default",
    "description": "Standard monitoring profile"
  },
  "_agent_hints": {...}
}
```

### 7.2 `--robot-events`

Incremental event stream since cursor.

```bash
ntm --robot-events --cursor=evt_20260321023045123456789
ntm --robot-events --cursor=evt_... --limit=100
ntm --robot-events --cursor=evt_... --category=agent,bead
ntm --robot-events --cursor=evt_... --actionability=action_required
```

**Response:**
```json
{
  "success": true,
  "timestamp": "2026-03-21T02:31:00Z",
  "cursor": "evt_20260321023100987654321",
  "events": [
    { /* event envelope */ },
    { /* event envelope */ }
  ],
  "has_more": false,
  "retention_expires_at": "2026-03-21T03:30:45Z"
}
```

**Parameters:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--cursor` | string | Required | Cursor from previous snapshot/events call |
| `--limit` | int | 100 | Max events to return |
| `--category` | string | all | Comma-separated category filter |
| `--actionability` | string | all | Filter: `background`, `interesting`, `action_required` |
| `--severity` | string | all | Filter: `debug`, `info`, `warning`, `error`, `critical` |
| `--session` | string | all | Filter to specific session |

### 7.3 `--robot-digest`

Token-efficient summary of what changed since cursor.

```bash
ntm --robot-digest --cursor=evt_20260321023045123456789
ntm --robot-digest --cursor=evt_... --format=terse
```

**Response:**
```json
{
  "success": true,
  "timestamp": "2026-03-21T02:31:00Z",
  "cursor": "evt_20260321023100987654321",
  "digest": {
    "period_start": "2026-03-21T02:30:45Z",
    "period_end": "2026-03-21T02:31:00Z",
    "event_count": 12,
    "by_category": {
      "agent": 5,
      "bead": 3,
      "alert": 2,
      "health": 2
    },
    "by_actionability": {
      "background": 8,
      "interesting": 3,
      "action_required": 1
    },
    "highlights": [
      "Agent cc_2 finished task (idle after 5m42s)",
      "Bead br-xyz became ready",
      "Alert: quota at 85%"
    ],
    "action_required_summary": [
      {
        "event_type": "alert.raised",
        "summary": "API quota at 85% - consider throttling",
        "command": "ntm --robot-diagnose=myproject"
      }
    ]
  }
}
```

### 7.4 `--robot-wait`

Block until a named condition is met.

```bash
ntm --robot-wait=myproject --condition=idle --timeout=5m
ntm --robot-wait=myproject --condition=any_output --timeout=2m
ntm --robot-wait=myproject --condition=action_required --timeout=10m
```

**Named Conditions (exhaustive list):**

| Condition | Description |
|-----------|-------------|
| `idle` | All agents in session are idle |
| `any_idle` | At least one agent is idle |
| `busy` | At least one agent is busy |
| `all_busy` | All agents are busy |
| `any_output` | Any agent produced new output |
| `any_error` | Any agent encountered an error |
| `action_required` | Any action_required event occurred |
| `bead_ready` | Any bead became ready |
| `mail_received` | Any mail was received |

**No free-form condition DSL.** Conditions are enumerated and documented. This prevents:
- Ambiguity in condition parsing
- Version skew between client expectations and server capabilities
- Untestable condition combinations

**Response:**
```json
{
  "success": true,
  "timestamp": "2026-03-21T02:35:00Z",
  "condition": "idle",
  "met": true,
  "waited_ms": 45230,
  "cursor": "evt_20260321023500123456789",
  "triggering_event": {
    "cursor": "evt_20260321023459987654321",
    "type": "agent.state_change",
    "summary": "Agent cc_2 became idle"
  }
}
```

### 7.5 `--robot-attention`

Get prioritized items that currently need attention.

```bash
ntm --robot-attention
ntm --robot-attention --limit=5
ntm --robot-attention --attention-session=myproject
```

**Response:**
```json
{
  "success": true,
  "timestamp": "2026-03-21T02:31:00Z",
  "items": [
    {
      "rank": 1,
      "category": "alert",
      "type": "quota_warning",
      "summary": "API quota at 85%",
      "severity": "warning",
      "age_sec": 120,
      "next_actions": [...]
    },
    {
      "rank": 2,
      "category": "bead",
      "type": "ready",
      "summary": "Bead br-xyz ready: Implement auth flow",
      "severity": "info",
      "age_sec": 45,
      "next_actions": [...]
    }
  ],
  "total_action_required": 3,
  "_agent_hints": {
    "summary": "1 alert, 2 ready beads need attention"
  }
}
```

---

## 8. Transport Parity

All attention feed commands MUST produce identical JSON schemas across:
- **CLI:** stdout JSON (primary interface)
- **HTTP:** REST API response body
- **SSE:** Server-Sent Events stream (each event = one SSE message)
- **WebSocket:** JSON messages over WebSocket

### 8.1 SSE Format

```
event: ntm_event
data: {"cursor":"evt_...","type":"agent.state_change",...}

event: ntm_event
data: {"cursor":"evt_...","type":"bead.ready",...}

event: ntm_heartbeat
data: {"cursor":"evt_...","ts":"2026-03-21T02:31:00Z"}
```

### 8.2 WebSocket Format

```json
{"type":"event","payload":{"cursor":"evt_...","type":"agent.state_change",...}}
{"type":"event","payload":{"cursor":"evt_...","type":"bead.ready",...}}
{"type":"heartbeat","payload":{"cursor":"evt_...","ts":"2026-03-21T02:31:00Z"}}
```

---

## 9. Profiles and Defaults

### 9.1 Profile Semantics

A **profile** is a named preset of filter/verbosity settings:

```json
{
  "profiles": {
    "default": {
      "description": "Standard monitoring",
      "categories": ["agent", "bead", "alert", "mail", "conflict", "health"],
      "min_actionability": "background",
      "min_severity": "info"
    },
    "quiet": {
      "description": "Action-required events only",
      "categories": ["agent", "alert", "conflict"],
      "min_actionability": "action_required",
      "min_severity": "warning"
    },
    "verbose": {
      "description": "All events including debug",
      "categories": ["*"],
      "min_actionability": "background",
      "min_severity": "debug"
    }
  }
}
```

### 9.2 Profile Selection

```bash
ntm --robot-events --profile=quiet --cursor=evt_...
```

### 9.3 Explicit Filters Override Profile

When explicit filters are provided alongside a profile, explicit filters win:

```bash
# Uses quiet profile but adds bead category
ntm --robot-events --profile=quiet --category=bead --cursor=evt_...
```

Response includes `filters_applied`:

```json
{
  "success": true,
  "filters_applied": {
    "profile": "quiet",
    "overrides": {
      "category": ["agent", "alert", "conflict", "bead"]
    }
  },
  "events": [...]
}
```

### 9.4 No Profile Specified

When no profile is specified, `default` is used:

```json
{
  "filters_applied": {
    "profile": "default",
    "overrides": null
  }
}
```

---

## 10. Transport Liveness and Schema Version

### 10.1 Heartbeat

For long-lived connections (SSE, WebSocket), a heartbeat event is sent every **30 seconds**:

```json
{
  "cursor": "evt_20260321023100000000000",
  "ts": "2026-03-21T02:31:00Z",
  "category": "system",
  "type": "heartbeat",
  "source": "event_stream",
  "actionability": "background",
  "severity": "debug",
  "summary": "Connection alive"
}
```

**Client responsibility:** If no heartbeat received for 60 seconds, consider connection dead and reconnect.

### 10.2 Schema Version

All responses include schema version in the envelope:

```json
{
  "success": true,
  "schema_version": "1.0.0",
  "timestamp": "2026-03-21T02:31:00Z",
  ...
}
```

Schema version follows semver:
- **Major:** Breaking changes to field semantics
- **Minor:** New fields added
- **Patch:** Documentation/bug fixes only

### 10.3 Capability Discovery

`--robot-capabilities` includes attention feed commands and their parameters:

```json
{
  "commands": [
    {
      "name": "events",
      "flag": "--robot-events",
      "category": "attention",
      "description": "Get events since cursor",
      "parameters": [
        {"name": "cursor", "type": "string", "required": true},
        {"name": "limit", "type": "int", "default": 100},
        {"name": "category", "type": "string", "default": "all"},
        {"name": "profile", "type": "string", "default": "default"}
      ]
    },
    ...
  ],
  "attention_feed": {
    "schema_version": "1.0.0",
    "categories": ["agent", "session", "bead", "mail", "alert", "conflict", "health", "system"],
    "actionability_classes": ["background", "interesting", "action_required"],
    "severity_levels": ["debug", "info", "warning", "error", "critical"],
    "profiles": ["default", "quiet", "verbose"],
    "wait_conditions": ["idle", "any_idle", "busy", "all_busy", "any_output", "any_error", "action_required", "bead_ready", "mail_received"],
    "heartbeat_interval_sec": 30,
    "cursor_retention_min_sec": 3600
  }
}
```

---

## 11. Degraded State Handling

### 11.1 Component Health

Snapshot and digest outputs include component health markers:

```json
{
  "success": true,
  "component_health": {
    "activity_detector": {"status": "healthy"},
    "bead_tracker": {"status": "healthy"},
    "mail_checker": {"status": "degraded", "reason": "Agent Mail server unreachable", "since": "2026-03-21T02:00:00Z"},
    "alert_manager": {"status": "healthy"}
  },
  "degraded_components": ["mail_checker"],
  "sessions": [...]
}
```

### 11.2 Degraded Summary Markers

When a component is degraded, affected fields include a marker:

```json
{
  "agent_mail": {
    "_degraded": true,
    "_degraded_reason": "Agent Mail server unreachable",
    "_degraded_since": "2026-03-21T02:00:00Z",
    "unread": null,
    "threads": null
  }
}
```

### 11.3 Capabilities with Degraded State

`--robot-capabilities` reports current degradation:

```json
{
  "attention_feed": {
    "degraded_features": [
      {
        "feature": "mail_events",
        "reason": "Agent Mail server unreachable",
        "impact": "mail.* events will not be generated"
      }
    ]
  }
}
```

---

## 12. Help and Documentation

### 12.1 `--robot-help` Operator Loop

`--robot-help` includes the canonical operator loop:

```
ATTENTION FEED — Operator Loop
==============================

1. BOOTSTRAP:  ntm --robot-snapshot
   → Get full state, receive initial cursor

2. POLL:       ntm --robot-events --cursor=<cursor>
   → Get new events since cursor

3. TRIAGE:     ntm --robot-digest --cursor=<cursor>
   → Get prioritized what-changed summary

4. ACT:        Use suggested commands from next_actions
   → e.g., ntm --robot-send=... or ntm --robot-diagnose=...

5. WAIT:       ntm --robot-wait=SESSION --condition=idle
   → Block until condition met

If CURSOR_EXPIRED:
   → Call --robot-snapshot to resync

Wait conditions: idle, any_idle, busy, all_busy, any_output, any_error,
                 action_required, bead_ready, mail_received
```

### 12.2 Error Examples

**CURSOR_EXPIRED:**
```json
{
  "success": false,
  "error_code": "CURSOR_EXPIRED",
  "error": "Cursor 'evt_20260320120000000000000' has expired",
  "hint": "Call --robot-snapshot to resync and obtain fresh cursor",
  "resync_required": true
}
```

**INVALID_CURSOR:**
```json
{
  "success": false,
  "error_code": "INVALID_CURSOR",
  "error": "Cursor 'foo' is malformed",
  "hint": "Cursor must be in format evt_<timestamp_nanos>"
}
```

**UNKNOWN_CONDITION:**
```json
{
  "success": false,
  "error_code": "INVALID_INPUT",
  "error": "Unknown wait condition 'foo'",
  "hint": "Valid conditions: idle, any_idle, busy, all_busy, any_output, any_error, action_required, bead_ready, mail_received"
}
```

---

## 13. Golden Fixtures for Testing

The following scenarios MUST have golden fixtures in the test suite:

### 13.1 Success Scenarios

1. **Snapshot bootstrap:** Fresh snapshot with cursor, sessions, beads, alerts
2. **Event replay:** Events returned for valid cursor, new cursor returned
3. **Digest generation:** Summarized what-changed with highlights
4. **Wait condition met:** Successful wait with triggering event
5. **Attention list:** Prioritized action-required items

### 13.2 Error Scenarios

1. **CURSOR_EXPIRED:** Cursor too old, resync required
2. **INVALID_CURSOR:** Malformed cursor string
3. **UNKNOWN_CONDITION:** Invalid wait condition name
4. **TIMEOUT:** Wait timed out without condition met

### 13.3 Profile Scenarios

1. **Default profile:** No profile specified, default applied
2. **Explicit profile:** quiet/verbose profile selected
3. **Override profile:** Profile with explicit filter override
4. **Conflicting filters:** Profile + contradictory explicit filters

### 13.4 Degraded Scenarios

1. **Single component degraded:** mail_checker down, other healthy
2. **Multiple components degraded:** Partial observability
3. **Full degradation:** All components unhealthy

### 13.5 Transport Scenarios

1. **Heartbeat:** SSE/WebSocket heartbeat after 30s
2. **Reconnect:** Client reconnects after heartbeat timeout
3. **Schema version:** Version included in all responses

---

## 14. Exit Codes

Following robot API conventions:

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Error (invalid input, command failed) |
| 2 | Unavailable (feature not implemented, dependency missing) |

**Mapping:**
- `CURSOR_EXPIRED` → exit 1 (recoverable error)
- `INVALID_CURSOR` → exit 1
- `INVALID_INPUT` → exit 1
- `TIMEOUT` → exit 1
- `NOT_IMPLEMENTED` → exit 2

---

## 15. Implementation Notes for Future Beads

### 15.1 Event Generation

Events should be generated at the source (activity detector, health monitor, etc.) and published to a central event bus. The event bus:
- Assigns monotonic cursors
- Manages retention
- Handles subscriber delivery

### 15.2 Cursor Storage

Cursors are timestamp-based for simplicity. Consider:
- Nanosecond precision for ordering
- UTC timezone always
- No external sequence generator dependency

### 15.3 Testing Requirements

- **Unit tests:** Envelope validation, cursor comparison, profile merging
- **Integration tests:** Event generation from real components
- **E2E tests:** Full operator loop with real sessions
- **Golden fixtures:** Canonical examples for all scenarios in §13

### 15.4 Logging

Log contract mismatches when commands cannot satisfy guarantees:
- Missing cursor in snapshot (should never happen)
- Event ordering violation (cursor A > B but ts(A) > ts(B))
- Profile not found
- Unknown category/type (forward compatibility)

---

## Appendix A: Example Event Payloads

### A.1 Agent State Change

```json
{
  "cursor": "evt_20260321023045123456789",
  "ts": "2026-03-21T02:30:45.123456789Z",
  "session": "myproject",
  "pane": "0.2",
  "category": "agent",
  "type": "state_change",
  "source": "activity_detector",
  "actionability": "interesting",
  "severity": "info",
  "summary": "Agent cc_2 became idle after 5m42s",
  "details": {
    "agent_type": "claude",
    "previous_state": "busy",
    "current_state": "idle",
    "duration_sec": 342,
    "model": "claude-sonnet-4-6"
  },
  "next_actions": [
    {
      "label": "Check output",
      "command": "ntm --robot-tail=myproject --panes=2 --lines=100"
    }
  ]
}
```

### A.2 Bead Ready

```json
{
  "cursor": "evt_20260321023100987654321",
  "ts": "2026-03-21T02:31:00.987654321Z",
  "category": "bead",
  "type": "ready",
  "source": "bead_tracker",
  "actionability": "interesting",
  "severity": "info",
  "summary": "Bead br-xyz ready: Implement OAuth flow",
  "details": {
    "bead_id": "br-xyz",
    "title": "Implement OAuth flow",
    "priority": 1,
    "type": "task",
    "unblocked_by": ["br-abc", "br-def"]
  },
  "next_actions": [
    {
      "label": "View bead",
      "command": "br show br-xyz"
    },
    {
      "label": "Claim bead",
      "command": "br update br-xyz --status=in_progress"
    }
  ]
}
```

### A.3 Alert Raised

```json
{
  "cursor": "evt_20260321023200123456789",
  "ts": "2026-03-21T02:32:00.123456789Z",
  "session": "myproject",
  "pane": "0.3",
  "category": "alert",
  "type": "raised",
  "source": "health_monitor",
  "actionability": "action_required",
  "severity": "warning",
  "summary": "Agent cc_3: API quota at 85%",
  "details": {
    "alert_id": "alert_quota_001",
    "quota_type": "anthropic_api",
    "current_usage": 85,
    "threshold": 80,
    "reset_at": "2026-03-22T00:00:00Z"
  },
  "next_actions": [
    {
      "label": "Diagnose",
      "command": "ntm --robot-diagnose=myproject --pane=3"
    },
    {
      "label": "Check tokens",
      "command": "ntm --robot-tokens --tokens-session=myproject"
    }
  ]
}
```

### A.4 Heartbeat

```json
{
  "cursor": "evt_20260321023300000000000",
  "ts": "2026-03-21T02:33:00Z",
  "category": "system",
  "type": "heartbeat",
  "source": "event_stream",
  "actionability": "background",
  "severity": "debug",
  "summary": "Connection alive"
}
```

---

## Appendix B: Changelog

- **2026-03-26:** Status updated to IMPLEMENTED; aligned with robot redesign (bd-j9jo3.9.4)
- **2026-03-21:** Initial contract (br-aa0nj)

---

*Reference: br-aa0nj, bd-j9jo3.9.4*
