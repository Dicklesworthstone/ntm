# PROPOSED_HYPERSYNC_SPEC__CODEX.md

Status: PROPOSED (final, revised)
Date: 2026-01-24
Owner: Codex (GPT-5)
Scope: Leader-authoritative, log-structured workspace replication fabric for NTM multi-agent workloads
Audience: NTM maintainers + HyperSync implementers

This document is the complete spec for the "converged" design from our conversation, with all previously identified flaws corrected and the best ideas from `PROPOSED_HYPERSYNC_SPEC__OPUS.md` folded in.

---

## Background Information for Context

This section exists to make this document fully self-contained for another model (or human) to evaluate the problem, constraints, and this spec's design decisions without needing any additional repo context, chat history, or tribal knowledge.

### A. What NTM Is (Named Tmux Manager)

NTM (Named Tmux Manager) is a Go CLI tool for orchestrating many AI coding agents inside tmux sessions. Its core behaviors are:

1) Session orchestration
   - Create a tmux session with a predictable pane grid.
   - Name panes using a deterministic convention (e.g., `myproj__cc_1`, `myproj__cod_3`).
   - Attach/detach safely; sessions persist across SSH disconnects.

2) Multi-agent spawning and control
   - Spawn many agents of different types (Claude Code, OpenAI Codex, Gemini CLI) across panes.
   - Send prompts to a subset by type (`--cc`, `--cod`, `--gmi`) or to all agents.
   - Interrupt agents (Ctrl+C) and monitor activity/health.

3) Rich local UX
   - Interactive dashboard and command palette for fast coordination.
   - Output capture tools (copy/save/grep/diff/extract).
   - Status detection and context window monitoring.

4) Machine-readable automation interface ("robot mode")
   - `--robot-*` flags return structured JSON for automation and agent-to-agent workflows.
   - Used by other tooling to inspect sessions/panes, tail outputs, send prompts, etc.

5) Integration with a broader multi-agent ecosystem
   - Agent Mail: multi-agent messaging + file reservations ("locks") + inbox/outbox, used to prevent agents clobbering each other.
   - Beads/bv: task triage and dependency-aware planning.
   - cass/cm: search and memory across previous agent sessions.

NTM’s reason for existence: it turns tmux into a "multi-agent command center" where dozens of concurrent agent processes can be orchestrated, compared, and managed with low friction.

### B. The User Workflow NTM Enables (Why This Is Hard)

This spec is motivated by an aggressive multi-agent development workflow:

- The user runs extremely high concurrency: sometimes 70+ Claude Code or Codex instances at once.
- All agents operate on the same codebase simultaneously.
- Critically: the user does NOT want separate worktrees that must later be merged.
  - Instead, agents work in one shared workspace so conflicts surface immediately.
  - The user resolves conflicts in real time (often using Agent Mail to coordinate).

This workflow is intentionally "high parallelism, high interaction": agents are expected to see each other’s changes quickly, and humans (or orchestrators) can intervene when hazards occur.

### C. The Current Problem (Why a Single Machine Still Fails)

Even with a very powerful leader machine (e.g., 512GB RAM, 64 logical cores, fast NVMe), very large numbers of agent processes can bring the machine to its knees.

Common failure modes at 70+ agents:
- CPU scheduler contention and tail latency spikes (interactive slowness even when average utilization looks fine).
- Huge process counts and occasional zombie processes (tooling + agent CLIs are not always clean under stress).
- Heavy terminal I/O and tmux server overhead (lots of scrollback output, pane capture, status polling).
- Disk I/O contention (agents running builds/tests, indexing, writing many files; also huge churn from caches).
- Network contention (agents streaming tokens and making API calls).
- Unpredictable emergent behavior from dozens of independent CLIs and their subprocesses.

In short: the machine may have enough RAM, but concurrency amplifies contention across CPU, I/O, process management, and interactive latency.

### D. The Desired Outcome (What “Just Works” Means)

We want to spread the compute load across multiple machines while preserving the user's core workflow:

1) Distribute agent processes onto multiple worker machines (Ubuntu 25.10 boxes via static IP).
2) Keep the user experience as if everything is happening on one machine.
3) Preserve the single shared workspace model:
   - any mutation made on one host appears on all others quickly enough to feel "instant"
   - no manual merge steps
4) Maintain correctness for real developer tools (especially git and lockfiles).

### E. Hard Constraints This Spec Must Satisfy

These constraints come directly from the workflow and our conversation:

1) Single leader, no additional server
   - The main machine is the leader and runs the show.
   - Workers are subordinate compute nodes.
   - We avoid introducing an extra "coordination server" beyond the leader.

2) Single shared workspace semantics (not worktrees)
   - We need a deterministic write order and a coherent view of the workspace.
   - Conflicts should be surfaced immediately, not hidden.

3) Coordination uses Agent Mail concepts
   - Agent Mail provides advisory file reservations used by agents/humans to avoid stepping on each other.
   - HyperSync must integrate with this rather than inventing an incompatible system.
   - But Agent Mail reservations are human-level coordination; they are not sufficient for POSIX correctness by themselves.

4) No "old tech" sync (no NFS/rsync as the core solution)
   - Traditional file sync and network filesystems either:
     - give eventual consistency (too weak), or
     - provide inconsistent/fragile behavior under heavy concurrency, or
     - are operationally heavy and not aligned with the goal of a modern, controllable fabric.

5) Ultra-fast propagation on modern hardware
   - Exploit NVMe (log-structured writes, async IO, batching).
   - Exploit fast networks (QUIC, erasure coding, backpressure-aware fanout).
   - Scale with cores (parallel chunk hashing, parallel apply when safe).

6) No silent divergence
   - If the leader is unreachable, workers must not accept local writes that would later "merge".
   - The safe behavior is read-only replication mount until reconnection.

7) Keep the NTM repo a pure Go project
   - NTM itself remains Go (Go toolchain only).
   - The HyperSync daemon (`hypersyncd`) may be implemented separately (Rust preferred), integrated by NTM at runtime.

### F. Why “Total-Order Op Log + Deterministic Replay” Is Non-Negotiable

To emulate a single shared workspace, the system must pick a single global ordering of mutations:

- If two agents write overlapping regions, the outcome must be defined by a deterministic order (like a single kernel scheduling writes).
- If a rename happens concurrently with a write, all machines must observe the same ordering and atomicity.
- Git and many build tools assume strong local filesystem semantics (especially around lockfiles and unlink semantics).

Therefore, the leader must serialize mutations into a single op log. Workers must replay that log deterministically.

This is the core "physics" the entire system rests on.

### G. Why Worker-Side Interception Is Required

Agents run on workers. The bytes are produced on workers. Therefore:

- The system must capture mutation payloads at the workers.
- Leader-side detection alone (e.g., eBPF on the leader) can only see leader-local writes.

Worker-side interception is mandatory for correctness.

### H. Why QUIC + RaptorQ + Content Addressing

HyperSync splits transport into two layers:

1) Control plane (QUIC reliable streams)
   - Idempotent mutation intents and commit acknowledgements.
   - Catch-up, snapshots, status reporting.
   - Needs reliability and ordering semantics.

2) Data plane (RaptorQ symbols, preferably via QUIC DATAGRAM)
   - Efficient fanout and loss tolerance when distributing chunk payloads.
   - RaptorQ is especially valuable when scaling to many workers because it reduces retransmission complexity and can tolerate packet loss without request/response storms.

Content addressing (BLAKE3 chunk hashes) enables:
- deduplication across repeated writes and shared files
- integrity verification on receipt
- efficient snapshot representation (manifest references chunk hashes)

### I. Agent Mail vs HyperSync Locks (Two Different Things)

It is important to distinguish:

- Agent Mail reservations:
  - Human/agent-level coordination mechanism ("I am working on internal/foo.go").
  - Used to avoid conflicts and to surface hazards.
  - Generally path-pattern based; advisory.

- HyperSync distributed locks:
  - Required for correctness of real tools (git lockfiles, flock/fcntl patterns, unlink semantics).
  - Must be leader-authoritative, deterministic, and enforced by the filesystem layer.

This spec integrates both:
- HyperSync provides correctness locks for the OS/tooling layer.
- Agent Mail provides coordination and hazard surfacing for the multi-agent workflow layer.

### J. Why Caches Must Be Local (Avoid Recreating the Problem)

Agents generate enormous file churn in caches (build caches, module caches, language server caches, package manager caches). Replicating caches across all machines would:
- saturate network bandwidth
- saturate NVMe writes
- dominate the op log and chunk store
- harm interactive latency and increase tail latencies

Therefore, this spec explicitly defines a "replicated workspace" namespace and a "local scratch" namespace, and expects NTM to route caches into local scratch by default.

### K. What This Spec Provides (At a Glance)

HyperSync provides:
- A transparent mount that agents use as their working directory.
- A leader-authoritative op log with canonical commit ordering.
- A chunk store for payload transfer and deduplication.
- QUIC + RaptorQ replication with bounded memory and backpressure.
- Snapshots, catch-up, and GC to keep the system bounded and operable.
- Hazard surfacing integrated with Agent Mail reservations.
- A scheduling framework to place agents across workers under stability constraints.

In the rest of the document, we specify the exact semantics, wire protocol, failure behavior, and integration surface required to build HyperSync correctly.

---

## 0. Executive Summary

HyperSync is a leader-authoritative, log-structured, content-addressed distributed workspace fabric. It enables many AI coding agents to run across multiple worker machines while maintaining single-workspace semantics: the shared workspace behaves like one machine with a single global ordering of writes, renames, unlinks, etc.

HyperSync is not "diff then rsync". It is an event-sourced filesystem state machine:

- All filesystem mutations are serialized by a single leader into an append-only op log.
- Workers intercept mutations where they occur (FUSE mount), forward them to the leader, and block until the leader commits the mutation (so the syscall's effect has a globally ordered commit index).
- The leader replicates committed log entries and the required content to all workers using QUIC (reliable control plane) and RaptorQ (loss-tolerant data plane).
- Workers deterministically replay the op log, producing identical filesystem state given the same log prefix.

Key properties:
- Correctness core: leader total-order op log + deterministic replay (not eventual consistency)
- Transparent to agents: a mount point is the shared workspace (no agent changes required)
- Data plane: content-addressed chunks (BLAKE3) + RaptorQ symbol stream for fanout and loss tolerance
- Control plane: QUIC reliable streams (idempotent intents, commit acks, catch-up)
- Conflict surfacing: Agent Mail reservations + hazard marking for unreserved conflicts
- Modern-hardware aware: NVMe-first, io_uring batching, mmap/hugepage optimizations optional

---

## 1. Goals and Non-Goals

### 1.1 Goals
1) Single-workspace semantics across many hosts, with a single global write order
2) Immediate surfacing of conflicts (hazards) aligned with Agent Mail reservations
3) Scale to 70+ agent processes without the leader becoming the bottleneck
4) Deterministic replayable state for audit/debug (op log + Merkle roots)
5) Exploit modern hardware (NVMe, high core counts, high RAM, fast LAN/WAN)

### 1.2 Non-Goals
1) Byzantine fault tolerance (leader is trusted)
2) Multi-leader consensus (single leader by design)
3) Perfect emulation of every POSIX corner case (we explicitly define supported semantics)
4) "Offline local edits that later merge" (no silent divergence; leader unreachable -> read-only)

---

## 2. Glossary and Notation

- Leader (L): single authoritative node that orders and commits all mutations
- Worker (W_i): node running agents; hosts a FUSE mount; replays leader log
- Client: an agent process performing filesystem operations on a worker
- Op log: ordered sequence of committed mutation operations Op[1..N]
- S_k: filesystem state after applying Op[1..k]
- a_i: highest log index applied by worker W_i (worker's applied index)
- commit_index: leader's latest committed log index
- IntentID: (client_id, seq_no), unique per client; used for idempotency
- client_id: stable identity for an initiating process. Recommended: Agent Mail name + worker id (e.g., BlueLake@fmd) plus a per-process nonce if needed.
- NodeID: stable identifier for a filesystem object (inode-like; survives rename)
- Chunk: content blob (<= 64 KiB) addressed by BLAKE3 hash

Notation:
- MUST/SHOULD/MAY are used in RFC-style normative sense.

---

## 3. Workspace Namespace and Policy (Critical for Performance)

Replicating everything including caches/build artifacts will recreate the same "knees" problem, just distributed. HyperSync therefore defines an explicit namespace split:

- `/ntmfs/ws/<workspace>`: replicated, leader-authoritative workspace (correctness-critical)
- `/ntmfs/local/<workspace>`: local-only scratch (NOT replicated)

Implementation note:
- `/ntmfs/ws/<workspace>` is the HyperSync FUSE mount.
- `/ntmfs/local/<workspace>` is a normal local directory created on each host (not part of the replicated mount).

NTM SHOULD set common cache env vars for agents to point into `/ntmfs/local/<workspace>/cache/...` (per worker), e.g.:
- `XDG_CACHE_HOME`
- `GOMODCACHE`, `GOCACHE`
- `npm_config_cache`, `PIP_CACHE_DIR`, etc.

This preserves single-workspace semantics for source files while keeping high-churn caches off the replication path.

Optional: `hypersync.ignore` patterns MAY additionally exclude paths from replication, but excluded paths are, by definition, not part of the single-workspace semantics. If enabled, NTM MUST surface this prominently in UI/robot output.

---

## 4. System Architecture

### 4.1 Components

Worker side (each W_i):
- FUSE filesystem mounted at `/ntmfs/ws/<workspace>` (mutation interception)
- Worker daemon:
  - Sends intents to leader (QUIC)
  - Uploads missing chunks to leader (QUIC)
  - Receives committed log entries and data (QUIC + RaptorQ)
  - Applies log entries to local backing store and cache
  - Tracks applied index a_i and reports lag

Leader (L):
- Authoritative op log:
  - Monotonic log index
  - Mutation-only entries (no logging of read/open/stat/close)
  - Idempotency via IntentID
- Content-addressed chunk store (BLAKE3 -> bytes)
- Merkle DAG / root:
  - Canonical Merkle root after each committed log index
  - Used for snapshot integrity, audit, and catch-up validation
- Replication engine:
  - Control plane: QUIC reliable streams (entries, catch-up, acks)
  - Data plane: RaptorQ symbols over QUIC DATAGRAM (preferred) or QUIC unidirectional stream (fallback)
- Scheduler:
  - Chooses worker placement for new agents via Thompson Sampling with explicit stability constraints
- Lock + hazard engine:
  - Implements distributed advisory locks required for correctness (flock/fcntl)
  - Integrates Agent Mail reservations for human-visible coordination/hazards

### 4.2 Leader-Local Requirement (No Bypass)

The leader machine MUST also access the workspace only through the HyperSync mount (leader runs a local worker instance). Any edits outside `/ntmfs/ws/<workspace>` are out-of-fabric and MUST be considered invalid for correctness.

---

## 5. Formal Consistency Model

### 5.1 State Vector

Filesystem state S includes:
- Directory tree: mapping (dir NodeID, name) -> NodeID (including hardlinks)
- Per-node metadata: type, mode, uid, gid, size, xattrs
- Canonical timestamps: mtime, ctime set by leader commit timestamp (see 9.4)
- File content mapping: NodeID -> ordered extents referencing chunk hashes
- Advisory lock state (see 10): lock table state is part of replicated state
- Optional: reservation state digest included in Merkle root (see 11)

Host compatibility requirement:
- For POSIX permission semantics to be meaningful, workers SHOULD share consistent uid/gid mappings (same numeric IDs). HyperSync replicates numeric uid/gid values; name mapping is a host concern.

### 5.2 Core Invariants
1) Total Order: leader assigns each committed mutation a unique log index k (strictly increasing)
2) Prefix Replay: worker W_i's state equals S_{a_i}
3) Atomicity: each Op[k] is applied atomically (no partial visibility of that op)
4) Determinism: applying the same log prefix yields identical state across workers

### 5.3 Linearization Points

Mutation linearization point:
- A mutation op is linearized at leader commit time when it is appended durably to the op log as index k.

Read semantics:
- Default mode: a read on worker W_i returns from S_{a_i} (may be stale relative to leader)
- Read-your-writes: the initiating client MUST block until a_i >= k for its own committed mutation k
- Strict reads (optional): read ops MAY block until a_i >= commit_index_at_read_start for stronger semantics

This yields:
- Default: sequential consistency across the cluster + read-your-writes for each client
- Optional strict reads: linearizable reads relative to the leader's commit_index snapshot

### 5.4 Happens-Before Relations (Examples)

- write(f, x) -> write(f, y) implies seq_x < seq_y
- write(f, x) -> rename(f -> g) implies seq_write < seq_rename
- writes before fsync(f) are durable before fsync returns success
- operations across different files are ordered by leader commit order (the op log)

---

## 6. Syscall-Level Contract (What Returns When)

HyperSync is a distributed filesystem; correctness depends on the syscall contract being explicit.

### 6.1 Mutations vs Non-Mutations

Logged (mutations, forwarded to leader, globally ordered and leader-commit-gated):
- create, mkdir, rmdir
- write, pwrite, truncate, ftruncate
- rename (including replace semantics)
- unlink
- chmod, chown
- link, symlink
- setxattr, removexattr
- fsync, fdatasync (barriers)
- flock/fcntl lock operations (see 10)

Not logged (served locally from S_{a_i} and worker caches):
- open/close (not logged; lease-coordinated with leader; see 6.7)
- read, pread
- stat, lstat, readdir

Open/close note (important):
- open/close are NOT part of the op log, but HyperSync MUST still coordinate them with the leader for correctness of unlink semantics and distributed locks (see 6.7 and 10).

### 6.2 Return Semantics (Default Mode)

For every logged mutation M:
- The worker MUST NOT make M visible to the calling process until the leader commits M at log index k (or returns an error).
- The worker MUST return success to the syscall only after it receives CommitAck(k) from the leader.
- After CommitAck(k), the worker MUST apply Op[k] locally before returning, so read-your-writes holds immediately on the same worker.

This is the core correction: mutation syscalls are leader-commit-gated.

### 6.3 Error Semantics and Partitions (No Silent Divergence)

If the leader is unreachable:
- `/ntmfs/ws/<workspace>` MUST become read-only (writes return EROFS).
- Reads MUST continue from the last applied state S_{a_i}.
- The worker daemon MUST attempt reconnection and then catch up (see 13).
- NTM MUST surface this state (sync_lag, read_only=true) in UI and robot output.

### 6.4 fsync/fdatasync Semantics

Fsync/Fdatasync are barriers:
- The leader MUST durably persist all prior mutations affecting that file NodeID before acknowledging fsync success.
- The worker MUST block fsync until the leader acks.

### 6.5 mmap Semantics (Decision)

V1 decision:
- mmap writes are DISABLED by default for correctness and simplicity.
- If enabled, HyperSync MUST treat msync/munmap/close as flush points:
  - dirty ranges are captured and forwarded as ordinary write ops
  - HyperSync MUST block msync until those writes are committed

Rationale: mmap without msync has weak durability guarantees anyway; requiring msync is acceptable.

### 6.6 O_DIRECT Semantics

O_DIRECT MAY bypass FUSE in some configurations; this is hazardous.

V1 decision:
- Worker FUSE layer MUST strip O_DIRECT and return a warning in logs/metrics.
- Optional future: FUSE passthrough for reads + intercepted writes where supported.

### 6.7 Open/Close Leases (Correct Unlink + Git-Style Workloads)

Why this exists:
- Unlink-on-open semantics (common in Unix tooling) require that a file's data remains accessible via open handles after its last directory entry is removed.
- A multi-host workspace must not delete file content early when another worker still has an open handle.

V1 rules:
- open() is served by the worker, but it MUST acquire a leader-issued lease:
  - Worker -> Leader: OpenLeaseRequest {path, flags, client_id, seq_no, pid_hint}
  - Leader -> Worker: OpenLeaseResponse {handle_id, node_id, open_at_index}
- Worker MUST NOT return open() success to the caller until it has applied at least open_at_index (a_i >= open_at_index), to avoid "open succeeded but local state is missing" anomalies.
- close() MUST release the lease:
  - Worker -> Leader: CloseLease {handle_id}
- Leader MUST treat a NodeID as deletable only when:
  - link_count == 0 AND open_lease_count == 0 AND no locks are held

open(O_CREAT/O_EXCL) handling:
- If open() includes O_CREAT, the leader MUST treat it as a mutation:
  - atomically create the path if absent (assigning a new NodeID) and then return an open lease
  - log the create as an op log entry (leader-commit-gated like all mutations)
- If O_EXCL is also set and the path exists at leader commit time, the leader MUST return EEXIST.

Performance note:
- The open lease RPC is on the QUIC control plane and should be cheap; the leader may batch or cache path->node resolution internally.

---

## 7. Identity Model (NodeID, Paths, Hardlinks)

### 7.1 NodeID

Each filesystem object has a stable NodeID (128-bit random) assigned by the leader at creation. NodeID persists across rename. This is required to make rename/write ordering unambiguous.

Lifetime rules:
- NodeID is created at create/mkdir/symlink/etc.
- NodeID remains live while it has at least one directory entry (link_count > 0) OR at least one open lease (see 6.7).
- Unlink removes a directory entry and decrements link_count, but does not necessarily delete the NodeID.

### 7.2 Directory Entries

Directory tree is represented as:
- (dir NodeID, name) -> NodeID

Hardlinks:
- Multiple directory entries may point to the same NodeID.
- The leader maintains link count as metadata.

### 7.3 File Content Representation

Each file NodeID maps to an ordered set of extents:
- extent = {offset, len, chunk_hash}

Chunks may be <= 64 KiB (default max). Smaller chunks are allowed (for unaligned and small writes).

---

## 8. Op Log (Mutation-Only) and Idempotency

### 8.1 Log Entry Schema (Normative)

Each committed entry MUST include:
- log_index (u64, monotonic)
- op_id (UUID)
- committed_at (RFC3339, leader time; used for canonical timestamps)
- intent_id: (client_id, seq_no) (idempotency key)
- origin_worker_id
- origin_agent_name (for hazard attribution)
- op (one of the mutation operations)
- hazard (optional, see 11)
- merkle_root (hash after applying this op)

### 8.2 Idempotency Rules (Mandatory)

Retry-safe behavior MUST be guaranteed:
- The pair (client_id, seq_no) uniquely identifies an intent.
- The leader MUST dedupe duplicate intents and return the SAME CommitAck (same log_index and op_id).
- Workers MUST retry with the same (client_id, seq_no) if they are unsure whether the intent committed.

This prevents double-apply under packet loss or client retries.

Resource-allocation idempotency:
- The leader SHOULD also dedupe OpenLeaseRequest and LockRequest by (client_id, seq_no) to avoid leaking handles/locks on retry.

---

## 9. Payload Transfer: Chunking, Upload, and Verification

This section fixes the most critical missing piece: the leader must receive bytes, not only hashes.

### 9.1 Chunking

- Default max chunk size: 64 KiB
- Hash: BLAKE3(bytes)

Write buffers are split into chunks of up to 64 KiB. Chunks may be smaller (e.g., last chunk, unaligned ranges, very small writes).

### 9.2 Inline Small Writes (Optimization)

For small writes, HyperSync MAY inline data in the WriteIntent to reduce round-trips:
- If total payload <= INLINE_THRESHOLD (default 8 KiB), include bytes directly in the intent.
- Otherwise, use the upload handshake below.

### 9.3 Upload Handshake (Required)

Control plane (QUIC reliable stream):
1) Worker -> Leader: WriteIntentHeader
   - intent_id, op metadata, list of (chunk_hash, chunk_len)
   - optional inline bytes for small writes

2) Leader -> Worker: ChunkNeed
   - bitset/list of chunk_hashes the leader does NOT have

3) Worker -> Leader: ChunkPut stream (QUIC reliable)
   - frames: {chunk_hash, chunk_len, bytes}
   - leader verifies BLAKE3 matches

4) Leader commits the op only after all needed chunks are present and verified, then returns CommitAck(log_index=k).

This is the strict correctness path.

### 9.4 Canonical Metadata and Timestamps

To make replicas identical, timestamp assignment MUST be canonical:
- committed_at from the leader is the canonical time for mtime/ctime updates induced by Op[k].
- Workers MUST set file mtime/ctime to the leader committed_at for that op.

If exact host-ctime semantics are required by a tool, it MUST run on a single host; HyperSync intentionally defines canonical leader timestamps instead.

### 9.5 Wire Messages (Sketch; Required Fields)

This is a concrete-enough wire sketch to prevent "hand-wavy" implementation drift. Exact encodings (protobuf/flatbuffers/bincode) are an implementation detail, but these fields are REQUIRED.

Idempotency key used across all mutation intents:
- intent_id = (client_id, seq_no)

Control plane (QUIC reliable streams):
- OpenLeaseRequest:
  - path, flags, client_id, seq_no, pid_hint
- OpenLeaseResponse:
  - handle_id, node_id, open_at_index
- CloseLease:
  - handle_id

- WriteIntentHeader:
  - intent_id
  - handle_id (preferred) OR path (for truncate-by-path, etc.)
  - op_type (WRITE/TRUNCATE/RENAME/UNLINK/etc.)
  - offset, len (as applicable)
  - chunks: list of {chunk_hash, chunk_len}
  - inline_bytes (optional; present iff payload <= INLINE_THRESHOLD)
  - reservation_context (optional; if present):
    - project_key
    - reservation_id (or ids)
    - holder_agent_name
    - exclusive (bool)
    - expires_at (optional)

- LockRequest:
  - client_id
  - handle_id OR node_id
  - lock_kind (flock_shared, flock_exclusive, fcntl_read, fcntl_write, unlock)
  - range (optional for fcntl): {start, len}
- LockResponse:
  - status (granted/denied)
  - holder (if denied)

- ChunkNeed:
  - intent_id
  - missing_hashes[] (may be empty)

- ChunkPut:
  - chunk_hash, chunk_len, bytes

- CommitAck:
  - intent_id
  - op_id
  - log_index
  - committed_at
  - merkle_root
  - hazard (optional)

- LogEntry (leader -> worker):
  - log_index, op_id, committed_at
  - op (mutation-only)
  - merkle_root
  - hazard (optional)

- WorkerApplied (worker -> leader; periodic):
  - applied_index (a_i)
  - read_only (bool)
  - missing_chunks_count
  - local_pressure (cpu, mem, disk)

Data plane (RaptorQ symbols over QUIC DATAGRAM preferred):
- ChunkSymbolFrame:
  - chunk_hash
  - block_id (encoding instance id)
  - symbol_id
  - symbol_bytes

Catch-up:
- ChunkFetchRequest (optional optimization):
  - hashes[]
- SnapshotRequest:
  - target_index (or "latest")
- SnapshotManifest:
  - snapshot_index
  - merkle_root
  - manifest_hash (content-addressed)
  - manifest_bytes (or chunk refs)

---

## 10. Advisory Locks (Correctness for Real Tools) and Agent Mail

### 10.1 Why Locks Matter

Programs like git rely on advisory locks and O_EXCL lockfiles. A multi-host shared workspace without a distributed lock story will corrupt .git state and/or create heisenbugs.

Therefore:
- HyperSync MUST implement a leader lock manager for flock/fcntl semantics on the mounted workspace.
- Agent Mail reservations remain the human-level coordination mechanism for agent behavior (who should edit what).

### 10.2 HyperSync Lock Manager (Normative)

Lock operations are treated as replicated state:
- The leader is authoritative for lock acquisition/release.
- Workers forward lock ops and block until leader acks.
- Locks are released on disconnect (worker lease timeout) to avoid deadlock.

This provides correctness for git/index.lock and similar patterns.

### 10.3 Agent Mail Reservations (Hazards, Not Hard Blocks)

Agent Mail reservations are checked and attached to mutation intents as ReservationContext:
- If the mutating client holds a reservation covering the path, the op is considered reserved.
- If not, the leader may still commit the op but MUST perform hazard detection (11) and notify.

Optional enforcement mode (off by default):
- If enabled, the leader MAY reject unreserved mutations with EACCES/EPERM to force reservation discipline.

---

## 11. Hazard Detection (Conflict Surfacing)

HyperSync surfaces, not hides, cross-agent conflicts.

### 11.1 Hazard Types (Minimum Set)
- OverlappingWrite: two unreserved writes overlap in byte range on the same file NodeID
- ConcurrentRename: rename conflicts with other rename/write on same NodeID in a small window
- WriteAfterUnlink: write to a NodeID that was unlinked (path no longer references it) without reservation

### 11.2 Detection Algorithm (Practical, Deterministic)

Leader maintains per NodeID a small rolling window of recent unreserved mutations (e.g., last 256 ops or last 5 seconds). On committing a new unreserved mutation, it checks for overlaps/conflicts within that window and, if found:
- marks the op with hazard metadata referencing the conflicting log_index
- emits an Agent Mail message to both involved agents (high importance)
- increments hazard counters for UI/robot output

Hazards do not block commits by default; they are surfaced immediately.

### 11.3 Merkle Root Includes Hazard/Reservation Digest (Integrity)

Each committed Merkle root SHOULD incorporate:
- file tree state hash
- hazard state digest (recent hazards)
- reservation digest (cached reservation state)

This makes "what state are we in?" auditable.

---

## 12. Replication: QUIC Control + RaptorQ Data Plane

### 12.1 QUIC Streams (Control Plane)

Each worker maintains a QUIC connection to the leader with at least:
- intents stream (worker -> leader): WriteIntentHeader + other mutation intents
- acks stream (leader -> worker): CommitAck + errors
- log stream (leader -> worker): committed log entries (metadata only)
- control stream (bidirectional): heartbeats, config, status, applied index reports

### 12.2 RaptorQ Data Plane (Chunks)

RaptorQ is used for loss tolerance and efficient fanout. V1 uses per-chunk encoding:
- symbol_size default: 1280 bytes (fits common MTU with overhead)
- repair_overhead default: 0.10 (10% extra symbols)

Preferred transport: QUIC DATAGRAM for symbols (unordered, no HOL blocking).
Fallback: QUIC unidirectional stream per worker (still works, less ideal).
Optional LAN optimization:
- If the network supports reliable multicast routing inside a single L2/L3 domain, the data plane MAY additionally emit symbols via multicast to reduce leader fanout overhead. This is strictly optional; QUIC unicast remains the default.

Design targets (not hard guarantees):
- Healthy LAN: commit->apply p50 <= 10ms, p99 <= 100ms
- Healthy WAN: commit->apply p50 <= 50ms, p99 <= 500ms

### 12.3 Apply Rules on Workers

Workers receive log entries in order. For each entry:
- If all referenced chunks are present locally, apply immediately.
- If chunks missing, worker may:
  - wait for arriving RaptorQ symbols (passive), and/or
  - request targeted catch-up for missing chunks (active; see 13)

Workers MUST NOT apply Op[k] until all required chunks are verified.

### 12.4 Replication Backpressure

Leader MUST bound memory:
- per-worker inflight log window (default 1000 entries)
- per-worker symbol emission rate based on observed decode progress and a_i lag

Workers MUST periodically report:
- applied index a_i
- decode backlog (missing chunk count)
- local disk/memory pressure

Leader MAY temporarily reduce replication rate or deprioritize a badly lagging worker (while still keeping correctness once it catches up).

Lagging worker policy (V1 decision):
- If a worker's lag exceeds MAX_LAG_ENTRIES (default 2,000,000) OR MAX_LAG_TIME (default 30m),
  - leader MAY stop streaming per-chunk symbols for that worker and instead require snapshot-based catch-up (13),
  - leader MUST continue accepting that worker's applied index reports and allow it to recover without impacting the rest of the cluster.

---

## 13. Snapshots, Bootstrap, Catch-Up

### 13.1 Snapshot Definition

A snapshot at log index k is:
- snapshot_index = k
- merkle_root = root_k
- a compact manifest sufficient to reconstruct S_k:
  - directory tree nodes + per-node metadata
  - per-file extent lists (chunk hashes + offsets)

Snapshots are content-addressed and stored in the chunk store as well.

### 13.2 When to Use Snapshots

Worker catch-up policy:
- If (commit_index - a_i) <= CATCHUP_LOG_THRESHOLD_ENTRIES (default 200000): replay log entries 1-by-1
- Else: transfer snapshot at a recent checkpoint index and then replay remaining tail

### 13.3 Snapshot Transfer

Snapshot transfer uses QUIC reliable streams (not RaptorQ) by default for determinism and simplicity, but MAY use RaptorQ for very large snapshot manifests.

### 13.4 Integrity

After applying snapshot + tail ops, worker MUST validate that its computed merkle_root equals leader-provided root at the target index.

---

## 14. Retention and Garbage Collection (Bounding Disk)

### 14.1 Retention Policy (V1 Decision)

Leader retains:
- the full op log for at least RETAIN_LOG_HOURS (default 72h) OR last RETAIN_LOG_ENTRIES (default 10M), whichever larger
- periodic snapshots every SNAPSHOT_INTERVAL (default 5 minutes) and retains last RETAIN_SNAPSHOTS (default 288 ~= 24h)

### 14.2 Chunk GC

Chunk store is bounded by reachability from retained snapshots + current head:
- The leader maintains per-chunk refcounts derived from file extents in retained roots.
- When a file's extent mapping changes, removed chunk references decrement refcount.
- Chunks with refcount==0 and not referenced by any retained snapshot MAY be deleted by GC.

GC MUST be incremental to avoid stalls (background task).

---

## 15. Scheduling (Leader) - Thompson Sampling with Explicit Metrics

### 15.1 Placement Objective

Goal: minimize expected pain (latency + resource contention + sync lag) while enforcing a stability constraint.

Each worker has a latent cost distribution C_i updated online. The leader selects worker by Thompson Sampling:
- sample c_i ~ posterior(C_i)
- choose worker with minimal sampled c_i among stable workers

### 15.2 Stability Constraint

Define stability in measurable terms (V1 decision):
- Each worker reports:
  - cpu_utilization (EWMA)
  - memory_pressure (EWMA)
  - sync_lag_ms (p95)
  - agent_count
  - leader_commit_rtt_ms (p95 for intents)
- A worker is stable if:
  - cpu_utilization < 0.85 AND
  - memory_pressure < 0.80 AND
  - sync_lag_ms < 50ms AND
  - agent_count < max_agents (config)

The "rho < 0.8" concept is kept as intuition, but V1 uses concrete measured thresholds because true M/G/k parameters are not directly observable in this workload.

### 15.3 Explainability (Alien-Artifact UX)

For each placement decision, leader SHOULD emit an evidence ledger (debug mode):
- sampled cost per worker
- top contributing penalties (cpu, mem, lag, rtt)
- why workers were excluded (unstable constraint)

---

## 16. Observability and UX

Leader exposes metrics:
- commit_index
- per-worker a_i, sync lag, inflight windows
- chunk store size, GC progress
- hazard counts and recent hazards
- lock manager state (counts, contention)

NTM dashboard/robot output SHOULD surface:
- leader reachable/read_only flags
- per-worker sync lag
- hazards (active + recent)
- reservations summary (Agent Mail)

### 16.1 NTM Integration Surface (Config + CLI + Robot)

Configuration (TOML; NTM-side):
```toml
[hypersync]
enabled = true
role = "leader" # or "worker"

[hypersync.leader]
bind_address = "0.0.0.0:7890"
state_dir = "/var/lib/ntm/hypersync"

[hypersync.replication]
symbol_size = 1280
repair_overhead = 0.10
max_inflight_entries = 1000

[hypersync.snapshots]
interval = "5m"
retain = 288
catchup_log_threshold_entries = 200000

[hypersync.scheduler]
max_agents_per_worker = 40
max_sync_lag_ms = 50
max_commit_rtt_ms = 200
```

CLI (proposed):
- `ntm hypersync init --workers fmd,yto,css` (bootstrap leader+workers)
- `ntm hypersync status` (show leader, workers, lag, hazards)
- `ntm hypersync log --tail 50` (recent committed ops + hazards)
- `ntm hypersync snapshot` (force snapshot)

Robot (proposed):
- `ntm --robot-snapshot` already exists; extend to include hypersync fields:
  - `hypersync.leader_reachable`, `hypersync.commit_index`, `hypersync.workers[]`, `hypersync.hazards[]`

---

## 17. Phase 0 Profiling (Extreme Optimization Discipline)

Before building anything large, we MUST measure real agent I/O patterns on a representative workload:
- write size distribution
- write frequency
- rename/unlink frequency
- fsync frequency
- mmap usage rate
- paths with highest churn (likely caches/build artifacts)

This phase determines:
- INLINE_THRESHOLD
- chunk size
- snapshot interval
- exclude/local cache strategy and NTM env defaults

High-leverage optimization levers (apply only after profiling proves hotspots):
- io_uring batching for chunk store reads/writes and snapshot transfers
- memory-mapped chunk store with hugepage-backed arenas for hot content
- batched commit acks (amortize syscall overhead) while preserving per-mutation commit ordering
- adaptive RaptorQ symbol rate control based on per-worker decode backlog
- lock manager fast path (in-memory leases) to keep git-style short locks cheap

---

## 18. Security

V1 decision:
- QUIC connections use mutual TLS between leader and workers (or an equivalent strong token scheme).
- All chunks are verified by hash at ingress and before apply.

---

## 19. Failure Modes

- Leader failure/unreachable: workers flip replicated mount to read-only (EROFS) and continue serving reads from last applied state.
- Worker failure: other workers continue; failed worker catches up on reconnect via snapshot/log replay.
- Lagging worker: may fall behind; leader bounds buffers and uses snapshot catch-up on reconnect or when lag exceeds thresholds.

---

## 20. Implementation Phases (Concrete)

Phase 0: Profiling and trace capture (mandatory)
Phase 1: Single-host fabric (leader+local worker) with:
  - mutation gating by leader commit
  - mutation-only op log
  - chunk upload handshake
  - Merkle root per commit
Phase 2: Add 1 remote worker:
  - replication streams
  - deterministic replay + integrity check
Phase 3: Add RaptorQ data plane:
  - symbol emission, decode, rate control
Phase 4: Add snapshots + GC:
  - bootstrap, catch-up, bounded storage
Phase 5: Add lock manager + Agent Mail hazard integration:
  - correctness for git locks + human-visible hazards
Phase 6: Scheduling + NTM integration:
  - host pool config, placement, dashboard/robot metrics

---

## 21. Deliverables (Repository/Tooling Friendly)

To keep NTM as a pure Go project:
- `hypersyncd` (daemon + FUSE filesystem) is a separate component (Rust preferred for perf/async I/O + reuse of asupersync/RaptorQ).
- NTM (Go) integrates with hypersyncd via CLI/robot commands and config.
- Tests:
  - Deterministic golden-replay test suite for HyperSync (in hypersyncd repo)
  - NTM integration tests remain Go (`go test ./...`)

---

End of spec.
