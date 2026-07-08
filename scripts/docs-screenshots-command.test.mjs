import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

async function readJSON(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

test("docs screenshot regeneration uses root Playwright dependencies", async () => {
  const [rootPkg, spec, config, readme] = await Promise.all([
    readJSON("package.json"),
    readFile("docs/screenshots/docs-screenshots.spec.ts", "utf8"),
    readFile("docs/screenshots/playwright.config.ts", "utf8"),
    readFile("docs/screenshots/README.md", "utf8"),
  ]);

  assert.equal(
    rootPkg.scripts?.["docs:screenshots"],
    "node node_modules/vite-plus/bin/vp exec -- playwright test --config docs/screenshots/playwright.config.ts --project=chromium",
  );
  // The exact version is checked against the CI container by
  // scripts/check-playwright-version.mjs; here only the exact-pin shape
  // matters, so Playwright bumps don't have to touch this test.
  assert.match(
    rootPkg.devDependencies?.["@playwright/test"] ?? "",
    /^\d+\.\d+\.\d+$/,
    "root package.json must pin an exact @playwright/test version",
  );

  const list = spawnSync(process.execPath, ["node_modules/vite-plus/bin/vp", "run", "docs:screenshots", "--list"], {
    encoding: "utf8",
  });
  assert.equal(list.status, 0, list.stderr || list.stdout);
  assert.match(list.stdout, /docs workflow screenshots/);

  for (const [path, contents] of [
    ["docs/screenshots/docs-screenshots.spec.ts", spec],
    ["docs/screenshots/playwright.config.ts", config],
    ["docs/screenshots/README.md", readme],
  ]) {
    assert.doesNotMatch(contents, /frontend\/node_modules/, `${path} must not assume nested frontend/node_modules`);
  }
});
