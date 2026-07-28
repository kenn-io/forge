// The controls popover opens from a detail pane's tab strip, and that strip is a
// 30px horizontal scroll container inside a leaf that clips overflow. Whether the
// popover survives that is a question about real painting and hit-testing: jsdom
// has no layout, so it cannot tell a usable popover from one clipped to a sliver.
import { createRawSnippet, mount, unmount } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
// The real z-index tokens: this popover's layer is defined relative to the modal
// layer, and the browser project loads no app stylesheet, so without the theme
// both sides of that comparison would collapse to `auto`.
import "@kenn-io/kit-ui/theme.css";

import WorkspacePaneControls from "./WorkspacePaneControls.svelte";
import { registerWorkspaceControls, resetWorkspaceHostForTest } from "../../stores/workspace-host.svelte.ts";

const controls = createRawSnippet(() => ({
  render: () =>
    `<div style="display: flex; gap: 4px">
       <button type="button" style="height: 22px; width: 90px">Save preset</button>
       <button type="button" style="height: 22px; width: 90px">Terminal options</button>
     </div>`,
}));

/**
 * The clipping ancestors, reproduced from TabbedPanelTree: a tab strip that scrolls
 * horizontally (which forces vertical clipping too) inside a leaf that hides
 * overflow. Placed low in the viewport so a popover that fell back to being
 * clipped by the strip could not be mistaken for a working one.
 */
function mountInTabStrip(): { host: HTMLElement; strip: HTMLElement; app: Record<string, unknown> } {
  const host = document.createElement("div");
  host.style.cssText = "position: fixed; left: 40px; top: 200px; width: 400px; height: 160px; overflow: hidden;";
  const strip = document.createElement("div");
  strip.style.cssText = "position: relative; display: flex; min-height: 30px; height: 30px; overflow-x: auto;";
  host.append(strip);
  document.body.append(host);
  const app = mount(WorkspacePaneControls, { target: strip });
  return { host, strip, app };
}

/**
 * The stacking the pane actually builds, which the strip alone does not reproduce:
 * the leaf's action container is `position: relative; z-index: 2` (it has to sit
 * above the strip's bottom-border pseudo-element), and the pane body next to it
 * holds a terminal whose xterm canvases carry their own z-indexes. Both compete one
 * level up, so a popover parented inside the actions container is clamped under the
 * canvas no matter how high its own z-index goes.
 */
function mountInPaneLeaf(): { host: HTMLElement; actions: HTMLElement; app: Record<string, unknown> } {
  const host = document.createElement("div");
  host.style.cssText = "position: fixed; left: 40px; top: 120px; width: 500px; height: 400px; overflow: hidden;";
  const strip = document.createElement("div");
  strip.style.cssText = "position: relative; display: flex; min-height: 30px; height: 30px; overflow-x: auto;";
  const actions = document.createElement("div");
  actions.style.cssText = "display: inline-flex; margin-left: auto; position: relative; z-index: 2;";
  strip.append(actions);
  const body = document.createElement("div");
  body.style.cssText = "position: relative; height: 370px;";
  const canvas = document.createElement("canvas");
  canvas.className = "xterm-link-layer";
  canvas.style.cssText = "position: absolute; inset: 0; z-index: 3;";
  body.append(canvas);
  host.append(strip, body);
  document.body.append(host);
  const app = mount(WorkspacePaneControls, { target: actions });
  return { host, actions, app };
}

describe("workspace controls popover in a real tab strip", () => {
  let mounted: { host: HTMLElement; strip: HTMLElement; app: Record<string, unknown> } | null = null;

  afterEach(() => {
    if (mounted) {
      unmount(mounted.app);
      mounted.host.remove();
      mounted = null;
    }
    resetWorkspaceHostForTest();
  });

  it("opens clear of the strip that clips it, and its controls are clickable", async () => {
    registerWorkspaceControls({ snippet: controls, workspaceKey: "ws-1" });
    mounted = mountInTabStrip();

    const trigger = mounted.strip.querySelector<HTMLButtonElement>("button[aria-label='Workspace controls']");
    expect(trigger).not.toBeNull();
    trigger!.click();

    const popover = await vi.waitFor(() => {
      const el = document.querySelector<HTMLElement>("[role='dialog'][aria-label='Workspace controls']");
      expect(el).not.toBeNull();
      // Positioned on the frame after opening, so wait for a real placement rather
      // than the pre-measurement one.
      expect(el!.getBoundingClientRect().width).toBeGreaterThan(0);
      return el!;
    });

    const stripRect = mounted.strip.getBoundingClientRect();
    const rect = popover.getBoundingClientRect();
    // Taller than the strip and hanging below it: the strip is 30px and clips, so
    // a popover confined to it would be a sliver.
    expect(rect.height).toBeGreaterThan(stripRect.height);
    expect(rect.bottom).toBeGreaterThan(stripRect.bottom);

    // The real question clipping decides: is anything there to click? A clipped
    // popover keeps its box but paints nothing, so hit-testing lands on whatever
    // is behind it.
    const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
    expect(popover.contains(hit)).toBe(true);

    const optionsButton = popover.querySelector<HTMLButtonElement>("button:last-of-type");
    const buttonRect = optionsButton!.getBoundingClientRect();
    const buttonHit = document.elementFromPoint(
      buttonRect.left + buttonRect.width / 2,
      buttonRect.top + buttonRect.height / 2,
    );
    expect(optionsButton!.contains(buttonHit) || optionsButton === buttonHit).toBe(true);
  });

  it("stays clickable over the terminal that fills the pane below it", async () => {
    registerWorkspaceControls({ snippet: controls, workspaceKey: "ws-1" });
    const leaf = mountInPaneLeaf();
    mounted = { host: leaf.host, strip: leaf.actions, app: leaf.app };

    const trigger = leaf.actions.querySelector<HTMLButtonElement>("button[aria-label='Workspace controls']");
    trigger!.click();

    const popover = await vi.waitFor(() => {
      const el = document.querySelector<HTMLElement>("[role='dialog'][aria-label='Workspace controls']");
      expect(el).not.toBeNull();
      expect(el!.getBoundingClientRect().width).toBeGreaterThan(0);
      return el!;
    });

    // A single-session pane has no chrome left, so the terminal reaches right up
    // under this popover. Every click on it landed on the canvas instead.
    const rect = popover.getBoundingClientRect();
    const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
    expect(popover.contains(hit)).toBe(true);
  });

  it("wraps a full control set into rows instead of one pane-wide bar", async () => {
    // Wider than the 440px cap, or the default 414px viewport forces wrapping
    // through the 100vw fallback and the cap could regress unnoticed.
    await page.viewport(900, 700);

    // What a workspace running an agent actually hands over: two dock modes, zoom,
    // options, rename, stop, the branch, Delete, Launch. In one row that is a bar as
    // wide as the pane -- the stacked chrome this popover was built to replace.
    const wide = createRawSnippet(() => ({
      render: () =>
        `<div style="display: contents">${[
          "Expand Terminal",
          "Collapse Terminal",
          "13px",
          "Options",
          "Rename session",
          "Stop session",
          "feature/long-branch-name-that-wraps",
          "Delete",
          "Launch session",
        ]
          .map((label) => `<button type="button" style="height: 22px">${label}</button>`)
          .join("")}</div>`,
    }));
    registerWorkspaceControls({ snippet: wide, workspaceKey: "ws-1" });
    const leaf = mountInPaneLeaf();
    mounted = { host: leaf.host, strip: leaf.actions, app: leaf.app };

    leaf.actions.querySelector<HTMLButtonElement>("button[aria-label='Workspace controls']")!.click();

    const popover = await vi.waitFor(() => {
      const el = document.querySelector<HTMLElement>("[role='dialog'][aria-label='Workspace controls']");
      expect(el!.getBoundingClientRect().width).toBeGreaterThan(0);
      return el!;
    });

    expect(popover.getBoundingClientRect().width).toBeLessThanOrEqual(440);
    const rows = new Set(
      Array.from(popover.querySelectorAll("button")).map((button) => Math.round(button.getBoundingClientRect().top)),
    );
    expect(rows.size).toBeGreaterThan(1);
  });

  it("yields to a modal opened from inside it", async () => {
    // Rename session, Stop session and the font picker all open a modal from these
    // controls, and the popover stays open beneath one on purpose. But it is
    // portalled to the end of `<body>`, which puts it after every in-tree modal in
    // document order, so at an equal z-index it covered the dialog it had just
    // opened and every click on Save landed on the popover.
    registerWorkspaceControls({ snippet: controls, workspaceKey: "ws-1" });
    const leaf = mountInPaneLeaf();
    mounted = { host: leaf.host, strip: leaf.actions, app: leaf.app };

    const trigger = leaf.actions.querySelector<HTMLButtonElement>("button[aria-label='Workspace controls']");
    trigger!.click();

    const popover = await vi.waitFor(() => {
      const el = document.querySelector<HTMLElement>("[role='dialog'][aria-label='Workspace controls']");
      expect(el!.getBoundingClientRect().width).toBeGreaterThan(0);
      return el!;
    });

    // The modal layer as kit-ui's Modal builds it: full-viewport backdrop at
    // --z-overlay, inserted before the portalled popover so document order alone
    // cannot save it.
    const modal = document.createElement("div");
    modal.style.cssText = "position: fixed; inset: 0; z-index: var(--z-overlay); background: rgba(0, 0, 0, 0.4);";
    popover.before(modal);

    try {
      const rect = popover.getBoundingClientRect();
      const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
      expect(hit).toBe(modal);
    } finally {
      modal.remove();
    }
  });
});
