import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { expect, request as playwrightRequest, test, type Page } from "@playwright/test";
import {
  administratorPassword,
  administratorUsername,
  closeSystemMenu,
  navigateSystem,
  openSystemMenu,
  readState,
  setShortcuts,
  setTheme,
  siftailEnvironment,
} from "./support";

test.describe.configure({ mode: "serial" });

async function login(page: Page): Promise<void> {
  await page.goto("/login");
  await expect(page.locator("#username")).toBeFocused();
  await page.locator("#username").fill(administratorUsername);
  await page.locator("#password").fill(administratorPassword);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/logs\?.*mode=history/);
  await expect(page.locator(".log-row")).toHaveCount(200);
}

let liveToken = "";

function getLiveToken(): string {
  if (liveToken) return liveToken;
  const state = readState();
  const output = execFileSync(
    state.binary,
    ["token", "create", "--server", "1", "--name", "browser-live"],
    {
      env: siftailEnvironment({ SIFTAIL_DATA_DIR: state.dataDirectory }),
      encoding: "utf8",
    },
  );
  liveToken = output.match(/^token \(shown once\): (.+)$/m)?.[1] ?? "";
  if (!liveToken) throw new Error("Live test token was not returned");
  return liveToken;
}

async function ingestLive(messages: string[]): Promise<void> {
  await ingestSource(messages, "api", "web", "api-container", "api-1");
}

async function ingestSource(
  messages: string[],
  application: string,
  service: string,
  containerID: string,
  containerName: string,
): Promise<void> {
  const state = readState();
  const now = Date.now();
  const response = await fetch(`http://${state.ingestAddress}/api/v1/ingest`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${getLiveToken()}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(messages.map((message, index) => ({
      timestamp: new Date(now + index).toISOString(),
      project: "browser-project",
      environment: "test",
      application,
      service,
      container_id: containerID,
      container_name: containerName,
      stream: index % 2 === 0 ? "stderr" : "stdout",
      level: index % 3 === 0 ? "ERROR" : "INFO",
      log: message,
    }))),
  });
  if (response.status !== 204) {
    throw new Error(`Live fixture ingestion returned HTTP ${response.status}`);
  }
}

// Ingests events for one browser-project/test application at fixed
// timestamps, one millisecond apart from baseMs, oldest first.
async function ingestAt(
  application: string,
  count: number,
  baseMs: number,
  label = application,
): Promise<void> {
  const state = readState();
  for (let start = 0; start < count; start += 100) {
    const size = Math.min(100, count - start);
    const response = await fetch(`http://${state.ingestAddress}/api/v1/ingest`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${getLiveToken()}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(Array.from({ length: size }, (_, offset) => ({
        timestamp: new Date(baseMs + start + offset).toISOString(),
        project: "browser-project",
        environment: "test",
        application,
        service: "web",
        stream: "stdout",
        level: "INFO",
        log: `${label}-${start + offset}`,
      }))),
    });
    if (response.status !== 204) {
      throw new Error(`fixture ingestion returned HTTP ${response.status}`);
    }
  }
}

function historyScopeURL(application: string, fromMs: number, toMs: number): string {
  const values = new URLSearchParams({
    mode: "history",
    server: "1",
    project: "browser-project",
    environment: "test",
    application,
    service: "web",
    from: new Date(fromMs).toISOString(),
    to: new Date(toMs).toISOString(),
    direction: "older",
    limit: "200",
  });
  return `/logs?${values.toString()}`;
}

// Holds matching requests until release() so a test can act while they are
// in flight.
async function holdRequests(page: Page, pattern: RegExp): Promise<{
  release: () => void;
  held: () => number;
}> {
  let waiting: Array<() => void> = [];
  let count = 0;
  let open = false;
  await page.route(pattern, async (route) => {
    count += 1;
    if (open) {
      await route.continue();
      return;
    }
    await new Promise<void>((resolve) => waiting.push(resolve));
    await route.continue().catch(() => undefined);
  });
  return {
    release: () => {
      open = true;
      waiting.forEach((resolve) => resolve());
      waiting = [];
    },
    held: () => count,
  };
}

async function ingestWithToken(token: string, application: string): Promise<number> {
  const state = readState();
  const response = await fetch(`http://${state.ingestAddress}/api/v1/ingest`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify([{
      timestamp: new Date().toISOString(),
      project: "browser-project",
      environment: "test",
      application,
      service: "web",
      stream: "stdout",
      level: "INFO",
      log: "browser managed token check",
    }]),
  });
  return response.status;
}

async function trackEventSources(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const NativeEventSource = window.EventSource;
    const stats = { active: 0, created: 0, closed: 0 };
    class TrackedEventSource extends NativeEventSource {
      private trackedClosed = false;

      constructor(url: string | URL, init?: EventSourceInit) {
        super(url, init);
        stats.active += 1;
        stats.created += 1;
      }

      override close(): void {
        if (!this.trackedClosed) {
          this.trackedClosed = true;
          stats.active -= 1;
          stats.closed += 1;
        }
        super.close();
      }
    }
    Object.defineProperty(window, "EventSource", {
      configurable: true,
      value: TrackedEventSource,
    });
    Object.defineProperty(window, "__siftailEventSourceStats", { value: stats });
  });
}

test("login, canonical History default, security headers, and logout", async ({ page }) => {
  const loginResponse = await page.goto("/login");
  expect(loginResponse?.headers()["content-security-policy"]).toContain("default-src 'self'");
  expect(loginResponse?.headers()["x-frame-options"]).toBe("DENY");
  await page.locator("#username").fill(administratorUsername);
  await page.locator("#password").fill(administratorPassword);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/logs\?.*mode=history/);
  const url = new URL(page.url());
  expect(url.searchParams.get("from")).toMatch(/Z$/);
  expect(url.searchParams.get("to")).toMatch(/Z$/);
  expect(url.searchParams.get("limit")).toBe("200");
  await openSystemMenu(page);
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/login$/);
  await page.goto("/logs");
  await expect(page).toHaveURL(/\/login\?return=/);
});

test("primary filters update and restore URL-owned History state", async ({ page }) => {
  await login(page);
  await page.locator("#server-filter").selectOption("1");
  await expect(page).toHaveURL(/server=1/);
  await page.locator("#project-filter").selectOption("browser-project");
  await expect(page).toHaveURL(/project=browser-project/);
  await page.locator("#environment-filter").selectOption("test");
  await page.locator("#application-filter").selectOption("api");
  await page.locator("#service-filter").selectOption("web");
  await expect(page).toHaveURL(/service=web/);

  await page.getByRole("button", { name: "Errors" }).click();
  await expect(page).toHaveURL(/levels=error%2Cfatal/);
  await page.locator("#stream-stdout").uncheck();
  await expect(page).toHaveURL(/streams=stderr%2Cunknown/);
  await page.locator("#stream-unknown").uncheck();
  await expect(page).toHaveURL(/streams=stderr/);
  await page.locator("#contains-filter").fill("needle-hostile");
  await expect(page).toHaveURL(/contains=needle-hostile/);
  await expect(page.locator(".log-row")).toHaveCount(1);
  await expect(page.locator(".row-message")).toContainText("needle-hostile");

  const filteredURL = page.url();
  await page.getByRole("link", { name: "24h" }).click();
  await expect(page).not.toHaveURL(filteredURL);
  await page.goBack();
  await expect(page).toHaveURL(filteredURL);
  await expect(page.locator("#contains-filter")).toHaveValue("needle-hostile");
  await expect(page.locator(".log-row")).toHaveCount(1);
});

test("confirmed History export preserves filters, focus, themes, mobile, and expiry", async ({
  page,
}) => {
  await login(page);
  await page.locator("#contains-filter").fill("needle-hostile");
  await expect(page).toHaveURL(/contains=needle-hostile/);
  const open = page.getByRole("button", { name: "Export matching History" });
  await open.focus();
  await open.press("Enter");
  const dialog = page.getByRole("dialog", { name: "Export matching History" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText("needle-hostile", { exact: true })).toBeVisible();
  await expect(dialog.getByText(/100,000 events/)).toBeVisible();
  await expect(dialog.locator("select[name='format']")).toBeFocused();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(open).toBeFocused();

  await setTheme(page, "dark");
  await open.click();
  await dialog.locator("select[name='format']").selectOption("ndjson");
  await dialog.getByRole("checkbox").check();
  const downloadPromise = page.waitForEvent("download");
  await dialog.getByRole("button", { name: "Create download" }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toMatch(
    /^siftail-history-\d{8}T\d{6}Z-\d{8}T\d{6}Z\.ndjson$/,
  );
  expect(await download.failure()).toBeNull();
  await expect(page.locator("#history-export-status")).toContainText(
    "download starts only after generation and auditing succeed",
  );

  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  await open.click();
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await dialog.getByRole("button", { name: "Cancel" }).click();

  const state = readState();
  execFileSync(state.binary, ["sessions", "revoke-all"], {
    env: siftailEnvironment({ SIFTAIL_DATA_DIR: state.dataDirectory }),
    encoding: "utf8",
  });
  await open.click();
  await dialog.getByRole("checkbox").check();
  await dialog.getByRole("button", { name: "Create download" }).click();
  await expect(page).toHaveURL(/\/login\?.*expired=1/);
});

test("cursor pagination appends without replacing existing rows", async ({ page }) => {
  await login(page);
  const firstID = await page.locator(".log-row").first().getAttribute("data-event-id");
  await expect(page.getByRole("button", { name: "Load older" })).toBeVisible();
  await page.getByRole("button", { name: "Load older" }).click();
  await expect(page.locator(".log-row")).toHaveCount(220);
  await expect(page.locator(".log-row").first()).toHaveAttribute("data-event-id", firstID ?? "");
  await expect(page.locator(".pagination-end")).toContainText("End of matching events");

  // The tail line marks the newest edge; Load newer polls beyond the top
  // row and reports honestly when nothing newer has arrived.
  await expect(page.locator("#history-tail")).toBeVisible();
  await page.getByRole("button", { name: "Load newer" }).click();
  await expect(page.locator("#history-update-status")).toContainText(
    "No newer events yet.",
  );
  await expect(page.locator(".log-row")).toHaveCount(220);
  await expect(page.getByRole("button", { name: "Load newer" })).toBeVisible();
});

test("History filtering and pagination work without JavaScript", async ({ browser }) => {
  const context = await browser.newContext({ javaScriptEnabled: false });
  const page = await context.newPage();
  await page.goto("/login");
  await page.locator("#username").fill(administratorUsername);
  await page.locator("#password").fill(administratorPassword);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/logs\?.*mode=history/);
  await expect(page.locator(".log-row")).toHaveCount(200);

  // Named checkbox groups submit as repeated parameters the server accepts.
  await page.locator("#level-info").uncheck();
  await page.locator("#stream-stdout").uncheck();
  await page.locator("#contains-filter").fill("needle-hostile");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(page.locator(".log-row")).toHaveCount(1);
  await expect(page.locator(".row-message")).toContainText("needle-hostile");
  await context.close();
});

test("inline details remain hostile text and preserve focus and clipboard content", async ({
  context,
  page,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await login(page);
  const hostileRow = page.locator(".log-row").filter({ hasText: "needle-hostile" });
  const toggle = hostileRow.getByRole("button", { name: /^Details: \w{3} .*needle-hostile/ });
  await toggle.focus();
  await toggle.press("Enter");
  const detail = hostileRow.locator(".event-detail");
  await expect(detail).toBeFocused();
  await expect(detail).toContainText("<script>window.siftailXSS=true</script>");
  await expect(detail.locator("script")).toHaveCount(0);
  await expect(detail.locator("img")).toHaveCount(0);
  await expect(detail.locator('[id^="event-attributes-"]')).toContainText('"a"');
  expect(await page.evaluate(() => (window as Window & { siftailXSS?: boolean }).siftailXSS))
    .toBeUndefined();

  await detail.getByRole("button", { name: "Copy message" }).click();
  await expect(detail.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toContain(
    "<script>window.siftailXSS=true</script> needle-hostile first line\nsecond line",
  );
  await detail.getByRole("button", { name: "Collapse details" }).click();
  await expect(toggle).toBeFocused();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
});

// Counts detail fetches so a collapse can be shown not to re-request (UX-03).
function countDetailRequests(page: Page): () => number {
  let count = 0;
  page.on("request", (request) => {
    if (/\/logs\/events\/\d+/.test(new URL(request.url()).pathname)) count += 1;
  });
  return () => count;
}

const severityCode = "(TRC|DBG|INF|WRN|ERR|FTL|UNK)";

test("History rows are one roving tab stop with content-named toggles and collapse", async ({
  page,
}) => {
  await login(page);
  const list = page.getByRole("list", { name: "History log events" });
  const rows = list.getByRole("listitem");
  await expect(rows).toHaveCount(200);
  await expect(list).toMatchAriaSnapshot(`
    - list "History log events, times in UTC":
      - listitem:
        - 'button /^Details: ${severityCode} \\S+, .+/ [expanded=false]'
  `);

  // Toggles are named from content, never from the event ID, and none is a
  // tab stop of its own.
  const labels = await list.locator(".event-toggle").evaluateAll((toggles) =>
    toggles.map((toggle) => ({
      label: toggle.getAttribute("aria-label") ?? "",
      id: toggle.closest(".log-row")?.getAttribute("data-event-id") ?? "",
      tabIndex: (toggle as HTMLElement).tabIndex,
    })));
  for (const { label, id, tabIndex } of labels) {
    expect(label).toMatch(new RegExp(`^Details: ${severityCode} \\S+, .+`));
    expect(label).not.toContain(`event ${id}`);
    expect(tabIndex).toBe(-1);
  }

  // One tab stop: the newest (first) row. Tab enters it, the next Tab leaves
  // the list for Load older.
  await expect(list.locator('.log-row[tabindex="0"]')).toHaveCount(1);
  await expect(rows.first()).toHaveAttribute("tabindex", "0");
  await page.locator("[data-open-export]").focus();
  for (let press = 0; press < 5; press += 1) {
    await page.keyboard.press("Tab");
    if (await rows.first().evaluate((row) => row === document.activeElement)) break;
  }
  await expect(rows.first()).toBeFocused();
  await expect(rows.first()).toHaveAttribute("aria-label", /^Row 1 of 200: /);
  await page.keyboard.press("Tab");
  await expect(page.locator("#load-older")).toBeFocused();

  // Arrows and J/K move the selection and announce the position.
  await page.keyboard.press("Shift+Tab");
  await expect(rows.first()).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(rows.nth(1)).toBeFocused();
  await expect(rows.nth(1)).toHaveAttribute("aria-label", /^Row 2 of 200: \w{3}, /);
  await expect(rows.first()).not.toHaveAttribute("aria-label", /.*/);
  await page.keyboard.press("j");
  await expect(rows.nth(2)).toHaveAttribute("aria-label", /^Row 3 of 200: /);
  await page.keyboard.press("ArrowUp");
  await expect(rows.nth(1)).toBeFocused();
  await expect(list.locator('.log-row[tabindex="0"]')).toHaveCount(1);

  // Leaving and re-entering the list returns to the selected row.
  await page.keyboard.press("Tab");
  await page.keyboard.press("Shift+Tab");
  await expect(rows.nth(1)).toBeFocused();

  // Enter on the selected row expands; Enter on the expanded row collapses
  // without fetching again, and the row stays collapsed.
  const detailRequests = countDetailRequests(page);
  const row = rows.nth(1);
  const toggle = row.locator(".event-toggle");
  await page.keyboard.press("Enter");
  await expect(row.locator(".event-detail")).toBeFocused();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  expect(detailRequests()).toBe(1);
  await row.focus();
  await page.keyboard.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(row.locator(".event-detail")).toHaveCount(0);
  await expect(row).toBeFocused();
  await page.waitForLoadState("networkidle");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(detailRequests()).toBe(1);

  // Space on the selected row toggles too.
  await page.keyboard.press("Space");
  await expect(row.locator(".event-detail")).toBeVisible();
  await row.focus();
  await page.keyboard.press("Space");
  await expect(row.locator(".event-detail")).toHaveCount(0);

  // The chevron: click expands, click on the expanded chevron collapses.
  const before = detailRequests();
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(row.locator(".event-detail")).toBeVisible();
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(row.locator(".event-detail")).toHaveCount(0);
  await expect(toggle).toBeFocused();
  await page.waitForLoadState("networkidle");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(detailRequests()).toBe(before + 1);

  // Esc clears the selection: the tab stop returns to the newest row.
  await row.focus();
  await page.keyboard.press("Escape");
  await expect(rows.first()).toHaveAttribute("tabindex", "0");
  await expect(row).toHaveAttribute("tabindex", "-1");

  // The list is a widget, not a shortcut: arrows and Enter keep working with
  // single-key shortcuts off.
  await setShortcuts(page, "off");
  await rows.first().focus();
  await page.keyboard.press("ArrowDown");
  await expect(rows.nth(1)).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(rows.nth(1).locator(".event-detail")).toBeVisible();

  // axe at the top of the page, with a row expanded: scrolled down, the
  // sticky header partially covers the filter links and axe counts that as
  // a target-size failure.
  await page.evaluate(() => window.scrollTo(0, 0));
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
});

test("Live rows keep the roving tab stop on the newest or selected row and collapse", async ({
  page,
}) => {
  await login(page);
  await page.locator(".mode-switch").getByRole("link", { name: "Live" }).click();
  await expect(page.locator("[data-live-status]")).toHaveText("Live");
  const list = page.getByRole("list", { name: "Live log events" });
  await expect(page.locator("[data-live-empty]")).toBeVisible();

  await ingestLive(["live-roving-0", "live-roving-1\nsecond line", "live-roving-2"]);
  const rows = list.getByRole("listitem");
  await expect(rows).toHaveCount(3);
  await expect(page.locator("[data-live-empty]")).toBeHidden();
  await expect(list).toMatchAriaSnapshot(`
    - list "Live log events, times in UTC":
      - listitem:
        - 'button "Details: ERR api/web, live-roving-0" [expanded=false]'
      - listitem:
        - 'button "Details: INF api/web, live-roving-1…" [expanded=false]'
      - listitem:
        - 'button "Details: INF api/web, live-roving-2" [expanded=false]'
  `);

  // The newest row (the last, nearest the tail line) is the tab stop.
  await expect(list.locator('.log-row[tabindex="0"]')).toHaveCount(1);
  await expect(rows.last()).toHaveAttribute("tabindex", "0");
  await ingestLive(["live-roving-3"]);
  await expect(rows).toHaveCount(4);
  await expect(rows.last()).toHaveAttribute("tabindex", "0");
  await expect(list.locator('.log-row[tabindex="0"]')).toHaveCount(1);

  // A selected row keeps the tab stop while new events arrive.
  await rows.last().focus();
  await expect(rows.last()).toHaveAttribute("aria-label", /^Row 4 of 4: ERR, api\/web, live-roving-3/);
  await page.keyboard.press("ArrowUp");
  await expect(rows.nth(2)).toBeFocused();
  await expect(rows.nth(2)).toHaveAttribute("aria-label", /^Row 3 of 4: /);
  await ingestLive(["live-roving-4"]);
  await expect(rows).toHaveCount(5);
  await expect(rows.nth(2)).toHaveAttribute("tabindex", "0");
  await expect(rows.last()).toHaveAttribute("tabindex", "-1");

  // Enter in both states, then the chevron in both states.
  const detailRequests = countDetailRequests(page);
  const row = rows.nth(2);
  const toggle = row.locator(".event-toggle");
  await row.focus();
  await page.keyboard.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await row.focus();
  await page.keyboard.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(row.locator(".event-detail")).toHaveCount(0);
  await toggle.click();
  await expect(row.locator(".event-detail")).toBeVisible();
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(row.locator(".event-detail")).toHaveCount(0);
  await page.waitForTimeout(300);
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(detailRequests()).toBe(2);

  // Clear view empties the list and brings the empty state back.
  await page.getByRole("button", { name: "Clear view" }).click();
  await expect(rows).toHaveCount(0);
  await expect(page.locator("[data-live-empty]")).toBeVisible();
});

// The display zone's wall clock for an instant, as fixed 24-hour parts
// (DESIGN.md §12.2). Runs in Node with full ICU, independent of the page.
function zoneParts(instant: Date, zone: string): Record<string, string> {
  return Object.fromEntries(new Intl.DateTimeFormat("en-US", {
    timeZone: zone, hourCycle: "h23", year: "numeric", month: "2-digit",
    day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit",
    fractionalSecondDigits: 3,
  }).formatToParts(instant).map((part) => [part.type, part.value]));
}

function zoneWall(instant: Date, zone: string): string {
  const p = zoneParts(instant, zone);
  return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}:${p.second}`;
}

// Every <time> on the page as text, datetime, and whether its text overflows.
async function timeElements(page: Page, selector = "time") {
  return page.locator(selector).evaluateAll((elements) => elements.map((element) => ({
    text: element.textContent ?? "",
    datetime: element.getAttribute("datetime"),
    overflow: element.scrollWidth > element.clientWidth + 1,
  })));
}

test("times render in one labelled 24-hour zone under a 12-hour locale", async ({
  browser,
}) => {
  const zone = "America/Los_Angeles";
  const context = await browser.newContext({ locale: "en-US", timezoneId: zone });
  const page = await context.newPage();
  const rowFormat = /^\d{2}:\d{2}:\d{2}\.\d{3}$/;
  const expectRowsInZone = async (expectedZone: string, format: RegExp) => {
    const rows = await timeElements(page, "#history-rows .log-row time");
    expect(rows.length).toBeGreaterThan(0);
    for (const row of rows) {
      expect(row.text).toMatch(format);
      expect(row.text).not.toMatch(/AM|PM/i);
      expect(row.overflow).toBe(false);
      const p = zoneParts(new Date(row.datetime ?? ""), expectedZone);
      expect(row.text.endsWith(`${p.hour}:${p.minute}:${p.second}.${p.fractionalSecond}`))
        .toBe(true);
    }
  };
  try {
    // Default: UTC everywhere, labelled UTC, even though the browser is in
    // Los Angeles with a 12-hour locale; nothing is rewritten client-side.
    await login(page);
    await expect(page.locator("main")).toHaveAttribute("data-timezone", "UTC");
    await expect(page.locator("#history-tail .tail-zone")).toHaveText("Times in UTC");
    await expect(page.locator(".result-summary")).toContainText(/\d{2}:\d{2} UTC/);
    await expectRowsInZone("UTC", rowFormat);
    const fromLabel = page.locator(".time-filters label").first();
    await expect(fromLabel).toContainText("From (UTC)");

    // The System menu offers the browser's zone; choosing it reloads with
    // every time rendered by the server in that zone.
    await openSystemMenu(page);
    const zoneSelect = page.locator("[data-timezone-select]");
    await expect(zoneSelect.locator("option")).toHaveText([
      "UTC", `Browser time zone (${zone})`,
    ]);
    await zoneSelect.selectOption(zone);
    await expect(page.locator("main")).toHaveAttribute("data-timezone", zone);
    await expect(page.getByRole("list", { name: `History log events, times in ${zone}` }))
      .toBeVisible();
    await expect(page.locator("#history-tail .tail-zone")).toHaveText(`Times in ${zone}`);
    await expect(page.locator(".result-summary")).toContainText(`${zone}`);
    await expectRowsInZone(zone, rowFormat);
    await openSystemMenu(page);
    await expect(zoneSelect).toHaveValue(zone);
    await expect(zoneSelect.locator("option:checked")).toHaveText(`Browser time zone (${zone})`);
    await closeSystemMenu(page);

    // The custom-range picker is labelled with and interpreted in the zone;
    // the URL keeps UTC.
    await expect(fromLabel).toContainText(`From (${zone})`);
    const fromPicker = page.locator('.time-filters input[type="datetime-local"]').first();
    await expect(fromPicker).toHaveAttribute("aria-label", `From (${zone})`);
    const toParam = new URL(page.url()).searchParams.get("to") ?? "";
    const fromParam = new URL(page.url()).searchParams.get("from") ?? "";
    await expect(fromPicker).toHaveValue(zoneWall(new Date(fromParam), zone));
    const target = new Date(Math.floor((Date.parse(toParam) - 2 * 3600_000) / 1000) * 1000);
    await fromPicker.fill(zoneWall(target, zone));
    await expect.poll(() => Date.parse(new URL(page.url()).searchParams.get("from") ?? ""))
      .toBe(target.getTime());
    await expect(page.locator(".time-filters label").first()).toContainText(`From (${zone})`);

    // The export dialog's absolute range is in the zone and keeps UTC
    // datetime values.
    const exportTimes = await timeElements(page, ".export-scope time");
    expect(exportTimes).toHaveLength(2);
    for (const item of exportTimes) {
      expect(item.text).toMatch(new RegExp(`^\\d{2} \\w{3} \\d{4} \\d{2}:\\d{2}:\\d{2} ${zone}$`));
      expect(item.datetime).toMatch(/Z$/);
    }

    // Event detail shows the zone, UTC, the received time, and the delay.
    await page.locator("#history-rows .event-toggle").first().click();
    const detail = page.locator(".event-detail").first();
    await expect(detail.getByText(`Event time (${zone})`, { exact: true })).toBeVisible();
    await expect(detail.getByText("Event time (UTC)", { exact: true })).toBeVisible();
    await expect(detail.getByText(`Received time (${zone})`, { exact: true })).toBeVisible();
    await expect(detail.getByText("Delivery delay", { exact: true })).toBeVisible();
    expect((await timeElements(page)).filter((item) => !item.datetime)).toEqual([]);

    // A range longer than a day adds the date and the column still fits.
    await page.getByRole("link", { name: "7d", exact: true }).click();
    await expect(page.locator("#history-rows")).toHaveAttribute("data-show-date", "");
    await expectRowsInZone(zone, /^\d{2} \w{3} \d{2}:\d{2}:\d{2}\.\d{3}$/);
    for (const scheme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.screenshot({ path: `.playwright-artifacts/time-zone-history-desktop-${scheme}.png` });
      await page.setViewportSize({ width: 390, height: 844 });
      await expectRowsInZone(zone, /^\d{2} \w{3} \d{2}:\d{2}:\d{2}\.\d{3}$/);
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth))
        .toBe(false);
      await page.screenshot({ path: `.playwright-artifacts/time-zone-history-mobile-${scheme}.png` });
    }
    await page.setViewportSize({ width: 1280, height: 800 });
    await openSystemMenu(page);
    await page.screenshot({ path: ".playwright-artifacts/time-zone-system-menu-dark.png" });
    await closeSystemMenu(page);
    await page.emulateMedia({ colorScheme: "light" });

    // Live formats its rows in the same zone from data-timezone.
    await page.goto("/logs?mode=live");
    await expect(page.locator("[data-live-status]")).toHaveText("Live");
    await expect(page.locator(".live-tail .tail-zone")).toHaveText(`Times in ${zone}`);
    await ingestLive(["zone-live-0"]);
    const liveRow = page.locator("#live-rows .log-row").filter({ hasText: "zone-live-0" });
    await expect(liveRow).toHaveCount(1);
    await expect(page.getByRole("list", { name: `Live log events, times in ${zone}` }))
      .toBeVisible();
    const [liveTime] = await timeElements(page, "#live-rows .log-row time");
    expect(liveTime.text).toMatch(rowFormat);
    const liveParts = zoneParts(new Date(liveTime.datetime ?? ""), zone);
    expect(liveTime.text).toBe(
      `${liveParts.hour}:${liveParts.minute}:${liveParts.second}.${liveParts.fractionalSecond}`,
    );
    expect(liveTime.overflow).toBe(false);

    // Status and Audit name the zone and every <time> carries datetime.
    await navigateSystem(page, "Status");
    await expect(page.getByRole("heading", { name: "Status", exact: true })).toBeVisible();
    await expect(page.locator(".status-facts div").filter({ hasText: "Oldest event" })
      .locator("dd")).toHaveText(new RegExp(`^\\d{2} \\w{3} \\d{4} \\d{2}:\\d{2}:\\d{2} ${zone}$`));
    expect((await timeElements(page)).filter((item) => !item.datetime)).toEqual([]);
    await navigateSystem(page, "Audit");
    await expect(page.getByRole("columnheader", { name: `Time (${zone})` })).toBeVisible();
    const auditTimes = await timeElements(page, ".audit-table time");
    expect(auditTimes.length).toBeGreaterThan(0);
    for (const item of auditTimes) {
      expect(item.datetime).toMatch(/Z$/);
      expect(item.text).toMatch(/^\d{2} \w{3} \d{4} \d{2}:\d{2}:\d{2}$/);
    }
  } finally {
    await context.close();
  }
});

test("Live pause, pending, clear-view, filters, reconnect, focus, and safe rendering", async ({
  page,
}) => {
  await trackEventSources(page);
  await login(page);
  await page.locator(".mode-switch").getByRole("link", { name: "Live" }).click();
  await expect(page).toHaveURL(/mode=live/);
  await expect(page.locator("[data-live-status]")).toHaveText("Live");
  await expect(page.locator("[data-live-empty]")).toBeVisible();

  const hostile = "<img src=x onerror=window.siftailLiveXSS=true> live-hostile";
  await ingestLive([hostile]);
  await expect(page.locator("#live-rows .log-row")).toHaveCount(1);
  await expect(page.locator("#live-rows .row-message")).toHaveText(hostile);
  await expect(page.locator("#live-rows img")).toHaveCount(0);
  expect(await page.evaluate(
    () => (window as Window & { siftailLiveXSS?: boolean }).siftailLiveXSS,
  )).toBeUndefined();

  const row = page.locator("#live-rows .log-row").first();
  await row.focus();
  await page.keyboard.press("Enter");
  await expect(row.locator(".event-detail")).toBeFocused();
  await expect(row.locator(".event-detail")).toContainText(hostile);
  await row.getByRole("button", { name: "Collapse details" }).click();

  await page.getByRole("button", { name: "Pause" }).click();
  await expect(page.locator("[data-live-status]")).toHaveText("Paused");
  await expect(page.locator("[data-live-notices]").getByText(
    "Live view paused. Logs are still being stored.",
  )).toBeVisible();
  await ingestLive(["paused-one", "paused-two", "paused-three"]);
  await expect(page.locator("#live-rows .log-row")).toHaveCount(1);
  await expect(page.locator("[data-live-pending]")).toContainText("3 new events");
  await page.getByRole("button", { name: "Resume" }).click();
  await expect(page.locator("#live-rows .log-row")).toHaveCount(4);
  await expect(page.locator("[data-live-pending]")).toBeHidden();

  await page.getByRole("button", { name: "Clear view" }).click();
  await expect(page.locator("#live-rows .log-row")).toHaveCount(0);
  await expect(page.getByText(/Persisted logs were not deleted/)).toBeAttached();
  await page.locator(".mode-switch").getByRole("link", { name: "History" }).click();
  await expect(page.locator(".log-row").filter({ hasText: "paused-three" })).toHaveCount(1);
  await page.locator(".mode-switch").getByRole("link", { name: "Live" }).click();
  await expect(page.locator("[data-live-status]")).toHaveText("Live");

  await page.locator("#live-contains-filter").fill("filter-match");
  await expect(page).toHaveURL(/contains=filter-match/);
  await ingestLive(["ignored-after-filter", "filter-match visible"]);
  await expect(page.locator("#live-rows .log-row")).toHaveCount(1);
  await expect(page.locator("#live-rows .row-message")).toHaveText("filter-match visible");
  await page.getByRole("button", { name: "Reconnect" }).click();
  await expect(page.locator("[data-live-notices]").getByText(/manually reconnected/)).toBeVisible();
  await expect.poll(async () => page.evaluate(
    () => (window as Window & {
      __siftailEventSourceStats: { active: number };
    }).__siftailEventSourceStats.active,
  )).toBe(1);
});

test("History and Live share one filter model across mode switches", async ({ page }) => {
  await login(page);
  await page.locator("#server-filter").selectOption("1");
  await page.locator("#project-filter").selectOption("browser-project");
  await page.locator("#application-filter").selectOption("api");
  await expect(page).toHaveURL(/application=api/);
  await page.getByRole("button", { name: "Errors" }).click();
  await expect(page).toHaveURL(/levels=error%2Cfatal/);
  await page.locator("#stream-unknown").uncheck();
  await expect(page).toHaveURL(/streams=stdout%2Cstderr/);
  await page.locator("#contains-filter").fill("carried");
  await expect(page).toHaveURL(/contains=carried/);

  // History → Live with the L shortcut carries scope, levels, streams, text.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press("l");
  await expect(page).toHaveURL(/mode=live/);
  const live = new URL(page.url()).searchParams;
  expect(live.get("server")).toBe("1");
  expect(live.get("project")).toBe("browser-project");
  expect(live.get("application")).toBe("api");
  expect(live.get("levels")).toBe("error,fatal");
  expect(live.get("streams")).toBe("stdout,stderr");
  expect(live.get("contains")).toBe("carried");
  await expect(page.locator("#live-server-filter")).toHaveValue("1");
  await expect(page.locator("#live-project-filter")).toHaveValue("browser-project");
  await expect(page.locator("#live-application-filter")).toHaveValue("api");
  await expect(page.locator("#level-info")).not.toBeChecked();
  await expect(page.locator("#level-error")).toBeChecked();
  await expect(page.locator("#stream-unknown")).not.toBeChecked();
  await expect(page.locator("#live-contains-filter")).toHaveValue("carried");
  await expect(page.locator("[data-live-status]")).toHaveText("Live");

  // The Live scope filters the stream by source keys.
  await page.locator("#live-contains-filter").fill("scoped-live");
  await expect(page).toHaveURL(/contains=scoped-live/);
  await expect(page.locator("[data-live-status]")).toHaveText("Live");
  await ingestSource(["scoped-live other app"], "worker", "web", "worker-container", "worker-1");
  await ingestLive(["scoped-live in scope"]);
  await expect(page.locator("#live-rows .log-row")).toHaveCount(1);
  await expect(page.locator("#live-rows .row-message")).toHaveText("scoped-live in scope");

  // Live → History with the segmented control carries the edited state.
  await page.locator(".mode-switch").getByRole("link", { name: "History" }).click();
  await expect(page).toHaveURL(/mode=history/);
  const history = new URL(page.url()).searchParams;
  expect(history.get("server")).toBe("1");
  expect(history.get("application")).toBe("api");
  expect(history.get("levels")).toBe("error,fatal");
  expect(history.get("streams")).toBe("stdout,stderr");
  expect(history.get("contains")).toBe("scoped-live");
  expect(history.get("from")).toBeTruthy();
  await expect(page.locator("#application-filter")).toHaveValue("api");
  await expect(page.locator("#contains-filter")).toHaveValue("scoped-live");
  await expect(page.locator(".log-row .row-message")).toHaveText(["scoped-live in scope"]);

  // L again, then a Live scope change re-renders the cascade and H re-renders the cascade.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press("l");
  await expect(page).toHaveURL(/mode=live/);
  await page.locator("#live-application-filter").selectOption("");
  await expect(page).not.toHaveURL(/application=/);
  await expect(page.locator("#live-project-filter")).toHaveValue("browser-project");
  await expect(page.locator("[data-live-status]")).toHaveText("Live");
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press("h");
  await expect(page).toHaveURL(/mode=history/);
  expect(new URL(page.url()).searchParams.get("project")).toBe("browser-project");
  expect(new URL(page.url()).searchParams.has("application")).toBe(false);
});

test("Show events around this time lands on the event from History and Live", async ({
  page,
}) => {
  await login(page);
  await page.locator("#server-filter").selectOption("1");
  await expect(page).toHaveURL(/server=1/);
  await expect(page.locator(".log-row")).toHaveCount(200);

  const row = page.locator("#history-rows > .log-row").nth(120);
  const eventID = await row.getAttribute("data-event-id");
  const datetime = await row.locator("time").getAttribute("datetime");
  await row.locator(".event-toggle").click();
  const around = row.getByRole("link", { name: "Show events around this time" });
  await expect(around).toBeVisible();
  await around.click();

  await expect(page).toHaveURL(new RegExp(`highlight=${eventID}`));
  const values = new URL(page.url()).searchParams;
  const at = Date.parse(datetime ?? "");
  expect(Date.parse(values.get("from") ?? "")).toBe(at - 60_000);
  expect(Date.parse(values.get("to") ?? "")).toBe(at + 60_000);
  expect(values.get("server")).toBe("1");
  const highlighted = page.locator("#history-rows > .log-row[data-highlighted]");
  await expect(highlighted).toHaveCount(1);
  await expect(highlighted).toHaveAttribute("data-event-id", eventID ?? "");
  await expect(highlighted).toHaveAttribute("aria-current", "true");
  await expect(highlighted).toBeFocused();
  await expect(highlighted).toBeInViewport();
  // Events both before and after the hit are on screen.
  const rows = page.locator("#history-rows > .log-row");
  const index = await rows.evaluateAll(
    (elements) => elements.findIndex((element) => element.hasAttribute("data-highlighted")),
  );
  expect(index).toBeGreaterThan(0);
  expect(await rows.count()).toBeGreaterThan(index + 1);
  await page.screenshot({
    path: ".playwright-artifacts/sft-116-surrounding-desktop.png",
  });

  // From Live, the link opens History around the streamed event.
  await page.goto("/logs?mode=live&server=1&application=api");
  await expect(page.locator("[data-live-status]")).toHaveText("Live");
  await ingestLive(["surrounding-live-hit"]);
  const liveRow = page.locator("#live-rows .log-row").first();
  await expect(liveRow).toContainText("surrounding-live-hit");
  const liveID = await liveRow.getAttribute("data-event-id");
  await liveRow.locator(".event-toggle").click();
  await liveRow.getByRole("link", { name: "Show events around this time" }).click();
  await expect(page).toHaveURL(new RegExp(`highlight=${liveID}`));
  expect(new URL(page.url()).searchParams.get("application")).toBe("api");
  const liveHit = page.locator("#history-rows > .log-row[data-highlighted]");
  await expect(liveHit).toContainText("surrounding-live-hit");
  await expect(liveHit).toBeFocused();
});

test("typing during a slowed filter request is kept and the form is never swapped", async ({
  page,
}) => {
  await login(page);
  const form = page.locator("#history-filter-form");
  await form.evaluate((element) => { element.dataset.marker = "original"; });
  const hold = await holdRequests(page, /\/logs\/rows\?/);
  const contains = page.locator("#contains-filter");
  await contains.click();
  await contains.pressSequentially("needle");
  await expect.poll(hold.held).toBe(1);
  // The indicator beside the result summary shows the request, and the
  // region reports it is busy.
  await expect(page.locator("#history-filter-loading > span")).toBeVisible();
  await expect(page.locator("#history-region")).toHaveAttribute("aria-busy", "true");
  await page.locator(".result-summary").screenshot({
    path: ".playwright-artifacts/sft-117-filter-loading.png",
  });
  await contains.pressSequentially("-hostile");
  await expect.poll(hold.held).toBe(2);
  hold.release();
  await expect(page).toHaveURL(/contains=needle-hostile/);
  await expect(page.locator(".log-row")).toHaveCount(1);
  await expect(contains).toHaveValue("needle-hostile");
  await expect(contains).toBeFocused();
  expect(await contains.evaluate((input: HTMLInputElement) => input.selectionStart)).toBe(14);
  await expect(form).toHaveAttribute("data-marker", "original");
  await expect(page.locator("#history-filter-loading > span")).toBeHidden();
  await expect(page.locator("#history-region")).toHaveAttribute("aria-busy", "false");
  await page.unrouteAll({ behavior: "ignoreErrors" });

  // A select replaced out of band keeps focus, and its cascade still loads.
  await page.locator("#server-filter").focus();
  await page.locator("#server-filter").selectOption("1");
  await expect(page).toHaveURL(/server=1/);
  await expect(page.locator("#server-filter")).toBeFocused();
  await expect(page.locator("#project-filter option[value='browser-project']")).toHaveCount(1);
  await expect(form).toHaveAttribute("data-marker", "original");
});

test("the mobile Filters disclosure keeps its open state across swaps", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);
  const disclosure = page.locator(".history-filter-disclosure");
  await expect(disclosure).not.toHaveAttribute("open", "");
  // Toggled from the keyboard, as an operator on a phone keyboard would.
  await page.locator(".history-filter-disclosure > summary").focus();
  await page.keyboard.press("Enter");
  await expect(disclosure).toHaveAttribute("open", "");
  await page.locator("#level-debug").uncheck();
  await expect(page).toHaveURL(/levels=/);
  await expect(disclosure).toHaveAttribute("open", "");
  await expect(page.locator("#level-debug")).toBeFocused();
  await page.locator("#server-filter").selectOption("1");
  await expect(page).toHaveURL(/server=1/);
  await expect(disclosure).toHaveAttribute("open", "");
  for (const theme of ["dark", "light"] as const) {
    await setTheme(page, theme);
    await page.screenshot({ path: `.playwright-artifacts/sft-117-mobile-open-${theme}.png` });
  }

  // Closed stays closed too, and the summary names the new scope.
  await page.locator(".history-filter-disclosure > summary").focus();
  await page.keyboard.press("Enter");
  await expect(disclosure).not.toHaveAttribute("open", "");
  await page.getByRole("button", { name: "Load older" }).click();
  await expect(page.locator(".log-row")).not.toHaveCount(200);
  await expect(disclosure).not.toHaveAttribute("open", "");
  await expect(page.locator("#history-filter-scope")).toContainText("Browser");
});

test("Load older and Load newer show Loading inside the button", async ({ page }) => {
  await login(page);
  for (const [name, pattern] of [
    ["Load older", /\/logs\/rows\?.*append=1/],
    ["Load newer", /\/logs\/rows\?.*prepend=1/],
  ] as const) {
    const hold = await holdRequests(page, pattern);
    const button = page.locator(name === "Load older" ? "#load-older" : "#load-newer");
    await button.click();
    await expect.poll(hold.held).toBe(1);
    await expect(button).toBeDisabled();
    await expect(button).toHaveAccessibleName("Loading…");
    await expect(button.locator(".htmx-indicator")).toBeVisible();
    await expect(button.locator(".paging-label")).toBeHidden();
    await (name === "Load older" ? page.locator("#history-pagination") : page.locator("#history-tail"))
      .screenshot({
        path: `.playwright-artifacts/sft-117-${name === "Load older" ? "older" : "newer"}-loading.png`,
      });
    hold.release();
    await expect(page.locator("#history-update-status")).not.toBeEmpty();
    await page.unrouteAll({ behavior: "ignoreErrors" });
    if (name === "Load newer") {
      await expect(page.locator("#load-newer")).toHaveAccessibleName("Load newer");
    }
  }
});

test("fragment errors render beside their cause and survive unrelated swaps", async ({
  page,
}) => {
  await login(page);
  const filterError = page.locator("#history-filter-error");
  await page.getByText("More filters").click();
  await page.locator("#http-status-filter").fill("abc");
  await page.locator("#http-status-filter").press("Tab");
  await expect(filterError.getByRole("alert")).toContainText("Check the time range and filters.");
  // The error sits directly after the filter form, before the results.
  expect(await page.evaluate(() => {
    const error = document.querySelector("#history-filter-error");
    const form = document.querySelector(".history-filter-disclosure");
    return error?.previousElementSibling === form;
  })).toBe(true);
  await expect(page.locator("#http-status-filter")).toHaveValue("abc");

  // A successful Load older does not clear it.
  await page.getByRole("button", { name: "Load older" }).click();
  await expect(page.locator("#history-update-status")).toContainText("additional");
  await expect(filterError.getByRole("alert")).toBeVisible();
  for (const theme of ["dark", "light"] as const) {
    await setTheme(page, theme);
    await filterError.scrollIntoViewIfNeeded();
    await page.screenshot({ path: `.playwright-artifacts/sft-117-filter-error-${theme}.png` });
  }

  // Fixing the cause clears it.
  await page.locator("#http-status-filter").fill("");
  await page.locator("#http-status-filter").press("Enter");
  await expect(filterError).toBeEmpty();

  // A failed Load older reports below the list, beside its button.
  await page.route(/\/logs\/rows\?.*append=1/, async (route) => {
    const url = new URL(route.request().url());
    url.searchParams.set("cursor", "tampered");
    await route.continue({ url: url.toString() });
  });
  await page.getByRole("button", { name: "Load older" }).click();
  const olderError = page.locator("#history-pagination #history-older-error");
  await expect(olderError.getByRole("alert")).toContainText(
    "The page cursor is invalid for these filters.",
  );
  await expect(page.getByRole("button", { name: "Load older" })).toBeEnabled();
  // Load newer succeeds without removing it.
  await page.getByRole("button", { name: "Load newer" }).click();
  await expect(page.locator("#history-update-status")).toContainText(/newer/);
  await expect(olderError.getByRole("alert")).toBeVisible();
  for (const theme of ["dark", "light"] as const) {
    await setTheme(page, theme);
    await page.locator("#history-pagination").screenshot({
      path: `.playwright-artifacts/sft-117-older-error-${theme}.png`,
    });
  }
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator("#history-pagination").screenshot({
    path: ".playwright-artifacts/sft-117-older-error-mobile-light.png",
  });
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  await page.setViewportSize({ width: 1280, height: 720 });
  // Dismiss removes it and returns focus to the control.
  await olderError.getByRole("button", { name: "Dismiss" }).click();
  await expect(olderError).toBeEmpty();
  await expect(page.locator("#load-older")).toBeFocused();
  await page.unrouteAll({ behavior: "ignoreErrors" });
});

test("the row cap trims opposite to the load direction and keeps focused and expanded rows", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await login(page);
  // Older direction: 1,100 events, five Load older pages.
  const olderBase = Date.now() - 3 * 60 * 60 * 1000;
  await ingestAt("cap-older", 1100, olderBase);
  await page.goto(historyScopeURL("cap-older", olderBase - 60_000, olderBase + 60_000));
  const rows = page.locator("#history-rows > .log-row");
  await expect(rows).toHaveCount(200);
  const newest = rows.first();
  const newestID = await newest.getAttribute("data-event-id");
  await newest.locator(".event-toggle").click();
  await expect(newest.locator(".event-toggle")).toHaveAttribute("aria-expanded", "true");
  for (let page_ = 1; page_ <= 5; page_ += 1) {
    await page.getByRole("button", { name: "Load older" }).click();
    await expect(page.locator("#history-update-status")).toContainText(/additional|limit/);
    await expect(rows).toHaveCount(Math.min(200 * (page_ + 1), 1000));
  }
  // The just-loaded oldest rows stay; the expanded newest row stays; the
  // trimmed newest interval is reachable from the tail line.
  await expect(rows.last()).toContainText("cap-older-0");
  await expect(rows.first()).toHaveAttribute("data-event-id", newestID ?? "");
  await expect(rows.nth(1)).toContainText("cap-older-998");
  await expect(page.locator("[data-loaded-count]")).toHaveText("1000");
  await expect(page.locator("#history-tail").getByRole("link", { name: "Return to newest events" }))
    .toBeVisible();
  await expect(page.locator("#history-update-status")).toContainText("newest loaded rows were removed");

  // Newer direction: 1,000 events loaded, then 200 newer arrive.
  const newerBase = Date.now() - 4 * 60 * 60 * 1000;
  await ingestAt("cap-newer", 1000, newerBase);
  await page.goto(historyScopeURL("cap-newer", newerBase - 60_000, newerBase + 60_000));
  await expect(rows).toHaveCount(200);
  for (let page_ = 1; page_ <= 4; page_ += 1) {
    await page.getByRole("button", { name: "Load older" }).click();
    await expect(rows).toHaveCount(200 * (page_ + 1));
  }
  const oldest = rows.last();
  await expect(oldest).toContainText("cap-newer-0");
  const oldestID = await oldest.getAttribute("data-event-id");
  await oldest.locator(".event-toggle").click();
  await expect(oldest.locator(".event-toggle")).toHaveAttribute("aria-expanded", "true");
  await ingestAt("cap-newer", 200, newerBase + 5_000, "cap-newer-late");
  await page.getByRole("button", { name: "Load newer" }).click();
  await expect(page.locator("#history-update-status")).toContainText("oldest loaded rows were removed");
  await expect(rows).toHaveCount(1000);
  await expect(page.locator("[data-loaded-count]")).toHaveText("1000");
  // The rows just loaded are at the top; the expanded oldest row remains.
  await expect(rows.first()).toContainText("cap-newer-late-199");
  await expect(page.locator(`#history-rows > .log-row[data-event-id="${oldestID}"]`)).toHaveCount(1);
  await expect(page.locator("#load-older")).toHaveCount(0);
  const kept = await rows.nth(998).locator(".row-message").textContent();
  expect(kept).toBe("cap-newer-201");
  // The trimmed older interval is reachable: the link lands on its newest
  // row, the one directly below the kept rows.
  const recover = page.locator("#history-pagination").getByRole("link", {
    name: "Continue to older events",
  });
  await expect(recover).toBeVisible();
  await recover.click();
  const highlighted = page.locator("#history-rows > .log-row[data-highlighted]");
  await expect(highlighted).toContainText("cap-newer-200");
  await expect(highlighted).toBeFocused();
  await expect(rows.first()).toContainText("cap-newer-200");
});

test("Live enforces rendered and pending caps and does not steal scroll", async ({ page }) => {
  test.setTimeout(90_000);
  await login(page);
  await page.locator(".mode-switch").getByRole("link", { name: "Live" }).click();
  await expect(page.locator("[data-live-status]")).toHaveText("Live");

  let delivered = 0;
  for (let batch = 0; batch < 11; batch += 1) {
    const messages = Array.from({ length: 100 }, (_, index) =>
      `render-cap-${batch}-${index}`);
    await ingestLive(messages);
    delivered += messages.length;
    await expect(page.locator("#live-rows .log-row")).toHaveCount(Math.min(delivered, 1000));
  }
  await expect(page.locator("#live-rows .log-row")).toHaveCount(1000);
  await expect(page.locator("[data-live-notices]").getByText(/Older rows were removed/)).toBeVisible();

  const scroll = page.locator("[data-live-scroll]");
  await scroll.evaluate((element) => {
    element.scrollTop = 0;
    element.dispatchEvent(new Event("scroll"));
  });
  await ingestLive(["scrolled-away-one", "scrolled-away-two"]);
  await expect(page.locator("#live-rows .log-row")).toHaveCount(1000);
  await expect(page.locator("[data-live-pending]")).toContainText("2 new events");
  await page.locator("[data-live-pending]").click();
  await expect(page.locator("#live-rows .row-message").last()).toHaveText("scrolled-away-two");

  await page.getByRole("button", { name: "Pause" }).click();
  for (let batch = 0; batch < 21; batch += 1) {
    const count = batch === 20 ? 1 : 100;
    await ingestLive(Array.from({ length: count }, (_, index) =>
      `pending-cap-${batch}-${index}`));
    if (batch < 20) {
      await expect(page.locator("[data-live-pending]")).toContainText(
        `${(batch + 1) * 100} new events`,
      );
    }
  }
  await expect(page.locator("[data-live-notices]").getByText(
    /Live view was truncated while you were away/,
  )).toBeVisible();
  await expect(page.locator("#live-rows .log-row")).toHaveCount(1000);
});

test("Live keyboard, themes, reduced motion, mobile, axe, and online invalidation", async ({
  page,
}) => {
  await login(page);
  await page.keyboard.press("l");
  await expect(page).toHaveURL(/mode=live/);
  await expect(page.locator("[data-live-status]")).toHaveText("Live");
  await page.keyboard.press("Space");
  await expect(page.locator("[data-live-status]")).toHaveText("Paused");
  await page.keyboard.press("Space");
  await expect(page.locator("[data-live-status]")).toHaveText("Live");

  await setTheme(page, "dark");
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/live-desktop-dark.png",
    fullPage: true,
  });
  await setTheme(page, "light");
  await page.emulateMedia({ reducedMotion: "reduce" });
  const behavior = await page.locator("[data-live-scroll]").evaluate(
    (element) => getComputedStyle(element).scrollBehavior,
  );
  expect(behavior).not.toBe("smooth");

  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  await expect(page.getByRole("button", { name: "Pause" })).toBeVisible();
  await page.screenshot({
    path: ".playwright-artifacts/live-mobile-light.png",
    fullPage: true,
  });
  results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);

  const state = readState();
  execFileSync(state.binary, ["sessions", "revoke-all"], {
    env: siftailEnvironment({ SIFTAIL_DATA_DIR: state.dataDirectory }),
    encoding: "utf8",
  });
  await expect(page).toHaveURL(/\/login\?return=.*expired=1/, { timeout: 10_000 });
});

test("shortcuts are scoped to Logs, discoverable, and inert on form pages", async ({ page }) => {
  await login(page);

  // Row navigation works in History: J selects, Enter expands, Esc clears.
  await page.keyboard.press("j");
  await expect(page.locator(".log-row").first()).toBeFocused();
  await page.keyboard.press("j");
  await expect(page.locator(".log-row").nth(1)).toBeFocused();
  await page.keyboard.press("k");
  await expect(page.locator(".log-row").first()).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator(".event-detail").first()).toBeVisible();
  await page.locator(".log-row").first().focus();
  await page.keyboard.press("Escape");
  await expect(page.locator(".log-row").first()).not.toBeFocused();

  // The ? overlay lists the shortcuts and closes with Escape.
  await page.keyboard.press("?");
  const help = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(help).toBeVisible();
  await expect(help.getByText("Pause or resume Live")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(help).toBeHidden();

  // On a form page, single letters are inert: L must not navigate away and
  // discard input.
  await navigateSystem(page, "Settings");
  await expect(page).toHaveURL(/\/settings$/);
  await page.keyboard.press("l");
  await page.keyboard.press("f");
  await expect(page).toHaveURL(/\/settings$/);
  await page.keyboard.press("?");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeVisible();
  await expect(page.getByText("active only in the Logs workspace")).toBeVisible();
  await page.keyboard.press("Escape");
});

// Records, after every document-level handler ran, whether a Space keydown
// was default-prevented, so a test can tell "the keyboard layer took Space"
// apart from native activation.
async function watchSpace(page: Page): Promise<void> {
  await page.evaluate(() => {
    const record: boolean[] = [];
    (window as unknown as { spacePrevented: boolean[] }).spacePrevented = record;
    window.addEventListener("keydown", (event) => {
      if (event.key === " ") record.push(event.defaultPrevented);
    });
  });
}

async function lastSpacePrevented(page: Page): Promise<boolean | undefined> {
  return page.evaluate(() => {
    const record = (window as unknown as { spacePrevented: boolean[] }).spacePrevented;
    return record[record.length - 1];
  });
}

test("Space keeps native activation on controls in History and Live", async ({ page }) => {
  await login(page);
  await watchSpace(page);

  // History has no pause control: Space from the page body is left to the
  // browser (page scrolling).
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press("Space");
  expect(await lastSpacePrevented(page)).toBe(false);

  // A summary toggles its disclosure natively.
  const disclosure = page.locator(".history-filter-disclosure").first();
  await expect(disclosure).toHaveAttribute("open", "");
  await disclosure.locator("summary").first().focus();
  await page.keyboard.press("Space");
  expect(await lastSpacePrevented(page)).toBe(false);
  await expect(disclosure).not.toHaveAttribute("open", "");
  await page.keyboard.press("Space");
  await expect(disclosure).toHaveAttribute("open", "");

  // A button activates natively.
  await page.locator("[data-palette-open]").focus();
  await page.keyboard.press("Space");
  expect(await lastSpacePrevented(page)).toBe(false);
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(palette).toBeHidden();

  // Live: Space on the Pause button activates it exactly once (a second,
  // shortcut-driven click would toggle it straight back to Live).
  await page.keyboard.press("l");
  await expect(page).toHaveURL(/mode=live/);
  await expect(page.locator("[data-live-status]")).toHaveText("Live");
  await watchSpace(page);
  await page.locator("[data-live-pause]").focus();
  await page.keyboard.press("Space");
  expect(await lastSpacePrevented(page)).toBe(false);
  await expect(page.locator("[data-live-status]")).toHaveText("Paused");
  await page.keyboard.press("Space");
  await expect(page.locator("[data-live-status]")).toHaveText("Live");

  // Space on the System menu summary opens the menu and leaves Live alone.
  const systemMenu = page.locator("[data-system-menu]");
  await systemMenu.locator("summary").focus();
  await page.keyboard.press("Space");
  expect(await lastSpacePrevented(page)).toBe(false);
  await expect(systemMenu).toHaveAttribute("open", "");
  await expect(page.locator("[data-live-status]")).toHaveText("Live");
  await page.keyboard.press("Escape");
  await expect(systemMenu).not.toHaveAttribute("open", "");
  await expect(systemMenu.locator("summary")).toBeFocused();

  // Away from controls, Space is the Live pause shortcut.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press("Space");
  expect(await lastSpacePrevented(page)).toBe(true);
  await expect(page.locator("[data-live-status]")).toHaveText("Paused");
});

test("the shortcuts preference makes single-key shortcuts inert", async ({ page }) => {
  await login(page);
  await expect(page.locator("html")).not.toHaveAttribute("data-shortcuts", /.*/);
  await setShortcuts(page, "off");
  await expect(page.locator("html")).toHaveAttribute("data-shortcuts", "off");

  // Server-rendered: the preference survives a reload before any script.
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-shortcuts", "off");
  await expect(page.locator("[data-shortcuts-select]")).toHaveValue("off");

  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  for (const key of ["j", "k", "f", "g", "h"]) await page.keyboard.press(key);
  await expect(page.locator(".log-row:focus")).toHaveCount(0);
  await expect(page.locator('input[name="contains"]')).not.toBeFocused();
  await page.keyboard.press("?");
  const help = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(help).toBeHidden();
  await page.keyboard.press("l");
  await expect(page).toHaveURL(/mode=history/);

  // Escape and Ctrl/Cmd+K are unaffected.
  await page.keyboard.press("Control+k");
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(palette).toBeHidden();

  // Live: Space from the page no longer pauses.
  await page.locator(".mode-switch a", { hasText: "Live" }).click();
  await expect(page).toHaveURL(/mode=live/);
  await expect(page.locator("[data-live-status]")).toHaveText("Live");
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press("Space");
  await page.keyboard.press("g");
  await expect(page.locator("[data-live-status]")).toHaveText("Live");

  // The help stays reachable from the System menu, says shortcuts are off,
  // and leads to the preference.
  await openSystemMenu(page);
  await page.getByRole("button", { name: "Keyboard shortcuts" }).click();
  await expect(help).toBeVisible();
  await expect(help.getByText("Keyboard shortcuts are off")).toBeVisible();
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await help.getByRole("button", { name: "Change the Shortcuts preference" }).click();
  await expect(help).toBeHidden();
  await expect(page.locator("[data-shortcuts-select]")).toBeFocused();

  // Back on: the same keys work again without a reload.
  await setShortcuts(page, "on");
  await expect(page.locator("html")).not.toHaveAttribute("data-shortcuts", /.*/);
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press("Space");
  await expect(page.locator("[data-live-status]")).toHaveText("Paused");
  await page.keyboard.press("?");
  await expect(help).toBeVisible();
  await expect(help.getByText("Keyboard shortcuts are off")).toBeHidden();
  results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.keyboard.press("Escape");
});

test("keyboard help and palette hint use the platform modifier", async ({ page }) => {
  await login(page);
  const button = page.locator("[data-palette-open]");
  await expect(button).toContainText("Ctrl K");
  await page.keyboard.press("?");
  const help = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(help).toBeVisible();
  for (const text of [
    "Open the command palette", "Open this help", "Focus message search",
    "Switch to History", "Switch to Live", "Next / previous log row",
    "Expand or collapse the selected row", "Pause or resume Live",
    "Jump to the newest live event",
  ]) {
    await expect(help.getByText(text)).toBeVisible();
  }
  const global = help.getByRole("region", { name: "Global shortcuts" });
  await expect(global.locator("kbd", { hasText: "Ctrl K" })).toBeVisible();
  await page.keyboard.press("Escape");

  // On Apple platforms both read ⌘K.
  await page.addInitScript(() => {
    Object.defineProperty(Navigator.prototype, "platform", { get: () => "MacIntel" });
    Object.defineProperty(Navigator.prototype, "userAgentData", { get: () => undefined });
  });
  await page.reload();
  await expect(button).toContainText("⌘K");
  await page.keyboard.press("?");
  await expect(global.locator("kbd", { hasText: "⌘K" })).toBeVisible();
  await expect(help.getByText("Ctrl K")).toHaveCount(0);
  await page.keyboard.press("Escape");
  await page.keyboard.press("Meta+k");
  await expect(page.getByRole("dialog", { name: "Command palette" })).toBeVisible();
  await page.keyboard.press("Escape");
});

test("sticky header leaves room for focused rows and the health target is 24 px", async ({
  page,
}) => {
  await login(page);
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 844 });
    const { padding, header } = await page.evaluate(() => ({
      padding: parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop),
      header: document.querySelector(".app-header")!.getBoundingClientRect().height,
    }));
    expect(padding).toBeGreaterThan(0);
    expect(Math.abs(padding - header)).toBeLessThan(0.5);
    const health = await page.locator(".health-indicator").boundingBox();
    expect(health!.width).toBeGreaterThanOrEqual(24);
    expect(health!.height).toBeGreaterThanOrEqual(24);
  }

  // A J-selected row far down the list is never left under the header.
  await page.setViewportSize({ width: 1280, height: 844 });
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  for (let index = 0; index < 30; index += 1) await page.keyboard.press("j");
  await page.keyboard.press("k");
  const rowTop = await page.locator(".log-row:focus").evaluate(
    (row) => row.getBoundingClientRect().top,
  );
  const headerBottom = await page.locator(".app-header").evaluate(
    (header) => header.getBoundingClientRect().bottom,
  );
  expect(rowTop).toBeGreaterThanOrEqual(headerBottom - 0.5);
});

test("command palette navigates, filters sources, and restores focus", async ({ page }) => {
  await login(page);

  // Open with the keyboard, navigate keyboard-only, and land on Status.
  await page.keyboard.press("Control+k");
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();
  const paletteInput = page.locator("[data-palette-input]");
  await expect(paletteInput).toBeFocused();
  // The combobox reports its listbox state, and points at an option only
  // while one exists.
  await expect(paletteInput).toHaveAttribute("aria-expanded", "true");
  await expect(paletteInput).toHaveAttribute("aria-activedescendant", "palette-option-0");
  await paletteInput.fill("zzqqxxnomatch");
  await expect(page.locator("[data-palette-empty]")).toBeVisible();
  await expect(paletteInput).toHaveAttribute("aria-expanded", "false");
  await expect(paletteInput).not.toHaveAttribute("aria-activedescendant", /.*/);
  await page.locator("[data-palette-input]").fill("status");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/status$/);

  // Axe stays clean with the dialog open.
  await page.keyboard.press("Control+k");
  await expect(palette).toBeVisible();
  const openResults = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(openResults.violations.map((violation) => violation.id)).toEqual([]);

  // Escape closes and restores focus to the page.
  await page.keyboard.press("Escape");
  await expect(palette).toBeHidden();

  // The header button opens it too, and a source command applies a filter.
  await page.goto("/logs");
  await page.locator("[data-palette-open]").click();
  await expect(palette).toBeVisible();
  await page.locator("[data-palette-input]").fill("browser-project");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/project=browser-project/);
  await expect(page.locator("[data-palette-open]")).toBeFocused();
});

test("source catalog preserves hierarchy, observations, responsive layout, and log navigation", async ({
  page,
}) => {
  await login(page);
  await page.getByRole("link", { name: "Sources", exact: true }).click();
  await expect(page).toHaveURL(/\/sources$/);
  await expect(page.getByRole("heading", { name: "Discovered sources" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Primary" })
    .getByRole("link", { name: "Sources" })).toHaveAttribute("aria-current", "page");

  const apiRow = page.getByRole("row").filter({ hasText: "api / web" }).first();
  await expect(apiRow).toContainText("Browser");
  await expect(apiRow).toContainText("browser-project / test");
  await expect(apiRow).toContainText("Active");
  await expect(apiRow).toContainText("Retained logs");
  await apiRow.getByRole("link", { name: "api/web" }).click();

  await expect(page.getByRole("heading", { name: "api/web" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Stable identity" })).toBeVisible();
  await expect(page.getByText(
    "Containers are ephemeral observations of this stable source, not separate sources.",
  )).toBeVisible();
  await expect(page.getByRole("row").filter({ hasText: "api-1" })).toContainText("Active");

  await setTheme(page, "dark");
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/sources-desktop-dark.png",
    fullPage: true,
  });
  await setTheme(page, "light");
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/sources-mobile-light.png",
    fullPage: true,
  });

  await page.getByRole("link", { name: "Open logs" }).click();
  await expect(page).toHaveURL(/\/logs\?.*application=api.*service=web/);
  await expect(page.locator(".log-row")).not.toHaveCount(0);
});

test("source aliases, clear logs, removal, confirmation focus, and expired sessions stay distinct", async ({
  page,
}) => {
  test.setTimeout(30_000);
  await login(page);
  await ingestSource(
    ["mutation-api-retained"],
    "mutation-api",
    "web",
    "mutation-api-container",
    "mutation-api-1",
  );
  await ingestSource(
    ["mutation-worker-retained"],
    "mutation-worker",
    "jobs",
    "mutation-worker-container",
    "mutation-worker-1",
  );
  await page.getByRole("link", { name: "Sources", exact: true }).click();
  const apiRow = page.getByRole("row").filter({ hasText: "mutation-api / web" }).first();
  await apiRow.getByRole("link", { name: "mutation-api/web" }).click();

  const hostileAlias = "<img src=x onerror=window.siftailSourceXSS=true> API";
  await page.locator("#source-alias").fill(hostileAlias);
  await page.getByRole("button", { name: "Save alias" }).click();
  await expect(page.getByRole("heading", { name: hostileAlias })).toBeVisible();
  await expect(page.getByText(
    "Source alias updated. Stable identity and stored events were unchanged.",
  )).toBeVisible();
  await expect(page.locator("img")).toHaveCount(0);
  expect(await page.evaluate(
    () => (window as Window & { siftailSourceXSS?: boolean }).siftailSourceXSS,
  )).toBeUndefined();
  await expect(page.getByText(
    "Alias changes only how this source is displayed. Original metadata remains unchanged.",
  )).toBeVisible();

  await page.getByRole("button", { name: "Clear logs" }).click();
  await expect(page.locator("#clear-confirmation")).toBeFocused();
  await expect(page.getByText(
    "Type the displayed source name exactly to clear retained logs.",
  )).toBeVisible();

  await page.locator("#clear-confirmation").fill(hostileAlias);
  const state = readState();
  execFileSync(state.binary, ["sessions", "revoke-all"], {
    env: siftailEnvironment({ SIFTAIL_DATA_DIR: state.dataDirectory }),
    encoding: "utf8",
  });
  await page.getByRole("button", { name: "Clear logs" }).click();
  await expect(page).toHaveURL(/\/login\?return=.*expired=1/);

  await login(page);
  await page.getByRole("link", { name: "Sources", exact: true }).click();
  await page.getByRole("row").filter({ hasText: hostileAlias })
    .getByRole("link", { name: hostileAlias }).click();
  await expect(page.getByText("Retained logs", { exact: true })).toBeVisible();
  await page.locator("#clear-confirmation").fill(hostileAlias);
  await page.getByRole("button", { name: "Clear logs" }).click();
  await expect(page.getByText(
    "Retained logs were cleared. The source, alias, and container observations remain.",
  )).toBeVisible();
  await expect(page.getByRole("heading", { name: hostileAlias })).toBeVisible();
  await expect(page.getByText("No retained logs", { exact: true })).toBeVisible();
  await expect(page.getByText("mutation-api-1")).toBeVisible();

  await page.getByRole("link", { name: "Sources", exact: true }).click();
  const workerRow = page.getByRole("row").filter({ hasText: "mutation-worker / jobs" }).first();
  await workerRow.getByRole("link", { name: "mutation-worker/jobs" }).click();
  await page.getByRole("button", { name: "Remove source" }).click();
  await expect(page.locator("#remove-confirmation")).toBeFocused();
  await expect(page.getByText("Type the complete removal phrase exactly.")).toBeVisible();
  await page.locator("#remove-confirmation").fill("remove mutation-worker/jobs");
  await page.getByRole("button", { name: "Remove source" }).click();
  await expect(page).toHaveURL(/\/sources$/);
  await expect(page.getByText(
    "Source removed. An active sender may discover it again.",
  )).toBeVisible();
  await expect(page.getByRole("row").filter({ hasText: "mutation-worker / jobs" })).toHaveCount(0);
  await expect(page.getByRole("row").filter({ hasText: hostileAlias })).toHaveCount(1);
});

test("Server token creation, one-time copy, rotation, navigation loss, and revocation", async ({
  page,
}) => {
  await login(page);
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Servers", exact: true, level: 2 })).toBeVisible();
  await page.locator("#server-name").fill("Browser managed");
  await page.locator("#server-hostname").fill("managed.example");
  await page.getByRole("button", { name: "Create Server" }).press("Enter");
  await expect(page.getByRole("heading", { name: "Browser managed" })).toBeVisible();

  await page.locator("#token-name").fill("primary");
  await page.getByRole("button", { name: "Create token" }).click();
  await expect(page.getByRole("heading", { name: "Copy this token now" })).toBeVisible();
  const tokenInput = page.locator("[data-token-secret]");
  const primary = await tokenInput.inputValue();
  expect(primary).toMatch(/^sft_[A-Za-z0-9_-]{43}$/);
  expect(page.url()).not.toContain(primary);
  await expect(tokenInput).toHaveAttribute("type", "password");
  await page.getByRole("button", { name: "Show" }).click();
  await expect(tokenInput).toHaveAttribute("type", "text");
  await expect(tokenInput).toBeFocused();
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.getByRole("button", { name: "Copy token" }).click();
  await expect(page.getByText("Token copied.")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Connect this Server" })).toBeVisible();
  await expect(page.getByText("COOLIFY_APP_NAME=siftail-self")).toBeVisible();
  await page.getByRole("button", { name: "Copy Coolify config" }).click();
  const copiedConfig = await page.evaluate(() => navigator.clipboard.readText());
  expect(copiedConfig).toContain(`Authorization Bearer ${primary}`);
  expect(copiedConfig).toContain("Exclude COOLIFY_APP_NAME ^siftail-self$");
  expect(copiedConfig.indexOf("Exclude COOLIFY_APP_NAME"))
    .toBeLessThan(copiedConfig.indexOf("Rename COOLIFY_APP_NAME"));
  expect(copiedConfig).not.toContain("__SIFTAIL_INGEST_TOKEN__");

  await page.getByRole("button", { name: "Send guided test" }).click();
  await expect(page.getByText("Test event committed.")).toBeVisible();
  await expect(page.locator("[data-guide-stage] span").filter({ hasText: "Passed" }))
    .toHaveCount(5);
  await expect(page.getByText("siftail-test / setup / guided-ingestion / probe"))
    .toBeVisible();
  await setTheme(page, "dark");
  let guideA11y = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(guideA11y.violations.map((violation) => violation.id)).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  guideA11y = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(guideA11y.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/generated-guide-mobile-light.png",
    fullPage: true,
  });

  await page.getByRole("link", { name: "Done" }).click();
  await expect(page.getByRole("heading", { name: "Browser managed" })).toBeVisible();
  expect(await page.content()).not.toContain(primary);
  await page.goBack();
  expect(await page.content()).not.toContain(primary);
  await page.goto("/servers");
  expect(await page.content()).not.toContain(primary);

  await page.getByRole("link", { name: "Browser managed" }).click();
  await page.locator("#token-name").fill("replacement");
  await page.getByRole("button", { name: "Create token" }).click();
  const replacement = await page.locator("[data-token-secret]").inputValue();
  expect(replacement).not.toBe(primary);
  await page.getByRole("link", { name: "Done" }).click();
  await expect(page.getByRole("row").filter({ hasText: "primary" })).toContainText("Active");
  await expect(page.getByRole("row").filter({ hasText: "replacement" })).toContainText("Active");
  expect(await ingestWithToken(primary, "managed-primary")).toBe(204);
  expect(await ingestWithToken(replacement, "managed-replacement")).toBe(204);
  await page.reload();
  await expect(page.getByRole("row").filter({ hasText: "replacement" })).not.toContainText("Never");

  const primaryRow = page.getByRole("row").filter({ hasText: "primary" });
  await primaryRow.getByLabel(/Type primary to revoke/).fill("primary");
  await primaryRow.getByRole("button", { name: "Revoke" }).click();
  await expect(page.getByText(
    "Token revoked. New ingestion requests using it fail immediately.",
  )).toBeVisible();
  await expect(page.getByRole("row").filter({ hasText: "primary" })).toContainText("Revoked");
  expect(await ingestWithToken(primary, "managed-primary-revoked")).toBe(401);
  expect(await ingestWithToken(replacement, "managed-replacement-active")).toBe(204);

  await setTheme(page, "dark");
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/servers-mobile-light.png",
    fullPage: true,
  });
});

test("retention settings validate, persist, remain responsive, and expire safely", async ({
  page,
}) => {
  await login(page);
  await navigateSystem(page, "Settings");
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByRole("heading", { name: "Retention limits" })).toBeVisible();
  const retentionDays = page.locator("#retention-days");
  const maximumDatabaseGiB = page.locator("#maximum-database-gib");
  await expect(retentionDays).toHaveValue("14");
  await expect(maximumDatabaseGiB).toHaveValue("4");

  await retentionDays.fill("0");
  await page.getByRole("button", { name: "Save retention settings" }).click();
  await expect(page.getByText(
    "Retention must be a whole number from 1 to 3,650 days.",
  )).toBeVisible();
  await expect(retentionDays).toBeFocused();

  await retentionDays.fill("30");
  await maximumDatabaseGiB.fill("8");
  await page.getByRole("button", { name: "Save retention settings" }).press("Enter");
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByText("Retention settings saved.")).toBeVisible();
  await page.reload();
  await expect(retentionDays).toHaveValue("30");
  await expect(maximumDatabaseGiB).toHaveValue("8");

  await setTheme(page, "dark");
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/settings-mobile-light.png",
    fullPage: true,
  });

  const state = readState();
  execFileSync(state.binary, ["sessions", "revoke-all"], {
    env: siftailEnvironment({ SIFTAIL_DATA_DIR: state.dataDirectory }),
    encoding: "utf8",
  });
  await page.reload();
  await expect(page).toHaveURL(/\/login\?return=.*expired=1/);
});

test("status is authenticated, refreshable, sanitized, responsive, and accessible", async ({
  page,
}) => {
  await login(page);
  const responsePromise = page.waitForResponse(
    (response) => response.url().endsWith("/status") &&
      response.request().method() === "GET",
  );
  await navigateSystem(page, "Status");
  const response = await responsePromise;
  expect(response.status()).toBe(200);
  expect(response.headers()["cache-control"]).toBe("no-store");
  await expect(page.getByRole("heading", { name: "Status", exact: true })).toBeVisible();
  await expect(page.getByText("Healthy", { exact: true }).first()).toBeVisible();
  for (const heading of ["Runtime", "Storage", "Ingestion", "Diagnostics"]) {
    await expect(page.getByRole("heading", { name: heading, exact: true })).toBeVisible();
  }
  expect(await page.content()).not.toContain("browser managed token check");
  expect(await page.content()).not.toContain("sft_");
  const checkResponse = page.waitForResponse(
    (candidate) => candidate.url().endsWith("/status/database-check") &&
      candidate.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Run safe database check" }).click();
  expect((await checkResponse).status()).toBe(303);
  await expect(page).toHaveURL(/\/status$/);
  await expect(page.getByText("Database check completed.")).toBeVisible();
  await expect(page.getByText(
    /Healthy · schema \d+\/\d+ · SQLite .* · ok · checkpoint completed/,
  ))
    .toBeVisible();
  await expect(page.getByText(/database_check_succeeded/))
    .toBeVisible();
  await expect(page.getByText(
    "The bounded database check completed successfully.",
  )).toBeVisible();
  expect(await page.content()).not.toContain("/tmp/");
  expect(await page.content()).not.toContain("/home/");
  await page.reload();
  await expect(page.getByText(/events\/min \(last 60 seconds\)/)).toBeVisible();

  await setTheme(page, "dark");
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/status-mobile-light.png",
    fullPage: true,
  });
});

test("full and configuration backups are typed, verified, and accessible", async ({
  page,
}) => {
  test.setTimeout(45_000);
  await login(page);
  const state = readState();
  // Browser backups go only to SIFTAIL_BACKUP_DIR, default <data>/backups (SEC-11).
  const backupDirectory = path.join(state.dataDirectory, "backups");
  const output = path.join(backupDirectory, "browser-full.sqlite");
  await navigateSystem(page, "Backup");
  await expect(page.getByRole("heading", { name: "Backup", exact: true })).toBeVisible();
  await expect(page.locator("#backup-file-name")).toBeFocused();
  await expect(page.getByText("Browser sessions are always excluded")).toBeVisible();
  await expect(page.getByText("restore --confirm RESTORE", {
    exact: false,
  })).toBeVisible();

  const escape = path.join(state.runDirectory, "escape.sqlite");
  for (const value of [escape, "../escape.sqlite"]) {
    await page.locator("#backup-file-name").fill(value);
    await page.getByRole("button", { name: "Create verified full backup" }).click();
    await expect(page.getByRole("alert")).toContainText("Paths are not accepted.");
  }
  expect(fs.existsSync(escape)).toBe(false);
  expect(fs.existsSync(path.join(state.dataDirectory, "escape.sqlite"))).toBe(false);

  await page.locator("#backup-file-name").fill("browser-full.sqlite");
  await page.getByRole("button", { name: "Create verified full backup" }).click();
  await expect(page).toHaveURL(/\/backup$/);
  await expect(page.getByText("Backup verified.", { exact: true })).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByText("browser-full.sqlite", { exact: true })).toBeVisible();
  await expect(page.getByText("SHA-256", { exact: true })).toBeVisible();
  expect(await page.content()).not.toContain(state.runDirectory);
  expect(fs.statSync(output).mode & 0o777).toBe(0o600);
  expect(fs.statSync(backupDirectory).mode & 0o777).toBe(0o700);

  // An empty name asks the server to generate one inside the directory.
  await expect(page.getByText("not a merge", { exact: false })).toBeVisible();
  await page.locator("#configuration-backup-file-name").fill("");
  await page.getByRole("button", {
    name: "Create verified configuration-only backup",
  }).click();
  await expect(page.getByText("Backup verified.", { exact: true })).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByText("configuration", { exact: true })).toBeVisible();
  const configurationName = (await page
    .locator(".status-facts div", { hasText: "Artifact" })
    .locator("dd")
    .textContent())?.trim() ?? "";
  expect(configurationName).toMatch(/^siftail-configuration-\d{8}T\d{6}Z\.sqlite$/);
  expect(await page.content()).not.toContain(state.runDirectory);
  expect(fs.statSync(path.join(backupDirectory, configurationName)).mode & 0o777)
    .toBe(0o600);

  await page.getByLabel("Artifact file name").fill(configurationName);
  await page.getByRole("button", { name: "Verify backup" }).click();
  await expect(page.getByText("Backup verified.", { exact: true })).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByText("configuration", { exact: true })).toBeVisible();
  expect(await page.content()).not.toContain(state.runDirectory);

  await setTheme(page, "dark");
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/backup-desktop-dark.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/backup-mobile-light.png",
    fullPage: true,
  });
});

test("audit filters, pagination, escaping, keyboard, themes, and mobile remain bounded", async ({
  page,
}) => {
  test.setTimeout(45_000);
  await login(page);
  const hostileAlias = "<img src=x onerror=window.siftailSourceXSS=true> API";
  await page.getByRole("link", { name: "Sources", exact: true }).click();
  const sourceRow = page.locator("tr", {
    has: page.locator(".source-path", { hasText: "api / web" }),
  }).first();
  await sourceRow.getByRole("link").click();
  await page.locator("#source-alias").fill(hostileAlias);
  await page.getByRole("button", { name: "Save alias" }).click();
  await expect(page.getByRole("heading", { name: hostileAlias })).toBeVisible();
  const csrfToken = await page.locator("input[name=csrf_token]").first().inputValue();
  const statuses = await page.evaluate(async (csrf) => {
    const results: number[] = [];
    for (let index = 0; index < 101; index += 1) {
      const body = new URLSearchParams({
        csrf_token: csrf,
        retention_days: String(30 + (index % 2)),
        maximum_database_gib: "8",
      });
      const response = await fetch("/settings/retention", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
        body,
        redirect: "manual",
      });
      results.push(response.status);
    }
    return results;
  }, csrfToken);
  // Fetch exposes a same-origin manual redirect as an opaque redirect in
  // Chromium, whose status is zero rather than the underlying 303.
  expect(statuses.every((status) => status === 0 || status === 303)).toBe(true);

  const auditNow = Date.now();
  const auditQuery = new URLSearchParams({
    from: new Date(auditNow - 60 * 60 * 1000).toISOString().replace(".000Z", "Z"),
    to: new Date(auditNow + 60 * 60 * 1000).toISOString().replace(".000Z", "Z"),
  });
  const response = await page.goto(`/audit?${auditQuery.toString()}`);
  expect(response?.headers()["cache-control"]).toBe("no-store");
  await expect(page.getByRole("heading", {
    name: "Security audit events",
    exact: true,
  })).toBeVisible();
  await expect(page.getByText(
    "Security audit events are retained separately from application logs.",
  )).toBeVisible();
  expect(await page.locator(".audit-table tbody tr").count()).toBe(100);
  const older = page.getByRole("link", { name: "Load older audit events" });
  await expect(older).toBeVisible();
  await older.click();
  await expect(page.locator(".audit-table tbody tr")).not.toHaveCount(0);

  await page.getByLabel("Category").selectOption("source_administration");
  await page.getByLabel("Outcome").selectOption("succeeded");
  await page.getByLabel("Exact action").fill("source.alias_set");
  await page.getByRole("button", { name: "Apply audit filters" }).click();
  await expect(page.locator("code", { hasText: "source.alias_set" }).first()).toBeVisible();
  await expect(page.getByText(hostileAlias).first()).toBeVisible();
  await expect(page.locator(".audit-table img")).toHaveCount(0);
  expect(await page.evaluate(
    () => (window as Window & { siftailSourceXSS?: boolean }).siftailSourceXSS,
  )).toBeUndefined();

  await page.locator(".audit-table-scroll").focus();
  await expect(page.locator(".audit-table-scroll")).toBeFocused();
  await setTheme(page, "dark");
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/audit-mobile-light.png",
    fullPage: true,
  });
});

test("keyboard, themes, reduced motion, mobile inspection, and axe smoke", async ({ page }) => {
  await login(page);
  await setTheme(page, "dark");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  // The preference persists server-side and is rendered before first paint,
  // so a full reload keeps the theme with no flash and no client storage.
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator("[data-theme-select]")).toHaveValue("dark");
  const darkResults = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(darkResults.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/history-desktop-dark.png",
    fullPage: true,
  });
  await page.keyboard.press("f");
  await expect(page.locator("#contains-filter")).toBeFocused();
  await page.locator("#contains-filter").fill("");
  await setTheme(page, "light");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await page.emulateMedia({ reducedMotion: "reduce" });
  const transitionDuration = await page.locator(".log-row").first().evaluate(
    (element) => getComputedStyle(element).transitionDuration,
  );
  expect(Number.parseFloat(transitionDuration)).toBeLessThan(0.001);

  await page.setViewportSize({ width: 390, height: 844 });
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  expect(overflow).toBe(false);
  await page.locator(".event-toggle").first().click();
  await expect(page.locator(".event-detail").first()).toBeVisible();
  await page.screenshot({
    path: ".playwright-artifacts/history-mobile-light-details.png",
    fullPage: true,
  });

  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
});

test("guided ingestion handles session expiry without retaining one-time material", async ({
  page,
}) => {
  await login(page);
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await page.getByRole("link", { name: "Browser managed" }).click();
  await page.locator("#token-name").fill("expired-guide");
  await page.getByRole("button", { name: "Create token" }).click();
  const token = await page.locator("[data-token-secret]").inputValue();
  const state = readState();
  execFileSync(state.binary, ["sessions", "revoke-all"], {
    env: siftailEnvironment({ SIFTAIL_DATA_DIR: state.dataDirectory }),
    encoding: "utf8",
  });
  await page.getByRole("button", { name: "Send guided test" }).click();
  await expect(page).toHaveURL(/\/login\?.*expired=1/);
  expect(await page.content()).not.toContain(token);
});

test("server-side session invalidation redirects the active browser", async ({ page }) => {
  await login(page);
  const state = readState();
  const output = execFileSync(state.binary, ["sessions", "revoke-all"], {
    env: siftailEnvironment({ SIFTAIL_DATA_DIR: state.dataDirectory }),
    encoding: "utf8",
  });
  expect(output).toMatch(/^revoked \d+ administrator session/);
  await page.reload();
  await expect(page).toHaveURL(/\/login\?return=.*expired=1/);
  await expect(page.getByText("Your session expired")).toBeVisible();
});

test("session cookie follows the http public URL and Settings signs out other sessions", async ({
  page,
  browser,
}) => {
  await login(page);
  const cookies = await page.context().cookies();
  const session = cookies.find((cookie) => cookie.name === "siftail_session");
  expect(session).toMatchObject({
    httpOnly: true, secure: false, sameSite: "Strict", path: "/",
  });
  expect(cookies.some((cookie) => cookie.name.startsWith("__Host-"))).toBe(false);
  const lifetimeDays = ((session?.expires ?? 0) * 1000 - Date.now()) / 86_400_000;
  expect(lifetimeDays).toBeGreaterThan(6.99);
  expect(lifetimeDays).toBeLessThanOrEqual(7);

  const otherContext = await browser.newContext();
  const otherPage = await otherContext.newPage();
  await login(otherPage);

  await navigateSystem(page, "Settings");
  const sessions = page.getByRole("region", { name: "Sessions" });
  await expect(sessions.getByText(/^\d+ other active sessions?\.$/)).toBeVisible();
  await expect(sessions.getByText(
    "A session ends after 12 hours without use and 7 days after sign-in",
    { exact: false },
  )).toBeVisible();
  await setTheme(page, "dark");
  await page.screenshot({ path: ".playwright-artifacts/settings-sessions-dark.png", fullPage: true });
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);

  const signOutOthers = sessions.getByRole("button", { name: "Sign out other sessions" });
  await signOutOthers.focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByText(
    "Other sessions signed out. This browser stays signed in.",
  )).toBeVisible();
  await expect(sessions.getByText("No other active sessions.")).toBeVisible();

  await otherPage.reload();
  await expect(otherPage).toHaveURL(/\/login\?return=.*expired=1/);
  await expect(otherPage.getByText("Your session expired")).toBeVisible();
  await otherContext.close();
  await page.reload();
  await expect(page).toHaveURL(/\/settings$/);

  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  expect(await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  )).toBe(false);
  await sessions.scrollIntoViewIfNeeded();
  await page.screenshot({ path: ".playwright-artifacts/settings-sessions-mobile-light.png", fullPage: true });
  results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.setViewportSize({ width: 1280, height: 720 });
  await setTheme(page, "system");

  const auditNow = Date.now();
  const auditQuery = new URLSearchParams({
    from: new Date(auditNow - 60 * 60 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z"),
    to: new Date(auditNow + 60 * 60 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z"),
    action: "session.revoke_others",
  });
  await page.goto(`/audit?${auditQuery.toString()}`);
  await expect(page.locator(".audit-table tbody tr code", {
    hasText: "session.revoke_others",
  }).first()).toBeVisible();
});

test("login behind an https public URL uses a __Host- cookie, HSTS, and only that origin", async () => {
  const state = readState();
  const dataDirectory = path.join(state.runDirectory, "https-data");
  fs.mkdirSync(dataDirectory, { mode: 0o700 });
  const uiAddress = process.env.SIFTAIL_PLAYWRIGHT_HTTPS_UI_ADDR ?? "127.0.0.1:19082";
  const ingestAddress = process.env.SIFTAIL_PLAYWRIGHT_HTTPS_INGEST_ADDR ?? "127.0.0.1:19083";
  const publicURL = "https://logs.playwright.test";
  const env = siftailEnvironment({
    SIFTAIL_DATA_DIR: dataDirectory,
    SIFTAIL_UI_ADDR: uiAddress,
    SIFTAIL_INGEST_ADDR: ingestAddress,
    SIFTAIL_PUBLIC_URL: publicURL,
  });
  execFileSync(state.binary, ["admin", "create", "--username", administratorUsername], {
    env, input: `${administratorPassword}\n${administratorPassword}\n`, encoding: "utf8",
  });
  const server = spawn(state.binary, ["serve"], { env, stdio: "ignore" });
  const exited = new Promise((resolve) => server.once("exit", resolve));
  // The test plays the TLS-terminating proxy: it reaches the UI listener over
  // loopback while the browser-visible origin is the https public URL.
  const api = await playwrightRequest.newContext({ baseURL: `http://${uiAddress}` });
  try {
    await expect.poll(async () => {
      try {
        return (await api.get("/health/ready")).status();
      } catch {
        return 0;
      }
    }, { timeout: 10_000 }).toBe(200);

    const loginPage = await api.get("/login");
    expect(loginPage.headers()["strict-transport-security"]).toBe("max-age=31536000");
    const ingestHealth = await api.get(`http://${ingestAddress}/health/live`);
    expect(ingestHealth.headers()["strict-transport-security"]).toBeUndefined();

    const form = { username: administratorUsername, password: administratorPassword };
    // DNS rebinding: an Origin that only matches the Host header is refused.
    const rebound = await api.post("/session", {
      form, headers: { Origin: `http://${uiAddress}` }, maxRedirects: 0,
    });
    expect(rebound.status()).toBe(403);

    const signedIn = await api.post("/session", {
      form, headers: { Origin: publicURL }, maxRedirects: 0,
    });
    expect(signedIn.status()).toBe(303);
    const setCookies = signedIn.headersArray()
      .filter((header) => header.name.toLowerCase() === "set-cookie")
      .map((header) => header.value);
    const session = setCookies.find((value) => value.startsWith("__Host-siftail_session="));
    expect(session).toBeDefined();
    expect(session).toMatch(/; Path=\/(;|$)/);
    expect(session).toContain("; HttpOnly");
    expect(session).toContain("; Secure");
    expect(session).toContain("; SameSite=Strict");
    expect(session?.toLowerCase()).not.toContain("domain=");
    expect(setCookies.some((value) => /^siftail_session=;.*Max-Age=0/.test(value))).toBe(true);
  } finally {
    await api.dispose();
    server.kill("SIGTERM");
    await exited;
  }
});

test("login failures are uniform and throttle the fifth attempt", async ({ page }) => {
  const fail = async (username: string) => {
    await page.goto("/login");
    await page.locator("#username").fill(username);
    await page.locator("#password").fill("wrong-browser-password");
    const response = page.waitForResponse(
      (candidate) => candidate.url().endsWith("/session") &&
        candidate.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Sign in" }).click();
    return response;
  };
  const known = await fail(administratorUsername);
  const knownError = await page.locator("#login-error").textContent();
  const unknown = await fail("MissingBrowserAdmin");
  const unknownError = await page.locator("#login-error").textContent();
  expect(known.status()).toBe(401);
  expect(unknown.status()).toBe(401);
  expect(unknownError).toBe(knownError);
  await fail("MissingBrowserAdmin");
  await fail("MissingBrowserAdmin");
  const limited = await fail("MissingBrowserAdmin");
  expect(limited.status()).toBe(429);
  expect(Number(limited.headers()["retry-after"])).toBeGreaterThan(0);
  await expect(page.getByText(/Too many attempts/)).toBeVisible();
});
