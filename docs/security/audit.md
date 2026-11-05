# Security-audit storage boundary

**Reviewed:** 2026-11-05
**Scope:** SFT-038 storage and migration, SFT-039 privileged-action
attribution, cleanup lifecycle, and authenticated inspection, plus SFT-105
aggregated sign-in rejections and reserved capacity (ADR 0010)

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
summaries, formats, backup basenames, and the fields of an aggregated
rejection window (window start, four reason counts, distinct-client count,
most frequent client summary and its count). A backup name must be a safe basename,
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
transaction, such as an aggregated sign-in rejection window. A privileged
mutation is not reported as successful when its required audit insert fails.

Every insertion applies the 100,000-record cap in its transaction. Both counts
come from trigger-maintained one-row tables (`security_audit_event_count` from
migration 0009, `security_audit_unauthenticated_count` from migration 0010),
so each check is a primary-key read.

- An administrator, local-operator, or system insert deletes the oldest
  `(occurred_at_us, id)` records overall when the total exceeds 100,000.
- An `unauthenticated` insert has a reserved sub-cap of 10,000 rows. It
  deletes only the oldest `unauthenticated` rows, through the partial index
  `security_audit_events_unauthenticated_idx`, to satisfy both the sub-cap and
  the total cap. Unauthenticated writes therefore never evict administrator,
  local-operator, or system rows, which always have at least 90,000 rows of
  capacity.

Explicit cleanup applies the configured age first, then the unauthenticated
sub-cap, then count overflow, and deletes no more than 1,000 records per
transaction. Default and maximum audit age are 365 days. Application-log
age/size cleanup, Clear logs, and application-event retention do not delete
audit rows.

Per-attempt `sign_in` rejection rows written before migration 0010 count
toward the sub-cap and age out normally. If they exceed it, each later
unauthenticated insert and each cleanup pass removes at most 1,000 of the
oldest, so a large legacy excess converges in bounded transactions.

The application root runs one owned audit cleanup worker immediately at
startup and then hourly. Each pass uses the database coordinator and deletes
at most 1,000 rows. Cleanup failure is reported only as a sanitized component
and error category; it neither terminates the process nor emits record
contents.

## Aggregated sign-in rejections (ADR 0010)

Rejected sign-in attempts are not recorded one row each. A sign-in request
that ends in a rejection never opens a coordinator transaction: the throttle
and admission decisions (`docs/security/sign-in-throttling.md`) happen before
any write, and the request only increments an in-memory counter. Four closed
reasons are counted:

| Reason | Meaning |
| --- | --- |
| `invalid_credentials` | the password was evaluated and did not match, or the username is unknown |
| `throttled` | the client or account key was inside its retry delay |
| `in_flight` | the key already had an attempt in flight |
| `overloaded` | the bounded Argon2 admission queue was full or its wait expired |

Windows are aligned 5-minute UTC intervals. For each window that had at least
one rejection, one owned writer records one `sign_in.rejected_summary` row
(category `authentication`, outcome `rejected`, actor `unauthenticated`, no
request ID) through the coordinator when the window ends. Its `occurred_at` is
the window end, and its metadata holds:

- `window_started_at`, the window start in exact UTC form;
- `invalid_credentials_count`, `throttled_count`, `in_flight_count`, and
  `overloaded_count`;
- `distinct_client_count`, the number of distinct client throttle keys (IPv4
  /32 or IPv6 /64), exact up to 1,024 and written as `1024+` beyond;
- `top_client` and `top_client_count`, the most frequent tracked client key
  and its attempts (ties go to the lexically smaller key).

Memory is bounded: at most 1,024 client keys are tracked per open window, and
a closed window keeps only its summary. If storage refuses the write, closed
windows are retried at the next window end, up to 12 pending windows (one
hour); older ones are dropped and the failure is reported as a sanitized
`audit`/`sign_in_audit` diagnostic and process-log warning. On graceful
shutdown the application root flushes the open window after the UI server has
drained and before the coordinator stops. A crash loses at most the open
window; that loss is accepted.

Successful sign-ins remain individual `sign_in` rows with the administrator
actor, written atomically with the new session, and so does every privileged
and local-operator action.

The Audit page renders an aggregate as one sentence, for example "Rejected
sign-ins 2026-11-05 12:00–12:05 UTC · 4 invalid credentials · 99,990
throttled · 5 concurrent attempt · 1 overloaded · 1,024 distinct clients (at
least) · most frequent 198.51.100.7/32 (50,000)", with the actor shown as
"unauthenticated (5-minute aggregate)". Exact per-attempt timing within a
window is not retained.

The planned `session.revoke_others` action for "Sign out other sessions" is
added by SFT-106.

## Recorded actions

The current implementation records these normalized actions:

| Action | Outcome and actor |
| --- | --- |
| `sign_in` | successful administrator sign-in (rows from before migration 0010 may also be rejected unauthenticated attempts) |
| `sign_in.rejected_summary` | rejected, unauthenticated: one aggregated window of rejected sign-in attempts |
| `session.revoke`, `session.revoke_all` | successful administrator or local-operator revocation |
| `administrator.create`, `administrator.password_reset` | successful local-operator recovery action |
| `server.create` | successful administrator or local-operator creation |
| `ingestion_token.create`, `ingestion_token.revoke` | successful administrator or local-operator token lifecycle action |
| `source.alias_set`, `source.alias_remove` | successful administrator presentation change |
| `source.clear_logs`, `source.remove` | successful administrator destructive source action with a bounded affected count |
| `retention.update` | successful administrator retention-policy change |

Browser attribution carries the authenticated administrator ID and internal
request ID. Maintenance CLI and control-socket actions are attributed to the
local operator. Internal direct store calls with no explicit attribution are
classified as system actions.

Backup, restore, export, and proxy-auth configuration actions have reserved
categories and safe metadata but no current mutation path. Their later
implementation tasks must use `RecordTx` for transactional success events and
must record only the defined nonsecret outcome; this task does not create
placeholder events for operations that do not exist.

## Authenticated inspection

`GET /audit` is session-protected and always `no-store`. Its canonical query
requires an explicit UTC `[from,to)` range of no more than 366 days and
optionally accepts one exact category, action, and outcome. Results are newest
first and keyset-paginated at 100 rows; the table has its own focusable
horizontal scroll region for narrow emergency inspection. The page renders
only typed identifiers and validated safe metadata through `html/template`.

## Migration and compatibility

Fresh creation and immutable schema-1 through schema-10 fixture tests reach
the current schema (originally schema 4 for this feature; migrations 0009 and
0010 add the counters and the partial index). Representative Server, administrator, session,
source, application-event, settings, and version-applicable audit data survive;
integrity and production critical reads/writes pass. Migration failure rolls
back its version, and older binaries refuse a newer schema rather than deleting,
recreating, or down-migrating the database. See
[`../operations/upgrade-compatibility.md`](../operations/upgrade-compatibility.md).

The migrations are additive and do not rewrite existing rows. They add bounded
future disk growth: at most 100,000 audit rows (at most 10,000 of them
unauthenticated, each with one partial-index entry), each with at most 2 KiB of
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

The SFT-039 regression run used `-benchtime=1s -count=3`: median record-at-cap
latency was 1.300 ms, the newest 100-row page was 349.295 µs, and the deliberate
oldest category scan was 24.859 ms. Privileged-action wiring did not alter the
store algorithm or add an index.

## Current limitation

Audit history is an operational accountability record, not a forensic
tamper-proof ledger. A user with filesystem access can alter or remove the
database, and the bounded age/count policy intentionally deletes old entries.
Failed requests rejected by shared browser security middleware before a
feature action is reached are not separately audited.
