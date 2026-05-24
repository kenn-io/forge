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
    return processFile(file.patch, {
      oldFile: contents.oldFile,
      newFile: contents.newFile,
      throwOnError: true,
    });
  }
  return parsePatchFiles(file.patch, undefined, true)[0]?.files[0];
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
