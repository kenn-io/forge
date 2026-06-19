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

export interface MarkdownRichPreviewOptions {
  splitOldLines?: readonly number[] | undefined;
  splitNewLines?: readonly number[] | undefined;
}

export function buildMarkdownRichPreview(
  source: DiffFile,
  repo: RepoContext,
  options: MarkdownRichPreviewOptions = {},
): MarkdownRichPreview {
  const oldBlocks = buildSideBlocks(buildSideDocument(source, "old"), repo, new Set(options.splitOldLines ?? []));
  const newBlocks = buildSideBlocks(buildSideDocument(source, "new"), repo, new Set(options.splitNewLines ?? []));
  return { blocks: alignBlocks(oldBlocks, newBlocks) };
}

function buildSideDocument(source: DiffFile, side: "old" | "new"): MarkdownSideDocument {
  const lines: Array<{ content: string; sourceLine?: number | undefined }> = [];
  for (const hunk of source.hunks ?? []) {
    if (lines.length > 0) {
      lines.push({ content: "" }, { content: "---" }, { content: "" });
    }
    for (const line of hunk.lines ?? []) {
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

function buildSideBlocks(
  document: MarkdownSideDocument,
  repo: RepoContext,
  splitLines: ReadonlySet<number>,
): MarkdownSideBlock[] {
  const definitionLines = extractMarkdownDefinitionLines(document.text, repo);
  return renderMarkdownBlocks(document.text, repo).flatMap((block) =>
    renderedSideBlocks(block, document, repo, definitionLines, splitLines),
  );
}

function renderedSideBlocks(
  block: RenderedMarkdownBlock,
  document: MarkdownSideDocument,
  repo: RepoContext,
  definitionLines: string[],
  splitLines: ReadonlySet<number>,
): MarkdownSideBlock[] {
  const blockLineMap = document.lineMap.slice(block.startLine - 1, block.endLine);
  if (blockLineMap.every((line) => line != null)) return sideBlocksForRenderedBlock(block, document, splitLines);

  const lines = document.lines
    .slice(block.startLine - 1, block.endLine)
    .map((content, index) => ({ content, sourceLine: blockLineMap[index] }))
    .filter((line): line is { content: string; sourceLine: number } => line.sourceLine != null);
  if (lines.length === 0) return [];

  const visibleDocument = buildVisibleDocument(lines, definitionLines);
  return renderMarkdownBlocks(visibleDocument.text, repo)
    .map((visibleBlock) =>
      sideBlocksForRenderedBlock(
        { ...visibleBlock, key: `${block.key}:${visibleBlock.key}` },
        visibleDocument,
        splitLines,
      ),
    )
    .flat();
}

function sideBlocksForRenderedBlock(
  block: RenderedMarkdownBlock,
  document: MarkdownSideDocument,
  splitLines: ReadonlySet<number>,
): MarkdownSideBlock[] {
  const sourceLines = sourceLinesForBlock(block, document.lineMap);
  if (sourceLines.some((line) => splitLines.has(line))) {
    return splitListBlock(block, document, splitLines) ?? [sideBlockForRenderedBlock(block, document.lineMap)];
  }
  return [sideBlockForRenderedBlock(block, document.lineMap)];
}

interface ListItemLineGroup {
  startLine: number;
  endLine: number;
  sourceLines: number[];
}

function splitListBlock(
  block: RenderedMarkdownBlock,
  document: MarkdownSideDocument,
  splitLines: ReadonlySet<number>,
): MarkdownSideBlock[] | null {
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

  const breakAfterIndexes = groups
    .map((group, index) => ({ group, index }))
    .filter(({ group }) => group.sourceLines.some((line) => splitLines.has(line)))
    .map(({ index }) => index);
  if (breakAfterIndexes.length === 0) return null;

  const segments: Array<{ startIndex: number; endIndex: number; groups: ListItemLineGroup[] }> = [];
  let startIndex = 0;
  for (const breakAfterIndex of breakAfterIndexes) {
    segments.push({
      startIndex,
      endIndex: breakAfterIndex,
      groups: groups.slice(startIndex, breakAfterIndex + 1),
    });
    startIndex = breakAfterIndex + 1;
  }
  if (startIndex < groups.length) {
    segments.push({
      startIndex,
      endIndex: groups.length - 1,
      groups: groups.slice(startIndex),
    });
  }

  return segments.map((segment) => {
    const sourceLines = segment.groups.flatMap((group) => group.sourceLines);
    const html = listSegmentHtml(list, items, segment.startIndex, segment.endIndex);
    return {
      ...block,
      key: `${block.key}:items:${segment.startIndex}-${segment.endIndex}`,
      startLine: segment.groups[0]!.startLine,
      endLine: segment.groups.at(-1)!.endLine,
      html,
      sourceLines,
      sourceStart: sourceLines[0],
      sourceEnd: sourceLines.at(-1),
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

function listSegmentHtml(list: Element, items: Element[], startIndex: number, endIndex: number): string {
  const wrapper = list.cloneNode(false) as Element;
  wrapper.classList.add("markdown-rich-diff__split-list");
  if (wrapper.tagName === "OL") {
    const start = parseInt(list.getAttribute("start") ?? "1", 10);
    if (!Number.isNaN(start)) wrapper.setAttribute("start", String(start + startIndex));
  }
  for (let index = startIndex; index <= endIndex; index++) {
    wrapper.append(items[index]!.cloneNode(true));
  }
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
  const ops = diffSequence(oldBlocks, newBlocks, blocksAlign);
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

function blocksAlign(oldBlock: MarkdownSideBlock, newBlock: MarkdownSideBlock): boolean {
  return alignmentHtml(oldBlock.html) === alignmentHtml(newBlock.html);
}

function alignmentHtml(html: string): string {
  return html.replace(/<ol\b([^>]*)>/g, (tag: string, attributes: string) => {
    if (!attributes.includes("markdown-rich-diff__split-list")) return tag;
    return `<ol${attributes.replace(/\sstart="\d+"/, "")}>`;
  });
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
  const aligned = oldBlock && newBlock && blocksAlign(oldBlock, newBlock);
  const split = aligned ? { beforeHtml: oldHtml, afterHtml: newHtml } : renderMarkdownSplitDiff(oldHtml, newHtml);
  return {
    key: `${index}:${oldBlock?.key ?? ""}:${newBlock?.key ?? ""}`,
    oldStart: oldBlock?.sourceStart,
    oldEnd: oldBlock?.sourceEnd,
    oldLines: oldBlock?.sourceLines,
    newStart: newBlock?.sourceStart,
    newEnd: newBlock?.sourceEnd,
    newLines: newBlock?.sourceLines,
    unifiedHtml: aligned ? newHtml : renderMarkdownDiff(oldHtml, newHtml),
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
