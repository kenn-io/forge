import type { DiffFile } from "../api/types.js";

export type DiffFileCategory = "plansDocs" | "generated" | "code" | "tests" | "other";
export type DiffFileCategoryFilter = DiffFileCategory | "all";
export type DiffFileCategoryCounts = Record<DiffFileCategoryFilter, number>;
type CategorizableDiffFile = Pick<DiffFile, "path"> & {
  is_generated?: boolean;
};

export const diffFileCategoryOptions: {
  value: DiffFileCategoryFilter;
  label: string;
}[] = [
  { value: "plansDocs", label: "Plans/docs" },
  { value: "code", label: "Code" },
  { value: "tests", label: "Tests" },
  { value: "other", label: "Other" },
  { value: "generated", label: "Generated" },
  { value: "all", label: "All" },
];

const codeExtensions = new Set([
  ".bash",
  ".c",
  ".cc",
  ".cpp",
  ".cs",
  ".css",
  ".go",
  ".gql",
  ".graphql",
  ".h",
  ".hpp",
  ".html",
  ".java",
  ".js",
  ".jsx",
  ".kt",
  ".kts",
  ".less",
  ".php",
  ".py",
  ".rb",
  ".rs",
  ".sass",
  ".scss",
  ".sh",
  ".sql",
  ".svelte",
  ".swift",
  ".ts",
  ".tsx",
  ".vue",
  ".zsh",
]);

const docsExtensions = new Set([".adoc", ".md", ".mdx", ".rst", ".txt"]);

const docsDirectoryNames = new Set(["doc", "docs", "documentation"]);
const generatedBasenames = new Set([
  "bun.lock",
  "bun.lockb",
  "cargo.lock",
  "composer.lock",
  "deno.lock",
  "flake.lock",
  "gemfile.lock",
  "go.sum",
  "gradle.lockfile",
  "mix.lock",
  "npm-shrinkwrap.json",
  "package-lock.json",
  "pipfile.lock",
  "pnpm-lock.yaml",
  "poetry.lock",
  "pubspec.lock",
  ".terraform.lock.hcl",
  "terraform.lock.hcl",
  "uv.lock",
  "yarn.lock",
]);
const generatedSuffixes = [".lock", ".lock.json", ".lock.yaml", ".lock.yml"];

function pathParts(path: string): string[] {
  return path
    .toLowerCase()
    .split(/[\\/]+/)
    .filter(Boolean);
}

function basename(path: string): string {
  const parts = pathParts(path);
  return parts[parts.length - 1] ?? "";
}

function extension(path: string): string {
  const base = basename(path);
  const dot = base.lastIndexOf(".");
  return dot >= 0 ? base.slice(dot) : "";
}

function hasTestSignal(parts: string[], base: string): boolean {
  return (
    parts.some(
      (part) => part === "test" || part === "tests" || part === "__tests__" || part === "e2e" || part === "spec",
    ) ||
    base.includes(".test.") ||
    base.includes(".spec.") ||
    base.endsWith("_test.go") ||
    base.endsWith("_test.py") ||
    base.startsWith("test_") ||
    base.endsWith(".snap")
  );
}

function hasDocsSignal(parts: string[], base: string, ext: string): boolean {
  return (
    parts.some((part) => docsDirectoryNames.has(part)) ||
    docsExtensions.has(ext) ||
    ["changelog", "code_of_conduct", "contributing", "license", "notice", "readme", "security"].some(
      (name) => base === name || base.startsWith(`${name}.`),
    )
  );
}

function hasGeneratedSignal(base: string): boolean {
  return generatedBasenames.has(base) || generatedSuffixes.some((suffix) => base.endsWith(suffix));
}

export function categorizeDiffFile(file: string | CategorizableDiffFile): DiffFileCategory {
  const path = typeof file === "string" ? file : file.path;
  const parts = pathParts(path);
  const base = basename(path);
  const ext = extension(path);
  const generatedMetadata = typeof file === "string" ? undefined : file.is_generated;

  if (generatedMetadata === true) return "generated";
  if (generatedMetadata !== false && hasGeneratedSignal(base)) return "generated";
  if (hasTestSignal(parts, base)) return "tests";
  if (hasDocsSignal(parts, base, ext)) return "plansDocs";
  if (codeExtensions.has(ext)) return "code";
  return "other";
}

export function filterDiffFilesByCategory(files: DiffFile[], filter: DiffFileCategoryFilter): DiffFile[] {
  if (filter === "all") return files;
  return files.filter((file) => categorizeDiffFile(file) === filter);
}

export function countDiffFilesByCategory(files: DiffFile[]): DiffFileCategoryCounts {
  const counts: DiffFileCategoryCounts = {
    plansDocs: 0,
    generated: 0,
    code: 0,
    tests: 0,
    other: 0,
    all: files.length,
  };

  for (const file of files) {
    counts[categorizeDiffFile(file)] += 1;
  }

  return counts;
}
