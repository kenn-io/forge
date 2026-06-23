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
