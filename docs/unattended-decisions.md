# Decisions taken in unattended runs

Sessions started by igris append here when they decide something the owner
would otherwise have been asked about (`CLAUDE.md` rule 7 and "Decision log").
The owner reviews this file after a run; an entry is a record, not an
approval. Entries are in the order they were taken, newest last.

## SFT-119 — Announcer timing and weights (2026-11-19)

- **Question:** DESIGN.md §26 asks for one announcer "with throttling and
  de-duplication" but gives no interval, no repeat window, and no rule for
  which message wins when several arrive inside one interval (for example a
  truncation notice immediately followed by "Connecting").
- **Options:** queue every message and speak them in order; newest always
  wins; newest wins within a weight class, with notices outranking connection
  states.
- **Chosen:** at most one message per 1 s, identical text dropped within
  10 s, and the newest queued message replaces an older one of equal or lower
  weight; "notice" (pause, resume, truncation, caps, flashes, backup state)
  outranks "status" (connection state). A queue would replay stale bursts; a
  plain newest-wins rule let a reconnect silence the truncation it followed.
- **Reversal cost:** three constants and the weight table at the top of
  `window.siftailAnnounce` in `internal/web/ui/assets/app.js`.

## SFT-119 — History keeps its server-swapped status region (2026-11-19)

- **Question:** DESIGN.md §26 says one announcer serves every page, but
  History's result announcements arrive as server-rendered out-of-band swaps
  into `#history-update-status`, and the task's acceptance names only Live,
  flashes, and Backup.
- **Options:** move History's announcements onto the shell announcer (server
  fragments and client trim messages); leave History's region as is.
- **Chosen:** left as is. Its messages are already one per swap, the
  change would touch the History fragment contract and its tests, and the
  task scope is Live, flash, and Backup. History pages therefore have two
  polite regions (shell announcer and `#history-update-status`).
- **Reversal cost:** replace the OOB `#history-update-status` paragraphs in
  `history.html` with a data attribute read by `app.js`, route
  `announceHistory` through `window.siftailAnnounce`, and update the
  History Playwright assertions that read `#history-update-status`.
