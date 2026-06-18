import type { DiffFile } from "../api/types.js";
import {
  extractMarkdownDefinitionLines,
  renderMarkdownBlocks,
  type RenderedMarkdownBlock,
  type RepoContext,
} from "./markdown.js";
import { renderMarkdownDiff, renderMarkdownSplitDiff } from "./markdown-diff.js";

type SourceLine = DiffFile["hunks"][number]["lines"][number];
type DiffOp<T> =
  | { kind: "equal"; oldItem: T; newItem: T }
  | { kind: "delete"; oldItem: T }
  | { kind: "insert"; newItem: T };

const MAX_BLOCK_COMPARISON_SIZE = 20_000;

interface MarkdownSideDocument {
  text: string;
  lines: string[];
  lineMap: Array<number | undefined>;
}

interface MarkdownSideBlock extends RenderedMarkdownBlock {
  sourceStart?: number | undefined;
  sourceEnd?: number | undefined;
  sourceLines: number[];
}

export interface MarkdownRichPreviewBlock {
  key: string;
  oldStart?: number | undefined;
  oldEnd?: number | undefined;
  oldLines?: number[] | undefined;
  newStart?: number | undefined;
  newEnd?: number | undefined;
  newLines?: number[] | undefined;
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
    lines: lines.map((line) => line.content),
    lineMap: lines.map((line) => line.sourceLine),
  };
}

function sideIncludesLine(side: "old" | "new", line: SourceLine): boolean {
  return side === "old" ? line.type !== "add" : line.type !== "delete";
}

function buildSideBlocks(document: MarkdownSideDocument, repo: RepoContext): MarkdownSideBlock[] {
  const definitionLines = extractMarkdownDefinitionLines(document.text, repo);
  return renderMarkdownBlocks(document.text, repo).flatMap((block) =>
    renderedSideBlocks(block, document, repo, definitionLines),
  );
}

function renderedSideBlocks(
  block: RenderedMarkdownBlock,
  document: MarkdownSideDocument,
  repo: RepoContext,
  definitionLines: string[],
): MarkdownSideBlock[] {
  const blockLineMap = document.lineMap.slice(block.startLine - 1, block.endLine);
  if (blockLineMap.every((line) => line != null)) return sideBlocksForRenderedBlock(block, document);

  const lines = document.lines
    .slice(block.startLine - 1, block.endLine)
    .map((content, index) => ({ content, sourceLine: blockLineMap[index] }))
    .filter((line): line is { content: string; sourceLine: number } => line.sourceLine != null);
  if (lines.length === 0) return [];

  const visibleDocument = buildVisibleDocument(lines, definitionLines);
  return renderMarkdownBlocks(visibleDocument.text, repo)
    .map((visibleBlock) =>
      sideBlocksForRenderedBlock({ ...visibleBlock, key: `${block.key}:${visibleBlock.key}` }, visibleDocument),
    )
    .flat();
}

function sideBlocksForRenderedBlock(block: RenderedMarkdownBlock, document: MarkdownSideDocument): MarkdownSideBlock[] {
  return splitListBlock(block, document) ?? [sideBlockForRenderedBlock(block, document.lineMap)];
}

interface ListItemLineGroup {
  startLine: number;
  endLine: number;
  sourceLines: number[];
}

function splitListBlock(block: RenderedMarkdownBlock, document: MarkdownSideDocument): MarkdownSideBlock[] | null {
  if (typeof globalThis.document === "undefined") return null;
  const template = globalThis.document.createElement("template");
  template.innerHTML = block.html;
  const rootElements = Array.from(template.content.children);
  if (rootElements.length !== 1) return null;

  const list = rootElements[0]!;
  if (list.tagName !== "UL" && list.tagName !== "OL") return null;
  const items = Array.from(list.children).filter((child) => child.tagName === "LI");
  if (items.length < 2 || items.length !== list.children.length) return null;

  const groups = listItemLineGroups(block, document);
  if (groups.length !== items.length) return null;

  return groups.map((group, index) => {
    const html = listItemHtml(list, items[index]!, index);
    return {
      ...block,
      key: `${block.key}:item:${index}`,
      startLine: group.startLine,
      endLine: group.endLine,
      html,
      sourceLines: group.sourceLines,
      sourceStart: group.sourceLines[0],
      sourceEnd: group.sourceLines.at(-1),
    };
  });
}

function listItemLineGroups(block: RenderedMarkdownBlock, document: MarkdownSideDocument): ListItemLineGroup[] {
  const startIndex = block.startLine - 1;
  const endIndex = block.endLine - 1;
  const markerLines: Array<{ index: number; indent: number }> = [];
  for (let index = startIndex; index <= endIndex; index++) {
    const marker = listMarkerIndent(document.lines[index] ?? "");
    if (marker == null) continue;
    markerLines.push({ index, indent: marker });
  }
  if (markerLines.length < 2) return [];

  const topLevelIndent = Math.min(...markerLines.map((line) => line.indent));
  const topLevelMarkers = markerLines.filter((line) => line.indent === topLevelIndent);
  return topLevelMarkers.map((line, index) => {
    const next = topLevelMarkers[index + 1];
    const groupStart = line.index;
    const groupEnd = (next?.index ?? endIndex + 1) - 1;
    const sourceLines = document.lineMap
      .slice(groupStart, groupEnd + 1)
      .filter((sourceLine): sourceLine is number => sourceLine != null);
    return {
      startLine: groupStart + 1,
      endLine: groupEnd + 1,
      sourceLines,
    };
  });
}

function listMarkerIndent(line: string): number | null {
  const match = line.match(/^(\s{0,12})(?:[-+*]|\d+[.)])\s+/);
  if (!match) return null;
  return indentationWidth(match[1]!);
}

function indentationWidth(value: string): number {
  return Array.from(value).reduce((width, char) => width + (char === "\t" ? 4 : 1), 0);
}

function listItemHtml(list: Element, item: Element, index: number): string {
  const wrapper = list.cloneNode(false) as Element;
  wrapper.classList.add("markdown-rich-diff__split-list");
  if (wrapper.tagName === "OL") {
    const start = parseInt(list.getAttribute("start") ?? "1", 10);
    if (!Number.isNaN(start)) wrapper.setAttribute("start", String(start + index));
  }
  wrapper.append(item.cloneNode(true));
  return wrapper.outerHTML;
}

function buildVisibleDocument(
  lines: Array<{ content: string; sourceLine: number }>,
  definitionLines: string[],
): MarkdownSideDocument {
  const sourceLines = lines.map((line) => line.content);
  const parserContextLines = definitionLines.length > 0 ? ["", ...definitionLines] : [];
  return {
    text: `${[...sourceLines, ...parserContextLines].join("\n")}\n`,
    lines: [...sourceLines, ...parserContextLines],
    lineMap: [...lines.map((line) => line.sourceLine), ...parserContextLines.map(() => undefined)],
  };
}

function sideBlockForRenderedBlock(
  block: RenderedMarkdownBlock,
  lineMap: Array<number | undefined>,
): MarkdownSideBlock {
  const sourceLines = sourceLinesForBlock(block, lineMap);
  return {
    ...block,
    sourceLines,
    sourceStart: sourceLines[0],
    sourceEnd: sourceLines.at(-1),
  };
}

function sourceLinesForBlock(block: RenderedMarkdownBlock, lineMap: Array<number | undefined>): number[] {
  return lineMap.slice(block.startLine - 1, block.endLine).filter((line): line is number => line != null);
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
    oldLines: oldBlock?.sourceLines,
    newStart: newBlock?.sourceStart,
    newEnd: newBlock?.sourceEnd,
    newLines: newBlock?.sourceLines,
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
