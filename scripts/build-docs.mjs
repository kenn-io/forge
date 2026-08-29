import { spawn } from "node:child_process";
import { cp, copyFile, mkdir, mkdtemp, readFile, rm, stat } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const repoRoot = path.resolve(path.dirname(scriptPath), "..");
const zensicalVersion = "0.0.51";

const publishedFiles = new Set([
  "archive.md",
  "commands.md",
  "configuration.md",
  "federated-fleet.md",
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
  "workflows.md",
]);
const publishedDirectoryEntries = new Set(["overrides", "stylesheets", "workflows"]);

function isPublishedDocsInput(sourceDir, candidate) {
  const relative = path.relative(sourceDir, candidate);
  if (relative === "") return true;
  return publishedFiles.has(relative) || publishedDirectoryEntries.has(relative);
}

export async function stageDocsSource(sourceDir, destinationDir, faviconSource) {
  await cp(sourceDir, destinationDir, {
    recursive: true,
    filter: (candidate) => isPublishedDocsInput(sourceDir, candidate),
  });
  await mkdir(path.join(destinationDir, "assets"), { recursive: true });
  await copyFile(faviconSource, path.join(destinationDir, "assets", "favicon.svg"));
}

export function publishedMarkdownPages() {
  return [...publishedFiles].filter((file) => file.endsWith(".md")).sort();
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

export async function stageSiteRoot(docsSourceDir, websiteSourceDir, faviconSource, siteRoot) {
  await cp(websiteSourceDir, siteRoot, { recursive: true });
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

export function docsSiteProjectArgs(env = process.env) {
  const project = env.KENN_FORGE_DOCS_SITE_PROJECT;
  return project && project !== "all" ? [`--project=${project}`] : [];
}

export async function buildDocs() {
  const sourceDir = path.join(repoRoot, "docs");
  const siteDir = path.join(repoRoot, "site");
  const stagingRoot = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-build-"));
  const stagedDocs = path.join(stagingRoot, "docs");

  try {
    await stageDocsSource(sourceDir, stagedDocs, path.join(repoRoot, "frontend", "public", "favicon.svg"));
    await copyFile(path.join(sourceDir, "zensical.toml"), path.join(stagingRoot, "zensical.toml"));

    await run(
      process.execPath,
      [
        path.join(repoRoot, "node_modules", "vite-plus", "bin", "vp"),
        "exec",
        "--",
        "playwright",
        "test",
        "--config",
        path.join(repoRoot, "docs", "screenshots", "playwright.config.ts"),
        "--project=chromium",
        "--output",
        path.join(stagingRoot, "test-results"),
      ],
      {
        cwd: repoRoot,
        env: {
          ...process.env,
          KENN_FORGE_DOCS_SCREENSHOT_DIR: path.join(stagedDocs, "assets", "generated"),
        },
      },
    );

    await run("uvx", ["--from", `zensical==${zensicalVersion}`, "zensical", "build"], {
      cwd: stagingRoot,
      env: process.env,
    });

    await stageSiteRoot(
      sourceDir,
      path.join(repoRoot, "website"),
      path.join(repoRoot, "frontend", "public", "favicon.svg"),
      path.join(stagingRoot, "site"),
    );
    await verifySiteRoot(path.join(stagingRoot, "site"));

    await run(
      process.execPath,
      [
        path.join(repoRoot, "node_modules", "vite-plus", "bin", "vp"),
        "exec",
        "--",
        "playwright",
        "test",
        "--config",
        path.join(repoRoot, "docs", "site", "playwright.config.ts"),
        ...docsSiteProjectArgs(),
        "--output",
        path.join(stagingRoot, "site-test-results"),
      ],
      {
        cwd: repoRoot,
        env: {
          ...process.env,
          KENN_FORGE_DOCS_SITE_DIR: path.join(stagingRoot, "site"),
        },
      },
    );

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
