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

  it("renders link target-only changes visibly", () => {
    const html = renderMarkdownDiff('<p><a href="/old">docs</a></p>', '<p><a href="/new">docs</a></p>');

    expect(html).toMatch(/<del><a href="\/old">docs<\/a><\/del>/);
    expect(html).toMatch(/<ins><a href="\/new">docs<\/a><\/ins>/);
  });

  it("renders heading level changes visibly", () => {
    const html = renderMarkdownDiff("<h2>Release notes</h2>", "<h3>Release notes</h3>");

    expect(html).toContain('<del class="markdown-diff__block"><h2>Release notes</h2></del>');
    expect(html).toContain('<ins class="markdown-diff__block"><h3>Release notes</h3></ins>');
  });

  it("falls back to a coarse visible diff for large comparisons", () => {
    const before = `<p>${Array.from({ length: 150 }, (_, index) => `old-${index}`).join(" ")}</p>`;
    const after = `<p>${Array.from({ length: 150 }, (_, index) => `new-${index}`).join(" ")}</p>`;

    const html = renderMarkdownDiff(before, after);

    expect(html).toContain("<del>old-0");
    expect(html).toContain("<ins>new-0");
  });
});
