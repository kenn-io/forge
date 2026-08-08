import { describe, expect, test } from "vite-plus/test";
import { buildSuggestionDiffFile, parseMarkdownSuggestions } from "./markdown-suggestions.js";
import type { ReviewThreadContext } from "../components/diff/review-thread-context.js";
import type { ReviewThread } from "../components/diff/review-thread-context.js";

describe("parseMarkdownSuggestions", () => {
  test("splits suggestion fences from surrounding markdown", () => {
    const blocks = parseMarkdownSuggestions(
      [
        "Please inline the guard.",
        "",
        "```suggestion",
        "if (err != nil) {",
        "\treturn err",
        "}",
        "```",
        "",
        "This keeps the branch simple.",
      ].join("\n"),
    );

    expect(blocks).toEqual([
      {
        type: "markdown",
        key: "markdown-0",
        text: "Please inline the guard.\n\n",
      },
      {
        type: "suggestion",
        key: "suggestion-1",
        replacement: "if (err != nil) {\n\treturn err\n}",
        fenceLine: 3,
      },
      {
        type: "markdown",
        key: "markdown-2",
        text: "\nThis keeps the branch simple.",
      },
    ]);
  });

  test("does not treat non-suggestion code fences as suggestions", () => {
    const blocks = parseMarkdownSuggestions("```ts\nconst ok = true;\n```");

    expect(blocks).toEqual([
      {
        type: "markdown",
        key: "markdown-0",
        text: "```ts\nconst ok = true;\n```",
      },
    ]);
  });

  test("parses CRLF suggestion fences with normalized replacement text", () => {
    const blocks = parseMarkdownSuggestions(
      ["Please apply this.", "", "```suggestion", "return compute();", "```", "", "Then rerun the check."].join("\r\n"),
    );

    expect(blocks).toEqual([
      {
        type: "markdown",
        key: "markdown-0",
        text: "Please apply this.\n\n",
      },
      {
        type: "suggestion",
        key: "suggestion-1",
        replacement: "return compute();",
        fenceLine: 3,
      },
      {
        type: "markdown",
        key: "markdown-2",
        text: "\nThen rerun the check.",
      },
    ]);
  });

  test("ignores suggestion-looking fences inside code blocks", () => {
    const blocks = parseMarkdownSuggestions(
      [
        "Reviewer explained this with a markdown example.",
        "",
        "````markdown",
        "```suggestion",
        "return client.publishThreads();",
        "```",
        "````",
        "",
        "```suggestion",
        "return actualSuggestion();",
        "```",
      ].join("\n"),
    );

    expect(blocks.filter((block) => block.type === "suggestion")).toEqual([
      {
        type: "suggestion",
        key: "suggestion-1",
        replacement: "return actualSuggestion();",
        fenceLine: 9,
      },
    ]);
  });

  test("ignores suggestion-looking fences inside indented code blocks", () => {
    const blocks = parseMarkdownSuggestions(
      [
        "Reviewer explained this with an indented markdown example.",
        "",
        "   ````markdown",
        "```suggestion",
        "return client.publishThreads();",
        "```",
        "   ````",
        "",
        "  ```suggestion",
        "return actualSuggestion();",
        "  ```",
      ].join("\n"),
    );

    expect(blocks.filter((block) => block.type === "suggestion")).toEqual([
      {
        type: "suggestion",
        key: "suggestion-1",
        replacement: "return actualSuggestion();",
        fenceLine: 9,
      },
    ]);
  });
});

describe("buildSuggestionDiffFile", () => {
  const thread: ReviewThread = {
    id: "42",
    provider_comment_id: "1001",
    path: "src/review.ts",
    side: "right",
    start_side: "right",
    start_line: 10,
    line: 11,
    new_line: 11,
    line_type: "context",
    diff_head_sha: "abc123",
    commit_sha: "abc123",
    body: "Please simplify this.",
    author_login: "reviewer",
    resolved: false,
    can_resolve: true,
    created_at: "2024-06-01T12:00:00Z",
    updated_at: "2024-06-01T12:00:00Z",
  };

  const context: ReviewThreadContext = {
    path: "src/review.ts",
    lineLabel: "src/review.ts:10-11",
    outdated: false,
    lines: [
      {
        key: "9",
        type: "context",
        oldNum: 9,
        newNum: 9,
        content: "function build() {",
        target: false,
      },
      {
        key: "10",
        type: "context",
        oldNum: 10,
        newNum: 10,
        content: "const value = compute();",
        target: true,
      },
      {
        key: "11",
        type: "context",
        oldNum: 11,
        newNum: 11,
        content: "return value;",
        target: true,
      },
      {
        key: "12",
        type: "context",
        oldNum: 12,
        newNum: 12,
        content: "}",
        target: false,
      },
    ],
  };

  test("builds a Pierre-compatible file diff replacing the commented range", () => {
    const file = buildSuggestionDiffFile(thread, context, "return compute();");

    expect(file.path).toBe("src/review.ts");
    expect(file.additions).toBe(1);
    expect(file.deletions).toBe(2);
    expect(file.patch).toContain("-const value = compute();");
    expect(file.patch).toContain("-return value;");
    expect(file.patch).toContain("+return compute();");
    expect(file.hunks).toEqual([
      {
        old_start: 9,
        old_count: 4,
        new_start: 9,
        new_count: 3,
        section: "Suggested change",
        lines: [
          { type: "context", old_num: 9, new_num: 9, content: "function build() {" },
          { type: "delete", old_num: 10, content: "const value = compute();" },
          { type: "delete", old_num: 11, content: "return value;" },
          { type: "add", new_num: 10, content: "return compute();" },
          { type: "context", old_num: 12, new_num: 11, content: "}" },
        ],
      },
    ]);
  });

  test("preserves a trailing blank replacement line in the preview diff", () => {
    const file = buildSuggestionDiffFile(thread, context, "return compute();\n");

    expect(file.additions).toBe(2);
    expect(file.hunks[0]?.lines.filter((line) => line.type === "add")).toEqual([
      { type: "add", new_num: 10, content: "return compute();" },
      { type: "add", new_num: 11, content: "" },
    ]);
  });
});
