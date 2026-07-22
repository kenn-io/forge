import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { createRawSnippet, tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { resetModalStack } from "../../stores/keyboard/modal-stack.svelte.js";
import type { InlineWorkspaceController } from "../../workspace-inline.js";
import WorkspaceDockPanel from "./WorkspaceDockPanel.svelte";
import { createTestController } from "./WorkspaceDockPanelTestController.svelte.js";

// The real kit-ui BottomDock mounts cleanly in jsdom given the same
// ResizeObserver/MutationObserver/rAF/getBoundingClientRect stubs its own
// suite (ReviewDrawer.test.ts) uses, so this file exercises the real
// component rather than mocking it.

const detailChildren = createRawSnippet(() => ({
  render: () => `<div data-testid="detail-marker">detail alive</div>`,
}));

function renderPanel(controller: InlineWorkspaceController, active: boolean, detailTitle = "#7 Fix the widget") {
  return render(WorkspaceDockPanel, {
    props: { controller, active, detailTitle, children: detailChildren },
  });
}

let rectSpy: ReturnType<typeof vi.spyOn>;

// BottomDock re-measures via getBoundingClientRect() at the start of every
// resize (keyboard or pointer), so this controls the "current height" a
// keyboard resize step computes from.
function mockRectHeight(height: number): void {
  rectSpy.mockReturnValue({
    width: 900,
    height,
    x: 0,
    y: 0,
    top: 0,
    right: 900,
    bottom: height,
    left: 0,
    toJSON: () => ({}),
  } as DOMRect);
}

describe("WorkspaceDockPanel", () => {
  beforeEach(() => {
    localStorage.clear();
    resetModalStack();
    vi.stubGlobal(
      "MutationObserver",
      class {
        observe(): void {}
        disconnect(): void {}
      },
    );
    vi.stubGlobal("requestAnimationFrame", () => 1);
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    rectSpy = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect");
    mockRectHeight(400);
  });

  afterEach(() => {
    cleanup();
    resetModalStack();
    localStorage.clear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("renders children full-height with no dock when inactive", () => {
    const controller = createTestController("split");
    renderPanel(controller, false);

    expect(document.querySelector(".kit-bottom-dock")).toBeNull();
    const detail = screen.getByTestId("detail-marker").closest(".workspace-dock-detail");
    expect(detail?.hasAttribute("hidden")).toBe(false);
    expect(detail?.hasAttribute("inert")).toBe(false);
  });

  it("renders the dock with the slot element when active in split mode", () => {
    const controller = createTestController("split");
    renderPanel(controller, true);

    expect(screen.getByRole("region", { name: "Workspace terminal" })).toBeTruthy();
    expect(document.querySelector(".workspace-dock-slot")).toBeTruthy();
    const detail = screen.getByTestId("detail-marker").closest(".workspace-dock-detail");
    expect(detail?.hasAttribute("hidden")).toBe(false);
  });

  it("renders no dock header bar", () => {
    const controller = createTestController("split");
    renderPanel(controller, true);

    // The dock's own header (label + expand/collapse controls) is gone: the
    // toggle and collapse controls now live in the embedded
    // WorkspaceTerminalView's toolbar instead, outside this panel entirely.
    expect(document.querySelector(".kit-bottom-dock__header")).toBeNull();
    expect(screen.queryByText("Workspace")).toBeNull();
    expect(screen.queryByRole("button", { name: "Collapse terminal" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Expand terminal" })).toBeNull();
  });

  it("collapse yields a full-height detail with the claim retained", async () => {
    const controller = createTestController("split");
    renderPanel(controller, true);

    // The dock's own close button is gone; the WTV toolbar's "Collapse
    // Terminal" button drives this the same way, directly through the
    // controller.
    controller.setDockMode("collapsed");
    await tick();

    expect(document.querySelector(".kit-bottom-dock")).toBeNull();
    const detail = screen.getByTestId("detail-marker").closest(".workspace-dock-detail");
    expect(detail?.hasAttribute("hidden")).toBe(false);
    expect(controller.release).not.toHaveBeenCalled();
  });

  it("expanded mode hides the detail but keeps it mounted with state", () => {
    const controller = createTestController("expanded");
    renderPanel(controller, true, "#7 Fix the widget");

    const detail = screen.getByTestId("detail-marker").closest<HTMLElement>(".workspace-dock-detail");
    expect(detail?.hasAttribute("hidden")).toBe(true);
    // jsdom does not reflect the `inert` IDL property back onto the
    // attribute (unlike real browsers), so the property is the faithful
    // check here.
    expect(detail?.inert).toBe(true);
    expect(screen.getByTestId("detail-marker")).toBeTruthy();

    const titlestrip = document.querySelector<HTMLElement>(".workspace-dock-titlestrip");
    expect(titlestrip?.textContent).toContain("#7 Fix the widget");
    // The title strip's own "Show details" button is now the only one in
    // this panel: the dock header that used to offer a second, identically
    // labeled control is gone (moved to the WTV toolbar as "Show Details").
    expect(titlestrip && within(titlestrip).getByRole("button", { name: "Show details" })).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "Show details" })).toHaveLength(1);
  });

  it("persists height per surface and clamps on restore", async () => {
    localStorage.setItem("middleman-workspace-dock-height-prs", "9999");
    const controller = createTestController("split", "prs");
    renderPanel(controller, true);

    const dock = document.querySelector<HTMLElement>(".kit-bottom-dock");
    expect(dock?.style.height).toBe("614px");
    // The BottomDock minHeight prop must reflect our own floor, or the CSS
    // clamp and the JS clamp disagree and heights in [160, 199] can never
    // actually render.
    expect(dock?.style.minHeight).toBe("160px");

    const handle = screen.getByRole("separator", { name: "Workspace terminal" });
    await fireEvent.keyDown(handle, { key: "ArrowDown" });
    await tick();

    expect(localStorage.getItem("middleman-workspace-dock-height-prs")).toBe("376");
    expect(document.querySelector<HTMLElement>(".kit-bottom-dock")?.style.height).toBe("376px");
  });

  it("clamps a stored height below the minimum on load", () => {
    localStorage.setItem("middleman-workspace-dock-height-prs", "50");
    const controller = createTestController("split", "prs");
    renderPanel(controller, true);

    expect(document.querySelector<HTMLElement>(".kit-bottom-dock")?.style.height).toBe("160px");
  });

  it("clamps a resize interaction below the minimum before persisting", async () => {
    const controller = createTestController("split", "prs");
    renderPanel(controller, true);

    // Simulate a small measured dock: one ArrowDown step (startHeight -
    // keyboardStep = 100 - 24 = 76px) requests a height under the 160px floor.
    mockRectHeight(100);
    const handle = screen.getByRole("separator", { name: "Workspace terminal" });
    await fireEvent.keyDown(handle, { key: "ArrowDown" });
    await tick();

    expect(localStorage.getItem("middleman-workspace-dock-height-prs")).toBe("160");
    expect(document.querySelector<HTMLElement>(".kit-bottom-dock")?.style.height).toBe("160px");
  });

  it("clamps a resize interaction above the maximum before persisting", async () => {
    const controller = createTestController("split", "prs");
    renderPanel(controller, true);

    // Simulate a large measured dock: one ArrowUp step (startHeight -
    // (-keyboardStep) = 5000 + 24 = 5024px) requests a height over the
    // window.innerHeight * 0.8 = 614px ceiling.
    mockRectHeight(5000);
    const handle = screen.getByRole("separator", { name: "Workspace terminal" });
    await fireEvent.keyDown(handle, { key: "ArrowUp" });
    await tick();

    expect(localStorage.getItem("middleman-workspace-dock-height-prs")).toBe("614");
    expect(document.querySelector<HTMLElement>(".kit-bottom-dock")?.style.height).toBe("614px");
  });

  it("resets expanded mode when the claim goes away", async () => {
    const controller = createTestController("expanded");
    const { rerender } = renderPanel(controller, true);

    expect(controller.setDockMode).not.toHaveBeenCalled();

    await rerender({ controller, active: false, detailTitle: "#7 Fix the widget", children: detailChildren });
    await tick();

    expect(controller.setDockMode).toHaveBeenCalledWith("split");
    const detail = screen.getByTestId("detail-marker").closest(".workspace-dock-detail");
    expect(detail?.hasAttribute("hidden")).toBe(false);
  });

  it("returns focus to the detail pane, only once it is visible, when collapsing via the control", async () => {
    const controller = createTestController("expanded");
    renderPanel(controller, true);
    const detail = screen.getByTestId("detail-marker").closest<HTMLElement>(".workspace-dock-detail");
    expect(detail).toBeTruthy();

    // Capture whether the detail pane still carried hidden/inert at the
    // exact moment .focus() was invoked on it — a real browser silently
    // ignores focus() on a hidden/inert element, so this is the load-bearing
    // assertion (document.activeElement alone is not: jsdom does not
    // enforce that restriction and would report success either way).
    let hiddenAtFocusTime: boolean | undefined;
    let inertAtFocusTime: boolean | undefined;
    const originalFocus = HTMLElement.prototype.focus;
    vi.spyOn(HTMLElement.prototype, "focus").mockImplementation(function (this: HTMLElement) {
      if (this === detail) {
        hiddenAtFocusTime = this.hidden;
        inertAtFocusTime = this.inert;
      }
      return originalFocus.call(this);
    });

    const titlestrip = document.querySelector<HTMLElement>(".workspace-dock-titlestrip");
    expect(titlestrip).toBeTruthy();
    await fireEvent.click(within(titlestrip as HTMLElement).getByRole("button", { name: "Show details" }));

    expect(hiddenAtFocusTime).toBe(false);
    expect(inertAtFocusTime).toBe(false);
    expect(document.activeElement).toBe(detail);
  });

  it("returns focus to the detail pane, only once it is visible, when the claim goes away while expanded", async () => {
    const controller = createTestController("expanded");
    const { rerender } = renderPanel(controller, true);
    const detail = screen.getByTestId("detail-marker").closest<HTMLElement>(".workspace-dock-detail");
    expect(detail).toBeTruthy();

    // Unlike the control-click path above, this does not independently prove
    // a regression: the $effect that drives this reset always runs inside an
    // already-active Svelte flush (that's how effects are scheduled), so a
    // state write performed inside it resolves synchronously before the next
    // line runs, with or without the tick() in WorkspaceDockPanel.svelte —
    // confirmed by instrumenting the effect directly and by removing tick()
    // here, which left this assertion green (see the fix report). tick() is
    // kept anyway for defensive consistency with collapseToDetail rather than
    // relying on that scheduling detail. This test still pins the correct
    // observable outcome: focus lands on a visible, non-inert detail pane.
    let hiddenAtFocusTime: boolean | undefined;
    const originalFocus = HTMLElement.prototype.focus;
    vi.spyOn(HTMLElement.prototype, "focus").mockImplementation(function (this: HTMLElement) {
      if (this === detail) hiddenAtFocusTime = this.hidden;
      return originalFocus.call(this);
    });

    await rerender({ controller, active: false, detailTitle: "#7 Fix the widget", children: detailChildren });

    await waitFor(() => expect(document.activeElement).toBe(detail));
    expect(hiddenAtFocusTime).toBe(false);
  });

  it("returns focus to the detail pane when mode is driven to split externally, not via a panel button", async () => {
    // The expand/collapse controls now live in the embedded
    // WorkspaceTerminalView's toolbar, outside this panel, so this drives
    // the same expanded -> split transition the way that toolbar's "Show
    // Details" button does: directly through the controller, never through
    // this panel's own collapseToDetail (which only the title strip's
    // button still calls).
    const controller = createTestController("expanded");
    renderPanel(controller, true);
    const detail = screen.getByTestId("detail-marker").closest<HTMLElement>(".workspace-dock-detail");
    expect(detail).toBeTruthy();

    let hiddenAtFocusTime: boolean | undefined;
    const originalFocus = HTMLElement.prototype.focus;
    vi.spyOn(HTMLElement.prototype, "focus").mockImplementation(function (this: HTMLElement) {
      if (this === detail) hiddenAtFocusTime = this.hidden;
      return originalFocus.call(this);
    });

    controller.setDockMode("split");
    await tick();

    await waitFor(() => expect(document.activeElement).toBe(detail));
    expect(hiddenAtFocusTime).toBe(false);
  });
});
