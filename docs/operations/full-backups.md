# Verified online full backups

**Reviewed:** 2026-07-28  
**Scope:** SFT-042 active-server format-1 full backup

## Create

Mount or create a protected destination directory that is separate from the
active `/data` database files, is persistent across container replacement, and
supports ordinary same-directory hard links, then run:

```bash
./siftail backup --output /backups/siftail-full.sqlite
```

The server must be active because the CLI uses the owner-only Unix control
socket. The authenticated Backup workspace provides the same operation. Only
one backup runs at a time. The CLI polls typed status until completion; the
browser replaces only its Backup region once per second. Neither path streams
database bytes through HTTP.

The output directory must already exist and have capacity for the current
logical SQLite page count plus 5% or at least 1 MiB of slack. Siftail refuses:

- the live main database, WAL, or shared-memory path;
- a missing/non-directory parent;
- an existing file or symbolic link at the final path; and
- a destination it cannot inspect, create, synchronize, or publish safely.

Siftail creates a random hidden staging file beside the requested output with
mode `0600`. It never overwrites the final path, including if another process
creates that path between validation and publication.

## Snapshot and verification contract

The active reader pool supplies a dedicated source connection to SQLite's
online backup API. Each step copies at most 256 pages and yields before the
next step. Ordinary ingestion remains on the single writer coordinator. SQLite
provides one consistent snapshot: a concurrent committed request is wholly
included or wholly absent, never partly copied.

After copying, Siftail modifies only the staged destination:

1. use a self-contained delete journal with `synchronous=FULL`;
2. enable SQLite secure deletion and delete every active or historical browser
   session row;
3. write one format-1 `full` metadata record containing creation time, source
   schema version, and completed state;
4. close and synchronize the artifact;
5. run full `integrity_check`;
6. require the supported schema version and every full-backup table;
7. require exactly one completed metadata record and zero session rows; and
8. stream SHA-256 calculation without loading the artifact into memory.

Only a verified inode is hard-linked atomically to the requested non-existing
filename. The staging link is removed and the parent directory is synchronized.
The final path is reopened and verified once more before success is reported.
The source security audit then records `backup.full` with only type, safe
basename, outcome, and a closed failure category.

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

It contains no browser session rows and no recoverable plaintext ingestion
token because Siftail never stores plaintext tokens. Deleted source content can
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
retains or re-renders the submitted full path. Restart clears this presentation
state but does not alter completed artifacts.

## Measured development-host cost

The repository benchmark uses a migrated source with 10,000 events containing
256-byte messages (about a 6.72 MiB artifact) and includes online copy, session
exclusion/metadata finalization, full verification, two checksums, file and
directory synchronization, atomic publication, and success auditing:

```bash
go test -run '^$' -bench '^BenchmarkCreateFullBackup$' \
  -benchtime=1s -count=3 -benchmem ./internal/backup
```

Three one-second Fedora/i5 runs measured a 95.762 ms/op median
(95.349–100.503 ms/op), 73.61 MB/s median artifact throughput, 128,844–130,613
bytes/op, and 1,086–1,088 allocations/op. These local results are not
guarantees for larger databases, slower synchronization, concurrent write
rates, filesystems, storage hardware, encryption, or failure conditions.
