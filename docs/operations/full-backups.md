# Verified full and configuration-only backups

**Reviewed:** 2026-11-05  
**Scope:** SFT-042–SFT-043 format-1 backup creation and verification; SFT-093
`VACUUM INTO` snapshot (ADR 0009); SFT-108 browser backup directory (audit
SEC-11)

## Create

Mount or create a protected destination directory that is separate from the
active `/data` database files, is persistent across container replacement, and
supports ordinary same-directory hard links, then run:

```bash
./siftail backup --output /backups/siftail-full.sqlite
```

For configuration without retained history:

```bash
./siftail backup --configuration-only \
  --output /backups/siftail-configuration.sqlite
```

The server must be active because the CLI uses the owner-only Unix control
socket. The CLI accepts any server-side path. Only one backup runs at a time.

### Browser backups and `SIFTAIL_BACKUP_DIR`

The authenticated Backup workspace provides the same operations, confined to
one directory: `SIFTAIL_BACKUP_DIR`, default `<SIFTAIL_DATA_DIR>/backups`
(`/data/backups` in the container). It must not be the data directory itself.
The browser submits only a plain file name of 1–128 ASCII letters, digits,
`.`, `_`, and `-`, starting with a letter or digit; absolute paths,
separators, `.`/`..`, hidden names, and anything else are refused before a
job starts and are never echoed back. An empty name creates
`siftail-full-<UTC time>.sqlite` or `siftail-configuration-<UTC time>.sqlite`.
Browser verification reads only a file of that name in the same directory.
Siftail creates a missing backup directory with mode `0700` on the first
browser backup; the directory may itself be a symbolic link the operator
chose, but a symbolic link at an artifact name is refused for both creation
and verification, so a link planted in the directory cannot redirect a write
or read outside it.

This bounds what a stolen browser session can do (audit SEC-11): it cannot
write a full database copy to an arbitrary writable path or probe arbitrary
files for existence. The default directory is on the data volume, so copy
artifacts off the volume and off-host; or point `SIFTAIL_BACKUP_DIR` at a
separate mounted volume. Use the CLI for any other destination. The CLI polls typed status until completion; the
browser replaces only its Backup region once per second. Neither path streams
database bytes through HTTP.

For full backup, the output directory must have capacity for the current logical
SQLite page count plus 5% or at least 1 MiB of slack. Configuration backup
reserves at least 2 MiB before starting; a larger configuration still fails
safely if the filesystem fills. Both types also reserve WAL headroom of 5% of
the database size (at least 1 MiB) on the filesystem that holds the database:
the snapshot's read transaction holds back WAL checkpoints, so the WAL grows
while ingestion continues during the backup. When the destination shares that
filesystem, the headroom is added to the destination requirement. Siftail
refuses:

- the live main database, WAL, or shared-memory path;
- a missing/non-directory parent;
- an existing file or symbolic link at the final path; and
- a destination it cannot inspect, create, synchronize, or publish safely.

Siftail creates a random hidden staging file beside the requested output with
mode `0600`. It never overwrites the final path, including if another process
creates that path between validation and publication.

## Snapshot and verification contract

Siftail runs `VACUUM INTO` the empty staging file on a dedicated source
connection (ADR 0009). The statement reads one read transaction, so it copies
one consistent snapshot: a concurrent committed request is wholly included or
wholly absent, never partly copied, and commits after the snapshot began are not
in the artifact. Concurrent commits never restart the copy, so a full backup
completes under steady ingestion. Ordinary ingestion remains on the single
writer coordinator. The dedicated connection is opened read-write without
create because SQLite opens the `VACUUM INTO` output with the source
connection's flags; the statement only reads the source database.

`VACUUM INTO` has no step callback. Progress is reported as staged bytes
written against the source's logical size (in-use pages × page size), sampled
while the statement runs. Cancellation interrupts the statement and removes
the staging file.

After copying, Siftail modifies only the staged destination:

1. use a self-contained delete journal with `synchronous=FULL`;
2. enable SQLite secure deletion and delete every active or historical browser
   session row;
3. write one format-1 `full` metadata record containing creation time, source
   schema version, and completed state;
4. close and synchronize the artifact (SQLite does not fsync `VACUUM INTO`
   output);
5. run full `integrity_check`;
6. require the supported schema version and every full-backup table;
7. require exactly one completed metadata record and zero session rows; and
8. stream SHA-256 calculation without loading the artifact into memory.

Only a verified inode is hard-linked atomically to the requested non-existing
filename. The staging link is removed and the parent directory is synchronized.
The final path is reopened and verified once more before success is reported.
The source security audit then records `backup.full` with only type, safe
basename, outcome, and a closed failure category.

### Configuration-only snapshot

A configuration artifact uses the same staging, synchronization, no-overwrite,
verification, and publication contract. Instead of copying the active database,
Siftail creates a fresh current-schema SQLite database and holds one consistent
source read transaction while it streams explicit columns in stable order for:

- administrator configuration and password hash;
- Servers;
- ingestion-token metadata and hashes;
- settings; and
- stable source identity, observed labels, and aliases.

It retains the required schema but leaves events, raw attributes, container
observations, security audit/diagnostic history, and sessions empty. One row at
a time is retained by the copier. The artifact is marked format-1
`configuration`; restoring it replaces the whole database with this
configuration and empty history. It is not a merge and is not a complete
log-history backup.

### Format version

Artifacts remain format 1. SFT-093 changed only how the full snapshot is copied:
a `VACUUM INTO` artifact contains no free pages, so it is usually smaller than
a 0.4.0 online-backup artifact of the same database, but its schema, metadata
record, verification, and restore handling are identical. `backup verify` and
`siftail restore` accept format-1 artifacts made either way. The restore
command's rollback capture of the current database uses the same `VACUUM INTO`
snapshot.

### Configuration determinism

Equivalent unchanged configuration has deterministic logical rows and
validation. SQLite header state can differ between creations, so byte-for-byte
identity and equal SHA-256 values are not promised.

## Verify without applying

```bash
./siftail backup verify /backups/siftail-full.sqlite
```

Verification is read-only. It requires a regular owner-private file, full
SQLite integrity, all expected tables, supported schema and format versions,
exactly one completed typed metadata row, zero sessions, and type-specific
empty history for configuration artifacts, then streams its byte count and
SHA-256. It never migrates, restores, or changes the artifact.

With the server active, verification uses the owner-only control socket and
records `backup.verify` with a safe basename, type on success, outcome, and
closed category. With the server stopped, verification runs directly and
cannot audit because it does not open or modify the active database. Failures
do not print the submitted path, SQLite details, hashes, or artifact contents.

## Contents and protection

A full backup contains:

- administrator configuration and password hash;
- Servers, ingestion-token hashes and metadata;
- sources, aliases, and container observations;
- retained immutable application events and attributes;
- retention and other settings;
- security audit history;
- schema migrations; and
- format/type/completion metadata.

Neither type contains browser session rows or a recoverable plaintext ingestion
token because Siftail never stores plaintext tokens. Both contain password and
ingestion-token hashes and must be protected as sensitive. Deleted source content can
still exist in older backups, host snapshots, or storage free space; Siftail
does not claim forensic erasure.

Treat the artifact as sensitive. Restrict host access, use encrypted
filesystems/volumes when appropriate, copy it off-host using operator-managed
tools, and define an external retention schedule. Siftail performs no outbound
upload, remote scheduling, telemetry, or automatic backup deletion.

## Failure and cancellation

Application shutdown cancels an active job and waits for its bounded cleanup.
Invalid paths, destination quota/full errors, interruption, integrity or
compatibility failure, publication races, and audit failure cannot return
success. Siftail removes its staging file; if a final link was created but
final verification or success auditing fails, it removes that link too. A
foreign file that won a no-overwrite race is never removed or changed.

The Backup page retains only the latest typed process-local result. It never
retains or re-renders a full path, including the backup directory. Restart clears this presentation
state but does not alter completed artifacts.

## Measured development-host cost

The repository benchmark uses a migrated source with 10,000 events containing
256-byte messages (about a 6.3 MiB artifact) and includes the `VACUUM INTO`
copy, session exclusion/metadata finalization, full verification, two
checksums, file and directory synchronization, atomic publication, and success
auditing. Run it with `TMPDIR` on a real disk (`AGENTS.md` §17.2):

```bash
TMPDIR=/path/on/real/disk go test -run '^$' \
  -bench '^BenchmarkCreateFullBackup$' \
  -benchtime=3x -count=5 -benchmem ./internal/backup
```

On 2026-11-05, five three-iteration runs on a Fedora i5-7500 host with `TMPDIR`
on btrfs over a SATA disk measured a 150.568 ms/op median
(143.512–158.488 ms/op), 43.93 MB/s median artifact throughput,
116,010–117,282 bytes/op, and 1,072–1,077 allocations/op. Single-iteration
runs on the same disk ranged from 1.07 to 4.38 s/op when synchronization
contended with other I/O. For comparison with the 0.4.0 online-backup figure
(95.762 ms/op on tmpfs), the same benchmark on tmpfs measured a 98.321 ms/op
median (96.633–98.701 ms/op). These local results are not guarantees for
larger databases, slower synchronization, concurrent write rates, filesystems,
storage hardware, encryption, or failure conditions.

The continuous-ingestion test backs up an approximately 256 MiB source while
one goroutine commits one event per transaction through the writer coordinator,
and requires completion within 90 s, at least 200 overlapping commits, a
verified artifact, and a captured set of ingested rows that is exactly a prefix
of commit order:

```bash
go test -run '^TestCreateFullBackupCompletesUnderContinuousIngestion$' \
  -count=1 -v ./internal/backup
```

With `TMPDIR` on the same btrfs disk it completed in 3.36 s while 824
transactions committed (213 during the `VACUUM INTO` statement itself).

The configuration benchmark uses the same 10,000-event source but copies one
administrator, Server, token, source, and their configuration into an
approximately 140 KiB artifact:

```bash
go test -run '^$' -bench '^BenchmarkCreateConfigurationBackup$' \
  -benchtime=1s -count=3 -benchmem ./internal/backup
```

Three one-second runs measured a 7.353 ms/op median
(7.323–7.382 ms/op), 19.50 MB/s median artifact throughput,
244,073–244,212 bytes/op, and 2,291 allocations/op. The excluded event volume
does not enter the row copier, but source SQLite snapshot and verification cost,
configuration cardinality, filesystem synchronization, storage, and encryption
still affect real deployments.
