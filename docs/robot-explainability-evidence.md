# Robot Explainability, Evidence, and Diagnostic Semantics Contract

> **Authoritative reference** for operational explainability across robot surfaces.
> All robot surfaces MUST include explanations for decisions, omissions, and degradations.

**Status:** AUTHORITATIVE
**Bead:** bd-j9jo3.1.8
**Created:** 2026-03-22

---

## 1. Purpose

This document defines how robot surfaces explain **why** data is stale, degraded, truncated, prioritized, or escalated. The goal is operational clarity: consumers can answer "why is this?" and "where can I learn more?" without grepping logs or reading source code.

**Design Goals:**
1. Every surface decision has a machine-readable explanation
2. Explanations are shallow for summaries, deep for inspect
3. Evidence is structured, not prose
4. Drill-down handles link to deeper investigation
5. Explanations are stable across transports

**Non-Goals:**
1. Per-field provenance (debug spam)
2. Natural language rationalization
3. Full audit trails (that's bd-j9jo3.1.15)
4. AI-style confidence scores

---

## 2. Explanation Shapes

### 2.1 Core Explanation Object

Every explainable decision includes an `explanation` object:

```json
{
  "decision": "truncated",
  "explanation": {
    "type": "budget_limit",
    "short": "20 of 47 sessions returned",
    "code": "EXPLAIN_BUDGET_EXCEEDED",
    "detail": "Payload budget of 2000 tokens reached at session 20",
    "evidence": {
      "budget_tokens": 2000,
      "actual_tokens": 2100,
      "items_returned": 20,
      "items_total": 47
    },
    "drill_down": {
      "label": "List all sessions",
      "command": "ntm --robot-status --limit=0"
    }
  }
}
```

### 2.2 Explanation Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | Yes | Explanation category (see §2.3) |
| `short` | string | Yes | One-line summary for compact display |
| `code` | string | Yes | Machine-readable explanation code |
| `detail` | string | No | Longer description for tooltips/logs |
| `evidence` | object | No | Structured supporting data |
| `drill_down` | DrillDown | No | Link to deeper investigation |
| `related_refs` | []string | No | Entity references involved |

### 2.3 Explanation Types

| Type | Description | Example |
|------|-------------|---------|
| `budget_limit` | Output truncated for token budget | 20 of 47 sessions |
| `time_limit` | Data expired or timed out | Cache expired 5m ago |
| `source_degraded` | Source returned partial data | Beads DB unavailable |
| `source_unavailable` | Source unreachable | MCP server not running |
| `prioritization` | Items ordered by algorithm | Top 5 by PageRank score |
| `escalation` | Item promoted in severity | Alert → Incident |
| `redaction` | Data removed for sensitivity | API key masked |
| `aggregation` | Items combined for brevity | 12 similar alerts grouped |
| `filter_applied` | Items excluded by filter | 3 resolved alerts hidden |
| `cursor_gap` | Events missed between fetches | 47 events since last poll |
| `policy_applied` | Rule or threshold triggered | Quota >80% warning |

### 2.4 Explanation Codes

Machine-readable codes follow the format `EXPLAIN_<CATEGORY>_<SPECIFIC>`:

```
EXPLAIN_BUDGET_EXCEEDED
EXPLAIN_BUDGET_TOKEN_LIMIT
EXPLAIN_BUDGET_ITEM_LIMIT
EXPLAIN_TIME_EXPIRED
EXPLAIN_TIME_STALE
EXPLAIN_SOURCE_UNAVAILABLE
EXPLAIN_SOURCE_DEGRADED
EXPLAIN_SOURCE_TIMEOUT
EXPLAIN_PRIORITY_PAGERANK
EXPLAIN_PRIORITY_SEVERITY
EXPLAIN_PRIORITY_RECENCY
EXPLAIN_ESCALATION_DURATION
EXPLAIN_ESCALATION_SEVERITY
EXPLAIN_ESCALATION_SCOPE
EXPLAIN_REDACTION_SENSITIVE
EXPLAIN_REDACTION_POLICY
EXPLAIN_FILTER_STATUS
EXPLAIN_FILTER_TYPE
EXPLAIN_CURSOR_GAP
EXPLAIN_CURSOR_EXPIRED
EXPLAIN_POLICY_THRESHOLD
EXPLAIN_POLICY_RULE
```

---

## 3. Evidence Structures

### 3.1 Evidence Principle

Evidence is structured data supporting an explanation. It answers "based on what?" without requiring log inspection.

### 3.2 Budget Evidence

```json
{
  "evidence": {
    "budget_type": "tokens",
    "budget_limit": 2000,
    "budget_actual": 2100,
    "items_returned": 20,
    "items_total": 47,
    "items_omitted": 27,
    "omitted_refs": ["session:ntm/c", "session:ntm/d", "..."]
  }
}
```

### 3.3 Freshness Evidence

```json
{
  "evidence": {
    "source": "beads",
    "last_success_at": "2026-03-22T03:25:00Z",
    "stale_after_ms": 60000,
    "current_age_ms": 120000,
    "last_error": "Connection refused",
    "last_error_at": "2026-03-22T03:26:00Z",
    "retry_count": 3
  }
}
```

### 3.4 Prioritization Evidence

```json
{
  "evidence": {
    "algorithm": "pagerank",
    "score": 0.264,
    "rank": 1,
    "factors": [
      { "name": "unblocks", "value": 5, "weight": 0.4 },
      { "name": "betweenness", "value": 0.12, "weight": 0.3 },
      { "name": "priority", "value": 1, "weight": 0.3 }
    ],
    "baseline_score": 0.15,
    "threshold_applied": 0.10
  }
}
```

### 3.5 Escalation Evidence

```json
{
  "evidence": {
    "trigger": "duration_exceeded",
    "duration_ms": 1800000,
    "threshold_ms": 1800000,
    "original_severity": "warning",
    "promoted_severity": "P1",
    "source_alert_id": "alert-xyz789",
    "promotion_rule": "alert_duration_30m"
  }
}
```

### 3.6 Redaction Evidence

```json
{
  "evidence": {
    "redaction_type": "sensitive_value",
    "field_path": "context.api_key",
    "policy": "mask_secrets",
    "original_length": 42,
    "preview": "sk-...9xyz"
  }
}
```

### 3.7 Cursor Gap Evidence

```json
{
  "evidence": {
    "cursor_type": "event_stream",
    "last_seen_cursor": "evt_20260322030000",
    "current_cursor": "evt_20260322033000",
    "gap_events": 47,
    "gap_duration_ms": 1800000,
    "events_sampled": 10,
    "events_dropped": 37
  }
}
```

---

## 4. Explanation Levels

### 4.1 Level Principle

Explanations vary by verbosity level:

| Level | Use Case | Content |
|-------|----------|---------|
| `none` | Minimal output | No explanations |
| `short` | Summary surfaces | `short` field only |
| `standard` | Normal operation | `short` + `code` + `evidence` summary |
| `full` | Debugging | All fields including `detail` |
| `verbose` | Deep debugging | Full evidence with drill-down |

### 4.2 Level Selection

Surfaces default to appropriate levels:

| Surface | Default Level |
|---------|---------------|
| `terse` | none |
| `status` | short |
| `snapshot` | standard |
| `attention` | standard |
| `diagnose` | full |
| `inspect-*` | full |

### 4.3 Requesting Levels

<!-- ntm-docs: skip -->
```bash
# Force verbose explanations
ntm --robot-snapshot --explain=verbose

# Disable explanations for minimal output
ntm --robot-status --explain=none
```

### 4.4 Level Example

**short (status):**
```json
{
  "sessions": { "count": 20, "truncated": true },
  "_explain": { "short": "20 of 47 sessions" }
}
```

**standard (snapshot):**
```json
{
  "sessions": { "count": 20, "truncated": true },
  "_explain": {
    "type": "budget_limit",
    "short": "20 of 47 sessions",
    "code": "EXPLAIN_BUDGET_EXCEEDED",
    "evidence": { "budget_tokens": 2000, "items_total": 47 }
  }
}
```

**full (diagnose):**
```json
{
  "sessions": { "count": 20, "truncated": true },
  "_explain": {
    "type": "budget_limit",
    "short": "20 of 47 sessions",
    "code": "EXPLAIN_BUDGET_EXCEEDED",
    "detail": "Token budget of 2000 reached. Sessions 21-47 omitted. Use --limit=0 for full list.",
    "evidence": {
      "budget_tokens": 2000,
      "actual_tokens": 2100,
      "items_returned": 20,
      "items_total": 47,
      "omitted_refs": ["session:ntm/c", "session:ntm/d", "..."]
    },
    "drill_down": {
      "label": "Full session list",
      "command": "ntm --robot-status --limit=0"
    }
  }
}
```

---

## 5. Per-Item Explanations

### 5.1 Item-Level Explain

Individual items can carry explanations:

```json
{
  "attention_items": [
    {
      "cursor": "evt_20260322033000",
      "summary": "Agent became idle",
      "actionability": "action_required",
      "_explain": {
        "type": "prioritization",
        "short": "Promoted: idle for 10m",
        "code": "EXPLAIN_PRIORITY_IDLE_DURATION",
        "evidence": {
          "idle_seconds": 600,
          "threshold_seconds": 300,
          "prior_actionability": "interesting"
        }
      }
    }
  ]
}
```

### 5.2 Aggregated Item Explanations

When items are grouped:

```json
{
  "alerts": [
    {
      "id": "alert-group-123",
      "type": "agent_stuck",
      "count": 5,
      "_explain": {
        "type": "aggregation",
        "short": "5 similar alerts grouped",
        "code": "EXPLAIN_AGGREGATION_SIMILAR",
        "evidence": {
          "grouped_ids": ["alert-1", "alert-2", "alert-3", "alert-4", "alert-5"],
          "group_key": "type+session",
          "dedup_window_ms": 300000
        },
        "drill_down": {
          "label": "Show all 5 alerts",
          "command": "ntm --robot-alerts --expand-groups"
        }
      }
    }
  ]
}
```

### 5.3 Omitted Item Explanations

When items are filtered out:

```json
{
  "alerts": [...],
  "_omitted": {
    "count": 3,
    "_explain": {
      "type": "filter_applied",
      "short": "3 resolved alerts hidden",
      "code": "EXPLAIN_FILTER_STATUS",
      "evidence": {
        "filter": "status != resolved",
        "omitted_refs": ["alert:ntm/a", "alert:ntm/b", "alert:ntm/c"]
      },
      "drill_down": {
        "label": "Include resolved",
        "command": "ntm --robot-alerts --include-resolved"
      }
    }
  }
}
```

---

## 6. Drill-Down Linking

### 6.1 Drill-Down Structure

Every explanation can include a drill-down handle:

```json
{
  "drill_down": {
    "label": "Inspect agent details",
    "command": "ntm --robot-inspect-pane --ref=\"agent:ntm/myproject/0.2\"",
    "returns": "Full agent state with context and history",
    "depth": "inspect",
    "refs": ["agent:ntm/myproject/0.2"]
  }
}
```

### 6.2 Drill-Down Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `label` | string | Yes | Human-readable action |
| `command` | string | Yes | ntm command to run |
| `returns` | string | No | What the command returns |
| `depth` | string | No | Surface category: status, inspect, diagnose |
| `refs` | []string | No | Entity references to investigate |

### 6.3 Drill-Down Chains

Explanations can chain to deeper levels:

```
snapshot → status → inspect → diagnose
   ↓          ↓         ↓          ↓
 summary   detail   full state  root cause
```

**Example chain:**

```json
// Level 1: snapshot
{
  "alerts": { "total_active": 3, "has_critical": true },
  "_explain": {
    "short": "3 active alerts (1 critical)",
    "drill_down": {
      "label": "Alert list",
      "command": "ntm --robot-alerts",
      "depth": "status"
    }
  }
}

// Level 2: alerts (status)
{
  "alerts": [
    {
      "id": "alert-xyz",
      "severity": "critical",
      "message": "Agent crashed",
      "_explain": {
        "short": "Process exited 5m ago",
        "drill_down": {
          "label": "Full diagnostics",
          "command": "ntm --robot-diagnose=myproject --panes=2",
          "depth": "diagnose"
        }
      }
    }
  ]
}

// Level 3: diagnose
{
  "diagnosis": {
    "root_cause": "OOM killer terminated process",
    "evidence": {
      "exit_code": 137,
      "dmesg_lines": ["oom-killer: ..."],
      "memory_peak_mb": 8192
    },
    "_explain": {
      "type": "diagnosis",
      "short": "OOM kill detected",
      "code": "EXPLAIN_DIAGNOSIS_OOM",
      "drill_down": {
        "label": "View system logs",
        "command": "journalctl -u ntm --since='5 minutes ago'"
      }
    }
  }
}
```

---

## 7. Source Health Explanations

### 7.1 Degraded Source

When a source is degraded, explain why:

```json
{
  "source_health": {
    "beads": {
      "available": true,
      "fresh": false,
      "_explain": {
        "type": "source_degraded",
        "short": "Beads cache 2m stale",
        "code": "EXPLAIN_SOURCE_STALE",
        "evidence": {
          "last_success_at": "2026-03-22T03:28:00Z",
          "stale_threshold_ms": 60000,
          "current_age_ms": 120000,
          "last_error": null
        },
        "drill_down": {
          "label": "Force refresh",
          "command": "br doctor --repair"
        }
      }
    }
  }
}
```

### 7.2 Unavailable Source

When a source is unavailable:

```json
{
  "source_health": {
    "mail": {
      "available": false,
      "_explain": {
        "type": "source_unavailable",
        "short": "Agent Mail server not running",
        "code": "EXPLAIN_SOURCE_UNAVAILABLE",
        "evidence": {
          "last_success_at": "2026-03-22T02:00:00Z",
          "last_error": "Connection refused",
          "last_error_at": "2026-03-22T03:30:00Z",
          "retry_count": 5,
          "next_retry_at": "2026-03-22T03:31:00Z"
        },
        "drill_down": {
          "label": "Start Agent Mail",
          "command": "mcp-agent-mail serve-http"
        }
      }
    }
  }
}
```

---

## 8. Incident Promotion Explanations

### 8.1 Promotion Evidence

When alerts escalate to incidents:

```json
{
  "incident": {
    "id": "inc-20260322-abc",
    "severity": "P1",
    "type": "agent_crashed",
    "_explain": {
      "type": "escalation",
      "short": "Promoted from alert after 30m",
      "code": "EXPLAIN_ESCALATION_DURATION",
      "evidence": {
        "trigger": "duration_exceeded",
        "source_alert": "alert:ntm/alert-xyz",
        "alert_created_at": "2026-03-22T03:00:00Z",
        "promotion_at": "2026-03-22T03:30:00Z",
        "duration_ms": 1800000,
        "threshold_ms": 1800000,
        "original_severity": "error",
        "promoted_severity": "P1",
        "promotion_rule": "default_promotion_30m"
      },
      "drill_down": {
        "label": "View promotion rules",
        "command": "ntm config get alerts.promotion_rules"
      },
      "related_refs": ["alert:ntm/alert-xyz"]
    }
  }
}
```

### 8.2 Multiple Promotion Triggers

When multiple conditions triggered promotion:

```json
{
  "_explain": {
    "type": "escalation",
    "short": "Promoted: critical + affects 4 agents",
    "code": "EXPLAIN_ESCALATION_MULTIPLE",
    "evidence": {
      "triggers": [
        { "type": "severity", "value": "critical", "threshold": "critical" },
        { "type": "scope", "value": 4, "threshold": 3 }
      ],
      "primary_trigger": "severity"
    }
  }
}
```

---

## 9. Attention Prioritization Explanations

### 9.1 Actionability Explanation

When items are classified for actionability:

```json
{
  "attention_item": {
    "cursor": "evt_123",
    "actionability": "action_required",
    "_explain": {
      "type": "prioritization",
      "short": "Requires action: error severity",
      "code": "EXPLAIN_PRIORITY_SEVERITY",
      "evidence": {
        "factors": {
          "severity": "error",
          "auto_clears": false,
          "duration_ms": 600000
        },
        "classification_rule": "error_no_autoclear"
      }
    }
  }
}
```

### 9.2 Ordering Explanation

When items are reordered:

```json
{
  "attention_items": [...],
  "_ordering_explain": {
    "type": "prioritization",
    "short": "Ordered by actionability, then severity, then recency",
    "code": "EXPLAIN_PRIORITY_COMPOSITE",
    "evidence": {
      "sort_keys": [
        { "field": "actionability", "direction": "desc", "weight": 1.0 },
        { "field": "severity", "direction": "desc", "weight": 0.5 },
        { "field": "timestamp", "direction": "desc", "weight": 0.2 }
      ]
    }
  }
}
```

---

## 10. Go Implementation

### 10.1 Explanation Types

```go
package robot

// ExplanationType categorizes explanations
type ExplanationType string

const (
    ExplainBudgetLimit      ExplanationType = "budget_limit"
    ExplainTimeLimit        ExplanationType = "time_limit"
    ExplainSourceDegraded   ExplanationType = "source_degraded"
    ExplainSourceUnavailable ExplanationType = "source_unavailable"
    ExplainPrioritization   ExplanationType = "prioritization"
    ExplainEscalation       ExplanationType = "escalation"
    ExplainRedaction        ExplanationType = "redaction"
    ExplainAggregation      ExplanationType = "aggregation"
    ExplainFilterApplied    ExplanationType = "filter_applied"
    ExplainCursorGap        ExplanationType = "cursor_gap"
    ExplainPolicyApplied    ExplanationType = "policy_applied"
)

// Explanation provides operational clarity for a decision
type Explanation struct {
    Type        ExplanationType        `json:"type"`
    Short       string                 `json:"short"`
    Code        string                 `json:"code"`
    Detail      string                 `json:"detail,omitempty"`
    Evidence    map[string]interface{} `json:"evidence,omitempty"`
    DrillDown   *DrillDown             `json:"drill_down,omitempty"`
    RelatedRefs []string               `json:"related_refs,omitempty"`
}

// DrillDown links to deeper investigation
type DrillDown struct {
    Label   string   `json:"label"`
    Command string   `json:"command"`
    Returns string   `json:"returns,omitempty"`
    Depth   string   `json:"depth,omitempty"` // status, inspect, diagnose
    Refs    []string `json:"refs,omitempty"`
}

// ExplanationLevel controls verbosity
type ExplanationLevel string

const (
    ExplainNone     ExplanationLevel = "none"
    ExplainShort    ExplanationLevel = "short"
    ExplainStandard ExplanationLevel = "standard"
    ExplainFull     ExplanationLevel = "full"
    ExplainVerbose  ExplanationLevel = "verbose"
)
```

### 10.2 Explanation Builders

```go
// BudgetExceededExplanation creates a truncation explanation
func BudgetExceededExplanation(returned, total int, budgetTokens int) *Explanation {
    return &Explanation{
        Type:  ExplainBudgetLimit,
        Short: fmt.Sprintf("%d of %d items returned", returned, total),
        Code:  "EXPLAIN_BUDGET_EXCEEDED",
        Detail: fmt.Sprintf("Payload budget of %d tokens reached at item %d. Items %d-%d omitted.",
            budgetTokens, returned, returned+1, total),
        Evidence: map[string]interface{}{
            "budget_tokens":  budgetTokens,
            "items_returned": returned,
            "items_total":    total,
            "items_omitted":  total - returned,
        },
    }
}

// SourceDegradedExplanation creates a source health explanation
func SourceDegradedExplanation(source string, ageMs int64, lastError string) *Explanation {
    return &Explanation{
        Type:  ExplainSourceDegraded,
        Short: fmt.Sprintf("%s cache %s stale", source, formatDuration(ageMs)),
        Code:  "EXPLAIN_SOURCE_STALE",
        Evidence: map[string]interface{}{
            "source":     source,
            "age_ms":     ageMs,
            "last_error": lastError,
        },
    }
}

// EscalationExplanation creates a promotion explanation
func EscalationExplanation(trigger string, sourceAlert string, durationMs int64) *Explanation {
    return &Explanation{
        Type:  ExplainEscalation,
        Short: fmt.Sprintf("Promoted from alert after %s", formatDuration(durationMs)),
        Code:  "EXPLAIN_ESCALATION_DURATION",
        Evidence: map[string]interface{}{
            "trigger":       trigger,
            "source_alert":  sourceAlert,
            "duration_ms":   durationMs,
        },
        RelatedRefs: []string{sourceAlert},
    }
}
```

### 10.3 Embedding Explanations

```go
// Explainable is an interface for types that can carry explanations
type Explainable interface {
    SetExplanation(e *Explanation)
    GetExplanation() *Explanation
}

// ExplainableSection is a section with optional explanation
type ExplainableSection struct {
    Explain *Explanation `json:"_explain,omitempty"`
}

func (s *ExplainableSection) SetExplanation(e *Explanation) {
    s.Explain = e
}

func (s *ExplainableSection) GetExplanation() *Explanation {
    return s.Explain
}

// AddExplanation conditionally adds explanation based on level
func AddExplanation(target Explainable, e *Explanation, level ExplanationLevel) {
    if level == ExplainNone {
        return
    }

    // Filter fields based on level
    filtered := filterExplanation(e, level)
    target.SetExplanation(filtered)
}

func filterExplanation(e *Explanation, level ExplanationLevel) *Explanation {
    if e == nil {
        return nil
    }

    result := &Explanation{
        Type:  e.Type,
        Short: e.Short,
        Code:  e.Code,
    }

    if level >= ExplainStandard {
        result.Evidence = e.Evidence
    }

    if level >= ExplainFull {
        result.Detail = e.Detail
        result.RelatedRefs = e.RelatedRefs
    }

    if level >= ExplainVerbose {
        result.DrillDown = e.DrillDown
    }

    return result
}
```

---

## 11. Transport Consistency

### 11.1 JSON

Explanations appear as `_explain` fields:

```json
{
  "sessions": [...],
  "_explain": { "type": "budget_limit", ... }
}
```

### 11.2 TOON

Explanations render as annotations:

```
SESSIONS (20 of 47, truncated)  # EXPLAIN_BUDGET_EXCEEDED
├─ myproject (3 agents)
└─ ... 27 more (ntm --robot-status --limit=0)
```

### 11.3 Markdown

Explanations render as footnotes:

```markdown
## Sessions (20 of 47)

| Session | Agents | Status |
|---------|--------|--------|
| myproject | 3 | attached |

> **Note:** 27 sessions omitted due to payload budget.
> Run `ntm --robot-status --limit=0` for full list.
```

---

## 12. Non-Goals

1. **Debug tracing** — This is operational explainability, not OTLP
2. **Per-field provenance** — Explains sections, not individual fields
3. **AI rationalization** — Explanations are mechanical, not heuristic
4. **Full audit trails** — See bd-j9jo3.1.15 for audit semantics

---

## 13. References

- [Robot Surface Taxonomy](robot-surface-taxonomy.md) — Lane definitions
- [Robot Action Errors](robot-action-errors.md) — Recovery semantics
- [Robot Ordering Pagination](robot-ordering-pagination.md) — Truncation rules
- [Robot Resource References](robot-resource-references.md) — Entity references

---

## Appendix: Changelog

- **2026-03-22:** Initial explainability contract (bd-j9jo3.1.8)

---

*Reference: bd-j9jo3.1.8*
