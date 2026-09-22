# Bounded asynchronous job admission

`POST /api/v1/jobs` uses one bounded executor rather than starting an unbounded
worker for each request. By default it allows four active jobs plus 64 waiting
jobs. Configure the same limits on both server entry points:

```sh
ntm serve --job-concurrency=4 --job-queue-capacity=64
ntm web --job-concurrency=4 --job-queue-capacity=64
```

The embedding API exposes `serve.Config.JobConcurrency` and `JobQueueCapacity`.
Zero selects the default; accepted positive limits are 1–128 concurrent jobs and
1–4096 waiting jobs. Limits remain fixed for the server lifetime. These are counts
of whole operations, not per-provider agent counts, thread counts, or pipeline
fanout limits. Existing resource-pressure and assignment policy checks still run.

## Admission, ownership, and cancellation

Capacity is reserved before cloning request parameters or creating a job record.
Preparing requests and cancelled workers still writing their final checkpoints
consume capacity. Rejected overload creates neither a job nor a worker and returns
HTTP 503 with `Retry-After: 1`. HTTP 202 is returned only after preparing the pending
receipt; on persistent servers that includes flushing it to the existing journal.
The `Location` header identifies the polling endpoint.

Accepted waiting jobs run FIFO, ordered by completed admission; concurrent receipt
preparation is not serialized behind a slow disk operation. Only the configured
number of worker goroutines executes the queue. A job's two-hour execution timeout
starts when it is dispatched, not while it waits. Closing the submitting HTTP
connection after acceptance does not cancel the accepted job.

Cancel via `DELETE /api/v1/jobs/{id}`. Queued cancellation never invokes an engine
and releases its capacity after finalizing the cancellation receipt. An active job
keeps its cancellation handle, row, and recovery evidence until its worker finishes.
The in-memory history-retention limit cannot evict a worker still checkpointing.
Cancellation retains already-recorded live progress rather than clearing it.

A failed cancellation write still signals the worker, but returns HTTP 503 with
`cancellation_requested: true` in the error details instead of claiming durable
success. A completed/failed/cancelled row cannot be cancelled again (HTTP 409).

Shutdown closes admission permanently, cancels the service-owned contexts, and
waits for receipt preparation and finalization as well as active execution. A
shutdown deadline is respected even when a checkpoint holds the store mutex.
The existing journal owner/worker fences remain responsible for preventing a new
process from recovering work that is still alive.

## Inspection and retries

`GET /api/v1/jobs` includes an `execution` object:

```json
{
  "max_concurrent": 4,
  "queue_capacity": 64,
  "running": 4,
  "queued": 12,
  "preparing": 1,
  "owned": 17,
  "accepting": true
}
```

`running` includes worker finalization; it is not a count of ready agents.
`owned` includes preparing admissions and cancellation finalizers. Queued rows stay
`pending` until dispatch; existing terminal statuses and job result formats remain.

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

This is a namespace snapshot, not a copy of files, immutable symlink targets,
provider configuration, or an execution permission grant. Workflow validation,
path confinement, run locks, and current policy checks still run normally.
Direct synchronous routes retain their existing behavior. Swarm default-directory
resolution and checkpoint source/destination resolution are not changed.

The private execution binding is not included in the public request fingerprint:
retrying an existing `operation_id` still asks for its original recorded outcome,
not a rerun in whatever project is currently selected on the server.

