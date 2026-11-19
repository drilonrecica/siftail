# H3 gate: interface, accessibility, and design

**Completed:** 2026-11-19
**Scope:** SFT-112 through SFT-125 (H3), checked by SFT-126
**Release action:** None. No tag, image, or publication.

This is the self-check gate for phase H3. It records the automated
accessibility matrix, the keyboard-only pass, the screenshot refresh,
before/after screenshots, the binary size delta, and the defects the gate
found. Every figure below was measured in this session on the tree at
`798afe5` plus this task's working changes. Nothing is carried over from the
implementing tasks' own evidence. The owner's review of the running
interface comes next (see [For the owner's review](#for-the-owners-review)).

## Verdict

**Pass, with one known gap and five small defects for the owner.** axe
(WCAG 2.0/2.1/2.2 A and AA) reports **0 violations in 144 runs**: 18
surfaces, in dark and light, Compact and Comfortable, at 1280 and 390 px.
None of those surfaces scrolls sideways. The keyboard-only pass gets from
sign-in through filtering, row inspection, both modes, the palette, the
System menu and keyboard help without using a pointer. `make playwright`
passes 61 of 61, and so does `make playwright-update`, which changed none of
the 12 reference screenshots. `GOFLAGS=-count=1 make check` passes.

The known gap is ADR 0011 D1. No H3 task built its layout (SFT-121 logged
this in `docs/unattended-decisions.md`). At 1280×800 in Compact density the
first History row starts at **690 px**, against the D1 target of 280 px; it
was 751 px before H3. The five small defects are findings 2–6. None of them
blocks keyboard or screen-reader use, and the gate recorded them without
fixing any (decision logged).

## Environment

Fedora Linux 44, Linux 7.2.15 x86-64, four logical CPUs, Go 1.27.3, Node.js
24.21.0, Playwright 1.64.1 with its bundled Chromium, `@axe-core/playwright`
4.14.0 (axe-core 4.14.0). The browser suite runs one worker against a real
`siftail serve` built by `tests/browser/global-setup.ts`.

## Results

| Command | Result | Duration |
|---|---|---|
| `GOFLAGS=-count=1 make check` (fmt-check, vet, test, build, compose-check) | pass, 16 packages ok, no cached results | not timed |
| `make playwright-update` (screenshot refresh) | 61 passed; `git status` shows no change under `tests/browser/__screenshots__` | 6.1 min |
| `make playwright` (final run, after the refresh) | 61 passed, 0 flaky | 6.0 min |
| `npx playwright test -g "H3 gate"` (during development) | 5 passed | 3.2 min |

The suite grew from 56 to 61 tests. The new tests are in
`tests/browser/siftail.spec.ts`:

- `H3 gate: axe on every page and overlay, <theme>, <density>, 1280 and 390 px`
  is generated once for each of the four theme and density combinations.
- `H3 gate: a keyboard-only investigation reaches every workspace action`.

Every run writes its evidence to `.playwright-artifacts/h3-gate/` (git-ignored):
`axe-<theme>-<density>.json` holds every surface checked, every violation, and
the first-row offsets; there are 144 viewport PNGs, one per surface and
combination; `keyboard-pass.json` holds the keyboard steps, plus three
keyboard screenshots.

## Accessibility matrix (DESIGN.md §33.6)

For each combination the test sets the preference cookie
(`<theme>.<density>.on.UTC`) directly. That way the sign-in page is covered as
well; the System menu path that writes the cookie has its own tests. The
emulated colour scheme matches the theme. Before running axe, each check
asserts that the page really renders that theme and density, and that it
does not scroll sideways.

| Surface (each at 1280×800 and 390×844) | dark compact | light compact | dark comfortable | light comfortable |
|---|---|---|---|---|
| Sign-in | 0 | 0 | 0 | 0 |
| History | 0 | 0 | 0 | 0 |
| History with a row expanded (event detail) | 0 | 0 | 0 | 0 |
| System menu open | 0 | 0 | 0 | 0 |
| Export dialog | 0 | 0 | 0 | 0 |
| Command palette | 0 | 0 | 0 | 0 |
| Keyboard help | 0 | 0 | 0 | 0 |
| Sources, source detail | 0 | 0 | 0 | 0 |
| Servers, server detail | 0 | 0 | 0 | 0 |
| Settings, Status, Audit, Backup | 0 | 0 | 0 | 0 |
| Live empty, Live with rows, Live paused | 0 | 0 | 0 | 0 |

Each cell is the axe violation count with tags `wcag2a`, `wcag2aa`,
`wcag21a`, `wcag21aa`, `wcag22aa`. The test fails on any violation, so the
zeros are enforced on every future run. `/logs/events/{id}` is not a page: it
is the htmx fragment behind row expansion and is covered by the expanded-row
surface. Earlier tests already cover the one-time token page, the degraded
banner, and reduced motion with axe or computed styles; this gate did not
repeat them.

The manual checks that §33.6 lists (screen-reader smoke, 200% zoom on a real
device, 390 px on a phone) are SFT-127 and SFT-128, both owned by the owner.

## Keyboard-only pass

There is no pointer input after `page.goto("/login")`; the test asserts each
step. The record below is `keyboard-pass.json` from the final run.

| # | Step | Keys | Focus after the step |
|---|---|---|---|
| 1 | Sign-in opens with the username focused | — | `#username` |
| 2 | Sign in; History shows 200 rows | type, Tab, type, Enter | body (new page) |
| 3 | Skip link moves into main | Tab, Enter, Tab | History mode link |
| 4 | Message search; Enter applies it (1 row) | F, type, Enter | `#contains-filter` (kept) |
| 5 | Tab reaches the only row; Enter expands it | Tab ×8, Enter | the event-detail section |
| 6 | Back to the row; Enter collapses | Shift+Tab, Enter | "Row 1 of 1: ERR, api/web, …" |
| 7 | Clear the search; move the selection | F, Ctrl+A, Backspace, Enter, Tab…, J ×2, K | "Row 2 of 200: WRN, …" |
| 8 | Arrows move too; Esc clears the selection | ↓, Esc | body; the tab stop is back on the newest row |
| 9 | Switch to Live | L | body (new page) |
| 10 | Pause away from controls | Space | status reads "Paused" |
| 11 | Resume; J/K and Enter on Live rows | Space, J, K, Enter | Live event-detail section |
| 12 | Back to History | Esc, H | body (new page) |
| 13 | Command palette | Ctrl+K, type "Status" | palette combobox |
| 14 | Activate the result | Enter | `/status` |
| 15 | System menu | Tab…, Enter | System summary, menu open |
| 16 | Settings from the menu | Tab…, Enter | `/settings` |
| 17 | Keyboard help | ? | the dialog's Close button |
| 18 | Close help | Esc | dialog hidden |

In steps 4–6 the hostile fixture message (`<script>window.siftailXSS=true</script>`)
stays text: the row name shows it literally and `window.siftailXSS` stays
undefined. What the pass shows about the interaction design:

- After the search is applied (step 4), focus stays in the search field, as
  §32.1 requires, so J types into the field. Reaching the results takes eight
  Tab presses past the remaining filter controls. This follows the
  specification. D1's two-row bar would shorten the trip; a "results" skip
  target is a possible follow-up.
- Expanding a row moves focus into its detail (step 5, SFT-114). To collapse
  it from the keyboard, either Shift+Tab back to the row or Tab to the
  detail's **Collapse details** button.

## Before and after

`docs/release/h3-gate/capture.mjs` captured both sets. It ran against a build
of `3e4f499` (the H2 gate, the last commit before SFT-112) and the current
tree, with the same fixtures, administrator, Compact density, and viewport,
so each pair differs only by build. JPEG, quality 82.

| Screen | Before (pre-H3) | After (this gate) |
|---|---|---|
| History, 1280, dark | [before](h3-gate/before/history-desktop-dark.jpg) | [after](h3-gate/after/history-desktop-dark.jpg) |
| History, 1280, light | [before](h3-gate/before/history-desktop-light.jpg) | [after](h3-gate/after/history-desktop-light.jpg) |
| History with a multi-line ERR expanded, 1280, dark | [before](h3-gate/before/history-expanded-desktop-dark.jpg) | [after](h3-gate/after/history-expanded-desktop-dark.jpg) |
| Live with rows, 1280, dark | [before](h3-gate/before/live-desktop-dark.jpg) | [after](h3-gate/after/live-desktop-dark.jpg) |
| Palette searching "bill", 1280, light | [before](h3-gate/before/palette-desktop-light.jpg) | [after](h3-gate/after/palette-desktop-light.jpg) |
| Sources, 1280, light | [before](h3-gate/before/sources-desktop-light.jpg) | [after](h3-gate/after/sources-desktop-light.jpg) |
| Status, 1280, dark | [before](h3-gate/before/status-desktop-dark.jpg) | [after](h3-gate/after/status-desktop-dark.jpg) |
| Sign-in, 1280, dark | [before](h3-gate/before/login-desktop-dark.jpg) | [after](h3-gate/after/login-desktop-dark.jpg) |
| History, 390, dark | [before](h3-gate/before/history-mobile-dark.jpg) | [after](h3-gate/after/history-mobile-dark.jpg) |
| History, 390, light | [before](h3-gate/before/history-mobile-light.jpg) | [after](h3-gate/after/history-mobile-light.jpg) |
| Settings, 390, light | [before](h3-gate/before/settings-mobile-light.jpg) | [after](h3-gate/after/settings-mobile-light.jpg) |

Visible differences between the pairs:

- **Severity:** rows carry a severity edge, and FTL shows as a filled chip
  (SFT-121).
- **Times:** rows show `02:40:58.880` instead of `03:40:44.940 AM`. The tail
  line opens with "Times in UTC" (SFT-115).
- **Header:** the palette hint reads `Ctrl K` on Linux instead of `⌘K`
  (SFT-113). At 390 px the header becomes brand, health, palette, and one
  **Menu** (SFT-122); the "Serve…" item truncated before.
- **Copy:** the eyebrow headings are gone, "All retained sources" is now "All
  sources", "Export matching History" is now "Export results" (SFT-122), and
  Wrap and `+N lines` are new (SFT-121).
- **Live:** it uses the same cascade filter bar as History (SFT-116), but its
  controls still sit in their own row (finding 2).

To capture again (the paths are examples; the script needs a short working
path for the control socket):

```bash
git archive 3e4f499 | tar -x -C "$WORK/before"
(cd "$WORK/before" && go build -o "$WORK/siftail-before" ./cmd/siftail)
go build -o "$WORK/siftail-after" ./cmd/siftail
node docs/release/h3-gate/capture.mjs "$PWD" "$WORK/siftail-before" "$WORK/shots/before" "" 19180 19181
node docs/release/h3-gate/capture.mjs "$PWD" "$WORK/siftail-after" "$WORK/shots/after" ".on.UTC" 19280 19281
```

## First-row offset (ADR 0011 D1)

The table gives the distance from the top of the document to the first log
row, with the filter bar open (the desktop default) and the bar's disclosure
closed at 390 px. The pre-H3 and SFT-121 figures come from `capture.mjs` with
its fixtures. The current figures come from the gate test, whose fixtures
differ but whose layout above the first row is the same; `capture.mjs`
measured 690 and 557 px on the current build too.

| Build | History 1280 Compact | Live 1280 Compact | History 390 Compact | History 1280 Comfortable | Live 1280 Comfortable |
|---|---|---|---|---|---|
| `3e4f499` (before H3) | 751 px | 523 px | 442 px | — | — |
| `1d1401c` (SFT-121) | 712 px | 579 px | 446 px | — | — |
| current tree | **690 px** | **557 px** | 424 px | 767 px | 622 px |
| D1 target | ≤ 280 px | — | — | — | — |

Live went up by 34 px during H3, because SFT-116 gave it the full source
cascade. The target is recorded on every run in `axe-*.json` but not
asserted; see finding 1.

## Binary size

The release binary is built the way the Dockerfile builds it (`-trimpath
-buildvcs=false -ldflags="-s -w -buildid="`). The plain build is `go build
./cmd/siftail`. Both are linux/amd64 with Go 1.27.3.

| Build | Release flags | Plain build |
|---|---|---|
| `3e4f499` (before H3) | 13,513,384 B | 20,892,264 B |
| `1d1401c` (SFT-121) | 13,640,392 B | 21,032,440 B |
| current tree | 13,754,184 B | 21,192,552 B |
| **Delta since SFT-121** | **+113,792 B (+0.83%)** | +160,112 B (+0.76%) |
| Delta over all of H3 | +240,800 B (+1.78%) | +300,288 B (+1.44%) |

The embedded assets went from 236,981 B to 238,058 B (+1,077 B) since
SFT-121, and the templates from 83,263 B to 82,341 B. Most of the growth is
Go code: SFT-123's hashing, ETag and gzip delivery, and SFT-122's per-entity
titles and copy.

## Findings

1. **Medium, known, needs a task. The D1 layout is not built.** ADR 0011 D1
   specifies one filter bar of at most two rows, with no fieldset boxes, and
   the Live controls on the tail line. Its consequences assign "the layout of
   D1" to SFT-121, whose acceptance left it out. SFT-121 logged that no H3
   task owns it. The first History row is at 690 px, against the 280 px
   target. At 390 px the filters collapse, so mobile is not affected. The
   follow-up would be a task rebuilding `history.html` and `live.html` filter
   forms, the filter CSS, and the filter Playwright selectors. This gate's
   matrix and Tab-order tests are its regression net.
2. **Low. Live controls sit in a toolbar row and Reconnect is always shown.**
   DESIGN §10.7 and §11.2 say Pause, Jump to newest, and Clear view sit on the
   Live tail line, and "Reconnect, shown only in the Disconnected state". In
   `live.html` the `.live-toolbar` row holds all four buttons, and `live.js`
   never hides `[data-live-reconnect]`, so Reconnect appears while Live is
   connected (see `after/live-desktop-dark.jpg`). Pressing it while connected
   is harmless. Moving the controls belongs with finding 1; hiding Reconnect
   is a few lines in `live.js` and can be done separately.
3. **Low. Row accessible names omit the zone.** §32.3's example name ends
   "…, 20:41:07.283 UTC, stderr". `rowSummary` in `keyboard.js` uses the
   `<time>` text, which has no zone, so the name is "…, 02:40:08.395,
   stdout". The list's own name ("… log events, times in UTC") still states
   the zone, so a screen-reader user hears it when entering the list. The fix
   is either to append the zone in `rowSummary` or to amend the example.
4. **Low. The native date-time fields show a 12-hour clock.** The custom
   range's `datetime-local` inputs render as "11/19/2026, 01:43:03 AM" in an
   en-US Chromium. §12.2 says times are always 24-hour. The control's display
   follows the browser locale and cannot be set from the page. The labels
   carry the zone, and the submitted value is 24-hour. The options are to
   accept this as a stated limitation in §12.2 or to replace the native
   control with text inputs in a fixed format.
5. **Low. Event detail mixes time formats.** Under UTC, the Timing block
   shows "Event time (UTC) 2026-11-19T02:46:09.511Z" (raw RFC 3339) next to
   "Received time (UTC) 19 Nov 2026 02:48:29.520693" (see
   `after/history-expanded-desktop-dark.jpg`). §12.2 asks for fixed formats.
   This looks like the UTC line and the display-zone line being merged when
   the display zone is UTC.
6. **Cosmetic, from before H3. The hidden skip link's shadow shows in the
   top-left corner.** `.skip-link` is moved out with `translateY(-150%)` from
   `top: var(--space-2)`, but its `--shadow-overlay` still reaches into the
   viewport. In the light theme this shows as a grey smudge behind the brand
   mark at 390 px (`after/history-mobile-light.jpg`, also in the before set).
   The fix is to drop the shadow until `:focus`, or to move the link fully out
   of view. A shadow does not affect contrast, which is why axe passes.

There were no other observations. The sticky header is 96% opaque by design
(`color-mix(… 96%, transparent)`), so scrolled content shows through it
faintly; this is intended.

## For the owner's review

The acceptance ends with the owner reviewing the running interface after the
unattended run. Suggested order:

1. Open the before/after pairs above.
2. Run `make playwright`, then browse `.playwright-artifacts/h3-gate/` for
   any combination (for example `history-390-light-comfortable.png`).
3. Decide on findings 1–6. The gate proposes these follow-up tasks:
   - D1 layout (findings 1 and 2);
   - Reconnect visibility and the row-name zone (findings 2 and 3, small);
   - the date-time field decision and the detail time format (findings 4
     and 5);
   - the skip-link shadow (finding 6, one rule).
4. Then run SFT-127 (screen reader) and SFT-128 (on-device 390 px), which
   cover the manual half of §33.6.

## Cumulative verification commands

```bash
GOFLAGS=-count=1 make check
make playwright-update          # refresh; no reference changed
make playwright                 # 61 passed
npx playwright test -g "H3 gate"
# Binary sizes (release flags as in the Dockerfile):
go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o "$WORK/rel" ./cmd/siftail
```
