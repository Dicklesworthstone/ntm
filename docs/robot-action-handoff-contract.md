# Robot Action-Handoff, Remediation, and Error Semantics Contract

> **Authoritative reference** for ntm robot mode error handling, action handoff, and recovery semantics.
> All robot surfaces MUST follow this contract for machine-actionable responses.

**Status:** AUTHORITATIVE
**Bead:** bd-j9jo3.1.7
**Created:** 2026-03-22
**Prerequisites:**
- docs/robot-surface-taxonomy.md (bd-j9jo3.1.1)
- docs/robot-section-model.md (bd-j9jo3.1.2)
- docs/robot-api-design.md

---

## 1. Purpose

This document defines how robot surfaces communicate:
1. **Recoverable errors** - failures an agent can handle automatically
2. **Next actions** - machine-readable follow-up commands
3. **Retry semantics** - when and how to retry failed operations
4. **Action handoff** - moving from summary to drill-down to actuation

**Design Goal:** An agent should never have to guess what to do next from human prose.

**Anti-Goal:** Wrapping every response in bureaucratic metadata.

---

## 2. Error Taxonomy

### 2.1 Error Code Categories

All robot errors use the `error_code` field with stable string identifiers.

| Category | Prefix | Retryable | Description |
|----------|--------|-----------|-------------|
| **Resource** | `*_NOT_FOUND` | No | Target doesn't exist |
| **Input** | `INVALID_*` | No | Bad request parameters |
| **Temporal** | `*_EXPIRED`, `TIMEOUT` | Conditional | Time-bounded failure |
| **Dependency** | `DEPENDENCY_MISSING` | No | Required tool unavailable |
| **Operational** | `*_FAILED`, `*_BUSY` | Maybe | Runtime failure |
| **Internal** | `INTERNAL_ERROR` | Maybe | Unexpected server error |
| **Permission** | `PERMISSION_DENIED` | No | Authorization failure |

### 2.2 Complete Error Code Registry

```go
// Resource errors - target doesn't exist
SESSION_NOT_FOUND      // Session name not found in tmux
PANE_NOT_FOUND         // Pane index doesn't exist
BEAD_NOT_FOUND         // Bead ID not found
ENSEMBLE_NOT_FOUND     // Ensemble doesn't exist
AGENT_NOT_FOUND        // Agent identifier not found

// Input errors - bad request
INVALID_FLAG           // Unknown or malformed flag
INVALID_CURSOR         // Cursor format invalid (not expired, just malformed)
INVALID_TARGET         // Inspect target not recognized
INVALID_FILTER         // Filter expression malformed
INVALID_RANGE          // Pagination range invalid

// Temporal errors - time-bounded failures
CURSOR_EXPIRED         // Event cursor too old, requires resync
REPLAY_WINDOW_EXPIRED  // Historical data no longer available
TIMEOUT                // Operation exceeded time limit
RATE_LIMITED           // Too many requests, backoff required

// Dependency errors - external requirements
DEPENDENCY_MISSING     // Required tool not installed (tmux, br, etc.)
SOURCE_UNAVAILABLE     // Data source temporarily unreachable
FEATURE_DISABLED       // Feature not enabled in configuration

// Operational errors - runtime failures
SOFT_EXIT_FAILED       // Graceful agent shutdown failed
HARD_KILL_FAILED       // Forced agent termination failed
SHELL_NOT_RETURNED     // Expected shell prompt not detected
CC_LAUNCH_FAILED       // Claude Code launch failed
CC_INIT_TIMEOUT        // Claude Code initialization timeout
PROMPT_SEND_FAILED     // Could not send prompt to agent
RESOURCE_BUSY          // Resource in use by another operation

// Internal errors - unexpected failures
INTERNAL_ERROR         // Catch-all for unexpected errors

// Permission errors - authorization
PERMISSION_DENIED      // Operation not authorized
```

### 2.3 Error Response Shape

All error responses include:

```json
{
  "success": false,
  "timestamp": "2026-03-22T03:45:00Z",
  "error": "Human-readable error message",
  "error_code": "SESSION_NOT_FOUND",
  "hint": "Use 'ntm --robot-status' to list available sessions",
  "recovery": {
    "retryable": false,
    "retry_after_ms": null,
    "resync_required": false,
    "next_actions": [
      {
        "action": "robot-status",
        "args": "",
        "reason": "List available sessions"
      }
    ]
  }
}
```

---

## 3. Retry Semantics

### 3.1 Retry Classification

| Error Code | Retryable | Strategy |
|------------|-----------|----------|
| `TIMEOUT` | Yes | Exponential backoff, max 3 attempts |
| `RATE_LIMITED` | Yes | Honor `retry_after_ms` if present |
| `RESOURCE_BUSY` | Yes | Short delay (100-500ms), max 5 attempts |
| `CURSOR_EXPIRED` | No | Requires resync (not retry) |
| `*_NOT_FOUND` | No | Different action required |
| `INVALID_*` | No | Fix input, then retry |
| `DEPENDENCY_MISSING` | No | Install dependency first |
| `INTERNAL_ERROR` | Maybe | Single retry with delay |

### 3.2 Retry Response Fields

```json
{
  "recovery": {
    "retryable": true,
    "retry_after_ms": 1000,
    "retry_strategy": "exponential_backoff",
    "max_retries": 3
  }
}
```

### 3.3 Idempotency Keys

For operations that modify state, clients SHOULD:
1. Generate a unique `idempotency_key` per logical operation
2. Reuse the same key on retries
3. Use format: `<agent_name>:<timestamp_ns>:<random_suffix>`

Servers MUST:
1. Accept `idempotency_key` in request headers or body
2. Return cached response for duplicate keys within TTL (default: 5 minutes)
3. Return `409 Conflict` if key reused with different parameters

---

## 4. Resync Protocol

### 4.1 When Resync Is Required

Resync is required when:
- `error_code` is `CURSOR_EXPIRED`
- `error_code` is `REPLAY_WINDOW_EXPIRED`
- Response includes `resync_required: true`

### 4.2 Resync Flow

```
1. Client receives CURSOR_EXPIRED with resync_cursor
2. Client calls --robot-snapshot to get fresh state
3. Client resumes streaming from resync_cursor
4. Client reconciles local state with snapshot
```

### 4.3 Resync Response Fields

```json
{
  "error_code": "CURSOR_EXPIRED",
  "recovery": {
    "resync_required": true,
    "resync_cursor": "evt_1711082700000000123",
    "resync_action": {
      "action": "robot-snapshot",
      "args": "--robot-snapshot",
      "reason": "Refresh state before resuming event stream"
    }
  }
}
```

---

## 5. Next Actions Shape

### 5.1 NextAction Structure

Every response MAY include `next_actions` suggesting follow-up commands:

```go
type NextAction struct {
    // Action is the robot command name (e.g., "robot-tail", "robot-send").
    // Required.
    Action string `json:"action"`

    // Args are the command arguments as a single string.
    // Required. Should be copy-paste ready.
    Args string `json:"args"`

    // Reason explains why this action is suggested.
    // Optional but recommended for operator context.
    Reason string `json:"reason,omitempty"`

    // Priority indicates relative importance (lower = more important).
    // Optional. Default is 0.
    Priority int `json:"priority,omitempty"`

    // Preconditions that must be true for this action to be valid.
    // Optional.
    Preconditions []string `json:"preconditions,omitempty"`
}
```

### 5.2 Common Next Action Patterns

| Situation | Suggested Action | Reason |
|-----------|------------------|--------|
| Session unhealthy | `robot-diagnose=SESSION` | Get detailed health diagnostics |
| Agent stuck | `robot-interrupt=SESSION --pane=N` | Send interrupt signal |
| Agent crashed | `robot-restart-pane=SESSION --pane=N` | Restart the agent |
| Quota warning | `robot-quota-status` | Check remaining quota |
| Mail pending | `robot-mail-check --project=X --agent=Y` | Read unread messages |
| Bead ready | `robot-bead-show=ID` | View bead details |

### 5.3 Action Handoff Hierarchy

The standard drill-down path is:

```
snapshot → status → digest → attention → inspect → act
   ↑                                            |
   └────────────── loop back ──────────────────←┘
```

Each level suggests the next:

| Surface | Suggests |
|---------|----------|
| `snapshot` | `robot-status` for summary, `robot-events` for streaming |
| `status` | `robot-diagnose` for unhealthy, `robot-attention` for alerts |
| `digest` | `robot-inspect-*` for drill-down, `robot-send` for actuation |
| `attention` | Specific `robot-inspect-*` for the attention item |
| `inspect` | `robot-send`, `robot-interrupt`, `robot-restart-pane` |
| `act` | `robot-status` to verify effect |

---

## 6. Degraded State Handling

### 6.1 Partial Success

When an operation partially succeeds:

```json
{
  "success": true,
  "partial": true,
  "degraded_sources": ["beads", "mail"],
  "source_health": {
    "sessions": { "fresh": true, "age_ms": 50 },
    "beads": { "fresh": false, "age_ms": 120000, "degraded_reason": "br timeout" },
    "mail": { "fresh": false, "age_ms": 5000, "degraded_reason": "network error" }
  },
  "warnings": [
    "Beads data is stale (2m old)",
    "Mail check failed, using cached data"
  ],
  "next_actions": [
    {
      "action": "robot-tools",
      "args": "",
      "reason": "Verify br is operational"
    }
  ]
}
```

### 6.2 Degraded Source Indicators

| Field | Type | Description |
|-------|------|-------------|
| `partial` | bool | True if some data is missing/stale |
| `degraded_sources` | []string | List of degraded source names |
| `source_health` | map | Per-source freshness info |
| `warnings` | []string | Human-readable degradation notes |

---

## 7. Transport Parity

### 7.1 Cross-Transport Consistency

All transports (CLI JSON, REST, SSE, WebSocket) MUST:
1. Use identical error codes
2. Return identical `recovery` structures
3. Include identical `next_actions`
4. Preserve `partial`/`degraded_sources` semantics

### 7.2 Transport-Specific Behavior

| Transport | Error Delivery | Retry Headers |
|-----------|---------------|---------------|
| CLI JSON | `error_code` in envelope | N/A |
| REST | HTTP status + `error_code` | `Retry-After` header |
| SSE | `event: error` type | Reconnect field |
| WebSocket | Message with `type: error` | N/A |

### 7.3 HTTP Status Code Mapping

| Error Code | HTTP Status |
|------------|-------------|
| `*_NOT_FOUND` | 404 |
| `INVALID_*` | 400 |
| `PERMISSION_DENIED` | 403 |
| `RATE_LIMITED` | 429 |
| `TIMEOUT` | 504 |
| `RESOURCE_BUSY` | 409 |
| `DEPENDENCY_MISSING` | 503 |
| `INTERNAL_ERROR` | 500 |

---

## 8. Implementation Checklist

### 8.1 For Each Robot Surface

- [ ] Return `error_code` (not just `error` string) on failure
- [ ] Include `hint` with actionable guidance
- [ ] Set `recovery.retryable` correctly
- [ ] Include `next_actions` suggesting follow-up
- [ ] Report `partial`/`degraded_sources` when applicable
- [ ] Handle cursor expiration with `resync_cursor`

### 8.2 For Each Error Path

- [ ] Map to specific error code (not generic `INTERNAL_ERROR`)
- [ ] Set appropriate HTTP status (REST transport)
- [ ] Include retry guidance if retryable
- [ ] Suggest resync action if state recovery needed
- [ ] Log structured error for debugging

---

## 9. Validation Criteria

This contract is satisfied when:

1. **Recovery Determinism**: An agent can determine retry/resync/fail without parsing prose
2. **Action Completeness**: Every response suggests a logical next step
3. **Transport Parity**: Error semantics identical across CLI/REST/SSE/WS
4. **Degradation Transparency**: Partial data is always explicitly marked

---

## Appendix A: Existing Error Code Usage

Current ntm error codes (from `internal/robot/types.go`):

```go
const (
    ErrCodeSessionNotFound   = "SESSION_NOT_FOUND"
    ErrCodePaneNotFound      = "PANE_NOT_FOUND"
    ErrCodeInvalidFlag       = "INVALID_FLAG"
    ErrCodeTimeout           = "TIMEOUT"
    ErrCodeNotImplemented    = "NOT_IMPLEMENTED"
    ErrCodeDependencyMissing = "DEPENDENCY_MISSING"
    ErrCodeInternalError     = "INTERNAL_ERROR"
    ErrCodePermissionDenied  = "PERMISSION_DENIED"
    ErrCodeResourceBusy      = "RESOURCE_BUSY"
    ErrCodeSoftExitFailed    = "SOFT_EXIT_FAILED"
    ErrCodeHardKillFailed    = "HARD_KILL_FAILED"
    ErrCodeShellNotReturned  = "SHELL_NOT_RETURNED"
    ErrCodeCCLaunchFailed    = "CC_LAUNCH_FAILED"
    ErrCodeCCInitTimeout     = "CC_INIT_TIMEOUT"
    ErrCodeBeadNotFound      = "BEAD_NOT_FOUND"
    ErrCodePromptSendFailed  = "PROMPT_SEND_FAILED"
)

// Additional codes from attention_contract.go:
const ErrCodeCursorExpired = "CURSOR_EXPIRED"

// Additional codes from ensemble.go:
const ErrCodeEnsembleNotFound = "ENSEMBLE_NOT_FOUND"
```

---

## Appendix B: Changelog

- **2026-03-22:** Initial contract (bd-j9jo3.1.7)

---

*Reference: bd-j9jo3.1.7*
