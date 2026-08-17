-- 019_routing_state.sql — persisted per-session routing state (B7,
-- bd-ws1-truth-safety-l5ddi.10). Makes sticky and round-robin routing REAL
-- across sequential CLI invocations: each send records the pane it routed to
-- (last_agent) and the rotation cursor it advanced to (rotation_cursor).
CREATE TABLE IF NOT EXISTS routing_state (
    session_name    TEXT PRIMARY KEY,
    last_agent      TEXT NOT NULL DEFAULT '',
    rotation_cursor INTEGER NOT NULL DEFAULT -1,
    updated_at      TIMESTAMP NOT NULL
);
