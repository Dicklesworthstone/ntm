# Reservation pattern overlap

The coordinator compares reservation path sets, not one glob against the text
of another glob. For example, `src/*/main.go` and `src/service/*.go` overlap at
`src/service/main.go` even though neither pattern matches the other pattern's
literal spelling. No file needs to exist yet for this conflict to be detected.

The shared `internal/reservationpath` matcher supports slash-separated,
case-sensitive patterns with the Go `path.Match` grammar (`*`, `?`, character
classes, `[^...]` negation, and backslash escapes), plus recursive `**`.
`**/` can consume zero or more directories; it does not consume just part of a
basename. Directory-qualified single stars never cross `/`. Unqualified globs
such as `main*.go` apply to basenames at any depth. Literal reservations protect
the exact path and its subtree, so `src` also protects `src/service/main.go`.
The spelling `[!a]` is a Go class containing `!` and `a`, not shell negation.

There are two distinct operations. `MayOverlap` compares two reservation
patterns. `Matches` compares a reservation with a concrete filename, whose
`*`, `?`, or bracket characters are not reinterpreted as a second glob. This
separation matters for staged files and exact path queries. Inputs are not
trimmed by the matcher: leading and trailing whitespace are filename bytes.

The matcher explores pairs of finite-state-machine positions rather than
expanding wildcards or walking the repository. Work is bounded by pattern
length, visited state pairs, and character-range comparisons. Invalid UTF-8,
NULs, malformed patterns, oversized inputs, and exhausted analysis budgets are
conservatively treated as possible overlap, never as evidence that work is
safe to run concurrently. Empty inputs remain non-reservations. Such uncertain
cases may produce false-positive conflict warnings and require inspection.

The matcher only determines the path relation. Same-agent reservations, shared versus exclusive
ownership, expiration, release state, existing coordinator opt-in flags, and
notification cooldowns retain their own checks. A possible overlap does not
authorize an automatic release, acquire a reservation, or replace the server's
atomic reservation checks. Paths are compared as supplied; this does not
resolve symlinks, filesystem case aliases, or separate copies of a repository.

Agent Mail's mutual-conflict check uses the same pattern intersection. The
staged-file guard uses concrete membership instead: it blocks for any active
exclusive holder other than the committer, without treating the staged path as
a glob or as a directory reservation. Missing expiry evidence is treated as
active, matching the coordinator, while a known past expiry or explicit release
still retires the reservation. The remote acquisition, renewal and exact-ID/path
release operations are unchanged.
