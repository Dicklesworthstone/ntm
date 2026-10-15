# Preview agent-worktree integration

`ntm worktrees merge <agent-name> --dry-run [--session <session>] --json`
computes Git's real three-way merge of committed agent work into the resolved
repository default branch. This uses the same source-worktree identity as the
existing `worktrees merge` command; it does not create a second isolation layout.

The result reports `source_branch`, `target_branch`, their exact commit IDs,
`source_commits`, `merge_tree`, `has_conflicts`, `conflicts`, `overwrite_risks`, `blockers`,
`already_merged`, and `can_merge`. A successful preview means the inspection
completed; it does not mean a merge happened. Automation must inspect `can_merge`
and `has_conflicts`. A conflict may have no individual path entries, so an empty
`conflicts` array alone is not evidence of a clean merge.

Preview does not switch branches, change either index or checkout, advance refs,
or enter a merge-in-progress state. Git may write unreachable objects while
computing the proposed tree, and configured custom merge drivers may run.
It requires the modern `git merge-tree --write-tree` interface (Git 2.38+).
An unsupported command, inspection failure, or malformed response is an error,
not an empty or conflict-free assessment.

Dirty source/target checkouts, a target not on the default branch, and pending
merge/rebase/cherry-pick/revert/bisect operations are reported as blockers. Only
committed changes participate in the tree calculation. Source registration,
branch identity, and shared repository identity must be valid. Merge no longer
accepts `--force`; identity and safety checks cannot be bypassed. Both branch tips are rechecked after
inspection, and a changed tip invalidates the assessment.

A preview is point-in-time evidence, not a lock against other Git writers.
Stop editing agents before integration. Global and local Git policy, hooks, and
merge drivers still require the operator's normal trust and review.

## Guarded application

Run the same command without `--dry-run` to apply. Unlike the previous path,
NTM never checks out the default branch automatically: select the intended
target branch yourself and stop other Git writers first. Both source and target
must be clean and have no pending Git operation. Preview always runs again
before application. Conflicting committed changes are refused without putting
conflict markers in either checkout or leaving a merge in progress.

To bind the operation to an inspected pair of commits, pass both complete IDs:

```sh
ntm worktrees merge agent --dry-run --json
ntm worktrees merge agent --expected-source <source_commit> --expected-target <target_commit>
```

A changed source or target tip invalidates those expectations. With neither
expectation supplied, the operation assesses the current tips. A partial pair,
empty explicitly supplied IDs, or expectations supplied with `--dry-run` are
errors. The engine passes the assessed source commit ID to Git, not its moving
branch name. It uses a non-fast-forward, two-parent ort merge, overrides inherited
branch merge options, disables autostash, and verifies the resulting tree and
parents against the assessment before acknowledging success. Already-merged
work is a no-op unless an earlier execution still requires recovery.
`--json` emits a structured success receipt instead of prose.

Ignored local files need an explicit guard: a real ort merge can replace them
despite `--no-overwrite-ignore`. Preview compares planned write paths against
ignored untracked paths, including ancestor/descendant collisions and Git's
`core.ignorecase` policy. `overwrite_risks` names the affected paths and the
`target_ignored_path_collision` blocker refuses application. The check runs
again at the mutation boundary. Unrelated ignored files and caches do not block
integration. Normal untracked files are already covered by the clean-checkout gate.

## Ownership and durable recovery

Application takes a nonblocking OS lock at `<git-common-dir>/ntm-merge.lock`
before repeating its mutation checks. All NTM sessions and linked worktrees in
that repository share this fence through Git execution, hooks, result checking
and the final receipt save. A competing NTM application fails explicitly; it
never waits and then applies an outdated plan. The lock file is a permanent
rendezvous inode and must not be removed while NTM is running.

Before starting Git, NTM durably saves `<git-common-dir>/ntm-merge.json` with a
unique attempt ID, `prepared` state, the complete assessed source/target/tree,
and timestamps. A successful verified application atomically replaces it with
`committed` and the verified commit ID. This file retains the last admitted
attempt, not an unbounded audit history. The next admission may replace a settled
receipt. A failed temporary write is retained for inspection but never authorizes
execution. Corrupt, oversized, unsupported or substituted records fail closed.

`prepared` means **outcome unknown**, not "running" or "nothing happened".
Cancellation, hook failure, worker death or a failed final save leaves that
barrier in place. Further applications refuse it, and previews report
`merge_recovery_required`, even when Git ancestry says the work is already merged.

```sh
ntm worktrees merge-status --json
# Inspect Git state and stop all editing agents, Git writers and orphaned hooks.
ntm worktrees merge-recover <attempt_id> --json
```

Both commands accept `--session <session>` to resolve the repository. Status
reads without taking the writer lock and is usable during a live merge. Recovery
must run from the recorded target checkout, takes the same fence, and is bound
to the inspected attempt ID. It never reruns a merge. It records `recovered` only
when the clean target HEAD is the exact two-parent commit with the assessed tree.
Re-running recovery for the same settled attempt is a no-op.

When the target still identifies its original pre-merge commit and is clean with
no pending Git operation, completion cannot be proved. After investigating the
attempt and stopping writers, the operator can explicitly settle it as abandoned:

```sh
ntm worktrees merge-recover <attempt_id> --abandon --json
```

Abandonment changes only the receipt; it does not abort, reset, stash, delete or
repair Git state. A dirty, moved, wrong-branch or otherwise ambiguous target is
refused. A stale attempt ID cannot acknowledge a newer merge. Hook effects
outside the checkout are not undone or proven absent by abandonment.

## Concurrency and qualification limits

Independent Git processes and editing agents do not participate in this advisory
fence. Stop them before integration or recovery. In particular, a surviving Git
child or hook after its NTM parent dies must be stopped or allowed to finish
before an explicit recovery decision. Snapshot checks and receipt reconciliation
are not an atomic transaction against arbitrary external writers.

Git hooks remain active. Execution errors retain Git's recovery state for
inspection. NTM never automatically resets, aborts, stashes, deletes or rolls back
that state. Configured merge drivers and hooks are trusted local executables;
their own side effects are outside checkout-preservation guarantees.

The ownership and recovery regression suite uses real Git repositories and
independent test processes. Selected-source Linux component tests are distinct
from full-repository, CLI and native Windows qualification.
