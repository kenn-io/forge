#!/usr/bin/env node

import { readdir, readFile, stat } from "node:fs/promises";
import { relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const API_MARKER = "/api/v1";
const SOURCE_EXTENSIONS = new Set([".js", ".jsx", ".svelte", ".ts", ".tsx"]);

const DEFAULT_SCAN_PATHS = ["frontend/src", "packages/ui/src"];

const GENERATED_CLIENT_RUNTIME_FILES = new Set(["frontend/src/lib/api/runtime.ts"]);

function toPosix(path) {
  return path.split(sep).join("/");
}

function hasSourceExtension(path) {
  return [...SOURCE_EXTENSIONS].some((ext) => path.endsWith(ext));
}

function isExcludedPath(posixPath) {
  const segments = posixPath.split("/");
  const basename = segments.at(-1) ?? "";

  if (!hasSourceExtension(posixPath)) return true;
  if (GENERATED_CLIENT_RUNTIME_FILES.has(posixPath)) return true;
  if (segments.includes("generated")) return true;
  if (segments.includes("__tests__")) return true;
  if (segments.includes("test")) return true;
  if (segments.includes("tests")) return true;
  if (segments.includes("e2e")) return true;
  if (segments.includes("e2e-full")) return true;
  if (basename.includes(".test.")) return true;
  if (basename.includes(".spec.")) return true;
  // Browser-tier test specs (src/**/*.browser.svelte.ts) mock the API at the
  // fetch boundary, so they reference /api/v1 paths the same way the .test.ts
  // specs they replaced did; they are tests, not production frontend code.
  if (basename.includes(".browser.svelte.")) return true;

  return false;
}

function isCommentOnly(line) {
  const trimmed = line.trim();
  return trimmed.startsWith("//") || trimmed.startsWith("*") || trimmed.startsWith("/*");
}

function contextFor(lines, index, radius = 5) {
  const start = Math.max(0, index - radius);
  const end = Math.min(lines.length, index + radius + 1);
  return lines.slice(start, end).join("\n");
}

function isAllowedStreamingTransport(line, context) {
  if (line.includes("/api/v1/events")) {
    return true;
  }

  if (
    (context.includes("WebSocket") || context.includes("buildWsUrl")) &&
    context.includes("/terminal") &&
    line.includes("/api/v1/workspaces/")
  ) {
    return true;
  }

  if (context.includes("fetch") && /ndjson/i.test(context) && line.includes("/api/v1/") && /\/logs?\b/.test(context)) {
    return true;
  }

  return false;
}

function isApiBasePathOnly(line, column) {
  const next = line[column + API_MARKER.length];
  return next === undefined || next === "`" || next === "'" || next === '"';
}

async function collectFiles(path) {
  const info = await stat(path).catch(() => null);
  if (!info) return [];

  if (info.isFile()) {
    return [path];
  }

  if (!info.isDirectory()) {
    return [];
  }

  const entries = await readdir(path, { withFileTypes: true });
  const files = await Promise.all(
    entries.map((entry) => {
      if (
        entry.name === "node_modules" ||
        entry.name === ".svelte-kit" ||
        entry.name === "dist" ||
        entry.name === "coverage"
      ) {
        return [];
      }
      return collectFiles(resolve(path, entry.name));
    }),
  );

  return files.flat();
}

export async function lintApiUrls({ root = process.cwd(), paths = DEFAULT_SCAN_PATHS } = {}) {
  const rootPath = resolve(root);
  const scanPaths = paths.map((path) => resolve(rootPath, path));
  const files = (await Promise.all(scanPaths.map(collectFiles))).flat();
  const findings = [];

  for (const file of files.sort()) {
    const relPath = toPosix(relative(rootPath, file));
    if (isExcludedPath(relPath)) continue;

    const content = await readFile(file, "utf8");
    const lines = content.split(/\r?\n/);

    lines.forEach((line, index) => {
      const column = line.indexOf(API_MARKER);
      if (column === -1 || isCommentOnly(line)) return;
      if (isApiBasePathOnly(line, column)) return;

      const context = contextFor(lines, index);
      if (isAllowedStreamingTransport(line, context)) return;

      findings.push({
        file: relPath,
        line: index + 1,
        column: column + 1,
        message:
          "Hardcoded /api/v1 endpoint in production frontend code. Use the generated client instead; only scoped streaming transports are allowed.",
      });
    });
  }

  return findings;
}

function parseArgs(argv) {
  const paths = [];
  let root = process.cwd();

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--root") {
      const value = argv[i + 1];
      if (!value) {
        throw new Error("--root requires a path");
      }
      root = value;
      i += 1;
      continue;
    }
    if (arg === "--help" || arg === "-h") {
      return { help: true, root, paths };
    }
    paths.push(arg);
  }

  return {
    help: false,
    root,
    paths: paths.length > 0 ? paths : DEFAULT_SCAN_PATHS,
  };
}

function printHelp() {
  console.log(`Usage: node scripts/lint-api-urls.mjs [--root DIR] [PATH...]

Detect hardcoded Middleman /api/v1 URLs in production frontend TypeScript
and Svelte code. Test files, generated code, and scoped streaming
transports are ignored.
`);
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (options.help) {
    printHelp();
    return;
  }

  const findings = await lintApiUrls(options);
  if (findings.length === 0) {
    return;
  }

  for (const finding of findings) {
    console.error(`${finding.file}:${finding.line}:${finding.column}: ${finding.message}`);
  }
  process.exitCode = 1;
}

const currentFile = fileURLToPath(import.meta.url);
if (resolve(process.argv[1] ?? "") === currentFile) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
