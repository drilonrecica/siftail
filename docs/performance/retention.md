# Retention concurrency and reclamation evidence

**Measured:** 2026-07-28
**Host:** Fedora Linux, Intel Core i5-7500 at 3.40 GHz, linux/amd64
**Scope:** SFT-033 bounded application-event retention

## Production limits and ordering

The lifecycle-owned worker runs once at startup and then once per hour. It
loads the atomic retention policy and deletes at most 10,000 events per
coordinator-owned transaction in:

```text
retention_at_us ASC, id ASC
```

Age deletion uses a fixed cutoff for the run. Size deletion begins at 95% of
the configured active footprint and remeasures after every delete/reclaim
cycle until the footprint is at or below 90%, no events remain, cancellation
occurs, or a checkpoint is busy.

Reclamation first attempts a passive checkpoint, incrementally vacuums at most
8,192 pages, and then uses a controlled truncate checkpoint to release the
physical WAL allocation. All three operations run as one bounded maintenance
command in the same 64-entry coordinator queue as ingestion and administrative
mutations. A busy checkpoint stops size deletion for that run so a long-lived
reader cannot cause speculative over-deletion. Routine full `VACUUM` is never
used.

Every committed nonempty chunk publishes one nonblocking global
`retention_purged` control after the coordinator returns success. A rollback
publishes nothing.

## Repeated benchmark fixture

Command:

```bash
go test -run '^$' -bench '^BenchmarkRetention' \
  -benchtime=1x -count=5 -benchmem ./internal/retention
```

The chunk fixture creates 10,000 events with a 1 KiB raw and text payload,
then times canonical deletion, incremental reclamation, and checkpoints.
Across five independent databases:

- median complete cleanup: 205.720 ms;
- range: 202.357–209.449 ms;
- effective payload rate: about 49.8 MiB/s at the median;
- main database: 40.17 MiB before, 8.133 MiB after;
- WAL: 40.53 MiB before, 0 MiB after;
- free pages: 0 before deletion and 2,048 after the 8,192-page reclamation cap;
- median measured allocation: about 597 KiB and 32,932–32,934 allocations.

The interference fixture starts cleanup of 20,000 256-byte events in
1,000-event transactions while measuring 100 one-event commits through the
same coordinator. Five samples measured:

- no-cleanup writer p95 median: 113 µs;
- concurrent-cleanup writer p95 median: 3.120 ms;
- concurrent run median: 126.677 ms.

The relative p95 increase is expected from deliberate serialization, while the
absolute result remains far below the normal 250 ms commit-latency target on
this host. Production ingestion batches have different allocation and fsync
shapes, so this is a maintenance-interference comparison rather than a
cross-host throughput guarantee.

## Correctness and resource coverage

Real-SQLite tests cover delayed old events, future-skewed events, equal
retention timestamps with ID tie-breaking, bounded multi-chunk deletion,
post-commit notification, trigger-forced rollback, cancellation after a
committed chunk, no-event exhaustion, a simulated 95%/90% size threshold over
real event deletion, active DB/WAL/SHM measurement, busy-reader checkpoint
behavior, worker restart/shutdown, and concurrent coordinator writes plus
read-pool queries. Targeted race tests repeat the concurrent paths.

The size-threshold unit test injects footprint readings because the accepted
minimum operator target is 1 GiB; ordinary tests do not create a disposable
gigabyte database. The benchmark separately proves physical main/WAL
reclamation with production SQLite pragmas. Filesystem allocation, sparse-file
behavior, storage latency, and concurrent external snapshots vary by host.
