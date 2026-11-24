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

## SFT-122 — The System menu becomes the single mobile menu below 40rem (2026-11-19)

- **Question:** §6.2 asks for "a single menu containing all destinations" at narrow widths but names neither the control nor the width; the header already has a native `<details>` System menu.
- **Options:** (a) a second, separate menu button for the primary items; (b) reuse the System menu: it also carries Logs/Sources/Servers (shown only when collapsed), its summary reads "Menu" with the §31 `menu` icon, and the bar's primary nav is hidden. Breakpoints considered: 52rem (where the brand name already hides), 40rem, 32rem.
- **Chosen:** (b) at 40rem. One menu, no JavaScript, no new component; display:none keeps exactly one "Primary" landmark in the accessibility tree at any width and the palette skips the in-menu copy. 40rem is where the three primary items plus a worded health label stop fitting; Playwright asserts no header truncation at 1280, 660, and 390 px.
- **Reversal cost:** low — the `data-menu-primary` nav and two summary spans in `topbar.html`, one media query in `app.css`, the palette selector, and the header-menu Playwright test.

## SFT-122 — Copy pass reaches Go and JavaScript strings, including CLI-visible diagnostics (2026-11-19)

- **Question:** The acceptance says "a copy pass over every template", but much of what the operator reads comes from handlers (flash notices, server errors, guided-test results, backup state, Status warnings), from `live.js`/`tokens.js`/`palette.js`, and from `internal/status` diagnostic summaries that `siftail diagnostics` also prints.
- **Options:** templates only; templates plus every string that renders in the interface.
- **Chosen:** templates plus the interface strings, since leaving "committed", "bounded", or "Server" in notices on the same pages would fail §36.2/§36.3. Five diagnostic summaries in `internal/status/state.go` changed wording ("bounded database check" → "database check", "verified artifact" → "verified file", …), which also changes the CLI `diagnostics` text; categories, codes, and JSON fields are unchanged. README, `docs/operations/export-formats.md`, and the `DESIGN.md` copy tables quote the new labels.
- **Reversal cost:** low — string literals and the tests that quote them.

## SFT-122 — Logs tabs titled by mode; detail pages titled by entity (2026-11-19)

- **Question:** With the eyebrow gone (D15), History and Live both render `h1` "Logs"; §6.4 fixes detail-page titles to the entity but says nothing about the Logs `<title>`, and the token page has no entity name of its own.
- **Options:** keep `<title>` "Logs" for both modes; title them "History" and "Live"; for the token page, title it "Copy this token now" or after the server.
- **Chosen:** "History · Siftail" and "Live · Siftail", so two tabs are told apart while the `h1` stays the destination name; the token page uses `h1` = server name and `<title>` "New token for <server>". The full table is in `DESIGN.md` §6.4.
- **Reversal cost:** low — the `PageMeta` switch in `ui.go` and the title assertions in `copy_test.go` and Playwright.

## SFT-124 — Compared screenshots pin fonts and bypass CSP for that one test (2026-11-19)

- **Question:** The acceptance asks for screenshot comparison against committed references, but proportional text uses the host's `system-ui` face (Fedora on the maintainer's machine, Ubuntu on CI), so pixel references would differ by host. Playwright's `stylePath`, caret hiding, and animation freezing are injected as a `<style>` element, which Siftail's `style-src 'self'` CSP blocks silently.
- **Options:** (a) compare as shipped and accept per-host references; (b) pin `--font-sans` to the vendored JetBrains Mono through `stylePath` and run only the comparison test with `bypassCSP`; (c) compare only element regions with no proportional text.
- **Chosen:** (b). References cover login, palette, and help (the data- and clock-independent views) at 1280 and 390 px in both themes, `maxDiffPixelRatio` 0.01, stored as `tests/browser/__screenshots__/<name>-chromium-linux.png`, refreshed with `make playwright-update`. Every other test still runs under the shipped CSP, and the console-clean test still asserts no CSP violation.
- **Reversal cost:** low — `screenshot.css`, the `toHaveScreenshot` block in `playwright.config.ts`, and the twelve PNGs.

## SFT-124 — Fix the 320 px reflow failure the new test found (2026-11-19)

- **Question:** The new 320 px reflow test failed on real pages: a server or source name with no break opportunity (for example the hostile alias another test sets) widened a source detail page by up to 1,148 px through its `h1`, the key subtitle, definition values, and the typed-confirmation forms. The task's impact is "test code and reference screenshots", and no H3 task owns this defect.
- **Options:** mark the check `test.fail` and leave the bug for a later task; fix it here.
- **Chosen:** fix it with one `overflow-wrap: anywhere` rule in `app.css` for `h1`–`h3`, `dd`, `.catalog-heading p`, `.source-action-form`, and `.inline-confirmation`. `DESIGN.md` §33 targets WCAG 2.2 AA, which includes 1.4.10 Reflow, and §34.3 allows horizontal scrolling only inside raw payload regions, never the page and a known-failing test would hide the regression it exists to catch. Scrolling tables keep their own scroll region and are unchanged.
- **Reversal cost:** low — one CSS rule.

## SFT-124 — Live reconnect test asserts native reconnects, not the Last-Event-ID header (2026-11-19)

- **Question:** The SSE drop test scripts the stream with `page.route`; Chromium reconnects natively after each drop but does not attach `Last-Event-ID` to a request Playwright fulfils, so the header cannot be asserted in the browser.
- **Options:** drive drops through the real server (no way to sever one stream from outside without a test-only endpoint); assert reconnect count in the browser and rely on the Go test for the server's `possible_gap` reply to `Last-Event-ID`.
- **Chosen:** the second: the browser test proves three native reconnects, each control event's effect, no retry after `shutdown` or a 401, and recovery against the real server; `internal/auth/live_test.go` already proves the server sends `possible_gap` for a valid `Last-Event-ID`.
- **Reversal cost:** low — add a test-only stream-sever hook later if end-to-end header proof is wanted.

## SFT-126 — The gate records the interface defects it found instead of fixing them (2026-11-19)

- **Question:** The gate found six interface deviations (`docs/release/h3-gate.md` findings 1–6): the unbuilt ADR 0011 D1 layout (first History row at 690 px against the 280 px target), Live controls in their own toolbar with Reconnect always shown (§11.2), row names without the zone (§32.3), 12-hour native date-time fields, mixed time formats in event detail, and the hidden skip link's shadow at the top-left corner. The H1 and H2 gates fixed small defects in-task; this task's acceptance says "defects become follow-up tasks" after the owner's review.
- **Options:** (a) fix the small ones here (skip-link shadow, Reconnect visibility, row-name zone) and record the rest; (b) fix none, measure and record all of them, and propose follow-up tasks.
- **Chosen:** (b). The acceptance names the owner's review as where defects turn into tasks; each fix changes behaviour that the screenshots and axe matrix in this gate are meant to certify, so fixing here would certify a tree the owner never saw reviewed; and D1, the largest, needs a task of its own anyway. The 280 px target is measured and written to the axe JSON on every run, not asserted, so the suite stays green until D1 is owned.
- **Reversal cost:** none; each finding is a small follow-up (the skip-link, Reconnect, and row-name fixes are one rule or a few lines each), D1 is a new task touching `history.html`, `live.html`, the filter CSS and the filter Playwright tests.

## SFT-126 — Before/after screenshots committed as JPEG with their capture script (2026-11-19)

- **Question:** The acceptance asks for before/after screenshots in `docs/release/h3-gate.md` but not for which screens, format, or how "before" is produced. The full gate matrix (4 theme/density combinations × 2 widths × 18 surfaces = 144 PNGs) is about 11 MB.
- **Options:** commit the whole PNG matrix; commit a curated set; leave all screenshots in the git-ignored `.playwright-artifacts`.
- **Chosen:** a curated set of 11 screens per build (22 JPEG files, quality 82, 1.4 MB) under `docs/release/h3-gate/`, captured by one script (`docs/release/h3-gate/capture.mjs`) against a build of `3e4f499` (the H2 gate, before SFT-112) and the current tree with identical fixtures, so the pairs differ only by the build. The full matrix stays reproducible with `make playwright` under `.playwright-artifacts/h3-gate/`.
- **Reversal cost:** low — delete the directory, or rerun the script with other screens.

## SFT-052 — Coolify drain template listens on 0.0.0.0 inside its container (2026-11-20)

- **Question:** SFT-109 (audit SEC-05) set `Listen 127.0.0.1` on the Forward input of both generated templates. Coolify `v4.1.1`'s `StartLogDrain.php` runs `coolify-log-drain` on Docker's default bridge network with `ports: 127.0.0.1:24224:24224`; Docker delivers a published port to the container's bridge address, not its loopback, so the Coolify template would accept no deliveries, and Coolify's `fluentd-async: true` makes that failure silent. The audit premise ("Coolify targets 127.0.0.1:24224") is true for the host, not for the container. The owner delegated the call ("you decide whats best").
- **Options:** (a) keep loopback and wait for the Coolify run to confirm; (b) restore `0.0.0.0` for the Coolify template only, keep the generic template on loopback; (c) split it into a separate task before SFT-052 closes.
- **Chosen:** (b). Without it the guide's ingestion proof cannot pass, and the guide's acceptance requires matching tested behaviour. The boundary becomes Coolify's host-side `127.0.0.1` publication; Docker's Fluentd driver cannot send `Shared_Key`, so no sender authentication is possible in this shape. Residual exposure, documented: containers on Docker's default `bridge` network can reach the drain container's address. The owner's Coolify run is the confirming evidence.
- **Reversal cost:** low — revert `internal/ingest/guide.go`'s Coolify `[INPUT]`, the Coolify fixture, the two test assertions, the `servers.html` notice, ARCHITECTURE §28.3 and the compatibility doc's "Forward input binding" section.

## SFT-052 — Validation runs on locally built images, not a published tag (2026-11-20)

- **Question:** `ghcr.io/drilonrecica/siftail:0.5.0` is not published, and publishing is the owner's release decision (SFT-057/058); the guide still needs an image for the Coolify run and two distinct tags for the upgrade step.
- **Options:** (a) build `siftail:sft-052a` and `siftail:sft-052b` on the Coolify server from `master` and select them with `COMPOSE_SIFTAIL_IMAGE`; (b) push private pre-release tags to a registry the owner controls.
- **Chosen:** (a), with (b) as fallback if Coolify's deploy step insists on pulling. Nothing is published, and the guide documents the released image path unchanged; the image source is recorded as a deviation in the validation record.
- **Reversal cost:** none — rerun the upgrade step against published tags after release.
