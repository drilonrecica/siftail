-- The security audit store enforces its 100,000-row cap in every insert
-- transaction. Counting the table each time read the whole time index, about
-- 100,000 entries per audit event at the cap. A one-row counter kept by
-- triggers gives the exact size in one primary-key read under every writer:
-- audit inserts, cap and age cleanup, and the configuration-backup exclusion.
-- SQLite disables its truncate optimization for a table with triggers, so even
-- an unqualified DELETE keeps the counter exact. Storage cost is one row; each
-- audit insert or deletion also updates that row. Application-event retention
-- never touches either table.
CREATE TABLE security_audit_event_count (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    row_count INTEGER NOT NULL CHECK (row_count >= 0)
) STRICT;

INSERT INTO security_audit_event_count(id, row_count)
SELECT 1, count(*) FROM security_audit_events;

CREATE TRIGGER security_audit_events_count_insert
AFTER INSERT ON security_audit_events
BEGIN
    UPDATE security_audit_event_count SET row_count = row_count + 1 WHERE id = 1;
END;

CREATE TRIGGER security_audit_events_count_delete
AFTER DELETE ON security_audit_events
BEGIN
    UPDATE security_audit_event_count SET row_count = row_count - 1 WHERE id = 1;
END;
