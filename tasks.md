# Siftail · igris execution plan

<!-- igris:format 2 -->

Runs the 2026-11 hardening and the remaining roadmap through igris, one fresh
Claude Code session per task. **`docs/tasks/*.md` is the authoritative task
record** (scope, acceptance, verification, evidence); this plan mirrors it so
igris can schedule it. Task IDs are the permanent SFT registry (`TASKS.md`).
Every session follows `CLAUDE.md` and `AGENTS.md`.

Findings are in `docs/audit/2026-11-04-full-audit.md`. Phases run in order;
each ends in a gate owned `agent + user` that stops the run for the owner.
Verify is `make check` unless the Verify cell says `ui`
(`make check && make playwright`), `race`, or `none`.

Run: `igris check --strict`, then `igris arise H0`, later
`igris arise H1 --through H3`.

<!-- igris:include tasks/h0.md -->

<!-- igris:include tasks/h1.md -->

<!-- igris:include tasks/h2.md -->

<!-- igris:include tasks/h3.md -->

<!-- igris:include tasks/r5.md -->

<!-- igris:include tasks/r6.md -->

<!-- igris:include tasks/r10.md -->
