import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { DiffFile, FilesResult } from "../../api/types.js";
import { STORES_KEY } from "../../context.js";
import type { DiffStore } from "../../stores/diff.svelte.js";
import DiffSidebar from "./DiffSidebar.svelte";
import PierreFileTree from "./PierreFileTree.svelte";

if (!globalThis.CSS) {
  globalThis.CSS = {} as typeof CSS;
}
globalThis.CSS.escape ??= (value: string) => value.replace(/\\/g, "\\\\").replace(/"/g, '\\"');

function makeFile(path: string, status: DiffFile["status"] = "modified"): DiffFile {
  return {
    path,
    old_path: path,
    status,
    is_binary: false,
    is_whitespace_only: false,
    additions: 1,
    deletions: 1,
    patch: "",
    hunks: [],
  };
}

function makeFilesResult(files: DiffFile[]): FilesResult {
  return {
    stale: false,
    files,
  };
}

function makeDiffStore(files: DiffFile[], overrides: Partial<DiffStore> = {}): DiffStore {
  const fileList = makeFilesResult(files);
  return {
    getVisibleFileList: () => fileList,
    getFileList: () => fileList,
    isFileListLoading: () => false,
    getActiveFile: () => files[0]?.path ?? null,
    getActiveFileRevealKey: () => 0,
    requestScrollToFile: vi.fn(),
    ...overrides,
  } as unknown as DiffStore;
}

function renderSidebar(diff: DiffStore) {
  const pulls = { getSelectedPR: () => null };
  return render(DiffSidebar, {
    props: { showCommits: false },
    context: new Map([[STORES_KEY, { diff, pulls }]]),
  });
}

function treeRoot(): ShadowRoot | null | undefined {
  const host = document.querySelector(".diff-file-tree");
  expect(host).toBeTruthy();
  return host?.shadowRoot;
}

async function findTreeItem(path: string): Promise<HTMLElement> {
  const selector = `[data-item-path="${CSS.escape(path)}"]`;
  let item: HTMLElement | null | undefined;
  await waitFor(() => {
    item = treeRoot()?.querySelector<HTMLElement>(selector);
    expect(item).toBeTruthy();
  });
  return item!;
}

describe("DiffSidebar", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders the Pierre file tree from diff files", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    const files = [
      makeFile("src/app.ts", "modified"),
      makeFile("src/new.ts", "added"),
      makeFile("docs/old.md", "deleted"),
    ];

    renderSidebar(makeDiffStore(files));

    expect((await findTreeItem("src/app.ts")).getAttribute("data-item-git-status")).toBe("modified");
    expect((await findTreeItem("src/new.ts")).getAttribute("data-item-git-status")).toBe("added");
    expect((await findTreeItem("docs/old.md")).getAttribute("data-item-git-status")).toBe("deleted");
    const modifiedItem = await findTreeItem("src/app.ts");
    const visibleStatusSections = Array.from(
      modifiedItem.querySelectorAll("[data-item-section='git'], [data-item-section='decoration']"),
    )
      .map((node) => node.textContent?.trim())
      .filter(Boolean);
    expect(visibleStatusSections).toEqual(["M"]);
    expect(consoleError).not.toHaveBeenCalledWith(expect.stringContaining("effect_update_depth_exceeded"));
  });

  it("preserves diff file order without folding case", async () => {
    const files = [makeFile("src/B.ts"), makeFile("src/C.ts"), makeFile("src/a.ts")];
    renderSidebar(makeDiffStore(files));

    const wantedPaths = new Set(files.map((file) => file.path));
    await waitFor(() => {
      const renderedPaths = Array.from(treeRoot()?.querySelectorAll<HTMLElement>("[data-item-path]") ?? [])
        .map((item) => item.dataset.itemPath ?? "")
        .filter((path) => wantedPaths.has(path));
      expect(renderedPaths).toEqual(["src/B.ts", "src/C.ts", "src/a.ts"]);
    });
  });

  it("filters both visible rows and tree status data without rebuilding in a loop", async () => {
    const files = Array.from({ length: 11 }, (_, i) => makeFile(i === 10 ? "docs/readme.md" : `src/file-${i}.ts`));
    renderSidebar(makeDiffStore(files));

    await fireEvent.input(screen.getByPlaceholderText("Filter files..."), {
      target: { value: "readme" },
    });

    await findTreeItem("docs/readme.md");
    await waitFor(() => {
      expect(treeRoot()?.querySelector('[data-item-path="src/file-0.ts"]')).toBeNull();
    });
  });

  it("selects active path changes without scrolling the file tree", async () => {
    const files = Array.from({ length: 12 }, (_, i) => makeFile(`src/file-${i}.ts`));
    const originalScrollIntoView = HTMLElement.prototype.scrollIntoView;
    const scrolledPaths: string[] = [];
    HTMLElement.prototype.scrollIntoView = function scrollIntoView() {
      scrolledPaths.push(this.dataset.itemPath ?? "");
    };

    try {
      const { rerender } = render(PierreFileTree, {
        props: {
          files,
          selectedPath: "src/file-0.ts",
        },
      });

      await findTreeItem("src/file-0.ts");
      expect(scrolledPaths).toEqual([]);

      await rerender({
        files,
        selectedPath: "src/file-8.ts",
      });

      await waitFor(() => {
        expect(treeRoot()?.querySelector('[data-item-path="src/file-8.ts"]')?.getAttribute("aria-selected")).toBe(
          "true",
        );
      });
      expect(scrolledPaths).toEqual([]);
    } finally {
      HTMLElement.prototype.scrollIntoView = originalScrollIntoView;
    }
  });

  it("scrolls the selected file tree row into view when reveal key changes", async () => {
    const files = Array.from({ length: 12 }, (_, i) => makeFile(`src/file-${i}.ts`));
    const originalScrollIntoView = HTMLElement.prototype.scrollIntoView;
    const scrolledPaths: string[] = [];
    HTMLElement.prototype.scrollIntoView = function scrollIntoView() {
      scrolledPaths.push(this.dataset.itemPath ?? "");
    };

    try {
      const { rerender } = render(PierreFileTree, {
        props: {
          files,
          selectedPath: "src/file-0.ts",
          selectedPathRevealKey: 0,
        },
      });

      await findTreeItem("src/file-0.ts");
      expect(scrolledPaths).toEqual([]);

      await rerender({
        files,
        selectedPath: "src/file-8.ts",
        selectedPathRevealKey: 1,
      });

      await findTreeItem("src/file-8.ts");
      await waitFor(() => {
        expect(scrolledPaths).toContain("src/file-8.ts");
      });
    } finally {
      HTMLElement.prototype.scrollIntoView = originalScrollIntoView;
    }
  });

  it("scrolls the selected file tree row into view when mounted with a pending reveal key", async () => {
    const files = Array.from({ length: 12 }, (_, i) => makeFile(`src/file-${i}.ts`));
    const originalScrollIntoView = HTMLElement.prototype.scrollIntoView;
    const scrolledPaths: string[] = [];
    HTMLElement.prototype.scrollIntoView = function scrollIntoView() {
      scrolledPaths.push(this.dataset.itemPath ?? "");
    };

    try {
      render(PierreFileTree, {
        props: {
          files,
          selectedPath: "src/file-8.ts",
          selectedPathRevealKey: 1,
        },
      });

      await findTreeItem("src/file-8.ts");
      await waitFor(() => {
        expect(scrolledPaths).toContain("src/file-8.ts");
      });
    } finally {
      HTMLElement.prototype.scrollIntoView = originalScrollIntoView;
    }
  });

  it("does not reuse a stale reveal key after the selected path was filtered out", async () => {
    const files = [makeFile("src/file-0.ts"), makeFile("src/file-8.ts")];
    const originalScrollIntoView = HTMLElement.prototype.scrollIntoView;
    const scrolledPaths: string[] = [];
    HTMLElement.prototype.scrollIntoView = function scrollIntoView() {
      scrolledPaths.push(this.dataset.itemPath ?? "");
    };

    try {
      const { rerender } = render(PierreFileTree, {
        props: {
          files,
          selectedPath: "src/file-0.ts",
          selectedPathRevealKey: 0,
        },
      });

      await findTreeItem("src/file-0.ts");
      expect(scrolledPaths).toEqual([]);

      await rerender({
        files,
        selectedPath: "src/hidden.ts",
        selectedPathRevealKey: 1,
      });

      expect(treeRoot()?.querySelector('[data-item-path="src/hidden.ts"]')).toBeNull();
      expect(scrolledPaths).toEqual([]);

      await rerender({
        files,
        selectedPath: "src/file-8.ts",
        selectedPathRevealKey: 1,
      });

      await waitFor(() => {
        expect(treeRoot()?.querySelector('[data-item-path="src/file-8.ts"]')?.getAttribute("aria-selected")).toBe(
          "true",
        );
      });
      expect(scrolledPaths).toEqual([]);
    } finally {
      HTMLElement.prototype.scrollIntoView = originalScrollIntoView;
    }
  });
});
