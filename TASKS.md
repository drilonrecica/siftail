# Siftail task registry

**Status:** Non-authoritative execution index
**Planning horizon:** `0.1.0` through `1.0.0`
**Canonical roadmap:** `PRODUCT.md` §19
**Canonical implementation order:** `ARCHITECTURE.md` §40

---

## How to use this registry

`TASKS.md` is the permanent task-ID registry and milestone dashboard. Detailed
task scope, dependencies, status, acceptance criteria, and evidence live in the
linked `docs/tasks/<milestone>.md` file. That milestone file is the sole source
of individual task status.

The tracker does not define product behavior. Accepted tests and migrations,
`DOMAIN.md`, `PRODUCT.md`, `ARCHITECTURE.md`, `DESIGN.md`, and accepted ADRs
remain authoritative. Correct the tracker when it conflicts with those sources.

Task IDs are permanent. Never renumber or reuse them.

Allowed task statuses:

- `Planned`: accepted work whose dependencies or scheduling are not ready;
- `Ready`: dependencies are complete and implementation may begin;
- `In Progress`: actively being implemented;
- `Blocked`: cannot progress and names the concrete blocker;
- `Done`: acceptance criteria and required verification are complete.

Update the relevant milestone file in the implementation change. Add the issue
or PR link, or record `direct maintainer implementation`. A task is not `Done`
when compilation alone succeeds or required verification is deferred without a
tracked follow-up.

Milestone implementation state and release state are separate. Completing every
task does not tag, publish, or otherwise release a version.

## Milestone dashboard

| Milestone | Task IDs | Implementation state | Release state | Detailed record |
|---|---|---|---|---|
| `0.1.0` | SFT-001–SFT-015 | Complete | Unreleased | [`docs/tasks/0.1.0.md`](docs/tasks/0.1.0.md) |
| `0.2.0` | SFT-016–SFT-024 | Complete | Unreleased | [`docs/tasks/0.2.0.md`](docs/tasks/0.2.0.md) |
| `0.3.0` | SFT-025–SFT-037 | In progress; SFT-025–SFT-029 are Done, SFT-030 is Ready | Unreleased | [`docs/tasks/0.3.0.md`](docs/tasks/0.3.0.md) |
| `0.4.0` | SFT-038–SFT-049 | Planned | Unreleased | [`docs/tasks/0.4.0.md`](docs/tasks/0.4.0.md) |
| `0.5.0` | SFT-050–SFT-058 | Planned | Unreleased | [`docs/tasks/0.5.0.md`](docs/tasks/0.5.0.md) |
| `1.0.0` | SFT-059–SFT-063 | Planned evidence gates | Unreleased | [`docs/tasks/1.0.0.md`](docs/tasks/1.0.0.md) |

**Next implementable task:** SFT-030 — Add source aliases, clear-logs, and
remove-source workflows.

The completed `0.2.0` requirement matrix and browser/security evidence are in
[`docs/release/0.2.0-gate.md`](docs/release/0.2.0-gate.md). Neither completed
implementation gate is a release declaration.

## Planning and dependency rules

- Implement tasks in dependency order. Do not begin a task until every listed
  dependency is `Done`.
- Keep pull requests focused. Tightly coupled tasks may share a pull request
  only when every affected entry is updated and review remains coherent.
- Mark a blocked task with a `Blocker` field naming the missing decision,
  dependency, authority, or external state; remove it when resolved.
- Record commands and measurements only when actually performed.
- Plan accepted roadmap work through `1.0.0`, but do not pull later-milestone
  behavior into an earlier task unless correctness or a hard-to-change schema
  requires it.
- Do not add post-`1.0` candidates until `PRODUCT.md` accepts them into a
  milestone.
- Supported external versions, including Coolify and Fluent Bit, are pinned
  from current official evidence during the implementing task rather than
  guessed in this planning index.

## Task entry template

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
