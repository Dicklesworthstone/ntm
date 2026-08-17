# Robot Auditability and Decision Trail Semantics

This document defines auditability, actor attribution, and operator-decision trail semantics for ntm robot mode surfaces.

## Overview

Operators and agents need to answer trust questions: "Who acknowledged this?", "What request caused this change?", "Why was this evidence hidden?" This contract defines how ntm records and exposes a trustworthy trail of decisions without becoming a compliance bureaucracy.

## Design Principles

1. **Operational trust**: Answer "who did what when" for recent decisions
2. **Actor attribution**: Distinguish human, agent, and system actions
3. **Correlation**: Link audit records to requests and affected entities
4. **Sensitivity-aware**: Audit trail respects redaction policies
5. **Bounded retention**: Keep recent decisions, summarize older ones

## Audit Event Structure

### Event Envelope

```json
{
  "audit_id": "aud_20260322040000_x7k",
  "audit_type": "attention_state_change",
  "audit_category": "operator_action",
  "timestamp": "2026-03-22T04:00:00.123Z",
  "actor": {
    "type": "agent",
    "id": "PeachPond",
    "session": "main",
    "pane": "pane-abc123"
  },
  "request": {
    "id": "req_20260322040000_abc",
    "correlation_id": "corr_20260322035900_xyz",
    "idempotency_key": "idem_ack_alert123_v1"
  },
  "target": {
    "type": "attention_item",
    "ref": "alert:agent_stuck:pane-abc123",
    "fingerprint": "fp-20260322040000"
  },
  "action": {
    "type": "acknowledge",
    "previous_state": "new",
    "new_state": "acknowledged"
  },
  "reason": "Agent confirmed awareness of stuck state",
  "evidence": {...},
  "sensitivity": {
    "redactions_applied": 0,
    "policy_version": "v2.1"
  }
}
```

## Actor Attribution

### Actor Types

| Type | Code | Description |
|------|------|-------------|
| Human | `ACTOR_HUMAN` | Direct human operator action |
| Agent | `ACTOR_AGENT` | AI coding agent action |
| System | `ACTOR_SYSTEM` | Automated system action |
| Unknown | `ACTOR_UNKNOWN` | Origin could not be determined |

### Actor Properties

```json
{
  "actor": {
    "type": "agent",
    "type_code": "ACTOR_AGENT",
    "id": "PeachPond",
    "display_name": "PeachPond (claude-code, opus-4.6)",
    "session": "main",
    "pane": "pane-abc123",
    "origin": {
      "transport": "cli",
      "source_ip": null,
      "user_agent": "ntm/1.0"
    },
    "authenticated": true,
    "permissions": ["attention:write", "actuation:request"]
  }
}
```

### Attribution Rules

| Signal | Actor Type | Confidence |
|--------|-----------|------------|
| CLI from tmux pane with known agent | `agent` | High |
| CLI from tmux pane without agent | `human` | High |
| REST with agent token | `agent` | High |
| REST with human token | `human` | High |
| System timer/cron | `system` | High |
| Automatic promotion | `system` | High |
| Unknown origin | `unknown` | Low |

## Audit-Worthy Events

### Event Categories

```
┌──────────────────────────────────────────────────────────────┐
│                  ATTENTION STATE CHANGES                      │
├──────────────────────────────────────────────────────────────┤
│  acknowledge    │ Item acknowledged                          │
│  snooze         │ Item snoozed                               │
│  dismiss        │ Item dismissed                             │
│  pin            │ Item pinned                                │
│  unpin          │ Item unpinned                              │
│  escalate       │ Item manually escalated                    │
│  mute           │ Mute rule created                          │
│  unmute         │ Mute rule removed                          │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│                   INCIDENT LIFECYCLE                          │
├──────────────────────────────────────────────────────────────┤
│  incident_opened │ Incident created (auto or manual)         │
│  incident_assigned │ Incident assigned to actor              │
│  incident_updated │ Incident description/severity changed    │
│  incident_resolved │ Incident resolved                       │
│  incident_reopened │ Incident reopened                       │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│                    ACTUATION REQUESTS                         │
├──────────────────────────────────────────────────────────────┤
│  actuation_requested │ Remediation action requested          │
│  actuation_approved  │ Action approved (if required)         │
│  actuation_executed  │ Action executed                       │
│  actuation_failed    │ Action failed                         │
│  actuation_rolled_back │ Action rolled back                  │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│                  DISCLOSURE DECISIONS                         │
├──────────────────────────────────────────────────────────────┤
│  override_created    │ Sensitivity override granted          │
│  override_expired    │ Override expired                      │
│  override_revoked    │ Override manually revoked             │
│  export_requested    │ Post-mortem export requested          │
│  export_completed    │ Export generated                      │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│                    SESSION EVENTS                             │
├──────────────────────────────────────────────────────────────┤
│  agent_registered    │ Agent registered in project           │
│  agent_deregistered  │ Agent removed                         │
│  reservation_created │ File reservation acquired             │
│  reservation_released│ File reservation released             │
│  conflict_detected   │ File conflict detected                │
│  conflict_resolved   │ File conflict resolved                │
└──────────────────────────────────────────────────────────────┘
```

### Event Priority

| Priority | Categories | Retention |
|----------|-----------|-----------|
| High | Incident lifecycle, actuation, disclosure | 30 days |
| Medium | Attention state changes | 7 days |
| Low | Session events, reservations | 24 hours |

## Request/Outcome Correlation

### Correlation Fields

```json
{
  "correlation": {
    "request_id": "req_20260322040000_abc",
    "correlation_id": "corr_20260322035900_xyz",
    "idempotency_key": "idem_ack_alert123_v1",
    "parent_audit_id": null,
    "related_audit_ids": ["aud_20260322035959_def"],
    "workflow_id": null
  }
}
```

### Correlation Rules

| Scenario | Correlation Pattern |
|----------|---------------------|
| Single request | `request_id` links audit to request |
| Multi-step workflow | `correlation_id` groups related audits |
| Retry | `idempotency_key` identifies duplicate requests |
| Cascading changes | `parent_audit_id` links caused audits |
| Related incidents | `related_audit_ids` cross-references |

## Retention Policy

### Durable Retention

High-priority audit events are durably retained:

```json
{
  "retention": {
    "policy": "durable",
    "retained_until": "2026-04-21T04:00:00Z",
    "retention_days": 30,
    "summarizable_after_days": 7,
    "archivable": true
  }
}
```

### Summarized Retention

Older events are summarized:

```json
{
  "summary": {
    "period": "2026-03-15",
    "period_type": "day",
    "event_counts": {
      "acknowledge": 45,
      "snooze": 12,
      "dismiss": 8,
      "incident_opened": 3,
      "incident_resolved": 2
    },
    "actor_counts": {
      "agent": 52,
      "human": 13,
      "system": 3
    },
    "details_available": false,
    "raw_events_expired": true
  }
}
```

### Retention Tiers

| Tier | Duration | Detail Level |
|------|----------|--------------|
| Live | 0-24h | Full detail |
| Recent | 1-7d | Full detail |
| Summarized | 7-30d | Daily summaries |
| Archived | 30d+ | Monthly summaries (if enabled) |

## Sensitivity Interaction

### Audit Trail Redaction

Audit events respect sensitivity policy:

```json
{
  "audit_event": {
    "target": {
      "ref": "alert:credential_leak:pane-abc123"
    },
    "action": {
      "type": "dismiss",
      "reason": "[REDACTED:SENSITIVE_CREDENTIAL]"
    },
    "sensitivity": {
      "redactions_applied": 1,
      "fields_redacted": ["action.reason"],
      "policy_version": "v2.1",
      "original_available": false
    }
  }
}
```

### Redaction Rules

| Field | Redaction Policy |
|-------|------------------|
| `action.reason` | Scan for secrets |
| `evidence.*` | Apply full sensitivity scan |
| `target.ref` | Never redact (structural) |
| `actor.id` | Never redact (attribution) |
| `timestamp` | Never redact |

### Audit vs Evidence

| Aspect | Audit Record | Evidence |
|--------|-------------|----------|
| Purpose | Who/what/when | Supporting detail |
| Redaction | Minimal | Full policy |
| Retention | Per audit tier | Per evidence tier |
| Joinability | Always | May be redacted away |

## Diagnostics Integration

### Recent Audit Queries

```json
{
  "action": "audit_query",
  "request_id": "req_20260322040000_x7k",
  "query": {
    "target_ref": "alert:agent_stuck:pane-abc123",
    "time_range": {
      "last_hours": 24
    },
    "event_types": ["acknowledge", "snooze", "dismiss"],
    "limit": 50
  }
}
```

### Query Response

```json
{
  "audit_trail": {
    "target_ref": "alert:agent_stuck:pane-abc123",
    "query_time_range": {
      "start": "2026-03-21T04:00:00Z",
      "end": "2026-03-22T04:00:00Z"
    },
    "events": [
      {
        "audit_id": "aud_20260322040000_x7k",
        "timestamp": "2026-03-22T04:00:00Z",
        "actor": {"type": "agent", "id": "PeachPond"},
        "action": {"type": "acknowledge"}
      }
    ],
    "total_in_range": 1,
    "truncated": false
  }
}
```

### Inspect Integration

Audit trail appears in inspect surfaces:

```json
{
  "inspect": {
    "attention_item": {
      "ref": "alert:agent_stuck:pane-abc123",
      "current_state": "acknowledged",
      "audit_summary": {
        "last_change": {
          "timestamp": "2026-03-22T04:00:00Z",
          "actor": "PeachPond",
          "action": "acknowledge"
        },
        "changes_24h": 3,
        "actors_24h": ["PeachPond", "system"]
      },
      "drill_down": {
        "type": "audit_trail",
        "ref": "audit:alert:agent_stuck:pane-abc123"
      }
    }
  }
}
```

## Transport Parity

All transports expose audit trail consistently:

### CLI

<!-- ntm-docs: skip -->
```bash
# Query audit trail for target
ntm audit --robot --target alert:agent_stuck:pane-abc123 --last 24h

# Query by actor
ntm audit --robot --actor PeachPond --last 24h

# Query by event type
ntm audit --robot --type incident_opened --last 7d
```

### REST

```http
GET /api/robot/audit?target=alert:agent_stuck:pane-abc123&last=24h HTTP/1.1

GET /api/robot/audit?actor=PeachPond&type=acknowledge HTTP/1.1
```

### SSE/WebSocket

Audit events stream with attention events:

```json
{
  "event_class": "audit",
  "event_type": "audit:attention_state_change",
  "payload": {
    "audit_id": "aud_20260322040000_x7k",
    "actor": {"type": "agent", "id": "PeachPond"},
    "action": {"type": "acknowledge"}
  }
}
```

## Explainability Integration

### Audit Explanation

```json
{
  "explanation": {
    "type": "audit_trail",
    "short": "Acknowledged by PeachPond",
    "code": "EXPLAIN_AUDIT_ACK",
    "evidence": {
      "actor": "PeachPond",
      "actor_type": "agent",
      "action": "acknowledge",
      "timestamp": "2026-03-22T04:00:00Z"
    },
    "drill_down": {
      "type": "audit_detail",
      "ref": "audit:aud_20260322040000_x7k"
    }
  }
}
```

### Explanation Codes

| Code | Meaning |
|------|---------|
| `EXPLAIN_AUDIT_ACK` | Item acknowledged, showing who/when |
| `EXPLAIN_AUDIT_SNOOZE` | Item snoozed, showing parameters |
| `EXPLAIN_AUDIT_DISMISS` | Item dismissed, showing reason |
| `EXPLAIN_AUDIT_ESCALATE` | Item escalated, showing actor |
| `EXPLAIN_AUDIT_INCIDENT` | Incident action, showing lifecycle |
| `EXPLAIN_AUDIT_OVERRIDE` | Disclosure override, showing scope |

## Error Handling

### Audit Query Errors

| Code | Meaning | Recovery |
|------|---------|----------|
| `TARGET_NOT_FOUND` | Target reference invalid | Check reference format |
| `RANGE_EXPIRED` | Requested time beyond retention | Narrow range |
| `ACTOR_NOT_FOUND` | Actor identifier unknown | Check actor ID |
| `AUDIT_UNAVAILABLE` | Audit system degraded | Retry later |

### Attribution Failures

```json
{
  "actor": {
    "type": "unknown",
    "type_code": "ACTOR_UNKNOWN",
    "attribution_failed": true,
    "failure_reason": "no_session_context",
    "fallback_used": true,
    "fallback_actor": "system"
  }
}
```

## Design Rationale

1. **Trust, not bureaucracy**: Focus on operational questions, not compliance theater
2. **Actor clarity**: Always distinguish human from agent from system
3. **Correlation**: Link audits to requests for debugging
4. **Bounded retention**: Keep useful detail, summarize old events
5. **Sensitivity-aware**: Audit trail respects redaction without losing attribution
6. **Transport neutral**: Same audit semantics everywhere
