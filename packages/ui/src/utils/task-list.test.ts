import { describe, expect, it } from "vite-plus/test";
import { listTaskItems, moveTaskListItem, toggleTaskListItem } from "./task-list.js";

describe("listTaskItems", () => {
  it("returns an empty list for empty input", () => {
    expect(listTaskItems("")).toEqual([]);
  });

  it("returns an empty list when there are no tasks", () => {
    expect(listTaskItems("plain prose\nno tasks here")).toEqual([]);
  });

  it("captures index, checked state, and line number in document order", () => {
    const src = [
      "intro line",
      "- [ ] first",
      "  text",
      "- [x] second",
      "* [X] third (uppercase)",
      "1. [ ] fourth (ordered)",
      "2) [x] fifth (ordered with paren)",
    ].join("\n");
    expect(listTaskItems(src)).toEqual([
      { index: 0, checked: false, line: 1 },
      { index: 1, checked: true, line: 3 },
      { index: 2, checked: true, line: 4 },
      { index: 3, checked: false, line: 5 },
      { index: 4, checked: true, line: 6 },
    ]);
  });

  it("keeps a longer fence open across shorter inner fences", () => {
    // A 4-backtick fence is allowed to contain a literal 3-backtick
    // line; the close fence must match the opener's char and have at
    // least as many backticks. Without this rule, the inner ``` would
    // prematurely close the block and the second `[ ] inside` would
    // wrongly count as a task, shifting indices for the real task.
    const src = ["````", "```", "- [ ] inside fenced block", "```", "````", "- [ ] real task after fence"].join("\n");
    expect(listTaskItems(src)).toEqual([{ index: 0, checked: false, line: 5 }]);
  });

  it("does not treat 4-space-indented fence syntax as a real fence", () => {
    // CommonMark: at top level, 4+ leading spaces means indented code,
    // even if the rest of the line is `` ``` ``. The `` ``` `` inside
    // the indented block must NOT open a fence — if it did, the real
    // task that follows would be hidden inside the bogus fenced
    // block and the index would drift.
    const src = ["    ```", "    - [ ] indented code line", "    ```", "- [ ] real task after indented code"].join(
      "\n",
    );
    expect(listTaskItems(src)).toEqual([{ index: 0, checked: false, line: 3 }]);
  });

  it("does not close a backtick fence with a tilde fence", () => {
    const src = [
      "```",
      "- [ ] inside backtick fence",
      "~~~",
      "- [ ] still inside backtick fence",
      "```",
      "- [ ] real task after fence",
    ].join("\n");
    expect(listTaskItems(src)).toEqual([{ index: 0, checked: false, line: 5 }]);
  });

  it("ignores task-shaped lines inside fenced code blocks", () => {
    const src = ["- [ ] real one", "```", "- [ ] not a task", "- [x] also fenced", "```", "- [x] real two"].join("\n");
    expect(listTaskItems(src)).toEqual([
      { index: 0, checked: false, line: 0 },
      { index: 1, checked: true, line: 5 },
    ]);
  });

  it("handles indented (nested) task list items", () => {
    const src = "- [ ] outer\n  - [x] inner";
    expect(listTaskItems(src)).toEqual([
      { index: 0, checked: false, line: 0 },
      { index: 1, checked: true, line: 1 },
    ]);
  });

  it("skips task-shaped lines inside indented code blocks", () => {
    // Top-level indented (4-space) code block. Marked treats this
    // as a code block, so the inner `- [ ]` is plain text, not a
    // task. listTaskItems must agree.
    const src = ["    - [ ] in indented code block", "    - [x] still in code block", "", "- [ ] real task"].join("\n");
    expect(listTaskItems(src)).toEqual([{ index: 0, checked: false, line: 3 }]);
  });

  it("treats tab-indented task-shaped lines at top level as code", () => {
    const src = "\t- [ ] tab indented\n- [ ] real task";
    expect(listTaskItems(src)).toEqual([{ index: 0, checked: false, line: 1 }]);
  });

  it("rejects checkbox markers without a trailing space (matches marked)", () => {
    // Marked treats `- [ ]nospace` as plain text, not a task — the
    // source helper must agree or data-task-index would drift.
    const src = "- [ ]nospace\n- [ ] withspace";
    expect(listTaskItems(src)).toEqual([{ index: 0, checked: false, line: 1 }]);
  });

  it("keeps nested task items inside a list (not as code blocks)", () => {
    // The 4-space indent here is continuation of the list, NOT
    // a code block — list context preserves the nesting rules.
    const src = "- [ ] outer\n    - [ ] inner";
    expect(listTaskItems(src)).toEqual([
      { index: 0, checked: false, line: 0 },
      { index: 1, checked: false, line: 1 },
    ]);
  });
});

describe("toggleTaskListItem", () => {
  it("flips an unchecked box to checked", () => {
    expect(toggleTaskListItem("- [ ] a", 0)).toBe("- [x] a");
  });

  it("flips a checked box to unchecked", () => {
    expect(toggleTaskListItem("- [x] a", 0)).toBe("- [ ] a");
  });

  it("normalizes uppercase X to space when unchecking", () => {
    expect(toggleTaskListItem("- [X] a", 0)).toBe("- [ ] a");
  });

  it("returns source unchanged when the index is out of range", () => {
    expect(toggleTaskListItem("- [ ] a", 5)).toBe("- [ ] a");
  });

  it("returns source unchanged when the index is negative", () => {
    expect(toggleTaskListItem("- [ ] a", -1)).toBe("- [ ] a");
  });

  it("toggles only the targeted item and leaves others intact", () => {
    const src = "- [ ] first\n- [ ] second\n- [x] third";
    expect(toggleTaskListItem(src, 1)).toBe("- [ ] first\n- [x] second\n- [x] third");
  });

  it("preserves line content after the checkbox", () => {
    const src = "- [ ] item with **bold** and `code`";
    expect(toggleTaskListItem(src, 0)).toBe("- [x] item with **bold** and `code`");
  });

  it("ignores task-shaped lines inside fenced code blocks when counting", () => {
    const src = ["- [ ] outer one", "```", "- [ ] fenced", "```", "- [ ] outer two"].join("\n");
    const out = toggleTaskListItem(src, 1);
    // index 1 is "outer two", not the fenced line
    expect(out).toBe(["- [ ] outer one", "```", "- [ ] fenced", "```", "- [x] outer two"].join("\n"));
  });

  it("supports ordered-list task markers", () => {
    expect(toggleTaskListItem("1. [ ] step one", 0)).toBe("1. [x] step one");
  });

  it("supports ordered-list task markers using ')' (CommonMark)", () => {
    // marked accepts `1)` as an ordered-list marker, so the source
    // helpers must too — otherwise data-task-index would drift.
    expect(toggleTaskListItem("1) [ ] step one", 0)).toBe("1) [x] step one");
  });

  it("does not flip task-shaped lines inside indented code blocks", () => {
    const src = ["    - [ ] in indented code block", "- [ ] real task"].join("\n");
    // Index 0 is the real task on line 1, not the code-block line.
    expect(toggleTaskListItem(src, 0)).toBe(["    - [ ] in indented code block", "- [x] real task"].join("\n"));
  });

  it("supports nested task items by document order", () => {
    const src = "- [ ] outer\n  - [ ] inner";
    expect(toggleTaskListItem(src, 1)).toBe("- [ ] outer\n  - [x] inner");
  });
});

describe("moveTaskListItem", () => {
  it("moves an item downward to a later position", () => {
    const src = "- [ ] A\n- [ ] B\n- [ ] C";
    expect(moveTaskListItem(src, 0, 2)).toBe("- [ ] B\n- [ ] C\n- [ ] A");
  });

  it("moves an item upward to an earlier position", () => {
    const src = "- [ ] A\n- [ ] B\n- [ ] C";
    expect(moveTaskListItem(src, 2, 0)).toBe("- [ ] C\n- [ ] A\n- [ ] B");
  });

  it("swaps two adjacent items", () => {
    const src = "- [ ] A\n- [ ] B\n- [ ] C";
    expect(moveTaskListItem(src, 0, 1)).toBe("- [ ] B\n- [ ] A\n- [ ] C");
    expect(moveTaskListItem(src, 1, 0)).toBe("- [ ] B\n- [ ] A\n- [ ] C");
  });

  it("returns source unchanged when from === to", () => {
    const src = "- [ ] A\n- [ ] B";
    expect(moveTaskListItem(src, 0, 0)).toBe(src);
  });

  it("returns source unchanged when an index is out of range", () => {
    const src = "- [ ] A\n- [ ] B";
    expect(moveTaskListItem(src, 0, 5)).toBe(src);
    expect(moveTaskListItem(src, 5, 0)).toBe(src);
    expect(moveTaskListItem(src, -1, 0)).toBe(src);
  });

  it("preserves checked state of the moved item", () => {
    const src = "- [ ] A\n- [x] B\n- [ ] C";
    expect(moveTaskListItem(src, 1, 0)).toBe("- [x] B\n- [ ] A\n- [ ] C");
  });

  it("preserves non-task content between task items", () => {
    const src = "- [ ] A\nsome prose\n- [ ] B\n- [ ] C";
    expect(moveTaskListItem(src, 0, 2)).toBe("some prose\n- [ ] B\n- [ ] C\n- [ ] A");
  });

  it("skips fenced task-shaped lines when counting", () => {
    const src = ["- [ ] real one", "```", "- [ ] fenced", "```", "- [ ] real two", "- [ ] real three"].join("\n");
    expect(moveTaskListItem(src, 0, 2)).toBe(
      ["```", "- [ ] fenced", "```", "- [ ] real two", "- [ ] real three", "- [ ] real one"].join("\n"),
    );
  });

  it("carries continuation lines along with the moved task", () => {
    const src = ["- [ ] first", "- [ ] second", "  continued text", "  more continuation", "- [ ] third"].join("\n");
    expect(moveTaskListItem(src, 1, 2)).toBe(
      ["- [ ] first", "- [ ] third", "- [ ] second", "  continued text", "  more continuation"].join("\n"),
    );
  });

  it("carries nested sub-task children along with the moved task", () => {
    const src = ["- [ ] outer first", "  - [ ] inner a", "  - [ ] inner b", "- [ ] outer second"].join("\n");
    // Move outer-first (index 0) to outer-second's slot (index 3).
    expect(moveTaskListItem(src, 0, 3)).toBe(
      ["- [ ] outer second", "- [ ] outer first", "  - [ ] inner a", "  - [ ] inner b"].join("\n"),
    );
  });

  it("does not see indented-code task-shaped lines as targets", () => {
    const src = ["    - [ ] in indented code block", "- [ ] real first", "- [ ] real second"].join("\n");
    expect(moveTaskListItem(src, 0, 1)).toBe(
      ["    - [ ] in indented code block", "- [ ] real second", "- [ ] real first"].join("\n"),
    );
  });

  it("returns source unchanged when dragging a task onto its own nested child", () => {
    const src = "- [ ] outer\n  - [ ] inner\n- [ ] sibling";
    // index 0 is outer, index 1 is inner. inner sits inside outer's
    // block, so moving outer to land on inner is a no-op.
    expect(moveTaskListItem(src, 0, 1)).toBe(src);
  });

  it("rejects cross-hierarchy moves between different indent levels", () => {
    const src = ["- [ ] outer", "  - [ ] inner", "- [ ] another outer"].join("\n");
    // inner (index 1, indent 2) onto outer (index 0, indent 0) —
    // different indents, refuse the move so we don't reparent it.
    expect(moveTaskListItem(src, 1, 0)).toBe(src);
    // Same direction: outer onto inner — also rejected.
    expect(moveTaskListItem(src, 2, 1)).toBe(src);
  });

  it("allows moves between same-level siblings under different parents", () => {
    // Both nested tasks live at indent 2 — even though they sit
    // under different parents, the moved indentation matches so
    // the markdown structure stays well-formed.
    const src = ["- [ ] outer A", "  - [ ] child A1", "- [ ] outer B", "  - [ ] child B1"].join("\n");
    // child A1 (index 1) to child B1 (index 3) — same indent.
    expect(moveTaskListItem(src, 1, 3)).toBe(
      ["- [ ] outer A", "- [ ] outer B", "  - [ ] child B1", "  - [ ] child A1"].join("\n"),
    );
  });

  it("carries blank-separated continuation paragraphs along with the task", () => {
    // Markdown allows a list item's body to span blank-separated
    // paragraphs as long as continuation stays indented. moveTask
    // must drag the whole multi-paragraph item together.
    const src = ["- [ ] first", "", "  paragraph two of first", "- [ ] second"].join("\n");
    expect(moveTaskListItem(src, 0, 1)).toBe(
      ["- [ ] second", "- [ ] first", "", "  paragraph two of first"].join("\n"),
    );
  });

  it("preserves blank lines and prose outside the moved block", () => {
    const src = ["Intro line", "", "- [ ] A", "- [ ] B", "  with continuation", "", "Trailing prose"].join("\n");
    // Move A (index 0) to B's slot (index 1). B carries its
    // continuation; A becomes a single-line block.
    expect(moveTaskListItem(src, 0, 1)).toBe(
      ["Intro line", "", "- [ ] B", "  with continuation", "- [ ] A", "", "Trailing prose"].join("\n"),
    );
  });
});
