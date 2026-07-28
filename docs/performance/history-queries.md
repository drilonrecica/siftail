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
- Each measured operation reads a 200-row page through the production list
  scanner. List rows select a maximum 2,048-character message preview and omit
  raw payload, attributes, and detail-only common fields; complete bounded
  payloads use the separate event lookup. The literal case scans until it finds
  200 matching rows, or reaches the end of the bounded range.
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
| 100,000 | unfiltered | 1.023 ms | 342,004 | 7,559 |
| 100,000 | exact source + level | 1.168 ms | 345,475 | 7,596 |
| 100,000 | literal `needle` | 39.864 ms | 212,520 | 3,795 |
| 1,000,000 | unfiltered | 0.993 ms | 341,976 | 7,559 |
| 1,000,000 | exact source + level | 1.142 ms | 345,472 | 7,596 |
| 1,000,000 | literal `needle` | 81.847 ms | 342,216 | 7,564 |

`EXPLAIN QUERY PLAN` selected `log_events_time_idx` for the unfiltered and
literal cases at both sizes. Exact full-source scope first used the unique
source identity index, then `log_events_source_time_idx`; Server and container
metadata used their primary-key lookups. An exact container query is separately
asserted to use `log_events_container_time_idx`.

No new index is justified by this baseline. Literal substring search has no
dedicated index by design and remains bounded by the required time range;
introducing FTS5 still requires the documented ADR and measurements.

## 0.2.0 gate rerun

SFT-024 reran each 100k/1M case once on the same host and configuration:

```bash
go test -run '^$' -bench '^BenchmarkHistoryStore(100K|1M)$' \
  -benchtime=1x -benchmem ./internal/logs
```

| Rows | Query | Time/op | Allocated bytes/op | Allocations/op |
|---:|---|---:|---:|---:|
| 100,000 | unfiltered | 1.109 ms | 342,088 | 7,561 |
| 100,000 | exact source + level | 1.376 ms | 345,472 | 7,596 |
| 100,000 | literal `needle` | 42.404 ms | 212,520 | 3,795 |
| 1,000,000 | unfiltered | 0.957 ms | 341,976 | 7,559 |
| 1,000,000 | exact source + level | 1.395 ms | 345,472 | 7,596 |
| 1,000,000 | literal `needle` | 85.732 ms | 342,216 | 7,564 |

The captured plans selected the same indexes described above. A single
iteration is a release-gate smoke measurement, not a statistically stable
comparison; it identified no plan change and does not replace the five-iteration
development baseline.

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
