# Robot Watch and Subscription Semantics

This document defines watch, subscription, heartbeat, and event-taxonomy semantics for ntm robot mode streaming surfaces.

## Overview

Watch mode enables long-lived consumption of robot events. Unlike one-shot JSON responses, watch streams must communicate liveness, event significance, idle periods, and subscription scope. This contract ensures consumers can distinguish between "nothing happened" and "connection broken."

## Event Taxonomy

### Event Classes

Events are classified by durability and significance:

```
┌──────────────────────────────────────────────────────────────┐
│                      DURABLE EVENTS                          │
│  (persisted, replayable, affect state)                       │
├──────────────────────────────────────────────────────────────┤
│  attention    Alert/incident/work requiring action           │
│  state        Agent/session/quota state change               │
│  action       Operator or agent performed action             │
│  lifecycle    Session/agent started/stopped/crashed          │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│                     EPHEMERAL EVENTS                         │
│  (not persisted, live-only, informational)                   │
├──────────────────────────────────────────────────────────────┤
│  heartbeat    Connection liveness signal                     │
│  progress     Source collection/sync progress                │
│  typing       Agent actively producing output                │
│  metric       Real-time counter/gauge update                 │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│                      CONTROL EVENTS                          │
│  (stream protocol, not content)                              │
├──────────────────────────────────────────────────────────────┤
│  connected    Stream established                             │
│  replay_start Replay from cursor beginning                   │
│  replay_end   Replay complete, switching to live             │
│  filtered     Event matched but excluded by subscription     │
│  bounded      Stream reached limit, closing                  │
│  backpressure Consumer too slow, events dropped              │
│  error        Protocol or source error                       │
│  disconnected Stream closing (reason provided)               │
└──────────────────────────────────────────────────────────────┘
```

### Event Structure

All events share this envelope:

```json
{
  "event_id": "evt_20260322040000_x7k",
  "event_class": "attention",
  "event_type": "alert:agent_stuck",
  "timestamp": "2026-03-22T04:00:00.123Z",
  "sequence": 12345,
  "severity": "warning",
  "reason_code": "alert:agent:stuck",
  "source": "pane-abc123",
  "session": "main",
  "durable": true,
  "payload": { ... },
  "correlation_id": "corr_20260322035900_abc",
  "explanation": { ... }
}
```

### Event Type Hierarchy

Types follow domain:category:specific format:

| Domain | Categories | Examples |
|--------|-----------|----------|
| `attention` | `alert`, `incident`, `work` | `attention:alert:agent_stuck`, `attention:incident:opened` |
| `state` | `agent`, `session`, `quota`, `source` | `state:agent:idle`, `state:quota:warning` |
| `action` | `operator`, `agent`, `system` | `action:operator:ack`, `action:agent:claim` |
| `lifecycle` | `session`, `agent`, `pane` | `lifecycle:agent:spawned`, `lifecycle:session:ended` |
| `progress` | `collector`, `sync`, `replay` | `progress:collector:quota`, `progress:replay:complete` |
| `control` | `heartbeat`, `filtered`, `bounded` | `control:heartbeat`, `control:replay_end` |

## Heartbeat Semantics

Heartbeats signal stream liveness when no material events occur.

### Heartbeat Properties

```json
{
  "event_class": "control",
  "event_type": "control:heartbeat",
  "timestamp": "2026-03-22T04:00:00Z",
  "sequence": 12346,
  "durable": false,
  "payload": {
    "stream_id": "watch_20260322040000_x7k",
    "uptime_ms": 60000,
    "events_since_start": 45,
    "sources_healthy": 4,
    "sources_degraded": 1,
    "next_heartbeat_ms": 5000
  }
}
```

### Heartbeat Cadence

| Condition | Interval | Rationale |
|-----------|----------|-----------|
| Default idle | 5s | Responsive liveness check |
| High activity | 30s | Events themselves prove liveness |
| Recovery mode | 1s | Fast detection during reconnect |
| Degraded sources | 5s | Frequent status updates |

### Idle vs Stuck Interpretation

Consumers interpret missing heartbeats:

| Condition | Interpretation | Consumer Action |
|-----------|---------------|-----------------|
| Heartbeat received on schedule | Stream healthy | Continue |
| Heartbeat late (1.5x interval) | Possible network lag | Wait |
| Heartbeat missing (3x interval) | Stream stuck or dead | Reconnect |
| Heartbeat with `sources_degraded > 0` | Partial data | Check source health |
| No events but heartbeat healthy | Nothing happening | Normal idle |

### Heartbeat Payload Contract

```json
{
  "heartbeat": {
    "stream_id": "watch_20260322040000_x7k",
    "uptime_ms": 60000,
    "events_since_start": 45,
    "events_since_last_heartbeat": 0,
    "sources_healthy": 4,
    "sources_degraded": 1,
    "sources_unavailable": 0,
    "degraded_reasons": ["quota:rate_limited"],
    "cursor_position": "cur_20260322040000",
    "next_heartbeat_ms": 5000,
    "subscription_active": true,
    "filtered_since_last": 3
  }
}
```

## Subscription Selectors

Consumers subscribe to specific event slices.

### Selector Types

| Selector | Syntax | Example |
|----------|--------|---------|
| `event_class` | Exact match | `attention`, `state` |
| `event_type` | Prefix match | `attention:alert:*`, `state:agent:*` |
| `severity` | Minimum level | `severity>=warning` |
| `source` | Glob pattern | `pane-abc*`, `agent:PeachPond` |
| `session` | Exact match | `session:main` |
| `agent` | Exact match | `agent:PeachPond` |
| `incident` | Exact or active | `incident:inc-20260322-abc`, `incident:active` |
| `reason_code` | Prefix match | `alert:agent:*`, `quota:*` |
| `durable` | Boolean | `durable:true` |

### Subscription Request

```json
{
  "action": "subscribe",
  "request_id": "req_20260322040000_x7k",
  "subscription": {
    "id": "sub_20260322040000_abc",
    "selectors": [
      {"event_class": "attention"},
      {"event_type": "state:agent:*"},
      {"severity": ">=warning"}
    ],
    "combine": "or",
    "exclude": [
      {"event_type": "control:heartbeat"}
    ],
    "options": {
      "include_heartbeats": true,
      "heartbeat_interval_ms": 5000,
      "replay_from": "cursor:cur_20260322035000",
      "bounded": false,
      "limit": null,
      "backpressure_policy": "drop_oldest"
    }
  }
}
```

### Selector Validation

| Condition | Response |
|-----------|----------|
| Valid selector | `subscription_active` |
| Unknown selector field | Error: `SELECTOR_UNKNOWN` |
| Malformed pattern | Error: `SELECTOR_INVALID` |
| Too broad (matches all) | Warning: `SELECTOR_BROAD` |
| No matches possible | Warning: `SELECTOR_EMPTY` |
| Conflicting selectors | Error: `SELECTOR_CONFLICT` |

### Subscription Acknowledgment

```json
{
  "event_class": "control",
  "event_type": "control:subscribed",
  "payload": {
    "subscription_id": "sub_20260322040000_abc",
    "selectors_active": 3,
    "warnings": ["SELECTOR_BROAD: event_class=attention matches ~40% of events"],
    "replay_from": "cur_20260322035000",
    "replay_events_pending": 127,
    "estimated_events_per_minute": 15
  }
}
```

## Replay-to-Live Handoff

Streams replay historical events before switching to live.

### Replay Sequence

```
┌─────────────┐
│  connected  │ Stream established
└──────┬──────┘
       │
┌──────▼──────┐
│ replay_start│ Beginning historical replay
└──────┬──────┘
       │
   ┌───┴───┐
   │ events│ Historical events (sequence ordered)
   └───┬───┘
       │
┌──────▼──────┐
│  replay_end │ Caught up to live
└──────┬──────┘
       │
   ┌───┴───┐
   │ events│ Live events (real-time)
   └───┬───┘
       │
       ▼
  (heartbeats during idle)
```

### Replay Start Event

```json
{
  "event_class": "control",
  "event_type": "control:replay_start",
  "payload": {
    "replay_from": "cur_20260322035000",
    "replay_to": "cur_20260322040000",
    "events_pending": 127,
    "estimated_duration_ms": 500
  }
}
```

### Replay End Event

```json
{
  "event_class": "control",
  "event_type": "control:replay_end",
  "payload": {
    "replayed_count": 127,
    "replay_duration_ms": 423,
    "gaps_detected": 0,
    "now_live": true,
    "cursor_position": "cur_20260322040000"
  }
}
```

### Event Markers

During replay and live, events include:

```json
{
  "event_id": "evt_20260322035500_abc",
  "replay": true,
  "replay_sequence": 45,
  "live": false
}
```

After replay_end:

```json
{
  "event_id": "evt_20260322040001_xyz",
  "replay": false,
  "live": true
}
```

## Backpressure and Boundedness

### Bounded Streams

Streams can be bounded by count or time:

```json
{
  "subscription": {
    "options": {
      "bounded": true,
      "limit": 100,
      "timeout_ms": 60000
    }
  }
}
```

### Bounded Event

When limit reached:

```json
{
  "event_class": "control",
  "event_type": "control:bounded",
  "payload": {
    "reason": "limit_reached",
    "limit": 100,
    "delivered": 100,
    "remaining": 47,
    "cursor_position": "cur_20260322040030"
  }
}
```

### Backpressure Policies

When consumer falls behind:

| Policy | Behavior |
|--------|----------|
| `drop_oldest` | Drop oldest undelivered events |
| `drop_newest` | Drop incoming events |
| `pause_source` | Stop collecting until caught up |
| `error` | Disconnect with backpressure error |

### Backpressure Event

```json
{
  "event_class": "control",
  "event_type": "control:backpressure",
  "payload": {
    "policy": "drop_oldest",
    "dropped_count": 15,
    "queue_depth": 1000,
    "consumer_lag_ms": 5000,
    "recommendation": "increase_rate_or_filter"
  }
}
```

## Source Progress Events

Progress events report collector/source status without being a telemetry firehose.

### Progress Event Structure

```json
{
  "event_class": "progress",
  "event_type": "progress:collector:quota",
  "durable": false,
  "payload": {
    "source": "caut",
    "phase": "collecting",
    "progress_pct": 75,
    "items_processed": 3,
    "items_total": 4,
    "started_at": "2026-03-22T04:00:00Z",
    "eta_ms": 500
  }
}
```

### Progress Event Types

| Type | When Emitted |
|------|--------------|
| `progress:collector:*` | Source adapter collecting data |
| `progress:sync:*` | Beads/handoff syncing |
| `progress:replay:*` | Historical replay progress |
| `progress:compaction:*` | Log/journal compaction |
| `progress:rotation:*` | Log rotation |

### Progress Visibility

Progress events are opt-in:

```json
{
  "subscription": {
    "selectors": [
      {"event_class": "progress"}
    ],
    "options": {
      "progress_interval_ms": 1000
    }
  }
}
```

## Transport Consistency

Watch semantics are consistent across transports.

### CLI

A dedicated `ntm watch --robot --filter` subscription flag set is design, not
shipped; the shipped CLI consumption pattern is cursor-based polling of the
attention feed (SSE streaming is served by `ntm serve`):

```bash
# Replay/poll attention events from a cursor, filtered by profile
ntm --robot-events --since-cursor=42 --profile=alerts

# Block until something needs attention, then return a digest
ntm --robot-attention --attention-cursor=42

# Designed subscription stream output format (JSON lines)
{"event_class":"control","event_type":"control:connected",...}
{"event_class":"control","event_type":"control:replay_start",...}
{"event_class":"attention","event_type":"attention:alert:agent_stuck",...}
{"event_class":"control","event_type":"control:replay_end",...}
{"event_class":"control","event_type":"control:heartbeat",...}
```

### REST (SSE)

```http
GET /api/robot/watch?filter=event_class:attention&filter=severity:>=warning HTTP/1.1
Accept: text/event-stream

event: connected
data: {"stream_id":"watch_20260322040000_x7k",...}

event: attention
data: {"event_id":"evt_20260322040001_abc",...}

event: heartbeat
data: {"uptime_ms":60000,...}
```

### WebSocket

```json
// Client -> Server: Subscribe
{"action":"subscribe","subscription":{"selectors":[{"event_class":"attention"}]}}

// Server -> Client: Events
{"event_class":"control","event_type":"control:subscribed",...}
{"event_class":"attention","event_type":"attention:alert:agent_stuck",...}
{"event_class":"control","event_type":"control:heartbeat",...}
```

### Transport Feature Matrix

| Feature | CLI | SSE | WebSocket |
|---------|-----|-----|-----------|
| Subscribe | Flags | Query params | Message |
| Resubscribe | Restart | Reconnect | Message |
| Heartbeats | JSON line | SSE event | JSON message |
| Backpressure | Pipe buffer | HTTP/2 flow | WS flow control |
| Reconnect | Manual | EventSource auto | Manual |
| Cursor resume | `--cursor` | `cursor` param | Message field |

## Error Events

### Error Structure

```json
{
  "event_class": "control",
  "event_type": "control:error",
  "payload": {
    "code": "SOURCE_UNAVAILABLE",
    "message": "Quota source not responding",
    "source": "caut",
    "recoverable": true,
    "retry_after_ms": 5000,
    "degraded_mode": true
  }
}
```

### Error Codes

| Code | Meaning | Recoverable |
|------|---------|-------------|
| `SOURCE_UNAVAILABLE` | Source adapter failed | Yes |
| `SOURCE_STALE` | Source data too old | Yes |
| `CURSOR_INVALID` | Requested cursor not found | No |
| `CURSOR_EXPIRED` | Cursor beyond retention | No |
| `SUBSCRIPTION_INVALID` | Malformed subscription | No |
| `BACKPRESSURE_OVERFLOW` | Consumer too slow | Depends |
| `STREAM_TIMEOUT` | No activity, closing | Yes |
| `AUTH_EXPIRED` | Token expired | Yes |
| `INTERNAL_ERROR` | Unexpected failure | Maybe |

## Disconnection

### Disconnection Event

```json
{
  "event_class": "control",
  "event_type": "control:disconnected",
  "payload": {
    "reason": "timeout",
    "code": "STREAM_TIMEOUT",
    "cursor_position": "cur_20260322040500",
    "resume_supported": true,
    "uptime_ms": 300000,
    "events_delivered": 156
  }
}
```

### Disconnection Reasons

| Reason | Code | Resume |
|--------|------|--------|
| `timeout` | `STREAM_TIMEOUT` | Yes |
| `client_close` | `CLIENT_CLOSE` | Yes |
| `server_shutdown` | `SERVER_SHUTDOWN` | Yes |
| `auth_expired` | `AUTH_EXPIRED` | Yes (re-auth) |
| `backpressure` | `BACKPRESSURE_OVERFLOW` | Yes |
| `error` | `INTERNAL_ERROR` | Maybe |
| `bounded` | `BOUNDED_COMPLETE` | No (done) |

## Integration with Other Contracts

### Identity Contract

Events use stable resource references:

```json
{
  "event_type": "attention:alert:agent_stuck",
  "ref": "alert:agent_stuck:pane-abc123:fp-20260322040000"
}
```

### Explainability Contract

Events include explanation when actionable:

```json
{
  "explanation": {
    "type": "attention_alert",
    "short": "Agent stuck for 5 minutes",
    "code": "EXPLAIN_AGENT_STUCK",
    "evidence": {...}
  }
}
```

### Attention State Contract

Events reflect attention state transitions:

```json
{
  "event_type": "action:operator:ack",
  "payload": {
    "ref": "alert:agent_stuck:pane-abc123",
    "previous_state": "new",
    "new_state": "acknowledged"
  }
}
```

### Request Identity Contract

Watch requests use correlation:

```json
{
  "action": "subscribe",
  "request_id": "req_20260322040000_x7k",
  "correlation_id": "corr_20260322035900_abc"
}
```

## Design Rationale

1. **Liveness is explicit**: Heartbeats prove the stream is alive, not just quiet
2. **Events are classified**: Durable vs ephemeral distinction prevents replay confusion
3. **Subscriptions are narrow**: Consumers request exactly what they need
4. **Replay is marked**: Consumers know which events are historical
5. **Backpressure is surfaced**: Consumers know when they're falling behind
6. **Progress is opt-in**: Source status available without noise
7. **Transport agnostic**: Same semantics everywhere
