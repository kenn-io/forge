import { defineConfig, devices } from "@playwright/test";

// Drives the built setup page (dist/) in a real browser against an
// in-test fake of the Go flow server and GitHub's manifest endpoint.
// Run `vp build` first; the package "test:e2e" script does both.
export default defineConfig({
  testDir: "tests",
  timeout: 30_000,
  // CI also writes a JSON report per suite for our flake monitor. Matrix jobs
  // name their suite with KENN_FORGE_PLAYWRIGHT_SUITE so each run keeps its own file.
  reporter: process.env.CI
    ? [
        ["list"],
        [
          "json",
          { outputFile: `test-results/playwright/${process.env.KENN_FORGE_PLAYWRIGHT_SUITE ?? "github-app-ui"}.json` },
        ],
      ]
    : "list",
  retries: process.env.CI ? 2 : 0,
  use: {
    ...devices["Desktop Chrome"],
  },
});
