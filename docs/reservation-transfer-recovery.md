# Reservation transfer recovery

Spawn recovery uses `handoff.TransferReservations` to move the reservation paths
recorded in a handoff. Destination acquisition now requires a non-nil receipt
covering every requested path exactly once. An empty, partial, duplicate, or
foreign-path reply without an operation error is not successful recovery.

`ErrTransferGrantEvidence` identifies replies that cannot safely authorize
further mutation. The transfer retains the reported grant paths and confirmed
source-release paths, sets `outcome_unknown`, and stops without automatically
releasing untrusted paths, retrying acquisition, or claiming rollback.

Exclusive and shared groups use the same acquisition helper. A failure in the
first group stops before acquiring the second. Conflict errors remain errors
even when the server omits the conflict detail array; conversely, conflict
details cannot be converted into success merely by omitting the Go error.

A propagation retry is permitted once, after successful partial-grant cleanup,
and only for a conflict-only error. A joined conflict plus ownership-readback,
transport, or cancellation error cannot authorize another acquisition. Failed
or incomplete cleanup preserves the partial grant evidence and stops both retry
and rollback. Cleanup and rollback failures are returned together with the
original cause, not only logged.

The result exposes `stage`, destination `attempts`, `cleanup_error`,
`rollback_error`, and `outcome_unknown`. `granted_paths` describes the last
acquisition reply, not a live reservation listing: those paths may subsequently
have been cleaned up. `rolled_back` means the source received complete replacement
coverage; it does not mean the release/acquire sequence was atomic. In particular,
an uncertain transport result can remain `outcome_unknown` even after source
coverage was restored.

A caller cancelled before transfer makes no mutation calls. Cancellation after
a confirmed source release stops destination dispatch and uses a separate,
bounded context to attempt source recovery. Uncertain effects and failed
compensation require inspection before another transfer is attempted.

## Scope

This change validates path coverage and compensation sequencing. It does not
introduce distributed leases, persist a crash-replay journal, or make the remote
release/acquire sequence atomic. Source release, renewal, and partial cleanup
retain the existing path-based Agent Mail operations and count acknowledgements.
Ownership authentication/readback remains the Agent Mail client's responsibility;
exact-ID transfer fencing and independent post-release verification are not
claimed here.
