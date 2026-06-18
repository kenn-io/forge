// @vitest-environment jsdom

import { describe, expect, it } from "vite-plus/test";
import type { DiffFile } from "../api/types.js";
import { buildMarkdownRichPreview } from "./markdown-rich-preview.js";

function markdownFile(lines: DiffFile["hunks"][number]["lines"]): DiffFile {
  return {
    path: "README.md",
    old_path: "README.md",
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: lines.filter((line) => line.type === "add").length,
    deletions: lines.filter((line) => line.type === "delete").length,
    patch: "",
    hunks: [{ old_start: 1, old_count: 20, new_start: 1, new_count: 20, lines }],
  };
}

describe("buildMarkdownRichPreview", () => {
  const repo = { provider: "github", owner: "acme", name: "widgets", repoPath: "acme/widgets" };

  it("preserves whole-document Markdown semantics while exposing block ranges", () => {
    const preview = buildMarkdownRichPreview(
      markdownFile([
        { type: "context", content: "- first", old_num: 1, new_num: 1 },
        { type: "context", content: "", old_num: 2, new_num: 2 },
        { type: "context", content: "- second", old_num: 3, new_num: 3 },
        { type: "context", content: "", old_num: 4, new_num: 4 },
        { type: "context", content: "[ref]: https://example.com", old_num: 5, new_num: 5 },
        { type: "context", content: "", old_num: 6, new_num: 6 },
        { type: "add", content: "See [the ref][ref]", new_num: 7 },
      ]),
      repo,
    );

    const html = preview.blocks.map((block) => block.unifiedHtml).join("");
    expect(html).toContain("<ul>");
    expect(html.match(/<ul>/g)).toHaveLength(1);
    expect(html).toContain('<a href="https://example.com">the ref</a>');
    expect(preview.blocks.some((block) => block.newStart === 7 && block.newEnd === 7)).toBe(true);
  });

  it("projects split block additions without inline underline markup on every word", () => {
    const preview = buildMarkdownRichPreview(
      markdownFile([
        { type: "context", content: "## Title", old_num: 1, new_num: 1 },
        { type: "context", content: "", old_num: 2, new_num: 2 },
        { type: "add", content: "Added paragraph with several words", new_num: 3 },
      ]),
      repo,
    );

    const added = preview.blocks.find((block) => block.newStart === 3);
    expect(added?.unifiedHtml).toContain('class="markdown-diff__block"');
    expect(added?.unifiedHtml).not.toContain("<ins>Added</ins>");
    expect(added?.beforeHtml).toContain("markdown-diff__placeholder");
    expect(added?.afterHtml).toContain("Added paragraph with several words");
  });

  it("uses a coarse fallback for large block comparisons", () => {
    const lines: DiffFile["hunks"][number]["lines"] = [];
    for (let index = 0; index < 160; index++) {
      lines.push({
        type: "delete",
        content: index % 20 === 0 ? "Shared paragraph" : `Old paragraph ${index}`,
        old_num: index * 2 + 1,
      });
      lines.push({ type: "delete", content: "", old_num: index * 2 + 2 });
    }
    for (let index = 0; index < 160; index++) {
      lines.push({
        type: "add",
        content: index % 20 === 0 ? "Shared paragraph" : `New paragraph ${index}`,
        new_num: index * 2 + 1,
      });
      lines.push({ type: "add", content: "", new_num: index * 2 + 2 });
    }

    const preview = buildMarkdownRichPreview(markdownFile(lines), repo);

    expect(preview.blocks.some((block) => block.oldStart != null && block.newStart != null)).toBe(false);
    expect(preview.blocks.some((block) => block.oldStart != null)).toBe(true);
    expect(preview.blocks.some((block) => block.newStart != null)).toBe(true);
  });

  it("does not render synthetic hunk separators as preview content", () => {
    const file = {
      ...markdownFile([]),
      hunks: [
        {
          old_start: 1,
          old_count: 1,
          new_start: 1,
          new_count: 1,
          lines: [{ type: "context", content: "# First hunk", old_num: 1, new_num: 1 }],
        },
        {
          old_start: 20,
          old_count: 1,
          new_start: 20,
          new_count: 1,
          lines: [{ type: "context", content: "Second hunk paragraph", old_num: 20, new_num: 20 }],
        },
      ],
    } satisfies DiffFile;

    const preview = buildMarkdownRichPreview(file, repo);
    const html = preview.blocks.map((block) => block.unifiedHtml).join("");

    expect(html).toContain("First hunk");
    expect(html).toContain("Second hunk paragraph");
    expect(html).not.toContain("<hr");
    expect(preview.blocks.every((block) => block.oldStart != null || block.newStart != null)).toBe(true);
  });

  it("strips synthetic hunk separator lines from blocks spanning hunks", () => {
    const file = {
      ...markdownFile([]),
      hunks: [
        {
          old_start: 1,
          old_count: 2,
          new_start: 1,
          new_count: 2,
          lines: [
            { type: "context", content: "```text", old_num: 1, new_num: 1 },
            { type: "context", content: "first hunk code", old_num: 2, new_num: 2 },
          ],
        },
        {
          old_start: 10,
          old_count: 2,
          new_start: 10,
          new_count: 2,
          lines: [
            { type: "context", content: "second hunk code", old_num: 10, new_num: 10 },
            { type: "context", content: "```", old_num: 11, new_num: 11 },
          ],
        },
      ],
    } satisfies DiffFile;

    const preview = buildMarkdownRichPreview(file, repo);
    const html = preview.blocks.map((block) => block.unifiedHtml).join("");

    expect(html).toContain("first hunk code");
    expect(html).toContain("second hunk code");
    expect(html).not.toContain("---");
    expect(preview.blocks.find((block) => block.unifiedHtml.includes("first hunk code"))?.newLines).toEqual([
      1, 2, 10, 11,
    ]);
  });

  it("hides synthetic separators around multi-hunk HTML blocks", () => {
    const file = {
      ...markdownFile([]),
      hunks: [
        {
          old_start: 1,
          old_count: 2,
          new_start: 1,
          new_count: 2,
          lines: [
            { type: "context", content: "<div>", old_num: 1, new_num: 1 },
            { type: "context", content: "first hunk html", old_num: 2, new_num: 2 },
          ],
        },
        {
          old_start: 10,
          old_count: 2,
          new_start: 10,
          new_count: 2,
          lines: [
            { type: "context", content: "second hunk html", old_num: 10, new_num: 10 },
            { type: "context", content: "</div>", old_num: 11, new_num: 11 },
          ],
        },
      ],
    } satisfies DiffFile;

    const preview = buildMarkdownRichPreview(file, repo);
    const html = preview.blocks.map((block) => block.unifiedHtml).join("");

    expect(html).toContain("first hunk html");
    expect(html).toContain("second hunk html");
    expect(html).not.toContain("---");
    expect(html).not.toContain("<hr");
  });

  it("hides synthetic separators around multi-hunk blockquotes", () => {
    const file = {
      ...markdownFile([]),
      hunks: [
        {
          old_start: 1,
          old_count: 1,
          new_start: 1,
          new_count: 1,
          lines: [{ type: "context", content: "> first hunk quote", old_num: 1, new_num: 1 }],
        },
        {
          old_start: 10,
          old_count: 1,
          new_start: 10,
          new_count: 1,
          lines: [{ type: "context", content: "> second hunk quote", old_num: 10, new_num: 10 }],
        },
      ],
    } satisfies DiffFile;

    const preview = buildMarkdownRichPreview(file, repo);
    const html = preview.blocks.map((block) => block.unifiedHtml).join("");

    expect(html).toContain("<blockquote>");
    expect(html).toContain("first hunk quote");
    expect(html).toContain("second hunk quote");
    expect(html).not.toContain("---");
    expect(html).not.toContain("<hr");
  });

  it("hides synthetic separators around multi-hunk list continuations", () => {
    const file = {
      ...markdownFile([]),
      hunks: [
        {
          old_start: 1,
          old_count: 1,
          new_start: 1,
          new_count: 1,
          lines: [{ type: "context", content: "- first hunk item", old_num: 1, new_num: 1 }],
        },
        {
          old_start: 10,
          old_count: 1,
          new_start: 10,
          new_count: 1,
          lines: [{ type: "context", content: "- second hunk item", old_num: 10, new_num: 10 }],
        },
      ],
    } satisfies DiffFile;

    const preview = buildMarkdownRichPreview(file, repo);
    const html = preview.blocks.map((block) => block.unifiedHtml).join("");

    expect(html).toContain("<ul>");
    expect(html).toContain("first hunk item");
    expect(html).toContain("second hunk item");
    expect(html).not.toContain("---");
    expect(html).not.toContain("<hr");
  });

  it("hides synthetic separators around multi-hunk tables without merging unrelated table blocks", () => {
    const file = {
      ...markdownFile([]),
      hunks: [
        {
          old_start: 1,
          old_count: 3,
          new_start: 1,
          new_count: 3,
          lines: [
            { type: "context", content: "| Name |", old_num: 1, new_num: 1 },
            { type: "context", content: "| --- |", old_num: 2, new_num: 2 },
            { type: "context", content: "| first hunk row |", old_num: 3, new_num: 3 },
          ],
        },
        {
          old_start: 10,
          old_count: 3,
          new_start: 10,
          new_count: 3,
          lines: [
            { type: "context", content: "| Name |", old_num: 10, new_num: 10 },
            { type: "context", content: "| --- |", old_num: 11, new_num: 11 },
            { type: "context", content: "| second hunk row |", old_num: 12, new_num: 12 },
          ],
        },
      ],
    } satisfies DiffFile;

    const preview = buildMarkdownRichPreview(file, repo);
    const html = preview.blocks.map((block) => block.unifiedHtml).join("");

    expect(html.match(/<table>/g)).toHaveLength(2);
    expect(html).toContain("first hunk row");
    expect(html).toContain("second hunk row");
    expect(html).not.toContain("---");
    expect(html).not.toContain("<hr");
  });

  it("keeps user-authored thematic breaks from source lines", () => {
    const preview = buildMarkdownRichPreview(
      markdownFile([
        { type: "context", content: "Before", old_num: 1, new_num: 1 },
        { type: "context", content: "", old_num: 2, new_num: 2 },
        { type: "context", content: "---", old_num: 3, new_num: 3 },
        { type: "context", content: "", old_num: 4, new_num: 4 },
        { type: "context", content: "After", old_num: 5, new_num: 5 },
      ]),
      repo,
    );

    const html = preview.blocks.map((block) => block.unifiedHtml).join("");

    expect(html).toContain("<hr");
  });
});
