# Durable job operation identity

An HTTP request ID identifies an attempt to talk to the server. An operation ID
identifies work that must not run twice. To protect a long-running job across a
lost HTTP response or server restart, include `operation_id` in `params` when
posting to `/api/v1/jobs`:

```json
{
  "type": "swarm_spawn",
  "params": {
    "operation_id": "deploy-42",
    "session": "myproject",
    "cc_count": 4,
    "safety": true,
    "launch_interval": "2s",
    "startup_timeout": "3m"
  }
}
```

The same control works for `pipeline_run`, `pipeline_exec`, `pipeline_resume`,
and `checkpoint_restore`. It is reserved by the shared dispatcher; the remaining
parameters still undergo each operation's strict validation. An explicitly
supplied ID must be a nonempty string of at most 200 bytes, without whitespace or
control characters. Invalid IDs and servers without a persistent state-backed job
journal fail the job **before invoking its engine**. As with other job validation,
inspect the polled job's terminal status, not merely its initial HTTP 202.

## Retry contract

Resubmit the same operation ID and the same request after a transport failure.
A retry can have a different HTTP/job ID. It never launches another copy of the
work once the original operation has a durable execution marker.

- A completed operation replays its original result.
- A failed or cancelled operation replays its original error and any partial
  result. Failure is not evidence that nothing happened; it is not automatically
  retried. Cancellation/deadline error identities survive persistence.
- Different job types or parameters with the same operation ID fail with a
  conflict. The entire request is fingerprinted, including content beyond 8 MiB;
  inline workflow parameters and secrets are not copied into the operation
  receipt. Results can contain sensitive operational data and are stored with
  private file permissions, just like the ordinary job journal.
- An in-flight duplicate fails without executing and reports the original job
  identity when its receipt is available. Poll that original job instead.
- A crash or panic after the execution marker but before a terminal receipt
  leaves an **unknown outcome**. A retry reports that uncertainty and refuses to
  run the operation again. Inspect existing sessions, panes, and pipeline state
  before making an explicit decision to submit new work with a new ID.

Each result includes `_operation`:

```json
{
  "_operation": {
    "id": "deploy-42",
    "original_job_id": "the-first-job-id",
    "status": "completed",
    "replayed": true
  }
}
```

Other metadata statuses are `failed`, `cancelled`, `in_progress`, `conflict`, and
`outcome_unknown`. The top-level job status still describes the current job
attempt; for example an unknown-outcome retry fails, rather than pretending the
original operation completed. Use `GET /api/v1/jobs/{original_job_id}` to inspect
the original execution while it remains in retained job history.

## Scope and storage

Operation IDs are unique across job types within one persistent server state
namespace. Use a new ID for intentionally new work. Reusing an ID never asks the
server to rerun work against a changed filesystem, configuration, or workflow
file; it requests the original outcome. Fingerprints describe the submitted
request, not the contents of external files or the ambient environment.

Receipts live in the job journal's `operations/` directory. A per-operation
kernel lock fences concurrent writers and is released on process death. An
atomic, flushed execution marker is published before calling the engine; the
terminal result is published before reporting completion. Unreadable, malformed,
unsupported, oversized, or wrongly identified receipts are errors, never evidence
that an operation is new. If a terminal receipt cannot be published, the original
marker blocks blind retries. A receipt is limited to 8 MiB.

Receipts are not automatically expired or discarded with the in-memory job list.
Deleting the persistent namespace or operation receipts removes retry protection.
This is an at-most-once engine-invocation guarantee while receipts are retained,
not a promise to roll back external effects or to complete work after a crash.

The existing `Idempotency-Key` HTTP cache is separate and remains process-local.
Use `params.operation_id` for durable execution identity; an HTTP header alone
does not opt into this guarantee. Omitting `operation_id` preserves ordinary job
execution behavior. These controls do not change direct non-job REST operations.

## Session targeting

Jobs accept `session` either in the top-level request envelope or in `params`.
The shared dispatcher passes the top-level value into the same typed engine
request used by nested parameters. When a nonempty top-level session and a nested
session are both supplied, they must be identical: a different value, an empty
nested value, or a non-string nested value fails before engine execution. It never
silently chooses a target.
Normal provider/session validation still applies.

For checkpoint restore, `session` names the source checkpoint namespace;
`params.target_session` remains the separate destination. For pipeline resume,
omitting both session fields still retains the saved session. Durable operation
identity fingerprints the original request envelope before normalization, so
retries should keep the same submitted shape as well as the same operation ID.

## Live startup recovery

On a persistent server, `swarm_spawn` jobs checkpoint their lifecycle as it runs.
No extra request flag is needed. Poll the existing `GET /api/v1/jobs/{id}` or job
list: `result._execution_in_progress: true` identifies a partial snapshot, not a
terminal outcome. The snapshot includes session/directory identity, observed
agents, and `spawn_progress` with a sequence, timestamp, last lifecycle event,
created pane IDs, and the mapping from physical pane addresses to tmux `%N` IDs.

The lifecycle stages are `create_session`, `split_window`, `layout`,
`launch_agent`, `start_monitor`, and `wait_ready`. Each has a `started` intent
checkpoint before the operation and a `finished` checkpoint afterwards. An
intent is not proof of completion; an error can accompany partial side effects.
The independent `observed_agents` list preserves recovery evidence even when a
backend returns an empty final agent list. Use the job's terminal status and
final spawn output for completion/readiness, not a lifecycle event alone.

Launch pacing waits occur before the launch-intent checkpoint. Finished events
are recorded even after cancellation. A checkpoint failure stops later lifecycle
effects and cancels the spawn context before later assignment can proceed. No
launch command or prompt is added to the progress schema.

If the backend returns no result or panics, its last progress remains in the job
receipt instead of disappearing. After process restart, interrupted progress is
marked `outcome_unknown` and is no longer reported as executing; work is not
replayed. A cancellation remains cancelled and keeps its original reason. When
a worker finishes normally, its final result replaces the partial snapshot and
removes `_execution_in_progress`. In-memory servers do not enable durable
lifecycle checkpointing.

For jobs with `operation_id`, progress is also flushed into the operation receipt
before it is forwarded to the ordinary job journal. An in-flight duplicate can
return that snapshot with `_operation.status: "in_progress"`. After a crash, a
retry returns the latest durable snapshot with `outcome_unknown`, including its
pane identities, even when the original job is no longer in the retained job
list. It still refuses to execute the operation again. No synthetic completion
is inferred from progress, and the abandoned worker is not reported as live.

An operation keeps progress if its backend returns no final result. A failed
final receipt write leaves the last durable in-flight snapshot available for
recovery. Reporting errors are sticky and cancel the execution context; a backend
that ignores them cannot turn a checkpoint failure into recorded success. Late
reports after completion or panic are rejected before touching the receipt.
