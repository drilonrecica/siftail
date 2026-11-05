# Database upgrade compatibility

**Reviewed:** 2026-11-05
**Scope:** SFT-047 schema 1–4 fixture and recovery matrix; SFT-087 schema 5;
SFT-090 schema 6

## Supported window

The current pre-public binary supports automatic forward migration from every
checked-in historical schema fixture, versions 1 through 6. It supports a
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
6 adds a stable-ID event whose time was defaulted to receive time.

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

## Matrix guarantees

For each schema 1–6 fixture, tests:

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
8. update retention through the coordinated store with its audit event; and
9. prove a failing next migration leaves no partial table while already
   successful migration versions remain committed.

A separate eight-case matrix creates and verifies both full and
configuration-only backups after every historical upgrade, stops the database,
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
