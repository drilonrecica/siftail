import fs from "node:fs";
import path from "node:path";

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
