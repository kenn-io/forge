#!/usr/bin/env node

import { readdir, readFile, stat } from "node:fs/promises";
import { relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

// The built SPA is served under a configurable base path (config base_path,
// e.g. /middleman/) by rewriting index.html's <script src>/<link href> at
// request time. That rewrite only reaches HTML, not URLs baked inside JS
// bundles. An asset URL written as `new URL("/assets/x.js", import.meta.url)`
// uses an absolute root path, so the browser resolves it against the origin
// and drops the base path prefix -- the request 404s under any non-root
// base_path. Worker URLs are the common offender (see pierre-worker-pool.ts).
//
// Base-path-safe asset URLs are relative (`new URL("./x.js", import.meta.url)`)
// so they resolve against the entry chunk's already-prefixed location. This
// guard fails the build if any absolute import.meta.url asset URL survives.

const DEFAULT_SCAN_PATHS = ["frontend/dist/assets"];

// new URL("<absolute path>", <...>import.meta.url): the first argument is a
// root-absolute path (starts with a single "/", excluding "//" protocol-
// relative URLs), and the base is import.meta.url. Bundlers minify the base to
// forms like ``+import.meta.url, so allow a short gap before import.meta.url.
const ABSOLUTE_IMPORT_META_URL = /new URL\(\s*(["'`])(\/[^/][^"'`]*)\1\s*,\s*[^,)]{0,40}import\.meta\.url/g;

function toPosix(path) {
  return path.split(sep).join("/");
}

async function collectJsFiles(path) {
  const info = await stat(path).catch(() => null);
  if (!info) return [];
  if (info.isFile()) return path.endsWith(".js") ? [path] : [];
  if (!info.isDirectory()) return [];

  const entries = await readdir(path, { withFileTypes: true });
  const nested = await Promise.all(entries.map((entry) => collectJsFiles(resolve(path, entry.name))));
  return nested.flat();
}

export async function findAbsoluteAssetUrls({ root = process.cwd(), paths = DEFAULT_SCAN_PATHS } = {}) {
  const rootPath = resolve(root);
  const scanPaths = paths.map((path) => resolve(rootPath, path));
  const files = (await Promise.all(scanPaths.map(collectJsFiles))).flat();
  const findings = [];

  for (const file of files.sort()) {
    const content = await readFile(file, "utf8");
    for (const match of content.matchAll(ABSOLUTE_IMPORT_META_URL)) {
      findings.push({
        file: toPosix(relative(rootPath, file)),
        url: match[2],
      });
    }
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
      if (!value) throw new Error("--root requires a path");
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
  console.log(`Usage: node scripts/check-asset-base-paths.mjs [--root DIR] [PATH...]

Fail the build when a bundled asset URL uses an absolute root path with
import.meta.url (e.g. new URL("/assets/x.js", import.meta.url)). Such URLs
ignore the configured base_path and 404 behind a subpath reverse proxy.
Defaults to scanning ${DEFAULT_SCAN_PATHS.join(", ")}.
`);
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (options.help) {
    printHelp();
    return;
  }

  const findings = await findAbsoluteAssetUrls(options);
  if (findings.length === 0) return;

  console.error(
    "Absolute import.meta.url asset URLs ignore base_path and break " +
      "subpath deployments. Make them relative (vite renderBuiltUrl):",
  );
  for (const finding of findings) {
    console.error(`  ${finding.file}: new URL("${finding.url}", import.meta.url)`);
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
