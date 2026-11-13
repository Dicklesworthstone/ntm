# Robot Request Identity, Idempotency, Retry, and Correlation Semantics Contract

> **Authoritative reference** for request identification and safe retry across robot surfaces.
> All actuation commands MUST follow this contract.

**Status:** SHIPPED
**Bead:** bd-j9jo3.1.10
**Created:** 2026-03-22

> **Shipping status:** shipped across CLI and REST. Durable idempotent
> actuation is active on `--robot-send ... --op-id=<ID>` and
> `--robot-interrupt ... --op-id=<ID>` (and their REST endpoints,
> `POST /api/v1/sessions/{id}/agents/send|interrupt`, via the
> `Idempotency-Key` header), `--robot-send-receipt=<ID>` retrieves send/interrupt
> receipts, `--robot-action-status=<ID>`, `--robot-wait-request=<ID>`, and
> `--robot-cancel-request=<ID>` (plus `GET /api/v1/requests/{id}`,
> `POST /api/v1/requests/{id}/wait`, and `POST /api/v1/requests/{id}/cancel`)
> provide generic request lifecycle inspection, wait, and cancellation, and
> `ntm --robot-causality --causality-chain=<ID>` supports correlation queries.

---

## 1. Purpose

This document defines how robot requests are identified, correlated, retried, and deduplicated. The goal is to make operator retries trustworthy: agents can safely repeat work after timeouts without creating duplicate side effects or misattributing results.

**Design Goals:**
1. Every actuation has a stable request identity
2. Retries are explicitly safe or unsafe
3. Outcomes are traceable to originating requests
4. Duplicate detection is mechanical, not heuristic
5. Transport failures don't cause ambiguous state

**Non-Goals:**
1. Distributed transaction semantics
2. Exactly-once delivery guarantees
3. Complex saga coordination
4. Cross-instance correlation

---

## 2. Request Identity

### 2.1 Request ID Structure

Every request carries a unique identifier:

```
req_<timestamp>_<random>

Example: req_20260322035400_a1b2c3d4
```

**Format:**
- Prefix: `req_`
- Timestamp: YYYYMMDDHHMMSS (UTC)
- Random: 8 hex characters

### 2.2 Request ID Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `request_id` | string | Yes | Unique request identifier |
| `idempotency_key` | string | No | Client-provided dedup key |
| `correlation_id` | string | No | Links related requests |
| `parent_request_id` | string | No | For request chains |
| `client_id` | string | No | Identifies requesting client |

### 2.3 Request ID Generation

**CLI (shipped shape — client-chosen durable operation ID):**
```bash
# Plain send (no durable identity recorded)
ntm --robot-send=myproject --msg="Start work"

# Client-provided operation ID (for retry tracking)
ntm --robot-send=myproject --msg="Start work" --op-id="op_start_work_1"
```

**REST:**
```bash
curl -X POST /api/robot/send \
  -H "X-Request-ID: req_20260322035400_a1b2c3d4" \
  -d '{"target": "...", "msg": "..."}'
```

**WebSocket:**
```json
{
  "type": "send",
  "request_id": "req_20260322035400_a1b2c3d4",
  "target": "agent:ntm/myproject/0.2",
  "msg": "Start work"
}
```

---

## 3. Idempotency

### 3.1 Idempotency Key

Clients provide a durable operation ID for deduplication (shipped as
`--op-id`; the `idem_*` key family below is the design vocabulary):

```bash
ntm --robot-send=myproject --msg="Start work" --op-id="idem_task123_retry1"
```

**Response (shipped shape):**
```json
{
  "success": true,
  "operation": {
    "operation_id": "idem_task123_retry1",
    "status": "completed",
    "payload_sha256": "..."
  }
}
```

### 3.2 Duplicate Detection

If the same idempotency key is resubmitted:

```json
{
  "success": true,
  "request_id": "req_20260322035400_a1b2c3d4",  // Original request ID
  "idempotency_key": "idem_task123_retry1",
  "duplicate": true,
  "original_response": {
    "success": true,
    "sent_at": "2026-03-22T03:54:00Z",
    "action_ref": "action:ntm/act-send-20260322035400"
  }
}
```

### 3.3 Idempotency Key Scope

| Scope | Lifetime | Use Case |
|-------|----------|----------|
| `request` | Single request | Default, no dedup |
| `session` | Client session | Retry within session |
| `persistent` | 24 hours | Retry across reconnects |
| `indefinite` | Until explicit clear | Long-running workflows |

### 3.4 Idempotency Key Format

```
idem_<scope>_<user_id>

Examples:
  idem_session_abc123
  idem_persistent_task456_v2
  idem_workflow_bd-xyz_step3
```

### 3.5 Shipped Semantics (`--op-id` / `Idempotency-Key`)

`--robot-send` and `--robot-interrupt` accept `--op-id=<ID>`; the REST send
and interrupt endpoints take the same ID as the `Idempotency-Key` header.

| Property | Behavior |
|----------|----------|
| Claim point | The ID is claimed durably (runtime state DB, `send_operations`) before any pane is touched. After preparation, a second atomic write records `operation.dispatch_started_at`, the final redacted payload digest, and the selected targets before the first keystroke or interrupt key. Both writes must succeed. Dry runs never claim. |
| Scope | One namespace per session, shared across actuation kinds: the same ID may be used in two sessions independently, but not for a send and an interrupt in one session. |
| Binding | The ID is bound to the actuation kind plus the caller's command spec. Send: session, `--pane/--panes/--type/--exclude/--all`, `--enter`, `--clear-input`, `--with-cass`/`--with-memory` toggles, input-message digest. Interrupt: session, `--panes`, `--type` (aliases canonicalized), `--all`, `--force`, input-message digest. Selector lists are order-insensitive; interrupt wait behavior (`--no-wait`, `--timeout`) is not bound, so a retry may lengthen the timeout. |
| Identical retry of a completed operation | Replays the recorded outcome (`operation.replayed: true`) without sending or interrupting again. A replayed failure carries a hint to use a new ID. |
| Conflicting reuse | `IDEMPOTENCY_CONFLICT` (REST 409); nothing is touched. |
| Retry while the first attempt is still running | `OPERATION_IN_PROGRESS` (REST 409) with the recorded targets' admissions `unknown`; reconcile with the receipt. |
| Recovery before dispatch | After 10 minutes, a claim with no dispatch marker can be recovered. Takeover replaces its ownership token, so a paused original caller cannot deliver input, release the replacement claim, or overwrite its receipt. |
| Recovery after dispatch may have started | A stale started operation returns `OPERATION_OUTCOME_UNKNOWN` (REST 409). Age never authorizes redelivery: the prior input may already have arrived, or the original caller may still be running. Inspect the receipt and the original panes; use a new operation ID only after deciding another delivery is needed. A late completion from the original owner can still settle the receipt. |
| Preflight or dispatch-boundary persistence failure | No input is sent. An unstarted claim is released so the ID remains retryable. After the durable dispatch boundary, the claim is retained until its owner records a terminal outcome. |
| Receipt | `--robot-send-receipt=<ID>` returns `operation` (with `kind`, original target admissions, and `dispatch_started_at` when applicable) plus `outcome` (send) or `interrupt_outcome` (interrupt). HTTP actuation errors preserve the same receipt under `details.operation`. Receipts expose payload digests and states, not message bytes or pane output, and completed records are pruned after 7 days. Unresolved started records are retained. |
| Upgrade with an unfinished legacy operation | Preexisting in-progress records are treated as possibly started because their delivery history cannot be reconstructed. They require reconciliation instead of automatic takeover. Completed receipts remain replayable. |
| No state store | Supplying an ID without the runtime state store fails closed with `NOT_IMPLEMENTED` before anything is touched. |

```bash
# Interrupt + retask, safely retryable after a caller timeout
ntm --robot-interrupt=myproject --msg="Stop and fix the build" --op-id="retask-42"
# ...timeout... identical retry: replays, no second Ctrl+C, no second task
ntm --robot-interrupt=myproject --msg="Stop and fix the build" --op-id="retask-42"
# Outcome by ID, from any process
ntm --robot-send-receipt="retask-42"
```

---

## 4. Retry Semantics

### 4.1 Command Classification

Commands are classified by retry safety:

| Command Type | Idempotent | Safe to Retry | Notes |
|--------------|------------|---------------|-------|
| Read commands | Yes | Always | No side effects |
| `send` (same content) | No | Use idempotency key | Duplicate message possible |
| `send` (different content) | No | Never | Creates new message |
| `spawn` (existing session) | Yes | Always | No-op if exists |
| `spawn` (new session) | No | Use idempotency key | Duplicate session possible |
| `interrupt` | No | Use idempotency key (`--op-id`) | Cumulative effect: a bare retry interrupts again and re-sends the task |
| `close-bead` | Yes | Always | No-op if closed |
| `ack-alert` | Yes | Always | No-op if acked |

### 4.2 Retry Response Fields

Responses include retry guidance:

```json
{
  "success": false,
  "error_code": "TIMEOUT",
  "retry": {
    "safe": true,
    "idempotent": false,
    "use_idempotency_key": true,
    "suggested_delay_ms": 1000,
    "max_retries": 3,
    "backoff": "exponential"
  }
}
```

### 4.3 Retry Decision Tree

```
REQUEST FAILED
├─ Timeout (no response received)
│   ├─ Read command → Retry immediately
│   └─ Write command → Retry with idempotency key
├─ Transport error (connection lost)
│   ├─ Read command → Retry on reconnect
│   └─ Write command → Check outcome before retry
├─ Error response
│   ├─ Retryable error → Follow retry guidance
│   └─ Non-retryable error → Do not retry
└─ Ambiguous (partial response)
    └─ Check outcome, then decide
```

### 4.4 Checking Outcomes Before Retry

When retry safety is uncertain:

```bash
# Check whether the send (or interrupt) completed (durable receipt by operation ID)
ntm --robot-send-receipt="op_start_work_1"
```

**Response:**
```json
{
  "request_id": "req_20260322035400_a1b2c3d4",
  "status": "completed",  // pending, completed, failed, unknown
  "action_ref": "action:ntm/act-send-20260322035400",
  "outcome": {
    "sent": true,
    "sent_at": "2026-03-22T03:54:00Z"
  }
}
```

---

## 5. Correlation

### 5.1 Correlation ID

Related requests share a correlation ID. A per-send `--correlation-id` flag is
design-not-shipped; the shipped correlation surface is the causality query,
which filters events by correlation/chain ID:

```bash
# Query all events on one correlation chain
ntm --robot-causality --causality-chain="corr_workflow_abc123"
```

### 5.2 Correlation in Responses

Responses echo correlation ID:

```json
{
  "request_id": "req_20260322035500_efgh5678",
  "correlation_id": "corr_workflow_abc123",
  "parent_request_id": "req_20260322035400_a1b2c3d4",
  "action_ref": "action:ntm/act-send-20260322035500"
}
```

### 5.3 Correlation in Events

Attention events include correlation:

```json
{
  "cursor": "evt_20260322035600",
  "category": "agent",
  "type": "response_received",
  "entity_ref": "agent:ntm/myproject/0.2",
  "correlation_id": "corr_workflow_abc123",
  "caused_by_request": "req_20260322035500_efgh5678"
}
```

### 5.4 Request Chain

For multi-step workflows:

```json
{
  "request_id": "req_20260322035600_ijkl9012",
  "correlation_id": "corr_workflow_abc123",
  "parent_request_id": "req_20260322035500_efgh5678",
  "chain": [
    "req_20260322035400_a1b2c3d4",
    "req_20260322035500_efgh5678",
    "req_20260322035600_ijkl9012"
  ]
}
```

---

## 6. Duplicate Handling

### 6.1 Duplicate Detection Window

Idempotency keys are cached:

| Scope | Cache Duration | Storage |
|-------|----------------|---------|
| `session` | Session lifetime | Memory |
| `persistent` | 24 hours | SQLite |
| `indefinite` | Until cleared | SQLite |

### 6.2 Duplicate Response

When a duplicate is detected:

```json
{
  "success": true,
  "duplicate": true,
  "idempotency_key": "idem_task123_retry1",
  "original_request_id": "req_20260322035400_a1b2c3d4",
  "original_completed_at": "2026-03-22T03:54:01Z",
  "original_response": { ... }
}
```

### 6.3 Superseded Requests

When a newer request supersedes:

```json
{
  "success": false,
  "error_code": "REQUEST_SUPERSEDED",
  "error": "Request superseded by newer request",
  "superseded_by": "req_20260322035700_mnop3456",
  "hint": "A newer request with the same target was processed. This request will not be executed.",
  "recovery": {
    "type": "check_outcome",
    "command": "ntm --robot-action-status --request-id=\"req_20260322035700_mnop3456\""
  }
}
```

### 6.4 Conflicting Requests

When requests conflict:

```json
{
  "success": false,
  "error_code": "REQUEST_CONFLICT",
  "error": "Conflicting request in progress",
  "conflict_with": "req_20260322035400_a1b2c3d4",
  "hint": "Another request targeting the same entity is being processed. Wait for completion or cancel.",
  "recovery": {
    "type": "wait_or_cancel",
    "wait_command": "ntm --robot-wait-request --request-id=\"req_20260322035400_a1b2c3d4\"",
    "cancel_command": "ntm --robot-cancel-request --request-id=\"req_20260322035400_a1b2c3d4\""
  }
}
```

---

## 7. Transport Recovery

### 7.1 Transport Failure Scenarios

| Scenario | Recovery Action |
|----------|-----------------|
| Timeout before response | Retry with idempotency key |
| Connection lost mid-request | Check outcome, then retry |
| Partial response received | Parse what's available, check outcome |
| Transport switch (CLI → REST) | Use same idempotency key |
| Server restart | Use persistent idempotency key |

### 7.2 Client State Persistence

Clients SHOULD persist:

```json
{
  "pending_requests": [
    {
      "request_id": "req_20260322035400_a1b2c3d4",
      "idempotency_key": "idem_task123_v1",
      "correlation_id": "corr_workflow_abc123",
      "command": "send",
      "target": "agent:ntm/myproject/0.2",
      "submitted_at": "2026-03-22T03:54:00Z",
      "timeout_at": "2026-03-22T03:55:00Z"
    }
  ]
}
```

### 7.3 Recovery on Reconnect

After transport failure:

```bash
# Check pending operations by their durable receipts
for op_id in $(cat pending_requests.json | jq -r '.pending_requests[].request_id'); do
  ntm --robot-send-receipt="$op_id"
done
```

**Possible outcomes:**
- `completed`: Remove from pending
- `failed`: Decide to retry or abandon
- `pending`: Wait for completion
- `unknown`: Retry with idempotency key

---

## 8. Cross-Transport Consistency

### 8.1 Request ID Across Transports

The same request ID works across all transports:

**CLI (shipped shape):**
```bash
ntm --robot-send=myproject --op-id="req_123" --msg="Start work"
```

**REST:**
```bash
curl -X POST /api/robot/send -H "X-Request-ID: req_123" ...
```

**WebSocket:**
```json
{ "type": "send", "request_id": "req_123", ... }
```

**SSE (outcome):**
```
event: action_completed
data: { "request_id": "req_123", ... }
```

### 8.2 Outcome Retrieval

Outcomes are retrievable regardless of originating transport:

```bash
# Check outcome from any transport
ntm --robot-send-receipt="req_123"
```

### 8.3 Transport-Specific Behavior

| Transport | Request ID | Idempotency | Outcome Delivery |
|-----------|------------|-------------|------------------|
| CLI | In response | Optional flag | Synchronous |
| REST | X-Request-ID | Optional header | Synchronous |
| WebSocket | In message | In message | Async message |
| SSE | N/A (read-only) | N/A | Event stream |

---

## 9. Go Implementation

### 9.1 Request Types

```go
package robot

import (
    "crypto/rand"
    "fmt"
    "time"
)

// RequestID uniquely identifies a request
type RequestID string

// NewRequestID generates a unique request ID
func NewRequestID() RequestID {
    ts := time.Now().UTC().Format("20060102150405")
    b := make([]byte, 4)
    rand.Read(b)
    return RequestID(fmt.Sprintf("req_%s_%x", ts, b))
}

// IdempotencyKey prevents duplicate processing
type IdempotencyKey string

// IdempotencyScope defines key lifetime
type IdempotencyScope string

const (
    ScopeSession    IdempotencyScope = "session"
    ScopePersistent IdempotencyScope = "persistent"
    ScopeIndefinite IdempotencyScope = "indefinite"
)

// CorrelationID links related requests
type CorrelationID string

// RequestContext carries request identity
type RequestContext struct {
    RequestID       RequestID       `json:"request_id"`
    IdempotencyKey  IdempotencyKey  `json:"idempotency_key,omitempty"`
    CorrelationID   CorrelationID   `json:"correlation_id,omitempty"`
    ParentRequestID RequestID       `json:"parent_request_id,omitempty"`
    ClientID        string          `json:"client_id,omitempty"`
    SubmittedAt     time.Time       `json:"submitted_at"`
}
```

### 9.2 Retry Types

```go
// RetryGuidance provides retry instructions
type RetryGuidance struct {
    Safe              bool   `json:"safe"`
    Idempotent        bool   `json:"idempotent"`
    UseIdempotencyKey bool   `json:"use_idempotency_key,omitempty"`
    SuggestedDelayMs  int    `json:"suggested_delay_ms,omitempty"`
    MaxRetries        int    `json:"max_retries,omitempty"`
    Backoff           string `json:"backoff,omitempty"` // none, linear, exponential
}

// DuplicateInfo describes a duplicate detection
type DuplicateInfo struct {
    Duplicate           bool        `json:"duplicate"`
    OriginalRequestID   RequestID   `json:"original_request_id,omitempty"`
    OriginalCompletedAt time.Time   `json:"original_completed_at,omitempty"`
    OriginalResponse    interface{} `json:"original_response,omitempty"`
}
```

### 9.3 Idempotency Cache

```go
// IdempotencyCache tracks processed requests
type IdempotencyCache interface {
    // Check returns the cached response if key exists
    Check(key IdempotencyKey) (*CachedResponse, bool)

    // Store caches a response for the key
    Store(key IdempotencyKey, response interface{}, scope IdempotencyScope)

    // Clear removes a key from cache
    Clear(key IdempotencyKey)
}

// CachedResponse holds a cached idempotent response
type CachedResponse struct {
    RequestID   RequestID
    Response    interface{}
    CompletedAt time.Time
    Scope       IdempotencyScope
}
```

### 9.4 Request Context in Responses

```go
// RobotResponse includes request context
type RobotResponse struct {
    Success       bool                   `json:"success"`
    Timestamp     string                 `json:"timestamp"`
    RequestID     RequestID              `json:"request_id,omitempty"`
    CorrelationID CorrelationID          `json:"correlation_id,omitempty"`
    Duplicate     *DuplicateInfo         `json:"duplicate,omitempty"`
    Retry         *RetryGuidance         `json:"retry,omitempty"`
    // ... other fields
}
```

---

## 10. Best Practices

### 10.1 Client Best Practices

1. **Always use idempotency keys for write operations**
2. **Persist request IDs for pending operations**
3. **Check outcomes before retrying uncertain requests**
4. **Use correlation IDs for multi-step workflows**
5. **Handle duplicates gracefully (they're success)**

### 10.2 Server Best Practices

1. **Always return request ID in responses**
2. **Cache idempotent responses for appropriate duration**
3. **Include retry guidance for retryable errors**
4. **Surface duplicates as success, not error**
5. **Log correlation ID for debugging**

---

## 11. Non-Goals

1. **Distributed transactions** — Single-instance only
2. **Exactly-once delivery** — At-most-once with dedup
3. **Saga coordination** — Simple request/response
4. **Cross-instance correlation** — Local correlation only

---

## 12. References

- [Robot Action Errors](robot-action-errors.md) — Error and recovery semantics
- [Robot Resource References](robot-resource-references.md) — Target references
- [Robot Schema Versioning](robot-schema-versioning.md) — Response envelope

---

## Appendix: Changelog

- **2026-03-22:** Initial request identity contract (bd-j9jo3.1.10)
- **2026-10-07:** `--robot-interrupt --op-id` (and the REST interrupt
  `Idempotency-Key`) ships on the shared durable operation protocol; the
  receipt reports the operation kind (§3.5)

---

*Reference: bd-j9jo3.1.10*
