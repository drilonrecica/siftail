# AGENTS.md — Siftail Repository Instructions

**Status:** Mandatory instructions for AI coding agents and contributors  
**Product:** Siftail  
**Primary maintainer:** Drilon Reçica  
**Repository model:** Public, maintainer-led, selectively accepting high-value contributions

---

## 1. Mission

Build and maintain **Siftail**, a fast, private, low-resource, self-hosted log viewer for Coolify and generic Fluent Bit HTTP ingestion.

Siftail must remain:

- one Docker container;
- one Go process;
- one local SQLite database;
- single-administrator;
- free of telemetry and external runtime services;
- bounded in memory and disk;
- focused on historical log inspection and live tailing.

Do not turn Siftail into a general observability platform.

---

## 2. Mandatory reading order

Before making a meaningful change, read:

1. `AGENTS.md`
2. `DOMAIN.md`
3. `PRODUCT.md`
4. `ARCHITECTURE.md`
5. `DESIGN.md` when changing UI or copy
6. the relevant `TASKS.md` entry for tracked implementation work
7. relevant tests, migrations, and ADRs

Do not rely on issue text, chat history, or assumptions instead of these documents.

`TASKS.md` tracks execution and status only. It cannot redefine behavior from
the authoritative specifications, accepted tests, migrations, or ADRs.

For a small localized change, reread the relevant sections rather than mechanically rereading every line. For architectural or domain changes, review all five documents.

---

## 3. Authority order

When sources conflict:

1. Existing behavior protected by accepted tests and migrations.
2. `DOMAIN.md` for semantics and invariants.
3. `PRODUCT.md` for scope and user outcomes.
4. `ARCHITECTURE.md` for technical decisions.
5. `DESIGN.md` for presentation and interactions.
6. Accepted ADRs for the reasoning behind decisions already reflected in the authoritative documents.
7. `TASKS.md` for accepted execution scope, dependency order, and status only.
8. Issue or PR description.
9. Historical discussion.

`AGENTS.md` governs repository process and implementation discipline. It does not
override the behavior defined by the four product specifications.

If implemented behavior intentionally changes, update tests and every affected authoritative document in the same change.

Do not silently “fix” a documented behavior by changing only code.

---

## 4. Non-negotiable product invariants

Every agent must preserve these unless an explicitly approved architectural change updates the documents and migrations.

### 4.1 Runtime

- One production container.
- One long-running production Go process. Focused, short-lived maintenance CLI
  invocations are allowed; commands that replace or rewrite the active database
  require the server process to be stopped.
- Two HTTP listeners in the same process: UI and ingestion.
- One persistent `/data` volume.
- No Node.js runtime in production.
- No Redis, PostgreSQL, Elasticsearch, ClickHouse, Loki, or sidecar dependency.

### 4.2 Privacy

- No telemetry.
- No analytics.
- No crash reporting.
- No external fonts, scripts, or CDNs.
- No automatic outbound API calls during ordinary operation.
- Never log incoming application payloads in Siftail process logs.
- Never expose credentials, hashes, or authorization headers in UI, diagnostics, support data, or errors.

### 4.3 Ingestion

- Trusted Server identity comes from the authenticated token.
- Payload metadata cannot impersonate another Server.
- Entire request is accepted atomically or rejected.
- Success is returned only after SQLite commit.
- No acknowledgement after parsing or queueing only.
- Queue is bounded by event count and retained bytes.
- Queue saturation returns explicit retryable failure.
- No unbounded goroutine per event/request.
- Multiline assembly belongs to Fluent Bit/source in version one.
- No hash-based message deduplication.
- Deduplication uses a stable source-provided event ID scoped to source when one
  is present. Identical repeats are idempotent; conflicting reuse rejects the
  entire request.

### 4.4 Events

- Events are immutable after persistence.
- Store event timestamp and receive timestamp.
- Historical order is `event_at_us DESC, id DESC`.
- Stream and log level remain independent.
- Preserve original level when supplied.
- Preserve raw application payload according to domain rules.
- Unknown structured fields remain in bounded JSON attributes.
- Live events are published only after commit.

### 4.5 Storage and retention

- SQLite is deliberate and first-class.
- One controlled writer.
- WAL mode.
- `synchronous=FULL`; an acknowledged transaction must survive an OS crash or
  power loss, subject to the guarantees of the host filesystem and storage.
- Global age and database-size retention.
- Oldest events by `min(event_at_us, received_at_us), id` are deleted first in
  bounded chunks.
- Full `VACUUM` is never automatic routine maintenance.
- Disk-full mode rejects ingestion and preserves reads where possible.
- Never recreate or delete the database silently after an error.
- Older binary refuses newer schema.

### 4.6 Security

- One local administrator.
- Argon2id password hashes.
- Opaque server-side sessions; no JWT architecture.
- Session and ingestion tokens stored only as hashes.
- CSRF token plus Origin validation for state-changing browser requests.
- Strict security headers.
- Identity-header or forward-auth proxy authentication is not part of the
  pre-public milestones. If introduced later, it requires explicit trusted
  networks and cryptographic/shared verification.
- No unauthenticated setup page.
- No password printed to logs.

### 4.7 Scope

Do not add:

- metrics;
- traces;
- uptime monitoring;
- Kubernetes-specific features;
- teams or roles;
- multi-tenancy;
- hosted SaaS behavior;
- dashboards or chart builders;
- AI analysis;
- automatic secret detection;
- built-in redaction in version one;
- alternative databases;
- public administration API;
- plugin framework;
- arbitrary query language;
- regular-expression log search;
- mobile app.

---

## 5. Before coding

For every nontrivial task:

1. Identify affected domain concepts.
2. Identify affected product workflow.
3. Identify affected architecture components.
4. Identify security/privacy impact.
5. Identify memory, disk, and latency impact.
6. Inspect existing implementation and tests.
7. Determine whether a migration is needed.
8. Determine whether documentation must change.
9. Define acceptance tests before implementation.
10. Keep the change narrowly scoped.

Do not begin by creating abstractions or adding dependencies.

---

## 6. Repository structure rules

Expected packages:

```text
internal/app
internal/audit
internal/auth
internal/backup
internal/config
internal/database
internal/diagnostics
internal/ingest
internal/logs
internal/retention
internal/sessions
internal/sources
internal/status
internal/web
```

Rules:

- Features own their handlers, validation, service logic, and SQL where practical.
- `internal/app` composes; it does not contain feature business logic.
- `internal/database` owns connection lifecycle, pragmas, migrations, integrity, backups, and checkpoints.
- Do not create `utils`, `common`, `helpers`, or universal repository packages.
- Do not create interfaces solely to mock a concrete type.
- Do not move code into `shared` merely because two call sites exist.
- Prefer duplication of a few clear lines over a vague generic abstraction.
- Extract only when ownership and semantics are clear.

---

## 7. Go coding rules

### 7.1 General style

- Use idiomatic Go.
- Format with `gofmt`.
- Keep functions small enough to understand, but do not fragment straightforward flows into meaningless wrappers.
- Prefer explicit types at domain boundaries.
- Avoid `map[string]any` beyond transport decoding and JSON attribute boundaries.
- Pass `context.Context` as the first argument for request/lifecycle work.
- Do not store contexts in structs except where a long-lived component has a clearly owned lifecycle.
- Check all errors.
- Wrap errors with operation context using `%w`.
- Use sentinel/category errors sparingly for transport mapping.
- Do not expose raw SQLite errors to clients.

### 7.2 Concurrency

- Application root owns component lifecycles.
- Use structured cancellation.
- Never start unmanaged goroutines in handlers or stores.
- Every goroutine must have a clear owner, stop condition, and test.
- Channels must be bounded unless there is a proven finite producer.
- Slow SSE clients must not block the writer or broker.
- Queue byte accounting must be released exactly once.
- Avoid lock ordering ambiguity.
- Run race tests for concurrency changes.

### 7.3 Errors and panics

- HTTP boundary recovers request panics and returns generic 500 with request ID.
- Writer/database lifecycle panic is process-fatal.
- Do not use panic for user input or expected operational errors.
- Do not silently swallow background errors.
- Classify background workers as critical or recoverable.

### 7.4 Logging

Siftail process logs may include:

- component;
- operation;
- duration;
- safe IDs;
- error category;
- request ID.

They must not include:

- incoming message content;
- raw request body;
- authorization header;
- token;
- password;
- password/token/session hash;
- proxy secret;
- full environment dump.

Do not log every successful batch at info.

---

## 8. SQL and SQLite rules

### 8.1 SQL style

- Use handwritten SQL.
- Bind every user value.
- Never concatenate user input into SQL.
- Keep queries near feature stores.
- Name selected columns explicitly; avoid `SELECT *` in stable application queries.
- Keep scanners aligned with selected columns and test them.
- Use transactions deliberately.

### 8.2 No ORM

Do not introduce an ORM, active record, generic repository framework, or database-agnostic abstraction.

### 8.3 Stores

Use concrete stores by default:

```go
type LogStore struct {
    db *sql.DB
}
```

Add an interface only when:

- there are genuinely multiple implementations;
- a meaningful boundary needs polymorphism;
- the interface belongs to the consumer;
- it improves testing without replacing real SQLite behavior.

### 8.4 Tests use real SQLite

For storage tests:

- create a temporary database;
- apply production migrations;
- configure relevant pragmas;
- execute real SQL;
- assert data and integrity.

Do not mock `database/sql` for ordinary store tests.

### 8.5 Index discipline

Every new index must document:

- query it supports;
- expected selectivity;
- write/storage cost;
- effect on retention deletion;
- benchmark evidence for large tables when relevant.

Do not index every JSON-derived field.

### 8.6 Transactions

- Ingestion batch commits atomically.
- Source discovery/upsert and events belong to the same safe transactional unit.
- Do not use `INSERT OR REPLACE` for immutable events.
- Publish live events after commit only.
- Avoid long write transactions.

### 8.7 Migrations

Every schema change requires:

- numbered migration;
- migration test from previous schemas;
- fresh-database test;
- data-preservation assertions;
- schema-too-new compatibility consideration;
- documentation/release note if operator impact exists.

Never edit a released migration. Add a new one.

Do not implement automatic down-migrations.

### 8.8 SQLite pragmas

Do not change WAL, synchronous mode, auto-vacuum, cache, mmap, page size, or checkpoint behavior casually. Such changes require tests and often benchmarks or ADR.

---

## 9. Ingestion implementation rules

### 9.1 Pipeline boundaries

Maintain:

```text
HTTP transport
→ decoder
→ ReceivedRecord
→ normalizer
→ CanonicalEvent
→ source resolution
→ writer
```

Do not persist transport `map[string]any` directly.

### 9.2 Request limits

Enforce:

- compressed bytes;
- decompressed bytes;
- event count;
- event bytes;
- JSON depth;
- aggregate decoding and queued event count;
- aggregate decoding and queued retained bytes;
- queue events;
- queue bytes.

A reverse proxy limit does not replace application limits.

### 9.3 Atomic requests

- Validate all records before queueing.
- One invalid record rejects the whole request.
- Never return success for partial insertion.
- Do not silently drop invalid records.

### 9.4 Authentication

- Authenticate token before expensive processing where possible.
- Derive trusted Server from token record.
- Treat incoming server name/IP as untrusted metadata.
- Use secure token verification.
- Revoked token fails immediately.

### 9.5 Queue

- Store complete `WriteBatch` objects.
- Account decoded batches against the aggregate ingestion budget before queueing.
- Queue is bounded by count and bytes.
- Queue full returns `503`.
- No persistent internal overflow spool.
- Fluent Bit owns retry buffering.

### 9.6 Acknowledgement

The HTTP handler waits for writer outcome.

Success only after commit.

Client disconnect after queueing may leave an ambiguous outcome; writer may still commit. This is acceptable for at-least-once-compatible semantics.

### 9.7 Deduplication

Allowed:

- stable source event ID, scoped to stable source.
- identical canonical repeats are successful no-ops.
- reuse of the same identity for different canonical content rejects the whole
  request with a conflict response.

Forbidden:

- message hash;
- timestamp + message hash;
- time-window identical-message collapse;
- `INSERT OR REPLACE`.

---

## 10. Domain-model rules

Use types or constructors that make invalid states difficult.

### 10.1 Timestamps

- store Unix microseconds;
- retain event and receive time;
- do not replace a valid old timestamp with receive time;
- handle implausible future timestamps explicitly;
- historical order includes internal ID tie-breaker.

### 10.2 Levels

Canonical levels only:

```text
trace debug info warn error fatal unknown
```

Preserve original value separately.

Do not infer level from stdout/stderr.

### 10.3 Raw content

- Preserve raw application payload as specified.
- Escape on display.
- Never mark incoming content as trusted HTML.
- Do not normalize away stack traces or whitespace without explicit rule.

### 10.4 Attributes

- bounded canonical JSON object;
- known fields normalized for exact filtering;
- unknown fields preserved within limits;
- no dynamic columns;
- no arbitrary extraction configuration in version one.

### 10.5 Sources

- stable identity excludes container instance;
- alias is presentation only;
- cache is reconstructable;
- no fuzzy automatic source merge;
- layered fallback behavior must be tested.

---

## 11. HTTP and web rules

### 11.1 Route ownership

Each feature registers routes. Root router applies shared middleware and mounts features.

Do not create one giant handler file.

### 11.2 Security middleware

UI routes require:

- secure session;
- CSRF for state changes;
- Origin verification;
- security headers;
- request ID;
- safe panic recovery;
- body/form limits;
- authentication throttling where relevant.

Ingestion routes use a separate middleware chain.

### 11.3 Methods

- GET is read-only.
- State changes use POST/PUT/PATCH/DELETE as designed.
- No destructive GET links.

### 11.4 Responses

- Return contextual validation errors.
- Unexpected errors are generic and include request ID.
- Do not expose internal paths, SQL, stack traces, or secrets.
- Health endpoints remain minimal.

### 11.5 Templates

- Use `html/template` escaping.
- Never use `template.HTML` with log data.
- Build explicit view models.
- Keep fragment templates focused.
- No external asset URLs.

### 11.6 HTMX

Use HTMX for server-driven partial interaction, not as an excuse for hidden state.

- Push complete History query into URL.
- Scope loading indicators.
- Preserve content and focus.
- Return proper error fragments.
- Do not replace the whole shell for filter changes.

### 11.7 JavaScript

Vanilla JavaScript only for focused client concerns.

- No framework addition without an approved ADR.
- No `innerHTML` with log content.
- Bound live arrays and DOM.
- Remove listeners on navigation/disconnect.
- Respect reduced motion.
- Test EventSource lifecycle.

### 11.8 CSS

- Plain CSS.
- Semantic custom properties.
- No Tailwind in version one.
- No external font.
- No inline styles that weaken CSP.
- No arbitrary one-off colors when a semantic token exists.

---

## 12. UI and design rules

Read `DESIGN.md` before UI work.

Required principles:

- operational console, not dashboard;
- dark-first with complete light theme;
- muted cyan primary accent and restrained amber secondary;
- severity text always visible;
- no color-only meaning;
- Compact and Comfortable density only;
- preserve existing rows during loading;
- History and Live remain separate modes;
- live auto-scroll stops when user scrolls away;
- 1,000 rendered / 2,000 pending initial browser caps;
- no animation per incoming event;
- mobile supports emergency inspection;
- WCAG 2.2 AA target.

Do not add:

- decorative charts;
- card grids for logs;
- mascot;
- scanlines;
- CRT effect;
- neon green terminal theme;
- humorous error copy;
- full-page spinners for fragment operations.

---

## 13. Authentication and secret rules

### 13.1 Passwords

- Argon2id.
- Password read securely.
- No plaintext password argument in normal CLI.
- Never log or echo password.
- Password change revokes sessions unless documented otherwise.

### 13.2 Sessions

- opaque random token;
- hash stored;
- secure cookie;
- immediate invalidation at expiry/revocation;
- cleanup after grace period;
- no JWT.

### 13.3 Ingestion tokens

- at least 32 random bytes;
- display once;
- store hash and nonsecret fingerprint only;
- bind to Server;
- allow controlled rotation;
- audit lifecycle.

### 13.4 `_FILE` secrets

- reject direct and file variants together;
- never log file content;
- validate readable file;
- document newline handling.

### 13.5 Trusted proxy

Ordinary TLS termination and reverse proxying do not authorize identity headers.
Do not implement identity-header authentication in the pre-public milestones.
If it is introduced later, require a configured proxy network plus a
verification secret or cryptographic assertion.

---

## 14. Retention and deletion rules

### 14.1 Retention

- global age limit;
- global database-size limit;
- whichever triggers first;
- oldest first by the canonical retention timestamp;
- bounded transactions;
- application logs only;
- separate audit retention.

### 14.2 Clear view vs clear logs

- `Clear view` is browser-only.
- `Clear logs` deletes retained application events for source.
- UI and code must never confuse them.

### 14.3 Remove source

Deletes relevant source metadata and event history under defined confirmation.

### 14.4 Secure deletion

Never claim forensic erasure.

### 14.5 Client notification

Purge emits control event to affected live/history views. Deletion does not wait for clients.

---

## 15. Backup and recovery rules

- Use SQLite online backup API for active database.
- Do not copy only the live main DB file in WAL mode.
- Verify backup before reporting success.
- Restore requires ingestion stopped.
- Preserve current DB as rollback copy.
- Run compatibility and integrity checks.
- Older binary refuses newer schema.
- No arbitrary SQL CLI.
- Full and configuration-only backup semantics must remain distinct.
- Active browser sessions are excluded from every backup type; restore always
  requires a fresh login.

---

## 16. Health, status, and diagnostics rules

### 16.1 Liveness

Process can respond. Do not fail liveness for transient database busy condition.

### 16.2 Readiness

Fail when:

- migration incomplete;
- database unwritable;
- writer unavailable;
- shutdown begun;
- critical degraded state active.

### 16.3 Status page

Show sanitized operational facts only. No application log messages or secrets.

### 16.4 Diagnostics

Latest bounded sanitized events only. Do not mirror process logs into SQLite unboundedly.

### 16.5 Self-ingestion

Generated Coolify configuration must exclude Siftail's own container. Add tests/documentation for recursive-loop prevention.

---

## 17. Performance rules

### 17.1 Budgets

- Idle RSS <50 MB target.
- Normal RSS <100 MB target.
- Sustained target 1,000 events/second.
- Temporary burst test 5,000 events/second.
- Normal p95 commit latency <250 ms target.

### 17.2 Optimization policy

- design bounded systems from the beginning;
- profile before low-level optimization;
- do not add complexity to eliminate insignificant allocations;
- measure production container, not only Go benchmarks;
- test realistic event distributions.

### 17.3 Regression review

Review meaningful regressions defined in `ARCHITECTURE.md`.

A performance-sensitive PR must include benchmark method and before/after results.

---

## 18. Testing requirements by change type

### 18.1 Parser/normalizer change

Required:

- table-driven unit tests;
- malformed input;
- limits;
- raw preservation;
- known/unknown fields;
- timestamp cases;
- level mapping;
- fallback source metadata.

### 18.2 Store/query change

Required:

- real SQLite integration test;
- fresh migrated DB;
- ordering;
- cursor edge cases;
- null/optional fields;
- transaction rollback;
- relevant index/benchmark review.

### 18.3 Ingestion change

Required:

- HTTP contract tests;
- authentication;
- request limits;
- atomicity;
- commit-before-response;
- queue saturation;
- database error;
- client cancellation if affected;
- no payload leak in logs.

### 18.4 Migration

Required:

- fixture for prior schema;
- migration to current;
- representative data preserved;
- integrity check;
- critical queries;
- schema-too-new test;
- release note/document update.

### 18.5 Concurrency change

Required:

- targeted stress test;
- `go test -race ./...`;
- shutdown test;
- slow consumer behavior if broker/queue involved;
- leak check where practical.

### 18.6 UI change

Required:

- server handler/template test where meaningful;
- Playwright critical path if behavior changes;
- keyboard check;
- dark/light check;
- responsive check;
- accessibility smoke check;
- log escaping/security review;
- screenshot in PR for human review, not necessarily snapshot test.

### 18.7 Security change

Required:

- threat scenario description;
- negative tests;
- no sensitive logging;
- audit behavior;
- security headers/cookie behavior if relevant;
- documentation update.

### 18.8 Backup/restore change

Required:

- backup while ingesting;
- verify valid/invalid backup;
- restore with rollback preservation;
- schema mismatch;
- interruption/failure path;
- integrity after restore.

---

## 19. Required commands

At minimum before merge:

```bash
go fmt ./...
go vet ./...
go test ./...
```

For concurrency/security/release-relevant changes:

```bash
go test -race ./...
```

When frontend tooling exists for Playwright or asset verification, run the repository-documented commands.

Before stable release, run all benchmark, integration, Playwright, migration, backup/restore, and soak procedures documented in `ARCHITECTURE.md`.

Do not claim commands passed unless they were actually run.

---

## 20. Static analysis

Use a small high-signal `golangci-lint` configuration if present.

Suitable checks:

- standard static analysis;
- unchecked errors;
- ineffective assignments;
- context misuse;
- selected security issues;
- accidental shadowing where useful.

Do not enable dozens of stylistic linters that create churn.

Do not rewrite clear code solely to satisfy a low-value style rule without discussing the rule.

---

## 21. Dependency policy

Before adding a dependency, document:

- problem solved;
- why standard library/current dependency is insufficient;
- transitive dependencies;
- binary/image impact;
- runtime resource impact;
- security maintenance;
- license;
- whether it expands public API.

Do not add dependencies for:

- trivial helper functions;
- cron;
- ID formatting aesthetics;
- generic validation framework;
- generic repository pattern;
- CSS framework;
- icon font;
- state management library;
- frontend framework.

Consequential dependency changes may require ADR.

---

## 22. Documentation rules

Update authoritative documents in the same change when behavior changes.

Examples:

- event semantics → `DOMAIN.md`;
- new capability/scope → `PRODUCT.md`;
- component/protocol/storage change → `ARCHITECTURE.md`;
- interaction/visual/copy change → `DESIGN.md`;
- implementation-process rule → `AGENTS.md`.
- execution dependency or status change → `TASKS.md`.

Operator-facing behavior also updates README/operator docs and release notes.

Update the relevant task status and issue/PR link in the implementation change.
Never mark a task `Done` until its acceptance and required verification are
complete. If implementation reveals a behavioral change, update the
authoritative document rather than encoding the change only in `TASKS.md`.

Do not leave stale examples with old environment prefix, old routes, or old names.

Canonical name:

- Siftail;
- `siftail`;
- `SIFTAIL_`.

---

## 23. ADR rules

Create an ADR under `docs/decisions/` for hard-to-reverse changes, including:

- SQLite driver;
- FTS5;
- acknowledgement semantics;
- new runtime service;
- new database;
- auth model;
- canonical event format;
- public admin API;
- built-in encryption;
- internal disk spool;
- clustering.

ADR format should include:

- context;
- decision;
- alternatives;
- consequences;
- migration/compatibility impact;
- status.

Do not create ADRs for routine implementation details.

---

## 24. PR and contribution rules

The repository is maintainer-led.

A good PR:

- solves one coherent issue;
- stays within product boundary;
- explains behavior and operational impact;
- includes tests;
- includes migration notes where relevant;
- includes screenshots for UI changes;
- includes benchmark results when performance-sensitive;
- updates docs;
- does not add speculative abstractions.

External PRs may be declined even when technically competent if they increase maintenance or scope.

High-value external contributions include:

- important bug fix;
- security fix;
- data-loss prevention;
- significant performance improvement;
- accessibility fix;
- Coolify compatibility update;
- precise documentation correction.

Do not assume broad feature requests will be accepted.

---

## 25. Commit rules

Strict Conventional Commits are not required.

Use descriptive commit messages and focused changes.

PR description should include:

- problem;
- approach;
- behavior change;
- risks;
- tests run;
- resource impact;
- migration/config impact;
- screenshots/benchmarks where relevant.

Important design history belongs in ADRs and release notes, not only commit messages.

---

## 26. Prohibited implementation patterns

Do not:

- add microservices;
- add a separate frontend app;
- add Node to production image;
- introduce an ORM;
- add database abstraction “for future PostgreSQL”;
- add repository interfaces everywhere;
- persist every process log internally;
- create an unbounded queue;
- spawn goroutine per event;
- acknowledge before commit;
- partially accept malformed batch;
- infer Server from payload;
- use container ID as stable source;
- merge sources by fuzzy name;
- infer level from stderr;
- dedupe by message hash;
- rewrite old events;
- mutate source metadata through alias;
- run full VACUUM automatically;
- copy live DB file as backup;
- trust proxy headers from arbitrary clients;
- use JWTs;
- expose arbitrary SQL;
- use `template.HTML` for logs;
- use `innerHTML` for raw log data;
- load CDN assets;
- add telemetry;
- make unverifiable performance/privacy claims.

---

## 27. Safe behavior when requirements are ambiguous

When a detail is not specified:

1. Preserve existing behavior and tests.
2. Choose the smallest solution consistent with domain invariants.
3. Prefer bounded resource use.
4. Prefer explicit failure over silent fallback.
5. Prefer privacy and local operation.
6. Avoid new dependency or public contract.
7. Add tests that document the choice.
8. Update documentation if the choice affects user-visible behavior.
9. Create an ADR if hard to reverse.

Do not invent enterprise flexibility.

---

## 28. Definition of done

A change is done only when:

- behavior matches authoritative docs;
- code is formatted and clear;
- errors are handled;
- resource use remains bounded;
- security/privacy reviewed;
- relevant tests pass;
- race test run when needed;
- migrations tested when needed;
- UI states and accessibility checked when needed;
- documentation updated;
- no secret/payload leak introduced;
- no unintended runtime dependency added;
- PR describes tests actually run;
- operator impact is clear.

Compilation alone is not completion.

---

## 29. Release gates

A stable release requires:

- fresh install;
- upgrade from every supported fixture path;
- backup/verify/restore;
- Coolify integration;
- generic Fluent Bit basic example;
- retry behavior;
- queue saturation;
- disk-full/degraded mode;
- graceful shutdown with queued writes;
- security checklist;
- Playwright smoke suite;
- race tests;
- performance measurements;
- soak test;
- operator release notes.

Do not publish `latest` from an unreviewed main-branch build. Use `edge` for development.

---

## 30. Milestone discipline

Implementation order follows `PRODUCT.md`.

Do not pull future milestone features into current milestone unless they are required for correctness or avoid rework in a hard-to-change schema.

Priority:

1. durable ingestion;
2. historical retrieval;
3. authentication and secure UI;
4. live tail;
5. retention and status;
6. backup and recovery;
7. hardening and public documentation;
8. post-dogfood improvements.

Do not polish branding before the ingestion path is trustworthy.

---

## 31. Agent completion report

At the end of a coding task, report concisely:

- files changed;
- behavior implemented;
- tests run and results;
- benchmarks run and results if applicable;
- migrations added;
- documentation updated;
- tracked task status updated where applicable;
- known limitations or follow-ups;
- anything not completed.

Never claim a test, benchmark, security review, or manual check was performed when it was not.

---

## 32. Final instruction

Siftail's quality depends more on what it refuses to become than on feature count.

When choosing between:

- generic and explicit;
- clever and understandable;
- scalable in theory and bounded in practice;
- feature-rich and maintainable;
- remotely integrated and private;

choose the option that keeps Siftail fast, private, reliable, and small.
