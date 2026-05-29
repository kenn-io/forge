import assert from "node:assert/strict";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { test } from "node:test";

import { findAbsoluteAssetUrls } from "./check-asset-base-paths.mjs";

async function write(root, file, content) {
  const fullPath = join(root, file);
  await mkdir(dirname(fullPath), { recursive: true });
  await writeFile(fullPath, content);
}

async function makeRoot() {
  return mkdtemp("/tmp/middleman-asset-base-");
}

test("flags the minified worker URL that breaks subpath deploys", async () => {
  const root = await makeRoot();
  // Exact shape Vite emits for new Worker(new URL(...)) with base "/".
  await write(
    root,
    "assets/index-abc.js",
    "x();new Worker(new URL(`/assets/worker-DXy4-71H.js`,``+import.meta.url),{type:`module`});y();",
  );

  const findings = await findAbsoluteAssetUrls({ root, paths: ["assets"] });

  assert.equal(findings.length, 1);
  assert.equal(findings[0].url, "/assets/worker-DXy4-71H.js");
  assert.match(findings[0].file, /index-abc\.js$/);
});

test("flags double-quoted absolute import.meta.url asset URLs", async () => {
  const root = await makeRoot();
  await write(
    root,
    "assets/chunk.js",
    'const u = new URL("/assets/img-9.png", import.meta.url);',
  );

  const findings = await findAbsoluteAssetUrls({ root, paths: ["assets"] });

  assert.deepEqual(
    findings.map((f) => f.url),
    ["/assets/img-9.png"],
  );
});

test("accepts the relative worker URL produced by the renderBuiltUrl fix", async () => {
  const root = await makeRoot();
  await write(
    root,
    "assets/index-fixed.js",
    "new Worker(new URL(``+new URL(`worker-DXy4-71H.js`,import.meta.url),import.meta.url),{type:`module`});",
  );

  const findings = await findAbsoluteAssetUrls({ root, paths: ["assets"] });

  assert.deepEqual(findings, []);
});

test("ignores absolute paths resolved against a non import.meta.url base", async () => {
  const root = await makeRoot();
  // Legit runtime URL building (e.g. API calls) must not be flagged: only
  // import.meta.url-relative asset references drop the base path.
  await write(
    root,
    "assets/api.js",
    'const u = new URL("/api/v1/pulls", window.location.origin);',
  );

  const findings = await findAbsoluteAssetUrls({ root, paths: ["assets"] });

  assert.deepEqual(findings, []);
});

test("ignores absolute external URLs and protocol-relative URLs", async () => {
  const root = await makeRoot();
  await write(
    root,
    "assets/ext.js",
    [
      'new URL("https://cdn.example.com/x.js", import.meta.url);',
      'new URL("//cdn.example.com/y.js", import.meta.url);',
    ].join("\n"),
  );

  const findings = await findAbsoluteAssetUrls({ root, paths: ["assets"] });

  assert.deepEqual(findings, []);
});
