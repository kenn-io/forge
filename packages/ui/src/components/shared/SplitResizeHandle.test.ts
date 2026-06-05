import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import SplitResizeHandle from "./SplitResizeHandle.svelte";

describe("SplitResizeHandle", () => {
  afterEach(() => {
    cleanup();
  });

  it("combines base and custom classes without comma separators", () => {
    render(SplitResizeHandle, {
      props: {
        ariaLabel: "Resize sidebar",
        class: "activity-split-resize-handle",
      },
    });

    const handle = screen.getByRole("button", {
      name: "Resize sidebar",
    });
    expect(handle.classList.contains("split-resize-handle")).toBe(true);
    expect(handle.classList.contains("activity-split-resize-handle")).toBe(true);
    expect(handle.className).not.toContain(",");
  });

  it("reports horizontal drag deltas", async () => {
    const onResizeStart = vi.fn();
    const onResize = vi.fn();
    const onResizeEnd = vi.fn();
    render(SplitResizeHandle, {
      props: {
        ariaLabel: "Resize sidebar",
        onResizeStart,
        onResize,
        onResizeEnd,
      },
    });

    const handle = screen.getByRole("button", {
      name: "Resize sidebar",
    });
    await fireEvent.mouseDown(handle, { clientX: 100 });
    await fireEvent.mouseMove(window, { clientX: 140 });
    await fireEvent.mouseUp(window, { clientX: 150 });

    expect(onResizeStart).toHaveBeenCalledOnce();
    expect(onResize).toHaveBeenCalledWith(expect.objectContaining({ deltaX: 40 }));
    expect(onResizeEnd).toHaveBeenCalledWith(expect.objectContaining({ deltaX: 50 }));
  });

  it("reports arrow-key resize deltas", async () => {
    const onResizeStart = vi.fn();
    const onResize = vi.fn();
    const onResizeEnd = vi.fn();
    render(SplitResizeHandle, {
      props: {
        ariaLabel: "Resize sidebar",
        keyboardStep: 32,
        onResizeStart,
        onResize,
        onResizeEnd,
      },
    });

    const handle = screen.getByRole("button", {
      name: "Resize sidebar",
    });
    await fireEvent.keyDown(handle, { key: "ArrowLeft" });
    await fireEvent.keyDown(handle, { key: "ArrowRight" });

    expect(onResizeStart).toHaveBeenCalledTimes(2);
    expect(onResize).toHaveBeenNthCalledWith(1, expect.objectContaining({ deltaX: -32 }));
    expect(onResize).toHaveBeenNthCalledWith(2, expect.objectContaining({ deltaX: 32 }));
    expect(onResizeEnd).toHaveBeenCalledTimes(2);
  });
});
