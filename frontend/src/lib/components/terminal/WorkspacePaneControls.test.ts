import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { createRawSnippet, flushSync } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";

import WorkspacePaneControls from "./WorkspacePaneControls.svelte";
import { registerWorkspaceControlsSnippet, resetWorkspaceHostForTest } from "../../stores/workspace-host.svelte.ts";

/**
 * Stands in for the live view's controls. The real ones are wired to that view's
 * state, which is the whole reason they arrive as a snippet rather than being
 * rebuilt here.
 */
const controls = createRawSnippet(() => ({
  render: () => `<button type="button">Save preset</button>`,
}));

function trigger(): HTMLElement | null {
  return screen.queryByRole("button", { name: "Workspace controls" });
}

describe("WorkspacePaneControls", () => {
  beforeEach(() => {
    resetWorkspaceHostForTest();
  });

  afterEach(() => {
    cleanup();
    resetWorkspaceHostForTest();
  });

  it("renders nothing while no workspace view is hosted", () => {
    render(WorkspacePaneControls);

    // Every leaf of a detail surface renders this, including the ones showing a
    // conversation with no workspace anywhere: a button that opened an empty
    // popover would be chrome for nothing.
    expect(trigger()).toBeNull();
  });

  it("opens the hosted view's controls in one popover", async () => {
    registerWorkspaceControlsSnippet(controls);
    render(WorkspacePaneControls);

    const button = trigger();
    expect(button).not.toBeNull();
    expect(button!.getAttribute("aria-expanded")).toBe("false");

    await fireEvent.click(button!);

    const popover = screen.getByRole("dialog", { name: "Workspace controls" });
    expect(popover.contains(screen.getByRole("button", { name: "Save preset" }))).toBe(true);
    expect(button!.getAttribute("aria-expanded")).toBe("true");
  });

  it("closes on Escape and on a click outside", async () => {
    registerWorkspaceControlsSnippet(controls);
    render(WorkspacePaneControls);

    await fireEvent.click(trigger()!);
    await fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();

    await fireEvent.click(trigger()!);
    await fireEvent.pointerDown(document.body);
    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();
  });

  it("closes when the hosted view goes away under it", async () => {
    registerWorkspaceControlsSnippet(controls);
    render(WorkspacePaneControls);
    await fireEvent.click(trigger()!);

    // The claim is released, the pane closed, the workspace deleted: the view
    // unregisters, and nothing else would close a popover left behind.
    flushSync(() => registerWorkspaceControlsSnippet(null));

    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();
    expect(trigger()).toBeNull();
  });
});
