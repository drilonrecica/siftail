# Supported Docker Compose deployment

The repository Compose files run one Siftail container with one named data
volume. They add no database, cache, sidecar, custom network, host filesystem
mount, or production Node.js runtime.

The default image is the future versioned public artifact:

```text
ghcr.io/drilonrecica/siftail:0.5.0
```

Until that image is published, or when verifying a local build, set
`COMPOSE_SIFTAIL_IMAGE` to an image that already exists in the selected container
engine. Do not interpret the default reference as evidence that the release
has been published.

## Choose the deployment shape

[`compose.yaml`](../../compose.yaml) is the supported base file. It exposes
ports `8080` and `8081` only inside the container network. Use it directly
when Coolify or another container-network reverse proxy assigns routes to
those internal ports.

[`compose.local.yaml`](../../compose.local.yaml) is an explicit local
override. It publishes both listeners to loopback only:

```text
127.0.0.1:8080 → UI listener 8080
127.0.0.1:8081 → ingestion listener 8081
```

Use the override for direct local access or when Caddy, Nginx, or another
reverse proxy runs on the host. Do not use it for Coolify or a reverse proxy
that reaches Siftail over the Compose/container network.

Commands below that name both files are for the local shape. Keep using the
same `-f compose.yaml -f compose.local.yaml` selection for every local
`config`, `run`, `up`, or forced recreation. Commands that omit `-f` operate
the base shape because `compose.yaml` is discovered automatically.

Siftail serves HTTP internally. Terminate HTTPS at the reverse proxy and route:

- the browser origin to container port `8080`; and
- the complete ingestion endpoint to container port `8081`.

The listeners use different authentication and request-limit boundaries. Do
not route browser traffic to the ingestion listener or collapse both internal
ports into one listener.

## Configure public addresses

Copy the nonsecret example:

```bash
cp .env.example .env
```

Set these two required values:

```env
SIFTAIL_PUBLIC_URL=https://logs.example.com
SIFTAIL_INGEST_PUBLIC_URL=https://ingest.logs.example.com/api/v1/ingest
```

`SIFTAIL_PUBLIC_URL` is the exact browser-facing origin: `http` or `https`, a
host, an optional port, and no path, credentials, query, or fragment. Sub-path
deployment is not supported. `SIFTAIL_INGEST_PUBLIC_URL` is the complete
source-reachable HTTP(S) URL and must end at `/api/v1/ingest`; it cannot
contain credentials, a query, or a fragment. Compose interpolation rejects
either missing value before container creation.

`SIFTAIL_PUBLIC_URL` is required by `siftail serve` itself, not only by
Compose: a `docker run` or any other start without it fails immediately with
`configuration invalid: SIFTAIL_PUBLIC_URL is required to serve the browser
interface`, and `siftail config validate` reports the same error. Setup and
recovery commands (`admin`, `sessions`, `server`, `token`, `database`,
`backup`, `restore`, `healthcheck`) do not need it. The browser origin check,
the session cookie, and HSTS derive only from this value, never from `Host` or
forwarded headers:

| Public URL | Session cookie | HSTS from the UI listener |
| --- | --- | --- |
| `https://…` | `__Host-siftail_session`; `Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`, no `Domain` | `Strict-Transport-Security: max-age=31536000` |
| `http://…` | `siftail_session`; `HttpOnly`, `SameSite=Strict`, `Path=/`, not `Secure` | none |

HSTS deliberately omits `includeSubDomains` and `preload`, because the
ingestion host may be a different subdomain under different rules; the
ingestion listener never sends HSTS. Once a browser has reached Siftail over
`https`, it refuses plain `http` to that host for one year. Use `http` only for
loopback or private access: when its host is not a loopback address or
`localhost`, `serve` logs one `public_url_plain_http` warning at startup.

Browser sessions end after 12 hours without use and 7 days after sign-in.
Settings offers **Sign out other sessions** to end every other browser's
session; `siftail sessions revoke-all` ends every session, including the
current one.

For loopback-only operation, the defaults in `.env.example` are usable:

```env
SIFTAIL_PUBLIC_URL=http://127.0.0.1:8080
SIFTAIL_INGEST_PUBLIC_URL=http://127.0.0.1:8081/api/v1/ingest
```

The local bind addresses and published ports are independently configurable:

```env
COMPOSE_SIFTAIL_LOCAL_UI_BIND=127.0.0.1
COMPOSE_SIFTAIL_LOCAL_UI_PORT=8080
COMPOSE_SIFTAIL_LOCAL_INGEST_BIND=127.0.0.1
COMPOSE_SIFTAIL_LOCAL_INGEST_PORT=8081
```

Keep both bind addresses on loopback unless you have deliberately designed
the host firewall, TLS, and exposure boundary. The base file is safer for
container-network proxying because it publishes no host ports.

## Trusted reverse proxies

`SIFTAIL_TRUSTED_PROXY_CIDRS` is empty by default. Leave it empty unless
Siftail must accept forwarded client-address metadata (`X-Forwarded-For`, used
for sign-in throttling and audit attribution) and you know the exact networks
from which the reverse proxy connects. Forwarded scheme and host headers are
never read; the browser origin comes from `SIFTAIL_PUBLIC_URL` alone.

If configured, list only those exact CIDRs:

```env
SIFTAIL_TRUSTED_PROXY_CIDRS=172.20.0.0/24
```

Never use `0.0.0.0/0` or `::/0`. Those values let arbitrary reachable clients
forge forwarded metadata. Trusted proxy configuration does not authorize
identity headers or forward-auth authentication; neither is supported in this
milestone.

## Initialize and start

Validate the resolved deployment without starting a container:

```bash
docker compose -f compose.yaml -f compose.local.yaml config --quiet
```

Create the administrator before the first server start:

```bash
docker compose -f compose.yaml -f compose.local.yaml run --rm siftail \
  admin create --username Admin
```

The command reads and confirms the password from the terminal without echoing
it. Do not place the password in `.env`, Compose YAML, command arguments, or
container environment.

Start local operation and wait for readiness:

```bash
docker compose -f compose.yaml -f compose.local.yaml up -d --wait
curl --fail http://127.0.0.1:8080/health/ready
```

For the base proxy/Coolify shape, omit `compose.local.yaml`:

```bash
docker compose -f compose.yaml up -d --wait
```

Sign in at `SIFTAIL_PUBLIC_URL`, create a Server, and create its ingestion
token in the authenticated UI. The token is displayed once. Keep it out of
Compose configuration and process logs. The UI generates the supported source
configuration and guided committed-ingestion test.

Focused CLI commands are also available. When the service is running, they
use the owner-only control socket and the single write coordinator:

```bash
docker compose exec siftail /siftail server create --name "Production"
docker compose exec siftail /siftail token create \
  --server 1 --name "production-drain"
```

The second command prints a one-time token to the current terminal. Prefer the
authenticated UI when terminal capture or command-session recording is a
concern.

The CLI decides whether the server is running by connecting to the control
socket (`/data/siftail-control.sock`), not by checking that the file exists.
With no server listening, `server`, `token`, `admin`, and `sessions` commands
write the database directly while holding the owner-only maintenance lock
(`/data/.siftail-maintenance.lock`):

- If a server is starting (it holds the lock before it opens the database and
  creates the socket afterwards) or a restore is running, the command writes
  nothing and exits non-zero with `the Siftail database is in use by a
  starting server or a running restore; retry the command`. Retry once the
  service is healthy or the restore has finished.
- While an offline command holds the lock, a server starting at that moment
  refuses with `database ownership: Siftail database is in use` and exits;
  the Compose restart policy starts it again.
- A socket left behind by a crash or `SIGKILL` is stale: nobody accepts on it.
  Offline commands report it and continue. Commands that need the running
  server (`backup`, `diagnostics`) stop and print the recovery. Starting the
  server replaces a stale socket. You can also remove it with
  `rm /data/siftail-control.sock` while the server is stopped.

## Health, restart, and shutdown

Compose health invokes `/siftail healthcheck`, which performs a bounded,
no-proxy loopback request to `/health/ready`. Liveness and readiness remain
available on UI port `8080`; only readiness drives the Compose health state.

The application drains accepted work for at most 30 seconds by default.
Compose gives the container 40 seconds before forced termination. If you
increase `SIFTAIL_SHUTDOWN_TIMEOUT`, also increase
`COMPOSE_SIFTAIL_STOP_GRACE_PERIOD` so the Compose grace remains longer:

```env
SIFTAIL_SHUTDOWN_TIMEOUT=45s
COMPOSE_SIFTAIL_STOP_GRACE_PERIOD=55s
```

Do not override `docker compose stop` with a shorter timeout. A request without
a successful committed response remains eligible for source-side retry.

Ordinary restart preserves the named volume:

```bash
docker compose restart siftail
```

## Volume ownership

The image and service run as numeric identity `65532:65532`. The supported
named volume is initialized from the image's owner-writable `/data` directory,
and setup commands exercise that same non-root ownership.

Do not add `user: root`, privileged mode, or world-writable permissions. The
supported Compose files deliberately do not use a host bind mount. If an
operator replaces the named volume with a host directory, that directory must
already be writable by `65532:65532`; host ownership and backup policy then
become the operator's responsibility.

Siftail owns its database, WAL, shared-memory, maintenance, rollback, and
staging files inside `/data`. Do not copy only `siftail.db` from a live WAL
database.

## Backup before upgrade

Create a verified backup before changing the image. The output path must not
already exist:

```bash
docker compose exec siftail /siftail backup \
  --output /data/siftail-pre-0.5.1.sqlite
docker compose exec siftail /siftail backup verify \
  /data/siftail-pre-0.5.1.sqlite
docker compose cp \
  siftail:/data/siftail-pre-0.5.1.sqlite \
  ./siftail-pre-0.5.1.sqlite
sha256sum ./siftail-pre-0.5.1.sqlite
```

The copied artifact contains sensitive application data and credential
hashes. Protect it. Compare the host `sha256sum` with the digest printed by
Siftail's verification before relying on the copy.

The CLI accepts any path inside the container. Backups started from the
browser Backup workspace go only to `SIFTAIL_BACKUP_DIR` (default
`/data/backups`, on the same volume) under a plain file name; copy them off
the volume the same way, for example
`docker compose cp siftail:/data/backups/<name>.sqlite ./`.

The full backup and restore contracts, including off-volume verification and
stopped-server restore, are documented in
[`full-backups.md`](full-backups.md) and [`restores.md`](restores.md).

## Upgrade and rollback

Choose an explicit version, pull it, and recreate only the service:

```env
COMPOSE_SIFTAIL_IMAGE=ghcr.io/drilonrecica/siftail:0.5.1
```

```bash
docker compose pull siftail
docker compose up -d --wait --force-recreate siftail
```

Local deployments must include their two `-f` arguments on both commands so
the recreated service retains its loopback publications.

### Upgrading to 0.5.0: public URL and sessions

Version 0.5.0 changes browser security (ADR 0010):

- `SIFTAIL_PUBLIC_URL` is mandatory for `serve`. Compose deployments already
  set it. A deployment started some other way without it now stops at startup
  with `configuration invalid: SIFTAIL_PUBLIC_URL is required to serve the
  browser interface`. A value with a path (sub-path deployment), query, or
  fragment is also refused. Run `siftail config validate` with the new image
  before switching.
- On an `https` public URL the session cookie is renamed to
  `__Host-siftail_session`, so every browser signs in again once after the
  upgrade. The old `siftail_session` cookie is ignored and deleted at the
  next sign-in or sign-out. The UI listener starts sending HSTS.
- Sessions now end after 12 hours idle and 7 days after sign-in. Sessions
  that were issued earlier with a 14-day lifetime follow the 7-day limit
  immediately; no data is rewritten.

The recreated container uses the existing named volume. Startup applies
supported forward migrations transactionally and refuses a schema newer than
the binary.

To roll back when no incompatible schema change occurred, restore the previous
image selection and recreate the service. If the newer binary advanced the
schema, the older binary will refuse startup. Stop the service and restore the
compatible verified pre-upgrade backup through the documented restore flow
with `--replace-unhealthy`, because a newer-schema database cannot become the
older binary's verified rollback; restore preserves it unmodified in a
`siftail.db.unhealthy-*` directory. Siftail never performs a down-migration.

## Stop or remove

Stop and remove the service while preserving all state:

```bash
docker compose down
```

The named volume remains and is reused by the next `docker compose up`.

Permanently remove the Compose-managed data volume only after preserving any
required backup:

```bash
docker compose down --volumes
```

`--volumes` is intentionally destructive. It removes the database, retained
logs, administrator, Servers, token hashes, settings, and audit history in the
Compose volume. It does not claim forensic erasure from backups, snapshots, or
underlying storage.

## Coolify boundary

Coolify consumes the base file, routes its assigned domains to internal ports
`8080` and `8081`, and preserves the named `/data` volume. The service
hardcodes `COOLIFY_APP_NAME=siftail-self`, matching the tested Fluent Bit
self-exclusion and preventing Siftail's own stdout from being recursively
drained when the collector configuration is correct.

Coolify may place every Compose interpolation control in its generated `.env`
and attach that file to the container. Deployment-only controls therefore use
the `COMPOSE_SIFTAIL_` prefix; do not rename them into the application-reserved
`SIFTAIL_` namespace.

This file does not replace the SFT-052 manual Coolify guide. A one-click
template, complete drain installation procedure, supported-version walkthrough,
and Coolify teardown evidence remain separate work.
