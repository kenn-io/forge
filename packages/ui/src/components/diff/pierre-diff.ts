import { getFiletypeFromFileName, parsePatchFiles, processFile } from "@pierre/diffs";
import type { FileContents, FileDiffMetadata, ThemeTypes } from "@pierre/diffs";
import type { DiffFile } from "../../api/types.js";
import { syntaxHighlightingDisabledForAutomation } from "./pierre-worker-pool.js";

interface ParsePierreFileDiffOptions {
  enableDemandContextExpansion?: boolean;
}

type PierreDiffDebugDetails = Record<string, unknown>;

const debugDiffStorageKey = "middleman:debug:diff";
const maxSparseContextLine = 50_000;
const syntheticPatchFiles = new WeakSet<DiffFile>();

// Pierre skips tokenization entirely for "text" diffs; pinning the
// language override is how automation runs (see pierre-worker-pool)
// drop shiki's per-hunk highlighting cost without changing the
// rendered structure.
function withAutomationLanguage(meta: FileDiffMetadata | undefined): FileDiffMetadata | undefined {
  if (!meta || !syntaxHighlightingDisabledForAutomation()) return meta;
  return { ...meta, lang: "text" };
}

export function appThemeType(): ThemeTypes {
  if (typeof document === "undefined") return "system";
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

export function parsePierreFileDiff(
  file: DiffFile,
  options: ParsePierreFileDiffOptions = {},
): FileDiffMetadata | undefined {
  const patchedFile = diffFileWithPatch(file);
  if (!patchedFile.patch) return undefined;
  if (options.enableDemandContextExpansion && canBuildSparsePatchContents(patchedFile)) {
    const contents = sparsePatchContents(patchedFile);
    const meta = withAutomationLanguage(
      withRenderMetadata(
        patchedFile,
        withSyntheticPatchCacheKey(patchedFile, processPatchWithContext(patchedFile, contents)),
        contextRenderCacheIdentity(patchedFile, "sparse", contents),
      ),
    );
    debugPierreDiff("parse sparse context diff", contextDebugDetails(patchedFile, "sparse", contents, meta));
    return meta;
  }
  const meta = withAutomationLanguage(
    withRenderMetadata(patchedFile, withSyntheticPatchCacheKey(patchedFile, parsePatchOnly(patchedFile))),
  );
  debugPierreDiff("parse patch diff", {
    path: patchedFile.path,
    status: patchedFile.status,
    cacheKey: meta?.cacheKey,
    hunkCount: meta?.hunks.length,
  });
  return meta;
}

export function parsePierreFileDiffWithContents(
  file: DiffFile,
  contents: { oldFile: FileContents; newFile: FileContents },
): FileDiffMetadata | undefined {
  const patchedFile = diffFileWithPatch(file);
  const meta = withAutomationLanguage(
    withRenderMetadata(
      patchedFile,
      withSyntheticPatchCacheKey(patchedFile, processPatchWithContext(patchedFile, contents)),
      contextRenderCacheIdentity(patchedFile, "full", contents),
    ),
  );
  debugPierreDiff("parse full context diff", contextDebugDetails(patchedFile, "full", contents, meta));
  return meta;
}

export function pierreFileContents(name: string, contents: string, cacheIdentity: string): FileContents {
  return {
    name,
    contents,
    cacheKey: fileContentsCacheKey(name, contents, cacheIdentity),
  };
}

export function debugPierreDiff(message: string, details?: PierreDiffDebugDetails): void {
  if (!pierreDiffDebugEnabled()) return;
  console.debug("[middleman:diff]", message, details ?? {});
}

export function pierreDiffDebugEnabled(): boolean {
  if (typeof window === "undefined") return false;
  try {
    if (new URLSearchParams(window.location.search).get("debugDiff") === "1") return true;
    return window.localStorage?.getItem(debugDiffStorageKey) === "1";
  } catch {
    return false;
  }
}

export function diffFileWithPatch(file: DiffFile): DiffFile {
  const patch = file.patch && patchHasFileHeader(file.patch) ? file.patch : synthesizePatch(file) || file.patch;
  if (patch === file.patch) return file;
  const patchedFile = { ...file, patch };
  syntheticPatchFiles.add(patchedFile);
  return patchedFile;
}

export function sparseContextMayDistortSyntax(file: DiffFile): boolean {
  const hunks = file.hunks ?? [];
  for (let index = 0; index < hunks.length - 1; index += 1) {
    const current = hunks[index];
    const next = hunks[index + 1];
    if (!current || !next || !hasCollapsedGap(current, next)) continue;
    if (hunkMayCarrySyntaxState(current)) return true;
  }
  return false;
}

function processPatchWithContext(
  file: DiffFile,
  contents: { oldFile: FileContents; newFile: FileContents },
): FileDiffMetadata | undefined {
  const parsed = tryProcessPatch(file.patch, contents);
  if (parsed) return parsed;

  const safePatch = safePierrePatch(file);
  if (safePatch === file.patch) return parsePatchOnly(file);
  return (
    tryProcessPatch(safePatch, {
      oldFile: fileContentsWithName(contents.oldFile, safePierreFileName(file, "old"), "safe-old"),
      newFile: fileContentsWithName(contents.newFile, safePierreFileName(file, "new"), "safe-new"),
    }) ?? parsePatchOnly({ ...file, patch: safePatch })
  );
}

function tryProcessPatch(
  patch: string,
  contents: { oldFile: FileContents; newFile: FileContents },
): FileDiffMetadata | undefined {
  try {
    return processFile(patch, {
      oldFile: contents.oldFile,
      newFile: contents.newFile,
      throwOnError: false,
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
    return parsePatchFiles(patch, undefined, false)[0]?.files[0];
  } catch {
    return undefined;
  }
}

function patchHasFileHeader(patch: string): boolean {
  return patch.startsWith("diff --git ") || patch.startsWith("--- ");
}

function withSyntheticPatchCacheKey(file: DiffFile, meta: FileDiffMetadata | undefined): FileDiffMetadata | undefined {
  if (!meta || !syntheticPatchFiles.has(file)) return meta;
  return {
    ...meta,
    cacheKey: fileContentsCacheKey(file.path, file.patch, `synthetic-diff:${file.status}:${file.old_path || ""}`),
  };
}

function withRenderMetadata(
  file: DiffFile,
  meta: FileDiffMetadata | undefined,
  cacheIdentity?: string,
): FileDiffMetadata | undefined {
  if (!meta) return meta;
  return {
    ...meta,
    cacheKey:
      cacheIdentity != null
        ? fileContentsCacheKey(file.path, file.patch, cacheIdentity)
        : (meta.cacheKey ??
          fileContentsCacheKey(
            file.path,
            file.patch,
            `${syntheticPatchFiles.has(file) ? "synthetic" : "provider"}-diff:${file.status}:${file.old_path || ""}`,
          )),
    lang: meta.lang ?? getFiletypeFromFileName(file.path),
  };
}

function contextRenderCacheIdentity(
  file: DiffFile,
  kind: "full" | "sparse",
  contents: { oldFile: FileContents; newFile: FileContents },
): string {
  return [
    `${kind}-diff`,
    file.status,
    file.old_path || "",
    contents.oldFile.cacheKey ?? "",
    contents.newFile.cacheKey ?? "",
  ].join(":");
}

function contextDebugDetails(
  file: DiffFile,
  kind: "full" | "sparse",
  contents: { oldFile: FileContents; newFile: FileContents },
  meta: FileDiffMetadata | undefined,
): PierreDiffDebugDetails {
  return {
    path: file.path,
    status: file.status,
    kind,
    cacheKey: meta?.cacheKey,
    hunkCount: meta?.hunks.length,
    oldCacheKey: contents.oldFile.cacheKey,
    newCacheKey: contents.newFile.cacheKey,
    oldLength: contents.oldFile.contents.length,
    newLength: contents.newFile.contents.length,
  };
}

function safePierrePatch(file: DiffFile): string {
  const oldName = safePierreFileName(file, "old");
  const newName = safePierreFileName(file, "new");
  let inHeader = true;
  return file.patch
    .split("\n")
    .map((line) => {
      if (line.startsWith("@@ ")) {
        inHeader = false;
        return line;
      }
      if (!inHeader) return line;
      if (line.startsWith("diff --git ")) return `diff --git a/${oldName} b/${newName}`;
      if (line === "--- /dev/null") return line;
      if (line === "+++ /dev/null") return line;
      if (line.startsWith("--- ")) return `--- a/${oldName}`;
      if (line.startsWith("+++ ")) return `+++ b/${newName}`;
      if (line.startsWith("rename from ")) return `rename from ${oldName}`;
      if (line.startsWith("rename to ")) return `rename to ${newName}`;
      return line;
    })
    .join("\n");
}

function synthesizePatch(file: DiffFile): string {
  if (!file.hunks?.length && !file.patch) return "";
  const oldName = file.old_path || file.path;
  const newName = file.path;
  const oldPath = patchPath(`a/${oldName}`);
  const newPath = patchPath(`b/${newName}`);
  const statusMetadata = file.status === "added" ? ["new file mode 100644"] : [];
  return [
    `diff --git ${oldPath} ${newPath}`,
    ...statusMetadata,
    `--- ${file.status === "added" ? "/dev/null" : oldPath}`,
    `+++ ${file.status === "deleted" ? "/dev/null" : newPath}`,
    ...(file.hunks?.length
      ? file.hunks.flatMap((hunk) => [
          `@@ -${hunk.old_start},${hunk.old_count} +${hunk.new_start},${hunk.new_count} @@${hunk.section ? ` ${hunk.section}` : ""}`,
          ...hunk.lines.map(patchLine),
        ])
      : patchBodyLines(file.patch)),
    "",
  ].join("\n");
}

function patchBodyLines(patch: string): string[] {
  const lines = patch.split("\n");
  if (lines.at(-1) === "") lines.pop();
  return lines;
}

function patchLine(line: { type: "context" | "add" | "delete"; content: string }): string {
  const prefix = line.type === "add" ? "+" : line.type === "delete" ? "-" : " ";
  return `${prefix}${line.content}`;
}

export function patchPath(path: string): string {
  if (path === "/dev/null" || !needsPatchPathQuote(path)) return path;
  return JSON.stringify(path)
    .replace(/[\u007f-\u009f]/gu, (char) => `\\u${char.charCodeAt(0).toString(16).padStart(4, "0")}`)
    .replace(/\u2028/g, "\\u2028")
    .replace(/\u2029/g, "\\u2029");
}

function needsPatchPathQuote(path: string): boolean {
  for (let index = 0; index < path.length; index += 1) {
    const code = path.charCodeAt(index);
    if (
      code === 0x22 ||
      code === 0x5c ||
      code < 0x20 ||
      (code >= 0x7f && code <= 0x9f) ||
      code === 0x2028 ||
      code === 0x2029
    ) {
      return true;
    }
  }
  return false;
}

function safePierreFileName(file: DiffFile, side: "old" | "new"): string {
  const source = side === "old" ? file.old_path || file.path : file.path;
  const extensionIndex = source.lastIndexOf(".");
  const extension = extensionIndex >= 0 ? source.slice(extensionIndex) : "";
  return `middleman-diff${extension.replace(/[^A-Za-z0-9.]/g, "")}`;
}

function canBuildSparsePatchContents(file: DiffFile): boolean {
  for (const hunk of file.hunks ?? []) {
    if (!lineRangeFits(hunk.old_start, hunk.old_count) || !lineRangeFits(hunk.new_start, hunk.new_count)) {
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

function hasCollapsedGap(
  current: NonNullable<DiffFile["hunks"]>[number],
  next: NonNullable<DiffFile["hunks"]>[number],
): boolean {
  const oldGap = next.old_start - (current.old_start + current.old_count);
  const newGap = next.new_start - (current.new_start + current.new_count);
  return oldGap > 0 || newGap > 0;
}

function hunkMayCarrySyntaxState(hunk: NonNullable<DiffFile["hunks"]>[number]): boolean {
  const oldState = syntaxStateAfterHunkSide(hunk, "old");
  const newState = syntaxStateAfterHunkSide(hunk, "new");

  return syntaxStateOpen(oldState) || syntaxStateOpen(newState);
}

function syntaxStateAfterHunkSide(
  hunk: NonNullable<DiffFile["hunks"]>[number],
  side: "old" | "new",
): { templateBacktickCount: number; blockCommentOpen: boolean } {
  let templateBacktickCount = 0;
  let blockCommentOpen = false;
  for (const line of hunk.lines) {
    if (side === "old" && line.type === "add") continue;
    if (side === "new" && line.type === "delete") continue;
    const content = line.content;
    templateBacktickCount += countUnescapedBackticks(content);
    blockCommentOpen = blockCommentStateAfterLine(content, blockCommentOpen);
  }

  return { templateBacktickCount, blockCommentOpen };
}

function syntaxStateOpen(state: { templateBacktickCount: number; blockCommentOpen: boolean }): boolean {
  return state.templateBacktickCount % 2 === 1 || state.blockCommentOpen;
}

function countUnescapedBackticks(line: string): number {
  let count = 0;
  let backslashes = 0;
  for (const char of line) {
    if (char === "\\") {
      backslashes += 1;
      continue;
    }
    if (char === "`" && backslashes % 2 === 0) count += 1;
    backslashes = 0;
  }
  return count;
}

function blockCommentStateAfterLine(line: string, open: boolean): boolean {
  let index = 0;
  while (index < line.length) {
    if (open) {
      const close = line.indexOf("*/", index);
      if (close < 0) return true;
      open = false;
      index = close + 2;
      continue;
    }

    const start = line.indexOf("/*", index);
    if (start < 0) return false;
    const close = line.indexOf("*/", start + 2);
    if (close < 0) return true;
    index = close + 2;
  }
  return open;
}

function lineRangeFits(start: number, count: number): boolean {
  if (!Number.isSafeInteger(start) || !Number.isSafeInteger(count)) return false;
  if (start < 1 || count < 0) return false;
  return start + count - 1 <= maxSparseContextLine;
}

function lineNumberFits(lineNumber: number): boolean {
  return Number.isSafeInteger(lineNumber) && lineNumber >= 1 && lineNumber <= maxSparseContextLine;
}

function sparsePatchContents(file: DiffFile): {
  oldFile: FileContents;
  newFile: FileContents;
} {
  const oldLines: string[] = [];
  const newLines: string[] = [];

  for (const hunk of file.hunks ?? []) {
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
    oldFile: pierreFileContents(file.old_path || file.path, oldContents, "sparse-old"),
    newFile: pierreFileContents(file.path, newContents, "sparse-new"),
  };
}

function joinSparseLines(lines: string[]): string {
  while (lines.length > 0 && lines[lines.length - 1] == null) lines.pop();
  return lines.map((line) => line ?? "").join("\n");
}

function fileContentsWithName(file: FileContents, name: string, cacheIdentity: string): FileContents {
  return {
    ...file,
    name,
    cacheKey: fileContentsCacheKey(name, file.contents, `${cacheIdentity}:${file.cacheKey ?? ""}`),
  };
}

function fileContentsCacheKey(name: string, contents: string, cacheIdentity: string): string {
  let hash = 0x811c9dc5;
  const seed = `${cacheIdentity}\0${name}\0${contents.length}\0${contents}`;
  for (let index = 0; index < seed.length; index += 1) {
    hash ^= seed.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return `${cacheIdentity}:${contents.length}:${(hash >>> 0).toString(36)}`;
}
