import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { findForbiddenDocsBranding } from "./check-docs-branding.mjs";

test("documentation branding check reports forbidden capitalization", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-branding-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));

  await mkdir(path.join(root, "docs"), { recursive: true });
  await mkdir(path.join(root, "docs", "superpowers", "specs"), { recursive: true });
  await mkdir(path.join(root, "context"), { recursive: true });
  await writeFile(path.join(root, "README.md"), "# kenn-forge\n");
  await writeFile(path.join(root, "docs", "bad.md"), "Welcome to Kenn Forge.\n");
  await writeFile(
    path.join(root, "docs", "superpowers", "specs", "historical.md"),
    "The original design called the product Kenn Forge.\n",
  );
  await writeFile(path.join(root, "context", "good.md"), "Use kenn-forge.\n");

  assert.deepEqual(await findForbiddenDocsBranding(root), [
    "docs/bad.md:1: Welcome to Kenn Forge.",
    "docs/superpowers/specs/historical.md:1: The original design called the product Kenn Forge.",
  ]);
});
