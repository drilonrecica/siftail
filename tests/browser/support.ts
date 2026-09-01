import fs from "node:fs";
import path from "node:path";
import { type Page } from "@playwright/test";

export const administratorUsername = "BrowserAdmin";
export const administratorPassword = "browser-test-password";
export const statePath = path.resolve(".playwright-state.json");

export type BrowserState = {
  pid: number;
  runDirectory: string;
  binary: string;
  dataDirectory: string;
  uiAddress: string;
  ingestAddress: string;
};

export function readState(): BrowserState {
  return JSON.parse(fs.readFileSync(statePath, "utf8")) as BrowserState;
}

export function siftailEnvironment(overrides: NodeJS.ProcessEnv = {}): NodeJS.ProcessEnv {
  const env = Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !name.startsWith("SIFTAIL_PLAYWRIGHT_")),
  );
  return { ...env, ...overrides };
}

// The System menu is a native <details>: its navigation links and the
// preference controls only become visible while it is open, so every
// interaction with them goes through these helpers.

export async function openSystemMenu(page: Page): Promise<void> {
  const menu = page.locator("[data-system-menu]");
  const open = await menu.evaluate(
    (element) => (element as HTMLDetailsElement).open,
  );
  if (!open) {
    await menu.locator("summary").click();
  }
}

export async function closeSystemMenu(page: Page): Promise<void> {
  const menu = page.locator("[data-system-menu]");
  await menu.evaluate((element) => {
    (element as HTMLDetailsElement).open = false;
  });
}

export async function navigateSystem(
  page: Page,
  name: "Status" | "Settings" | "Audit" | "Backup",
): Promise<void> {
  await openSystemMenu(page);
  await page
    .locator(".system-menu-nav")
    .getByRole("link", { name, exact: true })
    .click();
}

export async function setTheme(
  page: Page,
  theme: "system" | "dark" | "light",
): Promise<void> {
  await openSystemMenu(page);
  await page.locator("[data-theme-select]").selectOption(theme);
  await closeSystemMenu(page);
}

export async function setDensity(
  page: Page,
  density: "compact" | "comfortable",
): Promise<void> {
  await openSystemMenu(page);
  await page.locator("[data-density-select]").selectOption(density);
  await closeSystemMenu(page);
}
