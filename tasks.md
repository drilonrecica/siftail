# Siftail · igris execution plan

<!-- igris:format 2 -->

Runs the 2026-11 hardening and the remaining roadmap through igris, one fresh
Claude Code session per task. **`docs/tasks/*.md` is the authoritative task
record** (scope, acceptance, verification, evidence); this plan mirrors it so
igris can schedule it. Task IDs are the permanent SFT registry (`TASKS.md`).
Every session follows `CLAUDE.md` and `AGENTS.md`.

Findings are in `docs/audit/2026-11-04-full-audit.md`. Phases H0–H3 run in
order unattended; each ends in a self-check gate that writes
`docs/release/h<N>-gate.md` for the owner's review afterwards. R5 is where the
owner is needed (Coolify validation, screen-reader and mobile passes).
Verify is `make check` unless the Verify cell says `ui`
(`make check && make playwright`), `race`, or `none`.

Run: `igris check --strict`, then `igris arise H0 --through R10` for the
unattended run; it stops when a task needs the owner.

<!-- igris:include tasks/h0.md -->

<!-- igris:include tasks/h1.md -->

<!-- igris:include tasks/h2.md -->

<!-- igris:include tasks/h3.md -->

<!-- igris:include tasks/r5.md -->

<!-- igris:include tasks/r6.md -->

<!-- igris:include tasks/r10.md -->
