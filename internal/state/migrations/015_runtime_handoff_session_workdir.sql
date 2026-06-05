-- NTM State Store: drop singleton CHECK on runtime_handoff
-- Version: 015
-- Issue: ntm#135
--
-- Migration 011 declared `runtime_handoff` with `id INTEGER PRIMARY KEY
-- CHECK (id = 1)`, which restricted the table to a single row. The
-- intent — per the bead trail and the in-code comment that already
-- mentioned `(session_name, working_dir)` as the natural scope — was
-- always for one row per (session, working dir) pair, so that the same
-- session_name running in two different checkouts has independent
-- handoff state.
--
-- SQLite's ALTER TABLE can't drop a CHECK constraint, so the migration
-- does the canonical rebuild-via-shadow-table dance: create the new
-- table, copy rows, drop the old table, rename. The migration runner
-- already wraps each file in a transaction, so this migration does not
-- start its own BEGIN/COMMIT (doing so produces "cannot start a
-- transaction within a transaction").

CREATE TABLE runtime_handoff_v2 (
    -- A rowid-only INTEGER PRIMARY KEY without any CHECK constraint.
    -- Uniqueness is enforced by the unique index below.
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_name TEXT NOT NULL,
    -- New scoping column. Existing rows get '' so the index below holds.
    -- New writes are expected to populate this; the empty string is a
    -- valid value (used by callers that don't track a working dir).
    working_dir TEXT NOT NULL DEFAULT '',
    status TEXT,
    goal TEXT,
    goal_disclosure TEXT,
    now_text TEXT,
    now_disclosure TEXT,
    updated_at TIMESTAMP,
    active_beads TEXT,
    agent_mail_threads TEXT,
    blockers TEXT,
    blocker_disclosures TEXT,
    files TEXT,
    collected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stale_after TIMESTAMP NOT NULL
);

INSERT INTO runtime_handoff_v2 (
    session_name, working_dir, status, goal, goal_disclosure, now_text,
    now_disclosure, updated_at, active_beads, agent_mail_threads, blockers,
    blocker_disclosures, files, collected_at, stale_after
)
SELECT
    session_name, '' AS working_dir, status, goal, goal_disclosure, now_text,
    now_disclosure, updated_at, active_beads, agent_mail_threads, blockers,
    blocker_disclosures, files, collected_at, stale_after
FROM runtime_handoff;

DROP TABLE runtime_handoff;
ALTER TABLE runtime_handoff_v2 RENAME TO runtime_handoff;

CREATE UNIQUE INDEX IF NOT EXISTS idx_runtime_handoff_session_workdir
    ON runtime_handoff(session_name, working_dir);
