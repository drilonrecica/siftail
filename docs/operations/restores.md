# Stopped-server restore and rollback recovery

**Reviewed:** 2026-11-05
**Scope:** SFT-044 format-1 full/configuration restore; SFT-094 restore
without a healthy current database (ADR 0009)

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
unverified/corrupt/incompatible artifact, unsafe existing rollback,
insufficient space, or existing `/data/restore-staging`. Without
`--replace-unhealthy` it also refuses a missing or unhealthy current database,
with a message naming the flag; see the next section.

## Three current-database cases

Before changing anything, restore classifies the stopped current
`/data/siftail.db` and prints the result as `current database:`.

| Current database | Without `--replace-unhealthy` | With `--replace-unhealthy` |
| --- | --- | --- |
| **healthy**: compatible, at this binary's schema, integrity `ok` | Ordinary restore with a verified managed rollback | Identical; the flag has no effect |
| **missing**: no main file (lost or fresh volume) | Refused | Restored without a rollback copy |
| **unhealthy**: unreadable, not SQLite, integrity failure, incompatible, or a schema older or newer than this binary's | Refused | Files preserved unmodified, then restored without a rollback copy |

### Healthy current database

This is the ordinary path. Run the command above; the output includes
`current database: healthy` and `rollback: siftail.db.rollback`, and the
[safety sequence](#safety-sequence) below applies in full.

### Missing current database (lost volume)

When the volume was lost and `/data` is empty, or the database file is gone,
nothing can become a verified rollback. Mount the replacement volume at
`/data` (the directory must exist and be writable by the Siftail user), keep
the server stopped, and confirm the loss explicitly:

```bash
./siftail restore --confirm RESTORE --replace-unhealthy \
  /backups/siftail-full.sqlite
```

The output reports `current database: missing` and
`rollback: none (no healthy current database to capture)`. If a stray `-wal`,
`-shm`, or `-journal` sidecar remains without its main file, it is preserved
the same way as an unhealthy database (below) so it can never be applied to
the restored file, and the output names the preservation directory.

### Unhealthy current database (corruption, failed check, downgrade)

When `siftail database check --full` reports corruption, the file is not a
SQLite database, or a newer binary advanced the schema and you are returning to
an older one (`docker-compose.md`, upgrade rollback), restore with the same
flag:

```bash
./siftail restore --confirm RESTORE --replace-unhealthy \
  /backups/siftail-pre-upgrade.sqlite
```

Restore moves the main file and any `siftail.db-wal`, `siftail.db-shm`, and
`siftail.db-journal` sidecars, unmodified and by same-directory rename, into a
new owner-private (mode `0700`) directory such as
`/data/siftail.db.unhealthy-20261105T101500.123456Z/`, keeping their names so
the files still belong together. Renaming needs no additional space, so the
space preflight counts only the input artifact and slack. The output reports
`current database: unhealthy`, `rollback: none (...)`, and
`preserved: siftail.db.unhealthy-...`.

The preserved files are not a backup and are never verified, opened for
writing, or used by Siftail again. Siftail never deletes them. Copy them to
protected storage if you need them for forensics or manual salvage, then remove
the directory yourself; it contains the same sensitive data as the database.

### What the flag never changes

`--replace-unhealthy` never bypasses the maintenance lock, the `RESTORE`
confirmation, input verification, the staging rules, the space preflight,
post-install validation, or the `restore.apply` audit record. A current path
that exists but is not a regular file (for example a symbolic link or a
directory) is refused in every mode. The managed `/data/siftail.db.rollback`
is replaced only by a verified artifact, so when no rollback copy could be
made, any preceding managed rollback is left untouched.

## Safety sequence

The server and command contend on `/data/.siftail-maintenance.lock`; both cannot
own the database simultaneously. With the lock held, restore:

1. verifies the input without applying it;
2. runs a full read-only check on the current database and classifies it;
3. copies and re-verifies the input in owner-private staging;
4. for a healthy database, snapshots it with `VACUUM INTO` (the full-backup
   snapshot path, ADR 0009), including committed WAL state, into a verified
   session-free full rollback artifact; for a missing or unhealthy database
   with `--replace-unhealthy`, moves any existing files into the preservation
   directory instead;
5. for a healthy database, checkpoints and closes it before removing only its
   stale sidecars;
6. atomically installs the staged artifact;
7. opens it with production pragmas and supported forward migrations;
8. deletes all sessions and artifact-only metadata in the same transaction as
   a safe `restore.apply` local-operator audit;
9. runs full integrity, schema, required-query, permissions, checkpoint, and
   closed-file checks; and
10. only then, for a healthy database, atomically replaces the preceding
    `siftail.db.rollback`.

The input artifact is never changed. Output names are safe basenames; failures
do not print paths, SQL/SQLite details, hashes, password/token material, or log
payloads.

## Failure and interruption

Before replacement, failure removes only files created by the current attempt.
After replacement, an ordinary error or cancellation reinstalls the verified
pre-restore snapshot, removes sessions/artifact metadata, records a failed or
canceled restore audit, validates the recovered database, and returns failure.

With `--replace-unhealthy` there is no verified pre-restore state to reinstall.
A failure after replacement removes the installed copy (the input artifact is
unchanged), leaves no database at `/data/siftail.db`, keeps the preservation
directory intact, and reports `restore failed; no database was installed`
together with the preservation directory's name. Fix the cause and run the
same command again; it now takes the missing-database path. Do not start the
server in between, because it would create a new empty database.

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
