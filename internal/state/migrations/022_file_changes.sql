-- 022_file_changes.sql — cross-process ledger of agent file changes.
--
-- `ntm changes`, `ntm conflicts`, --robot-status, the dashboard Files panel and
-- the work-coordination adapter all read recorded file changes, but the only
-- writer lived in a per-process in-memory ring (tracker.GlobalFileChanges).
-- The process that observes changes is the session monitor, and every one of
-- those readers is a *different* process, so the readers could only ever see an
-- empty ring and reported a clean all-clear regardless of what agents did.
--
-- Rows are project-scoped: state.db is shared by every ntm process on the
-- machine, so an unscoped query would report one repository's conflicts while
-- the user is standing in another.
CREATE TABLE IF NOT EXISTS file_changes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_dir TEXT NOT NULL,
    session     TEXT NOT NULL DEFAULT '',
    agent       TEXT NOT NULL DEFAULT '',
    path        TEXT NOT NULL,
    change_type TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL
);

-- Reads are always "recent changes in this project", so lead with project_dir
-- and carry created_at for the range scan.
CREATE INDEX IF NOT EXISTS idx_file_changes_project_created
    ON file_changes(project_dir, created_at);
