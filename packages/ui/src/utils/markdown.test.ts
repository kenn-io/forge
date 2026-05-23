import { describe, expect, it } from "vitest";
import { buildCanonicalProviderItemURL } from "./item-reference.js";
import { renderMarkdown } from "./markdown.js";

describe("renderMarkdown task lists", () => {
  it("renders item references with the shared internal route and data attributes", () => {
    const html = renderMarkdown("See #12 and acme/tools#13", {
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

  it("renders gitlab issue and merge request references with provider fallback links", () => {
    const html = renderMarkdown("See #41 and group/project#42 and group/project!43 and !44", {
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group",
      name: "project",
      repoPath: "group/project",
    });

    expect(html).toContain(
      'href="/host/gitlab.example.com/issues/gitlab/group/project/41"',
    );
    expect(html).toContain(
      'data-number="41" data-item-type="issue"',
    );
    expect(html).toContain(
      'href="/host/gitlab.example.com/issues/gitlab/group/project/42"',
    );
    expect(html).toContain(
      'data-number="42" data-item-type="issue"',
    );
    expect(html).toContain(
      'data-external-url="https://gitlab.example.com/group/project/-/issues/42"',
    );
    expect(html).toContain(
      'href="/host/gitlab.example.com/pulls/gitlab/group/project/43"',
    );
    expect(html).toContain('data-item-type="pr"');
    expect(html).toContain(
      'data-external-url="https://gitlab.example.com/group/project/-/merge_requests/43"',
    );
    expect(html).toContain(
      'href="/host/gitlab.example.com/pulls/gitlab/group/project/44"',
    );
  });

  it("disambiguates overlapping gitlab issue and merge request numbers", () => {
    const html = renderMarkdown("See #10, !10, group/project#10, and group/project!10", {
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group",
      name: "project",
      repoPath: "group/project",
    });

    expect(html.match(/data-number="10" data-item-type="issue"/g)).toHaveLength(2);
    expect(html.match(/data-number="10" data-item-type="pr"/g)).toHaveLength(2);
    expect(html).toContain(
      'data-external-url="https://gitlab.example.com/group/project/-/issues/10"',
    );
    expect(html).toContain(
      'data-external-url="https://gitlab.example.com/group/project/-/merge_requests/10"',
    );
  });

  it("does not parse bang references outside GitLab repos", () => {
    const html = renderMarkdown("See acme/tools!13 and !14", {
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

  it("builds provider-canonical pull request fallback links", () => {
    expect(buildCanonicalProviderItemURL({
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 12,
      itemType: "pr",
    })).toBe("https://github.com/acme/widgets/pull/12");
    expect(buildCanonicalProviderItemURL({
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group",
      name: "project",
      repoPath: "group/project",
      number: 42,
      itemType: "pr",
    })).toBe("https://gitlab.example.com/group/project/-/merge_requests/42");
  });

  it("renders disabled checkboxes by default", () => {
    const html = renderMarkdown("- [ ] one\n- [x] two");
    expect(html).toContain('disabled=""');
    expect(html).not.toContain("data-task-index");
  });

  it("renders enabled checkboxes with sequential indices when interactiveTasks is set", () => {
    const html = renderMarkdown(
      "- [ ] alpha\n- [x] beta\n- [ ] gamma",
      undefined,
      { interactiveTasks: true },
    );
    expect(html).not.toContain('disabled=""');
    expect(html).toContain('data-task-index="0"');
    expect(html).toContain('data-task-index="1"');
    expect(html).toContain('data-task-index="2"');
  });

  it("starts the task index at zero for every render", () => {
    const opts = { interactiveTasks: true } as const;
    const first = renderMarkdown("- [ ] a", undefined, opts);
    const second = renderMarkdown("- [ ] b", undefined, opts);
    expect(first).toContain('data-task-index="0"');
    expect(second).toContain('data-task-index="0"');
  });

  it("preserves checked state when interactiveTasks is set", () => {
    const html = renderMarkdown("- [x] done", undefined, {
      interactiveTasks: true,
    });
    expect(html).toContain('checked=""');
  });

  it("caches interactive and non-interactive renders separately", () => {
    const src = "- [ ] task";
    const plain = renderMarkdown(src);
    const interactive = renderMarkdown(src, undefined, {
      interactiveTasks: true,
    });
    expect(plain).toContain('disabled=""');
    expect(interactive).toContain('data-task-index="0"');
  });

  it("emits a drag handle and item-level data-task-index for interactive tasks", () => {
    const html = renderMarkdown("- [ ] a\n- [ ] b", undefined, {
      interactiveTasks: true,
    });
    expect(html).toContain(
      '<li class="task-list-item task-list-item--interactive" data-task-index="0">',
    );
    expect(html).toContain(
      '<span class="task-drag-handle" data-task-index="0"',
    );
    expect(html).toContain(
      '<span class="task-drag-handle" data-task-index="1"',
    );
    expect(html).toContain('draggable="true"');
  });

  it("does not emit drag handles in non-interactive mode", () => {
    const html = renderMarkdown("- [ ] a");
    expect(html).not.toContain("task-drag-handle");
    expect(html).not.toContain("draggable");
  });

  it("emits only one input per task item in interactive mode", () => {
    const html = renderMarkdown("- [ ] a", undefined, {
      interactiveTasks: true,
    });
    const matches = html.match(/<input/g) ?? [];
    expect(matches.length).toBe(1);
  });

  it("renders blockquoted task items as non-interactive even when interactiveTasks is set", () => {
    // Source-side TASK_LINE doesn't match `> - [ ]` so the renderer
    // must NOT emit interactive checkboxes for them — otherwise
    // data-task-index would drift from the source helpers and
    // clicking would mutate the wrong line.
    const html = renderMarkdown(
      "> - [ ] inside blockquote\n\n- [ ] outside",
      undefined,
      { interactiveTasks: true },
    );
    // The blockquoted checkbox stays disabled with no data-task-index.
    expect(html).toMatch(
      /<blockquote>[\s\S]*<input disabled="" type="checkbox">[\s\S]*<\/blockquote>/,
    );
    // The plain task outside the blockquote keeps interactivity at
    // index 0 (the blockquoted one didn't consume an index).
    expect(html).toContain('data-task-index="0"');
    expect(html).not.toContain('data-task-index="1"');
  });

  it("preserves per-listitem indices when task items are nested", () => {
    // Each <li> and its drag handle MUST carry the same index as the
    // checkbox that lives directly inside that <li>. A nested child
    // must not leak its index back up to its parent's wrapper.
    const html = renderMarkdown(
      "- [ ] outer\n  - [ ] inner\n- [x] sibling",
      undefined,
      { interactiveTasks: true },
    );
    // The outer <li> wraps both the outer checkbox AND the nested
    // list — its data-task-index must match its OWN checkbox (0),
    // not the nested child's (1).
    expect(html).toContain(
      '<li class="task-list-item task-list-item--interactive" data-task-index="0">',
    );
    expect(html).toContain(
      '<li class="task-list-item task-list-item--interactive" data-task-index="1">',
    );
    expect(html).toContain(
      '<li class="task-list-item task-list-item--interactive" data-task-index="2">',
    );
    expect(html).toContain(
      '<span class="task-drag-handle" data-task-index="0"',
    );
    expect(html).toContain(
      '<span class="task-drag-handle" data-task-index="1"',
    );
    expect(html).toContain(
      '<span class="task-drag-handle" data-task-index="2"',
    );
    // Sanity-check pairing: the outer <li> contains the nested <li>
    // in its inner content, and the outer's drag handle precedes
    // the outer's checkbox.
    const outerOpen = html.indexOf(
      'data-task-index="0"><span class="task-drag-handle" data-task-index="0"',
    );
    expect(outerOpen).toBeGreaterThanOrEqual(0);
  });
});
