# PROPOSED_HYPERSYNC_SPEC__CODEX.md

Status: PROPOSED (rev 5; correctness + robustness + implementability + perf revisions)
Date: 2026-01-26
Owner: Codex (GPT-5)
Scope: Leader-authoritative, log-structured workspace replication fabric for NTM multi-agent workloads
Audience: NTM maintainers + HyperSync implementers

SpecVersion: 0.5
ProtocolVersion: hypersync/1
Compatibility: Linux-only V1 (see 0.1); macOS support is explicitly deferred.

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

### E.1 V1 Explicit Simplifying Assumption (User/UID Model)
This workflow is not multi-tenant. V1 explicitly optimizes for "one human user + many agent subprocesses".

**Normative (V1):**
- All processes accessing `/ntmfs/ws/<workspace>` on all nodes MUST run under the same effective UID and primary GID.
- If a different effective UID is observed on the mount, hypersyncd MUST:
  - either refuse the operation with `EACCES` and emit a loud error, OR
  - refuse to start unless `multi_user=true` is explicitly enabled (V2 feature; NOT required in V1).

Rationale:
- Supplementary groups are not reliably available to a FUSE daemon in a portable way.
- Making multi-UID correctness "accidentally best-effort" is a correctness trap; fail-fast is safer.

### F. Why "Total-Order Op Log + Deterministic Replay" Is Non-Negotiable

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

## 0.1 Assumptions, Guarantees, and Explicit Deviations (Alien Artifact Contract)

This section is normative. If an implementation cannot satisfy a MUST here, it MUST refuse to start (fail-fast) rather than silently degrade.

### 0.1.1 Assumptions (Required Environment)
1) Platform
   - V1 is Linux-only.
   - FUSE3 is required (kernel FUSE + libfuse3 or equivalent).
   - The backing filesystem on each host MUST be case-sensitive and POSIX-like (ext4/xfs strongly recommended).

2) Time
   - Leader timestamps are canonical for replicated metadata. Workers MAY have skew.
   - The leader MUST ensure committed_at is monotonic non-decreasing (see 9.4).

3) Identity
   - All nodes MUST agree on a WorkspaceID (128-bit random) to prevent cross-wiring.
   - client_id MUST be globally unique per client lifetime (see 2, 8.2).

4) Failure/availability model
   - Single leader only. No consensus.
   - Leader may crash/restart. Workers may crash/restart.
   - Network may partition.

5) Kernel/userspace interface constraints (Linux/FUSE correctness prerequisites)
   - The replicated workspace MUST be mounted via a mechanism that allows userspace to:
     - synchronously gate mutation visibility on a remote commit decision, AND
     - actively invalidate kernel caches (inode data, dentries, attributes) when remote commits apply.
   - For Linux V1, this implies:
     - FUSE3 with support for notify invalidations (notify_inval_inode/notify_inval_entry).
   - If the platform/kernel/libfuse combination cannot provide these primitives, hypersyncd MUST refuse to start.

### 0.1.2 Guarantees (What HyperSync Provides)
1) No silent divergence
   - If the leader is unreachable, the replicated mount MUST become read-only (writes return EROFS).
   - HyperSync MUST NOT accept or "queue" new mutations while leader-unreachable.

2) Mutation commit semantics
   - A logged mutation syscall returns success iff the leader has durably committed the op into the op log and has verified all required payload bytes (6.3, 9.3).
   - The mutation's linearization point is the leader's commit at log_index k (5.3).

2.1) Commit-gated visibility (kernel-visible effects)
   - For any syscall classified as a "logged mutation" in this spec, the calling process MUST NOT observe the effects of that mutation (via reads, readdir, stat, open, mmap, etc.) until the leader has committed it and the worker has applied it.
   - If hypersyncd cannot enforce this due to kernel caching behavior (e.g., writeback caching acknowledging write() before userspace sees it), it MUST refuse to start.

3) Deterministic replay
   - Given the same log prefix and chunk bytes, workers MUST converge to identical state for all replicated metadata and file contents (5.2).

4) Durability meaning (important)
   - "Durable" means "persisted to the leader's stable storage (WAL/oplog + chunk store)".
   - Workers MAY crash and lose local materialization; they MUST recover by catch-up from leader (13).

### 0.1.3 Explicit Deviations (Intentional, Documented Differences vs local POSIX)
1) atime
   - atime is NOT replicated and MUST NOT affect Merkle roots (avoids write amplification).

2) mmap writes
   - MAP_SHARED writable mmap is DISALLOWED in V1 by default (6.6). MAP_PRIVATE remains allowed.

2.1) mmap read coherence (explicit)
   - MAP_SHARED PROT_READ mmaps are permitted, but **coherence with remote writes is NOT guaranteed** unless the worker implements and the kernel honors page-cache invalidation for that mapping.
   - Portable rule for users/tools: to observe remote writes reliably, reopen/remap after the worker applies the corresponding commit.

3) fcntl range locks
   - V1 does not guarantee full POSIX fcntl byte-range semantics (10). Unsupported lock operations MUST return ENOTSUP.

4) Advisory lock persistence
   - Advisory lock state is runtime-only and is NOT persisted across leader restart (consistent with typical single-host crash behavior).
   - Locks are NOT part of deterministic filesystem replay state S_k and are NOT included in Merkle roots or snapshots (10).

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
- Client: a Linux process performing filesystem operations through the mount (often an agent or its subprocess).
- ProcKey: stable process identity on a worker: (pid, start_time_ticks) from `/proc/<pid>/stat` (prevents PID reuse bugs).
- MountInstanceID: 128-bit random nonce generated by hypersyncd at startup; changes when hypersyncd restarts.
- Op log: ordered sequence of committed mutation operations Op[1..N]
- S_k: filesystem state after applying Op[1..k]
- a_i: highest log index applied by worker W_i (worker's applied index)
- commit_index: leader's latest committed log index
- IntentID: (client_id, seq_no), unique per client; used for idempotency
- client_id: globally unique identity for an initiating client lifetime.
  - In V1, client_id MUST be derived from: (worker_id, mount_instance_id, proc_key) to preserve per-process program order and avoid head-of-line coupling across unrelated processes.
  - client_id MUST be globally unique for the lifetime of a ProcKey under a MountInstanceID.
  - client_id SHOULD include human-readable fields for observability (agent_name, worker_id), but uniqueness MUST NOT rely on them.
- seq_no: u64 strictly increasing per client_id; MUST NOT reset or wrap within a client_id lifetime.
- NodeID: stable identifier for a filesystem object (inode-like; survives rename)
- InodeNo: u64 inode number assigned by the leader at node creation; used as st_ino on all workers for determinism.
- HandleID: u64 identifier for an open file handle (per worker mount); used for lock ownership and release-on-close semantics.
- Chunk: content blob (<= 64 KiB) addressed by BLAKE3 hash
- WorkspaceID: 128-bit random identifier for one replicated workspace instance; prevents cross-workspace replay accidents.
- LeaderEpoch: u64 random identifier generated at leader startup; changes on leader restart; used to detect restart and reset ephemeral leases (locks/open-leases).

Notation:
- MUST/SHOULD/MAY are used in RFC-style normative sense.

### 2.1 Client Identity + Sequencing (Normative, Implementability-Critical)
This section makes the (client_id, seq_no) contract implementable for a Linux FUSE mount shared by many processes.

**2.1.1 client_id derivation (V1 REQUIRED)**
- On each FUSE request, hypersyncd MUST compute ProcKey = (pid, start_time_ticks).
- client_id MUST be computed as a stable 128-bit value:
  - client_id = BLAKE3_128("hypersync/client" || workspace_id || worker_id || mount_instance_id || pid || start_time_ticks)
- This ensures:
  - no cross-process idempotency collisions,
  - no PID reuse collisions,
  - no coupling between unrelated processes on the same worker.

**2.1.2 seq_no rules (V1 REQUIRED)**
- hypersyncd MUST maintain an in-memory seq_no counter per client_id.
- For a given client_id, seq_no MUST increment by 1 for each transmitted logged mutation or lock intent.
- hypersyncd MAY garbage-collect per-client counters when it observes the process has exited.

**2.1.3 Ordering guarantee**
- For any single client_id, hypersyncd MUST NOT concurrently transmit two logged mutation intents without assigning a total order (seq_no).
- The leader MUST commit intents for the same client_id in seq_no order.
  - If the leader receives seq_no=n+1 without having committed or rejected seq_no=n, it MUST buffer or reject with EAGAIN (implementation choice), but MUST NOT commit out of order.

Rationale:
- Preserves single-host "program order" as observed by the worker kernel.
- Avoids artificial head-of-line blocking between unrelated processes by not using a single per-worker client_id.

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
- NOTE: Advisory lock state and open-file leases are runtime coordination state. They are NOT part of S_k, are NOT replayed, and are NOT included in fs Merkle roots or snapshots (10, 6.8).

Host compatibility requirement:
- For POSIX permission semantics to be meaningful, workers SHOULD share consistent uid/gid mappings (same numeric IDs). HyperSync replicates numeric uid/gid values; name mapping is a host concern.

### 5.2 Core Invariants
1) Total Order: leader assigns each committed mutation a unique log index k (strictly increasing)
2) Prefix Replay: worker W_i's state equals S_{a_i}
3) Atomicity (scoped, realistic):
   - Metadata ops (create/mkdir/rmdir/rename/unlink/link/symlink/xattrs/chmod/chown) MUST appear atomic with respect to namespace traversal on each worker.
   - Content ops (write/truncate) follow POSIX regular-file semantics; implementations MUST NOT promise "all-or-nothing visibility" beyond what a single-host POSIX filesystem provides.
   - Cross-worker visibility is stepwise at op boundaries once Op[k] is applied; workers MUST NOT apply Op[k+1] before Op[k].
4) Determinism: applying the same committed log prefix (and referenced chunk bytes) yields identical replicated state across workers

### 5.2.1 Canonicalization Rules (Required for Determinism)
These rules are normative; without them, "deterministic replay" is underspecified.

1) Directory enumeration order (readdir)
   - `readdir` results MUST be presented in strict bytewise lexicographic order of entry names (memcmp over raw bytes).
   - This order MUST be stable across workers and independent of backing filesystem enumeration order.

2) Xattr ordering
   - xattr name/value sets MUST be stored and hashed in bytewise lexicographic order of xattr name.

3) Extent normalization
   - After applying any content mutation, a file's extent list MUST be normalized deterministically:
     - extents sorted by offset ascending
     - extents MUST NOT overlap
     - adjacent extents MAY be merged only if they are exactly contiguous and reference the same chunk_hash AND the merge decision is deterministic (i.e., merge whenever possible).

4) Inode numbers
   - All workers MUST report st_ino = InodeNo assigned by the leader at node creation.
   - InodeNo MUST be unique within a workspace and MUST NOT be reused within RETAIN_LOG_HOURS (to avoid confusing long-lived clients).

5) Symlink bytes
   - Symlink targets are treated as opaque bytes; no normalization (no path cleaning) is performed.

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

Leader-authoritative control-plane operations (NOT in the op log; still leader-ack gated):
- flock/fcntl lock operations (see 10)
- open-file lifetime leases (open-leases) used for safe unlink+GC behavior (6.8, 14.2)

Extended attribute constraints (V1):
- Maximum xattr value size: 64 KiB (XATTR_MAX_VALUE_SIZE)
- Maximum xattr name length: 255 bytes (XATTR_MAX_NAME_LEN)
- Maximum total xattr size per inode: 1 MiB (XATTR_MAX_TOTAL_SIZE)
- setxattr operations exceeding these limits MUST return ENOSPC or E2BIG.
- The leader enforces these limits; workers trust leader validation.

Rationale:
- ext4 default xattr limit is 64KB; xfs supports larger.
- Standardizing on 64KB ensures cross-worker compatibility.
- Total per-inode limit prevents abuse via many small xattrs.

Not logged (served locally from S_{a_i} and worker caches):
- open/close (not logged; see 6.8)
- read, pread
- stat, lstat, readdir

Open/close note (important):
- open/close are NOT part of the op log, but HyperSync MUST still coordinate them for correctness of unlink semantics and distributed locks (see 6.8 and 10).

### 6.1.1 Unsupported/Explicitly-Handled Syscalls (V1)
This list is normative to make implementation and testing unambiguous.

MUST be supported (either as logged mutations or local reads):
- openat, mkdirat, unlinkat, renameat, linkat, symlinkat (same semantics as their non-*at variants)
- rename replace semantics (POSIX rename)

MUST return ENOTSUP in V1 unless explicitly implemented and tested:
- renameat2 flags other than "replace" semantics (e.g., RENAME_EXCHANGE, RENAME_NOREPLACE) unless leader implements them correctly
- fallocate / FALLOC_FL_* (unless implemented as logged mutation with deterministic semantics)
- copy_file_range, reflink/clone ioctls, fiemap, fs-verity ioctls
- mknod (device/special files)

If a syscall is not supported, the returned errno MUST be ENOTSUP (preferred) or ENOSYS, consistently across workers.

### 6.2 Freshness Barriers (Prevent stale-path anomalies)

Workers may serve reads from S_{a_i}, but path-resolving mutations MUST NOT execute against stale state, or correctness becomes user-visible (ENOENT/EEXIST surprises).

Definitions:
- barrier_index: the leader commit_index value that the worker considers current at syscall start.

Normative rule for choosing barrier_index:
- barrier_index MUST be the worker's last-observed commit_index from the leader's control/log stream at the moment the worker begins handling the syscall.
- If the worker has not received any leader heartbeat or commit_index update within LEADER_STALE_MS (default 250ms on LAN; configurable), the worker MUST issue a BarrierRequest (9.5) to refresh commit_index before choosing barrier_index.

Rules:
1) For any logged mutation specified by path (e.g., create/mkdir/rename/unlink/chmod/chown/setxattr on a path):
   - The worker MUST ensure a_i >= barrier_index before submitting the intent to the leader.
   - If a_i < barrier_index, the worker MUST block the syscall until caught up.
   - If the leader becomes unreachable while waiting, the worker MUST fail the syscall with EROFS and flip the mount read-only (6.4).

2) For FD-based mutations (write/pwrite/ftruncate/flock/fcntl):
   - The worker SHOULD ensure it has applied at least the barrier_index that was current when the FD was opened (strict mode).
   - If strict mode is disabled, FD-based ops MAY proceed against S_{a_i} as long as they are still commit-gated (6.3).

Implementation note:
- Workers already receive commit_index via the replication/control stream. A dedicated BarrierRequest RPC (9.5) MAY be used when the worker suspects it is missing leader progress due to a transient gap.

### 6.3 Return Semantics (Default Mode)

For every logged mutation M:
- The worker MUST NOT make M visible to the calling process until the leader commits M at log index k (or returns an error).
- The worker MUST return success to the syscall only after it receives CommitAck(k) from the leader.
- After CommitAck(k), the worker MUST ensure it has applied all ops up through k locally before returning (a_i >= k), so read-your-writes holds immediately on the same worker.

This is the core correction: mutation syscalls are leader-commit-gated.

### 6.3.2 FUSE Caching and Visibility Rules (Normative)
To satisfy 0.1.2(2.1), the V1 Linux/FUSE implementation MUST enforce:

1) No writeback caching
   - The replicated mount MUST NOT use kernel writeback caching modes that allow write() to return before hypersyncd processes the write.
   - In libfuse terms: writeback_cache MUST be disabled.

2) Direct I/O for write-capable handles
   - For any open handle that grants write capability (O_WRONLY or O_RDWR), the worker MUST set DIRECT_IO for that handle.
   - Rationale: prevents kernel page-cache from exposing uncommitted writes and prevents MAP_SHARED PROT_WRITE.

3) Cache invalidation on apply (required if any kernel caching is enabled)
   - When applying a committed op that affects:
     - file data: worker MUST invalidate cached data for that inode (notify_inval_inode for affected range; whole-file invalidation is acceptable in V1).
     - directory entry set: worker MUST invalidate the relevant parent directory entry cache (notify_inval_entry) and attributes.
   - If the worker cannot successfully issue invalidations, it MUST fall back to attr_timeout=0 and entry_timeout=0 behavior (no kernel attr/dentry caching) OR refuse to start.

### 6.3.1 Batch Commit (Performance Under Load)

At high intent rates, individual commits become the bottleneck. The leader MUST implement batch commit:

Batch parameters (configurable):
- BATCH_WINDOW_MS (default 1ms): maximum time to accumulate intents before committing
- BATCH_MAX_OPS (default 100): maximum ops per batch
- BATCH_MAX_BYTES (default 1MB): maximum total payload per batch

Batch commit rules:
1) The leader MAY delay CommitAck for up to BATCH_WINDOW_MS to accumulate multiple intents.
2) A batch commits when ANY of the following triggers:
   - BATCH_WINDOW_MS elapsed since first intent in batch
   - BATCH_MAX_OPS intents accumulated
   - BATCH_MAX_BYTES payload accumulated
   - An fsync intent arrives (forces immediate flush)
3) All intents in a batch are assigned sequential log_index values.
4) A single WAL fsync durably commits the entire batch.
5) Workers receive CommitAck for their intent only after the batch is durable.

Group commit invariants:
- Intents within a batch are ordered by arrival time at the leader.
- Two intents from the same client MUST be committed in seq_no order.
- Hazard detection operates on the committed batch, not individual intents.

Telemetry (required):
- batch_size histogram (ops per batch)
- batch_latency_ms histogram (time from first intent to commit)
- batch_bytes histogram (payload bytes per batch)
- forced_flush_count counter (fsync-triggered early commits)

Backpressure:
- If the leader's pending intent queue exceeds MAX_PENDING_INTENTS (default 10000),
  the leader MUST reject new intents with EAGAIN and per-worker rate limiting kicks in.

### 6.4 Error Semantics and Partitions (No Silent Divergence)

If the leader is unreachable:
- `/ntmfs/ws/<workspace>` MUST become read-only (writes return EROFS).
- Reads MUST continue from the last applied state S_{a_i}.
- The worker daemon MUST attempt reconnection and then catch up (see 13).
- NTM MUST surface this state (sync_lag, read_only=true) in UI and robot output.

In-flight mutation ambiguity (explicit deviation):
- If a worker loses connectivity after transmitting an intent (and possibly chunk bytes) but before receiving CommitAck, the worker MAY be unable to know whether the intent committed.
- V1 MUST implement IntentStatusRequest/Response (9.5) to resolve this ambiguity upon reconnection.
- If the worker cannot resolve intent status within MUTATION_DEADLINE (configurable; default 30s), it MUST return EIO to the syscall and flip mount read-only.

### 6.4.1 Apply Failures (Disk Full, IO Errors, Permission Mismatch)
Workers may fail to apply committed ops due to local resource limits. This MUST NOT silently corrupt or diverge state.

Rules (normative):
1) If a worker cannot apply a committed op Op[k] (e.g., ENOSPC, EIO), it MUST:
   - stop advancing a_i at k-1,
   - flip the mount to read-only (EROFS for new mutations),
   - surface a terminal error state in WorkerApplied (9.5) including the failing log_index and errno,
   - continue serving reads from S_{a_i} (best-effort) unless the local materialization is itself corrupted.
2) A worker in this state MUST recover only by operator action (free disk / fix config) and then snapshot/log catch-up (13).

### 6.5 fsync/fdatasync Semantics

Fsync/Fdatasync are barriers:
- The leader MUST durably persist all prior mutations affecting that NodeID (file or directory) before acknowledging fsync success.
- The worker MUST block fsync until the leader acks.

Directory fsync (required for real tools):
- fsync() on a directory NodeID MUST act as a barrier for prior namespace mutations affecting that directory (create/unlink/rename entries in that directory).
- This is required for crash-safe atomic replace patterns that do (write temp -> fsync temp -> rename -> fsync dir).

### 6.6 mmap Semantics (Decision + Enforcement)

V1 decision:
1) MAP_SHARED writable mmap is DISALLOWED by default.
   - Reason: MAP_SHARED writes become locally visible via page cache before HyperSync can leader-commit-gate them.

2) Enforcement mechanism (normative for Linux/FUSE):
   - For any open() that grants write capability (O_WRONLY or O_RDWR), the worker MUST use DIRECT_IO semantics for that file handle (disables shared mmap by default in the kernel FUSE path).
   - The worker MUST NOT enable any "allow shared mmap with direct_io" capability flag.

3) Allowed mmap:
   - Read-only mmap (PROT_READ) is allowed.
   - MAP_PRIVATE writable mmap is allowed (does not mutate the underlying file).

Optional future (not V1):
- A V2 MAY support MAP_SHARED writable mmaps only if it can guarantee leader-commit gating of visibility, which is not currently achievable with standard FUSE semantics without kernel support.

### 6.7 O_DIRECT Semantics

O_DIRECT MAY bypass FUSE in some configurations; this is hazardous.

V1 decision:
- Worker FUSE layer MUST strip O_DIRECT and return a warning in logs/metrics.
- Optional future: FUSE passthrough for reads + intercepted writes where supported.

### 6.8 Open/Close Semantics (No per-open leader RPC in V1)

Why this exists:
- open/unlink/rename must behave sanely under Unix tooling (git, compilers, editors) without making the leader an "open() RPC server".

V1 rules (normative):
1) open() without creation/truncation is served locally:
   - open(O_RDONLY) and open(O_RDWR/O_WRONLY without O_TRUNC/O_CREAT) are handled on the worker using S_{a_i}.
   - For path-resolving opens, the worker SHOULD apply freshness barriers in "strict_open" mode:
     - If strict_open=true (default for correctness), the worker MUST wait until a_i >= barrier_index before returning open().

2) open() that implies a mutation is treated as a mutation:
   - open with O_CREAT and/or O_TRUNC MUST be translated into a logged mutation intent:
     - CreateIntent (with O_EXCL handled atomically at the leader)
     - TruncateIntent (for O_TRUNC)
   - The worker MUST NOT return open() success until those mutation(s) are committed and applied locally (a_i advanced).

3) Unlink-on-open behavior (correctness requirement) via Open-Leases (no per-open gating)
   POSIX requires: after unlink, the bytes remain accessible through any open FD until last close.

   V1 mechanism: per-worker per-NodeID "open-lease" state (control-plane; NOT in op log).
   - Each worker maintains a local open refcount per NodeID (derived from FUSE open/release).
   - When refcount transitions 0 -> 1, the worker MUST asynchronously send OpenLeaseAcquire(node_id) to the leader.
   - When refcount transitions 1 -> 0, the worker MUST asynchronously send OpenLeaseRelease(node_id) to the leader.
   - The leader associates open-leases with (worker_id, leader_epoch) and applies a lease TTL (OPEN_LEASE_TTL_MS, default 15000ms).
   - Workers MUST renew active open-leases every ttl/3; if renewals stop (disconnect/crash), the leader may expire that worker's leases after TTL.

   GC safety rule:
   - The leader MUST NOT delete orphaned content (link_count==0) while ANY worker holds an open-lease for that NodeID.
   - This preserves correct unlink-on-open semantics without turning open() into a leader RPC.

4) close() is local:
   - close()/release decrements local open refcounts.
   - close()/release MUST also trigger best-effort cleanup:
     - if a HandleID holds advisory locks, the worker MUST send LockRelease(handle_id, node_id) (10.2).
     - if NodeID refcount hits 0, worker sends OpenLeaseRelease(node_id).

Rationale:
- This avoids turning the leader into a high-QPS open()/close() authority while still preserving unlink-on-open safety and bounded GC behavior.

---

## 7. Identity Model (NodeID, Paths, Hardlinks)

### 7.1 NodeID

Each filesystem object has a stable NodeID (128-bit random) assigned by the leader at creation. NodeID persists across rename. This is required to make rename/write ordering unambiguous.

Inode number determinism:
- At creation, the leader MUST also assign InodeNo (u64) and replicate it as immutable node metadata.
- Workers MUST report st_ino = InodeNo for all stat-like results.

### 7.1.1 Symlink Handling (Cross-Boundary Safety)

Symlinks require special handling because they can reference paths outside the replicated workspace:

Rules (normative):
1) Symlink creation (symlink syscall) is logged like other mutations.
2) Symlink targets are stored verbatim (relative or absolute paths).
3) The leader does NOT validate symlink targets at creation time.
4) Resolution happens at access time, on the accessing worker.

Cross-boundary behavior:
- Symlinks to paths outside `/ntmfs/ws/<workspace>` will resolve to local paths on each worker.
- This MAY produce different results on different workers (intentional; consistent with single-host symlink semantics).
- Symlinks to `/ntmfs/local/<workspace>` are explicitly allowed (useful for cache shortcuts).

Warning:
- NTM SHOULD emit a warning when agents create symlinks with absolute paths outside the workspace.
- This is advisory only; HyperSync does not enforce path restrictions on symlink targets.

Lifetime rules:
- NodeID is created at create/mkdir/symlink/etc.
- NodeID remains live while it has at least one directory entry (link_count > 0) OR at least one open ref (see 6.8).
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
- fs_merkle_root (hash of filesystem state after applying this op; excludes locks/open-leases/atime)

Optional, recommended for observability (NOT used for replay correctness):
- meta_digest (opaque bytes): leader-computed digest of hazard/reservation info attached to this op, if desired.

### 8.1.1 Incremental Merkle Root Computation (Mandatory for Performance)

Computing a fresh Merkle root after every op is O(n) and will collapse throughput at scale.

V1 requirements:
1) The leader MUST use an incremental Merkle structure with O(log n) update complexity per mutation.
2) Recommended structure: Merkle Mountain Range (MMR) or persistent balanced Merkle tree.
3) The tree MUST be append-friendly for the common case (new file creates, writes to end of file).
4) For random-access mutations (mid-file writes, deletes), the implementation MAY use:
   - Lazy rebalancing (batch updates to amortize cost)
   - Segment-level Merkle roots with periodic consolidation

Implementation guidance:
- Use BLAKE3 truncated to 256 bits for internal nodes (same as chunks).
- Internal node format: BLAKE3(left_child_hash || right_child_hash || node_metadata).
- node_metadata MUST be a canonical serialization and MUST NOT include non-deterministic fields (addresses, pointer values, wall-clock other than committed_at, etc.).

Performance targets:
- Merkle root update: < 10us p99 for single-chunk mutations
- Merkle root update: < 100us p99 for large-file mutations (1000+ chunks)
- Proof generation (audit): < 1ms for any leaf

Telemetry (required):
- merkle_update_latency_us histogram
- merkle_tree_depth gauge
- merkle_rebalance_count counter

### 8.2 Idempotency Rules (Mandatory)

Retry-safe behavior MUST be guaranteed:
- The pair (client_id, seq_no) uniquely identifies an intent.
- The leader MUST dedupe duplicate intents and return the SAME CommitAck (same log_index and op_id).
- Workers MUST retry with the same (client_id, seq_no) if they are unsure whether the intent committed.

This prevents double-apply under packet loss or client retries.

Resource-allocation idempotency:
- The leader SHOULD also dedupe LockRequest and other allocation-like requests by (client_id, seq_no) to avoid leaking resources on retry.

Idempotency retention (required for robustness):
- The leader MUST retain an intent_id -> (log_index, op_id) mapping for at least INTENT_DEDUPE_TTL (default 24h) OR last INTENT_DEDUPE_ENTRIES (default 10M), whichever larger.
- If an intent_id is older than this retention, the leader MAY return UNKNOWN and force the worker to snapshot-catch-up (see 13) rather than risk double-commit.

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

### 9.3.1 Chunk Upload Interruption Recovery

If the ChunkPut stream is interrupted (network failure, worker crash):

Leader-side handling:
1) The leader MUST retain partially received chunks for PARTIAL_UPLOAD_TTL (default 60s).
2) Partial uploads are keyed by intent_id.
3) After PARTIAL_UPLOAD_TTL, the leader MAY discard partial state for that intent.

Worker-side recovery:
1) On reconnection, the worker SHOULD retry the same intent (same client_id, seq_no).
2) If the leader still has partial state, it responds with ChunkNeed listing only missing chunks.
3) If the leader has discarded partial state, it responds with ChunkNeed listing all chunks.
4) The worker MUST be prepared to re-upload all chunks if needed.

Idempotency guarantee:
- If the leader committed the op before the worker received CommitAck:
  - The retry will receive the original CommitAck (same log_index).
- If the leader did not commit:
  - The retry is treated as a fresh intent (partial state may help).

This ensures at-most-once commit semantics even under network instability.

### 9.4 Canonical Metadata and Timestamps

To make replicas identical and enable precise debugging, timestamp assignment MUST be canonical:
- committed_at from the leader is the canonical time for mtime/ctime updates induced by Op[k].
- Workers MUST set file mtime/ctime to the leader committed_at for that op.

Timestamp format (normative):
- committed_at MUST use RFC3339 with nanosecond precision: `YYYY-MM-DDTHH:MM:SS.nnnnnnnnnZ`
- Example: `2026-01-26T02:45:00.123456789Z`
- The leader MUST ensure committed_at is strictly monotonically increasing across all ops.
- If the system clock returns the same nanosecond for consecutive ops, the leader MUST
  increment the nanosecond component by 1 (synthetic monotonicity).

If exact host-ctime semantics are required by a tool, it MUST run on a single host; HyperSync intentionally defines canonical leader timestamps instead.

### 9.5 Wire Messages (Sketch; Required Fields)

This is a concrete-enough wire sketch to prevent "hand-wavy" implementation drift. Exact encodings (protobuf/flatbuffers/bincode) are an implementation detail, but these fields are REQUIRED.

Idempotency key used across all mutation intents:
- intent_id = (client_id, seq_no)

Control plane (QUIC reliable streams):
- Hello (worker -> leader; connection handshake):
  - protocol_version (string; must equal hypersync/1)
  - workspace_id
  - worker_id
  - leader_epoch_seen (optional; last epoch seen, for observability)
  - features: {quic_datagram_supported, raptorq_supported, compression_supported, ...}
- Welcome (leader -> worker):
  - workspace_id
  - leader_epoch (LeaderEpoch; changes on restart)
  - commit_index
  - negotiated_params: {chunk_max, inline_threshold, batch_window_ms, ...}
- Heartbeat (bidirectional; periodic):
  - leader_epoch
  - commit_index (leader->worker) OR applied_index a_i (worker->leader)

- BarrierRequest (optional but recommended for strict modes):
  - workspace_id
  - request_id (u64)
- BarrierResponse:
  - request_id
  - commit_index

- IntentStatusRequest (required for ambiguity resolution):
  - workspace_id
  - intent_id
- IntentStatusResponse:
  - intent_id
  - status: {COMMITTED, NOT_FOUND, IN_FLIGHT}
  - if COMMITTED: {log_index, op_id, committed_at, fs_merkle_root}

- WriteIntentHeader:
  - intent_id
  - handle_id (optional; present for FD-based mutations; required for correct close/unlock behavior)
  - node_id (preferred) OR path (for path-based operations prior to node resolution)
  - op_type (WRITE/TRUNCATE/RENAME/UNLINK/etc.)
  - write_mode (for writes): {PWRITE, APPEND}
  - offset (required iff write_mode==PWRITE)
  - len (as applicable)
  - chunks: list of {chunk_hash, chunk_len}
  - inline_bytes (optional; present iff payload <= INLINE_THRESHOLD)
  - open_flags (optional but recommended for correctness): includes O_APPEND bit for validation
  - reservation_context (optional; if present):
    - project_key
    - reservation_id (or ids)
    - holder_agent_name
    - exclusive (bool)
    - expires_at (optional)

- LockRequest:
  - intent_id (idempotency; REQUIRED)
  - client_id
  - handle_id (REQUIRED; locks are released when handle closes or explicit unlock)
  - node_id
  - lock_kind (flock_shared, flock_exclusive, fcntl_read, fcntl_write, unlock)
  - range (optional for fcntl): {start, len}
- LockResponse:
  - status (granted/denied)
  - holder (if denied)
  - lock_id (assigned by leader when granted)
  - lease_ttl_ms (required when granted; see 10.2)

- LockRenew (worker -> leader):
  - lock_id
  - worker_id
  - leader_epoch

- LockRelease (worker -> leader; best-effort but SHOULD be sent on close):
  - lock_id
  - worker_id
  - leader_epoch

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
  - fs_merkle_root
  - hazard (optional)
  - applied_offset (optional; present iff write_mode==APPEND; leader-chosen EOF offset)
  - errno (0 on success; else a Linux errno value)

- LogEntry (leader -> worker):
  - log_index, op_id, committed_at
  - op (mutation-only)
  - fs_merkle_root
  - hazard (optional)

- WorkerApplied (worker -> leader; periodic):
  - applied_index (a_i)
  - read_only (bool)
  - missing_chunks_count
  - local_pressure (cpu, mem, disk)
  - last_apply_error (optional): {log_index, errno, message}

- OpenLeaseAcquire (worker -> leader; async, best-effort):
  - worker_id
  - leader_epoch
  - node_id
- OpenLeaseRelease (worker -> leader; async, best-effort):
  - worker_id
  - leader_epoch
  - node_id
- OpenLeaseRenew (worker -> leader; periodic):
  - worker_id
  - leader_epoch
  - node_ids[] (batched)

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
  - fs_merkle_root
  - manifest_hash (content-addressed)
  - manifest_bytes (or chunk refs)

---

## 10. Advisory Locks (Correctness for Real Tools) and Agent Mail

### 10.1 Why Locks Matter

Programs like git rely on advisory locks and O_EXCL lockfiles. A multi-host shared workspace without a distributed lock story will corrupt .git state and/or create heisenbugs.

Therefore:
- HyperSync MUST implement a leader lock manager for flock semantics and atomic O_CREAT|O_EXCL behavior.
- Agent Mail reservations remain the human-level coordination mechanism for agent behavior (who should edit what).

### 10.2 HyperSync Lock Manager (Normative)

Lock operations are leader-authoritative runtime state (NOT in op log, NOT in snapshots, NOT in fs_merkle_root):
- The leader is authoritative for lock acquisition/release/renew.
- Workers forward lock ops and block the calling process until leader acks (grant/deny).
- Locks are released on disconnect / lease expiry to avoid deadlock.
- On leader restart (LeaderEpoch changes), all outstanding locks are considered lost and MUST be treated as released (explicit deviation; matches crash semantics).

V1 scope correction (implementable + correct):
1) Lockfiles (O_CREAT|O_EXCL) are the primary correctness mechanism for git-like workloads.
   - HyperSync MUST correctly implement atomic open(O_CREAT|O_EXCL) for paths in the replicated workspace (6.8).
   - This is REQUIRED even if flock/fcntl support is partial.

2) flock support (whole-file only) is REQUIRED.
   - shared/exclusive flock on node_id is supported.
   - Semantics are per-open-file-description on a worker, but enforced by the leader.

3) fcntl support is PARTIAL in V1.
   - Byte-range locks are NOT supported in V1 (return ENOTSUP) unless/until explicitly implemented and tested.
   - Whole-file fcntl locks MAY be mapped to the same mechanism as flock if start=0 and len=0.

4) Lease-based deadlock safety (required):
   - Every granted lock MUST have a lease_ttl_ms (default 5000ms).
   - Workers MUST renew held locks periodically (e.g., every ttl/3) over the control stream.
   - If a worker fails to renew, the leader MUST revoke locks after TTL expiry.

5) Grace period before revocation (required for reliability):
   - After TTL expiry, locks enter GRACE state for LOCK_GRACE_MS (default 2000ms).
   - During GRACE, the lock is still held but:
     - The leader sends LockExpiryWarning to the holding worker.
     - Other workers requesting the lock receive LOCK_HELD_EXPIRING status.
   - If the worker renews during GRACE, the lock returns to normal state.
   - After GRACE expires, the lock is forcibly released.

6) Worker identity and lock ownership:
   - worker_id is a stable identifier (persists across restarts); SHOULD be hostname or configured ID.
   - client_id is per-session (changes on restart).
   - Locks are associated with (worker_id, client_id) tuple.
   - On worker restart, new client_id MAY reclaim locks from old client_id of same worker_id if:
     - Old client_id has no active QUIC connection.
     - New client_id proves same worker_id (via worker_secret or mTLS cert).
   - This prevents lock starvation when workers restart quickly.

- LockExpiryWarning (leader -> worker):
  - lock_id, node_id, expires_at, grace_remaining_ms

Release-on-close requirement:
- If a process closes a file descriptor that holds a lock without explicit unlock, the worker MUST attempt to send LockRelease for that lock_id during FUSE release.
- If the LockRelease message is lost, TTL/GRACE safety still prevents permanent deadlock, but timely release is REQUIRED for tooling performance (git lockfiles).

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

Leader maintains per NodeID a small rolling window of recent unreserved mutations.
Determinism requirement:
- The window MUST be defined purely in terms of log index distance (e.g., "last 256 committed ops for this NodeID").
- Time-based pruning is permitted ONLY if based on leader committed_at and yields deterministic results given the op log.

On committing a new unreserved mutation, it checks for overlaps/conflicts within that window and, if found:
- marks the op with hazard metadata referencing the conflicting log_index
- emits an Agent Mail message to both involved agents (high importance)
- increments hazard counters for UI/robot output

Hazards do not block commits by default; they are surfaced immediately.

### 11.3 Merkle Root Excludes Runtime/Coordination State (Integrity)

fs_merkle_root MUST represent only filesystem state (tree + metadata + content) and MUST exclude hazard/reservation/lock/open-lease runtime state.

If auditing of hazards/reservations is desired:
- include hazard metadata on log entries (already required), and/or
- include a separate meta_digest in log entries (8.1) that is not used for replay integrity.

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

Apply pipeline (performance + determinism):
- Workers MUST apply ops in increasing log_index order to preserve Prefix Replay (a_i is contiguous).
- Workers MAY prefetch/verify chunk bytes for Op[k+N] in parallel (N configurable) while waiting to apply Op[k].
- The worker MUST only advance a_i when Op[a_i+1] is fully applied.
- Recommended implementation structure:
  1) decode/prefetch stage (parallel): ensure bytes for future ops
  2) apply stage (ordered): apply Op[a_i+1] to local materialization
  3) invalidate stage (NORMATIVE if any kernel caching is enabled):
     - invalidate inode data ranges for file content mutations
     - invalidate dentry/attr caches for namespace mutations
     - see 6.3.2 for required invalidation behavior
This provides high throughput on 64-core systems without breaking deterministic replay.

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

### 12.5 Intent Rate Limiting (DoS Protection)

To prevent a single misbehaving worker from overwhelming the leader:

Per-worker rate limits (configurable):
- INTENT_RATE_LIMIT_OPS (default 500/s): max intents per second per worker
- INTENT_RATE_LIMIT_BYTES (default 50MB/s): max payload bytes per second per worker
- INTENT_BURST_OPS (default 100): burst allowance for short spikes

Enforcement:
1) Leader maintains a token bucket per worker_id.
2) Intents exceeding the rate limit receive:
   - RATE_LIMITED error response
   - Retry-After header indicating backoff duration (in ms)
3) Workers MUST implement exponential backoff when receiving RATE_LIMITED.
4) Leader MAY temporarily quarantine a worker that repeatedly exceeds limits.

Telemetry:
- rate_limit_rejections counter (per worker)
- intent_rate histogram (per worker)

---

## 13. Snapshots, Bootstrap, Catch-Up

### 13.1 Snapshot Definition

A snapshot at log index k is:
- snapshot_index = k
- fs_merkle_root = root_k
- a compact manifest sufficient to reconstruct S_k:
  - directory tree nodes + per-node metadata
  - per-file extent lists (chunk hashes + offsets)

Snapshots are content-addressed and stored in the chunk store as well.

### 13.2 When to Use Snapshots

Worker catch-up policy:
Catch-up MUST consider both entries and time, and MUST remain compatible with chunk GC rules:
- Let replay_window = min(REPLAY_WINDOW_TIME, REPLAY_WINDOW_ENTRIES) (defaults: 10 minutes OR 2,000,000 entries).
- If the worker's missing range is entirely within replay_window:
  - The worker MAY replay log entries 1-by-1 (log-based catch-up).
- Otherwise:
  - The worker MUST transfer a snapshot at a recent checkpoint index and then replay the remaining tail.

Rationale:
- Log-only catch-up requires leader retention of chunks referenced by overwritten historical ops; bounding replay_window keeps this storage bounded (14.2).

### 13.3 Snapshot Transfer

Snapshot transfer uses QUIC reliable streams (not RaptorQ) by default for determinism and simplicity, but MAY use RaptorQ for very large snapshot manifests.

### 13.4 Integrity

After applying snapshot + tail ops, worker MUST validate that its computed fs_merkle_root equals leader-provided root at the target index.

---

## 14. Retention and Garbage Collection (Bounding Disk)

### 14.1 Retention Policy (V1 Decision)

Leader retains:
- the full op log for at least RETAIN_LOG_HOURS (default 72h) OR last RETAIN_LOG_ENTRIES (default 10M), whichever larger
- periodic snapshots every SNAPSHOT_INTERVAL (default 5 minutes).
- retained snapshots MUST cover at least RETAIN_LOG_HOURS worth of history by default (i.e., RETAIN_SNAPSHOTS default becomes 864 for 72h @ 5m).

### 14.2 Chunk GC

Chunk GC MUST preserve recoverability and deterministic replay.

Definitions:
- Head reachability: chunks referenced by the current filesystem state S_commit_index.
- Snapshot reachability: chunks referenced by retained snapshots.
- Replay protection window: chunks referenced by committed ops within replay_window (13.2), even if overwritten later.

Rules (normative):
1) The leader MUST retain any chunk that is:
   - reachable from head, OR
   - reachable from any retained snapshot, OR
   - referenced by any committed op in the replay protection window (time/entries), OR
   - referenced by any in-flight (not-yet-fully-applied-by-all-workers) snapshot transfer.

2) Unlinked/orphaned content safety:
   - Chunks that are only reachable from unlinked NodeIDs MUST be retained while ANY worker holds an open-lease for that NodeID (6.8, 9.5).
   - After all open-leases have been released/expired, the leader MAY delete orphaned chunks subject to replay protection window and snapshot reachability rules.

3) Incrementality:
   - GC MUST be incremental and rate-limited (background task) to avoid stalls.
   - GC MUST expose progress and current safety cutoffs in metrics/robot output.

Implementation note:
- Refcount maintenance MAY be implemented via a segment index + periodic compaction rather than per-chunk random writes.

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

### 17.1 Concrete profiling plan (required outputs)
The Phase 0 deliverable MUST include:
1) Syscall histogram (per workload):
   - open/stat/readdir rates
   - write size distribution (p50/p90/p99), including burstiness
   - rename/unlink frequency
   - fsync frequency (file + directory)
   - mmap usage: count of MAP_SHARED PROT_WRITE attempts (should be 0 in V1)

2) Path hotness:
   - top 1,000 paths by mutation rate
   - evidence to justify default cache exclusions (NTM env routing to /ntmfs/local)

3) End-to-end latency budget:
   - mutation syscall latency decomposition: (FUSE + hash + upload + leader commit + apply)
   - p50/p95/p99 and worst-case outliers

### 17.2 Microbench suite (must exist before Phase 2)
Provide a standalone Rust microbench harness that can run on a single host and multi-host:
- leader commit loop throughput (ops/s) at various batch sizes
- chunk hashing throughput (BLAKE3) for representative write sizes
- chunk store put/get latency (NVMe) under contention
- log append + fsync group commit cost
- apply throughput on worker with ordered apply and parallel prefetch
- replication fanout cost vs worker count (1, 2, 4, 8, 16)

Additions (required because these are likely primary bottlenecks at 70+ agents):
- FUSE crossing overhead microbench:
  - open/stat/readdir throughput (ops/s) and p99 latency with varying attr_timeout/entry_timeout settings
- Cache invalidation microbench:
  - cost of notify_inval_inode + notify_inval_entry under high mutation rates
- Append correctness + performance:
  - concurrent O_APPEND writers across workers: throughput and validation of non-overlap behavior

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

## 22. Correctness Invariants and Test Plan (More Implementable/Testable)

This section is normative: a V1 implementation is not "done" without these tests and invariants.

### 22.1 Invariants (MUST be asserted in debug builds; SHOULD be telemetry in prod)
1) Log monotonicity:
   - leader log_index strictly increases by 1 per committed op.
2) Prefix apply:
   - worker a_i increases by 1 with no gaps; worker MUST NOT apply k+1 before k.
3) Commit gating:
   - a mutation syscall returns success only after CommitAck(k) and local a_i >= k.
4) Chunk integrity:
   - every applied chunk hash MUST match BLAKE3(bytes).
5) GC safety:
   - no chunk is deleted while it is reachable from head, any retained snapshot, or replay protection window.
6) Read-only on partition:
   - leader unreachable => all new mutations fail EROFS (no queued writes).

7) Atomic append correctness:
   - For any file opened with O_APPEND, committed write ops MUST have leader-chosen offsets that are strictly non-overlapping and strictly increasing in log order.

8) Unlink-on-open safety:
   - Orphaned NodeID content MUST NOT be GC'd while any worker holds an open-lease for that NodeID.

### 22.2 Deterministic replay golden tests
Build a "golden trace" format:
- trace = {oplog segment(s), all referenced chunks, snapshot manifest(s), expected merkle roots}
Tests:
1) replay on empty store => exact merkle roots at checkpoints
2) replay with chunk corruption => detected and refused apply
3) replay with shuffled network delivery => still converges (chunks may arrive out of order; apply must not)

### 22.3 Crash and partition fault-injection matrix
Run with deterministic fault injection points:
1) Worker crash:
   - after sending intent, before CommitAck
   - after CommitAck, before local apply
   - during snapshot apply
2) Leader crash:
   - after receiving chunks, before commit
   - after commit durable write, before sending CommitAck
   - during GC cycle
3) Network:
   - packet loss bursts (simulate 1%, 5%, 10%)
   - full partition for 10s/60s/10m (workers flip read-only)

### 22.4 Real-world tool workloads (must run in CI for hypersyncd)
1) Git torture:
   - concurrent `git status`, `git add`, `git commit`, `git checkout`, `git gc` across workers
   - validate repo consistency (`git fsck`) after random kill -9 of workers
2) Language toolchain:
   - `go test ./...` while another worker edits files
   - ensure no deadlocks, no silent corruption
3) Editor-like scans:
   - simulate ripgrep / LSP file walks: massive open/stat/readdir
   - validate leader does not become bottleneck (no per-open RPC requirement in V1)
4) Append torture:
   - multiple workers concurrently append to the same file (O_APPEND) while another worker tails/reads
   - validate content is the concatenation of committed writes in log order (no overlaps, no holes unless explicitly written)

### 22.5 Performance regression tests (extreme optimization discipline)
Maintain performance baselines for:
- commit latency p50/p99 under load
- max sustainable ops/s before backpressure triggers
- apply lag distribution across N workers
- CPU utilization breakdown: hashing, oplog, chunk store, transport, FUSE

---

End of spec.
