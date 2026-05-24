import { parsePatchFiles } from "@pierre/diffs";
import type { FileDiffMetadata, ThemeTypes } from "@pierre/diffs";
import type { DiffFile } from "../../api/types.js";

export function appThemeType(): ThemeTypes {
  if (typeof document === "undefined") return "system";
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

export function parsePierreFileDiff(file: DiffFile): FileDiffMetadata | undefined {
  if (!file.patch) return undefined;
  return parsePatchFiles(file.patch, `middleman:${file.path}`, true)[0]?.files[0];
}
