# Siftail full audit — 2026-11-04

**Scope:** security, privacy, data integrity, correctness, performance, code
quality, UI/UX, accessibility, design, and process, over the tree at commit
`af2b31d` (SFT-079, 2026-09-02).
**Method:** three independent read-only review passes by area (security and
privacy; data integrity and performance; interface), each reading the named
packages in full, plus maintainer re-verification of every Critical finding,
`go build`, `go vet`, `go test ./...`, `govulncheck`, `go list -m -u all`,
`EXPLAIN QUERY PLAN` against the production migration, and WCAG contrast
computed from the token values.
**Maintainer review (2026-11-05):** all findings accepted; none rejected.
**Outcome:** every finding below maps to a task in
[`docs/tasks/0.5.0-hardening.md`](../tasks/0.5.0-hardening.md) (SFT-082–SFT-128).
The remediation runs through the Igris plan in [`tasks.md`](../../tasks.md).

Findings are observations with file references, not reproduced exploits unless
stated. "Confirmed" means the reviewer read the exact code path; "plausible"
means the mechanism is documented behaviour that was not executed.

---

## 1. Baseline

| Check | Result |
|---|---|
| `go build ./...`, `go vet ./...` | clean |
| `go test ./...` | 534 passed, 1 failed (`TestDurableIngestionSubprocessSmoke`, see DAT-10) |
| Playwright | 22 tests, green on 2026-09-02 |
| Go toolchain | `go.mod` `1.25.0`, Dockerfile `golang:1.25.12`; current stable 1.27.3; the 1.25 line is out of support |
| `govulncheck ./...` (Go 1.27.1 host) | 12 standard-library vulnerabilities reachable from Siftail code; all fixed in 1.27.2/1.27.3 |
| Go modules | all seven behind: go-sqlite3 1.14.48→1.14.52, x/crypto 0.54→0.58, x/net 0.56→0.62, x/sync 0.22→0.24, x/sys 0.47→0.49, x/term 0.45→0.47, x/text 0.40→0.43 |
| htmx | vendored 2.0.10; current 2.0.11 |
| npm dev dependencies | @playwright/test 1.62→1.64.1, @axe-core/playwright 4.12.1→4.14.0 |
| GitHub Actions | pinned by major tag, not commit SHA; no `permissions:` block |
| Dockerfile | base images digest-pinned; Go base image on an unsupported line |
| Repository | 81 commits, no tags, no published image; idle since 2026-09-02 |

---

## 2. Data integrity, correctness, performance (DAT)

**Verdict.** The core pipeline is well built: bounded admission, one transaction
per HTTP batch, a single writer, Live publication only after commit,
parameterised SQL, real SQLite in tests. Commit-before-acknowledgement holds on
the normal path. Two ordinary operational events nevertheless block a whole
server's ingestion permanently, one misclassification spreads that to every
server, online backups can livelock under steady ingestion, retention stalls the
writer, History pagination scans O(depth), and the recorded performance numbers
were measured on tmpfs.

| ID | Sev | Finding | Where | Fix → Task |
|---|---|---|---|---|
| DAT-01 | Critical | A recreated container (same name, new ID) blocks ingestion forever. `UNIQUE (source_id, container_name)` plus a lookup on `container_id IS ? AND container_name IS ?` means `docker compose up --force-recreate` or a Coolify redeploy makes the INSERT violate the name constraint, the whole batch rolls back with 503, Fluent Bit retries the same mixed batch forever, and every source on that server is blocked. **Confirmed by maintainer.** | `internal/database/migrations/0001_initial_ingestion.sql:68-69`, `internal/ingest/writer.go:314-326` | Migration drops both UNIQUEs; resolve by id, name is a mutable label; regression test → **SFT-087** |
| DAT-02 | Critical | Writer source/container caches are never invalidated after `RemoveSource`. Re-ingestion for a removed source reuses the cached ID, the INSERT fails the FK, the batch gets 503 until restart or LRU eviction. **Confirmed by maintainer (no invalidation path exists).** | `writer.go:161,193`; `internal/sources/mutations.go:95,136` | Shared epoch, or in-transaction verification, or one retry with cleared cache on FK error → **SFT-088** |
| DAT-03 | High | Any constraint or internal persistence error is mapped to `Unavailable`, flips the global `databaseCategory`, and gates every server with 503 until `RecoveryWorker` clears it 5 s later; the retried poison batch flips it back. | `writer.go:447-471`, `worker.go:56-60`, `status/state.go:224-234`, `http.go:111-116` | Batch-scoped 4xx/5xx for Constraint/Internal; degrade only on Busy/IO/Full → **SFT-089** |
| DAT-04 | High | Online backup restarts at the next `backup_step` whenever another connection commits. 256-page steps with a 1 ms sleep over a multi-GB file never finish under steady ingestion. The concurrent-write test performs one write and asserts `captured != 0 && != 1`. | `database/online_backup.go:82-110`, `backup/service.go:166-167` | `VACUUM INTO` (single snapshot) or `Step(-1)` in one read transaction; real continuous-write test → **SFT-093** |
| DAT-05 | High | Retention reclaim runs up to 8,192 `PRAGMA incremental_vacuum(1)` autocommit transactions, each an fsync under `synchronous=FULL`, while holding the only writer. The documented 205 ms came from tmpfs. | `retention/cleaner.go:206-223` | Chunked `incremental_vacuum(N)` with coordinator yields → **SFT-092** |
| DAT-06 | High | The History cursor is an `OR` predicate, not an index bound, so page N scans every index row newer than the cursor. Server-only and partial-hierarchy filters produce `USE TEMP B-TREE FOR ORDER BY` over the full range before `LIMIT 201`. Verified with `EXPLAIN QUERY PLAN`. | `logs/history_store.go:223-225,282-291` | Fold the cursor into the range bounds in Go; `source_id IN (…)` with the time index; cursor-depth and server-only benchmarks → **SFT-095** |
| DAT-07 | High | The UI server's 30 s `WriteTimeout` cuts History exports designed for 2 min / 256 MiB; the browser gets a truncated file with a mismatched Content-Length after the "succeeded" audit row. Only Live extends its deadline. History searches have no query timeout. | `app/app.go:534`, `auth/history_export.go:77,128`, `logs/export.go:22-24` | `SetWriteDeadline` per chunk; History query timeouts → **SFT-096** |
| DAT-08 | High | `VerifyToken` shares the four-connection reader pool with exports, `quick_check` and `contains` searches, so ingestion authentication can starve; a DB error returns 401, which looks like a revoked token. | `sources/store.go:270`, `database.go:21`, `http.go:106-110` | Dedicated auth connection or token cache invalidated on revoke; 503 on DB error → **SFT-096**, **SFT-089** |
| DAT-09 | High | Offline CLI mutations decide "server not running" by `Lstat` on the socket and write without the maintenance lock. They can write during startup (lock held, socket not yet created) or mid-restore. A stale socket after SIGKILL makes every CLI call fail. | `cmd/siftail/admin.go:146-170` | Acquire the maintenance lock; probe the socket; stale-socket fallback → **SFT-097** |
| DAT-10 | Medium | **The "flaky" smoke test is a date bomb.** Fixtures are dated 2026-07-28; the 14-day default retention runs at startup, so the second `serve` purges six events and the test has failed deterministically (`4, want 8`) since 2026-08-11. Also: readiness waits only on the UI `/health/live`; `freeTCPAddr` has a close-then-reuse race; `cmd.ProcessState` is never set without `Wait`. Product angle: a client with a wrong clock or a replayed backlog gets 204 and its data is deleted within the hour, silently. | `cmd/siftail/testdata/ingest/*`, `internal/ingest/testdata/*`, `retention/worker.go:35`, `cmd/siftail/main_test.go` | Fixtures relative to now, harness fixes → **SFT-082**; stale-event policy → **SFT-091** |
| DAT-11 | Medium | When `source_event_id` is present but no timestamp is supplied, the receive time is used and compared in `CanonicalEqual`; a retried batch gets 409 and is rejected whole. | `logs/normalize.go:186-191,694-706`, `writer.go:225-235` | Persist "timestamp defaulted", exclude from equality; conflict policy per ADR → **SFT-090** |
| DAT-12 | Medium | A timed-out ingest handler returns without writing a status; net/http sends an implicit 200. Masked today only because `WriteTimeout` and `RequestTimeout` are both 30 s. | `http.go:179-183`, `app.go:514` | Write 503 explicitly on timeout → **SFT-089** |
| DAT-13 | Medium | Size retention measures the file size and ignores the freelist, so it keeps deleting rows that freed pages would absorb. | `cleaner.go:123-146` | Footprint = `(page_count − freelist_count) × page_size + wal` → **SFT-092** |
| DAT-14 | Medium | `container_instances` is never pruned (DOMAIN §14 requires it); the 100k per-server cap eventually yields 403 for every new container, and the `count(*)` per new container grows linearly. | `writer.go:201-211` | Bounded cleanup → **SFT-087** |
| DAT-15 | Medium | Restore requires an existing, compatible, integrity-"ok" current database, which excludes the common disaster case (lost volume, corruption). | `backup/restore.go:115-128` | Fresh-install restore path, documented → **SFT-094** |
| DAT-16 | Medium | Exports and the active `quick_check` hold read snapshots up to 2 min, blocking WAL checkpoints; export stages up to 256 MiB on the data volume with no space check. | `logs/export.go:145-193`, `check.go:105`, `history_export.go:59` | Space check; bounded snapshot duration → **SFT-096** |
| DAT-17 | Low | `submit` returns `ctx.Err()` after admission while the transaction still commits (503 for a committed batch, no Live publish); `Run` drains before `Close` finishes `submitters.Wait()`. | `database/coordinator.go:80-82,173-178` | Drain after `Close` returns → **SFT-099** |
| DAT-18 | Low | A record with a top-level `message`/`msg` stores the whole record as the raw message; transport keys (`date`, `tag`, `container_*`, `coolify_*`) leak into attributes. | `normalize.go:297-301,455-460` | Filter transport keys → **SFT-099** |
| DAT-19 | Low | `classify` drops the driver error (no `%w`), so `errors.Is(err, sql.ErrConnDone)` can never match; `persistenceError` ends in identical fallthroughs; `_ = json.Unmarshal` in the decoder; verify reads only the first `integrity_check` row. | `database.go:312,356-374`, `decoder.go:92`, `backup/verify.go:46` | Wrap errors; remove swallowed errors → **SFT-099** |
| DAT-20 | Low | `ingestOutcomeWriter.Write` calls `WriteHeader(200)` on every write ("superfluous WriteHeader" log spam). | `http.go:199-203` | → **SFT-089** |
| DAT-21 | Low | Hot path: un-prepared per-event INSERT and dedup SELECT; JSON parsed three to four times per event (~212 allocs); `count(*)` over 100k audit rows on every audit insert. | `writer.go:226,237`, `decoder.go:80-84`, `normalize.go:113,592`, `audit/store.go:329-352` | Prepared statements, single parse, audit row counter → **SFT-099** |
| DAT-22 | Info | `/health/ready` (Docker HEALTHCHECK) reports unhealthy on storage-full; orchestrators that restart unhealthy containers make it worse. Full `quick_check` on every `Open`, including the CLI. | `database.go:98` | Document and reconsider readiness semantics → **SFT-089** |

**Performance observations.** Benchmarks run in `b.TempDir()`, which on the
recording host is tmpfs, so the 190 µs FULL-sync commit, 8.7k events/s and
205 ms retention figures reflect no-op fsync and AGENTS §17.2 ("measure the
production container") was not followed. The History fixture packs 1M events
into one second across two sources with 14-byte messages and never paginates by
cursor. On real disks, throughput without group commit is roughly one batch per
fsync latency. → **SFT-098** (group commit), **SFT-100** (real-disk baseline).

**Code quality.** Layering is sensible and tests use real SQLite. Largest
functions: `App.Run` ~235 lines, `Restorer.Restore` ~233, `persistTransaction`
~140. Duplication: four text validators, three DSN builders, four checkpoint
helpers, two `ensure*Space`, repeated PRAGMA blocks. Dead code: `takeString`,
`boundedCache.String`, test-only `Store.Recent`, `NewHistoryStore` vs `NewStore`.
Task-ID breadcrumb comments. → **SFT-099**.

**Clean checks.** No acknowledgement before commit on the normal path; no
partial batch visibility; one writer connection; per-version migration
transactions; no `OFFSET`; no `LIKE`; no `COUNT(*)` on History; SSE maps owned
by one goroutine; no broker goroutine leaks; overflow guards; decoder bounds on
compressed, decompressed, per-event bytes, depth and count; token revocation
re-checked inside the transaction; source cache filled only after commit.

---

## 3. Security and privacy (SEC)

**Verdict.** Defensive design, mostly done right. No XSS, no SQL injection, no
state-changing GET, no timing-unsafe secret comparison, a CSP with no inline
exceptions, bounded ingestion, no telemetry, CDN or remote fonts. The weaknesses
sit at the edges: one open redirect, concurrency and denial-of-service gaps on
the login path, an unsupported build toolchain, and a generated collector config
that opens an unauthenticated port.

| ID | Sev | Finding | Where | Fix → Task |
|---|---|---|---|---|
| SEC-01 | Medium | Open redirect after login: `safeReturnPath` rejects absolute URLs and `//` but accepts `/\evil.com`; browsers read the backslash as a slash. The test covers only an absolute URL. | `internal/auth/browser.go:404-415`, `browser_test.go:246` | Reject `\`, control and whitespace characters (raw and decoded) or allowlist route prefixes; tests for `/\x`, `/%5Cx`, `/\t/x` → **SFT-103** |
| SEC-02 | Medium | Login throttle bypass and login denial of service: `Check()` only reads; a failure is counted after Argon2, which is limited to two in flight process-wide. N parallel requests give N guesses; floods from rotating IPv6 addresses queue on the two-slot semaphore ahead of the real administrator. | `browser.go:209-232`, `throttle.go:40-88`, `password.go:26,102-108` | Reserve in `Check`; one in-flight per client and per account; IPv6 /64 grouping; bounded hash queue with fast 429; concurrency tests → **SFT-104** |
| SEC-03 | Medium | Every throttled (429) login writes an audit row before replying: a full-sync transaction, a `count(*)` over up to 100k rows, FIFO eviction. About 100k cheap unauthenticated requests erase the entire real audit trail and compete with ingestion for the single writer. | `browser.go:211-225`, `audit/store.go:202,329-350`, `database.go:35` | Aggregate rejections in memory, one row per window, throttle before any write, reserved capacity for authenticated rows → **SFT-105** |
| SEC-04 | Medium | Build toolchain out of support and no automated vulnerability scanning. 12 reachable standard-library vulnerabilities including GO-2026-6608 (MIME header memory-limit bypass, reachable unauthenticated on both listeners), GO-2026-6609 (Range parsing), GO-2026-6599/6600 (`html/template` context tracking, reachable from `ui.Renderer.render`). `govulncheck` appears only in release-gate docs. | `Dockerfile:3`, `go.mod:3`, `.github/workflows/ci.yml` | Go 1.27.3 with a `toolchain` line, module bumps → **SFT-083**; CI govulncheck job, Renovate → **SFT-084** |
| SEC-05 | Medium | The generated Fluent Bit config (Coolify and generic) declares `[INPUT] Name forward / Listen 0.0.0.0 / Port 24224`, an unauthenticated input. Anyone reaching the port injects records that Fluent Bit forwards with the server's valid token. Coolify's own docs target `127.0.0.1:24224`. | `internal/ingest/guide.go:118-121,151-153` | `Listen 127.0.0.1` with explanation; `Shared_Key` note for non-loopback → **SFT-109** |
| SEC-06 | Low | CI supply chain: actions pinned to moving tags; no top-level `permissions: contents: read`; digests never refreshed; no image build, scan or SBOM in CI. Positives: `npm ci --ignore-scripts`, `go mod verify`. | `.github/workflows/ci.yml` | SHA pins, least-privilege permissions, image scan job, Renovate → **SFT-084** |
| SEC-07 | Low | `X-Forwarded-For` reads only the first header line; a proxy that appends a second header line instead of a value leaves the client-supplied first line in control. | `internal/auth/proxy.go:51` | Join `Header.Values` before walking right to left → **SFT-103** |
| SEC-08 | Low | `SIFTAIL_PUBLIC_URL` is optional outside Compose. Without it the allowed origin derives from `Host` (DNS rebinding), cookies are never `Secure` behind HTTPS, and HSTS is never sent. | `browser.go:372-381,468`, `proxy.go:64-80`, `sessions/cookie.go:50-57` | `serve` refuses to start without it; `__Host-` cookie prefix on HTTPS; HSTS → **SFT-106** |
| SEC-09 | Low | Any session `Lookup` error is treated as expired, and `Lookup` writes a "last used" timestamp. During storage-full or a degraded database the administrator is redirected to login and cannot sign back in, exactly when Status and Settings matter. | `browser.go:272-276`, `sessions/store.go` | Separate `ErrInvalidSession` from infrastructure errors (503); skip the touch on write failure → **SFT-107** |
| SEC-10 | Low | The account throttle key is the username hash, independent of IP; one failure per minute keeps the administrator locked out indefinitely, and recovery needs CLI access. | `throttle.go:60-88` | Account throttle only for suspicious clients; exempt recently-authenticated clients → **SFT-104** |
| SEC-11 | Low | The web UI accepts arbitrary server filesystem paths for backup and verify. A stolen session can write a full database copy (password hash, token hashes, cursor HMAC key, all logs) to any writable directory and probe file existence. | `internal/auth/backup.go:57-63,88-95,175-185`, `backup/service.go:320-349` | Web backups only into `SIFTAIL_BACKUP_DIR` with plain filenames; CLI keeps arbitrary paths → **SFT-108** |
| SEC-12 | Low | Export never extends the 30 s write deadline; long exports are truncated after the "succeeded" audit row, and the `delivery_interrupted` row's write error is ignored. | `app.go:530-535`, `history_export.go:113-134` | Per-chunk `SetWriteDeadline` as Live does → **SFT-096** |
| SEC-13 | Info | A failed `SO_PEERCRED` lookup makes `Accept` return an error; `http.Server.Serve` exits and the errgroup takes the whole application down. | `app/control_linux.go` | Close that connection and continue → **SFT-103** |
| SEC-14 | Info | `validText` rejects C0 controls but allows C1 (U+0080–U+009F, including the escape introducer U+009B); names are printed raw by `siftail server list`. | `sources/store.go:312-325` | `unicode.IsControl` → **SFT-103** |
| SEC-15 | Info | AI-generated smells: `config.Secret`, `hasEnv` and the `_FILE` branch of `readString` are dead while AGENTS §13.4 and README describe `_FILE` as shipped; dead `X-Request-ID` fallback; impossible checks (`servers.go:187`, logout `!ok`); inconsistent nil checks; two `Referrer-Policy` middlewares; asset tests grep JavaScript source. | `config/secret.go`, `ingest/http.go:274`, `servers.go:187`, `ui_test.go:543-560` | Implement `_FILE` properly and remove the rest → **SFT-110**, **SFT-099**, **SFT-124** |
| SEC-16 | Info | htmx 2.0.10 defaults `includeIndicatorStyles` to true and tries to inject an inline `<style>` the CSP blocks, producing a console violation on every page. | `templates/head.html:4` | `"includeIndicatorStyles": false` → **SFT-123** |

**Clean checks.** No `template.HTML`/`JS`/`URL` or `safeHTML`; no `innerHTML`
or `insertAdjacentHTML` with event data; all SQL parameterised (only constant
column names and `ASC`/`DESC` are string-built); no state-changing GET;
constant-time comparison for CSRF, Argon2 and token hashes; no header injection;
export staging traversal-safe and fail-closed; request bodies and gzip output
capped; text exports quote control characters; no telemetry, external fetches,
CDN or remote fonts.

**Done well, keep it.** Layered Origin/Referer check, session-derived CSRF
token, `SameSite=Strict`, content-type allowlist, 64 KiB form cap; dummy Argon2
for unknown users; opaque hash-stored sessions with idle and absolute expiry,
revoked on password reset, re-checked every 5 s on Live, stripped from backups
under `secure_delete`; CSP without `unsafe-*`, `frame-ancestors 'none'`,
`base-uri 'none'`, COOP/CORP, `no-store` on authenticated pages, htmx history
cache off, sha384 pin on vendored htmx; `X-Forwarded-For` walked right to left
from trusted proxies only; token checked before the body is read; owner-only
control socket with `SO_PEERCRED`; distroless non-root digest-pinned image.

---

## 4. Interface: UX, accessibility, design (UX)

**Verdict.** Safe and functionally correct (strict escaping, sound CSRF and
origin checks, a no-JavaScript History path), but not yet pleasant to use. Two
WCAG Level A keyboard failures, inconsistent and unlabeled time zones, filters
lost when switching History and Live, screen-reader-opaque log rows, and several
signature items of the DESIGN.md spec that were never built while the 0.6.0
record marks them Done. Against Dozzle, Papertrail and Grafana Explore, Siftail's
advantages are honest state copy, bounded caps and the no-JS path; its gaps are
density, context around a hit, and search ergonomics.

| ID | Sev | Finding | Where | Fix → Task |
|---|---|---|---|---|
| UX-01 | Critical | Space is captured on every focusable element in the Logs scope and routed to the Live pause button, which History does not have: buttons, `<summary>` disclosures and page scrolling no longer respond to Space (WCAG 2.1.1). | `assets/keyboard.js:96-99` | Ignore Space on `button`, `a`, `summary`, `[role=button]`; bind only when a pause control exists → **SFT-113** |
| UX-02 | Critical | Single-character shortcuts F/H/L/J/K/G fire with focus anywhere outside a field and cannot be disabled or remapped (WCAG 2.1.4, Level A). DESIGN §32.6 promises a disable option that does not exist. | `keyboard.js:71-105` | Shortcuts preference (on/off) in the preference cookie next to theme and density → **SFT-113** |
| UX-03 | High | Clicking the expanded chevron, or Enter on a selected row, does not collapse the row: htmx fires the GET before the document-level handler clears the slot, and `afterSwap` re-marks it expanded. | `app.js:163-173,345-353`, `keyboard.js:100-104` | Capture-phase cancel or `htmx:confirm`; collapse tests → **SFT-114** |
| UX-04 | High | Time zones are inconsistent and unlabeled: rows render as UTC then are rewritten to local time without a zone; the range summary is UTC; labels say "(UTC)" above a hidden input replaced by a local `datetime-local` picker; event detail is UTC RFC3339; Live rows are local. 12-hour locales overflow the 8.5 rem column. | `history.go:167,257`, `app.js:229-246,296-324`, `history.html:62-66`, `event.go:184`, `app.css:1079` | One zone preference shown in summary and header; relabel picker; `hourCycle: 'h23'` → **SFT-115** |
| UX-05 | High | Switching History ↔ Live drops the investigation: Live links to bare `/logs`, the two modes use different source models (5-level cascade vs single select), so H/L reset the scope. DESIGN §8.1 says the modes share source and severity filters. | `live_page.go:125`, `ui.go:253` | Shared filter model; carry scope, levels, streams, contains → **SFT-116** |
| UX-06 | High | At ≤32 rem the filter panel collapses after every filter change, because `prepareResponsiveHistory` runs on every `afterSwap` and the region is swapped with `outerHTML`; focus is restored into a closed disclosure. | `app.js:286-291,340-343` | Preserve `open` across swaps → **SFT-117** |
| UX-07 | High | Text typed while a debounced search request is in flight is lost: the whole `#history-region` including the input is replaced with the server-echoed value, and the queued follow-up request is dropped. DESIGN §38 requires caret preservation. | `history.html:17-18` | Results-only target or `hx-sync` + `hx-preserve` → **SFT-117** |
| UX-08 | High | Log rows are opaque to screen readers: unnamed `<article tabindex=-1>` in a plain `div`, one tabbable control per row named by event ID, 200 Tab stops before "Load older", J/K uses plain focus rather than the roving tabindex the spec promises (§32.3). | `history.html:189` | `role=list`/`listitem` with roving tabindex; content-named toggles; row as the single tab stop → **SFT-114** |
| UX-09 | High | `a.button` links have no button styling: "Create server" (the first-run primary call to action), "Open logs", "Done" and "Next sources" render as plain underlined links. | `app.css:39-70`; screenshots `sources-desktop-dark.png`, `generated-guide-mobile-light.png` | Give `.button` border, background, `text-decoration: none` → **SFT-120** |
| UX-10 | High | Non-text contrast fails in both themes (WCAG 1.4.11): `--border-strong` on base/raised ≈2.0:1 (needs 3:1); `--text-muted` on `--surface-hover` 4.17/4.67, on `--surface-selected` 3.79/4.48 (needs 4.5:1). axe does not test hover states. | `assets/tokens.css` | Adjust tokens; row metadata uses `--text-secondary` on hover → **SFT-120** |
| UX-11 | High | No way to see the events around a hit; DESIGN §4.1 lists "what happened immediately before and after" as a core question. | `templates/event.html` | "Show ±1 min around this event" with current scope → **SFT-116** |
| UX-12 | Medium | Hitting the 1,000-row cap after "Load newer" trims from the top, deleting the rows just loaded and possibly the focused or expanded row. | `app.js:248-284` | Trim opposite to the load direction → **SFT-117** |
| UX-13 | Medium | The "Load older" indicator never appears: `.htmx-indicator` sits beside the button, not inside it. | `history.html:206-211` | → **SFT-117** |
| UX-14 | Medium | Fragment errors are retargeted to `#history-update-status` at the bottom of the region, possibly 200 rows from the filter that caused them; the next successful out-of-band swap removes the error. | `history.go:110-113` | Errors next to their cause → **SFT-117** |
| UX-15 | Medium | The topbar status dot is colour-only (WCAG 1.4.1); the global degraded banner (DESIGN §6.3) was never built, so disk-full shows as a dot. | `topbar.html:12-15` | Visible label when not Healthy; degraded banner → **SFT-118** |
| UX-16 | Medium | Live announcements are chatty and unreliable: three live regions, pause announced up to three times, "Reconnecting" repeated every 3 s during outages, the flash `role=status` exists at page load so it is often not announced and success flashes self-delete after 6 s. | `live.html:64`, `live.js:48-66,341-363`, `shell.html:10`, `app.js:41-45` | One throttled announcer; inject flash after load → **SFT-119** |
| UX-17 | Medium | Per Live event: forced layout (`scrollTop = scrollHeight`), `querySelectorAll` for trimming, `htmx.process`; stress tests only cover 100-event batches. | `live.js:250-264` | `requestAnimationFrame` batching; delegated detail clicks → **SFT-119** |
| UX-18 | Medium | Filter selects and inputs stretch to ~64 px tall in the fieldset grid; duplicate "Source" legend and label. | `app.css:756-780`; `live-desktop-dark.png` | `align-content: start`; remove duplicate → **SFT-120** |
| UX-19 | Medium | The mobile header truncates "Servers" to "Serve…"; the §6.2 single-menu collapse was not built. | `status-mobile-light.png` | Single mobile menu → **SFT-122** |
| UX-20 | Medium | Confirmation labels break into three lines because the label is `display: grid` and `<strong>` takes its own row. | `app.css:1700-1704`; `sources-desktop-dark.png` | → **SFT-120** |
| UX-21 | Medium | Live errors are raw `http.Error` text pages, unlike History's shell error. | `live_page.go:27-37` | Render in the shell → **SFT-118** |
| UX-22 | Medium | Two monospace faces on one screen: `code` and the token input use `ui-monospace`, not `--font-mono`. | `app.css:261-263,1795-1797` | One face → **SFT-120** |
| UX-23 | Medium | Live has no `<noscript>` and stays at "Connecting" without JavaScript (§33.5); export is JavaScript-only and borrows its CSRF token from the logout form. | `live.html`, `app.js:207-219` | `<noscript>`; export form without JS → **SFT-118** |
| UX-24 | Medium | Assets are served `no-cache` without ETag, Last-Modified or compression, so ~85 KB of CSS/JS is re-downloaded on every page view; fonts are `immutable` but their filenames are unversioned. | `ui.go:617-621` | ETag/versioned names; gzip → **SFT-123** |
| UX-25 | Medium | A failed guided test with a non-JSON response navigates to `response.url`, the POST-only test URL, leaving the one-time token page with a 405. | `tokens.js:103-105` | Stay on the page, show the error → **SFT-118** |
| UX-26 | Low | `aria-label` on plain `div`s, which screen readers ignore. | `components.html:6`, `status.html:10` | `role="group"` → **SFT-120** |
| UX-27 | Low | Palette input has `aria-expanded="true"` hard-coded; empty `aria-activedescendant` is written when nothing matches. | `palette.html:5`, `palette.js:122-125` | → **SFT-113** |
| UX-28 | Low | The help overlay omits Ctrl/Cmd+K (§32.5); the button always shows ⌘K on Linux and Windows (§6.1 says platform-specific). | `help-overlay.html`, `topbar.html:17` | → **SFT-113** |
| UX-29 | Low | `<time>` has no `datetime` in Status diagnostics and Audit. | `status.html:68`, `audit.html:41` | → **SFT-115** |
| UX-30 | Low | Headings: an `h2` inside the aside notice; detail pages titled by section ("Sources") rather than entity, and so is `<title>`; Servers labelled three times. | `settings.html:50`, `ui.go:101,105` | Per-entity `h1`/`<title>` → **SFT-122** |
| UX-31 | Low | Backup polls every second and replaces a region that contains `aria-live`. | `backup.html:3,33` | → **SFT-119** |
| UX-32 | Low | htmx `includeIndicatorStyles: true` injects inline CSS the `style-src 'self'` CSP blocks (console violation on every page). | `head.html:4` | → **SFT-123** |
| UX-33 | Low | The Live empty state says "this source" when "All sources" is selected and has no History link (§24.4). | `live.html:57` | → **SFT-122** |
| UX-34 | Low | `.health-indicator` is ~23 px (WCAG 2.5.8 minimum 24); no `scroll-padding-top`, so J/K-focused rows can hide under the sticky header (2.4.11). | `app.css` | → **SFT-113** |
| UX-35 | Info | Dead `pagination.html` partial still unit-tested; `tokens.js` (token page) collides with `tokens.css` (design tokens); `.history-*` classes reused on Sources, Servers, Live; `palette.js` and `app.js` both add key listeners against the single-key-owner rule (§39). | `components.html:19-30`, assets | → **SFT-125**, **SFT-113** |

**Design critique (actionable).** The spec's signature items are mostly
missing: no severity left-edge indicator (`.level-*` rules do not exist), info
not rendered secondary, Unicode glyphs instead of the inline SVG icon set, no
`+N lines` multiline hint, no selection outline distinct from hover; what
remains reads as a generic dark admin. The filter area outweighs the logs (about
300 px of fieldsets above an empty Live list; a single-row filter bar with
"More" would match Dozzle or Explore). Fatal (`#f79a94`) is visually weaker
than error (`#ee7b74`) in dark mode. History is newest-first, Live newest-last,
and the tail line sits above one list and below the other. Eighteen literal font
sizes bypass the tokens (§28.2); help text is ≈11 px. Body copy reads like spec
text ("Guided committed-receipt test", "Bounded local storage controls").
Internal capitalisation of "Server" leaks into the UI. The font ships as a
100–800 variable file instead of the 400/700 subset the spec calls for. No
wrap/clamp toggle for long messages and no match highlight in rows. → **SFT-112**
(design direction review and DESIGN.md amendments), **SFT-121**, **SFT-122**.

**Test gaps.** Asset tests grep source text instead of exercising behaviour.
No coverage for Space on buttons, chevron/Enter collapse, the mobile
filter-change flow, typing during an in-flight request, displayed time zones or
12-hour locales, the cap with "Load newer", error placement, invalid cursors,
Back/Forward after swaps, SSE drops and control events (`possible_gap`,
`retention_purged`, `source_removed`, `shutdown`, 401 on the stream), high-rate
Live, 10k-character lines and 500-line messages, a 1,000-row DOM,
accessibility-tree snapshots, Tab order, 200% zoom / 320 px, visual regression
(screenshots are written but never compared; no login, palette or help
screenshots), Live or export without JavaScript, any browser but Chromium.
→ **SFT-124**.

**Spec corrections.** DESIGN §27.2's `light-dark()` example has its arguments
reversed (the code is right). §27.1 `@layer` and §28.2 "no literal sizes" are
recorded as done in SFT-068 but were not implemented. SFT-079 is marked Done
with manual items recorded as not performed; the registry needs an "Accepted
with exceptions" status. → **SFT-112**, **SFT-125**.

**Done well, keep it.** Strict escaping everywhere (Live rows via
`textContent`, hostile-payload tests on History, Live, aliases, audit); strong
CSP and origin/CSRF; server-rendered preferences with no theme flash that work
without JavaScript; code-only flash cookies with fixed copy; URL-owned History
that works without JavaScript; typed confirmations with exact consequences; the
one-time token flow (`replaceState`, cleared on `pagehide`, placeholder
substitution at copy time); the Live state machine (generation counter against
stale events, bounded pending and rendered caps, pause never drops data);
fixed-width severity codes kept in sync between Go and JS by a test; no charts,
no spinners, reduced motion respected.

---

## 5. Rules and decisions amended by this audit

Adopted by the maintainer on 2026-11-04; each is written as an ADR before its
implementing task runs.

| Current rule | Amendment | ADR task |
|---|---|---|
| Schema `UNIQUE (source_id, container_id)` / `(source_id, container_name)` | Container instances are ephemeral; identity resolves by id with name as a mutable label | SFT-086 |
| ADR 0004 canonical equality includes `EventAtUS` | Equality ignores a defaulted timestamp; conflict policy drops conflicting duplicates with a metric instead of rejecting the batch | SFT-086 |
| DOMAIN §16 retention by `min(event_at, received_at)` | Events already past retention at ingest are flagged or rejected, or retention uses received time | SFT-086 |
| ADR 0001 one transaction per HTTP batch under `synchronous=FULL` | Keep FULL; add group commit (N queued batches in one transaction, SAVEPOINT per batch) | SFT-086 |
| ARCHITECTURE §25.1 online backup API | `VACUUM INTO` single-snapshot copy | SFT-086 |
| Restore requires a healthy current database | Fresh-install restore path | SFT-086 |
| ADR 0003 "audit every authentication outcome", FIFO cap | Aggregate unauthenticated rejections per window; reserve capacity for authenticated rows | SFT-102 |
| ADR 0003 at most two concurrent Argon2 operations | Keep the cap; add bounded admission with fast 429 and per-key in-flight limits | SFT-102 |
| `SIFTAIL_PUBLIC_URL` optional | Mandatory for `serve` | SFT-102 |
| Sessions 14 days absolute / 7 days idle | 7 days absolute / 12 hours idle; "sign out other sessions" | SFT-102 |
| AGENTS §13.4 `_FILE` secrets (dead code) | Implemented properly | SFT-102, SFT-110 |
| AGENTS §21 dependency policy | Adds toolchain and dependency currency plus CI vulnerability scanning | this change |
| AGENTS §17.2 | Benchmarks run on a real-disk `TMPDIR`; tmpfs results are labelled as such | this change |
| DESIGN §32 single-letter shortcuts always on; §8 separate History/Live models; UTC rendering | Shortcuts preference; one shared filter model carried across modes; one timezone preference | SFT-112 |
| TASKS.md status vocabulary | Adds "Accepted with exceptions" | SFT-125 |

---

## 6. Coverage

Read in full: `internal/ingest`, `internal/database` (+ all migrations),
`internal/logs`, `internal/backup`, `internal/app`, `internal/retention`,
`internal/sources/store.go`, `internal/auth` (all handlers), `internal/sessions`,
`internal/config`, `internal/web` (middleware, context, ui), all templates and
first-party JavaScript and CSS, `cmd/siftail/{main,admin,administrator,
healthcheck}.go`, `Dockerfile`, both Compose files, `.env.example`, `ci.yml`,
`tests/browser/siftail.spec.ts`, `playwright.config.ts`, DESIGN.md, ADRs
0001–0005, AGENTS §§7–13, 17, 20, 21, 26, docs/performance, docs/security.

Partially read: `internal/audit/store.go`, `internal/status`,
`internal/sources/{mutations,catalog,management}.go`, most `*_test.go` beyond
targeted reads, ARCHITECTURE.md beyond grepped sections, the minified htmx.

Not executed: Playwright (host has the suite; last green 2026-09-02); any
browser-driven reproduction of UX-03, UX-07, UX-17, which are reasoned from code
and htmx internals; the container build.
