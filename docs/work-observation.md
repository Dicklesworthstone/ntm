# Live verified-work observations

`ntm work-snapshot` supports continuous observations and a readiness wait. Both
use the existing `adapters.CollectDurableWork` query, not the legacy status or
snapshot display rows. They do not assign work, send prompts, acquire leases,
repair a tracker, or reserve a candidate for the caller.

## Stream work as JSON Lines

```sh
ntm work-snapshot --project=/data/projects/myapp --watch --refresh --interval=2s
```

Each line is a normal robot response with `project`, `work`, and `observation`.
The latter includes a process-local `sequence`, the number of collection
`samples` attempted, `checked_at`, and `terminal`. Check `success` before using
`work`. A failed read emits `success:false` and `work:null`; it never republishes
an earlier ready list. Ordinary source failures do not end a watch. The next
iteration can report recovery if the requested source becomes readable again.

`checked_at` dates completion of this observation, not original collection of
the cached candidates. The verification's `cache_collected_at` and
`cache_expires_at` remain unchanged on cache reuse. Sequence numbers are not
persistent event-log cursors and do not support replay across command restarts.

Ctrl-C, SIGTERM, or cancellation of the command context stops collection and
emits a terminal `CANCELLED` response when output is writable. An output failure
stops the command without another write or another collection. Reads and writes
are sequential: backpressure does not create a queue of stale samples.

## Wait for enough eligible candidates

```sh
ntm work-snapshot --project=/data/projects/myapp --wait-ready=2 --refresh \
  --limit=1 --interval=2s --wait-timeout=5m
```

This emits one terminal response when at least two canonical candidates pass
the current verifier, even though only one candidate is displayed. It requires
`verification.count_scope=verified_candidates`, a bound canonical source, and
consistent `verified_ready`/summary counts. Tool-reported or preview-only totals
cannot satisfy the condition. Existing dependency, gating, program, mutex-batch,
and observed reservation filtering remain the verifier's responsibility.

`--require-reservations` also requires an observed Agent Mail reservation read.
Without it, the verifier's normal graceful-degradation policy applies; inspect
`work.verification.reservations` to distinguish observed ownership from an
unavailable optional service. Neither setting replaces the atomic claim and
reservation checks required before dispatch. Unmapped path reservations are
not a promise that a candidate's complete future file set is conflict-free.

A successfully observed but insufficient count continues waiting. Source,
storage or read failures stop a ready wait with an error instead of being
misrepresented as an empty queue. Missing optional reservation evidence in the
strict mode continues waiting. A wait deadline emits `TIMEOUT` with no stale
work payload. Cancellation classification is retained in returned Go errors as
well as in the JSON envelope.

## Timing, scope, and refresh

`--timeout` bounds each work query (default 10 seconds, maximum one minute).
`--wait-timeout` bounds the overall ready wait (default five minutes, maximum
24 hours). `--interval` is a pause after each completed query and output, from
100 milliseconds to 30 seconds. Slow queries do not cause catch-up bursts.
State-store opening/migration and a blocked output writer retain their existing
synchronous behavior; the query context cannot forcibly interrupt those calls.

`--refresh` is explicit permission to collect replacement evidence on **every**
iteration. Without it, an existing source mismatch or corrupt snapshot remains
an error, not permission to execute tracker tools and replace the saved record.
Missing/expired snapshots use the durable query's normal live-collection path.
Use `--refresh` to follow tracker source changes during a watch or wait; omit it
to reuse a fixed candidate observation with fresh eligibility/reservation checks.

A long-lived command resolves and pins its project directory at startup. Changing
an input symlink cannot redirect it. If the pinned directory disappears or is
replaced, observation terminates; it does not follow a new project at that path.
These checks are not an atomic filesystem snapshot or a defense against every
hostile concurrent rename.

The default one-shot command is unchanged. `--watch` and `--wait-ready` are
mutually exclusive. Legacy `--robot-status` and `--robot-snapshot` work-row
consumers still require their separate migration; this command does not silently
alter their schemas or claim their counts are now source-verified.
