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
    render: 0,
  };
  let renderResults: boolean[] = [];
  let lastOptions: FileDiffOptions<unknown> | undefined;
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
  return {
    cleanUp,
    cleanUpCount: () => counts.cleanUp,
    FileDiff,
    lastOptions: () => lastOptions,
    metadata,
    parsePatchFiles: () => [{ files: [metadata] }],
    processFile: () => metadata,
    renderDiff,
    renderCount: () => counts.render,
    reset: () => {
      counts.cleanUp = 0;
      counts.render = 0;
      renderResults = [];
      lastOptions = undefined;
    },
    setRenderResults: (results: boolean[]) => {
      renderResults = [...results];
    },
  };
})();

vi.doMock("@pierre/diffs", () => ({
  FileDiff: pierre.FileDiff,
  parsePatchFiles: pierre.parsePatchFiles,
  processFile: pierre.processFile,
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

  it("cleans up rendered Pierre instances when deactivated", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const originalGetBoundingClientRect = Element.prototype.getBoundingClientRect;
    Element.prototype.getBoundingClientRect = function () {
      if (this instanceof HTMLElement && this.tagName === "DIFFS-CONTAINER") {
        return {
          top: 0,
          bottom: 240,
          left: 0,
          right: 500,
          width: 500,
          height: 240,
          x: 0,
          y: 0,
          toJSON: () => ({}),
        } as DOMRect;
      }
      return originalGetBoundingClientRect.call(this);
    };

    try {
      const file = makeFile();
      const { container, rerender } = render(PierreFileDiff, {
        props: { active: true, file },
      });

      await waitFor(() => {
        expect(pierre.renderCount()).toBe(1);
      });

      vi.useFakeTimers();
      await rerender({ active: false, file });
      await vi.advanceTimersByTimeAsync(10_000);

      expect(pierre.cleanUpCount()).toBe(1);
      expect(
        container.querySelector<HTMLElement>(".pierre-diff-shell")?.style.minHeight,
      ).toBe("240px");
    } finally {
      Element.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it("retries when Pierre declines an initial render attempt", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    pierre.setRenderResults([false, true]);

    render(PierreFileDiff, {
      props: { active: true, file: makeFile() },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(2);
    });
  });

  it("renders an inactive expanded file after a fallback viewport probe", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const originalGetBoundingClientRect = Element.prototype.getBoundingClientRect;
    const diffArea = document.createElement("div");
    diffArea.className = "diff-area";
    document.body.appendChild(diffArea);
    let fileNearViewport = false;
    Element.prototype.getBoundingClientRect = function () {
      if (this instanceof HTMLElement && this.classList.contains("diff-area")) {
        return rect({ top: 0, bottom: 400, height: 400 });
      }
      if (this instanceof HTMLElement && this.tagName === "DIFFS-CONTAINER") {
        return fileNearViewport
          ? rect({ top: 80, bottom: 180, height: 100 })
          : rect({ top: 1_200, bottom: 1_300, height: 100 });
      }
      return originalGetBoundingClientRect.call(this);
    };

    try {
      render(PierreFileDiff, {
        target: diffArea,
        props: { active: false, file: makeFile() },
      });

      await Promise.resolve();
      expect(pierre.renderCount()).toBe(0);

      fileNearViewport = true;
      await fireEvent.scroll(diffArea);

      await waitFor(() => {
        expect(pierre.renderCount()).toBe(1);
      });
    } finally {
      Element.prototype.getBoundingClientRect = originalGetBoundingClientRect;
      diffArea.remove();
    }
  });

  it("passes split diff style to Pierre when side-by-side mode is enabled", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");

    render(PierreFileDiff, {
      props: { active: true, file: makeFile(), viewMode: "split" },
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
      props: { active: true, file, lineAnnotations: firstAnnotations },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(1);
    });

    await rerender({ active: true, file, lineAnnotations: nextAnnotations });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(2);
    });
  });

  it("does not rerender when transient annotation metadata changes", async () => {
    const { default: PierreFileDiff } = await import("./PierreFileDiff.svelte");
    const file = makeFile();

    const { rerender } = render(PierreFileDiff, {
      props: { active: true, file },
    });

    await waitFor(() => {
      expect(pierre.renderCount()).toBe(1);
    });

    await rerender({
      active: true,
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

function rect({
  top,
  bottom,
  height,
}: {
  top: number;
  bottom: number;
  height: number;
}): DOMRect {
  return {
    top,
    bottom,
    height,
    left: 0,
    right: 500,
    width: 500,
    x: 0,
    y: top,
    toJSON: () => ({}),
  } as DOMRect;
}
