import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { DiffFile, DiffResult, FilesResult } from "../../api/types.js";
import { STORES_KEY } from "../../context.js";
import type { DiffScrollTarget, DiffStore } from "../../stores/diff.svelte.js";

vi.mock("./DiffFile.svelte", async () => ({
  default: (await import("./DiffViewTestFile.svelte")).default,
}));

import DiffView from "./DiffView.svelte";

if (!globalThis.CSS) {
  globalThis.CSS = {} as typeof CSS;
}
globalThis.CSS.escape ??= (value: string) => value.replace(/"/g, '\\"');

function makeFile(path: string): DiffFile {
  return {
    path,
    old_path: path,
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: 1,
    deletions: 1,
    patch: "",
    hunks: [],
  };
}

function makeFileWithLine(path: string, line: number): DiffFile {
  return {
    ...makeFile(path),
    patch: `@@ -${line},1 +${line},1 @@\n line ${line}\n`,
    hunks: [
      {
        old_start: line,
        old_count: 1,
        new_start: line,
        new_count: 1,
        lines: [
          {
            type: "context",
            content: `line ${line}`,
            old_num: line,
            new_num: line,
          },
        ],
      },
    ],
  };
}

function makeDiffStore(overrides: Partial<DiffStore> = {}): DiffStore {
  const activeFile = overrides.getActiveFile?.() ?? "a.ts";
  const diff: DiffResult = {
    stale: false,
    whitespace_only_count: 0,
    files: [makeFile(activeFile)],
  };
  const fileList: FilesResult = {
    stale: false,
    files: [makeFile("a.ts"), makeFile("b.ts")],
  };

  return {
    getDiff: () => diff,
    getVisibleDiffFiles: () => diff.files,
    getVisibleFileList: () => fileList,
    isDiffLoading: () => false,
    getDiffError: () => null,
    getTabWidth: () => 4,
    getWordWrap: () => false,
    getRichPreview: () => false,
    getFilePreviewGeneration: () => 0,
    getScrollTarget: () => null,
    consumeScrollTarget: vi.fn(),
    clearScrolling: vi.fn(),
    isScrolling: () => false,
    isFileCollapsed: () => false,
    toggleFileCollapsed: vi.fn(),
    setActiveFile: vi.fn(),
    getActiveFile: () => activeFile,
    requestScrollToFile: vi.fn(),
    stepPrev: vi.fn(),
    stepNext: vi.fn(),
    loadDiff: vi.fn(),
    clearDiff: vi.fn(),
    ...overrides,
  } as unknown as DiffStore;
}

function renderDiffView(
  diff: DiffStore,
  props: {
    keyboardActive?: boolean;
    pageKeyboardActive?: boolean;
  } = {},
) {
  return render(DiffView, {
    props: {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 1,
      loadOnMount: false,
      ...props,
    },
    context: new Map([[STORES_KEY, { diff }]]),
  });
}

describe("DiffView", () => {
  afterEach(() => {
    cleanup();
  });

  it("uses the workspace file list for keyboard navigation", async () => {
    const requestScrollToFile = vi.fn();
    const diff = makeDiffStore({ requestScrollToFile });

    renderDiffView(diff);
    await fireEvent.keyDown(window, { key: "j" });

    expect(requestScrollToFile).toHaveBeenCalledWith("b.ts");
  });

  it("pages the diff area with PageDown even when focus is outside the diff pane", async () => {
    const diff = makeDiffStore();
    const { container } = renderDiffView(diff);
    const diffArea = container.querySelector(".diff-area") as HTMLDivElement;
    Object.defineProperty(diffArea, "clientHeight", {
      configurable: true,
      value: 400,
    });
    Object.defineProperty(diffArea, "scrollHeight", {
      configurable: true,
      value: 2_000,
    });
    const outsideButton = document.createElement("button");
    document.body.append(outsideButton);
    outsideButton.focus();

    try {
      await fireEvent.keyDown(window, { key: "PageDown" });

      expect(diffArea.scrollTop).toBe(336);
      expect(document.activeElement).toBe(diffArea);
    } finally {
      outsideButton.remove();
    }
  });

  it("pages the focused diff area when only local page keys are active", async () => {
    const requestScrollToFile = vi.fn();
    const diff = makeDiffStore({ requestScrollToFile });
    const { container } = renderDiffView(diff, {
      keyboardActive: false,
      pageKeyboardActive: true,
    });
    const diffArea = container.querySelector(".diff-area") as HTMLDivElement;
    Object.defineProperty(diffArea, "clientHeight", {
      configurable: true,
      value: 400,
    });
    Object.defineProperty(diffArea, "scrollHeight", {
      configurable: true,
      value: 2_000,
    });
    const outsideButton = document.createElement("button");
    document.body.append(outsideButton);

    try {
      outsideButton.focus();
      await fireEvent.keyDown(window, { key: "PageDown" });
      expect(diffArea.scrollTop).toBe(0);

      diffArea.focus();
      await fireEvent.keyDown(diffArea, { key: "PageDown" });
      await fireEvent.keyDown(diffArea, { key: "j" });

      expect(diffArea.scrollTop).toBe(336);
      expect(document.activeElement).toBe(diffArea);
      expect(requestScrollToFile).not.toHaveBeenCalled();
    } finally {
      outsideButton.remove();
    }
  });

  it("cancels pending programmatic scroll when the user wheels the diff pane", async () => {
    let scrollTarget: DiffScrollTarget | null = { path: "b.ts" };
    const consumeScrollTarget = vi.fn(() => {
      scrollTarget = null;
    });
    const clearScrolling = vi.fn();
    const files = [makeFile("a.ts"), makeFile("b.ts")];
    const result: DiffResult = {
      stale: false,
      whitespace_only_count: 0,
      files,
    };
    const diff = makeDiffStore({
      getDiff: () => result,
      getVisibleDiffFiles: () => files,
      getScrollTarget: () => scrollTarget,
      consumeScrollTarget,
      clearScrolling,
    });

    const { container } = renderDiffView(diff);
    const diffArea = container.querySelector(".diff-area") as HTMLDivElement;
    await fireEvent.wheel(diffArea);

    expect(consumeScrollTarget).toHaveBeenCalledOnce();
    expect(clearScrolling).toHaveBeenCalledOnce();
  });

  it("keeps a scroll target pending until the file is rendered", async () => {
    const consumeScrollTarget = vi.fn();
    const diff = makeDiffStore({
      getScrollTarget: () => ({ path: "b.ts" }),
      consumeScrollTarget,
    });

    renderDiffView(diff);
    await Promise.resolve();

    expect(consumeScrollTarget).not.toHaveBeenCalled();
  });

  it("scrolls only the diff area for a sidebar file jump", async () => {
    const consumeScrollTarget = vi.fn();
    const scrollIntoView = vi.fn();
    const originalScrollIntoView = Element.prototype.scrollIntoView;
    const originalGetBoundingClientRect = Element.prototype.getBoundingClientRect;
    Element.prototype.scrollIntoView = scrollIntoView;
    Element.prototype.getBoundingClientRect = function () {
      if (this instanceof HTMLElement && this.classList.contains("diff-area")) {
        return {
          top: 100,
          bottom: 500,
          left: 0,
          right: 500,
          width: 500,
          height: 400,
          x: 0,
          y: 100,
          toJSON: () => ({}),
        } as DOMRect;
      }
      if (this instanceof HTMLElement && this.dataset.filePath === "b.ts") {
        const diffArea = document.querySelector(".diff-area") as HTMLDivElement;
        const top = 460 - diffArea.scrollTop;
        return {
          top,
          bottom: top + 40,
          left: 0,
          right: 500,
          width: 500,
          height: 40,
          x: 0,
          y: top,
          toJSON: () => ({}),
        } as DOMRect;
      }
      return originalGetBoundingClientRect.call(this);
    };

    try {
      const files = [makeFile("a.ts"), makeFile("b.ts")];
      const result: DiffResult = {
        stale: false,
        whitespace_only_count: 0,
        files,
      };
      const diff = makeDiffStore({
        getDiff: () => result,
        getVisibleDiffFiles: () => files,
        getScrollTarget: () => ({ path: "b.ts" }),
        consumeScrollTarget,
      });

      const { container } = renderDiffView(diff);

      const diffArea = container.querySelector(".diff-area") as HTMLDivElement;
      await waitFor(() => {
        expect(diffArea.scrollTop).toBe(360);
        expect(scrollIntoView).not.toHaveBeenCalled();
        expect(consumeScrollTarget).toHaveBeenCalled();
      });
    } finally {
      Element.prototype.scrollIntoView = originalScrollIntoView;
      Element.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it("normalizes legacy string scroll targets", async () => {
    const consumeScrollTarget = vi.fn();
    const originalGetBoundingClientRect = Element.prototype.getBoundingClientRect;
    Element.prototype.getBoundingClientRect = function () {
      if (this instanceof HTMLElement && this.classList.contains("diff-area")) {
        return {
          top: 100,
          bottom: 500,
          left: 0,
          right: 500,
          width: 500,
          height: 400,
          x: 0,
          y: 100,
          toJSON: () => ({}),
        } as DOMRect;
      }
      if (this instanceof HTMLElement && this.dataset.filePath === "b.ts") {
        const diffArea = document.querySelector(".diff-area") as HTMLDivElement;
        const top = 460 - diffArea.scrollTop;
        return {
          top,
          bottom: top + 40,
          left: 0,
          right: 500,
          width: 500,
          height: 40,
          x: 0,
          y: top,
          toJSON: () => ({}),
        } as DOMRect;
      }
      return originalGetBoundingClientRect.call(this);
    };

    try {
      const files = [makeFile("a.ts"), makeFile("b.ts")];
      const result: DiffResult = {
        stale: false,
        whitespace_only_count: 0,
        files,
      };
      const diff = makeDiffStore({
        getDiff: () => result,
        getVisibleDiffFiles: () => files,
        getScrollTarget: () => "b.ts" as unknown as DiffScrollTarget,
        consumeScrollTarget,
      });

      const { container } = renderDiffView(diff);
      const diffArea = container.querySelector(".diff-area") as HTMLDivElement;

      await waitFor(() => {
        expect(diffArea.scrollTop).toBe(360);
        expect(consumeScrollTarget).toHaveBeenCalled();
      });
    } finally {
      Element.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it("keeps a scroll target pending until the file has layout", async () => {
    const consumeScrollTarget = vi.fn();
    const originalGetBoundingClientRect = Element.prototype.getBoundingClientRect;
    let targetMeasurements = 0;

    Element.prototype.getBoundingClientRect = function () {
      if (this instanceof HTMLElement && this.classList.contains("diff-area")) {
        return {
          top: 100,
          bottom: 500,
          left: 0,
          right: 500,
          width: 500,
          height: 400,
          x: 0,
          y: 100,
          toJSON: () => ({}),
        } as DOMRect;
      }
      if (this instanceof HTMLElement && this.dataset.filePath === "b.ts") {
        targetMeasurements += 1;
        if (targetMeasurements === 1) {
          return {
            top: 0,
            bottom: 0,
            left: 0,
            right: 0,
            width: 0,
            height: 0,
            x: 0,
            y: 0,
            toJSON: () => ({}),
          } as DOMRect;
        }
        const diffArea = document.querySelector(".diff-area") as HTMLDivElement;
        const top = 460 - diffArea.scrollTop;
        return {
          top,
          bottom: top + 40,
          left: 0,
          right: 500,
          width: 500,
          height: 40,
          x: 0,
          y: top,
          toJSON: () => ({}),
        } as DOMRect;
      }
      return originalGetBoundingClientRect.call(this);
    };

    try {
      const files = [makeFile("a.ts"), makeFile("b.ts")];
      const result: DiffResult = {
        stale: false,
        whitespace_only_count: 0,
        files,
      };
      const diff = makeDiffStore({
        getDiff: () => result,
        getVisibleDiffFiles: () => files,
        getScrollTarget: () => ({ path: "b.ts" }),
        consumeScrollTarget,
      });

      const { container } = renderDiffView(diff);

      const diffArea = container.querySelector(".diff-area") as HTMLDivElement;
      await waitFor(() => {
        expect(diffArea.scrollTop).toBe(360);
        expect(consumeScrollTarget).toHaveBeenCalled();
        expect(targetMeasurements).toBeGreaterThan(1);
      });
    } finally {
      Element.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it("refocuses a line target after it is visible before consuming it", async () => {
    const consumeScrollTarget = vi.fn();
    const focus = vi.fn();
    const originalFocus = HTMLElement.prototype.focus;
    const originalGetBoundingClientRect = Element.prototype.getBoundingClientRect;
    HTMLElement.prototype.focus = focus;
    Element.prototype.getBoundingClientRect = function () {
      if (this instanceof HTMLElement && this.classList.contains("diff-area")) {
        return {
          top: 100,
          bottom: 500,
          left: 0,
          right: 500,
          width: 500,
          height: 400,
          x: 0,
          y: 100,
          toJSON: () => ({}),
        } as DOMRect;
      }
      if (this instanceof HTMLElement && this.dataset.diffPath === "b.ts" && this.dataset.diffNewLine === "2") {
        const diffArea = document.querySelector(".diff-area") as HTMLDivElement;
        const top = 460 - diffArea.scrollTop;
        return {
          top,
          bottom: top + 24,
          left: 0,
          right: 500,
          width: 500,
          height: 24,
          x: 0,
          y: top,
          toJSON: () => ({}),
        } as DOMRect;
      }
      return originalGetBoundingClientRect.call(this);
    };

    try {
      const files = [makeFile("a.ts"), makeFileWithLine("b.ts", 2)];
      const result: DiffResult = {
        stale: false,
        whitespace_only_count: 0,
        files,
      };
      const diff = makeDiffStore({
        getDiff: () => result,
        getVisibleDiffFiles: () => files,
        getScrollTarget: () => ({
          path: "b.ts",
          line: 2,
          side: "right",
        }),
        consumeScrollTarget,
      });

      renderDiffView(diff);

      await waitFor(() => {
        expect(consumeScrollTarget).toHaveBeenCalled();
        expect(focus).toHaveBeenCalledTimes(2);
      });
    } finally {
      HTMLElement.prototype.focus = originalFocus;
      Element.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it("finds line targets rendered inside Pierre shadow roots", async () => {
    const consumeScrollTarget = vi.fn();
    const focus = vi.fn();
    const originalFocus = HTMLElement.prototype.focus;
    const originalGetBoundingClientRect = Element.prototype.getBoundingClientRect;
    HTMLElement.prototype.focus = focus;
    Element.prototype.getBoundingClientRect = function () {
      if (this instanceof HTMLElement && this.classList.contains("diff-area")) {
        return {
          top: 100,
          bottom: 500,
          left: 0,
          right: 500,
          width: 500,
          height: 400,
          x: 0,
          y: 100,
          toJSON: () => ({}),
        } as DOMRect;
      }
      if (this instanceof HTMLElement && this.dataset.diffPath === "b.ts" && this.dataset.diffNewLine === "42") {
        const diffArea = document.querySelector(".diff-area") as HTMLDivElement;
        const top = 460 - diffArea.scrollTop;
        return {
          top,
          bottom: top + 24,
          left: 0,
          right: 500,
          width: 500,
          height: 24,
          x: 0,
          y: top,
          toJSON: () => ({}),
        } as DOMRect;
      }
      if (this instanceof HTMLElement && this.dataset.filePath === "b.ts") {
        return {
          top: 460,
          bottom: 520,
          left: 0,
          right: 500,
          width: 500,
          height: 60,
          x: 0,
          y: 460,
          toJSON: () => ({}),
        } as DOMRect;
      }
      return originalGetBoundingClientRect.call(this);
    };

    try {
      const files = [makeFile("a.ts"), makeFile("b.ts")];
      const result: DiffResult = {
        stale: false,
        whitespace_only_count: 0,
        files,
      };
      const diff = makeDiffStore({
        getDiff: () => result,
        getVisibleDiffFiles: () => files,
        getScrollTarget: () => ({
          path: "b.ts",
          line: 42,
          side: "right",
        }),
        consumeScrollTarget,
      });

      const { container } = renderDiffView(diff);
      const file = container.querySelector<HTMLElement>('[data-file-path="b.ts"]');
      const host = document.createElement("div");
      const shadowTarget = document.createElement("button");
      shadowTarget.dataset.diffPath = "b.ts";
      shadowTarget.dataset.diffNewLine = "42";
      file?.append(host);
      host.attachShadow({ mode: "open" }).append(shadowTarget);

      await waitFor(() => {
        expect(consumeScrollTarget).toHaveBeenCalled();
        expect(focus).toHaveBeenCalledWith({ preventScroll: true });
      });
    } finally {
      HTMLElement.prototype.focus = originalFocus;
      Element.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it("keeps a line scroll target pending until that line renders", async () => {
    const consumeScrollTarget = vi.fn();
    const originalGetBoundingClientRect = Element.prototype.getBoundingClientRect;
    Element.prototype.getBoundingClientRect = function () {
      if (this instanceof HTMLElement && this.classList.contains("diff-area")) {
        return {
          top: 100,
          bottom: 500,
          left: 0,
          right: 500,
          width: 500,
          height: 400,
          x: 0,
          y: 100,
          toJSON: () => ({}),
        } as DOMRect;
      }
      if (this instanceof HTMLElement && this.dataset.filePath === "b.ts") {
        return {
          top: 120,
          bottom: 180,
          left: 0,
          right: 500,
          width: 500,
          height: 60,
          x: 0,
          y: 120,
          toJSON: () => ({}),
        } as DOMRect;
      }
      return originalGetBoundingClientRect.call(this);
    };

    try {
      const files = [makeFile("a.ts"), makeFile("b.ts")];
      const result: DiffResult = {
        stale: false,
        whitespace_only_count: 0,
        files,
      };
      const diff = makeDiffStore({
        getDiff: () => result,
        getVisibleDiffFiles: () => files,
        getScrollTarget: () => ({
          path: "b.ts",
          line: 42,
          side: "right",
        }),
        consumeScrollTarget,
      });

      renderDiffView(diff);
      await Promise.resolve();

      expect(consumeScrollTarget).not.toHaveBeenCalled();
    } finally {
      Element.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it("consumes an unreachable line target after bounded retries", async () => {
    const consumeScrollTarget = vi.fn();
    const clearScrolling = vi.fn();
    const originalGetBoundingClientRect = Element.prototype.getBoundingClientRect;
    let scrollTarget: DiffScrollTarget | null = {
      path: "b.ts",
      line: 42,
      side: "right",
    };
    let rafId = 0;

    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      const id = ++rafId;
      queueMicrotask(() => callback(performance.now()));
      return id;
    });
    vi.stubGlobal("cancelAnimationFrame", vi.fn());

    Element.prototype.getBoundingClientRect = function () {
      if (this instanceof HTMLElement && this.classList.contains("diff-area")) {
        return {
          top: 100,
          bottom: 500,
          left: 0,
          right: 500,
          width: 500,
          height: 400,
          x: 0,
          y: 100,
          toJSON: () => ({}),
        } as DOMRect;
      }
      if (this instanceof HTMLElement && this.dataset.filePath === "b.ts") {
        return {
          top: 120,
          bottom: 180,
          left: 0,
          right: 500,
          width: 500,
          height: 60,
          x: 0,
          y: 120,
          toJSON: () => ({}),
        } as DOMRect;
      }
      return originalGetBoundingClientRect.call(this);
    };

    try {
      const files = [makeFile("a.ts"), makeFile("b.ts")];
      const result: DiffResult = {
        stale: false,
        whitespace_only_count: 0,
        files,
      };
      const diff = makeDiffStore({
        getDiff: () => result,
        getVisibleDiffFiles: () => files,
        getScrollTarget: () => scrollTarget,
        consumeScrollTarget: () => {
          scrollTarget = null;
          consumeScrollTarget();
        },
        clearScrolling,
      });

      renderDiffView(diff);

      await waitFor(() => {
        expect(consumeScrollTarget).toHaveBeenCalledOnce();
        expect(clearScrolling).toHaveBeenCalledOnce();
      });
    } finally {
      vi.unstubAllGlobals();
      Element.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });
});
