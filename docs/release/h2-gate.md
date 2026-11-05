# H2 gate: security and privacy

**Completed:** 2026-11-05
**Scope:** SFT-102 through SFT-110 (H2), reviewed by SFT-111
**Release action:** None. No tag, image, or publication.

This is the self-check gate for phase H2. It records an adversarial review of
every H2 change against ADR 0010, `AGENTS.md` §13, and the audit findings
SEC-01 to SEC-15, plus the verification runs the task required. Every figure
below was measured in this session on the tree at `bcd86bf` plus this task's
working changes; nothing is carried over from the implementing tasks' own
evidence. The companion security review is
`docs/security/0.5.0-hardening-review.md`.

## Verdict

**Pass. No blocker.** The review found one Medium defect in the new login
throttle, introduced by SFT-104 and fixed in this task with a failing test
first (finding 1), and one robustness gap closed with a one-line guard
(finding 2). No other H2 change deviates from ADR 0010 or reopens an audit
finding. `govulncheck` reports nothing the code calls. The Playwright suite
passes 24 of 24 including the four security scenarios. `make check` and `make
race-test` pass on the final tree. Six observations are left for the owner or
the backlog (findings 3 to 8); none weakens a shipped control.

## Environment

Fedora Linux 44, Linux 7.2.10 x86-64, four logical CPUs, Go 1.27.3 (toolchain
line `go1.27.3`), go-sqlite3 v1.14.52, golang.org/x/crypto v0.58.0, Node.js
24.21.0, Playwright 1.64.1 with Chromium. `/tmp` is tmpfs; `/var/tmp` is btrfs
on a SATA SSD. Tests are not benchmarks, so they ran on the default temp
directory (`AGENTS.md` §17.2 binds benchmarks, not tests).

## Results

Go commands ran with `GOFLAGS=-count=1`, so no result came from the test
cache. Two full passes were made. The first ran while three review agents
were executing Go tests on the same four CPUs (load average 7.6) and with
`TMPDIR=/var/tmp`; it is kept here because its failures are informative for
the owner (finding 8). The second ran alone on the final tree.

| Command | Result | Duration |
|---|---|---|
| `make check` (fmt-check, vet, test, build, compose-check), final tree, alone | pass, 16 packages ok | 28 s |
| `make race-test`, final tree, alone | pass, 16 packages ok, no races | 54 s |
| `govulncheck ./...` (latest) | 0 vulnerabilities affecting the code, 0 in imported packages, 1 in a required module the code does not call (unchanged from the H0 and H1 gates) | 9 s |
| `make playwright`, final tree | pass, 24 of 24 scenarios, including the four security scenarios listed below (also 24 of 24 in 106 s on the first pass) | 75 s |
| `git diff --check` | clean | n/a |
| First `make check`, under load with `TMPDIR=/var/tmp` | 14 packages ok; `cmd/siftail` failed `TestRestoreCLIRequiresStoppedServerConfirmationAndStaysSecretFree` (socket path length, finding 8); `internal/backup` failed `TestCreateFullBackupCompletesUnderContinuousIngestion` ("backup overlapped 136 commits, want at least 200", a timing assertion under load) | 48 s |
| First `make race-test`, same conditions | 14 packages ok; the same restore test; seven `internal/app` tests hit the 3-second startup wait ("server did not become ready", one "inspect fresh database: context deadline exceeded") | 188 s |
| Isolated rerun of the restore test, `TMPDIR=/var/tmp` then default | fail in 0.24 s, then pass in 0.14 s | n/a |
| `go test -run 'Throttle\|HashGate\|Login' ./internal/auth` after the fix | pass; the new test failed on the unfixed code first | 6 s |

## Adversarial review

Method: the gate session read in full the sign-in path
(`internal/auth/browser.go`, `password.go`, `throttle.go`, `signin_audit.go`,
`proxy.go`, the revoke-others handler in `settings.go`), the session store and
cookie policy (`internal/sessions/store.go`, `cookie.go`), the audit capacity
code and migration 0010 (`internal/audit/store.go`), the browser backup
directory (`internal/backup/directory.go`, `internal/auth/backup.go`), the
`_FILE` mechanism and public-URL validation (`internal/config/secret.go`,
`config.go`), the ingestion token check (`internal/ingest/http.go`), the
listener wiring in `internal/app/app.go`, and the four Playwright security
scenarios. Three independent reviewers, given only the specification sections
(ADR 0003, ADR 0010, `AGENTS.md` §13, the audit rows, the security docs) and
the files, not the implementing tasks' conclusions, covered authentication,
throttle, sessions and audit aggregation; the browser request boundary
(return path, origin, cookies, HSTS, public URL parsing, forwarded headers,
control socket, templates); and ingestion authentication, the generated
collector configuration, the backup directory, and `_FILE` secrets. The
second reviewer also ran the pure validators (`safeReturnPath`,
`publicOrigin`, `validatePublicOrigin`) against about 75 adversarial inputs in
a scratch program outside the repository. Every finding below was re-read in
the code by the gate session before it was recorded; the two fixed ones were
reproduced or guarded by a test first.

### Invariants checked and holding

Sign-in admission (ADR 0010 points 1 to 4; SEC-02, SEC-10):

- Order is reservation, in-flight limit, lane choice, bounded hash queue,
  Argon2, resolution. `Check` counts the failure and takes both in-flight
  slots before any hashing or write (`throttle.go` `Check`); every exit of
  `login` resolves the reservation exactly once: `Success`, `Failure`, or
  `Release` for overload, infrastructure error, and a failed session write
  after a correct password. Context cancellation can only return before the
  hash runs, so no evaluated guess goes uncounted.
- An unknown or disabled username costs the same Argon2 work on the dummy
  hash with production parameters, the same 401 text, and the same throttle
  accounting; lane selection depends only on the client key, never on the
  username, so the recent lane reveals nothing about account existence.
- The recent-client set is keyed by the canonical client throttle key (IPv4
  /32, IPv6 /64, zone stripped, IPv4-mapped unmapped), bounded at 64 with the
  oldest success evicted, and only `Success` adds to it. A recent client is
  still bound by its own client throttle. The client identity is always a
  parsed address or the literal `unknown`; `X-Forwarded-For` is consulted
  only when the immediate peer is in a configured trusted network, joined
  across header lines, walked right to left to the first untrusted address.
- The hash gate hands a freed slot straight to the next waiter in strict
  lane order (internal, recent, general); a wait that expires as the slot
  arrives uses it, a cancellation that races a grant releases it. The
  unbounded internal lane is reachable only from CLI create and reset.
- The Playwright throttle scenario and the SFT-104 concurrency tests (16
  parallel wrong passwords, one counted guess) pass on this tree.

Sessions (ADR 0010 points 14 to 16; SEC-09):

- `Lookup` returns `ErrInvalidSession` only for an unknown token, a disabled
  administrator, or a revoked, expired, or idle row; any database error wraps
  `ErrSessionUnavailable`, and `Protect` answers 503 with `no-store` and
  `Retry-After` without touching the cookie. The 7-day absolute limit is
  applied from `created_at_us` in the read clamp, every active predicate, the
  guarded touch, and cleanup, so 14-day legacy rows follow it without a
  rewrite. A failed touch is skipped; a touch that matched no row is re-read
  and re-judged, so a parallel request cannot sign the administrator out
  while a racing revocation still does.
- "Sign out other sessions" is a POST under `Protect` (form content type,
  exact configured origin, constant-time CSRF, exact form), excludes the
  current session by its server-side row ID, re-verifies that session inside
  the transaction, and audits only `session_count`.

Browser boundary (ADR 0010 points 9 to 13; SEC-01, SEC-07, SEC-08, SEC-13):

- The origin check compares `Origin`, or the `Referer` scheme and host,
  against the configured public origin only; no code reads `Host`,
  `X-Forwarded-Host`, or `X-Forwarded-Proto` (grep over `internal/` and
  `cmd/` finds none). Absent headers, `Origin: null`, and an empty
  configured origin fail closed.
- The return path is checked raw and percent-decoded for backslash, C0, C1,
  DEL, Unicode whitespace, invalid UTF-8, absolute form, host, opaque form,
  and a `//` prefix. All 20 bypass candidates from the audit and the reviewer
  were refused; every accepted value yields a same-origin `Location` after
  `http.Redirect` cleans the path.
- Cookie: `__Host-siftail_session` with `Secure`, `Path=/`, no `Domain`,
  `HttpOnly`, `SameSite=Strict` exactly when the public URL is https;
  `Protect` reads only the policy's name, so the legacy unprefixed cookie is
  never accepted on https and is expired on sign-in and sign-out. HSTS
  `max-age=31536000` without `includeSubDomains` or `preload` comes only from
  `SecurityHeaders` on the UI server; the ingestion and control servers use
  `web.APIHeaders`. The 503 session-unavailable page is rendered beneath
  those headers.
- `siftail serve` and `config validate` call `ValidateServe`; every other
  subcommand parses without a public URL. Userinfo, query, fragment, a
  non-empty path, other schemes, port 0 or above 65535, and whitespace are
  refused. The plain-http warning logs a category only.
- A `SO_PEERCRED` error or UID mismatch closes that one connection and the
  accept loop continues; the socket is mode 0600 and a stale socket is
  replaced only when owned by the current user and not a symlink.
- No `template.HTML`, `JS`, `URL`, or `HTMLAttr` in non-test code; the only
  template function is `severityCode`; every script tag is `src="/assets/…"`
  served from a basename allow-list; the CSP has no inline exception.

Audit capacity (ADR 0010 points 5 to 8; SEC-03):

- No rejection path performs I/O: every one calls only `signInRejections.add`.
  The summary row is written by the owned writer at window end and by the
  shutdown flush after the UI server drains and before the coordinator
  closes. The success row is written inside `Issue`'s transaction after a
  verified password.
- Migration 0010 adds only a trigger-maintained counter (insert and delete,
  `WHEN actor_type = 'unauthenticated'`) and a partial index; the 0004
  immutability trigger makes counter drift by `UPDATE` impossible.
- An unauthenticated insert evicts only through `WHERE actor_type =
  'unauthenticated'`; if that cannot satisfy the total cap the write fails
  with `ErrCapacityInvariant` rather than touching a privileged row. Cleanup
  applies age, then the sub-cap, then the total, each bounded by 1,000 rows.
- Summary metadata uses eight closed keys with canonical digits; `top_client`
  is the throttle key without its prefix, which is always a canonical address
  prefix or `unknown`, so no raw attacker string reaches metadata.

Ingestion authentication and generated configuration (SEC-05; ADR 0004):

- A missing, malformed, duplicate, non-Bearer, or whitespace-containing token
  gets the same 401 as a wrong token; the token is verified before any body
  byte is read; the comparison is constant time against every candidate with
  a dummy compare on a miss; a database error during verification is 503 for
  every token.
- The token is re-checked for revocation inside the write transaction and
  every event's Server must equal the authenticated Server.
- The ingestion listener sends no HSTS. Request logs carry method, route
  pattern, status, duration, and request ID only.
- The token enters the generated configuration only after matching
  `^sft_[A-Za-z0-9_-]{43}$`, the Server name is never interpolated, and both
  templates and both committed fixtures bind `127.0.0.1`.

Backup directory (SEC-11):

- The browser submits a bare name matched by `ValidFileName` (ASCII,
  alphanumeric first byte, at most 128 bytes); the path is always a direct
  child of the symlink-resolved directory; an existing path of any type at
  the artifact name is refused before staging and `link(2)` cannot replace
  one; verification refuses symlinks, non-regular files, and
  group-or-world-accessible artifacts; missing, non-regular, and corrupt
  artifacts all become the same failed state, so there is no existence
  oracle; no server path and no submitted name reach the browser.
- The CLI keeps arbitrary paths over the owner-only control socket.

`_FILE` secrets (ADR 0010 points 17 to 22; SEC-15):

- The registry is empty, so every `SIFTAIL_*_FILE` name is an unknown
  variable and startup fails. The mechanism itself: both variants set is a
  conflict by presence, not value; relative paths refused; `stat` before
  `open` and `fstat` after, so a FIFO cannot block and the checked file is
  the read file; the mode check applies to the resolved target; the read is
  bounded by `LimitReader` as well as the size check; only trailing CR and LF
  are trimmed; errors name the variable and path only; `Secret` redacts
  through every `fmt` verb, JSON, text, and `slog`, with the value behind a
  closure so reflection through `Config` cannot print it.

### Findings

1. **Medium, fixed. Throttle capacity eviction could erase a locked account's
   delay.** `loginThrottle.bound` evicted the least recently seen idle entry
   whenever the map exceeded 4,096 keys, regardless of that entry's failure
   history. Because `Release` leaves a zero-failure entry behind, an attacker
   who kept the hash gate full could send about 4,100 cheap requests with
   fresh usernames from one address, each withdrawn at once as overloaded,
   and push the administrator's account entry, with its five counted failures
   and 60-second delay, out of the map. The account throttle then restarted
   from zero, which is exactly the SEC-02 and SEC-10 condition SFT-104
   closed. Reproduced by
   `TestThrottleCapacityEvictionKeepsThrottledKeys` (`internal/auth/
   throttle_test.go`), which failed on the unfixed code. Fix in
   `internal/auth/throttle.go`: eviction now ranks idle entries in tiers,
   first entries with no failure history, then entries whose delay has
   passed, then entries inside an active delay, least recently seen first
   within a tier. Evicting a delayed entry would require 4,096 keys that each
   completed five counted Argon2 failures inside the 60-second delay, which
   the two hash slots cannot deliver. In-flight entries are still never
   evicted. `docs/security/sign-in-throttling.md` records the rule.
2. **Info, fixed. No guard resolved the reservation if a panic escaped
   `login`.** `web.PanicRecovery` would answer 500 but both keys would stay in
   flight until restart, so that username would be `429 in_flight` for every
   non-recent client. No reachable panic was found on the path. `login` now
   defers `Release`, which is a no-op once the reservation is resolved.
3. **Low, for the owner. An unauthenticated summary row can be deleted by
   its own insert.** When privileged rows already fill all 100,000 slots,
   which privileged inserts can bring about because `enforceCap` evicts rows
   of any kind, `enforceUnauthenticatedCap` satisfies the one-row total
   excess by deleting the oldest unauthenticated row, which is then the row
   just inserted. The ADR 0010 invariant holds (no privileged row is ever
   evicted), but the guessing signal is lost silently and the writer treats
   the window as written. It needs about 274 privileged rows a day for a
   year. Options are a small reserved minimum for unauthenticated rows or a
   failure that keeps the window pending; both change ADR 0010 point 7, so
   this is the owner's call.
4. **Info, for the owner. The shutdown flush dates the open window at its
   end and a restart inside the window writes a second row.** The flush
   writes `occurred_at = window start + 5 min`, up to five minutes in the
   future, and the new process opens a window with the same
   `window_started_at`. ADR 0010 point 6 says "at most once per window" and
   also mandates the flush; the default Audit page hides the future-dated
   row until the window end passes. A fix is to date a final flush at
   `min(window end, now)`; it changes point 5, so it is recorded, not done.
5. **Info, backlog. `top_client` is the most frequent tracked client.**
   After 1,024 distinct keys in a window, further keys are not counted, so a
   key that appears only after that point cannot be the reported top client.
   The distinct count is already marked `1024+`; the doc could say the same
   about `top_client`.
6. **Low, for the owner. The backup directory may alias the data directory
   through a symlink.** `validateBackupDir` compares cleaned paths without
   resolving symlinks, while `Directory.join` resolves them. An operator who
   points `SIFTAIL_BACKUP_DIR` at a symlink to the data directory passes
   validation and browser backups then land beside `siftail.db`. The live
   files, their `-wal` and `-shm`, and the control socket are protected by
   the exists check, and nothing is read or written outside the resolved
   directory. Operator misconfiguration only; an `EvalSymlinks` comparison
   at startup would close it.
7. **Low, already tracked. htmx injects an inline `<style>` that the CSP
   blocks.** `includeIndicatorStyles` defaults to true and the `htmx-config`
   meta does not disable it, so every page logs a CSP violation. This is
   SEC-16, assigned to SFT-123; `app.css` already defines the indicator
   rules. No change here.
8. **Info, for the owner. Non-canonical public URLs fail closed into a
   lockout, and the test harness has two environmental sensitivities.**
   `SIFTAIL_PUBLIC_URL` values such as `https://host:0443`,
   `https://127.0.0.1:08080`, a non-punycode hostname, an uncompressed IPv6
   literal, or a zoned address pass `config validate` but serialize to an
   origin no browser sends, so every POST including sign-in is refused. This
   is safe but unfriendly; canonicalizing the port, punycoding the host, and
   compressing IPv6 in `validatePublicOrigin` would turn it into a
   validation error. Separately, the first verification pass showed that
   (a) `TestRestoreCLIRequiresStoppedServerConfirmationAndStaysSecretFree`
   fails whenever `TMPDIR` is four bytes longer than `/tmp`, because
   `/var/tmp/<64-byte test name><digits>/001/siftail-control.sock` reaches
   the 108-byte Unix socket limit (the H0 gate flagged this), and (b) the
   3-second startup wait in `internal/app` tests and the 200-commit overlap
   assertion in `TestCreateFullBackupCompletesUnderContinuousIngestion` fail
   under heavy CPU contention. Both passed alone on the final tree. A
   shorter test name or an explicit short data directory, and a load-aware
   startup wait, would remove the fragility.

Two reviewer notes were examined and recorded as expected behavior: malformed
`X-Forwarded-For` entries are skipped rather than ending the walk, which only
matters if the operator lists a client's own network as a trusted proxy and
the runbook already says to list exact proxy CIDRs; and a client that shares
the administrator's NAT or /64 shares the recent-client exemption, which the
throttling doc documents as a tradeoff.

## Playwright security scenarios

The suite ran twice, 24 of 24 both times: on the first pass against the
tree at `bcd86bf` in 106 seconds and again on the final tree with the fixes
above in 75 seconds. The four scenarios that exercise H2 controls end to end:

| Scenario | What it proves |
|---|---|
| login, canonical History default, security headers, and logout | CSP, `nosniff`, `Referrer-Policy`, COOP, CORP, `X-Frame-Options`, `no-store` on an authenticated page; logout clears the cookie |
| session cookie follows the http public URL and Settings signs out other sessions | `siftail_session` with `HttpOnly`, `SameSite=Strict`, `Path=/`, not `Secure`, no `__Host-` cookie on http, 7-day expiry; a second browser context is signed out by "Sign out other sessions" via keyboard, the acting context stays signed in, the `session.revoke_others` row appears on the Audit page; axe clean in dark at desktop and light at 390 px |
| login behind an https public URL uses a `__Host-` cookie, HSTS, and only that origin | a second server with `https://logs.playwright.test`: HSTS on the UI listener and none on ingestion; an `Origin` matching only the `Host` header is refused with 403 (DNS rebinding); `__Host-siftail_session` with `Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`, no `Domain`; the legacy cookie is deleted |
| login failures are uniform and throttle the fifth attempt | a known and an unknown username get the same 401 text; the fifth failure is 429 with a positive `Retry-After` and the "Too many attempts" copy |

The backup scenario additionally checks that absolute paths and traversal are
refused in the UI, that the generated configuration name is used, and that
artifacts are 0600 inside a 0700 directory.

## Cumulative verification commands

```bash
GOFLAGS=-count=1 make check
GOFLAGS=-count=1 make race-test
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
make playwright
git diff --check
GOFLAGS=-count=1 go test -run 'Throttle|HashGate|Login' ./internal/auth
# Reproduction of finding 1 before the fix:
#   go test -count=1 -run TestThrottleCapacityEvictionKeepsThrottledKeys ./internal/auth
#   --- FAIL ... account lock was evicted by the flood: decision = 0
# Reproduction of finding 8(a):
#   TMPDIR=/var/tmp go test -count=1 -run TestRestoreCLI ./cmd/siftail   # FAIL in 0.24 s
#   go test -count=1 -run TestRestoreCLI ./cmd/siftail                   # ok
```
