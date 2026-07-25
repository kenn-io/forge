import { spawn } from "node:child_process";
import { cp, copyFile, mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const repoRoot = path.resolve(path.dirname(scriptPath), "..");

function isGeneratedAsset(sourceDir, candidate) {
  const relative = path.relative(sourceDir, candidate);
  const generatedDir = path.join("assets", "generated");
  return relative === generatedDir || relative.startsWith(`${generatedDir}${path.sep}`);
}

export async function stageDocsSource(sourceDir, destinationDir) {
  await cp(sourceDir, destinationDir, {
    recursive: true,
    filter: (candidate) => !isGeneratedAsset(sourceDir, candidate) && path.basename(candidate) !== "zensical.toml",
  });
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
  const stagingRoot = await mkdtemp(path.join(os.tmpdir(), "middleman-docs-build-"));
  const stagedDocs = path.join(stagingRoot, "docs");

  try {
    await stageDocsSource(sourceDir, stagedDocs);
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
          MIDDLEMAN_DOCS_SCREENSHOT_DIR: path.join(stagedDocs, "assets", "generated"),
        },
      },
    );

    await run("uvx", ["zensical", "build"], { cwd: stagingRoot, env: process.env });

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
