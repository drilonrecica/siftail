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

## SFT-120 — Raise `--text-muted` and darken light error/success instead of banning muted text in rows (2026-11-19)

- **Question:** `--text-muted` failed 4.5:1 on the hover and selected row surfaces (3.79/4.48 on selected), and light `--status-error` and `--status-success` failed on selected (4.42, 4.26). DESIGN.md §12.5 moves source and stream to `--text-secondary`, but §12.3 still assigns `--text-muted` to the TRC and DBG codes and the timestamps sit in the same rows, so the specification did not settle whether to change the tokens or every muted use inside rows.
- **Options:** (a) keep the tokens and move every muted use inside rows (time, TRC, DBG) to `--text-secondary`, flattening the row hierarchy; (b) lighten dark muted to `#8E99AB` and darken light muted to `#596273` so muted passes on all five surfaces, and darken light error to `#B3352C` and light success to `#1A7041`; still move source and stream to secondary per §12.5.
- **Chosen:** (b). It fixes every surface at once, keeps timestamps and quiet levels visibly quieter than metadata, and the hue shifts are small (each value moves a few percent in lightness). The contrast test locks all five surfaces in.
- **Reversal cost:** low — three token values in `tokens.css` and the §27.3 table; the contrast test would need the hover/selected pairs for muted dropped and the row CSS would need muted removed.

## SFT-120 — Leave dark `--status-fatal` and `--mark-bg` to SFT-121 (2026-11-19)

- **Question:** §27.3 (amended by ADR 0011) already lists dark fatal `#FF6B63` and the new `--mark-bg`, but both belong to the fatal signature and match highlight that SFT-121 builds; adding them here would change fatal's look without the chip and add an unused token.
- **Options:** apply both values now; leave them to SFT-121.
- **Chosen:** leave them to SFT-121, which owns the components that use them; §27.3 says so. The contrast test already covers `--status-fatal` and will check the new value when it lands (`#FF6B63` is 6.73:1 on base, 4.69:1 on selected, and `--text-inverse` on it is 6.83:1).
- **Reversal cost:** none — two token lines whenever wanted.
