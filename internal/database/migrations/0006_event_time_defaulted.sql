-- ADR 0007: record whether event time was defaulted to receive time, so that
-- stable source-event-ID deduplication can ignore a time that is Siftail's
-- observation rather than producer content. Adding a column with a constant
-- default does not rewrite the table.
ALTER TABLE log_events ADD COLUMN event_at_defaulted INTEGER NOT NULL DEFAULT 0
    CHECK (event_at_defaulted IN (0, 1));

-- Existing rows carry no record of how their time was chosen. Only rows with a
-- source_event_id are ever read by deduplication, and in practice only a
-- defaulted timestamp equals the receive time to the microsecond, so a row is
-- marked defaulted only when it has an ID and both times are equal.
--
-- Events are immutable after persistence (DOMAIN.md §6). This one-time,
-- schema-owned backfill of a new derived column is the only exception: the
-- immutability trigger is dropped and recreated unchanged inside this
-- migration's transaction, so no other writer can observe the gap.
DROP TRIGGER log_events_immutable_update;

UPDATE log_events SET event_at_defaulted = 1
WHERE source_event_id IS NOT NULL AND event_at_us = received_at_us;

CREATE TRIGGER log_events_immutable_update
BEFORE UPDATE ON log_events
BEGIN
    SELECT RAISE(ABORT, 'log events are immutable');
END;
