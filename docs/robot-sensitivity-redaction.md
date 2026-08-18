# Robot Sensitivity and Redaction Semantics

This document defines sensitivity classification, redaction, and disclosure-control semantics for ntm robot mode surfaces.

## Overview

Robot mode surfaces expose richer operational data than interactive output. This creates risk: pane output, mail content, command arguments, and diagnostic evidence may contain secrets, credentials, PII, or internal addresses. This contract ensures robot payloads remain safe by default while preserving agent ergonomics.

## Disclosure States

Every disclosable field has a disclosure state:

```
┌─────────────────────────────────────────────────────────────────────┐
│                       DISCLOSURE STATES                              │
├─────────────────────────────────────────────────────────────────────┤
│  visible       │ Content shown verbatim                             │
│  preview_only  │ First N chars shown, rest truncated               │
│  redacted      │ Content replaced with [REDACTED] marker           │
│  withheld      │ Field omitted entirely (not even placeholder)     │
│  hashed        │ Content replaced with deterministic hash          │
└─────────────────────────────────────────────────────────────────────┘
```

### State Properties

```json
{
  "field": "pane_output",
  "disclosure": {
    "state": "redacted",
    "state_code": "DISCLOSURE_REDACTED",
    "reason": "secret_detected",
    "reason_code": "REASON_SECRET_PATTERN",
    "pattern_matched": "ANTHROPIC_API_KEY=*",
    "original_length": 256,
    "preview_available": false,
    "hash": null,
    "policy_version": "v2.1",
    "policy_source": "builtin"
  }
}
```

### State Transitions

| Condition | Default State | Explanation |
|-----------|--------------|-------------|
| No sensitive content detected | `visible` | Content safe to show |
| Long content, no secrets | `preview_only` | Truncated for payload budget |
| Secret pattern matched | `redacted` | Content contains credentials |
| PII pattern matched | `redacted` | Content contains personal data |
| Internal address detected | `redacted` | Content contains internal URLs |
| Explicitly withheld field | `withheld` | Field never disclosed |
| Correlation required | `hashed` | Content hidden but joinable |

## Sensitivity Classification

### Content Categories

| Category | Code | Detection Method |
|----------|------|------------------|
| Credentials | `SENSITIVE_CREDENTIAL` | Pattern matching (API keys, tokens, passwords) |
| Environment secrets | `SENSITIVE_ENV` | `*_KEY`, `*_SECRET`, `*_TOKEN` patterns |
| PII | `SENSITIVE_PII` | Email, phone, SSN patterns |
| Internal addresses | `SENSITIVE_INTERNAL` | Internal domain patterns |
| File paths | `SENSITIVE_PATH` | Home directory, credential file paths |
| Command secrets | `SENSITIVE_CMD` | Password arguments, auth tokens |
| Mail content | `SENSITIVE_MAIL` | Body text from agent mail |
| Diagnostic dumps | `SENSITIVE_DIAG` | Stack traces, memory dumps |

### Detection Patterns

```json
{
  "patterns": {
    "credentials": [
      {"name": "api_key", "regex": "[A-Za-z0-9_-]{20,}[=]", "context": "after ="},
      {"name": "bearer", "regex": "Bearer\\s+[A-Za-z0-9._-]+", "context": "header"},
      {"name": "basic_auth", "regex": "Basic\\s+[A-Za-z0-9+/=]+", "context": "header"}
    ],
    "env_secrets": [
      {"name": "env_key", "regex": "[A-Z_]+_KEY=[^\\s]+"},
      {"name": "env_secret", "regex": "[A-Z_]+_SECRET=[^\\s]+"},
      {"name": "env_token", "regex": "[A-Z_]+_TOKEN=[^\\s]+"}
    ],
    "pii": [
      {"name": "email", "regex": "[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}"},
      {"name": "phone", "regex": "\\+?[0-9]{10,15}"}
    ],
    "internal": [
      {"name": "internal_url", "regex": "https?://[^/]*\\.internal\\.[^\\s]+"},
      {"name": "localhost", "regex": "https?://localhost[:/][^\\s]+"}
    ]
  }
}
```

### Classification Result

```json
{
  "classification": {
    "category": "SENSITIVE_CREDENTIAL",
    "confidence": "high",
    "patterns_matched": ["api_key", "env_secret"],
    "locations": [
      {"start": 45, "end": 89, "pattern": "api_key"},
      {"start": 120, "end": 156, "pattern": "env_secret"}
    ],
    "recommendation": "redact"
  }
}
```

## Redaction Markers

### Marker Format

Redacted content is replaced with structured markers:

```json
{
  "content": "[REDACTED:SENSITIVE_CREDENTIAL:len=44]",
  "original_field": "api_key",
  "disclosure": {
    "state": "redacted",
    "original_length": 44,
    "category": "SENSITIVE_CREDENTIAL",
    "reason_code": "REASON_SECRET_PATTERN"
  }
}
```

### Marker Types

| Marker | Meaning |
|--------|---------|
| `[REDACTED]` | Generic redaction |
| `[REDACTED:CREDENTIAL]` | Credential/secret redacted |
| `[REDACTED:PII]` | Personal information redacted |
| `[REDACTED:INTERNAL]` | Internal address redacted |
| `[REDACTED:len=N]` | Redacted with original length |
| `[WITHHELD]` | Field intentionally omitted |
| `[TRUNCATED:N/M]` | Showing N of M total characters |

### Redaction Evidence

Every redaction includes evidence for debugging:

```json
{
  "redaction_evidence": {
    "field": "pane_output",
    "action": "redacted",
    "category": "SENSITIVE_CREDENTIAL",
    "pattern_matched": "ANTHROPIC_API_KEY=*",
    "match_location": {"line": 5, "col": 12, "len": 56},
    "policy_version": "v2.1",
    "policy_source": "builtin",
    "timestamp": "2026-03-22T04:00:00Z"
  }
}
```

## Safe Previews

Some content can be partially shown:

### Preview Rules

| Category | Preview Allowed | Preview Length |
|----------|-----------------|----------------|
| Credentials | No | 0 |
| PII | No | 0 |
| Mail subject | Yes | 80 chars |
| Mail body | Yes | 200 chars (with warning) |
| Pane output | Yes | 500 chars (redacting secrets) |
| Command args | Partial | Redact values only |
| File paths | Yes | Path without home expansion |

### Preview Structure

```json
{
  "field": "mail_body",
  "disclosure": {
    "state": "preview_only",
    "preview": "Hey team, the deployment failed because...",
    "preview_length": 200,
    "original_length": 1500,
    "continuation_available": false,
    "sensitive_sections_redacted": 2
  }
}
```

### Safe Preview Generation

```go
type SafePreview struct {
    Content             string `json:"content"`
    OriginalLength      int    `json:"original_length"`
    TruncatedAt         int    `json:"truncated_at"`
    SensitiveSections   int    `json:"sensitive_sections"`
    RedactedInPreview   bool   `json:"redacted_in_preview"`
}
```

## Policy Provenance

Disclosure decisions are traceable:

### Policy Metadata

```json
{
  "policy": {
    "version": "v2.1",
    "source": "builtin",
    "updated_at": "2026-03-01T00:00:00Z",
    "rules_count": 45,
    "custom_rules_count": 0,
    "overrides": []
  }
}
```

### Policy Sources

| Source | Priority | Description |
|--------|----------|-------------|
| `builtin` | 0 | Default patterns shipped with ntm |
| `project` | 1 | Project-level `.ntm/sensitivity.yaml` |
| `session` | 2 | Session-level overrides |
| `explicit` | 3 | Per-request explicit allowlist |

### Policy Override

```json
{
  "override": {
    "field": "custom_header",
    "action": "allow",
    "reason": "operator_approved",
    "approved_by": "PeachPond",
    "approved_at": "2026-03-22T04:00:00Z",
    "expires_at": "2026-03-22T05:00:00Z",
    "scope": "session"
  }
}
```

## Field Classification

### Allowlisted Fields

Some fields are structurally safe:

```json
{
  "allowlisted_fields": [
    "session_name",
    "pane_id",
    "agent_name",
    "bead_id",
    "timestamp",
    "event_type",
    "severity",
    "reason_code",
    "schema_id"
  ]
}
```

### Blob Fields

Text blobs require scanning:

```json
{
  "blob_fields": [
    "pane_output",
    "mail_body",
    "command_args",
    "error_message",
    "stack_trace",
    "diagnostic_dump",
    "handoff_notes",
    "incident_description"
  ]
}
```

### Field Disclosure Matrix

| Field | Default State | Scan Required | Preview |
|-------|---------------|---------------|---------|
| `session_name` | visible | No | N/A |
| `pane_output` | scan | Yes | 500 chars |
| `mail_subject` | preview_only | Yes | 80 chars |
| `mail_body` | preview_only | Yes | 200 chars |
| `command_args` | scan | Yes | Partial |
| `error_message` | scan | Yes | 300 chars |
| `stack_trace` | redacted | No | No |
| `env_dump` | withheld | No | No |

## Hashed Disclosure

For correlation without exposure:

### Hash Properties

```json
{
  "field": "internal_url",
  "disclosure": {
    "state": "hashed",
    "hash": "sha256:abc123...",
    "hash_algorithm": "sha256",
    "salt": "session-specific",
    "original_length": 45,
    "correlation_id": "hash:abc123"
  }
}
```

### Hash Uses

| Use Case | Rationale |
|----------|-----------|
| Incident correlation | Same hash = same underlying entity |
| Deduplication | Detect repeated secrets without storing |
| Audit trail | Reference without disclosure |

## Explainability Integration

### Disclosure Explanation

```json
{
  "explanation": {
    "type": "disclosure",
    "short": "API key redacted",
    "code": "EXPLAIN_REDACTION",
    "detail": "Field pane_output contained pattern matching ANTHROPIC_API_KEY",
    "evidence": {
      "category": "SENSITIVE_CREDENTIAL",
      "pattern": "api_key",
      "policy_version": "v2.1"
    },
    "drill_down": {
      "type": "policy_detail",
      "ref": "policy:builtin:v2.1:credential:api_key"
    }
  }
}
```

### Explanation Codes

| Code | Meaning |
|------|---------|
| `EXPLAIN_REDACTION` | Content redacted due to sensitivity |
| `EXPLAIN_PREVIEW` | Content truncated to safe preview |
| `EXPLAIN_WITHHELD` | Field entirely omitted |
| `EXPLAIN_HASHED` | Content replaced with hash |
| `EXPLAIN_OVERRIDE` | Policy override applied |
| `EXPLAIN_ALLOWLIST` | Field on structural allowlist |

## Replay and History

### Historical Redaction

Replayed events apply current policy:

```json
{
  "replay_disclosure": {
    "original_state": "visible",
    "current_state": "redacted",
    "reason": "policy_updated",
    "policy_at_event": "v1.5",
    "policy_current": "v2.1",
    "retroactive_redaction": true
  }
}
```

### Replay Safety

| Scenario | Behavior |
|----------|----------|
| Event was visible, now sensitive | Redact in replay |
| Event was redacted, now safe | Keep redacted (conservative) |
| Policy version mismatch | Apply stricter policy |
| Override expired | Reapply base policy |

## Transport Consistency

All transports apply same disclosure rules:

### CLI

```bash
# Robot output includes disclosure metadata alongside normalized fields
ntm --robot-mail-check 2>/dev/null | jq '.messages[0].subject_disclosure'
{
  "disclosure_state": "preview_only",
  "preview": "Deploy plan for ..."
}
```

### REST

```json
{
  "pane": {
    "output": "[TRUNCATED:500/2500]...",
    "disclosure": {
      "state": "preview_only",
      "original_length": 2500
    }
  }
}
```

### SSE/WebSocket

```json
{
  "event_type": "state:pane:output",
  "payload": {
    "output": "[REDACTED:CREDENTIAL]",
    "disclosure": {
      "state": "redacted",
      "category": "SENSITIVE_CREDENTIAL"
    }
  }
}
```

## Surface Integration

### Snapshot

```json
{
  "snapshot": {
    "panes": [{
      "output": "[TRUNCATED:500/2500]",
      "output_disclosure": {"state": "preview_only"}
    }]
  }
}
```

### Attention

```json
{
  "attention_item": {
    "description": "Agent leaked credentials in output",
    "evidence": {
      "output_preview": "[REDACTED:CREDENTIAL:len=44]",
      "disclosure": {"state": "redacted"}
    }
  }
}
```

### Inspect

```json
{
  "inspect": {
    "pane": {
      "recent_output": [...],
      "sensitive_output_count": 3,
      "disclosure_summary": {
        "redacted": 2,
        "preview_only": 1,
        "visible": 50
      }
    }
  }
}
```

## Error Handling

### Redaction Failures

```json
{
  "error": {
    "code": "REDACTION_FAILED",
    "message": "Could not classify content safely",
    "field": "pane_output",
    "fallback": "withheld",
    "reason": "classification_timeout"
  }
}
```

### Error Fallbacks

| Error | Fallback State |
|-------|---------------|
| Classification timeout | `withheld` |
| Pattern engine error | `withheld` |
| Policy not found | `redacted` (conservative) |
| Field type unknown | `withheld` |

## Configuration

### Sensitivity Config

```yaml
# .ntm/sensitivity.yaml
version: "1"
patterns:
  - name: custom_token
    regex: "MYAPP_TOKEN=[A-Za-z0-9]+"
    category: SENSITIVE_CREDENTIAL

overrides:
  - field: custom_debug
    action: allow
    reason: "debug field is safe"

previews:
  pane_output: 1000
  mail_body: 500
```

### Runtime Options

```json
{
  "request_options": {
    "disclosure_level": "strict",
    "allow_previews": true,
    "include_hashes": false,
    "explain_redactions": true
  }
}
```

## Design Rationale

1. **Safe by default**: Unknown content is redacted, not shown
2. **Traceable decisions**: Every redaction includes evidence
3. **Preserved utility**: Safe previews and hashes enable debugging
4. **Consistent behavior**: Same rules across all transports
5. **Policy versioning**: Disclosure decisions are reproducible
6. **Conservative replay**: Historical content re-evaluated with current policy
