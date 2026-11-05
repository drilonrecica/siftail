# ADR 0009: Group Commit under FULL, Snapshot Backups, and Unhealthy-Database Restore

**Status:** Accepted

**Date:** 2026-11-05

**Amends:** [ADR 0001](0001-sqlite-storage-and-driver.md) (backup mechanism;
write-transaction shape)

## Context

**Throughput.** ADR 0001 fixes `synchronous=FULL` and one write coordinator.
The writer commits one HTTP batch per transaction, so each acknowledged batch
costs at least one fsync. The recorded 190 µs commit and 8.7k events/s came
from tmpfs, where fsync costs almost nothing. On a real disk, throughput is
roughly one batch per fsync latency, whatever the batch size (audit
performance observations). `ARCHITECTURE.md` §14.3 already allows grouping
complete batches into one transaction, provided one invalid request never
rolls back unrelated ones.

**Backups.** Full backups use the SQLite online backup API in 256-page steps.
When another connection writes to the source between steps, the backup
restarts from the beginning. Under steady ingestion a multi-GB backup never
finishes (DAT-04). The existing concurrency test performs one write and
cannot detect this.

**Restore.** Restore requires an existing current database that is
compatible, at the current schema, and passes integrity checks, so that a
verified rollback copy can be taken. That excludes the common disaster cases:
a lost volume, a corrupt file, or an upgrade that must be undone (DAT-15).
`ARCHITECTURE.md` §25.6 even tells the operator to "restore pre-upgrade
backup" in a state where restore refuses.

## Decision

### Group commit

1. Keep `synchronous=FULL`, WAL, and the single coordinator.
2. The writer takes the next queued batch, then drains further already-queued
   batches into the same transaction, up to the group bounds:
   - a maximum number of batches;
   - a maximum approximate retained-byte total;
   - a maximum wait for more batches, which defaults to zero.

   A batch larger than the byte bound forms a group of one. Defaults are set
   and recorded by SFT-098 from real-disk benchmarks; all three are
   configurable within documented limits.
3. The group runs in one `BEGIN IMMEDIATE` transaction. Each batch runs inside
   its own `SAVEPOINT`:
   - **Batch-scoped failure** (a validation, quota, constraint, or internal
     error attributable to that batch; SFT-089 categories): `ROLLBACK TO` and
     `RELEASE` that savepoint. The batch receives its own outcome, and the
     remaining batches continue.
   - **Transaction-level failure** (`Busy`, `IO`, `Full`, or any error after
     which SQLite may have rolled back the transaction): roll back the whole
     group. Every batch in it receives that classified retryable outcome
     (`503` or `507`) and none was acknowledged.
4. **Acknowledgement waits for the single commit.** No batch's success is
   delivered before `COMMIT` returns. A commit failure fails every batch in
   the group with the classified outcome.
5. **Visibility follows commit.** Live publication, source/container cache
   fills, and queue-accounting release happen after commit, for every
   successful batch, in ID order. A source or container row created inside a
   savepoint that was rolled back is never reused by a later batch in the
   group or cached.
6. Quotas, deduplication, and conflict decisions (ADR 0007) see earlier
   batches of the same group, exactly as if they had committed separately in
   queue order.
7. Maintenance and administrative mutations run between groups, never inside
   one. Shutdown drains queued batches through the same grouping.

### Snapshot backups via `VACUUM INTO`

8. Full backups copy the database with `VACUUM INTO` the exclusively created,
   empty, mode-0600 hidden staging file, run on a dedicated read connection.
   `VACUUM INTO` reads from one read transaction, so it produces a single
   consistent snapshot. Concurrent commits never restart it. The rest of the
   full-backup contract is unchanged: session purge under `secure_delete`, the
   format-1 completed metadata record, close-and-sync (SQLite does not fsync
   `VACUUM INTO` output), verification, checksum, hard-link publication, and
   final re-verification.
9. Cancellation interrupts the statement and removes the staging file.
   Progress is reported as staged bytes written against the source logical
   size, because `VACUUM INTO` has no step callback.
10. The restore command's pre-restore capture of the stopped current database
    uses the same mechanism, so Siftail has one snapshot path.

### Restore without a healthy current database

11. `siftail restore` accepts an explicit `--replace-unhealthy` flag. Without
    it, restore behaves as today and refuses an unusable current database,
    with a message naming the flag.
12. With the flag, the current database is classified before anything is
    changed:
    - **healthy** (compatible, current schema, integrity `ok`): the flag has
      no effect, and the ordinary verified-rollback path runs;
    - **missing** (no main file): restore proceeds without a rollback copy and
      says so;
    - **unhealthy** (unreadable, not SQLite, integrity failure, incompatible,
      or a schema newer or older than this binary's current): the main file
      and any `-wal`, `-shm`, and `-journal` sidecars are moved by
      same-directory rename into a new owner-private
      `<database>.unhealthy-<UTC timestamp>/` directory. Renaming needs no
      extra space, and this binary deletes nothing it cannot verify. Restore
      then proceeds without a rollback copy and prints where the preserved
      files are. Stray sidecars next to a missing main file are preserved the
      same way, so they can never be applied to the restored file.
13. The flag never bypasses the maintenance lock, the exact `RESTORE`
    confirmation, input verification, staging rules, space preflight,
    post-install validation, or the restore audit record. The managed
    `<database>.rollback` is replaced only by a verified artifact, so it is
    left untouched when no rollback copy could be made.

## Alternatives

- **`synchronous=NORMAL`.** This gains throughput by weakening the durability
  boundary: an acknowledged batch could be lost on power failure. ADR 0001
  rejects that.
- **Merge batches into one transaction without savepoints.** One invalid batch
  would roll back unrelated valid ones, which `ARCHITECTURE.md` §14.3
  prohibits.
- **Acknowledge before commit, or add a disk spool.** These violate
  commit-before-acknowledgement (ADR 0002) or the no-spool rule.
- **Online backup with `Step(-1)` in one read transaction.** This also gives a
  single snapshot, but it needs a hand-managed source read transaction and
  reports progress only through the same callback. `VACUUM INTO` is one
  statement, omits free pages so artifacts are smaller, and is directly
  testable.
- **Raise the step size or pause ingestion during backup.** A larger step only
  narrows the restart window. Pausing makes backups an ingestion outage.
- **Copy an unhealthy database as an unverified rollback.** This would break
  the rule that the managed rollback is always a verified full artifact.
  Preserving the raw files in a separate, clearly named directory keeps them
  without pretending they are a backup.
- **Delete the unhealthy database automatically.** This is prohibited by
  `AGENTS.md` §4.5 ("never recreate or delete the database silently").

## Consequences

- Real-disk throughput scales with group size, while each acknowledgement
  still means "my transaction committed under FULL".
- Per-batch latency may include the commit of a larger group, bounded by the
  group bounds.
- A backup's read transaction holds back WAL checkpoints for its duration, so
  WAL grows during long backups. Backup space preflight includes WAL headroom.
  Size retention already stops on a busy checkpoint instead of deleting rows
  (`ARCHITECTURE.md` §23.5).
- Backup artifacts no longer contain free pages. The artifact format is
  unchanged and verify and restore accept artifacts made either way.
- Disaster recovery (lost volume, corrupt file, rollback of an upgrade) works
  with the tool, and the operator chooses it explicitly.
- Tests cover: grouped batches with one failing batch (others commit, own
  outcomes), transaction-level failure (all retryable), Live publication after
  commit only, continuous ingestion during a multi-hundred-MB backup (completes
  within a bound and verifies), and restore over missing, corrupt, and healthy
  current databases.

## Migration and compatibility impact

There is no schema migration. Ingestion status codes and acknowledgement
semantics are unchanged (ADR 0002). Backup artifacts remain format 1. The
`--replace-unhealthy` flag is additive CLI surface. Group bounds are new
configuration with documented defaults. ADR 0001's choice of driver, WAL,
`synchronous=FULL`, incremental auto-vacuum, and one coordinator stands. Its
"SQLite's online backup API" becomes `VACUUM INTO`, which is still a
SQLite-provided consistent copy and never a copy of the live main file.
Implemented by SFT-093 (backups), SFT-094 (restore), and SFT-098 (group
commit).
