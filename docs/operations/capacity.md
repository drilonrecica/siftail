# Capacity planning

**Scope:** SFT-053. How to size memory, disk, and retention for Siftail 0.5.0
and what the figures can and cannot promise.

## Read this first: measured, bounded, estimated

Every number in this guide is one of three kinds, and is labeled so:

- **Measured**: observed on one host, with the cited document recording
  hardware, filesystem, command, and method. Not a guarantee on other
  hardware. All current measurements are from a Fedora x86-64 development host
  (Intel i5-7500, 4 cores, SATA SSD, btrfs) with `TMPDIR` on a real disk, as
  recorded in [`../performance/`](../performance/).
- **Bounded**: a configured limit enforced by the code. It holds on any
  hardware, but says nothing about speed.
- **Estimated**: arithmetic from bounds or measurements, shown with its
  inputs. Treat as planning, not a promise.

**Not yet measured:** the production container at 1,000 events/s sustained,
5,000 events/s burst, and datasets of 1 M to 10 M rows (SFT-054, recorded in
`docs/performance/` when it completes). Until then, database sizes beyond
what is cited below, and any row-count query claim above 1 M rows, are not
established. Do not size a deployment on a figure this guide marks as missing.

## Memory

| Item | Value | Kind |
| --- | --- | --- |
| Idle process RSS | 15,140 KiB at readiness and after 10 idle seconds (amd64 container) | **Measured**, one host ([`container-image.md`](container-image.md#measured-footprint)) |
| In-flight ingestion queue | 20,000 events and 32 MiB (`SIFTAIL_QUEUE_MAX_EVENTS`, `SIFTAIL_QUEUE_MAX_BYTES`) | **Bounded** |
| Resident ingestion work, including requests being decoded | 30,000 events and 64 MiB (`SIFTAIL_INGEST_RESIDENT_MAX_EVENTS`, `…_BYTES`) | **Bounded** |
| Concurrent request decoders | 4 (`SIFTAIL_INGEST_MAX_DECODERS`) | **Bounded** |
| One request | 5 MiB compressed, 25 MiB decompressed, 10,000 events, 1 MiB per event | **Bounded** |
| Filling the 32 MiB queue raised benchmark-process RSS by about 40 MB | | **Measured**, Go benchmark process, not the container ([`0.1.0.md`](../performance/0.1.0.md)) |

**Estimate:** a container memory limit of 256 MiB comfortably covers the idle
footprint plus the 64 MiB resident bound and SQLite page cache; give it more
before you lower any queue bound or raise a request cap. Raising
`SIFTAIL_MAX_*` or queue bounds raises the worst-case resident memory
proportionally. A request that exceeds a bound is refused (`413`), and a full
queue answers `503` so the sender retries; neither grows memory.

Siftail does not enforce a container memory limit itself. Set one in Compose
or Coolify and leave headroom over the bounded resident figures.

## Throughput

| Result | Value | Kind |
| --- | --- | --- |
| Group commit, 100-event batches, 16 producers | about 8,000–9,600 events/s, p95 about 190–250 ms | **Measured**, Go write path on the host above ([`group-commit.md`](../performance/group-commit.md)) |
| Single-event batches, 16 producers | about 1,700–2,100 events/s, p95 9–13 ms | **Measured**, same |
| Without group commit, small batches | about 300 batches/s ceiling (one fsync each) | **Measured**; why group commit exists |
| Writer is CPU-bound at about 9,500 events/s with 100-event batches | | **Measured**, same host |

These measure the write path, not the shipped container, and exclude cold
source discovery and a slower disk. **A disk with slow fsync lowers them;** a
network volume is the common cause. The 1,000 events/s sustained target is
established for this path only; the production-container confirmation is
SFT-054.

If throughput is the limit, senders see `503` and retry. Nothing acknowledged
is lost, but the sender's own buffer (256 MiB for the supported Fluent Bit
output) bounds how long an outage can last before the oldest chunks are
discarded there.

## Database, WAL, and filesystem space

Siftail stores everything in `/data/siftail.db` in WAL mode. Three things
share the data volume:

1. **The database file.** It grows with retained events and indexes. After
   deletion, free pages are reused before the file grows, and the file shrinks
   at most 8,192 pages (32 MiB at the default page size) per reclamation
   cycle, so it can be larger than the footprint for a while.
2. **The write-ahead log (`-wal`) and shared-memory file (`-shm`).** New
   commits land in the WAL, which is checkpointed back into the database. The
   WAL grows during sustained writes, a backup, or a long read, and counts
   toward the footprint Siftail limits. Size the volume so the WAL can grow by
   several hundred MiB under load without filling it.
3. **Temporary files.** A backup or restore stages a copy on the same volume,
   and an export stages up to 256 MiB.

**Footprint limit.** Retention's size limit (default 4 GiB, 1–1,024 GiB) is on
the *active footprint* (in-use pages plus WAL and SHM), not on the filesystem.
Size retention is therefore a cap on logs, not a reservation of space.

**Estimate for the volume:**

```text
volume ≥ footprint limit              (database + WAL + SHM at the cap)
       + free pages not yet returned  (up to 32 MiB per reclamation cycle)
       + largest transient use        (see below)
       + safety margin                (keep ≥ 20% free)
```

Largest transient use, whichever is largest in your operation:

| Operation | Needs on the data volume | Kind |
| --- | --- | --- |
| Backup written to the same volume | the logical database plus 5% or at least 1 MiB, **plus WAL growth** because ingestion continues | **Bounded** rule from [`full-backups.md`](full-backups.md) |
| Backup to another volume | that volume has the same room; the data volume still needs WAL headroom | same |
| Restore | the artifact plus the current logical database and WAL, plus 5% or at least 1 MiB | **Bounded** rule from [`restores.md`](restores.md) |
| Export | 320 MiB free at start (256 MiB limit plus 64 MiB reserve) | **Bounded** ([`export-formats.md`](export-formats.md)) |

When the volume fills, ingestion answers `507` and readiness fails until space
returns; see [`storage-full-recovery.md`](storage-full-recovery.md). Retention
cannot make space for itself if the filesystem is already full, so lower the
limits before that point. Monitor free space on the volume; Siftail does not
send alerts.

**Bytes per event** depend on message length, attributes, and indexes, so no
single figure is promised. Measure your own: note the database size and the
event count on Status after a day of real traffic and divide. The measured
cost of a 10,000-event, 256-byte-message source is a 6.3 MiB backup artifact
([`full-backups.md`](full-backups.md#measured-development-host-cost)), roughly
660 bytes per event including structure, which is a **measured** point for that
shape, not a rule.

**Estimate, with its inputs:** at that ~660 B/event figure, 1,000 events/s
sustained is about 52 GiB per day before retention, and the default 4 GiB cap
holds about 6.6 million events. Real events with longer messages and more
attributes will hold fewer. Set age and size limits so that the *size* limit
is the one you intend to bind, and choose the volume from the table above.

## Retention

Retention keeps events for at least the configured age after receipt and
deletes oldest-received first when the footprint reaches 95% of the size limit,
down to 90%. Whichever limit is reached first wins. If events arrive faster
than a day's worth fits under the size limit, the effective age is shorter
than configured and Status reports size-based deletion. Plan the size limit
from your event rate and the age you need, not the reverse.

Cleanup is bounded: **measured**, a 10,000-row delete chunk completed in a
median 247 ms and the writer held for at most about 80 ms per reclamation
chunk, with concurrent-cleanup ingest p95 of about 16 ms
([`../performance/retention.md`](../performance/retention.md)). See
[`retention.md`](retention.md).

## Query cost

**Measured** on one host, History's indexed shapes cost about one page of index
reads at both 100,000 and 1,000,000 rows (about 1.1–1.7 ms per page), deep
cursors cost the same as the first page, and a literal text search scans the
chosen time range (about 117 ms at 100,000 rows and 236 ms at 1,000,000)
([`../performance/history-queries.md`](../performance/history-queries.md)).
Text search is the cost that grows with data, so narrow the time range for
large databases. Behavior at 10 million rows is **not yet measured**.

## Backup and restore cost

**Measured:** a 10,000-event full backup (about 6.3 MiB) took a median
150 ms on this host, with 1–4 s spread when other disk I/O competed
([`full-backups.md`](full-backups.md#measured-development-host-cost)). A backup
of a continuously written 256 MiB database completed within the test's 90 s
limit. Time grows with database size and fsync cost; there is no figure for
multi-GiB databases yet. Run a backup against your own volume outside peak
hours, and time it, before relying on a window.

## Resource ceilings in one place

| Ceiling | Value | Kind |
| --- | --- | --- |
| Request size | 5 MiB compressed, 25 MiB decompressed | Bounded |
| Events per request / bytes per event | 10,000 / 1 MiB | Bounded |
| Ingest queue | 20,000 events, 32 MiB | Bounded |
| Resident ingest | 30,000 events, 64 MiB | Bounded |
| Group commit | 64 batches, 8 MiB, no wait | Bounded |
| Browser sessions active | 64 | Bounded |
| Status diagnostics retained | 100 | Bounded |
| Retention age / size | 1–3,650 days / 1–1,024 GiB | Bounded |
| Concurrent backup, export | one each | Bounded |
| Reclamation per cycle | 8,192 pages | Bounded |

Change a bound only with a measured reason; the defaults are the tested ones.
A bound that is hit produces a documented refusal or a retryable `503`/`507`,
never silent loss of an acknowledged event.

## What this guide does not claim

No hardware-independent throughput, no latency guarantee, no lossless delivery
across a long outage, no multi-instance or high-availability behavior (one
Siftail process owns one database), and no statement about 10-million-row
behavior until SFT-054 records it.
