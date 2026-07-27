# Siftail

This repository contains the implementation and authoritative specifications for
**Siftail**, a fast, private, self-hosted log viewer designed first for Coolify
and Fluent Bit.

The documents are written to be standalone. Coding agents should not need access to the planning conversation that produced them.

## Reading order

1. [`AGENTS.md`](AGENTS.md) — mandatory repository and implementation rules
2. [`DOMAIN.md`](DOMAIN.md) — canonical concepts, data semantics, and invariants
3. [`PRODUCT.md`](PRODUCT.md) — product vision, workflows, scope, and milestones
4. [`ARCHITECTURE.md`](ARCHITECTURE.md) — technical implementation and operations
5. [`DESIGN.md`](DESIGN.md) — interaction, visual, responsive, and accessibility specification
6. [`docs/decisions/`](docs/decisions/) — accepted reasoning for hard-to-reverse decisions
7. [`TASKS.md`](TASKS.md) — non-authoritative implementation dependencies and status

## Document responsibilities

| Document | Authority |
|---|---|
| `PRODUCT.md` | What is being built, for whom, why, and what is excluded |
| `DOMAIN.md` | Event model, source identity, ordering, lifecycle, retention, and invariants |
| `ARCHITECTURE.md` | Go/HTMX/SQLite runtime, security, ingestion, queries, deployment, testing, and recovery |
| `DESIGN.md` | Screens, interactions, visual system, copy, keyboard behavior, mobile, and accessibility |
| `AGENTS.md` | Mandatory working rules for coding agents and contributors |
| Accepted ADRs | Why consequential technical and domain decisions were approved |
| `TASKS.md` | What implementation work is ready, active, blocked, or complete |

`PRODUCT.md` §19 remains the canonical roadmap and `ARCHITECTURE.md` §40
remains the canonical technical sequence. `TASKS.md` turns those decisions into
reviewable work; it does not override them.

## Fixed project identity

- Product: **Siftail**
- Tagline: **Fast, private logs for self-hosted apps.**
- CLI and repository: `siftail`
- Environment prefix: `SIFTAIL_`
- Default database: `siftail.db`
- License: Apache License 2.0

The name is explained subtly in selected copy: users can **sift through historical logs and tail live events**.

## Foundational decisions

- one Docker container;
- one long-running Go process, with focused short-lived CLI commands from the same binary;
- separate UI and ingestion listeners;
- SQLite with WAL and `synchronous=FULL` through `mattn/go-sqlite3`;
- Go templates, HTMX, focused vanilla JavaScript, and plain CSS;
- no production Node.js runtime;
- one local administrator;
- one independently revocable ingestion token per server;
- commit-before-acknowledgement;
- batch atomicity;
- bounded memory and disk;
- no telemetry or external runtime services;
- Coolify-first, generic Fluent Bit HTTP best effort;
- metrics, traces, multi-tenancy, Kubernetes, dashboards, and AI analysis are out of scope.

## Development

The supported Go toolchain is declared by [`go.mod`](go.mod), which is also the
version source used by CI. CI enables CGO and installs the C compiler required
by the selected SQLite driver when that driver is introduced in SFT-005.

Common local checks:

```bash
make fmt        # go fmt ./...
make fmt-check  # verify formatting without modifying files
make vet        # go vet ./...
make test       # go test ./...
make race-test  # go test -race ./...
make build      # build the siftail binary
make check      # formatting, vet, tests, and metadata-bearing build
```

No process secret environment variable exists yet. When one is introduced,
direct and `_FILE` forms will be mutually exclusive; `_FILE` input removes only
trailing CR/LF bytes and never appears in sanitized configuration or logs.

### Ingestion compatibility

The authenticated endpoint is `POST /api/v1/ingest`. It accepts Fluent Bit
`json` as one object or an array of objects and `json_lines` as NDJSON, with an
optional `Content-Encoding: gzip`. The application independently caps compressed
and decompressed input, record count and size, JSON depth, and retained bytes.
Malformed final records, duplicate keys, trailing data, and non-object records
reject the complete request. Siftail currently returns `503` after valid decoding
because durable writer integration and commit-backed `204` acknowledgement are
assigned to SFT-012 and SFT-013.

The fixtures track Fluent Bit's official
[HTTP output](https://docs.fluentbit.io/manual/data-pipeline/outputs/http) and
Coolify's documented
[custom Fluent Bit drain](https://coolify.io/docs/knowledge-base/drain-logs).
Coolify aliases are compile-time compatibility rules, including the documented
`coolify.app_name` field.

### Current dependency rationale

- `github.com/mattn/go-sqlite3` v1.14.48 is the accepted SQLite driver. It
  bundles SQLite, is MIT licensed, requires CGO and a C toolchain, and adds no
  runtime service. Production images must therefore build explicitly for
  `linux/amd64` and `linux/arm64`. On linux/amd64 with Go 1.25, the initial
  linked database lifecycle increased the unstripped binary from 9,274,021
  bytes to 13,291,176 bytes (+4,017,155 bytes) in the SFT-005 linux/amd64
  verification build. Database or driver security updates require normal
  dependency review and a rebuilt container.
- `golang.org/x/sync/errgroup` provides owned cancellation and first-error
  propagation for the application’s critical components. The standard library
  does not provide the same combined group/error boundary. It has no transitive
  module dependencies and adds no runtime service or public API. Only its
  group/cancellation implementation is linked; binary-size impact has not yet
  been measured. It is maintained by the Go project under the BSD 3-Clause
  license.

## Important note

The specification documents are authoritative inputs to the implementation, not
generated descriptions of it. When implementation intentionally changes an
accepted decision, update the relevant specification, tests, migrations, and—
when the decision is consequential—an ADR in the same change.
