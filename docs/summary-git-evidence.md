# Git evidence in session summaries

Live `ntm summary` generation and the send/monitor paths using
`internal/summary.SummarizeSession` now retain a structured Git observation when
`IncludeGitDiff` is enabled. This is the existing summary engine, not a separate
command or a new summarizer.

The additive `git` JSON object contains `status`, `directory`, `observed_at`,
`entries`, and an optional safe `error`. Status is `clean`, `dirty`, `conflicted`,
or `unavailable`. An omitted object means inspection was not requested. Empty
entries with `unavailable` do not mean the checkout is clean. Directory identifies
the requested working directory; entry paths are relative to the repository root.

One `git --no-optional-locks status --porcelain=v1 -z` observation replaces three
independent diffs. Untracked files are enumerated individually, submodule changes
are not hidden by ignore configuration, and rename detection is enabled. This
also works before the first commit, when there is no HEAD to diff against.
Each entry retains Git's exact two-character status and, for renames/copies,
the original path. NUL-delimited filenames retain spaces, quotes, and newlines;
the detailed rendering quotes filenames rather than treating newlines as records.

The existing `files` list includes observed additions, modifications, deletions,
and conflicts. Renames appear as an old-path deletion and a new-path creation;
the raw status entry is the authoritative record of the rename. Deletion in the
working tree takes precedence in that display projection, while both index and
working-tree states remain available in the raw XY code. Agent-reported files
remain separate historical evidence and can describe earlier actions on the same
path. Git observations are **not** attribution of another user's edits to an agent.

## Handoff and rendering behavior

An unresolved conflict produces a blocked handoff with the affected paths and
an inspection/resolution next step, even if pane text says the task succeeded.
Dirty or unavailable Git state produces a partial handoff instead of a completed
one. A clean checkout does not erase explicitly pending tasks. Optional inspection
failure is not added to the agent's error list as though the agent caused it.

Brief and detailed renderings expose the observation before accomplishments.
LLM input includes it, and non-clean evidence is deterministically prefixed to
successful generated prose, so generated text cannot silently omit that notice.
The structured JSON and handoff findings retain the observation independently
of prose truncation. Existing stored summaries without Git evidence remain
readable; rendering does not rerun Git or claim a historical observation is fresh.

## Bounds and limits

Inspection uses the caller context and a ten-second command budget. Exceeding
the 16 MiB stdout limit cancels the child; partial/malformed/failed output is not
accepted as usable Git evidence. Pipe draining is bounded, and subprocess stderr
is not copied into summaries. Caller cancellation discards the summary rather
than becoming an optional-tool warning. The owner context is checked around
each Agent Mail thread request, before and after the optional LLM provider, and
before returning the final summary. A stopped owner cannot accept late provider
success or turn a provider failure into a successful fallback. An independent
provider timeout still permits fallback while the owner context remains live.
Providers must cooperate with the passed context; an implementation that ignores
cancellation is not forcibly terminated or detached into an unjoined goroutine.

This is an observation, not an atomic snapshot against concurrent editors. It
does not prove that tests passed, commits were pushed, or a task is complete.
Ignored files remain outside Git status, and resolved-but-uncommitted operations
are dirty rather than separately classified by operation name. The separate
robot synthesis engine is not changed. Component regression tests use real Git;
full CLI, native platform, and YAML serialization qualification are separate.

Git porcelain semantics: https://git-scm.com/docs/git-status#_porcelain_format_version_1
