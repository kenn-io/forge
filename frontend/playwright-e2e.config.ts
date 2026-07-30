import { defineConfig, devices } from "@playwright/test";
import { ensureE2EServer } from "./tests/e2e-full/support/e2eServer";

const serverInfo = await ensureE2EServer();

// Chromium can use twice the configured CI baseline. Firefox's heavier
// per-worker processes stay at the baseline so both managed and hosted
// runners can select concurrency that matches their available resources.
function configuredWorkers(): number | string {
  const args = process.argv.join(" ");
  const firefox = /--project[= ]firefox/.test(args);
  if (process.env.CI) {
    const configured = Number.parseInt(process.env.KENN_FORGE_CI_WORKERS ?? "", 10);
    const baseline = configured > 0 ? configured : 14;
    return firefox ? baseline : baseline * 2;
  }
  return firefox ? "50%" : "75%";
}

export default defineConfig({
  testDir: "./tests/e2e-full",
  testIgnore: /support\//,
  fullyParallel: true,
  workers: configuredWorkers(),
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
