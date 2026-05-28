import { parsePatchFiles, processFile } from "@pierre/diffs";
import type { FileContents, FileDiffMetadata, ThemeTypes } from "@pierre/diffs";
import type { DiffFile } from "../../api/types.js";

interface ParsePierreFileDiffOptions {
  enableDemandContextExpansion?: boolean;
}

const maxSparseContextLine = 50_000;

export function appThemeType(): ThemeTypes {
  if (typeof document === "undefined") return "system";
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

export function parsePierreFileDiff(
  file: DiffFile,
  options: ParsePierreFileDiffOptions = {},
): FileDiffMetadata | undefined {
  if (!file.patch) return undefined;
  if (options.enableDemandContextExpansion && canBuildSparsePatchContents(file)) {
    const contents = sparsePatchContents(file);
    return processPatchWithContext(file, contents);
  }
  return parsePatchOnly(file);
}

function processPatchWithContext(
  file: DiffFile,
  contents: { oldFile: FileContents; newFile: FileContents },
): FileDiffMetadata | undefined {
  const parsed = tryProcessPatch(file.patch, contents);
  if (parsed) return parsed;

  const safePatch = safePierrePatch(file);
  if (safePatch === file.patch) return parsePatchOnly(file);
  return tryProcessPatch(safePatch, {
    oldFile: { ...contents.oldFile, name: safePierreFileName(file, "old") },
    newFile: { ...contents.newFile, name: safePierreFileName(file, "new") },
  }) ?? parsePatchOnly({ ...file, patch: safePatch });
}

function tryProcessPatch(
  patch: string,
  contents: { oldFile: FileContents; newFile: FileContents },
): FileDiffMetadata | undefined {
  try {
    return processFile(patch, {
      oldFile: contents.oldFile,
      newFile: contents.newFile,
      throwOnError: true,
    });
  } catch {
    return undefined;
  }
}

function parsePatchOnly(file: DiffFile): FileDiffMetadata | undefined {
  const parsed = tryParsePatch(file.patch);
  if (parsed) return parsed;

  const safePatch = safePierrePatch(file);
  if (safePatch === file.patch) return undefined;
  return tryParsePatch(safePatch);
}

function tryParsePatch(patch: string): FileDiffMetadata | undefined {
  try {
    return parsePatchFiles(patch, undefined, true)[0]?.files[0];
  } catch {
    return undefined;
  }
}

function safePierrePatch(file: DiffFile): string {
  const oldName = safePierreFileName(file, "old");
  const newName = safePierreFileName(file, "new");
  return file.patch.split("\n").map((line) => {
    if (line.startsWith("diff --git ")) return `diff --git a/${oldName} b/${newName}`;
    if (line === "--- /dev/null") return line;
    if (line === "+++ /dev/null") return line;
    if (line.startsWith("--- ")) return `--- a/${oldName}`;
    if (line.startsWith("+++ ")) return `+++ b/${newName}`;
    if (line.startsWith("rename from ")) return `rename from ${oldName}`;
    if (line.startsWith("rename to ")) return `rename to ${newName}`;
    return line;
  }).join("\n");
}

function safePierreFileName(file: DiffFile, side: "old" | "new"): string {
  const source = side === "old" ? file.old_path || file.path : file.path;
  const extensionIndex = source.lastIndexOf(".");
  const extension = extensionIndex >= 0 ? source.slice(extensionIndex) : "";
  return `middleman-diff${extension.replace(/[^A-Za-z0-9.]/g, "")}`;
}

function canBuildSparsePatchContents(file: DiffFile): boolean {
  for (const hunk of file.hunks) {
    if (
      !lineRangeFits(hunk.old_start, hunk.old_count) ||
      !lineRangeFits(hunk.new_start, hunk.new_count)
    ) {
      return false;
    }

    for (const line of hunk.lines) {
      if (
        (line.old_num != null && !lineNumberFits(line.old_num)) ||
        (line.new_num != null && !lineNumberFits(line.new_num))
      ) {
        return false;
      }
    }
  }
  return true;
}

function lineRangeFits(start: number, count: number): boolean {
  if (!Number.isSafeInteger(start) || !Number.isSafeInteger(count)) return false;
  if (start < 1 || count < 0) return false;
  return start + count - 1 <= maxSparseContextLine;
}

function lineNumberFits(lineNumber: number): boolean {
  return Number.isSafeInteger(lineNumber) &&
    lineNumber >= 1 &&
    lineNumber <= maxSparseContextLine;
}

function sparsePatchContents(file: DiffFile): { oldFile: FileContents; newFile: FileContents } {
  const oldLines: string[] = [];
  const newLines: string[] = [];

  for (const hunk of file.hunks) {
    for (const line of hunk.lines) {
      if ((line.type === "context" || line.type === "delete") && line.old_num != null) {
        oldLines[line.old_num - 1] = line.content;
      }
      if ((line.type === "context" || line.type === "add") && line.new_num != null) {
        newLines[line.new_num - 1] = line.content;
      }
    }
  }
  const oldContents = joinSparseLines(oldLines);
  const newContents = joinSparseLines(newLines);

  return {
    oldFile: {
      name: file.old_path || file.path,
      contents: oldContents,
    },
    newFile: {
      name: file.path,
      contents: newContents,
    },
  };
}

function joinSparseLines(lines: string[]): string {
  while (lines.length > 0 && lines[lines.length - 1] == null) lines.pop();
  return lines.map((line) => line ?? "").join("\n");
}
