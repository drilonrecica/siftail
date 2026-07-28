# Siftail Implementation Tasks

**Status:** Non-authoritative execution tracker  
**Detailed horizon:** `0.1.0` and `0.2.0`  
**Canonical roadmap:** `PRODUCT.md` §19  
**Canonical implementation order:** `ARCHITECTURE.md` §40

---

## 1. How to use this tracker

`TASKS.md` records implementation order, dependencies, and completion status. It
does not define product behavior. When a task conflicts with accepted tests,
migrations, `DOMAIN.md`, `PRODUCT.md`, `ARCHITECTURE.md`, `DESIGN.md`, or an
accepted ADR, those sources win and this tracker must be corrected.

Task IDs are permanent. Never renumber or reuse them.

Allowed statuses:

- `Planned`: accepted work whose dependencies or scheduling are not ready;
- `Ready`: dependencies are complete and implementation may begin;
- `In Progress`: actively being implemented;
- `Blocked`: cannot progress; the task records the concrete blocker;
- `Done`: acceptance criteria and required verification are complete.

Update status and add the issue or PR link in the implementation change. A task
is not `Done` when compilation alone succeeds or required verification is
deferred without an explicit follow-up.

When a task is `Blocked`, add a `Blocker` field naming the concrete missing
decision, dependency, authority, or external state. Remove that field when the
blocker clears.

Tasks are sized for focused pull requests. One pull request may complete more
than one tightly coupled task only when it updates every affected task entry and
keeps review scope coherent.

After a milestone release, retain its task records in this file inside a
collapsed `<details>` section. Git history is not a substitute for the released
milestone record.

### Task entry template

```markdown
### SFT-NNN — Outcome-oriented title

**Status:** Planned  
**Milestone:** 0.x.0  
**Depends on:** SFT-NNN  
**Issue/PR:** —

**Authoritative references:** Relevant specification and ADR sections.

**Outcome:** The concrete capability delivered.

**Acceptance:**

- Observable completion condition.

**Verification:**

- Commands and focused scenarios that must pass.

**Impact:** Migration, documentation, security/privacy, and resource effects.
```

---

## 2. `0.1.0` — Durable ingestion and storage

Milestone authority: `PRODUCT.md` §19.1. No polished browser UI belongs in this
milestone.

### SFT-001 — Bootstrap the Go command and repository foundation

**Status:** Done
**Milestone:** 0.1.0
**Depends on:** None
**Issue/PR:** — (direct maintainer implementation, commit `e89931a`)
**Completed:** 2026-07-28
**Completion evidence:** CLI dispatcher tests and metadata-bearing binary invocation.

**Authoritative references:** `AGENTS.md` §§6–7; `ARCHITECTURE.md` §§3, 5, 30.

**Outcome:** Create the Go module, `cmd/siftail` command dispatcher, required
feature-package layout, build/version metadata, and functional `version` command.

**Acceptance:**

- The repository builds one `siftail` binary with no production Node.js runtime.
- Unknown commands fail with safe help and a nonzero exit status.
- `siftail version` reports version, commit, build date, and Go version without
  opening the database.
- No placeholder feature package or speculative abstraction is added.

**Verification:**

- `go fmt ./...`
- `go vet ./...`
- `go test ./...`
- Build and invoke `siftail version`.

**Impact:** Adds the initial Go dependency surface and package ownership
boundaries; no database migration or user-visible feature.

### SFT-002 — Establish development quality gates

**Status:** Done
**Milestone:** 0.1.0
**Depends on:** SFT-001
**Issue/PR:** — (direct maintainer implementation, commit `b09b849`)
**Completed:** 2026-07-28
**Completion evidence:** Local `make check` parity and the CI check/race workflows.

**Authoritative references:** `AGENTS.md` §§18–20; `ARCHITECTURE.md` §§32–35.

**Outcome:** Add repeatable local and CI checks for formatting, vetting, tests,
CGO compilation, and artifact metadata.

**Acceptance:**

- CI runs formatting verification, `go vet ./...`, and `go test ./...`.
- The supported Go and CGO toolchain is pinned in one documented location.
- Race-test execution can be enabled when concurrency code lands without
  restructuring the workflow.
- CI logs and artifacts contain no repository or environment secrets.

**Verification:**

- Run every configured check locally or through its documented dry-run path.
- Confirm the produced binary uses the selected SQLite/CGO build path once that
  dependency is introduced.

**Impact:** Development-only automation; no production runtime dependency.

### SFT-003 — Implement configuration and safe process logging

**Status:** Done
**Milestone:** 0.1.0
**Depends on:** SFT-001
**Issue/PR:** — (direct maintainer implementation, commit `1edf3a8`)
**Completed:** 2026-07-28
**Completion evidence:** Configuration boundary, `_FILE`, and sensitive-log capture tests.

**Authoritative references:** `ARCHITECTURE.md` §9; `AGENTS.md` §§7.4, 13.4.

**Outcome:** Parse and validate process configuration and initialize structured,
payload-safe process logging before other components start.

**Acceptance:**

- Documented `SIFTAIL_` settings, defaults, durations, sizes, URLs, addresses,
  and `_FILE` behavior validate deterministically.
- Unknown `SIFTAIL_` variables and contradictory direct/file secrets fail
  startup; unrelated environment variables are ignored.
- Sanitized effective configuration never exposes secrets or full environment
  contents.
- Process logs use safe fields and never include request bodies, messages,
  authorization values, credentials, or hashes.
- `siftail config validate` performs validation without opening SQLite.

**Verification:**

- Table-driven configuration and `_FILE` tests.
- Negative tests for every startup validation category.
- Log-capture tests proving sensitive values are absent.

**Impact:** Defines process configuration compatibility; no migration. Review
memory-limit defaults and secret handling.

### SFT-004 — Implement application lifecycle and local control transport

**Status:** Done
**Milestone:** 0.1.0
**Depends on:** SFT-003
**Issue/PR:** — (direct maintainer implementation, commit `88d91e9`)
**Completed:** 2026-07-28
**Completion evidence:** Listener, signal, timeout, socket, panic, and race tests.

**Authoritative references:** `ARCHITECTURE.md` §§4, 6–8, 30.

**Outcome:** Compose the application root, separate UI and ingestion listeners,
structured cancellation, graceful shutdown, and owner-only Unix control socket
used by administrative CLI commands.

**Acceptance:**

- Both listeners run in one long-lived process with distinct middleware roots.
- Critical component failure cancels the application exactly once.
- Shutdown marks readiness unhealthy, stops current listener admission, bounds
  active HTTP work by the configured timeout, and closes resources introduced
  by this task in documented order. Queue-drain and WAL-checkpoint integration
  remain assigned to SFT-013 and SFT-005 respectively.
- The control socket is owner-only, is removed on clean shutdown, rejects
  unauthorized access, and exposes no TCP administration API.
- HTTP panic recovery returns a generic error with request ID; lifecycle panics
  remain process-fatal.

**Verification:**

- Startup, listener-collision, cancellation, signal, timeout, and stale-socket
  tests.
- `go test -race ./...`
- Goroutine/leak check where practical.

**Impact:** Adds concurrency and a local administrative boundary; no public API
or migration. Security review covers socket permissions and safe errors.

### SFT-005 — Implement the SQLite lifecycle

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-003  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Real SQLite pragma, pool, corruption, compatibility,
checkpoint, close, application-lifecycle, and race tests; linux/amd64 binary
measured at 13,291,176 bytes from a 9,274,021-byte baseline.

**Authoritative references:** `ARCHITECTURE.md` §§10.1–10.5; ADR 0001.

**Outcome:** Open and close SQLite using the selected driver, required pragmas,
controlled writer/read connections, integrity checks, and schema compatibility
guardrails.

**Acceptance:**

- Fresh databases use WAL, `synchronous=FULL`, foreign keys, busy timeout,
  memory temp store, and incremental auto-vacuum as specified.
- One writer connection and at most four ordinary read connections are
  enforced.
- Startup refuses a newer schema, migration failure, failed quick check, or
  unwritable data directory without recreating or deleting data.
- SQLite errors are categorized safely and never returned raw to clients.

**Verification:**

- Real temporary SQLite tests for every pragma and connection limit.
- Corrupt, unwritable, busy, and schema-too-new tests.
- Open/close/checkpoint lifecycle tests.

**Impact:** Introduces `mattn/go-sqlite3` and CGO. Record dependency, license,
image, and architecture implications; no application schema yet.

### SFT-006 — Add the initial schema and migration harness

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-005  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Fresh/no-op/rollback migration tests, real SQLite
constraint and ordering tests, query-plan assertions for critical indexes,
schema compatibility checks, and full integrity verification.

**Authoritative references:** `ARCHITECTURE.md` §§10.4–10.7, 32.2–32.3;
`DOMAIN.md` §§5, 12–14; ADRs 0001 and 0004.

**Outcome:** Add numbered forward migrations for the `0.1.0` ingestion model
and a reusable real-SQLite fixture harness.

**Acceptance:**

- The schema covers migrations, settings required by startup, servers,
  ingestion tokens, stable sources, container instances, and immutable events.
- Constraints implement exact source identity, canonical values, foreign keys,
  stable-ID uniqueness, and derived retention ordering.
- Indexes match documented History, retention, request-ID, level, and container
  queries, with rationale recorded beside migration tests or benchmarks.
- Fresh migration, transactional rollback, representative data preservation,
  quick check, and schema-too-new behavior are tested.
- Released migration files are immutable after release.

**Verification:**

- Real SQLite fresh-database and migration tests.
- Critical insert, lookup, ordering, uniqueness, and rollback queries.
- `PRAGMA integrity_check`.

**Impact:** Adds the initial persistent schema. Documentation or ADR updates are
required if implementation cannot preserve a documented invariant.

### SFT-007 — Implement canonical event and source normalization

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-001  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Table-driven canonicalization, timestamp precedence,
raw/multiline preservation, source fallback/distinction, level inference,
duplicate/control/limit, canonical equality, JSON-depth, and bounded fuzz tests.

**Authoritative references:** `DOMAIN.md` §§5–12; `AGENTS.md` §10; ADR 0004.

**Outcome:** Implement explicit domain types and constructors that convert a
decoded record into a valid canonical event with trusted source identity still
separate from database IDs.

**Acceptance:**

- Timestamp, stream, normalized/original level, raw message, searchable text,
  common fields, attributes, source identity, container identity, and optional
  source event ID obey all documented bounds and preservation rules.
- Absent timestamps use receive time; invalid supplied timestamps reject.
- Conservative anchored level inference and structured-level precedence match
  the canonical table.
- Unknown structured fields survive as bounded canonical JSON; duplicate keys,
  controls, invalid UTF-8, and oversize values reject.
- Stable source keys are exact, bounded, case-sensitive, and independent of
  alias and container identity.

**Verification:**

- Table-driven normalizer tests covering malformed input, limits, fallbacks,
  timestamps, level mapping, raw preservation, known/unknown fields, and source
  identity distinctions.
- Fuzz tests for normalization boundaries where useful.

**Impact:** Establishes domain compatibility without a migration. No transport
maps may cross the normalizer boundary.

### SFT-008 — Implement Server and ingestion-token CLI management

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-004, SFT-006  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Real SQLite Server/token lifecycle, overlap,
revocation, missing-server rollback, hash-only storage, offline CLI, live
owner-only control-socket, one-time display, invalid-argument, and race tests.

**Authoritative references:** `DOMAIN.md` §§20, 30; `ARCHITECTURE.md` §§20, 30.

**Outcome:** Add concrete SQLite stores and focused CLI commands for Server
creation/listing and ingestion-token creation/revocation.

**Acceptance:**

- `server create/list` and token create/revoke work offline when the server is
  stopped and through the control socket while it is active.
- Tokens contain at least 32 random bytes, are displayed once, and persist only
  a secure hash plus nonsecret fingerprint and lifecycle metadata.
- Tokens are bound to one trusted Server; revoked tokens fail immediately.
- Rotation may overlap old and new tokens until explicit revocation.
- CLI help, errors, process lists, logs, and diagnostics never expose token
  plaintext or hashes after initial output.

**Verification:**

- Real SQLite lifecycle, uniqueness, revocation, rotation, and rollback tests.
- CLI subprocess tests for offline and online modes.
- Negative tests for secret leakage and unsafe output.

**Impact:** Uses the initial schema and adds secret-bearing administration.
Audit integration is added in `0.4.0`; no public administration API is created.

### SFT-009 — Implement the authenticated ingestion HTTP boundary

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-004, SFT-008  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** HTTP admission tests for route/method, bearer syntax,
active/revoked authentication, token-bound Server identity, media/encoding,
compressed size, timeout, request ID, safe status mapping, and payload-free
errors; full race suite.

**Authoritative references:** `ARCHITECTURE.md` §§11.1–11.4, 11.7; ADR 0002.

**Outcome:** Add the ingestion router and middleware for safe request identity,
token authentication, format admission, and outer connection/body limits.

**Acceptance:**

- Only `POST /api/v1/ingest` with a valid active Server token reaches expensive
  decoding.
- Server identity is derived only from the token; payload metadata cannot
  override it.
- Only documented media types and absent/`gzip` encoding are admitted.
- Compressed bytes, headers, request duration, and request IDs are bounded.
- Responses use the documented safe status categories and include
  `X-Request-ID` without leaking internal errors.

**Verification:**

- HTTP contract tests for method, auth, revocation, content type/encoding,
  compressed size, request IDs, and token-bound identity.
- Timing-safe verification tests where observable.
- Payload-leak log tests.

**Impact:** Introduces the public ingestion endpoint. Review authentication cost,
latency, and denial-of-service boundaries.

### SFT-010 — Implement bounded JSON and NDJSON decoding

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-007, SFT-009  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Official-format JSON array/JSON Lines fixtures,
Coolify alias fixtures, gzip/plain/multiline normalization, boundary and
decompression-bomb tests, object/duplicate/depth/trailing/final-record
rejections, atomic empty result assertions, and bounded decoder fuzzing.

**Authoritative references:** `ARCHITECTURE.md` §§11.3–12.4; `DOMAIN.md` §9;
ADR 0002.

**Outcome:** Stream-decode authenticated request bodies into a complete,
validated in-memory batch without partial admission.

**Acceptance:**

- `application/x-ndjson` accepts one object per nonempty line;
  `application/json` accepts one object or one object array.
- Gzip and decompressed readers enforce their independent caps while streaming.
- Empty batches, invalid gzip/UTF-8/JSON, duplicate keys at any depth, trailing
  data, non-object records, excess depth/count/size, and bad timestamps reject
  the whole request.
- Fluent Bit timestamp, tag, message, metadata fallback, and nested structured
  payload fixtures normalize through `ReceivedRecord` to `CanonicalEvent`.
- Raw application content and multiline whitespace are preserved.

**Verification:**

- Table-driven decoder tests for every accepted shape and rejection category.
- Boundary-size, decompression-bomb, last-record-invalid, and atomicity tests.
- Fuzz decoders with bounded corpora and execution time.

**Impact:** Adds hostile-input parsing but no dependency or migration. Review
peak allocation and ensure errors never include payload bytes.

### SFT-011 — Implement resident admission and the bounded queue

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-007  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Deterministic decoder/resident/queue boundary and
saturation tests, decode failure and cancellation ownership tests,
close/drain and exactly-once completion tests, concurrent accounting stress
under the race detector, and linux/amd64 admission accounting benchmark
(148.9 ns/op, 48 B/op, 1 alloc/op on an Intel i5-7500).

**Authoritative references:** `ARCHITECTURE.md` §§11.3, 13; `AGENTS.md` §9.5;
ADR 0002.

**Outcome:** Enforce concurrent-decoder, aggregate decode-plus-queue, and queued
batch capacity by event count and retained bytes.

**Acceptance:**

- At most four requests decode concurrently by default.
- Aggregate admission is capped at 30,000 events and 64 MiB; the queued subset
  is capped at 20,000 events and 32 MiB.
- Capacity transfers from decoder to queue without double counting and releases
  exactly once on every success, rejection, cancellation, and writer result.
- Queue saturation rejects the complete request with retryable `503`.
- No goroutine is spawned per event or abandoned by a canceled handler.

**Verification:**

- Deterministic boundary and saturation tests.
- Concurrent admission, cancellation, accounting, and shutdown stress tests.
- `go test -race ./...`
- Leak check where practical.

**Impact:** Establishes primary memory bounds and concurrency behavior; no
migration. Benchmark accounting overhead.

### SFT-012 — Implement coordinated transactional persistence

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-006, SFT-007, SFT-008, SFT-011  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Real-SQLite coordinator and writer tests cover serialized
transactions, rollback, trust and quota rejection, bounded cache churn,
stable-ID idempotency/conflict, deterministic post-commit publication, safe
database error/status classification, and concurrent race stress.

**Authoritative references:** `ARCHITECTURE.md` §§14–15; `DOMAIN.md` §§8, 12–14,
29; ADRs 0002 and 0004.

**Outcome:** Add the single write coordinator and persist complete batches,
source discovery, container metadata, and events in one short transaction.

**Acceptance:**

- The write coordinator is the only application mutation path.
- Distinct sources and containers resolve transactionally with event insertion;
  caches are bounded and reconstructable.
- Per-Server limits of 10,000 stable sources and 100,000 container instances
  reject the complete request when exceeded.
- Identical stable-ID retries are successful no-ops; conflicting canonical
  content rolls back the batch and returns `409`.
- Success is emitted only after commit; only newly committed events reach the
  nonblocking publication hook in deterministic ID order.
- Database, constraint, and commit failures roll back all source and event
  changes.

**Verification:**

- Real SQLite atomicity, rollback, quota, cache-churn, ordering, and dedup tests.
- Database busy/failure and conflicting-ID HTTP integration tests.
- Concurrent writer stress test and `go test -race ./...`.

**Impact:** Implements the durability boundary and hot write path. Review
transaction duration, index cost, memory, and safe SQLite error classification.

### SFT-013 — Complete commit-before-acknowledgement and shutdown integration

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-004, SFT-009, SFT-010, SFT-011, SFT-012  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Real-database HTTP tests prove commit-backed `204`,
last-record conflict rollback, retryable saturation/shutdown responses,
post-admission cancellation durability, bounded result delivery, and closed-queue
draining; application lifecycle tests cover listener and database shutdown.

**Authoritative references:** `ARCHITECTURE.md` §§7–8, 11.6–11.8, 13.4;
ADR 0002.

**Outcome:** Connect transport, decoder, admission, queue, writer, and lifecycle
into the complete ingestion outcome contract.

**Acceptance:**

- `204` is returned only after the full request commits.
- Permanent input failures, stable-ID conflicts, temporary saturation/database
  failures, and storage-full categories map to their documented statuses.
- A disconnect after queue admission may still commit; writer result delivery
  never blocks when the handler leaves.
- Shutdown rejects new requests, bounds in-flight decoding, drains admitted
  batches, and never claims success for an uncommitted request.
- Successful batch logging remains below info level and contains no payload.

**Verification:**

- End-to-end commit-before-response and last-record rollback tests.
- Client cancellation before/after admission and shutdown-with-queue tests.
- Database failure, timeout, and retry-status tests.
- `go test -race ./...`

**Impact:** Finalizes public acknowledgement and retry semantics. No migration;
latency includes queue and commit time by design.

### SFT-014 — Build the ingestion integration and smoke suites

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-013  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Real-database HTTP coverage plus the subprocess smoke
test build and run production Siftail, create Server/token state through CLI
paths, ingest plain NDJSON and gzip Fluent Bit fixtures, shut down cleanly, and
verify preserved rows through the bounded production log store.

**Authoritative references:** `AGENTS.md` §§18.1–18.5; `ARCHITECTURE.md` §32.

**Outcome:** Provide release-representative automated coverage and a focused
command-line smoke workflow for durable ingestion.

**Acceptance:**

- The HTTP suite covers every required auth, limit, format, atomicity, retry,
  deduplication, database-error, cancellation, and payload-preservation case.
- Tests use production migrations and real SQLite, never mocked SQL.
- An automated subprocess smoke test builds and starts Siftail, creates a Server
  and token through the CLI, submits plain and gzip fixtures, stops the process,
  and verifies committed rows through the production store.
- A documented command-line smoke workflow exercises token creation and plain
  and gzip ingestion without requiring an arbitrary SQL client.
- Test fixtures include canonical NDJSON and supported Fluent Bit envelope
  variants without real secrets.
- Repeated test runs leave no process, socket, database, or temporary-file leak.

**Verification:**

- `go fmt ./...`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- Run the command-line smoke workflow.

**Impact:** Adds test fixtures and development tooling only. Any discovered
contract mismatch must update authoritative docs before implementation changes.

### SFT-015 — Establish benchmarks and close the `0.1.0` gate

**Status:** Done
**Milestone:** 0.1.0  
**Depends on:** SFT-002, SFT-014  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** The reproducible normalization, decode, admission,
source-resolution, SQLite commit, idempotent retry, HTTP latency, sustained
batch, queue-ledger, and RSS baselines plus the `PRODUCT.md` §19.1
requirement-to-test matrix are recorded in `docs/performance/0.1.0.md`.

**Authoritative references:** `PRODUCT.md` §§15, 19.1; `ARCHITECTURE.md` §§33,
36; `AGENTS.md` §§17, 29.

**Outcome:** Measure the ingestion hot path and demonstrate that every `0.1.0`
requirement is implemented and reproducible.

**Acceptance:**

- Benchmarks cover normalization, decoded batch construction, queue admission,
  source resolution, SQLite commit, duplicate retries, and sustained ingestion.
- Method, hardware/container limits, dataset shape, SQLite settings, and results
  are recorded without presenting hardware-specific numbers as guarantees.
- Memory accounting is compared with measured RSS and queue saturation.
- Every `PRODUCT.md` §19.1 requirement maps to completed code and tests.
- Known limitations and performance gaps are recorded before tagging the
  milestone.

**Verification:**

- Required format, vet, unit, integration, race, smoke, and benchmark commands.
- Review dependency, privacy, security, disk, memory, and latency impact.

**Impact:** No feature or migration. Establishes the baseline used for future
regression review.

---

## 3. `0.2.0` — Authentication and historical browsing

Milestone authority: `PRODUCT.md` §19.2. The SFT-015 dependency gate is
complete.

### SFT-016 — Implement administrator storage and secure CLI recovery

**Status:** Done
**Milestone:** 0.2.0  
**Depends on:** SFT-015  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Schema-1 preservation and schema-2 compatibility tests,
real-SQLite single-account/create/reset/rollback tests, Argon2id parameter and
two-operation concurrency tests, and offline plus production-subprocess
control-socket CLI tests pass without sensitive output.

**Authoritative references:** `DOMAIN.md` §§18–19; `ARCHITECTURE.md` §§19.1–19.2;
ADR 0003.

**Outcome:** Add the single-administrator schema/store and secure CLI creation
and password-reset workflows.

**Acceptance:**

- Username and password validation matches the documented byte and character
  rules without trimming or normalization.
- Argon2id uses stored parameters, initially 32 MiB, three iterations, and
  parallelism one, with at most two concurrent hash operations.
- Passwords are read without normal plaintext command arguments and are never
  echoed or logged.
- Only one administrator may exist; password reset updates the stored hash
  transactionally.
- CLI works through the control socket online and safely offline.

**Verification:**

- Real SQLite migration, hash/verify, invalid-input, concurrency, and rollback
  tests.
- CLI subprocess and sensitive-output tests.
- `go test -race ./...`

**Impact:** Adds a numbered administrator migration and a password-hash
dependency. Document dependency, resource, license, and security rationale.

### SFT-017 — Implement bounded opaque sessions

**Status:** Done
**Milestone:** 0.2.0  
**Depends on:** SFT-016  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Schema-2 preservation, token-hash-only issuance,
clock-controlled absolute/idle/touch boundaries, deterministic 65th-login
eviction, cleanup/grace, revoke and concurrent lookup, transactional
password-reset rollback, cookie attributes, and online/offline revoke-all CLI
tests pass with real SQLite.

**Authoritative references:** `DOMAIN.md` §19; `ARCHITECTURE.md` §19.3;
ADR 0003.

**Outcome:** Add session persistence, issuance, lookup, rotation, expiry,
revocation, cap enforcement, and cleanup.

**Acceptance:**

- Session tokens contain 32 random bytes and only SHA-256 hashes are stored.
- At most 64 active sessions exist; cap behavior is deterministic and tested.
- Absolute 14-day and inactive 7-day expiry are enforced at use time.
- Revoked and expired sessions fail immediately; expired rows are cleaned after
  the documented grace period through the write coordinator.
- Administrator password reset/change revokes all existing sessions in the same
  coordinated mutation.
- Cookie construction uses `HttpOnly`, `SameSite=Strict`, root path, explicit
  expiry, and `Secure` according to configured public URL.

**Verification:**

- Real SQLite create/use/rotate/revoke/expire/cap/cleanup tests.
- Concurrent lookup/revocation and password-change tests.
- Cookie attribute tests and `go test -race ./...`.

**Impact:** Adds a numbered sessions migration and a recoverable cleanup worker.
Session plaintext must never enter logs, diagnostics, or backups.

### SFT-018 — Implement the authenticated browser security boundary

**Status:** Done
**Milestone:** 0.2.0  
**Depends on:** SFT-017  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Real-store browser tests cover uniform login failure,
fifth-attempt bounded throttling, fresh secure cookies, safe returns,
expiry/logout, HMAC CSRF, content type and exact Origin rejection, authenticated
no-store responses, proxy spoofing, ignored identity headers, strict security
headers, request IDs, panic recovery, and sensitive-log exclusion.

**Authoritative references:** `ARCHITECTURE.md` §§18–22; `DESIGN.md` §7;
`AGENTS.md` §§11, 13; ADR 0003.

**Outcome:** Add login/logout, authentication middleware, bounded throttling,
CSRF and Origin validation, security headers, safe recovery, and private-cache
behavior.

**Acceptance:**

- Login failures are uniform and do not disclose account existence or throttle
  keys.
- Per-client and per-account throttling is bounded and requires no external
  service.
- Every browser state change requires a valid session, CSRF token, allowed
  content type, and matching Origin/Referer policy.
- Baseline CSP and security headers are present; authenticated responses and
  fragments use `Cache-Control: no-store`.
- Forwarded routing/client metadata is trusted only from configured networks;
  identity headers are ignored.
- Logout and session expiry invalidate access immediately.

**Verification:**

- Handler tests for login, logout, throttle, CSRF, Origin, cookies, headers,
  proxy spoofing, expiry, panic recovery, and request IDs.
- Negative security scenarios and sensitive-log assertions.
- `go test -race ./...`

**Impact:** Introduces browser authentication and security middleware without
SSO, JWTs, CAPTCHA, or a public admin API.

### SFT-019 — Implement the historical query and cursor contract

**Status:** Done
**Milestone:** 0.2.0  
**Depends on:** SFT-015  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Typed parsing and canonical round-trip tests cover
absolute/default/preset ranges, boundaries, every filter family, byte limits,
malformed values, ordering, and URL safety. Real-SQLite tests cover coordinated
key creation and persistence plus cursor round trips, tampering, malformed
authenticated payloads, direction/query mismatch, and URL-safe encoding.
Bounded parser and cursor fuzz targets are included.

**Authoritative references:** `DOMAIN.md` §24; `ARCHITECTURE.md` §§16.1–16.5;
`PRODUCT.md` §13.

**Outcome:** Add explicit query types, URL parsing/serialization, absolute range
resolution, filter validation, and tamper-protected keyset cursors.

**Acceptance:**

- Queries require `[from,to)` endpoints no more than 31 days apart and resolve
  presets to stable absolute timestamps.
- Source, container, level, stream, message include/exclude, and selected exact
  common-field filters use explicit bounded types.
- Message filters use literal ASCII-only case folding; non-ASCII variants remain
  distinct and wildcard characters have no syntax.
- Cursors are versioned, integrity protected, bound to the canonical query
  fingerprint, and rejected after tampering or query change.
- Canonical URL output contains complete query state and no credentials, CSRF
  values, or opaque hidden metadata beyond the cursor.

**Verification:**

- Table-driven validation, round-trip, preset, boundary-time, filter, cursor,
  tampering, query-mismatch, and URL-safety tests.
- Fuzz cursor decoding and query parsing.

**Impact:** Establishes bookmark/query compatibility; no migration or external
dependency unless cryptographic key storage requires a documented addition.

### SFT-020 — Implement the historical SQLite query store

**Status:** Done
**Milestone:** 0.2.0  
**Depends on:** SFT-019  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Real migrated-SQLite tests cover canonical ordering,
older/newer keysets at equal timestamps, every filter family, literal wildcard
characters, ASCII/non-ASCII folding, null fields, event lookup, inactive
cascading catalog options, cancellation, hostile cursors, and existing-index
plans. The documented 100k and 1M benchmarks measure unfiltered, selective, and
literal reads; the 10M review remains the stated pre-public gate.

**Authoritative references:** `ARCHITECTURE.md` §§10.7–10.8, 16; `AGENTS.md`
§18.2.

**Outcome:** Query retained events and source options with bound SQL,
deterministic keyset pagination, exact filters, and bounded literal search.

**Acceptance:**

- Results order by `event_at_us DESC, id DESC` and default to 200 rows.
- All documented filters compose using bound values and known columns only.
- Cursor edges never skip or duplicate events with equal timestamps.
- Source catalog queries support cascading Server through Service selection and
  inactive historical sources.
- Null/optional fields and container filtering behave explicitly.
- Query cancellation/timeouts fail safely without exposing SQL.
- Index behavior is measured at 100k and 1M rows; 10M is retained as a
  pre-public release gate if impractical for ordinary CI.

**Verification:**

- Real migrated SQLite tests for ordering, all filters, pagination edges,
  literal search characters, non-ASCII behavior, optional fields, cancellation,
  and invalid cursors.
- Query benchmarks and plan/index review.

**Impact:** Adds read-path SQL and may refine indexes only through a numbered
migration with documented write/storage/retention cost.

### SFT-021 — Add the embedded web shell and login experience

**Status:** Done
**Milestone:** 0.2.0  
**Depends on:** SFT-018  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Renderer and browser tests cover administrator-present,
missing, expired, uniform-error, authenticated, and hostile escaped states;
embedded asset types, exact HTMX SHA-384 and license, CSP-local references,
snapshot disabling, and application-JavaScript DOM safety. Headless Chromium
checks cover keyboard focus, dark/light themes, reduced motion, desktop/mobile
layouts without page-wide horizontal overflow, and login/shell screenshots.

**Authoritative references:** `ARCHITECTURE.md` §18; `DESIGN.md` §§5–7, 27–35.

**Outcome:** Embed HTMX, templates, CSS, and focused assets into the Go binary
and render the authenticated application shell and accessible login page.

**Acceptance:**

- Assets are local and embedded; no CDN, external font, runtime Node.js, inline
  script, or inline style weakens CSP.
- Templates use explicit view models and `html/template` escaping.
- Login behavior, copy, focus, error association, password-manager support,
  dark/light themes, and reduced-motion behavior match `DESIGN.md`.
- Missing administrator state exposes no setup form and directs the operator to
  the CLI without host detail.
- HTMX history snapshot caching is disabled.

**Verification:**

- Handler/template tests for authenticated, unauthenticated, missing-admin, and
  escaped-content states.
- Static asset/CSP verification.
- Manual keyboard, dark/light, responsive, and accessibility smoke checks.

**Impact:** Adds vendored HTMX and initial static assets. Any development-only
frontend tooling must remain absent from the production image.

### SFT-022 — Implement the History workspace and URL-owned filtering

**Status:** Done
**Milestone:** 0.2.0  
**Depends on:** SFT-020, SFT-021  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Authenticated handler/template tests cover canonical
last-hour and preset redirects, every source/text/level/stream/common/container
filter, invalid ranges, safe query failures, hostile escaping, session expiry,
URL push state, and equal-time cursor append fragments. Headless Chromium
checks cover real ingestion, debounced filtering with focus preservation,
Back restoration, keyboard shortcuts, reduced motion, dark/light, desktop and
390px responsive layouts without page overflow. Store tests bound list message
previews and the updated documented 100k/1M benchmarks retain existing plans.

**Authoritative references:** `DESIGN.md` §§8–10, 23, 38; `PRODUCT.md` §§10.4,
13.

**Outcome:** Render and update the History workspace through focused HTMX
fragments while preserving complete investigation state in the URL.

**Acceptance:**

- First use defaults to the last hour; returning navigation restores a valid
  absolute query.
- Cascading source, time, level, stream, contains/excludes, common-field, and
  container filters map exactly to the validated query model.
- Filter requests preserve existing rows while loading and update only focused
  fragments.
- “Load older” appends one deterministic cursor page without replacing the
  shell, losing focus, or shifting existing context.
- Result summaries avoid expensive total counts and accurately indicate loaded
  rows and further availability.
- Browser Back/Forward refetches authorized state and restores URL-owned query
  state.

**Verification:**

- Handler and fragment tests for defaults, every filter family, invalid ranges,
  pagination, URL state, loading errors, and session expiry.
- Keyboard, focus, scroll-preservation, dark/light, responsive, and
  accessibility checks.
- Escaping review with hostile log fixtures.

**Impact:** Adds the main `0.2.0` user workflow but no Live mode, alias editing,
browser token management, deployment boundaries, or export.

### SFT-023 — Implement safe inline event details

**Status:** Done
**Milestone:** 0.2.0  
**Depends on:** SFT-020, SFT-021  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Authenticated real-SQLite handler and escaped-template
tests cover complete source/timing/severity/common metadata, recursively ordered
nested attributes, multiline hostile content, 16 KiB initial section bounds,
exact stored sizes, explicit complete schema-bounded retrieval, invalid queries,
database failure, and indistinguishable missing/deleted events. Application
JavaScript tests exclude `innerHTML` and require `textContent` clipboard and
independent replacement controls. A production binary with real ingestion and
headless Chromium verified inert hostile markup, multiline clipboard fidelity,
expansion/collapse focus, light theme, and a 390px layout without overflow.

**Authoritative references:** `DESIGN.md` §§12–13; `AGENTS.md` §§11.5, 18.6.

**Outcome:** Add authenticated event-detail retrieval and inline expansion for
message, source, timing, level/stream, common fields, attributes, and raw
payload.

**Acceptance:**

- Detail access is scoped to authenticated retained events and returns a safe
  not-found response after deletion.
- Incoming content is escaped and never converted to trusted HTML or inserted
  with `innerHTML`.
- Nested attributes have stable ordering and bounded initial presentation.
- Large and multiline payloads preserve content, show size/truncation honestly,
  and expose only bounded safe detail behavior.
- Expansion retains row context; focus enters predictably and returns to the
  row on collapse.
- Copy actions use text-safe browser APIs and never place content in headers or
  filenames.

**Verification:**

- Handler/template tests with HTML, control-like text, multiline, large,
  structured, missing, and deleted events.
- Keyboard, focus, responsive, and accessibility checks.
- Security review for template escaping, content types, CSP, and copy behavior.

**Impact:** Reads existing schema only. Raw log data remains hostile and must
not enter caches beyond the active authenticated response/DOM.

### SFT-024 — Close the `0.2.0` browser and security gate

**Status:** Done
**Milestone:** 0.2.0  
**Depends on:** SFT-022, SFT-023  
**Issue/PR:** — (direct maintainer implementation)
**Completed:** 2026-07-28
**Completion evidence:** Lockfile-pinned development-only Playwright 1.62.0 and
axe 4.12.1 tooling provisions Siftail through production CLI paths, ingests 220
fixtures, and guarantees process/database/socket/temp cleanup. Seven Chromium
scenarios passed in 22.2 seconds across native login/logout, uniform/throttled
failure, canonical/default/filter/Back/pagination state, hostile inline details,
clipboard and focus, online session invalidation, explicit dark/light,
reduced-motion, 390px responsive inspection, and WCAG A/AA axe checks. Browser
coverage exposed and closed native-form Origin, HTMX append-wrapper, and
light-theme contrast defects. The complete Go suite and race suite pass;
16-worker concurrent session/History stress passes under race. The measured
100k/1M query rerun retained existing plans and is recorded with the requirement
matrix, dependency/resource review, manual checklist, and explicit pre-public
limitations in `docs/release/0.2.0-gate.md`.

**Authoritative references:** `PRODUCT.md` §19.2; `AGENTS.md` §§18.6–18.7;
`ARCHITECTURE.md` §§32.5–32.6.

**Outcome:** Add development-only Playwright coverage and demonstrate that
authentication and historical browsing satisfy the milestone contract.

**Acceptance:**

- Playwright tooling is development-only, lockfile-pinned, and absent from the
  production runtime.
- Critical flows cover login, logout, failed/throttled login, History defaults,
  every primary filter, URL restoration, pagination, inline details, expiry,
  and hostile-content escaping.
- Keyboard, focus, dark/light, reduced motion, responsive emergency inspection,
  and accessibility smoke checks pass.
- Query benchmarks and concurrent session/read tests remain within documented
  targets or record an explicit limitation.
- Every `PRODUCT.md` §19.2 requirement maps to completed code and tests.

**Verification:**

- `go fmt ./...`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- Repository-documented Playwright and asset-verification commands.
- Manual security, accessibility, dark/light, and responsive checklists.

**Impact:** Adds development-only browser dependencies and milestone evidence;
no production Node.js runtime, new migration, or export capability.

---

## 4. Later milestones

Later work remains intentionally undecomposed until it becomes the active
planning horizon. `PRODUCT.md` §19 is authoritative.

| Milestone | Theme | Planning note |
|---|---|---|
| `0.3.0` | Live tail, aliases, source lifecycle, browser token management, retention, status, and Coolify guidance | Decompose after `SFT-024` and supported Coolify/Fluent Bit integration fixtures are known. |
| `0.4.0` | Backup, verified restore, diagnostics, audit, disk-full hardening, and bounded audited History export | Export belongs here so it never ships without required audit recording. |
| `0.5.0` | Public dogfood documentation, packaging, upgrade evidence, performance, soak testing, and core polish | Decompose after ordinary deployment and recovery behavior are proven. |
| `1.0.0` | Proven operational stability | Defined by evidence and release gates, not a speculative task list. |

Do not add post-dogfood candidates to this tracker until `PRODUCT.md` accepts
them into a milestone.
