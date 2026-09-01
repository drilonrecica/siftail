# Siftail Design Specification

**Status:** Authoritative product design specification  
**Product:** Siftail  
**Audience:** Maintainer, coding agents, designers, reviewers, contributors  
**Supersedes:** The pre-`0.6.0` design specification, per ADR 0005

---

## 1. Purpose

This document defines how Siftail looks, behaves, communicates, and remains
accessible across desktop and emergency mobile use.

It is authoritative for:

- information architecture and navigation;
- the History and Live workflows;
- source selection and the command palette;
- log-row presentation;
- filtering and search interactions;
- setup and administration flows;
- the visual system, tokens, and typography;
- brand application;
- responsive behavior;
- keyboard interaction and shortcut scoping;
- loading, error, empty, and notification states;
- accessibility;
- motion;
- interface copy.

The interface must reinforce the product scope: Siftail is an operational
console, not an analytics dashboard.

---

## 2. Design vision

Siftail is a **night-shift instrument**: the tool a self-hoster reaches for at
02:00 during an incident. Everything about the interface serves fast, calm,
keyboard-driven investigation.

It should be:

- keyboard-first — every investigation action reachable without the mouse;
- dense but legible — log rows maximize signal per vertical pixel;
- calm under failure — degraded states are stated plainly, never dramatized;
- visually quiet — chrome recedes, log content and severity carry the color;
- stable during high event rates — no layout shift, no per-event animation;
- precise — tabular timestamps, fixed-width severity codes, exact copy.

It should not look like:

- a generic card-heavy SaaS dashboard;
- a fake terminal or retro CRT simulation;
- a colorful analytics suite;
- an enterprise monitoring wallboard.

The instrument character comes from function, not decoration:

- monospace log surfaces with consistent alignment;
- fixed-width three-letter severity codes;
- a visible tail line marking the newest edge of a log list;
- strong keyboard affordances with discoverable shortcuts;
- one restrained accent color that never competes with severity.

Do not use scanlines, flickering cursors, typing animations, green-on-black
styling, decorative command prompts, neon gradients, decorative charts, card
grids for logs, mascots, or humorous error copy.

---

## 3. Brand application

### 3.1 Name

Use `Siftail` in human-facing product copy and `siftail` for technical
identifiers.

### 3.2 Tagline

> Fast, private logs for self-hosted apps.

### 3.3 Name explanation

Explain the product meaning subtly, at most in selected introductory copy:

> Sift through historical logs and tail live events from one lightweight,
> private interface.

Do not repeatedly state that Siftail combines "sift" and "tail." The tail
line (§12.6) expresses the identity functionally.

### 3.4 Logo concept

A compact mark suggesting several log lines narrowing into one clear stream
within a contained boundary. It must remain recognizable at favicon scale.
Avoid literal plumbing, a magnifying glass as the only motif,
database-cylinder clichés, shield-heavy security branding, and mascots.

### 3.5 Brand voice

Direct, factual, calm, technically literate, honest about limitations,
lightly friendly, never jokey during errors.

Preferred:

> No logs matched these filters.

> Ingestion is paused because the database is out of writable space.

Avoid:

> Uh-oh! Your logs are hiding 🙈

> Something went wrong.

---

## 4. Design principles

### 4.1 Optimize for investigation

The interface should help answer: which source emitted this; when it
happened; what the message says; what happened immediately before and after;
whether it is still occurring; whether a container changed.

### 4.2 Preserve context

Avoid full-page transitions and destructive replacement of content during
filtering. Keep the operator oriented.

### 4.3 Make state explicit

Always make these states visible when relevant: History or Live; active
source scope; active time range; active filters; live connection state;
pause state; result truncation; storage degradation; loading state; source
inactivity.

### 4.4 Keyboard first, mouse equal

Every investigation action has a shortcut, and every shortcut has a visible
clickable equivalent. Shortcuts are scoped (§32) so they can never destroy
form input or surprise the operator on administration pages.

### 4.5 Use density deliberately

Logs benefit from compact layout. Controls are comfortable enough to
operate; log rows maximize useful vertical information.

### 4.6 Do not rely on color alone

Severity, connection state, validation, and warnings require text, shape, or
position in addition to color. Severity codes (§12.3) are always visible.

### 4.7 Keep browser and server responsibilities clear

Server owns: query validation; rendered historical fragments; source and
settings truth; authentication and authorization; persistent event details;
**theme and density preferences** (§19.5).

Browser owns transient view behavior: scroll position; pending live-row
counter; selected keyboard row; paused rendering state; expansion state.

---

## 5. Information architecture

### 5.1 Primary navigation

Three primary destinations, in order:

```text
Logs   Sources   Servers
```

`Logs` is the primary destination and always first.

Four administration destinations group under a **System** menu in the header:

```text
System ▾
  Status
  Settings
  Audit
  Backup
```

The System menu also contains the theme and density preference controls and
the sign-out action. Do not add a Dashboard item.

### 5.2 Primary routes

```text
/login
/logs            (?mode=history default, ?mode=live)
/sources
/servers
/status
/settings
/audit
/backup
POST /preferences
```

### 5.3 Landing behavior

After authentication:

- first use with no sources: open Logs showing the first-run empty state with
  its setup path (§24.1);
- returning use: restore the previous investigation state from the URL;
- if the previous source no longer exists, explain and offer source
  selection;
- never force an overview dashboard.

### 5.4 Logs workspace hierarchy

```text
Global header (primary nav · health · palette · System)
└── History | Live mode switch
    └── Filter bar (source scope, time, levels, streams, text)
        └── Status/result summary
            └── Tail line (newest edge)
                └── Log event list
                    └── Expanded event details
```

---

## 6. Global application shell

### 6.1 Header layout

One slim, visually quiet top bar on every authenticated page:

```text
[◆ Siftail]  Logs  Sources  Servers          [●health] [⌘K] [System ▾]
```

- Brand mark and name link to `/logs`.
- Primary navigation marks the active destination with `aria-current="page"`
  and an accent underline.
- The health indicator is a small dot plus visually hidden text naming the
  current status severity; it links to `/status`. When status is Healthy the
  dot uses a muted neutral; Attention uses warning; Degraded/Unavailable use
  error. The dot is omitted (plain Status link remains in System) if a cheap
  cached severity value is unavailable.
- The palette button is labeled with the platform shortcut (`⌘K` or
  `Ctrl K`) and opens the command palette (§32.4).
- The System menu is a native `<details>`-based menu containing Status,
  Settings, Audit, Backup, the preference controls, and Sign out.

The header must not compete with logs: no additional accent fills, no
buttons with primary emphasis, no badges other than the health dot.

### 6.2 Narrow widths

At narrow widths the primary items remain visible as long as space allows;
below that the header collapses to brand, health, palette, and a single menu
containing all destinations. Current mode and source context remain visible
in the workspace heading.

### 6.3 Global degraded banner

Critical states such as disk-full mode display a persistent banner below the
header on every authenticated page:

> **Ingestion unavailable:** the database cannot write because storage is
> full. Existing logs remain available. Free disk space or reduce retention.

The banner includes a Status link, not a dismiss button. Critical unresolved
conditions are not dismissible.

### 6.4 Workspace heading

Each page renders an eyebrow (small caps label naming the section), an `h1`,
and a context line. Page metadata comes from one server-side definition per
page; the shell must not contain per-page conditional chains for titles,
eyebrows, or headings.

---

## 7. Login experience

### 7.1 Login screen

A centered card on the base surface sharing the shell's head and brand
partials (no authenticated chrome). Content:

- Siftail mark and name;
- concise product line: "Sign in to inspect your private application logs.";
- username and password with visible labels;
- sign-in button;
- no registration link, no forgotten-password email flow;
- recovery copy: "Lost access? Reset the administrator password with the
  Siftail CLI on the host."

The login page is themed server-side from the preference cookie like every
other page.

### 7.2 Error behavior

One uniform error:

> Sign-in failed. Check your credentials and try again.

During throttling:

> Too many attempts. Try again shortly.

Do not reveal whether the username exists or how throttling is keyed.

### 7.3 Accessibility

Username focus on initial load; visible labels; error associated with the
form via `role="alert"`; password-manager compatible; no animation that
shifts the form during failure.

---

## 8. Logs workspace

### 8.1 Mode switch

An explicit two-option segmented control:

```text
History | Live
```

Because switching modes performs navigation, the control renders as links
styled as a segmented control — **not** with `tab`/`tablist` roles. The
active mode is marked with `aria-current="page"`.

History and Live share source and severity filters but remain distinct
states. Do not append live records into a historical result set
automatically.

### 8.2 Current source context

A compact context line in the workspace heading:

```text
Hetzner Production / NextUp / production / api
```

Collapse dimensions with only one available value where useful; expanded
details always expose complete source identity.

### 8.3 Source switching

Sources are switched through the filter bar selects or through the command
palette (§32.4), which fuzzy-matches server, project, environment,
application, service, and alias and shows server context when more than one
server exists.

---

## 9. Source filters

### 9.1 Cascading selection

Canonical hierarchy:

```text
Server → Project → Environment → Application → Service
```

Rules:

- hide or collapse a dimension with exactly one relevant option;
- changing a parent resets invalid child selections;
- labels use aliases when available; original labels remain discoverable in
  detail;
- "All services" is allowed within an application;
- broad all-servers History is permitted only with bounded range.

### 9.2 Selector update behavior

Source selectors update results immediately after a valid selection. History
updates URL state through HTMX push/replace. Live reconnects explicitly with
a visible `Connecting` state.

### 9.3 Missing/inactive source

Inactive sources render an `Inactive` text badge and are not hidden;
historical investigation remains possible. If a source is removed while
open:

> This source was removed. Choose another source.

---

## 10. History mode

### 10.1 Default state

New source or first use: last one hour; all levels; all streams; no text
filter; newest first. Returning browser: restore from URL.

### 10.2 Time controls

Quick presets:

```text
15m · 1h · 6h · 24h · 7d · Custom
```

- Presets resolve to absolute endpoints when applied and keep those
  endpoints in every pagination URL.
- Custom range uses labeled From and To fields with `datetime-local` inputs
  (UTC text entry remains the no-JS fallback), validation that To is after
  From, half-open semantics `[From, To)`, and a maximum range of 31 days.
- Row and range display use the browser locale and timezone; event details
  also show canonical UTC.

### 10.3 Level filter

Multiple explicit levels: `trace debug info warn error fatal unknown`, with
shortcuts Errors (error+fatal), Problems (warn+error+fatal), and All. Each
option shows visible text. Level checkboxes are real named form controls so
filtering works without JavaScript; the canonical URL serialization remains
the CSV form (`levels=error,fatal`).

### 10.4 Stream filter

Independent multiselect: `stdout stderr unknown`. Never label stderr as
Error. Stream checkboxes follow the same named-control rule as levels.

### 10.5 Text search

Fields: Contains; Does not contain. Debounce by 300–500 ms; Enter executes
immediately; active search appears in the result summary. Comparison is
literal with ASCII-only case folding; `%`, `_`, and backslash are ordinary
characters. Placeholders: "Contains message text" / "Exclude message text".
Avoid implying regex support.

### 10.6 Structured filters

Exact filters for request ID, logger, HTTP method, HTTP status, error type,
and container instance live under a "More filters" disclosure. Do not expose
arbitrary JSON paths.

### 10.7 Filter bar behavior

The filter disclosure is collapsible at every width, defaulting open on
desktop. Its collapsed summary line names the active scope so a collapsed
bar never hides state (§4.3).

### 10.8 Result summary

Show loaded event count, current range, source scope, active text filters,
and whether more results exist in either direction. Prefer
"200 events loaded · more available" over an expensive total count.

### 10.9 Pagination

Initial page: 200 events, newest first. Both directions are reachable:

- **Load older** below the list (cursor-based, appends);
- **Load newer** at the tail line above the list (cursor-based, prepends)
  whenever newer matching events exist.

When loading: retain existing rows; localized progress near the control;
never replace the entire list; prevent duplicate requests.

The browser render cap is 1,000 rows. When the cap trims rows from one end,
the pagination control for that direction remains available so the trimmed
interval stays reachable; the trim is announced politely. The cap must never
remove the operator's only path forward.

### 10.10 URL state

Complete historical query state belongs in the URL:

```text
/logs?mode=history&server=1&app=nextup&service=api&levels=error,fatal&from=…&to=…&q=timeout
```

URLs never contain credentials, tokens, or CSRF secrets. Back/forward
restores query state.

---

## 11. Live mode

### 11.1 Connection states

Visible textual states: Connecting; Live; Paused; Reconnecting;
Disconnected; Truncated. A small static indicator plus text; no continuous
pulsing.

### 11.2 Live controls

Required: Pause/Resume; Jump to newest; source, level, and stream filters;
Clear view (browser-only — never labeled "Clear logs"); connection status.

### 11.3 Auto-scroll and the pending buffer

At or near the bottom: append events and maintain bottom position. When the
user scrolls upward: stop forced scrolling; buffer into the bounded pending
store; show the floating control:

```text
↓ 143 new events
```

Selecting it renders pending events, jumps to newest, and resumes bottom
tracking.

### 11.4 Browser limits

1,000 rendered rows; 2,000 pending rows. Exceeding the rendered limit
removes oldest browser rows with a subtle notice. Exceeding the pending
limit closes and reconnects the subscription from now:

> Live view was truncated while you were scrolled away. Use History to
> inspect the full interval.

### 11.5 Pause semantics

Pause affects browser rendering only. Status says Paused; copy:

> Live view paused. Logs are still being stored.

### 11.6 Reconnection

On disconnect: show Reconnecting; do not clear visible rows; after
reconnection always indicate a possible gap (version one does not replay)
and direct the user to History with an absolute range covering the gap.

---

## 12. Log event row

### 12.1 Anatomy

```text
▸ 20:41:07.283  ERR  nextup/api      Database connection failed        stderr
```

Columns, in order: expansion control; timestamp; severity code; source
summary; message preview; stream label. The row is entirely monospace
(§28.3) with fixed column alignment; timestamps use tabular figures.

### 12.2 Timestamp

Rows show local time with milliseconds (`20:41:07.283`); when the range
spans multiple days, include the date (`27 Jul 20:41:07.283`). Expanded
detail shows the full event timestamp with timezone, UTC, received
timestamp, and delivery delay.

### 12.3 Severity codes

Severity renders as a fixed-width three-letter code so every row aligns and
severity is always textual:

| Level | Code | Treatment |
|---|---|---|
| trace | `TRC` | heavily muted text |
| debug | `DBG` | muted text |
| info | `INF` | secondary text |
| warn | `WRN` | warning color + subtle left edge indicator |
| error | `ERR` | error color + subtle left edge indicator |
| fatal | `FTL` | fatal color, weight 700 + left edge indicator |
| unknown | `UNK` | outlined neutral |

Do not color the entire row. The code list is canonical; templates and the
live rendering module must share one mapping.

### 12.4 Message preview

One line in Compact density; up to two lines in Comfortable. Preserve
visible whitespace sensibly; show truncation ellipsis; indicate multiline
count when useful (`NullPointerException …  +18 lines`).

### 12.5 Row selection and actions

The keyboard-selected row gets a selection outline distinct from hover. No
checkbox multi-select. Actions on focus/hover or in expanded detail:
Expand/Collapse; Copy message; Copy raw event; Copy request ID when present.
Primary action is expansion; avoid per-row action clutter.

### 12.6 The tail line

The tail line is the rendered boundary at the newest edge of every log list
— the product's signature element.

- **Live:** a thin accent rule directly below the newest rendered row,
  carrying the connection state dot. It marks "now." When paused it renders
  in the warning color with the Paused label adjacent. It moves with the
  newest row; it does not animate per event.
- **History:** the same rule sits above the newest loaded row and hosts the
  **Load newer** control when newer matching events exist, and a **Go live**
  link when the range extends to the present. It marks where the tail
  begins beyond the loaded window.

The tail line is a functional landmark, not decoration: it always answers
"where does now start?" It must never receive severity colors, glow, or
continuous animation.

---

## 13. Expanded event detail

### 13.1 Layout

Expand inline below the row (no side drawer). Sections: Message; Source;
Timing; Severity and stream; Common structured fields; Remaining attributes;
Raw payload; Copy actions. The detail panel stays in the monospace family
for values; labels use the UI face. Expanding a row must not switch the
list's reading rhythm.

### 13.2 Source section

Server; project; environment; application; service; container name;
container ID; alias and original name where different.

### 13.3 Timing section

Event time; received time; delivery delay; internal event ID only under
advanced detail.

### 13.4 Attributes

Stable key ordering; nested objects collapsible; long values truncated with
expansion; strings escaped; no auto-linking of arbitrary content; request
IDs and HTTP paths may have focused copy actions.

### 13.5 Raw payload

Scrollable `<pre>` with no heavy syntax highlighting. Cap initial rendering;
provide "Show complete stored content" within safe limits; always show size.

### 13.6 Inline expansion behavior

Multiple rows may remain expanded; expansion state is browser-only; loading
details keeps row context; collapse returns focus to the row's toggle.

---

## 14. Future deployment boundaries

Reserved for a post-dogfood candidate; must not appear in version one. If
implemented after an approved product change, show a subtle separator
labeled `Container changed` (never `Deployment successful`), without
severity color, excluded from export and counts.

---

## 15. Sources page

### 15.1 Purpose

Manage discovered hierarchy and aliases without becoming an infrastructure
inventory.

### 15.2 Source list

Table with display name (plus alias badge), server, project/environment,
lifecycle state, last seen, and retained data. Pagination uses the shared
component (§30) with both directions where applicable.

### 15.3 Source detail

Actions: open logs; set/remove alias; inspect container instances; clear
logs; remove source.

### 15.4 Alias editing

Inline form. Copy:

> Aliases change display names only. Original source metadata remains
> unchanged.

### 15.5 Destructive actions

A visually distinct destructive zone using the danger button treatment
(§30.2).

`Clear logs` — "Delete all retained logs for **NextUp API** while keeping
the source and alias." Requires typing the display name.

`Remove source` — "Delete retained logs, aliases, container history, and
source metadata for **NextUp API**." Requires the stronger typed
confirmation. Never promise secure erasure.

---

## 16. Servers and ingestion tokens

### 16.1 Server list

Server name; active token status; token last used; source count; last event
received.

### 16.2 Create server

Display name; optional descriptive hostname. After creation, prompt token
creation.

### 16.3 Token creation

> Copy this token now. Siftail stores only a hash and cannot show it again.

Provide copy button; masked/unmasked toggle on this one screen; generated
Fluent Bit configuration; generated curl test; explicit Done
acknowledgement. Never redisplay the token.

### 16.4 Token rotation

Create replacement → display once → instruct update → test → revoke old. Do
not force immediate revocation.

### 16.5 Revoke token

> This server will stop sending logs with this token immediately.

Revocation confirmation requires typing the token name.

### 16.6 Generated configuration

Copyable blocks with warnings: token appears in the configuration; protect
the Coolify configuration; restart relevant resources; exclude Siftail
itself from its own drain.

---

## 17. Guided ingestion test

Five stages with success/failure state and safe detail:

```text
1. Request received
2. Token authenticated
3. Event normalized
4. Event committed
5. Source discovered
```

Failure copy examples: "The request reached Siftail, but the ingestion token
was rejected." / "The request authenticated, but record 1 was not valid
JSON." / "The event was valid, but Siftail could not commit it to the
database." Never display raw authorization data.

---

## 18. Status page

### 18.1 Purpose

A compact appliance-status page, not a metrics dashboard.

### 18.2 Sections

**Runtime:** version; uptime; architecture; UI/ingestion readiness.

**Storage:** database size; WAL size; schema and SQLite version; configured
limit; oldest/newest event; retention age; last cleanup; last bounded
database-check result and a `Run safe database check` action that preserves
the page and never exposes paths or raw SQLite errors.

**Ingestion:** events accepted today; recent rate; queued events/bytes;
rejected batches; last successful ingest; last database error.

**Diagnostics:** latest sanitized operational events with time, textual
severity, component, category, fixed safe summary, optional request ID.

### 18.3 Visualization

Text, definition lists, and a single horizontal capacity bar for database
usage. No time-series charts.

### 18.4 Status severity

Healthy; Attention; Degraded; Unavailable. Always textual; the header
health dot (§6.1) reflects the same value.

---

## 19. Settings page

### 19.1 Runtime settings

Editable: retention days; maximum database size. Read-only process
configuration shows environment-derived values with:

> Requires container configuration change and restart.

### 19.2 Validation

Server-side errors appear next to the field:

> Retention must be a whole number from 1 to 3,650 days.

> Maximum database size must be a whole number from 1 to 1,024 GiB.

Both fields submit as one retention-policy change; a field error keeps
submitted values visible and focuses the first invalid field.

### 19.3 Save behavior

Localized progress; success notification "Retention settings saved."; no
full-page spinner.

### 19.5 Interface preferences

Theme (System / Dark / Light) and density (Compact / Comfortable) are
**server-stored preferences** submitted to `POST /preferences` from the
System menu, persisted in a non-secret cookie, and rendered server-side as
`data-theme` / `data-density` attributes before first paint.

- With JavaScript, changes apply instantly and persist in the background.
- Without JavaScript, the preference form submits normally and returns to
  the current page.
- There is no first-paint theme flash in any configuration.

---

## 20. Audit page

Dense chronological table with filters for range, category, outcome, and
exact action. Fields: time; action code; outcome; actor/context; safe
summary; request ID. Audit details never reveal passwords, hashes, tokens,
authorization headers, or raw payloads. Display:

> Security audit events are retained separately from application logs.

---

## 21. Backup and recovery

### 21.1 Browser role

The browser explains commands, shows the last process result, initiates
full/configuration-only backups, and verifies an accessible server-side
artifact. Restore remains CLI-only in version one; the page documents:

> Restoring replaces the active database and requires the Siftail server to
> be stopped. Run `siftail restore --confirm RESTORE /path/to/backup.sqlite`
> on the host. Siftail preserves one verified `siftail.db.rollback` artifact
> and always requires a fresh sign-in.

### 21.2 Backup actions

Full backup and configuration-only backup, with the typed process states,
single-operation limit, one-second focused polling, disabled controls while
running, and safe-basename-only reporting defined by the recovery
architecture. Explain:

> Full backups include retained application logs. Configuration-only backups
> exclude application logs, diagnostics, and audit history. All backups
> exclude browser sessions, so restore requires a new sign-in.

While a job polls, focus and caret position in unrelated controls must be
preserved; the polling region excludes any focused input.

### 21.3 Artifact download

Browser download of backup artifacts is a recorded follow-up (SFT-080), not
part of this specification's version.

---

## 22. Export experience

### 22.1 Formats

`Text (siftail-text-v1)` and `NDJSON (schema 1)`, both preserving escaped
hostile content, UTC timestamps, `null` optional fields, bounded attributes,
lossless base64 raw payload, and canonical newest-first ordering.

### 22.2 Export dialog

Shows source scope, time range, active filters, format, and the hard
100,000-event, 256 MiB, and 31-day limits. Copy:

> Exports contain the complete matching result set up to the configured
> export limit, not only the rows currently loaded.

Filter text appears only in the authenticated dialog, never in audit
metadata or the suggested filename.

### 22.3 Export confirmation

Every export requires the explicit confirmation checkbox because the
complete matching count is deliberately not calculated. Generation uses
native download navigation; failures re-render History with filters
preserved and a localized error; closing the dialog restores focus to its
trigger.

---

## 23. Loading states

- Preserve existing content; show localized progress.
- History filter loading keeps rows visible, sets `aria-busy="true"` on the
  updating region for the duration of the request, and shows a small
  indicator near the result summary.
- `Load older` / `Load newer` change their own label to "Loading…" and
  disable only themselves.
- Live connection uses static status text. No full-page spinners anywhere.

---

## 24. Empty states

Every empty state names the situation and offers its action as a real
control, not prose.

### 24.1 No sources (first run)

> **No log sources yet.**
>
> Create a server, generate its ingestion token, and point a Coolify or
> Fluent Bit log drain at Siftail. Logs appear here the moment the first
> event commits.

Actions: **Create server** (→ `/servers`); **View setup guide** (→ the
token flow documentation section on `/servers`).

### 24.2 No logs in range

> No logs arrived for this source during the selected time range.

Actions: **Last 24 hours** (applies the preset); **Switch source**.

### 24.3 No filter match

> No logs matched these filters. Try a longer time range or remove a message
> filter.

Action: **Clear text filters**.

### 24.4 Empty live view

> Waiting for new events from this source.

Shows connection status; links to History.

### 24.5 No audit events

> No security audit events match this range.

No decorative illustration anywhere.

---

## 25. Error states

- Errors render near the failed component (filter errors in the result
  region; alias validation next to the field; token generation in its
  panel; backup failure in its section).
- Unexpected errors are generic and show `Request ID: 01…`.
- Database unavailable during History: "Logs could not be read because the
  database is temporarily unavailable. Existing filters were preserved."
- Session expiry redirects to login with "Your session expired. Sign in
  again to continue." and a safe return path.

---

## 26. Notifications

One shared notice region rendered by the shell directly below the header.

- Post-action messages ("Retention settings saved.", "Token revoked.") are
  delivered as **one-time flash messages** via a short-lived cookie set
  before the redirect — never as URL query parameters. A reload or shared
  link never re-shows a stale notice.
- Screen-reader accessible via a polite live region.
- Success notices auto-dismiss; critical errors remain until resolved.
- No stacked floods: newest replaces equivalent messages.
- No bouncing or celebratory animation.

---

## 27. Visual system

### 27.1 Architecture

Two stylesheets, both embedded and served same-origin:

- `tokens.css` — font faces, primitive palette, semantic tokens, type
  scale, spacing, radii, shadows, density variables;
- `app.css` — everything else, organized in native cascade layers
  `@layer base, chrome, components, pages`.

Every color used by a component references a semantic token. No literal
colors in component rules; no arbitrary one-off colors when a token exists.

### 27.2 Theming model

Each semantic color is defined **exactly once** with `light-dark()`:

```css
:root { color-scheme: dark light; }
:root[data-theme="dark"]  { color-scheme: dark; }
:root[data-theme="light"] { color-scheme: light; }

:root {
  /* static dark fallbacks first, for browsers without light-dark() */
  --surface-base: #0f1218;
  --surface-base: light-dark(#0f1218, #f3f4f8);
}
```

`data-theme` is rendered server-side from the preference cookie (§19.5);
absence means System. Browsers below the `light-dark()` support floor
receive the complete dark theme via the fallback declarations (ADR 0005).
Duplicated per-theme token blocks are forbidden.

### 27.3 Color tokens

Canonical values (dark, light). Implementation must verify every
text/background pairing against WCAG 2.2 AA before merge; if a value fails
in verification it is adjusted toward compliance and this table is updated
in the same change.

| Token | Dark | Light |
|---|---|---|
| `--surface-base` | `#0F1218` | `#F3F4F8` |
| `--surface-raised` | `#151A24` | `#FBFCFE` |
| `--surface-overlay` | `#1B2230` | `#FFFFFF` |
| `--surface-hover` | `#212A3C` | `#E8EAF2` |
| `--surface-selected` | `#26304B` | `#E1E5F7` |
| `--text-primary` | `#E9EDF5` | `#1B202B` |
| `--text-secondary` | `#B4BDCD` | `#444C5E` |
| `--text-muted` | `#808B9D` | `#5F6879` |
| `--text-inverse` | `#0D1017` | `#F5F7FC` |
| `--border-subtle` | `#262E3E` | `#D6DAE5` |
| `--border-strong` | `#3E4A61` | `#A7AFC2` |
| `--accent` | `#8B96EC` | `#4653C8` |
| `--accent-hover` | `#A2ACF2` | `#3945B0` |
| `--status-info` | `#85A9D6` | `#33608F` |
| `--status-warning` | `#E2A93F` | `#8F5B00` |
| `--status-error` | `#EE7B74` | `#BC3A31` |
| `--status-fatal` | `#F79A94` | `#8F1D25` |
| `--status-success` | `#6FC08D` | `#1F7A48` |
| `--focus-ring` | `#ADB7FF` | `#4653C8` |

### 27.4 Color rules

- The periwinkle-indigo accent is the only brand hue. It marks interaction
  and the current position (active nav, focused controls, the tail line,
  selection) — never status or severity.
- Severity and status colors never appear on chrome or interactive
  affordances unrelated to their meaning.
- Warning amber, error red, success green, and info blue stay visually
  distant from the accent.
- Dark is the design-first theme; light is complete and equally verified,
  built on subtle neutral surfaces, never large pure-white expanses.

### 27.5 Spacing, radius, elevation

```text
--space-1 .25rem  --space-2 .5rem  --space-3 .75rem  --space-4 1rem
--space-6 1.5rem  --space-8 2rem
--radius-sm .3rem  --radius-md .55rem  --radius-lg .8rem
--shadow-raised   (subtle, for menus and popovers)
--shadow-overlay  (pronounced, for dialogs)
```

Every shadow token used must be defined. Log rows use no radius and no
shadow; separation comes from hairline borders.

---

## 28. Typography

### 28.1 Faces

- **UI:** `system-ui, -apple-system, "Segoe UI", sans-serif` — navigation,
  labels, buttons, forms, notices, prose.
- **Data:** **JetBrains Mono** (vendored subsetted woff2, weights 400 and
  700, OFL license vendored alongside), falling back to
  `ui-monospace, "SFMono-Regular", Consolas, "Liberation Mono", monospace`
  with metric-compatible sizing. `font-display: swap`; the regular weight is
  preloaded.

No external font requests of any kind.

### 28.2 Scale

Root: 15px Compact, 16px Comfortable (§29). Tokens:

```text
--text-2xs .733rem   --text-xs .8rem    --text-sm .867rem
--text-md  1rem      --text-lg 1.133rem --text-xl 1.333rem
--text-2xl 1.6rem    --text-log .833rem (log rows)
```

Weights: 400, 500, 600, 700 only. No literal font sizes or nonstandard
weights in component rules.

### 28.3 Monospace usage

Monospace for: log messages, timestamps (tabular figures), severity codes,
IDs, code and configuration, technical metadata, token displays, and the
values inside expanded event details. Sans for chrome. A log list never
switches families mid-flow.

---

## 29. Density

Two modes, server-stored (§19.5): **Compact** (default; single-line
preview, 15px root, tighter row padding) and **Comfortable** (two-line
preview, 16px root, larger targets). Density affects presentation only,
never query size.

---

## 30. Components

Shared template partials render these primitives; pages must not re-invent
them. Prefer native elements (`<button>`, `<dialog>`, `<details>`,
`<fieldset>`, `<table>` for tabular data).

### 30.1 Component set

| Component | Notes |
|---|---|
| Button | primary (accent fill), default, quiet, danger |
| Form field | label, control, help, inline error |
| Severity code | §12.3 mapping, shared with live rendering |
| Badge | alias, inactive, outcome labels |
| Notice | info, success, warning, error; used by the flash region |
| Segmented mode switch | §8.1 |
| System menu | native `<details>` menu |
| Dialog | native `<dialog>`, §33.4 focus contract |
| Command palette | §32.4 |
| Keyboard help overlay | §32.5 |
| Pagination | both directions, self-disabling loading label |
| Empty state | message + real action buttons |
| Status indicator | dot + text |
| Code block | `<pre>` + copy action |
| Log row / tail line / event detail | §12–§13 |

### 30.2 Button emphasis

Primary actions use the accent fill with dark inverse text. Danger buttons
use a filled error treatment — destructive actions must read as destructive,
visually heavier than quiet buttons. At most one primary button per view
region.

---

## 31. Icons

A very small local inline-SVG set (approximately: chevron, copy, close,
external, search, dot indicator), consistent 1.5px stroke on a 16px grid.
Decorative icons are `aria-hidden`; icon-only buttons carry accessible
labels. No icon fonts, no external icons, no icon library dependency.
Severity is never icon-only.

---

## 32. Keyboard interaction

### 32.1 Scoping model

Shortcuts are registered against the workspace scope declared by the page
(`data-kb-scope` on `<main>`). Three shortcuts are global; everything else
exists only in the Logs workspace. On pages without a scope, single-letter
keys do nothing. Shortcuts never fire while focus is in an input, select,
textarea, or editable content.

### 32.2 Global shortcuts

| Shortcut | Action |
|---|---|
| `Ctrl/Cmd+K` | Open command palette |
| `?` | Open keyboard help |
| `Esc` | Close topmost overlay, else clear row selection |

`Ctrl/Cmd+K` is claimed only when the palette can open; it is never
prevented and dropped.

### 32.3 Logs workspace shortcuts

| Shortcut | Action |
|---|---|
| `F` | Focus message search |
| `L` | Switch to Live |
| `H` | Switch to History |
| `J` / `K` | Next / previous log row (roving tabindex, both modes) |
| `Enter` | Expand/collapse selected row |
| `Space` | Pause/resume (Live) |
| `G` | Jump to newest (Live) |

Row navigation works identically in History and Live and never traps focus
inside the list.

### 32.4 Command palette

Opened by `Ctrl/Cmd+K` or the header button. A modal dialog with a single
input and a ranked result list:

- **Sources** — switch the active source scope (server, project,
  environment, application, service, alias all match); selecting one
  navigates with the new scope in the current mode;
- **Navigation** — every destination, including System pages;
- **Page actions** — the current page's server-declared commands (e.g.
  "Export matching History", "Create server", "Run safe database check" as
  navigation to the relevant control).

Matching is client-side fuzzy scoring over server-rendered entries; the
palette makes no network requests and executes nothing destructive
directly — destructive actions only navigate to their confirmations.
Keyboard: `↑/↓` move, `Enter` activates, `Esc` closes and restores focus.

### 32.5 Keyboard help

`?` opens an overlay listing global and currently scoped shortcuts with
their visible-control equivalents. Every shortcut in this section appears
there.

### 32.6 Shortcut safety

Never override browser conventions beyond the declared set; every action
has a visible clickable equivalent; use `event.key` carefully across
layouts; allow future disabling if accessibility feedback requires it.

---

## 33. Accessibility

### 33.1 Target

WCAG 2.2 AA.

### 33.2 Required practices

Semantic landmarks and headings; visible focus rings (`--focus-ring`, 2px,
offset); sufficient contrast in both themes; no color-only meaning; labels
for every control; errors associated with fields; adequate touch targets;
keyboard-operable dialogs and menus; correct focus restoration; skip link;
polite status announcements; reduced-motion support; no focus stealing when
logs arrive.

### 33.3 Live-region restraint

Announce only: connection state changes; pause/resume; throttled new-event
counts while scrolled away; truncation; critical errors; successful
administrative actions. Never announce individual log events.

### 33.4 Dialogs

Native `<dialog>`: initial focus on a meaningful control; focus trapped
while modal; `Esc` closes noncritical dialogs; destructive confirmations
cannot close accidentally during submission; focus returns to the trigger.

### 33.5 Progressive enhancement

Core investigation works without JavaScript: filters (named controls),
pagination (links/forms), theme and density (form submission), sign-in,
administration forms. JavaScript layers on live streaming, instant filter
application, shortcuts, the palette, and copy actions. When a capability is
genuinely JS-only (Live streaming), its absence is stated, not silent.

### 33.6 Checks

Automated: axe in Playwright across pages, themes, and open overlays.
Manual: keyboard-only investigation; screen-reader smoke of login, filters,
row expansion, dialogs; 200% zoom; reduced motion; 390px emergency pass.

---

## 34. Responsive behavior

### 34.1 Desktop first

Dense investigation is the primary case. Filter bar in one or two rows;
aligned row columns; multi-column detail metadata.

### 34.2 Narrow desktop / tablet

Filters wrap into grouped rows; low-priority dimensions collapse;
navigation compacts per §6.2; message remains primary.

### 34.3 Mobile emergency support (390px)

Required: sign in; select source; view recent errors; change time preset;
search; expand event; copy message/raw; switch modes; pause/resume; see
status warnings. Rows stack:

```text
20:41:07.283  ERR
nextup/api
Database connection failed
```

Horizontal scrolling only inside raw payload regions, never the page.

### 34.4 Not required on mobile

Full desktop density; complex filter layouts; a separate app; offline
behavior; PWA installation.

---

## 35. Motion

Minimal functional transitions only: short detail expansion; menu/palette
appearance; smooth jump-to-newest; brief notice entry. No animation on
arriving events; no continuous pulsing (the tail line is static). Respect
`prefers-reduced-motion: reduce` by removing nonessential transitions and
smooth scrolling.

---

## 36. Content and microcopy

### 36.1 Terminology

Use: event/log event; source; server; application; service; container;
History; Live; Clear view; Clear logs; Remove source; ingestion token;
retention; database size. Avoid: "stream" for the source hierarchy;
"delete" for browser-only clearing; "deploy" for inferred container
transitions; "secure delete"; "archive".

### 36.2 Copy rules

- Controls state exactly what happens ("Save changes", not "Submit"); an
  action keeps its name through the flow ("Export…" → "Export started").
- Privacy copy is honest: "Anything your applications write to logs is
  stored. Siftail does not redact content in this version." Never "Your
  logs are completely secure."
- Performance copy uses concrete claims (one container, local SQLite,
  bounded retention), never superlatives.
- Destructive copy states exactly what is deleted and what remains.
- Rows use concise local time; audit and destructive operations use
  absolute times.

---

## 37. State ownership

Server-stored (cookie or database): theme; density; sessions.

URL-owned: complete History query; mode; pagination cursors.

Browser-local (in-page or `localStorage`): scroll position; pending
counter; row selection; expansion; keyboard-help seen flag; optional recent
palette entries later.

Never store in the browser: ingestion tokens; session tokens outside the
secure cookie; passwords; raw log payload caches beyond the active DOM;
CSRF secrets.

---

## 38. HTMX interaction conventions

- Good fragment targets: log rows region, pagination regions, event
  details, settings sections, alias form, status subsections, backup
  region. Never replace the shell for ordinary actions.
- Selectors apply immediately; text search debounced; URL state pushed;
  HTMX history caching disabled; authenticated responses use
  `Cache-Control: no-store`.
- Server returns contextual error fragments for expected failures and a
  generic request-ID fragment for unexpected ones.
- After fragment replacement: preserve focus and caret in filter inputs;
  announce updates concisely; keep scroll stable unless the action moves it.

---

## 39. JavaScript module constraints

Small framework-free modules, one concern each, all embedded and CSP-clean
(no inline scripts):

- the live module owns one EventSource lifecycle, bounds arrays and DOM,
  renders through DOM text APIs (never `innerHTML` with event content),
  handles reconnect/truncation control events, and is browser-tested;
- the keyboard module owns the scope registry (§32.1) and is the only
  key-event listener;
- the palette module reads server-rendered entries from the DOM and makes
  no network requests;
- shared enhancements (copy, dialogs, cascading selects, preference
  submission) stay in the application module.

No JavaScript state system overlapping HTMX; no framework without an
approved ADR.

---

## 40. Page-specific acceptance criteria

**Logs:** keyboard-complete; query in URL; source and mode always visible;
no full-page spinner; deterministic order; escaped content; Live never
steals scroll; caps enforced with both pagination directions reachable.

**Sources:** aliases clearly presentation-only; inactive identifiable;
destructive actions visually distinct and typed-confirmed.

**Servers:** token shown once; configuration copyable; guided test clear;
revocation impact explained.

**Status:** no sensitive values; degraded state obvious; no charts; useful
recovery direction; header dot consistent with page severity.

**Settings:** restart-required values labeled; server-authoritative
validation; preferences apply without flash and without JavaScript.

**Audit:** sanitized; separate retention explained; filterable.

**Backup:** typed process states; focus preserved during polling; restore
guidance exact; no raw paths.

---

## 41. Design QA checklist

Before merging a UI change:

- Does it preserve the operator-console character without kitsch?
- Is the current source and mode still clear?
- Is every action keyboard accessible and listed in keyboard help?
- Is focus visible, restored, and never stolen?
- Does it work in dark and light, Compact and Comfortable, reduced motion?
- Does it rely on color alone anywhere?
- Are log contents escaped; does any JS touch `innerHTML` with event data?
- Does it introduce an external asset, font request, or inline script?
- Do empty, loading, and failure states exist with real actions?
- Is destructive copy exact; is privacy copy honest?
- Does high-rate live rendering remain stable?
- Does mobile emergency use remain possible?

---

## 42. Initial interface copy set

### Product introduction

> Siftail is a fast, private log viewer for self-hosted apps. Sift through
> recent history and tail live events without sending operational data to an
> external service.

### No telemetry

> No telemetry. No external log storage. No subscription.

### Token warning

> Copy this token now. Siftail stores only a hash and cannot show it again.

### Live paused

> Live view paused. Logs are still being stored.

### Live truncated

> Live view was truncated while you were away from the newest event. Use
> History to inspect the complete interval.

### No matches

> No logs matched these filters. Try a longer time range or remove a message
> filter.

### Disk full

> Ingestion is unavailable because the database cannot write to storage.
> Existing logs remain available.

### Alias help

> Aliases change display names only. Original source metadata remains
> unchanged.

### Redaction limitation

> Siftail stores what your applications emit. Log-content redaction is not
> enabled in this version.

### Deletion limitation

> Deleted records may remain in backups, filesystem snapshots, or storage
> free space. Siftail does not claim forensic erasure.

### First run

> No log sources yet. Create a server, generate its ingestion token, and
> point a Coolify or Fluent Bit log drain at Siftail.

---

## 43. Final design summary

The Siftail interface should make recent log investigation feel direct:

> Choose a source, bound the time range, filter the noise, expand the
> evidence, or switch to Live and follow events without losing control of
> the viewport — hands on the keyboard the whole time.

Every design choice serves that workflow while preserving privacy, speed,
accessibility, and visual restraint.
