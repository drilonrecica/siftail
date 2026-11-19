# Siftail: rules for every session

**Read `AGENTS.md` first and follow it.** It is the mandatory rule set for this
repository; `DOMAIN.md`, `PRODUCT.md`, `ARCHITECTURE.md`, `DESIGN.md`, and the
accepted ADRs under `docs/decisions/` are authoritative. Nothing below overrides
them.

## When igris started this session

The repository's work runs through igris (`tasks.md`, one fresh session per
task). For a task `SFT-NNN`:

1. Read your row in the plan file, then the task's entry in the `docs/tasks/`
   record it names (`docs/tasks/0.5.0-hardening.md` for SFT-082–SFT-128; the
   milestone file for older IDs), then every audit finding and specification
   section the entry references. The audit is
   `docs/audit/2026-11-04-full-audit.md`.
2. Implement exactly that task to its acceptance criteria. Do not start other
   tasks; mention adjacent problems in your final message instead.
3. Update the task's entry in the `docs/tasks/` record as part of the task:
   `**Status:** Done`, `**Issue/PR:** — (direct maintainer implementation via
   igris)`, `**Completed:** <date>`, and `**Completion evidence:**` with the
   commands actually run and their actual results (`AGENTS.md` §31). Never
   claim a check you did not run. Do not edit `TASKS.md` dashboard rows unless
   the task says so.
4. **Never edit the Status column of `tasks.md` or `tasks/*.md`**; igris owns
   it. Never commit; igris commits after verify. Never add AI attribution,
   `Co-Authored-By`, or session trailers anywhere. If you delete or rename a
   file, grep `tasks/*.md` for its path and update every Context cell that
   names it in the same task; igris refuses to record a task whose plan names
   a path that no longer exists, and that stops the whole unattended run.
5. Keep `make check` green (fmt-check, vet, test, build, compose-check). Tasks
   whose Verify cell says `ui` must also pass `make playwright`.
6. UI tasks (phase H3) load the `frontend-design` skill, follow `DESIGN.md` as
   amended by ADR 0011 and SFT-112, and check their own work with Playwright
   screenshots at desktop and 390 px in both themes.
7. Decisions inside the task's scope do not need the owner. The owner has
   delegated them for unattended runs: when a design or implementation choice,
   an ambiguous or seemingly wrong specification, or a trade-off between
   acceptable options comes up, pick the option that best fits the
   authoritative documents, carry on, and record it in
   `docs/unattended-decisions.md` (see "Decision log" below). Do not ask and
   wait for these.

   Still ask the owner in this chat and wait, without working around it, when
   the task needs something only the owner can supply or approve: an action on
   the owner's machines or accounts (Coolify, a phone, a screen reader),
   credentials, evidence the owner must produce, an approval a task row
   assigns to the owner (`agent + user` and `user` rows), tagging or
   publishing a release, anything outside this repository, or a change that
   contradicts an accepted ADR. Never invent owner evidence or approval.
8. Finish with `igris done SFT-NNN --note "<one line>"` as your very last
   action, only when acceptance holds and nothing waits on the owner.

## Decision log

Every decision taken under rule 7 without the owner is appended to
`docs/unattended-decisions.md` in the same task, newest last, as one entry:

```markdown
## SFT-NNN — <short title of the decision> (<date>)

- **Question:** what had to be decided, and why the documents did not settle it.
- **Options:** the ones seriously considered.
- **Chosen:** the option taken, and the reason.
- **Reversal cost:** what the owner would have to undo to choose differently.
```

Log decisions the owner would plausibly have wanted a say in; routine
implementation detail does not belong there. Mention the entries in the
`igris done` note ("N decisions logged").

## Unattended runs

Phases run in yolo mode with nobody watching. The rules in `AGENTS.md` are the
only guard: no `sudo`, no package installs, no network beyond the official
sources a task names (Go module proxy, GitHub Actions SHAs, htmx release,
Google Fonts for vendoring), no writes outside this repository except
`$TMPDIR`, no `git` history rewriting. Benchmarks must run with `TMPDIR` on a
real disk, never tmpfs (`AGENTS.md` §17.2).
