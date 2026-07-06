import type { DiffFile, DiffLine } from "../api/types.js";
import {
  reviewThreadStartLine,
  reviewThreadTargetLine,
  type ReviewThread,
  type ReviewThreadContext,
  type ReviewThreadContextLine,
} from "../components/diff/review-thread-context.js";

export type MarkdownSuggestionBlock =
  | {
      type: "markdown";
      key: string;
      text: string;
    }
  | {
      type: "suggestion";
      key: string;
      replacement: string;
      fenceLine: number;
    };

export type ApplySuggestionRequest = {
  suggestions: {
    threadID: string;
    replacement: string;
  }[];
  message?: string | undefined;
};

type Fence = {
  marker: "`" | "~";
  length: number;
  info: string;
};

const suggestionInfo = /^suggestion(?:[\s:]|$)/i;

function fenceContent(line: string): string | null {
  let indent = 0;
  while (indent < line.length && indent < 3 && line[indent] === " ") {
    indent += 1;
  }
  if (line[indent] === " ") return null;
  return line.slice(indent);
}

function openingFence(line: string): Fence | null {
  const content = fenceContent(line);
  if (content === null) return null;
  const match = content.match(/^([`~]{3,})([^\r\n]*)$/);
  if (!match) return null;
  const marker = match[1]![0] as "`" | "~";
  if (!match[1]!.split("").every((char) => char === marker)) return null;
  return {
    marker,
    length: match[1]!.length,
    info: match[2]!.trim(),
  };
}

function closesFence(line: string, fence: Fence): boolean {
  const content = fenceContent(line);
  if (content === null) return false;
  let count = 0;
  while (count < content.length && content[count] === fence.marker) {
    count += 1;
  }
  if (count < fence.length) return false;
  return content.slice(count).trim() === "";
}

function closingFenceIndex(lines: string[], openIndex: number, fence: Fence): number {
  for (let i = openIndex + 1; i < lines.length; i += 1) {
    if (closesFence(lines[i]!, fence)) return i;
  }
  return -1;
}

function pushMarkdownBlock(blocks: MarkdownSuggestionBlock[], text: string): void {
  if (text.length === 0) return;
  blocks.push({
    type: "markdown",
    key: `markdown-${blocks.length}`,
    text,
  });
}

export function parseMarkdownSuggestions(raw: string): MarkdownSuggestionBlock[] {
  if (raw === "") return [];

  const lines = raw.replace(/\r\n/g, "\n").split("\n");
  const blocks: MarkdownSuggestionBlock[] = [];
  let markdownStart = 0;
  let lineIndex = 0;

  while (lineIndex < lines.length) {
    const fence = openingFence(lines[lineIndex]!);
    if (!fence) {
      lineIndex += 1;
      continue;
    }

    const closeIndex = closingFenceIndex(lines, lineIndex, fence);
    if (closeIndex === -1) break;
    if (!suggestionInfo.test(fence.info)) {
      lineIndex = closeIndex + 1;
      continue;
    }

    pushMarkdownBlock(
      blocks,
      lines.slice(markdownStart, lineIndex).join("\n") + (lineIndex > markdownStart ? "\n" : ""),
    );
    blocks.push({
      type: "suggestion",
      key: `suggestion-${blocks.length}`,
      replacement: lines.slice(lineIndex + 1, closeIndex).join("\n"),
      fenceLine: lineIndex + 1,
    });
    lineIndex = closeIndex + 1;
    markdownStart = lineIndex;
  }

  pushMarkdownBlock(blocks, lines.slice(markdownStart).join("\n"));
  return blocks;
}

function targetLines(context: ReviewThreadContext): ReviewThreadContextLine[] {
  return context.lines.filter((line) => line.target);
}

function originalLineNumber(line: ReviewThreadContextLine): number | undefined {
  return line.newNum ?? line.oldNum;
}

function contextLineNumber(line: ReviewThreadContextLine, delta: number): number | undefined {
  const num = originalLineNumber(line);
  return num === undefined ? undefined : num + delta;
}

function suggestionLines(replacement: string): string[] {
  if (replacement === "") return [];
  const normalized = replacement.replace(/\r\n/g, "\n");
  return normalized.split("\n");
}

function patchLine(line: DiffLine): string {
  switch (line.type) {
    case "add":
      return `+${line.content}`;
    case "delete":
      return `-${line.content}`;
    default:
      return ` ${line.content}`;
  }
}

function patchFor(file: DiffFile): string {
  const lines = file.hunks.flatMap((hunk) => [
    `@@ -${hunk.old_start},${hunk.old_count} +${hunk.new_start},${hunk.new_count} @@ ${hunk.section ?? ""}`.trimEnd(),
    ...hunk.lines.map(patchLine),
  ]);
  return lines.join("\n");
}

export function buildSuggestionDiffFile(
  thread: ReviewThread,
  context: ReviewThreadContext,
  replacement: string,
): DiffFile {
  const targets = targetLines(context);
  const replacementLines = suggestionLines(replacement);
  const replacementDelta = replacementLines.length - targets.length;
  const before = context.lines.filter(
    (line) => !line.target && (originalLineNumber(line) ?? 0) < reviewThreadStartLine(thread),
  );
  const after = context.lines.filter(
    (line) => !line.target && (originalLineNumber(line) ?? 0) > reviewThreadTargetLine(thread),
  );
  const oldStart = before[0]
    ? (originalLineNumber(before[0]) ?? reviewThreadStartLine(thread))
    : reviewThreadStartLine(thread);
  const newStart = oldStart;
  const diffLines: DiffLine[] = [
    ...before.map<DiffLine>((line) => {
      const num = originalLineNumber(line);
      return {
        type: "context",
        ...(num !== undefined ? { old_num: num, new_num: num } : {}),
        content: line.content,
      };
    }),
    ...targets.map<DiffLine>((line) => {
      const num = originalLineNumber(line);
      return {
        type: "delete",
        ...(num !== undefined ? { old_num: num } : {}),
        content: line.content,
      };
    }),
    ...replacementLines.map<DiffLine>((content, index) => ({
      type: "add",
      new_num: reviewThreadStartLine(thread) + index,
      content,
    })),
    ...after.map<DiffLine>((line) => {
      const oldNum = originalLineNumber(line);
      const newNum = contextLineNumber(line, replacementDelta);
      return {
        type: "context",
        ...(oldNum !== undefined ? { old_num: oldNum } : {}),
        ...(newNum !== undefined ? { new_num: newNum } : {}),
        content: line.content,
      };
    }),
  ];
  const oldCount = diffLines.filter((line) => line.type !== "add").length;
  const newCount = diffLines.filter((line) => line.type !== "delete").length;
  const additions = replacementLines.length;
  const deletions = targets.length;
  const file: DiffFile = {
    path: context.path || thread.path,
    old_path: thread.old_path || context.path || thread.path,
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions,
    deletions,
    patch: "",
    hunks: [
      {
        old_start: oldStart,
        old_count: oldCount,
        new_start: newStart,
        new_count: newCount,
        section: "Suggested change",
        lines: diffLines,
      },
    ],
  };
  file.patch = patchFor(file);
  return file;
}
