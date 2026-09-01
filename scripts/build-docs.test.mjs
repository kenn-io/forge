import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import * as docsBuild from "./build-docs.mjs";

const { stageDocsSource } = docsBuild;

test("docs staging includes only public site inputs", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-build-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));

  const source = path.join(root, "source");
  const staged = path.join(root, "staged");
  const generatedAssets = path.join(root, "generated-assets");
  const favicon = path.join(root, "frontend", "public", "favicon.svg");
  await mkdir(path.join(source, "stylesheets"), { recursive: true });
  await mkdir(path.join(source, "workflows"), { recursive: true });
  await mkdir(path.join(source, "overrides"), { recursive: true });
  await mkdir(path.join(source, "assets", "generated"), { recursive: true });
  await mkdir(path.join(source, "internal"), { recursive: true });
  await mkdir(path.join(source, "adr"), { recursive: true });
  await mkdir(path.join(source, "reports"), { recursive: true });
  await mkdir(path.join(source, "screenshots"), { recursive: true });
  await mkdir(generatedAssets, { recursive: true });
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
  await writeFile(path.join(generatedAssets, "workflow.svg"), "<svg><title>workflow</title></svg>\n");
  await writeFile(path.join(generatedAssets, ".docs-assets.synced"), "not public\n");
  await writeFile(favicon, "<svg><title>canonical favicon</title></svg>\n");

  await stageDocsSource(source, staged, favicon, generatedAssets);

  assert.equal(await readFile(path.join(staged, "index.md"), "utf8"), "# Docs\n");
  assert.equal(await readFile(path.join(staged, "kenn-forge-mcp.md"), "utf8"), "# MCP\n");
  assert.equal(await readFile(path.join(staged, "workflows", "code-reviewer.md"), "utf8"), "# Reviewer\n");
  assert.equal(await readFile(path.join(staged, "stylesheets", "extra.css"), "utf8"), ":root {}\n");
  assert.equal(await readFile(path.join(staged, "overrides", "main.html"), "utf8"), "<script>palette</script>\n");
  assert.equal(
    await readFile(path.join(staged, "assets", "favicon.svg"), "utf8"),
    "<svg><title>canonical favicon</title></svg>\n",
  );
  assert.equal(
    await readFile(path.join(staged, "assets", "generated", "workflow.svg"), "utf8"),
    "<svg><title>workflow</title></svg>\n",
  );
  await assert.rejects(readFile(path.join(staged, "assets", "generated", ".docs-assets.synced"), "utf8"), {
    code: "ENOENT",
  });

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

async function writeSiteFixture(root) {
  const docsSource = path.join(root, "docs-source");
  const websiteSource = path.join(root, "website-source");
  const favicon = path.join(root, "favicon.svg");
  const siteRoot = path.join(root, "site");

  await mkdir(path.join(docsSource, "workflows"), { recursive: true });
  for (const page of docsBuild.publishedMarkdownPages()) {
    await mkdir(path.dirname(path.join(docsSource, page)), { recursive: true });
    await writeFile(path.join(docsSource, page), `# ${page}\n`);
  }
  const llmsEntries = docsBuild
    .publishedMarkdownPages()
    .map((page) => `- [${page}](${docsBuild.twinURLFor(page)}): entry`)
    .join("\n");
  await writeFile(path.join(docsSource, "llms.txt"), `# Kenn Forge\n\n${llmsEntries}\n`);

  await mkdir(path.join(websiteSource, "guide"), { recursive: true });
  await writeFile(path.join(websiteSource, "index.html"), "<h1>pitch</h1>\n");
  await writeFile(path.join(websiteSource, "guide", "index.html"), "<h1>guide</h1>\n");
  await writeFile(favicon, "<svg />\n");

  for (const page of docsBuild.publishedMarkdownPages()) {
    const withoutExtension = page.slice(0, -".md".length);
    const rendered =
      page === "index.md"
        ? path.join(siteRoot, "docs", "index.html")
        : path.join(siteRoot, "docs", withoutExtension, "index.html");
    await mkdir(path.dirname(rendered), { recursive: true });
    await writeFile(rendered, "<html>rendered</html>\n");
  }

  return { docsSource, websiteSource, favicon, siteRoot };
}

test("site root staging pairs every published page with a markdown twin", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-site-root-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const { docsSource, websiteSource, favicon, siteRoot } = await writeSiteFixture(root);

  await docsBuild.stageSiteRoot(docsSource, websiteSource, favicon, siteRoot);
  await docsBuild.verifySiteRoot(siteRoot);

  assert.equal(await readFile(path.join(siteRoot, "index.html"), "utf8"), "<h1>pitch</h1>\n");
  assert.equal(await readFile(path.join(siteRoot, "docs", "index.md"), "utf8"), "# index.md\n");
  assert.equal(
    await readFile(path.join(siteRoot, "docs", "workflows", "activity.md"), "utf8"),
    `# ${path.join("workflows", "activity.md")}\n`,
  );
  assert.ok((await readFile(path.join(siteRoot, "llms.txt"), "utf8")).includes("https://forge.kenn.io/docs/index.md"));
});

test("site verification fails when a markdown twin or llms.txt entry is missing", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-site-root-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const { docsSource, websiteSource, favicon, siteRoot } = await writeSiteFixture(root);

  await docsBuild.stageSiteRoot(docsSource, websiteSource, favicon, siteRoot);
  await rm(path.join(siteRoot, "docs", "quickstart.md"));
  await assert.rejects(docsBuild.verifySiteRoot(siteRoot), /markdown twin for quickstart\.md/);

  await docsBuild.stageSiteRoot(docsSource, websiteSource, favicon, siteRoot);
  await writeFile(path.join(siteRoot, "llms.txt"), "# Kenn Forge\n");
  await assert.rejects(docsBuild.verifySiteRoot(siteRoot), /llms\.txt entry for/);
});
