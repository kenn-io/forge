import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

import { minifyNativeSVG } from "./generate-docs-screenshots.mjs";

async function readJSON(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

test("published SVGs keep text while compacting path geometry", () => {
  const source = [
    '<?xml version="1.0"?>',
    '<svg xmlns="http://www.w3.org/2000/svg">',
    "  <title>Workflow screenshot</title>",
    '  <path d="M 0.123456 0 L -0.654321 7.000000 Z"/>',
    "</svg>",
    "",
  ].join("\n");

  assert.equal(
    minifyNativeSVG(source),
    '<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><title>Workflow screenshot</title><path d="M.123 0L-.654 7Z"/></svg>\n',
  );
});

test("docs screenshot generation is an independent Playwright command", async () => {
  const [rootPkg, spec, config, readme] = await Promise.all([
    readJSON("package.json"),
    readFile("docs/screenshots/docs-screenshots.spec.ts", "utf8"),
    readFile("docs/screenshots/playwright.config.ts", "utf8"),
    readFile("docs/screenshots/README.md", "utf8"),
  ]);

  // The exact version is checked against the CI container by
  // scripts/check-playwright-version.mjs; here only the exact-pin shape
  // matters, so Playwright bumps don't have to touch this test.
  assert.match(
    rootPkg.devDependencies?.["@playwright/test"] ?? "",
    /^\d+\.\d+\.\d+$/,
    "root package.json must pin an exact @playwright/test version",
  );

  const list = spawnSync(process.execPath, ["scripts/generate-docs-screenshots.mjs", "--list"], {
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
