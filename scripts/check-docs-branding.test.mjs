import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { findForbiddenDocsBranding } from "./check-docs-branding.mjs";

test("docs branding check flags bare binary names in published prose", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-branding-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));

  await mkdir(path.join(root, "docs", "workflows"), { recursive: true });
  await writeFile(
    path.join(root, "docs", "index.md"),
    [
      "# Kenn Forge",
      "",
      "Forge syncs your repositories. Run `kenn-forge daemon start` to begin.",
      "kenn-forge keeps data local.",
      "",
      "```sh",
      "kenn-forge serve",
      "```",
      "",
      "See [MCP companion](kenn-forge-mcp.md) and https://github.com/kenn-io/forge.",
      "",
    ].join("\n"),
  );
  await writeFile(path.join(root, "docs", "quickstart.md"), "Install Kenn Forge, then start Forge.\n");
  await writeFile(path.join(root, "docs", "adr-notes.md"), "kenn-forge internal notes stay unchecked.\n");

  assert.deepEqual(await findForbiddenDocsBranding(root), ["docs/index.md:4: kenn-forge keeps data local."]);
});
