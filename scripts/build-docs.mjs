import { spawn } from "node:child_process";
import { cp, copyFile, mkdir, mkdtemp, readdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const repoRoot = path.resolve(path.dirname(scriptPath), "..");
const zensicalVersion = "0.0.51";

const publishedFiles = new Set([
  "activity-relay.md",
  "archive.md",
  "commands.md",
  "configuration.md",
  "devboxes.md",
  "external-pr-context.md",
  "federated-fleet.md",
  "gh-shim.md",
  "index.md",
  "integrations.md",
  "kenn-forge-mcp.md",
  "quickstart.md",
  "settings.md",
  path.join("overrides", "main.html"),
  path.join("stylesheets", "extra.css"),
  "troubleshooting.md",
  path.join("workflows", "activity.md"),
  path.join("workflows", "code-reviewer.md"),
  path.join("workflows", "docs.md"),
  path.join("workflows", "issue-triager.md"),
  path.join("workflows", "repositories.md"),
  path.join("workflows", "workspaces.md"),
  path.join("workflows", "worklists.md"),
  "workflows.md",
]);
const publishedDirectoryEntries = new Set(["overrides", "stylesheets", "workflows"]);

function isPublishedDocsInput(sourceDir, candidate) {
  const relative = path.relative(sourceDir, candidate);
  if (relative === "") return true;
  return publishedFiles.has(relative) || publishedDirectoryEntries.has(relative);
}

export async function stageDocsSource(sourceDir, destinationDir, faviconSource, generatedAssetsSource) {
  await cp(sourceDir, destinationDir, {
    recursive: true,
    filter: (candidate) => isPublishedDocsInput(sourceDir, candidate),
  });
  await mkdir(path.join(destinationDir, "assets"), { recursive: true });
  await copyFile(faviconSource, path.join(destinationDir, "assets", "favicon.svg"));
  await cp(generatedAssetsSource, path.join(destinationDir, "assets", "generated"), {
    recursive: true,
    filter: (candidate) => candidate === generatedAssetsSource || candidate.endsWith(".svg"),
  });
}

export function publishedDocsFiles() {
  return [...publishedFiles].sort();
}

export function publishedMarkdownPages() {
  return publishedDocsFiles().filter((file) => file.endsWith(".md"));
}

function twinPathFor(siteRoot, page) {
  return path.join(siteRoot, "docs", page);
}

function renderedPathFor(siteRoot, page) {
  const withoutExtension = page.slice(0, -".md".length);
  if (page === "index.md") return path.join(siteRoot, "docs", "index.html");
  return path.join(siteRoot, "docs", withoutExtension, "index.html");
}

export function twinURLFor(page) {
  return `https://forge.kenn.io/docs/${page.split(path.sep).join("/")}`;
}

// The website build inlines these into its pages instead of serving them.
const websiteInlinedEntries = new Set(["fonts", "hold-first-paint.js"]);
const websiteFontURL = /url\("\/fonts\/([^"/]+\.woff2)"\)/g;
const charsetMeta = /<meta charset="utf-8"\s*\/?>/i;

function readHoldScript(websiteSourceDir) {
  return readFile(path.join(websiteSourceDir, "hold-first-paint.js"), "utf8");
}

// Hide the page until its stylesheets and fonts load; see kenn-io/actions
// first-paint. The browser looks for the charset in the first 1024 bytes, so
// the script goes after it.
async function inlineHoldScript(file, label, holdScript) {
  const html = await readFile(file, "utf8");
  const meta = html.match(charsetMeta);
  if (!meta) throw new Error(`${label} has no <meta charset="utf-8">`);
  await writeFile(file, html.replace(charsetMeta, () => `${meta[0]}\n    <script>\n${holdScript}</script>`));
}

// A font in its own file arrives after the stylesheet that names it, so the
// page first paints in a fallback face. Embed the fonts in the stylesheet and
// hold the first paint until they decode.
async function inlineWebsiteFirstPaintAssets(websiteSourceDir, siteRoot) {
  const holdScript = await readHoldScript(websiteSourceDir);
  const entries = await readdir(websiteSourceDir, { recursive: true, withFileTypes: true });
  for (const entry of entries.filter((candidate) => candidate.isFile())) {
    const relative = path.relative(websiteSourceDir, path.join(entry.parentPath, entry.name));
    const staged = path.join(siteRoot, relative);
    if (entry.name.endsWith(".css")) {
      const css = await readFile(staged, "utf8");
      const fonts = new Map();
      for (const [, name] of css.matchAll(websiteFontURL)) {
        const font = await readFile(path.join(websiteSourceDir, "fonts", name));
        fonts.set(name, `url("data:font/woff2;base64,${font.toString("base64")}")`);
      }
      await writeFile(staged, css.replace(websiteFontURL, (_, name) => fonts.get(name)));
    } else if (entry.name.endsWith(".html")) {
      await inlineHoldScript(staged, relative, holdScript);
    }
  }
}

// The documentation uses system fonts, so on its pages the hold script waits
// only for the stylesheets. The first-paint check requires it on every page
// that has a stylesheet. Zensical also copies the theme templates under
// overrides/ into the output; they are not pages.
export async function holdDocsFirstPaint(docsSiteDir, websiteSourceDir) {
  const holdScript = await readHoldScript(websiteSourceDir);
  const entries = await readdir(docsSiteDir, { recursive: true, withFileTypes: true });
  for (const entry of entries.filter((candidate) => candidate.isFile() && candidate.name.endsWith(".html"))) {
    const file = path.join(entry.parentPath, entry.name);
    const relative = path.relative(docsSiteDir, file);
    if (relative.split(path.sep)[0] === "overrides") continue;
    await inlineHoldScript(file, path.join("docs", relative), holdScript);
  }
}

export async function stageSiteRoot(docsSourceDir, websiteSourceDir, faviconSource, siteRoot) {
  await cp(websiteSourceDir, siteRoot, {
    recursive: true,
    filter: (candidate) => !websiteInlinedEntries.has(path.relative(websiteSourceDir, candidate)),
  });
  await inlineWebsiteFirstPaintAssets(websiteSourceDir, siteRoot);
  await copyFile(faviconSource, path.join(siteRoot, "favicon.svg"));
  for (const page of publishedMarkdownPages()) {
    const twin = twinPathFor(siteRoot, page);
    await mkdir(path.dirname(twin), { recursive: true });
    await copyFile(path.join(docsSourceDir, page), twin);
  }
  await copyFile(path.join(docsSourceDir, "llms.txt"), path.join(siteRoot, "llms.txt"));
}

async function nonEmptyFile(candidate) {
  try {
    const info = await stat(candidate);
    return info.isFile() && info.size > 0;
  } catch {
    return false;
  }
}

export async function verifySiteRoot(siteRoot) {
  const missing = [];
  for (const staticPage of ["index.html", path.join("guide", "index.html")]) {
    if (!(await nonEmptyFile(path.join(siteRoot, staticPage)))) missing.push(staticPage);
  }
  for (const page of publishedMarkdownPages()) {
    if (!(await nonEmptyFile(renderedPathFor(siteRoot, page)))) {
      missing.push(`rendered page for ${page}`);
    }
    if (!(await nonEmptyFile(twinPathFor(siteRoot, page)))) {
      missing.push(`markdown twin for ${page}`);
    }
  }
  const llmsPath = path.join(siteRoot, "llms.txt");
  if (!(await nonEmptyFile(llmsPath))) {
    missing.push("llms.txt");
  } else {
    const llms = await readFile(llmsPath, "utf8");
    for (const page of publishedMarkdownPages()) {
      if (!llms.includes(twinURLFor(page))) missing.push(`llms.txt entry for ${page}`);
    }
  }
  if (missing.length > 0) {
    throw new Error(`site output is missing required entries:\n  ${missing.join("\n  ")}`);
  }
}

function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      stdio: "inherit",
      ...options,
    });
    child.on("error", reject);
    child.on("exit", (code, signal) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(signal ? `${command} terminated by ${signal}` : `${command} exited with status ${code}`));
    });
  });
}

export async function buildDocs() {
  const sourceDir = path.join(repoRoot, "docs");
  const siteDir = path.join(repoRoot, "site");
  const stagingRoot = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-build-"));
  const stagedDocs = path.join(stagingRoot, "docs");

  try {
    await run("bash", [path.join(repoRoot, "scripts", "sync-docs-assets.sh")], {
      cwd: repoRoot,
      env: process.env,
    });
    await stageDocsSource(
      sourceDir,
      stagedDocs,
      path.join(repoRoot, "frontend", "public", "favicon.svg"),
      path.join(sourceDir, "assets", "generated"),
    );
    await copyFile(path.join(sourceDir, "zensical.toml"), path.join(stagingRoot, "zensical.toml"));

    await run("uvx", ["--from", `zensical==${zensicalVersion}`, "zensical", "build"], {
      cwd: stagingRoot,
      env: process.env,
    });
    await holdDocsFirstPaint(path.join(stagingRoot, "site", "docs"), path.join(repoRoot, "website"));

    await stageSiteRoot(
      sourceDir,
      path.join(repoRoot, "website"),
      path.join(repoRoot, "frontend", "public", "favicon.svg"),
      path.join(stagingRoot, "site"),
    );
    await verifySiteRoot(path.join(stagingRoot, "site"));

    await rm(siteDir, { recursive: true, force: true });
    await cp(path.join(stagingRoot, "site"), siteDir, { recursive: true });
  } finally {
    await rm(stagingRoot, { recursive: true, force: true });
  }

  console.log(`Documentation site built at ${siteDir}`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  buildDocs().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
