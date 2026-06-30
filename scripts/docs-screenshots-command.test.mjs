import assert from "node:assert/strict";
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
  assert.equal(rootPkg.devDependencies?.["@playwright/test"], "1.61.0");

  for (const [path, contents] of [
    ["docs/screenshots/docs-screenshots.spec.ts", spec],
    ["docs/screenshots/playwright.config.ts", config],
    ["docs/screenshots/README.md", readme],
  ]) {
    assert.doesNotMatch(contents, /frontend\/node_modules/, `${path} must not assume nested frontend/node_modules`);
  }
});
