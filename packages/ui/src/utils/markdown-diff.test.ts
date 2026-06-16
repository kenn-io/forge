// @vitest-environment jsdom

import { describe, expect, it } from "vite-plus/test";
import { renderMarkdownDiff, renderMarkdownSplitDiff } from "./markdown-diff.js";

function htmlFragment(html: string): DocumentFragment {
  const template = document.createElement("template");
  template.innerHTML = html;
  return template.content;
}

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

  it("continues pairing compatible element changes after an incompatible sibling", () => {
    const html = renderMarkdownDiff(
      "<p><em>alpha</em><strong>beta</strong><code>gamma</code></p>",
      '<p><a href="/link">link</a><strong>two</strong><code>three</code></p>',
    );

    expect(html).toContain("<del><em>alpha</em></del>");
    expect(html).toContain('<ins><a href="/link">link</a></ins>');
    expect(html).toContain("<strong><del>beta</del><ins>two</ins></strong>");
    expect(html).toContain("<code><del>gamma</del><ins>three</ins></code>");
    expect(html).not.toContain("<del><strong>beta</strong></del>");
    expect(html).not.toContain("<ins><strong>two</strong></ins>");
  });

  it("preserves list item structure for inserted and deleted items", () => {
    const html = renderMarkdownDiff("<ul><li>Removed</li><li>Keep</li></ul>", "<ul><li>Keep</li><li>Added</li></ul>");
    const fragment = htmlFragment(html);

    expect(fragment.querySelector("ul > del")).toBeNull();
    expect(fragment.querySelector("ul > ins")).toBeNull();
    expect(fragment.querySelector('ul > li[data-diff-kind="delete"] > del')?.textContent).toBe("Removed");
    expect(fragment.querySelector('ul > li[data-diff-kind="insert"] > ins')?.textContent).toBe("Added");
  });

  it("preserves table row structure for inserted and deleted rows", () => {
    const html = renderMarkdownDiff(
      "<table><tbody><tr><td>Removed</td></tr><tr><td>Keep</td></tr></tbody></table>",
      "<table><tbody><tr><td>Keep</td></tr><tr><td>Added</td></tr></tbody></table>",
    );
    const fragment = htmlFragment(html);

    expect(fragment.querySelector("tbody > del")).toBeNull();
    expect(fragment.querySelector("tbody > ins")).toBeNull();
    expect(fragment.querySelector('tbody > tr[data-diff-kind="delete"] > td > del')?.textContent).toBe("Removed");
    expect(fragment.querySelector('tbody > tr[data-diff-kind="insert"] > td > ins')?.textContent).toBe("Added");
  });

  it("preserves table cell structure for inserted and deleted cells", () => {
    const html = renderMarkdownDiff(
      "<table><tbody><tr><td>Removed</td><td>Keep</td></tr></tbody></table>",
      "<table><tbody><tr><td>Keep</td><td>Added</td></tr></tbody></table>",
    );
    const fragment = htmlFragment(html);

    expect(fragment.querySelector("tr > del")).toBeNull();
    expect(fragment.querySelector("tr > ins")).toBeNull();
    expect(fragment.querySelector('tr > td[data-diff-kind="delete"] > del')?.textContent).toBe("Removed");
    expect(fragment.querySelector('tr > td[data-diff-kind="insert"] > ins')?.textContent).toBe("Added");
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

  it("projects split structural additions with same-structure placeholders", () => {
    const split = renderMarkdownSplitDiff("<ul><li>Keep</li></ul>", "<ul><li>Keep</li><li>Added</li></ul>");
    const beforeFragment = htmlFragment(split.beforeHtml);
    const afterFragment = htmlFragment(split.afterHtml);

    expect(beforeFragment.querySelector("ul > ins")).toBeNull();
    expect(
      beforeFragment.querySelector('ul > li.markdown-diff__placeholder[data-diff-kind="insert"] > ins')?.textContent,
    ).toBe("Added");
    expect(afterFragment.querySelector('ul > li[data-diff-kind="insert"] > ins')?.textContent).toBe("Added");
  });
});
