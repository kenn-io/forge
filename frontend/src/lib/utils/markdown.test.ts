import { Effect } from "effect";
import { describe, expect, it, vi } from "vite-plus/test";
import { buildCanonicalProviderItemURL } from "./item-reference.js";
import {
  githubAttachmentParagraphSources,
  renderMarkdown,
  renderMarkdownBlocks,
  renderMarkdownEffect,
  renderMarkdownSync,
  type RepoContext,
} from "./markdown.js";

describe("renderMarkdown task lists", () => {
  it("proxies private GitHub attachment images through the repo-scoped API", async () => {
    const source = "https://github.com/user-attachments/assets/11111111-2222-3333-4444-555555555555";
    const html = await renderMarkdown(`<img width="1440" height="1000" alt="Project list" src="${source}" />`, {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    });

    expect(html).toContain(
      `src="/api/v1/repo/github/acme/widgets/markdown-image?source=${encodeURIComponent(source)}"`,
    );
    expect(html).toContain('width="1440"');
    expect(html).toContain('height="1000"');
  });

  it("keeps the configured base path in proxied image URLs", async () => {
    const previousBasePath = window.__BASE_PATH__;
    window.__BASE_PATH__ = "/kenn-forge/";
    const source = "https://github.com/user-attachments/assets/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";

    try {
      const html = await renderMarkdown(`![Private image](${source})`, {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
      });

      expect(html).toContain(
        `src="/kenn-forge/api/v1/repo/github/acme/widgets/markdown-image?source=${encodeURIComponent(source)}"`,
      );
    } finally {
      if (previousBasePath === undefined) delete window.__BASE_PATH__;
      else window.__BASE_PATH__ = previousBasePath;
    }
  });

  it("proxies images committed to the repository through the repo-scoped API", async () => {
    const repo = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    };
    const sources = [
      "https://github.com/acme/widgets/blob/feat/search-controls/docs/images/search.png?raw=true",
      "https://github.com/acme/widgets/raw/main/docs/images/search.png",
      "https://raw.githubusercontent.com/Acme/Widgets/main/docs/images/search.png",
    ];

    for (const source of sources) {
      const html = await renderMarkdown(`![Search options](${source})`, repo);
      expect(html).toContain(
        `src="/api/v1/repo/github/acme/widgets/markdown-image?source=${encodeURIComponent(source)}"`,
      );
    }
  });

  it("leaves GitHub images outside the repository on direct loading", async () => {
    const repo = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    };
    const sources = [
      "https://github.com/acme/other/blob/main/docs/images/search.png?raw=true",
      "https://raw.githubusercontent.com/acme/other/main/docs/images/search.png",
      "https://github.com/acme/widgets/tree/main/docs/images/search.png",
      "https://github.com/acme/widgets/blob/main",
      "https://gist.githubusercontent.com/acme/widgets/main/docs/images/search.png",
    ];

    for (const source of sources) {
      const html = await renderMarkdown(`![Search options](${source})`, repo);
      expect(html).toContain(`src="${source}"`);
      expect(html).not.toContain("markdown-image");
    }
  });

  it("proxies private GitLab upload images through the repo-scoped API", async () => {
    const source = "/uploads/0123456789abcdef/private-image.png";
    const canonicalSource = "https://gitlab.example.com/group/project/uploads/0123456789abcdef/private-image.png";
    const html = await renderMarkdown(`![Private image](${source})`, {
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group",
      name: "project",
      repoPath: "group/project",
    });

    expect(html).toContain(
      `src="/api/v1/host/gitlab.example.com/repo/gitlab/group/project/markdown-image?source=${encodeURIComponent(canonicalSource)}"`,
    );

    const fullSource = "https://gitlab.example.com/-/project/42/uploads/0123456789abcdef/private-image.png";
    const fullPathHtml = await renderMarkdown(`![Private image](${fullSource})`, {
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group",
      name: "project",
      repoPath: "group/project",
    });
    expect(fullPathHtml).toContain(
      `src="/api/v1/host/gitlab.example.com/repo/gitlab/group/project/markdown-image?source=${encodeURIComponent(fullSource)}"`,
    );
  });

  it("renders item references with the shared internal route and data attributes", async () => {
    const html = await renderMarkdown("See #12 and acme/tools#13", {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    });

    expect(html).toContain('class="item-ref" href="/issues/github/acme/widgets/12"');
    expect(html).toContain('data-platform-host="github.com"');
    expect(html).toContain('data-owner="acme"');
    expect(html).toContain('data-name="widgets"');
    expect(html).toContain('data-repo-path="acme/widgets"');
    expect(html).toContain('data-number="12"');
    expect(html).toContain('data-external-url="https://github.com/acme/widgets/issues/12"');
    expect(html).toContain('href="/issues/github/acme/tools/13"');
    expect(html).toContain('data-repo-path="acme/tools"');
    expect(html).toContain('data-external-url="https://github.com/acme/tools/issues/13"');
  });

  it("turns pasted provider item URLs into in-app item references", async () => {
    const html = await renderMarkdown(
      "See https://github.com/acme/widgets/pull/7 and [the fix](https://github.com/acme/tools/issues/13) but not https://github.com/acme/widgets/commit/abc or https://example.com/acme/widgets/pull/9",
      { provider: "github", platformHost: "github.com", owner: "acme", name: "widgets", repoPath: "acme/widgets" },
    );

    expect(html).toContain('class="item-ref" href="/pulls/github/acme/widgets/7"');
    expect(html).toContain('data-item-type="pr"');
    expect(html).toContain('data-external-url="https://github.com/acme/widgets/pull/7"');
    expect(html).toContain('class="item-ref" href="/issues/github/acme/tools/13"');
    expect(html).toContain(">the fix</a>");
    expect(html).toContain('href="https://github.com/acme/widgets/commit/abc"');
    expect(html).toContain('href="https://example.com/acme/widgets/pull/9"');
    expect(html).not.toContain('data-number="9"');
  });

  it("turns pasted gitlab merge request and issue URLs into item references", async () => {
    const html = await renderMarkdown(
      "See https://gitlab.example.com/group/sub/project/-/merge_requests/5 and https://gitlab.example.com/group/project/-/issues/6",
      {
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "group",
        name: "project",
        repoPath: "group/project",
      },
    );

    expect(html).toContain('href="/host/gitlab.example.com/pulls/gitlab/group%2Fsub/project/5"');
    expect(html).toContain('data-repo-path="group/sub/project"');
    expect(html).toContain('data-item-type="pr"');
    expect(html).toContain('href="/host/gitlab.example.com/issues/gitlab/group/project/6"');
  });

  it("renders gitlab issue and merge request references with provider fallback links", async () => {
    const html = await renderMarkdown("See #41 and group/project#42 and group/project!43 and !44", {
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group",
      name: "project",
      repoPath: "group/project",
    });

    expect(html).toContain('href="/host/gitlab.example.com/issues/gitlab/group/project/41"');
    expect(html).toContain('data-number="41" data-item-type="issue"');
    expect(html).toContain('href="/host/gitlab.example.com/issues/gitlab/group/project/42"');
    expect(html).toContain('data-number="42" data-item-type="issue"');
    expect(html).toContain('data-external-url="https://gitlab.example.com/group/project/-/issues/42"');
    expect(html).toContain('href="/host/gitlab.example.com/pulls/gitlab/group/project/43"');
    expect(html).toContain('data-item-type="pr"');
    expect(html).toContain('data-external-url="https://gitlab.example.com/group/project/-/merge_requests/43"');
    expect(html).toContain('href="/host/gitlab.example.com/pulls/gitlab/group/project/44"');
  });

  it("disambiguates overlapping gitlab issue and merge request numbers", async () => {
    const html = await renderMarkdown("See #10, !10, group/project#10, and group/project!10", {
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group",
      name: "project",
      repoPath: "group/project",
    });

    expect(html.match(/data-number="10" data-item-type="issue"/g)).toHaveLength(2);
    expect(html.match(/data-number="10" data-item-type="pr"/g)).toHaveLength(2);
    expect(html).toContain('data-external-url="https://gitlab.example.com/group/project/-/issues/10"');
    expect(html).toContain('data-external-url="https://gitlab.example.com/group/project/-/merge_requests/10"');
  });

  it("does not parse bang references outside GitLab repos", async () => {
    const html = await renderMarkdown("See acme/tools!13 and !14", {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    });

    expect(html).toContain("acme/tools!13");
    expect(html).toContain("!14");
    expect(html).not.toContain('data-item-type="pr"');
  });

  it("builds provider-canonical pull request fallback links", async () => {
    expect(
      buildCanonicalProviderItemURL({
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
        number: 12,
        itemType: "pr",
      }),
    ).toBe("https://github.com/acme/widgets/pull/12");
    expect(
      buildCanonicalProviderItemURL({
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "group",
        name: "project",
        repoPath: "group/project",
        number: 42,
        itemType: "pr",
      }),
    ).toBe("https://gitlab.example.com/group/project/-/merge_requests/42");
  });

  it("renders disabled checkboxes by default", async () => {
    const html = await renderMarkdown("- [ ] one\n- [x] two");
    expect(html).toContain('disabled=""');
    expect(html).not.toContain("data-task-index");
  });

  it("renders enabled checkboxes with sequential indices when interactiveTasks is set", async () => {
    const html = await renderMarkdown("- [ ] alpha\n- [x] beta\n- [ ] gamma", undefined, {
      interactiveTasks: true,
    });
    expect(html).not.toContain('disabled=""');
    expect(html).toContain('data-task-index="0"');
    expect(html).toContain('data-task-index="1"');
    expect(html).toContain('data-task-index="2"');
  });

  it("starts the task index at zero for every render", async () => {
    const opts = { interactiveTasks: true } as const;
    const first = await renderMarkdown("- [ ] a", undefined, opts);
    const second = await renderMarkdown("- [ ] b", undefined, opts);
    expect(first).toContain('data-task-index="0"');
    expect(second).toContain('data-task-index="0"');
  });

  it("preserves checked state when interactiveTasks is set", async () => {
    const html = await renderMarkdown("- [x] done", undefined, {
      interactiveTasks: true,
    });
    expect(html).toContain('checked=""');
  });

  it("caches interactive and non-interactive renders separately", async () => {
    const src = "- [ ] task";
    const plain = await renderMarkdown(src);
    const interactive = await renderMarkdown(src, undefined, {
      interactiveTasks: true,
    });
    expect(plain).toContain('disabled=""');
    expect(interactive).toContain('data-task-index="0"');
  });

  it("emits a drag handle and item-level data-task-index for interactive tasks", async () => {
    const html = await renderMarkdown("- [ ] a\n- [ ] b", undefined, {
      interactiveTasks: true,
    });
    expect(html).toContain('<li class="task-list-item task-list-item--interactive" data-task-index="0">');
    expect(html).toContain('<span class="task-drag-handle" data-task-index="0"');
    expect(html).toContain('<span class="task-drag-handle" data-task-index="1"');
    expect(html).toContain('draggable="true"');
  });

  it("does not emit drag handles in non-interactive mode", async () => {
    const html = await renderMarkdown("- [ ] a");
    expect(html).not.toContain("task-drag-handle");
    expect(html).not.toContain("draggable");
  });

  it("emits only one input per task item in interactive mode", async () => {
    const html = await renderMarkdown("- [ ] a", undefined, {
      interactiveTasks: true,
    });
    const matches = html.match(/<input/g) ?? [];
    expect(matches.length).toBe(1);
  });

  it("renders blockquoted task items as non-interactive even when interactiveTasks is set", async () => {
    // Source-side TASK_LINE doesn't match `> - [ ]` so the renderer
    // must NOT emit interactive checkboxes for them — otherwise
    // data-task-index would drift from the source helpers and
    // clicking would mutate the wrong line.
    const html = await renderMarkdown("> - [ ] inside blockquote\n\n- [ ] outside", undefined, {
      interactiveTasks: true,
    });
    // The blockquoted checkbox stays disabled with no data-task-index.
    expect(html).toMatch(/<blockquote>[\s\S]*<input disabled="" type="checkbox">[\s\S]*<\/blockquote>/);
    // The plain task outside the blockquote keeps interactivity at
    // index 0 (the blockquoted one didn't consume an index).
    expect(html).toContain('data-task-index="0"');
    expect(html).not.toContain('data-task-index="1"');
  });

  it("preserves per-listitem indices when task items are nested", async () => {
    // Each <li> and its drag handle MUST carry the same index as the
    // checkbox that lives directly inside that <li>. A nested child
    // must not leak its index back up to its parent's wrapper.
    const html = await renderMarkdown("- [ ] outer\n  - [ ] inner\n- [x] sibling", undefined, {
      interactiveTasks: true,
    });
    // The outer <li> wraps both the outer checkbox AND the nested
    // list — its data-task-index must match its OWN checkbox (0),
    // not the nested child's (1).
    expect(html).toContain('<li class="task-list-item task-list-item--interactive" data-task-index="0">');
    expect(html).toContain('<li class="task-list-item task-list-item--interactive" data-task-index="1">');
    expect(html).toContain('<li class="task-list-item task-list-item--interactive" data-task-index="2">');
    expect(html).toContain('<span class="task-drag-handle" data-task-index="0"');
    expect(html).toContain('<span class="task-drag-handle" data-task-index="1"');
    expect(html).toContain('<span class="task-drag-handle" data-task-index="2"');
    // Sanity-check pairing: the outer <li> contains the nested <li>
    // in its inner content, and the outer's drag handle precedes
    // the outer's checkbox.
    const outerOpen = html.indexOf('data-task-index="0"><span class="task-drag-handle" data-task-index="0"');
    expect(outerOpen).toBeGreaterThanOrEqual(0);
  });
});

describe("renderMarkdown mermaid diagrams", () => {
  it("renders mermaid fences as mermaid diagram targets", async () => {
    const html = await renderMarkdown("```mermaid\ngraph TD\n  A --> B\n```");

    expect(html).toContain('<pre class="mermaid">graph TD\n  A --&gt; B</pre>');
    expect(html).not.toContain("language-mermaid");
  });
});

describe("renderMarkdown code highlighting", () => {
  it("highlights fenced code blocks with Shiki", async () => {
    const html = await renderMarkdown('```ts\nconst value: string = "ok";\n```');

    expect(html).toContain('<pre class="shiki shiki-themes github-light-default github-dark-default"');
    expect(html).toContain("--shiki-light:");
    expect(html).toContain("--shiki-dark:");
    expect(html).toContain("const");
    expect(html).not.toContain("language-ts");
  });

  it("uses Shiki bundled languages beyond the app's common-language fixtures", async () => {
    const html = await renderMarkdown("```zig\nconst value: u8 = 1;\n```");

    expect(html).toContain('<pre class="shiki shiki-themes github-light-default github-dark-default"');
    expect(html).toContain("--shiki-light:");
    expect(html).toContain("--shiki-dark:");
    expect(html).toContain("value");
  });

  it("passes the fenced TOML language through to Shiki", async () => {
    const html = await renderMarkdown('```toml\nmodel_provider = "my-custom"\n```');

    expect(html).toContain('<pre class="shiki shiki-themes github-light-default github-dark-default"');
    expect(html).toContain("--shiki-light:");
    expect(html).toContain("--shiki-dark:");
    expect(html).toContain("model_provider");
    expect(html).not.toContain("language-toml");
  });

  it("strips user-authored inline styles while preserving Shiki theme variables", async () => {
    const html = await renderMarkdown(
      '<span style="position:fixed;color:red">raw</span>\n\n```ts\nconst value = 1;\n```',
    );

    expect(html).toContain("<span>raw</span>");
    expect(html).not.toContain("position:fixed");
    expect(html).not.toContain("color:red");
    expect(html).toContain("--shiki-light:");
    expect(html).toContain("--shiki-dark:");
  });

  it("strips styles from raw HTML that forges Shiki class names", async () => {
    const html = await renderMarkdown(
      [
        '<pre class="shiki" style="--shiki-light:#000000;--shiki-dark:#000000">',
        '<span style="--shiki-light:#000000;--shiki-dark:#000000">forged</span>',
        "</pre>",
        "",
        "```ts",
        "const value = 1;",
        "```",
      ].join("\n"),
    );

    expect(html).toContain('<pre class="shiki">');
    expect(html).toContain("<span>forged</span>");
    expect(html).not.toContain("--shiki-light:#000000");
    expect(html).not.toContain("--shiki-dark:#000000");
    expect(html).not.toContain("data-kenn-forge-shiki");
    expect(html).toContain("--shiki-light:");
    expect(html).toContain("--shiki-dark:");
  });

  it("keeps synchronous block rendering explicitly unhighlighted after Shiki has loaded", async () => {
    await renderMarkdown("```ts\nconst value = 1;\n```");

    const blocks = renderMarkdownBlocks("```ts\nconst value = 1;\n```");

    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.html).toContain("<pre><code>");
    expect(blocks[0]?.html).not.toContain("shiki");
    expect(blocks[0]?.html).toContain("const value = 1;");
  });

  it("falls back to escaped plain text for unknown fence languages", async () => {
    const html = await renderMarkdown("```not-a-real-language\n<script>alert(1)</script>\n```");

    expect(html).toContain('<pre class="shiki shiki-themes github-light-default github-dark-default"');
    expect(html).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
  });

  it("falls back to plain code blocks after the per-render highlighted fence budget", async () => {
    const source = Array.from({ length: 21 }, (_, index) => `\`\`\`ts\nconst value${index} = ${index};\n\`\`\``).join(
      "\n\n",
    );

    const html = await renderMarkdown(source);

    expect(html.match(/<pre class="shiki/g)).toHaveLength(20);
    expect(html.match(/<pre><code>/g)).toHaveLength(1);
    expect(html).toContain("const value20 = 20;");
  });

  it("falls back to plain code blocks after the per-render language budget", async () => {
    const languages = ["bash", "css", "diff", "go", "html", "json", "python", "rust", "ruby"];
    const source = languages.map((lang, index) => `\`\`\`${lang}\nvalue_${index}\n\`\`\``).join("\n\n");

    const html = await renderMarkdown(source);

    expect(html.match(/<pre class="shiki/g)).toHaveLength(8);
    expect(html.match(/<pre><code>/g)).toHaveLength(1);
    expect(html).toContain("value_8");
  });
});

describe("renderMarkdown details blocks", () => {
  const source = [
    "<details open>",
    "",
    "<summary>Tips for collapsed sections</summary>",
    "",
    "### You can add a header",
    "",
    "You can add text within a collapsed section.",
    "",
    "```ruby",
    'puts "Hello World"',
    "```",
    "",
    "</details>",
  ].join("\n");

  it("preserves GitHub-style details blocks with rendered markdown inside", async () => {
    const html = await renderMarkdown(source);

    expect(html).toContain("<details");
    expect(html).toContain('open=""');
    expect(html).toContain("<summary>Tips for collapsed sections</summary>");
    expect(html).toContain("<h3>You can add a header</h3>");
    expect(html).toContain("<p>You can add text within a collapsed section.</p>");
    expect(html).toContain('<pre class="shiki shiki-themes github-light-default github-dark-default"');
    expect(html).toContain("puts");
    expect(html).toContain('"Hello World"');
    expect(html).toContain("</details>");
  });

  it("keeps details blocks as one rendered block for rich markdown previews", async () => {
    const blocks = renderMarkdownBlocks(source);

    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.startLine).toBe(1);
    expect(blocks[0]?.endLine).toBe(13);
    expect(blocks[0]?.html).toContain("<details");
    expect(blocks[0]?.html).toContain("<summary>Tips for collapsed sections</summary>");
    expect(blocks[0]?.html).toContain("<h3>You can add a header</h3>");
    expect(blocks[0]?.html).toContain("</details>");
  });

  it("does not treat details tags inside fenced code as block boundaries", async () => {
    const blocks = renderMarkdownBlocks(
      [
        "<details>",
        "",
        "<summary>Markup sample</summary>",
        "",
        "```html",
        "</details>",
        "```",
        "",
        "Still inside the collapsed section.",
        "",
        "</details>",
        "",
        "Afterwards.",
      ].join("\n"),
    );

    expect(blocks).toHaveLength(2);
    expect(blocks[0]?.html).toContain("<details>");
    expect(blocks[0]?.html).toContain("&lt;/");
    expect(blocks[0]?.html).toContain("details");
    expect(blocks[0]?.html).toContain("&gt;");
    expect(blocks[0]?.html).toContain("<p>Still inside the collapsed section.</p>");
    expect(blocks[0]?.html).toContain("</details>");
    expect(blocks[0]?.html).not.toContain("Afterwards.");
    expect(blocks[1]?.html).toContain("<p>Afterwards.</p>");
  });
});

describe("renderMarkdown line breaks", () => {
  it("renders single newlines as hard breaks by default", async () => {
    const html = await renderMarkdown("first line\nsecond line\n\nnext paragraph");
    expect(html).toContain("first line<br>second line");
    expect(html.match(/<p>/g)).toHaveLength(2);
  });

  it("joins single newlines into the paragraph when collapsing soft breaks", async () => {
    const html = await renderMarkdown("first line\nsecond line\n\nnext paragraph", undefined, {
      collapseSingleLineBreaks: true,
    });
    expect(html).not.toContain("<br>");
    expect(html).toContain("first line\nsecond line");
    expect(html.match(/<p>/g)).toHaveLength(2);
  });

  it("keeps collapsed and hard-break renders in separate caches", async () => {
    const raw = "cache me\nplease";
    const hard = await renderMarkdown(raw);
    const soft = await renderMarkdown(raw, undefined, { collapseSingleLineBreaks: true });
    expect(hard).toContain("<br>");
    expect(soft).not.toContain("<br>");
    expect(renderMarkdownSync(raw)).toContain("<br>");
    expect(renderMarkdownSync(raw, undefined, { collapseSingleLineBreaks: true })).not.toContain("<br>");
  });
});

describe("markdown video", () => {
  const githubRepo: RepoContext = {
    provider: "github",
    platformHost: "github.com",
    owner: "acme",
    name: "widgets",
    repoPath: "acme/widgets",
  };
  const gitlabRepo: RepoContext = {
    provider: "gitlab",
    platformHost: "gitlab.com",
    owner: "group",
    name: "project",
    repoPath: "group/project",
  };
  const attachment = "https://github.com/user-attachments/assets/a1";
  const githubMediaSrc = (source: string) =>
    `src="/api/v1/repo/github/acme/widgets/markdown-media?source=${encodeURIComponent(source)}"`;

  function videos(html: string): HTMLVideoElement[] {
    const container = document.createElement("div");
    container.innerHTML = html;
    return [...container.querySelectorAll("video")];
  }

  it("keeps a raw GitHub attachment video as a proxied player without autoplay", () => {
    const html = renderMarkdownSync(`<video src="${attachment}" autoplay></video>`, githubRepo);

    const [video, ...rest] = videos(html);
    expect(rest).toHaveLength(0);
    expect(video?.classList.contains("markdown-video")).toBe(true);
    expect(video?.hasAttribute("controls")).toBe(true);
    expect(video?.getAttribute("preload")).toBe("metadata");
    expect(video?.hasAttribute("autoplay")).toBe(false);
    expect(html).toContain(githubMediaSrc(attachment));
  });

  it("keeps a raw GitHub video whose source child is an attachment", () => {
    const html = renderMarkdownSync(`<video controls><source src="${attachment}"></video>`, githubRepo);

    expect(videos(html)).toHaveLength(1);
    expect(html).toContain(githubMediaSrc(attachment));
  });

  it("drops only a raw GitHub video from another host", () => {
    const html = renderMarkdownSync(
      `<p>before</p>\n<video src="https://cdn.example.com/x.mp4"></video>\n<p>after</p>`,
      githubRepo,
    );

    expect(videos(html)).toHaveLength(0);
    expect(html).toContain("<p>before</p>");
    expect(html).toContain("<p>after</p>");
    expect(html).not.toContain("cdn.example.com");
  });

  it("plays a standalone GitHub attachment link once the probe says it is a video", () => {
    const html = renderMarkdownSync(`Intro\n\n${attachment}\n\nOutro`, githubRepo, {
      mediaOutcomes: new Map([[attachment, "video"]]),
    });

    expect(videos(html)).toHaveLength(1);
    expect(html).toContain(githubMediaSrc(attachment));
    expect(html).toContain("Intro");
    expect(html).toContain("Outro");
  });

  it.each([
    ["no probe result", undefined],
    ["a non-video probe result", "link" as const],
    ["an unknown probe result", "unknown" as const],
  ])("renders a standalone GitHub attachment as a link with %s", (_label, outcome) => {
    const html = renderMarkdownSync(attachment, githubRepo, {
      mediaOutcomes: outcome ? new Map([[attachment, outcome]]) : undefined,
    });

    expect(videos(html)).toHaveLength(0);
    expect(html).toContain(`<a href="${attachment}"`);
  });

  it.each([
    ["after text on the previous line", `text\n${attachment}`],
    ["in a list item", `- ${attachment}\n- more`],
    ["in a quoted paragraph with more text", `> ${attachment}\n> more`],
    ["as labelled link text", `[demo](${attachment})`],
  ])("keeps an attachment link %s", (_label, markdown) => {
    const html = renderMarkdownSync(markdown, githubRepo, { mediaOutcomes: new Map([[attachment, "video"]]) });

    expect(videos(html)).toHaveLength(0);
    expect(html).toContain(`href="${attachment}"`);
  });

  it("lists only standalone attachment paragraphs as probe candidates", () => {
    const other = "https://github.com/user-attachments/assets/b2";
    const ownRepoAsset = "https://github.com/acme/widgets/assets/12/0f8e1a52-3c55-4d39-8a43-7c1f0b5d9e21";
    const foreignRepoAsset = "https://github.com/other/repo/assets/12/0f8e1a52-3c55-4d39-8a43-7c1f0b5d9e21";

    expect(
      githubAttachmentParagraphSources(
        `${attachment}\n\ntext ${other}\n\n${ownRepoAsset}\n\n${foreignRepoAsset}`,
        githubRepo,
      ),
    ).toEqual([attachment, ownRepoAsset]);
  });

  it("plays GitLab image syntax that names a video upload", () => {
    const upload = "https://gitlab.com/group/project/uploads/abc/DEMO.MOV";
    const html = renderMarkdownSync("![demo](/uploads/abc/DEMO.MOV)", gitlabRepo);

    expect(videos(html)).toHaveLength(1);
    expect(html).toContain(
      `src="/api/v1/repo/gitlab/group/project/markdown-media?source=${encodeURIComponent(upload)}"`,
    );
    expect(html).not.toContain("<img");
  });

  it("keeps GitLab image syntax for non-video uploads as an image", () => {
    const html = renderMarkdownSync("![d](/uploads/abc/diagram.png)", gitlabRepo);

    expect(videos(html)).toHaveLength(0);
    expect(html).toContain("<img");
  });

  it("removes raw GitLab video tags", () => {
    const html = renderMarkdownSync(
      '<video src="https://gitlab.com/group/project/uploads/abc/demo.mp4"></video>',
      gitlabRepo,
    );

    expect(videos(html)).toHaveLength(0);
  });

  it("resolves a Gitea repository attachment against the repository", () => {
    const html = renderMarkdownSync('<video src="attachments/u1" controls></video>', {
      provider: "gitea",
      platformHost: "gitea.example.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    });

    expect(videos(html)[0]?.getAttribute("src")).toBe("https://gitea.example.com/acme/widgets/attachments/u1");
  });

  it.each([
    ["the configured host", "codeberg.example", "https://codeberg.example/attachments/u1"],
    ["the provider default host", undefined, "https://codeberg.org/attachments/u1"],
  ])("resolves a Forgejo host attachment against %s", (_label, platformHost, expected) => {
    const html = renderMarkdownSync('<video src="/attachments/u1" controls></video>', {
      provider: "forgejo",
      platformHost,
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    });

    expect(videos(html)[0]?.getAttribute("src")).toBe(expected);
  });

  it("removes raw Bitbucket video tags", () => {
    const html = renderMarkdownSync('<video src="https://bitbucket.org/x.mp4"></video>', {
      provider: "bitbucket",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    });

    expect(videos(html)).toHaveLength(0);
  });

  it("keeps raw video without a repository but never autoplays it", () => {
    const html = renderMarkdownSync('<video src="https://cdn.example.com/x.mp4" autoplay loop></video>');

    const [video] = videos(html);
    expect(video?.getAttribute("src")).toBe("https://cdn.example.com/x.mp4");
    expect(video?.hasAttribute("autoplay")).toBe(false);
    expect(video?.hasAttribute("controls")).toBe(true);
  });

  it("treats a forged player marker as raw markup", () => {
    const html = renderMarkdownSync(
      '<video data-kenn-forge-media="guess" src="https://cdn.example.com/x.mp4"></video>',
      githubRepo,
    );

    expect(videos(html)).toHaveLength(0);
    expect(html).not.toContain("data-kenn-forge-media");
  });

  it("probes a standalone attachment and renders the confirmed video as a player", async () => {
    const source = "https://github.com/user-attachments/assets/effect-render-probe";
    const requested: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = new Request(input, init);
        requested.push(new URL(request.url).searchParams.get("source") ?? "");
        return new Response(new Uint8Array([0]), { status: 206, headers: { "Content-Type": "video/mp4" } });
      }),
    );
    try {
      const html = await Effect.runPromise(renderMarkdownEffect(`Clip:\n\n${source}`, githubRepo));

      expect(requested).toEqual([source]);
      expect(videos(html)).toHaveLength(1);
      expect(html).toContain(githubMediaSrc(source));
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("does not reuse a cached link once a later probe confirms the video", async () => {
    const source = "https://github.com/user-attachments/assets/cache-key-check";
    const markdown = `Clip:\n\n${source}`;

    const before = await renderMarkdown(markdown, githubRepo, { mediaOutcomes: new Map([[source, "unknown"]]) });
    const after = await renderMarkdown(markdown, githubRepo, { mediaOutcomes: new Map([[source, "video"]]) });

    expect(videos(before)).toHaveLength(0);
    expect(videos(after)).toHaveLength(1);
  });

  it("never leaks the player marker into output", () => {
    const html = renderMarkdownSync(attachment, githubRepo, { mediaOutcomes: new Map([[attachment, "video"]]) });

    expect(videos(html)).toHaveLength(1);
    expect(html).not.toContain("data-kenn-forge-media");
  });
});
