# Source-verified live work projections

The work/coordination adapter used by live robot projections now verifies its
ready preview against the same `internal/worksource` identity and eligibility
implementation used by actionable BV planning. It does not introduce a second
Git or JSONL identity engine.

When `.beads/issues.jsonl` exists, collection records its canonical path,
SHA-256 digest, project directory, and local checkout HEAD. It checks the source
before reading tool results and again before publishing them. Dirty local edits
remain valid by default; verification does not fetch, check out, import, repair,
or require `origin/main`.

The ready preview is filtered against canonical lifecycle, ownership fields,
blocking dependencies, container/non-dispatchable types, deferrals, project
operator gates, private labels, and held mutex labels. A configured program
allow-list is applied when explicitly supplied through the adapter's Go config.
Final atomic assignment and live reservation checks remain mandatory.

## Receipt and count semantics

The normalized `work.verification` object contains the source identity,
advisory dirtiness, excluded IDs with deterministic reasons and blocker IDs,
the tool's original `reported_ready`, a `count_scope`, and remediation guidance.
Excluded receipts do not include task titles or descriptions.

For `count_scope: verified_preview`, `work.summary.ready` and
`work.triage.ready_count` count only the returned verified preview, not the
unchecked global tool total. A preview remains bounded by the collector limit;
a zero preview is not proof that the complete project backlog is empty. An
excluded top recommendation is removed. In-progress and blocked evidence remain
available rather than being silently discarded.

When every collected candidate is excluded, the receipt says
`NO_CLAIMABLE_WORK`; a readable source does not become unavailable just because
its candidates are ineligible. Source mismatches instead return
`STALE_WORK_COORDINATION`, mark work unavailable, clear ready candidates/counts
and the top recommendation, and retain available mismatch evidence. Optional
triage enrichment must not swallow this failure. Cancellation and ordinary tool
failures keep their actual error identity instead of being labeled mismatches.

## Database-only workspaces

A workspace with no JSONL export retains its existing tool-backed behavior and
counts, explicitly marked `tool_reported_unverified` with no source receipt.
An export appearing during collection is rejected rather than silently mixing
the two modes. An unreadable, malformed, disappearing, or unstable export is
not a database-only fallback. Explicit source/program policies cannot be
satisfied through this unverified path.

## Scope

This change covers live adapter collection. Persisted SQLite `RuntimeWork`
readers still need source-receipt storage and read-time validation; the receipt
is not a claim that those readers are already protected. External Agent Mail
reservation and assignment evidence is not imported into this filter, and
strict policy fields are Go adapter options, not new CLI flags.

Tests cover immutable filtering, source changes, cross-project expected
identities, database-only behavior, export appearance, malformed JSONL,
cancellation, and the actual adapter `Collect` entry point with hermetic tool
fixtures. The focused helper tests can run with the real worksource package;
the full adapter surface test requires the repository's normal dependencies.
