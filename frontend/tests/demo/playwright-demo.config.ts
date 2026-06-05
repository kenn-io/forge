import path from "node:path";
import { fileURLToPath } from "node:url";

import { defineConfig, devices } from "@playwright/test";

const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");

export default defineConfig({
  testDir: ".",
  timeout: 30_000,
  expect: { timeout: 5_000 },
  use: {
    baseURL: "http://127.0.0.1:4173",
    trace: "off",
    screenshot: "off",
  },
  webServer: {
    command: "bun run dev --host 127.0.0.1 --port 4173 --strictPort",
    port: 4173,
    cwd: frontendDir,
    reuseExistingServer: true,
    timeout: 120_000,
  },
  projects: [
    {
      name: "demo",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 360, height: 640 },
      },
    },
  ],
});
