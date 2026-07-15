import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import TerminalSplitTree from "./TerminalSplitTree.svelte";
import type { PaneNode } from "./terminal-layout";

vi.mock("./TerminalPane.svelte", async () => ({
  default: (await import("./TerminalSplitTreeTestPane.svelte")).default,
}));

const sessions = [
  {
    key: "ws-1:shell-a",
    workspace_id: "ws-1",
    target_key: "plain_shell",
    label: "Shell A",
    kind: "plain_shell" as const,
    status: "running" as const,
    display_region: "panel",
    created_at: "2026-07-15T00:00:00Z",
  },
  {
    key: "ws-1:shell-b",
    workspace_id: "ws-1",
    target_key: "plain_shell",
    label: "Shell B",
    kind: "plain_shell" as const,
    status: "running" as const,
    display_region: "panel",
    created_at: "2026-07-15T00:01:00Z",
  },
  {
    key: "ws-1:shell-c",
    workspace_id: "ws-1",
    target_key: "plain_shell",
    label: "Shell C",
    kind: "plain_shell" as const,
    status: "running" as const,
    display_region: "panel",
    created_at: "2026-07-15T00:02:00Z",
  },
];

function leaf(id: string, sessionKey: string): PaneNode {
  return { type: "leaf", id, sessionKey };
}

function split(direction: "horizontal" | "vertical" = "horizontal"): PaneNode {
  return {
    type: "split",
    id: "split-1",
    direction,
    ratio: 0.4,
    first: leaf("leaf-a", sessions[0]!.key),
    second: leaf("leaf-b", sessions[1]!.key),
  };
}

function mockRect(width = 1000, height = 600): void {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    width,
    height,
    x: 0,
    y: 0,
    top: 0,
    right: width,
    bottom: height,
    left: 0,
    toJSON: () => ({}),
  });
}

describe("TerminalSplitTree", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe(): void {}
        unobserve(): void {}
        disconnect(): void {}
      },
    );
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("resizes a horizontal split with truthful pixel ARIA values", async () => {
    mockRect();
    const onRatioChange = vi.fn();
    render(TerminalSplitTree, {
      props: {
        workspaceId: "ws-1",
        node: split(),
        sessions,
        displayLabels: {},
        activeSessionKey: sessions[0]!.key,
        onRatioChange,
      },
    });

    const handle = screen.getByRole("separator", { name: "Resize split" });
    expect(handle.getAttribute("aria-orientation")).toBe("vertical");
    expect(handle.getAttribute("aria-valuemin")).toBe("120");
    expect(handle.getAttribute("aria-valuemax")).toBe("880");
    expect(handle.getAttribute("aria-valuenow")).toBe("400");

    await fireEvent.keyDown(handle, { key: "ArrowRight" });
    expect(onRatioChange).toHaveBeenCalledWith("split-1", expect.closeTo(0.424));

    onRatioChange.mockClear();
    await fireEvent.keyDown(handle, { key: "ArrowDown" });
    expect(onRatioChange).not.toHaveBeenCalled();
  });

  it("uses the vertical extent and clamps split ratios", async () => {
    mockRect();
    const onRatioChange = vi.fn();
    const node = split("vertical");
    if (node.type === "split") node.ratio = 0.87;
    render(TerminalSplitTree, {
      props: {
        workspaceId: "ws-1",
        node,
        sessions,
        displayLabels: {},
        activeSessionKey: sessions[0]!.key,
        onRatioChange,
      },
    });

    const handle = screen.getByRole("separator", { name: "Resize split" });
    expect(handle.getAttribute("aria-orientation")).toBe("horizontal");
    expect(handle.getAttribute("aria-valuenow")).toBe("522");
    await fireEvent.keyDown(handle, { key: "ArrowDown" });
    expect(onRatioChange).toHaveBeenCalledWith("split-1", 0.88);
  });

  it("updates only the targeted nested split", async () => {
    mockRect();
    const onRatioChange = vi.fn();
    const node: PaneNode = {
      type: "split",
      id: "outer",
      direction: "horizontal",
      ratio: 0.5,
      first: leaf("leaf-a", sessions[0]!.key),
      second: {
        type: "split",
        id: "inner",
        direction: "vertical",
        ratio: 0.4,
        first: leaf("leaf-b", sessions[1]!.key),
        second: leaf("leaf-c", sessions[2]!.key),
      },
    };
    render(TerminalSplitTree, {
      props: {
        workspaceId: "ws-1",
        node,
        sessions,
        displayLabels: {},
        activeSessionKey: sessions[0]!.key,
        onRatioChange,
      },
    });

    const handles = screen.getAllByRole("separator", { name: "Resize split" });
    await fireEvent.keyDown(handles[1]!, { key: "ArrowDown" });
    expect(onRatioChange).toHaveBeenCalledTimes(1);
    expect(onRatioChange).toHaveBeenCalledWith("inner", 0.44);
  });
});
