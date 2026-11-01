# Bounded asynchronous job admission

`POST /api/v1/jobs` uses one bounded executor rather than starting an unbounded
worker for each request. By default it allows four active jobs and 64 additional
owned requests (68 total, including preparation and finalization). Configure the
same limits on both server entry points:

```sh
ntm serve --job-concurrency=4 --job-queue-capacity=64
ntm web --job-concurrency=4 --job-queue-capacity=64
```

The embedding API exposes `serve.Config.JobConcurrency` and `JobQueueCapacity`.
Zero selects the default; accepted positive limits are 1–128 concurrent jobs and
1–4096 additional requests. Limits remain fixed for the server lifetime. These
are counts of whole operations, not per-provider agent counts, thread counts, or
pipeline fanout limits. Existing resource-pressure and assignment policy checks
still run. Resource-blocked jobs may occupy otherwise idle worker capacity; the
hard admission bound is always concurrency plus queue capacity, not an unbounded
second resource-wait queue.

## Admission, ownership, and cancellation

Capacity is reserved before cloning request parameters or creating a job record.
Preparing requests and cancelled workers still writing their final checkpoints
consume capacity. Rejected overload creates neither a job nor a worker and returns
HTTP 503 with `Retry-After: 1`. HTTP 202 is returned only after preparing the pending
receipt; on persistent servers that includes flushing it to the existing journal.
The `Location` header identifies the polling endpoint.

Jobs sharing execution resources run FIFO, ordered by completed admission;
concurrent receipt preparation is not serialized behind a slow disk operation.
Independent jobs can pass a resource-blocked waiter. Only the configured number
of worker goroutines executes the queue. A job's two-hour execution timeout starts
when it is dispatched, not while it waits. Closing the submitting HTTP connection
after acceptance does not cancel the accepted job.

Cancel via `DELETE /api/v1/jobs/{id}`. Queued cancellation never invokes an engine
and releases its capacity after finalizing the cancellation receipt. An active job
keeps its cancellation handle, row, execution resources, and recovery evidence
until its worker finishes. The in-memory history-retention limit cannot evict a
worker still checkpointing. Cancellation retains already-recorded live progress
rather than clearing it.

A failed cancellation write still signals the worker, but returns HTTP 503 with
`cancellation_requested: true` in the error details instead of claiming durable
success. A completed/failed/cancelled row cannot be cancelled again (HTTP 409).

On persistent servers, the worker saves its final receipt before GET/list can
report completion. If that save fails after the engine returns, the job reports
`failed` and retains the complete observed result, with `result.observed_status`,
`result.outcome_unknown: true`, and `result._journal_error`. The engine may already
have completed its work: inspect the retained run, session, or pane identities
before deciding whether to retry. An existing cancellation keeps `cancelled` and
its original reason while retaining the worker's final evidence.

The last durable job checkpoint remains available for restart recovery. An
independent durable `operation_id` receipt remains authoritative, so repeating
that operation ID returns its recorded outcome without running the engine again.
Late progress callbacks cannot replace a terminal receipt after worker ownership
is released.

Shutdown closes admission permanently, cancels the service-owned contexts, and
waits for receipt preparation and finalization as well as active execution. A
shutdown deadline is respected even when a checkpoint holds the store mutex.
The existing journal owner/worker fences remain responsible for preventing a new
process from recovering work that is still alive.

## Startup history and recovery

Startup retains the newest 1,000 job receipts for GET/list. Recovery reads the
directory in bounded batches and limits retained result payloads while reading,
so a server with years of journal history does not first load every historical
result into memory. The retention order matches ordinary job-store eviction:
older creation times are discarded from the in-memory view first, with smaller
job IDs discarded first when creation times tie.

The retention window does not limit safety checks. Every stored job receipt is
validated and every worker fence is checked before any interrupted receipt is
changed. Corrupt or oversized evidence and live worker fences block startup even
when they belong to jobs outside the retained window. Recovery updates every
interrupted receipt on disk, including older ones, and verifies the completed
recovery before publishing history. A cancelled job keeps its cancellation reason
while unfinished progress becomes an explicit unknown outcome.

Retention does not delete journal files, expire durable `operation_id` receipts,
or replay work. Older receipts remain in the state database's `.jobs` directory
for inspection even when their jobs are no longer exposed by GET/list. Repeating
recovery preserves already-recorded outcomes and interruption evidence.

## Session-aware execution

A pipeline and a swarm spawn targeting the same tmux session cannot execute
concurrently through this queue. Spawn labels are part of the target identity:
`session=project,label=dev` shares a scope with `session=project--dev`. A checkpoint
restore with `target_session` locks the destination, not its source archive name.
The normalized top-level/parameter session binding is shared with dispatch;
conflicting explicit sessions fail admission rather than locking the wrong one.

Pipeline resumes bind their effective session at admission: an explicit override
wins, otherwise the shared saved-state reader supplies the session. They acquire
both that session and the canonical saved-run pathname as execution scopes. Two
resumes of one saved run cannot overlap even with different session overrides;
unrelated runs and sessions may proceed concurrently. Project and state-directory
symlink aliases share the same saved-run scope.

This binding freezes identity, not execution state. Dispatch retains the admitted
session even if a newer saved checkpoint names another session, but reloads the
actual state through the existing engine and run lock so completed steps are not
replayed from an old snapshot. A detected state-directory redirection fails before
loading state or entering the engine. The private binding does not change public
parameters or durable operation-ID fingerprints.

When the run namespace or implicit session cannot be resolved, the job retains
an exclusive queue barrier and the existing dispatcher error path. This permits
a completed operation-ID retry to replay its durable result even after its saved
state is gone. The queue does not invent a destination or replay uncertain work.
A checkpoint restore without `target_session` uses its source session: the shared
checkpoint loader verifies metadata matches that namespace before restoring.

All scopes for a job are acquired together. Earlier blocked multi-scope jobs keep
their ordering on every required scope, so younger jobs cannot starve them.
Cancelling a queued waiter removes this ordering barrier without executing it.
Resource ownership lasts through final checkpoint writes, even after cancellation
or an engine failure. These are execution-order guarantees within this server's
generic jobs queue, not cross-process locks, file reservations, authorization, or
isolation from direct CLI/synchronous REST operations.

## Inspection and retries

`GET /api/v1/jobs` includes an `execution` object:

```json
{
  "max_concurrent": 4,
  "queue_capacity": 64,
  "running": 4,
  "queued": 12,
  "waiting_for_resources": 3,
  "preparing": 1,
  "owned": 17,
  "accepting": true
}
```

`running` includes worker finalization; it is not a count of ready agents.
`waiting_for_resources` is the subset of live queued jobs blocked by running
owners or earlier conflicting waiters. `owned` includes preparing admissions and
cancellation finalizers. Queued rows stay `pending` until dispatch; existing
terminal statuses and job result formats remain.

The request envelope is strict: unknown fields, extra JSON values, and malformed
parameter containers are rejected before admission. Integer-valued workflow
variables retain their JSON number precision. The queue deep-copies nested maps
and slices without normalizing the public request shape, preserving durable
`operation_id` fingerprints and the established session-conflict validation.

Queueing does not replace durable operation identity. An accepted job lost to a
process crash is not automatically rerun; inspect its recovered evidence and use
the documented operation-ID retry contract. Cancelling a queued job does not claim
an operation ID because it has not reached the operation engine/guard. Direct
synchronous REST routes and the separate bead-close async route are not routed
through this executor by this change.

## Queued pipeline project identity

File-backed, inline, and resumed pipelines freeze the server's absolute project
selection during admission. A later `PATCH /api/v1/config` cannot redirect a
queued pipeline's relative workflow path, working directory, or resume-state
lookup to another project. The pending job receipt exposes that selection as
`project_dir`, and the ordinary shared pipeline methods execute against it.

This is a namespace snapshot, not a copy of files, an adversarial filesystem
isolation boundary, a provider-configuration snapshot, or an execution permission
grant. Resumes additionally canonicalize their project selection and reject
detected state-directory redirection at dispatch. Workflow validation, path
confinement, run locks, and current policy checks still run normally. Direct
synchronous routes and checkpoint source/destination execution retain their
existing behavior. Queued swarm directory selection is described below.

The private execution binding is not included in the public request fingerprint:
retrying an existing `operation_id` still asks for its original recorded outcome,
not a rerun in whatever project is currently selected on the server.

## Queued swarm project identity

`swarm_spawn` jobs now bind a working directory before admission completes.
Omitting `params.working_dir`, or supplying an empty string, selects the server's
current project, not an unrelated process working directory. A relative override
is resolved against that selected project; an absolute override selects that
directory directly. The resulting absolute path appears as `job.project_dir` in
the pending response and is persisted with the admission receipt. A null or
non-string override fails admission with HTTP 400 and creates no job.

For a server selecting `/work/project-a`, this request launches in
`/work/project-a/component`, even if `/api/v1/config` switches the server to
`/work/project-b` while the job is queued:

```json
{
  "type": "swarm_spawn",
  "params": {
    "session": "component-workers",
    "working_dir": "component",
    "cc_count": 2,
    "launch_ready_timeout": "45s"
  }
}
```

Dispatch passes the binding to the same spawn service, preserving launch pacing,
readiness, cancellation, progress recording, reservation policy, and partial
failure output. Dry runs use the same binding. Queued cancellation keeps the
recorded directory without starting an agent. Direct CLI and synchronous REST
spawning retain their existing directory semantics.

The original parameters and operation-ID fingerprint are unchanged. An
operation-ID retry asks for the original recorded result, not another execution
in the newly selected project. `job.project_dir` describes this admission;
on replay the recorded result describes the earlier operation's actual effects.
This freezes a path selection, not file contents, symlink targets, provider
configuration, filesystem permissions, or authorization. Existing spawn checks
still run at execution; admission does not create the selected directory.

## Readiness-gated fleet startup

A `swarm_spawn` job can require each newly launched agent to initialize before
starting the next one. Send this body to `POST /api/v1/jobs`:

```json
{
  "type": "swarm_spawn",
  "params": {
    "session": "myproject",
    "cc_count": 3,
    "cod_count": 2,
    "launch_ready_timeout": "45s",
    "launch_interval": "2s",
    "startup_timeout": "5m"
  }
}
```

`launch_ready_timeout` is a positive Go duration for each agent's readiness
check, starting after its process launch returns. It uses the shared spawn
readiness detector and its existing agent-type safety preflight. The last agent
is checked too. Omit the field for the original launch-all behavior; zero,
negative, empty, null and malformed supplied values fail before spawning.
`launch_interval` remains an optional minimum start-to-start spacing, while
`startup_timeout` bounds the entire spawn, including launches, readiness waits
and work assignment. Previews validate these controls but never launch or wait.

Failure or cancellation stops later agent launches; it does not kill processes,
remove panes, or retry commands. The spawn engine still prepares the session's
pane topology first. Inspect the partial result before retrying. Persistent jobs
record each launch before its readiness wait and merge individual readiness
updates without losing earlier agents or their durable pane identities. The
result echoes the effective `launch_ready_timeout`. Readiness is an initialization
observation, not a guarantee of later availability or an atomic work claim; all
existing assignment and reservation gates still apply.
