-- Work rows alone cannot prove either readiness or an empty queue. Retain the
-- complete source-bound collection separately from display-limited work rows.
CREATE TABLE runtime_work_snapshots (
    project_dir TEXT PRIMARY KEY NOT NULL CHECK (length(project_dir) > 0),
    revision TEXT NOT NULL CHECK (length(revision) = 64),
    collected_at_ns INTEGER NOT NULL,
    stale_after_ns INTEGER NOT NULL CHECK (stale_after_ns > collected_at_ns),
    payload BLOB NOT NULL CHECK (length(payload) BETWEEN 1 AND 33554432)
);
CREATE INDEX idx_runtime_work_snapshots_expiry ON runtime_work_snapshots(stale_after_ns);
