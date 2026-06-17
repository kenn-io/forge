import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vite-plus/test";

// jsdom does not ship IntersectionObserver; install a stub that reports the
// observed element as visible immediately so the viewport-gated render effect
// actually runs under test. The original global (if any) is saved and restored after
// the suite so it does not leak into sibling test files.
type GlobalWithIO = { IntersectionObserver?: unknown };
type GlobalWithResizeObserver = { ResizeObserver?: unknown };
type GlobalWithCSSStyleSheet = {
  CSSStyleSheet?: {
    prototype: CSSStyleSheet & { replaceSync?: (text: string) => void };
  };
};
let originalIntersectionObserver: unknown;
let originalIntersectionObserverExisted = false;
let originalResizeObserver: unknown;
let originalResizeObserverExisted = false;
let originalReplaceSync: unknown;

beforeAll(() => {
  originalIntersectionObserverExisted = "IntersectionObserver" in globalThis;
  originalIntersectionObserver = (globalThis as GlobalWithIO).IntersectionObserver;
  class IntersectionObserverStub {
    private readonly callback: IntersectionObserverCallback;
    root: Element | null = null;
    rootMargin = "";
    thresholds: readonly number[] = [];
    constructor(callback: IntersectionObserverCallback) {
      this.callback = callback;
    }
    observe(target: Element): void {
      // Report the element as visible immediately so viewport-gated work
      // (like tokenization in DiffFile) actually executes under test.
      const entry = {
        isIntersecting: true,
        intersectionRatio: 1,
        target,
        boundingClientRect: {} as DOMRectReadOnly,
        intersectionRect: {} as DOMRectReadOnly,
        rootBounds: null,
        time: 0,
      } as IntersectionObserverEntry;
      this.callback([entry], this as unknown as IntersectionObserver);
    }
    unobserve(): void {}
    disconnect(): void {}
    takeRecords(): IntersectionObserverEntry[] {
      return [];
    }
  }
  (globalThis as GlobalWithIO).IntersectionObserver = IntersectionObserverStub;

  originalResizeObserverExisted = "ResizeObserver" in globalThis;
  originalResizeObserver = (globalThis as GlobalWithResizeObserver).ResizeObserver;
  class ResizeObserverStub {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  (globalThis as GlobalWithResizeObserver).ResizeObserver = ResizeObserverStub;

  originalReplaceSync = (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype.replaceSync;
  if ((globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype) {
    (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync ??= function replaceSync(): void {};
  }
});

afterAll(() => {
  if (originalIntersectionObserverExisted) {
    (globalThis as GlobalWithIO).IntersectionObserver = originalIntersectionObserver;
  } else {
    delete (globalThis as GlobalWithIO).IntersectionObserver;
  }
  if (originalResizeObserverExisted) {
    (globalThis as GlobalWithResizeObserver).ResizeObserver = originalResizeObserver;
  } else {
    delete (globalThis as GlobalWithResizeObserver).ResizeObserver;
  }
  if ((globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype) {
    if (originalReplaceSync) {
      (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync = originalReplaceSync as (
        text: string,
      ) => void;
    } else {
      delete (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync;
    }
  }
});

import DiffFile from "./DiffFile.svelte";
import type { DiffFile as DiffFileType, FilePreview } from "../../api/types.js";
import { STORES_KEY } from "../../context.js";
import type { DiffReviewDraftComment, DiffReviewLineRange } from "../../stores/diff-review-draft.svelte.js";
import { createDiffStore } from "../../stores/diff.svelte.js";
import { renderedCodeSide } from "./pierre-dom.js";
import type { ReviewThread } from "./review-thread-context.js";

function makeFile(overrides: Partial<DiffFileType> = {}): DiffFileType {
  return {
    path: "src/foo.ts",
    old_path: "src/foo.ts",
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: 3,
    deletions: 1,
    patch: `diff --git a/src/foo.ts b/src/foo.ts
--- a/src/foo.ts
+++ b/src/foo.ts
@@ -1,3 +1,5 @@
 line 1
-old line
+new line
`,
    hunks: [
      {
        old_start: 1,
        old_count: 3,
        new_start: 1,
        new_count: 5,
        lines: [
          {
            type: "context",
            content: "line 1",
            old_num: 1,
            new_num: 1,
          },
          { type: "delete", content: "old line", old_num: 2 },
          { type: "add", content: "new line", new_num: 2 },
        ],
      },
    ],
    ...overrides,
  };
}

function makeTwoAdditionFile(): DiffFileType {
  return makeFile({
    additions: 2,
    deletions: 1,
    patch: `diff --git a/src/foo.ts b/src/foo.ts
--- a/src/foo.ts
+++ b/src/foo.ts
@@ -1,2 +1,3 @@
 line 1
-old line
+new line
+newer line
`,
    hunks: [
      {
        old_start: 1,
        old_count: 2,
        new_start: 1,
        new_count: 3,
        lines: [
          {
            type: "context",
            content: "line 1",
            old_num: 1,
            new_num: 1,
          },
          { type: "delete", content: "old line", old_num: 2 },
          { type: "add", content: "new line", new_num: 2 },
          { type: "add", content: "newer line", new_num: 3 },
        ],
      },
    ],
  });
}

function makeAddedConfigFile(): DiffFileType {
  return makeFile({
    path: ".github/ISSUE_TEMPLATE/config.yml",
    old_path: ".github/ISSUE_TEMPLATE/config.yml",
    status: "added",
    additions: 4,
    deletions: 0,
    patch: `diff --git a/.github/ISSUE_TEMPLATE/config.yml b/.github/ISSUE_TEMPLATE/config.yml
--- /dev/null
+++ b/.github/ISSUE_TEMPLATE/config.yml
@@ -0,0 +1,4 @@
+# Route every new issue through one of the templates above.
+# Discussions is enabled for this repo, add a contact_links entry pointing
+# usage/"how do I" questions there instead of the issue tracker.
+blank_issues_enabled: false
`,
    hunks: [
      {
        old_start: 0,
        old_count: 0,
        new_start: 1,
        new_count: 4,
        lines: [
          { type: "add", content: "# Route every new issue through one of the templates above.", new_num: 1 },
          {
            type: "add",
            content: "# Discussions is enabled for this repo, add a contact_links entry pointing",
            new_num: 2,
          },
          {
            type: "add",
            content: '# usage/"how do I" questions there instead of the issue tracker.',
            new_num: 3,
          },
          { type: "add", content: "blank_issues_enabled: false", new_num: 4 },
        ],
      },
    ],
  });
}

function makeReviewThread(overrides: Partial<ReviewThread> = {}): ReviewThread {
  return {
    id: "thread-1",
    provider_comment_id: "comment-1",
    path: "src/foo.ts",
    side: "right",
    line: 2,
    new_line: 2,
    line_type: "add",
    body: "Published review note",
    author_login: "reviewer",
    resolved: false,
    can_resolve: false,
    created_at: "2026-03-30T14:01:00Z",
    updated_at: "2026-03-30T14:01:00Z",
    ...overrides,
  };
}

// Use unique owner per test so module-level collapsed state doesn't leak.
let testCounter = 0;
function uniqueOwner(): string {
  return `test-owner-${++testCounter}`;
}

function renderDiffFile(
  file: DiffFileType,
  options: {
    richPreview?: boolean;
    richPreviewEnabled?: boolean;
    contextExpansionEnabled?: boolean;
    reviewEnabled?: boolean;
    diffHeadSHA?: string;
    nativeMultilineRanges?: boolean;
    owner?: string;
    draftComments?: DiffReviewDraftComment[];
    reviewThreads?: ReviewThread[];
    createComment?: (body: string, range: DiffReviewLineRange) => Promise<boolean>;
    canReplyToThreads?: boolean;
    viewMode?: "unified" | "split";
    replyToDiscussion?: (
      owner: string,
      name: string,
      number: number,
      discussionID: string,
      body: string,
    ) => Promise<boolean>;
  } = {},
) {
  const diff = createDiffStore();
  if (options.richPreview) diff.setRichPreview(true);
  if (options.viewMode) diff.setViewMode(options.viewMode);
  const diffReviewDraft = {
    getComments: () => options.draftComments ?? [],
    isSubmitting: () => false,
    getError: () => null,
    createComment: options.createComment ?? (() => Promise.resolve(true)),
    deleteComment: () => Promise.resolve(true),
  };
  const owner = options.owner ?? uniqueOwner();
  const result = render(DiffFile, {
    props: {
      file,
      provider: "github",
      platformHost: "github.com",
      owner,
      name: "n",
      repoPath: `${owner}/n`,
      number: 1,
      ...(options.richPreviewEnabled !== undefined && {
        richPreviewEnabled: options.richPreviewEnabled,
      }),
      ...(options.contextExpansionEnabled !== undefined && {
        contextExpansionEnabled: options.contextExpansionEnabled,
      }),
      ...(options.reviewEnabled !== undefined && {
        reviewEnabled: options.reviewEnabled,
      }),
      ...(options.canReplyToThreads !== undefined && {
        canReplyToThreads: options.canReplyToThreads,
      }),
      ...(options.diffHeadSHA !== undefined && {
        diffHeadSHA: options.diffHeadSHA,
      }),
      ...(options.nativeMultilineRanges !== undefined && {
        nativeMultilineRanges: options.nativeMultilineRanges,
      }),
      ...(options.reviewThreads !== undefined && {
        reviewThreads: options.reviewThreads,
      }),
    },
    context: new Map([
      [
        STORES_KEY,
        {
          diff,
          diffReviewDraft,
          detail: {
            replyToDiscussion: options.replyToDiscussion ?? (() => Promise.resolve(true)),
          },
        },
      ],
    ]),
  });
  return { ...result, diff };
}

function textPreview(path: string, text: string): FilePreview {
  return {
    path,
    media_type: "text/plain; charset=utf-8",
    encoding: "base64",
    content: Buffer.from(text, "utf8").toString("base64"),
    size: text.length,
  };
}

describe("DiffFile", () => {
  afterEach(() => {
    cleanup();
    localStorage.removeItem("diff-rich-preview");
    localStorage.removeItem("diff-view-mode");
  });

  async function expectPierreDiffText(pattern: RegExp): Promise<void> {
    await waitFor(() => {
      const host = document.querySelector(".pierre-diff");
      expect(host?.shadowRoot?.textContent).toMatch(pattern);
    });
  }

  it("renders file content when not collapsed", async () => {
    renderDiffFile(makeFile());

    expect(screen.getByText("src/foo.ts")).toBeTruthy();
    await expectPierreDiffText(/old linenew line/);
  });

  it("exposes stable line targets inside the Pierre shadow root", async () => {
    renderDiffFile(makeFile());

    await waitFor(() => {
      const root = document.querySelector(".pierre-diff")?.shadowRoot;
      expect(root?.querySelector('[data-diff-path="src/foo.ts"][data-diff-old-line="2"]')).toBeTruthy();
      expect(root?.querySelector('[data-diff-path="src/foo.ts"][data-diff-new-line="2"]')).toBeTruthy();
    });
  });

  it("shows a loading state before viewport-gated Pierre rendering starts", async () => {
    const visibleObserver = (globalThis as GlobalWithIO).IntersectionObserver;
    class PendingIntersectionObserverStub {
      root: Element | null = null;
      rootMargin = "";
      thresholds: readonly number[] = [];
      observe(): void {}
      unobserve(): void {}
      disconnect(): void {}
      takeRecords(): IntersectionObserverEntry[] {
        return [];
      }
    }
    (globalThis as GlobalWithIO).IntersectionObserver = PendingIntersectionObserverStub;

    try {
      renderDiffFile(makeFile());

      expect(screen.getByRole("status").textContent).toContain("Loading diff");
      expect(document.querySelector(".pierre-diff--pending")).toBeTruthy();
    } finally {
      (globalThis as GlobalWithIO).IntersectionObserver = visibleObserver;
    }
  });

  it("renders an empty textual diff state without staying stuck loading", async () => {
    renderDiffFile(
      makeFile({
        additions: 0,
        deletions: 0,
        patch: "",
        hunks: [],
      }),
    );

    await waitFor(() => {
      expect(screen.queryByRole("status")).toBeNull();
      expect(screen.getByText("No textual changes")).toBeTruthy();
    });
  });

  it("treats nullable hunk payloads as hunkless diffs", async () => {
    renderDiffFile(
      makeFile({
        additions: 0,
        deletions: 0,
        patch: "",
        hunks: null as unknown as DiffFileType["hunks"],
      }),
    );

    await waitFor(() => {
      expect(screen.queryByRole("status")).toBeNull();
      expect(screen.getByText("No textual changes")).toBeTruthy();
    });
  });

  it("shows unified diff content when rich preview is disabled", async () => {
    renderDiffFile(makeFile({ path: "README.md", old_path: "README.md" }), {
      richPreview: true,
      richPreviewEnabled: false,
    });

    expect(screen.queryByLabelText("Before markdown preview")).toBeNull();
    await expectPierreDiffText(/old linenew line/);
  });

  it("renders markdown rich preview as a unified annotated document by default", async () => {
    renderDiffFile(makeFile({ path: "README.md", old_path: "README.md" }), {
      richPreview: true,
    });

    await waitFor(() => {
      expect(screen.queryByLabelText("Before markdown preview")).toBeNull();
      expect(screen.queryByLabelText("After markdown preview")).toBeNull();
      expect(document.querySelector(".markdown-rich-diff--unified del")?.textContent).toBe("old");
      expect(document.querySelector(".markdown-rich-diff--unified ins")?.textContent).toBe("new");
    });
  });

  it("keeps markdown rich preview side by side when split diff mode is enabled", async () => {
    renderDiffFile(makeFile({ path: "README.md", old_path: "README.md" }), {
      richPreview: true,
      viewMode: "split",
    });

    await waitFor(() => {
      const beforePreview = screen.getByLabelText("Before markdown preview");
      const afterPreview = screen.getByLabelText("After markdown preview");

      expect(beforePreview.querySelector("del")?.textContent).toBe("old");
      expect(beforePreview.querySelector("ins")).toBeNull();
      expect(afterPreview.querySelector("del")).toBeNull();
      expect(afterPreview.querySelector("ins")?.textContent).toBe("new");
    });
  });

  it("hides content after clicking the header to collapse", async () => {
    renderDiffFile(makeFile());

    const header = screen.getByTitle("Collapse file");
    await fireEvent.click(header);

    expect(document.querySelector(".file-content")).toBeNull();
  });

  it("shows content again after toggling collapse twice", async () => {
    renderDiffFile(makeFile());

    const header = screen.getByTitle("Collapse file");
    await fireEvent.click(header);

    const expandHeader = screen.getByTitle("Expand file");
    await fireEvent.click(expandHeader);

    const content = document.querySelector(".file-content");
    expect(content?.classList.contains("file-content--collapsed")).toBe(false);
  });

  async function selectPierreLine(
    line: number,
    side: "left" | "right",
    options: { shiftKey?: boolean } = {},
  ): Promise<void> {
    await clickLineCommentButton(line, side, options);
  }

  async function clickLineCommentButton(
    line: number,
    side: "left" | "right",
    options: { shiftKey?: boolean } = {},
  ): Promise<void> {
    const button = await findLineCommentButton(line, side);
    button.dispatchEvent(
      new MouseEvent("pointerdown", {
        bubbles: true,
        button: 0,
        shiftKey: options.shiftKey,
      }),
    );
    await fireEvent.mouseDown(button, {
      button: 0,
      shiftKey: options.shiftKey,
    });
    await fireEvent.pointerUp(button, {
      pointerId: 1,
      pointerType: "mouse",
      shiftKey: options.shiftKey,
    });
    await fireEvent.click(button, { shiftKey: options.shiftKey });
  }

  async function keyboardActivateLineCommentButton(line: number, side: "left" | "right"): Promise<void> {
    const button = await findLineCommentButton(line, side);
    button.focus();
    await fireEvent.click(button);
  }

  async function findLineCommentButton(line: number, side: "left" | "right"): Promise<HTMLButtonElement> {
    const sideLabel = side === "left" ? "old" : "new";
    return await waitFor(() => {
      const element = document
        .querySelector(".pierre-diff")
        ?.shadowRoot?.querySelector<HTMLButtonElement>(
          `[data-middleman-line-comment-button][aria-label="Comment on ${sideLabel} line ${line}"]`,
        );
      expect(element).toBeTruthy();
      return element!;
    });
  }

  function selectedPierreLines(): NodeListOf<Element> | undefined {
    return document.querySelector(".pierre-diff")?.shadowRoot?.querySelectorAll("[data-selected-line]");
  }

  function splitColumnSide(element: Element | null | undefined): "additions" | "deletions" | null {
    const code = element?.closest("code");
    return code ? (renderedCodeSide(code) ?? null) : null;
  }

  function expandedContextLineTexts(): string[] {
    return Array.from(
      document
        .querySelector(".pierre-diff")
        ?.shadowRoot?.querySelectorAll<HTMLElement>("[data-content] [data-line-type='context-expanded']") ?? [],
    ).map((line) => line.textContent?.trim() ?? "");
  }

  it("allows shift-selecting ranges only when native multiline ranges are supported", async () => {
    const { unmount } = renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      nativeMultilineRanges: true,
    });

    await selectPierreLine(1, "right");
    await selectPierreLine(2, "right", { shiftKey: true });

    expect(selectedPierreLines()?.length).toBeGreaterThanOrEqual(2);

    unmount();
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      nativeMultilineRanges: false,
    });

    await selectPierreLine(1, "right");
    await selectPierreLine(2, "right", { shiftKey: true });

    expect(selectedPierreLines()).toHaveLength(4);
  });

  it("toggles an empty inline composer from the line comment button", async () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
    });

    await clickLineCommentButton(2, "right");
    const textarea = screen.getByPlaceholderText("Leave a comment");
    expect(textarea).toBeTruthy();
    await waitFor(() => {
      expect(textarea).toBe(document.activeElement);
    });
    await waitFor(() => {
      expect(selectedPierreLines()).toHaveLength(4);
    });

    await clickLineCommentButton(2, "right");

    expect(screen.queryByPlaceholderText("Leave a comment")).toBeNull();
    expect(selectedPierreLines()).toHaveLength(0);
  });

  it("keeps the final line visible when opening an inline composer", async () => {
    renderDiffFile(makeAddedConfigFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
    });

    await clickLineCommentButton(4, "right");
    await waitFor(() => expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy());

    const root = document.querySelector(".pierre-diff")?.shadowRoot;
    const lineTargets = Array.from(root?.querySelectorAll<HTMLElement>('[data-diff-new-line="4"]') ?? []);
    expect(lineTargets.some((line) => line.textContent?.includes("blank_issues_enabled: false"))).toBe(true);

    await waitFor(() => {
      const slot = root?.querySelector<HTMLSlotElement>('slot[name="annotation-additions-4"]');
      expect(slot).toBeTruthy();
      expect(slot?.closest('[data-diff-new-line="4"]')).toBeNull();
    });
  });

  it("keeps right-side inline composers in the additions column in split mode", async () => {
    renderDiffFile(makeTwoAdditionFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      viewMode: "split",
    });

    async function assertRightSideComposer(line: number): Promise<void> {
      const button = await findLineCommentButton(line, "right");
      expect(splitColumnSide(button)).toBe("additions");

      await clickLineCommentButton(line, "right");

      await waitFor(() => {
        expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
        const slot = document
          .querySelector(".pierre-diff")
          ?.shadowRoot?.querySelector<HTMLSlotElement>(`slot[name="annotation-additions-${line}"]`);
        expect(splitColumnSide(slot)).toBe("additions");
      });

      await fireEvent.click(screen.getByText("Cancel"));
      await waitFor(() => expect(screen.queryByPlaceholderText("Leave a comment")).toBeNull());
    }

    await assertRightSideComposer(2);
    await assertRightSideComposer(3);
  });

  it("infers split column side from code-column order when non-code siblings are present", () => {
    const pre = document.createElement("pre");
    const deletions = document.createElement("code");
    const separator = document.createElement("span");
    const additions = document.createElement("code");
    pre.append(deletions, separator, additions);

    expect(renderedCodeSide(deletions)).toBe("deletions");
    expect(renderedCodeSide(additions)).toBe("additions");
  });

  it("focuses the inline composer textarea exactly once after opening", async () => {
    // Regression guard for issues #445/#446: repeated focus retries (extra
    // frames or timers) made the composer's visible focus indicator flicker.
    // The composer must mutate textarea focus a single time.
    const focusSpy = vi.spyOn(HTMLTextAreaElement.prototype, "focus");
    try {
      renderDiffFile(makeFile(), {
        reviewEnabled: true,
        diffHeadSHA: "diff-head",
      });

      await clickLineCommentButton(2, "right");
      const textarea = screen.getByPlaceholderText("Leave a comment");
      await waitFor(() => {
        expect(textarea).toBe(document.activeElement);
      });

      // Cover the windows the old retry implementation used: two animation
      // frames plus a 50ms timer after the initial focus.
      await new Promise((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => resolve(undefined)));
      });
      await new Promise((resolve) => setTimeout(resolve, 80));

      expect(focusSpy).toHaveBeenCalledTimes(1);
      expect(textarea).toBe(document.activeElement);
    } finally {
      focusSpy.mockRestore();
    }
  });

  it("toggles an empty inline composer from keyboard line comment button activation", async () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
    });

    await clickLineCommentButton(2, "right");
    expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
    await waitFor(() => {
      expect(selectedPierreLines()).toHaveLength(4);
    });

    await keyboardActivateLineCommentButton(2, "right");

    expect(screen.queryByPlaceholderText("Leave a comment")).toBeNull();
    expect(selectedPierreLines()).toHaveLength(0);
  });

  it("keeps shift-click line comment button selection from collapsing active ranges", async () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      nativeMultilineRanges: true,
    });

    await selectPierreLine(1, "right");
    await clickLineCommentButton(2, "right", { shiftKey: true });

    await waitFor(() => {
      expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
      expect(selectedPierreLines()?.length).toBeGreaterThanOrEqual(2);
    });

    await clickLineCommentButton(2, "right", { shiftKey: true });

    expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
    expect(selectedPierreLines()?.length).toBeGreaterThanOrEqual(2);
  });

  it("toggles an active multiline composer from keyboard line comment button activation", async () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      nativeMultilineRanges: true,
    });

    await selectPierreLine(1, "right");
    await selectPierreLine(2, "right", { shiftKey: true });

    await waitFor(() => {
      expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
      expect(selectedPierreLines()?.length).toBeGreaterThanOrEqual(2);
    });

    await keyboardActivateLineCommentButton(2, "right");

    expect(screen.queryByPlaceholderText("Leave a comment")).toBeNull();
    expect(selectedPierreLines()).toHaveLength(0);
  });

  it("does not create multiline review ranges across separate hunks", async () => {
    renderDiffFile(
      makeFile({
        additions: 2,
        deletions: 0,
        patch: `diff --git a/src/foo.ts b/src/foo.ts
--- a/src/foo.ts
+++ b/src/foo.ts
@@ -1,0 +1,1 @@
+first hunk
@@ -20,0 +20,1 @@
+second hunk
`,
        hunks: [
          {
            old_start: 1,
            old_count: 1,
            new_start: 1,
            new_count: 1,
            lines: [{ type: "add", content: "first hunk", new_num: 1 }],
          },
          {
            old_start: 20,
            old_count: 1,
            new_start: 20,
            new_count: 1,
            lines: [{ type: "add", content: "second hunk", new_num: 20 }],
          },
        ],
      }),
      {
        reviewEnabled: true,
        diffHeadSHA: "diff-head",
        nativeMultilineRanges: true,
      },
    );

    await selectPierreLine(1, "right");
    await selectPierreLine(20, "right", { shiftKey: true });

    await waitFor(() => {
      expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
    });
    expect(selectedPierreLines()).toHaveLength(4);
  });

  it("renders saved draft comments inline at their selected range", async () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      draftComments: [
        {
          id: "draft-1",
          body: "Follow up here",
          path: "src/foo.ts",
          side: "right",
          start_side: "right",
          start_line: 1,
          line: 2,
          new_line: 2,
          line_type: "add",
          diff_head_sha: "diff-head",
          created_at: "2026-03-30T14:01:00Z",
          updated_at: "2026-03-30T14:01:00Z",
        },
      ],
    });

    await waitFor(() => {
      expect(screen.getByText("Follow up here")).toBeTruthy();
      expect(selectedPierreLines()?.length).toBeGreaterThanOrEqual(4);
    });
  });

  it("opens a new inline composer on a line that already has a saved draft", async () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      draftComments: [
        {
          id: "draft-existing",
          body: "Existing draft on this line",
          path: "src/foo.ts",
          side: "right",
          line: 2,
          new_line: 2,
          line_type: "add",
          diff_head_sha: "diff-head",
          created_at: "2026-03-30T14:01:00Z",
          updated_at: "2026-03-30T14:01:00Z",
        },
      ],
    });

    await waitFor(() => {
      expect(screen.getByText("Existing draft on this line")).toBeTruthy();
      const selectedDraftLine = document
        .querySelector(".pierre-diff")
        ?.shadowRoot?.querySelector('[data-selected-line][data-diff-new-line="2"]');
      expect(selectedDraftLine).toBeTruthy();
    });

    await clickLineCommentButton(2, "right");

    await waitFor(() => {
      expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
    });
    expect(screen.getByText("Existing draft on this line")).toBeTruthy();
  });

  it("renders published review threads under their matching diff line", async () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      reviewThreads: [makeReviewThread()],
    });

    await waitFor(() => {
      expect(screen.getByText("Published review note")).toBeTruthy();
    });
    const host = document
      .querySelector("[data-review-thread-id='thread-1']")
      ?.closest("[slot='annotation-additions-2']");
    expect(host).toBeTruthy();
  });

  it("lets published inline review threads be replied to", async () => {
    const replyToDiscussion = vi.fn().mockResolvedValue(true);
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      canReplyToThreads: true,
      reviewThreads: [makeReviewThread()],
      replyToDiscussion,
    });

    await fireEvent.click(await screen.findByRole("button", { name: "Reply" }));
    const textarea = screen.getByPlaceholderText("Reply to thread");
    expect(textarea).toBe(document.activeElement);

    await fireEvent.input(textarea, {
      target: { value: "Follow-up reply" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Reply" }));

    await waitFor(() => {
      expect(replyToDiscussion).toHaveBeenCalledWith(expect.any(String), "n", 1, "thread-1", "Follow-up reply");
    });
  });

  it("does not render stale-head review threads under a matching current line", async () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "current-head",
      reviewThreads: [
        makeReviewThread({
          diff_head_sha: "stale-head",
        }),
      ],
    });

    await waitFor(() => {
      expect(screen.getByText("Published review note")).toBeTruthy();
    });
    expect(screen.getByText("File")).toBeTruthy();
    const comment = document.querySelector("[data-review-thread-id='thread-1']");
    expect(comment?.parentElement?.classList.contains("file-content")).toBe(true);
    expect(comment?.closest("[slot^='annotation-']")).toBeNull();
  });

  it("does not match added-file threads only because old paths are empty", () => {
    renderDiffFile(
      makeFile({
        path: "src/new.ts",
        old_path: "",
        status: "added",
      }),
      {
        reviewEnabled: true,
        reviewThreads: [
          makeReviewThread({
            id: "thread-other-added-file",
            path: "src/other-new.ts",
            old_path: "",
            body: "Wrong added file note",
          }),
        ],
      },
    );

    expect(screen.queryByText("Wrong added file note")).toBeNull();
  });

  it("renders unmatched review threads at the file header", () => {
    renderDiffFile(
      makeFile({
        hunks: [
          {
            old_start: 60,
            old_count: 1,
            new_start: 60,
            new_count: 1,
            lines: [
              {
                type: "context",
                content: "visible context",
                old_num: 60,
                new_num: 60,
              },
            ],
          },
        ],
      }),
      {
        reviewThreads: [
          makeReviewThread({
            id: "thread-file",
            line: 1,
            new_line: 1,
            line_type: "file",
            body: "File-level note",
          }),
        ],
      },
    );

    expect(screen.getByText("File-level note")).toBeTruthy();
    expect(screen.getByText("File")).toBeTruthy();
    const comment = document.querySelector("[data-review-thread-id='thread-file']");
    expect(comment?.parentElement?.classList.contains("file-content")).toBe(true);
  });

  it("clears an open inline composer when review context changes", async () => {
    const file = makeFile();
    const owner = uniqueOwner();
    const { rerender } = renderDiffFile(file, {
      owner,
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
    });

    await selectPierreLine(1, "right");
    expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
    expect(selectedPierreLines()).toHaveLength(4);

    await rerender({
      file,
      provider: "github",
      owner,
      name: "n",
      repoPath: "o/n",
      number: 1,
      reviewEnabled: false,
      diffHeadSHA: "diff-head",
    });

    expect(screen.queryByPlaceholderText("Leave a comment")).toBeNull();

    await rerender({
      file,
      provider: "github",
      owner,
      name: "n",
      repoPath: "o/n",
      number: 1,
      reviewEnabled: true,
      diffHeadSHA: "new-diff-head",
    });
    await selectPierreLine(1, "right");
    expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();

    await rerender({
      file,
      provider: "github",
      owner,
      name: "n",
      repoPath: "o/n",
      number: 1,
      reviewEnabled: true,
      diffHeadSHA: "another-diff-head",
    });

    expect(screen.queryByPlaceholderText("Leave a comment")).toBeNull();
    expect(selectedPierreLines()).toHaveLength(0);
  });

  it("loads and expands hidden context from a single Pierre expander click", async () => {
    const oldText = Array.from({ length: 90 }, (_, index) => `shared ${index + 1}`);
    const newText = [...oldText];
    oldText[1] = "old early";
    newText[1] = "new early";
    oldText[77] = "old late";
    newText[77] = "new late";

    const file = makeFile({
      path: "src/context.txt",
      old_path: "src/context.txt",
      patch: `diff --git a/src/context.txt b/src/context.txt
--- a/src/context.txt
+++ b/src/context.txt
@@ -1,3 +1,3 @@
 shared 1
-old early
+new early
 shared 3
@@ -77,3 +77,3 @@ lateContext
 shared 77
-old late
+new late
 shared 79
`,
      hunks: [
        {
          old_start: 1,
          old_count: 3,
          new_start: 1,
          new_count: 3,
          lines: [
            {
              type: "context",
              content: "shared 1",
              old_num: 1,
              new_num: 1,
            },
            { type: "delete", content: "old early", old_num: 2 },
            { type: "add", content: "new early", new_num: 2 },
            {
              type: "context",
              content: "shared 3",
              old_num: 3,
              new_num: 3,
            },
          ],
        },
        {
          old_start: 77,
          old_count: 3,
          new_start: 77,
          new_count: 3,
          lines: [
            {
              type: "context",
              content: "shared 77",
              old_num: 77,
              new_num: 77,
            },
            { type: "delete", content: "old late", old_num: 78 },
            { type: "add", content: "new late", new_num: 78 },
            {
              type: "context",
              content: "shared 79",
              old_num: 79,
              new_num: 79,
            },
          ],
        },
      ],
    });
    const { diff } = renderDiffFile(file);
    const loadFilePreview = vi
      .spyOn(diff, "loadFilePreview")
      .mockImplementation(async (_owner, _name, _number, path, side) => {
        return textPreview(path, side === "old" ? oldText.join("\n") : newText.join("\n"));
      });

    const expandButton = await waitFor(() => {
      const button = document
        .querySelector(".pierre-diff")
        ?.shadowRoot?.querySelector<HTMLElement>("[data-expand-button]");
      expect(button).toBeTruthy();
      return button!;
    });

    await fireEvent.click(expandButton);

    await waitFor(() => {
      const text = document.querySelector(".pierre-diff")?.shadowRoot?.textContent ?? "";
      expect(text).toContain("shared 10");
      expect(text).toContain("@@ -77,3 +77,3 @@ lateContext");
      const expandedLines = expandedContextLineTexts();
      expect(expandedLines.length).toBeGreaterThan(0);
      expect(expandedLines.every((line) => line.length > 0)).toBe(true);
      expect(expandedLines.some((line) => line.includes("shared 10"))).toBe(true);
    });
    expect(loadFilePreview).toHaveBeenCalledWith(expect.any(String), "n", 1, "src/context.txt", "old");
    expect(loadFilePreview).toHaveBeenCalledWith(expect.any(String), "n", 1, "src/context.txt", "new");
  });

  it("continues expanding context after full file text is loaded", async () => {
    const oldText = Array.from({ length: 90 }, (_, index) => `shared ${index + 1}`);
    const newText = [...oldText];
    oldText[1] = "old early";
    newText[1] = "new early";
    oldText[77] = "old late";
    newText[77] = "new late";

    const file = makeFile({
      path: "src/context.txt",
      old_path: "src/context.txt",
      patch: `diff --git a/src/context.txt b/src/context.txt
--- a/src/context.txt
+++ b/src/context.txt
@@ -1,3 +1,3 @@
 shared 1
-old early
+new early
 shared 3
@@ -77,3 +77,3 @@ lateContext
 shared 77
-old late
+new late
 shared 79
`,
      hunks: [
        {
          old_start: 1,
          old_count: 3,
          new_start: 1,
          new_count: 3,
          lines: [
            {
              type: "context",
              content: "shared 1",
              old_num: 1,
              new_num: 1,
            },
            { type: "delete", content: "old early", old_num: 2 },
            { type: "add", content: "new early", new_num: 2 },
            {
              type: "context",
              content: "shared 3",
              old_num: 3,
              new_num: 3,
            },
          ],
        },
        {
          old_start: 77,
          old_count: 3,
          new_start: 77,
          new_count: 3,
          lines: [
            {
              type: "context",
              content: "shared 77",
              old_num: 77,
              new_num: 77,
            },
            { type: "delete", content: "old late", old_num: 78 },
            { type: "add", content: "new late", new_num: 78 },
            {
              type: "context",
              content: "shared 79",
              old_num: 79,
              new_num: 79,
            },
          ],
        },
      ],
    });
    const { diff } = renderDiffFile(file);
    const loadFilePreview = vi
      .spyOn(diff, "loadFilePreview")
      .mockImplementation(async (_owner, _name, _number, path, side) => {
        return textPreview(path, side === "old" ? oldText.join("\n") : newText.join("\n"));
      });

    const firstExpandButton = await waitFor(() => {
      const button = document
        .querySelector(".pierre-diff")
        ?.shadowRoot?.querySelector<HTMLElement>("[data-expand-button]");
      expect(button).toBeTruthy();
      return button!;
    });
    await fireEvent.click(firstExpandButton);

    await waitFor(() => {
      const text = document.querySelector(".pierre-diff")?.shadowRoot?.textContent ?? "";
      expect(text).toContain("shared 10");
      expect(text).not.toContain("shared 50");
      const expandedLines = expandedContextLineTexts();
      expect(expandedLines.length).toBeGreaterThan(0);
      expect(expandedLines.every((line) => line.length > 0)).toBe(true);
      expect(expandedLines.some((line) => line.includes("shared 10"))).toBe(true);
    });

    const nextExpandButton = await waitFor(() => {
      const buttons = Array.from(
        document.querySelector(".pierre-diff")?.shadowRoot?.querySelectorAll<HTMLElement>("[data-expand-button]") ?? [],
      );
      const button = buttons.find((candidate) => candidate !== firstExpandButton);
      expect(button).toBeTruthy();
      return button!;
    });
    await fireEvent.click(nextExpandButton);

    await waitFor(() => {
      const text = document.querySelector(".pierre-diff")?.shadowRoot?.textContent ?? "";
      expect(text).toContain("shared 50");
      const expandedLines = expandedContextLineTexts();
      expect(expandedLines.every((line) => line.length > 0)).toBe(true);
      expect(expandedLines.some((line) => line.includes("shared 50"))).toBe(true);
    });
    expect(loadFilePreview).toHaveBeenCalledTimes(2);
  });

  it("hides Pierre context expansion when file text loading is disabled", async () => {
    const file = makeFile({
      path: "src/context.ts",
      old_path: "src/context.ts",
      patch: `diff --git a/src/context.ts b/src/context.ts
--- a/src/context.ts
+++ b/src/context.ts
@@ -1,3 +1,3 @@
 shared 1
-old early
+new early
 shared 3
@@ -77,3 +77,3 @@ lateContext
 shared 77
-old late
+new late
 shared 79
`,
      hunks: [
        {
          old_start: 1,
          old_count: 3,
          new_start: 1,
          new_count: 3,
          lines: [
            {
              type: "context",
              content: "shared 1",
              old_num: 1,
              new_num: 1,
            },
            { type: "delete", content: "old early", old_num: 2 },
            { type: "add", content: "new early", new_num: 2 },
            {
              type: "context",
              content: "shared 3",
              old_num: 3,
              new_num: 3,
            },
          ],
        },
        {
          old_start: 77,
          old_count: 3,
          new_start: 77,
          new_count: 3,
          lines: [
            {
              type: "context",
              content: "shared 77",
              old_num: 77,
              new_num: 77,
            },
            { type: "delete", content: "old late", old_num: 78 },
            { type: "add", content: "new late", new_num: 78 },
            {
              type: "context",
              content: "shared 79",
              old_num: 79,
              new_num: 79,
            },
          ],
        },
      ],
    });
    const { diff } = renderDiffFile(file, {
      contextExpansionEnabled: false,
    });
    const loadFilePreview = vi.spyOn(diff, "loadFilePreview");

    await waitFor(() => {
      const root = document.querySelector(".pierre-diff")?.shadowRoot;
      expect(root?.textContent).toContain("@@ -77,3 +77,3 @@ lateContext");
      expect(root?.querySelector("[data-expand-button]")).toBeNull();
    });
    expect(loadFilePreview).not.toHaveBeenCalled();
  });

  it("keeps raw hunk headers on Pierre context separators", async () => {
    const file = makeFile({
      path: "src/context.ts",
      old_path: "src/context.ts",
      patch: `diff --git a/src/context.ts b/src/context.ts
--- a/src/context.ts
+++ b/src/context.ts
@@ -1,3 +1,3 @@
 shared 1
-old early
+new early
 shared 3
@@ -17,3 +17,3 @@ usefulContext
 shared 17
-old late
+new late
 shared 19
`,
      hunks: [
        {
          old_start: 1,
          old_count: 3,
          new_start: 1,
          new_count: 3,
          lines: [
            {
              type: "context",
              content: "shared 1",
              old_num: 1,
              new_num: 1,
            },
            { type: "delete", content: "old early", old_num: 2 },
            { type: "add", content: "new early", new_num: 2 },
            {
              type: "context",
              content: "shared 3",
              old_num: 3,
              new_num: 3,
            },
          ],
        },
        {
          old_start: 17,
          old_count: 3,
          new_start: 17,
          new_count: 3,
          lines: [
            {
              type: "context",
              content: "shared 17",
              old_num: 17,
              new_num: 17,
            },
            { type: "delete", content: "old late", old_num: 18 },
            { type: "add", content: "new late", new_num: 18 },
            {
              type: "context",
              content: "shared 19",
              old_num: 19,
              new_num: 19,
            },
          ],
        },
      ],
    });

    renderDiffFile(file);

    await expectPierreDiffText(/@@ -17,3 \+17,3 @@ usefulContext/);
  });
});
