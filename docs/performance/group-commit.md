# Group commit (SFT-098)

**Measured:** 2026-11-05
**Scope:** the durable write path before and after group commit (ADR 0009); the
basis for the default group bounds. Not a hardware-independent guarantee.
**Storage:** `TMPDIR` on btrfs on a SATA SSD (Crucial BX500), not tmpfs.

## Method and environment

- Host: Fedora Linux 7.2.10 x86-64; Intel Core i5-7500, four cores, 3.40 GHz.
- Go 1.27.3 linux/amd64 with CGO; `github.com/mattn/go-sqlite3` v1.14.52
  bundling SQLite 3.53.4.
- Production pragmas: WAL, `synchronous=FULL`, foreign keys on, one writer
  connection. Every acknowledged batch still waits for a `FULL` commit.
- `BenchmarkGroupCommitConcurrent` (`internal/ingest/group_benchmark_test.go`)
  runs 16 or 64 concurrent producers. Each producer admits a batch through the
  real admission ledger and queue, the writer worker persists it with the real
  writer and coordinator, and the producer waits for its outcome, as an HTTP
  client does. Batches carry 1, 10, or 100 events with stable source event IDs
  for one source and container. Latency is enqueue to acknowledgement. The
  benchmark excludes HTTP decoding, which runs before admission and in
  parallel.
- Command, run from the repository root:

  ```bash
  mkdir -p .bench-tmp
  TMPDIR=$PWD/.bench-tmp go test -run '^$' \
    -bench '^BenchmarkGroupCommitConcurrent$' \
    -benchtime=1000x -count=3 -benchmem -timeout 30m ./internal/ingest
  ```

- **Before** is the same benchmark against the pre-SFT-098 writer: one
  transaction per batch, deferred `BEGIN`, no savepoints. It covered 10- and
  100-event batches with 16 producers. `max-batches=1` on the new writer is a
  group of one per transaction. It matched the old writer within run-to-run
  noise in every case measured both ways, so it stands in for "before" in the
  other rows.
- Every cell gives the three runs, in run order.

## Results

### Before and after at the default bounds

`max-batches=64`, 8 MiB, zero wait.

| Batch | Producers | Writer | Batches/s | Events/s | p95 latency |
|---:|---:|---|---|---|---|
| 1 event | 16 | per-batch (`max-batches=1`) | 345 / 267 / 271 | 345 / 267 / 271 | 71 / 88 / 106 ms |
| 1 event | 16 | **group commit** | 1,676 / 1,770 / 2,100 | 1,676 / 1,770 / 2,100 | 11 / 13 / 9 ms |
| 1 event | 64 | per-batch (`max-batches=1`) | 277 / 285 / 279 | 277 / 285 / 279 | 301 / 269 / 314 ms |
| 1 event | 64 | **group commit** | 3,463 / 3,365 / 3,072 | 3,463 / 3,365 / 3,072 | 31 / 44 / 35 ms |
| 10 events | 16 | before (old writer) | 120 / 183 / 197 | 1,199 / 1,828 / 1,972 | 135 / 167 / 129 ms |
| 10 events | 16 | per-batch (`max-batches=1`) | 180 / 184 / 184 | 1,801 / 1,840 / 1,839 | 150 / 128 / 137 ms |
| 10 events | 16 | **group commit** | 579 / 523 / 594 | 5,789 / 5,229 / 5,943 | 37 / 53 / 47 ms |
| 10 events | 64 | per-batch (`max-batches=1`) | 149 / 214 / 165 | 1,494 / 2,142 / 1,645 | 1,032 / 380 / 1,009 ms |
| 10 events | 64 | **group commit** | 826 / 769 / 834 | 8,261 / 7,691 / 8,336 | 95 / 126 / 88 ms |
| 100 events | 16 | before (old writer) | 68 / 62 / 67 | 6,764 / 6,214 / 6,741 | 285 / 360 / 302 ms |
| 100 events | 16 | per-batch (`max-batches=1`) | 70 / 70 / 59 | 7,048 / 7,014 / 5,902 | 274 / 292 / 374 ms |
| 100 events | 16 | **group commit** | 96 / 80 / 92 | 9,610 / 8,029 / 9,185 | 194 / 254 / 217 ms |
| 100 events | 64 | per-batch (`max-batches=1`) | 65 / 55 / 58 | 6,518 / 5,533 / 5,805 | 1,194 / 1,476 / 1,571 ms |
| 100 events | 64 | **group commit** | 93 / 86 / 91 | 9,313 / 8,626 / 9,115 | 733 / 822 / 765 ms |

A second run of the 100-event, 16-producer default gave 95.8 / 95.1 / 95.6 /
95.8 batches/s, with p95 of 194 / 195 / 188 / 188 ms.

Per-batch commit costs about one fsync on this disk. Small batches therefore
top out near 300 batches/s whatever their size, so single-event batches missed
the 1,000 events/s target (`AGENTS.md` §17.1). Group commit spreads one fsync
over every batch queued while the previous group committed, so throughput
grows with concurrency. Small and medium batches gain 3–12× in throughput, and
p95 commit latency falls by a similar factor.

At 100 events per batch the writer is CPU bound, at about 9,500 inserted
events/s on this machine, and group commit adds about 35%. Sixty-four
concurrent 100-event producers offer more than that ceiling, so their latency
is queueing at saturation, not commit time. It is still lower than with
per-batch commits.

### Choosing the batch bound

| Batch | Producers | `max-batches=16` | `=64` (default) | `=256` |
|---:|---:|---|---|---|
| 10 events | 16 | 632 / 645 / 635 batches/s, p95 42 / 42 / 38 ms | 579 / 523 / 594, p95 37 / 53 / 47 ms | 575 / 639 / 533, p95 42 / 33 / 58 ms |
| 10 events | 64 | 791 / 711 / 751, p95 87 / 212 / 114 ms | 826 / 769 / 834, p95 95 / 126 / 88 ms | 841 / 773 / 843, p95 96 / 130 / 83 ms |
| 100 events | 16 | 30 / 27 / 96, p95 971 / 963 / 201 ms | 96 / 80 / 92, p95 194 / 254 / 217 ms | 75 / 71 / 94, p95 301 / 346 / 195 ms |
| 100 events | 64 | 92 / 95 / 98, p95 810 / 798 / 686 ms | 93 / 86 / 91, p95 733 / 822 / 765 ms | 87 / 95 / 97, p95 1,211 / 723 / 792 ms |

With 16 producers at most 16 batches are ever queued, so bounds of 16 and
above should behave alike. Bounds above 64 buy nothing measurable. A
`max-batches=16` repeat at 100 events gave 91.7 / 29.7 / 92.3 / 95.6
batches/s. Sporadic multi-second stalls occurred in 3 of 7 runs at 16 and in
none of 7 at 64, which points to disk noise rather than the bound. Larger
groups also mean a maintenance turn may wait one group longer.

### Single-request latency is unchanged

At low load every group has one batch. It now runs `BEGIN IMMEDIATE`,
`SAVEPOINT`, and `RELEASE` instead of a deferred `BEGIN`. The same real disk
gave these numbers for three runs of 500 operations each:

| Benchmark | Before | After |
|---|---|---|
| `BenchmarkBatchWriterSQLiteCommit` | 3.43 / 4.06 / 3.91 ms/op | 3.55 / 3.42 / 3.46 ms/op |
| `BenchmarkHTTPCommitLatency` p95 | 6.39 / 6.81 / 6.11 ms | 5.88 / 5.38 / 5.54 ms |
| `BenchmarkHTTPSustained100EventBatch` (one client) | 6,014 / 6,289 / 6,243 events/s | 6,982 / 6,800 / 6,914 events/s |

## Defaults

| Variable | Default | Allowed |
|---|---|---|
| `SIFTAIL_INGEST_GROUP_MAX_BATCHES` | `64` | 1–1024 |
| `SIFTAIL_INGEST_GROUP_MAX_BYTES` | `8388608` (8 MiB of approximate retained bytes) | 1 to `SIFTAIL_QUEUE_MAX_BYTES` |
| `SIFTAIL_INGEST_GROUP_MAX_WAIT` | `0s` | 0s–100ms |

- **64 batches** is where throughput stops improving above. It bounds one
  group's transaction to about 0.7 s at 100-event batches on this machine.
- **8 MiB** keeps a group to a quarter of the default 32 MiB queue. It holds
  64 batches of typical size (64 × 100 × 512 B ≈ 3.2 MiB) while large batches
  form smaller groups. A batch at or above the bound forms a group of one.
- **Zero wait** means grouping never delays a lone request. Batches that queue
  while a commit is in flight are grouped anyway, which is where every gain
  above comes from. A positive wait only helps a disk whose fsync is much
  slower than the arrival gap, and it adds that wait to every group.
  `SIFTAIL_INGEST_GROUP_MAX_BATCHES=1` restores one commit per batch.

## Limitations

- One machine and one SSD. A disk with a faster fsync gains less, and a
  network or HDD volume gains more.
- The benchmark has one source and container and fully unique event IDs. Cold
  source discovery and deduplication lookups add per-event CPU, which lowers
  the CPU ceiling but not the fsync saving.
- This is not the production-container measurement that `AGENTS.md` §17.2
  asks for. That, and re-running the older documents on a real disk, belong
  to SFT-100.
