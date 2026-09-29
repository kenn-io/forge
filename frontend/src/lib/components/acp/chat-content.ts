import { isSafeExternalHTTPURL } from "../../utils/safe-external-url.js";
import type { ChatContent } from "./chat-types.js";
import { parseDiffFromFile } from "@pierre/diffs";

const base64Pattern = /^[A-Za-z0-9+/=\s]*$/;

// data: URLs are only ever built for <img>/<audio> sources, from a validated
// media type and base64 payload. SVG is allowed: an <img> renders it without
// scripts or external loads.
export function mediaDataURL(content: ChatContent, family: "image" | "audio"): string | undefined {
  const mime = content.mimeType?.trim().toLowerCase() ?? "";
  if (!new RegExp(`^${family}/[a-z0-9.+-]+$`).test(mime)) return undefined;
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

// Line diff for one file's old/new text, with unchanged runs trimmed to a few
// lines of context. It has no size cutoff: every edit gets a real diff.
export function lineDiff(oldText: string | null | undefined, newText: string | null | undefined): DiffLine[] {
  const diff = parseDiffFromFile(
    { name: "file", contents: oldText ?? "" },
    { name: "file", contents: newText ?? "" },
    { context: diffContext },
  );
  const text = (line: string | undefined) => (line ?? "").replace(/\r?\n$/, "");
  const lines: DiffLine[] = [];
  let end = 0;
  for (const hunk of diff.hunks) {
    if (hunk.collapsedBefore > 0) lines.push({ kind: "gap", text: "" });
    for (const block of hunk.hunkContent) {
      if (block.type === "context") {
        for (let i = 0; i < block.lines; i++) {
          lines.push({ kind: "context", text: text(diff.additionLines[block.additionLineIndex + i]) });
        }
        end = block.additionLineIndex + block.lines;
        continue;
      }
      for (let i = 0; i < block.deletions; i++) {
        lines.push({ kind: "removed", text: text(diff.deletionLines[block.deletionLineIndex + i]) });
      }
      for (let i = 0; i < block.additions; i++) {
        lines.push({ kind: "added", text: text(diff.additionLines[block.additionLineIndex + i]) });
      }
      end = block.additionLineIndex + block.additions;
    }
  }
  if (end < diff.additionLines.length) lines.push({ kind: "gap", text: "" });
  return lines;
}
