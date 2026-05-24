import { parsePatchFiles, processFile } from "@pierre/diffs";
import type { FileContents, FileDiffMetadata, ThemeTypes } from "@pierre/diffs";
import type { DiffFile } from "../../api/types.js";

interface ParsePierreFileDiffOptions {
  enableDemandContextExpansion?: boolean;
}

export function appThemeType(): ThemeTypes {
  if (typeof document === "undefined") return "system";
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

export function parsePierreFileDiff(
  file: DiffFile,
  options: ParsePierreFileDiffOptions = {},
): FileDiffMetadata | undefined {
  if (!file.patch) return undefined;
  if (options.enableDemandContextExpansion) {
    const contents = sparsePatchContents(file);
    return processFile(file.patch, {
      cacheKey: `middleman:${file.path}`,
      oldFile: contents.oldFile,
      newFile: contents.newFile,
      throwOnError: true,
    });
  }
  return parsePatchFiles(file.patch, `middleman:${file.path}`, true)[0]?.files[0];
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

  return {
    oldFile: {
      name: file.old_path || file.path,
      contents: joinSparseLines(oldLines),
      cacheKey: `middleman:${file.path}:old:patch`,
    },
    newFile: {
      name: file.path,
      contents: joinSparseLines(newLines),
      cacheKey: `middleman:${file.path}:new:patch`,
    },
  };
}

function joinSparseLines(lines: string[]): string {
  while (lines.length > 0 && lines[lines.length - 1] == null) lines.pop();
  return lines.map((line) => line ?? "").join("\n");
}
