# Dependencies inside parallel pipeline bodies

A parallel body's children form a dependency graph. Declaration order is not
execution order: a child can depend on a later declaration, and only ready
children consume `settings.limits.substep_parallel_max` worker slots. Independent
children remain concurrent; a ready consumer does not wait for unrelated slow
work to finish.

For example, this consumer waits for `producer` even though it is written first:

```yaml
steps:
  - id: build
    parallel:
      - id: consume
        depends_on: [producer]
        command: "test '${steps.build_producer.output}' = ready && printf consumed"
      - id: producer
        command: "printf ready"
        output_var: produced_value
```

Dependencies can name either the authored sibling ID or its concrete scoped ID.
The outer workflow validator resolves these aliases too; mixed authored/scoped
references to one producer count as one prerequisite. Static nested parallel
groups keep their full scope. Prefer authored sibling IDs inside repeated bodies,
whose concrete runtime addresses change with each iteration.
The workflow definition is not rewritten, so scheduling does not change recovery
hashes. The existing scoped output (`steps.build_producer.output`) and a producer's
unique `output_var` are restored/published before releasing its dependents. Shared
`output_var` names retain their existing whole-group aggregate/collect/last rules;
use scoped outputs for dependencies within such a group.

A dependency is released only after the canonical step lifecycle, including its
retries and success hooks, returns and the result checkpoint succeeds. Failure
blocks dependent children transitively without running them. `on_error: continue`
continues independent children; it does not turn a failed prerequisite into
success or hide cancellation/deadline expiry behind another child's failure.
Ordinary condition skips retain their existing ordering semantics and do
not manufacture output values. `fail_fast` cancels active work and prevents further
admission; started children are joined before returning.

A checkpoint failure prevents new dependent effects and is not hidden by
`on_error: continue`. This is an ordering guarantee, not rollback or an exactly-once
claim; the existing command and agent-delivery receipt protocols remain responsible
for recovering external effects.

Completed children retained from a partial run are not dispatched again. Their
canonical outputs are rebuilt before new work starts. A retained consumer whose
producer must run again is rejected as inconsistent recovery rather than silently
using stale results or repeating the consumer's side effects.

An in-flight result or a fire-and-forget command (`wait: none`) must re-enter the
canonical lifecycle: a launch acknowledgment is not evidence that its process has
settled. That lifecycle's existing journals still decide whether replay is safe;
the parallel scheduler does not itself authorize another command launch.

Foreach children retain their owned `output_var` shapes at the final group merge;
the human-readable status summary cannot replace the collection. New executions
also checkpoint the captured binding with the completed child result, so a resumed
parallel body can adopt aggregate, collect, and last-value outputs without
repeating completed work. The capture occurs under the variable lock at the
foreach aggregation boundary, before success hooks can replace those aliases.
Empty collections and an absent last writer remain distinct from stale data;
parsed aliases absent from the receipt are cleared during restoration.

The versioned child receipt is stored in `ParallelState.ChildCheckpoints` within
the same execution-state file as the result. It binds the parent/child definitions,
completed result, and owned aliases. Changed definitions, mismatched or missing
results, contradictory in-flight evidence, malformed versions, and foreign aliases
stop recovery before dispatch. Snapshot copies preserve immutable receipts and
independent keyed collections. A receipt error cannot discard an already completed
result or be hidden by `on_error: continue`. Existing explicit whole-run reset and
iteration-scope removal clear the containing group's receipts with its state.

Foreach collection names must be distinct from direct siblings' output aliases,
including generated `_parsed` aliases. Scalar-only shared-name merges retain their
existing behavior. Old completed foreach results without a binding receipt remain
an explicit recovery error: the scheduler neither trusts an overwritten runtime
alias nor repeats completed effects to manufacture a collection.

Resume also validates and restores these bindings when a completed static outer
parallel group is skipped as a whole. Nested static groups reconstruct their
own final shared-name merges with the canonical aggregate/collect/last rules.
Handled child failures remain available as terminal evidence rather than being
deleted from a completed continue-mode group. Absent parsed outputs are cleared;
unsuccessful unique writers do not erase a prior value they never replaced.

Output restoration follows recorded completion timestamps. At a timestamp tie,
ordinary results precede group merges, nested merges precede enclosing merges,
and step IDs provide a stable final ordering. This removes Go map iteration from
the reconstruction decision; it does not introduce global alias ownership across
unrelated concurrent producers. Receipt validation finishes before graph mutation,
attempt timestamp updates, or any new checkpoint write. In-flight results cannot
mark graph nodes completed or restore output aliases, even when their saved status
says completed.

This does not reconstruct arbitrary dynamic branch/loop scopes or top-level
foreach output bindings outside these parallel completion receipts. Legacy groups
without parallel progress metadata keep their existing recovery path. A completed
parallel group hiding an unfinished or background child is refused rather than
silently skipping its lifecycle. Receipts are consistency checks, not signatures
against an actor who can rewrite both checkpoint contents and their hashes.

Duplicate/ambiguous runtime identities, cycles, unknown dependencies, and unfinished
external prerequisites fail before child dispatch. Top-level prerequisites remain
owned by the outer workflow scheduler. This change schedules inline `parallel`
bodies, including materialized nested bodies; it does not add sibling scheduling to
sequential loop or branch bodies.

## Interrupted success hooks

A parallel child can finish its body before cancellation interrupts its
`on_success` handoff. Its completion receipt now records body completion and
hook settlement separately. A normal recovery adopts the body, restores its
original output bindings, and finishes the pending hooks through the existing
hook runner; it does not run the body again to reconstruct a notification.
Completed hook bodies are skipped, including a completed hook whose own nested
success tail still needs work. The native hook depth limit remains in effect.
Failed or unfinished hooks retain the existing hook runner and command-journal
retry rules; this is not an exactly-once guarantee for a partially executed hook.

Recovery tails occupy bounded parallel worker slots. An unrelated ready sibling
can proceed while one handoff waits, but a dependent child is released only after
its producer's hook status and result have been checkpointed. A further
cancellation retains the pending tail. A completed outer group claiming an
unfinished child tail is rejected rather than silently dropping the handoff.
Changing the hook-settlement flag without updating its bound receipt hash is
rejected, as are conflicting saved hook identities. Existing unpublished receipt
formats are not a stable compatibility interface.

Receipt creation is a checkpoint boundary after external effects, even if the
failure is serialization or a missing aggregate binding rather than filesystem
IO. Such failure preserves the completed child result and enters the same sticky
checkpoint-failure path as the rest of the executor, cancelling workflow-wide
work rather than permitting independent dispatch under `on_error: continue`.
