# Historical query baseline

**Measured:** 2026-07-28 (SFT-020 baseline); 2026-11-05 (SFT-095 bounds)  
**Scope:** development measurements; not a hardware-independent guarantee

## SFT-095: cursor and hierarchy bounds (2026-11-05)

Audit DAT-06 found that a History cursor was only an `OR` predicate, so page N
scanned every index row newer than the cursor, and that Server-only and
partial-hierarchy filters sorted every matching row in the range in a
temporary B-tree before `LIMIT`. SFT-095 folds the cursor's timestamp into the
range bound, resolves hierarchy filters to `source_id` (`=` for a complete
identity, `+source_id IN (…)` against `log_events_time_idx` otherwise), keeps
several levels a filter, and replaces the request-ID index with one in History
order (migration 0008). `ARCHITECTURE.md` §16.2–16.2.1 describes the plans and
`TestHistoryQueryPlansReadIndexOrder` asserts index use and no `TEMP B-TREE`
for each filter shape with and without a cursor.

### Method and environment

- Host: Fedora Linux, kernel 7.2.10, x86-64; Intel Core i5-7500, four cores,
  3.40 GHz nominal; 31 GiB RAM; no container limits.
- Storage: `TMPDIR` on btrfs on a SATA SSD (Crucial BX500), not tmpfs.
- Toolchain: Go 1.27.3 linux/amd64 with CGO; SQLite 3.53.4 bundled by
  `github.com/mattn/go-sqlite3` v1.14.52; production migrations and pragmas
  through `database.Open`.
- Fixture (rewritten for SFT-095): 100,000 or 1,000,000 events one millisecond
  apart (the sustained 1,000 events/second target), round-robin across four
  sources on two Servers. Messages are 80–399 bytes of access-log-like text;
  one in ten events is `error`, one in ten `warn`, one in three `stderr`, and
  one in 1,000 contains `needle`. Setup runs in one transaction outside timing.
- Cases: `unfiltered`; `source_level` (complete identity + `error`); `literal`
  (`needle`); `server_only` (one Server: two of four sources);
  `server_two_levels` (Server + `warn,error`); and `deep_unfiltered`,
  `deep_source`, `deep_server_only`, which continue from a cursor 90% of the
  way to the oldest event. Every case reads one 200-row page through
  `Store.History`.
- "Before" ran the same benchmark file against the query builder at commit
  `151add8` (the parent of this change); "after" is this change. Both on
  schema 8.

```bash
mkdir -p .bench-tmp
TMPDIR=$PWD/.bench-tmp go test -run '^$' \
  -bench '^BenchmarkHistoryStore(100K|1M)$' \
  -benchtime=5x -count=1 -benchmem ./internal/logs
rm -rf .bench-tmp
```

### Results

Five iterations each; allocations were unchanged within 1%
(≈392–399 kB and ≈7,680–7,764 allocations per page, 236–386 kB for the
literal case).

| Rows | Query | Before | After |
|---:|---|---:|---:|
| 100,000 | unfiltered | 1.20 ms | 5.34 ms¹ |
| 100,000 | source + level | 2.52 ms | 5.67 ms¹ |
| 100,000 | literal `needle` | 2,208 ms² | 139 ms |
| 100,000 | Server only | 73.0 ms | 1.27 ms |
| 100,000 | Server + two levels | 63.3 ms | 2.49 ms |
| 100,000 | deep cursor, unfiltered | 9.36 ms | 1.27 ms |
| 100,000 | deep cursor, source | 3.03 ms | 1.30 ms |
| 100,000 | deep cursor, Server only | 13.0 ms | 1.19 ms |
| 1,000,000 | unfiltered | 4.81 ms¹ | 1.09 ms |
| 1,000,000 | source + level | 9.78 ms¹ | 1.17 ms |
| 1,000,000 | literal `needle` | 623 ms | 248 ms |
| 1,000,000 | Server only | 1,232 ms | 1.24 ms |
| 1,000,000 | Server + two levels | 1,014 ms | 2.47 ms |
| 1,000,000 | deep cursor, unfiltered | 77.8 ms | 1.07 ms |
| 1,000,000 | deep cursor, source | 21.0 ms | 1.12 ms |
| 1,000,000 | deep cursor, Server only | 110 ms | 1.29 ms |

¹ The first cases after seeding run against a cold page cache; with five
iterations the first one dominates. Their plans did not change, and the other
fixture size shows the warm figure.  
² Same plan before and after (time index, residual `instr`); the difference is
page-cache and host noise, not this change. Literal search remains bounded by
the time range, as before.

Server-only and partial-hierarchy pages went from a full-range sort (1.2 s at 1M rows, growing with
the range) to about one page of index reads, and a deep cursor now costs the
same as the first page at both sizes.

Plans captured at both sizes with `-benchtime=1x -v` read events through
`log_events_time_idx` (unfiltered, literal, Server only, Server + two levels,
deep unfiltered, deep Server only), `log_events_source_level_time_idx`
(source + level), and `log_events_source_time_idx` (deep source), each with the
range `event_at_us>? AND event_at_us<?` as the index bound and no temporary
B-tree.

A time-index plan costs time proportional to the rows skipped before a page
fills. In this fixture a Server is half the traffic; a Server or partial
hierarchy that is a small fraction of a busy instance skips more rows, bounded
by the 31-day range, like literal search. The 10-million-event review remains
the pre-public release gate below.

## SFT-020 baseline (2026-07-28)

The sections below record the original fixture (two sources, 14-byte
messages, events one microsecond apart, no cursor) and are kept as history.

### Method and environment

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

### Results

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

### 0.2.0 gate rerun

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
