import type { RepoBrowserTreeEntry } from "@middleman/ui/api/types";

const markdownExtensions = new Set([".md", ".mdx"]);

export function chooseRepoBrowserInitialPath(
  entries: readonly Pick<RepoBrowserTreeEntry, "path" | "type">[],
): string | null {
  const files = entries.filter((entry) => entry.type === "file" || entry.type === "blob");
  const rootReadme = files.find((entry) => isReadme(entry.path) && !entry.path.includes("/"));
  const nestedReadme = files.find((entry) => isReadme(entry.path));
  return rootReadme?.path ?? nestedReadme?.path ?? files[0]?.path ?? null;
}

export function isRepoBrowserMarkdownPath(path: string | null | undefined): boolean {
  if (!path) return false;
  return markdownExtensions.has(extension(path));
}

export function formatRepoBrowserFileSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  const kib = bytes / 1024;
  if (kib < 1024) return `${formatDecimal(kib)} KB`;
  return `${formatDecimal(kib / 1024)} MB`;
}

export function formatRepoBrowserCommitDate(value: string | null | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  }).format(date);
}

export function formatRepoBrowserCommitAge(value: string | null | undefined, now: Date = new Date()): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || Number.isNaN(now.getTime())) return "";
  const diffMs = Math.max(0, now.getTime() - date.getTime());
  const minuteMs = 60 * 1000;
  const hourMs = 60 * minuteMs;
  const dayMs = 24 * hourMs;
  if (diffMs < hourMs) return `${Math.max(1, Math.floor(diffMs / minuteMs))}m`;
  if (diffMs < dayMs) return `${Math.floor(diffMs / hourMs)}h`;
  const days = Math.floor(diffMs / dayMs);
  if (days < 14) return `${days}d`;
  const weeks = Math.floor(days / 7);
  if (weeks < 10) return `${weeks}w`;
  const months = Math.floor(days / 30);
  if (months < 18) return `${months}mo`;
  return `${Math.floor(days / 365)}y`;
}

function basename(path: string): string {
  return (
    path
      .split(/[\\/]+/)
      .filter(Boolean)
      .at(-1) ?? ""
  );
}

function isReadme(path: string): boolean {
  return /^readme(?:\.[^.]+)?$/i.test(basename(path));
}

function extension(path: string): string {
  const base = basename(path).toLowerCase();
  const dot = base.lastIndexOf(".");
  return dot >= 0 ? base.slice(dot) : "";
}

function formatDecimal(value: number): string {
  return value >= 10 ? value.toFixed(0) : value.toFixed(1).replace(/\.0$/, "");
}
