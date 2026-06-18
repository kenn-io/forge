# Rich Preview Review Card Anchoring Implementation Plan

> **Status:** Implemented on the rich-preview review-card branch. This plan is kept as historical implementation context; completed checkboxes describe work already done, not new pending tasks.
> RED/GREEN notes below describe the original implementation workflow at the time each step was executed. They are not predictions about the current repository state.

**Goal:** Replace fragment-rendered Markdown rich preview with a source-line-aware render model that preserves Markdown semantics and anchors review cards in unified and split modes.

**Architecture:** Add a pure Markdown rich-preview model under `packages/ui/src/utils/` that builds old/new Markdown documents from diff hunks, maps generated Markdown lines back to source diff lines, renders top-level Markdown tokens through the canonical renderer, and produces unified/split block HTML. `DiffRichPreview.svelte` consumes that model and only assigns/render review cards against exact mapped source-line sets; start/end ranges are display metadata and do not imply that hidden hunk-gap lines are contained. Structured Markdown containers anchor at the top-level token boundary so cards do not become invalid list/table/blockquote children. Synthetic hunk separators are parser-only, have no source mapping, are stripped from spanning tokens before rendering, and are not rendered or aligned as preview blocks. Stripped spanning-token renders carry in-document reference definitions as parser context. User-authored `---` lines keep their source mapping and render normally. The model uses a bounded block comparison strategy for large Markdown diffs.

**Tech Stack:** Svelte 5, TypeScript, Marked, DOMPurify, existing `renderMarkdownDiff` and `renderMarkdownSplitDiff`, Vite+/Vitest, Playwright.

---

### Task 1: Add Failing Model Tests

**Files:**

- Create: `packages/ui/src/utils/markdown-rich-preview.test.ts`

- [x] **Step 1: Write failing tests**

Add tests for the wished-for API:

```ts
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
});
```

- [x] **Step 2: Run tests and verify RED**

Run: `cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/utils/markdown-rich-preview.test.ts`

Expected: FAIL because `./markdown-rich-preview.js` does not exist.

### Task 2: Implement The Pure Rich Preview Model

**Files:**

- Create: `packages/ui/src/utils/markdown-rich-preview.ts`
- Modify: `packages/ui/src/utils/markdown.ts`

- [x] **Step 1: Expose canonical block rendering from Markdown utilities**

Add an exported `renderMarkdownBlocks(raw, repo)` helper in `markdown.ts`. It should use the existing `getMarked(repo)` instance, lexer, renderer state, and DOMPurify attributes. It returns visible top-level token blocks with generated Markdown line ranges and sanitized HTML. Reference definitions inside the reconstructed hunk document must keep resolving through this helper.

- [x] **Step 2: Build `buildMarkdownRichPreview`**

Create `markdown-rich-preview.ts` with:

```ts
export interface MarkdownRichPreviewBlock {
  key: string;
  oldStart?: number | undefined;
  oldEnd?: number | undefined;
  oldLines?: number[] | undefined;
  newStart?: number | undefined;
  newEnd?: number | undefined;
  newLines?: number[] | undefined;
  unifiedHtml: string;
  beforeHtml?: string | undefined;
  afterHtml?: string | undefined;
}

export interface MarkdownRichPreview {
  blocks: MarkdownRichPreviewBlock[];
}
```

The function builds old/new side documents from diff hunk lines, renders canonical token blocks, aligns equal blocks with an LCS pass below a block-product threshold, falls back to coarse delete/insert projection above that threshold, pairs adjacent delete/insert runs by order, and uses `renderMarkdownDiff` plus `renderMarkdownSplitDiff` for each output block.

- [x] **Step 3: Run model tests and verify GREEN**

Run: `cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/utils/markdown-rich-preview.test.ts`

Expected: PASS.

### Task 3: Wire The Model Into DiffRichPreview

**Files:**

- Modify: `packages/ui/src/components/diff/DiffRichPreview.svelte`
- Modify: `packages/ui/src/components/diff/DiffFile.svelte`

- [x] **Step 1: Remove fragment-rendering helpers**

Delete `markdownLineBlocks`, `pushMarkdownLineBlock`, `isFenceLine`, and block-local rendering from `DiffRichPreview.svelte`.

- [x] **Step 2: Consume `buildMarkdownRichPreview`**

Add a `$derived.by` value that calls `buildMarkdownRichPreview(file, { provider, platformHost, owner, name, repoPath })` for Markdown files. Assign review threads to model blocks by `reviewThreadTargetSide()` and `reviewThreadTargetLine()`. Treat file-level, stale-head, and unassigned threads as fallback file-level cards.

- [x] **Step 3: Render unified and split block streams**

Unified mode renders each block's `unifiedHtml` followed by assigned review cards. Split mode renders rows with before/after HTML and places cards in the side pane matching the review target side. Fallback cards render in a separated stack before the block stream.

### Task 4: Add Failing Component And E2E Coverage, Then Make It Green

**Files:**

- Modify: `packages/ui/src/utils/markdown-rich-preview.test.ts`
- Modify: `packages/ui/src/components/diff/DiffFile.test.ts`
- Modify: `frontend/tests/e2e-full/diff-view.spec.ts`

- [x] **Step 1: Add model tests**

Add tests proving synthetic separators stay hidden for standalone fenced-code, HTML-block, blockquote, list, and table cases, stripped spanning-token renders keep in-document reference definitions available through the approved parser-context API, user-authored thematic breaks remain visible, untargeted lists remain whole rendered lists, split-line inputs expose separate rich-preview anchor blocks for targeted list items, targeted loose lists preserve paragraph-wrapped item subtrees, and review threads targeting hidden hunk gaps fall back instead of anchoring to a spanning block's display range.

- [x] **Step 2: Add component tests**

Add tests proving split mode does not dump line comments at the top, comments preserve source order, list-item review cards render after the matching item rather than after the whole list, and added/deleted list-item review cards keep unchanged sibling items aligned at middle and edge positions, including lazy multiline changed items.

- [x] **Step 3: Run component tests and verify RED before production wiring if not already red**

Run focused tests with `cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/components/diff/DiffFile.test.ts -t "markdown rich preview"`

- [x] **Step 4: Run component tests and verify GREEN**

Run: `cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/components/diff/DiffFile.test.ts`

- [x] **Step 5: Update Playwright assertion**

Extend the existing rich-preview review-card e2e case to assert the card has a rendered Markdown block immediately before it and is not the first child of the preview.

- [x] **Step 6: Add multi-card and hidden-gap fallback e2e coverage**

Add a diff-view e2e case with a multi-hunk Markdown block and a review thread targeting a hidden source gap, proving the card remains a file-level fallback instead of rendering inside `.markdown-rich-diff--unified`.
Add another diff-view e2e case with multiple list-item review threads returned out of source order, proving rich preview renders the cards in source order, anchors each card after the matching rendered item, and does not add synthetic split-list margins.
Add browser coverage proving rich preview side-by-side panes render changed text without native `<ins>`/`<del>` underlines or strike-through styling while keeping an inline background cue for word-level changes, and that split rich preview uses wide file-pane space rather than the unified prose-width cap.

### Task 5: Styling, Validation, And Commit

**Files:**

- Modify: `packages/ui/src/components/diff/DiffRichPreview.svelte`

- [x] **Step 1: Quiet block-level diff styling**

Change rich-preview CSS so `ins.markdown-diff__block` and `del.markdown-diff__block` use block background/border without text underline. Keep inline `ins`/`del` background styling for non-block changes in unified and split modes, and let split mode expand to the available file width.

- [x] **Step 2: Run Svelte validation**

Run `vp exec svelte-mcp svelte-autofixer packages/ui/src/components/diff/DiffRichPreview.svelte --svelte-version 5` and the same for `DiffFile.svelte`. If the helper exits silently, run `node node_modules/vite-plus/bin/vp run ui-package-check`.

- [x] **Step 3: Run verification**

Run:

```bash
(cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/utils/markdown-rich-preview.test.ts)
(cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/components/diff/DiffFile.test.ts)
node node_modules/vite-plus/bin/vp run ui-package-check
(cd frontend && node node_modules/.bin/playwright test --config=playwright-e2e.config.ts tests/e2e-full/diff-view.spec.ts -g "rich preview shows review thread cards")
(cd frontend && node node_modules/.bin/playwright test --config=playwright-e2e.config.ts tests/e2e-full/diff-view.spec.ts -g "rich preview anchors multiple list review cards")
(cd frontend && node node_modules/.bin/playwright test --config=playwright-e2e.config.ts tests/e2e-full/diff-view.spec.ts -g "rich preview keeps untargeted lists intact")
(cd frontend && node node_modules/.bin/playwright test --config=playwright-e2e.config.ts tests/e2e-full/diff-view.spec.ts -g "rich preview keeps unchanged list siblings aligned")
(cd frontend && node node_modules/.bin/playwright test --config=playwright-e2e.config.ts tests/e2e-full/diff-view.spec.ts -g "rich preview side-by-side panes do not underline")
(cd frontend && node node_modules/.bin/playwright test --config=playwright-e2e.config.ts tests/e2e-full/diff-view.spec.ts -g "rich preview keeps hidden hunk-gap review threads")
git diff --check
```

- [x] **Step 4: Commit**

Commit the implementation with a rationale-first conventional message, then push the existing PR branch.
