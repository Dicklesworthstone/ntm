-- 023_metrics_session_fk.sql — drop the FKs that made the metrics tables
-- permanently unwritable.
--
-- blocked_commands.session_id and metric_snapshots.session_id were declared
-- REFERENCES sessions(id), and Open() enables PRAGMA foreign_keys. But nothing
-- in the shipped binary ever inserts into `sessions`: state.Store.CreateSession
-- has no production caller, because the live session model is runtime_sessions,
-- not the 001/002 sessions/agents tables. With the FK target permanently empty,
-- every insert into these two tables was rejected:
--
--   * RecordBlockedCommand discarded the error, so the Tier-0
--     destructive_cmd_incidents metric counted zero incidents forever.
--   * `ntm metrics snapshot save` surfaced it as a raw
--     "constraint failed: FOREIGN KEY constraint failed (787)".
--
-- Verified on a live 3.2MB state.db in daily use: every table carrying an FK to
-- sessions/agents held 0 rows, while every table without one (runtime_sessions,
-- runtime_agents, attention_events, send_operations) was populated.
--
-- The columns keep their data but lose the constraint. session_id here is a
-- tmux session *name*, which was never a key into `sessions` to begin with, so
-- the reference was wrong as well as unsatisfiable. The other five tables that
-- reference sessions/agents (agents, tasks, reservations, event_log,
-- bead_history) are left alone: none of their writers has a production caller,
-- so no shipped path is blocked by them.

CREATE TABLE blocked_commands_rebuilt (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT,
    agent_id TEXT,
    command TEXT NOT NULL,
    reason TEXT NOT NULL,
    blocked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO blocked_commands_rebuilt (id, session_id, agent_id, command, reason, blocked_at)
    SELECT id, session_id, agent_id, command, reason, blocked_at FROM blocked_commands;

DROP TABLE blocked_commands;
ALTER TABLE blocked_commands_rebuilt RENAME TO blocked_commands;

CREATE INDEX IF NOT EXISTS idx_blocked_commands_session_id ON blocked_commands(session_id);
CREATE INDEX IF NOT EXISTS idx_blocked_commands_agent_id ON blocked_commands(agent_id);

CREATE TABLE metric_snapshots_rebuilt (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT,
    snapshot_name TEXT NOT NULL,
    snapshot_data TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO metric_snapshots_rebuilt (id, session_id, snapshot_name, snapshot_data, created_at)
    SELECT id, session_id, snapshot_name, snapshot_data, created_at FROM metric_snapshots;

DROP TABLE metric_snapshots;
ALTER TABLE metric_snapshots_rebuilt RENAME TO metric_snapshots;

CREATE INDEX IF NOT EXISTS idx_metric_snapshots_session_id ON metric_snapshots(session_id);
CREATE INDEX IF NOT EXISTS idx_metric_snapshots_snapshot_name ON metric_snapshots(snapshot_name);
CREATE INDEX IF NOT EXISTS idx_metric_snapshots_created_at ON metric_snapshots(created_at);
