# Stopped-server restore and rollback recovery

**Reviewed:** 2026-07-28
**Scope:** SFT-044 format-1 full/configuration restore

## Before restoring

Restore is destructive and CLI-only. It replaces `/data/siftail.db`; it never
merges records. A configuration-only artifact therefore produces configured
Servers, credentials, settings, and sources with empty log, container, and
pre-restore audit history.

Protect the input artifact and ensure the `/data` filesystem has free space for
the artifact plus the current logical database and WAL, plus 5% or at least
1 MiB of slack. Stop the Siftail process and confirm its owner-only control
socket is gone. Then run:

```bash
./siftail restore --confirm RESTORE /backups/siftail-full.sqlite
```

The exact case-sensitive confirmation prevents accidental invocation. Restore
refuses an active server, unsafe/current/managed-rollback input path,
unverified/corrupt/incompatible artifact, incompatible current rollback source,
unsafe existing rollback, insufficient space, or existing
`/data/restore-staging`.

## Safety sequence

The server and command contend on `/data/.siftail-maintenance.lock`; both cannot
own the database simultaneously. With the lock held, restore:

1. verifies the input without applying it;
2. runs a full read-only check on the current database;
3. copies and re-verifies the input in owner-private staging;
4. snapshots the current database through SQLite's online backup API, including
   committed WAL state, into a verified session-free full rollback artifact;
5. checkpoints and closes the old database before removing only its stale
   sidecars;
6. atomically installs the staged artifact;
7. opens it with production pragmas and supported forward migrations;
8. deletes all sessions and artifact-only metadata in the same transaction as
   a safe `restore.apply` local-operator audit;
9. runs full integrity, schema, required-query, permissions, checkpoint, and
   closed-file checks; and
10. only then atomically replaces the preceding `siftail.db.rollback`.

The input artifact is never changed. Output names are safe basenames; failures
do not print paths, SQL/SQLite details, hashes, password/token material, or log
payloads.

## Failure and interruption

Before replacement, failure removes only files created by the current attempt.
After replacement, an ordinary error or cancellation reinstalls the verified
pre-restore snapshot, removes sessions/artifact metadata, records a failed or
canceled restore audit, validates the recovered database, and returns failure.

A process or host crash may leave `/data/restore-staging`. Siftail preserves it
instead of recursively deleting possible recovery state; both server startup
and the next restore refuse to proceed. Depending on the exact boundary, the active main database
is either the fully copied input or the checkpointed old database, while the
verified pre-restore candidate remains in staging. Inspect and protect these
files before manual recovery; do not delete the directory blindly.

## Recover the managed rollback

`/data/siftail.db.rollback` is a verified format-1 full artifact representing
the database immediately before the last successful restore, including
committed WAL state but excluding browser sessions. The managed path is refused
as direct input so the only rollback cannot be consumed by a failed attempt.
First copy it with owner-private permissions to separate protected storage:

```bash
cp --preserve=mode /data/siftail.db.rollback \
  /backups/siftail-rollback-recovery.sqlite
chmod 0600 /backups/siftail-rollback-recovery.sqlite
./siftail backup verify /backups/siftail-rollback-recovery.sqlite
./siftail restore --confirm RESTORE \
  /backups/siftail-rollback-recovery.sqlite
```

Recovery is another full restore and creates a new managed rollback of the
state it replaces. Every restore rolls password and ingestion-token state back
to the selected point; rotate credentials when this matters. Sessions never
survive, so sign in again after restart.
