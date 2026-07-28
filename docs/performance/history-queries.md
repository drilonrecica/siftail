# Historical query baseline

**Measured:** 2026-07-28  
**Scope:** SFT-020 development baseline; not a hardware-independent guarantee

## Method and environment

- Host: Fedora Linux 7.1.4 x86-64.
- CPU: Intel Core i5-7500, four physical cores, 3.40 GHz nominal.
- Memory: 31 GiB host RAM; no container memory or CPU limit.
- Toolchain: Go 1.26.5 linux/amd64 with CGO. The repository compatibility floor
  remains the version in `go.mod`.
- SQLite: bundled by `github.com/mattn/go-sqlite3` v1.14.48 with production
  migrations, WAL, `synchronous=FULL`, foreign keys, five-second busy timeout,
  memory temporary storage, incremental auto-vacuum, one writer connection,
  and four read connections.
- Fixture: either 100,000 or 1,000,000 immutable events across two sources.
  Half use each source, one in ten is `error`, one in three is `stderr`, and one
  in 1,000 contains the literal `needle`. Event timestamps are increasing
  microseconds. Setup occurs in one transaction outside benchmark timing.
- Each measured operation reads a 200-row page through the production scanner.
  The literal case scans until it finds 200 matching rows, or reaches the end
  of the bounded range.
- Measurement command:

  ```bash
  go test -run '^$' -bench '^BenchmarkHistoryStore(100K|1M)$' \
    -benchtime=5x -count=1 ./internal/logs
  ```

- Plans were captured on both fixture sizes with:

  ```bash
  go test -run '^$' -bench '^BenchmarkHistoryStore(100K|1M)$' \
    -benchtime=1x -count=1 -v ./internal/logs
  ```

## Results

These are development-machine measurements:

| Rows | Query | Time/op | Allocated bytes/op | Allocations/op |
|---:|---|---:|---:|---:|
| 100,000 | unfiltered | 1.272 ms | 465,092 | 12,015 |
| 100,000 | exact source + level | 1.548 ms | 468,636 | 12,052 |
| 100,000 | literal `needle` | 39.718 ms | 274,216 | 6,029 |
| 1,000,000 | unfiltered | 1.284 ms | 465,080 | 12,015 |
| 1,000,000 | exact source + level | 1.556 ms | 468,576 | 12,052 |
| 1,000,000 | literal `needle` | 82.259 ms | 465,320 | 12,020 |

`EXPLAIN QUERY PLAN` selected `log_events_time_idx` for the unfiltered and
literal cases at both sizes. Exact full-source scope first used the unique
source identity index, then `log_events_source_time_idx`; Server and container
metadata used their primary-key lookups. An exact container query is separately
asserted to use `log_events_container_time_idx`.

No new index is justified by this baseline. Literal substring search has no
dedicated index by design and remains bounded by the required time range;
introducing FTS5 still requires the documented ADR and measurements.

## Review and remaining gate

- Pages fetch `limit+1`, return at most 500 events, and do not run `COUNT(*)`.
- Existing write and retention index cost is unchanged; no migration was added.
- Query memory is bounded by the page cap plus the schema-bounded event payload.
- Tests cover cancellation, hostile cursors, literal `%`, `_`, and backslash,
  and ASCII-only case folding. Benchmark fixtures contain no real payloads or
  secrets.
- The 10-million-event query and plan review remains an explicit pre-public
  release gate. It was not run for this change and these results must not be
  generalized to other storage, filesystems, CPUs, or containers.
