# HyperSync: Distributed Workspace Fabric for NTM

**Version**: 0.1.0-draft
**Author**: Claude Opus 4.5 (with corrections from GPT 5.2)
**Date**: 2026-01-23
**Status**: PROPOSED

---

## Executive Summary

HyperSync is a leader-authoritative, log-structured, erasure-coded distributed filesystem designed specifically for multi-agent AI coding workloads. It enables 70+ Claude Code / Codex instances to operate across multiple machines while maintaining **single-workspace semantics** — agents see identical, consistent state as if running on one machine.

### Design Principles

1. **Leader Authority**: Single source of truth, no split-brain
2. **Total-Order Consistency**: Deterministic replay, not eventual consistency
3. **Worker-Side Interception**: Capture writes where they occur
4. **Loss-Tolerant Replication**: RaptorQ fountain codes over QUIC
5. **Measure First**: Profile real agent I/O before optimizing

---

## 1. Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           HYPERSYNC FABRIC                              │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │                     LEADER (main machine)                         │  │
│  │                     512GB RAM, 64 cores, NVMe                     │  │
│  │  ┌─────────────────────────────────────────────────────────────┐  │  │
│  │  │  Authoritative Op Log                                       │  │  │
│  │  │  ├─ Monotonic sequence numbers                              │  │  │
│  │  │  ├─ Content-addressed chunks (Blake3)                       │  │  │
│  │  │  └─ Merkle root per committed index                         │  │  │
│  │  └─────────────────────────────────────────────────────────────┘  │  │
│  │  ┌─────────────────────────────────────────────────────────────┐  │  │
│  │  │  RaptorQ Encoder + QUIC Broadcaster                         │  │  │
│  │  │  ├─ Symbolizes log entries                                  │  │  │
│  │  │  ├─ Unicast to each worker (QUIC)                           │  │  │
│  │  │  └─ Optional LAN multicast when available                   │  │  │
│  │  └─────────────────────────────────────────────────────────────┘  │  │
│  │  ┌─────────────────────────────────────────────────────────────┐  │  │
│  │  │  Scheduler (Thompson Sampling)                              │  │  │
│  │  │  ├─ Maintains posterior on worker costs                     │  │  │
│  │  │  ├─ Enforces stability constraint ρ < 0.8                   │  │  │
│  │  │  └─ Integrates with NTM spawn                               │  │  │
│  │  └─────────────────────────────────────────────────────────────┘  │  │
│  │  ┌─────────────────────────────────────────────────────────────┐  │  │
│  │  │  Agent Mail Bridge                                          │  │  │
│  │  │  ├─ Cluster-wide file reservations                          │  │  │
│  │  │  ├─ Hazard marking for unreserved writes                    │  │  │
│  │  │  └─ Lock state replicated in Merkle DAG                     │  │  │
│  │  └─────────────────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                              │                                          │
│              ┌───────────────┼───────────────┐                          │
│              │               │               │                          │
│              ▼               ▼               ▼                          │
│  ┌───────────────┐  ┌───────────────┐  ┌───────────────┐               │
│  │  WORKER: fmd  │  │  WORKER: yto  │  │  WORKER: jain │               │
│  │  ┌─────────┐  │  │  ┌─────────┐  │  │  ┌─────────┐  │               │
│  │  │  FUSE   │  │  │  │  FUSE   │  │  │  │  FUSE   │  │               │
│  │  │ ntmfs/  │  │  │  │ ntmfs/  │  │  │  │ ntmfs/  │  │               │
│  │  └────┬────┘  │  │  └────┬────┘  │  │  └────┬────┘  │               │
│  │       │       │  │       │       │  │       │       │               │
│  │  ┌────▼────┐  │  │  ┌────▼────┐  │  │  ┌────▼────┐  │               │
│  │  │ Daemon  │  │  │  │ Daemon  │  │  │  │ Daemon  │  │               │
│  │  │ (write  │  │  │  │ (write  │  │  │  │ (write  │  │               │
│  │  │ capture)│  │  │  │ capture)│  │  │  │ capture)│  │               │
│  │  └────┬────┘  │  │  └────┬────┘  │  │  └────┬────┘  │               │
│  │       │       │  │       │       │  │       │       │               │
│  │  ┌────▼────┐  │  │  ┌────▼────┐  │  │  ┌────▼────┐  │               │
│  │  │ RaptorQ │  │  │  │ RaptorQ │  │  │  │ RaptorQ │  │               │
│  │  │ Decoder │  │  │  │ Decoder │  │  │  │ Decoder │  │               │
│  │  └─────────┘  │  │  └─────────┘  │  │  └─────────┘  │               │
│  └───────────────┘  └───────────────┘  └───────────────┘               │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### Component Responsibilities

| Component | Location | Responsibility |
|-----------|----------|----------------|
| **Op Log** | Leader | Single source of truth for all file operations |
| **FUSE Mount** | Workers | Intercept all filesystem operations |
| **Worker Daemon** | Workers | Capture writes, forward to leader, apply replicated ops |
| **RaptorQ Encoder** | Leader | Symbolize log entries for loss-tolerant broadcast |
| **RaptorQ Decoder** | Workers | Reconstruct log entries from symbol stream |
| **QUIC Transport** | All | NAT-traversal, congestion control, reliable delivery |
| **Scheduler** | Leader | Place agents on workers using Thompson Sampling |
| **Agent Mail Bridge** | Leader | Cluster-wide file reservations and hazard detection |

---

## 2. Consistency Model

### 2.1 Total-Order Invariant

All file operations are assigned a **globally unique, monotonically increasing sequence number** by the leader. Workers replay operations in sequence order, guaranteeing:

```
∀ workers W₁, W₂:
  ∀ operations O₁, O₂ where seq(O₁) < seq(O₂):
    W₁ applies O₁ before O₂ ∧ W₂ applies O₁ before O₂
```

This provides **linearizability** for the operation log.

### 2.2 Single-Workspace Equivalence

The system guarantees that the distributed execution is **equivalent to sequential execution on a single machine**:

```
Definition: Single-Workspace Equivalence
  A distributed execution E is equivalent to some sequential execution S if:
  1. All operations in E appear in S
  2. The final state after E equals the final state after S
  3. Read operations return values consistent with S
```

### 2.3 Happens-Before Relations

```
Write-Order:     write(f, data₁) → write(f, data₂)  ⟹  seq₁ < seq₂
Rename-Order:    write(f, data) → rename(f, g)      ⟹  seq_write < seq_rename
Fsync-Barrier:   write(f, data) → fsync(f)          ⟹  data durable before fsync returns
Cross-File:      Operations on different files are ordered by leader receipt time
```

### 2.4 Conflict Detection

When two agents write to the same file region without Agent Mail reservations:

```
Agent A @ fmd:  write(foo.rs, offset=0, len=100)   → seq=1001
Agent B @ yto:  write(foo.rs, offset=50, len=100)  → seq=1002

Leader detects: overlapping range [50,100) without reservation
Action: Mark seq=1002 as HAZARD, notify both agents via Agent Mail
```

Hazards are **not prevented** (writes still apply) but are **surfaced immediately** for human resolution.

---

## 3. Write Path Protocol

### 3.1 Operation Flow

```
┌─────────────────────────────────────────────────────────────────────┐
│                        WRITE PATH                                   │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  1. Agent writes to /ntmfs/workspace/foo.rs                         │
│     │                                                               │
│     ▼                                                               │
│  2. FUSE intercepts write() syscall                                 │
│     ├─ Captures: path, offset, length, data bytes                   │
│     ├─ Computes: Blake3 hash of data                                │
│     └─ Checks: Agent Mail reservation status                        │
│     │                                                               │
│     ▼                                                               │
│  3. Worker daemon sends WriteIntent to leader                       │
│     │  {                                                            │
│     │    "type": "write",                                           │
│     │    "path": "foo.rs",                                          │
│     │    "offset": 0,                                               │
│     │    "length": 1024,                                            │
│     │    "content_hash": "blake3:abc123...",                        │
│     │    "content": <bytes>,  // or reference if already known      │
│     │    "agent": "BlueLake",                                       │
│     │    "worker": "fmd",                                           │
│     │    "reservation_id": 42  // or null                           │
│     │  }                                                            │
│     │                                                               │
│     ▼                                                               │
│  4. Leader receives WriteIntent                                     │
│     ├─ Assigns sequence number (monotonic)                          │
│     ├─ Stores content in chunk store (if new hash)                  │
│     ├─ Appends to op log                                            │
│     ├─ Detects hazards (overlapping unreserved writes)              │
│     └─ Computes new Merkle root                                     │
│     │                                                               │
│     ▼                                                               │
│  5. Leader sends Commit to originating worker                       │
│     │  { "seq": 1001, "status": "committed" }                       │
│     │                                                               │
│     ▼                                                               │
│  6. FUSE returns success to agent                                   │
│     │                                                               │
│     ▼                                                               │
│  7. Leader broadcasts log entry via RaptorQ/QUIC                    │
│     │                                                               │
│     ▼                                                               │
│  8. All workers decode and apply operation                          │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### 3.2 Op Log Schema

```rust
/// A single operation in the log
#[derive(Serialize, Deserialize)]
struct OpLogEntry {
    /// Globally unique, monotonically increasing
    seq: u64,

    /// Timestamp of leader commit (for debugging, not ordering)
    committed_at: DateTime<Utc>,

    /// The operation
    op: FileOp,

    /// Agent that initiated (for hazard attribution)
    agent: String,

    /// Worker where operation originated
    origin_worker: String,

    /// Agent Mail reservation (if any)
    reservation_id: Option<u64>,

    /// Hazard flag (set if unreserved overlap detected)
    hazard: Option<HazardInfo>,

    /// Merkle root after this operation
    merkle_root: [u8; 32],
}

#[derive(Serialize, Deserialize)]
enum FileOp {
    Create {
        path: PathBuf,
        mode: u32,
    },
    Write {
        path: PathBuf,
        offset: u64,
        length: u64,
        content_hash: Blake3Hash,
    },
    Truncate {
        path: PathBuf,
        length: u64,
    },
    Rename {
        from: PathBuf,
        to: PathBuf,
    },
    Unlink {
        path: PathBuf,
    },
    Mkdir {
        path: PathBuf,
        mode: u32,
    },
    Rmdir {
        path: PathBuf,
    },
    Chmod {
        path: PathBuf,
        mode: u32,
    },
    Chown {
        path: PathBuf,
        uid: u32,
        gid: u32,
    },
    Symlink {
        target: PathBuf,
        link: PathBuf,
    },
    Hardlink {
        existing: PathBuf,
        new: PathBuf,
    },
    Setxattr {
        path: PathBuf,
        name: String,
        value: Vec<u8>,
    },
    Removexattr {
        path: PathBuf,
        name: String,
    },
    Fsync {
        path: PathBuf,
        datasync: bool,
    },
}

#[derive(Serialize, Deserialize)]
struct HazardInfo {
    /// Conflicting operation sequence number
    conflicts_with: u64,

    /// Type of conflict
    conflict_type: ConflictType,

    /// Notification sent to agents
    notified: bool,
}

#[derive(Serialize, Deserialize)]
enum ConflictType {
    OverlappingWrite { range: (u64, u64) },
    ConcurrentRename,
    WriteAfterUnlink,
}
```

### 3.3 Chunk Store

Content is stored separately from the op log, deduplicated by Blake3 hash:

```rust
struct ChunkStore {
    /// Map from content hash to storage location
    index: DashMap<Blake3Hash, ChunkLocation>,

    /// Memory-mapped chunk files (64KB aligned)
    mmap_arena: MmapArena,
}

struct ChunkLocation {
    /// File containing this chunk
    file_id: u32,

    /// Offset within file
    offset: u64,

    /// Length of chunk
    length: u32,

    /// Reference count (for GC)
    refcount: AtomicU32,
}
```

**Chunk sizing**: Default 64KB chunks, but small files (<64KB) stored inline in the op log entry to reduce indirection.

---

## 4. POSIX Semantics Handling

### 4.1 Standard Syscalls

| Syscall | Handling |
|---------|----------|
| `open()` | Local (no log entry), tracked for fsync ordering |
| `read()` | Local from cached state |
| `write()` | Forwarded to leader, blocks until committed |
| `close()` | Local (no log entry) |
| `fsync()` | Forwarded as barrier, blocks until all prior writes durable |
| `rename()` | Atomic via leader (single log entry) |
| `unlink()` | Forwarded to leader |
| `mkdir()` / `rmdir()` | Forwarded to leader |
| `stat()` / `lstat()` | Local from cached state |
| `chmod()` / `chown()` | Forwarded to leader |
| `link()` / `symlink()` | Forwarded to leader |

### 4.2 mmap Handling

**Problem**: mmap writes bypass the `write()` syscall entirely. Changes happen directly to memory pages.

**Solution**: Track dirty pages and capture on msync/munmap/close:

```rust
impl FuseHandler {
    fn mmap(&self, path: &Path, flags: u32) -> Result<MmapHandle> {
        // Create mapping with write-tracking
        let handle = MmapHandle::new(path, flags);

        if flags & PROT_WRITE != 0 {
            // Register for dirty page tracking
            self.dirty_tracker.register(handle.id, path.to_owned());
        }

        Ok(handle)
    }

    fn msync(&self, handle: MmapHandle, flags: u32) -> Result<()> {
        // Capture all dirty pages
        let dirty_ranges = self.dirty_tracker.get_dirty_ranges(handle.id);

        for (offset, length) in dirty_ranges {
            // Read the actual bytes from the mapping
            let data = handle.read_range(offset, length);

            // Forward as write to leader
            self.forward_write(handle.path, offset, data)?;
        }

        // Wait for leader commit
        self.wait_for_commits()?;

        Ok(())
    }
}
```

**Limitation**: We cannot intercept every memory store. Agents using mmap for writes must call `msync()` or `munmap()` to propagate changes. This matches POSIX semantics (mmap without msync has undefined durability).

### 4.3 Rename Atomicity

Renames are atomic even when crossing directories:

```rust
fn rename(&self, from: &Path, to: &Path) -> Result<()> {
    // Single op log entry for atomic rename
    let entry = OpLogEntry {
        op: FileOp::Rename {
            from: from.to_owned(),
            to: to.to_owned(),
        },
        // ...
    };

    // Leader commits atomically
    // All workers see rename as single operation
    self.forward_to_leader(entry)?;

    Ok(())
}
```

**Edge case**: Rename over existing file is atomic replacement (old file unlinked, new file created at target path, as single operation).

### 4.4 O_DIRECT Handling

**Problem**: O_DIRECT bypasses the page cache and may bypass FUSE in some configurations.

**Solutions** (in order of preference):

1. **FUSE passthrough mode** (Linux 5.10+): Use `FUSE_PASSTHROUGH` for reads, intercept writes
2. **LD_PRELOAD shim**: Intercept `open()` with O_DIRECT, redirect to non-O_DIRECT with manual alignment
3. **Disable O_DIRECT**: Strip the flag (acceptable for most agent workloads)

```rust
fn open(&self, path: &Path, flags: u32) -> Result<FileHandle> {
    let mut adjusted_flags = flags;

    if flags & O_DIRECT != 0 {
        // Log warning, strip O_DIRECT
        warn!("O_DIRECT stripped for {}", path.display());
        adjusted_flags &= !O_DIRECT;
    }

    // Continue with adjusted flags
    self.do_open(path, adjusted_flags)
}
```

### 4.5 File Locks (flock/fcntl)

File locks become **cluster-wide** via integration with Agent Mail:

```rust
fn flock(&self, path: &Path, operation: FlockOp) -> Result<()> {
    match operation {
        FlockOp::LockShared => {
            // Acquire shared reservation via Agent Mail
            self.agent_mail.file_reservation_paths(
                path,
                exclusive: false,
                ttl_seconds: 3600,
            )?;
        }
        FlockOp::LockExclusive => {
            // Acquire exclusive reservation
            self.agent_mail.file_reservation_paths(
                path,
                exclusive: true,
                ttl_seconds: 3600,
            )?;
        }
        FlockOp::Unlock => {
            // Release reservation
            self.agent_mail.release_file_reservations(path)?;
        }
    }
    Ok(())
}
```

---

## 5. Replication Protocol

### 5.1 RaptorQ Symbolization

Each committed log entry is encoded using RaptorQ (RFC 6330) for loss-tolerant broadcast:

```rust
struct ReplicationEncoder {
    /// RaptorQ encoder instance
    encoder: RaptorQEncoder,

    /// Symbol size (must divide evenly into MTU)
    symbol_size: usize,  // Default: 1280 bytes

    /// Repair symbol overhead (percentage extra symbols)
    repair_overhead: f32,  // Default: 0.1 (10%)
}

impl ReplicationEncoder {
    fn encode_entry(&self, entry: &OpLogEntry) -> SymbolStream {
        // Serialize entry
        let data = bincode::serialize(entry)?;

        // Create source block
        let source_block = SourceBlock::new(&data, self.symbol_size);

        // Generate source symbols + repair symbols
        let num_source = source_block.num_symbols();
        let num_repair = (num_source as f32 * self.repair_overhead).ceil() as usize;

        // Return iterator over symbols
        SymbolStream {
            source_block,
            num_source,
            num_repair,
            current: 0,
        }
    }
}
```

### 5.2 QUIC Transport

Each worker maintains a QUIC connection to the leader:

```rust
struct WorkerConnection {
    /// QUIC connection handle
    quic: QuicConnection,

    /// Streams
    write_stream: QuicStream,      // Worker → Leader: WriteIntents
    commit_stream: QuicStream,     // Leader → Worker: Commit confirmations
    replication_stream: QuicStream, // Leader → Worker: RaptorQ symbols
    control_stream: QuicStream,    // Bidirectional: heartbeats, config
}

impl WorkerConnection {
    async fn send_write_intent(&self, intent: WriteIntent) -> Result<CommitResponse> {
        // Serialize and send
        self.write_stream.send(bincode::serialize(&intent)?).await?;

        // Wait for commit confirmation
        let response = self.commit_stream.recv().await?;
        Ok(bincode::deserialize(&response)?)
    }

    async fn receive_replication(&self) -> Result<OpLogEntry> {
        // Receive symbols until we can decode
        let mut decoder = RaptorQDecoder::new();

        loop {
            let symbol = self.replication_stream.recv().await?;
            decoder.add_symbol(symbol)?;

            if decoder.can_decode() {
                let data = decoder.decode()?;
                return Ok(bincode::deserialize(&data)?);
            }
        }
    }
}
```

### 5.3 Backpressure and Flow Control

```rust
struct FlowController {
    /// Maximum outstanding unacknowledged entries
    max_inflight: usize,  // Default: 1000

    /// Current inflight count per worker
    inflight: DashMap<WorkerId, AtomicUsize>,

    /// Backpressure signal
    backpressure: AtomicBool,
}

impl FlowController {
    fn should_pause_replication(&self, worker: WorkerId) -> bool {
        let count = self.inflight.get(&worker)
            .map(|c| c.load(Ordering::Relaxed))
            .unwrap_or(0);

        count >= self.max_inflight
    }

    fn on_worker_ack(&self, worker: WorkerId, seq: u64) {
        // Decrease inflight count
        if let Some(count) = self.inflight.get(&worker) {
            count.fetch_sub(1, Ordering::Relaxed);
        }
    }
}
```

### 5.4 Worker Catch-Up

When a worker reconnects after being offline:

```rust
async fn catch_up(&self, worker: WorkerId, last_seq: u64) -> Result<()> {
    let current_seq = self.log.latest_seq();

    if current_seq - last_seq > SNAPSHOT_THRESHOLD {
        // Too far behind, send snapshot
        let snapshot = self.create_snapshot()?;
        self.send_snapshot(worker, snapshot).await?;
    } else {
        // Replay log entries
        for seq in (last_seq + 1)..=current_seq {
            let entry = self.log.get(seq)?;
            self.send_entry(worker, entry).await?;
        }
    }

    Ok(())
}
```

---

## 6. Agent Mail Integration

### 6.1 Cluster-Wide Reservations

File reservations are enforced across all workers:

```rust
struct ClusterReservationManager {
    /// Local Agent Mail client
    agent_mail: AgentMailClient,

    /// Cache of active reservations
    reservations: DashMap<PathPattern, ReservationInfo>,
}

impl ClusterReservationManager {
    async fn check_reservation(&self, path: &Path, agent: &str) -> ReservationStatus {
        // Check if agent holds reservation
        if let Some(res) = self.reservations.get(path) {
            if res.holder == agent {
                return ReservationStatus::Held;
            } else {
                return ReservationStatus::HeldByOther(res.holder.clone());
            }
        }

        ReservationStatus::Unreserved
    }

    async fn acquire(&self, path: &Path, agent: &str, exclusive: bool) -> Result<()> {
        // Forward to Agent Mail
        self.agent_mail.file_reservation_paths(
            project_key: &self.project_key,
            agent_name: agent,
            paths: vec![path.to_string_lossy().to_string()],
            exclusive,
            ttl_seconds: 3600,
        ).await?;

        // Update local cache
        self.reservations.insert(path.to_path_buf(), ReservationInfo {
            holder: agent.to_string(),
            exclusive,
            expires: Instant::now() + Duration::from_secs(3600),
        });

        Ok(())
    }
}
```

### 6.2 Hazard Surfacing

When an unreserved write conflicts with another:

```rust
async fn surface_hazard(&self, entry: &OpLogEntry, conflict: &OpLogEntry) -> Result<()> {
    let hazard = HazardInfo {
        conflicts_with: conflict.seq,
        conflict_type: detect_conflict_type(entry, conflict),
        notified: false,
    };

    // Send Agent Mail notification to both agents
    self.agent_mail.send_message(
        to: vec![entry.agent.clone(), conflict.agent.clone()],
        subject: format!("HAZARD: Conflicting writes to {}", entry.path()),
        body_md: format!(
            "## Write Conflict Detected\n\n\
            **File**: `{}`\n\n\
            | Agent | Seq | Operation |\n\
            |-------|-----|----------|\n\
            | {} | {} | {} |\n\
            | {} | {} | {} |\n\n\
            Please coordinate via Agent Mail to resolve.",
            entry.path(),
            conflict.agent, conflict.seq, conflict.op.description(),
            entry.agent, entry.seq, entry.op.description(),
        ),
        importance: "high",
    ).await?;

    Ok(())
}
```

### 6.3 Lock State in Merkle DAG

Reservation state is included in the Merkle root for consistency:

```rust
fn compute_merkle_root(&self, seq: u64) -> Blake3Hash {
    let mut hasher = Blake3Hasher::new();

    // Hash file tree
    hasher.update(&self.file_tree.root_hash());

    // Hash reservation state
    for (path, res) in self.reservations.iter() {
        hasher.update(path.as_bytes());
        hasher.update(res.holder.as_bytes());
        hasher.update(&[res.exclusive as u8]);
    }

    // Hash sequence number
    hasher.update(&seq.to_le_bytes());

    hasher.finalize()
}
```

---

## 7. Scheduling: Thompson Sampling

### 7.1 Worker State Model

```rust
struct WorkerState {
    /// Worker identifier
    id: WorkerId,

    /// Posterior distribution for cost (Normal-Inverse-Gamma)
    cost_posterior: NormalInverseGamma,

    /// Current load metrics
    current_agents: u32,
    cpu_utilization: f64,
    memory_pressure: f64,
    sync_lag_ms: f64,

    /// Stability constraint
    arrival_rate: f64,      // λ: agents spawned per minute
    service_rate: f64,      // μ: agents completed per minute
}

impl WorkerState {
    fn utilization(&self) -> f64 {
        // ρ = λ / μ
        if self.service_rate > 0.0 {
            self.arrival_rate / self.service_rate
        } else {
            1.0  // Saturated
        }
    }

    fn is_stable(&self) -> bool {
        self.utilization() < 0.8
    }
}
```

### 7.2 Thompson Sampling Selection

```rust
impl Scheduler {
    fn select_worker(&self, rng: &mut impl Rng) -> WorkerId {
        let mut best_worker = None;
        let mut best_sample = f64::MAX;

        for worker in &self.workers {
            // Skip unstable workers
            if !worker.is_stable() {
                continue;
            }

            // Sample from posterior
            let sample = worker.cost_posterior.sample(rng);

            if sample < best_sample {
                best_sample = sample;
                best_worker = Some(worker.id);
            }
        }

        best_worker.unwrap_or_else(|| {
            // All workers saturated, pick least loaded
            self.workers.iter()
                .min_by_key(|w| w.current_agents)
                .map(|w| w.id)
                .unwrap()
        })
    }

    fn update_posterior(&mut self, worker: WorkerId, observed_cost: f64) {
        if let Some(w) = self.workers.iter_mut().find(|w| w.id == worker) {
            w.cost_posterior.update(observed_cost);
        }
    }
}
```

### 7.3 Cost Function

```rust
fn compute_cost(metrics: &WorkerMetrics, task_duration: Duration) -> f64 {
    let base_cost = task_duration.as_secs_f64();

    // Penalty for high CPU
    let cpu_penalty = if metrics.cpu_utilization > 0.8 {
        (metrics.cpu_utilization - 0.8) * 10.0
    } else {
        0.0
    };

    // Penalty for sync lag
    let sync_penalty = metrics.sync_lag_ms / 100.0;

    // Penalty for memory pressure
    let mem_penalty = if metrics.memory_pressure > 0.7 {
        (metrics.memory_pressure - 0.7) * 5.0
    } else {
        0.0
    };

    base_cost + cpu_penalty + sync_penalty + mem_penalty
}
```

---

## 8. NTM Integration

### 8.1 Configuration

```toml
# ~/.config/ntm/config.toml

[hypersync]
enabled = true
role = "leader"  # or "worker"

[hypersync.leader]
# Leader-specific config (only if role = "leader")
bind_address = "0.0.0.0:7890"
log_path = "/var/lib/ntm/hypersync/log"
chunk_path = "/var/lib/ntm/hypersync/chunks"

[hypersync.workers]
# Worker pool definition
fmd = { host = "51.222.245.56", port = 7890 }
yto = { host = "37.187.75.150", port = 7890 }
jain_ovh_box = { host = "57.129.136.76", port = 7890 }

[hypersync.scheduler]
algorithm = "thompson_sampling"
stability_threshold = 0.8
cost_window_seconds = 300

[hypersync.replication]
transport = "quic"  # or "quic+multicast"
raptorq_symbol_size = 1280
raptorq_repair_overhead = 0.1
quic_max_streams = 100

[hypersync.agent_mail]
enabled = true
project_key = "/data/projects/myproject"
hazard_notifications = true
```

### 8.2 CLI Commands

```bash
# Initialize HyperSync cluster
ntm hypersync init --workers fmd,yto,jain_ovh_box

# Show cluster status
ntm hypersync status
# Output:
#   Leader: css (this machine)
#   Workers:
#     fmd:        12 agents, ρ=0.45, sync_lag=2ms
#     yto:         8 agents, ρ=0.32, sync_lag=3ms
#     jain_ovh_box: 15 agents, ρ=0.58, sync_lag=5ms
#   Total: 35 agents across 3 workers

# Spawn with automatic placement
ntm spawn --name agent-1 --distribute "claude --model opus"

# Spawn on specific worker
ntm spawn --name agent-2 --worker fmd "codex"

# Spawn batch with distribution
ntm spawn --name "agent-{1..20}" --distribute "claude --model sonnet"

# View replication status
ntm hypersync log --tail 10
# Output:
#   seq=1001  write(src/main.rs, 0, 1024)  agent=BlueLake@fmd  ✓ replicated
#   seq=1002  rename(old.rs, new.rs)       agent=RedCat@yto   ✓ replicated
#   seq=1003  write(src/lib.rs, 512, 256)  agent=BlueLake@fmd  HAZARD: conflicts with seq=1001

# Force snapshot
ntm hypersync snapshot --output /tmp/snapshot.tar.zst

# Worker catch-up
ntm hypersync catchup --worker fmd --from-seq 1000
```

### 8.3 Robot Mode Extensions

```json
{
  "command": "spawn",
  "args": {
    "name": "agent-42",
    "cmd": "claude --model opus",
    "distribute": true,
    "worker_preference": ["fmd", "yto"]
  }
}
```

Response:

```json
{
  "status": "ok",
  "session": "agent-42",
  "worker": "fmd",
  "hypersync": {
    "mount_path": "/ntmfs/workspace",
    "leader": "css",
    "sync_lag_ms": 2
  }
}
```

---

## 9. Performance Optimizations

### 9.1 io_uring Integration

```rust
struct IoUringBatcher {
    ring: IoUring,
    registered_buffers: Vec<&'static mut [u8]>,
    pending: VecDeque<PendingOp>,
}

impl IoUringBatcher {
    fn batch_reads(&mut self, ops: &[ReadOp]) -> Result<Vec<Vec<u8>>> {
        // Submit all reads in single syscall
        for (i, op) in ops.iter().enumerate() {
            let sqe = opcode::Read::new(
                types::Fd(op.fd),
                self.registered_buffers[i].as_mut_ptr(),
                op.len as u32,
            )
            .offset(op.offset)
            .build()
            .flags(squeue::Flags::BUFFER_SELECT);

            unsafe { self.ring.submission().push(&sqe)?; }
        }

        // Single syscall for all operations
        self.ring.submit_and_wait(ops.len())?;

        // Collect results
        let mut results = Vec::with_capacity(ops.len());
        for cqe in self.ring.completion() {
            let len = cqe.result() as usize;
            let buf_idx = cqe.user_data() as usize;
            results.push(self.registered_buffers[buf_idx][..len].to_vec());
        }

        Ok(results)
    }
}
```

### 9.2 Memory-Mapped Chunk Store

```rust
struct MmapChunkStore {
    /// Memory-mapped arena with huge pages
    arena: MmapMut,

    /// Chunk index
    index: DashMap<Blake3Hash, (usize, usize)>,  // (offset, len)

    /// Free list for allocation
    free_list: Mutex<BTreeMap<usize, usize>>,  // offset → size
}

impl MmapChunkStore {
    fn new(path: &Path, size: usize) -> Result<Self> {
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .open(path)?;

        file.set_len(size as u64)?;

        // Map with huge pages
        let arena = unsafe {
            MmapOptions::new()
                .len(size)
                .huge(Some(HugepageSize::Huge2MB))
                .populate()
                .map_mut(&file)?
        };

        Ok(Self {
            arena,
            index: DashMap::new(),
            free_list: Mutex::new(BTreeMap::from([(0, size)])),
        })
    }

    fn get(&self, hash: &Blake3Hash) -> Option<&[u8]> {
        self.index.get(hash).map(|entry| {
            let (offset, len) = *entry;
            &self.arena[offset..offset + len]
        })
    }

    fn put(&self, hash: Blake3Hash, data: &[u8]) -> Result<()> {
        // Allocate from free list
        let offset = self.allocate(data.len())?;

        // Copy data (zero-copy would require caller cooperation)
        self.arena[offset..offset + data.len()].copy_from_slice(data);

        // Update index
        self.index.insert(hash, (offset, data.len()));

        Ok(())
    }
}
```

### 9.3 Lock-Free Log Append

```rust
struct LockFreeLog {
    /// Current write position
    write_pos: AtomicU64,

    /// Memory-mapped log segments
    segments: RwLock<Vec<MmapMut>>,

    /// Segment size (1GB default)
    segment_size: usize,
}

impl LockFreeLog {
    fn append(&self, entry: &OpLogEntry) -> Result<u64> {
        let data = bincode::serialize(entry)?;
        let len = data.len() as u64;

        // Reserve space atomically
        let offset = self.write_pos.fetch_add(len + 8, Ordering::SeqCst);

        // Write length prefix + data
        let segment_idx = (offset / self.segment_size as u64) as usize;
        let segment_offset = (offset % self.segment_size as u64) as usize;

        let segments = self.segments.read();
        let segment = &segments[segment_idx];

        // Write length
        segment[segment_offset..segment_offset + 8]
            .copy_from_slice(&len.to_le_bytes());

        // Write data
        segment[segment_offset + 8..segment_offset + 8 + data.len()]
            .copy_from_slice(&data);

        // Ensure visibility
        atomic::fence(Ordering::Release);

        Ok(offset)
    }
}
```

### 9.4 Zero-Copy Network Path

```rust
async fn send_chunk_zero_copy(
    socket: &UdpSocket,
    chunk: &MmapSlice,
    dest: SocketAddr,
) -> Result<()> {
    // Use sendfile-style zero-copy if available
    #[cfg(target_os = "linux")]
    {
        use std::os::unix::io::AsRawFd;

        // Create iovec pointing to mmap'd region
        let iov = libc::iovec {
            iov_base: chunk.as_ptr() as *mut _,
            iov_len: chunk.len(),
        };

        // Send without copying
        let msg = libc::msghdr {
            msg_iov: &iov as *const _ as *mut _,
            msg_iovlen: 1,
            // ... other fields
        };

        unsafe {
            libc::sendmsg(socket.as_raw_fd(), &msg, 0);
        }
    }

    Ok(())
}
```

---

## 10. Failure Handling

### 10.1 Leader Failure

**Design decision**: Leader failure halts the fabric. This is intentional — we prefer availability within a consistent system over split-brain.

```rust
impl LeaderHealthMonitor {
    fn on_leader_unreachable(&self) {
        // Pause all local writes
        self.fuse_handler.set_read_only(true);

        // Notify agents
        self.broadcast_notification(
            "HyperSync leader unreachable. Workspace is read-only until reconnection."
        );

        // Begin reconnection attempts
        self.reconnect_loop.start();
    }

    fn on_leader_restored(&self) {
        // Resume writes
        self.fuse_handler.set_read_only(false);

        // Catch up from last known seq
        self.catch_up_from(self.last_committed_seq);

        // Notify agents
        self.broadcast_notification("HyperSync connection restored.");
    }
}
```

### 10.2 Worker Failure

Workers can fail independently without affecting others:

```rust
impl Scheduler {
    fn on_worker_failure(&mut self, worker: WorkerId) {
        // Mark worker as unavailable
        self.workers.get_mut(&worker).map(|w| w.available = false);

        // Reassign agents to other workers
        for agent in self.agents_on_worker(worker) {
            let new_worker = self.select_worker(&mut rand::thread_rng());
            self.reassign_agent(agent, new_worker);
        }

        // Notify affected agents
        self.notify_agents_of_migration(worker);
    }
}
```

### 10.3 Network Partition

During partition, workers continue serving reads from cached state:

```rust
impl PartitionHandler {
    fn during_partition(&self) {
        // Reads: serve from local cache
        self.fuse_handler.enable_stale_reads(true);

        // Writes: queue locally, apply optimistically
        self.fuse_handler.enable_write_queue(true);

        // Track divergence
        self.divergence_tracker.start();
    }

    fn after_partition_heals(&self) {
        // Submit queued writes to leader
        for write in self.write_queue.drain() {
            match self.submit_to_leader(write).await {
                Ok(_) => { /* Success */ }
                Err(Conflict(other)) => {
                    // Our optimistic write conflicts
                    self.surface_hazard(write, other);
                }
            }
        }
    }
}
```

---

## 11. Observability

### 11.1 Metrics

```rust
struct HyperSyncMetrics {
    // Replication
    log_entries_committed: Counter,
    log_entries_replicated: Counter,
    replication_lag_ms: Histogram,

    // Transport
    quic_bytes_sent: Counter,
    quic_bytes_received: Counter,
    raptorq_symbols_sent: Counter,
    raptorq_decode_failures: Counter,

    // FUSE
    fuse_read_ops: Counter,
    fuse_write_ops: Counter,
    fuse_latency_ms: Histogram,

    // Scheduling
    agents_spawned: Counter,
    agents_per_worker: GaugeVec,
    worker_utilization: GaugeVec,

    // Hazards
    hazards_detected: Counter,
    hazards_resolved: Counter,
}
```

### 11.2 Tracing

```rust
#[instrument(skip(self, data))]
async fn handle_write(&self, path: &Path, offset: u64, data: &[u8]) -> Result<()> {
    let span = info_span!("write", path = %path.display(), offset, len = data.len());

    async move {
        // Forward to leader
        let seq = self.forward_to_leader(path, offset, data).await?;
        info!(seq, "write committed");

        Ok(())
    }
    .instrument(span)
    .await
}
```

### 11.3 Dashboard

```
┌─────────────────────────────────────────────────────────────────────┐
│                    HYPERSYNC DASHBOARD                              │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  CLUSTER STATUS: ● HEALTHY                                          │
│                                                                     │
│  Leader: css (this machine)                                         │
│  Log seq: 145,892 | Merkle root: abc123...                          │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │  WORKERS                                                    │   │
│  ├──────────┬────────┬───────┬──────────┬───────────┬─────────┤   │
│  │  Name    │ Agents │  ρ    │ Sync Lag │ CPU       │ Memory  │   │
│  ├──────────┼────────┼───────┼──────────┼───────────┼─────────┤   │
│  │  fmd     │   12   │ 0.45  │   2ms    │ ████░░ 67%│ ███░░ 54%│  │
│  │  yto     │    8   │ 0.32  │   3ms    │ ██░░░░ 34%│ ██░░░ 41%│  │
│  │  jain    │   15   │ 0.58  │   5ms    │ █████░ 82%│ ████░ 73%│  │
│  └──────────┴────────┴───────┴──────────┴───────────┴─────────┘   │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │  RECENT OPERATIONS                                          │   │
│  ├─────────┬────────────────────────────────────┬──────────────┤   │
│  │  Seq    │ Operation                          │ Status       │   │
│  ├─────────┼────────────────────────────────────┼──────────────┤   │
│  │  145892 │ write(src/main.rs, 0, 1024)        │ ✓ replicated │   │
│  │  145891 │ rename(old.rs → new.rs)            │ ✓ replicated │   │
│  │  145890 │ write(src/lib.rs, 512, 256)        │ ⚠ HAZARD     │   │
│  │  145889 │ mkdir(tests/)                      │ ✓ replicated │   │
│  └─────────┴────────────────────────────────────┴──────────────┘   │
│                                                                     │
│  HAZARDS: 1 active | RESERVATIONS: 23 active                       │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 12. Implementation Phases

### Phase 0: Profiling (1 week)

**Goal**: Understand real agent I/O patterns before building.

```bash
# Instrument current NTM sessions
ntm profile start --session myproject --output /tmp/io-profile.json

# Run normal agent workload for 24 hours

ntm profile stop --session myproject

# Analyze
ntm profile analyze /tmp/io-profile.json
# Output:
#   Files written: 1,247
#   Total bytes: 45.2 MB
#   Avg write size: 2.3 KB
#   Rename frequency: 0.3/minute
#   mmap usage: 0.1% of writes
#   O_DIRECT usage: 0%
#   fsync frequency: 12/minute
```

### Phase 1: Single-Host Fabric (2 weeks)

**Goal**: Validate FUSE + op log + chunk store on leader only.

- Implement FUSE handler with full POSIX semantics
- Implement op log with Merkle roots
- Implement chunk store with Blake3 deduplication
- Validate correctness with POSIX test suite

### Phase 2: Replication (2 weeks)

**Goal**: Add worker replicas with RaptorQ broadcast.

- Implement RaptorQ encoder/decoder (leverage asupersync)
- Implement QUIC transport layer
- Implement worker daemon
- Validate sync latency < 10ms on LAN

### Phase 3: Agent Mail Integration (1 week)

**Goal**: Cluster-wide reservations and hazard detection.

- Implement reservation forwarding
- Implement hazard detection and notification
- Validate reservation semantics across workers

### Phase 4: Scheduling (1 week)

**Goal**: Thompson Sampling placement with stability constraints.

- Implement worker state tracking
- Implement Thompson Sampling selection
- Implement NTM `--distribute` flag
- Validate load balancing under heavy spawn

### Phase 5: Hardening (2 weeks)

**Goal**: Production readiness.

- Failure handling (leader restart, worker failure, partition)
- Snapshot and catch-up
- Observability (metrics, tracing, dashboard)
- Performance optimization (io_uring, huge pages)

---

## 13. Open Questions

### 13.1 Decided

| Question | Decision | Rationale |
|----------|----------|-----------|
| Transport protocol | QUIC + RaptorQ | NAT traversal, congestion control, loss tolerance |
| Write interception | FUSE | Complete, portable, proven |
| Consistency model | Total-order | Required for single-workspace semantics |
| Leader failure mode | Halt fabric | Prefer consistency over availability |

### 13.2 To Be Determined

| Question | Options | Notes |
|----------|---------|-------|
| mmap handling | (a) Track dirty pages, (b) Require explicit msync | Depends on agent usage patterns |
| O_DIRECT handling | (a) Strip flag, (b) LD_PRELOAD shim | Depends on profiling results |
| Chunk size | 64KB default | May tune based on profiling |
| Snapshot format | (a) tar.zst, (b) custom binary | Trade-off: compatibility vs speed |
| eBPF hints | (a) Implement, (b) Skip | Low priority, optional optimization |

---

## 14. References

- [RFC 6330: RaptorQ Forward Error Correction](https://www.rfc-editor.org/rfc/rfc6330)
- [QUIC: A UDP-Based Multiplexed and Secure Transport](https://www.rfc-editor.org/rfc/rfc9000)
- [FUSE: Filesystem in Userspace](https://www.kernel.org/doc/html/latest/filesystems/fuse.html)
- [io_uring: Efficient I/O with Linux](https://kernel.dk/io_uring.pdf)
- [Blake3: Fast Cryptographic Hash Function](https://github.com/BLAKE3-team/BLAKE3)
- [Thompson Sampling Tutorial](https://web.stanford.edu/~bvr/pubs/TS_Tutorial.pdf)

---

## Appendix A: Wire Protocol Messages

```rust
#[derive(Serialize, Deserialize)]
enum LeaderMessage {
    /// Commit confirmation for a write
    Commit { seq: u64, merkle_root: [u8; 32] },

    /// Replicated log entry (RaptorQ encoded)
    ReplicatedEntry { symbols: Vec<RaptorQSymbol> },

    /// Snapshot for catch-up
    Snapshot {
        up_to_seq: u64,
        merkle_root: [u8; 32],
        data: Vec<u8>,  // Compressed
    },

    /// Hazard notification
    Hazard { entry: OpLogEntry, conflicts_with: u64 },

    /// Reservation update
    ReservationUpdate { path: PathBuf, holder: Option<String>, exclusive: bool },

    /// Heartbeat
    Heartbeat { leader_seq: u64, timestamp: u64 },
}

#[derive(Serialize, Deserialize)]
enum WorkerMessage {
    /// Write intent from agent
    WriteIntent {
        path: PathBuf,
        offset: u64,
        content_hash: [u8; 32],
        content: Option<Vec<u8>>,  // None if leader already has chunk
        agent: String,
        reservation_id: Option<u64>,
    },

    /// Other file operation
    FileOp { op: FileOp, agent: String },

    /// Acknowledgement of replicated entry
    Ack { seq: u64 },

    /// Catch-up request
    CatchUpRequest { from_seq: u64 },

    /// Heartbeat response
    HeartbeatAck { worker_id: WorkerId, last_applied_seq: u64 },

    /// Metrics report
    Metrics {
        cpu: f64,
        memory: f64,
        agent_count: u32,
        sync_lag_ms: f64,
    },
}
```

---

## Appendix B: Crate Structure

```
hypersync/
├── Cargo.toml
├── src/
│   ├── lib.rs
│   │
│   ├── log/
│   │   ├── mod.rs
│   │   ├── entry.rs        # OpLogEntry, FileOp
│   │   ├── store.rs        # Lock-free log storage
│   │   └── merkle.rs       # Merkle DAG
│   │
│   ├── chunk/
│   │   ├── mod.rs
│   │   ├── store.rs        # Content-addressed chunk store
│   │   └── hash.rs         # Blake3 utilities
│   │
│   ├── fuse/
│   │   ├── mod.rs
│   │   ├── handler.rs      # FUSE operation handlers
│   │   ├── mmap.rs         # mmap tracking
│   │   └── posix.rs        # POSIX semantics helpers
│   │
│   ├── transport/
│   │   ├── mod.rs
│   │   ├── quic.rs         # QUIC connection management
│   │   ├── raptorq.rs      # RaptorQ encoding/decoding
│   │   └── multicast.rs    # Optional LAN multicast
│   │
│   ├── leader/
│   │   ├── mod.rs
│   │   ├── server.rs       # Leader daemon
│   │   ├── scheduler.rs    # Thompson Sampling
│   │   └── hazard.rs       # Conflict detection
│   │
│   ├── worker/
│   │   ├── mod.rs
│   │   ├── daemon.rs       # Worker daemon
│   │   ├── cache.rs        # Local cache management
│   │   └── catchup.rs      # Log replay / snapshot
│   │
│   ├── agentmail/
│   │   ├── mod.rs
│   │   ├── reservations.rs # Cluster-wide reservations
│   │   └── notifications.rs # Hazard notifications
│   │
│   └── metrics/
│       ├── mod.rs
│       └── prometheus.rs   # Metrics export
│
├── tests/
│   ├── posix_compliance.rs
│   ├── replication.rs
│   ├── hazard_detection.rs
│   └── scheduling.rs
│
└── benches/
    ├── write_throughput.rs
    ├── replication_latency.rs
    └── fuse_overhead.rs
```

---

*End of specification.*
