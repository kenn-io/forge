// GFM task list helpers. Mirrors the same recognition rules the marked
// renderer applies — see RenderMarkdownOpts.interactiveTasks — so that
// data-task-index attributes emitted there line up with the source-
// position-based toggling here.

// A line is a task-list item when it begins with optional leading
// whitespace, a bullet (-, *, +) or ordered marker (1.), at least one
// space, the checkbox token `[ ]`, `[x]`, or `[X]`, AND a whitespace
// character (or end-of-line) after the closing bracket. Marked's
// renderer applies the same trailing-whitespace rule — without it
// `- [ ]text` lexes as plain prose, so the source helpers must
// agree or data-task-index would drift. Anything inside a fenced
// code block is ignored so `[ ]` shown in code samples doesn't
// shift indices.
// CommonMark accepts both `1.` and `1)` as ordered-list markers, and
// marked renders either shape as a task-list item — accept both here
// or `1) [ ]` lexes as a task on render but plain prose in source,
// drifting data-task-index out of sync.
const TASK_LINE = /^([\t ]*(?:[-*+]|\d+[.)])[\t ]+\[)([ xX])(\])(?:[\t ]|$)/;
const BULLET_LINE = /^[\t ]*(?:[-*+]|\d+[.)])[\t ]+/;
// Fenced code block opener: 3+ matching characters (` or ~) with
// optional leading whitespace. Captures the character and run so the
// close fence can be validated against the same marker and length per
// CommonMark — e.g. a four-backtick fence can contain a literal
// three-backtick line without ending the block.
const FENCE_LINE = /^[\t ]*(`{3,}|~{3,})/;

export interface TaskItem {
  // Zero-based index of this task in document order.
  index: number;
  // Whether the checkbox is currently checked.
  checked: boolean;
  // Line number (zero-based) where the checkbox lives.
  line: number;
}

function leadingWhitespaceCount(line: string): number {
  let i = 0;
  while (i < line.length && (line[i] === " " || line[i] === "\t")) i++;
  return i;
}

// CommonMark indented code block start: 4+ leading spaces or a leading
// tab, at a position where a code block can begin (i.e. outside a list
// context). Inside a list, the same indentation continues the list
// item rather than starting a code block.
function isIndentedCodeStart(line: string): boolean {
  if (line.startsWith("\t")) return true;
  if (line.startsWith("    ")) return true;
  return false;
}

type TaskLineVisitor = (match: RegExpMatchArray, lineIndex: number, taskIndex: number) => void;

// Walks `lines` and invokes `visitor` for every task-list line that
// the markdown renderer would treat as such. Skips lines inside
// fenced code blocks and top-level indented code blocks. List
// context is tracked so an indented task line under a parent bullet
// is recognized as a nested task rather than a code-block line.
function walkTaskLines(lines: string[], visitor: TaskLineVisitor): void {
  // `openFence` is null outside a fenced code block; while inside,
  // it records the opener's marker character and minimum length, so
  // the close fence has to match (same char, at least as long).
  let openFence: { char: string; length: number } | null = null;
  let listIndent: number | null = null;
  let taskIndex = 0;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    // At top level (outside a list), a line starting with 4 spaces or
    // a tab is an indented code block per CommonMark — it can't open
    // a fence, even if the rest of the line is `` ``` ``. Skip it
    // before fence detection so the `` ``` `` inside doesn't open a
    // bogus block and shift downstream task indices. Inside a fenced
    // block (openFence !== null) we still need to fall through so a
    // matching close-fence line can end the block; inside a list, the
    // same indent continues the list item and may legitimately open a
    // fence as part of the item's content.
    if (openFence === null && listIndent === null && isIndentedCodeStart(line)) {
      continue;
    }
    const fenceMatch = line.match(FENCE_LINE);
    if (fenceMatch) {
      const run = fenceMatch[1]!;
      const char = run[0]!;
      if (openFence === null) {
        openFence = { char, length: run.length };
        continue;
      }
      if (char === openFence.char && run.length >= openFence.length) {
        openFence = null;
        // A fenced block at the same indent as the list bullet ends
        // the list — Markdown treats the fence as block-level content.
        if (listIndent !== null && leadingWhitespaceCount(line) <= listIndent) {
          listIndent = null;
        }
        continue;
      }
      // Mismatched marker or shorter run: this line stays inside the
      // current code block as plain content.
      continue;
    }
    if (openFence !== null) continue;
    if (line.trim() === "") continue;

    const indent = leadingWhitespaceCount(line);

    // Dedent out of the list when a non-blank line sits at or below
    // the list bullet indent and isn't itself a bullet at that level.
    if (listIndent !== null && indent < listIndent) {
      listIndent = null;
    }

    const taskMatch = line.match(TASK_LINE);
    if (taskMatch) {
      visitor(taskMatch, i, taskIndex++);
      if (listIndent === null || indent < listIndent) {
        listIndent = indent;
      }
      continue;
    }

    // Non-task bullet still establishes/preserves list context so
    // a subsequent indented task line is recognized as nested.
    if (BULLET_LINE.test(line)) {
      if (listIndent === null) listIndent = indent;
      continue;
    }

    // Non-bullet line at or below list indent terminates the list.
    if (listIndent !== null && indent <= listIndent) {
      listIndent = null;
    }
  }
}

export function listTaskItems(source: string): TaskItem[] {
  if (!source) return [];
  const out: TaskItem[] = [];
  walkTaskLines(source.split("\n"), (m, lineIndex, taskIndex) => {
    out.push({
      index: taskIndex,
      checked: m[2] !== " ",
      line: lineIndex,
    });
  });
  return out;
}

// A task block spans the task line plus any immediately-following
// continuation lines that belong to the same item — more-indented
// content (multi-line description, nested sub-tasks) is carried
// along with the task when it moves. Blank lines do NOT terminate
// the block on their own: Markdown allows blank-separated
// paragraphs that remain part of a list item as long as the next
// non-blank line is still indented past the bullet. The block ends
// at the first non-blank line that sits at or below the bullet's
// indentation.
function findTaskBlockEnd(lines: string[], start: number): number {
  const bulletIndent = leadingWhitespaceCount(lines[start]!);
  let end = start + 1;
  let pendingBlankRun = 0;
  while (end < lines.length) {
    const line = lines[end]!;
    if (line.trim() === "") {
      pendingBlankRun++;
      end++;
      continue;
    }
    if (leadingWhitespaceCount(line) <= bulletIndent) {
      // The next non-blank line dedents out of the item — strip
      // the buffered blank lines, they belong to the separator
      // between this item and what follows.
      return end - pendingBlankRun;
    }
    // Indented content continues the block, including any blank
    // lines we buffered on the way here.
    pendingBlankRun = 0;
    end++;
  }
  // Reached EOF; drop trailing blanks from the block.
  return end - pendingBlankRun;
}

// Returns a new source string with the Nth task-list item moved to
// the position currently occupied by the Mth task-list item. The
// moved item carries its continuation lines and nested sub-tasks
// with it. If either index is out of range, or they're equal, the
// source is returned unchanged.
export function moveTaskListItem(source: string, fromIndex: number, toIndex: number): string {
  if (!source) return source;
  if (fromIndex === toIndex) return source;
  if (fromIndex < 0 || toIndex < 0) return source;
  const lines = source.split("\n");
  const taskLines: number[] = [];
  walkTaskLines(lines, (_m, lineIndex) => {
    taskLines.push(lineIndex);
  });
  if (fromIndex >= taskLines.length) return source;
  if (toIndex >= taskLines.length) return source;
  const fromStart = taskLines[fromIndex]!;
  const fromEnd = findTaskBlockEnd(lines, fromStart);
  const toStart = taskLines[toIndex]!;
  const toEnd = findTaskBlockEnd(lines, toStart);
  // Refuse cross-hierarchy moves: dropping a nested item onto a
  // top-level sibling (or vice versa) would silently change the
  // markdown structure — the moved block keeps its original indent
  // and would reparent or orphan itself. Only same-indent moves
  // (true siblings under the same parent list) are allowed.
  if (leadingWhitespaceCount(lines[fromStart]!) !== leadingWhitespaceCount(lines[toStart]!)) {
    return source;
  }
  // Refuse no-ops: dragging a task onto something inside its own
  // block would either be a no-op or self-overlap, both of which we
  // pass through unchanged.
  if (toStart >= fromStart && toStart < fromEnd) return source;
  const moved = lines.slice(fromStart, fromEnd);
  const without = [...lines.slice(0, fromStart), ...lines.slice(fromEnd)];
  // Where to insert depends on direction: moving down lands the block
  // where the target block ended (minus the removed block's length);
  // moving up lands it where the target block started.
  let insertAt: number;
  if (fromIndex < toIndex) {
    insertAt = toEnd - (fromEnd - fromStart);
  } else {
    insertAt = toStart;
  }
  return [...without.slice(0, insertAt), ...moved, ...without.slice(insertAt)].join("\n");
}

// Returns a new source string with the Nth task-list checkbox toggled.
// If `index` is out of range, the source is returned unchanged.
export function toggleTaskListItem(source: string, index: number): string {
  if (!source) return source;
  if (index < 0) return source;
  const lines = source.split("\n");
  let result = source;
  let mutated = false;
  walkTaskLines(lines, (m, lineIndex, taskIndex) => {
    if (mutated) return;
    if (taskIndex !== index) return;
    const prefix = m[1]!;
    const ch = m[2]!;
    const next = ch === " " ? "x" : " ";
    const original = lines[lineIndex]!;
    lines[lineIndex] = `${prefix}${next}${original.slice(prefix.length + 1)}`;
    result = lines.join("\n");
    mutated = true;
  });
  return result;
}
