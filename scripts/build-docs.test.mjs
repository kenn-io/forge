import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { stageDocsSource } from "./build-docs.mjs";

test("docs staging excludes previously generated screenshots", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-build-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));

  const source = path.join(root, "source");
  const staged = path.join(root, "staged");
  await mkdir(path.join(source, "assets", "generated"), { recursive: true });
  await writeFile(path.join(source, "index.md"), "# Docs\n");
  await writeFile(path.join(source, "assets", "generated", "stale.svg"), "<svg />\n");

  await stageDocsSource(source, staged);

  assert.equal(await readFile(path.join(staged, "index.md"), "utf8"), "# Docs\n");
  await assert.rejects(readFile(path.join(staged, "assets", "generated", "stale.svg"), "utf8"), {
    code: "ENOENT",
  });
});
