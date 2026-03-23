CREATE TABLE IF NOT EXISTS attention_item_states (
    item_key TEXT PRIMARY KEY,
    dedup_key TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'new',
    fingerprint TEXT NOT NULL DEFAULT '',
    acknowledged_at TIMESTAMP,
    acknowledged_by TEXT,
    snoozed_until TIMESTAMP,
    dismissed_at TIMESTAMP,
    dismissed_by TEXT,
    pinned INTEGER NOT NULL DEFAULT 0,
    pinned_at TIMESTAMP,
    pinned_by TEXT,
    muted INTEGER NOT NULL DEFAULT 0,
    muted_at TIMESTAMP,
    muted_by TEXT,
    override_priority TEXT,
    override_reason TEXT,
    override_expires_at TIMESTAMP,
    resurfacing_policy TEXT NOT NULL DEFAULT 'on_change',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_attention_item_states_dedup ON attention_item_states(dedup_key);
CREATE INDEX IF NOT EXISTS idx_attention_item_states_state ON attention_item_states(state);
CREATE INDEX IF NOT EXISTS idx_attention_item_states_snoozed ON attention_item_states(snoozed_until);
CREATE INDEX IF NOT EXISTS idx_attention_item_states_pinned ON attention_item_states(pinned);
CREATE INDEX IF NOT EXISTS idx_attention_item_states_muted ON attention_item_states(muted);
