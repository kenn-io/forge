import { defineConfig, devices } from "@playwright/test";
import path from "node:path";

const siteDirInput = process.env.KENN_FORGE_DOCS_SITE_DIR;

if (!siteDirInput) {
  throw new Error("KENN_FORGE_DOCS_SITE_DIR must point to rendered site output");
}
const siteDir = path.resolve(siteDirInput);

export default defineConfig({
  testDir: ".",
  testMatch: "docs-site.spec.ts",
  workers: 1,
  // CI also writes a JSON report per suite for our flake monitor. Matrix jobs
  // name their suite with KENN_FORGE_PLAYWRIGHT_SUITE so each run keeps its own file.
  reporter: process.env.CI
    ? [
        ["list"],
        [
          "json",
          { outputFile: `test-results/playwright/${process.env.KENN_FORGE_PLAYWRIGHT_SUITE ?? "docs-site"}.json` },
        ],
      ]
    : "list",
  retries: process.env.CI ? 2 : 0,
  use: {
    baseURL: "http://127.0.0.1:4178",
  },
  projects: [
    {
      name: "chromium",
      grepInvert: /\[webkit\]/,
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1280, height: 800 },
      },
    },
    {
      name: "webkit-iphone",
      grep: /\[webkit\]/,
      use: {
        ...devices["iPhone 13"],
        browserName: "webkit",
      },
    },
  ],
  webServer: {
    command: "python3 -m http.server 4178 --bind 127.0.0.1",
    cwd: siteDir,
    url: "http://127.0.0.1:4178",
    reuseExistingServer: false,
  },
});
