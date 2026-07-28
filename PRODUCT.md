# Siftail Product Specification

**Status:** Authoritative planning document  
**Audience:** Maintainer, coding agents, contributors, reviewers  
**Product:** Siftail  
**Tagline:** Fast, private logs for self-hosted apps.  
**Primary integration:** Coolify custom Fluent Bit log drains  
**Secondary integration:** Generic Fluent Bit HTTP output, best effort  
**License:** Apache License 2.0

---

## 1. Purpose of this document

This document defines **what Siftail is**, **who it serves**, **which problems it solves**, **which product behaviors are required**, and **which capabilities are intentionally excluded**.

It is authoritative for:

- product vision and positioning;
- target users and supported deployment context;
- product principles;
- functional scope;
- user workflows;
- milestones and release criteria;
- success criteria;
- explicit non-goals.

Implementation details belong primarily in `ARCHITECTURE.md`. Canonical entities, event semantics, and invariants belong in `DOMAIN.md`. Interface and interaction details belong in `DESIGN.md`. Coding-agent rules belong in `AGENTS.md`.

When documents appear to conflict, apply this order:

1. Existing behavior protected by tests and accepted migrations.
2. `DOMAIN.md` for canonical semantics and invariants.
3. `PRODUCT.md` for scope and intended outcomes.
4. `ARCHITECTURE.md` for technical realization.
5. `DESIGN.md` for user interaction and presentation.
6. Accepted ADRs for the reasoning behind later approved decisions.

`AGENTS.md` governs implementation process and repository rules. It does not override
the product or system behavior defined above.

A deliberate change to product behavior must update all affected documents in the same change.

---

## 2. Executive summary

Siftail is a **single-user, self-hosted log viewer and bounded log inbox** for small self-hosted environments. It is designed first for applications deployed through Coolify and receiving logs through Coolify's custom Fluent Bit log drain.

Siftail receives application logs over authenticated HTTP, stores them locally in SQLite, and exposes a focused browser interface for:

- sifting through recent historical logs;
- tailing live events as they arrive;
- filtering by server, project, environment, application, service, container, level, and stream;
- searching message content;
- inspecting structured attributes;
- enforcing bounded retention by age and database size;
- backing up and restoring the local database;
- monitoring Siftail's own ingestion and storage health.

The name is explained subtly, not repeatedly: users can **sift through history and tail live events** from one private interface.

Siftail is deliberately not a replacement for Grafana, Loki, Elasticsearch, Axiom, Datadog, or a full observability platform. It does not attempt to provide metrics, distributed tracing, dashboards, multi-tenancy, teams, cloud storage, or compliance archiving.

The core product promise is:

> A fast, private, low-resource operational troubleshooting tool that runs as one container, stores data locally, has no telemetry, and requires no paid external service.

---

## 3. Product vision

### 3.1 Vision statement

Provide self-hosters with the smallest practical tool that can reliably receive, retain, search, and live-tail application logs without sending operational data to a third party or requiring a resource-heavy observability stack.

### 3.2 Product positioning

Siftail is:

- **Coolify-first**, but not Coolify-exclusive;
- **single-administrator**, but able to receive logs from multiple servers;
- **local-first**, with all operational data stored on the operator's infrastructure;
- **bounded by design**, so memory and disk use cannot grow without limits;
- **focused on troubleshooting**, not analytics or business intelligence;
- **an appliance**, not a platform requiring ongoing infrastructure assembly.

### 3.3 Primary value proposition

Siftail replaces the recurring cost and privacy compromise of hosted log-drain services for small self-hosted deployments.

The operator receives:

- one container;
- one persistent data volume;
- one local SQLite database;
- one administrative interface;
- one ingestion token per source server;
- no external runtime service;
- no usage-based bill;
- no telemetry;
- no vendor data retention.

### 3.4 Differentiation

Siftail differentiates itself through restraint:

| Dimension | Siftail position |
|---|---|
| Deployment | One container, one process, one volume |
| Storage | Local SQLite |
| Runtime dependencies | None beyond the container |
| User model | One administrator |
| Primary workflow | Troubleshoot recent application behavior |
| Cost | Free and self-hosted |
| Privacy | No external data transfer or telemetry |
| Resource model | Bounded memory, bounded disk, negligible idle CPU |
| Interface | Dense operational console, not a dashboard suite |
| Integration | Ready-to-paste Coolify/Fluent Bit configuration |

---

## 4. Brand and communication

### 4.1 Name

**Siftail** is the product, repository, Docker image, and CLI name.

Canonical naming:

- Product: `Siftail`
- CLI: `siftail`
- Repository: `siftail`
- Container image: `ghcr.io/drilonrecica/siftail`
- Environment variable prefix: `SIFTAIL_`
- Default database filename: `siftail.db`

### 4.2 Tagline

> **Fast, private logs for self-hosted apps.**

This is the primary tagline and should be used consistently.

### 4.3 Subtle name explanation

The name meaning may appear once in the README introduction and once in an About or brand-story section:

> Sift through historical logs and tail live events from one lightweight, private interface.

Do not repeatedly explain the portmanteau. Do not put the explanation in every screen, release note, or logo lockup.

### 4.4 Brand character

The brand should feel:

1. fast;
2. private;
3. simple;
4. dependable;
5. technical;
6. professionally maintained;
7. slightly retro-terminal;
8. mildly friendly and indie, never cute or unserious.

The brand must not imitate enterprise observability marketing. Avoid claims such as “infinite scale,” “AI-powered insights,” or “complete observability.”

### 4.5 Visual direction summary

- restrained operational console;
- dark-first, optional light theme;
- muted cyan/blue primary accent;
- restrained warm amber secondary accent;
- semantic warning/error/success colors remain distinct from the brand accent;
- system sans-serif for UI;
- system monospace for logs and identifiers;
- no external fonts;
- no mascot;
- no decorative gradients;
- no scanlines, CRT effects, neon overload, or fake terminal theatrics.

The proposed logo concept is several log lines being filtered into one clear continuing stream inside a compact contained shape.

---

## 5. Target user and deployment context

### 5.1 Primary user

The primary user is a technically capable self-hoster or software developer who:

- operates one or more Coolify servers;
- deploys a modest number of web services, APIs, workers, or personal products;
- needs recent logs for debugging and incident investigation;
- does not want a hosted log provider;
- values privacy and ownership;
- prefers a small operational footprint;
- can manage Docker, environment variables, DNS, and reverse proxies;
- may access the tool from desktop and occasionally from a phone through Tailscale or HTTPS.

### 5.2 User model

Version one supports:

- one local administrator account;
- multiple browser sessions for that administrator;
- multiple source servers;
- one independently revocable ingestion token per server.

Version one does not support:

- teams;
- invitations;
- user registration;
- roles or permissions;
- organizations;
- tenants;
- public sharing.

### 5.3 Supported environments

Primary supported environment:

- Docker deployment managed through Coolify;
- custom Coolify Fluent Bit log drain;
- persistent local volume;
- TLS terminated by Coolify or another reverse proxy.

Secondary supported environment:

- Docker Compose;
- generic Fluent Bit HTTP output;
- local or Tailscale-only networking.

Initial published container architectures:

- `linux/amd64`;
- `linux/arm64`.

### 5.4 Expected scale

Siftail targets personal and small self-hosted workloads, not enterprise-scale clusters.

Expected workload envelope:

| Workload | Approximate rate |
|---|---:|
| Normal | 1–50 events/second |
| Heavy | 100–500 events/second |
| Sustained engineering target | 1,000 events/second |
| Temporary burst test | 5,000 events/second |

Normal installations are expected to retain recent history for roughly 14 days with a configurable database-size limit, typically around 4 GB.

---

## 6. Product principles

Every product and implementation decision must be evaluated against these principles, in order.

### 6.1 Performance first

Siftail must feel immediate and must not materially interfere with the applications it monitors.

Performance means:

- durable batch ingestion with low latency;
- predictable memory use;
- fast recent-history queries;
- responsive filtering;
- bounded browser DOM usage;
- negligible idle CPU;
- no unnecessary runtime services.

Performance does not mean pursuing benchmark vanity at the cost of correctness, maintainability, or user safety.

### 6.2 Maximum practical privacy

Siftail must not send logs, usage data, diagnostics, or metadata outside the operator's infrastructure.

Required privacy properties:

- no telemetry;
- no analytics;
- no crash reporting;
- no remote fonts or scripts;
- no external API calls;
- no hosted dependency;
- no automatic update checks unless explicitly introduced and disabled by default;
- no AI analysis;
- no advertiser or vendor access;
- application logs remain local unless the administrator explicitly exports or backs them up.

Siftail does not promise that application logs are free of secrets. It stores what applications emit.

### 6.3 Minimum resource use

The product must remain small enough to run continuously on an ordinary self-hosted server.

Targets:

- idle RSS below 50 MB;
- normal-operation RSS below 100 MB;
- effectively zero idle CPU;
- bounded in-memory ingestion queue;
- one Go process;
- one SQLite database;
- no Redis, PostgreSQL, Elasticsearch, ClickHouse, or Java runtime;
- no Node.js runtime in production.

### 6.4 Reliability and explicit failure

Siftail must reject work clearly rather than fail invisibly.

Under pressure, it must:

- return explicit retryable HTTP responses;
- avoid unbounded memory growth;
- avoid filling the host disk beyond configured limits;
- preserve read access when ingestion is unavailable;
- never acknowledge a batch before its SQLite transaction commits;
- avoid partial acceptance of a request;
- avoid silent database recreation or data loss.

### 6.5 Simplicity

Siftail should be understandable by one maintainer months after implementation.

Prefer:

- direct code;
- handwritten SQL;
- explicit lifecycle management;
- ordinary HTML;
- HTMX for fragment updates;
- small vanilla JavaScript modules for live behavior;
- a narrow feature set.

Reject abstractions, frameworks, or services added only for hypothetical future needs.

### 6.6 Focused scope

Siftail must remain a log troubleshooting tool. It is not an observability platform.

A feature is acceptable only when it solves a repeated real workflow without violating the performance, privacy, simplicity, or resource principles.

---

## 7. Problems to solve

### 7.1 Current problem

Hosted log-drain providers are convenient but may:

- charge by ingestion, storage, or retention;
- impose free-tier limits;
- transfer potentially sensitive application data off infrastructure;
- create external-service dependence;
- be excessive for personal or small deployments.

Self-hosted observability stacks may avoid subscription cost but often require multiple components, substantial RAM, and ongoing maintenance.

### 7.2 User needs

The user needs to answer questions such as:

- What happened in this application during the last hour?
- Did errors begin after a redeployment?
- Which service produced this stack trace?
- What other logs share this request ID?
- Is the problem occurring live right now?
- Can I inspect logs from several Coolify servers in one private interface?
- How much disk space are logs using?
- Will the logger fill my server disk?
- Can I back up or restore the logger safely?
- Can I use it without sending data to a third party?

### 7.3 Product answer

Siftail provides:

- authenticated HTTP ingestion;
- automatic source discovery;
- recent local history;
- live SSE streaming;
- source and severity filters;
- bounded substring search;
- structured-field filtering for selected attributes;
- automatic age and size retention;
- explicit backup and restore tooling;
- operational health and diagnostics.

---

## 8. Goals

### 8.1 Functional goals

Siftail must:

1. receive logs from Coolify's custom Fluent Bit drain;
2. support generic Fluent Bit HTTP output on a best-effort basis;
3. authenticate each source server with an independently revocable token;
4. preserve plain text, JSON logs, and already assembled multiline stack traces;
5. store event and receive timestamps;
6. preserve stable logical source identity across container replacements;
7. expose historical filtering and search;
8. expose live tailing without polling;
9. enforce both age-based and size-based retention;
10. prevent unbounded memory and disk use;
11. support safe backup, verification, and restoration;
12. provide a secure single-administrator interface;
13. remain usable on mobile for emergency inspection;
14. provide clear setup guidance and generated Fluent Bit configuration;
15. run without external runtime dependencies.

### 8.2 Quality goals

Siftail must be:

- secure by default;
- accessible to WCAG 2.2 AA as a design target;
- deterministic in event ordering;
- honest about limitations;
- maintainable by one primary maintainer;
- testable with real SQLite databases;
- safe to upgrade through automatic transactional migrations;
- suitable for sustained personal production use.

### 8.3 Operational goals

The operator should be able to:

- deploy Siftail through Coolify or Compose;
- initialize the administrator from the CLI;
- create a server token;
- paste generated Fluent Bit configuration into Coolify;
- verify ingestion through a guided test;
- inspect logs within minutes of deployment;
- upgrade by pulling a new image and restarting;
- diagnose readiness or storage problems;
- create and verify backups without stopping normal ingestion;
- restore through an explicit maintenance operation.

---

## 9. Non-goals and rejected scope

The following are explicitly outside version one and should not be introduced accidentally.

### 9.1 Observability expansion

- metrics collection or storage;
- Prometheus server behavior;
- distributed tracing;
- OpenTelemetry collector functionality;
- uptime monitoring;
- synthetic checks;
- application performance monitoring;
- profiling;
- dashboards or chart builders;
- log-derived metrics.

### 9.2 Enterprise and SaaS features

- multi-tenancy;
- organizations;
- multiple users;
- permissions and roles;
- SSO integrations;
- trusted identity-header or forward-auth authentication before the public dogfood release;
- hosted control plane;
- billing;
- usage metering;
- cloud storage;
- compliance retention or legal hold;
- audit-export compliance packages.

### 9.3 Infrastructure expansion

- Kubernetes-specific support;
- clustering;
- horizontal scaling;
- distributed ingestion;
- Redis;
- PostgreSQL support;
- Elasticsearch or OpenSearch compatibility;
- ClickHouse support;
- Loki API compatibility;
- pluggable storage engines;
- arbitrary runtime plugins.

### 9.4 Product expansion

- native mobile application;
- PWA requirements;
- AI summaries or AI troubleshooting;
- automatic secret detection;
- built-in log-content redaction in version one;
- arbitrary regular-expression query language;
- full JSON-path query language;
- alerting in version one;
- log forwarding to other systems;
- permanent ingestion suppression rules;
- user-defined dashboards;
- public sharing links.

### 9.5 Misleading promises

Siftail must not claim:

- forensic secure deletion;
- automatic secret safety;
- encrypted database storage by default;
- exactly-once delivery when sources do not provide stable event IDs;
- unlimited scale;
- guaranteed losslessness during source-side misconfiguration;
- complete Coolify API integration.

---

## 10. Core product capabilities

### 10.1 Installation and first-run setup

Required outcomes:

- deploy one container with one persistent volume;
- configure UI and ingestion listeners;
- configure a public URL explicitly;
- run `siftail admin create` to create the administrator;
- sign in through the browser;
- create a named server and ingestion token;
- display the token once;
- generate Coolify Fluent Bit configuration;
- run a guided test event;
- confirm that the event authenticated, normalized, committed, and appeared under the discovered source.

Acceptance criteria:

- no unauthenticated browser setup page is exposed;
- no generated password is printed to logs;
- token plaintext is never shown again after creation;
- setup is possible without editing a configuration file;
- the system clearly distinguishes UI and ingestion addresses.

### 10.2 Server and token management

The administrator can:

- create a logical source server;
- assign a display name;
- create one active ingestion token per server initially;
- rotate or revoke the token;
- see creation and last-use metadata;
- generate updated Fluent Bit configuration;
- perform a test ingestion;
- remove a server only through a destructive flow.

The token determines trusted server identity. Incoming payloads cannot impersonate another configured server.

### 10.3 Log ingestion

Required behavior:

- accept gzip-compressed NDJSON batches;
- accept compatible Fluent Bit HTTP JSON formats through normalization;
- reject unsupported or malformed requests explicitly;
- authenticate before expensive processing where practical;
- enforce compressed, decompressed, event-count, event-size, and nesting limits;
- accept a complete request atomically or reject it before queueing;
- acknowledge only after SQLite commit;
- return retryable status codes for temporary capacity or storage problems;
- preserve original application payloads according to the domain model;
- create unknown sources automatically using layered metadata fallbacks.

### 10.4 Historical log workspace

The historical workspace must provide:

- restored previous investigation state, with a one-hour fallback;
- quick time presets: 15m, 1h, 6h, 24h, 7d, Custom;
- cascading source filters;
- multiple explicit level selection;
- independent stdout/stderr/unknown stream filtering;
- literal ASCII case-folded message substring search;
- one temporary “does not contain” filter;
- an exact container-instance filter under More filters;
- exact filters for selected normalized fields such as request ID, logger, method, status, and error type;
- deterministic cursor pagination;
- 200 events per initial page;
- inline expansion of event details;
- text and NDJSON export with limits;
- complete query state in the browser URL.

Historical results must not silently mix newly arriving live events into the result set.
One historical query may cover at most 31 days.

### 10.5 Live-tail workspace

The live workspace must provide:

- explicit Live mode separate from History mode;
- SSE connection status: Connecting, Live, Paused, Disconnected;
- source, level, and stream filters;
- pause and resume;
- auto-scroll while the user is at the bottom;
- bounded rendering and pending buffers;
- a visible new-events counter when the user scrolls upward;
- a jump-to-newest action;
- clear truncation notice when browser-only limits are exceeded;
- isolation of slow subscribers so they never block ingestion.

Initial browser targets:

- maximum rendered rows: 1,000;
- maximum pending rows while scrolled away: 2,000.

### 10.6 Source discovery and management

Siftail automatically creates source records when logs first arrive.

The administrator can:

- browse server → project → environment → application → service;
- view current and previous container instances;
- set a display alias;
- clear logs while keeping source configuration;
- remove a source and its associated metadata through a stronger destructive flow;
- see active and inactive state;
- see first-seen and last-seen times.

Source aliases change presentation only and never rewrite original metadata.

### 10.7 Deployment boundaries

Deployment-boundary inference is a post-dogfood candidate, not a version-one
requirement. If accepted later, Siftail may infer a lightweight deployment boundary
when the active container identity changes for a stable service.

A deployment boundary:

- is an inferred internal marker;
- appears as a subtle separator in history;
- does not count as an application log event;
- is not included in application-log exports;
- does not claim a deployment succeeded;
- does not require Coolify API integration.

### 10.8 Retention and disk control

The administrator can configure:

- global application-log retention age;
- global maximum database size;
- separate security-audit retention.

Defaults:

- application logs: 14 days;
- maximum database size: 4 GiB;
- audit records: 365 days;
- audit record cap: 100,000;
- cleanup interval: one hour.

Application-log age accepts whole values from 1 through 3,650 days. The maximum
active database footprint accepts whole values from 1 through 1,024 GiB. These
two values are saved atomically; partial retention-policy updates are not
supported.

When either application-log threshold is reached, oldest application logs are deleted in bounded chunks.
Size retention starts when the active SQLite footprint reaches 95% of the configured
limit and targets 90%. The active footprint is the main database plus its WAL and SHM
files.

If the host filesystem becomes full despite normal retention, Siftail enters degraded read-only behavior:

- ingestion returns `507 Insufficient Storage`;
- history and administrative read access remain available where possible;
- a critical warning is shown;
- cleanup may attempt safe bounded reclamation;
- Siftail does not buffer indefinitely in memory or delete unrelated data.

### 10.9 Authentication and session management

Required behavior:

- one local administrator account;
- Argon2id password hash;
- opaque server-side sessions;
- only session-token hashes stored;
- secure, HTTP-only, SameSite=Strict cookies;
- explicit expiration and revocation;
- expired sessions become unusable immediately;
- expired session rows are removed after a seven-day grace period;
- progressive login throttling by source identity and account;
- no CAPTCHA or external dependency;
- ordinary reverse-proxy and TLS termination support without trusting identity headers.

### 10.10 Security audit

Siftail records immutable, bounded security-sensitive administrative events, including:

- successful and failed sign-ins;
- session revocation;
- password changes;
- ingestion-token creation, rotation, and revocation;
- source alias changes;
- retention changes;
- backup and restore operations;
- source purges;

Audit entries never include plaintext credentials, authorization headers, full application log content, or session tokens.

### 10.11 Status and diagnostics

The authenticated status page must show:

- process uptime;
- application version;
- database schema version;
- SQLite version;
- database size;
- WAL size;
- oldest and newest event;
- events received today;
- current ingestion rate;
- queue events and bytes;
- rejected batches;
- last database error;
- last retention result;
- last backup result;
- active live clients;
- readiness state;
- sanitized effective configuration.

The diagnostics area may show the latest 100 sanitized operational events. It must not mirror raw process logs or application logs.

### 10.12 Backup, verification, and restore

Before the first public release, Siftail must provide:

- full database backup using SQLite's online backup mechanism;
- configuration-only backup;
- backup verification;
- explicit restore command;
- schema compatibility validation;
- preservation of the current database as a rollback copy during restore;
- clear requirement that ingestion be stopped during restore;
- audit events for backup and restore actions.

Every backup type excludes active and historical administrator sessions. Restoring a
backup therefore requires a new sign-in.

Suggested CLI:

```bash
siftail backup --output /backup/siftail.sqlite
siftail backup --configuration-only --output /backup/siftail-config.sqlite
siftail backup verify /backup/siftail.sqlite
siftail restore /backup/siftail.sqlite
```

Scheduling and remote backup storage remain operator responsibilities.

### 10.13 Export

Historical queries support:

- plain-text export;
- NDJSON export.

Exports must:

- represent the complete matching result set within configured limits;
- stream from the database;
- avoid loading the whole export into memory;
- preserve multiline content correctly;
- record the export action in the audit log;
- require confirmation for large exports.

CSV, XML, PDF, and database-format log exports are not required.

---

## 11. Primary user journeys

### 11.1 Deploy and connect a Coolify server

1. Deploy Siftail with `/data` persisted.
2. Configure `SIFTAIL_PUBLIC_URL`, UI listener, and ingestion listener.
3. Run `siftail admin create`.
4. Sign in.
5. Create server `Hetzner Production`.
6. Copy the one-time ingestion token.
7. Generate the Coolify Fluent Bit configuration.
8. Paste it into Coolify's custom log drain.
9. Exclude the Siftail container to prevent recursive ingestion.
10. Restart or apply the relevant Coolify resources.
11. Run the guided test.
12. Confirm the test log appears.

Success condition: a new operator can complete the flow using written documentation without inspecting source code.

### 11.2 Investigate a production error

1. Open Siftail.
2. Return to the previous source and range or choose an application through the quick switcher.
3. Select History mode.
4. Choose the last hour.
5. Select `warn`, `error`, and `fatal`.
6. Search for an error phrase or request ID.
7. Expand an event.
8. inspect raw payload and structured attributes.
9. move to adjacent events for context.
10. copy or export the relevant subset.

Success condition: common investigations require no query language.

### 11.3 Tail an active incident

1. Open the affected source.
2. Switch to Live mode.
3. Filter to relevant levels and service.
4. Observe new events.
5. Scroll upward to inspect context.
6. New events accumulate in a bounded client buffer.
7. Use the new-events indicator to return to the bottom.
8. Pause if necessary.
9. Switch to History mode when a bounded search is needed.

Success condition: live investigation remains stable under high event rates and never forces the user's scroll position.

### 11.4 Respond to disk pressure

1. Status page reports database approaching configured limit.
2. The retention worker deletes oldest eligible events in chunks.
3. Incremental vacuum and WAL maintenance run within bounded limits.
4. The UI reports size-based pruning.
5. If the host disk is actually full, ingestion becomes unavailable with `507` while read access remains.
6. The administrator frees host space or adjusts retention.

Success condition: Siftail never knowingly consumes unlimited disk or memory.

### 11.5 Rotate a compromised server token

1. Open server settings.
2. Select Rotate token.
3. Confirm the action.
4. Receive a new token once.
5. Update Coolify configuration.
6. Verify test ingestion.
7. Revoke the previous token.
8. Review the audit event.

Success condition: other servers continue ingesting normally.

### 11.6 Back up and upgrade

1. Create a verified backup.
2. Pull a newer stable image.
3. Restart Siftail.
4. Automatic migrations run transactionally.
5. Startup quick check succeeds.
6. Readiness becomes healthy.
7. Verify history and ingestion.

If the database schema is newer than the binary supports, the older binary refuses startup with a precise message.

---

## 12. Interface information architecture

Primary pages:

- `/login`
- `/logs`
- `/sources`
- `/servers`
- `/settings`
- `/status`
- `/audit`

The log workspace is the primary destination. After first use, Siftail reopens the last investigation rather than showing a dashboard.

Primary log modes:

- History
- Live

Navigation should remain compact. There is no analytics dashboard homepage.

---

## 13. Search and filter product contract

Version one provides:

- literal substring search over `message_text` with ASCII-only case folding;
- one literal “does not contain” condition using the same comparison;
- selected exact structured-field filters;
- explicit level multiselect;
- explicit stream multiselect;
- hierarchical source selection;
- an exact container-instance filter under More filters;
- half-open time ranges `[from, to)` of at most 31 days;
- deterministic cursor pagination.

Non-ASCII text is compared byte-for-byte after valid UTF-8 decoding. Search terms have
no wildcard or escape syntax.

Version one does not provide:

- regular expressions;
- Boolean query syntax;
- arbitrary field expressions;
- arbitrary JSON-path queries;
- fuzzy search;
- saved-search CRUD;
- FTS5 by default.

Complete historical query state is represented in the URL so browser bookmarks serve as saved views.

---

## 14. Privacy and security promises

### 14.1 Promises Siftail makes

- Siftail does not transmit logs or product usage to the maintainer.
- Siftail does not embed third-party analytics, fonts, or scripts.
- All application logs are stored in the operator-controlled data volume.
- The application authenticates browser access and ingestion by default.
- Destructive operations require proportional confirmation.
- Security-sensitive actions are audited.
- Ingestion tokens and session tokens are stored as hashes, not recoverable plaintext.
- Browser state-changing requests use explicit CSRF protection and origin validation.

### 14.2 Responsibilities retained by the operator

- prevent applications from logging secrets;
- secure the host and persistent volume;
- use disk or volume encryption when at-rest encryption is required;
- protect backups;
- configure reverse-proxy TLS correctly;
- restrict ingestion networking where practical;
- keep Siftail updated;
- verify external backup schedules;
- avoid exposing the ingestion endpoint unnecessarily.

### 14.3 Explicit limitations

- Siftail stores exactly what applications emit.
- Version one does not redact content.
- Host or volume encryption is external to Siftail.
- Deletion cannot guarantee forensic erasure from SSDs, WAL files, snapshots, or backups.
- Occasional duplicates may appear when delivery is retried without a stable source event ID.

---

## 15. Performance and resource acceptance targets

### 15.1 Memory

- Idle RSS: below 50 MB on the production container image.
- Normal operation RSS: below 100 MB under representative traffic and several active browser clients.
- Queue memory is explicit, configurable, and included in resource documentation.
- Overload produces rejection, never unbounded allocation.

### 15.2 CPU

- Idle CPU should be effectively zero.
- No frequent polling loop should wake the process unnecessarily.
- Maintenance workers use low-frequency timers and bounded work.

### 15.3 Ingestion

Normal-load commit latency targets:

- p50 below 75 ms;
- p95 below 250 ms;
- p99 below 500 ms.

Heavy sustained-load target:

- p95 below one second at the defined sustained engineering load.

These are measured engineering targets, not hardware-independent contractual guarantees.

### 15.4 Query performance

Benchmarks must cover:

- 100,000 events;
- 1,000,000 events;
- 10,000,000 events.

Representative queries:

- newest events for one service;
- errors in the last hour;
- substring search in one application over one day;
- exact request-ID lookup;
- cursor pagination;
- source listing;
- retention deletion.

### 15.5 Image and dependency footprint

- multi-stage image;
- no Node runtime in final image;
- no package manager cache in final image;
- image-size regressions over 20% require review;
- new runtime dependency requires documented justification.

---

## 16. Accessibility and responsive-product requirements

Siftail targets WCAG 2.2 AA.

Required behaviors:

- keyboard-accessible primary workflows;
- visible focus indication;
- color never conveys severity alone;
- semantic form labels and errors;
- correctly managed dialogs;
- reduced-motion support;
- live connection and status changes announced accessibly without flooding assistive technology;
- sufficient contrast in dark and light themes;
- emergency mobile use for recent errors, search, expansion, copying, and live pause/resume.

Desktop remains the primary dense workflow. Mobile is responsive and functional, not a separate product.

---

## 17. Configuration product model

Infrastructure configuration comes from environment variables and requires restart when changed.

Runtime operational settings live in SQLite and apply immediately where safe.

No configuration file is supported in version one.

Environment examples:

```env
SIFTAIL_DATA_DIR=/data
SIFTAIL_UI_ADDR=:8080
SIFTAIL_INGEST_ADDR=:8081
SIFTAIL_PUBLIC_URL=https://logs.example.com
SIFTAIL_INGEST_PUBLIC_URL=https://ingest.logs.example.com/api/v1/ingest
SIFTAIL_LOG_LEVEL=info
SIFTAIL_LOG_FORMAT=text
```

Secrets may use `_FILE` variants. Setting both a direct secret and its `_FILE` variant is an error.

The UI shows a sanitized effective configuration, never raw secrets or complete environment dumps.

---

## 18. Release and distribution policy

### 18.1 Repository and license

- public repository;
- licensed under Apache License 2.0;
- maintainer-led governance;
- external contributions accepted selectively;
- technically sound scope expansion may still be declined.

### 18.2 Artifacts

Version one publishes:

- multi-architecture Docker images for `linux/amd64` and `linux/arm64`;
- Docker Compose example;
- manual Coolify deployment instructions.

A Coolify one-click template comes only after ordinary deployment is stable.

### 18.3 Channels

- `latest`: latest stable release;
- `edge`: successful development image;
- semantic version tags: `0.4.1`, `0.4`, `0` as appropriate.

### 18.4 Versioning

Semantic Versioning is used from the beginning.

Before `1.0`:

- database migrations remain forward-safe;
- configuration names are not changed casually;
- ingestion compatibility is preserved where practical;
- breaking changes are explicit in release notes;
- “pre-1.0” is not permission to destroy user data.

### 18.5 Support

Before `1.0`, only the latest stable version is supported.

After `1.0`, the latest minor release in the current major receives ordinary support. Critical backports may be made when reasonable.

---

## 19. Milestones

### 19.1 `0.1.0` — Durable ingestion and storage

Required:

- repository foundations;
- configuration validation;
- SQLite opening and migrations;
- canonical event normalization;
- server token authentication;
- NDJSON and compatible Fluent Bit HTTP decoding;
- bounded queue;
- batch commit;
- commit-before-acknowledgement;
- administrator server creation and token management through the CLI;
- source discovery;
- integration tests;
- command-line ingestion tests;
- initial benchmark harness.

No polished browser UI is required.

### 19.2 `0.2.0` — Authentication and historical browsing

Required:

- administrator CLI creation;
- login and sessions;
- CSRF and security headers;
- historical query store;
- initial HTMX log workspace;
- source filters;
- time range;
- level and stream filters;
- cursor pagination;
- inline details;
- URL query state.

### 19.3 `0.3.0` — Live tail, aliases, and retention

Required:

- SSE broker;
- live/history mode separation;
- pause/resume and bounded client buffers;
- aliases;
- source lifecycle;
- browser server and ingestion-token management;
- retention age and size limits;
- status page;
- generated Coolify configuration;
- guided test ingestion;
- responsive emergency mobile behavior.

This is the first personally usable target.

### 19.4 `0.4.0` — Backup, recovery, and hardening

Required:

- full and configuration-only backup;
- backup verification;
- restore;
- database checks;
- operational diagnostics;
- audit log;
- bounded text and NDJSON History export with audit recording;
- disk-full degraded mode;
- migration fixtures;
- failure-path tests;
- security review checklist.

This milestone is required before public release.

### 19.5 `0.5.0` — Public dogfood release

Required:

- complete operator documentation;
- Docker Compose example;
- manual Coolify deployment guide;
- multi-architecture image;
- stable upgrade procedure;
- performance measurements;
- soak testing;
- polished core interface;
- known-issues documentation.

### 19.6 `1.0.0` — Proven operational stability

`1.0` is earned when:

- Siftail has been relied upon for production logs for several months;
- no known data-loss defect remains;
- migrations and backups are proven;
- resource targets remain stable;
- ingestion and essential configuration contracts are stable;
- common operations are documented;
- the maintainer can confidently reject incompatible expansion;
- the release checklist passes consistently.

`1.0` is not defined by feature count, stars, or calendar date.

---

## 20. Product success criteria

### 20.1 Primary success

The project succeeds when the maintainer can replace paid hosted log draining for personal Coolify servers with Siftail and trust it in daily production use.

### 20.2 Quantitative criteria

- idle RSS below target;
- normal RSS below target;
- sustained ingest benchmark meets target on documented hardware;
- normal p95 commit latency meets target;
- retention prevents uncontrolled database growth;
- no accepted batch is lost in tested graceful-shutdown scenarios;
- migration fixtures pass for every released schema;
- backup and restore pass end-to-end;
- critical UI workflows pass Playwright smoke tests;
- no outbound network requests occur during ordinary operation.

### 20.3 Qualitative criteria

- common investigations require no documentation after initial learning;
- UI remains calm and readable during incidents;
- setup instructions are sufficient for a technically capable self-hoster;
- the product feels significantly simpler than deploying a full observability stack;
- code remains understandable by one maintainer;
- feature requests can be rejected using a clear product boundary.

---

## 21. Feature evaluation framework

A proposed feature must answer all of the following:

1. Which repeated real personal workflow does it solve?
2. Why is the current workflow insufficient?
3. Can a simpler change solve the same problem?
4. Does it increase idle or steady-state resource use?
5. Does it expand the security or privacy surface?
6. Does it add a new runtime service?
7. Does it require a new public compatibility contract?
8. Does it create ongoing maintenance obligations?
9. Is it compatible with one-container deployment?
10. Does it move Siftail toward a general observability platform?

A useful idea may be documented without being accepted.

The first likely post-dogfood candidates are:

- simple grouped webhook notifications;
- browser-local recent searches;
- improved structured-attribute inspection;
- inferred deployment boundaries.

None are guaranteed.

---

## 22. Risks and mitigations

### 22.1 SQLite scale concerns

**Risk:** Large databases or broad substring searches become slow.

**Mitigation:** Require bounded time ranges, use indexed source/time filters, benchmark realistic tiers, add FTS5 only after measured need, and keep the supported scale honest.

### 22.2 Log storms

**Risk:** Incidents create sudden high-volume bursts.

**Mitigation:** Bounded request limits, bounded queue by events and bytes, batched writes, `503` backpressure, Fluent Bit filesystem buffering, size retention, and documented capacity.

### 22.3 Disk exhaustion

**Risk:** Logs or unrelated host data fill the disk.

**Mitigation:** Global database cap, chunked retention, status warnings, degraded read-only mode, `507` response, no unbounded memory fallback.

### 22.4 Sensitive log content

**Risk:** Applications emit credentials or personal data.

**Mitigation:** Clear documentation, no false redaction promise, secure browser access, local storage, protected backups, optional host encryption.

### 22.5 Scope creep

**Risk:** Community requests push toward metrics, teams, Kubernetes, or dashboards.

**Mitigation:** Explicit non-goals, maintainer-led governance, feature evaluation framework, and ADR requirement for consequential changes.

### 22.6 Coolify integration changes

**Risk:** Metadata fields or custom drain behavior changes between Coolify versions.

**Mitigation:** Layered metadata fallbacks, preservation of original attributes, loose coupling, generated configuration rather than API mutation, and documented tested versions.

### 22.7 Maintainer burden

**Risk:** Public open-source support becomes disproportionate.

**Mitigation:** Latest-stable-only support before `1.0`, narrow artifacts, no broad plugin system, selective PR acceptance, operator-focused docs, and clear issue templates.

---

## 23. Documentation required before public release

The public documentation set must cover:

- product overview and boundary;
- Coolify installation;
- Docker Compose installation;
- administrator initialization;
- server and token creation;
- Fluent Bit configuration generation;
- private networking and public HTTPS;
- Tailscale deployment example where appropriate;
- retention and disk sizing;
- backup, verification, and restore;
- updates and migrations;
- downgrade limitations;
- ingestion troubleshooting;
- disk-full recovery;
- security model;
- privacy model;
- source purging and deletion limitations;
- uninstallation and data removal;
- generic Fluent Bit example;
- supported release policy.

Written documentation is authoritative. Videos may supplement it later but must not replace it.

---

## 24. Final product boundary

Siftail is complete when it can reliably do the following and resist doing much more:

> Receive logs from one or more self-hosted servers, store a bounded recent history locally, provide fast historical filtering and live tailing, protect access, expose operational health, and support safe backup and recovery—all from one low-resource container with no paid or external runtime service.

Everything else must justify its existence against that boundary.
