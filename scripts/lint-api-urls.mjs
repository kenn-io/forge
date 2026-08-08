#!/usr/bin/env node

import { readdir, readFile, stat } from "node:fs/promises";
import { relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const API_MARKER = "/api/v1";
const SOURCE_EXTENSIONS = new Set([".js", ".jsx", ".svelte", ".ts", ".tsx"]);

const DEFAULT_SCAN_PATHS = ["frontend/src"];

const API_URL_MESSAGE =
  "Manual Kenn Forge API URL in production frontend code. Use the generated client through the frontend runtime (or injected typed UI client) for REST requests; use the configured API base helper for browser resource URLs. Only explicit scoped transport helpers are exempt.";

const GENERATED_CLIENT_RUNTIME_FILES = new Set([
  "frontend/src/lib/api/runtime.ts",
  "frontend/src/lib/api/runtime-base.ts",
]);

// This helper builds a Kenn Forge proxy URL whose nested /api/v1 belongs to
// Kata, not Kenn Forge. Keep the exception tied to the proxy boundary itself;
// callers and neighboring files remain subject to the normal API-client rule.
const SCOPED_UPSTREAM_PROXY_HELPERS = new Map([
  ["frontend/src/lib/api/kata/daemons.ts", new Set(["kataProxyPath"])],
]);
const GENERATED_CLIENT_FACTORIES = new Set(["createAPIClient", "createRuntimeClient"]);

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
  if (line.includes("/api/v1/events") || line.includes("/api/v1/kata/tasks/events")) {
    return true;
  }

  if (context.includes("EventSource") && context.includes("/api/v1/events")) {
    return true;
  }

  if (
    (context.includes("WebSocket") || context.includes("buildWsUrl")) &&
    context.includes("/terminal") &&
    context.includes("/api/v1/workspaces/")
  ) {
    return true;
  }

  if (context.includes("fetch") && /ndjson/i.test(context) && line.includes("/api/v1/") && /\/logs?\b/.test(context)) {
    return true;
  }

  return false;
}

function lineNumberAt(content, offset) {
  return content.slice(0, offset).split(/\r?\n/).length;
}

function lineStartOffsets(content) {
  const offsets = [0];
  const newline = /\r?\n/g;
  let match;
  while ((match = newline.exec(content)) !== null) {
    offsets.push(match.index + match[0].length);
  }
  return offsets;
}

function scopedUpstreamProxyRanges(content, path) {
  const helperNames = SCOPED_UPSTREAM_PROXY_HELPERS.get(path);
  if (!helperNames) return [];

  const sourceFile = ts.createSourceFile(path, content, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const ranges = [];

  function visit(node) {
    if (ts.isFunctionDeclaration(node) && node.name && helperNames.has(node.name.text)) {
      ranges.push([node.getStart(sourceFile), node.end]);
      return;
    }
    ts.forEachChild(node, visit);
  }

  visit(sourceFile);
  return ranges;
}

function isInScopedUpstreamProxy(offset, ranges) {
  return ranges.some(([start, end]) => offset >= start && offset < end);
}

function svelteScriptRegions(content, path) {
  if (!path.endsWith(".svelte")) return [{ content, offset: 0 }];

  const regions = [];
  const lower = content.toLowerCase();
  let cursor = 0;
  while (cursor < content.length) {
    const open = lower.indexOf("<script", cursor);
    if (open === -1) break;
    const boundary = lower[open + "<script".length];
    if (boundary !== ">" && boundary?.trim() !== "") {
      cursor = open + "<script".length;
      continue;
    }
    const openEnd = lower.indexOf(">", open + "<script".length);
    if (openEnd === -1) break;

    let close = lower.indexOf("</script", openEnd + 1);
    let closeEnd = -1;
    while (close !== -1) {
      closeEnd = lower.indexOf(">", close + "</script".length);
      if (closeEnd === -1 || lower.slice(close + "</script".length, closeEnd).trim() === "") break;
      close = lower.indexOf("</script", close + "</script".length);
    }
    if (close === -1 || closeEnd === -1) break;

    const offset = openEnd + 1;
    regions.push({ content: content.slice(offset, close), offset });
    cursor = closeEnd + 1;
  }
  return regions;
}

function unwrapExpression(node) {
  while (
    ts.isParenthesizedExpression(node) ||
    ts.isAsExpression(node) ||
    ts.isSatisfiesExpression(node) ||
    ts.isTypeAssertionExpression(node)
  ) {
    node = node.expression;
  }
  return node;
}

function staticString(node, declarations, seen = new Set()) {
  node = unwrapExpression(node);
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text;

  if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken) {
    const left = staticString(node.left, declarations, seen);
    const right = staticString(node.right, declarations, seen);
    return left === null || right === null ? null : left + right;
  }

  if (ts.isTemplateExpression(node)) {
    let value = node.head.text;
    for (const span of node.templateSpans) {
      const expression = staticString(span.expression, declarations, seen);
      if (expression === null) return null;
      value += expression + span.literal.text;
    }
    return value;
  }

  if (ts.isIdentifier(node)) {
    if (seen.has(node.text)) return null;
    const declaration = declarations.get(node.text);
    if (!declaration) return null;
    const nextSeen = new Set(seen);
    nextSeen.add(node.text);
    return staticString(declaration, declarations, nextSeen);
  }

  return null;
}

function constDeclarations(sourceFile) {
  const declarations = new Map();

  function visit(node) {
    if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      node.initializer &&
      ts.isVariableDeclarationList(node.parent) &&
      (node.parent.flags & ts.NodeFlags.Const) !== 0
    ) {
      declarations.set(node.name.text, node.initializer);
    }
    ts.forEachChild(node, visit);
  }

  visit(sourceFile);
  return declarations;
}

function referencesForbiddenConstant(node, declarations) {
  let found = false;

  function visit(child) {
    if (found) return;
    if (ts.isIdentifier(child)) {
      const initializer = declarations.get(child.text);
      if (initializer && staticString(initializer, declarations)?.includes(API_MARKER)) {
        found = true;
        return;
      }
    }
    ts.forEachChild(child, visit);
  }

  visit(node);
  return found;
}

function nearestAliasScope(node) {
  let current = node.parent;
  while (current) {
    if (ts.isSourceFile(current) || ts.isFunctionLike(current)) return current;
    current = current.parent;
  }
  return undefined;
}

function referencesAlias(node, aliasesByScope) {
  let found = false;

  function visit(child) {
    if (found) return;
    if (ts.isIdentifier(child)) {
      let scope = nearestAliasScope(child);
      while (scope) {
        if (aliasesByScope.get(scope)?.has(child.text)) {
          found = true;
          return;
        }
        scope = nearestAliasScope(scope);
      }
    }
    ts.forEachChild(child, visit);
  }

  visit(node);
  return found;
}

function isGeneratedClientFactoryCall(node) {
  node = unwrapExpression(node);
  return ts.isCallExpression(node) && ts.isIdentifier(node.expression) && GENERATED_CLIENT_FACTORIES.has(node.expression.text);
}

function apiPathAliases(sourceFile) {
  const declarations = [];
  function collect(node) {
    if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      node.initializer &&
      ts.isVariableDeclarationList(node.parent) &&
      (node.parent.flags & ts.NodeFlags.Const) !== 0
    ) {
      declarations.push({ name: node.name.text, initializer: node.initializer, scope: nearestAliasScope(node) });
    }
    ts.forEachChild(node, collect);
  }
  collect(sourceFile);

  const aliasesByScope = new Map();
  let changed = true;
  while (changed) {
    changed = false;
    for (const { name, initializer, scope } of declarations) {
      if (!scope) continue;
      const aliases = aliasesByScope.get(scope) ?? new Set();
      if (aliases.has(name)) continue;
      if (isGeneratedClientFactoryCall(initializer)) continue;
      if (initializer.getText(sourceFile).includes(API_MARKER) || referencesAlias(initializer, aliasesByScope)) {
        aliases.add(name);
        aliasesByScope.set(scope, aliases);
        changed = true;
      }
    }
  }
  return aliasesByScope;
}

function structuralFindings(content, path, scopedProxyRanges) {
  const findings = [];

  for (const region of svelteScriptRegions(content, path)) {
    const sourceFile = ts.createSourceFile(path, region.content, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
    const declarations = constDeclarations(sourceFile);
    const pathAliases = apiPathAliases(sourceFile);

    function addFinding(node) {
      const offset = region.offset + node.getStart(sourceFile);
      if (isInScopedUpstreamProxy(offset, scopedProxyRanges)) return;
      const line = lineNumberAt(content, offset);
      const lineText = content.split(/\r?\n/)[line - 1] ?? "";
      const column = offset - content.lastIndexOf("\n", offset - 1);
      const context = contextFor(content.split(/\r?\n/), line - 1);
      if (isAllowedStreamingTransport(lineText, context)) return;
      findings.push({ file: path, line, column, message: API_URL_MESSAGE });
    }

    function visit(node) {
      if (ts.isVariableDeclaration(node) && node.initializer) {
        const value = staticString(node.initializer, declarations);
        if (
          (value?.includes(API_MARKER) &&
            !node.initializer.getText(sourceFile).includes(API_MARKER) &&
            !referencesForbiddenConstant(node.initializer, declarations)) ||
          (referencesAlias(node.initializer, pathAliases) && !isGeneratedClientFactoryCall(node.initializer))
        ) {
          addFinding(node.initializer);
        }
        return;
      }

      if (
        ts.isBinaryExpression(node) ||
        ts.isTemplateExpression(node) ||
        ts.isStringLiteral(node) ||
        ts.isNoSubstitutionTemplateLiteral(node)
      ) {
        const value = staticString(node, declarations);
        if (
          value?.includes(API_MARKER) &&
          !node.getText(sourceFile).includes(API_MARKER) &&
          !referencesForbiddenConstant(node, declarations)
        ) {
          addFinding(node);
          return;
        }
      }
      ts.forEachChild(node, visit);
    }

    visit(sourceFile);
  }

  return findings;
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
    const lineStarts = lineStartOffsets(content);
    const scopedProxyRanges = scopedUpstreamProxyRanges(content, relPath);

    findings.push(...structuralFindings(content, relPath, scopedProxyRanges));

    lines.forEach((line, index) => {
      const column = line.indexOf(API_MARKER);
      if (column === -1 || isCommentOnly(line)) return;
      if (isInScopedUpstreamProxy(lineStarts[index] + column, scopedProxyRanges)) return;

      const context = contextFor(lines, index);
      if (isAllowedStreamingTransport(line, context)) return;

      findings.push({
        file: relPath,
        line: index + 1,
        column: column + 1,
        message: API_URL_MESSAGE,
      });
    });
  }

  return findings.sort((a, b) => a.file.localeCompare(b.file) || a.line - b.line || a.column - b.column);
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

Detect hardcoded Kenn Forge /api/v1 URLs in production frontend TypeScript
and Svelte code. Test files, generated code, and scoped streaming
or upstream-proxy transports are ignored.
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
