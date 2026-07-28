# Security-audit storage boundary

**Reviewed:** 2026-07-28
**Scope:** SFT-038 storage and migration; privileged-action/UI wiring follows
in SFT-039

## Stored model

Migration `0004_security_audit.sql` adds one `STRICT`, immutable table with:

- occurrence time and internal ID;
- constrained category, action, outcome, and actor type;
- optional positive administrator, Server, and source identifiers;
- an optional bounded internal request ID; and
- a required JSON object containing only whitelisted safe metadata.

The optional entity identifiers intentionally have no foreign keys. Audit
history must neither block deletion of a source or Server nor be rewritten
when referenced operational metadata disappears. They are historical
attribution values, not live ownership relationships.

Updates are rejected by a database trigger. Cleanup may delete records, but no
supported operation rewrites an existing event. The chronological
`(occurred_at_us DESC, id DESC)` index serves newest-first reads and
oldest-first retention deletion with one index entry per low-volume audit
record.

## Metadata and secret boundary

The concrete store accepts at most 12 metadata fields, 256 UTF-8 bytes per
value, and 2 KiB of encoded JSON. Keys are a closed list for nonsecret names,
fingerprints, counts, configuration values, result categories, client
summaries, formats, and backup basenames. A backup name must be a safe basename,
not an internal filesystem path.

There is no accepted key for:

- passwords or password hashes;
- ingestion/session tokens or token/session hashes;
- authorization headers;
- raw request bodies;
- application messages, attributes, or raw payloads.

The store does not perform automatic secret detection or redaction. Callers
must construct metadata from explicit safe fields and must never copy an
arbitrary request, environment map, log record, error string, or application
query into audit metadata. Unknown keys, control characters, oversized fields,
invalid identifiers, and invalid enum values fail before a transaction writes
anything. Errors do not echo rejected values.

## Atomicity and capacity

`audit.RecordTx` inserts into a caller-owned coordinator transaction so a
successful privileged mutation and its success audit event commit or roll back
together. `Store.Record` handles outcomes that require their own coordinated
transaction, such as a rejected attempt. Wiring those calls into privileged
features is SFT-039 scope.

Every insertion applies the 100,000-record cap in its transaction, deleting the
oldest `(occurred_at_us, id)` record when required. Explicit cleanup applies
the configured age first, then count overflow, and deletes no more than 1,000
records per transaction. Default and maximum audit age are 365 days.
Application-log age/size cleanup, Clear logs, and application-event retention
do not delete audit rows.

## Migration and compatibility

Fresh creation and schema-1, schema-2, and schema-3 upgrade tests reach schema
4. Representative Server, administrator, source, and application-event data
survive; integrity checks and critical reads pass. Migration failure rolls back,
and binaries supporting only schema 3 refuse schema 4 rather than deleting or
recreating the database.

The migration is additive and does not rewrite existing rows. It adds bounded
future disk growth: at most 100,000 audit rows, each with at most 2 KiB of
metadata plus fixed columns and one chronological index. Actual SQLite
overhead varies with values, pages, WAL state, and filesystem allocation.

## Measured store cost

Measured on the repository's documented Fedora/i5 development host with
production SQLite pragmas and a 100,000-row fixture:

```bash
go test -run '^$' \
  -bench '^BenchmarkAudit(RecordAtCapacity|List100K)$' \
  -benchtime=3s -count=5 -benchmem ./internal/audit
```

- record and enforce the full-table cap: median 1.301 ms/op, 3,490–3,505
  allocated bytes/op, 74 allocations/op;
- newest 100-row page: median 325.800 µs/op, 113,821–113,822 bytes/op, 2,570
  allocations/op;
- worst-case category match at the oldest row: median 23.935 ms/op,
  18,682–18,686 bytes/op, 72 allocations/op.

The selective case deliberately demonstrates the bounded 100,000-row scan
without a speculative category index. Audit writes are low volume, and all
measurements remain far below ordinary interactive latency targets on this
host. These microbenchmarks exclude HTTP/template work and are not guarantees
for other storage or hardware.

## Current limitation

No existing sign-in, token, source, retention, backup, restore, or export
workflow claims an audit event from SFT-038 alone. The following tracked task
must wire defined privileged outcomes atomically and add the authenticated,
paginated Audit view before the product claims end-to-end audit coverage.
