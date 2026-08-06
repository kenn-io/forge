import { flushSync, mount, unmount } from "svelte";
import { describe, expect, it } from "vite-plus/test";
import { createPaneLayoutStore, type PaneLayoutStore } from "../../stores/paneLayout.svelte.js";
import InlineWorkspacePaneHarness from "./InlineWorkspacePaneHarness.svelte";

/**
 * Real-browser coverage for the inline workspace pane's focus contract, which
 * replaced `WorkspaceDockPanel`'s.
 *
 * These need native focus semantics — jsdom does not enforce that a focused
 * element must be visible and connected, so a pane that closes under the focused
 * terminal would appear to keep focus there and the stranding bug would pass.
 */
function store(): PaneLayoutStore {
  localStorage.clear();
  return createPaneLayoutStore("prs", ["conversation", "workspace"], {
    type: "split",
    id: "split-root",
    direction: "vertical",
    ratio: 0.5,
    first: { type: "leaf", id: "leaf-detail", tabs: ["conversation"], activeTabKey: "conversation" },
    second: { type: "leaf", id: "leaf-workspace", tabs: ["workspace"], activeTabKey: "workspace" },
  });
}

async function settleFocusRestore(): Promise<void> {
  // Focus is restored via tick().then(...) after the DOM update; two macrotask
  // turns comfortably cover the microtask chain.
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function mountHarness(layout: PaneLayoutStore, workspaceAvailable = true) {
  const target = document.createElement("div");
  // Sized: the layout flattens below its measured width threshold, and a
  // flattened tree offers no close control at all.
  target.style.width = "1600px";
  target.style.height = "800px";
  document.body.appendChild(target);
  const props = $state({ layout, workspaceAvailable });
  const instance = mount(InlineWorkspacePaneHarness, { target, props });
  flushSync();
  return {
    target,
    props,
    dispose: () => {
      flushSync(() => unmount(instance));
      target.remove();
    },
  };
}

function terminalButton(target: HTMLElement): HTMLButtonElement | null {
  return target.querySelector<HTMLButtonElement>('[data-pane-key="workspace"] button');
}

describe("inline workspace pane focus", () => {
  it("takes focus back when the pane the user closes held it", async () => {
    const layout = store();
    const { target, dispose } = mountHarness(layout);

    try {
      const button = terminalButton(target);
      expect(button).not.toBeNull();
      button?.focus();
      expect(document.activeElement).toBe(button);

      target.querySelector<HTMLButtonElement>('[data-testid="pane-hide-workspace"]')?.click();
      flushSync();
      await settleFocusRestore();

      // Left on <body>, the focused element is gone, keyboard users are stranded
      // and the app's global single-key shortcuts are armed against nothing.
      expect(document.activeElement).toBe(target.querySelector(".detail-pane-layout"));
    } finally {
      dispose();
    }
  });

  it("takes focus back when the workspace goes away on its own", async () => {
    const layout = store();
    const { target, props, dispose } = mountHarness(layout);

    try {
      terminalButton(target)?.focus();

      // A deletion or a released claim makes the pane unavailable and unmounts
      // it out from under the focused terminal.
      props.workspaceAvailable = false;
      flushSync();
      await settleFocusRestore();

      expect(document.activeElement).toBe(target.querySelector(".detail-pane-layout"));
    } finally {
      dispose();
    }
  });

  it("leaves focus alone when the pane closes while the user is elsewhere", async () => {
    const layout = store();
    const { props, dispose } = mountHarness(layout);
    // Stand-in for the sidebar row the user just clicked.
    const sidebarRow = document.createElement("button");
    sidebarRow.textContent = "PR #8";
    document.body.appendChild(sidebarRow);

    try {
      sidebarRow.focus();
      expect(document.activeElement).toBe(sidebarRow);

      props.workspaceAvailable = false;
      flushSync();
      await settleFocusRestore();

      expect(document.activeElement).toBe(sidebarRow);
    } finally {
      dispose();
      sidebarRow.remove();
    }
  });

  it("drops the divider between a maximized pane and its hidden sibling", () => {
    const layout = store();
    const { target, dispose } = mountHarness(layout);

    try {
      expect(target.querySelector(".tabbed-panel-split-divider")).not.toBeNull();

      const workspaceLeaf = target.querySelector('[data-pane-key="workspace"]')!.closest(".tabbed-panel-leaf")!;
      workspaceLeaf.querySelector<HTMLButtonElement>('[data-testid="pane-toggle-zoom"]')?.click();
      flushSync();

      // A divider left rendered would sit draggable on top of a supposedly
      // full-size pane, silently mutating a ratio the user cannot see.
      expect(target.querySelector(".tabbed-panel-split-divider")).toBeNull();
      expect(target.querySelector(".tabbed-panel-split-child.first")?.hasAttribute("hidden")).toBe(true);
    } finally {
      dispose();
    }
  });
});
