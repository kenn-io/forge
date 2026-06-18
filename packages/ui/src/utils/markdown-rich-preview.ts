import type { DiffFile } from "../api/types.js";
import { renderMarkdownBlocks, type RenderedMarkdownBlock, type RepoContext } from "./markdown.js";
import { renderMarkdownDiff, renderMarkdownSplitDiff } from "./markdown-diff.js";

type SourceLine = DiffFile["hunks"][number]["lines"][number];
type DiffOp<T> =
  | { kind: "equal"; oldItem: T; newItem: T }
  | { kind: "delete"; oldItem: T }
  | { kind: "insert"; newItem: T };

const MAX_BLOCK_COMPARISON_SIZE = 20_000;

interface MarkdownSideDocument {
  text: string;
  lineMap: Array<number | undefined>;
}

interface MarkdownSideBlock extends RenderedMarkdownBlock {
  sourceStart?: number | undefined;
  sourceEnd?: number | undefined;
}

export interface MarkdownRichPreviewBlock {
  key: string;
  oldStart?: number | undefined;
  oldEnd?: number | undefined;
  newStart?: number | undefined;
  newEnd?: number | undefined;
  unifiedHtml: string;
  beforeHtml?: string | undefined;
  afterHtml?: string | undefined;
}

export interface MarkdownRichPreview {
  blocks: MarkdownRichPreviewBlock[];
}

export function buildMarkdownRichPreview(source: DiffFile, repo: RepoContext): MarkdownRichPreview {
  const oldBlocks = buildSideBlocks(buildSideDocument(source, "old"), repo);
  const newBlocks = buildSideBlocks(buildSideDocument(source, "new"), repo);
  return { blocks: alignBlocks(oldBlocks, newBlocks) };
}

function buildSideDocument(source: DiffFile, side: "old" | "new"): MarkdownSideDocument {
  const lines: Array<{ content: string; sourceLine?: number | undefined }> = [];
  for (const hunk of source.hunks) {
    if (lines.length > 0) {
      lines.push({ content: "" }, { content: "---" }, { content: "" });
    }
    for (const line of hunk.lines) {
      const sourceLine = side === "old" ? line.old_num : line.new_num;
      if (sideIncludesLine(side, line)) lines.push({ content: line.content, sourceLine });
    }
  }
  return {
    text: `${lines.map((line) => line.content).join("\n")}\n`,
    lineMap: lines.map((line) => line.sourceLine),
  };
}

function sideIncludesLine(side: "old" | "new", line: SourceLine): boolean {
  return side === "old" ? line.type !== "add" : line.type !== "delete";
}

function buildSideBlocks(document: MarkdownSideDocument, repo: RepoContext): MarkdownSideBlock[] {
  return renderMarkdownBlocks(document.text, repo)
    .map((block) => {
      const range = sourceRangeForBlock(block, document.lineMap);
      return {
        ...block,
        sourceStart: range.start,
        sourceEnd: range.end,
      };
    })
    .filter((block) => block.sourceStart != null || block.sourceEnd != null);
}

function sourceRangeForBlock(
  block: RenderedMarkdownBlock,
  lineMap: Array<number | undefined>,
): { start?: number | undefined; end?: number | undefined } {
  const sourceLines = lineMap.slice(block.startLine - 1, block.endLine).filter((line): line is number => line != null);
  return {
    start: sourceLines[0],
    end: sourceLines.at(-1),
  };
}

function alignBlocks(oldBlocks: MarkdownSideBlock[], newBlocks: MarkdownSideBlock[]): MarkdownRichPreviewBlock[] {
  if (oldBlocks.length * newBlocks.length > MAX_BLOCK_COMPARISON_SIZE) {
    return renderCoarseBlocks(oldBlocks, newBlocks);
  }
  const ops = diffSequence(oldBlocks, newBlocks, (oldBlock, newBlock) => oldBlock.html === newBlock.html);
  const blocks: MarkdownRichPreviewBlock[] = [];
  for (let i = 0; i < ops.length; i++) {
    const op = ops[i]!;
    if (op.kind === "equal") {
      blocks.push(renderBlock(blocks.length, op.oldItem, op.newItem));
      continue;
    }
    if (op.kind === "insert") {
      blocks.push(renderBlock(blocks.length, undefined, op.newItem));
      continue;
    }

    const deleteRun: MarkdownSideBlock[] = [];
    while (ops[i]?.kind === "delete") {
      deleteRun.push((ops[i] as Extract<DiffOp<MarkdownSideBlock>, { kind: "delete" }>).oldItem);
      i++;
    }

    const insertRun: MarkdownSideBlock[] = [];
    while (ops[i]?.kind === "insert") {
      insertRun.push((ops[i] as Extract<DiffOp<MarkdownSideBlock>, { kind: "insert" }>).newItem);
      i++;
    }
    i--;

    const pairs = Math.min(deleteRun.length, insertRun.length);
    for (let pair = 0; pair < pairs; pair++) {
      blocks.push(renderBlock(blocks.length, deleteRun[pair], insertRun[pair]));
    }
    for (let index = pairs; index < deleteRun.length; index++) {
      blocks.push(renderBlock(blocks.length, deleteRun[index], undefined));
    }
    for (let index = pairs; index < insertRun.length; index++) {
      blocks.push(renderBlock(blocks.length, undefined, insertRun[index]));
    }
  }
  return blocks;
}

function renderCoarseBlocks(
  oldBlocks: MarkdownSideBlock[],
  newBlocks: MarkdownSideBlock[],
): MarkdownRichPreviewBlock[] {
  const blocks: MarkdownRichPreviewBlock[] = [];
  for (const oldBlock of oldBlocks) {
    blocks.push(renderBlock(blocks.length, oldBlock, undefined));
  }
  for (const newBlock of newBlocks) {
    blocks.push(renderBlock(blocks.length, undefined, newBlock));
  }
  return blocks;
}

function renderBlock(
  index: number,
  oldBlock: MarkdownSideBlock | undefined,
  newBlock: MarkdownSideBlock | undefined,
): MarkdownRichPreviewBlock {
  const oldHtml = oldBlock?.html ?? "";
  const newHtml = newBlock?.html ?? "";
  const split = renderMarkdownSplitDiff(oldHtml, newHtml);
  return {
    key: `${index}:${oldBlock?.key ?? ""}:${newBlock?.key ?? ""}`,
    oldStart: oldBlock?.sourceStart,
    oldEnd: oldBlock?.sourceEnd,
    newStart: newBlock?.sourceStart,
    newEnd: newBlock?.sourceEnd,
    unifiedHtml: renderMarkdownDiff(oldHtml, newHtml),
    beforeHtml: split.beforeHtml,
    afterHtml: split.afterHtml,
  };
}

function diffSequence<T>(
  oldItems: readonly T[],
  newItems: readonly T[],
  equal: (left: T, right: T) => boolean,
): DiffOp<T>[] {
  const rows = oldItems.length + 1;
  const cols = newItems.length + 1;
  const lengths = Array.from({ length: rows }, () => Array<number>(cols).fill(0));
  for (let i = oldItems.length - 1; i >= 0; i--) {
    for (let j = newItems.length - 1; j >= 0; j--) {
      lengths[i]![j] = equal(oldItems[i]!, newItems[j]!)
        ? lengths[i + 1]![j + 1]! + 1
        : Math.max(lengths[i + 1]![j]!, lengths[i]![j + 1]!);
    }
  }

  const ops: DiffOp<T>[] = [];
  let i = 0;
  let j = 0;
  while (i < oldItems.length && j < newItems.length) {
    if (equal(oldItems[i]!, newItems[j]!)) {
      ops.push({ kind: "equal", oldItem: oldItems[i]!, newItem: newItems[j]! });
      i++;
      j++;
    } else if (lengths[i + 1]![j]! >= lengths[i]![j + 1]!) {
      ops.push({ kind: "delete", oldItem: oldItems[i]! });
      i++;
    } else {
      ops.push({ kind: "insert", newItem: newItems[j]! });
      j++;
    }
  }
  while (i < oldItems.length) {
    ops.push({ kind: "delete", oldItem: oldItems[i]! });
    i++;
  }
  while (j < newItems.length) {
    ops.push({ kind: "insert", newItem: newItems[j]! });
    j++;
  }
  return ops;
}
