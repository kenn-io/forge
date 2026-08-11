import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { clearActiveTabbedPanelDrag, readTabbedPanelTabDrag } from "../shared/tabbed-panel-drag.js";
import { sessionPaneKey } from "../../stores/session-pane-key.js";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { ComponentProps } from "svelte";
import AppRuntimeHarness from "../../../test/AppRuntimeHarness.svelte";
import DockedTerminalPanelTestHarness from "./DockedTerminalPanelTestHarness.svelte";
import { clearActiveTerminalDrag, readRuntimeSessionDrag } from "./terminal-drag";

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
];

function renderDockedTerminalPanel(props: ComponentProps<typeof DockedTerminalPanelTestHarness>) {
  return render(AppRuntimeHarness, { props: { component: DockedTerminalPanelTestHarness, ...props } });
}

describe("DockedTerminalPanel", () => {
  afterEach(() => {
    cleanup();
    clearActiveTerminalDrag();
    clearActiveTabbedPanelDrag();
  });

  it("publishes and clears a detail-pane payload from the terminal selector", async () => {
    const paneKey = sessionPaneKey("ws-1", undefined, sessions[1]!.key);
    renderDockedTerminalPanel({
      sessions,
      tree: { type: "leaf", id: "leaf-a", sessionKey: sessions[0]!.key },
      activeSessionKey: sessions[0]!.key,
      dragScope: "detail:prs",
      paneKeyForSession: (sessionKey: string) => (sessionKey === sessions[1]!.key ? paneKey : null),
    });
    const data = new Map<string, string>();
    const dataTransfer = {
      effectAllowed: "none",
      setData: (type: string, value: string) => data.set(type, value),
      getData: (type: string) => data.get(type) ?? "",
    };
    const selector = screen.getByRole("button", { name: /Shell B/ });

    await fireEvent.dragStart(selector, { dataTransfer });
    expect(readRuntimeSessionDrag({ dataTransfer } as unknown as DragEvent, "ws-1")).toBe(sessions[1]!.key);
    expect(readTabbedPanelTabDrag({ dataTransfer } as unknown as DragEvent, "detail:prs")).toBe(paneKey);

    await fireEvent.dragEnd(selector, { dataTransfer });
    expect(readRuntimeSessionDrag({ dataTransfer } as unknown as DragEvent, "ws-1")).toBeNull();
    expect(readTabbedPanelTabDrag({ dataTransfer } as unknown as DragEvent, "detail:prs")).toBeNull();
  });

  it("inverts vertical keyboard deltas and clamps terminal height", async () => {
    const onResize = vi.fn();
    renderDockedTerminalPanel({ onResize });

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
    const minView = renderDockedTerminalPanel({ height: 160, onResize: minResize });
    await fireEvent.keyDown(screen.getByRole("separator", { name: "Resize terminal panel" }), { key: "ArrowDown" });
    expect(minResize).toHaveBeenLastCalledWith(160);
    minView.unmount();

    const maxResize = vi.fn();
    renderDockedTerminalPanel({ height: 560, onResize: maxResize });
    await fireEvent.keyDown(screen.getByRole("separator", { name: "Resize terminal panel" }), { key: "ArrowUp" });
    expect(maxResize).toHaveBeenLastCalledWith(560);
  });

  it("disables the shared handle with the panel", async () => {
    const onResize = vi.fn();
    renderDockedTerminalPanel({ disabled: true, onResize });

    const handle = screen.getByRole("separator", { name: "Resize terminal panel" });
    expect(handle.hasAttribute("disabled")).toBe(true);
    await fireEvent.keyDown(handle, { key: "ArrowUp" });
    expect(onResize).not.toHaveBeenCalled();
  });

  it("reports input activation only when DOM focus enters the panel", async () => {
    const onInputActivate = vi.fn();
    const onInputDeactivate = vi.fn();
    renderDockedTerminalPanel({ onInputActivate, onInputDeactivate });
    const panel = screen.getByRole("region", { name: "Terminal panel" });
    const child = screen.getByRole("separator", { name: "Resize terminal panel" });
    child.addEventListener("wheel", (event) => event.stopPropagation());

    await fireEvent.wheel(panel);
    await fireEvent.pointerDown(panel);

    expect(onInputActivate).not.toHaveBeenCalled();

    await fireEvent.focusIn(child);

    expect(onInputActivate).toHaveBeenCalledOnce();

    await fireEvent.focusOut(child, { relatedTarget: document.body });

    expect(onInputDeactivate).toHaveBeenCalledOnce();
  });
});
