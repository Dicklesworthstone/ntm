-- Bind non-spoofable actor provenance (OS UID, tmux pane, SLB session, or authenticated HTTP principal)
-- alongside display actor names for SLB two-person approval enforcement.
ALTER TABLE approvals ADD COLUMN requested_provenance TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN approved_provenance TEXT NOT NULL DEFAULT '';
