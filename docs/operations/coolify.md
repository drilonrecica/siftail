# Manual Coolify deployment

This guide deploys Siftail as a Coolify Docker Compose resource and connects
the Coolify server's log drain to it. Siftail does not call the Coolify API,
read its database, or modify Coolify on its own: every Coolify change below is
made by you in the Coolify dashboard.

**Validated:** see [Validation record](#validation-record). Coolify releases
and Fluent Bit images other than the ones recorded there are unverified; the
pinned drain contract is in
[`coolify-fluent-bit-compatibility.md`](../integrations/coolify-fluent-bit-compatibility.md).

Examples use `logs.example.com` (browser) and `ingest.example.com`
(ingestion). Substitute your own names. Nothing in this guide needs or shows
a real token, password, or private address.

## What you need

- A Coolify v4 instance and one server it manages, with Coolify's proxy
  (Traefik) running.
- Two DNS names pointing at that server: one for the browser interface and one
  for ingestion. They may be sibling subdomains.
- **SSH access to the Coolify server.** The Siftail image contains no shell, so
  Coolify's web **Terminal** cannot open a session in it. Administrator setup,
  recovery, and backups run as `docker exec … /siftail …` over SSH.
- A Siftail image the server can pull. The supported image is
  `ghcr.io/drilonrecica/siftail:0.5.0`; set `COMPOSE_SIFTAIL_IMAGE` to choose
  another tag explicitly.

## 1. Create the resource

1. In your project and environment, choose **New → Docker Compose Empty**.
2. Paste [`compose.yaml`](../../compose.yaml) from the release you deploy,
   unchanged.

Do not edit the service definition. In particular keep:

- `COOLIFY_APP_NAME: siftail-self`, the marker the generated drain
  configuration excludes so Siftail never ingests its own output. Coolify adds
  its own `COOLIFY_APP_NAME` only when the service does not define one;
- the named volume `siftail-data` mounted at `/data`; and
- `expose` without host `ports`. Coolify's proxy reaches both listeners over the
  container network.

Do not add `user: root`, privileged mode, a Docker socket, or a host bind
mount. Do not use `compose.local.yaml` on Coolify.

## 2. Set environment variables

Coolify lists the variables the Compose file requires. Set:

```env
SIFTAIL_PUBLIC_URL=https://logs.example.com
SIFTAIL_INGEST_PUBLIC_URL=https://ingest.example.com/api/v1/ingest
```

- `SIFTAIL_PUBLIC_URL` is the exact browser origin: `https`, host, no path. It
  alone decides the origin check, the `__Host-` session cookie, and HSTS.
  Sub-path deployment is not supported.
- `SIFTAIL_INGEST_PUBLIC_URL` is the complete URL the drain will post to and
  must end in `/api/v1/ingest`. Generated configuration and the guided test use
  only this value.
- Leave `SIFTAIL_TRUSTED_PROXY_CIDRS` empty. It only affects which client
  address is recorded for sign-in throttling and audit entries; forwarded
  scheme and host are never read. If you set it, list the exact Coolify proxy
  network, never `0.0.0.0/0` or `::/0`.
- Optional: `COMPOSE_SIFTAIL_IMAGE` to pin an explicit image, and the timeouts
  described in [`docker-compose.md`](docker-compose.md#health-restart-and-shutdown).

Coolify may write every variable, including the `COMPOSE_SIFTAIL_` controls,
into the container's environment. That is expected: Compose-only controls use
that prefix precisely so Siftail does not reject them. Never create a
`SIFTAIL_*` variable that is not documented; Siftail refuses to start on
unknown variables in its namespace. No password or token belongs here.

## 3. Assign domains to the two listeners

Open the `siftail` service's settings and set its domains, each with the
container port it routes to:

```text
https://logs.example.com:8080,https://ingest.example.com:8081
```

The `:8080` and `:8081` suffixes select the container port; the public
domains stay on 443 with Coolify's certificates. The browser domain must reach
only `8080` and the ingestion domain only `8081`. The listeners have different
authentication and request limits; do not route both names to one port.

## 4. Deploy and check readiness

Deploy. The container becomes healthy when `/health/ready` succeeds; Coolify
shows the health state from the image's health check. From any machine:

```bash
curl --fail https://logs.example.com/health/ready
curl -sI https://logs.example.com/login | grep -i strict-transport-security
```

The second command must show `max-age=31536000`. If it shows nothing,
`SIFTAIL_PUBLIC_URL` is not `https`.

Before an administrator exists the sign-in page says **No administrator is
configured**. Siftail is running; continue with step 5.

## 5. Create the administrator

Over SSH on the Coolify server, find the container. Coolify names it after the
service and the resource identifier:

```bash
docker ps --filter name=siftail- --format '{{.Names}}'
```

Create the administrator in that container (substitute the name):

```bash
docker exec -it siftail-<id> /siftail admin create --username Admin
```

The command asks for the password twice without echo. Never pass the password
as an argument, a Coolify variable, or in the Compose file. Sign in at
`https://logs.example.com`.

### Administrator recovery

```bash
docker exec -it siftail-<id> /siftail admin reset-password
docker exec siftail-<id> /siftail sessions revoke-all
```

A password reset already ends every session. `sessions revoke-all` ends them
without changing the password. Both use the running server's owner-only
control socket; no restart is needed.

## 6. Create a Server and its token

1. Open **Servers**, enter a display name (for example the Coolify server's
   name), and choose **Create server**.
2. In that Server's **Create or rotate token**, name the token (for example
   `coolify-drain`) and choose **Create token**.

The token is shown once, in a single field. The **Connect this server** panel
below it shows the endpoint and the generated configuration with a
placeholder; **Copy Coolify config** puts the configuration with the real token
into your clipboard. Leaving the page clears it. If you lose it, create a new
token and revoke the old one.

## 7. Install the generated drain configuration

In Coolify, open **Servers → your server → Log Drains**:

1. Choose the **Custom FluentBit** drain.
2. Paste the copied configuration into the configuration field, unchanged.
   Leave the parser field empty.
3. Save and enable the drain. Coolify starts a container named
   `coolify-log-drain` on the server.

The pasted configuration contains the token. Coolify stores it in its
database and writes it to `/data/coolify/log-drains/fluent-bit.conf` on the
server; treat Coolify backups and that directory as secret material.

Check what the generated configuration must contain before relying on it:

- `Listen 0.0.0.0` on the Forward input. Coolify publishes port 24224 on the
  server's `127.0.0.1` only and Docker delivers it to the drain container's
  network address, so a listener on the container's own `127.0.0.1` would
  receive nothing. Never publish 24224 more widely: the input is
  unauthenticated, and Docker's log driver cannot send a shared key. A
  configuration generated before this guide's release may still say
  `127.0.0.1`; create a new token to regenerate it;
- the `grep` filter `Exclude COOLIFY_APP_NAME ^siftail-self$`, **before** the
  `modify` filter that renames `COOLIFY_APP_NAME`;
- `Retry_Limit False` and `storage.total_limit_size 256M` on the output;
- `tls On` and port `443` for an `https` ingestion URL.

Then check that the drain started and accepted the file:

```bash
docker ps --filter name=coolify-log-drain
docker logs --tail 50 coolify-log-drain
```

## 8. Turn on Drain Logs for the resources to collect

For each application or service whose logs Siftail should receive, enable
**Drain Logs** in its advanced settings and **restart** it. Coolify applies
the logging driver only when the container is recreated.

Leave **Drain Logs disabled on the Siftail resource itself.** The exclusion
marker is the primary defence; the disabled toggle is the second.

## 9. Prove ingestion end to end

1. In the token's **Connect this server** panel choose **Test connection**.
   All five checks must pass: endpoint reached, token accepted, event read,
   event stored, source listed. A pass creates one event under
   `siftail-test / setup / guided-ingestion / probe`; it follows ordinary
   retention.
2. Make a drained application write a line (open a page, run a job), then
   open **Logs** and filter by that application. Its project, environment,
   and application appear as Coolify reports them.
3. Open **Status**: queued events return to zero and nothing is degraded.

The guided test proves Siftail's public ingestion path, not the drain. Only
step 2 proves that `coolify-log-drain` delivers. Docker's Fluentd driver is
asynchronous in Coolify, so a drain that cannot be reached does not stop your
applications; their lines are simply not delivered. Do not consider setup done
until real application lines appear.

## Recursive-ingestion diagnosis

If Siftail's own output were drained back into itself, every line it writes
(startup, warnings, rate-limited ingestion diagnostics) would arrive as an
event, and diagnostics caused by ingestion would create more ingestion. The
signs are Siftail's own messages in **Logs**, usually under an application
named `siftail-self` or the Siftail resource's name, and event volume that
grows without application activity.

If you see this, disable the drain on the server first, then check:

```bash
# The marker on Siftail's container, exactly siftail-self.
docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' siftail-<id> |
  grep '^COOLIFY_APP_NAME='
# Siftail's own logging driver; fluentd means Drain Logs is on for it.
docker inspect --format '{{.HostConfig.LogConfig.Type}}' siftail-<id>
# The exclusion exists and precedes the rename.
sudo grep -n 'siftail-self\|Rename COOLIFY_APP_NAME' \
  /data/coolify/log-drains/fluent-bit.conf
```

Correct whichever differs: the marker must be exactly `siftail-self`, the
driver should be `json-file` (turn **Drain Logs** off on Siftail and restart
it), and the `grep` line must come before the `Rename` line. Re-enable the
drain afterwards. Do not rename the marker to match a different application
name; it is anchored so `siftail-shop` or `my-siftail` stay collected.

## Delivery, retry, and saturation

- A `204` from Siftail means the whole request committed to SQLite. Nothing
  short of that is acknowledged.
- `503` (queue or temporary database pressure) and `507` (storage full) are
  retried by Fluent Bit without a retry-count limit. Status shows the
  degraded state; [`storage-full-recovery.md`](storage-full-recovery.md)
  covers `507`.
- `400`, `401`, `403`, `413`, `415`, and `429` need a correction (token,
  format, configured limits); retrying the same request does not help.
- Fluent Bit buffers at most 256 MiB for the Siftail output. When the buffer
  is full it discards the oldest chunk. In Coolify the buffer lives under
  `/tmp` in `coolify-log-drain`, so it is also lost when Coolify replaces that
  container. This is a bounded cushion, not lossless delivery.
- A chunk replayed after a long outage is accepted even when its events are
  already older than the retention age; Status counts them as late events.
  See [`retention.md`](retention.md).

## Common failures

| Symptom | Likely cause | What to do |
| --- | --- | --- |
| Deploy fails before the container starts, naming `SIFTAIL_PUBLIC_URL` or `SIFTAIL_INGEST_PUBLIC_URL` | Required variable missing | Set both in step 2 |
| Container restarts; logs say `configuration invalid` | A URL has a path, query, or wrong ingestion path, or an unknown `SIFTAIL_*` variable exists | Fix the named variable; `docker logs siftail-<id>` names it |
| Browser shows the ingestion listener's `404`, or ingestion returns `404` | Domains routed to the wrong ports | Step 3: `8080` for the browser name, `8081` for ingestion |
| Sign-in loops or cookie is rejected | Browser uses a name other than `SIFTAIL_PUBLIC_URL`, or that value is `http` behind HTTPS | Use the exact public origin |
| **Test connection**: endpoint not reached | DNS, certificate, or the server cannot reach its own public name | Resolve and `curl` the ingestion URL from the server |
| **Test connection**: token rejected | Token revoked or mistyped | Create a new token |
| Test passes but no application lines appear | Drain not running, Drain Logs off, resource not restarted, or the Forward input listens on `127.0.0.1` inside `coolify-log-drain` | `docker logs coolify-log-drain`; check step 8; `docker inspect --format '{{.HostConfig.LogConfig.Type}}'` on the application must print `fluentd`; the configuration must say `Listen 0.0.0.0` (step 7) |
| `coolify-log-drain` logs `401` | The token in the pasted configuration was revoked or rotated | Paste the configuration of the current token |
| Applications appear without project or environment | Coolify did not attach those fields for that resource type | Expected fallback; see the compatibility notes |
| Event volume grows with no application activity | Recursive ingestion | See [Recursive-ingestion diagnosis](#recursive-ingestion-diagnosis) |

## Backup before every change

Before an upgrade, a configuration change, or teardown, take and verify a
backup on the volume and copy it off the server:

```bash
docker exec siftail-<id> /siftail backup --output /data/backups/pre-change.sqlite
docker exec siftail-<id> /siftail backup verify /data/backups/pre-change.sqlite
docker cp siftail-<id>:/data/backups/pre-change.sqlite ./pre-change.sqlite
sha256sum ./pre-change.sqlite
```

Compare the digest with the one `backup verify` printed. The file contains
log data and credential hashes; protect it. The **Backup** page offers the
same into `/data/backups`. Full contract: [`full-backups.md`](full-backups.md),
[`restores.md`](restores.md).

## Upgrade

1. Back up (above).
2. Set `COMPOSE_SIFTAIL_IMAGE` to the new explicit tag, for example
   `ghcr.io/drilonrecica/siftail:0.5.1`. Never track `latest` or `edge`.
3. Redeploy. Coolify recreates the container on the same `siftail-data`
   volume; startup applies forward migrations in a transaction.
4. Check `/health/ready`, sign in, and confirm new application lines arrive.

While the container is replaced the ingestion domain is briefly unavailable;
Fluent Bit buffers and retries.

## Rollback

If the newer version did not change the schema, set `COMPOSE_SIFTAIL_IMAGE`
back and redeploy. If it did, the older binary refuses to start against the
newer schema: stop the resource and restore the pre-upgrade backup with the
stopped-server flow in [`restores.md`](restores.md) using
`--replace-unhealthy`. Siftail never migrates down.
[`upgrade-compatibility.md`](upgrade-compatibility.md) lists schema versions.

## Teardown

1. Back up if you need the data.
2. Disable **Drain Logs** on every resource that used it and restart them.
3. Disable the custom log drain on the Coolify server. Its configuration,
   including the token, remains in Coolify's settings; clear the field if you
   will not use it again.
4. Revoke the token in Siftail (**Servers → Revoke**) if Siftail stays.
5. Delete the Siftail resource in Coolify. Choose whether Coolify also deletes
   the volumes: keeping the volume keeps every event, the administrator, and
   token hashes on the server; deleting it removes them, but not from backups
   or snapshots.

## Validation record

- **2026-11-24:** the maintainer accepted this guide as working without a
  recorded step-by-step run on their Coolify instance. No
  Coolify version, Fluent Bit image, per-step result, ingestion proof,
  restart, upgrade, or teardown evidence was captured. Until such a run is
  recorded here, treat every Coolify version as unverified beyond the pinned
  `v4.1.1` drain contract in the compatibility notes.
