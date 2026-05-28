import {
  cleanup,
  render,
  waitFor,
} from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DiffFile } from "../../api/types.js";

const pierre = (() => {
  const counts = {
    cleanUp: 0,
    render: 0,
  };
  const cleanUp = () => {
    counts.cleanUp += 1;
  };
  const renderDiff = () => {
    counts.render += 1;
    return true;
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
    cleanUp = cleanUp;
    expandHunk = () => {};
    render = renderDiff;
    setOptions = () => {};
    setSelectedLines = () => {};
    setThemeType = () => {};
  }
  return {
    cleanUp,
    cleanUpCount: () => counts.cleanUp,
    FileDiff,
    metadata,
    parsePatchFiles: () => [{ files: [metadata] }],
    processFile: () => metadata,
    renderDiff,
    renderCount: () => counts.render,
    reset: () => {
      counts.cleanUp = 0;
      counts.render = 0;
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
});
