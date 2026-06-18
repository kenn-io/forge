# Rich Preview Review Card Anchoring Implementation Plan

> **Status:** Implemented on the rich-preview review-card branch. This plan is kept as historical implementation context; completed checkboxes describe work already done, not new pending tasks.
> RED/GREEN notes below describe the original implementation workflow at the time each step was executed. They are not predictions about the current repository state.

**Goal:** Replace fragment-rendered Markdown rich preview with a source-line-aware render model that preserves Markdown semantics and anchors review cards in unified and split modes.

**Architecture:** Add a pure Markdown rich-preview model under `packages/ui/src/utils/` that builds old/new Markdown documents from diff hunks, maps generated Markdown lines back to source diff lines, renders top-level Markdown tokens through the canonical renderer, and produces unified/split block HTML. `DiffRichPreview.svelte` consumes that model and only assigns/render review cards against explicit block ranges. Structured Markdown containers anchor at the top-level token boundary so cards do not become invalid list/table/blockquote children. Synthetic hunk separators are parser-only, have no source mapping, are stripped from spanning tokens before rendering, and are not rendered or aligned as preview blocks. User-authored `---` lines keep their source mapping and render normally. The model uses a bounded block comparison strategy for large Markdown diffs.

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
  newStart?: number | undefined;
  newEnd?: number | undefined;
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

- Modify: `packages/ui/src/components/diff/DiffFile.test.ts`
- Modify: `frontend/tests/e2e-full/diff-view.spec.ts`

- [x] **Step 1: Add component tests**

Add tests proving Markdown semantics survive anchored cards, split mode does not dump line comments at the top, synthetic separators stay hidden for standalone and spanning-token cases, and user-authored thematic breaks remain visible.

- [x] **Step 2: Run component tests and verify RED before production wiring if not already red**

Run focused tests with `cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/components/diff/DiffFile.test.ts -t "markdown rich preview"`

- [x] **Step 3: Run component tests and verify GREEN**

Run: `cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/components/diff/DiffFile.test.ts`

- [x] **Step 4: Update Playwright assertion**

Extend the existing rich-preview review-card e2e case to assert the card has a rendered Markdown block immediately before it and is not the first child of the preview.

### Task 5: Styling, Validation, And Commit

**Files:**

- Modify: `packages/ui/src/components/diff/DiffRichPreview.svelte`

- [x] **Step 1: Quiet block-level diff styling**

Change rich-preview CSS so `ins.markdown-diff__block` and `del.markdown-diff__block` use block background/border without text underline. Keep inline `ins`/`del` styling for non-block changes.

- [x] **Step 2: Run Svelte validation**

Run `vp exec svelte-mcp svelte-autofixer packages/ui/src/components/diff/DiffRichPreview.svelte --svelte-version 5` and the same for `DiffFile.svelte`. If the helper exits silently, run `node node_modules/vite-plus/bin/vp run ui-package-check`.

- [x] **Step 3: Run verification**

Run:

```bash
(cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/utils/markdown-rich-preview.test.ts)
(cd frontend && node ../node_modules/vite-plus/bin/vp test run ../packages/ui/src/components/diff/DiffFile.test.ts)
node node_modules/vite-plus/bin/vp run ui-package-check
(cd frontend && node node_modules/.bin/playwright test --config=playwright-e2e.config.ts tests/e2e-full/diff-view.spec.ts -g "rich preview shows review thread cards")
git diff --check
```

- [x] **Step 4: Commit**

Commit the implementation with a rationale-first conventional message, then push the existing PR branch.
