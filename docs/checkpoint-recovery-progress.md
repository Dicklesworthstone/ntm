# Live checkpoint recovery evidence

Persistent `POST /api/v1/jobs` checkpoint-restore jobs publish live evidence at
`GET /api/v1/jobs/{id}` under `result.restore_progress`. No new request flags are
required. An `operation_id` records the same evidence in its durable receipt, so
retries can return the original outcome without running recovery again.

The shared checkpoint restorer records a `before` boundary and an `after`
outcome around stopping an existing session, creating the replacement session,
creating each additional pane, launching each agent, and delivering each context
message. Initial-pane identification and verified process startup are separate
observations. Evidence includes the checkpoint ID, immutable source session,
actual destination session, working directory, sequence, time, and physical pane
IDs. Commands, launch arguments, and scrollback are not copied into these events.

The cumulative view contains:

- `previous_session_stopped` and `session_created`: successful calls observed by
  the restorer. False means unconfirmed, not proof a session exists or is absent.
- `created_pane_ids` and `pane_bindings`: observed physical pane IDs mapped to
  their saved pane IDs, window/index positions, and saved agent types. A newly
  created session can be known before its initial pane ID has been read back.
- `launch_accepted_pane_ids`, `startup_verified_pane_ids`, and
  `context_injected_pane_ids`: distinct, deduplicated evidence. Launch acceptance
  is not proof of startup, readiness for work, or completion of an agent task.

`last_event.phase=before` is an uncertain boundary: a crash can occur on either
side of the action. An `after` event with `outcome=uncertain` means the call failed
without proving whether external effects occurred. Neither permits blind replay.
Even `after/succeeded` is evidence of an acknowledged call, not an exactly-once
execution or authorization guarantee. Inspect the named session/panes and saved
pipeline or assignment state before starting a new operation after interruption.

Journal writes complete before the next material action. A write failure stops
the restore and retains its error cause; the partially restored session is left
for inspection, not destroyed or automatically recreated. Recipient identity is
rechecked after the pre-delivery write, before submitting saved context.
Cancellation does not discard an already-finished action's evidence. Job status
and the restorer's final result remain authoritative for terminal completion;
normal journal recovery still marks interrupted execution as outcome unknown.

Dry runs do not publish execution events or mutate tmux. Direct CLI and
synchronous REST restores do not attach a journal observer. Embedders may opt into
this same observer with `checkpoint.WithRestoreProgress`; it receives the actual
execution context, including downstream journal hooks, and is synchronous,
ordered, and scoped to one restore. A failing or panicking observer stops further
actions. It must not reenter its own restore callback.

## Context delivery waits for the agent UI

Every restore that requests context injection, including CLI and synchronous
REST, waits for two separately captured, fresh, confidently idle observations
of the exact created pane. Only its visible screen is classified, not historical
scrollback or a sibling pane's activity. The shared status detector supplies the
idle classification; last-known state and display-only idle heuristics do not
authorize delivery. Known interactive gates, existing composer drafts, queued
messages, and omp completion lists withhold context instead of consuming input.

Each recipient has a 30-second readiness budget, with 200-millisecond polling;
an earlier caller cancellation or deadline wins. Missing or ambiguous panes,
changed agent identity, shell/dead/service panes, and capture failures refuse
without sending. The readiness check runs after the pre-delivery journal flush,
and physical identity is checked again immediately before the existing send.

Waiting never clears a draft, dismisses a dialog, sends an interrupt, or launches
the agent again. Failure returns `ErrRestoreContextNotReady` with the pane and
reason, retains already-completed deliveries, and leaves the restored session
available for inspection. Dry runs do not wait. These observations are not an
atomic lock on the UI: an independent user or process can still change a pane
between observation and input. A completed send is not proof the agent finished
processing the context.
