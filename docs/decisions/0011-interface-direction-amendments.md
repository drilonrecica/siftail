# ADR 0011: Interface Direction Amendments

**Status:** Accepted

**Date:** 2026-11-05

**Amends:** [ADR 0005](0005-operator-console-visual-redesign.md) (shortcut
model, preference set); `DESIGN.md` §§6, 8, 10–13, 19, 27–32, 36, 37, 42

**Decided by:** the SFT-112 session of the unattended igris run, on the
maintainer's standing instruction that design decisions are recorded here so
that any of them can be reversed after the run. Every decision below names the
alternative it rejected and what reversing it costs.

## Reversal cost at a glance

Read this first. The owner can reverse any decision; these are the ones that
get expensive once the H3 tasks have built on them.

| Decision | Cost to reverse after H3 | Why |
|---|---|---|
| D1 single-row filter bar with Live controls on the tail line | **High** | SFT-116 (shared filter model) and SFT-117 (swap targets, preserved state) build their templates, CSS, and Playwright tests on this layout for both modes. |
| D2 keep History newest-first and Live newest-last | **High if reversed** (zero to keep) | Pagination cursors, the 1,000-row cap trimming rule, the tail line's two placements, and the Playwright suite all encode the two orientations. |
| D7 time zone preference stored as an IANA zone and rendered server-side | **Medium** | Every handler that formats a time (History, event detail, Live page, Status, Audit, Backup, export dialog) gains a zone parameter in SFT-115. Going back to browser-side rewriting means undoing that in each. |
| D5 copy voice, renames, and sentence case | **Medium** | SFT-122 changes template text and the Playwright selectors that quote it. Each rename is a find-and-replace, but there are many. |
| D6 keyboard-shortcuts preference | **Low** | One cookie field, one attribute, one guard in the keyboard module. |
| D3 severity signature, D4 icon sprite, D8–D13 row details, type scale, font subset | **Low** | CSS, one template partial, and the font files. No contract changes. |
| D14 drop the `@layer` requirement, D15 drop the eyebrow | **Low** | Documentation and a CSS reorganisation that was never done. |

Nothing here changes a database schema, the ingestion contract, the export
formats, or the History URL contract.

## Context

The 2026-11-04 audit (§4, "Design critique" and "Spec corrections") found that
the interface built for the 0.6.0 redesign is safe and functionally correct
but generic: the signature items `DESIGN.md` describes were not built, the
filter area outweighs the logs it filters, the two log modes read in opposite
directions, fatal is weaker than error in dark mode, body copy quotes the
specification instead of speaking to an operator, and the specification
itself contains errors (a reversed `light-dark()` example, two requirements
recorded as done that were never implemented). Audit UX-02, UX-04, and UX-05
require three decisions that `DESIGN.md` left open or got wrong: whether
single-letter shortcuts can be turned off, which time zone the interface shows,
and whether History and Live share one filter model.

The H3 implementation tasks (SFT-113 to SFT-126) need those decisions fixed
before they start, in one place, with reasons, because the run is unattended
and the owner reviews the result afterwards. This ADR is that place.
`DESIGN.md` is amended in the same change and remains the authoritative
specification; this document explains why it says what it says.

The screenshots in `.playwright-artifacts` from the H2 gate were the visual
evidence: `history-desktop-dark.png` shows the first log row about 745 px
below the top of a 1280 px viewport; `live-desktop-dark.png` shows roughly
300 px of boxed fieldsets above an empty list; `live-mobile-light.png` shows
the list beginning below the fold at 390 px.

## Decision

### D1. Filter bar: one bar, two rows at most, Live controls on the tail line

The Logs workspace gets one filter bar shared by History and Live. On a
desktop viewport it occupies at most two rows; the fieldset boxes, their
legends, and the stacked "Filters" panel are removed.

```text
┌──────────────────────────────────────────────────────────────────────────┐
│ [History|Live] Scope [All servers ▾][nextup ▾][api ▾]  15m 1h 6h 24h 7d  │
│                                                   Custom   More ▾  Apply │
│ TRC DBG INF WRN ERR FTL UNK · Errors Problems All   stdout stderr unknown │
│ [ Contains message text                                                ] │
├──────────────────────────────────────────────────────────────────────────┤
│ 200 events loaded, newest first · last hour (UTC) · all sources   Wrap ▢ │
│ ───────────────── Load newer · Go live ────────────────────────────────  │
│ ▸ 13:32:03.033  INF  managed-primary/web   browser managed token check   │
```

- **Row one:** the History/Live mode switch; the source scope as cascade
  selects showing only the dimensions that have more than one option (§9.1
  already requires this); the time presets with Custom (History only); a
  "More" disclosure; and the Apply button, rendered quiet. Apply exists for
  keyboard and no-JavaScript use; with JavaScript, selectors apply on change
  and text applies on debounce or Enter, as today.
- **Row two:** the seven severity-code toggles as labelled checkbox chips
  with the Errors, Problems, and All shortcuts beside them; the three stream
  chips; the Contains field, which takes the remaining width.
- **More:** Does not contain; the custom From and To range; the structured
  filters (container instance, request ID, logger, HTTP method, HTTP status,
  error type). Live shows only the controls its stream supports and leaves
  the others out rather than disabling them.
- **Below the bar:** the result summary on one line, including the display
  zone label (D7) and the Wrap toggle (D8), then the tail line, then the rows.
- **Live controls** (Pause/Resume, Jump to newest, Clear view) move onto the
  Live tail line beside the connection state. Reconnect appears only in the
  Disconnected state. The separate Live toolbar row is removed. This mirrors
  History, whose tail line already hosts Load newer and Go live: in both
  modes the tail line is the control strip for the newest edge.
- **Target:** at 1280×800 in Compact density with the bar open, the first log
  row begins within 280 px of the top of the viewport. The H3 gate (SFT-126)
  records the measured value.
- **Narrow widths (below 48 rem):** the bar collapses into the existing
  "Filters" disclosure, closed by default, whose summary line names the
  active scope and range so nothing is hidden. The open state survives
  fragment swaps (SFT-117).

The filter model behind the bar is one model for both modes: scope, levels,
streams, and contains serialise to both History and Live URLs, and switching
modes carries them (SFT-116). The Live source selector becomes the same
cascade as History.

**Rejected:** keeping the fieldset layout and only tightening its spacing.
The boxes are the problem: five bordered groups with bold legends read as a
settings form, not an instrument, and they push the logs below the fold.
Also rejected: a popover "Filters" button that hides every filter behind one
click (Grafana Explore style). It saves vertical space but violates §4.3,
which requires active filters to stay visible, and it costs a click per
filter change during an incident.

### D2. List direction: History newest-first, Live newest-last; the tail line explains the flip

Both orientations stay as they are. History reads from the newest event at
the top down into the past, with Load newer on the tail line above the list
and Load older below. Live reads down to the newest event at the bottom, with
the tail line below the newest row. The rule is now written down: **time flows
away from the tail line.** The tail line always marks the newest edge; the
newest edge is where the operator's attention lands on arrival, which is the
top in History (triage the latest) and the bottom in Live (watch the stream).
The result summary states the order in words ("newest first") so the
orientation is never implicit.

**Rejected:** one orientation for both modes. Newest-first Live would push
rows down under the reader's eye; no tailing tool does that. Newest-last
History would open 200 rows deep with the newest at the bottom, scroll to it
on load, and show the oldest rows to a no-JavaScript reader; it would also
reverse the cursor directions, the cap-trimming rule (SFT-117), and the
Playwright suite for no gain in investigation speed. The audit's observation
is right that the two differ; the fix is to make the difference legible, not
to remove it.

### D3. Severity signature: left edge, fatal as the only filled code, info secondary

| Level | Code | Treatment |
|---|---|---|
| trace | `TRC` | `--text-muted`, weight 400, no edge |
| debug | `DBG` | `--text-muted`, weight 400, no edge |
| info | `INF` | `--text-secondary`, weight 400, no edge |
| warn | `WRN` | `--status-warning`, weight 400, 2 px left edge in the same colour |
| error | `ERR` | `--status-error`, weight 400, 2 px left edge |
| fatal | `FTL` | weight 700, code rendered as a filled chip (`--status-fatal` background, `--text-inverse` text), 3 px left edge |
| unknown | `UNK` | `--text-secondary`, dotted underline, no edge |

The left edge is an inset box shadow on the row, full row height, so it never
changes layout. The row background is never tinted. The message column keeps
`--text-primary` for every level, so severity never dims the evidence.

The dark-theme fatal token changes from `#F79A94` to `#FF6B63`, a deeper and
more saturated red than error (`#EE7B74`): 6.73:1 on the base surface, 6.25:1
raised, 4.69:1 on the selected surface, and 6.83:1 as a chip under
`--text-inverse`. The light-theme fatal (`#8F1D25`) already reads stronger
than error and stays. Fatal is now stronger than error by weight, by fill,
and by edge width, so the ordering holds without colour.

**Rejected:** tinting the whole fatal row (forbidden by §12.3 and it swamps
the message); an icon for fatal (severity must never be icon-only and the
codes already align); keeping the pink fatal token and relying on weight
alone (weight is lost at 12.5 px in a monospace face).

### D4. Icons: one inline SVG sprite, eight symbols

A single template partial renders an inline `<svg>` sprite of `<symbol>`
elements once per page, hidden, and every icon is `<svg class="icon"
aria-hidden="true" focusable="false"><use href="#icon-name"/></svg>`. The
set: `chevron` (expansion; rotated by CSS when expanded), `copy`, `check`
(copied confirmation), `close`, `search`, `external`, `arrow-down` (new
events), `menu` (narrow-width header). All on a 16 px grid, 1.5 px stroke,
`currentColor`. Icon-only buttons carry a visible-text or `aria-label` name.
The Live module builds icons with `createElementNS`, never `innerHTML`. The
Unicode glyphs (`›`, `↓`, `⌘`) are replaced, except `⌘` and `Ctrl` inside
`<kbd>`, which are text.

**Rejected:** an external sprite file referenced by URL (one more cacheable
asset, but `<use>` across documents has had inconsistent browser behaviour
and the sprite is under 2 KB); an icon font (banned by `AGENTS.md` §21);
keeping Unicode glyphs (they render differently per platform font and
cannot be stroked to match the type).

### D5. Copy voice and capitalisation

The interface speaks to the operator about their logs, never about Siftail's
implementation. Rules, now in `DESIGN.md` §36:

1. **Sentence case** for every heading, label, button, menu item, and
   notice. Capitalise only proper nouns (Siftail, Coolify, Fluent Bit,
   SQLite, UTC) and the two mode names, History and Live. "Server",
   "source", "service", "project", "environment", "application",
   "container", and "token" are common nouns in the interface, whatever
   the domain documents capitalise.
2. **Name things by what they do for the operator**, not by the mechanism:
   "Test connection", not "Guided committed-receipt test"; "Retention", not
   "Bounded local storage controls"; "System health: Attention" as the header
   indicator's name.
3. **Words that do not appear in the interface:** committed, canonical,
   bounded, deliberate, synthetic, trusted, sanitized, immutable, appliance,
   artifact, receipt, stable identity, normalized. Say what happens instead:
   "stored", "newest first", "limited to", "sent by".
4. **Buttons are verb plus object** and keep their name through the flow
   ("Export results" opens a dialog whose button is "Download").
5. **Errors say what happened and what to do**, in the interface's voice,
   never apologising and never vague. Empty states invite the next action.
6. **The eyebrow label is removed** (D15); the `h1` names the page and the
   context line names the scope in plain words.

Renames SFT-122 applies (old → new): "Guided committed-receipt test" → "Test
connection"; "Create Server" → "Create server"; "Connect this Server" →
"Connect this server"; "Export matching History" → "Export results"; "Create
download" → "Download"; export confirmation "I confirm this complete matching
range will leave Siftail." → "Export every matching event, not only the rows
loaded on this page."; Settings context "Bounded local storage controls" →
"Retention"; Sources context "Stable sources discovered from committed logs"
→ "Services that have sent logs"; Servers context "Trusted identities for
authenticated ingestion" → "Each server sends logs with its own token";
Audit context "Immutable, bounded administrative security events" →
"Sign-ins and administrative actions"; Backup context "Verified local SQLite
recovery artifacts" → "Database backups"; Status context "Healthy · sanitized
local operational state" → the severity word alone; Live empty state
"Waiting for new events from this source" → "Waiting for events from
{scope}" with a History link. The destination names Logs, Sources, Servers,
Status, Settings, Audit, and Backup stay.

**Rejected:** renaming the Status destination to "System health". The
destination name is conventional and short; the phrase is used where it
carries meaning, on the header indicator. Also rejected: keeping domain
capitalisation ("Server") in the interface for consistency with
`DOMAIN.md`. The documents are for implementers; the interface is for
operators, and capitalised common nouns read as jargon.

### D6. Keyboard shortcuts preference

A third interface preference, **Keyboard shortcuts: On / Off**, default On,
lives in the preference cookie beside theme and density and is rendered
server-side as `data-shortcuts="on|off"` on `<html>`. When Off, every
single-character shortcut is inert: F, H, L, J, K, G, Space, and `?`.
`Esc` and `Ctrl/Cmd+K` stay, because WCAG 2.1.4 concerns printable
single-key shortcuts only. The System menu gains a **Keyboard shortcuts**
item that opens the help overlay, so the overlay has a visible control in
both states; when shortcuts are off the overlay says so and links to the
preference. Space is never bound on a `button`, `a`, `summary`,
`[role=button]`, or form control, and the Live pause binding exists only when
a pause control is present (UX-01). The cookie format becomes
`theme.density.shortcuts.timezone` with the parser accepting fewer fields.

**Rejected:** shortcuts active only while a log row has focus (satisfies
WCAG 2.1.4 but breaks F, H, and L, which are used before any row is
focused); remapping (complexity nobody asked for); default Off (the product
is keyboard-first; WCAG requires a mechanism, not a default).

### D7. Time zone preference: UTC or the browser's zone, rendered by the server

A fourth preference, **Time zone**, holds either `utc` (default) or an IANA
zone name. The System menu offers "UTC" and "Browser time zone (Europe/Berlin)"
where JavaScript fills the second option from
`Intl.DateTimeFormat().resolvedOptions().timeZone`; without JavaScript only
UTC is offered and the menu says so. The server validates the value (at most
64 characters, `[A-Za-z0-9_+/-]`, loadable with `time.LoadLocation`), stores
it in the cookie, and renders **every** time in that zone: History rows and
range summary, event detail, Status, Audit, Backup, the export dialog, and
the custom-range picker labels. The Live module formats its rows with the same
zone from a `data-timezone` attribute. A zone that cannot be loaded renders
UTC and the label says UTC.

Formats are fixed and locale-independent so columns never move:
`15:04:05.000` for rows, `02 Jan 15:04:05.000` when the range spans more than
one day, `02 Jan 2006 15:04` for ranges and absolute times, always 24-hour
(`hourCycle: 'h23'` in JavaScript). The zone is labelled wherever a time is
shown: the result summary and column header carry the zone name ("UTC",
"Europe/Berlin"); event detail shows the preferred zone, UTC, and received
time; `<time>` always carries `datetime` in UTC. The History URL keeps UTC
RFC3339 `from` and `to`; the picker converts.

**Rejected:** browser-side rewriting of server-rendered UTC (the current
behaviour and the cause of UX-04: unlabeled, inconsistent, flashes on load,
breaks without JavaScript); default to the browser zone (the server cannot
know it on the first request, so the first paint would differ from the
second; UTC is also what exports, Status, and Audit already show, and the
preference is one click away in the menu that also holds theme and density);
a per-session rather than per-cookie zone (the cookie already carries
non-secret interface preferences and survives sign-in).

### D8. Wrap toggle

A **Wrap** toggle (a `button` with `aria-pressed`) sits at the end of the
result summary in History and on the tail line in Live. Default off: Compact
clamps the preview to one line, Comfortable to two, with an ellipsis. On:
the preview wraps in full. The state is browser-local (`localStorage`),
applied as a class on the list, and survives swaps and new Live rows. The
preview itself remains the server-bounded excerpt; the complete message is
in the detail panel.

**Rejected:** a server-stored preference (presentation of one list, not an
interface-wide setting); always wrapping in Comfortable (destroys the scan
rhythm that density exists for).

### D9. Match highlight

When `contains` is set, matching substrings in the message preview are
wrapped in `<mark>`. The server splits the preview into segments in the view
model and the template escapes each segment; the Live module does the same
with `document.createTextNode` and `createElement('mark')`. Matching is the
same literal, ASCII-case-folded comparison as the query. Two tokens are added:
`--mark-bg` (dark `#4A4420`, light `#F5E7A3`) with the row's text colour
inherited (8.4:1 and 13.1:1). The mark adds no padding, so widths do not
change. Only `contains` highlights; `excludes` has nothing to show.

**Rejected:** the accent as highlight colour (reserved for position and
interaction; it would compete with selection); client-side highlighting of
server-rendered rows (`innerHTML` risk with hostile content, and it flashes).

### D10. Multiline hint

Rows whose stored message has more than one line end the preview with a
muted `+N lines` suffix in the same monospace face. The server supplies the
count in the History view model and as an additive `lines` field in the Live
event payload. The suffix is part of the message cell, so it never shifts
columns.

### D11. Selection, hover, and expansion

Hover: `--surface-hover` background only. Keyboard-selected row (the roving
tab stop): `--surface-selected` background plus the 2 px `--focus-ring`
outline drawn inset so the edge indicator and the outline never collide.
Expanded: the chevron rotates 90° and the detail panel opens below; no colour
change on the row. Row metadata (source, stream) uses `--text-secondary`, not
`--text-muted`, because muted fails 4.5:1 on hover and selected surfaces in
both themes (UX-10). `--text-muted` is for help text on base and raised
surfaces and is never used inside rows or table rows that have hover states.

### D12. Type scale and minimum size

`--text-2xs` (11 px at the Compact root) is removed from the scale. Nothing
in the interface renders below 12 px; help text uses `--text-xs`. Component
rules use the scale tokens only; the eighteen literal `font-size` values in
`app.css` are replaced in SFT-121. The scale otherwise stands.

### D13. Font subset

JetBrains Mono ships as static instances at weights 400 and 700, subset to
latin and latin-extended (four files), replacing the 100–800 variable files.
The regular latin file is preloaded; `font-display: swap` stays. Monospace
text uses weights 400 and 700 only: severity codes are 400 and `FTL` is 700,
which is part of the fatal signature (D3). The binary-size delta is recorded
by SFT-121.

**Rejected:** keeping the variable file (weights the specification forbids,
larger than two static instances, and the 600 the current CSS asks for is
synthesised to 700 anyway with static files, so nothing is lost).

### D14. Specification errors

- §27.2 `light-dark()` takes the light value first. The example is corrected;
  `tokens.css` was already right.
- §27.1's `@layer base, chrome, components, pages` requirement is **removed**.
  Layers would reorder the cascade of a 2,000-line stylesheet during an
  unattended run for no operator-visible benefit; the replacement rule is
  that `app.css` is organised in commented sections in that order, with no
  `!important` and selector specificity at most two classes except for state
  selectors. The owner can reintroduce layers with its own task.
- §28.2 "no literal font sizes" stands and is implemented by SFT-121 (D12).

### D15. Eyebrow removed, heading compacted

The small-caps eyebrow above each `h1` is removed. It duplicated the mode
switch on Logs and the page name elsewhere, and spent 20 px of the vertical
budget D1 reclaims. The workspace heading becomes the `h1` with the context
line on the same row at desktop widths and below it at narrow widths.
`pageMeta` loses its eyebrow argument when SFT-122 applies the copy pass.

### D16. Header health label and tokens

The header health indicator shows a visible text label whenever the status
is not Healthy ("Attention", "Degraded", "Unavailable") beside the dot, with
the accessible name "System health: Attention"; Healthy keeps the dot only
(UX-15). `--border-strong` moves to dark `#5A6A86` (3.43:1 on base, 3.19:1
raised) and light `#79849B` (3.42:1, 3.66:1) so non-text contrast passes
WCAG 1.4.11; SFT-120 verifies these values with its contrast test and may
adjust them, recording the final numbers in §27.3.

### D17. Event detail: events around this time

Event detail gains a **Show events around this time** action that opens
History at the event's time ±1 minute with the current scope and the event
highlighted (UX-11, SFT-116). It sits with the copy actions. The window is
fixed at one minute; wider context is one preset click away.

## Alternatives considered as a whole

- **Leave the direction to the implementing tasks.** Eleven tasks would each
  decide a piece, and the result would be another coherent-looking but
  unlevel interface, which is exactly what ADR 0005 was written to end.
- **Write a design review document separate from this ADR.** The task
  allowed either. One document with every decision and its reversal cost is
  easier for the owner to act on the morning after the run than a review plus
  an ADR that summarises it.
- **A second visual identity revision.** Not needed. ADR 0005's identity
  (graphite-blue surfaces, periwinkle-indigo accent, JetBrains Mono, the tail
  line) is right; it was never fully built. This ADR finishes it.

## Consequences

- `DESIGN.md` is amended in this change: §6.1, §6.4, §8.1, §9.1, §10.2,
  §10.7, §10.8, §11.2, §12.2–§12.5, §13.1, §19.5, §24.4, §27.1–§27.3, §28.1,
  §28.2, §31, §32.2, §32.3, §32.5, §32.6, §34.1, §36, §37, and §42. Section
  numbers are unchanged so existing references stay valid.
- SFT-113 implements D6; SFT-115 implements D7; SFT-116 implements the shared
  model of D1 and D17; SFT-117 builds on D1's targets; SFT-118 implements
  D16's label; SFT-120 implements D11's token rules and D16's border values;
  SFT-121 implements D3, D4, D8–D10, D12, D13, and the layout of D1; SFT-122
  implements D5 and D15.
- The preference cookie grows to four fields. Old cookies parse with
  defaults for the missing fields; no migration.
- The Live SSE event payload gains an additive `lines` field.
- `ARCHITECTURE.md` is updated by SFT-115 and SFT-116 where it describes the
  preference cookie and the URL state, as their task entries already require.
- The owner's review after the run (SFT-126) is the point at which any of the
  decisions above is reversed; the table at the top says what each costs.

## Migration and compatibility impact

No schema change, no ingestion or export change, no History URL change. The
preference cookie is additive. Live SSE consumers (only Siftail's own module)
receive one extra field. Reversing any decision here is a superseding ADR or
an amendment to this one, plus the `DESIGN.md` sections it names.

ADR 0005's remaining decisions stand: the operator-console character, the
visual identity, the `light-dark()` theming model, the vendored monospace face,
server-stored preferences, the information architecture, the scoped shortcut
model (now with an off switch), and the tail line as the signature element.
