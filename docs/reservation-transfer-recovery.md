# Reservation transfer recovery

Spawn recovery uses `handoff.TransferReservations` to move the file reservations
recorded in a handoff. The handoff captures each server-reported reservation ID,
project ID, owner, creation time, path, mode, reason, and expiry. It does not infer
missing ownership from the agent or project requesting the handoff.

## Source identity before mutation

Every nonempty transfer, including a same-agent renewal, requires complete
captured lease identity. A path is reusable and cannot identify the lease that
existed when the handoff was created. Missing or nonpositive IDs, missing owner
or creation time, impossible recorded lifetimes, duplicate IDs or paths, and
mixed project IDs fail before external I/O. Duplicate path records are rejected,
not merged into a potentially different reservation mode.

Recovery independently reads the project's active reservations and verifies the
entire source batch before any release, renewal, or acquisition. Each captured
ID must still have the same project, owner, creation time, path, mode, and reason,
and must be live and unreleased. A renewal may change expiry without changing
lease identity; an old recorded expiry alone does not reject a still-active,
independently verified lease. Missing, changed, expired, or released leases and
unavailable or inconsistent readback all stop recovery.

`ErrTransferSourceEvidence` identifies these source preflight failures. The
result has `stage: validate` or `stage: verify_source`, no destination attempts,
and `outcome_unknown: false`: this attempt has not made a mutation. Underlying
read and cancellation causes remain available through the error chain. Older
path-only handoffs remain readable as context, but cannot authorize transfer.
Inspect current reservations and capture a new handoff rather than guessing
which newer lease now occupies the old path.

Source release and same-agent renewal send only `file_reservation_ids`; `paths`
is omitted entirely. If another process replaces a lease after verification,
the operation still selects only the captured IDs, not the replacement path.
An incomplete acknowledgement or operation error stops further acquisition or
compensation and records uncertainty. `requested_ids` parallels
`requested_paths`; `released_ids` parallels `released_paths` after a complete
release acknowledgement. A successful same-agent renewal reports the captured
IDs in `granted_ids`, not newly acquired leases.

## Destination receipts and compensation

Destination acquisition requires a non-nil receipt covering every requested
path exactly once, with distinct positive reservation IDs across both mode
groups. An empty, partial, duplicate, or foreign-path reply without an operation
error is not successful recovery.

`ErrTransferGrantEvidence` identifies replies that cannot safely authorize
further mutation. The transfer retains reported grant paths and IDs plus
confirmed source-release evidence, sets `outcome_unknown`, and stops without
releasing untrusted handles, retrying acquisition, or claiming rollback.

The Agent Mail client labels decode and ownership-readback failures with
`ErrReservationUnverified`, retaining the underlying cause and any recovered
handles. Transfer recovery recognizes that classification even when every
returned path looks requested and a conflict is also present. It does not issue
cleanup, retry, or rollback mutations from that unverified receipt. This also
stops the transfer when a later shared group fails verification after an earlier
exclusive grant; all reported handles remain available for inspection.

Exclusive and shared groups use the same acquisition helper. A failure in the
first group stops before acquiring the second. Conflict errors remain errors
even when the server omits the conflict detail array; conversely, conflict
details cannot be converted into success merely by omitting the Go error.

A propagation retry is permitted once, after successful partial-grant cleanup,
and only for a conflict-only error. Cleanup selects the verified destination
receipt IDs, never paths that might now identify replacement leases. A joined
conflict plus ownership-readback, transport, or cancellation error cannot
authorize another acquisition. Failed or incomplete cleanup preserves the
partial grant evidence and stops both retry and rollback. Cleanup and rollback
failures are returned together with the original cause, not only logged.

The result exposes `stage`, destination `attempts`, `cleanup_error`,
`rollback_error`, and `outcome_unknown`. `granted_paths` and `granted_ids` describe
the last acquisition reply, not a live reservation listing: those leases may
subsequently have been cleaned up. `rolled_back` means the source received
complete replacement coverage; it does not mean the release/acquire sequence
was atomic or that the old source IDs were restored. Capture fresh identity
before another transfer after rollback. An uncertain transport result can
remain `outcome_unknown` even after source coverage was restored.

A caller cancelled before transfer makes no mutation calls. Cancellation after
a confirmed source release stops destination dispatch and uses a separate,
bounded context to attempt source recovery. Uncertain effects and failed
compensation require inspection before another transfer is attempted.

## Scope

These checks bind source mutations to captured lease identity and destination
cleanup to verified receipt IDs. They rely on the server honoring ID selectors
and retaining unique lease identities. They do not authenticate a replacement
server, protect against out-of-band database ID reuse, persist a crash-replay
journal, or make the remote release/acquire sequence atomic. Release and renewal
still rely on count acknowledgements: independent post-mutation verification
is not provided. The source read and its subsequent mutation are not a server
transaction; exact-ID selection prevents a path fallback but cannot prevent
all concurrent remote changes. Ownership authentication and destination grant
readback remain the Agent Mail client's responsibility.
