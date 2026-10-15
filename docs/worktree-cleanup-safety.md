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
merge, recovery, provisioning, or cleanup receives the existing busy error
rather than racing.

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

## Provisioning participates in ownership

`CreateForAgent`, used by `ntm spawn --worktrees`, now acquires the same
repository fence before creating directories, invoking Git, or returning an
existing checkout for reuse. It retains that ownership through final checkout
validation. A busy repository fails explicitly; a prepared, corrupt, or
unsupported merge-recovery record also refuses provisioning. No automatic
retry or recovery is performed, and the recovery record is not changed.

Creation and removal share the directory-substitution check. A symlink below
the project root cannot redirect provisioning into another directory, while
an ordinary alias to the project itself remains supported. Reused and newly
created checkouts must pass the same registration, branch, and common-repository
identity checks as removal. Matching branch names alone are not sufficient.

Relative project paths are resolved once before Git executes, without changing
the caller's reusable manager. A failed or canceled Git invocation still reports
a retained checkout through `Created`; no rollback or deletion is attempted.
An identity failure after Git creates a checkout likewise reports that partial
mutation instead of acknowledging a usable checkout.

## Fail before session setup

The spawn path's existing `PreflightForAgent` call now checks the same recovery
barrier and existing-checkout identity before session setup can proceed. It
rejects a live NTM repository owner, redirected directories, foreign `.git`
identity, invalid evidence, and paths still registered by a missing detached
worktree, even when the expected agent branch is absent.

Preflight resolves relative project scope once and uses the operation's context
with a bounded inspection deadline. It opens an existing ownership file without
creating one, probes its lock, and leaves the recovery receipt, checkouts, index,
and branch tips untouched. Repositories with no ownership file remain unchanged.
Preflight is not a reservation: a new owner or unresolved merge may appear after
it returns. `CreateForAgent` still acquires its own fence and repeats validation;
a prior successful preflight cannot override that later refusal.

## Limits

This lock coordinates these NTM merge, cleanup, and `CreateForAgent` operations,
not arbitrary Git commands, editing agents, or the separate `internal/git`
provisioning implementation. It is released when provisioning returns: it is
not an agent-lifetime lease or a transaction covering the complete spawn/launch
workflow. Empty-directory housekeeping is unchanged. Tip comparisons are
snapshot checks, not an atomic compare-and-delete transaction against external
Git writers. A batch may partially complete when an individual worktree refuses
removal; failures are reported and the refused checkout is retained. NTM does
not recreate a removed checkout or erase a surviving branch to hide an error.

This change applies to `internal/worktrees` and the plural `worktrees` command
family. The separate `internal/git` worktree manager is not changed. Component
regressions exercise real Git and deliberate wrapper failures; they are not
full CLI, fleet, or native Windows qualification.
