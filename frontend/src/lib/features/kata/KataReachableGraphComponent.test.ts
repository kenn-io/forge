import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataTaskSummary } from "../../api/kata/taskTypes.js";
import KataReachableGraph from "./KataReachableGraph.svelte";

function task(overrides: Partial<KataTaskSummary> = {}): KataTaskSummary {
  const shortID = overrides.short_id ?? "root";
  return {
    id: overrides.id ?? 1,
    uid: overrides.uid ?? "issue-root",
    project_id: overrides.project_id ?? 7,
    project_uid: overrides.project_uid ?? "project-kata",
    project_name: overrides.project_name ?? "Kata",
    short_id: shortID,
    qualified_id: overrides.qualified_id ?? `Kata#${shortID}`,
    title: overrides.title ?? "Root task",
    status: overrides.status ?? "open",
    metadata: overrides.metadata ?? {},
    revision: overrides.revision ?? 1,
    author: overrides.author ?? "middleman",
    priority: overrides.priority,
    blocks: overrides.blocks,
    blocked_by: overrides.blocked_by,
    related: overrides.related,
    closed_reason: overrides.closed_reason,
    created_at: overrides.created_at ?? "2026-06-29T12:00:00Z",
    updated_at: overrides.updated_at ?? "2026-06-29T12:00:00Z",
  };
}

function graphNodeButtonWithText(text: string): HTMLButtonElement {
  const button = screen
    .getAllByText(text)
    .find((element) => element.closest(".svelte-flow__node"))
    ?.closest(".svelte-flow__node")
    ?.querySelector<HTMLButtonElement>("button.graph-task-node");
  expect(button).toBeTruthy();
  return button!;
}

describe("KataReachableGraph", () => {
  beforeEach(() => {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      writable: true,
      value: vi.fn((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    });
    class TestResizeObserver {
      observe = vi.fn();
      unobserve = vi.fn();
      disconnect = vi.fn();
    }
    vi.stubGlobal("ResizeObserver", TestResizeObserver);
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders node titles and priority markers", () => {
    const root = task({ uid: "issue-root", short_id: "root", title: "Root task", priority: 0 });
    render(KataReachableGraph, {
      props: {
        sourceUID: root.uid,
        selectedUID: root.uid,
        tasks: [root],
        selectedDetail: null,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });

    expect(screen.getByRole("region", { name: "Reachable task graph" })).toBeTruthy();
    expect(screen.getAllByText("Root task").length).toBeGreaterThan(0);
    expect(screen.getByText("P0")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Source task, selected, Root task, Kata#root, open/ })).toBeTruthy();
  });

  it("filters done nodes", async () => {
    const root = task({
      uid: "issue-root",
      short_id: "root",
      blocks: [{ uid: "issue-done", short_id: "done" }],
    });
    const done = task({
      uid: "issue-done",
      short_id: "done",
      title: "Done task",
      status: "closed",
      closed_reason: "done",
    });
    render(KataReachableGraph, {
      props: {
        sourceUID: root.uid,
        selectedUID: root.uid,
        tasks: [root, done],
        selectedDetail: null,
        onBack: () => {},
        onSelectIssue: () => {},
      },
    });

    expect(screen.getAllByText("Done task").length).toBeGreaterThan(0);
    await fireEvent.click(screen.getByRole("button", { name: /Graph filters/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Hide done" }));
    expect(screen.queryAllByText("Done task")).toEqual([]);
  });

  it("selects cached nodes and returns to the list", async () => {
    const root = task({ uid: "issue-root", short_id: "root", title: "Root task" });
    const onSelectIssue = vi.fn();
    const onBack = vi.fn();
    render(KataReachableGraph, {
      props: {
        sourceUID: root.uid,
        selectedUID: null,
        tasks: [root],
        selectedDetail: null,
        onBack,
        onSelectIssue,
      },
    });

    await fireEvent.click(graphNodeButtonWithText("Root task"));
    expect(onSelectIssue).toHaveBeenCalledWith("issue-root");

    onSelectIssue.mockClear();
    await fireEvent.keyDown(graphNodeButtonWithText("Root task"), { key: "Enter" });
    expect(onSelectIssue).toHaveBeenCalledWith("issue-root");

    onSelectIssue.mockClear();
    await fireEvent.keyDown(graphNodeButtonWithText("Root task"), { key: " " });
    expect(onSelectIssue).toHaveBeenCalledTimes(1);
    expect(onSelectIssue).toHaveBeenCalledWith("issue-root");
    await fireEvent.click(screen.getByRole("button", { name: "Back to task list" }));
    expect(onBack).toHaveBeenCalled();
  });
});
