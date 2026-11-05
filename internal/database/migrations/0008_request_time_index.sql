-- History request-ID filtering reads rows in (event_at_us DESC, id DESC)
-- order and stops at the page limit. The single-column request_id index
-- returned every row for the request in rowid order, so SQLite sorted them all
-- in a temporary B-tree before applying LIMIT, which is unbounded when a
-- producer reuses one request ID. Carrying the History order in the index
-- makes the lookup an ordered range. The index stays partial, so events
-- without a request ID still cost nothing; each indexed row grows by the
-- 8-byte event time. Retention deletion does not use it.
DROP INDEX log_events_request_id_idx;

CREATE INDEX log_events_request_time_idx
ON log_events(request_id, event_at_us DESC, id DESC)
WHERE request_id IS NOT NULL;
