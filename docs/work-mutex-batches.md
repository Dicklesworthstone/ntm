# Mutex-compatible work recommendations

For projects with a canonical `.beads/issues.jsonl`, actionable recommendations
now select a ranked batch with no overlapping nonempty `mutex:*` labels. Two
otherwise eligible tasks labelled `mutex:db` are alternatives, not two safe
assignments to run together. The earlier eligible candidate wins; independent
work below the rejected alternative can still fill the requested batch size.

The shared source filter applies lifecycle, dependencies, operator policy,
privacy, deferral and existing ownership checks before reserving a candidate's
mutex groups within the batch. A task requiring several groups acquires all or
none in that plan. A rejected or blocked candidate never reserves an unrelated
group. Group names use the existing case/whitespace normalization.

Existing holders include in-progress work, non-terminal tasks with an assignee,
and tasks named in caller-supplied live ownership or reservation evidence. They
protect their groups even when omitted from the proposed candidates, outside
the selected program, private, blocked, or not yet running. Closing a tracker
row does not cancel an externally reported assignment or reservation: that
evidence must be retired by its owner. A historical assignee on a closed or
tombstoned row, with no live external ownership, does not retain a group forever.
Unassigned deferred work alone does not reserve a mutex.

Exclusion reason `mutex_held` means the source/policy already reported a holder.
`mutex_conflict` instead means an earlier eligible candidate in this same plan
uses a required group. It does not claim that the earlier recommendation was
actually assigned. Neither reason includes private task text or owner details.

This applies through `bv.GetActionableRecommendationsContext`, before its
requested result limit, without another selection engine. Each call is read-only
and independent: it does not change the tracker, snapshot or caller policy.
Selection is greedy in the established ranking, not a maximum-cardinality
optimizer. Callers must still perform live atomic claims and reservation checks;
this is not a distributed lease and cannot exclude a competing process after
source verification. Projects without a JSONL export retain their existing
DB-only behavior rather than claiming source-verified mutex coordination.

## Claim-time enforcement

The ordinary assignment and guarded stale-work claim transactions now recheck
all requested mutex groups inside the same `BEGIN IMMEDIATE` transaction that
changes the issue's status and assignee. Independent NTM processes using these
claim paths against one SQLite database cannot both newly claim different tasks
sharing a group. The issue row is the ownership record; there is no additional
lock table, lease timer, or cleanup daemon. This check also works for DB-only
workspaces, independently of whether recommendations had JSONL provenance.

Planning and claims share the same Unicode case/whitespace normalization and
tracker ownership predicate. In-progress tasks hold groups even without an
assignee; any other nonterminal assigned task also holds them. Historical owners
on closed/tombstoned rows do not. A retry excludes only its exact issue ID, not
all tasks owned by its actor. Even idempotent and stale-recovery claims recheck
for conflicting peers before returning permission to continue.

A refused multi-group claim changes no task, event, dirty marker or mutex
ownership. Failed writes and commits roll back together. Existing exact-owner
claim release makes the groups available again. `AssignmentMutexError` wraps
both `ErrAssignmentMutexHeld` and `ErrBeadAssignmentIneligible` and names only
the requested task and its conflicting groups, not holder identities or private
text. Typed eligibility refusals never trigger corruption-rebuild recovery,
even when a task/group name contains words resembling a SQLite diagnostic.

This is transactional tracker ownership, not a distributed lease over agent
lifetimes. Direct `br`/SQL edits, generic unguarded claims, independently copied
Beads databases, and Agent Mail reservations are outside this database gate.
Mutating labels or prematurely closing/releasing an active task can remove its
tracker protection; callers must retain their existing live reservation and
assignment-lifecycle checks and inspect uncertain external effects before retry.
