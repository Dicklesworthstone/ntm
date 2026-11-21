# ADR-0001: HyperSync Distributed Filesystem Fabric Deferred Out of Core NTM Scope

- **Status:** Accepted (Disposition Recorded)
- **Date:** 2026-11-20
- **Supersedes / Disposes:** `docs/planning/PROPOSED_HYPERSYNC_SPEC__CODEX.md`, `docs/planning/PROPOSED_HYPERSYNC_SPEC__OPUS.md`

## Context

Early planning documents proposed **HyperSync**, a leader-authoritative, log-structured, content-addressed distributed FUSE filesystem fabric (`hypersyncd`) intended to let coding agents on multiple worker machines share one POSIX directory tree with serialized global write ordering.

## Decision

HyperSync is **explicitly out of scope for NTM's core Go single-binary repository** and is deferred as a potential independent companion project rather than an unimplemented NTM subsystem.

## Rationale

1. **System Boundary Mismatch:** HyperSync requires a privileged/kernel-coupled FUSE daemon (`libfuse`), strict writeback-cache refusal, cross-host `open`/`unlink`/`flock` brokers, and BLAKE3 content-addressed replication. NTM's core value proposition is a zero-daemon, pure-Go local control plane over `tmux`.
2. **Existing Practical Multi-Agent Isolation:** NTM already addresses concurrent agent editing safely via two shipped primitives:
   - **MCP Agent Mail advisory file reservations** (`ntm locks`, reservation watcher, routing conflict warnings).
   - **Reviewed Git worktree isolation** (`ntm spawn --worktrees`, `ntm worktrees merge --dry-run` using `git merge-tree --write-tree` and crash-recovery journaling).
3. **Honest Documentation:** Keeping speculative FUSE specs unmarked in `docs/planning/` without a disposition record creates false expectations during audits. This ADR formally records that `docs/planning/PROPOSED_HYPERSYNC_SPEC__*.md` are historical research artifacts, not planned NTM v1.x deliverables.

## Consequences

- No `hypersync` flags, config keys, or partial packages are introduced into `ntm`.
- If a standalone `hypersyncd` project is ever built externally, NTM can integrate with it as an optional external tool (like `br`, `bv`, `cass`, `dcg`, `pt`) via `internal/tools` without embedding a FUSE filesystem in `ntm`.
