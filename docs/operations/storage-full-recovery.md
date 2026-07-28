# Storage-full degraded mode and recovery

**Reviewed:** 2026-07-28  
**Scope:** SFT-041 active-server storage failure and deterministic recovery

## Observable behavior

Siftail acknowledges an ingestion batch only after its complete SQLite
transaction commits. `SQLITE_FULL` from the main database, WAL, or SQLite
temporary storage is classified as storage full and returns `507 Insufficient
Storage`. Temporary busy or I/O unavailability returns `503 Service
Unavailable`. Responses, diagnostics, and process logs contain neither the raw
SQLite error nor the application payload.

The first failed commit rolls back the complete batch and marks ingestion
unready. Subsequent requests are still token-authenticated, but Siftail rejects
them before decoding or queueing their bodies:

- `/health/live` remains `200` so orchestration does not create a restart loop;
- `/health/ready` returns the minimal `503 not ready`;
- History, Status, diagnostics, and other read paths remain available where
  SQLite can still serve the bounded reader pool;
- Status shows the `storage_full` category and a critical recovery warning; and
- Fluent Bit remains responsible for its configured bounded retry buffer.

Siftail does not keep an in-memory overflow, create an internal disk spool,
delete or recreate the database, run automatic full `VACUUM`, or acknowledge
an uncommitted request.

## Safe operator procedure

1. Preserve the existing `/data` volume and database files. Do not delete
   `siftail.db`, `siftail.db-wal`, or `siftail.db-shm`.
2. Free capacity on the host filesystem or enlarge the volume/quota. Removing
   unrelated files outside Siftail is an operator decision.
3. If retained application logs are the pressure source, lower the global
   retention age or size limit before the filesystem is completely exhausted.
   The hourly bounded retention worker deletes only application events in
   canonical oldest-first chunks.
4. Watch authenticated Status or `/health/ready`. While degraded, Siftail
   performs one bounded recovery probe every five seconds.
5. Resume or inspect Fluent Bit delivery after readiness becomes healthy.
   At-least-once delivery can retry an ambiguous client disconnect; stable
   source event IDs retain their ordinary idempotency semantics.

The probe inserts a bounded 64 KiB value into the internal settings table and
deletes it in the same coordinator-owned transaction. A successful commit
leaves no probe row, records a sanitized recovery diagnostic, clears the
database degradation, and allows ingestion again. A failed probe changes
nothing and retries later. Restarting Siftail does not delete, replace, or
repair the database and is not itself evidence that capacity is available.

## If recovery does not occur

Confirm that the volume containing `/data` and SQLite temporary-file location
both have capacity and that the database remains writable by the Siftail
process. Run the bounded check:

```bash
./siftail database check
```

If the server must be stopped for investigation, use `database check --full`
only after it is stopped. Follow
[`database-checks.md`](database-checks.md) for the read-only/offline safety
boundary. Preserve the original database and WAL files before any external
repair attempt. Siftail never performs silent replacement or automatic
down-migration.

## Verified fault boundary

Repository tests constrain SQLite `max_page_count` in WAL mode to emulate a
full database quota, force temporary storage to its page limit, and persist an
oversized atomic batch. They assert `507` classification, rollback, concurrent
retained reads, bounded retention releasing pages, a failed-then-successful
probe transition, restart integrity, worker shutdown, and absence of a
persistent probe row. These deterministic SQLite quota tests complement, but
do not claim to reproduce every host filesystem or storage-controller failure.

Three one-second development-host benchmark runs measured the successful
durable probe at a 214.177 µs/op median (212.067–277.295 µs/op), 969–985
bytes/op, and 31 allocations/op. The probe is inactive while healthy and runs
at most once per five seconds while degraded. These local Fedora/i5
measurements are not guarantees for other databases, filesystems, storage
hardware, or sync latency.
