import { isSafeExternalHTTPURL } from "../../utils/safe-external-url.js";
import type { ChatContent } from "./chat-types.js";

const base64Pattern = /^[A-Za-z0-9+/=\s]*$/;

// Agent images render only as safe raster formats, like data: images in
// markdown; SVG can carry script and external references.
const rasterImageTypes = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/avif"]);

export function isRasterImageType(mimeType: string | null | undefined): boolean {
  return rasterImageTypes.has(mimeType?.trim().toLowerCase() ?? "");
}

// data: URLs are only ever built for <img>/<audio> sources, from a validated
// media type and base64 payload.
export function mediaDataURL(content: ChatContent, family: "image" | "audio"): string | undefined {
  const mime = content.mimeType?.trim().toLowerCase() ?? "";
  if (family === "image" ? !isRasterImageType(mime) : !/^audio\/[a-z0-9.+-]+$/.test(mime)) return undefined;
  if (!content.data || !base64Pattern.test(content.data)) return undefined;
  return `data:${mime};base64,${content.data.replace(/\s+/g, "")}`;
}

export function formatBytes(size: number | null | undefined): string {
  if (size == null || !Number.isFinite(size) || size < 0) return "";
  if (size < 1024) return `${size} B`;
  const units = ["KB", "MB", "GB"];
  let value = size / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

// Only web and mail links open; file:// and other schemes stay plain text.
export function resourceHref(uri: string | null | undefined): string | undefined {
  if (!uri) return undefined;
  if (isSafeExternalHTTPURL(uri)) return uri;
  return /^mailto:[^\s/]+@[^\s]+$/i.test(uri) ? uri : undefined;
}

function lastSegment(uri: string): string {
  const path = uri.replace(/[?#].*$/, "");
  return decodeURIComponentSafe(path.slice(path.lastIndexOf("/") + 1));
}

function decodeURIComponentSafe(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

export function resourceFilename(content: ChatContent): string {
  const fromUri = content.uri ? lastSegment(content.uri) : "";
  const name = (fromUri || content.name || "resource").replace(/[\\/:*?"<>|]+/g, "_");
  return name || "resource";
}

const extensionLanguages: Record<string, string> = {
  ts: "typescript",
  tsx: "tsx",
  js: "javascript",
  jsx: "jsx",
  mjs: "javascript",
  cjs: "javascript",
  json: "json",
  md: "markdown",
  py: "python",
  go: "go",
  rs: "rust",
  rb: "ruby",
  java: "java",
  kt: "kotlin",
  swift: "swift",
  c: "c",
  h: "c",
  cpp: "cpp",
  cc: "cpp",
  hpp: "cpp",
  cs: "csharp",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  yaml: "yaml",
  yml: "yaml",
  toml: "toml",
  sql: "sql",
  html: "html",
  css: "css",
  scss: "scss",
  svelte: "svelte",
  vue: "vue",
  xml: "xml",
  diff: "diff",
};
const mimeLanguages: Record<string, string> = {
  "application/json": "json",
  "text/markdown": "markdown",
  "text/html": "html",
  "text/css": "css",
  "text/javascript": "javascript",
  "application/javascript": "javascript",
  "text/typescript": "typescript",
  "text/x-python": "python",
  "text/x-go": "go",
  "text/x-rust": "rust",
  "text/x-shellscript": "bash",
  "application/x-sh": "bash",
  "text/yaml": "yaml",
  "application/yaml": "yaml",
  "application/xml": "xml",
  "text/xml": "xml",
  "application/toml": "toml",
  "text/x-diff": "diff",
};

// Highlighter language for an embedded text resource, from its file extension
// or media type; undefined renders plain text.
export function resourceLanguage(content: ChatContent): string | undefined {
  const segment = content.uri ? lastSegment(content.uri) : (content.name ?? "");
  const extension = segment.includes(".") ? segment.slice(segment.lastIndexOf(".") + 1).toLowerCase() : "";
  return extensionLanguages[extension] ?? mimeLanguages[content.mimeType?.toLowerCase() ?? ""];
}

const ESC = String.fromCharCode(27);
const BEL = String.fromCharCode(7);
// CSI sequences (colors, cursor moves), OSC sequences (titles, links), and
// two-byte escapes. Output is shown as plain text, so they are dropped.
const ansiPattern = new RegExp(
  `${ESC}\\[[0-?]*[ -/]*[@-~]|${ESC}\\][^${BEL}${ESC}]*(?:${BEL}|${ESC}\\\\)|${ESC}[@-_]`,
  "g",
);

export function stripAnsi(text: string): string {
  return text.replace(ansiPattern, "");
}

export function decodeBase64(data: string): Uint8Array {
  const binary = atob(data.replace(/\s+/g, ""));
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
}

export type DiffLine = { kind: "context" | "added" | "removed" | "gap"; text: string };

const diffContext = 3;
const maxDiffCells = 4_000_000;

// Line diff for one file's old/new text: an LCS walk, with unchanged runs
// trimmed to a few lines of context. Very large inputs skip the LCS and show
// the whole replacement.
export function lineDiff(oldText: string | null | undefined, newText: string | null | undefined): DiffLine[] {
  const before = oldText ? oldText.split("\n") : [];
  const after = newText ? newText.split("\n") : [];
  const lines: DiffLine[] = [];
  if (before.length * after.length > maxDiffCells) {
    return [
      ...before.map((text): DiffLine => ({ kind: "removed", text })),
      ...after.map((text): DiffLine => ({ kind: "added", text })),
    ];
  }
  const width = after.length + 1;
  const table = new Uint32Array((before.length + 1) * width);
  for (let i = before.length - 1; i >= 0; i--) {
    for (let j = after.length - 1; j >= 0; j--) {
      table[i * width + j] =
        before[i] === after[j]
          ? table[(i + 1) * width + j + 1]! + 1
          : Math.max(table[(i + 1) * width + j]!, table[i * width + j + 1]!);
    }
  }
  let i = 0;
  let j = 0;
  while (i < before.length || j < after.length) {
    if (i < before.length && j < after.length && before[i] === after[j]) {
      lines.push({ kind: "context", text: before[i]! });
      i += 1;
      j += 1;
    } else if (i < before.length && (j === after.length || table[(i + 1) * width + j]! >= table[i * width + j + 1]!)) {
      lines.push({ kind: "removed", text: before[i]! });
      i += 1;
    } else {
      lines.push({ kind: "added", text: after[j]! });
      j += 1;
    }
  }
  return trimContext(lines);
}

function trimContext(lines: DiffLine[]): DiffLine[] {
  const keep = lines.map((line) => line.kind !== "context");
  const near = lines.map((_, index) =>
    keep.slice(Math.max(0, index - diffContext), index + diffContext + 1).some(Boolean),
  );
  const result: DiffLine[] = [];
  lines.forEach((line, index) => {
    if (near[index]) result.push(line);
    else if (result.at(-1)?.kind !== "gap") result.push({ kind: "gap", text: "" });
  });
  return result;
}
