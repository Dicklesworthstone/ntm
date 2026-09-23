# Durable work queries

`ntm work-snapshot` queries complete, project-scoped work evidence across NTM
process restarts. Output is JSON, with the standard success/error response and
`work.verification` receipts. This command never dispatches prompts, claims
Beads, imports tracker data, creates Agent Mail projects, or acquires leases.

```sh
ntm work-snapshot --project=/data/projects/myapp --limit=5
ntm work-snapshot --project=/data/projects/myapp --refresh
```

The first successful collection stores all direct ready candidates before
canonical eligibility, reservation/mutex selection and the visible cutoff.
`work.summary.ready` and `verification.verified_ready` describe the verified
candidate total, not the number of displayed rows. An explicit empty candidate
set is persisted too; absence and failure are not rewritten as a healthy empty
queue. The underlying state DB follows NTM's existing selected-config path.

Reuse is bounded by the original 45-second observation window. It verifies the
canonical project path, JSONL bytes, checkout HEAD, saved source/program policy,
and fresh peer-reservation evidence using the existing live work verifier.
A released reservation can make a previously excluded candidate visible because
the cache retains candidates, not just the old displayed preview. Ordinary dirty
local development remains supported. A deferred-work deadline invalidates the
old candidate set even when HEAD and JSONL bytes have not changed.

Source mismatch or corrupt evidence returns a visible failure, without a hidden
live fallback. `--refresh` explicitly asks for a new collection; it does not
repair the source. Missing/expired observations and unavailable markers can be
collected live. Database-only observations remain unverified and are recollected
rather than reused as source-bound caches. Optional Agent Mail outages remain
explicit `unavailable` reservation evidence, not a checked-empty lock set.

## Persistence and concurrency

Migration 024 adds `runtime_work_snapshots`, keyed by canonical project. Each
opaque versioned payload has a SHA-256 revision binding the project, complete
payload and original collection/expiry timestamps. Reads reject mismatches and
oversized or malformed payloads. A newer failure marker replaces an old healthy
observation. An older collector cannot overwrite newer evidence; equal-time
retries must be byte-identical.

Publication is transactional and cancellation includes waiting for the local
writer and SQLite. Reuse releases database locks before filesystem/network
verification, then checks both expiry and the durable revision again. Successful
reads never extend TTL or rewrite the receipt. Final claims and live file-path
reservation gates are still mandatory before dispatch.

## Current integration boundary

This increment makes persistence and read-time verification reachable through
`ntm work-snapshot` and `adapters.CollectDurableWork`. Existing `--robot-status`,
`--robot-snapshot` and raw `RuntimeWork` inspection consumers are NOT migrated
by this commit. The prepared follow-on integration patch for those consumers
remains separate. The command does not claim their row-based results are safe.

Focused tests cover real serialized source evidence and SQLite close/reopen,
project isolation, rollback, late writers, cancellation, expiry, corruption,
source mismatch, deferred readiness and changing peer reservations. The native
CLI regression exercises the actual command and adapters with hermetic br/bv
fixtures; it requires the normal repository toolchain/dependencies. A scoped
verification harness is not a replacement for that full integration test.
