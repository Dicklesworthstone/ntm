ALTER TABLE incidents ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE incidents ADD COLUMN family TEXT NOT NULL DEFAULT '';
ALTER TABLE incidents ADD COLUMN category TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_incidents_fingerprint ON incidents(fingerprint);
CREATE INDEX IF NOT EXISTS idx_incidents_fingerprint_status ON incidents(fingerprint, status);
