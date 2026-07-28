import { execFileSync } from "node:child_process";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import {
  administratorPassword,
  administratorUsername,
  readState,
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

test("cursor pagination appends without replacing existing rows", async ({ page }) => {
  await login(page);
  const firstID = await page.locator(".log-row").first().getAttribute("data-event-id");
  await expect(page.getByRole("button", { name: "Load older" })).toBeVisible();
  await page.getByRole("button", { name: "Load older" }).click();
  await expect(page.locator(".log-row")).toHaveCount(220);
  await expect(page.locator(".log-row").first()).toHaveAttribute("data-event-id", firstID ?? "");
  await expect(page.locator(".pagination-end")).toContainText("End of matching events");
});

test("inline details remain hostile text and preserve focus and clipboard content", async ({
  context,
  page,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await login(page);
  const hostileRow = page.locator(".log-row").filter({ hasText: "needle-hostile" });
  const toggle = hostileRow.getByRole("button", { name: /Show details/ });
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

test("Live pause, pending, clear-view, filters, reconnect, focus, and safe rendering", async ({
  page,
}) => {
  await trackEventSources(page);
  await login(page);
  await page.getByRole("tab", { name: "Live" }).click();
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
  await page.getByRole("tab", { name: "History" }).click();
  await expect(page.locator(".log-row").filter({ hasText: "paused-three" })).toHaveCount(1);
  await page.getByRole("tab", { name: "Live" }).click();
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

test("Live enforces rendered and pending caps and does not steal scroll", async ({ page }) => {
  test.setTimeout(90_000);
  await login(page);
  await page.getByRole("tab", { name: "Live" }).click();
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

  await page.locator("[data-theme-select]").selectOption("dark");
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/live-desktop-dark.png",
    fullPage: true,
  });
  await page.locator("[data-theme-select]").selectOption("light");
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

  await page.locator("[data-theme-select]").selectOption("dark");
  let results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(results.violations.map((violation) => violation.id)).toEqual([]);
  await page.screenshot({
    path: ".playwright-artifacts/sources-desktop-dark.png",
    fullPage: true,
  });
  await page.locator("[data-theme-select]").selectOption("light");
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
  await expect(page).toHaveURL(/\/sources\?notice=source-removed/);
  await expect(page.getByText(
    "Source removed. An active sender may discover it again.",
  )).toBeVisible();
  await expect(page.getByRole("row").filter({ hasText: "mutation-worker / jobs" })).toHaveCount(0);
  await expect(page.getByRole("row").filter({ hasText: hostileAlias })).toHaveCount(1);
});

test("keyboard, themes, reduced motion, mobile inspection, and axe smoke", async ({ page }) => {
  await login(page);
  await page.locator("[data-theme-select]").selectOption("dark");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
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
  await page.locator("[data-theme-select]").selectOption("light");
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
