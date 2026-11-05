# Source catalog baseline

**Measured:** 2026-07-28
**Scope:** SFT-029 development baseline; not a hardware-independent guarantee

## Real-disk rerun (SFT-100, 2026-11-05)

- Host: Fedora Linux, kernel 7.2.10, x86-64; Intel Core i5-7500, four cores,
  3.40 GHz nominal; 31 GiB RAM; no container limits.
- Storage: `TMPDIR` on **btrfs** (`/dev/sda3`) on a Crucial BX500 480 GB SATA
  SSD (`CT480BX500SSD1`, non-rotational). Real disk with `fsync`; not tmpfs.
- Toolchain: Go 1.27.3 linux/amd64 with CGO; `github.com/mattn/go-sqlite3`
  v1.14.52 (SQLite 3.53.4); production migrations and pragmas.
- Command: `make bench BENCH_GROUPS="sources"` (`scripts/bench.sh`, which refuses a
  tmpfs `TMPDIR` and prints the filesystem and device class).
- Benchmark: `BenchmarkSourceCatalog10000`, `-benchtime=3s -count=5`.

Median **382.1 µs/page** (range 379.0–398.1 µs), 111,510 bytes/op, 2,391
allocations/op, against 385.3 µs, 111,462 bytes, and 2,391 allocations on
2026-07-28 (storage unrecorded, presumed tmpfs). The fixture is read-only, so
the result is unchanged within noise.

## Method and environment (2026-07-28)

- Host: Fedora Linux 7.1.4-204.fc44.x86_64, linux/amd64.
- CPU: Intel Core i5-7500, four physical cores, 3.40 GHz nominal.
- Memory: 31 GiB host RAM; no container memory or CPU limit.
- Toolchain: Go 1.26.5 linux/amd64 with CGO. The repository compatibility floor
  remains the version in `go.mod`.
- SQLite: production migrations and connection pragmas.
- Fixture: the per-Server safety maximum of 10,000 stable sources on one
  trusted Server. Sources span 100 projects and 10,000 applications. Every
  tenth source has one container observation and one retained event so the
  catalog exercises its bounded retained-data and container-existence facts.
- Operation: read a 100-source keyset page through the production scanner.
  Successive benchmark operations rotate the `after` source ID through the
  fixture. Setup is outside benchmark timing.
- Measurement command:

  ```bash
  go test -run '^$' -bench '^BenchmarkSourceCatalog10000$' \
    -benchtime=3s -count=5 -benchmem ./internal/sources
  ```

## Results

Five development-machine samples measured a median of **385.318 µs/page**, a
range of 385.174–388.733 µs/page, 111,462 allocated bytes/op, and 2,391
allocations/op.

`EXPLAIN QUERY PLAN` is asserted in the real-SQLite store test. Pagination uses
the `sources` integer primary key, the trusted Server join uses the `servers`
integer primary key, retained-event presence/latest time uses
`log_events_source_time_idx`, and container existence uses the existing
covering unique index whose prefix is `source_id`.

No new index is justified. The list fetches `limit+1`, returns at most 200
sources, does not run a retained-event `COUNT(*)`, and does not load container
rows. Source detail separately returns at most the 200 most recently seen
container observations ordered by `last_seen_at_us DESC, id DESC`.

## Review and limitations

- The query returns inactive and empty sources; lifecycle state is derived from
  the fixed 24-hour active and 90-day cleanup-eligibility boundaries.
- Server identity comes only from the source foreign-key relationship. Aliases
  affect display only, and container identity is reported as an observation.
- The fixture contains synthetic metadata and no application payloads,
  credentials, telemetry, or outbound runtime calls.
- This is a SQLite/store microbenchmark. It excludes authentication, template
  rendering, HTTP/TCP, browser RSS, a production container, and concurrent
  ingestion. It is evidence for bounded large-catalog behavior, not a latency
  promise on other hardware.
