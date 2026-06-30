import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: /docs-screenshots\.spec\.ts/,
  fullyParallel: false,
  workers: 1,
  timeout: 120_000,
  expect: {
    timeout: 10_000,
  },
  use: {
    ...devices["Desktop Chrome"],
    viewport: { width: 1280, height: 820 },
    deviceScaleFactor: 1,
    trace: "off",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1280, height: 820 },
        deviceScaleFactor: 1,
      },
    },
  ],
});
