import {
  cleanup,
  render,
  waitFor,
} from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DiffLineAnnotation, FileDiffOptions } from "@pierre/diffs";
import type { DiffFile } from "../../api/types.js";

const pierre = (() => {
  const counts = {
    cleanUp: 0,
    render: 0,
    virtualized: 0,
  };
  let renderResults: boolean[] = [];
  let lastOptions: FileDiffOptions<unknown> | undefined;
  let lastVirtualizer: unknown;
  const cleanUp = () => {
    counts.cleanUp += 1;
  };
  const renderDiff = () => {
    counts.render += 1;
    return renderResults.shift() ?? true;
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
    expandHunk = () => {};
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
    FileDiff,
    lastOptions: () => lastOptions,
    lastVirtualizer: () => lastVirtualizer,
    metadata,
    parsePatchFiles: () => [{ files: [metadata] }],
    processFile: () => metadata,
    renderDiff,
    renderCount: () => counts.render,
    reset: () => {
      counts.cleanUp = 0;
      counts.render = 0;
      counts.virtualized = 0;
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
