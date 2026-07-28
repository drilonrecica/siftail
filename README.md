# Siftail

This repository contains the implementation and authoritative specifications for
**Siftail**, a fast, private, self-hosted log viewer designed first for Coolify
and Fluent Bit.

The documents are written to be standalone. Coding agents should not need access to the planning conversation that produced them.

## Reading order

1. [`AGENTS.md`](AGENTS.md) — mandatory repository and implementation rules
2. [`DOMAIN.md`](DOMAIN.md) — canonical concepts, data semantics, and invariants
3. [`PRODUCT.md`](PRODUCT.md) — product vision, workflows, scope, and milestones
4. [`ARCHITECTURE.md`](ARCHITECTURE.md) — technical implementation and operations
5. [`DESIGN.md`](DESIGN.md) — interaction, visual, responsive, and accessibility specification
6. [`docs/decisions/`](docs/decisions/) — accepted reasoning for hard-to-reverse decisions
7. [`TASKS.md`](TASKS.md) — non-authoritative milestone dashboard and permanent
   task-ID registry; follow its link to the relevant
   [`docs/tasks/`](docs/tasks/) milestone record

## Document responsibilities

| Document | Authority |
|---|---|
| `PRODUCT.md` | What is being built, for whom, why, and what is excluded |
| `DOMAIN.md` | Event model, source identity, ordering, lifecycle, retention, and invariants |
| `ARCHITECTURE.md` | Go/HTMX/SQLite runtime, security, ingestion, queries, deployment, testing, and recovery |
| `DESIGN.md` | Screens, interactions, visual system, copy, keyboard behavior, mobile, and accessibility |
| `AGENTS.md` | Mandatory working rules for coding agents and contributors |
| Accepted ADRs | Why consequential technical and domain decisions were approved |
| `TASKS.md` | Milestone rollup, release state, permanent task-ID ranges, and links to detailed task records |
| `docs/tasks/<milestone>.md` | Individual task scope, dependencies, status, acceptance criteria, and evidence |

`PRODUCT.md` §19 remains the canonical roadmap and `ARCHITECTURE.md` §40
remains the canonical technical sequence. `TASKS.md` indexes the detailed
milestone records under `docs/tasks/`; together they turn those decisions into
reviewable work without overriding them.

## Fixed project identity

- Product: **Siftail**
- Tagline: **Fast, private logs for self-hosted apps.**
- CLI and repository: `siftail`
- Environment prefix: `SIFTAIL_`
- Default database: `siftail.db`
- License: Apache License 2.0

The name is explained subtly in selected copy: users can **sift through historical logs and tail live events**.

## Foundational decisions

- one Docker container;
- one long-running Go process, with focused short-lived CLI commands from the same binary;
- separate UI and ingestion listeners;
- SQLite with WAL and `synchronous=FULL` through `mattn/go-sqlite3`;
- Go templates, HTMX, focused vanilla JavaScript, and plain CSS;
- no production Node.js runtime;
- one local administrator;
- one independently revocable ingestion token per server;
- commit-before-acknowledgement;
- batch atomicity;
- bounded memory and disk;
- no telemetry or external runtime services;
- Coolify-first, generic Fluent Bit HTTP best effort;
- metrics, traces, multi-tenancy, Kubernetes, dashboards, and AI analysis are out of scope.

## Development

The supported Go toolchain is declared by [`go.mod`](go.mod), which is also the
version source used by CI. CI enables CGO and installs the C compiler required
by the selected SQLite driver when that driver is introduced in SFT-005.

Common local checks:

```bash
make fmt        # go fmt ./...
make fmt-check  # verify formatting without modifying files
make vet        # go vet ./...
make test       # go test ./...
make race-test  # go test -race ./...
make build      # build the siftail binary
make check      # formatting, vet, tests, and metadata-bearing build
make frontend-check # list and validate development-only browser tests
make playwright     # run the Chromium browser/security suite
```

Measured engineering baselines are recorded under
[`docs/performance/`](docs/performance/); they describe their exact hardware and
method and are not cross-machine performance guarantees.

Browser verification requires Node.js 22 or newer only on the development/CI
host:

```bash
npm ci --ignore-scripts
npx playwright install --with-deps chromium
make frontend-check
make playwright
```

An installed Chromium may be used locally with
`SIFTAIL_PLAYWRIGHT_CHROMIUM=/path/to/chromium make playwright`. Node,
Playwright, axe, browsers, and their reports are not production runtime
dependencies. The 0.2.0 method, requirement matrix, and measured limitations
are recorded in [`docs/release/0.2.0-gate.md`](docs/release/0.2.0-gate.md).

No process secret environment variable exists yet. When one is introduced,
direct and `_FILE` forms will be mutually exclusive; `_FILE` input removes only
trailing CR/LF bytes and never appears in sanitized configuration or logs.

### Ingestion compatibility

The authenticated endpoint is `POST /api/v1/ingest`. It accepts Fluent Bit
`json` as one object or an array of objects and `json_lines` as NDJSON, with an
optional `Content-Encoding: gzip`. The application independently caps compressed
and decompressed input, record count and size, JSON depth, and retained bytes.
Malformed final records, duplicate keys, trailing data, and non-object records
reject the complete request. A `204 No Content` response means the complete
request committed durably to SQLite and includes
`X-Siftail-Ingest-Outcome: committed`. Capacity and temporary database failures
return `503`; storage-full failures return `507`; a conflicting reuse of a
stable source event ID returns `409`.

The date-stamped fixtures pin Coolify `v4.1.1` with its shipped Fluent Bit
`2.0` image reference (observed as `v2.0.14`) and generic Fluent Bit `v5.0.9`;
they do not imply support for every release. Coolify's dotted hierarchy aliases
and Docker's `source` stream field are compile-time compatibility rules. The
tested configuration, upstream evidence, retry/buffering limits, and mandatory
Siftail self-exclusion are recorded in
[`docs/integrations/coolify-fluent-bit-compatibility.md`](docs/integrations/coolify-fluent-bit-compatibility.md).

### Generated source configuration and guided test

Set `SIFTAIL_INGEST_PUBLIC_URL` to the complete operator-reachable endpoint,
including `/api/v1/ingest`, for example:

```env
SIFTAIL_PUBLIC_URL=https://logs.example.com
SIFTAIL_INGEST_PUBLIC_URL=https://ingest.logs.example.com/api/v1/ingest
```

The value must use HTTP or HTTPS, contain no credentials, query, or fragment,
and identify exactly the ingestion path. It is never derived from request or
forwarded headers.

After creating a token in the authenticated Server page, the same no-store
one-time screen offers:

- pinned Coolify and generic Fluent Bit configuration;
- an external curl command;
- exact `COOLIFY_APP_NAME=siftail-self` recursive-ingestion prevention;
- bounded 256 MiB source-side retry storage and its data-loss warning; and
- an explicit guided committed-receipt test.

The plaintext exists in one password field. Configuration and curl previews
contain a nonsecret placeholder; their copy buttons substitute the token only
into the clipboard. Page exit clears the field and previews, browser history is
replaced with the nonsecret Server detail URL, and later pages cannot recover
the token.

The guided button sends one bounded synthetic event to the configured public
ingestion URL. It bypasses environment proxy variables, refuses redirects, and
never logs or returns the token. Only `204 No Content` with Siftail's
`X-Siftail-Ingest-Outcome: committed` marker is shown as committed.
Connection failure, authentication rejection, retryable `429`/`503`/`507`, and
other rejection remain distinct. A committed test creates one event under
`siftail-test / setup / guided-ingestion / probe`; it is an application event
and follows ordinary retention.

The focused CLI can instead emit exactly one complete artifact:

```bash
./siftail token create --server 1 --name coolify --output coolify
./siftail token create --server 1 --name generic --output generic
./siftail token create --server 1 --name delivery-test --output curl
```

Each generated stdout stream contains the new token exactly once and is
therefore one-time secret material. Avoid shell history, logs, and
world-readable files. The default `--output token` preserves the ordinary
one-time token response.

### Command-line ingestion smoke test

This focused workflow needs the `siftail` binary, a POSIX shell, `curl`, and
`gzip`; it does not require an SQLite client or arbitrary SQL. Use disposable
paths and ports when running it locally:

```bash
export SIFTAIL_DATA_DIR=/tmp/siftail-smoke
export SIFTAIL_UI_ADDR=127.0.0.1:18080
export SIFTAIL_INGEST_ADDR=127.0.0.1:18081

./siftail server create --name Smoke
TOKEN_OUTPUT=$(./siftail token create --server 1 --name smoke)
while IFS= read -r LINE; do
  case "$LINE" in
    "token (shown once): "*) SIFTAIL_SMOKE_TOKEN=${LINE#*: } ;;
  esac
done <<EOF
$TOKEN_OUTPUT
EOF
unset TOKEN_OUTPUT LINE

./siftail serve &
SIFTAIL_SMOKE_PID=$!
trap 'kill "$SIFTAIL_SMOKE_PID" 2>/dev/null; wait "$SIFTAIL_SMOKE_PID" 2>/dev/null' EXIT
until curl --silent --fail "http://$SIFTAIL_UI_ADDR/health/live" >/dev/null; do sleep 0.1; done

curl --fail-with-body \
  -H "Authorization: Bearer $SIFTAIL_SMOKE_TOKEN" \
  -H "Content-Type: application/x-ndjson" \
  --data-binary '{"timestamp":"2026-07-28T08:00:00Z","application":"smoke","service":"api","log":"plain smoke event"}' \
  "http://$SIFTAIL_INGEST_ADDR/api/v1/ingest"

printf '%s\n' '{"date":"2026-07-28T08:00:01Z","coolify_application_name":"smoke","coolify_service_name":"worker","log":"gzip smoke event"}' |
  gzip -c |
  curl --fail-with-body \
    -H "Authorization: Bearer $SIFTAIL_SMOKE_TOKEN" \
    -H "Content-Type: application/x-ndjson" \
    -H "Content-Encoding: gzip" \
    --data-binary @- \
    "http://$SIFTAIL_INGEST_ADDR/api/v1/ingest"

kill "$SIFTAIL_SMOKE_PID"
wait "$SIFTAIL_SMOKE_PID"
trap - EXIT
unset SIFTAIL_SMOKE_TOKEN SIFTAIL_SMOKE_PID
```

Both ingestion calls must return HTTP `204`; that status is the commit
confirmation. Remove the disposable data directory after the process exits.

### Administrator recovery CLI

Siftail has exactly one case-sensitive local administrator. Create it locally:

```bash
./siftail admin create --username Admin
```

Reset a lost password with:

```bash
./siftail admin reset-password
```

On a terminal, both commands read and confirm the password without echo. For
deliberate automation they accept exactly two standard-input lines: password
then confirmation. Passwords are never accepted as command arguments. When
Siftail is active the commands use the owner-only control socket; when it is
stopped they open the local database directly. Usernames are 3–64
case-sensitive ASCII letters, digits, `.`, `_`, or `-`. Passwords are 12–1024
valid UTF-8 bytes and are not trimmed or normalized; CR/LF line terminators from
the two-line input are removed.

Schema migration `0002` adds the single administrator record and migration
`0003` adds hashed, bounded browser sessions. Migration `0004` adds immutable,
bounded security-audit storage. Upgrades apply them transactionally and
preserve ingestion, administrator, session, source, and event data. Older
binaries refuse databases with newer schema versions.

All browser sessions can be invalidated locally:

```bash
./siftail sessions revoke-all
```

The command uses the same online owner-only control path or stopped-server
offline path. It prints only the number revoked, never a session token or hash.
Password reset revokes every active session in the same database transaction.
Sessions have a 14-day absolute lifetime, a 7-day idle lifetime, and a maximum
of 64 active records; invalid records are removed after a 7-day grace period by
bounded hourly cleanup.

### Browser security boundary

The UI exposes `/login`, creates sessions through `POST /session`, and signs out
through `POST /session/logout`. Successful sign-in always rotates to a newly
issued opaque session and continues to `/logs` or a validated local return path.
Failures use uniform copy; the bounded in-memory client/account throttle begins
temporary `429` responses on the fifth failure and never requires an external
service.

Every authenticated browser mutation requires the session, an HMAC-derived CSRF
token, form content type, and an exact allowed Origin or Referer. Authenticated
responses use `Cache-Control: no-store`. UI responses set a self-only CSP,
clickjacking, MIME, referrer, permissions, opener, and resource-policy headers;
HTTPS public URLs also enable HSTS and Secure session cookies.
UI responses use a same-origin-only referrer policy so native forms can satisfy
exact Origin/Referer validation without sending referrers cross-origin.

Set `SIFTAIL_PUBLIC_URL` to the operator-facing HTTP(S) origin. Forwarded
client, scheme, and host metadata is considered only when the immediate peer is
inside `SIFTAIL_TRUSTED_PROXY_CIDRS`; direct clients cannot spoof it. Siftail
does not treat `Remote-User`, `X-Forwarded-User`, or other identity headers as
authentication.

### Embedded browser interface

Login and the authenticated application shell are rendered from escaped Go
templates embedded in the binary. CSS, focused preference JavaScript, the
favicon, and HTMX are local embedded assets; ordinary operation makes no CDN,
font, analytics, or other outbound browser request. Login exposes no setup
form, uses uniform associated errors, and points recovery to the local CLI
without host detail.

The shell is dark-first with a complete light theme, visible focus, a skip
link, reduced-motion handling, and responsive layouts for emergency mobile
inspection. Theme (`System`, `Dark`, or `Light`) and density (`Compact` or
`Comfortable`) are presentation-only browser-local preferences. HTMX history
snapshots are disabled both declaratively and with a zero-entry history cache,
so authenticated DOM is refetched instead of restored from a snapshot.

### Historical query compatibility

History URLs use `mode=history` and absolute UTC `from`/`to` endpoints with
half-open `[from,to)` semantics. Presets (`15m`, `1h`, `6h`, `24h`, and `7d`)
are resolved immediately into those absolute endpoints, so a bookmarked page
does not drift with the clock. The default range is one hour, the maximum is
31 days, and page limits are 200 by default and at most 500.

The URL can carry a Server and source hierarchy, exact container instance,
comma-separated canonical levels and streams, literal `contains` and
`excludes` terms, selected exact common fields, traversal direction, and an
opaque cursor. Text filters are bounded to 512 UTF-8 bytes. Complete query
state is serialized canonically; credentials, session tokens, CSRF values, and
the local cursor key are never part of the URL.

History cursors are versioned URL-safe values authenticated with HMAC-SHA256.
They bind `(event_at_us,id)`, direction, and the fingerprint of the complete
canonical query excluding only the cursor itself. Siftail rejects altered
cursors and cursors reused with another query. A random 32-byte key is created
through the database mutation coordinator and persisted in `settings`; this
contract adds no schema migration, dependency, or external key service.

Historical reads use explicit bound SQLite queries, fetch only `limit+1`, and
retain the canonical `event_at_us DESC, id DESC` order across equal timestamps.
Known filters compose without accepting SQL syntax; message search uses
SQLite's ASCII-only `lower()` with `instr()`, so `%`, `_`, and backslash are
literal and non-ASCII case variants remain distinct. Source-catalog reads
include inactive retained sources and are capped at 10,000 options per level.
The measured 100k/1M development baseline and query-plan review are recorded in
[`docs/performance/history-queries.md`](docs/performance/history-queries.md).

`/logs` redirects relative/default requests to an absolute canonical last-hour
URL, then renders the URL-owned History workspace. `/logs/rows` updates only
the focused History region and pushes the canonical URL; HTMX snapshots remain
disabled, so Back and Forward refetch authenticated state. Filters cover the
source hierarchy, presets/custom UTC range, canonical levels, independent
streams, 400 ms literal contains/excludes search, exact common fields, and
container instance.

“Load older” uses the protected cursor to append rows out-of-band while
replacing only its pagination control. Duplicate requests are disabled, search
focus and scroll context are preserved, and summaries never issue a total
count. List rows select at most a 2,048-character message preview and omit raw
payload, attributes, and detail-only fields. The browser keeps at most 1,000
History rows and announces when its presentation cap requires refinement;
stored events remain unchanged.

Each retained row can expand independently through the authenticated
`/logs/events/{id}` route. The escaped inline view shows the message, full
source hierarchy, event and receive timing, level and stream, normalized common
fields, recursively ordered JSON attributes, and the raw payload. Initial
message, attributes, and raw sections are each capped at 16 KiB and report the
exact stored byte size; an explicit in-place action may retrieve the complete
schema-bounded event. There is no detail download or export route. Copy actions
read `textContent` through the browser clipboard API, and deletion or an unknown
ID returns the same safe not-found fragment.

The bounded export store and stable `siftail-text-v1`/NDJSON schema-1 contracts
are implemented for the next audited workflow, but remain unexposed until that
workflow is complete. They reuse the typed History filters, stream canonical
newest-first events with one-row encoder state, and fail rather than return a
partial result at 100,000 events, 256 MiB, 31 days, or the query deadline. See
[`docs/operations/export-formats.md`](docs/operations/export-formats.md).

### Source catalog

The authenticated `/sources` page lists every discovered stable source,
including inactive sources and sources with no retained events. Each row shows
its trusted Server, exact project/environment/application/service hierarchy,
alias state, fixed 24-hour active/inactive state, first/last observation, and
whether retained logs remain. Reads use source-ID keyset pagination with 100
rows by default and at most 200; they do not run an event count.

Source detail keeps stable identity keys separate from display labels and
aliases. Container IDs and names appear only as ephemeral observations under
their source. At most the 200 most recently seen container observations are
loaded, with an explicit notice when older observations exist. “Open logs”
returns to History with the exact Server and four stable source-key dimensions.
The measured 10,000-source plan and latency baseline is recorded in
[`docs/performance/source-catalog.md`](docs/performance/source-catalog.md).

An alias may be set or removed from source detail and changes presentation
only. Every mutation uses the single database coordinator and the browser's
authenticated CSRF plus exact-origin protections. `Clear logs` requires typing
the displayed source name and deletes only application events at or below a
captured watermark in 10,000-event transactions; it keeps the stable source,
alias, Server, and container observations. `Remove source` requires the
stronger `remove <display name>` phrase, deletes watermark-bounded events,
aliases, unreferenced container observations, and source metadata when
referentially safe, but never deletes the trusted Server. Events committed
after either watermark remain, and an active sender may rediscover a removed
source.

Both destructive actions notify connected Live clients without waiting for
them and clearly avoid any promise of forensic erasure. Their successful
mutations and immutable audit records share the relevant coordinator
transaction.

### Browser Server and token management

The authenticated `/servers` workspace creates trusted Servers and manages
their independently revocable ingestion tokens through the same
coordinator-owned store used by the CLI. It shows only nonsecret token names,
fingerprints, creation/last-use/revocation times, active state, source count,
and last event time.

Token creation and rotation return one no-store response with a masked
plaintext credential, show/hide and copy controls, and an explicit Done action.
Siftail stores only the token hash and cannot display the plaintext again.
Creating a replacement leaves the old token active until it is explicitly
revoked; revocation takes effect at authentication and is rechecked at durable
batch commit. Successful batch commit updates token last-use metadata in the
same transaction. The complete threat and exposure boundary is documented in
[`docs/security/ingestion-tokens.md`](docs/security/ingestion-tokens.md).

### Security-audit storage

Schema migration `0004` adds the immutable, separately retained security-audit
store used by the hardening milestone. It keeps at most 100,000 events, defaults
to 365-day retention, removes oldest records in transactions of at most 1,000,
and is never touched by application-log retention. Safe metadata is limited to
whitelisted nonsecret fields and 2 KiB of JSON; passwords, tokens, hashes,
authorization headers, and application payloads have no accepted metadata
field. See [`docs/security/audit.md`](docs/security/audit.md) for the schema,
atomic-write contract, recorded action taxonomy, measurements, and limitations.

The migration is additive and automatic. It preserves schema-1/2/3 data, but
an older Siftail binary correctly refuses the resulting schema-4 database.
The application records sign-in/session, administrator recovery, Server/token,
source lifecycle, and retention actions. Successful mutations are recorded
atomically; rejected sign-ins use their own bounded transaction. The
authenticated, no-store `/audit` workspace provides a maximum 366-day range,
exact category/action/outcome filters, and 100-row keyset pages without
exposing credentials or application payloads. A recoverable worker applies the
365-day audit retention independently at startup and hourly.

### Browser retention settings

The protected `/settings` workspace configures the two global application-log
retention thresholds as one atomic policy. Age accepts 1–3,650 whole days and
the active SQLite footprint target accepts 1–1,024 whole GiB; defaults are 14
days and 4 GiB. An invalid or failed save leaves both prior values unchanged,
and settings survive process restart in the existing SQLite `settings` table.

Whichever threshold is reached first is authoritative. Cleanup is oldest-first
by the earlier of event time and receive time, then event ID, and size cleanup
measures the main database plus WAL and shared-memory files. These controls
apply only to application events: they do not remove Servers, tokens, aliases,
sessions, settings, or security audit history. Retention is not forensic
erasure and does not control backups, snapshots, or unrelated host files.

The lifecycle-owned cleanup worker runs at startup and hourly. It deletes no
more than 10,000 events per transaction through the single writer coordinator,
then performs bounded incremental vacuum and controlled WAL checkpointing.
Size cleanup stops for the current run when a reader prevents checkpointing,
instead of deleting speculatively, and retries later. Connected Live views
receive a post-commit retention notice; deletion never waits for a browser.
Measured reclamation and writer-interference evidence is recorded in
[`docs/performance/retention.md`](docs/performance/retention.md).

### Storage-full recovery

A failed SQLite commit caused by full database, WAL, temporary storage, or host
filesystem capacity is never acknowledged. Full storage returns `507`; other
temporary database failures return `503`. After token authentication, Siftail
rejects requests during the known degraded state before decoding or queueing
their payloads. History and authenticated operational reads continue where
SQLite permits, liveness remains healthy, and readiness reports `503`.

While degraded, Siftail tests writability every five seconds with one bounded
64 KiB coordinator-serialized transaction. It restores readiness only after that
transaction commits. It never deletes or recreates the database, starts an
overflow spool, or claims recovery because the process restarted. Bounded
retention may release application-event pages first. The operator procedure,
failure categories, and safe escalation boundary are documented in
[`docs/operations/storage-full-recovery.md`](docs/operations/storage-full-recovery.md).

### Health and status

The unauthenticated UI-listener endpoints are deliberately minimal:

```text
GET /health/live   process HTTP responsiveness
GET /health/ready  migration/integrity startup, writer, writable storage,
                   shutdown, and critical-degradation readiness
```

Liveness remains `200` during a transient database failure to avoid restart
loops. Readiness returns `503` with only `not ready` when the writer is
unavailable, storage cannot commit, shutdown has begun, or retention exhausts
application events without reaching the configured size target. A bounded
recovery-probe commit recovers database readiness; retention degradation clears
only after a successful cleanup result.

The authenticated `/status` page shows version, uptime, architecture,
schema and SQLite versions, DB/WAL/SHM sizes, the configured retention limit,
index-backed oldest/newest times, queue gauges, current-process ingestion
totals and 60-second rate, last cleanup, last safe database category, the last
manual database check, and at most 100 sanitized diagnostics. Diagnostic
summaries come from a closed internal list and may include only typed
severity/component/category, an internal request ID, and recovery time. The
process-local ring resets on restart. It contains no messages, raw payloads,
attributes, credentials, hashes, paths, authorization headers, or environment
dump, and nothing is reported externally.

### Database checks and local diagnostics

Run a bounded quick database check with:

```bash
./siftail database check
```

When Siftail is active, this uses the owner-only control socket, runs
`quick_check` on the bounded read pool, and orders a passive WAL checkpoint
through the database maintenance coordinator. The authenticated Status page
offers the same check through `Run safe database check`.

For a stopped server, the command opens the existing database read-only. It
does not create, migrate, checkpoint, or rewrite the database. A full
integrity scan is available only while stopped:

```bash
./siftail database check --full
```

Output is a fixed path-free report containing schema compatibility, SQLite
version, integrity, durability pragmas, the source of the writability result,
checkpoint state, page/free counts, and DB/WAL/SHM byte counts. Stopped-server
writability is an advisory filesystem-access result, not proof that a future
SQLite commit will succeed. Corrupt, newer-schema, busy, canceled, and
unavailable failures print safe categories and return failure without
recreating the database.

The latest process-local diagnostics are available only while the server is
active:

```bash
./siftail diagnostics
```

This prints at most 100 validated entries through the owner-only control
socket. There is no arbitrary SQL command, raw process-log export, support
bundle, public administration endpoint, or outbound reporting.

### Verified full and configuration-only backups

With Siftail running, create a full backup through the owner-only control
socket:

```bash
./siftail backup --output /backups/siftail-full.sqlite
```

Create a configuration-only artifact, or verify either artifact type without
applying it:

```bash
./siftail backup --configuration-only --output /backups/siftail-config.sqlite
./siftail backup verify /backups/siftail-config.sqlite
```

The authenticated Backup workspace exposes the same single-job operation with
bounded page/row progress, read-only verification, and a sanitized final result.
The destination directory
must already exist on protected storage and have room for the logical database
plus 5% or at least 1 MiB of slack. Existing output files are never overwritten.
Siftail creates a hidden mode-`0600` staging file in the same directory, copies
the active WAL database with SQLite's online backup API, removes all browser
sessions from the artifact, writes versioned completion metadata, runs full
integrity/schema/table/session verification, streams a SHA-256 checksum, syncs
the data, and only then atomically exposes the final filename.

Ingestion may continue during the snapshot. A concurrent committed batch is
captured wholly before or after SQLite's consistent snapshot boundary. Failure,
cancellation, full destination, invalid path, failed verification, or a
no-overwrite race removes Siftail's partial output. Scheduling, off-host
copying, lifecycle, and encryption of the verified artifact remain operator
responsibilities. Full backups contain retained application logs, password and
token hashes, configuration, sources, and audit history, so protect them as
sensitive data. Configuration-only backups still contain password and token
hashes, Servers, settings, and source presentation configuration, but exclude
events, container observations, audit history, diagnostics, and sessions.
Restore replaces the database with the saved configuration and empty history;
it is not a merge. See
[`docs/operations/full-backups.md`](docs/operations/full-backups.md).

### Stopped-server restore

Stop Siftail, then restore a verified full or configuration-only artifact with
explicit confirmation:

```bash
./siftail restore --confirm RESTORE /backups/siftail-full.sqlite
```

The command and server share an exclusive maintenance lock, so they cannot
open the database concurrently. Restore verifies and stages the artifact,
captures the current database and committed WAL state as a verified
session-free `siftail.db.rollback`, atomically installs the new database,
applies supported forward migrations, removes artifact metadata and sessions,
records a safe local-operator audit, and runs full open/closed integrity and
critical-schema checks. The previous managed rollback is replaced only after
the restored database passes.

Restoring configuration replaces the complete database with empty log history;
it does not merge. Password and ingestion-token state roll back to the artifact
point, so review credentials afterward. A fresh login is always required. Copy
the managed rollback to a separate protected path before restoring that copy.
See [`docs/operations/restores.md`](docs/operations/restores.md).

### Current dependency rationale

- `github.com/mattn/go-sqlite3` v1.14.48 is the accepted SQLite driver. It
  bundles SQLite, is MIT licensed, requires CGO and a C toolchain, and adds no
  runtime service. Production images must therefore build explicitly for
  `linux/amd64` and `linux/arm64`. On linux/amd64 with Go 1.25, the initial
  linked database lifecycle increased the unstripped binary from 9,274,021
  bytes to 13,291,176 bytes (+4,017,155 bytes) in the SFT-005 linux/amd64
  verification build. Database or driver security updates require normal
  dependency review and a rebuilt container.
- `golang.org/x/sync/errgroup` provides owned cancellation and first-error
  propagation for the application’s critical components. The standard library
  does not provide the same combined group/error boundary. It has no transitive
  module dependencies and adds no runtime service or public API. Only its
  group/cancellation implementation is linked; binary-size impact has not yet
  been measured. It is maintained by the Go project under the BSD 3-Clause
  license.
- `golang.org/x/crypto` v0.54.0 provides the reviewed Argon2id implementation;
  the standard library has no Argon2 password hash. `golang.org/x/term` v0.45.0
  provides no-echo terminal password input. Both are maintained by the Go
  project under BSD 3-Clause licenses. `x/term` brings `golang.org/x/sys`
  v0.47.0 as its only module dependency; these packages add no runtime service,
  outbound call, or public network API. On this linux/amd64 Go 1.26.5
  development build, linking the administrator implementation increased the
  unstripped binary from 14,523,440 to 14,757,904 bytes (+234,464 bytes,
  about 1.6%). An Argon2id operation deliberately uses 32 MiB for three
  iterations with parallelism one; a process-wide two-operation semaphore caps
  concurrent configured hash memory at 64 MiB plus implementation overhead.
  Password hashing is request-driven and adds no persistent idle allocation.
  Security updates to these modules require ordinary dependency and release
  review.
- HTMX 2.0.10 is vendored as its exact 51,238-byte minified distribution under
  the Zero-Clause BSD license. It has no transitive package dependencies and
  is served only as an embedded same-origin asset; the standard library does
  not provide its server-driven fragment interaction model. HTMX adds no Go
  API, production Node runtime, runtime service, outbound request, or idle
  server allocation. On this linux/amd64 Go 1.26.5 development build, all
  SFT-021 embedded UI code and assets together increased the unstripped binary
  from 18,205,480 to 18,292,368 bytes (+86,888 bytes, about 0.48%) in builds
  with the build ID removed for comparison. Browser
  memory is limited to the loaded document and static assets; later History
  and Live tasks retain their separate DOM caps. Security updates require
  explicitly replacing the vendored file, license review, and integrity tests.

## Important note

The specification documents are authoritative inputs to the implementation, not
generated descriptions of it. When implementation intentionally changes an
accepted decision, update the relevant specification, tests, migrations, and—
when the decision is consequential—an ADR in the same change.
