# Context exhaustion forecasts

The resident coordinator now uses its existing context-rotation observation
pass to forecast exhaustion from recent per-pane context occupancy. This is an
advisory warning, not a new rotation trigger. Enable the existing checker with
a positive `[rotation] usage_percent_threshold`; no new configuration or
background service is required. A disabled checker does not collect forecasts.

The coordinator appends `alert.warning` events to the shared attention feed with
`reason_code: "context_exhaustion_forecast"` and
`source: "coordinator.context_forecast"`. The ordinary attention/event readers
can consume them. Events are persisted when the shared feed has a durable store;
the forecasting history itself is resident-local and starts empty after restart.

## Meaning of a warning

A warning means the observed recent growth would exhaust the reported context
capacity within the forecast horizon **if that growth continues**. It is not a
guarantee: workloads, the model's effective capacity, and automatic compaction
can change. An operator can inspect the pane and prepare a handoff before the
ordinary high-occupancy rotation threshold is reached.

The existing velocity predictor is used, rather than a second prediction engine.
It computes growth between the first and last samples in a five-minute window.
The observer admits at most one sample per 30 seconds, requires at least three
samples spanning one minute, and emits:

- `warning`: usage above 70% and positive estimated time remaining below 15 minutes.
- `urgent`: usage above 75% and positive estimated time remaining below 8 minutes.

Both levels remain warning-severity advisories. The first warning and an urgency
increase are emitted promptly; otherwise a continuing episode is reminded no
more frequently than once per five-minute window. Compaction or a confirmed
stable period re-arms a subsequent warning. Occupancy at or beyond the capacity
is handled by existing occupancy monitoring, not represented as a positive ETA.

## Evidence and attribution

Readings reuse the coordinator's existing transcript attribution and Oh My Pi
status-bar gauge. Per-process transcript bindings take precedence. Directory
fallback is accepted only for an unambiguous pane of that agent type. Several
same-type panes sharing a directory do not all inherit its newest transcript.
Forecasting does not add another discovery scan or terminal capture.

Events include the physical pane ID, window/pane index, root PID, agent name/type,
model, usage source, capacity source, current tokens, usage percentage,
`tokens_per_minute`, `minutes_to_exhaustion`, sample count, window duration, and
observation timestamp. `advisory` is always `true`. A model-registry capacity is
labelled `model_registry`, distinct from a transcript- or status-bar-reported
capacity. A registry capacity is accepted only for a recognized model, including
configured overrides and provider-qualified names. An unknown model without a
positive native window has unknown capacity and produces no exhaustion forecast
or automatic rotation; the generic display fallback does not authorize either.
Negative native counters or windows are invalid. Existing freshness policies
remain separate: growth forecasts require recent observations, while an idle
process-bound transcript can retain usable occupancy for rotation.

Only live, non-service, non-shell panes with an observed PID are sampled. A
cached timestamp is not a new observation. Missing, ambiguous, stale (older than
one minute), future, regressing, or conflicting observations invalidate history.
A changed process identity, title, agent type, model, transcript, or capacity,
a decrease in occupancy, or a gap longer than the prediction window starts a
new baseline. Unobserved panes are pruned. The root PID is a current identity
signal, not historical PID-incarnation proof.

Transcript timestamps currently come from file modification times. They do not
prove when a particular token-bearing record was written. The forecast is a
trend in observed occupancy, not an exact token-arrival-rate measurement.

## Safety boundary

Forecasts never enqueue, confirm, compact, interrupt, send input, or replace a
pane. This remains true when `rotation.auto_confirm` is enabled and while an
agent is actively working. Existing occupancy, token-count, and session-age
rotation triggers and their mandatory fire-time safety gates are unchanged.

## Tests

`internal/context/forecast_test.go` covers prediction arithmetic, sampling,
reset boundaries, stale/future/cached data, escalation/reminders, pruning, and
concurrent observer use. `internal/coordinator/context_forecast_test.go` exercises
the real coordinator tick and event envelope with injected terminal/transcript
observations, including shared-directory attribution and non-actuation.
