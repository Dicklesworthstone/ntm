-- Persist machine-readable Graveyard §0.19 EvidenceLedger records alongside
-- compact audit decisions so automated policy/control decisions (early_stop,
-- assign --relax, etc.) carry structured expected-loss, regret, calibration,
-- and fallback provenance queryable via GetDecisionHistory.
ALTER TABLE audit_decision_log ADD COLUMN evidence_json TEXT;
