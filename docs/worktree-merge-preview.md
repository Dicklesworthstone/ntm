# Preview agent-worktree integration

`ntm worktrees merge <agent-name> --dry-run [--session <session>] --json`
computes Git's real three-way merge of committed agent work into the resolved
repository default branch. This uses the same source-worktree identity as the
existing `worktrees merge` command; it does not create a second isolation layout.

The result reports `source_branch`, `target_branch`, their exact commit IDs,
`source_commits`, `merge_tree`, `has_conflicts`, `conflicts`, `blockers`,
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
branch identity, and shared repository identity must be valid; `--force` does
not bypass those requirements in a preview. Both branch tips are rechecked after
inspection, and a changed tip invalidates the assessment.

A preview is point-in-time evidence, not a lock against other Git writers.
Stop editing agents before integration. Global and local Git policy, hooks, and
merge drivers still require the operator's normal trust and review.
