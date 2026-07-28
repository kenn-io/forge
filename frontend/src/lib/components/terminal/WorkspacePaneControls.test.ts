import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { createRawSnippet, flushSync } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";

import WorkspacePaneControls from "./WorkspacePaneControls.svelte";
import {
  registerWorkspaceControls,
  resetWorkspaceHostForTest,
  setWorkspaceControlsBusy,
} from "../../stores/workspace-host.svelte.ts";

/**
 * Stands in for the live view's controls. The real ones are wired to that view's
 * state, which is the whole reason they arrive as a snippet rather than being
 * rebuilt here.
 */
const snippet = createRawSnippet(() => ({
  render: () => `<button type="button">Save preset</button>`,
}));

function hostControls(workspaceKey = "ws-1"): void {
  registerWorkspaceControls({ snippet, workspaceKey });
}

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
    hostControls();
    render(WorkspacePaneControls);

    const button = trigger();
    expect(button).not.toBeNull();
    expect(button!.getAttribute("aria-expanded")).toBe("false");

    await fireEvent.click(button!);

    const popover = screen.getByRole("dialog", { name: "Workspace controls" });
    expect(popover.contains(screen.getByRole("button", { name: "Save preset" }))).toBe(true);
    expect(button!.getAttribute("aria-expanded")).toBe("true");
  });

  it("renders the strip actions only for the leaf that owns them", () => {
    // The popover follows the workspace to any leaf hosting one of its panes, but
    // the strip actions carry Delete: a workspace split across leaves must not
    // grow one visible Delete per leaf.
    const stripActions = createRawSnippet(() => ({
      render: () => `<button type="button">Delete workspace main</button>`,
    }));
    registerWorkspaceControls({ snippet, stripActions, workspaceKey: "ws-1" });

    render(WorkspacePaneControls, { props: { showStripActions: false } });
    expect(screen.queryByRole("button", { name: "Delete workspace main" })).toBeNull();
    cleanup();

    render(WorkspacePaneControls);
    expect(screen.getByRole("button", { name: "Delete workspace main" })).toBeTruthy();
  });

  it("closes on Escape and on a click outside", async () => {
    hostControls();
    render(WorkspacePaneControls);

    await fireEvent.click(trigger()!);
    await fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();

    await fireEvent.click(trigger()!);
    await fireEvent.pointerDown(document.body);
    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();
  });

  it("closes when the pane starts hosting a different workspace", async () => {
    hostControls("ws-1");
    render(WorkspacePaneControls);
    await fireEvent.click(trigger()!);

    // The surface keeps rendering while the user selects another item, so one
    // embedded view hands over the same snippet for a different workspace. Its
    // buttons would silently act on that one.
    flushSync(() => hostControls("ws-2"));

    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();
    expect(trigger()).not.toBeNull();
  });

  it("stays open while a hosted control is mid-save", async () => {
    hostControls();
    render(WorkspacePaneControls);
    await fireEvent.click(trigger()!);
    flushSync(() => setWorkspaceControlsBusy("ws-1", true));

    await fireEvent.pointerDown(document.body);
    await fireEvent.keyDown(window, { key: "Escape" });

    // The control owns the pending feedback for its own save; dismissing the
    // popover under it would strand the user not knowing whether it landed.
    expect(screen.getByRole("dialog", { name: "Workspace controls" })).toBeTruthy();

    flushSync(() => setWorkspaceControlsBusy("ws-1", false));
    await fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();
  });

  it("is dismissable while another workspace's save is still in flight", async () => {
    hostControls("ws-1");
    render(WorkspacePaneControls);
    await fireEvent.click(trigger()!);
    // A preset apply started on ws-1 clears its pending flag only while ws-1 is
    // still the hosted workspace, so a switch mid-save leaves that write reported
    // forever. Held against the workspace it belongs to, it cannot pin ws-2's
    // popover open.
    flushSync(() => setWorkspaceControlsBusy("ws-1", true));
    flushSync(() => hostControls("ws-2"));

    await fireEvent.click(trigger()!);
    await fireEvent.keyDown(window, { key: "Escape" });

    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();
  });

  it("closes when the hosted view goes away under it", async () => {
    hostControls();
    render(WorkspacePaneControls);
    await fireEvent.click(trigger()!);

    // The claim is released, the pane closed, the workspace deleted: the view
    // unregisters, and nothing else would close a popover left behind.
    flushSync(() => registerWorkspaceControls(null));

    expect(screen.queryByRole("dialog", { name: "Workspace controls" })).toBeNull();
    expect(trigger()).toBeNull();
  });
});
