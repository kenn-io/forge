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
  use: {
    baseURL: "http://127.0.0.1:4178",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1280, height: 800 },
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
