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
