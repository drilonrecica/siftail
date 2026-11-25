# Known issues and limitations

**Applies to:** 0.5.0 (public dogfood release). **Last reviewed:** 2026-11-25
(SFT-058 gate).

This list is part of every release's notes. An entry stays until a release
fixes it; the fixing release's notes say so. None of the issues below loses,
corrupts, or wrongly acknowledges ingested data: the soak and the recovery
suites found no such defect. An unresolved data-loss, recovery, security, or
unbounded-resource defect would block the release instead of appearing here
(SFT-058).

Severity: **medium** can mislead an operator during an incident or degrade a
documented workflow; **low** is a visible flaw with a working path around it.

## Operations

- **Online `siftail database check` fails on large databases (medium).** The
  CLI waits at most 10 seconds for the running server, and the active
  `quick_check` takes longer on a database of about 1.6 GiB. The command then
  reports `active database check failed`, and Status records the check as
  "Failed · canceled". The same 10-second limit applies to each status poll of
  `siftail backup`, which can fail once on a loaded host while the backup
  itself continues. Data is not affected. Workaround: for a large database run
  the check with the server stopped (`docker compose stop siftail`, then
  `docker compose run --rm siftail database check`), and confirm a backup with
  `siftail backup verify <file>` once it is written. Evidence:
  [`0.5.0-soak.md`](../performance/0.5.0-soak.md#findings), F1.
- **Readiness after a restart grows with database size (low).** Startup runs
  `quick_check` before it reports ready and accepts ingestion. On the soak
  host this took 4.6 s at about 0.5 GB and 174.5 s at 2.29 GiB. Fluent Bit and
  other retrying clients recover by themselves; plan restarts of large
  instances accordingly, and use `/health/live`, not `/health/ready`, for any
  restart-on-unhealthy decision. Evidence: soak F2.
- **A cleanup run that meets a transient storage failure waits a full hour
  (low).** Retention retries only at its next hourly run, so the database can
  stay above the size limit for up to an hour after a short failure. A restart
  runs cleanup at startup. Evidence: soak F4.
- **Live closes unfiltered streams often under heavy load (low).** At
  1,000–5,000 events/s an unfiltered Live view regularly reaches its bounded
  buffer and is closed by design (the server never blocks on a slow viewer);
  the browser offers to reconnect. Narrow the filters or use History at that
  rate. Evidence: soak F3.
- **Idle memory after heavy load is above the idle target (low).** After the
  soak's saturation phases the idle process held 71.6 MiB RSS against the
  50 MB idle target. It is flat, not growing. A freshly started container idles
  at about 15 MiB. Evidence: soak F5.

## Performance evidence not yet collected

- The production container has **not** been measured on a quiet host at
  1,000 events/s sustained, at a 5,000 events/s burst, or with 1 to 10 million
  stored events (SFT-129 was skipped). The figures in
  [`capacity.md`](../operations/capacity.md) are labelled measured, bounded, or
  estimated; do not size a deployment on a figure it marks as missing.
- The release workflow smoke-tests the arm64 image natively before it
  publishes, but no native arm64 performance has been measured.

## Interface

- **The History and Live filter area is taller than designed (medium).** The
  first log row on a desktop starts at about 690 px instead of the designed
  280 px, so fewer rows fit on screen. Mobile at 390 px is not affected.
  Evidence: [`h3-gate.md`](h3-gate.md#findings), finding 1.
- **Live's Reconnect button is always shown (low),** also while connected;
  pressing it then is harmless. H3 finding 2.
- **Row names read by screen readers omit the time zone (low).** The list's own
  name states the zone. H3 finding 3.
- **The custom time range shows a 12-hour clock in some browser locales
  (low).** The native date-time control follows the browser locale; the labels
  state the zone and the submitted value is 24-hour. H3 finding 4.
- **Event detail mixes two time formats when the display zone is UTC (low).**
  H3 finding 5.
- **A faint shadow from the hidden skip link shows at the top left in the light
  theme at 390 px (cosmetic).** H3 finding 6.
- **No manual screen-reader or on-device phone review was done for 0.5.0.**
  Automated keyboard, axe, contrast, and 390 px checks pass in Chromium; the
  owner-run checks (SFT-127, SFT-128) were skipped. Accessibility is a WCAG 2.2
  AA target, not a certified claim.

## Security and privacy limitations

These are deliberate boundaries or accepted residual risks, recorded in
[`0.5.0-hardening-review.md`](../security/0.5.0-hardening-review.md#residual-limitations):

- Anyone who controls the host, the container runtime, the environment, or the
  data volume controls Siftail. There is no encryption at rest; protect the
  volume and copied backups yourself.
- There is no redaction: whatever applications log is stored and shown. Keep
  secrets out of logs at the source.
- Clients that share the administrator's NAT address or IPv6 /64 share the
  administrator's sign-in exemption; after a restart that exemption is empty
  until the next successful sign-in.
- Unauthenticated sign-in failures are audited as per-window aggregates, not
  one row per attempt.
- `SIFTAIL_BACKUP_DIR` is compared with the data directory by path, so a
  symlink can alias the two; `SIFTAIL_PUBLIC_URL` is not canonicalized, and a
  non-canonical value fails closed as a sign-in lockout rather than a startup
  error. Use plain, exact values.
- `govulncheck` lists a module-level advisory for `golang.org/x/crypto/openpgp`,
  which Siftail does not import.
- Deleting events or sources does not securely erase them from the disk.

## Scope limitations

These are product boundaries, not defects (`PRODUCT.md`): one administrator,
no roles or teams, no metrics or traces, no regular-expression search, no
public administration API, Linux containers only (`linux/amd64`,
`linux/arm64`), and Fluent Bit (or any client of the documented HTTP contract)
as the only ingestion path. Multiline assembly belongs to Fluent Bit.
