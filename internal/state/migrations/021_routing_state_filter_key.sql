-- 021_routing_state_filter_key.sql — key routing state by (session, filter set)
-- (bd-88um4). The rotation cursor is an index into the CURRENT invocation's
-- FILTERED candidate list; keying it only by session corrupts the anchor when
-- sends alternate between different --cc/--cod filters or --exclude sets (a
-- 2x2 cc/cod session can starve a pane indefinitely). Each distinct filter set
-- now rotates independently. Existing rows migrate to the empty filter key
-- (the unfiltered send path).
CREATE TABLE routing_state_v2 (
    session_name    TEXT NOT NULL,
    filter_key      TEXT NOT NULL DEFAULT '',
    last_agent      TEXT NOT NULL DEFAULT '',
    rotation_cursor INTEGER NOT NULL DEFAULT -1,
    updated_at      TIMESTAMP NOT NULL,
    PRIMARY KEY (session_name, filter_key)
);

INSERT INTO routing_state_v2 (session_name, filter_key, last_agent, rotation_cursor, updated_at)
    SELECT session_name, '', last_agent, rotation_cursor, updated_at FROM routing_state;

DROP TABLE routing_state;

ALTER TABLE routing_state_v2 RENAME TO routing_state;
