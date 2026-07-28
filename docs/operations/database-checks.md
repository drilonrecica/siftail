# Database checks and bounded diagnostics

**Reviewed:** 2026-07-28
**Scope:** SFT-040 production checks and process-local diagnostic history

## Safety boundary

`siftail database check` has two execution paths:

- with an active server, the owner-only Unix control socket runs
  `PRAGMA quick_check` through the bounded read pool and orders one passive WAL
  checkpoint through the maintenance coordinator;
- with a stopped server, the command opens the existing file with SQLite
  `mode=ro` and `query_only`, then runs either `quick_check` or, with `--full`,
  `integrity_check`.

The stopped path does not create a missing database, apply migrations, change
pragmas, checkpoint WAL, or run application writes. `--full` is refused while
the server is active because an unbounded-duration integrity scan should not
compete with ingestion. A server that appears during a stopped-server check
causes an explicit retry failure.

The fixed report contains only:

- quick/full mode;
- actual and supported schema versions plus compatibility;
- SQLite version and integrity result;
- journal, synchronous, foreign-key, and auto-vacuum state;
- writability and how it was determined;
- passive checkpoint state and frame counts;
- main database, WAL, and shared-memory byte counts; and
- page and free-page counts.

It never contains the database path, SQL text supplied by a user, table
contents, application messages, credentials, hashes, headers, or environment
values. The stopped-server writability result combines non-mutating file-open
access with file/directory permission bits. It is advisory and cannot promise
that a later durable SQLite commit will succeed.

Corruption, schema incompatibility, checkpoint contention, cancellation, and
unavailability return a nonzero exit with a safe category. A check never
deletes or recreates the target.

## Diagnostic history

The application keeps exactly the latest 100 operational diagnostics in a
process-local ring. Component/category pairs are closed internal enums;
severity and summary are selected from prewritten strings. Callers cannot
submit a message. An event may also contain a validated internal request ID
and recovery time.

The authenticated Status page and active-only `siftail diagnostics` command
read this same ring. The command uses the owner-only control socket and refuses
extra arguments. There is no arbitrary SQL command, persistent process-log
mirror, support-data bundle, public administration API, or outbound report.
Restarting Siftail intentionally clears the ring.

## Measured cost

The repository benchmark builds a migrated database with 100,000 bounded audit
rows, stops it cleanly, and opens it read-only for every iteration:

```bash
go test -run '^$' \
  -bench '^BenchmarkStoppedDatabaseCheck$' \
  -benchtime=1s -count=3 -benchmem ./internal/database
```

On the documented Fedora/i5 development host:

- quick check median: 19.068 ms/op, 9,273–9,348 bytes/op, 157 allocations/op;
- full integrity check median: 39.515 ms/op, 9,244–9,282 bytes/op, 157
  allocations/op.

These are local development-host measurements, not guarantees for other
filesystems, hardware, database sizes, corruption patterns, or concurrent
active workloads.
