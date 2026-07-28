# Siftail Domain Specification

**Status:** Authoritative domain model  
**Product:** Siftail  
**Audience:** Maintainer, coding agents, reviewers, contributors

---

## 1. Purpose

This document defines the canonical concepts, value semantics, lifecycle rules, and invariants of Siftail. It is intentionally independent of HTTP frameworks, HTML rendering, and most storage details.

Coding agents must not invent alternative meanings for domain terms. If implementation pressure suggests changing an invariant here, the change is architectural and must be reviewed, documented, and accompanied by migrations and tests where applicable.

This document is authoritative for:

- event identity and timestamp semantics;
- source hierarchy;
- source discovery and lifecycle;
- log normalization;
- structured attributes;
- event ordering;
- deduplication;
- retention and purging;
- administrator, session, and ingestion-token lifecycle;
- audit semantics;
- backup and restore semantics;
- live-subscription behavior;
- deployment-boundary semantics;
- domain errors and state transitions.

---

## 2. Bounded context

Siftail's bounded context is **recent operational application logs for self-hosted systems**.

Inside the bounded context:

- application events are received;
- source identities are normalized;
- events are persisted and ordered;
- recent history is searched;
- live committed events are streamed;
- retention is enforced;
- access and administrative changes are secured and audited.

Outside the bounded context:

- metrics;
- distributed tracing;
- alert correlation;
- application issue tracking;
- uptime checks;
- deployment orchestration;
- application configuration;
- business analytics;
- user identity management beyond one administrator;
- secret classification;
- compliance archiving.

Siftail may observe container transitions, but it does not own deployment truth.
Version one stores container metadata only. A future inferred boundary must never
claim that a deployment succeeded.

---

## 3. Ubiquitous language

Use these terms consistently in code, schema, tests, documentation, and UI.

### 3.1 Administrator

The single human operator authorized to use the browser administration interface and recovery CLI.

There is exactly one active administrator account in version one. Multiple concurrent sessions are allowed.

### 3.2 Session

A revocable server-side browser-authentication record associated with the administrator. The browser holds an opaque token; Siftail stores only a hash.

### 3.3 Server

A trusted logical origin of ingested events. A server is established administratively and bound to one or more ingestion-token records over time.

Examples:

- `Hetzner Production`
- `Prishtina Home Lab`
- `Staging VPS`

The server identity is trusted because it is derived from the authenticated ingestion token, not from payload text.

### 3.4 Ingestion token

A high-entropy bearer credential authorizing one logical server to submit events. The plaintext token is displayed once. Only a token hash is retained.

### 3.5 Project

A grouping supplied by source metadata, commonly matching a Coolify project. It is not a user-created Siftail project-management object.

### 3.6 Environment

A source grouping such as `production`, `staging`, or `development`.

### 3.7 Application

A stable logical deployed application within an environment.

### 3.8 Service

A stable logical component inside an application, such as `api`, `worker`, `web`, or `scheduler`.

### 3.9 Container instance

An ephemeral runtime instance identified by a container ID, container name, or equivalent source metadata. A redeployment may replace the container instance while preserving the same stable service.

### 3.10 Source

The stable normalized hierarchy used to group events:

```text
Server
└── Project
    └── Environment
        └── Application
            └── Service
                └── Container instance
```

In most domain operations, “source” means the stable hierarchy through Service.
Container instance is retained as event metadata and may support a future
deployment-boundary feature.

### 3.11 Source alias

An administrator-defined display label for a discovered source. An alias changes presentation only. It never rewrites event metadata or source keys.

### 3.12 Log event

An immutable persisted application event accepted from an authenticated source.

### 3.13 Received record

A transport-decoded record before canonical normalization. It may contain arbitrary transport metadata and untrusted values.

### 3.14 Canonical event

A normalized event ready for persistence. It conforms to the mandatory event invariants in this document.

### 3.15 Structured attributes

Additional application-provided fields preserved as JSON after selected common fields have been normalized.

### 3.16 Deployment boundary

A reserved post-dogfood concept for an inferred transition between container instances
for the same stable service. It is not part of the version-one persisted model.

### 3.17 Retention policy

The global rules controlling application-event age and maximum database size.

### 3.18 Clear logs

Delete application events for a source while retaining the source, aliases, and relevant administrative metadata.

### 3.19 Remove source

Delete application events and removable source metadata, aliases, and inferred deployment history for a selected source.

### 3.20 Security audit event

An immutable low-volume record of authentication or security-sensitive administrative activity.

### 3.21 Diagnostic event

A sanitized internal operational notification about Siftail itself. It is not an application log event and must not contain incoming application payloads.

### 3.22 Full backup

A backup containing all database-backed state, including application events.

### 3.23 Configuration-only backup

A backup containing configuration and administrative metadata but excluding application events and reusable plaintext credentials.

---

## 4. Canonical aggregate relationships

Conceptual relationships:

```text
Administrator
├── Sessions
└── SecurityAuditEvents

Server
├── IngestionTokens
├── Projects
└── SecurityAuditEvents

Project
└── Environments
    └── Applications
        └── Services
            ├── ContainerInstances
            ├── LogEvents
            └── DeploymentBoundaries

RetentionPolicy
└── governs LogEvents globally

BackupArtifact
└── captures selected persisted state
```

Siftail may implement these with normalized tables and foreign keys, but table layout does not redefine the domain.

---

## 5. Canonical event model

Every persisted log event must conform to the following conceptual model.

```text
LogEvent
- id: InternalEventID
- event_at_us: UnixMicroseconds
- received_at_us: UnixMicroseconds
- retention_at_us: derived UnixMicroseconds
- source_id: StableSourceID
- container_instance_id: optional ContainerInstanceID
- stream: LogStream
- level_normalized: NormalizedLogLevel
- level_original: optional string
- message_raw: bytes or text preserving original application payload
- message_text: searchable human-readable string
- attributes_json: optional canonical JSON object
- source_event_id: optional string
- transport_metadata_json: optional narrowly retained metadata, if explicitly required
```

### 5.1 Internal event ID

`id` is a monotonically increasing SQLite integer key assigned at persistence.

It is:

- internal;
- stable after creation;
- unique within one Siftail database;
- used as an ordering tie-breaker;
- suitable for cursor pagination;
- not a source-generated identity;
- not globally unique across restored or merged databases.

### 5.2 Event timestamp

`event_at_us` is the timestamp associated with the application event.

Precedence:

1. valid source event timestamp;
2. valid Fluent Bit record timestamp;
3. Siftail receive time when no valid source timestamp exists.

Rules:

- stored as Unix microseconds in an integer;
- normalized to UTC semantics;
- may be earlier or later than receive time because clocks can drift;
- is not silently clamped to receive time;
- an explicitly supplied timestamp must parse unambiguously and fall within
  years 0001 through 9999 or the entire request is rejected;
- only an absent timestamp falls back to receive time;
- event-time source must be testable.

### 5.3 Receive timestamp

`received_at_us` is assigned when Siftail accepts the HTTP request for processing, before queue delay and commit.

It is:

- generated by Siftail;
- stored as Unix microseconds;
- used to diagnose buffering and delivery delay;
- not normally the primary display ordering.

### 5.4 Commit timestamp

A per-event commit timestamp is not stored. Commit latency is operational state, not permanent event data.

`retention_at_us` is the immutable derived value
`min(event_at_us, received_at_us)`. It may be represented by an indexed
expression rather than a separately stored column.

### 5.5 Source identity

Every event belongs to exactly one trusted Server and exactly one stable normalized source through Service.

Missing source dimensions use explicit fallback values; they are never represented as empty identity components.

### 5.6 Container identity

Container identity is optional but should be retained when present. It is not part of stable service identity.

### 5.7 Stream

Allowed values:

```text
stdout
stderr
unknown
```

Stream is independent of log level.

Rules:

- `stderr` does not imply `error`;
- `stdout` does not imply `info`;
- unknown or unrecognized values normalize to `unknown` while original transport metadata may be retained where useful.

### 5.8 Normalized log level

Allowed values:

```text
trace
debug
info
warn
error
fatal
unknown
```

The normalized value supports consistent filtering.

### 5.9 Original log level

When the source emits a level string, preserve its original trimmed value subject to maximum-length and control-character validation.

Examples:

- `WARNING` → original `WARNING`, normalized `warn`
- `ERR` → original `ERR`, normalized `error`
- `NOTICE` → original `NOTICE`, normalized `info`
- no level → original absent, normalized based on conservative inference or `unknown`

### 5.10 Raw message

`message_raw` preserves the original application payload as faithfully as practical.

For plain text:

- preserve the text content exactly after transport decoding;
- do not normalize whitespace;
- do not remove stack-trace lines;
- do not redact;
- reject invalid UTF-8 at the JSON transport boundary.

For structured JSON:

- preserve the original application JSON text when available;
- do not replace it solely with reserialized JSON if exact original bytes are available;
- transport envelope fields need not be duplicated into raw application payload.

### 5.11 Searchable message

`message_text` is the human-readable searchable representation.

Precedence for structured records:

1. recognized `message` field;
2. recognized `msg` field;
3. recognized alternative documented message field;
4. compact JSON representation if no message field exists.

For plain text, `message_text` equals the decoded text representation of `message_raw`.

`message_text` may be normalized only enough to support safe display and search. It must not materially change meaning.

### 5.12 Structured attributes

`attributes_json` contains remaining application fields after selected common fields are normalized.

Rules:

- object only at the top level;
- canonical valid JSON;
- bounded nesting depth;
- maximum canonical size 256 KiB;
- duplicate JSON keys at any nesting level reject the entire request;
- no dynamic database columns;
- no arbitrary schema mutation;
- unknown fields are preserved when within limits;
- transport-only fields may be excluded when already represented canonically.

### 5.13 Optional source event ID

`source_event_id` is an optional stable identifier supplied by the source.

It is used for deduplication only when:

- explicitly present;
- no longer than 255 UTF-8 bytes and free of control characters;
- scoped to the stable source;
- treated as an opaque, case-sensitive value.

Siftail does not invent a source event ID from message hashes.

---

## 6. Event invariants

The following are non-negotiable without an approved domain change.

1. Persisted application events are immutable.
2. Every event belongs to one token-authenticated server.
3. Event and receive timestamps are both retained.
4. Historical ordering is deterministic.
5. Stream and severity are separate concepts.
6. Original level is preserved when present.
7. Raw application payload is preserved according to documented normalization.
8. Unknown structured fields are not silently discarded unless they exceed explicit safety limits.
9. One request is committed atomically or not accepted.
10. A successful ingestion response means the batch transaction committed.
11. No event is acknowledged merely because it entered an in-memory queue.
12. Events without stable source IDs may legitimately be duplicated after retry.
13. Hash-based message deduplication is prohibited.
14. Multiline reconstruction is a source/Fluent Bit responsibility in version one.
15. A future deployment boundary is not a log event.
16. Retention deletes oldest eligible events first.
17. Aliases never mutate original metadata.
18. Application-log deletion does not erase security audit history.

---

## 7. Historical ordering

Canonical descending historical order:

```sql
ORDER BY event_at_us DESC, id DESC
```

Canonical ascending replay/live order after commit:

```sql
ORDER BY id ASC
```

Rationale:

- event time expresses application chronology;
- internal ID resolves equal timestamps deterministically;
- committed batch publication in ID order aligns live display with persisted order.

Cursor identity therefore contains both:

```text
(event_at_us, id)
```

A cursor must also be bound to the query shape or validated so it cannot produce undefined traversal under incompatible filters.

---

## 8. Deduplication semantics

### 8.1 Supported deduplication

When `source_event_id` exists, Siftail enforces uniqueness on:

```text
(stable_source_id, source_event_id)
```

A duplicate with the same identity and identical canonical content is a
successful idempotent no-op. Canonical comparison includes event time, source
identity, container identity, stream, normalized and original level, raw
message, structured attributes, and normalized common fields. It excludes the
internal ID and receive time.

If the same identity is reused for different canonical content, Siftail rejects
the entire request with `Conflict`; no new events from that request commit.
Conflicts among records in the same request follow the same rule.

### 8.2 Unsupported deduplication

Do not deduplicate based on:

- timestamp + message hash;
- message equality in a time window;
- container + message;
- normalized attributes;
- request body hash alone.

Repeated messages are valid operational evidence.

### 8.3 Delivery guarantee

Siftail provides **durable at-least-once-compatible ingestion semantics**:

- successful response means committed;
- retry after ambiguous network failure may produce duplicates when no stable event ID exists;
- exactly-once delivery is not claimed.

---

## 9. Normalization pipeline

Canonical stages:

```text
Authenticated HTTP request
→ bounded decompression
→ transport decoder
→ ReceivedRecord
→ canonical normalizer
→ CanonicalEvent with stable SourceIdentity
→ transactional database source resolution
→ batch persistence
→ committed event publication
```

### 9.1 Transport decoding

Transport decoding is responsible for:

- content type;
- gzip handling;
- NDJSON framing;
- compatible Fluent Bit HTTP batch shapes;
- record count limits;
- JSON depth limits;
- per-record raw representation;
- syntax errors.

It is not responsible for trusted source identity.

### 9.2 Canonical normalization

Normalization is responsible for:

- event timestamp extraction;
- message extraction;
- level mapping;
- stream mapping;
- structured attribute separation;
- source metadata extraction;
- source event ID extraction;
- fallback values;
- size validation;
- safe text validation.

### 9.3 Failure behavior

A request is atomic.

If any record makes the request invalid under the defined contract:

- reject the entire request before queueing when possible;
- do not persist a prefix of records;
- return a non-success response;
- include a safe record index and reason;
- never include raw failed payload content in internal logs.

No “best effort” partial batch insertion is allowed in version one.

---

## 10. Log-level normalization

Canonical mapping table:

| Input examples | Normalized |
|---|---|
| `trace`, `TRACE` | `trace` |
| `debug`, `DBG` | `debug` |
| `info`, `information`, `notice` | `info` |
| `warn`, `warning` | `warn` |
| `error`, `err`, `severe` | `error` |
| `fatal`, `critical`, `crit`, `panic`, `emerg`, `alert` | `fatal` |
| absent or unrecognized | `unknown` |

When no structured level exists, version one performs conservative prefix-only
inference. After ignoring leading ASCII whitespace, it recognizes these
case-insensitive tokens:

```text
TRACE DEBUG INFO NOTICE WARN WARNING ERROR ERR
FATAL CRITICAL CRIT PANIC EMERG ALERT
```

The token must be at the start, optionally enclosed in `[]`, and followed by
end-of-message, ASCII whitespace, or `:`. It must not classify a normal sentence
because a level word appears later in the message.

Structured level fields take precedence over text inference.

---

## 11. Structured-field normalization

Selected common fields may be normalized for exact filters.

Initial field set:

- `logger`;
- `request_id`;
- `error_type`;
- `http_method`;
- `http_path`;
- `http_status`;
- `duration_ms`.

Rules:

- source-specific aliases may map to these fields through compile-time normalization rules;
- normalized values remain bounded;
- original structured properties remain in raw JSON or attributes unless duplication is intentionally removed and documented;
- arbitrary user-configurable extraction rules are outside version one;
- arbitrary JSON-path filtering is outside version one.

`request_id` is application data and is distinct from Siftail's internal HTTP request correlation ID.

---

## 12. Source identity

### 12.1 Trust boundary

Server identity comes from the authenticated ingestion token.

Incoming payload fields such as hostname, IP, or server name are descriptive metadata. They cannot select another trusted Server.

### 12.2 Stable source key

The stable source key includes normalized values for:

```text
server_id
project_key
environment_key
application_key
service_key
```

Container instance is excluded from stable source identity.

### 12.3 Display labels and identity keys

Each source dimension has:

- a stable normalized key;
- original or most recent source-provided label;
- optional administrator display alias.

Display precedence:

1. administrator alias;
2. current source-provided label;
3. normalized key;
4. explicit fallback label.

Aliases never participate in uniqueness or event ownership.

### 12.4 Layered metadata fallbacks

Server:

1. token-bound server;
2. supplied hostname as descriptive metadata only;
3. `unknown-server` should be needed only for imports or maintenance cases, not ordinary authenticated ingestion.

Project:

1. Coolify project metadata;
2. compose/project metadata;
3. `default-project`.

Environment:

1. Coolify environment metadata;
2. recognized deployment environment;
3. `default-environment`.

Application:

1. Coolify application metadata;
2. compose application/project metadata;
3. service metadata;
4. container name;
5. `unknown-application`.

Service:

1. Coolify service metadata;
2. Docker Compose service;
3. stable container-name derivation;
4. `default`.

Container:

1. container ID;
2. container name;
3. absent.

Fallback behavior must be covered by table-driven tests.

### 12.5 Normalization of keys

Stable keys should:

- be deterministic;
- preserve distinctions important to identity;
- reject control characters and invalid UTF-8;
- be no longer than 128 UTF-8 bytes per component;
- trim surrounding ASCII whitespace but otherwise preserve the supplied,
  case-sensitive UTF-8 value without slugification or locale-sensitive folding;
- not depend on display alias;
- not be regenerated from mutable labels after creation without an explicit merge operation.

The composite key is the length-delimited sequence of the five normalized
components under the trusted `server_id`. Case-only or Unicode-normalization
changes therefore create distinct sources rather than risking an implicit merge.
Migrations must preserve this identity.

---

## 13. Source discovery and lifecycle

### 13.1 Discovery

A source is created automatically when the first valid event for a new stable source is committed.

Discovery occurs in the same transaction or an equivalent atomic unit as event persistence so events cannot reference nonexistent sources.

### 13.2 First seen and last seen

Every source tracks:

- `first_seen_at`;
- `last_seen_at`.

Batch persistence updates `last_seen_at` in aggregate, not once per event.

### 13.3 Active state

Derived state:

- Active: received an event within the last 24 hours.
- Inactive: no event within 24 hours.
- Cleanup eligible: inactive for 90 days.

These durations are fixed process constants in version one.

### 13.4 Automatic source removal

A source must not be automatically removed when it:

- has a manual alias;
- has an active server/token relationship;
- is referenced by retained administrative metadata;
- has application events remaining.

Automatic cleanup may remove only unprotected, empty, stale source metadata.

Each server is limited to 10,000 retained stable sources. A request that would
create sources beyond that limit is rejected atomically. This is a safety bound,
not a sizing target.

### 13.5 Source merge

Automatic source merge is not part of version one.

If metadata changes create two stable sources, Siftail must not silently merge historical data based on fuzzy name similarity.

---

## 14. Container instance lifecycle

A container instance tracks:

- source service;
- source-provided container ID, name, or both;
- first seen;
- last seen;
- active/inactive observation state.

Container instances are ephemeral. They should not clutter primary source selection.

Unreferenced container instances are removed by bounded cleanup. Each server is
limited to 100,000 retained container instances; a request that would exceed the
limit is rejected atomically.

Inferring a deployment boundary from container changes is a post-dogfood
candidate and is not implemented in the pre-public milestones.

---

## 15. Deployment-boundary semantics

### 15.1 Creation condition

If this post-dogfood feature is accepted, create an inferred boundary when:

- a stable service has a previously observed active or most recent container identity;
- a committed event arrives with a different valid container identity;
- the transition is not merely missing metadata becoming present unless explicitly handled;
- duplicate or oscillating metadata is debounced according to a deterministic rule.

### 15.2 Boundary fields

Conceptual model:

```text
DeploymentBoundary
- id
- source_id
- observed_at_us
- previous_container_instance_id
- next_container_instance_id
- inference_reason
```

### 15.3 Invariants

- It is not an application event.
- It does not affect application event count.
- It does not match message searches.
- It is not included in plain-text or NDJSON application exports.
- It does not claim deployment success.
- It may be removed when a source is removed.
- It appears only as contextual timeline presentation.

---

## 16. Retention semantics

### 16.1 Application-event retention

Two global limits apply:

1. age limit;
2. maximum database-size limit.

The first threshold reached triggers oldest-first deletion.

Defaults:

- 14 days;
- 4 GiB.

Supported operator values are whole units:

- age: 1 through 3,650 days;
- maximum active database footprint: 1 through 1,024 GiB.

Both limits are persisted and changed as one retention policy. A validation or
storage failure leaves the complete prior policy active; Siftail never applies
one field from a failed update.

### 16.2 Age eligibility

An event is age-expired when its retention timestamp is older than the cutoff.

Canonical retention timestamp is the earlier of event time and receive time:

```text
retention_at_us = min(event_at_us, received_at_us)
```

Consequences:

- delayed old events may become immediately eligible because their event time is old;
- future-skewed source clocks cannot evade retention because receive time becomes authoritative;
- no additional mutable retention timestamp is required if the expression is indexed or queried efficiently;
- all retention tests must cover old delayed events, future-skewed events, and equal timestamps.

### 16.3 Size eligibility

The size worker wakes when the active SQLite footprint reaches 95% of the
configured limit and deletes oldest application events regardless of age toward
90%. If safe bounded reclamation cannot keep the footprint within the configured
target, ingestion enters storage-full degraded mode.

### 16.4 Deletion order

Canonical deletion order:

```text
retention_at_us ASC, id ASC
```

Deletion occurs in bounded batches, initially around 10,000 events per transaction, tuned by benchmark.

### 16.5 What retention does not delete

Application-event retention does not automatically delete:

- administrator;
- active sessions under their own lifecycle;
- server records;
- ingestion-token metadata;
- security audit events;
- aliases;
- protected source metadata;
- settings;
- diagnostic events under their own bounded policy.

### 16.6 Size accounting

The active SQLite footprint is:

- main database file;
- WAL file;
- shared-memory file when present.

Backup files, restore rollback copies, export staging files, filesystem
snapshots, and unrelated host data are excluded. The configured limit is a
Siftail safety target; bounded overshoot by one admitted batch and transient WAL
work is possible, and Siftail cannot constrain unrelated files.

---

## 17. Purge semantics

### 17.1 Clear logs

`Clear logs`:

- captures an internal event-ID watermark and deletes application events within
  the selected source scope at or below that watermark in bounded chunks;
- retains source identity;
- retains aliases;
- retains server/token configuration;
- records an audit event;
- notifies affected live/history clients through a control event.

### 17.2 Remove source

`Remove source`:

- captures an internal event-ID watermark and deletes application events within
  the source scope at or below that watermark in bounded chunks;
- deletes removable container instances;
- deletes aliases;
- deletes the selected source metadata when referentially safe;
- never deletes the trusted Server merely because one source is removed;
- records an audit event;
- notifies affected browser clients.

Purge operations are not one large atomic transaction. Events committed after
the watermark remain. A removed source may be discovered again if its sender
continues to emit events; the confirmation copy must state this.

### 17.3 Confirmation

- Clear logs: type the affected display name or equivalent strong confirmation.
- Remove source: stronger explicit source-name confirmation.
- Reset all application data: CLI only with explicit flags.

### 17.4 Erasure limitation

No domain operation may be described as secure forensic erasure. Deleted content may persist in:

- SQLite free pages before reclamation;
- WAL files;
- filesystem snapshots;
- SSD wear-leveling;
- backups;
- external source buffers.

---

## 18. Administrator model

Conceptual fields:

```text
Administrator
- id
- username
- password_hash
- created_at
- password_changed_at
- disabled_at: absent in ordinary version-one operation
```

Invariants:

- one active administrator;
- username comparisons use a documented normalization rule;
- password plaintext is never persisted;
- password changes revoke existing sessions unless an explicit exception is designed;
- administrator creation and reset are available from CLI recovery flows;
- administrator deletion without replacement is prohibited through the browser UI.

---

## 19. Session lifecycle

Conceptual model:

```text
Session
- id
- administrator_id
- token_hash
- created_at
- last_used_at
- expires_at
- revoked_at
- user_agent_summary: optional
- client_identity_summary: optional
```

State machine:

```text
Active
├── expires → Expired
├── logout → Revoked
├── password change → Revoked
└── revoke all → Revoked

Expired/Revoked
└── cleanup after grace period → Deleted
```

Rules:

- expiration makes a session invalid immediately;
- expired or revoked record may remain for seven days for security context;
- default session absolute lifetime is 14 days and default inactivity lifetime is 7 days; both are process constants initially, not user-facing settings;
- session token plaintext exists only in the browser cookie and during request verification;
- stored hash must be suitable for high-entropy token verification;
- sessions are not JWTs;
- session records are not kept indefinitely for audit; relevant actions are copied to audit events.

---

## 20. Ingestion-token lifecycle

Conceptual model:

```text
IngestionToken
- id
- server_id
- name
- token_hash
- token_prefix_or_fingerprint
- created_at
- last_used_at
- revoked_at
- replacement_token_id: optional
```

State machine:

```text
Created → Active → Revoked
              └── Rotated → replacement Active, previous eventually Revoked
```

Rules:

- token has at least 32 bytes of cryptographic randomness before encoding;
- plaintext displayed once;
- only token hash stored;
- token name is unique within relevant scope;
- token determines server identity;
- revocation takes effect immediately for new requests;
- rotation must not silently revoke the old token before the operator can update source configuration unless explicitly chosen;
- significant actions are audited;
- token hashes and plaintext never appear in diagnostics or support bundles.

---

## 21. Authentication throttling

Login protection tracks failures by:

- administrator identifier without revealing whether it exists;
- client IP or trusted proxy client identity;
- bounded time windows.

Progression:

- initial failures: ordinary uniform response;
- repeated failures: increasing delay;
- sustained failures: temporary rejection window;
- success: controlled decay or reset.

Invariants:

- no permanent browser lockout;
- no CAPTCHA;
- no external service;
- no username-existence leak;
- significant failures become audit events;
- throttle state is bounded in memory or storage.

---

## 22. Security audit model

Conceptual fields:

```text
SecurityAuditEvent
- id
- occurred_at
- category
- action
- outcome
- actor_type
- administrator_id: optional
- server_id: optional
- source_id: optional
- safe_metadata_json
- request_id: optional
```

Audit categories include:

- authentication;
- session;
- administrator credential;
- ingestion token;
- source administration;
- retention settings;
- backup and restore;
- export;
- proxy-auth configuration;
- destructive operations.

Audit invariants:

- immutable after insertion;
- no raw application payload;
- no plaintext password;
- no session token;
- no ingestion token;
- no raw authorization header;
- no password hash or token hash;
- retained independently for at most 365 days and 100,000 records, whichever
  limit removes an older record first;
- application-log purge does not erase audit history.

---

## 23. Diagnostic event model

Diagnostic events describe Siftail operational state safely.

Examples:

- database entered read-only mode;
- retention cleanup failed;
- backup verification failed;
- queue saturation occurred;
- migration check failed;
- Fluent Bit test request rejected.

Conceptual fields:

```text
DiagnosticEvent
- id or bounded in-memory sequence
- occurred_at
- severity
- component
- safe_message
- request_id: optional
- recovered_at: optional
```

Invariants:

- no incoming message payload;
- no secret material;
- summaries come from a closed set of prewritten operational messages rather
  than caller-supplied text;
- component, category, severity, request ID, and optional recovery time are
  individually bounded and validated;
- bounded to the latest 100 records;
- in-memory diagnostic history is process-local and intentionally resets on
  restart;
- not a substitute for process stdout/stderr logs;
- may be stored in a dedicated bounded table or memory structure.

---

## 24. Historical search query model

Conceptual query:

```text
HistoricalQuery
- source_scope
- container_instance_id: optional
- from_us
- to_us
- levels[]
- streams[]
- contains_text: optional
- excludes_text: optional
- request_id: optional
- logger: optional
- http_method: optional
- http_status: optional
- error_type: optional
- cursor: optional
- direction
- limit
```

Invariants:

- bounded time range required;
- default range one hour;
- maximum range 31 days;
- range semantics are inclusive `from_us` and exclusive `to_us`;
- quick presets resolve to absolute timestamps and remain a historical snapshot
  until the operator chooses a new range;
- query state serializable into URL parameters;
- message contains/excludes use literal ASCII-case-insensitive substring
  matching; `%`, `_`, and the SQL escape character have no wildcard meaning;
- non-ASCII case variants are distinct in version one;
- no regex;
- no arbitrary query language;
- no arbitrary JSON path;
- result order deterministic;
- page size bounded, default 200;
- cursor pagination, not offset pagination;
- cursor is a versioned opaque value containing event time, event ID, and a
  fingerprint of the canonical query shape, protected against tampering;
- query export applies to complete matching range within export limits.

---

## 25. Live subscription model

Conceptual subscription:

```text
LiveSubscription
- id
- connection_context
- source_scope
- levels[]
- streams[]
- bounded_delivery_queue
- last_delivered_event_id
- dropped_or_truncated_count
```

Rules:

- only committed events are published;
- publisher never blocks the database writer on a slow client;
- each subscriber queue is bounded to 256 messages and 2 MiB;
- when a subscriber cannot keep up, Siftail sends a truncation control
  indication when possible and closes the subscription;
- reconnect begins with newly committed events; version one does not replay
  `Last-Event-ID` and reports that a gap may exist;
- live client truncation affects browser presentation only, not persisted history;
- deletion/purge sends control events to affected Live and History workspaces;
- live state is not mixed automatically into an active historical query.

---

## 26. Backup domain semantics

### 26.1 Full backup

Contains all database state required for complete restoration:

- administrator;
- server records;
- token hashes and metadata;
- sources and aliases;
- application events;
- settings;
- audit events;
- schema metadata.

A full backup does not contain recoverable plaintext tokens because Siftail never stores them.
Active and historical session rows are excluded. Restore always requires a
fresh login.

### 26.2 Configuration-only backup

Contains:

- administrator configuration and hash;
- server definitions;
- token metadata and hashes as explicitly designed;
- sources and aliases;
- settings;
- schema metadata.

Excludes:

- application log events;
- raw log attributes;
- security audit and diagnostic events;
- active and historical browser sessions;
- plaintext credentials.

### 26.3 Backup verification

Verification confirms:

- file readability;
- SQLite integrity or quick check;
- supported schema metadata;
- expected required tables;
- backup type;
- no incomplete backup marker.

### 26.4 Restore

Restore requires:

- ingestion stopped;
- valid verified backup;
- compatible schema version;
- explicit confirmation;
- current database preserved as rollback copy;
- controlled replacement;
- startup integrity check after restore;
- audit record where possible.

A configuration-only restore replaces the active database; it is not a merge or
import. A current binary automatically migrates an older supported restored
schema. An older binary must refuse a backup/database schema newer than it
supports.

Restore rolls password and ingestion-token state back to the backup point.
Previously revoked credentials may therefore become valid again; the operator
must review and rotate credentials after restoration when that risk matters.

---

## 27. Internal request correlation ID

Every HTTP request receives an internal request ID.

It is:

- generated by Siftail;
- returned in `X-Request-ID`;
- included in safe internal logs and error responses;
- not automatically persisted on every application event;
- distinct from application `request_id` attributes;
- not trusted solely because a caller supplied a header.

If caller-supplied IDs are accepted, they must be validated and clearly distinguished from generated IDs.

---

## 28. Domain error categories

Recommended domain/operational categories:

```text
Unauthorized
Forbidden
InvalidInput
UnsupportedFormat
EventTooLarge
BatchTooLarge
TooManyEvents
QueueFull
RateLimited
StorageUnavailable
StorageFull
SchemaTooNew
MigrationFailed
IntegrityFailed
Conflict
NotFound
AlreadyRevoked
BackupInvalid
RestoreUnsafe
```

Transport layers map these categories to HTTP or CLI responses. Raw SQLite errors and internal paths are not domain responses.

---

## 29. Ingestion outcome semantics

Canonical outcomes:

### 29.1 Accepted

- authentication valid;
- entire request decoded and normalized;
- source identities resolved;
- transaction committed;
- response success, typically `204 No Content`;
- committed events published to live broker after commit.

### 29.2 Permanent request rejection

Examples:

- malformed JSON;
- unsupported content type;
- invalid token;
- event too large;
- invalid field shape;
- excessive nesting.

The unchanged request should not be retried indefinitely.

### 29.3 Temporary rejection

Examples:

- queue full;
- temporary database unavailable;
- rate limit;
- graceful shutdown in progress.

Return retryable status such as `429` or `503` with safe hints.

### 29.4 Storage full

Return `507 Insufficient Storage`. Read access remains available where possible.

### 29.5 Atomicity

No response may report success if only a subset committed.

---

## 30. Source and data ownership

The operator owns:

- all application-event content;
- all source metadata;
- all configuration;
- all backup artifacts;
- all exported logs.

Siftail does not:

- claim ownership;
- transmit data externally;
- derive hosted analytics;
- retain maintainer-accessible copies;
- require a cloud account.

---

## 31. Data-lifecycle matrix

| Data class | Default lifecycle | Removal mechanism | Notes |
|---|---|---|---|
| Application events | 14 days or size cap | Retention, clear logs, remove source | High volume |
| Structured attributes | Same as event | Same as event | May contain sensitive data |
| Deployment boundaries | Tied to source/history | Source removal or cleanup | Not exported as logs |
| Source metadata | Inactive after 24h; cleanup eligible after 90d | Automatic if unprotected or explicit removal | Aliases protect source |
| Container instances | Derived, medium-lived | Source cleanup/removal | Ephemeral runtime identity |
| Sessions | Invalid immediately at expiry; row removed after 7d | Cleanup worker or revocation | Audit remains |
| Ingestion-token metadata | Until revoked and administratively removed | Token management | Plaintext never stored |
| Security audit | 365 days | Separate retention | Low volume |
| Diagnostic events | Latest bounded set | Ring/bounded cleanup | No application payload |
| Full backups | Operator controlled | External storage lifecycle | Protect externally |
| Configuration backups | Operator controlled | External storage lifecycle | No plaintext secrets |

---

## 32. Domain examples

### 32.1 Plain-text event

Incoming record:

```json
{
  "date": "2026-07-27T20:14:55.123456Z",
  "log": "database connection failed",
  "stream": "stderr",
  "application": "nextup",
  "service": "api",
  "container_id": "abc123"
}
```

Canonical result:

```text
event_at_us       = parsed date
received_at_us    = Siftail receive time
server            = token-bound server
application       = nextup
service           = api
container          = abc123
stream             = stderr
level_normalized   = unknown unless strong inference applies
level_original     = absent
message_raw        = database connection failed
message_text       = database connection failed
attributes_json    = remaining bounded fields
```

### 32.2 Structured JSON event

Application payload:

```json
{
  "level": "ERROR",
  "message": "checkout failed",
  "request_id": "req_123",
  "customer_id": 842,
  "duration_ms": 391
}
```

Canonical result:

```text
level_original    = ERROR
level_normalized  = error
message_text      = checkout failed
message_raw       = exact original JSON when available
request_id        = req_123 normalized field
attributes_json   = customer_id and other remaining fields
```

### 32.3 Legitimate repeated messages

Three events with identical message and timestamp granularity but no source event ID are stored as three events. Siftail does not collapse them.

### 32.4 Retry with stable ID

Two events from the same stable source with `source_event_id=evt-42` are treated idempotently according to the persistence rule. The same ID from a different stable source is unrelated.

### 32.5 Future container transition

The stable service `nextup/api` emits from container `api-old`, then from `api-new`.
Version one stores both container instances without creating a boundary. The reserved
post-dogfood feature could later create one inferred boundary at the first committed
event from `api-new`.

---

## 33. Domain acceptance checklist

Before a domain-affecting feature is complete, verify:

- terminology matches this document;
- event invariants remain true;
- timestamps are tested;
- ordering is deterministic;
- source trust cannot be forged through payload metadata;
- aliases do not alter identity;
- retries do not cause unsafe deduplication;
- batch atomicity is preserved;
- retention does not remove audit records;
- purge semantics match selected action;
- live publication occurs only after commit;
- backup semantics remain explicit;
- no false privacy or erasure claim is introduced;
- migrations preserve historical meaning.

---

## 34. Prohibited domain shortcuts

Coding agents must not:

- use container name as the only stable source identity;
- derive trusted server identity from payload fields;
- store only receive time;
- order solely by timestamp without a tie-breaker;
- infer error level from `stderr`;
- reconstruct multiline events across concurrent sources in Siftail;
- hash messages to remove duplicates;
- mutate historical events after persistence;
- implement source alias by rewriting stored events;
- delete audit history during log retention;
- call container transition a confirmed deployment;
- treat configuration backup as a complete disaster-recovery backup;
- expose raw log payloads in diagnostics;
- claim secure erasure after SQL deletion.

---

## 35. Final domain invariant summary

Siftail's domain can be summarized as follows:

> An authenticated server submits an atomic batch of records. Siftail normalizes each record into an immutable event with event time, receive time, stable source identity, preserved message content, independent stream and severity, and optional structured attributes. The batch becomes accepted only after durable commit. Committed events are deterministically searchable, may be streamed live, and expire oldest-first under global bounded retention. Administrative security state and audit history follow separate lifecycles.
