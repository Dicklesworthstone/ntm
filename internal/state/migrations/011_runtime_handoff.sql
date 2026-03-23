-- NTM State Store: Runtime Handoff Projection
-- Version: 011
-- Description: Persists the latest normalized handoff summary and disclosure metadata
-- Bead: bd-j9jo3.3.5

CREATE TABLE IF NOT EXISTS runtime_handoff (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    session_name TEXT NOT NULL,
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
