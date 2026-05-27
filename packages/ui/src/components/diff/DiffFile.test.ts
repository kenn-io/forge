import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";

// Mock highlight utils to avoid loading Shiki in tests.
vi.mock("../../utils/highlight.js", () => ({
  tokenizeLineDual: () => Promise.resolve([]),
  langFromPath: () => "text",
}));

// jsdom does not ship IntersectionObserver; install a stub that reports the
// observed element as visible immediately so the tokenization effect actually
// runs under test. The original global (if any) is saved and restored after
// the suite so it does not leak into sibling test files.
type GlobalWithIO = { IntersectionObserver?: unknown };
let originalIntersectionObserver: unknown;
let originalIntersectionObserverExisted = false;

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
    takeRecords(): IntersectionObserverEntry[] { return []; }
  }
  (globalThis as GlobalWithIO).IntersectionObserver = IntersectionObserverStub;
});

afterAll(() => {
  if (originalIntersectionObserverExisted) {
    (globalThis as GlobalWithIO).IntersectionObserver = originalIntersectionObserver;
  } else {
    delete (globalThis as GlobalWithIO).IntersectionObserver;
  }
});

import DiffFile from "./DiffFile.svelte";
import type { DiffFile as DiffFileType } from "../../api/types.js";
import { STORES_KEY } from "../../context.js";
import type { DiffReviewDraftComment } from "../../stores/diff-review-draft.svelte.js";
import { createDiffStore } from "../../stores/diff.svelte.js";
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
    hunks: [{
      old_start: 1,
      old_count: 3,
      new_start: 1,
      new_count: 5,
      lines: [
        { type: "context", content: "line 1", old_num: 1, new_num: 1 },
        { type: "delete", content: "old line", old_num: 2 },
        { type: "add", content: "new line", new_num: 2 },
      ],
    }],
    ...overrides,
  };
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
    reviewEnabled?: boolean;
    diffHeadSHA?: string;
    nativeMultilineRanges?: boolean;
    owner?: string;
    draftComments?: DiffReviewDraftComment[];
    reviewThreads?: ReviewThread[];
  } = {},
) {
  const diff = createDiffStore();
  if (options.richPreview) diff.setRichPreview(true);
  const diffReviewDraft = {
    getComments: () => options.draftComments ?? [],
    isSubmitting: () => false,
    getError: () => null,
    createComment: () => Promise.resolve(true),
    deleteComment: () => Promise.resolve(true),
  };
  return render(DiffFile, {
    props: {
      file,
      owner: options.owner ?? uniqueOwner(),
      name: "n",
      number: 1,
      ...(options.richPreviewEnabled !== undefined && {
        richPreviewEnabled: options.richPreviewEnabled,
      }),
      ...(options.reviewEnabled !== undefined && {
        reviewEnabled: options.reviewEnabled,
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
    context: new Map([[STORES_KEY, { diff, diffReviewDraft }]]),
  });
}

describe("DiffFile", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders file content when not collapsed", () => {
    renderDiffFile(makeFile());

    expect(screen.getByText("src/foo.ts")).toBeTruthy();
    expect(screen.getByText(/@@ -1,3 \+1,5 @@/)).toBeTruthy();
  });

  it("shows unified diff content when rich preview is disabled", () => {
    renderDiffFile(makeFile({ path: "README.md", old_path: "README.md" }), {
      richPreview: true,
      richPreviewEnabled: false,
    });

    expect(screen.queryByLabelText("Before markdown preview")).toBeNull();
    expect(screen.getByText(/@@ -1,3 \+1,5 @@/)).toBeTruthy();
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

  it("allows shift-selecting ranges only when native multiline ranges are supported", async () => {
    const { unmount } = renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      nativeMultilineRanges: true,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Comment on new line 1" }));
    await fireEvent.click(screen.getByRole("button", { name: "Comment on new line 2" }), {
      shiftKey: true,
    });

    expect(document.querySelectorAll(".gutter-new.gutter--selected")).toHaveLength(2);

    unmount();
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      nativeMultilineRanges: false,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Comment on new line 1" }));
    await fireEvent.click(screen.getByRole("button", { name: "Comment on new line 2" }), {
      shiftKey: true,
    });

    expect(document.querySelectorAll(".gutter-new.gutter--selected")).toHaveLength(1);
  });

  it("does not create multiline review ranges across separate hunks", async () => {
    renderDiffFile(makeFile({
      additions: 2,
      deletions: 0,
      hunks: [
        {
          old_start: 1,
          old_count: 1,
          new_start: 1,
          new_count: 1,
          lines: [
            { type: "add", content: "first hunk", new_num: 1 },
          ],
        },
        {
          old_start: 20,
          old_count: 1,
          new_start: 20,
          new_count: 1,
          lines: [
            { type: "add", content: "second hunk", new_num: 20 },
          ],
        },
      ],
    }), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      nativeMultilineRanges: true,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Comment on new line 1" }));
    await fireEvent.click(screen.getByRole("button", { name: "Comment on new line 20" }), {
      shiftKey: true,
    });

    const selected = Array.from(document.querySelectorAll(".gutter-new.gutter--selected"));
    expect(selected).toHaveLength(1);
    expect(selected[0]?.textContent?.trim()).toBe("20");
  });

  it("renders saved draft comments inline at their selected range", () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      draftComments: [{
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
      }],
    });

    expect(screen.getByText("Follow up here")).toBeTruthy();
    expect(document.querySelectorAll(".gutter-new.gutter--selected")).toHaveLength(2);
  });

  it("renders published review threads under their matching diff line", () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      reviewThreads: [makeReviewThread()],
    });

    expect(screen.getByText("Published review note")).toBeTruthy();
    const comment = document.querySelector("[data-review-thread-id='thread-1']");
    const previous = comment?.previousElementSibling;
    expect(previous?.querySelector("[aria-label='Comment on new line 2']")).toBeTruthy();
  });

  it("does not render stale-head review threads under a matching current line", () => {
    renderDiffFile(makeFile(), {
      reviewEnabled: true,
      diffHeadSHA: "current-head",
      reviewThreads: [makeReviewThread({
        diff_head_sha: "stale-head",
      })],
    });

    expect(screen.getByText("Published review note")).toBeTruthy();
    expect(screen.getByText("File")).toBeTruthy();
    const comment = document.querySelector("[data-review-thread-id='thread-1']");
    expect(comment?.parentElement?.classList.contains("file-content")).toBe(true);
    const previous = comment?.previousElementSibling;
    expect(previous?.querySelector("[aria-label='Comment on new line 2']")).toBeFalsy();
  });

  it("does not match added-file threads only because old paths are empty", () => {
    renderDiffFile(makeFile({
      path: "src/new.ts",
      old_path: "",
      status: "added",
    }), {
      reviewEnabled: true,
      reviewThreads: [makeReviewThread({
        id: "thread-other-added-file",
        path: "src/other-new.ts",
        old_path: "",
        body: "Wrong added file note",
      })],
    });

    expect(screen.queryByText("Wrong added file note")).toBeNull();
  });

  it("renders unmatched review threads at the file header", () => {
    renderDiffFile(makeFile({
      hunks: [{
        old_start: 60,
        old_count: 1,
        new_start: 60,
        new_count: 1,
        lines: [
          { type: "context", content: "visible context", old_num: 60, new_num: 60 },
        ],
      }],
    }), {
      reviewThreads: [makeReviewThread({
        id: "thread-file",
        line: 1,
        new_line: 1,
        line_type: "file",
        body: "File-level note",
      })],
    });

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

    await fireEvent.click(screen.getByRole("button", { name: "Comment on new line 1" }));
    expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
    expect(document.querySelectorAll(".gutter-new.gutter--selected")).toHaveLength(1);

    await rerender({
      file,
      owner,
      name: "n",
      number: 1,
      reviewEnabled: false,
      diffHeadSHA: "diff-head",
    });

    expect(screen.queryByPlaceholderText("Leave a comment")).toBeNull();
    expect(document.querySelectorAll(".gutter-new.gutter--selected")).toHaveLength(0);

    await rerender({
      file,
      owner,
      name: "n",
      number: 1,
      reviewEnabled: true,
      diffHeadSHA: "new-diff-head",
    });
    await fireEvent.click(screen.getByRole("button", { name: "Comment on new line 1" }));
    expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();

    await rerender({
      file,
      owner,
      name: "n",
      number: 1,
      reviewEnabled: true,
      diffHeadSHA: "another-diff-head",
    });

    expect(screen.queryByPlaceholderText("Leave a comment")).toBeNull();
    expect(document.querySelectorAll(".gutter-new.gutter--selected")).toHaveLength(0);
  });
});
