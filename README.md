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
request committed durably to SQLite. Capacity and temporary database failures
return `503`; storage-full failures return `507`; a conflicting reuse of a
stable source event ID returns `409`.

The fixtures track Fluent Bit's official
[HTTP output](https://docs.fluentbit.io/manual/data-pipeline/outputs/http) and
Coolify's documented
[custom Fluent Bit drain](https://coolify.io/docs/knowledge-base/drain-logs).
Coolify aliases are compile-time compatibility rules, including the documented
`coolify.app_name` field.

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
`0003` adds hashed, bounded browser sessions. Upgrades apply them transactionally
and preserve ingestion and administrator data. Older binaries refuse databases
with newer schema versions.

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
