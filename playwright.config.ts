import { defineConfig, devices } from "@playwright/test";

const uiAddress = process.env.SIFTAIL_PLAYWRIGHT_UI_ADDR ?? "127.0.0.1:19080";

export default defineConfig({
  testDir: "./tests/browser",
  fullyParallel: false,
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  globalSetup: "./tests/browser/global-setup.ts",
  globalTeardown: "./tests/browser/global-teardown.ts",
  reporter: [["line"], ["html", { open: "never" }]],
  outputDir: "test-results",
  use: {
    baseURL: `http://${uiAddress}`,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        launchOptions: process.env.SIFTAIL_PLAYWRIGHT_CHROMIUM
          ? { executablePath: process.env.SIFTAIL_PLAYWRIGHT_CHROMIUM }
          : {},
      },
    },
  ],
});
