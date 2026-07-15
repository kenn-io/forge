import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import KataResizableSashTestHarness from "./KataResizableSashTestHarness.svelte";

function mockRect(width = 800, height = 600): void {
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

describe("KataResizableSash", () => {
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

  it("resizes horizontal panes with normal and accelerated keyboard steps", async () => {
    mockRect();
    const onResize = vi.fn();
    render(KataResizableSashTestHarness, { props: { onResize } });

    const handle = screen.getByRole("separator", { name: "Resize Kata panes" });
    expect(handle.getAttribute("aria-orientation")).toBe("vertical");
    expect(handle.getAttribute("aria-valuemin")).toBe("200");
    expect(handle.getAttribute("aria-valuemax")).toBe("600");
    expect(handle.getAttribute("aria-valuenow")).toBe("300");

    await fireEvent.keyDown(handle, { key: "ArrowRight" });
    expect(onResize).toHaveBeenLastCalledWith(316);

    onResize.mockClear();
    await fireEvent.keyDown(handle, { key: "ArrowRight", shiftKey: true });
    expect(onResize).toHaveBeenLastCalledWith(364);

    onResize.mockClear();
    await fireEvent.keyDown(handle, { key: "ArrowDown" });
    expect(onResize).not.toHaveBeenCalled();
  });

  it("uses the vertical axis and clamps both resize bounds", async () => {
    mockRect();
    const onResize = vi.fn();
    render(KataResizableSashTestHarness, {
      props: { orientation: "vertical", primarySize: 210, onResize },
    });

    const handle = screen.getByRole("separator", { name: "Resize Kata panes" });
    expect(handle.getAttribute("aria-orientation")).toBe("horizontal");
    expect(handle.getAttribute("aria-valuemax")).toBe("400");
    expect(handle.getAttribute("aria-valuenow")).toBe("210");

    await fireEvent.keyDown(handle, { key: "ArrowUp", shiftKey: true });
    expect(onResize).toHaveBeenLastCalledWith(200);

    onResize.mockClear();
    await fireEvent.keyDown(handle, { key: "ArrowDown", shiftKey: true });
    expect(onResize).toHaveBeenLastCalledWith(274);
  });
});
