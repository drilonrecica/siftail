# ADR 0010: Authentication, Audit Capacity, Public URL, and Secret Amendments

**Status:** Accepted

**Date:** 2026-11-05

**Amends:** [ADR 0003](0003-single-administrator-authentication.md) (hash
admission, session lifetimes, sign-in auditing, browser origin); `AGENTS.md`
§13.4 (`_FILE` secrets)

## Context

The 2026-11-04 audit (§3 and §5) found that the single-administrator model of
ADR 0003 is right, but its edges fail under abuse or misconfiguration:

- **Login concurrency (SEC-02, SEC-10).** The throttle only reads in `Check`
  and counts a failure after Argon2 finishes. N parallel requests therefore
  get N guesses. The two-slot hash semaphore has an unbounded wait, so a flood
  from rotating addresses queues ahead of the real administrator. The account
  throttle key ignores the client, so one failure a minute from anywhere keeps
  the administrator locked out, and recovery needs CLI access.
- **Audit eviction (SEC-03).** Every rejected or throttled sign-in writes its
  own audit row in a full-sync transaction, and the 100,000-row FIFO cap
  evicts the oldest row of any kind. About 100,000 cheap unauthenticated
  requests erase the whole real audit trail and compete with ingestion for
  the single writer.
- **Browser origin (SEC-08).** `SIFTAIL_PUBLIC_URL` is optional outside
  Compose. Without it, the allowed origin comes from `Host` (DNS rebinding),
  cookies are never `Secure` behind HTTPS, and HSTS is never sent.
- **Session lifetime.** 14 days absolute and 7 days idle is long for a
  credential that can read every application log, and the only way to end
  other sessions is the CLI's revoke-all.
- **`_FILE` secrets (SEC-15).** `AGENTS.md` §13.4, `ARCHITECTURE.md` §9.3, and
  `PRODUCT.md` §17 describe `_FILE` variants. In the code, the `_FILE` branch
  of `readString` is unreachable, because no secret-bearing setting exists,
  and `config.Secret` and `hasEnv` are unused. The contract also leaves open
  the questions an implementation must answer: precedence, empty values,
  newline handling, and file permissions.

## Decision

### Bounded Argon2 admission

1. Keep ADR 0003's Argon2id parameters (32 MiB, three iterations,
   parallelism one) and its cap of **two concurrent hash operations**.
2. Every sign-in attempt that would hash a password gets admission first.
   This includes the dummy hash for an unknown username. Admission order:
   1. **Throttle reservation.** `Check` reserves an attempt for the client key
      and the account key. A success releases the reservation. A failure
      keeps it as a counted failure. A request that is already throttled is
      answered `429` before any hashing or write.
   2. **Per-key in-flight limit.** At most one attempt may be in flight per
      client key and one per account key. A second concurrent attempt for
      the same key gets `429` at once. Client keys group IPv6 addresses by
      /64 and IPv4 addresses by /32, after trusted-proxy resolution.
   3. **Bounded hash queue.** Up to **8** attempts may wait for a hash slot,
      each for at most **2 seconds**. When the queue is full, or the wait
      expires, the reply is `429` with `Retry-After: 2`. Nothing is hashed and
      no audit row is written for that request.
   4. **Recent-client lane.** A client key that completed a successful
      sign-in in the last 7 days (a bounded in-memory set of 64 keys, matching
      the session cap) waits in a separate queue of **2**. That queue is
      served before the general queue. A flood from other addresses can delay
      such a client by at most one hash duration plus one other lane waiter,
      and it can never be rejected for general-queue overflow.
3. **Account lockout only for unproven clients.** The account throttle still
   counts failures from every client. It no longer blocks a client in the
   recent-client lane. That client remains subject to its own client throttle.
   A remote attacker can no longer keep the administrator locked out from a
   network the administrator recently signed in from.
4. All numbers above are constants, not configuration. SFT-104 may tune them
   from measurements and record the final values in `ARCHITECTURE.md` §19.5.

### Aggregated unauthenticated audit rows

5. Rejected sign-in attempts are no longer audited one row each. These are
   wrong credentials, unknown usernames, throttle `429`s, in-flight `429`s,
   and admission-overflow `429`s. The process counts them in memory and
   writes **one aggregated row per 5-minute window**, and only for windows
   that had at least one rejection. A row has:
   - action `sign_in.rejected_summary`, outcome `rejected`, and actor
     `unauthenticated`;
   - `occurred_at` at the window end, with the window start in metadata;
   - counts per closed reason: `invalid_credentials`, `throttled`,
     `in_flight`, and `overloaded`;
   - the number of distinct client keys, counted exactly up to 1,024 and
     reported as "at least 1,024" beyond that;
   - the most frequent client summary and its count.

   Metadata stays within the existing closed-key, 12-field, 2 KiB limits.
6. The throttle and admission decisions happen before any write. A request
   that ends in a rejection never opens a coordinator transaction of its
   own. The aggregate is written by one owned worker through the coordinator,
   at most once per window. On graceful shutdown it is flushed. A crash loses
   at most the open window; that loss is accepted.
7. **Reserved capacity.** Rows with actor `unauthenticated` have their own cap
   of **10,000**. An insert above that cap evicts only the oldest
   `unauthenticated` row. The overall 100,000-row cap still applies. Because
   unauthenticated rows can never exceed 10,000, at least 90,000 rows remain
   for administrator, local-operator, and system actions. No unauthenticated
   write can evict any of those. The count uses a second trigger-maintained
   counter, as migration 0009 does for the total.
8. Successful sign-ins remain individual rows (`sign_in`, `succeeded`,
   administrator actor). So do every privileged action and every
   local-operator action.

### Mandatory `SIFTAIL_PUBLIC_URL`

9. `siftail serve` refuses to start without a valid `SIFTAIL_PUBLIC_URL` and
   names the variable in the error. `siftail config validate` reports the
   same error, because it validates the configuration `serve` would use.
   Commands that serve no browser traffic, such as `admin`, `sessions`,
   `server`, `token`, `database`, `backup`, `restore`, and `healthcheck`, do
   not require it.
10. The value is an exact origin: `http` or `https`, a host, an optional port,
    and an empty or `/` path. It has no credentials, query, or fragment.
    Sub-path deployment is not supported.
11. The browser origin check and the `Secure` cookie decision use only this
    configured origin, never `Host` or forwarded headers.
12. With an `https` public URL:
    - the session cookie is `__Host-siftail_session`, with `Secure`,
      `Path=/`, no `Domain`, `HttpOnly`, and `SameSite=Strict`;
    - the UI listener sends `Strict-Transport-Security: max-age=31536000`.
      It does not set `includeSubDomains` or `preload`, because the
      ingestion host may be a different subdomain under different rules. The
      ingestion listener never sends HSTS.
13. With an `http` public URL, the cookie stays `siftail_session` without
    `Secure`, and no HSTS is sent. This mode exists for loopback and private
    access. If the host is not a loopback address or `localhost`, startup
    logs one warning by category.

### Session lifetimes and "Sign out other sessions"

14. Sessions get an **idle lifetime of 12 hours** and an **absolute lifetime
    of 7 days**. Both are constants. The cap of 64 active sessions, token
    rotation on sign-in, and the 7-day cleanup grace after expiry stay as
    they are.
15. The absolute limit is enforced at lookup as the earlier of the stored
    `expires_at_us` and `created_at_us + 7 days`. Sessions created before
    the upgrade, which store a 14-day expiry, therefore follow the new limit
    without a data rewrite.
16. Settings offers **Sign out other sessions**. It revokes every active
    session of the administrator except the current one, in one transaction,
    and writes a `session.revoke_others` audit row with the revoked count.
    Live streams on revoked sessions end within the existing 5-second
    re-check. Password reset and `siftail sessions revoke-all` keep revoking
    every session.

### `_FILE` secrets contract

17. **Scope.** The contract applies only to process settings that the
    configuration registry declares secret. Each secret `SIFTAIL_X` has
    exactly one file variant, `SIFTAIL_X_FILE`. Non-secret settings have no
    `_FILE` variant; a name such as `SIFTAIL_PUBLIC_URL_FILE` is an unknown
    reserved variable and fails validation. Version 0.5.0 declares **no**
    secret setting. SFT-110 implements and tests the mechanism, using a
    test-only registration, so that the first real secret inherits it
    unchanged.
18. **Precedence and conflicts.** There is no precedence. If both the direct
    and the file variable are present in the environment, startup fails, even
    if either is empty. The error names both variables and never shows a
    value. A secret that is present with an empty direct value, or that is
    empty after trimming its file, is invalid.
19. **Reading.** The path must be absolute. The file is read once at startup
    and never reloaded. It must resolve, following symlinks as Docker and
    Kubernetes secret mounts require, to a regular file of at most 64 KiB.
    The content must be valid UTF-8 without NUL.
20. **Newlines.** All trailing CR and LF bytes are removed, and nothing else
    is. Leading or trailing spaces and tabs are kept. A CR or LF left inside
    the value after trimming is an error, because a secret is one line.
21. **Permissions.** Startup refuses a file that is group- or world-writable,
    because another account could replace the secret. A world- or
    group-readable file is accepted without a warning. Docker and Kubernetes
    mount secrets as `0444` or `0644` inside an isolated container
    filesystem, so a warning would fire on every correct deployment. The
    operator documentation recommends `0400` or `0440` on the host.
22. **Disclosure.** Error messages name the variable and the path, never the
    content or its length. Sanitized configuration shows a secret as
    `[redacted]` and says whether it came from the environment or a file. The
    value is held only by the component that uses it.

## Alternatives

- **Raise the hash concurrency or lower the Argon2 cost.** This weakens the
  memory bound or the guessing cost. Bounded admission keeps both and turns
  overload into a cheap, fast `429`.
- **An unbounded or very long hash queue.** This turns a flood into
  unbounded latency and memory and still starves the administrator.
- **Remove the account throttle.** Distributed guessing across many
  addresses would then face only per-client limits. The recent-client
  exemption removes the lockout without removing the account limit.
- **A CAPTCHA or proof-of-work on sign-in.** `PRODUCT.md` §10.9 excludes
  CAPTCHA and external dependencies. Proof-of-work adds a JavaScript
  dependency to the no-JavaScript sign-in path.
- **Keep one audit row per rejection and only raise the cap.** A larger cap
  delays eviction but does not prevent it, and every rejection still costs a
  full-sync write.
- **Do not audit unauthenticated rejections.** The operator would lose the
  signal that someone is guessing. Aggregates keep the signal at a bounded
  cost.
- **Derive the origin from `Host` when `SIFTAIL_PUBLIC_URL` is unset.**
  This is the current behavior, and it is the DNS-rebinding and
  insecure-cookie gap the audit found.
- **Require `https` always.** That breaks loopback and private-network use,
  where TLS has no terminator. The `http` warning outside loopback is the
  proportionate control.
- **Make session lifetimes configurable.** That adds surface for a
  single-administrator tool. Constants keep one reviewed policy.
- **`_FILE` precedence (file wins) instead of a conflict error.** A silent
  winner hides misconfiguration. `AGENTS.md` §13.4 already requires
  rejection.
- **Reject world-readable secret files.** That breaks the standard Docker
  and Kubernetes secret mounts.
- **Remove `_FILE` support until a secret exists.** The documents have
  promised it since version one, and fixing the contract now prevents an ad
  hoc design when the first secret arrives.

## Consequences

- Parallel requests cannot multiply guesses. A flood cannot hold the hash
  slots for unbounded time. A recently seen administrator signs in within a
  bounded delay during a flood. After a restart the recent-client set is
  empty, so until the next success the administrator competes in the general
  queue. That tradeoff is documented in `docs/security`.
- Unauthenticated noise costs at most one audit write per 5 minutes. It can
  occupy at most 10,000 rows, about 34 days of continuous attack in summary
  rows, and it can never evict administrator, local-operator, or system
  history.
- The audit view shows rejected sign-ins as window summaries, not as
  individual attempts. Exact per-attempt timing within a window is not
  retained.
- Every `serve` deployment must set `SIFTAIL_PUBLIC_URL`. Compose already
  requires it. A direct `docker run` without it now fails at startup with a
  clear error.
- On an `https` deployment, upgrading renames the cookie, so every
  administrator signs in again once. Browsers that reach Siftail over `https`
  remember HSTS for one year.
- Administrators sign in more often: after 12 hours idle and at least weekly.
- The `_FILE` mechanism is real and tested, but no shipped variable uses it
  yet. The documents say so.

## Migration and compatibility impact

SFT-105 adds one additive migration for the reserved-capacity counter: a
trigger-maintained count row for `unauthenticated` audit rows, initialized
from the existing rows. It ships with a fixture test. Existing per-attempt
`sign_in` rejection rows stay as they are, count toward the new reserved cap,
and age out normally. The session schema is unchanged; the shorter absolute
lifetime is enforced at lookup (point 15). `SIFTAIL_PUBLIC_URL` becomes
required for `serve`, which breaks deployments that omitted it outside
Compose; the release notes and `docs/operations/docker-compose.md` say so.
The cookie rename on `https` invalidates existing browser sessions once. No
ingestion contract, backup format, or CLI flag changes.

ADR 0003's remaining decisions stand: one local administrator, Argon2id
parameters, opaque hashed tokens, the 64-session cap, CLI-only creation and
reset, the control socket, and no trusted identity headers.

Implemented by SFT-104 (admission and throttle), SFT-105 (audit aggregation
and reserved capacity), SFT-106 (public URL, cookies, HSTS, session lifetimes,
sign out other sessions), and SFT-110 (`_FILE` mechanism).
