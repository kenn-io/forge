import { createRawSnippet, flushSync, mount, unmount } from "svelte";
import { describe, expect, it, vi } from "vite-plus/test";
import WorkspaceDockPanel from "../../../../../packages/ui/src/components/workspace/WorkspaceDockPanel.svelte";
import { createTestController } from "../../../../../packages/ui/src/components/workspace/WorkspaceDockPanelTestController.svelte.js";

const detailChildren = createRawSnippet(() => ({
  render: () => `<div data-testid="detail-marker">detail alive</div>`,
}));

// context/ui-design-system.md: kit-ui's BottomDock has no prop to hide its
// own top-edge resize handle, so WorkspaceDockPanel.svelte hides it in
// expanded mode with a `:global(.kit-bottom-dock > .kit-split-resize-handle)
// { display: none }` override scoped to `.workspace-dock-panel--expanded`.
// A manual re-audit was
// the only guard against a future kit-ui bump renaming or restructuring that
// handle out from under the override (silently un-hiding it, or hiding it
// permanently) — this pins the actual computed style in a real browser
// rather than relying on the class name still being present.
describe("WorkspaceDockPanel teardown", () => {
  it("resets an expanded surface to split when the panel unmounts", () => {
    const controller = createTestController("expanded");
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        active: true,
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      flushSync();
      expect(controller.getDockMode()).toBe("expanded");
    } finally {
      // Navigation or selection clearing can unmount the panel outright;
      // the reset-on-inactive effect never observes that, so teardown
      // itself must return the surface to split or the next claimed item
      // opens with its detail hidden.
      flushSync(() => unmount(instance));
      target.remove();
    }

    expect(controller.getDockMode()).toBe("split");
  });
});

// A slot attachment that renders a real focusable control, standing in for
// the terminal subtree that lives inside the dock in production.
function focusableSlotAttachment(node: HTMLElement): () => void {
  const button = document.createElement("button");
  button.textContent = "terminal";
  node.appendChild(button);
  return () => button.remove();
}

async function settleFocusRestore(): Promise<void> {
  // The panel restores focus via tick().then(...) after the DOM update;
  // two macrotask turns comfortably cover the microtask chain.
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("WorkspaceDockPanel dock-close focus", () => {
  it("returns focus to the detail when a split dock collapses", async () => {
    const controller = { ...createTestController("split"), slotAttachment: focusableSlotAttachment };
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        active: true,
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      flushSync();
      const terminalButton = target.querySelector<HTMLButtonElement>(".workspace-dock-slot button");
      expect(terminalButton).not.toBeNull();
      terminalButton?.focus();
      expect(document.activeElement).toBe(terminalButton);

      // Toolbar collapse unmounts the BottomDock subtree along with the
      // focused element; focus must land on the detail wrapper, not <body>.
      flushSync(() => controller.setDockMode("collapsed"));
      await settleFocusRestore();

      expect(document.activeElement).toBe(target.querySelector(".workspace-dock-detail"));
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
    }
  });

  it("returns focus to the detail when the claim releases while split", async () => {
    const controller = { ...createTestController("split"), slotAttachment: focusableSlotAttachment };
    let active = $state(true);
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        get active() {
          return active;
        },
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      flushSync();
      const terminalButton = target.querySelector<HTMLButtonElement>(".workspace-dock-slot button");
      terminalButton?.focus();
      expect(document.activeElement).toBe(terminalButton);

      // Deletion/release while split: active goes false, the dock unmounts,
      // and the detail wrapper must reclaim focus.
      active = false;
      flushSync();
      await settleFocusRestore();

      expect(document.activeElement).toBe(target.querySelector(".workspace-dock-detail"));
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
    }
  });

  it("preserves focus on an outside control when a selection change resets an expanded dock", async () => {
    const controller = { ...createTestController("expanded"), slotAttachment: focusableSlotAttachment };
    const target = document.createElement("div");
    document.body.appendChild(target);
    // Stand-in for the sidebar row the user just clicked: the store resets
    // an expanded dock to split when a claim is replaced, and the delayed
    // focus restore must not yank focus off the row.
    const sidebarRow = document.createElement("button");
    sidebarRow.textContent = "PR #8";
    document.body.appendChild(sidebarRow);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        active: true,
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      flushSync();
      sidebarRow.focus();
      expect(document.activeElement).toBe(sidebarRow);

      flushSync(() => controller.setDockMode("split"));
      await settleFocusRestore();

      expect(document.activeElement).toBe(sidebarRow);
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      sidebarRow.remove();
    }
  });

  it("returns focus to the detail when expanded collapses to split with focus in the terminal", async () => {
    const controller = { ...createTestController("expanded"), slotAttachment: focusableSlotAttachment };
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        active: true,
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      flushSync();
      const terminalButton = target.querySelector<HTMLButtonElement>(".workspace-dock-slot button");
      terminalButton?.focus();
      expect(document.activeElement).toBe(terminalButton);

      // Collapse-from-expanded with focus still inside the terminal (e.g.
      // the WTV toolbar's own collapse button): the detail unhides and must
      // take focus back per the collapse contract.
      flushSync(() => controller.setDockMode("split"));
      await settleFocusRestore();

      expect(document.activeElement).toBe(target.querySelector(".workspace-dock-detail"));
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
    }
  });

  it("does not steal focus when the dock closes while the user is typing elsewhere", async () => {
    const controller = { ...createTestController("split"), slotAttachment: focusableSlotAttachment };
    const target = document.createElement("div");
    document.body.appendChild(target);
    const outsideInput = document.createElement("input");
    document.body.appendChild(outsideInput);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        active: true,
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      flushSync();
      outsideInput.focus();
      expect(document.activeElement).toBe(outsideInput);

      flushSync(() => controller.setDockMode("collapsed"));
      await settleFocusRestore();

      expect(document.activeElement).toBe(outsideInput);
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      outsideInput.remove();
    }
  });
});

describe("WorkspaceDockPanel collapsed reopen strip", () => {
  it("offers a reopen affordance at the bottom of the pane while collapsed", () => {
    const focusTerminal = vi.fn();
    const controller = { ...createTestController("collapsed"), slotAttachment: focusableSlotAttachment, focusTerminal };
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        active: true,
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      flushSync();
      // Collapsed: no dock, but the pane itself must keep the terminal
      // reachable — not only the detail header's Focus Terminal action.
      expect(target.querySelector(".workspace-dock-slot")).toBeNull();
      const reopen = target.querySelector<HTMLButtonElement>(".workspace-dock-reopenstrip button");
      expect(reopen).not.toBeNull();

      reopen?.click();
      flushSync();
      expect(focusTerminal).toHaveBeenCalled();
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
    }
  });

  it("hides the reopen strip while the dock is open or the claim is inactive", () => {
    const controller = { ...createTestController("split"), slotAttachment: focusableSlotAttachment };
    let active = $state(true);
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        get active() {
          return active;
        },
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      flushSync();
      expect(target.querySelector(".workspace-dock-reopenstrip")).toBeNull();

      flushSync(() => controller.setDockMode("collapsed"));
      expect(target.querySelector(".workspace-dock-reopenstrip")).not.toBeNull();

      // No claim, no workspace: nothing to reopen.
      active = false;
      flushSync();
      expect(target.querySelector(".workspace-dock-reopenstrip")).toBeNull();
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
    }
  });
});

describe("WorkspaceDockPanel resize handle", () => {
  it("hides the BottomDock resize handle only while expanded", () => {
    const controller = createTestController("split");
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        active: true,
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      const splitHandle = document.querySelector<HTMLElement>(".kit-split-resize-handle");
      expect(splitHandle).not.toBeNull();
      expect(getComputedStyle(splitHandle!).display).not.toBe("none");

      flushSync(() => controller.setDockMode("expanded"));

      const expandedHandle = document.querySelector<HTMLElement>(".kit-split-resize-handle");
      expect(expandedHandle).not.toBeNull();
      expect(getComputedStyle(expandedHandle!).display).toBe("none");

      flushSync(() => controller.setDockMode("split"));

      const restoredHandle = document.querySelector<HTMLElement>(".kit-split-resize-handle");
      expect(restoredHandle).not.toBeNull();
      expect(getComputedStyle(restoredHandle!).display).not.toBe("none");
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
    }
  });

  it("keeps nested terminal split handles visible while expanded", () => {
    // The hosted terminal reparents its split tree into the dock body, and
    // its pane handles carry the same kit class as BottomDock's top-edge
    // handle. The expanded-mode hide must only reach the dock's own direct
    // child, or internal pane resizing silently dies in expanded mode.
    const controller = createTestController("expanded");
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceDockPanel, {
      target,
      props: {
        controller,
        active: true,
        detailTitle: "#7 Fix the widget",
        children: detailChildren,
      },
    });

    try {
      flushSync();
      const slot = document.querySelector<HTMLElement>(".workspace-dock-slot");
      expect(slot).not.toBeNull();
      const nestedHandle = document.createElement("div");
      nestedHandle.className = "kit-split-resize-handle kit-split-resize-handle--horizontal";
      slot!.appendChild(nestedHandle);

      const dockHandle = document.querySelector<HTMLElement>(".kit-bottom-dock > .kit-split-resize-handle");
      expect(dockHandle).not.toBeNull();
      expect(getComputedStyle(dockHandle!).display).toBe("none");
      expect(getComputedStyle(nestedHandle).display).not.toBe("none");
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
    }
  });
});
