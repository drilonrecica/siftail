# ADR 0004: Canonical Events, Sources, and Deduplication

**Status:** Accepted  
**Date:** 2026-07-27

## Context

Coolify and generic Fluent Bit inputs carry inconsistent envelopes and ephemeral
container metadata. Historical logs must retain stable meaning across container
replacement, preserve original payload evidence, and avoid unsafe identity inference.
HTTP retries may also resend events when a producer supplies a stable event ID.

## Decision

Normalize transport records into a typed canonical event before persistence. Trusted
Server identity comes only from the authenticated ingestion token. Stable source
identity is the exact bounded logical hierarchy under that Server and excludes
container instance; aliases are presentation only.

Persist immutable event and receive timestamps, order History by
`event_at_us DESC, id DESC`, and derive retention order from the earlier of event and
receive time. Preserve raw application content, independent stream and level values,
the original supplied level, and bounded unknown attributes.

When a stable source event ID is present, scope it to stable source. Repeating the ID
with identical canonical persisted content is a no-op. Reusing it with different
canonical content rejects the entire request with `409`. Do not deduplicate by message
hash or replace existing rows.

## Alternatives

- Container identity as source identity fragments history on every deployment.
- Payload-provided Server identity permits impersonation across configured servers.
- Fuzzy source merging can combine unrelated applications.
- Hash-based deduplication loses legitimate repeated messages.
- `INSERT OR REPLACE` mutates immutable history.

## Consequences

- Source normalization and source discovery participate in the same transaction as
  event insertion.
- Producer retries can be idempotent only when they provide a stable event ID.
- Conflicting ID reuse is explicit and may require producer correction.
- Source and container discovery require hard per-server quotas.
- Canonical comparison and raw-preservation behavior require table-driven and real
  SQLite tests.

## Migration and compatibility impact

The source key, canonical event fields, retention order, and deduplication comparison
set are schema and ingestion compatibility contracts. Changing any of them requires a
superseding ADR, forward migration where applicable, preserved-data tests, and release
notes.
