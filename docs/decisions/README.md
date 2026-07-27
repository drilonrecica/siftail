# Siftail Architecture Decision Records

Accepted ADRs preserve the reasoning for consequential, hard-to-reverse choices.
Authoritative behavior remains in `DOMAIN.md`, `PRODUCT.md`, `ARCHITECTURE.md`, and
`DESIGN.md`; an ADR explains why that behavior was selected.

## Index

1. [SQLite storage and driver](0001-sqlite-storage-and-driver.md)
2. [Atomic bounded ingestion and acknowledgement](0002-atomic-bounded-ingestion.md)
3. [Single-administrator authentication](0003-single-administrator-authentication.md)
4. [Canonical events, sources, and deduplication](0004-canonical-events-sources-and-deduplication.md)

Do not edit an accepted ADR to reverse its decision. Add a superseding ADR and update
every affected authoritative document, migration, and accepted test in the same change.
