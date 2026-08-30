import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import * as screenshotTools from "./generate-docs-screenshots.mjs";

const { assertSafeOutputDirectory, minifyNativeSVG } = screenshotTools;

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

test("failed screenshot publication restores the previous generation", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-publication-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const output = path.join(root, "generated");
  const stagingRoot = path.join(root, ".staging");
  await mkdir(output);
  await mkdir(stagingRoot);
  await writeFile(path.join(output, "current.svg"), "current generation\n");

  await assert.rejects(
    () => screenshotTools.publishGeneration(path.join(stagingRoot, "missing"), output, stagingRoot),
    { code: "ENOENT" },
  );
  assert.equal(await readFile(path.join(output, "current.svg"), "utf8"), "current generation\n");
});

test("docs screenshot command lists the capture suite", () => {
  const list = spawnSync(process.execPath, ["scripts/generate-docs-screenshots.mjs", "--list"], {
    encoding: "utf8",
  });
  assert.equal(list.status, 0, list.stderr || list.stdout);
  assert.match(list.stdout, /docs workflow screenshots/);
});
