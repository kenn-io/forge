import { describe, expect, it } from "vite-plus/test";
import { summarizeMarkdownBody } from "./markdown-excerpt.js";

describe("summarizeMarkdownBody", () => {
  it("returns empty string for empty or whitespace-only input", () => {
    expect(summarizeMarkdownBody("")).toBe("");
    expect(summarizeMarkdownBody("   \n\t  ")).toBe("");
    expect(summarizeMarkdownBody(null)).toBe("");
    expect(summarizeMarkdownBody(undefined)).toBe("");
  });

  it("strips HTML comments, fenced code blocks, images, and markdown formatting", () => {
    const raw = [
      "<!-- Describe your changes here -->",
      "## Summary",
      "",
      "This PR sets **cache-pool** to `default-tier` in `region-a` and fixes `snake_case_name`.",
      "",
      "- [x] Updates [the client](https://example.com/client)",
      "- [ ] Keeps ![screenshot](https://example.com/shot.png) out of the summary",
      "",
      "```go",
      'func main() { fmt.Println("ignore code") }',
      "```",
      "",
      "> Quoted note with _emphasis_ and ~~old~~ text.",
    ].join("\n");

    expect(summarizeMarkdownBody(raw, 400)).toBe(
      "Summary This PR sets cache-pool to default-tier in region-a and fixes snake_case_name. Updates the client Keeps out of the summary Quoted note with emphasis and old text.",
    );
  });

  it("truncates cleanly at a word boundary with an ellipsis", () => {
    const raw =
      "First sentence describes the architecture change in detail so the hover card stays compact and readable.";
    const summary = summarizeMarkdownBody(raw, 55);
    expect(summary).toBe("First sentence describes the architecture change in…");
  });
});
