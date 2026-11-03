import { defineConfig, devices } from "@playwright/test";
import { e2eReuseExistingServer, getAvailablePort, parseE2EPort } from "./src/lib/dev/e2ePort";

const host = "127.0.0.1";
const port = parseE2EPort(process.env.PLAYWRIGHT_PORT) ?? (await getAvailablePort(host));
process.env.PLAYWRIGHT_PORT = String(port);
const baseURL = `http://${host}:${port}`;
// Bound concurrent browser instances on shared CI runners.
const sharedViteWorkerLimit = 7;

function ciWorkers(): number | undefined {
  if (!process.env.CI) return undefined;
  const configured = Number.parseInt(process.env.KENN_FORGE_CI_WORKERS ?? "", 10);
  return configured > 0 ? Math.min(configured, sharedViteWorkerLimit) : sharedViteWorkerLimit;
}

const workers = ciWorkers();

export default defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: true,
  ...(workers ? { workers } : {}),
  timeout: 30_000,
  // CI also writes a JSON report per suite for our flake monitor. Matrix jobs
  // name their suite with KENN_FORGE_PLAYWRIGHT_SUITE so each run keeps its own file.
  reporter: process.env.CI
    ? [
        ["list"],
        [
          "json",
          { outputFile: `test-results/playwright/${process.env.KENN_FORGE_PLAYWRIGHT_SUITE ?? "e2e-mock"}.json` },
        ],
      ]
    : "list",
  retries: process.env.CI ? 2 : 0,
  expect: {
    timeout: 5_000,
  },
  use: {
    baseURL,
    trace: "on-first-retry",
  },
  webServer: {
    command: `node ../node_modules/vite-plus/bin/vp build --logLevel warn && node ../node_modules/vite-plus/bin/vp preview --host ${host} --port ${port} --strictPort`,
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
  ],
});
