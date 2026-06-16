// @vitest-environment jsdom

import { describe, expect, it } from "vite-plus/test";
import { renderMarkdownDiff, renderMarkdownSplitDiff } from "./markdown-diff.js";

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

  it("pairs adjacent compatible element changes in order", () => {
    const html = renderMarkdownDiff(
      "<p><em>alpha</em><strong>beta</strong></p>",
      "<p><em>one</em><strong>two</strong></p>",
    );

    expect(html).toContain("<em><del>alpha</del><ins>one</ins></em>");
    expect(html).toContain("<strong><del>beta</del><ins>two</ins></strong>");
    expect(html).not.toContain("<del><em>alpha</em></del>");
    expect(html).not.toContain("<ins><em>one</em></ins>");
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

  it("projects split diffs with placeholders to keep changed prose aligned", () => {
    const split = renderMarkdownSplitDiff("<p>Hello old world</p>", "<p>Hello new world</p>");

    expect(split.beforeHtml).toContain("<del>old</del>");
    expect(split.beforeHtml).not.toContain("new");
    expect(split.afterHtml).not.toContain("old");
    expect(split.afterHtml).toContain("<ins>new</ins>");
  });

  it("projects split block additions with opposite-side placeholders", () => {
    const split = renderMarkdownSplitDiff("<h2>Intro</h2>", "<h2>Intro</h2><p>Added note</p>");

    expect(split.beforeHtml).toContain("<h2>Intro</h2>");
    expect(split.beforeHtml).toContain(
      '<ins class="markdown-diff__block markdown-diff__placeholder" aria-hidden="true"><p>Added note</p></ins>',
    );
    expect(split.afterHtml).toContain('<ins class="markdown-diff__block"><p>Added note</p></ins>');
  });
});
