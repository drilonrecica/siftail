# ADR 0001: SQLite Storage and Driver

**Status:** Accepted  
**Date:** 2026-07-27

## Context

Siftail is a single-administrator appliance with bounded retention, one production
container, and no external runtime service. Storage must support transactional batch
ingestion, deterministic historical reads, online backup, forward migrations, and
predictable operation on small hosts.

## Decision

Use one local SQLite database through `mattn/go-sqlite3` with CGO. Run in WAL mode with
foreign keys enabled, `synchronous=FULL`, incremental auto-vacuum configured at
database creation, and one application-owned write coordinator. Use a small read pool
and SQLite's online backup API.

The active size-retention footprint is the main database plus WAL and SHM files.
Siftail remains SQLite-specific and uses handwritten SQL rather than a database
abstraction or ORM.

## Alternatives

- PostgreSQL, ClickHouse, Loki, and Elasticsearch add services and operational cost
  outside the product boundary.
- A pure-Go SQLite driver simplifies cross-compilation but is not the selected,
  production-proven driver contract for the initial implementation.
- `synchronous=NORMAL` reduces sync work but weakens the chosen durability boundary.
- Multiple independent writers increase contention and complicate maintenance ordering.

## Consequences

- Production images require explicit CGO builds for supported architectures.
- Write throughput is bounded by one SQLite writer, so batching and short transactions
  matter.
- Broad substring search has honest limits and must remain time-bounded.
- Online backup must use the SQLite API; copying only the live main file is invalid in
  WAL mode.
- Pragmas and indexes are product-significant and require tests and measurement.

## Migration and compatibility impact

The initial database must enable incremental auto-vacuum before ordinary tables are
populated. Every later schema change uses a numbered forward migration. Older binaries
refuse newer schemas. Changing the driver, journal mode, or durability setting requires
a superseding ADR and compatibility plan.
