import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vite-plus/test";
import type { DiffFile } from "../../api/types.js";
import DiffSummaryChip from "./DiffSummaryChip.svelte";
import { DiffSummaryFilesResult } from "./diff-summary.js";

function file(path: string, additions: number, deletions: number): DiffFile {
  return {
    path,
    old_path: path,
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions,
    deletions,
    patch: "",
    hunks: [],
  };
}

function rowText(popover: HTMLElement, label: string): string {
  const row = Array.from(popover.querySelectorAll(".diff-summary-row")).find((candidate) =>
    candidate.textContent?.includes(label),
  );
  expect(row).toBeTruthy();
  return row?.textContent?.replace(/\s+/g, " ").trim() ?? "";
}

function statLabel(additions: number, deletions: number): string {
  const additionLabel = additions === 1 ? "addition" : "additions";
  const deletionLabel = deletions === 1 ? "deletion" : "deletions";
  return `${additions} ${additionLabel}, ${deletions} ${deletionLabel}`;
}

type GlobalWithResizeObserver = { ResizeObserver?: unknown };
let originalResizeObserver: unknown;
let originalResizeObserverExisted = false;

// kit-ui Tooltip repositions via ResizeObserver, which jsdom lacks.
beforeAll(() => {
  originalResizeObserverExisted = "ResizeObserver" in globalThis;
  originalResizeObserver = (globalThis as GlobalWithResizeObserver).ResizeObserver;
  class ResizeObserverStub {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  (globalThis as GlobalWithResizeObserver).ResizeObserver = ResizeObserverStub;
});

afterAll(() => {
  if (originalResizeObserverExisted) {
    (globalThis as GlobalWithResizeObserver).ResizeObserver = originalResizeObserver;
  } else {
    delete (globalThis as GlobalWithResizeObserver).ResizeObserver;
  }
});

describe("DiffSummaryChip", () => {
  afterEach(() => {
    cleanup();
  });

  it("loads file totals on hover and shows them by category", async () => {
    const loadFiles = vi.fn(async () => [
      file("docs/plan.md", 10, 2),
      file("src/App.svelte", 40, 6),
      file("src/App.test.ts", 20, 8),
      file("mise.toml", 1, 1),
      file("bun.lock", 1, 1),
      {
        ...file("src/api/generated/schema.ts", 2, 2),
        is_generated: true,
      },
    ]);

    render(DiffSummaryChip, {
      props: {
        additions: 74,
        deletions: 20,
        loadFiles: async () => new DiffSummaryFilesResult(false, await loadFiles()),
      },
    });

    const trigger = screen.getByRole("button", { name: statLabel(74, 20) });
    await fireEvent.focusIn(trigger);

    const popover = await screen.findByRole("tooltip");
    // kit Tooltip describes its wrapper span, not the focusable trigger; the
    // chip wires the button to the summary content directly so assistive tech
    // focused on the real button still gets the description.
    const describedBy = trigger.getAttribute("aria-describedby");
    expect(describedBy).toBeTruthy();
    expect(popover.querySelector(`[id="${describedBy}"]`)).not.toBeNull();
    const labels = Array.from(popover.querySelectorAll(".diff-summary-row > span:first-child")).map(
      (label) => label.textContent,
    );
    expect(labels).toEqual(["Plans/docs", "Code", "Tests", "Generated"]);
    expect(screen.getByText("Plans/docs")).toBeTruthy();
    expect(screen.queryByText("Total")).toBeNull();
    expect(rowText(popover, "Plans/docs")).toBe("Plans/docs +10 −2");
    expect(screen.getByText("Code")).toBeTruthy();
    expect(rowText(popover, "Code")).toBe("Code +41 −7");
    expect(screen.getByText("Tests")).toBeTruthy();
    expect(rowText(popover, "Tests")).toBe("Tests +20 −8");
    expect(screen.queryByText("Other")).toBeNull();
    expect(screen.getByText("Generated")).toBeTruthy();
    expect(rowText(popover, "Generated")).toBe("Generated +3 −3");
    expect(loadFiles).toHaveBeenCalledTimes(1);
  });

  it("hides categories with no changed lines", async () => {
    render(DiffSummaryChip, {
      props: {
        additions: 60,
        deletions: 14,
        loadFiles: vi.fn(
          async () =>
            new DiffSummaryFilesResult(false, [file("src/App.svelte", 40, 6), file("src/App.test.ts", 20, 8)]),
        ),
      },
    });

    await fireEvent.focusIn(screen.getByRole("button", { name: statLabel(60, 14) }));

    const popover = await screen.findByRole("tooltip");
    expect(within(popover).getByText("Code")).toBeTruthy();
    expect(rowText(popover, "Code")).toBe("Code +40 −6");
    expect(screen.getByText("Tests")).toBeTruthy();
    expect(rowText(popover, "Tests")).toBe("Tests +20 −8");
    expect(screen.queryByText("Plans/docs")).toBeNull();
    expect(screen.queryByText("Generated")).toBeNull();
    expect(screen.queryByText("Other")).toBeNull();
  });

  it("does not cache stale file responses", async () => {
    const loadFiles = vi
      .fn()
      .mockResolvedValueOnce(new DiffSummaryFilesResult(true, []))
      .mockResolvedValueOnce(new DiffSummaryFilesResult(false, [file("src/App.svelte", 4, 1)]));

    render(DiffSummaryChip, {
      props: {
        additions: 4,
        deletions: 1,
        loadFiles,
      },
    });

    const trigger = screen.getByRole("button", {
      name: statLabel(4, 1),
    });
    await fireEvent.focusIn(trigger);

    expect(await screen.findByText("Changed files are still refreshing.")).toBeTruthy();
    await fireEvent.focusOut(trigger);
    await fireEvent.focusIn(trigger);

    const popover = await screen.findByRole("tooltip");
    expect(within(popover).getByText("Code")).toBeTruthy();
    expect(rowText(popover, "Code")).toBe("Code +4 −1");
    expect(loadFiles).toHaveBeenCalledTimes(2);
  });

  it("discards file responses for superseded summary keys", async () => {
    let resolveFirst: ((value: DiffSummaryFilesResult) => void) | undefined;
    let resolveSecond: ((value: DiffSummaryFilesResult) => void) | undefined;
    const loadFiles = vi
      .fn()
      .mockReturnValueOnce(
        new Promise<DiffSummaryFilesResult>((resolve) => {
          resolveFirst = resolve;
        }),
      )
      .mockReturnValueOnce(
        new Promise<DiffSummaryFilesResult>((resolve) => {
          resolveSecond = resolve;
        }),
      );

    const { rerender } = render(DiffSummaryChip, {
      props: {
        additions: 10,
        deletions: 0,
        summaryKey: "sha-1",
        loadFiles,
      },
    });

    await fireEvent.focusIn(screen.getByRole("button", { name: statLabel(10, 0) }));
    await rerender({
      additions: 5,
      deletions: 1,
      summaryKey: "sha-2",
      loadFiles,
    });

    resolveFirst?.(new DiffSummaryFilesResult(false, [file("docs/old.md", 10, 0)]));
    await waitFor(() => expect(loadFiles).toHaveBeenCalledTimes(2));
    resolveSecond?.(new DiffSummaryFilesResult(false, [file("src/new.ts", 5, 1)]));

    const popover = await screen.findByRole("tooltip");
    expect(within(popover).getByText("Code")).toBeTruthy();
    expect(rowText(popover, "Code")).toBe("Code +5 −1");
    expect(screen.queryByText("Plans/docs")).toBeNull();
  });

  it("reloads immediately when the summary key changes while open", async () => {
    const loadFiles = vi
      .fn()
      .mockResolvedValueOnce(new DiffSummaryFilesResult(false, [file("docs/old.md", 10, 0)]))
      .mockResolvedValueOnce(new DiffSummaryFilesResult(false, [file("src/new.ts", 5, 1)]));

    const { rerender } = render(DiffSummaryChip, {
      props: {
        additions: 10,
        deletions: 0,
        summaryKey: "sha-1",
        loadFiles,
      },
    });

    await fireEvent.focusIn(screen.getByRole("button", { name: statLabel(10, 0) }));
    expect(await screen.findByText("Plans/docs")).toBeTruthy();

    await rerender({
      additions: 5,
      deletions: 1,
      summaryKey: "sha-2",
      loadFiles,
    });

    const popover = await screen.findByRole("tooltip");
    expect(within(popover).getByText("Code")).toBeTruthy();
    expect(rowText(popover, "Code")).toBe("Code +5 −1");
    await waitFor(() => expect(loadFiles).toHaveBeenCalledTimes(2));
    expect(screen.queryByText("Plans/docs")).toBeNull();
  });
});
