import { describe, expect, test } from "vite-plus/test";
import { renderDocsMarkdown, slugify, splitFrontmatter } from "./markdown";
import { buildFolderIndex } from "./folderLinks";
import type { TreeNode } from "./types";

const tree: TreeNode = {
  name: "Notes",
  rel_path: "",
  is_dir: true,
  children: [
    {
      name: "Projects",
      rel_path: "Projects",
      is_dir: true,
      children: [
        { name: "alpha.md", rel_path: "Projects/alpha.md", is_dir: false, size: 1 },
        { name: "obsidian.md", rel_path: "Projects/obsidian.md", is_dir: false, size: 1 },
      ],
    },
    {
      name: "Daily",
      rel_path: "Daily",
      is_dir: true,
      children: [{ name: "alpha.md", rel_path: "Daily/alpha.md", is_dir: false, size: 1 }],
    },
    { name: "README.md", rel_path: "README.md", is_dir: false, size: 1 },
  ],
};

const baseOptions = {
  folderID: "notes",
  currentDocPath: "Projects/alpha.md",
  index: buildFolderIndex(tree),
  buildDocURL: (folder: string, path: string, anchor?: string) =>
    `/docs?folder=${folder}&doc=${encodeURIComponent(path)}${anchor ? `#${anchor}` : ""}`,
  buildBlobURL: (folder: string, path: string) =>
    `/api/v1/docs/folders/${folder}/blob?path=${encodeURIComponent(path)}`,
};

describe("splitFrontmatter", () => {
  test("removes a leading YAML block when present", () => {
    const result = splitFrontmatter("---\ntitle: Foo\n---\n# Heading\nbody");
    expect(result.frontmatter).toBe("title: Foo");
    expect(result.body).toBe("# Heading\nbody");
  });

  test("leaves body alone when no frontmatter is present", () => {
    const result = splitFrontmatter("# Heading\nbody");
    expect(result.frontmatter).toBeNull();
    expect(result.body).toBe("# Heading\nbody");
  });
});

describe("slugify", () => {
  test("produces stable lowercase hyphenated ids", () => {
    expect(slugify("Hello, World!")).toBe("hello-world");
    expect(slugify("  Multiple   Spaces  ")).toBe("multiple-spaces");
    expect(slugify("Already-Slugged")).toBe("already-slugged");
  });
});

describe("renderDocsMarkdown", () => {
  test("strips frontmatter and renders heading with id anchor", () => {
    const html = renderDocsMarkdown("---\ntitle: x\n---\n# Hello World\n", baseOptions);
    expect(html).toContain('<h1 id="hello-world">Hello World</h1>');
    expect(html).not.toContain("title: x");
  });

  test("resolves a unambiguous wikilink to a doc URL", () => {
    const html = renderDocsMarkdown("See [[obsidian]] for details.", baseOptions);
    expect(html).toMatch(/class="wikilink"/);
    expect(html).toMatch(/data-doc-link="Projects\/obsidian.md"/);
    expect(html).toMatch(/href="\/docs\?folder=notes&amp;doc=Projects%2Fobsidian.md"/);
  });

  test("flags ambiguous wikilinks with all candidates", () => {
    const html = renderDocsMarkdown("Refer to [[alpha]].", baseOptions);
    expect(html).toMatch(/wikilink--ambiguous/);
    expect(html).toContain("Projects/alpha.md");
    expect(html).toContain("Daily/alpha.md");
  });

  test("marks missing wikilinks as faded spans", () => {
    const html = renderDocsMarkdown("Missing: [[no-such-note]].", baseOptions);
    expect(html).toMatch(/wikilink--missing/);
    expect(html).toMatch(/data-wikilink="missing"/);
  });

  test("rewrites relative markdown links to in-app doc URLs", () => {
    const html = renderDocsMarkdown("[Obsidian](./obsidian.md)", baseOptions);
    expect(html).toMatch(/class="doc-link"/);
    expect(html).toMatch(/data-doc-link="Projects\/obsidian.md"/);
    expect(html).toContain("Obsidian");
  });

  test("preserves explicit aliases on wikilinks", () => {
    const html = renderDocsMarkdown("[[obsidian|Obsidian replacement]]", baseOptions);
    expect(html).toContain("Obsidian replacement");
    expect(html).toMatch(/data-doc-link="Projects\/obsidian.md"/);
  });

  test("attaches anchor metadata when a heading is referenced", () => {
    const html = renderDocsMarkdown("[[obsidian#architecture]]", baseOptions);
    expect(html).toMatch(/data-anchor="architecture"/);
  });

  test("opens external links in a new tab", () => {
    const html = renderDocsMarkdown("[Example Docs](https://docs.example.com)", baseOptions);
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noreferrer"');
  });

  test("tags kata issue links so the app can intercept them", () => {
    const html = renderDocsMarkdown("[Issue](kata://issue/abc)", baseOptions);
    // The renderer parks the UID in a data attribute and uses a safe
    // href so the DOMPurify scheme allowlist doesn't drop it.
    expect(html).toMatch(/data-kata-link="issue"/);
    expect(html).toMatch(/data-kata-uid="abc"/);
  });

  test("preserves anchor fragments on relative markdown links", () => {
    const html = renderDocsMarkdown("[See API](obsidian.md#api)", baseOptions);
    expect(html).toMatch(/data-doc-link="Projects\/obsidian\.md"/);
    expect(html).toMatch(/data-anchor="api"/);
  });

  test("scrubs data:image/svg+xml from raw HTML img tags", () => {
    const html = renderDocsMarkdown(`<img src="data:image/svg+xml,%3Csvg%3E" alt="x">`, baseOptions);
    expect(html).not.toMatch(/src="data:image\/svg/);
  });

  test("does not turn #abc inside link text into a kata anchor", () => {
    const html = renderDocsMarkdown("[see #abc](https://example.com)", baseOptions);
    expect(html).not.toMatch(/data-kata-link/);
    expect(html).toMatch(/see #abc/);
  });

  test("mention regex stops on trailing sentence punctuation", () => {
    const html = renderDocsMarkdown("Hello @wes.", baseOptions);
    expect(html).toMatch(/data-kata-mention="wes"/);
    expect(html).not.toMatch(/data-kata-mention="wes\."/);
  });

  test("rewrites relative image src to a blob URL", () => {
    const html = renderDocsMarkdown("![logo](../assets/logo.png)", baseOptions);
    expect(html).toMatch(/src="\/api\/v1\/docs\/folders\/notes\/blob\?path=assets%2Flogo.png"/);
  });

  test("does not rewrite local SVG images to the blob endpoint", () => {
    const html = renderDocsMarkdown("![diagram](../assets/diagram.svg)", baseOptions);
    expect(html).not.toContain("/api/v1/docs/folders/notes/blob");
    expect(html).toMatch(/src="\.\.\/assets\/diagram.svg"/);
  });

  test("renders ![[image.png]] embed as a blob image", () => {
    const html = renderDocsMarkdown("![[assets/logo.png|logo alt]]", baseOptions);
    expect(html).toMatch(/<img[^>]+src="\/api\/v1\/docs\/folders\/notes\/blob/);
    expect(html).toMatch(/alt="logo alt"/);
  });

  test("does not rewrite SVG wikilink embeds to the blob endpoint", () => {
    const html = renderDocsMarkdown("![[assets/diagram.svg]]", baseOptions);
    expect(html).not.toContain("/api/v1/docs/folders/notes/blob");
    expect(html).toContain("assets/diagram.svg");
  });

  test("preserves GitHub-style details blocks with rendered markdown inside", () => {
    const source = [
      "<details open>",
      "",
      "<summary>Tips for collapsed sections</summary>",
      "",
      "### You can add a header",
      "",
      "You can add text within a collapsed section.",
      "",
      "</details>",
    ].join("\n");
    const html = renderDocsMarkdown(source, baseOptions);

    expect(html).toContain("<details");
    expect(html).toContain('open=""');
    expect(html).toContain("<summary>Tips for collapsed sections</summary>");
    expect(html).toContain('<h3 id="you-can-add-a-header">You can add a header</h3>');
    expect(html).toContain("<p>You can add text within a collapsed section.</p>");
    expect(html).toContain("</details>");
  });
});

describe("hardened rendering", () => {
  test("renders GFM pipe tables into <table>/<thead>/<tbody>/<th>/<td>", () => {
    const source = ["| Name | Status |", "|------|--------|", "| Foo  | open   |", "| Bar  | done   |"].join("\n");
    const html = renderDocsMarkdown(source, baseOptions);
    expect(html).toMatch(/<table[^>]*>/);
    expect(html).toMatch(/<thead>[\s\S]*<th[^>]*>Name<\/th>/);
    expect(html).toMatch(/<tbody>[\s\S]*<td[^>]*>Foo<\/td>/);
    expect(html).toMatch(/<td[^>]*>done<\/td>/);
  });

  test("renders mermaid fences as mermaid diagram targets", () => {
    const html = renderDocsMarkdown("```mermaid\ngraph TD\n  A --> B\n```", baseOptions);

    expect(html).toContain('<pre class="mermaid">graph TD\n  A --&gt; B</pre>');
    expect(html).not.toContain("language-mermaid");
  });

  test("images get loading=lazy and decoding=async for perf", () => {
    const html = renderDocsMarkdown("![logo](./assets/logo.png)", baseOptions);
    expect(html).toMatch(/<img[^>]+loading="lazy"/);
    expect(html).toMatch(/<img[^>]+decoding="async"/);
  });

  test("strips data: and javascript: image src", () => {
    const dataUri = "![bad](data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=)";
    const dataHtml = renderDocsMarkdown(dataUri, baseOptions);
    expect(dataHtml).not.toContain("data:image/svg");

    const jsHref = "[click](javascript:alert(1))";
    const jsHtml = renderDocsMarkdown(jsHref, baseOptions);
    expect(jsHtml).not.toContain("javascript:");
  });

  test("renders external https images as-is", () => {
    const html = renderDocsMarkdown("![](https://example.com/logo.png)", baseOptions);
    expect(html).toMatch(/<img[^>]+src="https:\/\/example.com\/logo.png"/);
  });

  test("drops protocol-relative markdown link and image", () => {
    const linkHtml = renderDocsMarkdown("[click](//evil.com/x)", baseOptions);
    expect(linkHtml).not.toContain("evil.com");
    expect(linkHtml).not.toContain("<a ");

    const imgHtml = renderDocsMarkdown("![bad](//evil.com/x.png)", baseOptions);
    expect(imgHtml).not.toContain("evil.com");
    expect(imgHtml).not.toContain("<img");
  });

  test("drops protocol-relative href on raw HTML anchors", () => {
    // Raw HTML reaches the DOMPurify attribute hook with the bytes intact,
    // so mixed slash/backslash variants must all be rejected there.
    for (const href of ["//evil.com/x", "\\\\evil.com/x", "/\\evil.com/x", "\\/evil.com/x"]) {
      const html = renderDocsMarkdown(`<a href="${href}">click</a>`, baseOptions);
      expect(html).not.toContain("evil.com");
    }
  });

  test("keeps single-slash root-relative links", () => {
    const html = renderDocsMarkdown("[home](/docs/readme)", baseOptions);
    expect(html).toContain('href="/docs/readme"');
  });
});

describe("kata short-id links", () => {
  test("renders bare #abcd as a clickable kata link", () => {
    const html = renderDocsMarkdown("See #budget for details.", baseOptions);
    expect(html).toMatch(/<a[^>]+class="kata-link"/);
    expect(html).toMatch(/data-kata-link="true"/);
    expect(html).toMatch(/data-kata-short-id="budget"/);
    expect(html).toContain("#budget");
  });

  test("renders project/#abcd as a qualified kata link", () => {
    const html = renderDocsMarkdown("Closed by notes:inbox/#capt yesterday.", baseOptions);
    expect(html).toMatch(/data-kata-short-id="capt"/);
    expect(html).toMatch(/data-kata-project="notes:inbox"/);
    expect(html).toContain("notes:inbox/#capt");
  });

  test("recognizes short-ids at the start of a paragraph", () => {
    const html = renderDocsMarkdown("#yoga is the priority today.", baseOptions);
    expect(html).toMatch(/data-kata-short-id="yoga"/);
  });

  test("does not match URL fragments inside markdown links", () => {
    // The `#abc` is inside an href, not raw text — it should not be
    // tokenized as a kata link.
    const html = renderDocsMarkdown("[anchor](page.md#abc)", baseOptions);
    expect(html).not.toMatch(/data-kata-link/);
  });

  test("does not match inside inline code", () => {
    const html = renderDocsMarkdown("Use `#abc` literally.", baseOptions);
    expect(html).not.toMatch(/data-kata-link/);
    expect(html).toContain("<code>#abc</code>");
  });

  test("ignores ATX headings", () => {
    // A line that begins with `#` followed by a space is a heading,
    // not a kata link.
    const html = renderDocsMarkdown("# Heading\n", baseOptions);
    expect(html).toMatch(/<h1[^>]*>Heading<\/h1>/);
    expect(html).not.toMatch(/data-kata-link/);
  });

  test("rejects mid-word #refs to avoid false positives", () => {
    const html = renderDocsMarkdown("issue#abc-tagged", baseOptions);
    // Preceded by 'issue' (word char), should not match bare form.
    expect(html).not.toMatch(/data-kata-link/);
  });
});

describe("mentions", () => {
  test("renders @handle as a styled kata-mention span", () => {
    const html = renderDocsMarkdown("Hey @wes look at this.", baseOptions);
    expect(html).toMatch(/<span class="kata-mention" data-kata-mention="wes">@wes<\/span>/);
  });

  test("does not match @ preceded by a word char (email avoidance)", () => {
    const html = renderDocsMarkdown("ping me at me@example.com", baseOptions);
    expect(html).not.toMatch(/kata-mention/);
  });
});
