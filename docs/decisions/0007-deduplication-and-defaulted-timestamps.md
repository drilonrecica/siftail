# ADR 0007: Deduplication with Defaulted Timestamps and Conflict Policy

**Status:** Accepted

**Date:** 2026-11-05

**Amends:** [ADR 0004](0004-canonical-events-sources-and-deduplication.md)
(deduplication comparison set and conflict outcome)

## Context

ADR 0004 makes a repeated stable source event ID with identical canonical
content an idempotent no-op. It rejects the entire request with `409` when the
ID is reused with different content. Two defects follow (audit DAT-11):

1. **Defaulted timestamps are compared.** When a record supplies no timestamp,
   normalization uses the request's receive time (`DOMAIN.md` §5.2). A retry
   arrives with a new receive time, so its defaulted `event_at_us` differs, and
   `CanonicalEqual` reports a conflict. The retry of a batch Siftail already
   committed is rejected with `409`, which is the opposite of idempotency.
2. **One conflict rejects the whole request.** A Fluent Bit chunk carries many
   records, often from many sources on one server. Fluent Bit's `http` output
   does not treat `409` as permanent: it schedules a retry for a non-success
   flush. The generated and pinned configurations set `Retry_Limit False` (no
   retry-count limit, `docs/integrations/coolify-fluent-bit-compatibility.md`).
   The same chunk, with the same conflicting record, is therefore re-sent
   indefinitely and can never succeed. Every valid event in it is withheld
   until Fluent Bit discards the chunk at its 256 MiB buffer bound, and newer
   chunks queue behind it. A single producer bug thus becomes silent loss of
   unrelated events. With a finite `Retry_Limit` the outcome is the same loss,
   only sooner.

Container labels also become mutable under ADR 0006. Comparing the container
name, as the comparison does today, would turn a retry after a rename into a
false conflict.

## Decision

1. **Record whether event time was defaulted.** The canonical event carries
   `event_at_defaulted`, true exactly when neither a source timestamp nor a
   Fluent Bit record timestamp was present and the receive time was used
   (`DOMAIN.md` §5.2 precedence step 3). It is persisted with the event.
2. **Canonical equality.** Equality compares everything ADR 0004 compares,
   with two refinements:
   - event time is compared only when neither the stored nor the incoming
     event defaulted it. A defaulted time is Siftail's observation, not
     producer content;
   - container identity is compared by container ID when both events carry
     one, and by container name only when neither carries an ID. One side with
     an ID and the other without is a difference.

   Receive time and the internal ID remain excluded.
3. **Conflict policy: drop and count, do not reject.** When a record reuses a
   stable source event ID that already names different canonical content, in
   the database or earlier in the same request, that record is not persisted.
   The rest of the request commits normally and the request succeeds (`204`).
   The first committed content for an ID is authoritative and is never
   replaced. Each dropped record:
   - increments a per-server conflicting-duplicate counter (cumulative since
     process start, with the time of the last occurrence) shown on the Status
     page;
   - is not published to Live;
   - triggers at most one diagnostic entry per server per minute. The entry
     uses a closed `ingest` category from the typed diagnostic table
     (`ARCHITECTURE.md` §27.5) and never carries the source event ID, message,
     attributes, or any other application field (`AGENTS.md` §16).
     Per-server and per-source attribution lives in the Status counter, not
     in the diagnostic.

   Identical repeats remain silent idempotent no-ops and are not counted as
   conflicts.
4. **`409` leaves the ingestion contract.** Ingestion no longer returns `409`.
   The `Conflict` domain category remains available to other operations.

## Alternatives

- **Keep whole-request `409`.** This is correct only for a producer that reads
  status codes and fixes itself. Fluent Bit, the primary producer, retries
  instead, so `409` turns one conflicting record into indefinite redelivery
  and eventual loss of every co-batched event, without acknowledging any of
  them.
- **Replace the stored event with the new content.** This violates event
  immutability (`DOMAIN.md` §6) and is the `INSERT OR REPLACE` behavior
  ADR 0004 prohibits.
- **Store the conflicting record as a new event without the ID.** This keeps
  the content but silently breaks the idempotency contract: a later retry of
  that record would add another copy each time.
- **Return a success status with a per-record error body.** Fluent Bit ignores
  response bodies, so the information would reach no one who can act on it.
  The counter and diagnostic reach the operator, who can.
- **Exclude event time from equality entirely.** This would let a producer
  reuse an ID for an event at a different supplied time without detection.
  Only a defaulted time is excluded.

## Consequences

- Retrying a batch without timestamps is an idempotent no-op.
- A producer that reuses IDs loses only the reused records, and the operator
  sees the counter and diagnostic instead of a blocked server.
- Request atomicity (`DOMAIN.md` §6 invariant 9) is unchanged: the request's
  outcome, including each drop decision, commits in one transaction or not at
  all. A dropped record is a deliberate, counted outcome of a committed
  request, not a partially applied batch.
- Producers relying on `409` to detect ID reuse must watch the Status counter
  instead. This is a documented ingestion-contract change with release notes.
- Tests cover: retry without timestamp (`204`, no duplicate), a true conflict
  in the database and within one request (other records commit, counter
  increments, no Live publication), a rename between attempts (no conflict),
  and migrated rows.

## Migration and compatibility impact

A forward migration adds `event_at_defaulted INTEGER NOT NULL DEFAULT 0 CHECK
(event_at_defaulted IN (0, 1))` to `log_events`. Adding a column with a
constant default does not rewrite the table. Existing rows carry no record of
how their time was chosen. The migration therefore marks a row as defaulted
only when it has a `source_event_id` (the only rows equality reads) and
`event_at_us = received_at_us`. Only a defaulted timestamp produces exact
microsecond equality in practice. An upgrade fixture is required.

The deduplication comparison set and the ingestion status contract are both
compatibility contracts under ADR 0004. This ADR is that required amendment.
ADR 0004's other decisions (scope of the ID, no hash deduplication, no
replacement) stand. Implemented by SFT-090.
