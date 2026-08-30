import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { test } from "node:test";

import { assertSafeOutputDirectory, minifyNativeSVG } from "./generate-docs-screenshots.mjs";

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

test("screenshot generation rejects broad output directories", () => {
  const allowedRoots = ["/workspace/docs/assets", "/tmp"];

  for (const directory of ["/", "/workspace", "/workspace/other", "/data", "/tmp"]) {
    assert.throws(() => assertSafeOutputDirectory(directory, allowedRoots), /refusing to replace protected directory/);
  }
  for (const directory of ["/workspace/docs/assets/generated", "/tmp/kenn-forge-docs-assets"]) {
    assert.doesNotThrow(() => assertSafeOutputDirectory(directory, allowedRoots));
  }
});

test("docs screenshot command lists the capture suite", () => {
  const list = spawnSync(process.execPath, ["scripts/generate-docs-screenshots.mjs", "--list"], {
    encoding: "utf8",
  });
  assert.equal(list.status, 0, list.stderr || list.stdout);
  assert.match(list.stdout, /docs workflow screenshots/);
});
