import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vite-plus/test";
import type { DiffLineAnnotation, FileDiffOptions } from "@pierre/diffs";
import type { DiffFile } from "../../api/types.js";

type GlobalWithCSSStyleSheet = {
  CSSStyleSheet?: {
    prototype: CSSStyleSheet & { replaceSync?: (text: string) => void };
  };
};

let originalReplaceSync: unknown;

beforeAll(() => {
  originalReplaceSync = (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype.replaceSync;
  if ((globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype) {
    (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync ??= function replaceSync(): void {};
  }
});

afterAll(() => {
  if (!(globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet?.prototype) return;
  if (originalReplaceSync) {
    (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync = originalReplaceSync as (
      text: string,
    ) => void;
  } else {
    delete (globalThis as GlobalWithCSSStyleSheet).CSSStyleSheet.prototype.replaceSync;
  }
});

const pierre = (() => {
  const counts = {
    cleanUp: 0,
    expand: 0,
    render: 0,
    virtualized: 0,
  };
  let renderResults: boolean[] = [];
  let events: string[] = [];
  let lastExpansion:
    | {
        direction: unknown;
        expansionLineCount: number | undefined;
        hunkIndex: number;
      }
    | undefined;
  let lastOptions: FileDiffOptions<unknown> | undefined;
  let lastVirtualizer: unknown;
  const cleanUp = () => {
    counts.cleanUp += 1;
  };
  const renderDiff = (props?: { fileContainer?: HTMLElement }) => {
    counts.render += 1;
    const didRender = renderResults.shift() ?? true;
    events.push(`render:${String(didRender)}`);
    if (didRender && props?.fileContainer) {
      const root = props.fileContainer.shadowRoot ?? props.fileContainer.attachShadow({ mode: "open" });
      root.innerHTML = `
        <pre data-diff-type="unified">
          <code data-unified>
            <div data-gutter>
              <div data-line-index="0" data-line-type="change-addition"></div>
            </div>
            <div data-content>
              <div data-line data-line-index="0" data-line-type="change-addition">func newName() {}</div>
            </div>
          </code>
          <div data-separator data-expand-index="0">
            <button type="button" data-expand-button data-expand-down>expand</button>
          </div>
        </pre>
      `;
    }
    return didRender;
  };
  const metadata = {
    additionLines: ["new line\n"],
    deletionLines: ["old line\n"],
    hunks: [
      {
        collapsedBefore: 0,
        hunkSpecs: "@@ -1,2 +1,2 @@",
      },
    ],
  };
  let parsedMetadata = metadata;
  class FileDiff {
    constructor(options?: FileDiffOptions<unknown>) {
      lastOptions = options;
    }
    cleanUp = cleanUp;
    expandHunk = (hunkIndex: number, direction: unknown, expansionLineCount?: number) => {
      counts.expand += 1;
      events.push("expand");
      lastExpansion = { direction, expansionLineCount, hunkIndex };
    };
    getLineIndex = (lineNumber: number): [number, number] => [lineNumber, lineNumber];
    render = renderDiff;
    setOptions = (options?: FileDiffOptions<unknown>) => {
      lastOptions = options;
    };
    setSelectedLines = () => {};
    setThemeType = () => {};
  }
  class VirtualizedFileDiff extends FileDiff {
    constructor(options?: FileDiffOptions<unknown>, virtualizer?: unknown) {
      super(options);
      counts.virtualized += 1;
      lastVirtualizer = virtualizer;
    }
  }
  return {
    cleanUp,
    cleanUpCount: () => counts.cleanUp,
    expandCount: () => counts.expand,
    events: () => [...events],
    FileDiff,
    lastExpansion: () => lastExpansion,
    lastOptions: () => lastOptions,
    lastVirtualizer: () => lastVirtualizer,
    metadata,
    parsePatchFiles: () => [{ files: [parsedMetadata] }],
    processFile: () => parsedMetadata,
    renderDiff,
    renderCount: () => counts.render,
    reset: () => {
      counts.cleanUp = 0;
      counts.expand = 0;
      counts.render = 0;
      counts.virtualized = 0;
      lastExpansion = undefined;
      events = [];
      renderResults = [];
      lastOptions = undefined;
      lastVirtualizer = undefined;
      parsedMetadata = metadata;
    },
    setMetadata: (next: typeof metadata) => {
      parsedMetadata = next;
    },
    setRenderResults: (results: boolean[]) => {
      renderResults = [...results];
    },
    virtualizedCount: () => counts.virtualized,
    VirtualizedFileDiff,
  };
})();

vi.doMock("@pierre/diffs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@pierre/diffs")>();
  return {
    ...actual,
    FileDiff: pierre.FileDiff,
    parsePatchFiles: pierre.parsePatchFiles,
    processFile: pierre.processFile,
    VirtualizedFileDiff: pierre.VirtualizedFileDiff,
  };
});

vi.doMock("./pierre-worker-pool.js", () => ({
  diffTokenizeMaxLineLength: 180,
  getPierreDiffWorkerPool: () => undefined,
  syntaxHighlightingDisabledForAutomation: () => false,
}));

function makeFile(): DiffFile {
  return {
    path: "src/foo.ts",
    old_path: "src/foo.ts",
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: 1,
    deletions: 1,
    patch: `diff --git a/src/foo.ts b/src/foo.ts
--- a/src/foo.ts
+++ b/src/foo.ts
@@ -1,2 +1,2 @@
 line 1
-old line
+new line
`,
    hunks: [
      {
        old_start: 1,
        old_count: 2,
        new_start: 1,
        new_count: 2,
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
  };
}

function makePatchOnlyFile(): DiffFile {
  return {
    ...makeFile(),
    status: "added",
    old_path: "",
    deletions: 0,
    patch: "@@ -0,0 +1,1 @@\n+export const patchOnly = true;\n",
    hunks: [],
  };
}

function makeMetadataOnlyFile(): DiffFile {
  return {
    ...makeFile(),
    path: "src/new.ts",
    old_path: "src/old.ts",
    status: "renamed",
    additions: 0,
    deletions: 0,
    patch: [
      "diff --git a/src/old.ts b/src/new.ts",
      "similarity index 100%",
      "rename from src/old.ts",
      "rename to src/new.ts",
      "",
    ].join("\n"),
    hunks: [],
  };
}

function makeSyntaxStateGapFile(): DiffFile {
  const path = "src/example.test.ts";
  return {
    ...makeFile(),
    path,
    old_path: path,
    additions: 4,
    deletions: 0,
    patch: `diff --git a/${path} b/${path}
--- a/${path}
+++ b/${path}
@@ -10,3 +10,4 @@ function render() {
 const html = \`
+  <span>new</span>
   <div>
@@ -80,2 +81,4 @@ afterRender();
+vi.doMock("./worker", () => ({
+  run: () => undefined,
+}));
 function makeFile() {}
`,
    hunks: [
      {
        old_start: 10,
        old_count: 3,
        new_start: 10,
        new_count: 4,
        section: "function render() {",
        lines: [
          { type: "context", content: "const html = `", old_num: 10, new_num: 10 },
          { type: "add", content: "  <span>new</span>", new_num: 11 },
          { type: "context", content: "  <div>", old_num: 11, new_num: 12 },
        ],
      },
      {
        old_start: 80,
        old_count: 2,
        new_start: 81,
        new_count: 4,
        section: "afterRender();",
        lines: [
          { type: "add", content: 'vi.doMock("./worker", () => ({', new_num: 81 },
          { type: "add", content: "  run: () => undefined,", new_num: 82 },
          { type: "add", content: "}));", new_num: 83 },
          { type: "context", content: "function makeFile() {}", old_num: 80, new_num: 84 },
        ],
      },
    ],
  };
}

describe("PierreFileDiff", () => {
  afterEach(() => {
    vi.useRealTimers();
    cleanup();
    pierre.reset();
  });

  it("uses Pierre virtualized diffs when a viewer virtualizer is provided", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const virtualizer = { type: "simple" };

    render(PierreFileDiff, {
      props: { file: makeFile(), virtualizer: virtualizer as never },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(1);
    });

    expect(pierre.virtualizedCount()).toBe(1);
    expect(pierre.lastVirtualizer()).toEqual(virtualizer);
  });

  it("retries when Pierre declines an initial render attempt", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    pierre.setRenderResults([false, true]);

    render(PierreFileDiff, {
      props: { file: makeFile() },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(2);
    });
  });

  it("renders patch text even when structured hunks are absent", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");

    render(PierreFileDiff, {
      props: { file: makePatchOnlyFile() },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(1);
    });

    expect(document.querySelector(".empty-textual-diff")).toBeNull();
  });

  it("falls back to sparse rendering when syntax full-context loading fails", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const loadFileText = vi.fn(async () => {
      throw new Error("preview failed");
    });

    render(PierreFileDiff, {
      props: { file: makeSyntaxStateGapFile(), loadFileText },
    });

    await waitFor(() => {
      expect(loadFileText).toHaveBeenCalled();
    });

    await waitFor(() => {
      expect(document.querySelector(".context-error")?.textContent).toContain("preview failed");
      expect(pierre.renderCount()).toBe(1);
    });
  });

  it("shows the empty textual state for metadata-only patches", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    pierre.setMetadata({
      ...pierre.metadata,
      additionLines: [],
      deletionLines: [],
      hunks: [],
    });

    render(PierreFileDiff, {
      props: { file: makeMetadataOnlyFile() },
    });

    await waitFor(() => {
      expect(document.querySelector(".empty-textual-diff")).not.toBeNull();
    });

    expect(pierre.renderCount()).toBe(0);
  });

  it("replays context expansion after a deferred full-context render", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const loadFileText = vi.fn(async (side: "old" | "new") =>
      side === "old" ? "line 1\nold line\n" : "line 1\nnew line\n",
    );
    const hadCancelAnimationFrame = "cancelAnimationFrame" in globalThis;
    const hadRequestAnimationFrame = "requestAnimationFrame" in globalThis;
    const originalCancelAnimationFrame = globalThis.cancelAnimationFrame;
    const originalRequestAnimationFrame = globalThis.requestAnimationFrame;
    const frameCallbacks: FrameRequestCallback[] = [];
    globalThis.requestAnimationFrame = ((callback: FrameRequestCallback) => {
      frameCallbacks.push(callback);
      return frameCallbacks.length;
    }) as typeof requestAnimationFrame;
    globalThis.cancelAnimationFrame = (() => {}) as typeof cancelAnimationFrame;
    pierre.setRenderResults([true, false, true, true]);

    try {
      render(PierreFileDiff, {
        props: { file: makeFile(), loadFileText },
      });

      const expandButton = await waitFor(() => {
        const button = document
          .querySelector(".pierre-diff")
          ?.shadowRoot?.querySelector<HTMLElement>("[data-expand-button]");
        expect(button).toBeTruthy();
        return button!;
      });

      await fireEvent.click(expandButton);

      for (const callback of frameCallbacks.splice(0)) {
        callback(performance.now());
      }

      await waitFor(() => {
        expect(pierre.expandCount()).toBe(1);
      });
      const events = pierre.events();
      const failedRenderIndex = events.indexOf("render:false");
      const replayRenderIndex = events.findIndex(
        (event, index) => index > failedRenderIndex && event === "render:true",
      );
      const expandIndex = events.indexOf("expand");
      expect(failedRenderIndex).toBeGreaterThan(-1);
      expect(replayRenderIndex).toBeGreaterThan(failedRenderIndex);
      expect(expandIndex).toBeGreaterThan(replayRenderIndex);
      expect(pierre.lastExpansion()).toEqual({
        direction: "down",
        expansionLineCount: undefined,
        hunkIndex: 0,
      });
      expect(loadFileText).toHaveBeenCalledTimes(2);
    } finally {
      if (hadRequestAnimationFrame) {
        globalThis.requestAnimationFrame = originalRequestAnimationFrame;
      } else {
        delete (globalThis as { requestAnimationFrame?: unknown }).requestAnimationFrame;
      }
      if (hadCancelAnimationFrame) {
        globalThis.cancelAnimationFrame = originalCancelAnimationFrame;
      } else {
        delete (globalThis as { cancelAnimationFrame?: unknown }).cancelAnimationFrame;
      }
    }
  });

  it("passes split diff style to Pierre when side-by-side mode is enabled", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");

    render(PierreFileDiff, {
      props: { file: makeFile(), viewMode: "split" },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(1);
    });

    expect(pierre.lastOptions()?.diffStyle).toBe("split");
  });

  it("caps syntax tokenization line length for Pierre renders", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const { diffTokenizeMaxLineLength } = await import("./pierre-worker-pool.js");

    render(PierreFileDiff, {
      props: { file: makeFile() },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(1);
    });

    expect(pierre.lastOptions()?.tokenizeMaxLineLength).toBe(diffTokenizeMaxLineLength);
  });

  it("rerenders when annotation metadata changes without moving lines", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const file = makeFile();
    const firstAnnotations: DiffLineAnnotation<unknown>[] = [
      {
        side: "additions",
        lineNumber: 2,
        metadata: { id: "thread-1", body: "old body", canReply: false },
      },
    ];
    const nextAnnotations: DiffLineAnnotation<unknown>[] = [
      {
        side: "additions",
        lineNumber: 2,
        metadata: { id: "thread-1", body: "new body", canReply: true },
      },
    ];

    const { rerender } = render(PierreFileDiff, {
      props: { file, lineAnnotations: firstAnnotations },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(1);
    });

    await rerender({ file, lineAnnotations: nextAnnotations });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(2);
    });
  });

  it("does not rerender when transient annotation metadata changes", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const file = makeFile();

    const { rerender } = render(PierreFileDiff, {
      props: { file },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(1);
    });

    await rerender({
      file,
      selectedRange: { start: 2, end: 2, side: "additions" },
      transientLineAnnotation: {
        side: "additions",
        lineNumber: 2,
        metadata: { id: "composer:additions:2", body: "draft text" },
      },
    });
    await Promise.resolve();

    expect(pierre.renderCount()).toBe(1);
  });
});
