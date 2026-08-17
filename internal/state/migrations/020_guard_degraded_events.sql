-- 020_guard_degraded_events.sql — pre-commit guard degraded-mode ledger (A1,
-- bd-ws1-truth-safety-l5ddi.1). Every time the guard hook fails OPEN (Agent
-- Mail unreachable at commit time, commit allowed anyway) the hook records a
-- row here, because a WARN line in commit scrollback is unobserved by
-- construction. `ntm doctor` and the dashboard doctor endpoint surface the
-- count so degraded protection is visible after the fact.
CREATE TABLE IF NOT EXISTS guard_degraded_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_path   TEXT NOT NULL,
    project_key TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_guard_degraded_events_created
    ON guard_degraded_events(created_at);
