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
work is a no-op; a retry without stale explicit expectations creates no new commit.
`--json` emits a structured success receipt instead of prose.

Ignored local files need an explicit guard: a real ort merge can replace them
despite `--no-overwrite-ignore`. Preview compares planned write paths against
ignored untracked paths, including ancestor/descendant collisions and Git's
`core.ignorecase` policy. `overwrite_risks` names the affected paths and the
`target_ignored_path_collision` blocker refuses application. The check runs
again at the mutation boundary. Unrelated ignored files and caches do not block
integration. Normal untracked files are already covered by the clean-checkout gate.

## Recovery and concurrency limits

The checks do not lock out arbitrary Git processes or editing agents. Stop them
before integration. In particular, commit expectations are snapshot guards,
not an atomic checkout-wide transaction or cross-process writer fence.

Git hooks remain active. A failed hook, command error, cancellation, or unexpected
post-merge tree/parents produces an inspection-required error, not an automatic
retry. Git may already have prepared the index, written checkout files, or moved
HEAD. Inspect `git status`, `HEAD`, and `MERGE_HEAD` before deciding how to recover.
NTM never automatically resets, aborts, stashes, deletes, or rolls back that state.
Configured merge drivers and hooks are trusted local executables; their own
side effects are outside the checkout-preservation guarantees of preflight.
