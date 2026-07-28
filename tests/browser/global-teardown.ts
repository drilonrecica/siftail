import fs from "node:fs";
import { readState, statePath } from "./support";

export default async function globalTeardown(): Promise<void> {
  if (!fs.existsSync(statePath)) return;
  const state = readState();
  const running = () => {
    try {
      process.kill(state.pid, 0);
      return true;
    } catch {
      return false;
    }
  };
  try {
    process.kill(state.pid, "SIGTERM");
  } catch {
    // An already-exited process needs no further signal.
  }
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (!running()) break;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (running()) {
    process.kill(state.pid, "SIGKILL");
    for (let attempt = 0; attempt < 20 && running(); attempt += 1) {
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }
  if (running()) {
    throw new Error(`Siftail browser fixture process ${state.pid} did not stop`);
  }
  fs.rmSync(state.runDirectory, { recursive: true, force: true });
  fs.rmSync(statePath, { force: true });
}
