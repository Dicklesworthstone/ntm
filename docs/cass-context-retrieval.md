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
budget. In the context-aware robot/assignment pipeline, caller cancellation joins
both searches and discards their candidates.
If only an optional search fails or times out, the other source remains usable.
A warning identifies the unavailable source or rejected identity evidence without
logging task text, historical content, or subprocess stderr. If neither source
succeeds, the original task is preserved with a skipped-enrichment receipt.

The shared CASS executor retains at most 32 MiB of stdout and 64 KiB of stderr.
Exceeding either capture budget cancels the child and returns a typed output-limit
error; partial JSON is never treated as successful history. Pipe draining defaults
to two seconds; ordinary send retains its prior one-second drain bound through
the same executor's `WaitDelay` setting. This is not a process-group supervisor and does
not promise to terminate arbitrary detached descendants of a custom CASS wrapper.

Fresh assignments still record the final enriched prompt before dispatch.
Recovery replays that recorded prompt; it does not run these searches again.
Memory injection continues through the existing context-aware CM pipeline.

## Ordinary send integration

`ntm send --with-cass` now uses the maintained, output-bounded search client
instead of a separate subprocess runner and reduced JSON decoder. Snippet-only
results contribute context; nonblank full content wins when both are present.
Indexed timestamps drive age filtering, recency scoring, rendered age and the
`cass_injection.sources` age receipt rather than a renamed archive's date.
Workspace metadata is retained and overrides filename guesses whenever a caller
supplies a project preference. Saturated relevance ties preserve the configured
project bonus; disabling it preserves the original order of equal-score hits.

Retrieval keywords and historical content pass through the existing real
redaction engine. All three formats retain the historical-data framing and
existing post-redaction prompt budget. Query failures preserve the original
task, report a safe failure class and never inject partial captured JSON.
An uninitialized index remains an optional empty-context state with an explicit
skip reason; it does not mask another scope's output-limit or decoding error.

Ordinary send now resolves the target session's authoritative project before
requesting project candidates. This applies to inferred sessions as well as
explicit names; the caller's current directory never supplies project preference.
The existing strict resolver groups panes by their physical repository, so linked
worktrees retain their shared project identity. When the session project cannot
be established locally, including remote tmux sessions, send retains global
history and reports a warning instead of guessing a workspace. Disabling project
preference avoids this extra resolution and the scoped search.

The command context now owns both searches. Canceling send or reaching its parent
deadline cancels and joins retrieval, discards partial candidates, and returns a
failed send without beginning prompt delivery. The optional CASS lookup still has
its own configured timeout: when only that timeout expires, send preserves its
existing graceful-degradation behavior and may deliver the original task. These
changes do not migrate the standalone robot CASS query helpers. Fresh-assignment
replay remains unchanged.

## Scope and qualification

Unit tests cover scope filtering, deterministic merge order, exact-ID deduplication,
unknown identities, argument boundaries, shared budgets, partial failures, and
cancellation. Subprocess tests exercise the real executor against deterministic
CASS-shaped fixtures, including a local hit absent from the limited global query.
Native installed-CASS and full tmux/assignment integration qualification remain
separate from these component tests.

Ordinary-send adapter tests exercise the real client, subprocess executor,
decoder and redaction implementation. They cover indexed evidence, snippet/full
content precedence, scoped and global policy, partial failures, both output caps,
disabled/full-window skips, all formatting paths, and the inherited-pipe timeout.
Command-level tests run the ordinary send handler and real CASS executor with a
recording tmux transport. They verify target-project selection from another
same-named workspace, disabled preference, unknown-project fallback, and caller
cancellation/deadline during both in-flight searches with no prompt delivery.
