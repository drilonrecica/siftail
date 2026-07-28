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
7. [`TASKS.md`](TASKS.md) — non-authoritative implementation dependencies and status

## Document responsibilities

| Document | Authority |
|---|---|
| `PRODUCT.md` | What is being built, for whom, why, and what is excluded |
| `DOMAIN.md` | Event model, source identity, ordering, lifecycle, retention, and invariants |
| `ARCHITECTURE.md` | Go/HTMX/SQLite runtime, security, ingestion, queries, deployment, testing, and recovery |
| `DESIGN.md` | Screens, interactions, visual system, copy, keyboard behavior, mobile, and accessibility |
| `AGENTS.md` | Mandatory working rules for coding agents and contributors |
| Accepted ADRs | Why consequential technical and domain decisions were approved |
| `TASKS.md` | What implementation work is ready, active, blocked, or complete |

`PRODUCT.md` §19 remains the canonical roadmap and `ARCHITECTURE.md` §40
remains the canonical technical sequence. `TASKS.md` turns those decisions into
reviewable work; it does not override them.

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
```

Measured engineering baselines are recorded under
[`docs/performance/`](docs/performance/); they describe their exact hardware and
method and are not cross-machine performance guarantees.

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

Set `SIFTAIL_PUBLIC_URL` to the operator-facing HTTP(S) origin. Forwarded
client, scheme, and host metadata is considered only when the immediate peer is
inside `SIFTAIL_TRUSTED_PROXY_CIDRS`; direct clients cannot spoof it. Siftail
does not treat `Remote-User`, `X-Forwarded-User`, or other identity headers as
authentication.

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

## Important note

The specification documents are authoritative inputs to the implementation, not
generated descriptions of it. When implementation intentionally changes an
accepted decision, update the relevant specification, tests, migrations, and—
when the decision is consequential—an ADR in the same change.
