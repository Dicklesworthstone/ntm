-- Snapshot the resolved settings that governed an ensemble's creation.
-- NULL identifies an older run with no snapshot; '{}' remains a deliberate
-- configuration containing zero values and must not inherit new defaults.
ALTER TABLE ensemble_sessions ADD COLUMN budget_json TEXT;
ALTER TABLE ensemble_sessions ADD COLUMN synthesis_config_json TEXT;
