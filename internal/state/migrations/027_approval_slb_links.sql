-- External approval identity is independent of the orchestration correlation
-- key. Do not infer a project or an authorization from a caller-supplied key.
CREATE TABLE approval_slb_links (
    approval_id TEXT PRIMARY KEY NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
    request_id TEXT NOT NULL CHECK (length(trim(request_id)) > 0),
    project_dir TEXT NOT NULL CHECK (length(trim(project_dir)) > 0),
    UNIQUE (project_dir, request_id)
);
