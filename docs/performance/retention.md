# Retention concurrency and reclamation evidence

**Measured:** 2026-11-05
**Host:** Fedora Linux, Intel Core i5-7500 at 3.40 GHz, linux/amd64, Go 1.27.3
**Storage:** btrfs on a SATA SSD (`TMPDIR` on the data volume, not tmpfs)
**Scope:** SFT-033 bounded application-event retention; SFT-092 chunked
reclamation and the in-use-page footprint (audit DAT-05, DAT-13)

## Production limits and ordering

The lifecycle-owned worker runs once at startup and then once per hour. It
loads the atomic retention policy and deletes at most 10,000 events per
coordinator-owned transaction in:

```text
received_at_us ASC, id ASC
```

That order has applied since ADR 0008 (schema 7).

Age deletion uses a fixed cutoff for the run. Size deletion begins at 95% of
the configured active footprint and remeasures after every delete/reclaim
cycle until the footprint is at or below 90%, no events remain, cancellation
occurs, or a checkpoint is busy.

The footprint (`DOMAIN.md` §16.6) is

```text
(page_count − freelist_count) × page_size + WAL file + SHM file
```

with the three page values read in one statement from one read snapshot.
Pages a deletion freed therefore count as reclaimed at once, before vacuum
returns them to the filesystem. Before SFT-092 the main database's file size
was used, so freed pages kept the measurement high and size retention went on
deleting events that the freelist would already have absorbed (DAT-13).

## Reclamation and writer holds

One reclamation cycle is a sequence of coordinator operations in the same
64-entry queue as ingestion and administrative mutations:

1. a passive checkpoint preflight;
2. `PRAGMA incremental_vacuum(1024)` chunks, at most 8,192 pages per cycle,
   stopping early once the freelist is empty;
3. a controlled truncate checkpoint to release the physical WAL allocation.

Each step is its own writer turn, so an ingestion commit queued behind one
step runs before the next. One chunk is one transaction with one WAL commit.
The pragma releases one page per result step, so the rows are drained: an
`Exec` would step once and release a single page. That is why the earlier code
issued 8,192 `incremental_vacuum(1)` autocommit transactions, each an fsync
under `synchronous=FULL`, inside one maintenance operation. On this disk that
single operation held the writer for about 19 s (DAT-05).

A busy checkpoint stops size deletion for that run so a long-lived reader
cannot cause speculative over-deletion. Routine full `VACUUM` is never used.

Every committed nonempty chunk publishes one nonblocking global
`retention_purged` control after the coordinator returns success. A rollback
publishes nothing.

### Maximum writer hold per chunk

A 1,024-page (4 MiB) chunk held the writer for **72–189 ms**. Across five
reclaim benchmark samples the median was 74 ms and the maximum 176 ms. In four
runs of the concurrent-ingestion test, three of them under the race detector,
the longest chunk held 71–189 ms. The fixture relocates live pages: 10,000 expired 3,000-byte events
fill the front of the file and 2,000 retained events its tail. The checkpoints
held it for 13–118 ms, median 15 ms. A full 8,192-page cycle took 262–608 ms of
writer time in total, spread over ten turns.

`TestCleanerReclaimsInBoundedChunksDuringConcurrentIngestion` runs the same
fixture with production chunk sizes while a goroutine commits ingestion
continuously through the coordinator. It asserts eight full chunks of at most
1,024 pages each, a hold of at most 1 s per chunk, at least one ingestion
commit between chunks, and that every committed event is stored. In every
recorded run, an ingestion commit landed in each of the seven gaps. The 1 s
ceiling leaves room for slower disks while still failing on the old per-page
commits. The test logs the temp-directory filesystem. Run it with `TMPDIR` on
a real disk for a meaningful hold.

A chunk-size probe on the same fixture shape measured these maximum holds:
256 pages 68 ms, 512 pages 84 ms, 1,024 pages 99 ms, 2,048 pages 189 ms, and
4,096 pages 221 ms. The 1,024-page chunk keeps one hold well inside the
250 ms commit-latency target. Larger chunks barely shorten the total.

## Repeated benchmark fixture

Command, run once on the pre-SFT-092 code and once on the current code:

```bash
TMPDIR=<directory on the data volume> go test -run '^$' \
  -bench '^BenchmarkRetention' -benchtime=1x -count=5 -benchmem \
  ./internal/retention
```

The disk showed outliers in both runs. For example, one baseline sample
before the change had a 559 ms p95. The tables give the median and the range
of five samples.

`BenchmarkRetentionDeleteChunk10000` creates 10,000 events with a 1 KiB raw
and text payload. It then times canonical deletion, reclamation, and
checkpoints. The main database goes from 40.18 MiB to 8.145 MiB and the WAL
from 40.67 MiB to 0. The freelist goes from 0 to 2,048 free pages, which are
left after the 8,192-page cycle cap.

| Complete cleanup | Before SFT-092 | After SFT-092 |
| --- | --- | --- |
| Median | 23.07 s | 270 ms |
| Range | 20.41–66.51 s | 238–420 ms |
| Allocations | 32,979 (600 KB) | 579 (24 KB) |

`BenchmarkRetentionWriterInterference` cleans up 20,000 256-byte events in
1,000-event transactions while it measures 100 one-event commits through the
same coordinator. `BenchmarkRetentionWriterBaseline` measures the same commits
without cleanup.

| Writer commits | Before SFT-092 | After SFT-092 |
| --- | --- | --- |
| Baseline p95, median | 3.67 ms | 3.66 ms |
| Concurrent-cleanup p95, median (range) | 27.9 ms (14.3–155.4 ms) | 21.1 ms (12.9–29.3 ms) |
| Concurrent run, median | 8.89 s | 610 ms |

That fixture frees few pages, so its p95 mostly reflects the 1,000-event
delete transactions, not the vacuum. The reclaim benchmark and the
concurrent-ingestion test measure the vacuum holds directly.

`BenchmarkRetentionReclaimChunks` is the relocation fixture described above,
with production chunk sizes. It reports `max_chunk_ms`, `max_checkpoint_ms`,
`reclaim_ms`, `chunks`, and `pages_released`. All five samples released
8,192 pages in eight chunks, and the file went from 48.42 MiB to 16.38 MiB.

The relative p95 increase under cleanup is expected from deliberate
serialization. Production ingestion batches have different allocation and
fsync shapes, so these benchmarks compare maintenance interference. They do
not guarantee throughput on other hosts.

The record before SFT-092 (2026-07-28) gave 205 ms for a complete cleanup and
a 3.12 ms concurrent p95. Those numbers came from `b.TempDir()` on tmpfs and
are a no-fsync comparison only.

## Correctness and resource coverage

Real-SQLite tests cover:

- events: delayed old events, future-skewed events, and equal retention
  timestamps with ID tie-breaking;
- deletion: bounded multi-chunk deletion, post-commit notification,
  trigger-forced rollback, cancellation after a committed chunk, and no-event
  exhaustion;
- size limits: a simulated 95%/90% size threshold over real event deletion;
- the in-use-page footprint: free pages are excluded and an uncheckpointed WAL
  counts in full, and size retention over the real page measure stops at the
  target while almost nothing is vacuumed (DAT-13);
- vacuum: whole-chunk drains and the rejection of a zero-page vacuum, which
  would release the whole freelist;
- concurrency: chunked reclamation under concurrent ingestion, busy-reader
  checkpoint behavior, worker restart and shutdown, and concurrent coordinator
  writes plus read-pool queries.

Targeted race tests repeat the concurrent paths.

The size-threshold tests scale or inject footprint readings because the
minimum accepted operator target is 1 GiB, and ordinary tests do not create a
disposable gigabyte database. Filesystem allocation, sparse-file behavior,
storage latency, and concurrent external snapshots vary by host.
