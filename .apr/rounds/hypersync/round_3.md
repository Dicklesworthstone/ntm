## Ranked issues found (severity + confidence)

1. **Client identity + sequencing is underspecified for a shared FUSE mount** (how `client_id`/`seq_no` are assigned across many Linux processes/threads, PID reuse, daemon restart, ordering guarantees). This is core to idempotency + “program order” semantics. **Severity: Critical. Confidence: High.**

2. **Permission/ownership enforcement model is not explicit** (kernel `default_permissions` vs leader-side checks; supplementary groups; multi-user behavior). Without an explicit contract you can get “should be EACCES but commits” or vice versa under races, or silently diverging access rules. **Severity: Critical. Confidence: High.**

3. **“Durable commit” does not fully pin down chunk-store durability ordering** (oplog commit ack must imply referenced chunk bytes are stable on leader storage, not just received+hashed). Otherwise leader crash can produce committed ops referencing missing bytes. **Severity: Critical. Confidence: Medium-High.**

4. **Path-resolution authority is ambiguous** (spec sometimes implies worker may resolve to `node_id` and send that; but leader must be authoritative to avoid stale/misapplied namespace ops). **Severity: High. Confidence: High.**

5. **Open-lease / orphan GC race** (leases are “async best-effort”; without an explicit orphan-retention grace, GC can violate unlink-open semantics if any lazy-fetch or partial materialization exists, or if GC runs aggressively). **Severity: High. Confidence: Medium-High.**

6. **Signal / cancellation semantics for commit-gated syscalls are missing** (`EINTR`/`SA_RESTART` interactions, what happens if a process is killed while a mutation is in-flight). This is a major failure-mode corner that can break idempotency or return misleading errors. **Severity: High. Confidence: High.**

7. **Syscall surface is incomplete for real developer workflows** (notably `utimensat`/`touch`, `getxattr/listxattr`, `readlink`, `statfs`, `access`, `fchmod/fchown`, `fsyncdir`). Missing these makes implementation/testing ambiguous and breaks common tools. **Severity: High. Confidence: High.**

8. **Read-only mode behavior is incomplete** (e.g., open with write flags, lock ops, O_TRUNC/O_CREAT translation, and other mutation-ish operations should fail consistently as `EROFS`). **Severity: Medium-High. Confidence: High.**

9. **Lock semantics need tighter mapping to Linux rules** (flock is per-open-file-description; fcntl is per-process; dup/fork/close interactions; reclaim-after-restart rules). Current text is close but not crisp enough to implement correctly and to write conformance tests. **Severity: High. Confidence: Medium.**

10. **Deterministic replay contract omits a few determinism-critical details** (sparse holes, stat-field normalization beyond `st_ino`, and explicit mode/umask handling). **Severity: Medium. Confidence: Medium.**

11. **Freshness barrier strategy may be adding avoidable tail latency** (once leader is authoritative for path ops, pre-waiting for `a_i >= barrier_index` can be optional and should be framed as a tunable). **Severity: Medium. Confidence: High.**

12. **V1 “RaptorQ everywhere” risks implementability and CPU cost** (FEC encoding overhead may dominate on 64 cores before network becomes the limiter; spec should more strongly define a baseline (QUIC streams) with explicit perf gates for enabling RaptorQ). **Severity: Medium. Confidence: Medium-High.**

13. **Observability needs stronger correlation** (intent→commit→apply tracing IDs, per-op latency decomposition fields, “flight recorder” for last N commits). Without this, debugging multi-host FS issues is brutal. **Severity: Medium. Confidence: High.**

14. **Test plan should include property-based/fuzz + syscall conformance matrix** (especially unsupported syscalls and errno consistency, plus randomized state-machine tests). **Severity: Medium. Confidence: High.**

---

## Proposed spec patches (git-diff style)

```diff
diff --git a/PROPOSED_HYPERSYNC_SPEC__CODEX.md b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
index 6a6a6a6..7b7b7b7 100644
--- a/PROPOSED_HYPERSYNC_SPEC__CODEX.md
+++ b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
@@ -1,10 +1,10 @@
 # PROPOSED_HYPERSYNC_SPEC__CODEX.md
 
-Status: PROPOSED (rev 4; correctness + determinism + implementability revisions)
+Status: PROPOSED (rev 5; correctness + robustness + implementability + perf revisions)
 Date: 2026-01-26
 Owner: Codex (GPT-5)
 Scope: Leader-authoritative, log-structured workspace replication fabric for NTM multi-agent workloads
 Audience: NTM maintainers + HyperSync implementers
 
-SpecVersion: 0.4
+SpecVersion: 0.5
 ProtocolVersion: hypersync/1
 Compatibility: Linux-only V1 (see 0.1); macOS support is explicitly deferred.
 
@@ -64,6 +64,24 @@
   7) Keep the NTM repo a pure Go project
     - NTM itself remains Go (Go toolchain only).
     - The HyperSync daemon (`hypersyncd`) may be implemented separately (Rust preferred), integrated by NTM at runtime.
 
+### E.1 V1 Explicit Simplifying Assumption (User/UID Model)
+This workflow is not multi-tenant. V1 explicitly optimizes for "one human user + many agent subprocesses".
+
+**Normative (V1):**
+- All processes accessing `/ntmfs/ws/<workspace>` on all nodes MUST run under the same effective UID and primary GID.
+- If a different effective UID is observed on the mount, hypersyncd MUST:
+  - either refuse the operation with `EACCES` and emit a loud error, OR
+  - refuse to start unless `multi_user=true` is explicitly enabled (V2 feature; NOT required in V1).
+
+Rationale:
+- Supplementary groups are not reliably available to a FUSE daemon in a portable way.
+- Making multi-UID correctness "accidentally best-effort" is a correctness trap; fail-fast is safer.
+
@@ -116,6 +134,11 @@
  ## 0.1 Assumptions, Guarantees, and Explicit Deviations (Alien Artifact Contract)
  This section is normative. If an implementation cannot satisfy a MUST here, it MUST refuse to start (fail-fast) rather than silently degrade.
 
  ### 0.1.1 Assumptions (Required Environment)
  1) Platform
@@ -125,6 +148,23 @@
     - FUSE3 is required (kernel FUSE + libfuse3 or equivalent).
     - The backing filesystem on each host MUST be case-sensitive and POSIX-like (ext4/xfs strongly recommended).
 
+ 1.1) Backing store constraints (for deterministic apply + atomic rename)
+    - Each node MUST have a single local backing directory (MaterializationRoot) on a single local filesystem.
+    - MaterializationRoot MUST support:
+      - atomic rename within the same filesystem (POSIX rename),
+      - `fsync()` of regular files,
+      - `fsync()` / `fdatasync()` of directories (or an equivalent durable directory barrier).
+    - If these properties cannot be verified at startup, hypersyncd MUST refuse to start.
+
  2) Time
     - Leader timestamps are canonical for replicated metadata. Workers MAY have skew.
     - The leader MUST ensure committed_at is monotonic non-decreasing (see 9.4).
@@ -136,6 +176,15 @@
  4) Failure/availability model
     - Single leader only. No consensus.
     - Leader may crash/restart. Workers may crash/restart.
     - Network may partition.
+
+ 4.1) User identity model (V1)
+    - V1 assumes a single effective UID + primary GID across the cluster (E.1).
+    - hypersyncd MUST mount with `default_permissions` enabled so the kernel enforces permissions locally.
+    - As a consequence, permission checks are defined against each worker's kernel view of S_{a_i}
+      (which is always a prefix of the leader log). This matches the non-multi-tenant intent of V1.
 
  5) Kernel/userspace interface constraints (Linux/FUSE correctness prerequisites)
@@ -151,6 +200,20 @@
     - If the platform/kernel/libfuse combination cannot provide these primitives, hypersyncd MUST refuse to start.
 
  ### 0.1.2 Guarantees (What HyperSync Provides)
@@ -160,13 +223,22 @@
  2) Mutation commit semantics
     - A logged mutation syscall returns success iff the leader has durably committed the op into the op log and has verified all required payload bytes (6.3, 9.3).
     - The mutation's linearization point is the leader's commit at log_index k (5.3).
 
+ 2.0) Durability is end-to-end (oplog + referenced bytes)
+    - CommitAck(k) MUST imply:
+      - the op log entry for k is durable on leader stable storage, AND
+      - all chunk bytes referenced by Op[k] are durable on leader stable storage (not merely received+hashed).
+    - If the leader uses buffered/asynchronous chunk writes, it MUST enforce a "chunks durable before CommitAck" rule
+      (e.g., chunk WAL + group fsync, or direct sync writes).
+
  2.1) Commit-gated visibility (kernel-visible effects)
     - For any syscall classified as a "logged mutation" in this spec, the calling process MUST NOT observe the effects of that mutation (via reads, readdir, stat, open, mmap, etc.) until the leader has committed it and the worker has applied it.
     - If hypersyncd cannot enforce this due to kernel caching behavior (e.g., writeback caching acknowledging write() before userspace sees it), it MUST refuse to start.
 
@@ -191,6 +263,22 @@
  2.1) mmap read coherence (explicit)
     - MAP_SHARED PROT_READ mmaps are permitted, but **coherence with remote writes is NOT guaranteed** unless the worker implements and the kernel honors page-cache invalidation for that mapping.
     - Portable rule for users/tools: to observe remote writes reliably, reopen/remap after the worker applies the corresponding commit.
 
+ 5) Signals and interruptibility (explicit deviation)
+    - Once a logged mutation intent has been transmitted to the leader, the syscall is treated as **non-interruptible**:
+      - hypersyncd SHOULD behave as if the syscall is restartable (SA_RESTART-like) and MUST NOT return EINTR after submission.
+      - If the calling task is killed (SIGKILL), in-flight ambiguity is resolved by idempotency + IntentStatus (6.4, 9.5).
+    - Rationale: returning EINTR for operations that may already be committed is a correctness trap for callers.
+
@@ -221,6 +309,11 @@
  ## 2. Glossary and Notation
  - Leader (L): single authoritative node that orders and commits all mutations
  - Worker (W_i): node running agents; hosts a FUSE mount; replays leader log
- - Client: an agent process performing filesystem operations on a worker
+ - Client: a Linux process performing filesystem operations through the mount (often an agent or its subprocess).
+ - ProcKey: stable process identity on a worker: (pid, start_time_ticks) from `/proc/<pid>/stat` (prevents PID reuse bugs).
+ - MountInstanceID: 128-bit random nonce generated by hypersyncd at startup; changes when hypersyncd restarts.
  - Op log: ordered sequence of committed mutation operations Op[1..N]
  - S_k: filesystem state after applying Op[1..k]
  - a_i: highest log index applied by worker W_i (worker's applied index)
@@ -229,11 +322,14 @@
  - IntentID: (client_id, seq_no), unique per client; used for idempotency
  - client_id: globally unique identity for an initiating client lifetime.
-   - MUST include a 128-bit random nonce generated at client start (ClientNonce).
-   - SHOULD include human-readable fields for observability (agent_name, worker_id), but uniqueness MUST NOT rely on them.
+   - In V1, client_id MUST be derived from: (worker_id, mount_instance_id, proc_key) to preserve per-process program order and avoid head-of-line coupling across unrelated processes.
+   - client_id MUST be globally unique for the lifetime of a ProcKey under a MountInstanceID.
+   - client_id SHOULD include human-readable fields for observability (agent_name, worker_id), but uniqueness MUST NOT rely on them.
  - seq_no: u64 strictly increasing per client_id; MUST NOT reset or wrap within a client_id lifetime.
  - NodeID: stable identifier for a filesystem object (inode-like; survives rename)
@@ -245,6 +341,44 @@
 
  Notation:
  - MUST/SHOULD/MAY are used in RFC-style normative sense.
 
+### 2.1 Client Identity + Sequencing (Normative, Implementability-Critical)
+This section makes the (client_id, seq_no) contract implementable for a Linux FUSE mount shared by many processes.
+
+**2.1.1 client_id derivation (V1 REQUIRED)**
+- On each FUSE request, hypersyncd MUST compute ProcKey = (pid, start_time_ticks).
+- client_id MUST be computed as a stable 128-bit value:
+  - client_id = BLAKE3_128("hypersync/client" || workspace_id || worker_id || mount_instance_id || pid || start_time_ticks)
+- This ensures:
+  - no cross-process idempotency collisions,
+  - no PID reuse collisions,
+  - no coupling between unrelated processes on the same worker.
+
+**2.1.2 seq_no rules (V1 REQUIRED)**
+- hypersyncd MUST maintain an in-memory seq_no counter per client_id.
+- For a given client_id, seq_no MUST increment by 1 for each transmitted logged mutation or lock intent.
+- hypersyncd MAY garbage-collect per-client counters when it observes the process has exited.
+
+**2.1.3 Ordering guarantee**
+- For any single client_id, hypersyncd MUST NOT concurrently transmit two logged mutation intents without assigning a total order (seq_no).
+- The leader MUST commit intents for the same client_id in seq_no order.
+  - If the leader receives seq_no=n+1 without having committed or rejected seq_no=n, it MUST buffer or reject with EAGAIN (implementation choice), but MUST NOT commit out of order.
+
+Rationale:
+- Preserves single-host "program order" as observed by the worker kernel.
+- Avoids artificial head-of-line blocking between unrelated processes by not using a single per-worker client_id.
+
@@ -352,6 +486,16 @@
  3) Atomicity (scoped, realistic):
@@ -366,6 +510,20 @@
  5) Symlink bytes
     - Symlink targets are treated as opaque bytes; no normalization (no path cleaning) is performed.
 
+ 6) Sparse holes (determinism + correctness)
+    - Files MAY be sparse.
+    - Any unwritten hole region MUST read as zero bytes and MUST NOT affect fs_merkle_root except via file size and explicit extent descriptors.
+    - A write beyond EOF MUST create a hole between old EOF and write offset (like local POSIX semantics).
+    - Implementation note (determinism): workers MAY choose to materialize holes as:
+      - true sparse holes (seek + write), OR
+      - explicit zero extents,
+      but the user-visible read bytes MUST match and fs_merkle_root MUST be computed from the logical content model, not from on-disk allocation details.
+
@@ -405,29 +563,46 @@
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
+ - utimensat / utimes (mtime/atime set; ctime becomes leader committed_at)
  - fsync, fdatasync (barriers)
 
  Leader-authoritative control-plane operations (NOT in the op log; still leader-ack gated):
  - flock/fcntl lock operations (see 10)
  - open-file lifetime leases (open-leases) used for safe unlink+GC behavior (6.8, 14.2)
 
@@ -448,10 +623,23 @@
  Not logged (served locally from S_{a_i} and worker caches):
  - open/close (not logged; see 6.8)
  - read, pread
  - stat, lstat, readdir
+ - readlink
+ - getxattr, listxattr
+ - access (MAY be implemented as getattr + kernel default_permissions; if implemented explicitly, MUST match Linux errno behavior)
+ - statfs (filesystem stats; MUST be deterministic across workers, see 6.1.2)
 
  Open/close note (important):
  - open/close are NOT part of the op log, but HyperSync MUST still coordinate them for correctness of unlink semantics and distributed locks (see 6.8 and 10).
 
+### 6.1.2 stat/statfs Field Normalization (Determinism Contract)
+To avoid "heisenbugs" where tools compare stat-like fields across nodes:
+- The following fields MUST be deterministic across workers: st_ino (InodeNo), st_mode, st_uid, st_gid, st_size, st_nlink, st_mtime, st_ctime, st_atime (atime is local-only; see 0.1.3).
+- The following fields MAY be synthesized with fixed constants per workspace: st_dev, st_blksize.
+- st_blocks MUST be computed from logical file size (e.g., ceil(st_size/512)) and MUST NOT depend on local sparse allocation.
+- statfs fields MAY be fixed per workspace (e.g., f_bsize=4096) and SHOULD report leader capacity if available; otherwise report worker-local capacity but MUST be clearly labeled as non-authoritative in debug/metrics.
+
  ### 6.1.1 Unsupported/Explicitly-Handled Syscalls (V1)
  This list is normative to make implementation and testing unambiguous.
 
@@ -462,13 +650,22 @@
  MUST be supported (either as logged mutations or local reads):
  - openat, mkdirat, unlinkat, renameat, linkat, symlinkat (same semantics as their non-*at variants)
  - rename replace semantics (POSIX rename)
+ - fsyncdir (directory fsync barrier; required by many atomic-replace patterns)
 
  MUST return ENOTSUP in V1 unless explicitly implemented and tested:
  - renameat2 flags other than "replace" semantics (e.g., RENAME_EXCHANGE, RENAME_NOREPLACE) unless leader implements them correctly
  - fallocate / FALLOC_FL_* (unless implemented as logged mutation with deterministic semantics)
  - copy_file_range, reflink/clone ioctls, fiemap, fs-verity ioctls
  - mknod (device/special files)
+ - open(O_TMPFILE) / O_TMPFILE-like semantics
+ - open(O_PATH) MAY be supported; if unsupported MUST return ENOTSUP consistently
 
  If a syscall is not supported, the returned errno MUST be ENOTSUP (preferred) or ENOSYS, consistently across workers.
 
@@ -478,40 +675,57 @@
  ### 6.2 Freshness Barriers (Prevent stale-path anomalies)
  Workers may serve reads from S_{a_i}, but path-resolving mutations MUST NOT execute against stale state, or correctness becomes user-visible (ENOENT/EEXIST surprises).
 
  Definitions:
  - barrier_index: the leader commit_index value that the worker considers current at syscall start.
 
  Normative rule for choosing barrier_index:
  - barrier_index MUST be the worker's last-observed commit_index from the leader's control/log stream at the moment the worker begins handling the syscall.
  - If the worker has not received any leader heartbeat or commit_index update within LEADER_STALE_MS (default 250ms on LAN; configurable), the worker MUST issue a BarrierRequest (9.5) to refresh commit_index before choosing barrier_index.
 
  Rules:
-1) For any logged mutation specified by path (e.g., create/mkdir/rename/unlink/chmod/chown/setxattr on a path):
-   - The worker MUST ensure a_i >= barrier_index before submitting the intent to the leader.
-   - If a_i < barrier_index, the worker MUST block the syscall until caught up.
-   - If the leader becomes unreachable while waiting, the worker MUST fail the syscall with EROFS and flip the mount read-only (6.4).
+1) Path authority rule (correctness-critical):
+   - For any path-based logged mutation, the leader is authoritative for path resolution and namespace validation.
+   - The worker MUST send the path(s) and operation parameters to the leader; it MUST NOT rely on local path->NodeID resolution for correctness.
+
+2) Barrier use for path-based logged mutations (V1 default):
+   - The worker SHOULD NOT pre-block purely to reach barrier_index before submitting a path-based mutation intent.
+   - Instead, it MUST submit the intent promptly, and the leader MUST decide success/errno based on its authoritative state machine at serialization time.
+   - After CommitAck(k), the worker MUST still satisfy commit-gated visibility (6.3): it MUST apply through k before returning success.
+
+   Optional strict mode (debug/correctness paranoia):
+   - If strict_mutation_barrier=true, the worker MUST ensure a_i >= barrier_index before submitting the intent, as in rev4.
 
-2) For FD-based mutations (write/pwrite/ftruncate/flock/fcntl):
+3) For local path-resolving non-logged operations (open without create/truncation, stat, readdir, readlink, getxattr/listxattr):
+   - If strict_open=true (default), the worker MUST ensure a_i >= barrier_index before returning results, OR it MUST block until it can do so.
+   - If strict_open=false, these operations may return from S_{a_i} (stale prefix semantics).
+
+4) For FD-based mutations (write/pwrite/ftruncate/flock/fcntl):
    - The worker SHOULD ensure it has applied at least the barrier_index that was current when the FD was opened (strict mode).
    - If strict mode is disabled, FD-based ops MAY proceed against S_{a_i} as long as they are still commit-gated (6.3).
 
  Implementation note:
  - Workers already receive commit_index via the replication/control stream. A dedicated BarrierRequest RPC (9.5) MAY be used when the worker suspects it is missing leader progress due to a transient gap.
 
@@ -519,14 +733,31 @@
  ### 6.3 Return Semantics (Default Mode)
  For every logged mutation M:
  - The worker MUST NOT make M visible to the calling process until the leader commits M at log index k (or returns an error).
  - The worker MUST return success to the syscall only after it receives CommitAck(k) from the leader.
  - After CommitAck(k), the worker MUST ensure it has applied all ops up through k locally before returning (a_i >= k), so read-your-writes holds immediately on the same worker.
 
  This is the core correction: mutation syscalls are leader-commit-gated.
 
+### 6.3.0 Partial Writes, Short Writes, and EINTR (Normative)
+To keep the op log well-defined and replayable:
+- For write/pwrite-like logged mutations, on success hypersyncd MUST return the full requested byte count for that kernel request.
+- hypersyncd MUST NOT return a short write as "success" unless:
+  - the leader explicitly committed a corresponding short write op (V1 SHOULD NOT do this), OR
+  - the kernel request itself is smaller (e.g., due to FUSE max_write splitting).
+- EINTR handling:
+  - Once an intent has been submitted to the leader, hypersyncd MUST NOT return EINTR for that syscall (see 0.1.3(5)).
+  - If the process is interrupted before submission, hypersyncd MAY return EINTR without side effects.
+
  ### 6.3.2 FUSE Caching and Visibility Rules (Normative)
  To satisfy 0.1.2(2.1), the V1 Linux/FUSE implementation MUST enforce:
@@ -536,6 +767,15 @@
     - In libfuse terms: writeback_cache MUST be disabled.
 
  2) Direct I/O for write-capable handles
@@ -551,6 +791,16 @@
     - If the worker cannot successfully issue invalidations, it MUST fall back to attr_timeout=0 and entry_timeout=0 behavior (no kernel attr/dentry caching) OR refuse to start.
+
+ 4) Mandatory mount option defaults (V1)
+    - hypersyncd MUST document and default to:
+      - default_permissions=1
+      - attr_timeout=0 (unless invalidation is proven reliable; then MAY be >0)
+      - entry_timeout=0 (unless invalidation is proven reliable; then MAY be >0)
+      - negative_timeout=0
+    - Any non-zero cache timeouts MUST be gated by an explicit "invalidation works" self-test at startup.
 
@@ -602,6 +852,26 @@
  ### 6.4 Error Semantics and Partitions (No Silent Divergence)
  If the leader is unreachable:
  - `/ntmfs/ws/<workspace>` MUST become read-only (writes return EROFS).
  - Reads MUST continue from the last applied state S_{a_i}.
  - The worker daemon MUST attempt reconnection and then catch up (see 13).
  - NTM MUST surface this state (sync_lag, read_only=true) in UI and robot output.
+
+ Read-only means "no new mutations of any kind":
+ - open() requests that include write capability (O_WRONLY/O_RDWR) MUST fail with EROFS while read_only=true,
+   even if open() would otherwise be served locally.
+ - lock operations (flock/fcntl) MUST fail with EROFS while read_only=true.
+ - OpenLease renewals SHOULD continue best-effort; if they fail, leases may expire (acceptable; open FDs are local-only at that point).
 
  In-flight mutation ambiguity (explicit deviation):
@@ -645,6 +915,35 @@
  ### 6.8 Open/Close Semantics (No per-open leader RPC in V1)
@@ -684,6 +983,38 @@
     - The leader associates open-leases with (worker_id, leader_epoch) and applies a lease TTL (OPEN_LEASE_TTL_MS, default 15000ms).
     - Workers MUST renew active open-leases every ttl/3; if renewals stop (disconnect/crash), the leader may expire that worker's leases after TTL.
 
+    Orphan GC race protection (V1 REQUIRED):
+    - When a NodeID transitions to link_count==0 at commit index k (becomes orphaned), the leader MUST retain its chunks for at least ORPHAN_GRACE_MS
+      regardless of open-lease state.
+      - Default: ORPHAN_GRACE_MS = max(3 * OPEN_LEASE_TTL_MS, 60_000ms).
+    - Rationale: OpenLeaseAcquire is async; this grace prevents GC from racing lease acquisition in normal operation.
+
+    Idempotency:
+    - OpenLeaseAcquire / Release / Renew MUST be idempotent on the leader.
+      - Duplicate Acquire MUST NOT extend TTL unboundedly without renewal logic.
+      - Duplicate Release MUST be safe.
+
  GC safety rule:
     - The leader MUST NOT delete orphaned content (link_count==0) while ANY worker holds an open-lease for that NodeID.
     - This preserves correct unlink-on-open semantics without turning open() into a leader RPC.
 
@@ -745,6 +1076,18 @@
  ### 8.1 Log Entry Schema (Normative)
  Each committed entry MUST include:
  - log_index (u64, monotonic)
  - op_id (UUID)
  - committed_at (RFC3339, leader time; used for canonical timestamps)
  - intent_id: (client_id, seq_no) (idempotency key)
+ - caller (required for audit + future permissions):
+   - caller_uid (u32)
+   - caller_gid (u32)
+   - caller_pid (u32)
+   - origin_proc_key (ProcKey)
  - origin_worker_id
  - origin_agent_name (for hazard attribution)
  - op (one of the mutation operations)
  - hazard (optional, see 11)
  - fs_merkle_root (hash of filesystem state after applying this op; excludes locks/open-leases/atime)
@@ -813,6 +1156,20 @@
  ### 9.3 Upload Handshake (Required)
  Control plane (QUIC reliable stream):
  1) Worker -> Leader: WriteIntentHeader
@@ -823,8 +1180,15 @@
  3) Worker -> Leader: ChunkPut stream (QUIC reliable)
     - frames: {chunk_hash, chunk_len, bytes}
     - leader verifies BLAKE3 matches
- 4) Leader commits the op only after all needed chunks are present and verified, then returns CommitAck(log_index=k).
+ 4) Leader durability rule (normative):
+    - Leader commits the op only after:
+      - all needed chunks are present AND verified, AND
+      - all needed chunks are durable on leader storage, AND
+      - the op log entry is durable.
+    - Only then may the leader return CommitAck(log_index=k).
 
  This is the strict correctness path.
 
@@ -866,6 +1230,30 @@
  ### 9.5 Wire Messages (Sketch; Required Fields)
@@ -873,12 +1261,15 @@
  Control plane (QUIC reliable streams):
  - Hello (worker -> leader; connection handshake):
    - protocol_version (string; must equal hypersync/1)
    - workspace_id
    - worker_id
+   - mount_instance_id (MountInstanceID; REQUIRED)
    - leader_epoch_seen (optional; last epoch seen, for observability)
    - features: {quic_datagram_supported, raptorq_supported, compression_supported, ...}
  - Welcome (leader -> worker):
    - workspace_id
    - leader_epoch (LeaderEpoch; changes on restart)
    - commit_index
    - negotiated_params: {chunk_max, inline_threshold, batch_window_ms, ...}
@@ -900,6 +1291,12 @@
  - WriteIntentHeader:
    - intent_id
+   - origin_proc_key (ProcKey; REQUIRED)
+   - caller_uid, caller_gid, caller_pid (REQUIRED)
    - handle_id (optional; present for FD-based mutations; required for correct close/unlock behavior)
    - node_id (preferred) OR path (for path-based operations prior to node resolution)
@@ -910,6 +1307,14 @@
    - inline_bytes (optional; present iff payload <= INLINE_THRESHOLD)
    - open_flags (optional but recommended for correctness): includes O_APPEND bit for validation
+   - path_resolution (normative):
+     - For path-based ops, leader resolves path(s) on authoritative namespace at serialization time.
+     - If a worker provides node_id for a path op, leader MUST treat it as a hint only and MUST validate against authoritative state.
    - reservation_context (optional; if present):
      - project_key
      - reservation_id (or ids)
@@ -937,6 +1342,18 @@
  - CommitAck:
    - intent_id
    - op_id
    - log_index
    - committed_at
    - fs_merkle_root
    - hazard (optional)
    - applied_offset (optional; present iff write_mode==APPEND; leader-chosen EOF offset)
    - errno (0 on success; else a Linux errno value)
+   - trace (required for observability):
+     - trace_id (u128) # stable across retry; derived from intent_id
+     - leader_commit_latency_us
+     - leader_chunk_wait_us
+     - leader_wal_fsync_us
+     - leader_chunk_fsync_us (0 if using chunk WAL group fsync)
 
@@ -956,6 +1373,20 @@
  ### 10.2 HyperSync Lock Manager (Normative)
@@ -974,6 +1405,25 @@
  2) flock support (whole-file only) is REQUIRED.
     - shared/exclusive flock on node_id is supported.
     - Semantics are per-open-file-description on a worker, but enforced by the leader.
+
+    Linux mapping clarity (normative):
+    - A "file description" corresponds to a single FUSE open handle (HandleID).
+      - dup() does not create a new file description; it reuses the same one.
+      - fork() inherits file descriptions.
+      - Therefore, HandleID is the correct lock owner for flock in V1.
+    - hypersyncd MUST ensure that a lock held by a HandleID is released when the last reference to that HandleID closes (FUSE release).
 
  3) fcntl support is PARTIAL in V1.
@@ -984,6 +1434,15 @@
     - Whole-file fcntl locks MAY be mapped to the same mechanism as flock if start=0 and len=0.
+
+    fcntl mapping (explicit deviation):
+    - Linux fcntl locks are per-process. V1 does NOT fully emulate per-process semantics.
+    - If whole-file fcntl locks are supported, they are mapped to HandleID ownership (like flock), and this deviation MUST be documented in logs/metrics.
 
@@ -1101,6 +1560,18 @@
  ### 12.2 RaptorQ Data Plane (Chunks)
@@ -1113,6 +1584,22 @@
  Preferred transport: QUIC DATAGRAM for symbols (unordered, no HOL blocking). Fallback: QUIC unidirectional stream per worker (still works, less ideal).
@@ -1120,6 +1607,26 @@
  Design targets (not hard guarantees):
  - Healthy LAN: commit->apply p50 <= 10ms, p99 <= 100ms
  - Healthy WAN: commit->apply p50 <= 50ms, p99 <= 500ms
+
+ V1 implementability gate (normative):
+ - RaptorQ MUST be negotiated via Hello/Welcome features.
+ - A V1 implementation MUST support a baseline mode without RaptorQ:
+   - chunk replication via QUIC reliable streams (ChunkPut fanout or on-demand ChunkFetch).
+ - RaptorQ MAY be enabled only if:
+   - encode CPU cost is < 15% of a leader core at the target throughput, AND
+   - p99 commit->apply latency improves vs baseline at N>=4 workers under representative load.
+ - These thresholds MUST be validated by Phase 0/17.2 microbench results before making RaptorQ the default.
 
@@ -1143,6 +1650,20 @@
  Apply pipeline (performance + determinism):
@@ -1153,6 +1674,20 @@
    3) invalidate stage (NORMATIVE if any kernel caching is enabled):
       - invalidate inode data ranges for file content mutations
       - invalidate dentry/attr caches for namespace mutations
       - see 6.3.2 for required invalidation behavior
+
+  Initiator fast-path (performance optimization, V1 SHOULD):
+  - For the worker that originated an intent, the leader SHOULD piggyback the missing log entries (a_i+1..k) on the CommitAck path
+    (or send them immediately on the same connection) to minimize "wait for prefix" latency before returning from the syscall.
+  - This does not change correctness; it only reduces tail latency under concurrent commit load.
 
@@ -1279,6 +1820,21 @@
  ### 14.2 Chunk GC
@@ -1293,6 +1849,16 @@
  2) Unlinked/orphaned content safety:
     - Chunks that are only reachable from unlinked NodeIDs MUST be retained while ANY worker holds an open-lease for that NodeID (6.8, 9.5).
     - After all open-leases have been released/expired, the leader MAY delete orphaned chunks subject to replay protection window and snapshot reachability rules.
+    - Additionally, orphaned chunks MUST be retained for at least ORPHAN_GRACE_MS after the unlink that caused link_count==0 (6.8).
 
@@ -1395,6 +1961,34 @@
  ### 17.2 Microbench suite (must exist before Phase 2)
  Provide a standalone Rust microbench harness that can run on a single host and multi-host:
@@ -1405,6 +2000,25 @@
  Additions (required because these are likely primary bottlenecks at 70+ agents):
  - FUSE crossing overhead microbench:
    - open/stat/readdir throughput (ops/s) and p99 latency with varying attr_timeout/entry_timeout settings
  - Cache invalidation microbench:
    - cost of notify_inval_inode + notify_inval_entry under high mutation rates
  - Append correctness + performance:
    - concurrent O_APPEND writers across workers: throughput and validation of non-overlap behavior
+
+ Additional V1-required microbenches (new):
+ - Path authority + leader validation:
+   - rename/unlink/create throughput and p99 latency when leader resolves paths (no worker pre-catchup),
+     with varying directory sizes (10, 1k, 100k entries).
+ - Directory representation:
+   - readdir cost when directory entries are stored sorted vs sorted-on-demand (must include offset-cookie correctness).
+ - Lock manager contention:
+   - git-style lockfile pattern: tight loop of (open O_CREAT|O_EXCL, write small, fsync, close, rename, fsyncdir) across workers.
+ - Intent ambiguity resolution:
+   - IntentStatusRequest QPS under synthetic packet loss and worker reconnect storms.
+ - Chunk durability cost model:
+   - compare chunk WAL group fsync vs per-chunk fsync; report p50/p99 commit latency impact.
 
@@ -1496,6 +2110,45 @@
  ### 22.1 Invariants (MUST be asserted in debug builds; SHOULD be telemetry in prod)
@@ -1513,6 +2166,22 @@
  8) Unlink-on-open safety:
     - Orphaned NodeID content MUST NOT be GC'd while any worker holds an open-lease for that NodeID.
+
+ 9) Client identity correctness:
+    - client_id MUST be stable for a given ProcKey under a MountInstanceID.
+    - seq_no MUST be strictly increasing per client_id.
+    - Leader MUST NOT commit seq_no out of order for a client_id.
+
+ 10) Read-only correctness:
+    - When read_only=true, all mutations (including open for write and lock ops) MUST fail with EROFS.
+
+ 11) Path authority:
+    - For any path-based op, leader resolution is authoritative; worker-provided node_id hints must be validated and MUST NOT cause mis-application.
 
@@ -1536,6 +2205,34 @@
  ### 22.3 Crash and partition fault-injection matrix
@@ -1558,6 +2255,19 @@
  3) Network:
     - packet loss bursts (simulate 1%, 5%, 10%)
     - full partition for 10s/60s/10m (workers flip read-only)
+
+ 4) Signals / interruption:
+    - SIGINT/SIGTERM delivered to a process while a logged mutation is awaiting CommitAck
+      - verify hypersyncd does not return EINTR after submission
+      - verify idempotency under user-level retries
+
+ 5) Orphan GC race:
+    - worker opens file (holds FD), another worker unlinks it, leader runs GC
+      - verify ORPHAN_GRACE_MS prevents premature deletion
+      - verify behavior under delayed OpenLeaseAcquire delivery
+
@@ -1564,6 +2288,32 @@
  ### 22.4 Real-world tool workloads (must run in CI for hypersyncd)
@@ -1586,6 +2336,20 @@
  4) Append torture:
     - multiple workers concurrently append to the same file (O_APPEND) while another worker tails/reads
     - validate content is the concatenation of committed writes in log order (no overlaps, no holes unless explicitly written)
+
+ 5) Timestamp behavior:
+    - `touch` / utimensat across workers
+    - verify mtime set matches request (within leader canonicalization rules) and ctime updates to committed_at
+
+ 6) Unsupported syscall errno matrix:
+    - run a syscall probe suite to ensure unsupported operations return ENOTSUP (or ENOSYS) consistently across workers.
 
 ---  End of spec.
```

```diff
diff --git a/PROPOSED_HYPERSYNC_SPEC__CODEX.md b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
index 7b7b7b7..8c8c8c8 100644
--- a/PROPOSED_HYPERSYNC_SPEC__CODEX.md
+++ b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
@@ -526,6 +526,52 @@
  ### 6.3.1 Batch Commit (Performance Under Load)
@@ -586,6 +632,52 @@
   Backpressure:
   - If the leader's pending intent queue exceeds MAX_PENDING_INTENTS (default 10000),
    the leader MUST reject new intents with EAGAIN and per-worker rate limiting kicks in.
+
+### 6.3.3 Leader State-Machine vs Backing-FS (Correctness Boundary)
+To avoid nondeterminism from "do the op on ext4/xfs and see what happens":
+- The leader MUST decide success/errno for all logged mutations using a deterministic in-memory state machine of S_k.
+- The leader MAY materialize S_k to its own local backing store for its own local mount, but that materialization MUST NOT be the source of truth for validation decisions.
+- The worker MUST return the leader's errno unchanged to the caller.
+
+Rationale:
+- Different local backing filesystems can differ subtly in errno behavior and edge cases.
+- A deterministic state machine is required for repeatable replay, fuzz testing, and correctness.
+
+Implementation note:
+- This does NOT require the leader to emulate every Linux errno corner case.
+  It requires the leader to be *the one place* that defines the contract, and for tests to lock that down.
+
+### 6.3.4 Canonical Operation Encoding (Implementability Requirement)
+For each logged operation type, the spec requires a canonical field set that is:
+- sufficient to replay deterministically,
+- stable across versions,
+- and independent of local inode numbers or backing-store paths.
+
+Minimum required op variants (V1):
+- CreateOp { parent_node_id, name, mode, uid, gid, kind=(file|dir), ino_assigned }
+- SymlinkOp { parent_node_id, name, target_bytes, mode, uid, gid, ino_assigned }
+- LinkOp { new_parent_node_id, new_name, target_node_id }
+- UnlinkOp { parent_node_id, name, target_node_id, target_was_dir=false }
+- RmdirOp { parent_node_id, name, target_node_id, target_was_dir=true }
+- RenameOp { src_parent, src_name, dst_parent, dst_name, src_node_id, dst_replaced_node_id? }
+- ChmodOp { target_node_id, mode }
+- ChownOp { target_node_id, uid, gid }
+- UtimensOp { target_node_id, atime_ns, mtime_ns } # leader sets ctime via committed_at
+- SetxattrOp { target_node_id, name_bytes, value_bytes, flags }
+- RemovexattrOp { target_node_id, name_bytes }
+- TruncateOp { target_node_id, new_size }
+- WriteOp { target_node_id, write_mode=(pwrite|append), offset_or_none, len, chunk_refs[], open_flags_snapshot }
+- FsyncOp { target_node_id, kind=(file|dir) }
+
+Notes:
+- parent_node_id/name pairs are used in ops that mutate directory entries to avoid path parsing in the replay engine.
+- For path-based intents submitted by workers, the leader MUST resolve to these canonical fields before commit.
```

```diff
diff --git a/PROPOSED_HYPERSYNC_SPEC__CODEX.md b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
index 8c8c8c8..9d9d9d9 100644
--- a/PROPOSED_HYPERSYNC_SPEC__CODEX.md
+++ b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
@@ -658,6 +658,44 @@
  ### 6.6 mmap Semantics (Decision + Enforcement)
@@ -701,6 +739,44 @@
  ### 6.7 O_DIRECT Semantics
@@ -712,6 +794,60 @@
  V1 decision:
  - Worker FUSE layer MUST strip O_DIRECT and return a warning in logs/metrics.
  - Optional future: FUSE passthrough for reads + intercepted writes where supported.
+
+### 6.7.1 hypersyncd Internal I/O Policy (Extreme-Perf Clarification)
+This section is explicitly about hypersyncd's own storage engine, not user I/O flags.
+
+V1 SHOULD:
+- Use buffered I/O for the local materialization backing store (so kernel readahead helps reads) **only if** invalidation is proven reliable.
+- Use async I/O for the chunk store (io_uring or threadpool) and treat fsync batching as the main optimization lever.
+
+V1 MUST expose toggles for benchmarking:
+- chunk_store.sync_mode = {wal_group_fsync, direct_fsync_per_batch, dsync}
+- chunk_store.preallocate = {off,on}
+- apply.direct_io_reads = {off,on} (read path experimentation)
+
+Each toggle MUST have a corresponding microbench entry in 17.2.
+
@@ -741,6 +871,52 @@
  ### 6.8 Open/Close Semantics (No per-open leader RPC in V1)
@@ -752,6 +928,52 @@
  V1 rules (normative):
  1) open() without creation/truncation is served locally:
@@ -760,6 +988,36 @@
       - If strict_open=true (default for correctness), the worker MUST wait until a_i >= barrier_index before returning open().
+
+    Read-only mode clarification:
+    - If read_only=true, open() that requests write capability MUST fail with EROFS (even though open is otherwise local).
 
  2) open() that implies a mutation is treated as a mutation:
@@ -788,6 +1046,56 @@
     - close()/release MUST also trigger best-effort cleanup:
@@ -798,6 +1106,74 @@
       - if NodeID refcount hits 0, worker sends OpenLeaseRelease(node_id).
 
  Rationale:
  - This avoids turning the leader into a high-QPS open()/close() authority while still preserving unlink-on-open safety and bounded GC behavior.
+
+### 6.8.1 Open-Lease Correctness Self-Test (Startup MUST)
+hypersyncd MUST run a startup self-test to validate that the "no per-open RPC" strategy is safe under the chosen caching settings:
+- Create file A, open it on worker, unlink it on another worker, ensure:
+  - worker with open FD can still read bytes from A after unlink commit is applied.
+- If the worker is configured for any lazy chunk fetching, the test MUST include reading beyond the currently cached region.
+- If the self-test fails, hypersyncd MUST refuse to start (fail-fast).
+
+This prevents an implementation from silently shipping with a GC/lease race.
```

```diff
diff --git a/PROPOSED_HYPERSYNC_SPEC__CODEX.md b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
index 9d9d9d9..aeaeaea 100644
--- a/PROPOSED_HYPERSYNC_SPEC__CODEX.md
+++ b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
@@ -1609,6 +1609,68 @@
  ### 16. Observability and UX
@@ -1616,6 +1678,68 @@
  Leader exposes metrics:
@@ -1624,6 +1754,68 @@
  NTM dashboard/robot output SHOULD surface:
@@ -1632,6 +1822,68 @@
  ### 16.1 NTM Integration Surface (Config + CLI + Robot)
@@ -1660,6 +1850,64 @@
   Robot (proposed):
   - `ntm --robot-snapshot` already exists; extend to include hypersync fields:
    - `hypersync.leader_reachable`, `hypersync.commit_index`, `hypersync.workers[]`, `hypersync.hazards[]`
+
+### 16.2 Traceability (Required for Debuggability at Scale)
+To debug a distributed filesystem under high concurrency, every mutation must be traceable across stages.
+
+Normative:
+- Every intent MUST carry a trace_id (derived from intent_id).
+- The leader MUST emit structured spans for:
+  - intent_receive
+  - chunk_need_compute
+  - chunk_ingest_verify
+  - chunk_durable_wait
+  - batch_commit_wal_fsync
+  - commit_ack_send
+- The worker MUST emit structured spans for:
+  - fuse_request_start
+  - barrier_wait (if any)
+  - intent_send
+  - commit_ack_wait
+  - apply_wait_for_prefix
+  - apply_local
+  - fuse_reply
+
+Required robot output additions:
+- per-op breakdown for the last N (default 50) slowest ops:
+  - {trace_id, op_type, caller_pid, origin_agent_name, total_ms, fuse_ms, net_ms, leader_commit_ms, apply_ms}
+
+Required "flight recorder":
+- both leader and worker MUST maintain an in-memory ring buffer of the last N=10,000 commit events with trace_id and timing fields.
+- This ring buffer MUST be dumpable via a CLI command without requiring external tracing infra.
```

```diff
diff --git a/PROPOSED_HYPERSYNC_SPEC__CODEX.md b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
index aeaeaea..bfbfbfb 100644
--- a/PROPOSED_HYPERSYNC_SPEC__CODEX.md
+++ b/PROPOSED_HYPERSYNC_SPEC__CODEX.md
@@ -1712,6 +1712,66 @@
  ## 18. Security
  V1 decision:
  - QUIC connections use mutual TLS between leader and workers (or an equivalent strong token scheme).
  - All chunks are verified by hash at ingress and before apply.
+
+### 18.1 Replay / Cross-Workspace Safety (Hardening)
+Normative:
+- All control-plane messages MUST include workspace_id.
+- The leader MUST reject any message whose workspace_id does not match.
+- The worker MUST reject any Welcome/log stream whose workspace_id does not match its configured workspace.
+
+Optional but strongly recommended:
+- Include a monotonically increasing ConnectionEpoch per worker (separate from LeaderEpoch) to simplify debugging and avoid stale connections.
```

### Notes on what these patches intentionally *do not* do

* They **do not introduce additional servers** or any multi-leader behavior.
* They **do not change “single shared workspace”** semantics.
* They **do not allow offline worker writes**; leader unreachable remains `EROFS`.
* They **do not rely on NFS/rsync**.

If you want, I can also produce a small “Syscall conformance matrix” appendix (as a diff) that enumerates *every* relevant syscall/flag combo and the expected behavior/errno in V1—useful for building a deterministic test harness and for preventing spec drift during implementation.
