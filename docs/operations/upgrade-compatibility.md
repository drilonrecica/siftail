# Database upgrade compatibility

**Reviewed:** 2026-07-28
**Scope:** SFT-047 schema 1–4 fixture and recovery matrix

## Supported window

The current pre-public binary supports automatic forward migration from every
checked-in historical schema fixture, versions 1 through 4. It supports a
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
immutable security-audit event.

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

## Matrix guarantees

For each schema 1–4 fixture, tests:

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
