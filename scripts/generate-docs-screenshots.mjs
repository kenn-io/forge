import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, readdir, rename, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const repoRoot = path.resolve(path.dirname(scriptPath), "..");
const defaultOutput = path.join(repoRoot, "docs", "assets", "generated");

export function minifyNativeSVG(svg) {
  return `${svg
    .replace(/>\s+</g, "><")
    .replace(/ d="([^"]*)"/g, (_attribute, geometry) => {
      const compact = geometry
        .replace(/-?(?:\d+\.\d+|\.\d+)/g, (number) =>
          String(Math.round(Number(number) * 1000) / 1000).replace(/^(-?)0\./, "$1."),
        )
        .replace(/\s*([MLHVCSQTAZ])\s*/gi, "$1")
        .replace(/\s+(-)/g, "$1");
      return ` d="${compact}"`;
    })
    .trim()}\n`;
}

function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { stdio: "inherit", ...options });
    child.on("error", reject);
    child.on("exit", (code, signal) => {
      if (code === 0) return resolve();
      reject(new Error(signal ? `${command} terminated by ${signal}` : `${command} exited with status ${code}`));
    });
  });
}

async function expectedAssets() {
  return (await readFile(path.join(repoRoot, "scripts", "docs-assets.txt"), "utf8"))
    .split("\n")
    .filter(Boolean)
    .sort();
}

async function verifyGeneration(directory) {
  const expected = await expectedAssets();
  const actual = (await readdir(directory)).filter((entry) => entry.endsWith(".svg")).sort();
  if (actual.join("\n") !== expected.join("\n")) {
    throw new Error(`screenshot generation did not produce the required asset set\nexpected:\n${expected.join("\n")}\nactual:\n${actual.join("\n")}`);
  }
}

async function minifyGeneration(directory) {
  for (const asset of await expectedAssets()) {
    const assetPath = path.join(directory, asset);
    await writeFile(assetPath, minifyNativeSVG(await readFile(assetPath, "utf8")));
  }
}

export async function generateDocsScreenshots(args = process.argv.slice(2)) {
  const listOnly = args.includes("--list");
  const outputArg = args.find((arg) => !arg.startsWith("--"));
  const output = path.resolve(outputArg ?? process.env.DOCS_ASSETS_OUTPUT ?? defaultOutput);
  const stagingRoot = listOnly ? "" : await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-assets-"));
  const stagedOutput = listOnly ? "" : path.join(stagingRoot, "generated");

  try {
    if (!listOnly) await mkdir(stagedOutput, { recursive: true });
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
        ...(listOnly ? ["--list"] : ["--output", path.join(stagingRoot, "test-results")]),
      ],
      {
        cwd: repoRoot,
        env: listOnly
          ? process.env
          : { ...process.env, KENN_FORGE_DOCS_SCREENSHOT_DIR: stagedOutput },
      },
    );
    if (listOnly) return;

    await minifyGeneration(stagedOutput);
    await verifyGeneration(stagedOutput);
    await rm(output, { recursive: true, force: true });
    await mkdir(path.dirname(output), { recursive: true });
    await rename(stagedOutput, output);
    console.log(`Documentation screenshots generated at ${output}`);
  } finally {
    if (stagingRoot) await rm(stagingRoot, { recursive: true, force: true });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  generateDocsScreenshots().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
