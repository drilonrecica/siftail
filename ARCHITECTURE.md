# Siftail Architecture Specification

**Status:** Authoritative technical architecture  
**Product:** Siftail  
**Audience:** Maintainer, coding agents, reviewers, contributors

---

## 1. Purpose

This document defines how Siftail is implemented and operated. It translates the product scope and domain invariants into a concrete architecture.

It is authoritative for:

- runtime topology;
- technology choices;
- package organization;
- storage architecture;
- ingestion protocol;
- queueing and batching;
- HTTP listeners and routes;
- authentication and security controls;
- historical querying and live streaming;
- retention and maintenance;
- backups and recovery;
- configuration;
- observability of Siftail itself;
- deployment and release artifacts;
- testing, benchmarking, and release gates.

Do not alter a hard-to-reverse decision here without updating the corresponding documents and, when appropriate, creating an ADR under `docs/decisions/`.

---

## 2. Architectural goals

The architecture must optimize, in order, for:

1. predictable performance;
2. privacy;
3. minimal resource use;
4. durability;
5. operational simplicity;
6. maintainability by one primary maintainer;
7. a narrow public compatibility surface.

The system should be easy to describe:

> One long-running Go process receives logs on one listener, serves the authenticated
> UI on another listener, stores data in one SQLite database, and embeds its HTML,
> HTMX, JavaScript, and CSS assets into the binary. Focused, short-lived CLI commands
> run from the same binary.

---

## 3. Fixed technology decisions

| Concern | Decision |
|---|---|
| Backend language | Go |
| HTTP foundation | `net/http` with Chi |
| Database | SQLite |
| Go SQLite driver | `mattn/go-sqlite3` using CGO |
| Server rendering | Go `html/template` |
| Partial interactions | HTMX embedded locally |
| Live delivery | Native Server-Sent Events / `EventSource` |
| Client scripting | Small focused vanilla JavaScript modules |
| Styling | Plain CSS with semantic custom properties |
| Deployment | One Docker container |
| Runtime process | One long-running Go process; short-lived administrative CLI commands |
| Persistent state | One mounted `/data` directory |
| Production Node.js | None |
| TLS | Reverse proxy termination |
| Retry buffering | Fluent Bit filesystem buffering |
| Primary integration | Coolify custom Fluent Bit configuration |

Rejected foundational alternatives:

- Svelte/SvelteKit runtime;
- React;
- Gin, Fiber, or Echo;
- PostgreSQL;
- Redis;
- Elasticsearch/OpenSearch;
- ClickHouse;
- Loki;
- ORM;
- general plugin framework;
- microservices;
- separate worker process;
- frontend container.

---

## 4. Runtime topology

```text
                         Browser
                            │
                     HTTPS reverse proxy
                            │
                      UI listener :8080
                            │
┌────────────────────────────────────────────────────────┐
│                   One Siftail process                   │
│                                                        │
│  UI router      Auth/session      Templates/HTMX       │
│      │                 │                 │              │
│      ├──────────── Read stores ──────────┤              │
│      │                                   │              │
│  SSE broker ◄──── committed event publisher            │
│      ▲                                   ▲              │
│      │                                   │              │
│  Historical queries                Batch writer         │
│                                          ▲              │
│                                   bounded queue          │
│                                          ▲              │
│  Ingestion listener :8081 ─ decoder ─ normalizer        │
│                                                        │
│  Retention / session cleanup / WAL maintenance         │
│  Backup / diagnostics / health                         │
└──────────────────────────┬─────────────────────────────┘
                           │
                    SQLite in /data

Coolify applications
        │ stdout/stderr
Coolify Fluent Bit drain
        │ HTTP + gzip + JSON lines
        └──────────────────────────────► ingestion :8081
```

### 4.1 Listener separation

Default listeners:

- UI and browser endpoints: `:8080`
- ingestion API: `:8081`

Both run in one process.

Reasons:

- distinct authentication models;
- distinct request-size limits;
- ingestion can remain private while UI is proxied publicly;
- accidental browser middleware cannot affect ingestion;
- operator can route or firewall them separately;
- simpler incident diagnosis.

The listeners must not share mutable router state that makes shutdown or middleware policy ambiguous.

### 4.2 Persistent layout

Recommended `/data` layout:

```text
/data/
├── siftail.db
├── siftail.db-wal
├── siftail.db-shm
├── siftail-control.sock  # owner-only; exists only while the server is active
├── restore-staging/       # created only during controlled restore
└── backups/               # optional operator-mounted destination, not required
```

Siftail owns its database files. It does not own Fluent Bit's source-side buffer directory.

---

## 5. Repository layout

Recommended repository:

```text
cmd/
  siftail/
    main.go

internal/
  app/
  audit/
  auth/
  backup/
  config/
  database/
  diagnostics/
  ingest/
  logs/
  retention/
  sessions/
  sources/
  status/
  web/

migrations/
templates/
static/
docs/
  decisions/
testdata/
scripts/

AGENTS.md
ARCHITECTURE.md
DESIGN.md
DOMAIN.md
PRODUCT.md
README.md
SECURITY.md
CONTRIBUTING.md
CHANGELOG.md
LICENSE
```

### 5.1 Package philosophy

Use feature-oriented packages with a small shared infrastructure core.

Each feature may own:

- domain-adjacent types;
- validation;
- concrete SQLite store;
- service logic;
- handlers;
- templates or template models where practical;
- tests.

Avoid generic packages named:

- `utils`;
- `common`;
- `helpers`;
- `shared`;
- `repositories`;
- `services` containing unrelated functionality.

### 5.2 Root application

`internal/app` or `cmd/siftail` owns composition only:

- parse and validate startup configuration;
- initialize process logger;
- open database;
- run migrations and integrity checks;
- construct stores and services;
- start long-running components;
- coordinate cancellation;
- enforce shutdown ordering;
- return an exit status.

It must not contain log-query SQL, normalization logic, or HTML handlers.

---

## 6. Component model

### 6.1 Critical components

Unexpected termination of a critical component terminates the process through controlled cancellation:

- ingestion HTTP server;
- UI HTTP server;
- batch writer;
- database lifecycle manager;
- application root.

### 6.2 Recoverable periodic components

These may fail one run, emit a sanitized diagnostic, and retry with bounded backoff:

- retention worker;
- session cleanup worker;
- audit cleanup worker;
- incremental vacuum worker;
- status aggregation worker where one exists.

### 6.3 Lifecycle interface

Long-running components should expose behavior equivalent to:

```go
type Component interface {
    Run(ctx context.Context) error
}
```

Do not require every component to implement a formal interface if direct functions are clearer. The invariant is explicit ownership and cancellation, not interface ceremony.

### 6.4 Error propagation

Use `errgroup` or equivalent structured concurrency from the application root.

Rules:

- first critical error cancels the application context;
- shutdown is coordinated once;
- recoverable workers handle their own retry loop;
- panics in HTTP handlers are recovered at the HTTP boundary;
- panics in the writer or database lifecycle are process-fatal;
- shutdown errors are aggregated without hiding the initiating failure.

---

## 7. Startup sequence

Canonical startup:

```text
1. Parse environment and CLI mode.
2. Validate all process-level configuration.
3. Initialize safe internal logging.
4. Ensure data directory exists and is writable.
5. Open SQLite with controlled connection settings.
6. Verify schema version is not newer than binary.
7. Apply ordered automatic migrations transactionally.
8. Run PRAGMA quick_check.
9. Initialize administrator/bootstrap state checks.
10. Initialize stores, caches, broker, and writer queue.
11. Start writer and background workers.
12. Start UI and ingestion listeners.
13. Mark readiness healthy.
```

Failure rules:

- invalid critical environment: fail before listeners start;
- schema newer than binary: fail safely with exact supported/actual versions;
- migration failure: do not recreate database;
- quick check failure: refuse ingestion and ordinary startup; allow explicit recovery commands;
- missing administrator: UI may expose a locked informational page, but not an unauthenticated setup workflow; administrator must be created through CLI.

---

## 8. Graceful shutdown

Canonical SIGTERM/SIGINT behavior:

```text
1. Mark readiness unhealthy.
2. Stop accepting new ingestion requests.
3. Stop accepting new UI connections.
4. Allow in-flight decoders to finish within timeout.
5. Drain accepted queued batches through the writer.
6. Publish committed batches already accepted.
7. Send shutdown control event to SSE clients and close streams.
8. Stop periodic workers.
9. Perform a bounded WAL checkpoint when safe.
10. Close database connections.
11. Exit.
```

Default shutdown timeout: 30 seconds, configurable.

Siftail must not wait indefinitely. If the timeout expires, it exits with a clear process log. Fluent Bit remains responsible for retrying requests that did not receive success.

A request is considered accepted only after commit. An in-flight request without success may be retried.

---

## 9. Configuration architecture

### 9.1 Configuration ownership

Process/infrastructure configuration: environment variables.

Runtime operational configuration: SQLite-backed settings managed through authenticated UI or focused CLI.

No configuration file in version one.

### 9.2 Environment variables

Initial set:

```env
SIFTAIL_DATA_DIR=/data
SIFTAIL_UI_ADDR=:8080
SIFTAIL_INGEST_ADDR=:8081
SIFTAIL_PUBLIC_URL=https://logs.example.com
SIFTAIL_INGEST_PUBLIC_URL=https://ingest.logs.example.com/api/v1/ingest
SIFTAIL_LOG_LEVEL=info
SIFTAIL_LOG_FORMAT=text
SIFTAIL_SHUTDOWN_TIMEOUT=30s
SIFTAIL_TRUSTED_PROXY_CIDRS=
```

Additional limit variables may be supported only when process-level tuning is justified:

```env
SIFTAIL_MAX_COMPRESSED_REQUEST_BYTES=5242880
SIFTAIL_MAX_DECOMPRESSED_REQUEST_BYTES=26214400
SIFTAIL_MAX_EVENTS_PER_REQUEST=10000
SIFTAIL_MAX_EVENT_BYTES=1048576
SIFTAIL_QUEUE_MAX_EVENTS=20000
SIFTAIL_QUEUE_MAX_BYTES=33554432
SIFTAIL_INGEST_RESIDENT_MAX_EVENTS=30000
SIFTAIL_INGEST_RESIDENT_MAX_BYTES=67108864
SIFTAIL_INGEST_MAX_DECODERS=4
```

Defaults must be documented and visible in sanitized effective configuration.

### 9.3 `_FILE` secrets

Secret environment variables may support `_FILE` variants.

Rules:

- direct and `_FILE` form cannot both be set;
- file is read at startup;
- trailing newline handling is documented;
- file content is never logged;
- permissions are operator responsibility but insecure conditions may warn;
- secret is kept in memory only as required.

### 9.4 Runtime settings

SQLite-backed settings include:

- log retention days;
- maximum database size;
- audit retention days;
- source aliases;
- theme default where server-level default is needed;
- token metadata and lifecycle;
- export limits where operationally adjustable.

Application-log retention uses one `application_retention` settings row with a
strict JSON object containing age days and maximum database bytes. The browser
accepts whole age values from 1 through 3,650 days and whole database values
from 1 through 1,024 GiB. Saves validate before entering the coordinator and
upsert both fields in one short transaction. An absent row resolves to 14 days
and 4 GiB without writing during a read; malformed or out-of-range stored data
fails explicitly instead of silently restoring defaults.

Browser-personal preferences such as theme and density should remain browser-local unless there is a clear cross-device requirement.

### 9.5 Validation

Fail startup for:

- malformed bind address;
- listener port collision;
- unwritable data directory;
- invalid public UI or ingestion URL;
- malformed trusted proxy CIDR;
- nonpositive limits;
- queue byte limit smaller than max event size;
- both secret and `_FILE` form set.

Unknown variables using the reserved `SIFTAIL_` prefix fail validation. Unknown
unrelated environment variables are ignored.

---

## 10. SQLite architecture

### 10.1 Driver

Use `mattn/go-sqlite3` with CGO.

Consequences:

- build multi-architecture images explicitly;
- use a controlled glibc-based or otherwise compatible runtime image;
- do not claim a pure static binary unless verified;
- container is primary artifact;
- native binaries may come later with platform-specific build testing.

### 10.2 Connection model

Use:

- one write coordinator for ingestion, retention, purges, settings, authentication,
  audit, session cleanup, and other mutations;
- one dedicated writer connection or a `*sql.DB` constrained to one writer connection;
- a read connection pool capped at four connections by default;
- separate backup connection as required by online backup API;
- short-lived migration connection during startup where appropriate.

SQLite remains authoritative. In-memory caches are reconstructable.

### 10.3 Pragmas

Initial required settings, validated through tests and benchmarks:

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA temp_store = MEMORY;
PRAGMA auto_vacuum = INCREMENTAL;
```

Important nuance: incremental auto-vacuum must be configured when the database is created, or enabling it later requires a full `VACUUM`. The initial migration/database creation must set it correctly before ordinary tables are populated.

Other settings such as cache size, mmap size, page size, and WAL autocheckpoint must be benchmark-driven, not copied blindly.

### 10.4 Schema ownership

The `database` package owns:

- connection opening;
- pragmas;
- schema version table;
- migrations;
- transaction helpers;
- quick/full integrity checks;
- online backup plumbing;
- WAL checkpoint operations;
- database size reporting;
- schema compatibility checks.

Feature packages own their SQL through concrete store types.

### 10.5 Migrations

Migrations are:

- ordered;
- embedded in the binary;
- automatic at startup;
- transactional when SQLite permits;
- forward-only in normal operation;
- recorded in schema metadata;
- tested from every released historical schema fixture.

Startup refuses a schema newer than the binary supports.

No automatic down-migrations.

Before a risky migration, the release notes must state whether a verified backup is strongly recommended. The application may perform a lightweight safety backup for selected migrations only if designed explicitly; it must not silently consume excessive disk.

### 10.6 Schema outline

A likely normalized schema includes:

- `schema_migrations`;
- `settings`;
- `administrators`;
- `sessions`;
- `servers`;
- `ingestion_tokens`;
- `sources`;
- `container_instances`;
- `log_events`;
- `security_audit_events`;
- `diagnostic_events` if persisted.

`sources` stores the complete stable logical hierarchy as bounded, typed columns. This
avoids speculative hierarchy joins while preserving exact identity and foreign-key
integrity. Deployment boundaries are not stored in version one.

Migration `0004` adds `security_audit_events` as an immutable `STRICT` table.
Category, outcome, actor type, action, occurrence time, optional safe entity
identifiers, request ID, and a bounded JSON metadata object are explicit
fields. Entity identifiers deliberately are not foreign keys: deleting a
source or other referenced object must not mutate or block its historical
audit attribution. The feature store accepts only whitelisted metadata keys,
at most 12 fields, 256 bytes per value, and 2 KiB total encoded JSON.

One `(occurred_at_us DESC, id DESC)` index supports newest-first pages and the
reverse oldest-first cleanup scan. Occurrence time is selective for ordinary
pages, and the index has one low-volume entry per audit event. It adds
write/storage cost but shortens both primary reads and retention deletion;
application-event retention never uses or deletes it. Category and outcome do
not receive speculative indexes: the table is hard-capped at 100,000 rows, and
the measured worst-case selective scan remains bounded. Add an index only if
the implemented audit UI benchmarks justify its cost.

### 10.7 Log-event schema sketch

Illustrative, not a substitute for migrations:

```sql
CREATE TABLE log_events (
    id                    INTEGER PRIMARY KEY,
    event_at_us           INTEGER NOT NULL,
    received_at_us        INTEGER NOT NULL,
    retention_at_us       INTEGER GENERATED ALWAYS AS
                              (min(event_at_us, received_at_us)) STORED,
    source_id             INTEGER NOT NULL,
    container_instance_id INTEGER,
    stream                TEXT NOT NULL,
    level_normalized      TEXT NOT NULL,
    level_original        TEXT,
    message_raw           BLOB NOT NULL,
    message_text          TEXT NOT NULL,
    attributes_json       TEXT,
    source_event_id       TEXT,
    logger                TEXT,
    request_id            TEXT,
    error_type            TEXT,
    http_method           TEXT,
    http_path             TEXT,
    http_status           INTEGER,
    duration_ms           REAL,
    FOREIGN KEY(source_id) REFERENCES sources(id),
    FOREIGN KEY(container_instance_id) REFERENCES container_instances(id)
);
```

Potential uniqueness:

```sql
CREATE UNIQUE INDEX log_events_source_event_id_uq
ON log_events(source_id, source_event_id)
WHERE source_event_id IS NOT NULL;
```

Core indexes should cover:

```sql
CREATE INDEX log_events_time_idx
ON log_events(event_at_us DESC, id DESC);

CREATE INDEX log_events_retention_idx
ON log_events(retention_at_us, id);

CREATE INDEX log_events_source_time_idx
ON log_events(source_id, event_at_us DESC, id DESC);

CREATE INDEX log_events_source_level_time_idx
ON log_events(source_id, level_normalized, event_at_us DESC, id DESC);

CREATE INDEX log_events_request_id_idx
ON log_events(request_id)
WHERE request_id IS NOT NULL;

CREATE INDEX log_events_container_time_idx
ON log_events(container_instance_id, event_at_us DESC, id DESC)
WHERE container_instance_id IS NOT NULL;
```

These indexes serve primary history order, source filtering, retention deletion, level
filtering, exact request-ID lookup, and opt-in container filtering. The container index
is intentionally limited to a secondary workflow. Benchmarks at 1M and 10M rows must
confirm that each retained index earns its write, storage, and retention-deletion cost.
Avoid indexing every normalized attribute automatically.

### 10.8 Handwritten SQL

Use handwritten SQL and small typed scanners.

Do not use:

- ORM;
- dynamic active-record model;
- generic repository framework;
- hidden N+1 query behavior;
- database-agnostic query abstraction.

Dynamic historical filters should be assembled through a safe internal query builder or explicit conditional SQL construction with bound parameters. Do not concatenate user values into SQL.

---

## 11. Ingestion API

### 11.1 Endpoint

Canonical endpoint:

```http
POST /api/v1/ingest
Authorization: Bearer <server-token>
Content-Type: application/x-ndjson
Content-Encoding: gzip   # optional but recommended
```

Accepted request shapes are exactly:

- `application/x-ndjson`: one JSON object per non-empty line;
- `application/json`: one JSON object or one array of JSON objects.

Only absent or `gzip` content encoding is accepted. Empty batches, trailing
non-whitespace data, invalid UTF-8, duplicate JSON object keys at any nesting level,
non-object records, and unsupported timestamp forms reject the entire request.
Documented Fluent Bit timestamps and tag fields are normalized within those shapes.
The endpoint contract must be documented with examples.

### 11.2 Authentication order

Recommended order:

1. apply connection and header limits;
2. parse authorization header;
3. authenticate token using constant-time-safe hash verification semantics;
4. establish trusted server identity;
5. enforce compressed body limit;
6. decompress within strict limit;
7. decode and normalize.

Do not perform expensive decompression of an unauthenticated request when avoidable.

### 11.3 Request limits

Initial defaults:

| Limit | Default |
|---|---:|
| Compressed request | 5 MiB |
| Decompressed request | 25 MiB |
| Events per request | 10,000 |
| Single event payload | 1 MiB |
| JSON nesting depth | 32 |
| Canonical attributes | 256 KiB per event |
| Queue events | 20,000 |
| Queue retained bytes | 32 MiB |
| Decode plus queue events | 30,000 |
| Decode plus queue retained bytes | 64 MiB |
| Concurrent decoders | 4 |

Limits must be applied in the application even when the reverse proxy also limits requests.

### 11.4 Decompression safety

Use a bounded reader around gzip decompression.

Requirements:

- reject decompressed content beyond limit;
- avoid reading entire huge payload before detecting limit;
- limit compression ratio indirectly through compressed/decompressed caps;
- close readers promptly;
- report safe error without payload logging.

### 11.5 Decoder pipeline

```text
HTTP body
→ bounded raw reader
→ optional gzip reader
→ NDJSON / JSON transport decoder
→ ReceivedRecord stream
→ per-record validation
→ canonical normalization
→ complete in-memory WriteBatch
```

Version one stream-decodes the entire request into a bounded batch structure and does
not queue partial requests. A decoder acquires aggregate resident event and byte
capacity as records are retained. On successful queue admission, ownership of that
accounting transfers to the queue without double counting; rejection or parse failure
releases it exactly once.

### 11.6 Request atomicity

Before queueing:

- all records must decode;
- all records must normalize;
- batch limits must pass;
- approximate retained-byte accounting must pass;
- source identity inputs must be valid.

If record 9,999 fails, none of the request is queued.

### 11.7 Response semantics

| Status | Meaning | Retry guidance |
|---|---|---|
| `204` | Entire batch committed | No retry |
| `400` | Malformed or invalid input | Do not retry unchanged |
| `401` | Invalid or missing token | Fix credentials |
| `403` | Authenticated token violates source policy | Fix configuration |
| `409` | Stable source event ID reused with different canonical content | Fix producer identity |
| `413` | Request/event too large | Reduce batch or event |
| `415` | Unsupported format/encoding | Fix format |
| `429` | Temporary rate limit | Retry with backoff |
| `503` | Queue/database/shutdown temporarily unavailable | Retry |
| `507` | Storage unavailable/full | Retry cautiously and alert |

Every response includes `X-Request-ID`.

Do not return raw database errors or stack traces.

### 11.8 Successful response

Default success is `204 No Content` to minimize response overhead.

A diagnostic test endpoint or setup test may return structured metadata, but ordinary production ingestion should remain compact.

---

## 12. Canonical normalization architecture

### 12.1 Type boundaries

Illustrative types:

```go
type ReceivedRecord struct {
    Timestamp any
    Fields    map[string]any
    Raw       []byte
    Tag       string
}

type CanonicalEvent struct {
    EventAtUS      int64
    ReceivedAtUS   int64
    Source         SourceIdentity
    Container      *ContainerIdentity
    Stream         LogStream
    Level          NormalizedLevel
    OriginalLevel  string
    MessageRaw     []byte
    MessageText    string
    AttributesJSON []byte
    SourceEventID  string
    CommonFields   CommonFields
}
```

Use concrete types and validation constructors rather than passing `map[string]any` beyond the decoder/normalizer boundary.

### 12.2 Parser architecture

Do not implement a runtime plugin system.

Use compile-time decoders/normalizers for:

- canonical NDJSON;
- Fluent Bit HTTP JSON lines;
- Fluent Bit JSON array or documented HTTP format;
- plain message fields;
- nested JSON application payloads where documented.

### 12.3 Multiline

Fluent Bit must assemble multiline stack traces before submission.

Siftail treats each received record as atomic.

Do not heuristically combine lines across events in version one. This avoids merging unrelated concurrent exceptions.

### 12.4 Raw payload policy

The architecture must define exactly which bytes are considered the application payload and avoid storing the full Fluent Bit envelope redundantly.

Recommended:

- preserve exact text from recognized `log`/`message` payload;
- if payload itself is JSON, preserve exact nested JSON bytes when decoder makes them available;
- preserve normalized leftover attributes as JSON;
- store transport envelope only where necessary for debugging compatibility.

---

## 13. In-memory ingestion queue

### 13.1 Write batch

Illustrative type:

```go
type WriteBatch struct {
    Events        []CanonicalEvent
    ApproxBytes   int64
    Result        chan error
    RequestID     string
    AuthenticatedServerID int64
}
```

The result channel must be buffered or otherwise used safely so writer completion cannot deadlock after request cancellation.

### 13.2 Capacity

Queue capacity is the queued subset of the process-wide ingestion resident budget and
is enforced by both:

- total queued event count;
- total approximate retained bytes.

Whichever threshold is reached first rejects the next complete request with `503`.

Decode and queue capacity share accounting for at most 30,000 resident events and
64 MiB of retained bytes. At most four requests decode concurrently. Accounting must
be atomic, transfer ownership explicitly, and be released exactly once.

### 13.3 No unbounded goroutines

HTTP handlers must not spawn an unbounded goroutine per event or per delayed write.

One request may wait synchronously for its batch result under request context and server timeout.

### 13.4 Cancellation semantics

Once a batch is queued, request cancellation does not necessarily cancel persistence. The system must choose one safe rule.

Recommended:

- queued batch remains eligible for commit;
- handler may stop waiting if client disconnects;
- writer commits or fails normally;
- ambiguous client outcome is compatible with at-least-once retry;
- result delivery never blocks writer.

Do not remove arbitrary queued batches based on a disconnected client if doing so complicates queue consistency.

---

## 14. Batch writer

### 14.1 Single writer

One write coordinator serializes application-event transactions and all other SQLite
mutations. Retention and administrative writes may be scheduled between ingestion
batches but cannot open an independent competing writer path.

Benefits:

- predictable SQLite write behavior;
- easy batching;
- clear queue accounting;
- easier shutdown drain;
- source upsert aggregation;
- simple commit publication.

### 14.2 Transaction flow

For each `WriteBatch`:

```text
1. Begin transaction.
2. Resolve distinct stable sources using cache and upserts.
3. Resolve container instances.
4. Check source-event-ID idempotency.
5. Insert events using prepared statements.
6. Update source/container last_seen in aggregate.
7. Commit.
8. Release queue accounting.
9. Notify waiting request.
10. Publish newly committed events to the live broker in ID order.
```

No live publication before successful commit.

### 14.3 Batching across HTTP requests

Initial implementation may commit one HTTP request per transaction for clear atomicity.

A later optimization may group multiple complete HTTP batches into one SQLite transaction while preserving per-request all-or-nothing and completion results. This is optional and must be benchmark-driven.

Never merge requests in a way that makes one invalid request roll back unrelated already-validated requests without clear handling.

### 14.4 Prepared statements

Prepare hot insert/upsert statements at writer initialization or per connection according to driver behavior.

Check every error.

### 14.5 Deduplication conflicts

Use the partial unique index for non-null `source_event_id`. When the existing event's
canonical persisted content is identical, the record is an idempotent no-op and is not
republished. When the same ID has different canonical content, reject the entire
request with `409` and roll back every change made for it. Do not use `INSERT OR
REPLACE`, because replacement mutates historical identity and can delete/reinsert rows.

---

## 15. Source resolution and cache

### 15.1 Batch-level resolution

For each batch:

1. collect distinct source identities;
2. check bounded in-memory cache;
3. query/upsert misses;
4. update cache after transaction-safe resolution;
5. insert events using internal IDs;
6. update `last_seen` once per source.

### 15.2 Cache rules

- cache is bounded;
- cache may use LRU or another simple strategy;
- SQLite is authoritative;
- cache entries can be rebuilt;
- no correctness depends on cache survival;
- stale display aliases are not cached in the ingestion path unless safely invalidated;
- cache key uses stable normalized identity, never mutable display name.

### 15.3 Discovery bounds

Enforce at most 10,000 stable sources and 100,000 container instances per server.
Creating records beyond either limit rejects the complete ingestion request. Inactive
container-instance metadata may be removed only when no retained event references it.

---

## 16. Historical query architecture

### 16.1 Query requirements

Every query includes:

- half-open time range `[from, to)` of at most 31 days;
- source scope or explicit all-source scope;
- page limit;
- ordering direction;
- optional level/stream/message/common-field filters;
- optional exact container-instance filter.

Relative presets resolve to absolute microsecond endpoints when a query starts. Page
links retain those endpoints so pagination never drifts with wall-clock time.

### 16.2 Cursor pagination

Use a versioned, authenticated cursor containing the query fingerprint and keyset over:

```text
(event_at_us, id)
```

Descending older-page predicate:

```sql
WHERE
  event_at_us < :cursor_event_at
  OR (event_at_us = :cursor_event_at AND id < :cursor_id)
ORDER BY event_at_us DESC, id DESC
LIMIT :limit
```

Reject a cursor when its version or fingerprint does not match the current query. Do
not use offset pagination for deep history.

### 16.3 Search

Version one uses literal substring search constrained by time and preferably source.
Only ASCII letters are case-folded. Valid non-ASCII UTF-8 is compared byte-for-byte,
and `%`, `_`, and backslash have no wildcard or escape meaning.

Do not add FTS5 until measured need.

If FTS5 is later added, it requires an ADR addressing:

- storage overhead;
- write amplification;
- retention synchronization;
- migration time;
- tokenizer behavior;
- backup implications.

### 16.4 Dynamic filters

Use bound parameters.

A small internal SQL-clause builder is acceptable if:

- it accepts only known columns/operators;
- values are always bound;
- order is deterministic;
- it is feature-specific, not a general query language.

### 16.5 Query timeouts

Apply request contexts and reasonable server timeouts. Broad expensive queries should fail clearly rather than monopolize the database indefinitely.

### 16.6 Exports

Export runs a streaming cursor query independent of the visible page. One export may
run at a time. It writes a private staging file under `/data`, verifies final row and
byte limits, then serves the completed artifact. Cancellation removes the staging
file, so clients never receive an undocumented partial export.

Requirements:

- enforce max rows and/or bytes;
- stream encoder output to the bounded staging artifact;
- respond with text or NDJSON;
- do not hold all results in memory;
- audit the action;
- cancel on client disconnect;
- preserve event order.

Initial export safety defaults:

- maximum 100,000 events;
- maximum 256 MiB streamed response;
- whichever limit is reached first;
- explicit refusal rather than silent partial output.

---

## 17. Live SSE architecture

### 17.1 Broker

Use one in-process broker with filtered subscribers.

The broker receives committed batches from the writer.

Subscriber structure:

```go
type Subscriber struct {
    ID            string
    Filter        LiveFilter
    Events        chan LiveMessage
    LastEventID   int64
    Truncated     atomic.Bool
}
```

### 17.2 Bounded subscribers

Each subscriber has a bounded queue of at most 256 messages and 2 MiB of encoded
payload. The process accepts at most 16 concurrent live subscriptions.

The broker must not block when a subscriber is slow.

The broker command ingress is bounded to 256 commands and the queued publication
subset is bounded to 10,000 events and 16 MiB of retained canonical payload.
Whichever publication bound is reached first rejects that Live publication
without affecting its already committed database transaction. Because a dropped
broker publication would otherwise create a silent gap, the broker marks all
current subscribers truncated and closes them. Filter value lists are bounded to
256 entries before they enter broker-owned state.

When full:

1. mark subscriber truncated;
2. attempt to send a control message if possible;
3. close the subscription or require client resynchronization;
4. never block ingestion or writer publication.

### 17.3 SSE endpoint

Example:

```http
GET /logs/live/stream?source=42&level=error&level=fatal
Accept: text/event-stream
```

SSE events:

```text
event: log
id: 12345
data: <JSON or pre-rendered safe fragment>

event: control
data: {"type":"truncated"}

event: control
data: {"type":"source_purged"}
```

The payload is compact JSON interpreted by the focused client module. Message and
attribute previews are capped at 8 KiB per event; the browser fetches complete event
details through the authenticated event-detail route.

The endpoint accepts only `source`, `level`, `stream`, and `contains` query
parameters. Source, level, and stream are repeatable exact filters; `contains`
occurs at most once, is at most 512 UTF-8 bytes, and follows the same literal
ASCII-case-insensitive/non-ASCII-exact semantics as History. Unknown or invalid
parameters reject the connection. The initial stream sets a three-second native
reconnect delay.

Transport-owned control types are:

- `heartbeat` every 15 seconds;
- `possible_gap` when a valid `Last-Event-ID` identifies a reconnect that
  version one cannot replay;
- `truncated` for subscriber or broker overflow;
- `source_purged` and `source_removed` for source-scoped lifecycle changes;
- `session_invalid` or `unavailable` when periodic session validation fails;
- `shutdown` when the application-owned broker stops.

Every frame flush uses a five-second write deadline. Proxy response buffering
and response compression are disabled for the stream.

### 17.4 Reconnection

Native `EventSource` reconnects automatically. Version one does not replay
`Last-Event-ID`; reconnection starts from newly committed events and always displays a
possible-gap notice with a link to an absolute History range.

An open stream revalidates its server-side session every five seconds, so expiry
or online revocation closes the connection without waiting for browser activity.

Purge and source-removal control events are also delivered through a lightweight
authenticated History control stream so an open historical view cannot silently show
deleted rows.

### 17.5 Security

SSE requires a normal authenticated browser session, exact same-origin
`Origin` or `Referer` validation, and `Accept: text/event-stream`. Chromium may
omit `Origin` on a same-origin EventSource GET, so the validated same-origin
`Referer` path remains required rather than weakening the check. CSRF is not
required for the read-only GET stream. Responses are `no-store` and carry the
ordinary browser security headers.

---

## 18. HTTP UI architecture

### 18.1 Server rendering

Use `html/template` with embedded templates.

Rules:

- no unsafe raw HTML from log content;
- templates escape all event text;
- any safe HTML type must be created only by controlled internal rendering, never from incoming logs;
- partial templates have explicit view models;
- avoid passing database entities directly when it creates presentation coupling.

### 18.2 HTMX

HTMX handles:

- filter-driven partial refresh;
- forms;
- source alias updates;
- settings updates;
- pagination/load older;
- dialogs or detail fragments where useful;
- localized progress indicators;
- URL push/replace for history queries.

HTMX is embedded in the binary. No CDN.

Authenticated HTML, fragments, exports, and token-display responses use
`Cache-Control: no-store`. Disable HTMX history snapshot caching; browser navigation
reissues an authenticated request instead of restoring potentially sensitive DOM.

### 18.3 Vanilla JavaScript

Use a small module for:

- EventSource lifecycle;
- live pause/resume;
- auto-scroll detection;
- pending counter;
- DOM row cap;
- keyboard navigation;
- copy actions;
- source quick switcher behavior where native HTML is insufficient;
- theme/density local preferences.

Do not introduce Alpine.js by default. Avoid overlapping state systems.

### 18.4 Routes

Illustrative UI routes:

```text
GET  /login
POST /session
POST /session/logout

GET  /logs
GET  /logs/rows
GET  /logs/events/{id}
GET  /logs/live/stream
GET  /logs/export

GET  /servers
POST /servers
POST /servers/{id}/tokens
POST /tokens/{id}/revoke

GET  /sources
POST /sources/{id}/alias
POST /sources/{id}/clear
POST /sources/{id}/remove

GET  /settings
POST /settings/retention

GET  /status
GET  /audit
```

Actual route names may vary, but state-changing operations use non-GET methods and CSRF protection.

---

## 19. Authentication architecture

### 19.1 Administrator creation

CLI:

```bash
siftail admin create
siftail admin reset-password
```

The command reads password securely from TTY where possible. Do not accept plaintext password as a normal command-line argument because shell history and process lists can expose it.

An optional one-time bootstrap environment mechanism may be added only if it can be made safe and clearly documented. CLI remains primary.

The one administrator username is 3–64 ASCII letters, digits, `.`, `_`, or `-`, and
is compared case-sensitively. Passwords are 12–1024 UTF-8 bytes and are never trimmed
or normalized.

### 19.2 Password hashing

Use Argon2id.

Initial parameters:

- memory: 32 MiB;
- iterations: 3;
- parallelism: 1.

Store parameters with the hash and permit at most two concurrent password-hash
operations. Benchmark on low-resource hardware before changing these values.

### 19.3 Sessions

Use 32-byte high-entropy opaque tokens and allow at most 64 active sessions.

Store a SHA-256 hash of the token, not plaintext.

Cookie:

```text
HttpOnly
Secure when public origin is HTTPS
SameSite=Strict
Path=/
```

Set explicit max age/expiry. Default absolute lifetime is 14 days and default inactivity lifetime is 7 days. These are constants initially, not runtime UI settings. Rotate the session token on successful login.

### 19.4 CSRF

For every state-changing browser request:

- require signed or synchronizer CSRF token;
- verify `Origin` and appropriate `Referer` fallback;
- reject unsupported content types;
- do not allow state changes through GET;
- attach token automatically to HTMX requests.

### 19.5 Login throttling

Progressive per-client and per-account throttling.

Implementation can use:

- bounded in-memory buckets with cleanup; or
- compact SQLite state if restart persistence is needed.

Do not introduce Redis.

Responses must not reveal whether username exists.

### 19.6 Reverse-proxy boundary

Version one supports ordinary reverse proxies and TLS termination but does not accept
identity headers or forward-auth assertions. Forwarded scheme, host, and client
address metadata is considered only from configured proxy networks. Trusted identity
authentication is deferred until after public dogfood and requires a separate ADR.

---

## 20. Ingestion-token security

### 20.1 Token generation

- at least 32 random bytes;
- generated from `crypto/rand`;
- encoded using a URL-safe representation;
- include a nonsecret prefix for operator recognition if useful;
- displayed once.

### 20.2 Storage

Store:

- secure token hash;
- short nonsecret prefix/fingerprint;
- name;
- server ID;
- creation/last-use/revocation times.

Because token entropy is high, a fast cryptographic hash or keyed hash can be appropriate. The exact design must prevent plaintext recovery and timing leaks. Password-hash cost is not necessary for random API tokens if secure comparison and database compromise considerations are addressed.

### 20.3 Verification

- parse token format;
- derive lookup prefix if used;
- retrieve bounded candidate set;
- compare securely;
- reject revoked token;
- update `last_used_at` efficiently, not necessarily on every event transaction if write amplification is excessive.

### 20.4 Rotation

Create new token, display once, permit overlap, then revoke old token explicitly or after operator-selected grace period. All transitions audited.

---

## 21. Browser security headers

Application-owned baseline:

```text
Content-Security-Policy:
  default-src 'self';
  script-src 'self';
  style-src 'self';
  img-src 'self' data:;
  connect-src 'self';
  object-src 'none';
  base-uri 'none';
  frame-ancestors 'none';
  form-action 'self';

X-Content-Type-Options: nosniff
Referrer-Policy: same-origin
Permissions-Policy: camera=(), microphone=(), geolocation=()
Cross-Origin-Opener-Policy: same-origin
```

Also consider:

- `Cross-Origin-Resource-Policy: same-origin`;
- `X-Frame-Options: DENY` as legacy defense;
- HSTS only when public URL is HTTPS and deployment behavior is understood.

Avoid inline scripts/styles so CSP remains strict without nonces in version one.

The UI listener uses `same-origin` because Chromium otherwise serializes native
form navigation origins as `null`, making required exact Origin/Referer
validation reject Siftail's own login and logout forms. The policy suppresses
cross-origin referrers while allowing local form navigation to carry verifiable
same-origin metadata. Common ingestion-listener responses retain
`no-referrer`.

---

## 22. TLS and proxy behavior

TLS terminates at Coolify, Caddy, Traefik, Nginx, or another reverse proxy.

Siftail serves HTTP internally.

Requirements:

- explicit `SIFTAIL_PUBLIC_URL` for browser origin;
- explicit `SIFTAIL_INGEST_PUBLIC_URL` for generated source configuration, with same-origin path derivation allowed only when deliberately configured;
- trust forwarded scheme/host only from configured proxy networks;
- secure cookie decision based on explicit public URL, not arbitrary request headers;
- optional direct-request rejection in strict proxy-only mode;
- ingestion may use private HTTP over an encrypted network such as Tailscale, but HTTPS is recommended across untrusted networks.

Siftail does not issue or renew certificates.

---

## 23. Retention worker

### 23.1 Schedule

Default: once per hour using an internal timer loop.

No cron daemon in the container.

### 23.2 Age cleanup

Delete expired events in bounded chunks.

Each transaction should complete quickly enough not to starve ingestion.

### 23.3 Size cleanup

Measure the active SQLite footprint as the main database plus WAL and SHM files.
When it reaches 95% of the configured limit:

- delete oldest events by `(retention_at_us, id)` toward 90%;
- repeat bounded chunks;
- stop on error;
- expose last result;
- never delete audit/configuration indiscriminately.

### 23.4 Audit cleanup

Delete the oldest security audit records in bounded chunks when they are older than
the configured age or the table exceeds 100,000 rows. Application-log retention never
deletes audit records.

The audit store enforces the count cap in the same transaction as every insert,
keeping the newest 100,000 events by `(occurred_at_us, id)`. Explicit cleanup
deletes at most 1,000 rows per coordinator transaction, applying the age limit
first and then repairing any count overflow. The application root owns one
recoverable cleanup worker that runs once at startup and then hourly. A
cleanup error is logged by safe component/category only and retried on the
next interval.

### 23.5 Vacuum and checkpoint

After meaningful deletion:

- preflight with a passive WAL checkpoint;
- run incremental vacuum for at most 8,192 pages per reclamation cycle;
- perform a controlled truncate checkpoint to release physical WAL allocation;
- avoid full `VACUUM` automatically.

A busy checkpoint stops size-driven deletion for the current run and retries
on the next scheduled cycle. This prevents a long-lived reader from causing
speculative event deletion merely because physical WAL reclamation is delayed.

A full `VACUUM` is a manual CLI maintenance command because it rewrites the database and may need substantial free disk.

### 23.6 Concurrency

Retention writes serialize through the writer/database coordination strategy.

Send maintenance work through the write coordinator. Do not let retention and
ingestion create uncontrolled `SQLITE_BUSY` contention.

---

## 24. Disk-full and degraded mode

### 24.1 Detection

Detect through:

- SQLite full/I/O error classification;
- database/WAL size checks;
- optional filesystem free-space status for warnings.

### 24.2 State

When storage becomes unavailable:

- set readiness unhealthy for ingestion;
- reject ingestion with `507` for full storage or `503` for temporary database issue;
- authenticate each ingestion request, then reject known degradation before
  decoding or queueing its payload;
- preserve UI read access where possible;
- show a critical warning;
- record sanitized diagnostic;
- attempt only safe bounded cleanup;
- do not delete database;
- do not accept into unbounded memory.

### 24.3 Recovery

After operator frees space or retention succeeds:

- while degraded, retry every five seconds with one coordinator-serialized,
  bounded 64 KiB settings mutation that is inserted and deleted in the same
  transaction;
- require that transaction to commit successfully, leaving no settings row;
- clear degraded state;
- restore readiness;
- record recovery event;
- allow Fluent Bit retries to resume.

The probe exercises the same main-database/WAL commit path as application
writes. A failed probe remains safely categorized and is logged only at debug
with no SQLite detail. It does not recreate, replace, delete, vacuum, or
migrate the database. Retention remains an independent bounded worker and may
release pages that allow a later probe to succeed.

---

## 25. Backups and restore

### 25.1 Online backup

Use SQLite's online backup API through the selected driver or a carefully validated equivalent.

Do not copy the live main database file alone while WAL writes are active.

The active process owns one bounded asynchronous backup manager. It accepts at
most one job, uses a separate source reader and destination connection, copies
at most 256 pages per step, yields between steps, and stops with application
cancellation. Ingestion continues through the ordinary writer coordinator;
SQLite defines whether a concurrent committed write is before or after the
consistent snapshot boundary.

### 25.2 Full backup flow

```text
1. Validate an existing destination directory, a non-source output path, and
   available space for the logical page count plus 5% or 1 MiB slack.
2. Refuse every existing destination and create a random same-directory hidden
   staging file exclusively with mode 0600.
3. Run the SQLite online backup API in 256-page steps.
4. Switch only the staged artifact to a self-contained delete journal, enable
   secure deletion, delete every session row, and write exactly one format-1
   completed full-backup metadata record in a committed transaction.
5. Close and synchronize the staged artifact.
6. Run full SQLite integrity, schema compatibility, required-table,
   backup-metadata, completed-state, and zero-session verification.
7. Stream SHA-256 calculation with bounded memory.
8. Atomically hard-link the verified inode to the final non-existing filename,
   remove the staging link, and synchronize the parent directory.
9. Reopen and verify the final path, then report success and audit only the
   safe basename/type. Remove the final link if final verification or success
   audit recording fails.
```

Every backup type excludes the `sessions` table contents.
The manager retains only the latest process-local typed progress/outcome and
never retains the requested full path after completion. CLI and authenticated
browser requests start a job quickly and poll that bounded state; they do not
hold a multi-gigabyte backup in an HTTP response.

### 25.3 Configuration-only backup

Create a new current-schema SQLite database rather than serializing a second
format. Hold one source read transaction while counting and streaming explicit
columns in primary-key order for `administrators`, `servers`,
`ingestion_tokens`, `settings`, and `sources`. Insert through one destination
transaction in foreign-key-safe order. Memory remains bounded to one row plus
SQLite connection state; the progress unit is copied configuration rows.

The resulting database retains required empty tables so it can follow the
ordinary compatibility and restore path. It contains no `log_events`,
`container_instances`, `security_audit_events`, or `sessions` rows. Finalization,
mode-`0600` staging, no-overwrite publication, synchronization, checksum,
verification, cleanup, and single-job serialization use the full-backup safety
contract. Capacity preflight reserves at least 2 MiB; actual writes still fail
safely if the selected configuration is larger than available space.

Restore replaces the complete database with the configuration snapshot and
empty history; it does not merge records.

### 25.4 Verify command

```bash
siftail backup verify /path/backup.sqlite
```

Checks:

- a regular readable owner-private SQLite file rather than a symlink;
- full `integrity_check`;
- every expected schema table and exact supported schema version;
- exactly one completed format-1 metadata row;
- a recognized `full` or `configuration` type;
- zero session rows for every type;
- zero event, container-observation, and security-audit rows for configuration
  artifacts; and
- a streaming SHA-256 checksum and byte count.

Verification opens the artifact read-only and never applies migrations or
changes. With the server active, the CLI and browser submit it to the same
single-job manager and record `backup.verify`. With the server stopped, the CLI
verifies directly without opening the active database and therefore cannot
record an audit event. Errors expose only a closed category, not paths, SQLite
details, hashes, or artifact contents.

### 25.5 Restore flow

```text
1. Require maintenance mode / ingestion stopped.
2. Verify backup.
3. Confirm destructive operation.
4. Close active database connections.
5. Move current database files to rollback location.
6. Place restored database atomically.
7. Remove stale WAL/SHM safely.
8. Open restored database.
9. Check schema compatibility.
10. Migrate a supported older schema using normal forward migrations.
11. Run quick check.
12. Reopen services or instruct restart.
```

Restore is primarily CLI-driven. Keep exactly one managed rollback copy and replace
the preceding managed rollback only after the new database passes compatibility and
integrity checks. Because sessions are excluded, every restore invalidates browser
sessions and requires a new sign-in.

### 25.6 Downgrade behavior

Older binary + newer schema:

- refuse startup;
- state current schema and supported maximum;
- instruct operator to reinstall compatible version or restore pre-upgrade backup;
- never attempt best-effort down-migration.

---

## 26. Health endpoints

### 26.1 Liveness

```http
GET /health/live
```

Meaning: process is running and HTTP stack can respond.

Liveness should not fail merely because SQLite is temporarily busy. Avoid restart loops.

Response contains no sensitive configuration.

### 26.2 Readiness

```http
GET /health/ready
```

Healthy when:

- migrations complete;
- quick check passed at startup;
- database writable;
- writer running;
- shutdown not started;
- critical degraded state absent.

Response remains minimal.

Startup migration, integrity, and writable checks establish the initial state.
The writer lifecycle owns the live ready/not-ready transition. A
storage-unavailable commit marks readiness unhealthy; while degraded, a
coordinator-serialized bounded transaction tests recovery every five seconds
and only its successful commit recovers database readiness. Retention remains
independently degraded when the size target cannot be reached after
application events are exhausted, and only a later successful retention result
clears that condition. Shutdown makes readiness unhealthy before listeners
drain. Liveness remains healthy for these database and degradation states while
the HTTP stack can respond.

### 26.3 Authentication

Health endpoints may be unauthenticated for orchestration, but expose only status code and minimal generic body.

Detailed status page is authenticated.

The status page combines bounded in-memory operational state with sanitized,
index-backed SQLite facts. In-memory state uses 60 one-second event-rate
buckets and the latest 100 diagnostic entries. It contains aggregate counts,
safe categories, and fixed internal summaries only. “Events accepted today”
is explicitly scoped to the current process because reconstructing an exact
received-today count would require an unbounded event scan after restart.
Oldest and newest event facts use one-row lookups through the existing History
time index. No status query reads message, raw payload, attributes,
credentials, hashes, headers, or environment values.

---

## 27. Internal logging and diagnostics

### 27.1 Output

Siftail logs to stdout/stderr only.

Formats:

- text default;
- optional JSON.

### 27.2 Levels

Use the Go standard library `log/slog` unless a concrete missing capability is demonstrated.

Default `info` includes:

- startup/shutdown;
- migrations;
- listener startup;
- retention summaries;
- backup/restore;
- degraded/recovered transitions;
- critical auth attack indicators;
- critical worker errors.

Do not log each accepted batch at info.

### 27.3 Sensitive-data rules

Never log:

- incoming application message content;
- raw request bodies;
- authorization headers;
- session tokens;
- ingestion tokens;
- password values;
- token/password hashes;

On decode failure, log only safe metadata:

- request ID;
- authenticated server ID/name;
- body size;
- record index;
- decoder stage;
- error category.

### 27.4 Correlation IDs

Generate an internal request ID for every request and return it as `X-Request-ID`.

Use a sortable random identifier such as ULID only if dependency and collision behavior are justified; random UUID or a small internal generator is acceptable. Avoid a dependency solely for aesthetic IDs.

### 27.5 Diagnostic event list

Maintain exactly the latest 100 sanitized operational events in a bounded table or
ring. This powers the authenticated diagnostics section.

It is not a second log database.

The implemented process-local ring accepts only typed component/category
pairs whose severity and summary are selected from closed internal tables.
Callers cannot persist arbitrary error strings. An entry may carry one bounded
internal request ID and an optional recovery time; it never carries a path,
request body, environment map, credential, hash, or application field. The
ring resets on restart by design. Authenticated Status and the owner-only
`siftail diagnostics` control-socket command expose at most those 100 entries.

### 27.6 Recursive ingestion prevention

Generated Coolify Fluent Bit configuration must exclude Siftail's own container/service. Otherwise Siftail stdout may be drained into Siftail recursively.

Document and test the exclusion strategy against supported Coolify behavior.

---

## 28. Coolify and Fluent Bit integration

### 28.1 Integration boundary

Siftail does not modify Coolify through its API or database.

The UI generates a ready-to-paste custom Fluent Bit configuration.

This reduces coupling and security surface.

### 28.2 Output protocol

Use Fluent Bit HTTP output:

- HTTP POST;
- `json_lines` or compatible documented JSON format;
- gzip compression;
- authorization header;
- retry enabled;
- filesystem buffering enabled and bounded.

### 28.3 Illustrative configuration

Compatibility evidence is pinned on 2026-07-28 to Coolify `v4.1.1` with its
shipped `cr.fluentbit.io/fluent/fluent-bit:2.0` image reference, and to generic
Fluent Bit `v5.0.9`. The fixtures and exact limitations are recorded in
`docs/integrations/coolify-fluent-bit-compatibility.md`. This does not imply a
broader supported release range. Exact keys are validated against Coolify's
custom drain source and representative production-path payloads.

```ini
[SERVICE]
    Flush                     2
    Log_Level                 info
    storage.path              /tmp/siftail-fluent-bit
    storage.sync              normal
    storage.checksum          off
    storage.max_chunks_up     16
    storage.backlog.mem_limit 16M

[INPUT]
    Name              forward
    Listen            0.0.0.0
    Port              24224
    storage.type      filesystem

[FILTER]
    Name    grep
    Match   *
    Exclude COOLIFY_APP_NAME ^siftail-self$

[OUTPUT]
    Name           http
    Match          *
    Host           logs.example.com
    Port           443
    URI            /api/v1/ingest
    Format         json_lines
    json_date_key  date
    json_date_format iso8601
    Compress       gzip
    Header         Content-Type application/x-ndjson
    Header         Authorization Bearer <TOKEN>
    tls            On
    Retry_Limit    False
    storage.total_limit_size 256M
```

The 256 MiB filesystem buffer is a bounded retry cushion, not a lossless-delivery
promise. The generator must refuse to present a configuration as ready when the
supported Coolify version cannot provide a tested self-container exclusion.

This sample remains conceptual; the pinned fixture is the tested configuration
shape. Generated configuration must account for:

- public HTTPS vs private networking;
- exact exclusion fields available from Coolify;
- metadata renaming;
- server token;
- host/port;
- buffering path viability inside the Coolify log-drain container;
- supported Fluent Bit syntax/version.

### 28.4 Filesystem buffering

Source-side buffering belongs to Fluent Bit.

Siftail must not create its own persistent overflow spool.

Generated config should bound Fluent Bit storage to prevent source disk exhaustion.

### 28.5 Metadata mapping

Coolify metadata is used when available, with fallbacks defined in `DOMAIN.md`.

The receiver must tolerate:

- absent metadata;
- renamed fields;
- generated container names;
- generic Fluent Bit senders;
- plain text and structured JSON.

### 28.6 Test workflow

After token creation, UI provides:

- generated `curl` command;
- one-time token handling guidance;
- live receipt test status;
- normalized source preview;
- commit confirmation;
- troubleshooting hints.

Do not build a general API console.

The generated artifacts use only the explicit
`SIFTAIL_INGEST_PUBLIC_URL`; they never trust request or forwarded host data.
The URL is a complete HTTP(S) `/api/v1/ingest` URL without credentials, query,
or fragment. Browser previews retain a nonsecret placeholder and substitute
the token only for an explicit clipboard action, keeping token plaintext in
one one-time DOM field.

The guided action is an administrator-triggered bounded POST from the UI
process to that configured URL. Its client bypasses environment proxies and
refuses redirects so the Authorization header cannot be redirected or routed
through an ambient proxy. It reports a commit only for Siftail's `204`
contract plus its `X-Siftail-Ingest-Outcome: committed` response marker;
delivery failure, authentication rejection, retryable capacity or storage
failure, and other rejection remain distinct. The synthetic committed
event uses a random stable event ID and the fixed source
`siftail-test/setup/guided-ingestion/probe`, then follows ordinary application
retention. No token, response body, or authorization header enters process
logs, diagnostics, browser URLs, or persistent configuration.

### 28.7 External references

Implementation should verify current official documentation during development:

- Coolify drain logs: <https://coolify.io/docs/knowledge-base/drain-logs>
- Fluent Bit HTTP output: <https://docs.fluentbit.io/manual/data-pipeline/outputs/http>
- Fluent Bit output formats: <https://docs.fluentbit.io/manual/data-pipeline/outputs/output_formats>

These references inform integration, but Siftail's tested contract and integration tests are authoritative for releases.

---

## 29. Security threat model

### 29.1 Protected assets

- application log content;
- structured attributes;
- administrator password hash;
- sessions;
- ingestion tokens;
- source metadata;
- backups;
- audit history.

### 29.2 Adversaries

- unauthenticated internet client;
- malicious or compromised source server;
- browser-based cross-site attacker;
- attacker with read access to database backup;
- accidental operator misconfiguration;
- malformed/high-volume ingestion client;
- log content containing HTML/JavaScript or terminal control sequences.

### 29.3 Core controls

- independent listener policies;
- token-authenticated ingestion;
- token-bound server identity;
- strict body/decompression limits;
- bounded queue;
- output escaping;
- no raw HTML from logs;
- CSRF and origin validation;
- secure session cookies;
- Argon2id passwords;
- hashed high-entropy tokens;
- login throttling;
- strict CSP and security headers;
- no external assets;
- safe diagnostics;
- proportional destructive confirmations;
- audit events;
- no arbitrary SQL/admin API.

### 29.4 Log-content attacks

Logs are hostile input.

Defenses:

- escape HTML;
- never evaluate ANSI sequences as control behavior in browser;
- preserve text without allowing markup;
- bound event size and JSON depth;
- avoid regex on untrusted content in ingestion path;
- ensure copy/download uses correct content types;
- sanitize filenames;
- set `nosniff`;
- never place raw log content into headers.

### 29.5 Database compromise limitation

Hashing tokens protects plaintext recovery but does not make a database backup harmless. Application logs and password hashes remain sensitive. Documentation must require backup protection and optional host/volume encryption.

---

## 30. CLI architecture

Focused commands:

```text
siftail serve
siftail version
siftail config validate

siftail admin create
siftail admin reset-password
siftail sessions revoke-all

siftail server create
siftail server list
siftail token create
siftail token revoke

siftail database check
siftail database check --full
siftail database checkpoint
siftail database vacuum
siftail database stats

siftail backup --output <path>
siftail backup --configuration-only --output <path>
siftail backup verify <path>
siftail restore <path>

siftail diagnostics
```

Rules:

- no arbitrary SQL command;
- no plaintext password command argument;
- destructive operations require explicit confirmation or `--yes` for deliberate automation;
- commands return useful exit codes;
- commands can run in container through `docker exec`;
- help text avoids exposing secrets.

When `siftail serve` is active, administrative commands use a private owner-only Unix
control socket under `/data` so all mutations still pass through the write coordinator.
No TCP administration API is exposed. Direct database maintenance and restore commands
require the server to be stopped; online backup and diagnostics use the control socket.
`version` and `config validate` do not open the database.

`database check` is special-cased as an active-safe bounded inspection. While
the server is active it runs `quick_check` through the read pool and orders one
passive WAL checkpoint through the maintenance coordinator. `database check
--full` is refused while the server is active. With the server stopped, quick
and full checks open the existing file read-only, never create or migrate it,
never run a checkpoint, and report filesystem access separately from an
observed application commit. Reports have a closed, path-free field set:
schema compatibility, SQLite version, integrity, required pragmas,
writability source, checkpoint state, page/free counts, and DB/WAL/SHM byte
counts. Corrupt, incompatible, busy, canceled, and unavailable results are
reduced to safe categories.

---

## 31. API compatibility policy

### 31.1 Public contracts in version one

- ingestion HTTP contract;
- health endpoints;
- documented CLI commands;
- environment variables;
- database upgrade behavior;
- backup artifact compatibility within stated rules.

### 31.2 Non-public contracts

- internal HTML fragment routes;
- template structure;
- JavaScript module internals;
- database schema for direct third-party use;
- internal Go packages;
- diagnostics table layout.

There is no public administration REST API in version one.

---

## 32. Testing architecture

### 32.1 Test strategy

Integration-heavy balance:

- focused unit tests for parsing, normalization, validation, cursor encoding;
- real temporary SQLite tests for stores, transactions, migrations, retention, auth;
- HTTP contract tests for status codes, limits, headers, atomicity;
- Playwright smoke tests for critical user workflows;
- benchmarks for ingestion, queries, retention, backup;
- race and stress tests for concurrency.

### 32.2 Real SQLite

Use temporary real databases. Do not mock SQL for ordinary store tests.

Tests should configure WAL and production-relevant pragmas.

### 32.3 Migration fixtures

Maintain a fixture for every released schema version.

For each:

```text
open historical fixture
→ migrate to current
→ verify schema
→ verify representative data
→ run quick/integrity check
→ execute critical queries
```

### 32.4 HTTP ingestion tests

Cover:

- valid NDJSON;
- gzip;
- invalid gzip;
- oversized compressed body;
- decompression bomb behavior;
- too many events;
- oversized single event;
- invalid JSON at last record;
- atomic rejection;
- invalid/revoked token;
- token-bound server identity;
- queue full;
- database failure;
- commit-before-response;
- client cancellation;
- duplicate source event ID;
- multiline payload preservation;
- structured JSON extraction;
- missing metadata fallbacks.

### 32.5 Concurrency tests

Cover:

- simultaneous ingestion;
- retention while ingesting;
- backup while ingesting;
- many SSE subscribers;
- slow subscribers;
- token revocation during requests;
- shutdown with queued writes;
- WAL checkpoint during reads;
- database busy conditions;
- source cache churn;
- queue byte accounting.

Run:

```bash
go test -race ./...
```

in CI or a required periodic release job.

### 32.6 Browser tests

Critical Playwright flows:

- login;
- history filtering;
- URL state restore;
- live connection;
- pause/resume;
- scroll-away counter;
- row expansion;
- source alias;
- token creation and one-time display;
- token revocation;
- destructive confirmation;
- mobile emergency viewport smoke test.

Do not build broad fragile screenshot tests.

---

## 33. Benchmarks and performance gates

### 33.1 Data tiers

- 100,000 events;
- 1,000,000 events;
- 10,000,000 events.

Synthetic data must include:

- uneven source distribution;
- plain text;
- JSON attributes;
- stack traces;
- repeated messages;
- varied message sizes;
- multiple levels;
- recent and old time ranges.

### 33.2 Ingestion benchmarks

Measure:

- events/second;
- request commit latency percentiles;
- SQLite transaction time;
- source-resolution cache hit rate;
- allocations;
- RSS of production container;
- behavior at queue saturation.

### 33.3 Query benchmarks

Measure queries defined in `PRODUCT.md`.

### 33.4 Regression thresholds

Review when:

- throughput decreases >15%;
- commit latency increases >20%;
- idle memory increases >15 MB;
- normal memory increases >25 MB;
- image grows >20%.

CI noise must be considered. Correctness and durability remain hard gates; noisy microbenchmarks should not block every commit.

---

## 34. Packaging and image build

### 34.1 Build stages

Recommended multi-stage build:

1. frontend asset preparation, including pinned local HTMX and any development-only tooling;
2. Go build with CGO for target architecture;
3. minimal runtime image with required C library and CA certificates;
4. non-root runtime user where compatible with mounted volume permissions.

No compiler, Node runtime, npm cache, or source tree in final image.

### 34.2 Embedded assets

Use Go `embed` for:

- templates;
- CSS;
- JavaScript;
- HTMX;
- migrations where desired.

### 34.3 Container behavior

- default command starts server;
- health checks target liveness/readiness;
- graceful SIGTERM;
- `/data` documented as required volume;
- UI and ingestion ports exposed/documented;
- no privileged mode;
- no Docker socket;
- no host filesystem access beyond mounted data.

### 34.4 Architectures

Publish:

- `linux/amd64`;
- `linux/arm64`.

Cross-builds must be tested because CGO is used.

---

## 35. Release architecture

Channels:

- stable semver tags;
- `latest` points to stable;
- `edge` points to successful main build.

Stable release checklist includes:

- all tests;
- race tests;
- migration fixtures;
- fresh installation;
- upgrade test;
- backup/verify/restore;
- Coolify integration;
- Fluent Bit retry behavior;
- disk-full behavior;
- graceful shutdown;
- security checklist;
- resource measurement;
- soak test.

Release notes include:

- Added;
- Changed;
- Fixed;
- Security;
- Database migrations;
- Configuration changes;
- Upgrade instructions;
- Known issues.

---

## 36. Soak testing

Before stable public milestones, run several hours of mixed workload:

- continuous ingestion;
- bursts;
- historical search;
- multiple SSE clients;
- slow client;
- retention cycle;
- online backup;
- restart;
- token rotation;
- source transition;
- simulated temporary disk/database errors.

Success means:

- no unbounded memory growth;
- no goroutine leak;
- no accepted-batch loss;
- database remains valid;
- WAL remains controlled;
- UI remains responsive;
- recovery is clear.

---

## 37. Dependency policy

A new dependency must be justified by:

- concrete current need;
- resource impact;
- security maintenance;
- license compatibility;
- transitive dependency size;
- whether standard library or existing dependency suffices.

Prefer:

- Go standard library;
- Chi;
- SQLite driver;
- minimal cryptography packages from trusted sources;
- pinned HTMX asset.

Avoid adding a dependency for:

- trivial ID generation;
- generic mapping helpers;
- validation that is clearer in direct code;
- cron scheduling;
- logging abstraction layers without value;
- CSS frameworks;
- icon libraries when a few inline local SVGs suffice.

---

## 38. ADR policy

Create a lightweight ADR for consequential changes, including:

- SQLite driver change;
- FTS5 adoption;
- change to commit-before-acknowledgement;
- new runtime service;
- alternative database;
- authentication architecture change;
- canonical event-format change;
- public administration API;
- built-in encryption;
- clustering;
- persistent internal spool.

Do not create ADRs for routine refactors or component spacing.

---

## 39. Known architectural trade-offs

### 39.1 SQLite

Chosen for simplicity and resource use. Trade-off: one primary writer and limited broad-search scale. Mitigated by batching, WAL, indexes, bounded scope, and honest target scale.

### 39.2 Commit-before-acknowledgement

Chosen for durability. Trade-off: request latency includes commit time. Mitigated by batches and efficient writer.

### 39.3 HTMX plus vanilla JavaScript

Chosen for low dependency and server-owned state. Trade-off: sophisticated client interaction needs custom code. Mitigated by keeping live UI constrained and modules focused.

### 39.4 No built-in redaction

Chosen to avoid false confidence and ingestion cost. Trade-off: sensitive emitted content is stored. Mitigated through documentation and application-side logging discipline.

### 39.5 No built-in encryption at rest

Chosen to avoid key-management and nonstandard SQLite complexity. Trade-off: database is readable to host/storage attackers. Mitigated by host/volume encryption and protected backups.

### 39.6 At-least-once-compatible semantics

Chosen because generic HTTP retry can be ambiguous. Trade-off: occasional duplicates. Mitigated by optional stable source event IDs and truthful UI.

---

## 40. Implementation order

Recommended sequence:

1. repository, config, logger, database opening;
2. migrations and test fixture harness;
3. domain types and normalizer tests;
4. server/token store and CLI token creation;
5. ingestion auth, limits, decoder;
6. bounded queue and writer;
7. source resolution and event persistence;
8. benchmark harness;
9. administrator auth and sessions;
10. historical query store and cursor;
11. minimal templates and HTMX history page;
12. SSE broker and live client module;
13. retention and size management;
14. status/diagnostics;
15. Coolify config generator and guided test;
16. backup/verify/restore;
17. audit and hardening;
18. bounded History export;
19. public documentation and packaging.

Do not begin visual polish before ingestion correctness is demonstrated.

---

## 41. Architecture acceptance checklist

A release candidate must satisfy:

- one process and one persistent volume;
- no production Node runtime;
- dual listener separation;
- token-bound trusted server identity;
- strict body and decompression limits;
- atomic request semantics;
- commit-before-success response;
- queue bounded by events and bytes;
- one controlled SQLite writer;
- live publication after commit only;
- cursor pagination;
- retention by age and size;
- safe degraded storage behavior;
- automatic migrations and schema-too-new refusal;
- online backup and verified restore;
- strict browser security headers;
- no raw log content in internal diagnostics;
- no external telemetry/assets;
- resource and concurrency tests;
- Coolify self-ingestion exclusion.

---

## 42. Final architecture summary

Siftail is intentionally a compact monolith:

> A single lifecycle-managed Go process with separate UI and ingestion listeners, embedded server-rendered assets, a bounded authenticated ingestion pipeline, a single batched SQLite writer, indexed historical reads, an in-process bounded SSE broker, and explicit retention, backup, security, and failure behavior.

The architecture should remain boring where boring improves trust.
