# Supported Docker Compose deployment

The repository Compose files run one Siftail container with one named data
volume. They add no database, cache, sidecar, custom network, host filesystem
mount, or production Node.js runtime.

The default image is the future versioned public artifact:

```text
ghcr.io/drilonrecica/siftail:0.5.0
```

Until that image is published, or when verifying a local build, set
`SIFTAIL_IMAGE` to an image that already exists in the selected container
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

`SIFTAIL_PUBLIC_URL` is the exact browser-facing HTTP(S) origin.
`SIFTAIL_INGEST_PUBLIC_URL` is the complete source-reachable HTTP(S) URL and
must end at `/api/v1/ingest`; it cannot contain credentials, a query, or a
fragment. Compose interpolation rejects either missing value before container
creation.

For loopback-only operation, the defaults in `.env.example` are usable:

```env
SIFTAIL_PUBLIC_URL=http://127.0.0.1:8080
SIFTAIL_INGEST_PUBLIC_URL=http://127.0.0.1:8081/api/v1/ingest
```

The local bind addresses and published ports are independently configurable:

```env
SIFTAIL_LOCAL_UI_BIND=127.0.0.1
SIFTAIL_LOCAL_UI_PORT=8080
SIFTAIL_LOCAL_INGEST_BIND=127.0.0.1
SIFTAIL_LOCAL_INGEST_PORT=8081
```

Keep both bind addresses on loopback unless you have deliberately designed
the host firewall, TLS, and exposure boundary. The base file is safer for
container-network proxying because it publishes no host ports.

## Trusted reverse proxies

`SIFTAIL_TRUSTED_PROXY_CIDRS` is empty by default. Leave it empty unless
Siftail must accept forwarded scheme, host, or client-address metadata and you
know the exact networks from which the reverse proxy connects.

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

## Health, restart, and shutdown

Compose health invokes `/siftail healthcheck`, which performs a bounded,
no-proxy loopback request to `/health/ready`. Liveness and readiness remain
available on UI port `8080`; only readiness drives the Compose health state.

The application drains accepted work for at most 30 seconds by default.
Compose gives the container 40 seconds before forced termination. If you
increase `SIFTAIL_SHUTDOWN_TIMEOUT`, also increase
`SIFTAIL_STOP_GRACE_PERIOD` so the Compose grace remains longer:

```env
SIFTAIL_SHUTDOWN_TIMEOUT=45s
SIFTAIL_STOP_GRACE_PERIOD=55s
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

The full backup and restore contracts, including off-volume verification and
stopped-server restore, are documented in
[`full-backups.md`](full-backups.md) and [`restores.md`](restores.md).

## Upgrade and rollback

Choose an explicit version, pull it, and recreate only the service:

```env
SIFTAIL_IMAGE=ghcr.io/drilonrecica/siftail:0.5.1
```

```bash
docker compose pull siftail
docker compose up -d --wait --force-recreate siftail
```

Local deployments must include their two `-f` arguments on both commands so
the recreated service retains its loopback publications.

The recreated container uses the existing named volume. Startup applies
supported forward migrations transactionally and refuses a schema newer than
the binary.

To roll back when no incompatible schema change occurred, restore the previous
image selection and recreate the service. If the newer binary advanced the
schema, the older binary will refuse startup. Stop the service and restore the
compatible verified pre-upgrade backup through the documented restore flow;
Siftail never performs a down-migration.

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

This file does not replace the SFT-052 manual Coolify guide. A one-click
template, complete drain installation procedure, supported-version walkthrough,
and Coolify teardown evidence remain separate work.
