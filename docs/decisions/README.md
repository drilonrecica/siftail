# Siftail Architecture Decision Records

Accepted ADRs preserve the reasoning for consequential, hard-to-reverse choices.
Authoritative behavior remains in `DOMAIN.md`, `PRODUCT.md`, `ARCHITECTURE.md`, and
`DESIGN.md`; an ADR explains why that behavior was selected.

## Index

1. [SQLite storage and driver](0001-sqlite-storage-and-driver.md)
2. [Atomic bounded ingestion and acknowledgement](0002-atomic-bounded-ingestion.md)
3. [Single-administrator authentication](0003-single-administrator-authentication.md)
4. [Canonical events, sources, and deduplication](0004-canonical-events-sources-and-deduplication.md)
5. [Operator-console visual redesign](0005-operator-console-visual-redesign.md)
6. [Ephemeral container identity](0006-ephemeral-container-identity.md)
7. [Deduplication with defaulted timestamps and conflict policy](0007-deduplication-and-defaulted-timestamps.md)
   (amends 0004)
8. [Retention basis and stale events](0008-retention-basis-and-stale-events.md)
   (amends 0004)
9. [Group commit, snapshot backups, and unhealthy-database restore](0009-group-commit-and-snapshot-backups.md)
   (amends 0001)
10. [Authentication, audit capacity, public URL, and secret amendments](0010-authentication-amendments.md)
    (amends 0003)

An amending ADR replaces only the parts it names; the amended ADR gains an
**Amended by** pointer and its remaining decisions stand.

Do not edit an accepted ADR to reverse its decision. Add a superseding ADR and update
every affected authoritative document, migration, and accepted test in the same change.
