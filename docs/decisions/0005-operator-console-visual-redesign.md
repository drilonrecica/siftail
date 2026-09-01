# ADR 0005: Operator-Console Visual Redesign

**Status:** Accepted  
**Date:** 2026-09-02

## Context

The interface surface became complete before it became coherent. The Live
workspace was the last screen implemented against the original design
specification; Settings, Status, Audit, and Backup were added functionally on
borrowed page styles, Backup was never added to the specified information
architecture, and several specified interactions (source quick switcher,
keyboard help, shared notifications, a reusable component set) were never
built. The implementation also accumulated defects the specification does not
permit: page-global single-letter shortcuts that navigate away from unsaved
forms, empty states without their specified actions, forward-only pagination,
a dark-theme flash on light-preference machines caused by browser-local theme
application, and tab ARIA semantics on plain links.

A maintainer-approved redesign resolves this by replacing the visual system,
information architecture, keyboard model, and preference storage in one
planned milestone rather than by incremental patching.

## Decision

Adopt the **operator-console redesign** as the authoritative interface
direction, specified in full in `DESIGN.md`:

- **Character:** keyboard-first operational console — calm, dense, precise;
  the existing bans on decorative and terminal-kitsch styling remain.
- **Visual identity:** cold graphite-blue dark-first surfaces with a
  desaturated periwinkle-indigo primary accent. Severity colors are reserved
  for meaning and never share the accent hue. A complete light theme remains
  mandatory. This supersedes the previous muted-cyan/amber identity.
- **Theming implementation:** each semantic color is defined exactly once
  using CSS `light-dark()`, with static dark fallbacks declared first for
  browsers below the supported floor (baseline support mid-2024; older
  browsers receive the complete dark theme). The duplicated per-theme token
  blocks are removed.
- **Typography:** a subsetted open-source monospace face (JetBrains Mono,
  OFL) is vendored into the embedded assets and served same-origin, used for
  log content, timestamps, severity codes, identifiers, and secrets; UI
  chrome remains on system stacks. Severity renders as fixed-width
  three-letter text codes. Vendoring an embedded font is compatible with the
  privacy invariant (no external fonts, scripts, or CDNs) because no request
  leaves the origin; `AGENTS.md` §11.8 is amended to say so explicitly.
- **Preferences:** theme and density move from browser `localStorage` to a
  server-readable cookie so the server renders `data-theme`/`data-density`
  before first paint. This removes the theme flash, works without JavaScript,
  and keeps the preference non-secret. This supersedes the browser-local
  storage decision in the previous `DESIGN.md` §27.5/§37.
- **Information architecture:** Logs, Sources, and Servers form the primary
  navigation; Settings, Status, Audit, and Backup group under a System menu;
  a header health indicator links to Status. Backup joins the specified IA.
- **Interaction model:** shortcuts are scoped per workspace with only
  `?`, `Esc`, and `Ctrl/Cmd+K` global; a command palette provides source
  switching, navigation, and page actions; a keyboard-help overlay lists
  active bindings; row navigation works in both History and Live.
- **Signature element:** the tail line — a rendered boundary marking the
  newest edge of a log list, expressing the sift/tail identity functionally
  (Live: the "now" edge; History: the load-newer/go-live edge).

## Alternatives

- Incremental restyling of individual screens preserves inconsistency and
  leaves the specification untruthful; it was the path that produced the
  current unlevel state.
- Adopting a frontend framework or CSS framework would violate accepted
  architecture invariants and add a build pipeline without addressing the
  actual defects, which are design and specification debt.
- Keeping the cyan/amber identity was viable but retained the category's
  default dashboard look and an unused secondary accent; the maintainer chose
  a fully open identity revision.
- Keeping system monospace stacks avoids binary growth but renders the log
  surface differently on every operating system and weakens the typographic
  identity of the product's core surface.
- Keeping `localStorage` preferences avoids a cookie but cannot eliminate the
  first-paint theme flash and fails without JavaScript.

## Consequences

- `DESIGN.md` is rewritten as the authoritative specification of the new
  system; `AGENTS.md` §11.8 and §12 are amended in the same change.
- The redesign is tracked as milestone `0.6.0` (SFT-064–SFT-081). The scope
  of SFT-056 is delivered by this milestone.
- The embedded binary grows by the subsetted font size (tens of kilobytes);
  the delta is recorded per the existing image-size discipline.
- Browser tests are updated in lockstep per task; navigation and
  theme-control selectors change once behind shared test helpers.
- A one-time visual break with all previous screenshots and documentation
  imagery is accepted.

## Migration and compatibility impact

No schema migration. The preference cookie is additive; existing
`localStorage` values are ignored and harmless. URL query contracts for
History remain canonical and unchanged (the CSV level/stream serialization is
preserved while repeated form parameters become accepted on parse). Browsers
below the `light-dark()` floor receive the complete dark theme via declared
fallbacks. Reversing this decision requires a superseding ADR.
