import {
  cleanup,
  fireEvent,
  render,
  waitFor,
} from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DiffLineAnnotation, FileDiffOptions } from "@pierre/diffs";
import type { DiffFile } from "../../api/types.js";

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
    | { direction: unknown; expansionLineCount: number | undefined; hunkIndex: number }
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
    hunks: [{
      collapsedBefore: 0,
      hunkSpecs: "@@ -1,2 +1,2 @@",
    }],
  };
  class FileDiff {
    constructor(options?: FileDiffOptions<unknown>) {
      lastOptions = options;
    }
    cleanUp = cleanUp;
    expandHunk = (
      hunkIndex: number,
      direction: unknown,
      expansionLineCount?: number,
    ) => {
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
    parsePatchFiles: () => [{ files: [metadata] }],
    processFile: () => metadata,
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
    },
    setRenderResults: (results: boolean[]) => {
      renderResults = [...results];
    },
    virtualizedCount: () => counts.virtualized,
    VirtualizedFileDiff,
  };
})();

vi.doMock("@pierre/diffs", () => ({
  FileDiff: pierre.FileDiff,
  parsePatchFiles: pierre.parsePatchFiles,
  processFile: pierre.processFile,
  VirtualizedFileDiff: pierre.VirtualizedFileDiff,
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
    hunks: [{
      old_start: 1,
      old_count: 2,
      new_start: 1,
      new_count: 2,
      lines: [
        { type: "context", content: "line 1", old_num: 1, new_num: 1 },
        { type: "delete", content: "old line", old_num: 2 },
        { type: "add", content: "new line", new_num: 2 },
      ],
    }],
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

  it("replays context expansion after a deferred full-context render", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const loadFileText = vi.fn(async (side: "old" | "new") =>
      side === "old" ? "line 1\nold line\n" : "line 1\nnew line\n"
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
          ?.shadowRoot
          ?.querySelector<HTMLElement>("[data-expand-button]");
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
      const replayRenderIndex = events.findIndex((event, index) =>
        index > failedRenderIndex && event === "render:true"
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

  it("rerenders when annotation metadata changes without moving lines", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const file = makeFile();
    const firstAnnotations: DiffLineAnnotation<unknown>[] = [{
      side: "additions",
      lineNumber: 2,
      metadata: { id: "thread-1", body: "old body", canReply: false },
    }];
    const nextAnnotations: DiffLineAnnotation<unknown>[] = [{
      side: "additions",
      lineNumber: 2,
      metadata: { id: "thread-1", body: "new body", canReply: true },
    }];

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
