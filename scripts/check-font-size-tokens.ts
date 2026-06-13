#!/usr/bin/env node

import { readdir, readFile, stat } from "node:fs/promises";
import { extname, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

type Finding = {
  file: string;
  line: number;
  column: number;
  message: string;
};

type CheckOptions = {
  root?: string;
  paths?: string[];
};

type ParsedArgs = {
  help: boolean;
  root: string;
  paths: string[];
};

const DEFAULT_SCAN_PATHS = ["frontend/src", "packages/ui/src"];
const SOURCE_EXTENSIONS = new Set([".css", ".svelte", ".ts"]);
const ALLOWED_RELATIVE_FONT_SIZES = new Set(["0.9em"]);
const FONT_SIZE_RE = /font-size\s*:\s*([^;]+)/gi;
const FONT_SHORTHAND_RE = /(^|[;{\s])font\s*:\s*([^;]+)/gi;

function toPosix(path: string): string {
  return path.split(sep).join("/");
}

function isExcludedPath(posixPath: string): boolean {
  const segments = posixPath.split("/");
  const basename = segments.at(-1) ?? "";

  if (!SOURCE_EXTENSIONS.has(extname(posixPath))) return true;
  if (segments.includes("generated")) return true;
  if (segments.includes("__tests__")) return true;
  if (segments.includes("test")) return true;
  if (segments.includes("tests")) return true;
  if (segments.includes("e2e")) return true;
  if (segments.includes("e2e-full")) return true;
  if (basename.includes(".test.")) return true;
  if (basename.includes(".spec.")) return true;

  return false;
}

function isCommentOnly(line: string): boolean {
  const trimmed = line.trim();
  return trimmed.startsWith("//") || trimmed.startsWith("*") || trimmed.startsWith("/*");
}

function isFontSizeTokenDefinition(line: string): boolean {
  return /^\s*--font-size-[\w-]+\s*:/.test(line);
}

function isAllowedFontValue(value: string): boolean {
  const trimmed = value.trim().toLowerCase();
  if (trimmed.includes("calc(")) return false;
  if (["inherit", "initial", "unset", "revert"].includes(trimmed)) return true;
  if (ALLOWED_RELATIVE_FONT_SIZES.has(trimmed)) return true;
  if (trimmed.includes("var(--font-size-")) return true;
  return false;
}

async function collectFiles(path: string): Promise<string[]> {
  const info = await stat(path).catch(() => null);
  if (!info) return [];
  if (info.isFile()) return [path];
  if (!info.isDirectory()) return [];

  const entries = await readdir(path, { withFileTypes: true });
  const files = await Promise.all(
    entries.map((entry) => {
      if (["node_modules", ".svelte-kit", "dist", "coverage"].includes(entry.name)) {
        return [];
      }
      return collectFiles(resolve(path, entry.name));
    }),
  );

  return files.flat();
}

function collectFindingsInLine(line: string, index: number, relPath: string): Finding[] {
  if (isCommentOnly(line) || isFontSizeTokenDefinition(line)) return [];

  const findings: Finding[] = [];

  for (const match of line.matchAll(FONT_SIZE_RE)) {
    const value = match[1] ?? "";
    if (isAllowedFontValue(value)) continue;
    findings.push({
      file: relPath,
      line: index + 1,
      column: (match.index ?? 0) + 1,
      message:
        "Disallowed font-size value found. Use a --font-size-* token or approved relative font-size instead; calc() is not allowed.",
    });
  }

  for (const match of line.matchAll(FONT_SHORTHAND_RE)) {
    const value = match[2] ?? "";
    if (isAllowedFontValue(value)) continue;
    findings.push({
      file: relPath,
      line: index + 1,
      column: (match.index ?? 0) + (match[1]?.length ?? 0) + 1,
      message:
        "Disallowed font shorthand value found. Use a --font-size-* token or approved relative font-size instead; calc() is not allowed.",
    });
  }

  return findings;
}

export async function checkFontSizeTokens({
  root = process.cwd(),
  paths = DEFAULT_SCAN_PATHS,
}: CheckOptions = {}): Promise<Finding[]> {
  const rootPath = resolve(root);
  const scanPaths = paths.map((path) => resolve(rootPath, path));
  const files = (await Promise.all(scanPaths.map(collectFiles))).flat();
  const findings: Finding[] = [];

  for (const file of files.sort()) {
    const relPath = toPosix(relative(rootPath, file));
    if (isExcludedPath(relPath)) continue;

    const content = await readFile(file, "utf8");
    const lines = content.split(/\r?\n/);
    lines.forEach((line, index) => {
      findings.push(...collectFindingsInLine(line, index, relPath));
    });
  }

  return findings;
}

function parseArgs(argv: string[]): ParsedArgs {
  const paths: string[] = [];
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
    if (arg === "--help" || arg === "-h") return { help: true, root, paths };
    paths.push(arg);
  }

  return { help: false, root, paths: paths.length > 0 ? paths : DEFAULT_SCAN_PATHS };
}

function printHelp(): void {
  console.log(`Usage: node scripts/check-font-size-tokens.ts [--root DIR] [PATH...]

Detect raw frontend font-size lengths. Production styles should use
--font-size-* design tokens, with 0.9em allowed for relative small text.
`);
}

async function main(): Promise<void> {
  const options = parseArgs(process.argv.slice(2));
  if (options.help) {
    printHelp();
    return;
  }

  const findings = await checkFontSizeTokens(options);
  if (findings.length === 0) return;

  for (const finding of findings) {
    console.error(`${finding.file}:${finding.line}:${finding.column}: ${finding.message}`);
  }
  process.exitCode = 1;
}

const currentFile = fileURLToPath(import.meta.url);
if (resolve(process.argv[1] ?? "") === currentFile) {
  main().catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
