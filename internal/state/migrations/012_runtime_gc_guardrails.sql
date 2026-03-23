ALTER TABLE runtime_quota ADD COLUMN used_pct_source TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE runtime_quota ADD COLUMN used_pct_known INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_runtime_quota_used_source ON runtime_quota(used_pct_source);
