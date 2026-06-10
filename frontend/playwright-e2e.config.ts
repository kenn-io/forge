import { defineConfig, devices } from "@playwright/test";
import { ensureE2EServer } from "./tests/e2e-full/support/e2eServer";

const serverInfo = await ensureE2EServer();

export default defineConfig({
  testDir: "./tests/e2e-full",
  testIgnore: /support\//,
  fullyParallel: true,
  workers: 2,
  timeout: 30_000,
  retries: process.env.CI ? 2 : 0,
  expect: {
    timeout: 5_000,
  },
  use: {
    baseURL: serverInfo.base_url,
    trace: "on-first-retry",
  },
  globalTeardown: "./tests/e2e-full/support/e2eServerTeardown.ts",
  projects: [
    {
      name: "chromium",
      testIgnore: /roborev/,
      use: {
        ...devices["Desktop Chrome"],
      },
    },
    {
      name: "firefox",
      testIgnore: /roborev/,
      use: {
        ...devices["Desktop Firefox"],
      },
    },
    {
      name: "webkit",
      testIgnore: /roborev/,
      use: {
        ...devices["Desktop Safari"],
        deviceScaleFactor: 1,
      },
    },
    {
      name: "roborev",
      testMatch: /roborev/,
      fullyParallel: false,
      workers: 1,
      use: {
        ...devices["Desktop Chrome"],
      },
    },
    {
      name: "roborev-firefox",
      testMatch: /roborev/,
      fullyParallel: false,
      workers: 1,
      use: {
        ...devices["Desktop Firefox"],
      },
    },
  ],
});
