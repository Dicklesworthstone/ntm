# Worktree cleanup safety

The existing `ntm worktrees remove <agent-name>` and `ntm worktrees clean`
commands use the same guarded removal engine. Their confirmation and `--force`
controls are unchanged: authorized cleanup can still discard uncommitted,
ignored, and unmerged work. Stop editing agents and independent Git writers
before cleanup. This is not an automatic safe-to-delete assessment.

## Git refusals are binding

A failed `git worktree remove --force` stops that removal. NTM never falls back
to recursive filesystem deletion, unlocks the worktree, prunes registrations,
or deletes the branch after a refusal. In particular, a Git worktree lock is
not bypassed by the command's `--force` confirmation override.

Successful exit is checked against the filesystem and Git registration before
branch cleanup. A wrapper that does nothing cannot authorize the branch step.
If Git removes the checkout but a wrapper then fails, the branch stays intact
for inspection. A retry with an absent checkout and surviving branch is refused;
only an absent checkout, branch, and registration is a successful no-op.

## Ownership and merge recovery

Cleanup acquires the existing repository-wide merge lock, shared across NTM
sessions and linked worktrees. It retains ownership through checkout removal,
branch cleanup, and verification. Nonempty session cleanup holds one lock over
the whole batch; it does not release ownership between agents. A competing NTM
merge, recovery, or cleanup receives the existing busy error rather than racing.

A prepared or unreadable merge receipt blocks removal before any member of a
nonempty batch is removed. The receipt is not modified by cleanup. Inspect
`ntm worktrees merge-status` and use the existing explicit merge recovery flow
only after stopping any surviving Git writers or hooks. Cleanup cannot erase
checkout evidence needed to resolve an uncertain merge. Removing already-absent
work through the single-agent API also honors this recovery barrier. Empty
session-directory housekeeping remains a no-work operation.

Each removal verifies the full registered path, expected agent branch, and
common Git directory. Detached or changed branches, foreign repositories,
symlink substitutions below the project root, and non-regular `.git` files are
refused. The project itself may be reached through an ordinary directory alias,
such as macOS's `/var` symlink. Registration inspection uses NUL-delimited Git
output; malformed output never means a checkout is absent.

The source branch tip is read before removal and compared again before branch
cleanup. Changed tips are retained for inspection. Both checkout absence and
branch absence must be observed before success. Batch errors retain their
underlying busy, recovery-required, and cancellation classifications.

## Limits

This lock coordinates these NTM merge and cleanup paths, not arbitrary Git
commands, editing agents, or every provisioning path. Tip comparisons are
snapshot checks, not an atomic compare-and-delete transaction against external
Git writers. A batch may partially complete when an individual worktree refuses
removal; failures are reported and the refused checkout is retained. NTM does
not recreate a removed checkout or erase a surviving branch to hide an error.

This change applies to `internal/worktrees` and the plural `worktrees` command
family. The separate `internal/git` worktree manager is not changed. Component
regressions exercise real Git and deliberate wrapper failures; they are not
full CLI, fleet, or native Windows qualification.
