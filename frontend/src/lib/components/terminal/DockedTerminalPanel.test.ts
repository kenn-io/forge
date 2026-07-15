import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import DockedTerminalPanelTestHarness from "./DockedTerminalPanelTestHarness.svelte";

describe("DockedTerminalPanel", () => {
  afterEach(() => cleanup());

  it("inverts vertical keyboard deltas and clamps terminal height", async () => {
    const onResize = vi.fn();
    render(DockedTerminalPanelTestHarness, { props: { onResize } });

    const handle = screen.getByRole("separator", { name: "Resize terminal panel" });
    expect(handle.getAttribute("aria-orientation")).toBe("horizontal");
    expect(handle.getAttribute("aria-valuemin")).toBe("160");
    expect(handle.getAttribute("aria-valuemax")).toBe("560");
    expect(handle.getAttribute("aria-valuenow")).toBe("300");

    await fireEvent.keyDown(handle, { key: "ArrowUp" });
    expect(onResize).toHaveBeenLastCalledWith(324);

    onResize.mockClear();
    await fireEvent.keyDown(handle, { key: "ArrowDown" });
    expect(onResize).toHaveBeenLastCalledWith(276);
  });

  it("clamps keyboard resizing at the terminal height limits", async () => {
    const minResize = vi.fn();
    const minView = render(DockedTerminalPanelTestHarness, {
      props: { height: 160, onResize: minResize },
    });
    await fireEvent.keyDown(screen.getByRole("separator", { name: "Resize terminal panel" }), { key: "ArrowDown" });
    expect(minResize).toHaveBeenLastCalledWith(160);
    minView.unmount();

    const maxResize = vi.fn();
    render(DockedTerminalPanelTestHarness, {
      props: { height: 560, onResize: maxResize },
    });
    await fireEvent.keyDown(screen.getByRole("separator", { name: "Resize terminal panel" }), { key: "ArrowUp" });
    expect(maxResize).toHaveBeenLastCalledWith(560);
  });

  it("disables the shared handle with the panel", async () => {
    const onResize = vi.fn();
    render(DockedTerminalPanelTestHarness, {
      props: { disabled: true, onResize },
    });

    const handle = screen.getByRole("separator", { name: "Resize terminal panel" });
    expect(handle.hasAttribute("disabled")).toBe(true);
    await fireEvent.keyDown(handle, { key: "ArrowUp" });
    expect(onResize).not.toHaveBeenCalled();
  });
});
