-- Preserve unavailable context separately from a measured empty window.
-- Existing projection rows have no measurement and therefore remain NULL.
ALTER TABLE runtime_agents ADD COLUMN context_percent REAL;
