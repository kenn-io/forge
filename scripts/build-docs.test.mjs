import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { stageDocsSource } from "./build-docs.mjs";

test("docs staging includes only public site inputs", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-build-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));

  const source = path.join(root, "source");
  const staged = path.join(root, "staged");
  const favicon = path.join(root, "frontend", "public", "favicon.svg");
  await mkdir(path.join(source, "stylesheets"), { recursive: true });
  await mkdir(path.join(source, "workflows"), { recursive: true });
  await mkdir(path.join(source, "overrides"), { recursive: true });
  await mkdir(path.join(source, "assets", "generated"), { recursive: true });
  await mkdir(path.join(source, "internal"), { recursive: true });
  await mkdir(path.join(source, "adr"), { recursive: true });
  await mkdir(path.join(source, "reports"), { recursive: true });
  await mkdir(path.join(source, "screenshots"), { recursive: true });
  await mkdir(path.dirname(favicon), { recursive: true });
  await writeFile(path.join(source, "index.md"), "# Docs\n");
  await writeFile(path.join(source, "kenn-forge-mcp.md"), "# MCP\n");
  await writeFile(path.join(source, "workflows", "code-reviewer.md"), "# Reviewer\n");
  await writeFile(path.join(source, "stylesheets", "extra.css"), ":root {}\n");
  await writeFile(path.join(source, "overrides", "main.html"), "<script>palette</script>\n");
  await writeFile(path.join(source, "overrides", "internal.html"), "<script>private</script>\n");
  await writeFile(path.join(source, "assets", "generated", "stale.svg"), "<svg />\n");
  await writeFile(path.join(source, "internal", "private.md"), "# Private\n");
  await writeFile(path.join(source, "adr", "0001-private.md"), "# Private\n");
  await writeFile(path.join(source, "reports", "private.md"), "# Private\n");
  await writeFile(path.join(source, "screenshots", "README.md"), "# Build only\n");
  await writeFile(path.join(source, "workflows", "internal.md"), "# Private\n");
  await writeFile(path.join(source, "stylesheets", "internal.css"), ":root {}\n");
  await writeFile(favicon, "<svg><title>canonical favicon</title></svg>\n");

  await stageDocsSource(source, staged, favicon);

  assert.equal(await readFile(path.join(staged, "index.md"), "utf8"), "# Docs\n");
  assert.equal(await readFile(path.join(staged, "kenn-forge-mcp.md"), "utf8"), "# MCP\n");
  assert.equal(await readFile(path.join(staged, "workflows", "code-reviewer.md"), "utf8"), "# Reviewer\n");
  assert.equal(await readFile(path.join(staged, "stylesheets", "extra.css"), "utf8"), ":root {}\n");
  assert.equal(await readFile(path.join(staged, "overrides", "main.html"), "utf8"), "<script>palette</script>\n");
  assert.equal(
    await readFile(path.join(staged, "assets", "favicon.svg"), "utf8"),
    "<svg><title>canonical favicon</title></svg>\n",
  );

  for (const internalPath of [
    "assets/generated/stale.svg",
    "internal/private.md",
    "adr/0001-private.md",
    "reports/private.md",
    "screenshots/README.md",
    "workflows/internal.md",
    "stylesheets/internal.css",
    "overrides/internal.html",
  ]) {
    await assert.rejects(readFile(path.join(staged, internalPath), "utf8"), { code: "ENOENT" });
  }
});
