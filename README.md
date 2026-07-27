# Siftail Planning Package

This package contains the authoritative planning documents for **Siftail**, a fast, private, self-hosted log viewer designed first for Coolify and Fluent Bit.

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

Siftail is built with **Go 1.22** and requires **CGO** for the SQLite driver (`mattn/go-sqlite3`).

Common local checks:

```bash
make fmt        # go fmt ./...
make vet        # go vet ./...
make test       # go test ./...
make race-test  # go test -race ./...
make build      # build the siftail binary
```

## Important note

These are planning and implementation specifications, not generated application code. When implementation intentionally changes an accepted decision, update the relevant specification, tests, migrations, and—when the decision is consequential—an ADR in the same change.
