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

## SFT-121 — Implement the listed signature items, not the D1 filter-bar layout (2026-11-19)

- **Question:** ADR 0011's Consequences say SFT-121 implements "D3, D4, D8–D10, D12, D13, and the layout of D1", but the task's row in `tasks/h3.md` and its acceptance in `docs/tasks/0.5.0-hardening.md` list only the signature items (severity edge, sprite, `+N lines`, selection, fatal weight, type scale, font subset, Wrap, match highlight) and say nothing about the single-row filter bar or moving the Live controls onto the tail line.
- **Options:** (a) also rebuild both filter forms into D1's single-row bar and move Pause/Jump/Clear/Reconnect onto the Live tail line; (b) implement the acceptance list only and place the Wrap toggle where §12.4 says (end of the History result summary, on the Live tail line), leaving the bar and the Live toolbar as they are.
- **Chosen:** (b). The acceptance list is the task's contract, D1 is flagged in the ADR as the most expensive decision to reverse, and the rebuild would rewrite both filter templates and many Playwright selectors inside a task already carrying nine items. **No remaining H3 task owns D1's layout**, so SFT-126's 280 px first-row target cannot be met as things stand (at 390 px the Live viewport is still all filters); the owner should add a task for it or assign it to an existing one.
- **Reversal cost:** none to undo; doing D1 later is a new task touching `history.html`, `live.html`, the filter CSS, and the filter Playwright tests.

## SFT-121 — Count `+N lines` over the full stored message in SQL (2026-11-19)

- **Question:** §12.4 says the server supplies the count of stored lines, but History rows only load a 2,048-character preview, and the specification does not say whether N counts all lines or those after the first, nor how trailing breaks count.
- **Options:** count in Go over the preview (cheap, undercounts long messages); count in SQL over the complete `message_text` (exact, one more pass over each row's text).
- **Chosen:** SQL over the complete message, N = line breaks after trimming trailing `\r`/`\n` (lines after the first); Live computes the same in Go over the full message. `BenchmarkHistoryStore100K` (TMPDIR on btrfs, SATA SSD): a 200-row page went from about 1.0–1.4 ms to about 1.4 ms warm; the literal search case is unchanged (~116 ms). Messages are capped at 1 MiB, so the extra pass is bounded.
- **Reversal cost:** low — one select expression in `history_store.go` and its test.

## SFT-121 — Ship four static font files although they are larger than the variable pair (2026-11-19)

- **Question:** ADR 0011 D13 chose static 400/700 instances, reasoning they are smaller than the variable file. Instancing the vendored variable subsets (fontTools `instancer`, no new download) gives four files of 57,932 bytes against 43,056 bytes for the two variable files (+14,876 bytes), so that premise does not hold.
- **Options:** keep the variable files; ship the four static files as specified.
- **Chosen:** the four static files: §28.1 and D13 require them, they remove the weights the specification forbids (the variable axis allowed 400–800), and FTL's 700 is a real instance, not a synthesised weight. Only the 400 latin file is preloaded, so a page that shows no FTL code never fetches the 700 files. Binary: 20,998,528 → 21,028,216 bytes (+29,688, including the CSS, templates, and code of this task).
- **Reversal cost:** low — restore the two variable files and their `@font-face` rules and the asset map entries.
