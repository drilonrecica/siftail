# Ingestion hot path and audit cap: allocations before and after

**Measured:** 2026-11-05 (SFT-099; audit DAT-21)
**Host:** Intel Core i5-7500 (4 cores), Linux 7.2.10, Go 1.27.1
**Storage:** `TMPDIR=/var/tmp/siftail-bench` on btrfs, Crucial BX500 480 GB
SATA SSD (`CT480BX500SSD1`, non-rotational). Real disk with `fsync`; no tmpfs
number appears here.

## SFT-100 rerun (2026-11-05)

Same benchmarks, `make bench BENCH_GROUPS=ingest`, five runs, Go 1.27.3,
SQLite 3.53.4 (go-sqlite3 v1.14.52), `TMPDIR` on btrfs (`/dev/sda3`) on the
Crucial BX500 SATA SSD (non-rotational), the same disk as below. Medians:
`DecodeBatch100` 407 µs, 331,888 B, 3,813 allocs; `Normalize` 8.90 µs, 83
allocs; `BatchWriter100Events` 7.63 ms, 4,631 allocs; `BatchWriterSQLiteCommit`
3.77 ms, 164 allocs; `BatchWriterIdempotentRetry` 3.38 ms, 206 allocs;
`AuditRecordAtCapacity` 3.60 ms, 74 allocs. Allocation counts reproduce the
SFT-099 "after" column exactly and times fall inside its ranges. The whole
document is real-disk; none of it is tmpfs.

## What changed

- Each received record is tokenized once. `logs.ParseRecord` validates the
  record (UTF-8, duplicate keys at every depth, nesting, trailing data) and
  splits its top-level members in the same pass. Before, the decoder ran
  `ValidateJSON`, then `json.Unmarshal` into a map, then `Normalize` validated
  the record again, and a record without `log` was validated and unmarshalled
  a third time as structured content.
- Field values that are plain ASCII JSON strings are converted directly
  instead of through reflective `json.Unmarshal`. Anything else still goes
  through `json.Unmarshal`, so the results are the same; a test pins that.
- The writer prepares the per-event `log_events` INSERT and the stable-ID
  lookup once per commit group instead of compiling them for every event.
- The audit store reads a trigger-maintained row counter (migration 0009)
  instead of running `count(*)` over the capped table on every audit insert.

## Method

```bash
TMPDIR=/var/tmp/siftail-bench go test -run '^$' -benchmem -count=6 \
  -bench 'BenchmarkNormalize$' ./internal/logs
TMPDIR=/var/tmp/siftail-bench go test -run '^$' -benchmem -count=6 \
  -bench 'BenchmarkDecodeBatch100$|BenchmarkBatchWriterSQLiteCommit$|BenchmarkBatchWriterIdempotentRetry$|BenchmarkBatchWriter100Events$' \
  ./internal/ingest
TMPDIR=/var/tmp/siftail-bench go test -run '^$' -benchmem -count=6 \
  -bench 'BenchmarkAuditRecordAtCapacity$' ./internal/audit
```

The "before" runs used the tree at commit `7969db0` (SFT-098), and the "after"
runs the SFT-099 tree, on the same host and disk. `BenchmarkBatchWriter100Events`
is new in SFT-099; it was added and measured before any writer change. The
audit benchmark was run with `-count=8` after the change because two of the
first six runs measured 81 ms and 98 ms per operation at a `b.N` of 12 to 14,
consistent with disk write stalls; the eight-run repeat had no such run
(3.08 to 3.70 ms). Values are medians.

## Results

| Benchmark | Time before | Time after | Bytes before | Bytes after | Allocs before | Allocs after |
|---|---:|---:|---:|---:|---:|---:|
| `DecodeBatch100` (100 NDJSON events) | 1,045 µs | 409 µs | 567,002 | 331,888 | 9,714 | 3,813 |
| `Normalize` (one unparsed record) | 12.4 µs | 9.1 µs | 5,055 | 3,821 | 102 | 83 |
| `BatchWriter100Events` (100 events, stable IDs) | 14.5 ms | 7.6 ms | 476,968 | 395,864 | 6,797 | 4,630 |
| `BatchWriterSQLiteCommit` (1 event per batch) | 3.89 ms | 3.77 ms | 8,208 | 8,689 | 154 | 163 |
| `BatchWriterIdempotentRetry` (1 event per batch) | 3.33 ms | 3.51 ms | 7,213 | 8,122 | 192 | 205 |
| `AuditRecordAtCapacity` (100,000 rows) | 4.50 ms | 3.30 ms | 3,579 | 3,462 | 74 | 74 |

Per decoded event, allocations fall from about 97 to 38 and decode time from
about 10.4 µs to 4.1 µs. A 100-event batch spends about half as long in the
writer: two compiled statements serve all 100 events.

A single-event batch now pays the preparation of both statements for one
use, which adds 9 to 13 allocations (about 0.5 to 0.9 KB). Its time is
dominated by the commit `fsync` either way; the difference between the
single-event time medians is within run-to-run noise. The audit insert at the
cap saves about 1.2 ms of the `count(*)` scan; the rest is the commit `fsync`.

`Normalize` is measured on a record built outside `ParseRecord`, so it still
includes its own validation and field copy. Its gain comes from the
direct string conversion and from not decoding the record twice.
