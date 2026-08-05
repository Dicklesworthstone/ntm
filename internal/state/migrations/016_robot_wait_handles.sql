CREATE TABLE IF NOT EXISTS robot_wait_handles (
    id TEXT PRIMARY KEY,
    session_name TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    canceled_at TIMESTAMP,
    completed_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_robot_wait_handles_active
    ON robot_wait_handles (id, completed_at);
