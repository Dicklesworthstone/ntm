# Robot Redesign Transition Guide

> **Overview of the robot subsystem redesign and how to extend it safely.**
> This document covers the unified section model, attention feed, consumer metadata, and test entry points.

**Status:** CURRENT
**Bead:** bd-j9jo3.9.4
**Created:** 2026-03-26

---

## 1. Design Philosophy

The robot subsystem redesign follows two core principles from AGENTS.md:

1. **No backwards compatibility** - We're in early development with no external users. We do things the RIGHT way with NO TECH DEBT.
2. **Pure Go** - All tooling is Go-only. No external build systems, no shell scripts for core functionality.

The LLM is the driver; ntm is the nervous system. The robot API provides:
- **Sensing:** observe agent states, session health, alerts, beads, mail
- **Actuation:** spawn, send, interrupt, wait, route

The robot API does NOT provide planning, scheduling, or a second source of truth.

---

## 2. Architecture Overview

The redesign introduces a unified model where all output formats project from the same underlying data:

```
                    ┌─────────────────────────────────────────┐
                    │           RobotRegistry                 │
                    │  (surfaces, sections, schema bindings)  │
                    └───────────────────┬─────────────────────┘
                                        │
                    ┌───────────────────▼─────────────────────┐
                    │           SnapshotOutput                │
                    │    (unified state from all sources)     │
                    └───────────────────┬─────────────────────┘
                                        │
                    ┌───────────────────▼─────────────────────┐
                    │         SectionProjection               │
                    │  (sections with truncation/omission)    │
                    └───────────────────┬─────────────────────┘
                                        │
              ┌─────────────────┬───────┴───────┬─────────────────┐
              ▼                 ▼               ▼                 ▼
          ┌───────┐        ┌────────┐      ┌────────┐        ┌────────┐
          │ JSON  │        │  TOON  │      │ Terse  │        │Markdown│
          └───────┘        └────────┘      └────────┘        └────────┘
```

### Key Components

| Component | File | Purpose |
|-----------|------|---------|
| `RobotRegistry` | `registry.go` | Canonical source for surfaces, sections, consumer metadata |
| `SectionProjection` | `sections.go` | Unified projection model with truncation semantics |
| `AttentionFeed` | `attention_feed.go` | Cursor-based event stream with replay support |
| `AttentionEvent` | `attention_feed.go` | Normalized event envelope for all attention events |
| `ConsumerGuidance` | `registry.go` | Machine-readable hints for API consumers |

---

## 3. Consumer Metadata

The redesign adds rich metadata for machine consumers at both surface and section levels:

### Surface-Level Metadata

```go
type RobotSurfaceDescriptor struct {
    // ... basic fields ...
    ConsumerGuidance *ConsumerGuidance   // How to interpret this surface
    Boundedness      *BoundednessInfo    // Limits, pagination, truncation
    FollowUp         *FollowUpInfo       // Drill-down paths, related surfaces
    ActionHandoff    *ActionHandoffInfo  // Structured action hints
    RequestSemantics *RequestSemantics   // Idempotency, correlation
    AttentionOps     *AttentionOpsInfo   // ack/snooze/pin/escalate support
    Explainability   *ExplainabilityInfo // Diagnostic entry points
    Lifecycle        *LifecycleInfo      // stable/beta/deprecated status
}
```

### Key Metadata Types

| Type | Purpose |
|------|---------|
| `ConsumerGuidance` | Intended use, polling recommendations, summary hints |
| `BoundednessInfo` | Default/max limits, pagination support, payload budget |
| `FollowUpInfo` | Inspect surfaces for drill-down, related surfaces |
| `ActionHandoffInfo` | Whether surface emits action hints, action types |
| `RequestSemantics` | Idempotency keys, correlation IDs |
| `AttentionOpsInfo` | ack/snooze/pin/escalate support flags |
| `ExplainabilityInfo` | Diagnostic handles, evidence summaries, aggregation cues |

---

## 4. Section Projection Model

All renderers project from `SectionProjection` which provides consistent truncation and omission semantics.

### Section Types

| Section | Scope | Purpose |
|---------|-------|---------|
| `summary` | Global | System-wide counts and health |
| `sessions` | Global | Session inventory with agent details |
| `work` | Global | Bead work items by state |
| `alerts` | Global | Active alerts with severity |
| `attention` | Global | Prioritized attention events |
| `coordination` | Global | Agent mail and conflicts |
| `health` | Global | System component health |

### Truncation Semantics

When a section exceeds limits:
- `TruncatedCount` reports items omitted
- `Reason` explains why (limit, token_budget, relevance, staleness)
- `ResumptionHint` provides how to fetch more

### Limit Tiers

```go
TerseSectionLimits()    // Minimal: 5 sessions, 3 alerts
CompactSectionLimits()  // Moderate: 10 sessions, 5 alerts
DefaultSectionLimits()  // Standard: 20 sessions, 10 alerts
DashboardSectionLimits() // TUI: 50 sessions, 50 alerts
```

---

## 5. Attention Feed Contract

The attention feed provides cursor-based event streaming. See `docs/attention-feed-contract.md` for the full contract.

### Operator Loop

```
1. BOOTSTRAP:  --robot-snapshot → baseline state + initial cursor
2. POLL:       --robot-events --cursor=X → new events
3. TRIAGE:     --robot-attention → prioritized items
4. ACT:        --robot-send, --robot-interrupt → take action
5. WAIT:       --robot-wait --condition=idle → block until met
6. REPEAT:     goto POLL
```

### Event Envelope

```json
{
  "cursor": 12345,
  "ts": "2026-03-26T02:30:45.123456Z",
  "session": "myproject",
  "pane": "0.2",
  "category": "agent",
  "type": "state_change",
  "source": "activity_detector",
  "actionability": "interesting",
  "severity": "info",
  "summary": "Agent cc_2 became idle",
  "details": { ... },
  "next_actions": [ ... ]
}
```

### Actionability Classes

| Class | Meaning |
|-------|---------|
| `background` | Informational, no action typically needed |
| `interesting` | Worth noticing but not urgent |
| `action_required` | Demands prompt operator decision |

### Cursor Semantics

- Cursors are monotonically increasing integers
- Cursor 0 means "start from beginning"
- Cursor -1 means "start from now" (no replay)
- If a cursor expires, `CURSOR_EXPIRED` error returned with resync hint

---

## 6. Test Entry Points

### Unit Tests

| Area | Test File | Key Tests |
|------|-----------|-----------|
| Sections | `sections_test.go` | Projection, truncation, limit tiers |
| Registry | `registry_test.go` | Surface/section lookup, metadata |
| Attention Feed | `attention_feed_test.go` | Append, replay, cursor expiration |
| Events | `events_test.go` | Filter resolution, output building |
| Profiles | `attention_profile_test.go` | Filter profiles, threshold matching |
| Operator Loop | `operator_loop_test.go` | Loop iteration, condition matching |

### Integration Tests

```bash
go test ./internal/robot/... -run Integration
```

| Test | Purpose |
|------|---------|
| `TestValidationIntegration*` | Contract validation against fixtures |
| `TestOperatorLoopIntegration*` | Full loop with real feed |

### E2E Tests

```bash
go test ./e2e/... -run Overlay
```

| Test | Purpose |
|------|---------|
| `TestOverlayFeedCursorPropagation*` | Cursor flows through overlay |
| `TestOverlayFeedGracefulDegradation*` | Missing feed handled cleanly |
| `TestOverlayFeedResponseStructure*` | JSON contract validation |

### Golden Fixtures

Located in `internal/robot/testdata/`:
- `golden_snapshot*.json` - Canonical snapshot outputs
- `golden_events*.json` - Event envelope examples
- `golden_attention*.json` - Attention response fixtures

---

## 7. Extending the Redesign

### Adding a New Section

1. Add constant in `sections.go`:
   ```go
   const SectionFoo = "foo"
   ```

2. Add to `SectionOrderWeight`:
   ```go
   SectionOrderWeight[SectionFoo] = 75  // Between health (70) and quota (80)
   ```

3. Implement projection function:
   ```go
   func projectFooSection(snapshot *SnapshotOutput, opts SectionProjectionOptions) ProjectedSection
   ```

4. Add to `ProjectSections()` switch

5. Add format hints in `DefaultSectionFormatHints()`

6. Add tests in `sections_test.go`

### Adding a New Event Type

1. Add category/type constants if needed

2. Emit via `AttentionFeed.Append()`:
   ```go
   feed.Append(AttentionEvent{
       Category:      CategoryFoo,
       Type:          TypeBar,
       Actionability: ActionabilityInteresting,
       Severity:      SeverityInfo,
       Summary:       "Something happened",
       Details:       map[string]any{"key": "value"},
   })
   ```

3. Add next_actions if mechanically derivable

4. Document in `attention-feed-contract.md`

5. Add golden fixture

### Adding Consumer Metadata

1. Add to appropriate descriptor in `buildRobotSurfaceMetadata()`:
   ```go
   "my_surface": {
       ConsumerGuidance: &ConsumerGuidance{
           IntendedUse: "Quick lookup of X",
           PollingRecommendation: "Suitable for polling every 30s",
       },
       Boundedness: &BoundednessInfo{
           DefaultLimit: 20,
           MaxLimit: 100,
           SupportsPagination: true,
       },
   },
   ```

2. Update registry tests

---

## 8. Related Documentation

| Doc | Purpose |
|-----|---------|
| `robot-api-design.md` | API design principles, flag patterns |
| `attention-feed-contract.md` | Full attention feed specification |
| `robot-section-model.md` | Section projection details |
| `robot-schema-versioning.md` | Schema versioning strategy |
| `robot-request-identity.md` | Idempotency and correlation |
| `robot-attention-state.md` | Operator ack/snooze/pin state |
| `robot-explainability-evidence.md` | Diagnostic handles, evidence |
| `robot-contract-examples.md` | Worked examples |

---

## Appendix: Key Bead Trail

| Bead | Description |
|------|-------------|
| bd-j9jo3 | Robot redesign epic |
| bd-j9jo3.6.5 | Section projection model |
| bd-j9jo3.8.2 | Dashboard section alignment |
| br-aa0nj | Attention feed contract |
| br-kpvhy | --robot-events implementation |
| bd-j9jo3.9.4 | Documentation refresh (this doc) |

---

*Reference: bd-j9jo3.9.4*
