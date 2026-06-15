// @vitest-environment jsdom

import { describe, expect, it } from "vite-plus/test";
import { renderMarkdownDiff } from "./markdown-diff.js";

describe("renderMarkdownDiff", () => {
  it("renders changed prose as one annotated HTML document", () => {
    const html = renderMarkdownDiff("<p>Hello old world</p>", "<p>Hello new world</p>");

    expect(html).toContain("<p>");
    expect(html).toContain("<del>old</del>");
    expect(html).toContain("<ins>new</ins>");
    expect(html).toContain("Hello");
    expect(html).toContain("world");
  });

  it("marks inserted block nodes inline with the rendered document", () => {
    const html = renderMarkdownDiff("<h2>Intro</h2>", "<h2>Intro</h2><p>Added note</p>");

    expect(html).toContain("<h2>Intro</h2>");
    expect(html).toContain('<ins class="markdown-diff__block"><p>Added note</p></ins>');
  });
});
