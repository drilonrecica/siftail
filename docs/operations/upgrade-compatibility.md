# Database upgrade compatibility

**Reviewed:** 2026-11-05
**Scope:** SFT-047 schema 1–4 fixture and recovery matrix; SFT-087 schema 5;
SFT-090 schema 6; SFT-091 schema 7; SFT-095 schema 8; SFT-099 schema 9;
SFT-105 schema 10

## Supported window

The current pre-public binary supports automatic forward migration from every
checked-in historical schema fixture, versions 1 through 10. It supports a
fresh database through the same production open path. Before `1.0`, only the
latest stable Siftail release receives ordinary support, but that release must
continue to open every schema fixture retained in this repository.

The database schema is not a third-party API. The compatibility promise is
Siftail-to-Siftail forward upgrade: never direct application access,
down-migration, or arbitrary reconstruction of a database from SQL.

An older binary refuses a database whose maximum recorded migration is newer
than its embedded maximum. It reports actual and supported versions and does
not delete, recreate, migrate down, or otherwise alter representative data.

## Fixture provenance

Immutable SQLite snapshots live in
`internal/database/testdata/upgrades/`. Schema 1 contains representative
settings, trusted Server/token, source/container, and immutable application
event state. Schema 2 adds a deterministic synthetic Argon2id administrator;
schema 3 adds a hashed synthetic browser session; schema 4 adds a sanitized
immutable security-audit event; schema 5 adds a recreated container instance
(the same name with a new container ID), which schema 4 could not hold; schema
6 adds a stable-ID event whose time was defaulted to receive time; schema 7
adds a late event whose event time is 30 days before its receipt. Schema 8
changes only an index and carries the same rows as schema 7; event 15, present
in every fixture, has the request ID the rebuilt index must find. Schema 9
adds only the security-audit row counter and carries the same rows as schema
8, with a counter of 1. Schema 10 adds only the unauthenticated-row counter and
its partial index and carries the same rows as schema 9; its one audit row is
a system row, so the new counter is 0.

Every name, address, token, credential, timestamp, and payload is an obvious
synthetic test value. The generator contains no production data and makes no
network calls. Fixture and released-migration SHA-256 digests are pinned by
tests. The fixture README records exact hashes and the explicit generation
procedure.

Released migration files and existing fixtures are append-only. Adding a new
migration adds one new current-schema fixture; it does not regenerate older
snapshots. Correcting an existing fixture requires explicit overwrite,
reviewed provenance and digest changes, and a release note explaining why the
historical artifact changed.

No fixture is removed before `1.0` merely because its schema is old. A future
removal requires an authoritative minimum-upgrade policy, release notes,
continued support for every schema inside that window, and a documented
intermediate upgrade path where necessary.

## Schema 5: container identity

Migration `0005_container_identity.sql` (ADR 0006) rebuilds
`container_instances` without `UNIQUE (source_id, container_id)` and
`UNIQUE (source_id, container_name)`, keeping the source foreign key, the
column checks, and `UNIQUE (id, source_id)`, and adds two non-unique lookup
indexes. Every row is copied with its `id`, so every event keeps its container
reference. Because `log_events` references the rebuilt table, the migration
runs with foreign-key enforcement disabled outside its transaction (SQLite's
documented rebuild procedure) and commits only after `PRAGMA
foreign_key_check` reports no violation; enforcement is restored before the
connection is used again. The rebuild copies only the bounded container table,
but the foreign-key check reads every retained event once, so the first open
after upgrade grows with database size: about 0.3 seconds for 2,000,000
events (515 MB) on btrfs on a SATA SSD (`sqlite3` 3.50, measured 2026-11-05).

The migration is forward-only. A binary that supports only schema 4 refuses
the upgraded database with its actual and supported versions and changes
nothing. Downgrading requires restoring a backup taken before the upgrade;
there is no down-migration.

## Schema 6: defaulted event time

Migration `0006_event_time_defaulted.sql` (ADR 0007) adds
`log_events.event_at_defaulted INTEGER NOT NULL DEFAULT 0 CHECK
(event_at_defaulted IN (0, 1))`, which records that an event carried no
timestamp and its time is the receive time. Stable source-event-ID
deduplication ignores such a time, so a retried batch without timestamps is an
idempotent no-op instead of a conflict.

Existing rows carry no record of how their time was chosen. The migration marks
a row as defaulted only when it has a `source_event_id` and `event_at_us =
received_at_us`; no other row is read by deduplication. That one-time backfill
of a new derived column is the only write to stored events. It drops the event
immutability trigger and recreates it unchanged inside the same migration
transaction. The backfill walks the partial `source_event_id` index, so its
cost follows the number of stable-ID rows, not table size.

Adding a column to a STRICT table makes SQLite run `quick_check` over that
table, so the first open after the upgrade runs about one extra integrity pass
over `log_events`. That pass costs about as much as the `quick_check` every
open already runs. Measured with `sqlite3` 3.50 on btrfs on a SATA SSD on
2026-11-05, on 2,000,000 events (1.3 GB, 1% with a stable ID, 0.1%
backfilled): the whole migration took 5.8 seconds with a warm page cache,
compared with 5.5 seconds for the database `quick_check`. The host was under
heavy unrelated IO load at the time, and cold-cache runs took 61 to 197
seconds, so expect the first open after the upgrade to take noticeably longer
on large databases.

The migration is forward-only. A binary that supports only schema 5 refuses
the upgraded database with its actual and supported versions and changes
nothing. Downgrading requires restoring a backup taken before the upgrade.

## Schema 7: retention by receipt

Migration `0007_retention_by_receipt.sql` (ADR 0008) creates
`log_events_received_idx ON log_events (received_at_us, id)` and drops
`log_events_retention_idx`. Age retention, size retention, and every bounded
deletion now order by receive time, so an accepted event is kept for at least
the configured age after Siftail received it. The stored generated column
`retention_at_us` (the earlier of event and receive time) cannot be redefined
without rewriting every row. It stays in the schema, deprecated and unused,
and a later rebuild may remove it.

No stored event is written. Every row keeps its recorded receive time, so the
new basis applies to old and new rows alike. Rows whose event time preceded
their receive time are retained longer than before, and none becomes eligible
sooner. See [`retention.md`](retention.md) for the operator-facing behavior and
the late-event counter.

Building the index reads the whole event table once, inside the migration
transaction, on the first open after the upgrade. Measured on 2026-11-05 with
the release binary's SQLite 3.53.4 (`go-sqlite3` 1.14.52), btrfs on a SATA SSD
(Crucial BX500), on a synthetic schema-6 database of 4.11 GiB with 1,750,002
events (`TMPDIR` and the data directory on that disk):

| First open after upgrade | Wall clock | Peak RSS | Read from disk |
|---|---|---|---|
| Cold page cache | 6 min 6 s | 120 MiB | 9.4 GB |
| Warm page cache | 19 s | 120 MiB | 0.36 GB |
| Ordinary open, no migration (warm, for comparison) | 10 s | 15 MiB | 0.06 GB |

Each open also runs the startup `quick_check`. The host had about 9 GiB of
available memory, so it could not cache the whole file. Siftail runs with
`temp_store = MEMORY`, so the index sort uses no temporary files. Its working
set is the 105 MiB difference in peak RSS. The database file grew by 37 MB
for the new index. The dropped index's pages go to the freelist and are reused.
The WAL peaked at 42 MB and was checkpointed at the end of the open.

Expect the first start after upgrading a large database to take minutes when
its pages are not cached. The UI and ingestion listeners open only after the
migration commits. Until then the healthcheck cannot connect, and Fluent Bit
keeps its chunks and retries. With the image's healthcheck (10-second start period, three 30-second retries),
Docker may mark the container unhealthy during a long upgrade. Docker does not
restart an unhealthy container on its own. Do not restart it by hand either.
The migration runs in one transaction, so an interrupted upgrade rolls back
completely. Nothing is lost, but the next start begins the work again. Take
and verify a backup before upgrading
([`full-backups.md`](full-backups.md)).

The migration is forward-only. A binary that supports only schema 6 refuses
the upgraded database with its actual and supported versions and changes
nothing. Downgrading requires restoring a backup taken before the upgrade.

## Schema 8: request-ID index in History order

Migration `0008_request_time_index.sql` (SFT-095) replaces
`log_events_request_id_idx ON log_events (request_id)` with
`log_events_request_time_idx ON log_events (request_id, event_at_us DESC, id
DESC)`, still partial on `request_id IS NOT NULL`. A History request-ID filter
now reads the request's rows in page order and stops at the page limit;
before, SQLite collected every row for the request and sorted them in a
temporary B-tree first, which was unbounded when a producer reused one request
ID. Each indexed row grows by its 8-byte event time; events without a request
ID still cost nothing in the index. Retention deletion does not use it.

No stored event is written. Building the index reads the whole event table
once, inside the migration transaction, on the first open after the upgrade,
like schema 7; only rows with a request ID are sorted into it. Its duration on
a large database was not measured separately, so plan for the first start
after the upgrade to take as long as the schema 7 measurement above, and take
and verify a backup before upgrading.

The migration is forward-only. A binary that supports only schema 7 refuses
the upgraded database with its actual and supported versions and changes
nothing. Downgrading requires restoring a backup taken before the upgrade.

## Schema 9: security-audit row counter

Migration `0009_security_audit_count.sql` (SFT-099) adds the one-row `STRICT`
table `security_audit_event_count`, seeds it with the current number of
`security_audit_events` rows, and adds `AFTER INSERT` and `AFTER DELETE`
triggers on `security_audit_events` that keep it exact. The audit store reads
this counter to enforce its 100,000-row cap in every audit insert instead of
counting the table each time, which read about 100,000 index entries per
audit event at the cap. Every writer of the audit table, including age and cap
cleanup and the configuration-backup exclusion, goes through the triggers;
SQLite disables its truncate optimization for a table with triggers, so even
an unqualified `DELETE` is counted.

The migration reads the audit table once (at most 100,000 rows) and writes one
row; it does not touch application events, so the first start after the
upgrade is not measurably longer. Backup verification and restore validation
require the counter table for schema 9 artifacts.

The migration is forward-only. A binary that supports only schema 8 refuses
the upgraded database with its actual and supported versions and changes
nothing. Downgrading requires restoring a backup taken before the upgrade.

## Schema 10: reserved unauthenticated audit capacity

Migration `0010_security_audit_unauthenticated.sql` (SFT-105, ADR 0010) adds
the one-row `STRICT` table `security_audit_unauthenticated_count`, seeds it
with the number of existing `unauthenticated` audit rows, and adds `AFTER
INSERT` and `AFTER DELETE` triggers on `security_audit_events` that fire only
for that actor. It also adds the partial index
`security_audit_events_unauthenticated_idx` on `(occurred_at_us, id) WHERE
actor_type = 'unauthenticated'`, which lets the store evict the oldest
unauthenticated row without walking privileged history. The store keeps
unauthenticated rows at or below 10,000 so that no unauthenticated write can
evict an administrator, local-operator, or system row.

Per-attempt `sign_in` rejection rows from earlier versions stay as they are and
count toward the new sub-cap. If an upgraded database holds more than 10,000
of them, each later unauthenticated insert and each hourly cleanup pass
removes at most 1,000 of the oldest, so the excess converges without a long
transaction; privileged rows are never touched by that trimming.

The migration reads the audit table once (at most 100,000 rows) and writes one
counter row plus one index entry per unauthenticated row; it does not touch
application events. Backup verification and restore validation require the new
counter table for schema 10 artifacts. The migration is forward-only. A binary
that supports only schema 9 refuses the upgraded database with its actual and
supported versions and changes nothing.

## Matrix guarantees

For each schema 1–10 fixture, tests:

1. copy the immutable file and verify its pinned digest;
2. open it through production pragmas, compatibility checks, migrations,
   startup quick check, and writable check;
3. require the exact ordered migration history and current schema;
4. preserve representative settings, identity, event, administrator, session,
   and audit rows when those tables existed;
5. run full integrity and immutable-event checks;
6. authenticate the representative administrator where applicable;
7. commit a canonical event through the controlled ingestion writer and read
   it through the History store;
8. update retention through the coordinated store with its audit event, and
   require the audit row counter to equal the audit row count; and
9. prove a failing next migration leaves no partial table while already
   successful migration versions remain committed.

A separate per-schema matrix (one case per fixture, nine today) creates and
verifies both full and configuration-only backups after every historical
upgrade, stops the database,
restores each artifact, verifies the managed rollback, requires session and
artifact-metadata removal, checks full-versus-configuration history semantics,
and commits/reads a new event after restore.

## Verification

Run the focused compatibility matrix:

```bash
go test -count=1 ./internal/database ./internal/backup
```

Release verification also runs:

```bash
go fmt ./...
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
```

These fixtures are small correctness evidence. They are not realistic
performance, disk-capacity, production-secret, corruption, or soak fixtures.
