-- ADR 0008: application-event retention is measured from receipt. Age
-- retention, size retention, and every bounded deletion walk
-- (received_at_us, id), so an accepted event is never deleted by the next age
-- run merely because its claimed event time is old.
CREATE INDEX log_events_received_idx ON log_events(received_at_us, id);

-- The generated column retention_at_us (min(event_at_us, received_at_us))
-- cannot be redefined without rewriting every row. It stays in the schema,
-- deprecated and unused; only its index is removed.
DROP INDEX log_events_retention_idx;
