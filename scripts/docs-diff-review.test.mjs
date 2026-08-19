import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { linkSignature } from "../skills/docs-diff-review/assets/viewer-utils.mjs";
import { discoverChangedPages } from "../skills/docs-diff-review/scripts/assemble-review.mjs";

test("rewritten internal links have the same signature on both sides", () => {
  const before = linkSignature(["/before/guide/", "/before/reference/"], "/before");
  const after = linkSignature(["/after/guide/", "/after/reference/"], "/after");

  assert.equal(before, after);
});

test("page discovery notices changed bytes in a referenced image", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-review-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const before = path.join(root, "before");
  const after = path.join(root, "after");
  for (const site of [before, after]) {
    await mkdir(path.join(site, "assets"), { recursive: true });
    await writeFile(
      path.join(site, "index.html"),
      '<article><h1>Guide</h1><img src="/assets/shot.svg"></article>',
    );
  }
  await writeFile(path.join(before, "assets", "shot.svg"), "<svg><text>before</text></svg>");
  await writeFile(path.join(after, "assets", "shot.svg"), "<svg><text>after</text></svg>");

  assert.deepEqual(await discoverChangedPages(before, after), ["/"]);
});

test("page discovery uses rendered output instead of changed docs paths", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-review-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const before = path.join(root, "before");
  const after = path.join(root, "after");
  for (const site of [before, after]) {
    await mkdir(path.join(site, "guide"), { recursive: true });
  }
  await writeFile(
    path.join(before, "guide", "index.html"),
    "<article><p>Old rendered copy</p></article>",
  );
  await writeFile(
    path.join(after, "guide", "index.html"),
    "<article><p>New rendered copy</p></article>",
  );

  assert.deepEqual(await discoverChangedPages(before, after), ["/guide/"]);
});
