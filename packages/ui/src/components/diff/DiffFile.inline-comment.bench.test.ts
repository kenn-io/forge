import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import type { DiffFile as DiffFileType } from "../../api/types.js";
import { STORES_KEY } from "../../context.js";
import { createDiffStore } from "../../stores/diff.svelte.js";
import type { DiffReviewLineRange } from "../../stores/diff-review-draft.svelte.js";
import DiffFile from "./DiffFile.svelte";

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

  originalReplaceSync = (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet
    ?.prototype.replaceSync;
  if ((globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype) {
    (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync
      ??= function replaceSync(): void {};
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
      (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync =
        originalReplaceSync as (text: string) => void;
    } else {
      delete (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync;
    }
  }
});

afterEach(() => {
  cleanup();
});

function makeLargeFile(lineCount: number): DiffFileType {
  const lines = [
    { type: "context" as const, content: "export function render() {", old_num: 1, new_num: 1 },
    ...Array.from({ length: lineCount }, (_, index) => ({
      type: "add" as const,
      content: `  renderLine(${index}, "value-${index}");`,
      new_num: index + 2,
    })),
  ];
  const patchLines = [
    "diff --git a/src/large.ts b/src/large.ts",
    "--- a/src/large.ts",
    "+++ b/src/large.ts",
    `@@ -1,1 +1,${lineCount + 1} @@`,
    " export function render() {",
    ...Array.from({ length: lineCount }, (_, index) =>
      `+  renderLine(${index}, "value-${index}");`
    ),
  ];

  return {
    path: "src/large.ts",
    old_path: "src/large.ts",
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: lineCount,
    deletions: 0,
    patch: `${patchLines.join("\n")}\n`,
    hunks: [{
      old_start: 1,
      old_count: 1,
      new_start: 1,
      new_count: lineCount + 1,
      lines,
    }],
  };
}

function renderDiffFile(file: DiffFileType) {
  const diff = createDiffStore();
  const diffReviewDraft = {
    getComments: () => [],
    isSubmitting: () => false,
    getError: () => null,
    createComment: (_body: string, _range: DiffReviewLineRange) => Promise.resolve(true),
    deleteComment: () => Promise.resolve(true),
  };

  return render(DiffFile, {
    props: {
      file,
      provider: "github",
      platformHost: "github.com",
      owner: `bench-owner-${file.additions}`,
      name: "widgets",
      repoPath: "bench-owner/widgets",
      number: 42,
      reviewEnabled: true,
      diffHeadSHA: "diff-head",
      nativeMultilineRanges: true,
    },
    context: new Map([[
      STORES_KEY,
      {
        diff,
        diffReviewDraft,
        detail: {
          replyToDiscussion: () => Promise.resolve(true),
        },
      },
    ]]),
  });
}

async function waitForRenderedDiff(): Promise<void> {
  await waitFor(() => {
    const host = document.querySelector(".pierre-diff");
    expect(host?.shadowRoot?.querySelector("[data-content]")).toBeTruthy();
  }, { timeout: 30_000 });
}

async function findLineTarget(line: number): Promise<HTMLElement> {
  return await waitFor(() => {
    const target = document
      .querySelector(".pierre-diff")
      ?.shadowRoot
      ?.querySelector<HTMLElement>(
        `[data-column-number="${line}"][data-line-type="change-addition"]`,
      );
    expect(target).toBeTruthy();
    return target!;
  }, { timeout: 30_000 });
}

async function openAndCloseComposer(line: number): Promise<number> {
  const target = await findLineTarget(line);
  const startedAt = performance.now();

  await fireEvent.pointerDown(target, {
    button: 0,
    pointerId: 1,
    pointerType: "mouse",
  });
  await fireEvent.pointerUp(document, {
    pointerId: 1,
    pointerType: "mouse",
  });
  await waitFor(() => {
    expect(screen.getByPlaceholderText("Leave a comment")).toBeTruthy();
  }, { timeout: 30_000 });

  const elapsedMs = performance.now() - startedAt;
  await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() => {
    expect(screen.queryByPlaceholderText("Leave a comment")).toBeNull();
  }, { timeout: 30_000 });

  return elapsedMs;
}

function percentile(values: number[], p: number): number {
  const sorted = [...values].sort((a, b) => a - b);
  const index = Math.min(
    sorted.length - 1,
    Math.max(0, Math.ceil((p / 100) * sorted.length) - 1),
  );
  return sorted[index] ?? 0;
}

function benchmarkLineCounts(): number[] {
  const raw = process.env.DIFF_INLINE_COMMENT_BENCH_LINES;
  if (!raw) return [100, 500, 1_000];
  const counts = raw
    .split(",")
    .map((part) => Number(part.trim()))
    .filter((value) => Number.isInteger(value) && value > 0);
  if (counts.length === 0) {
    throw new Error(`invalid DIFF_INLINE_COMMENT_BENCH_LINES=${JSON.stringify(raw)}`);
  }
  return counts;
}

const benchDescribe = process.env.RUN_DIFF_INLINE_COMMENT_BENCH === "1"
  ? describe
  : describe.skip;

benchDescribe("DiffFile inline comment opening benchmark", () => {
  it("measures opening and closing an inline composer by diff size", async () => {
    const results = [];
    const samples = Number(process.env.DIFF_INLINE_COMMENT_BENCH_SAMPLES ?? "7");
    const warmups = Number(process.env.DIFF_INLINE_COMMENT_BENCH_WARMUPS ?? "2");
    const sizes = benchmarkLineCounts();

    for (const lineCount of sizes) {
      renderDiffFile(makeLargeFile(lineCount));
      await waitForRenderedDiff();
      const line = lineCount + 1;
      for (let i = 0; i < warmups; i += 1) {
        await openAndCloseComposer(line);
      }
      const timings = [];
      for (let i = 0; i < samples; i += 1) {
        timings.push(await openAndCloseComposer(line));
      }
      results.push({
        lineCount,
        samples,
        medianMs: Number(percentile(timings, 50).toFixed(2)),
        p95Ms: Number(percentile(timings, 95).toFixed(2)),
        minMs: Number(Math.min(...timings).toFixed(2)),
        maxMs: Number(Math.max(...timings).toFixed(2)),
      });
      cleanup();
    }

    console.log(`[diff-inline-comment-bench] ${JSON.stringify(results)}`);
    expect(results).toHaveLength(sizes.length);
  }, 180_000);
});
