# Siftail Design Specification

**Status:** Authoritative product design specification  
**Product:** Siftail  
**Audience:** Maintainer, coding agents, designers, reviewers, contributors

---

## 1. Purpose

This document defines how Siftail should look, behave, communicate, and remain accessible across desktop and emergency mobile use.

It is authoritative for:

- information architecture;
- navigation;
- History and Live workflows;
- source selection;
- log-row presentation;
- filtering and search interactions;
- setup and administration flows;
- visual system;
- brand application;
- responsive behavior;
- keyboard interactions;
- loading, error, and empty states;
- accessibility;
- motion;
- interface copy.

The interface must reinforce the product scope: Siftail is an operational console, not an analytics dashboard.

---

## 2. Design vision

Siftail should feel like a **restrained operational console with a slight retro-terminal character and friendly indie polish**.

It should be:

- dense but legible;
- calm under failure;
- quick to navigate;
- clear about current scope and filters;
- visually stable during high event rates;
- professional enough for production use;
- distinct without decorative excess.

It should not look like:

- a generic card-heavy SaaS dashboard;
- a fake terminal;
- a colorful analytics suite;
- a consumer chat application;
- an enterprise monitoring wallboard;
- a retro CRT simulation.

The retro influence is subtle:

- monospace log content;
- compact information density;
- crisp separators;
- small terminal-like connection indicators;
- strong keyboard affordances;
- precise timestamps and identifiers.

Do not use:

- scanlines;
- flickering cursors;
- typing animations;
- green-on-black everywhere;
- distorted CRT effects;
- decorative command prompts;
- neon gradients.

---

## 3. Brand application

### 3.1 Name

Use `Siftail` in human-facing product copy and `siftail` for technical identifiers.

### 3.2 Tagline

Primary tagline:

> Fast, private logs for self-hosted apps.

### 3.3 Name explanation

The product meaning should be explained subtly, at most in selected introductory copy:

> Sift through historical logs and tail live events from one lightweight, private interface.

Do not repeatedly state that Siftail is a combination of “sift” and “tail.”

### 3.4 Logo concept

A compact mark should suggest:

- several log lines entering;
- filtering or narrowing;
- one clear continuing stream;
- a contained private boundary.

It must remain recognizable at favicon scale.

Avoid:

- literal plumbing;
- a magnifying glass as the only motif;
- database-cylinder clichés;
- shield-heavy security branding;
- mascots.

### 3.5 Brand voice

Voice qualities:

- direct;
- factual;
- calm;
- technically literate;
- honest about limitations;
- lightly friendly;
- never jokey during errors.

Preferred copy:

> No logs matched these filters.

Avoid:

> Uh-oh! Your logs are hiding 🙈

Preferred:

> Ingestion is paused because the database is out of writable space.

Avoid:

> Something went wrong.

---

## 4. Design principles

### 4.1 Optimize for investigation

The interface should help answer:

- which source emitted this;
- when it happened;
- what the message says;
- what happened immediately before and after;
- whether it is still occurring;
- whether a container changed.

### 4.2 Preserve context

Avoid full-page transitions and destructive replacement of content during filtering. Keep the operator oriented.

### 4.3 Make state explicit

Always make these states visible when relevant:

- History or Live;
- active source scope;
- active time range;
- active filters;
- live connection state;
- pause state;
- result truncation;
- storage degradation;
- loading state;
- source inactivity.

### 4.4 Use density deliberately

Logs benefit from compact layout. Controls should be comfortable enough to operate, but log rows should maximize useful vertical information.

### 4.5 Do not rely on color alone

Severity, connection state, validation, and warnings require text, shape, icon, or position in addition to color.

### 4.6 Keep browser and server responsibilities clear

Server owns:

- query validation;
- rendered historical fragments;
- source and settings truth;
- authentication;
- authorization;
- persistent event details.

Browser owns transient preferences and live-view behavior:

- theme;
- density;
- scroll position;
- pending live-row counter;
- selected keyboard row;
- paused rendering state.

---

## 5. Information architecture

### 5.1 Primary navigation

Recommended items:

```text
Logs
Sources
Servers
Status
Settings
Audit
```

`Logs` is the primary destination and should be first.

Do not add a Dashboard item.

Navigation may collapse low-frequency items under an Administration section at narrower widths.

### 5.2 Primary routes

```text
/login
/logs
/sources
/servers
/status
/settings
/audit
```

### 5.3 Landing behavior

After authentication:

- first use: open Logs with a source-selection prompt and last-one-hour fallback;
- returning use: restore the previous investigation state from URL or browser-local state;
- do not force an overview dashboard;
- if the previous source no longer exists, explain and offer source selection.

### 5.4 Logs workspace hierarchy

```text
Global header/navigation
└── Source context / quick switcher
    └── History | Live mode switch
        └── Filter bar
            └── Status/result summary
                └── Log event list
                    └── Expanded event details
```

---

## 6. Global application shell

### 6.1 Desktop layout

Recommended:

- compact top bar or narrow left rail;
- product mark and name;
- primary navigation;
- source quick switcher;
- status indicator when degraded;
- account menu.

A persistent wide sidebar tree is not required by default because it consumes space and can become noisy across multiple servers. Source navigation belongs primarily in filters and the quick switcher.

### 6.2 Header behavior

The header must remain visually quiet. It should not compete with logs.

Possible elements:

```text
[Siftail] [Logs Sources Servers Status]      [⌘K Source] [Health] [Account]
```

At narrow widths, use a compact menu while keeping current mode/source visible.

### 6.3 Global degraded banner

Critical states such as disk-full mode display a persistent banner below the header:

> **Ingestion unavailable:** the database cannot write because storage is full. Existing logs remain available. Free disk space or reduce retention.

The banner includes a Status link, not a generic dismiss button. Critical unresolved conditions should not be dismissible permanently.

---

## 7. Login experience

### 7.1 Login screen

Content:

- Siftail mark and name;
- concise product line;
- username;
- password;
- sign-in button;
- no registration link;
- no forgotten-password email flow;
- recovery guidance pointing to CLI without exposing host details.

Suggested supporting copy:

> Sign in to inspect your private application logs.

Recovery copy:

> Lost access? Reset the administrator password with the Siftail CLI on the host.

### 7.2 Error behavior

Use one uniform error:

> Sign-in failed. Check your credentials and try again.

Do not reveal whether username exists or whether throttling is account- or IP-based.

During throttling:

> Too many attempts. Try again shortly.

### 7.3 Accessibility

- username focus on initial load;
- labels visible, not placeholder-only;
- error associated with form;
- password manager compatible;
- no animation that shifts form during failure.

---

## 8. Logs workspace

### 8.1 Mode switch

Use an explicit two-option control:

```text
History | Live
```

This is a semantic tab or segmented control with correct ARIA behavior.

History and Live share source and severity filters but remain distinct states.

Do not append live records into a historical result set automatically.

### 8.2 Current source context

Display a compact breadcrumb or context line:

```text
Hetzner Production / NextUp / production / api
```

Collapse dimensions with only one available value where useful, but expanded details must always expose complete source identity.

### 8.3 Source quick switcher

Open with:

- `Ctrl+K` or `Cmd+K`;
- `/` when focus is not in an input.

Search terms can match:

- server;
- project;
- environment;
- application;
- service;
- alias.

Example entries:

```text
NextUp · production · api
Qisara · production · worker
BletaQA · staging · web
```

Include server name as secondary context when more than one server exists.

The quick switcher is not a command palette for arbitrary administration. Keep it focused on source selection.

---

## 9. Source filters

### 9.1 Cascading selection

Canonical hierarchy:

```text
Server → Project → Environment → Application → Service
```

Use separate cascading selects or compact popovers.

Rules:

- hide or collapse a dimension with exactly one relevant option;
- changing a parent resets invalid child selections;
- labels use aliases when available;
- original source label remains discoverable in detail;
- “All services” is allowed within an application;
- broad “all servers” history is permitted only with bounded range and clear cost.

### 9.2 Selector update behavior

Source selectors update results immediately after a valid selection.

History URL updates through HTMX push/replace state.

Live stream reconnects explicitly with visible `Connecting` state.

### 9.3 Missing/inactive source

Inactive source label:

```text
NextUp API  Inactive
```

Do not hide inactive sources immediately. Allow historical investigation.

If a source is removed while open:

> This source was removed. Choose another source.

---

## 10. History mode

### 10.1 Default state

Returning browser:

- restore last valid source;
- restore previous range and filters where sensible.

New source or first use:

- last one hour;
- all levels;
- all streams;
- no text filter;
- newest first.

### 10.2 Time controls

Quick presets:

```text
15m · 1h · 6h · 24h · 7d · Custom
```

Display absolute range in a tooltip or summary:

```text
27 Jul 2026, 20:00–21:00
```

Use the browser locale and browser timezone for row and range display. Event details
also show the canonical UTC timestamp. Version one has no server-side timezone setting.

Custom range requirements:

- clear From and To labels;
- validation that To is after From;
- half-open semantics `[From, To)`;
- maximum range of 31 days;
- Apply action if native controls create partial intermediate values.

Resolve a relative preset to absolute endpoints when it is applied and keep those
endpoints in every pagination URL.

### 10.3 Level filter

Multiple explicit levels:

```text
trace debug info warn error fatal unknown
```

Provide shortcuts:

- Errors = error + fatal;
- Problems = warn + error + fatal;
- All.

Each option includes visible text, not color only.

### 10.4 Stream filter

Independent multiselect:

```text
stdout stderr unknown
```

Do not label stderr as Error.

### 10.5 Text search

Fields:

- Contains;
- Does not contain.

Behavior:

- debounce contains/excludes by approximately 300–500 ms;
- Enter executes immediately;
- show active search as part of result summary;
- do not run on every keystroke without delay;
- search controls are clearly scoped to message text;
- comparison is literal with ASCII-only case folding;
- valid non-ASCII text is compared exactly;
- `%`, `_`, and backslash are ordinary characters, not wildcard syntax.

Placeholder examples:

```text
Contains message text
Exclude message text
```

Avoid implying regex support.

### 10.6 Structured filters

Initially support exact filters for:

- request ID;
- logger;
- HTTP method;
- HTTP status;
- error type;
- container instance.

These may live under a “More filters” disclosure to keep the main bar compact.

Do not expose arbitrary JSON paths.

### 10.7 Result summary

Show:

- loaded event count;
- current range;
- source scope;
- whether more older results exist;
- active text filter;
- loading state.

Avoid claiming total result count if calculating it would be expensive. Prefer:

> 200 events loaded · more available

instead of forcing a full `COUNT(*)`.

### 10.8 Pagination

Initial page: 200 events.

Primary control:

> Load older

The control uses cursor pagination. It may trigger automatically near the bottom later, but an explicit button remains accessible.

When loading:

- retain existing rows;
- show localized progress near the button;
- do not replace the entire list;
- prevent duplicate requests.

### 10.9 URL state

Complete historical query state belongs in the URL.

Example:

```text
/logs?mode=history&server=1&app=nextup&service=api&levels=error,fatal&from=...&to=...&q=timeout
```

URLs must not contain:

- credentials;
- tokens;
- raw hidden metadata;
- CSRF tokens.

Browser back/forward must restore query state.

---

## 11. Live mode

### 11.1 Connection states

Visible textual states:

- Connecting;
- Live;
- Paused;
- Reconnecting;
- Disconnected;
- Truncated.

Use a small indicator plus text. Do not use a pulsing animation continuously.

### 11.2 Live controls

Required:

- Pause/Resume;
- Jump to newest;
- source filters;
- level filters;
- stream filters;
- clear browser view, which does not delete persisted logs;
- connection status.

Label browser-only clear action carefully:

> Clear view

Never label it `Clear logs`.

### 11.3 Auto-scroll

When the viewport is at or near the bottom:

- append events;
- maintain bottom position.

When the user scrolls upward:

- stop forced scrolling;
- continue receiving into bounded pending buffer;
- show floating/new-events control:

```text
↓ 143 new events
```

Selecting it:

- renders pending events as allowed;
- jumps to newest;
- resumes bottom tracking.

### 11.4 Browser limits

Initial limits:

- 1,000 rendered rows;
- 2,000 pending rows.

When rendered limit is exceeded:

- remove oldest browser rows;
- retain persisted server history;
- show a subtle notice if context was truncated.

When pending limit is exceeded:

- close and reconnect the subscription from “now”;
- state clearly:

> Live view was truncated while you were scrolled away. Use History to inspect the full interval.

### 11.5 Pause semantics

Pause affects browser rendering/subscription behavior according to implementation, not server persistence.

Preferred behavior:

- EventSource may remain connected;
- bounded pending buffer continues;
- status says Paused;
- no implication that ingestion stopped.

Copy:

> Live view paused. Logs are still being stored.

### 11.6 Reconnection

On disconnect:

- show Reconnecting;
- do not clear visible rows;
- use native reconnect behavior;
- after reconnection, always indicate a possible gap because version one does not replay;
- direct the user to History using an absolute range covering the gap.

---

## 12. Log event row

### 12.1 Default anatomy

Compact example:

```text
20:41:07.283  ERROR  nextup/api  Database connection failed
```

Recommended columns:

1. expansion control;
2. timestamp;
3. normalized level;
4. source/service summary;
5. message preview;
6. optional stream indicator;
7. actions on focus/hover.

### 12.2 Timestamp

Default row shows local time with milliseconds:

```text
20:41:07.283
```

When range spans multiple days, include date:

```text
27 Jul 20:41:07.283
```

Expanded detail shows:

- full event timestamp with timezone;
- UTC representation where useful;
- received timestamp;
- delivery delay.

### 12.3 Severity treatment

Use visible label and subtle indicator.

- `TRACE`: heavily muted;
- `DEBUG`: muted;
- `INFO`: neutral;
- `WARN`: amber label + subtle left indicator;
- `ERROR`: red label + subtle left indicator;
- `FATAL`: stronger red treatment, not flashing;
- `UNKNOWN`: outlined neutral label.

Do not color the entire row strongly.

### 12.4 Message preview

Default:

- one line in Compact density;
- up to two lines in Comfortable density;
- preserve visible whitespace sensibly;
- show truncation ellipsis;
- indicate multiline count when useful:

```text
NullPointerException …  +18 lines
```

### 12.5 Row selection

Keyboard-selected row gets a clear focus/selection outline distinct from hover.

Selection does not imply checkbox multi-select. Bulk event actions are not required.

### 12.6 Row actions

Available on focus/hover or expanded detail:

- Expand/Collapse;
- Copy message;
- Copy raw event;
- Copy request ID when present;
- Open source filter;
- Add message text to filter where useful.

Avoid action clutter in every row. Primary action is expansion.

---

## 13. Expanded event detail

### 13.1 Layout

Expand inline below the row.

Sections:

1. Message;
2. Source;
3. Timing;
4. Severity and stream;
5. Common structured fields;
6. Remaining attributes;
7. Raw payload;
8. Copy actions.

### 13.2 Source section

Show:

- server;
- project;
- environment;
- application;
- service;
- container name;
- container ID;
- alias and original name where different.

### 13.3 Timing section

Show:

- event time;
- received time;
- delivery delay;
- internal event ID only under advanced/detail information.

### 13.4 Attributes

Render key/value pairs safely.

Rules:

- stable key ordering for readability;
- nested objects collapsible;
- long values truncated with expansion;
- strings escaped;
- no clickable auto-linking of arbitrary content by default;
- request IDs and HTTP paths may have focused copy actions.

### 13.5 Raw payload

Display in a scrollable `<pre>` with syntax-neutral or very light safe highlighting.

Do not introduce a heavy syntax-highlighting library.

Large raw payload:

- cap initial browser rendering;
- provide “Show full payload” if within safe UI limit;
- provide raw download endpoint if too large;
- always show size.

### 13.6 Inline expansion behavior

- one or multiple rows may remain expanded;
- expansion state can be browser-only;
- loading details keeps row context;
- focus moves predictably;
- collapse returns focus to row control.

A side drawer is not the default because inline expansion preserves timeline context and is simpler on mobile.

---

## 14. Future deployment boundaries

This interaction is reserved for a post-dogfood candidate and must not appear in
version one. If implemented after an approved product change, show a subtle separator:

```text
──────── Container changed · 14:32:11 ────────
api-7f9d → api-a302
```

Copy must say `Container changed` or `Deployment boundary inferred`, not `Deployment successful`.

Markers:

- do not receive severity color;
- do not look like log rows;
- do not appear in export;
- do not affect event counts;
- may include tooltip explaining inference.

---

## 15. Sources page

### 15.1 Purpose

Manage discovered hierarchy and aliases without turning the page into an infrastructure inventory system.

### 15.2 Source list

Show grouped source tree or table with:

- display name;
- server;
- project/environment;
- service;
- active/inactive state;
- first seen;
- last seen;
- log count for recent retained interval only if inexpensive;
- alias indicator.

### 15.3 Source detail

Actions:

- open logs;
- set/remove alias;
- inspect container instances;
- clear logs;
- remove source.

### 15.4 Alias editing

Inline or small dialog.

Copy:

> Alias changes only how this source is displayed. Original metadata remains unchanged.

Validation errors remain inline.

### 15.5 Destructive actions

`Clear logs`:

> Delete all retained logs for **NextUp API** while keeping the source and alias.

Require typing display name.

`Remove source`:

> Delete retained logs, aliases, container history, and source metadata for **NextUp API**.

Stronger confirmation.

Never promise secure erasure.

---

## 16. Servers and ingestion tokens

### 16.1 Server list

Show:

- server name;
- active token status;
- token last used;
- source count;
- last event received;
- ingestion health.

### 16.2 Create server

Fields:

- display name;
- optional descriptive hostname;
- connection mode guidance: public HTTPS or private network.

After creation, prompt token creation.

### 16.3 Token creation

Token display screen:

> Copy this token now. Siftail stores only a hash and cannot show it again.

Provide:

- copy button;
- masked/unmasked toggle during this one screen;
- generated Fluent Bit configuration;
- generated curl test;
- explicit Done acknowledgement.

Do not redisplay token after leaving.

### 16.4 Token rotation

Flow:

1. create replacement token;
2. display once;
3. instruct update of source;
4. test new token;
5. revoke old token.

Do not force immediate old-token revocation unless chosen.

### 16.5 Revoke token

Ordinary confirmation is sufficient, but show impact:

> This server will stop sending logs with this token immediately.

### 16.6 Generated configuration

The generator asks for or derives:

- Siftail ingestion URL;
- public/private mode;
- token;
- TLS setting;
- optional source metadata;
- Siftail self-exclusion.

Present code in copyable block with warnings:

- token appears in the configuration;
- protect the Coolify configuration;
- restart/apply relevant resources;
- verify filesystem-buffer path behavior;
- exclude Siftail itself.

---

## 17. Guided ingestion test

### 17.1 Test stages

Display progress:

```text
1. Request received
2. Token authenticated
3. Event normalized
4. Event committed
5. Source discovered
```

Each stage has success/failure state and safe detail.

### 17.2 Failure copy examples

Invalid token:

> The request reached Siftail, but the ingestion token was rejected.

Malformed record:

> The request authenticated, but record 1 was not valid JSON.

Storage unavailable:

> The event was valid, but Siftail could not commit it to the database.

Never display raw authorization data.

---

## 18. Status page

### 18.1 Purpose

A compact appliance-status page, not a metrics dashboard.

### 18.2 Sections

#### Runtime

- version;
- uptime;
- architecture;
- UI/ingestion readiness.

#### Storage

- database size;
- WAL size;
- schema and SQLite version;
- configured size limit;
- oldest event;
- newest event;
- retention age;
- last cleanup.
- last bounded database-check result and time;
- a `Run safe database check` action that preserves the Status page, reports
  a concise success/failure notice, and never exposes database paths or raw
  SQLite errors.

#### Ingestion

- events accepted today by the current process;
- current recent rate;
- queued events;
- queued bytes;
- rejected batches;
- last successful ingest;
- last database error.

#### Live clients

- active SSE connections;
- slow/truncated client count if meaningful.

#### Backup

- last backup result;
- last verification;
- restore guidance.

#### Diagnostics

- latest sanitized operational events.
- each row shows time, textual severity, component, category, fixed safe
  summary, optional request ID, and optional recovery time;
- never accept or render arbitrary process-error text.

### 18.3 Visualization

Use text, progress bars, and small status blocks. Avoid time-series charts in version one.

Database usage may use a single horizontal capacity bar because it communicates a bounded resource directly.

### 18.4 Status severity

- Healthy;
- Attention;
- Degraded;
- Unavailable.

Always include text.

---

## 19. Settings page

### 19.1 Runtime settings

Editable:

- retention days;
- maximum database size;
- audit retention days;
- selected export limits if exposed.

Read-only process configuration:

- data directory category;
- UI listener;
- ingestion listener;
- public URL;
- authentication mode;
- forwarded-header trust networks;
- log format/level;
- request and queue limits.

### 19.2 Restart-required values

Read-only values configured through environment variables show:

> Requires container configuration change and restart.

Do not pretend they can be hot-reloaded.

### 19.3 Validation

Server-side errors appear next to field and in a summary when needed.

Examples:

> Retention must be a whole number from 1 to 3,650 days.

> Maximum database size must be a whole number from 1 to 1,024 GiB.

Both fields are submitted as one retention-policy change. A field error keeps
both submitted values visible, focuses the first invalid field, and does not
change either stored value.

### 19.4 Save behavior

- localized progress;
- current page remains visible;
- success notification:

> Retention settings saved.

- no full-page spinner.

---

## 20. Audit page

### 20.1 Presentation

Dense chronological list with filters for category and outcome.

Fields:

- time;
- action;
- outcome;
- actor/context;
- safe summary;
- request ID when relevant.

### 20.2 Security

Audit details never reveal:

- passwords;
- hashes;
- tokens;
- authorization headers;
- raw application payloads.

### 20.3 Retention notice

Display:

> Security audit events are retained separately from application logs.

---

## 21. Backup and recovery interface

### 21.1 Browser role

The browser may:

- explain commands;
- show last backup result;
- initiate safe backup if designed;
- verify an accessible server-side backup path where appropriate.

Restore remains primarily CLI because it requires stopping ingestion and replacing the active database.

### 21.2 Backup actions

Options:

- Full backup;
- Configuration-only backup.

Explain:

> Full backups include retained application logs. Configuration-only backups exclude
> application logs, diagnostics, and audit history. All backups exclude browser
> sessions, so restore requires a new sign-in.

The Backup workspace starts at most one asynchronous operation and shows typed
process-local state:

- no backup run;
- full backup in progress with copied/total SQLite pages;
- configuration-only backup in progress with copied/total configuration rows;
- read-only artifact verification in progress;
- backup verified with safe basename, byte size, and SHA-256;
- canceled before publication; or
- failed with destination-capacity/permission guidance and no raw path or
  SQLite error.

While running, the output input and action are disabled and only the focused
region polls once per second. Existing page content remains visible. The
server-side output path is never echoed after submission; only its safe
basename may appear in the result and security audit.

Configuration-only copy states explicitly that it includes credential hashes,
Servers, settings, and source presentation configuration; excludes logs,
container observations, audit/diagnostic history, and sessions; and restores by
replacement with empty history rather than merging. Verification copy states
that it is read-only and checks type, compatibility, integrity, exclusions,
permissions, and checksum without restoring.

### 21.3 Restore warning

Browser documentation/status copy:

> Restoring replaces the active database and requires the Siftail server to be
> stopped. Run `siftail restore --confirm RESTORE /path/to/backup.sqlite` on the
> host. Siftail preserves one verified `siftail.db.rollback` artifact and always
> requires a fresh sign-in.

The browser provides explanation only, not a restore form. It tells the
operator to copy the managed rollback to another protected path before using
that copy as a recovery artifact.

### 21.4 Destructive confirmation

Restore requires the exact case-sensitive CLI confirmation `--confirm RESTORE`.
No browser restore path exists in version one; if one is introduced later, it
also requires typing `RESTORE` and re-entering the administrator password.

---

## 22. Export experience

### 22.1 Formats

- Plain text;
- NDJSON.

### 22.2 Export dialog

Show:

- source scope;
- time range;
- active filters;
- selected format;
- estimated or maximum event count where available;
- warning for large export.

Copy:

> Exports contain the complete matching result set up to the configured export limit, not only the rows currently loaded.

### 22.3 Large export

Require explicit confirmation.

During streaming:

- button shows progress state;
- cancellation is possible through browser download behavior;
- UI does not freeze.

---

## 23. Loading states

### 23.1 Principle

Preserve existing content and show localized progress.

### 23.2 History filter loading

- existing rows remain visible;
- result region gets subtle busy treatment;
- small indicator near result summary;
- `aria-busy=true` on target region;
- controls remain usable except conflicting action.

### 23.3 Pagination loading

`Load older` changes to:

> Loading…

Only the pagination control is disabled.

### 23.4 Live connection

Use static status text and subtle indicator. Avoid a full-page spinner.

### 23.5 Button actions

Disable only the active action button. Do not disable unrelated navigation unless consistency requires it.

---

## 24. Empty states

### 24.1 No sources

> No log sources have been discovered yet.
>
> Create a server token and connect a Coolify or Fluent Bit log drain.

Actions:

- Create server;
- View setup guide.

### 24.2 No logs in range

> No logs arrived for this source during the selected time range.

Action:

- Last 24 hours;
- Switch source.

### 24.3 No filter match

> No logs matched these filters.
>
> Try a longer time range or remove a message filter.

### 24.4 Empty live view

> Waiting for new events from this source.

Show connection status and link to History.

### 24.5 No audit events

> No security audit events match this range.

No decorative illustration required.

---

## 25. Error states

### 25.1 Contextual first

Errors belong near the failed component.

Examples:

- filter query failed: within result region;
- alias validation: next to alias field;
- token generation: in token panel;
- backup failure: in backup section;
- session expiry: global redirect/login notice.

### 25.2 Global notifications

Use restrained notifications for completed actions or nonlocal problems.

Success examples:

- Token created;
- Alias updated;
- Backup verified;
- Settings saved.

Error notifications should include a path to details, not only “Failed.”

### 25.3 Request ID

Unexpected errors show:

> Request ID: `01…`

This assists diagnosis without revealing stack traces.

### 25.4 Database unavailable

History error:

> Logs could not be read because the database is temporarily unavailable. Existing filters were preserved.

### 25.5 Session expired

After redirect to login:

> Your session expired. Sign in again to continue.

Preserve a safe return path to the previous page.

---

## 26. Notifications

Use a small notification region.

Rules:

- screen-reader accessible;
- success auto-dismisses after a reasonable duration;
- critical error remains until acknowledged or resolved;
- no stacked flood;
- newest replaces or groups repeated equivalent messages;
- no bouncing or celebratory animation.

---

## 27. Visual system

### 27.1 CSS architecture

Use semantic custom properties and small component classes.

Suggested token categories:

```css
:root {
  --surface-base: ...;
  --surface-raised: ...;
  --surface-overlay: ...;
  --surface-hover: ...;
  --surface-selected: ...;

  --text-primary: ...;
  --text-secondary: ...;
  --text-muted: ...;
  --text-inverse: ...;

  --border-subtle: ...;
  --border-strong: ...;

  --accent-primary: ...;
  --accent-secondary: ...;

  --status-info: ...;
  --status-warning: ...;
  --status-error: ...;
  --status-fatal: ...;
  --status-success: ...;

  --focus-ring: ...;

  --space-1: ...;
  --space-2: ...;
  --radius-sm: ...;
  --shadow-overlay: ...;
}
```

Do not hard-code literal colors across components.

### 27.2 Color direction

Brand:

- muted cyan/blue primary;
- restrained warm amber secondary.

Semantics:

- warning uses brighter amber/orange distinct from brand secondary;
- error/fatal use red family;
- success uses green;
- info remains neutral/cyan only where not confused with brand interaction.

### 27.3 Dark theme

Dark-first default for new browser unless system preference or explicit product default dictates otherwise.

Use dark neutral surfaces, not pure black everywhere.

Ensure:

- long log reading is comfortable;
- muted text still meets contrast requirements;
- warning/error labels remain readable;
- selected/focused states are distinguishable.

### 27.4 Light theme

Fully supported, not an afterthought.

Avoid excessively bright white expanses; use subtle neutral surfaces and borders.

### 27.5 Theme preference

Options:

- System;
- Dark;
- Light.

Store in browser local storage.

No server round trip required.

---

## 28. Typography

### 28.1 UI stack

```css
font-family:
  system-ui,
  -apple-system,
  BlinkMacSystemFont,
  "Segoe UI",
  sans-serif;
```

### 28.2 Log stack

```css
font-family:
  ui-monospace,
  "SFMono-Regular",
  Consolas,
  "Liberation Mono",
  monospace;
```

### 28.3 Use of monospace

Use monospace for:

- log messages;
- timestamps;
- IDs;
- code/configuration;
- technical metadata.

Use sans-serif for:

- navigation;
- labels;
- descriptions;
- buttons;
- forms;
- notices.

Do not use monospace for every UI element.

### 28.4 Font sizing

Recommended baseline:

- UI body around 14–16px depending on density;
- compact log rows around 12.5–14px;
- minimum readable mobile size;
- no user-controlled arbitrary font-size matrix in version one.

---

## 29. Density

Two modes:

- Compact, default;
- Comfortable.

Stored browser-locally.

Compact:

- single-line preview;
- smaller vertical padding;
- optimized for desktop investigation.

Comfortable:

- up to two-line preview;
- larger targets and spacing;
- useful on smaller screens or for readability.

Density affects presentation only, not query size.

---

## 30. Native and reusable components

Prefer semantic native elements:

- `<button>`;
- `<input>`;
- `<select>`;
- `<dialog>`;
- `<details>` where semantics fit;
- `<table>` only for truly tabular data;
- `<fieldset>`;
- `<form>`.

Small reusable template components:

- Button;
- Icon button;
- Form field;
- Select/multiselect pattern;
- Badge;
- Severity label;
- Notice/banner;
- Tabs;
- Dialog;
- Dropdown/menu;
- Pagination;
- Empty state;
- Status indicator;
- Code block with copy;
- Log row;
- Event detail section.

Do not import a complete UI framework.

---

## 31. Icons

Use a very small local set of inline SVG icons.

Requirements:

- decorative icons hidden from assistive technology;
- icon-only buttons have accessible labels and tooltips;
- consistent stroke/size;
- no external icon font;
- no large dependency solely for icons.

Severity text remains visible even if icons are used.

---

## 32. Keyboard interaction

### 32.1 Global shortcuts

Suggested:

| Shortcut | Action |
|---|---|
| `/` or `Ctrl/Cmd+K` | Open source switcher |
| `F` | Focus message search |
| `L` | Switch to Live |
| `H` | Switch to History |
| `Space` | Pause/resume Live when log workspace owns focus |
| `J` | Move to next log row |
| `K` | Move to previous log row |
| `Enter` | Expand selected row |
| `Esc` | Close overlay or clear active selection as appropriate |
| `G` | Jump to newest live event |
| `?` | Open keyboard help |

### 32.2 Shortcut safety

- never fire while typing in input, textarea, select, or editable content;
- do not override browser conventions casually;
- every action has a visible clickable equivalent;
- use `event.key` carefully across keyboard layouts;
- show shortcuts in help;
- allow future disabling if accessibility feedback requires it.

### 32.3 Row navigation

Use roving tabindex or a well-defined focus model.

Do not trap keyboard focus inside the log list.

---

## 33. Accessibility

### 33.1 Target

WCAG 2.2 AA design target.

### 33.2 Required practices

- semantic landmarks and headings;
- visible focus rings;
- sufficient contrast;
- no color-only meaning;
- labels for every form control;
- errors associated with fields;
- target sizes appropriate for touch where possible;
- keyboard-operable dialogs and menus;
- correct focus restoration;
- skip link to main content;
- status announcements using appropriate live regions;
- reduced-motion support;
- no automatic focus stealing when logs arrive.

### 33.3 Live-region restraint

Do not announce every incoming log to screen readers. That would be unusable.

Announce only:

- connection state changes;
- pause/resume;
- new-event count when user is scrolled away, with throttling;
- truncation;
- critical errors;
- successful administrative actions.

### 33.4 Dialogs

Use native `<dialog>` where browser support and behavior are acceptable, with tested focus handling.

Requirements:

- initial focus on meaningful control;
- focus trapped while modal;
- Escape closes noncritical dialog;
- destructive confirmation cannot close accidentally during submission;
- focus returns to trigger.

### 33.5 Contrast and severity

Test both themes for:

- normal text;
- muted metadata;
- severity labels;
- focus rings;
- selected rows;
- banners;
- disabled controls.

### 33.6 Automated and manual checks

Automated:

- axe or equivalent in Playwright smoke tests;
- HTML validation where practical.

Manual:

- keyboard-only login and investigation;
- screen-reader smoke test for login, filters, row expansion, dialog;
- 200% zoom;
- reduced motion;
- dark/light high contrast review;
- mobile touch targets.

---

## 34. Responsive behavior

### 34.1 Desktop first

Desktop is primary for dense investigations.

At wide widths:

- filter bar can occupy one or two rows;
- source context visible;
- timestamp, level, source, and message align consistently;
- expanded details use multi-column metadata where readable.

### 34.2 Tablet/narrow desktop

- filters wrap into grouped rows;
- low-priority source dimensions collapse;
- navigation compacts;
- message remains primary;
- expanded details use one or two columns.

### 34.3 Mobile emergency support

Required:

- sign in;
- select source;
- view recent errors;
- change time preset;
- search;
- expand event;
- copy message/raw event;
- switch Live/History;
- pause/resume;
- see status warning.

Mobile row layout may stack:

```text
20:41:07.283  ERROR
nextup/api
Database connection failed
```

Horizontal scrolling should be limited to raw code/payload regions, not the whole page.

### 34.4 Not required on mobile

- full desktop density;
- complex multi-column filter layout;
- separate mobile app;
- offline behavior;
- PWA installation.

---

## 35. Motion

Use minimal functional transitions:

- short detail expansion;
- subtle dropdown appearance;
- smooth jump to newest;
- brief notification entry;
- restrained loading indicator.

Avoid animation on every arriving event. High-rate live rows should appear without expensive transitions.

Respect:

```css
@media (prefers-reduced-motion: reduce) {
  /* Remove nonessential transitions and smooth scrolling. */
}
```

No continuous pulsing for Live state. A stable dot and text are sufficient.

---

## 36. Content and microcopy guidelines

### 36.1 Terminology

Use:

- event or log event;
- source;
- server;
- application;
- service;
- container;
- History;
- Live;
- Clear view;
- Clear logs;
- Remove source;
- ingestion token;
- retention;
- database size.

Avoid ambiguous terms:

- stream when referring to a source hierarchy;
- delete when action only clears browser view;
- deploy when only container transition is inferred;
- secure delete;
- archive when retention is bounded recent history.

### 36.2 Privacy copy

Good:

> Siftail has no telemetry and does not send logs to an external service.

Good:

> Anything your applications write to logs is stored. Siftail does not redact content in this version.

Avoid:

> Your logs are completely secure.

### 36.3 Performance copy

Use concrete claims backed by architecture:

- one container;
- local SQLite;
- bounded retention;
- no external services.

Avoid unverified superlatives such as “blazing fast” in technical docs.

### 36.4 Destructive copy

State exactly what is deleted and what remains.

### 36.5 Time copy

Use absolute times in details and concise local time in rows. Avoid ambiguous relative-only copy for audit and destructive operations.

---

## 37. Browser-local state

Allowed browser-local preferences:

- theme;
- density;
- last selected source;
- last History/Live mode;
- last query URL or safe filter state;
- keyboard-help dismissed state;
- optional recent searches later.

Do not store:

- ingestion tokens;
- session tokens outside secure cookie;
- passwords;
- raw log payload caches beyond active DOM/session;
- CSRF secrets in local storage if a safer cookie/DOM strategy exists.

---

## 38. HTMX interaction conventions

### 38.1 Fragment boundaries

Good fragment targets:

- log rows region;
- load-older region;
- event details;
- settings section;
- alias form;
- status subsection.

Avoid replacing the entire application shell for ordinary actions.

### 38.2 History updates

Selectors update immediately. Text search is debounced. HTMX updates URL state.
Disable HTMX history snapshot caching. Authenticated pages and fragments use
`Cache-Control: no-store`, so Back and Forward navigation refetches authorized state.

### 38.3 Error handling

Server returns contextual error fragments for expected validation/query errors.

Unexpected errors use a generic safe fragment with request ID.

### 38.4 Focus

After fragment replacement:

- do not reset focus unnecessarily;
- preserve search input focus;
- announce result update concisely;
- keep scroll position stable unless action explicitly changes it.

---

## 39. JavaScript live-module constraints

The live module must:

- be small and framework-free;
- own one EventSource lifecycle;
- disconnect cleanly on navigation;
- bound arrays and DOM nodes;
- avoid memory leaks from listeners;
- escape/render through DOM text APIs or trusted server fragments;
- never use `innerHTML` with raw event content;
- pause without implying ingestion stopped;
- handle reconnect and truncation control events;
- respect reduced motion;
- be covered by browser tests.

---

## 40. Page-specific acceptance criteria

### 40.1 Logs

- usable with keyboard;
- query reflected in URL;
- source and mode always visible;
- no full-page spinner;
- deterministic row order;
- expanded content escaped;
- Live does not steal scroll;
- browser caps enforced.

### 40.2 Sources

- aliases clearly presentation-only;
- inactive sources identifiable;
- destructive actions distinct;
- source hierarchy understandable.

### 40.3 Servers

- token shown once;
- generated configuration copyable;
- test flow clear;
- token impact explained.

### 40.4 Status

- no sensitive values;
- storage and degraded state obvious;
- no decorative dashboard charts;
- useful recovery direction.

### 40.5 Settings

- restart-required values labeled;
- validation authoritative on server;
- secrets never shown.

### 40.6 Audit

- security-relevant but sanitized;
- separate retention explained;
- filterable without exposing raw logs.

---

## 41. Design QA checklist

Before merging a UI change:

- Does it preserve the operational-console character?
- Does it add visual noise or unnecessary card layout?
- Is the current source and mode still clear?
- Is every action keyboard accessible?
- Is focus visible and restored?
- Does it work in dark and light themes?
- Does it work with reduced motion?
- Does it rely on color alone?
- Are log contents escaped?
- Does it introduce an external asset or font?
- Does it add a JavaScript state system overlapping HTMX?
- Does mobile emergency use remain possible?
- Are loading and failure states defined?
- Is destructive copy exact?
- Is privacy copy honest?
- Does high-rate live rendering remain stable?

---

## 42. Initial interface copy set

### Product introduction

> Siftail is a fast, private log viewer for self-hosted apps. Sift through recent history and tail live events without sending operational data to an external service.

### No telemetry

> No telemetry. No external log storage. No subscription.

### Token warning

> Copy this token now. Siftail stores only a hash and cannot show it again.

### Live paused

> Live view paused. Logs are still being stored.

### Live truncated

> Live view was truncated while you were away from the newest event. Use History to inspect the complete interval.

### No matches

> No logs matched these filters. Try a longer time range or remove a message filter.

### Disk full

> Ingestion is unavailable because the database cannot write to storage. Existing logs remain available.

### Alias help

> Aliases change display names only. Original source metadata remains unchanged.

### Redaction limitation

> Siftail stores what your applications emit. Log-content redaction is not enabled in this version.

### Deletion limitation

> Deleted records may remain in backups, filesystem snapshots, or storage free space. Siftail does not claim forensic erasure.

---

## 43. Final design summary

The Siftail interface should make recent log investigation feel direct:

> Choose a source, bound the time range, filter the noise, expand the evidence, or switch to Live and follow events without losing control of the viewport.

Every design choice should serve that workflow while preserving privacy, speed, accessibility, and visual restraint.
