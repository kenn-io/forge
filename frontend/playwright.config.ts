import { defineConfig, devices } from "@playwright/test";
import { e2eReuseExistingServer, getAvailablePort, parseE2EPort } from "./src/lib/dev/e2ePort";

const host = "127.0.0.1";
const port = parseE2EPort(process.env.PLAYWRIGHT_PORT) ?? (await getAvailablePort(host));
process.env.PLAYWRIGHT_PORT = String(port);
const baseURL = `http://${host}:${port}`;

export default defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: true,
  timeout: 30_000,
  retries: process.env.CI ? 2 : 0,
  expect: {
    timeout: 5_000,
  },
  use: {
    baseURL,
    trace: "on-first-retry",
  },
  webServer: {
    command: `bun run dev --host ${host} --port ${port} --strictPort`,
    url: baseURL,
    reuseExistingServer: e2eReuseExistingServer(),
    timeout: 120_000,
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
      },
    },
    {
      name: "firefox",
      use: {
        ...devices["Desktop Firefox"],
      },
    },
    {
      name: "webkit",
      use: {
        ...devices["Desktop Safari"],
        deviceScaleFactor: 1,
      },
    },
  ],
});
