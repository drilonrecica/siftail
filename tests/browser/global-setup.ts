import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import {
  administratorPassword,
  administratorUsername,
  siftailEnvironment,
  statePath,
  type BrowserState,
} from "./support";

const root = path.resolve(".");

function run(binary: string, args: string[], env: NodeJS.ProcessEnv, input?: string): string {
  return execFileSync(binary, args, {
    cwd: root,
    env,
    input,
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
  });
}

async function waitForReady(url: string): Promise<void> {
  let lastError = "not started";
  for (let attempt = 0; attempt < 100; attempt += 1) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
      lastError = `HTTP ${response.status}`;
    } catch (error) {
      lastError = String(error);
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`Siftail did not become ready: ${lastError}`);
}

function fixtures(): Record<string, unknown>[] {
  const now = Date.now();
  const events: Record<string, unknown>[] = [];
  for (let index = 0; index < 220; index += 1) {
    const alternateSource = index >= 210;
    events.push({
      timestamp: new Date(now - (index + 5) * 1000).toISOString(),
      project: "browser-project",
      environment: "test",
      application: alternateSource ? "worker" : "api",
      service: alternateSource ? "jobs" : "web",
      container_id: alternateSource ? "worker-container" : "api-container",
      container_name: alternateSource ? "worker-1" : "api-1",
      stream: index % 2 === 0 ? "stderr" : "stdout",
      level: index % 3 === 0 ? "ERROR" : index % 3 === 1 ? "WARN" : "INFO",
      log: index === 0
        ? "<script>window.siftailXSS=true</script> needle-hostile first line\nsecond line"
        : `ordinary browser event ${String(index).padStart(3, "0")}`,
      request_id: index === 0 ? "browser-request-1" : undefined,
      logger: index === 0 ? "browser-http" : undefined,
      http_method: index === 0 ? "POST" : undefined,
      http_path: index === 0 ? "/unsafe/<path>" : undefined,
      http_status: index === 0 ? 503 : undefined,
      error_type: index === 0 ? "temporary" : undefined,
      nested: index === 0
        ? { z: "<img src=x onerror=window.siftailXSS=true>", a: 1 }
        : undefined,
    });
  }
  return events;
}

export default async function globalSetup(): Promise<void> {
  const screenshotDirectory = path.join(root, ".playwright-artifacts");
  fs.rmSync(screenshotDirectory, { recursive: true, force: true });
  fs.mkdirSync(screenshotDirectory, { mode: 0o700 });
  const runDirectory = fs.mkdtempSync(path.join(os.tmpdir(), "siftail-playwright-"));
  const dataDirectory = path.join(runDirectory, "data");
  const binary = path.join(runDirectory, "siftail");
  fs.mkdirSync(dataDirectory, { mode: 0o700 });
  const uiAddress = process.env.SIFTAIL_PLAYWRIGHT_UI_ADDR ?? "127.0.0.1:19080";
  const ingestAddress = process.env.SIFTAIL_PLAYWRIGHT_INGEST_ADDR ?? "127.0.0.1:19081";
  const env = siftailEnvironment({
    SIFTAIL_DATA_DIR: dataDirectory,
    SIFTAIL_UI_ADDR: uiAddress,
    SIFTAIL_INGEST_ADDR: ingestAddress,
    SIFTAIL_PUBLIC_URL: `http://${uiAddress}`,
  });
  let pid = 0;
  try {
    execFileSync("go", ["build", "-o", binary, "./cmd/siftail"], {
      cwd: root,
      env,
      stdio: "inherit",
    });
    run(binary, ["admin", "create", "--username", administratorUsername], env,
      `${administratorPassword}\n${administratorPassword}\n`);
    run(binary, ["server", "create", "--name", "Browser"], env);
    const tokenOutput = run(binary, ["token", "create", "--server", "1", "--name", "browser"], env);
    const token = tokenOutput.match(/^token \(shown once\): (.+)$/m)?.[1];
    if (!token) throw new Error("production token command did not return its one-time token");

    const log = fs.openSync(path.join(runDirectory, "siftail.log"), "a");
    const server = spawn(binary, ["serve"], {
      cwd: root,
      env,
      detached: true,
      stdio: ["ignore", log, log],
    });
    fs.closeSync(log);
    server.unref();
    pid = server.pid ?? 0;
    if (pid <= 0) throw new Error("Siftail process did not start");
    await waitForReady(`http://${uiAddress}/health/ready`);

    const response = await fetch(`http://${ingestAddress}/api/v1/ingest`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(fixtures()),
    });
    if (response.status !== 204) {
      throw new Error(`fixture ingestion returned HTTP ${response.status}`);
    }
    const state: BrowserState = {
      pid, runDirectory, binary, dataDirectory, uiAddress, ingestAddress,
    };
    fs.writeFileSync(statePath, JSON.stringify(state), { mode: 0o600 });
  } catch (error) {
    if (pid > 0) {
      try {
        process.kill(pid, "SIGTERM");
      } catch {
        // The failed child already exited.
      }
    }
    fs.rmSync(runDirectory, { recursive: true, force: true });
    throw error;
  }
}
