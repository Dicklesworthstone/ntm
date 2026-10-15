# Indexed evidence in robot assignment context

Robot prompt enrichment uses the maintained CASS search-response decoder rather
than a second reduced response schema. This applies to robot sends and the
shared fresh-assignment path used by CLI assignments, bulk assignments, swarm
spawn assignments, and the session coordinator.

## What controls context selection

When a hit supplies `workspace`, project preference compares the complete
absolute indexed path with the current workspace. Clean-equivalent paths match;
basename matches, path prefixes, case-folded guesses, relative metadata and
malformed paths do not. An indexed different project overrides any suggestion
made by the transcript filename. Assignment enrichment binds this comparison
to the authoritative bead project even when a caller reuses filter settings
from another project; it does not mutate the caller's settings.

The project bonus also breaks ties after relevance scores saturate at 1.0.
Otherwise the highest-ranked foreign result could always remain ahead of local
history even when the configured project preference gave both the same final
score. Disabling the preference retains the original relevance ordering.

When supplied, `created_at` controls age filtering, recency scoring and rendered
age. The shared decoder accepts RFC3339 strings and supported numeric Unix
seconds/milliseconds. Moving or renaming a transcript does not override its
indexed timestamp. Missing timestamps retain filename-date fallback; malformed
timestamp data degrades the optional lookup instead of being injected.

A hit with absent or blank `content` uses its `snippet`. Full content takes
precedence when present. Both take the existing redaction, historical-data
framing and token-budget path before they reach an agent. Query JSON retains
the decoded workspace and timestamp so the selection can be inspected.

## Boundaries

Project preference is ranking, **not a cross-project exclusion or authorization
boundary**. Other projects' relevant results may still be included. Legacy
hits with no workspace metadata retain the older filename heuristic; that
heuristic is not proof of project identity. Paths are compared lexically,
without reading archived session paths or resolving symlinks on disk.

The standalone Cobra `ntm send` CASS implementation is separate and is not
changed by this robot-path update. Fresh assignment context is still recorded
before dispatch, and recovery replays that recorded payload rather than
retrieving new history. Operation cancellation propagates through robot CASS
retrieval and stops subsequent memory retrieval; canceled enrichment returns
the original task and explicit skip evidence.
