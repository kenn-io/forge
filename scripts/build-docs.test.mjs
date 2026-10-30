import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

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
  await mkdir(path.join(websiteSource, "fonts"), { recursive: true });
  await mkdir(path.join(websiteSource, "styles"), { recursive: true });
  await writeFile(path.join(websiteSource, "index.html"), '<head><meta charset="utf-8" /></head><h1>pitch</h1>\n');
  await writeFile(
    path.join(websiteSource, "guide", "index.html"),
    '<head><meta charset="utf-8" /></head><h1>guide</h1>\n',
  );
  await writeFile(path.join(websiteSource, "hold-first-paint.js"), "hold();\n");
  await writeFile(path.join(websiteSource, "fonts", "Mono.woff2"), "font-bytes");
  await writeFile(
    path.join(websiteSource, "styles", "site.css"),
    '@font-face { src: url("/fonts/Mono.woff2") format("woff2"); }\n',
  );
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

  assert.equal(await readFile(path.join(siteRoot, "docs", "index.md"), "utf8"), "# index.md\n");
  assert.equal(
    await readFile(path.join(siteRoot, "docs", "workflows", "activity.md"), "utf8"),
    `# ${path.join("workflows", "activity.md")}\n`,
  );
  assert.ok((await readFile(path.join(siteRoot, "llms.txt"), "utf8")).includes("https://forge.kenn.io/docs/index.md"));
});

test("site root staging embeds website fonts and holds first paint on every page", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-site-root-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const { docsSource, websiteSource, favicon, siteRoot } = await writeSiteFixture(root);

  await docsBuild.stageSiteRoot(docsSource, websiteSource, favicon, siteRoot);

  assert.equal(
    await readFile(path.join(siteRoot, "styles", "site.css"), "utf8"),
    '@font-face { src: url("data:font/woff2;base64,Zm9udC1ieXRlcw==") format("woff2"); }\n',
  );
  assert.equal(
    await readFile(path.join(siteRoot, "guide", "index.html"), "utf8"),
    '<head><meta charset="utf-8" />\n    <script>\nhold();\n</script></head><h1>guide</h1>\n',
  );
  await assert.rejects(readFile(path.join(siteRoot, "fonts", "Mono.woff2")), { code: "ENOENT" });
});

test("site root staging rejects a website page without a charset to hold first paint after", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-site-root-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const { docsSource, websiteSource, favicon, siteRoot } = await writeSiteFixture(root);
  await writeFile(path.join(websiteSource, "guide", "index.html"), "<h1>guide</h1>\n");

  await assert.rejects(
    docsBuild.stageSiteRoot(docsSource, websiteSource, favicon, siteRoot),
    /guide\/index\.html has no <meta charset="utf-8">/,
  );
});

test("docs pages hold first paint after their charset", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-hold-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const docsSite = path.join(root, "docs");
  const websiteSource = path.join(root, "website");
  await mkdir(path.join(docsSite, "quickstart"), { recursive: true });
  await mkdir(websiteSource, { recursive: true });
  await writeFile(path.join(websiteSource, "hold-first-paint.js"), "hold($&);\n");
  await writeFile(path.join(docsSite, "404.html"), '<head><meta charset="utf-8"><link rel="stylesheet"></head>\n');
  await writeFile(path.join(docsSite, "quickstart", "index.html"), '<head><meta charset="utf-8"></head>\n');
  await writeFile(path.join(docsSite, "extra.css"), "body {}\n");
  await mkdir(path.join(docsSite, "overrides"), { recursive: true });
  await writeFile(path.join(docsSite, "overrides", "main.html"), '{% extends "base.html" %}\n');

  await docsBuild.holdDocsFirstPaint(docsSite, websiteSource);

  assert.equal(
    await readFile(path.join(docsSite, "404.html"), "utf8"),
    '<head><meta charset="utf-8">\n    <script>\nhold($&);\n</script><link rel="stylesheet"></head>\n',
  );
  assert.equal(
    await readFile(path.join(docsSite, "quickstart", "index.html"), "utf8"),
    '<head><meta charset="utf-8">\n    <script>\nhold($&);\n</script></head>\n',
  );
  assert.equal(await readFile(path.join(docsSite, "extra.css"), "utf8"), "body {}\n");
  assert.equal(await readFile(path.join(docsSite, "overrides", "main.html"), "utf8"), '{% extends "base.html" %}\n');
});

test("docs hold rejects a page without a charset", async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-hold-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const docsSite = path.join(root, "docs");
  const websiteSource = path.join(root, "website");
  await mkdir(docsSite, { recursive: true });
  await mkdir(websiteSource, { recursive: true });
  await writeFile(path.join(websiteSource, "hold-first-paint.js"), "hold();\n");
  await writeFile(path.join(docsSite, "index.html"), "<head></head>\n");

  await assert.rejects(docsBuild.holdDocsFirstPaint(docsSite, websiteSource), /docs\/index\.html has no <meta charset/);
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

// Vercel builds the docs from only the files .vercelignore uploads, so a
// published page missing there fails the production deploy while local
// builds, which read the whole checkout, still pass.
test("the Vercel upload includes every docs build input", () => {
  const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
  const dropped = new Set(
    execFileSync("git", ["ls-files", "--cached", "--ignored", "--exclude-from=.vercelignore"], {
      cwd: repoRoot,
      encoding: "utf8",
    }).split("\n"),
  );
  const inputs = [...docsBuild.publishedDocsFiles(), "llms.txt", "zensical.toml"].map((file) =>
    path.posix.join("docs", ...file.split(path.sep)),
  );

  assert.deepEqual(
    inputs.filter((file) => dropped.has(file)),
    [],
  );
});
