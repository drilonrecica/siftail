# Operator guide

**Scope:** SFT-053. The single entry point for running Siftail 0.5.0: install,
configure, operate, diagnose, back up, restore, upgrade, roll back, export, and
shut down. Each section gives the shortest correct path and links the detailed
contract. Capacity planning is in [`capacity.md`](capacity.md).

Every command below is a Siftail command or a standard Docker/Compose tool.
There is no arbitrary-SQL command and no public administration endpoint. The
examples use Docker Compose and the service name `siftail` from
[`compose.yaml`](../../compose.yaml). For Coolify, use
[`coolify.md`](coolify.md) (it runs the same binary as
`docker exec siftail-<id> /siftail …`). Outside a container, drop the Compose
prefix and run `./siftail …`.

## Active versus stopped

Siftail commands fall into three groups. Running one in the wrong state either
refuses safely or says what to do; none corrupts the database.

| Command | Server must be | Notes |
| --- | --- | --- |
| `backup`, `backup --configuration-only`, `diagnostics` | **active** | Use the owner-only control socket. Refuse with a recovery message if the socket is stale. |
| `backup verify <file>` | either | Reads the artifact only. |
| `database check` | either | Active: bounded `quick_check`. Stopped: read-only open. |
| `database check --full` | **stopped** | Full integrity scan; refused while active. |
| `restore --confirm RESTORE <file>` | **stopped** | Destructive, CLI only. |
| `admin create`, `admin reset-password`, `sessions revoke-all`, `server …`, `token …` | either | Active: through the control socket. Stopped: writes the database directly under the maintenance lock. |
| `config validate`, `version`, `healthcheck` | either | `config validate` needs the same environment as `serve`. |

How the active/stopped decision is made, the stale-socket recovery, and the
maintenance lock are in
[`docker-compose.md`](docker-compose.md#initialize-and-start).

To run a stopped-server command with Compose, stop the service first and use a
one-off container on the same volume. `run` uses the image entrypoint, so pass
only the Siftail arguments:

```bash
docker compose stop siftail
docker compose run --rm --no-deps siftail database check --full
docker compose up -d --wait siftail
```

## 1. Install

Follow [`docker-compose.md`](docker-compose.md) (or [`coolify.md`](coolify.md)).
In order:

1. Copy `.env.example` to `.env` and set `SIFTAIL_PUBLIC_URL` (the exact
   browser origin) and `SIFTAIL_INGEST_PUBLIC_URL` (must end in
   `/api/v1/ingest`). Use `https` in production. `http` is for loopback or
   private access only.
2. `docker compose config --quiet` to validate the resolved file.
3. Create the administrator before the first start:
   `docker compose run --rm siftail admin create --username Admin`.
4. `docker compose up -d --wait`, then
   `curl --fail <public UI origin>/health/ready`.
5. Sign in, create a Server and its ingestion token in the UI (shown once), and
   use the generated source configuration and the guided test.

Image selection, platforms, and the runtime contract (UID/GID `65532`, one
process, `/data` as the only volume) are in
[`container-image.md`](container-image.md).

## 2. Configure

All configuration is environment variables with the `SIFTAIL_` prefix, read at
startup. An unknown `SIFTAIL_*` variable stops startup and names it. Retention
age and size are not environment variables: they are set in the browser on
Settings ([`retention.md`](retention.md)).

| Variable | Default | Meaning |
| --- | --- | --- |
| `SIFTAIL_PUBLIC_URL` | required | Exact browser origin. Decides origin checks and cookie security. No path, query, or credentials. |
| `SIFTAIL_INGEST_PUBLIC_URL` | required | Public ingestion URL, ending `/api/v1/ingest`. |
| `SIFTAIL_DATA_DIR` | `/data` | Database and control files. |
| `SIFTAIL_BACKUP_DIR` | `<data dir>/backups` | Where the browser Backup page writes. Must not be the data directory itself. |
| `SIFTAIL_UI_ADDR` / `SIFTAIL_INGEST_ADDR` | `:8080` / `:8081` | The two listeners. |
| `SIFTAIL_LOG_LEVEL` / `SIFTAIL_LOG_FORMAT` | `info` / `text` | Process log. |
| `SIFTAIL_SHUTDOWN_TIMEOUT` | `30s` | Drain time on stop. Keep the container stop grace longer. |
| `SIFTAIL_TRUSTED_PROXY_CIDRS` | empty | Only networks your reverse proxy actually uses. Never `0.0.0.0/0` or `::/0`. |
| Request, queue, and group-commit bounds | see [`capacity.md`](capacity.md) | Change only with a measured reason. |

Check a configuration with the new image before switching to it:

```bash
docker compose run --rm siftail config validate
```

### `_FILE` secrets

Release 0.5.0 has **no** secret environment variable, so there is nothing to
put in a file. The `_FILE` mechanism (absolute path, regular file, at most
64 KiB, not group/world-writable, never together with the plain variable) exists
for the first secret setting a later release adds; until then any
`SIFTAIL_*_FILE` variable stops startup. The administrator password and
ingestion tokens are never environment values. See
[`docker-compose.md`](docker-compose.md#secret-files-_file).

## 3. Administrator recovery

There is exactly one local administrator. Recovery needs shell access to the
host or container, never a browser flow:

```bash
docker compose exec siftail /siftail admin reset-password   # server running
docker compose run --rm siftail admin reset-password        # server stopped
```

On a terminal the password is read twice without echo; it is never accepted as
an argument. Reset revokes every browser session in the same transaction. To
sign everyone out without changing the password:

```bash
docker compose exec siftail /siftail sessions revoke-all
```

Sign-in throttling and its operator effect are in
[`../security/sign-in-throttling.md`](../security/sign-in-throttling.md).
Details: [README, Administrator recovery CLI](../../README.md#administrator-recovery-cli).

## 4. Ingestion and retries

Siftail acknowledges a request only after it has committed to SQLite: `204`
with `X-Siftail-Ingest-Outcome: committed`. Nothing short of that is
acknowledged, and the sender owns retry.

| Response | Meaning | What the sender should do |
| --- | --- | --- |
| `204` + committed marker | Durable | Nothing. |
| `503` | Queue full or temporary database pressure | Retry. Fluent Bit retries without a count limit in the supported configuration. |
| `507` | Storage full | Retry after [recovery](storage-full-recovery.md). |
| `400`, `401`, `403`, `413`, `415`, `429` | Request, token, format, size limit, or throttling | Correct the cause; resending the same request does not help. |

Retries are safe. A record with a stable `source_event_id` and identical
content is an idempotent no-op; a record without a timestamp is not compared by
time. A client that loses the response after a commit may resend, so the sender
must not assume exactly-once delivery. Limits on the sender's own buffer
(Fluent Bit keeps at most 256 MiB for the Siftail output in the supported
Coolify setup) mean delivery is bounded at-least-once, not lossless. Details:
[README, Ingestion compatibility](../../README.md#ingestion-compatibility),
[`coolify.md`](coolify.md#delivery-retry-and-saturation),
[`../integrations/coolify-fluent-bit-compatibility.md`](../integrations/coolify-fluent-bit-compatibility.md).

Test a path end to end with the Server page's **Test connection**, or from a
shell with the CLI described in
[README, Command-line ingestion smoke test](../../README.md#command-line-ingestion-smoke-test).

## 5. Health and status

```text
GET /health/live    process responds (stays 200 through a transient database failure)
GET /health/ready   migrated, writer healthy, storage writable, not shutting down
```

Both are on the UI listener and unauthenticated; readiness returns only
`not ready` on `503`. The container health check is
`/siftail healthcheck`, a loopback request to `/health/ready`. A container
reported unhealthy is not necessarily broken: read **Status** (signed in) for
the reason, because Status carries the state, sizes, queue gauges, rates, and
up to 100 sanitized diagnostics. `docker inspect` shows only the health
verdict.

## 6. Diagnostics

In increasing order of effort:

1. **Status** page in the browser.
2. `docker compose exec siftail /siftail diagnostics` (active server): the same
   at most 100 sanitized entries from a closed list. They reset on restart and
   contain no messages, payloads, paths, or credentials.
3. `docker compose exec siftail /siftail database check` (active): bounded
   `quick_check` plus a passive WAL checkpoint.
4. `docker compose logs siftail`: the process log, at `SIFTAIL_LOG_LEVEL`.
   Startup refusals (configuration, newer schema, database ownership) print
   here.
5. Stopped only: `docker compose run --rm siftail database check --full`
   ([`database-checks.md`](database-checks.md)).

There is no support bundle and nothing is sent anywhere. If a database check
fails, preserve `/data` before any repair attempt and restore a verified
backup ([section 8](#8-backup-verify-and-restore)).

## 7. Retention and disk full

Retention is by **receipt time**, set in the browser on Settings: age
(default 14 days, 1–3,650) and active database footprint (default 4 GiB,
1–1,024). It runs at startup and hourly, and size retention deletes oldest
received first from 95% back to 90% of the limit. Freed pages are reused
immediately but the file shrinks slowly. Late events are stored, counted, and
never silently dropped. See [`retention.md`](retention.md).

When the data filesystem is full, ingestion returns `507`, readiness fails,
sign-in keeps working, and Siftail probes for recovery every five seconds. The
procedure is to free space or enlarge the volume and, if the logs themselves
are the pressure, lower the retention limits. Do not delete database files.
Restarting does not help by itself. See
[`storage-full-recovery.md`](storage-full-recovery.md).

## 8. Backup, verify, and restore

```bash
# Server running: create, verify, copy off the volume, compare checksums.
docker compose exec siftail /siftail backup --output /data/siftail-full.sqlite
docker compose exec siftail /siftail backup verify /data/siftail-full.sqlite
docker compose cp siftail:/data/siftail-full.sqlite ./siftail-full.sqlite
sha256sum ./siftail-full.sqlite
```

Add `--configuration-only` for Servers, token hashes, settings, and sources
without event history. A backup is a complete consistent snapshot taken while
ingestion continues; browser sessions are never included. Every artifact holds
password and token **hashes** and a full one holds the retained logs: protect
both as sensitive. Scheduling, off-host copying, encryption, and deleting old
backups are yours; Siftail does none of them. See
[`full-backups.md`](full-backups.md).

Restore is destructive, replaces rather than merges, and runs only on a stopped
server:

```bash
docker compose stop siftail
docker compose run --rm --no-deps \
  -v "$PWD":/restore:ro siftail restore --confirm RESTORE /restore/siftail-full.sqlite
docker compose up -d --wait siftail
```

A healthy current database is kept as a verified `siftail.db.rollback`; a
missing or unhealthy one needs `--replace-unhealthy` and is preserved
unmodified in `siftail.db.unhealthy-*`. Every session is removed by restore, so
sign in again. See [`restores.md`](restores.md).

## 9. Upgrade and rollback

1. Read the release notes and
   [`upgrade-compatibility.md`](upgrade-compatibility.md) for the supported
   schema window. 0.5.0 specifics (mandatory public URL, one re-sign-in on
   `https`) are in
   [`docker-compose.md`](docker-compose.md#upgrading-to-050-public-url-and-sessions).
2. Create and verify a backup ([section 8](#8-backup-verify-and-restore)) and
   copy it off the volume.
3. Validate: `docker compose run --rm siftail config validate` with the new
   image (set `COMPOSE_SIFTAIL_IMAGE`).
4. `docker compose pull siftail && docker compose up -d --wait --force-recreate siftail`.
5. Confirm `/health/ready`, then Status (version, schema, no new diagnostics).

Migrations run forward only and transactionally at startup. **Rollback:** if
the new version did not change the schema, set the previous image and
recreate. If it did, the older binary refuses the newer database; stop, then
restore the pre-upgrade backup with `--replace-unhealthy`. There is never a
down-migration, so the pre-upgrade backup is the rollback.

## 10. Export

History results export from the browser (**Export results** in History) as a
confirmed, bounded download in the documented formats. It needs 320 MiB free on
the data volume while it runs, and the staging file is deleted afterwards.
There is no unauthenticated or scripted export endpoint. Formats and limits:
[`export-formats.md`](export-formats.md). For whole-database export use a
backup.

## 11. Shutdown and removal

```bash
docker compose stop siftail          # drains accepted work, then exits
docker compose down                  # removes the container, keeps the volume
docker compose down --volumes        # also deletes all data: destructive
```

Siftail drains accepted work for `SIFTAIL_SHUTDOWN_TIMEOUT` (30 s) and the
container gets 40 s before it is killed; raise both together. A request without
a committed `204` stays eligible for the sender's retry. A forced kill is
recovered by SQLite on the next start. See
[`docker-compose.md`](docker-compose.md#health-restart-and-shutdown).

## Security, privacy, and data ownership

- **Ownership.** All data lives in one SQLite database in `/data` on your
  host. Siftail has no hosted component or external account.
- **No outbound behavior.** Ordinary operation makes no outbound request: no
  telemetry, analytics, update check, CDN, external font or asset, or error
  reporting. The only requests Siftail itself initiates are the **Test
  connection** request you click (to your own ingestion URL) and its loopback
  health check and control socket. Siftail never
  calls the Coolify API.
- **What it stores.** Retained application logs, hashed administrator password,
  hashed ingestion tokens (never plaintext), Servers, sources, settings, a
  bounded security-audit history, and hashed sessions.
- **Deletion is not forensic erasure.** Retention, **Clear logs**, removing a
  source, and `down --volumes` remove data from Siftail's database. Copies can
  persist in backups, exports, host snapshots, and filesystem free space. Siftail
  does not overwrite storage and makes no claim of secure erasure.
- **Scope of protection.** Siftail authenticates one administrator and
  ingestion tokens, checks origins and CSRF, throttles sign-in, and audits
  security events. It does not encrypt the database at rest; use an encrypted
  volume or host if you need that. TLS is the reverse proxy's job.
  Review evidence: [`../security/0.5.0-hardening-review.md`](../security/0.5.0-hardening-review.md).

## Where things are

| Task | Document |
| --- | --- |
| Compose install and upgrade | [`docker-compose.md`](docker-compose.md) |
| Coolify install and log drain | [`coolify.md`](coolify.md) |
| Image contract | [`container-image.md`](container-image.md) |
| Capacity planning | [`capacity.md`](capacity.md) |
| Retention and late events | [`retention.md`](retention.md) |
| Storage full | [`storage-full-recovery.md`](storage-full-recovery.md) |
| Database checks | [`database-checks.md`](database-checks.md) |
| Backup, restore | [`full-backups.md`](full-backups.md), [`restores.md`](restores.md) |
| Schema upgrades | [`upgrade-compatibility.md`](upgrade-compatibility.md) |
| Export | [`export-formats.md`](export-formats.md) |
