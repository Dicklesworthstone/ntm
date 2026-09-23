# Source-verified live work projections

The work/coordination adapter used by live robot projections verifies direct
tracker ready candidates against the same `internal/worksource` identity and
eligibility implementation used by actionable BV planning. It does not
introduce a second Git or JSONL identity engine.

When `.beads/issues.jsonl` exists, collection records its canonical path,
SHA-256 digest, project directory, and local checkout HEAD. It checks the source
before reading tool results and again before publishing them. Dirty local edits
remain valid by default; verification does not fetch, check out, import, repair,
or require `origin/main`.

Candidates are filtered against canonical lifecycle, ownership fields,
blocking dependencies, container/non-dispatchable types, deferrals, project
operator gates, private labels, and held mutex labels. A configured program
allow-list is applied when explicitly supplied through the adapter's Go config.
The shared filter selects a mutex-compatible batch in tracker order. Final
atomic assignment and live reservation checks remain mandatory.

## Candidate collection precedes display limits

The adapter's summary collector retains its bounded previews for enrichment.
Membership for verification comes from an additional direct
`br ready --json --limit 100001` read, within the same source-identity window.
The display limit is applied only after eligibility and mutex selection. This
allows eligible work below a blocked or operator-gated top-N prefix to appear
rather than falsely reporting that the backlog is drained.

A maximum of 100,000 candidate records is accepted; a sentinel row beyond that
limit rejects the collection instead of publishing a partial total. Explicit
empty arrays are valid. Empty output, null, malformed or duplicate records,
invalid priorities, failed envelopes, declared pagination/truncation, and
inconsistent envelope totals fail with `WORK_CANDIDATES_INCOMPLETE`. A failed
tracker command retains its actual error. Neither kind of failure is an empty
queue, and neither falls back to stale summary candidates.

## Receipt and count semantics

The normalized `work.verification` object contains the source identity,
advisory dirtiness, excluded IDs with deterministic reasons and blocker IDs,
the tool's original `reported_ready`, a `count_scope`, and remediation guidance.
Excluded receipts do not include task titles or descriptions.

For live `count_scope: verified_candidates`, `work.summary.ready`,
`work.triage.ready_count`, and `verification.verified_ready` count the complete
source-verified, mutex-compatible batch from the direct candidate read BEFORE
the display cutoff. `candidates_observed` includes rejected candidates;
`preview_limit` and `preview_truncated` describe the returned `work.ready` list.
A preview can contain fewer rows than the verified ready total. A top
recommendation outside the returned preview is removed.

The internal filtering helper can still produce `verified_preview` when given
only a preview; those counts are scoped to that input. Counts from either mode
are not a claim that work absent from the tracker's ready set is claimable.
In-progress and blocked evidence remain available rather than being silently
discarded.

When every collected candidate is excluded, the receipt says
`NO_CLAIMABLE_WORK`; a readable source does not become unavailable just because
its candidates are ineligible. Source mismatches instead return
`STALE_WORK_COORDINATION`, mark work unavailable, clear ready candidates/counts
and the top recommendation, and retain available mismatch evidence. Optional
triage enrichment must not swallow this failure. Cancellation and ordinary tool
failures keep their actual error identity instead of being labeled mismatches.

## Live Agent Mail reservation evidence

The live adapter reads project-wide reservations before eligibility, mutex
selection and preview limiting. All owners count, including peers and the
querying agent. It uses the existing complete paginated reader, independently
verifies numeric project identity, and checks that identity again after listing.
The final canonical source check also covers the reservation-read interval.
Inspection never ensures a project, registers an agent, acquires or renews a
lease, or repairs tracker state.

Only NTM's exact `bead assignment: <id>` reason establishes a bead binding.
Expired or released reservations do not exclude candidates. Other live path
reservations are counted as unmapped, never guessed from arbitrary prose,
paths, or prefixes. A live reservation on a closed bead still holds that
bead's canonical mutex groups while its worker unwinds. Independent work below
reserved candidates can fill the preview normally.

`work.verification.reservations` distinguishes these observation states:

- `observed`: both project identity checks and the complete paginated read
  succeeded. Includes `project_id`, `observed_at`, `active`, `mapped_beads` and
  `unmapped`. An observed empty set has zero counts.
- `unavailable`: a failed, inconsistent, malformed or timed-out read. `reason`
  uses the normal disclosure/redaction path. Zero counts do not mean no locks.
- `not_checked`: no reader or no canonical JSONL source was available.

One 1.5-second budget covers the optional identity and reservation reads.
A parent cancellation stops collection; an Agent Mail outage does not turn a
readable Beads backlog into an empty queue. Its reservation receipt instead says
unavailable. `verified_candidates` still describes canonical-source validation,
not a claim that unavailable external ownership checks succeeded.

This observation is not a cross-service transaction or a lease. Ownership can
change after the read, and unmapped path reservations still require the final
live path-reservation checks at dispatch.

## Database-only workspaces

A workspace with no JSONL export retains its existing tool-backed counts,
explicitly marked `tool_reported_unverified` with no source receipt or
`verified_ready` total. Its ready candidate read must still succeed explicitly;
a failed or malformed response is not evidence of an empty queue. The visible
list is bounded after that read. An export appearing during collection is
rejected rather than silently mixing the two modes. An unreadable, malformed,
disappearing, or unstable export is not a database-only fallback. Explicit
source/program policies cannot be satisfied through this unverified path.
Reservation evidence is marked `not_checked` in this mode.

## Scope

This change covers live adapter collection. Persisted SQLite `RuntimeWork`
readers still need source-receipt storage and read-time validation; the receipt
is not a claim that those readers are already protected. Active assignment-ledger
barriers are not imported into this filter. Reservation evidence is connected
only to the live adapter, not the BV planning API. Strict policy fields are Go
adapter options, not new CLI flags. Final atomic claim and reservation gates
remain required on every dispatch path.

Tests cover immutable filtering, source changes, cross-project expected
identities, database-only behavior, export appearance, malformed JSONL,
cancellation, bounded complete candidate reads, excluded-prefix starvation,
verified totals independent of preview size, and the actual adapter `Collect`
entry point with hermetic tool fixtures. Reservation regressions cover peer and
last-page ownership, closing-owner mutexes, expiry, invalid/foreign records,
project recreation, read failures, and cancellation. The focused helper tests
can run with the real worksource package; the full adapter surface tests require
the repository's normal dependencies.
