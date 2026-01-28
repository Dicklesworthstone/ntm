# Robot Mode API Design Principles

> **Authoritative Reference** for NTM robot mode API design.
> All robot commands MUST follow these principles for consistency.

## Overview

NTM's robot mode provides a JSON API for AI agents and automation tools to interact with tmux sessions and agent orchestration. This document establishes the canonical patterns that ensure a coherent, intuitive, and ergonomic interface.

**Design Goals:**
1. **Predictable** - Consistent patterns across all commands
2. **Discoverable** - Self-documenting via `--robot-capabilities`
3. **Ergonomic** - Minimal typing, logical flag names
4. **Backward-compatible** - Breaking changes require deprecation period

---

## 1. Command Naming Patterns

### 1.1 Core Session Operations

Session-scoped commands use `=SESSION` syntax:

```bash
--robot-send=SESSION        # Send prompts to agents
--robot-tail=SESSION        # Capture pane output
--robot-spawn=SESSION       # Create new session
--robot-context=SESSION     # Get context window usage
--robot-wait=SESSION        # Wait for agent states
--robot-interrupt=SESSION   # Send Ctrl+C to agents
--robot-activity=SESSION    # Get agent activity state
--robot-health=SESSION      # Get session health
--robot-diagnose=SESSION    # Comprehensive health check
```

### 1.2 Global Commands

Commands that operate globally (no session context) are bool flags:

```bash
--robot-status              # List all sessions
--robot-version             # Version info
--robot-plan                # bv global plan
--robot-tools               # Tool inventory
--robot-capabilities        # API discovery
--robot-snapshot            # Unified state dump
--robot-triage              # bv triage analysis
--robot-dashboard           # Dashboard summary
```

### 1.3 Tool Bridges

External tool integrations follow `--robot-<tool>-<action>` pattern with **separate modifier flags**:

```bash
# CORRECT: Bool flag + separate modifiers
ntm --robot-jfp-search --query="debugging" --limit=10
ntm --robot-cass-search --query="auth error" --since=7d
ntm --robot-xf-search --query="rust async" --limit=20
ntm --robot-dcg-check --command="rm -rf /data"
ntm --robot-mail-check --project=myproj --agent=cc_1

# AVOID: Inline values in main flag (legacy pattern)
ntm --robot-jfp-search="debugging"      # Deprecated
ntm --robot-cass-search="auth error"    # Deprecated
```

### 1.4 Resource Lookups

Simple ID/path lookups MAY use inline values:

```bash
--robot-bead-show=bd-123        # Single bead by ID
--robot-bead-claim=bd-123       # Claim bead by ID
--robot-forecast=bd-123         # Forecast for bead
--robot-impact=src/main.go      # Impact for file
--robot-schema=status           # Schema for type
```

---

## 2. Parameter Patterns

### 2.1 Global Shared Modifiers

These flags are shared across many commands and MUST NOT be tool-prefixed:

| Flag | Description | Used With |
|------|-------------|-----------|
| `--limit=N` | Max results to return | search, list commands |
| `--offset=N` | Pagination offset | list commands |
| `--since=DATE` | Start date filter | search, history, diff |
| `--until=DATE` | End date filter | search commands |
| `--panes=1,2,3` | Filter to specific panes | session commands |
| `--all` | Include user pane (default: agent panes only) | send, interrupt |
| `--lines=N` | Lines to capture | tail, inspect |
| `--query=Q` | Search query | search commands |
| `--type=T` | Filter by type | activity, alerts, beads |
| `--session=S` | Filter by session | alerts, palette |
| `--timeout=D` | Operation timeout | wait, ack, interrupt |
| `--verbose` | Detailed output | status, health commands |
| `--dry-run` | Preview without executing | send, spawn, restart |
| `--output=PATH` | Output file path | save, monitor |

Note: `--robot-limit` and `--robot-offset` are accepted as explicit aliases for robot list outputs (status, snapshot, history). Unprefixed flags remain canonical.

**Example (Correct):**
```bash
ntm --robot-cass-search --query="auth" --limit=20 --since=7d
ntm --robot-alerts --type=error --session=myproject
ntm --robot-wait=myproject --timeout=2m --panes=1,2
```

**Example (Deprecated):**
```bash
ntm --robot-cass-search="auth" --cass-limit=20 --cass-since=7d  # Old pattern
ntm --robot-alerts --alerts-type=error --alerts-session=myproject  # Old pattern
```

### 2.2 Tool-Specific Modifiers

Only use tool prefix for options unique to that tool:

| Flag | Description | Why Prefixed |
|------|-------------|--------------|
| `--spawn-cc=N` | Claude agents to spawn | spawn-specific |
| `--spawn-cod=N` | Codex agents to spawn | spawn-specific |
| `--spawn-gmi=N` | Gemini agents to spawn | spawn-specific |
| `--spawn-preset=NAME` | Use preset recipe | spawn-specific |
| `--probe-method=M` | Probe detection method | probe-specific |
| `--xf-mode=semantic` | XF search mode | xf-specific |
| `--bulk-strategy=S` | Bulk assign strategy | bulk-assign-specific |

### 2.3 Aliases for Backward Compatibility

When standardizing flags, keep old prefixed versions as aliases:

```go
// Canonical (new)
rootCmd.Flags().IntVar(&limit, "limit", 20, "Max results to return")
// Alias (deprecated, kept for backward compatibility)
rootCmd.Flags().IntVar(&limit, "cass-limit", 20, "DEPRECATED: use --limit")
```

---

## 3. Output Envelope

All robot commands MUST include these fields:

```json
{
  "success": true,
  "timestamp": "2026-01-22T10:30:00Z",
  "error": null,
  "error_code": null,
  "hint": null
}
```

### 3.1 Success Response

```json
{
  "success": true,
  "timestamp": "2026-01-22T10:30:00Z",
  "sessions": [...],
  "_agent_hints": {
    "summary": "3 sessions active, 12 agents total"
  }
}
```

### 3.2 Error Response

```json
{
  "success": false,
  "timestamp": "2026-01-22T10:30:00Z",
  "error": "Session 'myproject' not found",
  "error_code": "SESSION_NOT_FOUND",
  "hint": "Use --robot-status to list available sessions"
}
```

### 3.3 Standard Error Codes

| Code | Meaning |
|------|---------|
| `SESSION_NOT_FOUND` | Session does not exist |
| `PANE_NOT_FOUND` | Pane does not exist |
| `TOOL_NOT_FOUND` | External tool not installed |
| `PROJECT_NOT_FOUND` | Project does not exist |
| `AGENT_NOT_FOUND` | Agent does not exist |
| `THREAD_NOT_FOUND` | Thread does not exist |
| `INVALID_FLAG` | Invalid flag combination |
| `INVALID_INPUT` | Invalid parameter value |
| `MISSING_REQUIRED` | Required parameter missing |
| `TIMEOUT` | Operation timed out |
| `NOT_IMPLEMENTED` | Feature not yet available |
| `PERMISSION_DENIED` | Authorization required |
| `INTERNAL_ERROR` | Unexpected internal error |

---

## 4. Exit Codes

| Code | Meaning | When Used |
|------|---------|-----------|
| 0 | Success | Operation completed successfully |
| 1 | Error | Parse error, command failed, invalid input |
| 2 | Unavailable | Tool not installed, NOT_IMPLEMENTED |

**Exit Code Mapping:**
- `TOOL_NOT_FOUND` → exit 2
- `NOT_IMPLEMENTED` → exit 2
- `MISSING_REQUIRED` → exit 1
- `INVALID_FLAG` → exit 1
- `INVALID_INPUT` → exit 1
- `SESSION_NOT_FOUND` → exit 1
- All other errors → exit 1

---

## 5. Pagination Pattern

Commands that return lists MUST support pagination:

```json
{
  "success": true,
  "timestamp": "2026-01-22T10:30:00Z",
  "total_matches": 150,
  "offset": 20,
  "count": 10,
  "has_more": true,
  "items": [...],
  "_agent_hints": {
    "next_offset": 30,
    "pages_remaining": 12
  }
}
```

**Required Flags:**
- `--limit=N` - Max items to return (default varies by command)
- `--offset=N` - Skip first N items (default 0)

**Required Response Fields:**
- `total_matches` - Total count before pagination
- `offset` - Current offset echoed back
- `count` - Number of items in current response
- `has_more` - Boolean indicating more results available
- `_agent_hints.next_offset` - Next offset value for convenience

**Status/Snapshot/History Note:** These commands expose pagination under a `pagination` object:
`{limit, offset, count, total, has_more, next_cursor}`.

---

## 6. Array Fields

Critical arrays are ALWAYS present, even if empty:

```json
{
  "sessions": [],
  "agents": [],
  "messages": [],
  "hits": []
}
```

This allows safe iteration without null checks.

---

## 7. Optional Fields

Use `omitempty` for optional fields - they are absent when not applicable:

- `error`, `error_code`, `hint` - Only on error
- `_agent_hints` - Only when hints available
- `variant` - Only if agent has model variant
- `body` - Only when `--include-bodies` set

---

## 8. Agent Hints

Include `_agent_hints` object with actionable suggestions:

```json
{
  "_agent_hints": {
    "summary": "Brief human-readable summary",
    "suggested_action": "What to do next",
    "next_offset": 20,
    "pages_remaining": 5,
    "safer_alternative": "Alternative command if blocked",
    "warnings": ["Any issues to be aware of"]
  }
}
```

---

## 9. Filter Echo Pattern

Commands with multiple filters SHOULD echo the active filters:

```json
{
  "success": true,
  "filters": {
    "status": "open",
    "type": "error",
    "session": "myproject",
    "since": "2026-01-01",
    "until": null
  },
  "items": [...]
}
```

This helps agents verify their filters were applied correctly.

---

## 10. Verbose Flag Pattern

The `--verbose` flag is a global modifier that increases output detail.

**When to implement `--verbose`:**
- Safety checks - show analysis details
- Status commands - show extended metadata
- Commands where abbreviated vs detailed output makes sense

**Example (without --verbose):**
```json
{
  "success": true,
  "allowed": false,
  "severity": "high",
  "rationale": "Destructive command"
}
```

**Example (with --verbose):**
```json
{
  "success": true,
  "allowed": false,
  "severity": "high",
  "rationale": "Destructive command",
  "analysis": {
    "command_parsed": ["git", "reset", "--hard"],
    "flags_detected": ["--hard"],
    "risk_factors": ["discards uncommitted changes"]
  }
}
```

---

## 11. Documentation Requirements

Every robot command must document:

1. **Command interface** with examples
2. **Output schema** with JSON example
3. **Error response** with JSON example (including error_code)
4. **Modifier flags** table with scope (global/tool-specific)
5. **Exit codes** (must follow standard)
6. **Unit tests** (80% coverage target)

---

## 12. Backward Compatibility

- JSON output is the default and must remain stable
- TOON format is opt-in via `--robot-format=toon`
- Schema changes must be additive (new fields ok, removal requires deprecation)
- Deprecation period: 2 minor versions before removal
- Old prefixed flags remain as aliases during deprecation

---

## 13. Flag Deprecation Reference

The following prefixed flags are deprecated. Use the canonical unprefixed form:

### CASS Flags
| Deprecated | Canonical |
|------------|-----------|
| `--cass-limit` | `--limit` |
| `--cass-since` | `--since` |
| `--cass-agent` | `--agent` |
| `--cass-workspace` | `--workspace` |

### JFP Flags
| Deprecated | Canonical |
|------------|-----------|
| `--jfp-category` | `--category` |
| `--jfp-tag` | `--tag` |

---

## 14. New Robot Command PR Checklist

Use this checklist when adding or modifying a robot command:

- **Naming:** Global commands are bool flags; session-scoped commands use `=SESSION`.
- **Modifiers:** Prefer global unprefixed flags (`--limit`, `--since`, `--type`). Keep deprecated prefixed aliases when standardizing.
- **Output Envelope:** Always include `success`, `timestamp`, and error fields (`error`, `error_code`, `hint`) per the standard schema.
- **Arrays:** Critical arrays are always present (empty slice, not null or omitted).
- **Pagination:** For list outputs, include `total_matches`, `offset`, `count`, `has_more`, and `_agent_hints.next_offset`.
- **Filters:** Echo active filters in a `filters` object when multiple filters are supported.
- **Errors & Exit Codes:** Use standard error codes and map to exit code 1 or 2 as specified.
- **Determinism:** Ensure stable ordering for arrays and schema fields (especially in JSON).
- **Docs:** Add/refresh examples and schema notes in this document; ensure `--robot-help` points here.
- **Capabilities:** Update `--robot-capabilities` output/schema if new fields or commands are added.
- **Tests:** Add unit tests for flag parsing, validation, and output; add/update E2E script with required log prefix and exit-code assertions.

### Tokens Flags
| Deprecated | Canonical |
|------------|-----------|
| `--tokens-days` | `--days` |
| `--tokens-since` | `--since` |
| `--tokens-group-by` | `--group-by` |
| `--tokens-session` | `--session` |
| `--tokens-agent` | `--agent` |

### History Flags
| Deprecated | Canonical |
|------------|-----------|
| `--history-pane` | `--pane` |
| `--history-type` | `--type` |
| `--history-last` | `--last` |
| `--history-since` | `--since` |
| `--history-stats` | `--stats` |

### Activity Flags
| Deprecated | Canonical |
|------------|-----------|
| `--activity-type` | `--type` |

### Wait Flags
| Deprecated | Canonical |
|------------|-----------|
| `--wait-timeout` | `--timeout` |
| `--wait-type` | `--type` |
| `--wait-panes` | `--panes` |
| `--wait-poll` | `--poll` |
| `--wait-any` | `--any` |
| `--wait-exit-on-error` | `--exit-on-error` |
| `--wait-transition` | `--transition` |

### Route Flags
| Deprecated | Canonical |
|------------|-----------|
| `--route-type` | `--type` |
| `--route-strategy` | `--strategy` |
| `--route-exclude` | `--exclude` |

### Files Flags
| Deprecated | Canonical |
|------------|-----------|
| `--files-window` | `--window` |
| `--files-limit` | `--limit` |

### Inspect Flags
| Deprecated | Canonical |
|------------|-----------|
| `--inspect-index` | `--index` |
| `--inspect-lines` | `--lines` |
| `--inspect-code` | `--code` |

### Metrics Flags
| Deprecated | Canonical |
|------------|-----------|
| `--metrics-period` | `--period` |

### Alerts Flags
| Deprecated | Canonical |
|------------|-----------|
| `--alerts-severity` | `--severity` |
| `--alerts-type` | `--type` |
| `--alerts-session` | `--session` |

### Beads Flags
| Deprecated | Canonical |
|------------|-----------|
| `--beads-status` | `--status` |
| `--beads-priority` | `--priority` |
| `--beads-assignee` | `--assignee` |
| `--beads-type` | `--type` |
| `--beads-limit` | `--limit` |

### Summary/Diff Flags
| Deprecated | Canonical |
|------------|-----------|
| `--summary-since` | `--since` |
| `--diff-since` | `--since` |

### Triage/Analysis Flags
| Deprecated | Canonical |
|------------|-----------|
| `--triage-limit` | `--limit` |
| `--attention-limit` | `--limit` |
| `--hotspots-limit` | `--limit` |
| `--relations-limit` | `--limit` |
| `--relations-threshold` | `--threshold` |
| `--file-beads-limit` | `--limit` |

### Ack Flags
| Deprecated | Canonical |
|------------|-----------|
| `--ack-timeout` | `--timeout` |
| `--ack-poll` | `--poll` |

### Save Flags
| Deprecated | Canonical |
|------------|-----------|
| `--save-output` | `--output` |

### Replay Flags
| Deprecated | Canonical |
|------------|-----------|
| `--replay-id` | `--id` |
| `--replay-dry-run` | `--dry-run` |

### Provider Flags
| Deprecated | Canonical |
|------------|-----------|
| `--account-status-provider` | `--provider` |
| `--accounts-list-provider` | `--provider` |
| `--quota-check-provider` | `--provider` |

### Verbose Flags
| Deprecated | Canonical |
|------------|-----------|
| `--is-working-verbose` | `--verbose` |
| `--agent-health-verbose` | `--verbose` |
| `--smart-restart-verbose` | `--verbose` |

### Palette Flags
| Deprecated | Canonical |
|------------|-----------|
| `--palette-session` | `--session` |
| `--palette-category` | `--category` |
| `--palette-search` | `--search` |

### Dismiss Flags
| Deprecated | Canonical |
|------------|-----------|
| `--dismiss-session` | `--session` |
| `--dismiss-all` | `--all` |

### Interrupt Flags
| Deprecated | Canonical |
|------------|-----------|
| `--interrupt-msg` | `--msg` |
| `--interrupt-all` | `--all` |
| `--interrupt-force` | `--force` |
| `--interrupt-no-wait` | `--no-wait` |
| `--interrupt-timeout` | `--timeout` |

### Pipeline Flags
| Deprecated | Canonical |
|------------|-----------|
| `--pipeline-session` | `--session` |
| `--pipeline-vars` | `--vars` |
| `--pipeline-dry-run` | `--dry-run` |
| `--pipeline-background` | `--background` |

### Diagnose Flags
| Deprecated | Canonical |
|------------|-----------|
| `--diagnose-fix` | `--fix` |
| `--diagnose-brief` | `--brief` |
| `--diagnose-pane` | `--pane` |

### Markdown Flags
| Deprecated | Canonical |
|------------|-----------|
| `--md-compact` | `--compact` |
| `--md-session` | `--session` |
| `--md-sections` | `--sections` |
| `--md-max-beads` | `--max-beads` |
| `--md-max-alerts` | `--max-alerts` |

### Bulk-Assign Flags
| Deprecated | Canonical |
|------------|-----------|
| `--bulk-strategy` | `--strategy` |
| `--skip-panes` | `--skip` |
| `--prompt-template` | `--template` |

---

## 15. JSON Schema Generation

NTM provides built-in JSON Schema generation for all robot command outputs. This enables:

- **Type-safe integration** - Generate client types from schemas
- **Validation** - Validate responses against canonical schemas
- **Documentation** - Auto-generate API docs from schemas

### Usage

```bash
# Simple form (recommended)
ntm --schema status              # Schema for status output
ntm --schema all                 # All available schemas

# Long form (equivalent)
ntm --robot-schema=status
ntm --robot-schema=all
```

### Available Schema Types

| Type | Description | Command |
|------|-------------|---------|
| `status` | Full system status | `--robot-status` |
| `snapshot` | Unified state dump | `--robot-snapshot` |
| `version` | Version information | `--robot-version` |
| `spawn` | Session creation | `--robot-spawn` |
| `send` | Message delivery | `--robot-send` |
| `interrupt` | Agent interruption | `--robot-interrupt` |
| `tail` | Pane output capture | `--robot-tail` |
| `ack` | Send acknowledgment | `--robot-ack` |
| `inspect` | Pane inspection | `--robot-inspect-pane` |
| `ensemble` | Ensemble state | `--robot-ensemble` |
| `ensemble_spawn` | Ensemble creation | `--robot-ensemble-spawn` |
| `beads_list` | Bead listing | `--robot-bead-list` |
| `assign` | Work assignment | `--robot-assign` |
| `triage` | Triage analysis | `--robot-triage` |
| `health` | Health check | `--robot-health` |
| `diagnose` | Diagnostic report | `--robot-diagnose` |
| `agent_health` | Agent health | `--robot-agent-health` |
| `is_working` | Working state | `--robot-is-working` |
| `all` | All schemas | - |

### Example Output

```bash
ntm --schema status | jq '.schema.properties | keys'
[
  "_meta",
  "agent_mail",
  "alerts",
  "beads",
  "error",
  "error_code",
  "generated_at",
  "sessions",
  "success",
  "summary",
  "system",
  "timestamp",
  "version"
]
```

### Schema Versioning

All schemas include:
- `$schema`: JSON Schema draft version (draft-07)
- `title`: Human-readable schema title
- Schema output includes `version` field (currently `1.0.0`)

---

## Quick Reference Card

```
GLOBAL COMMANDS (bool flags)
  --robot-status          List all sessions
  --robot-version         Version info
  --robot-snapshot        Unified state dump
  --robot-capabilities    API discovery
  --schema=TYPE           JSON Schema generation

SESSION COMMANDS (=SESSION syntax)
  --robot-send=S          Send prompts
  --robot-tail=S          Capture output
  --robot-wait=S          Wait for state
  --robot-spawn=S         Create session

GLOBAL MODIFIERS (unprefixed)
  --limit=N               Max results
  --offset=N              Pagination offset
  --since=DATE            Date filter
  --query=Q               Search query
  --type=T                Type filter
  --panes=1,2             Pane filter
  --all                   Include user pane (default: agent panes only)
  --timeout=D             Timeout
  --verbose               Detailed output
  --dry-run               Preview mode

OUTPUT FORMAT
  --robot-format=json     JSON (default)
  --robot-format=toon     Token-efficient
  --robot-markdown        Markdown tables
  --robot-terse           Single-line state
```

---

*Last updated: 2026-01-27*
*Reference: bd-3045p, bd-12nbo*
