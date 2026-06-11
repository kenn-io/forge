import { defineConfig, devices } from "@playwright/test";

// Drives the built setup page (dist/) in a real browser against an
// in-test fake of the Go flow server and GitHub's manifest endpoint.
// Run `vp build` first; the package "test:e2e" script does both.
export default defineConfig({
  testDir: "tests",
  timeout: 30_000,
  use: {
    ...devices["Desktop Chrome"],
  },
});
