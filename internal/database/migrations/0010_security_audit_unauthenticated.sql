-- Reserved security audit capacity (ADR 0010). Rows with actor
-- 'unauthenticated' have their own cap of 10,000 inside the 100,000-row total,
-- and an unauthenticated insert above it evicts only the oldest
-- unauthenticated row. Administrator, local-operator, and system rows therefore
-- keep at least 90,000 rows that no unauthenticated write can evict.
--
-- A one-row counter kept by triggers, like migration 0009's total, makes the
-- sub-cap check a primary-key read. It is seeded from the existing rows, so
-- per-attempt sign-in rejections written before this migration count toward
-- the sub-cap and age out normally. Storage cost is one row; each
-- unauthenticated insert or deletion also updates that row.
CREATE TABLE security_audit_unauthenticated_count (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    row_count INTEGER NOT NULL CHECK (row_count >= 0)
) STRICT;

INSERT INTO security_audit_unauthenticated_count(id, row_count)
SELECT 1, count(*) FROM security_audit_events
WHERE actor_type = 'unauthenticated';

CREATE TRIGGER security_audit_events_unauthenticated_count_insert
AFTER INSERT ON security_audit_events
WHEN NEW.actor_type = 'unauthenticated'
BEGIN
    UPDATE security_audit_unauthenticated_count
    SET row_count = row_count + 1 WHERE id = 1;
END;

CREATE TRIGGER security_audit_events_unauthenticated_count_delete
AFTER DELETE ON security_audit_events
WHEN OLD.actor_type = 'unauthenticated'
BEGIN
    UPDATE security_audit_unauthenticated_count
    SET row_count = row_count - 1 WHERE id = 1;
END;

-- Supports only the sub-cap eviction "oldest unauthenticated row first"
-- (ORDER BY occurred_at_us, id LIMIT n). Without it, finding the oldest
-- unauthenticated row walks the chronological index past up to 90,000
-- privileged rows. The partial index holds only unauthenticated rows, which
-- the sub-cap bounds at 10,000 entries (after legacy rows age out), so its
-- write and storage cost is one small entry per aggregated window. Retention
-- of application events never touches it; audit age cleanup deletes through
-- the chronological index and keeps this one in step. Rows are immutable, so
-- an actor never changes after insert.
CREATE INDEX security_audit_events_unauthenticated_idx
ON security_audit_events(occurred_at_us, id)
WHERE actor_type = 'unauthenticated';
