-- Scope idempotent send operations per session (#245 review follow-up).
--
-- 017 keyed send_operations on operation_id alone, a single global
-- namespace: the same operation ID used in two different sessions collided
-- as a spurious IDEMPOTENCY_CONFLICT. Rebuild the table with a composite
-- (operation_id, session_name) primary key so sessions are independent
-- namespaces. Receipts are short-lived operational data (7-day GC
-- retention) and the old shape never appeared in a tagged release, so a
-- drop-and-recreate is safe.
DROP TABLE IF EXISTS send_operations;

CREATE TABLE send_operations (
    operation_id   TEXT NOT NULL,
    session_name   TEXT NOT NULL,
    binding_hash   TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    payload_bytes  INTEGER NOT NULL,
    status         TEXT NOT NULL,            -- in_progress | completed
    outcome_json   TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL,
    completed_at   TIMESTAMP,
    PRIMARY KEY (operation_id, session_name)
);

CREATE INDEX IF NOT EXISTS idx_send_operations_session
    ON send_operations (session_name, created_at);
