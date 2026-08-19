import { spawn } from "node:child_process";
import { cp, copyFile, mkdir, mkdtemp, rm } from "node:fs/promises";
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
