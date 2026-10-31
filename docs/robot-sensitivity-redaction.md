# Robot Sensitivity and Redaction Semantics

This document defines sensitivity classification, redaction, and disclosure-control semantics for ntm robot mode surfaces.

## Overview

Robot mode surfaces expose richer operational data than interactive output. This creates risk: pane output, mail content, command arguments, and diagnostic evidence may contain secrets, credentials, PII, or internal addresses. This contract ensures robot payloads remain safe by default while preserving agent ergonomics.

## REST Request Integrity

`ntm serve` treats request admission separately from output redaction. When REST
redaction is enabled, `redact` and `block` both reject JSON requests with detected
secrets before handlers or idempotency replay run. Accepted request bytes remain
unchanged. No route receives a credential or instruction replaced by a redaction
placeholder, even when such a replacement would preserve JSON syntax.

This policy applies to every `/api/v1` request body, regardless of `Content-Type`,
because the API handlers decode JSON even when the header is missing or mislabeled.
Declared `application/json` bodies elsewhere are also checked. Empty bodies remain
available to routes with optional input. Nonempty malformed JSON without detected
secrets is rejected with `400 BAD_REQUEST`; an unreadable body is also rejected
without forwarding its partial contents. Bodies above 10 MiB return `413 REQUEST_TOO_LARGE`, including
chunked bodies and bodies whose first 10 MiB happen to form valid JSON.

| Configuration | Detected secret in a JSON request |
|---------------|----------------------------------|
| Enabled `redact` | Reject with `422 SECRETS_DETECTED` |
| Enabled `block` | Reject with `422 SECRETS_DETECTED` |
| `warn`, `off`, or disabled REST redaction | No secret-based rejection or substitution by this middleware |
| Explicitly allowlisted match or disabled category | Original bytes pass if no other findings require rejection |

The CLI enables REST redaction for `redact` and `block`; its default `warn` mode
does not enable this middleware. Existing allowlists and category settings still
govern detection. Allowlisting is an explicit operator policy choice and also
affects output handling; removing a real credential from a request is preferable
to broadening the allowlist merely to get a request accepted.

Secret rejections use the normal API error envelope. `details.findings_count`
reports the total, and `details.findings` contains up to 100 objects with only
`category`, `start`, and `end`. Locations are zero-based byte offsets in the
original body, with an exclusive `end`. `findings_truncated: true` indicates that
more locations were omitted. The error and hint explain the refusal. Diagnostics
never include raw matches, credential hashes, arbitrary field names, body excerpts,
or decoder error text. Logs retain the request summary and category counts.

JSON admission examines the strings that a handler can decode, including
property names and JSON documents embedded inside strings. Another layer of
escaping cannot hide a detected credential from this inspection. Configured
extra patterns are checked against both the raw input and decoded strings on
JSON API surfaces, including anchored patterns on complete string values.
Existing raw, intermediate, and logical match allowlists remain effective, and
disabled categories keep their existing field semantics. This uses the same
scanner and maps findings back through the original escape sequences; it never
decodes the whole request into a map or reserializes accepted input. Duplicate
properties, exact number spellings, and whitespace remain intact. Generic text
and log callers retain their existing scanning contract.

### Why requests are rejected

The handlers require exact input for several different reasons:

| Handler surface | Integrity requirement |
|-----------------|-----------------------|
| `PUT /api/v1/policy/` | Saves the submitted policy text to disk |
| `POST /api/v1/approvals/request` | Persists actions, resources, and reasons; also logs the action |
| Agent `/send` and pane `/input` | Dispatch the submitted instruction or text |
| Jobs and pipelines | Execute work and persist inputs or decisions |
| Pipeline `/recover` | Records an operator's exact acceptance reason |
| Safety and validation routes | Must evaluate the caller's actual input |

Substituting placeholders on selected routes would change what is stored,
executed, or validated. Passing original detected secrets through while redacting
only responses would also permit them into existing handler logs and persistence.
Uniform rejection avoids both problems and applies to future API handlers too.
A refused request leaves the idempotency key available for a corrected submission.

Idempotency replay also verifies the entire request body within the same 10 MiB
limit. Reusing a key with different bytes anywhere in the accepted body returns
`422 IDEMPOTENT_REPLAY`; an equal prefix is insufficient. Unreadable, oversized,
or canceled bodies cannot dispatch a handler or retrieve a cached success. A
successful read restores the exact submitted bytes, and refused reads do not
reserve the key or replace an existing cached result. These replay-integrity
checks also apply when redaction is disabled.

Response redaction remains separate: JSON output in both `redact` and `block`
modes keeps its structure and safe neighboring fields. A response is processed
after its handler runs, so output filtering preserves the handler's HTTP status.
If a custom pattern crosses a JSON boundary that cannot be safely rewritten, the
server withholds the response body and returns a valid JSON object with
`response_unavailable: true` and `error_code: "RESPONSE_REDACTION_FAILED"`.
Its hint explains that the handler has already run and may have completed the
operation; inspect the operation's state before retrying. This is not a request
rejection and does not imply a rollback. The original body and pattern details
are never included in that diagnostic. Responses without findings retain their
exact bytes, including large integers, whitespace, and duplicate object keys.
Flushing a JSON response does not bypass protection: buffering continues until
the complete document can be checked, even when a credential spans writes.

The existing pipeline recovery response
exception preserves the exact 64-character lowercase hexadecimal `receipt_token`
only on `/api/v1/pipelines/<id>/recover`. Clients submit it under the request's
`receipt` field; there is no new request-redaction exception. A sensitive recovery
reason is rejected before the journal changes, and a corrected reason can still
use the same receipt.

### Streaming output

Enabled `redact` and `block` policies also protect server event payloads in
WebSocket live delivery, persisted replay, attention subscriptions, and both SSE
routes (`/events` and `/api/v1/attention/stream`). Each complete JSON payload is
serialized and checked before disclosure. This retains credential field-name
context and covers typed maps, structs, arrays, and raw JSON without rounding
large numeric cursors through floating-point values. The shared source event is
not rewritten. WebSocket persistence and live delivery use the same serialized
payload; historical replay is checked against the current policy even if the
original record was stored while redaction was disabled. Complete existing
redaction markers remain stable when replayed.

Routing metadata is refused if it contains detected secrets rather than being
replaced with a different topic or event type. Unsafe WebSocket events trigger
a reset or a failed attention subscription requiring resynchronization. Delivery
cannot silently continue past an event withheld by redaction. SSE checks each
complete frame, including replay and heartbeat diagnostics, using the current
configuration. It closes the stream on an unsafe frame or a frame above 10 MiB
without emitting a partial or truncated event. Diagnostic logs contain static
failure reasons and finding counts, never source payloads or marshaler errors.

### Checkpoint archives

Checkpoint import and export require an additional check because JSON scanning
cannot see credentials inside base64 or compressed archive entries. With enabled
`redact` or `block`, imports inspect the decoded archive before checkpoint creation
or overwrite. Exports inspect the completed archive before either a binary
download or an inline base64 response. Accepted exports use the exact inspected
snapshot. An unsafe inline JSON envelope is refused too, so response redaction
cannot silently replace part of the base64 archive.

Recovery commands and Git patches must retain their intended values. Detected
secrets therefore cause `422 CHECKPOINT_SENSITIVE_CONTENT`, with a static message
and no credential contents or artifact names. The server does not rewrite those
values. Rejected imports leave existing checkpoints untouched, and rejected
exports leave source recovery data intact. The existing `redact_secrets` export
option still explicitly sanitizes scrollback; the resulting archive must pass
inspection of its remaining contents as well.

Inspection covers selected entries, archive names and metadata, and decoded gzip
scrollback, including headers and payloads across concatenated gzip members.
Checksums and trailers must complete successfully. Limits are 64 MiB for the
encoded snapshot, 100 MiB per expanded entry, 1 GiB for aggregate expanded data,
and 4,096 entries or gzip members. Tar expansion also includes internal extension
headers, and post-archive zero padding is limited to 1 MiB. Exceeding a limit
returns `422 CHECKPOINT_INSPECTION_LIMIT`. An unreadable or malformed import
returns `400 BAD_REQUEST`; an export that cannot be inspected returns
`500 INTERNAL_ERROR`. The ordinary 10 MiB HTTP request limit still applies to
the complete import JSON body, including its base64 field.

Allowlists and enabled detection categories continue to govern these checks.
`warn`, `off`, and disabled REST redaction retain their existing behavior.

An import error after publication is different from an admission refusal. If the
complete checkpoint was observed at its destination but finalization failed, the
API returns `500 CHECKPOINT_IMPORT_FINALIZATION_FAILED` with
`publication_state: "published"`. A publication attempt whose outcome cannot be
confirmed returns `500 CHECKPOINT_IMPORT_PUBLICATION_UNCERTAIN` with
`publication_state: "unknown"`. Both report `inspection_required: true`,
`finalization_complete: false`, and `retry_safe: false`. Neither establishes that
the previous checkpoint is unchanged. A published checkpoint reference is exact
or explicitly withheld if it contains sensitive content; paths, commands, and
underlying error text are never included in these diagnostics.

These terminal error receipts are retained for same-key idempotency replay, so a
retry does not repeat an import while its receipt remains cached. A different
body with that key still conflicts. Ordinary pre-publication validation errors
remain retryable. Retention uses the existing bounded in-memory cache, so expiry,
eviction, and server restart do not provide a durable retry guarantee. Inspect
the destination checkpoint and retained recovery evidence before importing again.

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
