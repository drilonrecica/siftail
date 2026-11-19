// H3 gate (SFT-126): captures the same curated screens from one Siftail
// binary, so builds of different commits can be compared side by side, and
// prints each screen's first log-row offset (ADR 0011 D1).
//
//   node capture.mjs <repoRoot> <binary> <outDir> <cookieSuffix> <uiPort> <ingestPort>
//
// cookieSuffix is "" for the pre-H3 "theme.density" preference cookie and
// ".on.UTC" for the current four-field cookie. The work directory is created
// beside outDir and the server runs with a relative data directory, so the
// control socket stays within the 108-byte sun_path limit. Uses the
// repository's @playwright/test; development-only, never shipped.
import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const [repoRoot, binary, outDir, cookieSuffix, uiPort, ingestPort] = process.argv.slice(2);
const require = createRequire(path.join(repoRoot, "package.json"));
const { chromium } = require("@playwright/test");

const work = fs.mkdtempSync(path.join(path.dirname(outDir), "cap-"));
const data = path.join(work, "data");
fs.mkdirSync(data, { mode: 0o700 });
fs.mkdirSync(outDir, { recursive: true });
const ui = `127.0.0.1:${uiPort}`;
const ingest = `127.0.0.1:${ingestPort}`;
const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith("SIFTAIL_")));
Object.assign(env, {
  SIFTAIL_DATA_DIR: "data", SIFTAIL_UI_ADDR: ui, SIFTAIL_INGEST_ADDR: ingest,
  SIFTAIL_PUBLIC_URL: `http://${ui}`, SIFTAIL_INGEST_PUBLIC_URL: `http://${ingest}/api/v1/ingest`,
});
const run = (args, input) => execFileSync(binary, args, { cwd: work, env, input, encoding: "utf8" });
run(["admin", "create", "--username", "GateAdmin"], "gate-review-password\ngate-review-password\n");
run(["server", "create", "--name", "Production"]);
const token = run(["token", "create", "--server", "1", "--name", "drain"]).match(/^token \(shown once\): (.+)$/m)[1];
const logFd = fs.openSync(path.join(path.dirname(outDir), "serve.log"), "w");
const server = spawn(binary, ["serve"], { cwd: work, env, stdio: ["ignore", logFd, logFd] });
try {
  for (let i = 0; i < 100; i++) {
    try { if ((await fetch(`http://${ui}/health/ready`)).ok) break; } catch {}
    await new Promise((r) => setTimeout(r, 50));
  }
  // Fixed, realistic fixtures so both builds show the same events.
  const base = Date.now() - 10 * 60 * 1000;
  const apps = [["shop", "api"], ["shop", "worker"], ["billing", "web"]];
  const messages = [
    ["INFO", "GET /api/orders 200 12ms"],
    ["INFO", "order 4821 queued for fulfilment"],
    ["WARN", "slow query: SELECT * FROM orders WHERE customer_id = $1 took 812ms"],
    ["ERROR", "payment provider timeout after 30s\n  at charge (billing/stripe.go:88)\n  at handle (billing/http.go:41)"],
    ["DEBUG", "cache hit ratio 0.93"],
    ["INFO", "POST /api/checkout 201 48ms"],
    ["FATAL", "database connection refused: dial tcp 10.0.0.5:5432: connect: connection refused"],
  ];
  const events = [];
  for (let i = 0; i < 120; i++) {
    const [application, service] = apps[i % apps.length];
    const [level, log] = messages[i % messages.length];
    events.push({
      timestamp: new Date(base + i * 4000).toISOString(),
      project: "storefront", environment: "production", application, service,
      container_id: `${application}-${service}-1`, container_name: `${application}-${service}-1`,
      stream: level === "INFO" || level === "DEBUG" ? "stdout" : "stderr", level, log,
    });
  }
  const post = (body) => fetch(`http://${ingest}/api/v1/ingest`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if ((await post(events)).status !== 204) throw new Error("ingest failed");

  const browser = await chromium.launch();
  const shots = [
    // name, theme, width, height, action
    ["login-desktop-dark", "dark", 1280, 800, "login"],
    ["history-desktop-dark", "dark", 1280, 800, "history"],
    ["history-desktop-light", "light", 1280, 800, "history"],
    ["history-expanded-desktop-dark", "dark", 1280, 800, "expanded"],
    ["live-desktop-dark", "dark", 1280, 800, "live"],
    ["palette-desktop-light", "light", 1280, 800, "palette"],
    ["sources-desktop-light", "light", 1280, 800, "/sources"],
    ["status-desktop-dark", "dark", 1280, 800, "/status"],
    ["history-mobile-dark", "dark", 390, 844, "history"],
    ["history-mobile-light", "light", 390, 844, "history"],
    ["settings-mobile-light", "light", 390, 844, "/settings"],
  ];
  for (const [name, theme, width, height, action] of shots) {
    const context = await browser.newContext({ viewport: { width, height }, colorScheme: theme });
    const value = cookieSuffix ? `${theme}.compact${cookieSuffix}` : `${theme}.compact`;
    await context.addCookies([{ name: "siftail_prefs", value, url: `http://${ui}` }]);
    const page = await context.newPage();
    await page.goto(`http://${ui}/login`);
    if (action !== "login") {
      await page.fill("#username", "GateAdmin");
      await page.fill("#password", "gate-review-password");
      await page.click("button[type=submit]");
      await page.waitForURL(/\/logs/);
      await page.waitForSelector(".log-row");
      if (action === "expanded") {
        await page.locator(".log-row", { hasText: "payment provider" }).first().locator(".event-toggle").click();
        await page.waitForSelector(".event-detail");
      } else if (action === "palette") {
        await page.locator("[data-palette-open]").click();
        await page.waitForSelector("dialog[open]");
        await page.keyboard.type("bill");
      } else if (action === "live") {
        await page.goto(`http://${ui}/logs?mode=live`);
        await page.waitForTimeout(800);
        const now = Date.now();
        await post(messages.map(([level, log], i) => ({
          timestamp: new Date(now + i).toISOString(), project: "storefront", environment: "production",
          application: "shop", service: "api", container_id: "shop-api-1", container_name: "shop-api-1",
          stream: "stderr", level, log,
        })));
        await page.waitForTimeout(1500);
      } else if (action.startsWith("/")) {
        await page.goto(`http://${ui}${action}`);
      }
    }
    await page.waitForTimeout(300);
    const firstRow = page.locator(".log-row").first();
    const top = (await firstRow.count()) === 0 ? null : await firstRow
      .evaluate((row) => Math.round(row.getBoundingClientRect().top + window.scrollY));
    console.log(`${name}\tfirst-row-top=${top}`);
    await page.screenshot({ path: path.join(outDir, `${name}.jpg`), type: "jpeg", quality: 82 });
    await context.close();
  }
  await browser.close();
} finally {
  server.kill("SIGTERM");
  fs.rmSync(work, { recursive: true, force: true });
}
