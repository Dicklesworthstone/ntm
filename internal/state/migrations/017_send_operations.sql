-- Durable idempotent send operations (#245).
--
-- One row per caller-supplied operation ID. The row is claimed atomically
-- (INSERT OR IGNORE) before any keystroke is injected, binds the operation
-- to its canonical targets + payload digest, and records per-target
-- admission receipts on completion so a caller that timed out can
-- reconcile the outcome later by operation ID. The payload itself is never
-- stored — only its SHA-256 digest and byte count.
CREATE TABLE IF NOT EXISTS send_operations (
    operation_id   TEXT PRIMARY KEY,
    session_name   TEXT NOT NULL,
    binding_hash   TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    payload_bytes  INTEGER NOT NULL,
    status         TEXT NOT NULL,            -- in_progress | completed
    outcome_json   TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL,
    completed_at   TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_send_operations_session
    ON send_operations (session_name, created_at);
