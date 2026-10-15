# Project-aware context retrieval

Fresh assignment enrichment now retrieves project candidates as well as global
history. Previously, same-project preference could only re-rank the first few
global results: a relevant project session outside that limit never reached the
ranking engine.

This applies to the shared robot prompt-context pipeline: robot sends and fresh
CLI, bulk, spawn, and coordinator assignments. Assignment scope still comes from
the authoritative bead project, not a reused caller filter or the current shell.

## Retrieval policy

When CASS is requested and both the query and filter enable same-project
preference, an absolute, control-character-free current workspace enables two
searches: one global and one with CASS's `--workspace` filter. Both use the same
query keywords, age restriction, agent filters, per-search result limit, shared
client timeout, and maintained response decoder. There are at most two concurrent
searches per enrichment, not a query per hit or per agent type. A nonpositive
candidate limit uses five results per scope.

Global results remain eligible. The additional candidates pass through the
existing relevance, indexed-age, project-preference, formatting, redaction, and
prompt-budget machinery. This is a retrieval/ranking preference, not a
cross-project authorization or exclusion boundary. Without a usable workspace,
or with preference disabled, only the global query runs. Disabled CASS, an empty
keyword query, and a context window already at its injection threshold launch no
search.

The workspace query is not trusted blindly: its hits must carry a matching full
indexed workspace path. Missing, relative, prefix-only, and conflicting project
identities are not promoted to local history. Exact archive/agent/positive-line
identities are deduplicated; two messages without line evidence are not silently
collapsed. A global result with explicit conflicting workspace metadata is never
relabeled as local just because a scoped query returned the same archive line.

## Failure and lifecycle behavior

Both searches share one timeout rather than each consuming a sequential full
budget. Caller cancellation joins both searches and discards their candidates.
If only an optional search fails or times out, the other source remains usable.
A warning identifies the unavailable source or rejected identity evidence without
logging task text, historical content, or subprocess stderr. If neither source
succeeds, the original task is preserved with a skipped-enrichment receipt.

The shared CASS executor retains at most 32 MiB of stdout and 64 KiB of stderr.
Exceeding either capture budget cancels the child and returns a typed output-limit
error; partial JSON is never treated as successful history. Pipe draining retains
its existing two-second bound. This is not a process-group supervisor and does
not promise to terminate arbitrary detached descendants of a custom CASS wrapper.

Fresh assignments still record the final enriched prompt before dispatch.
Recovery replays that recorded prompt; it does not run these searches again.
Memory injection continues through the existing context-aware CM pipeline.

## Scope and qualification

The separate ordinary `ntm send` enrichment implementation in
`internal/cass/inject.go` and standalone robot CASS query helpers are not migrated
by this change. The active shared prompt-context path uses the maintained client;
this does not claim every legacy helper uses the new candidate policy.

Unit tests cover scope filtering, deterministic merge order, exact-ID deduplication,
unknown identities, argument boundaries, shared budgets, partial failures, and
cancellation. Subprocess tests exercise the real executor against deterministic
CASS-shaped fixtures, including a local hit absent from the limited global query.
Native installed-CASS and full tmux/assignment integration qualification remain
separate from these component tests.
