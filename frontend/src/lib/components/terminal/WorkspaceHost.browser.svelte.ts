import { page } from "vite-plus/test/browser";
import { flushSync, mount, unmount } from "svelte";
import { describe, expect, it, vi, beforeEach, afterEach } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS } from "@middleman/ui";
import { createDiffStore } from "@middleman/ui/stores/diff";

import { STORES_KEY } from "../../../../../packages/ui/src/context.js";
import { createMockApiFetch, jsonResponse, type MockRouteOverride } from "../../../test/mockApiFetch.js";
import AttachmentHost from "../../../test/AttachmentHost.svelte";
import { navigate } from "../../stores/router.svelte.ts";
import {
  getInlineWorkspaceController,
  registerSlotElement,
  resetWorkspaceHostForTest,
} from "../../stores/workspace-host.svelte.ts";
import WorkspaceHost from "./WorkspaceHost.svelte";
import InlineWorkspacePaneHarness from "./InlineWorkspacePaneHarness.svelte";
import { createPaneLayoutStore } from "../../../../../packages/ui/src/stores/paneLayout.svelte.js";

const WAIT = 10_000;

const identityA = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widget",
  repoPath: "acme/widget",
  number: 7,
  itemType: "pull",
};

const workspace = {
  id: "ws-1",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widget",
  repo: {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widget",
    repo_path: "acme/widget",
  },
  item_type: "pull_request",
  item_number: 7,
  git_head_ref: "feature/inline-host",
  worktree_path: "/tmp/worktree",
  tmux_session: "middleman-ws-1",
  status: "ready",
  enrichment_status: "fresh",
  created_at: "2026-04-29T00:00:00Z",
};

const emptyRuntime = { launch_targets: [], sessions: [] };

function workspaceRoutes(): MockRouteOverride {
  return (req) => {
    if (req.method !== "GET") return null;
    if (req.url.pathname === "/api/v1/workspaces/ws-1") return jsonResponse(workspace);
    if (req.url.pathname === "/api/v1/workspaces/ws-1/runtime") return jsonResponse(emptyRuntime);
    if (req.url.pathname === "/api/v1/workspaces") return jsonResponse({ workspaces: [workspace] });
    return null;
  };
}

// A no-op EventSource: WTV opens one to watch for workspace/diff invalidation
// events, and a real EventSource would spin retrying against a backend that
// doesn't exist in this tier. Mirrors browserAppHarness.ts's NoopEventSource.
class NoopEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  readyState = 0;
  withCredentials = false;
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: unknown) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;

  constructor(url: string | URL) {
    this.url = String(url);
  }

  addEventListener(): void {}
  removeEventListener(): void {}
  dispatchEvent(): boolean {
    return false;
  }
  close(): void {
    this.readyState = 2;
  }
}

// The attach path (WorkspaceHost.svelte's placement effect) moves the host
// into parking synchronously, then defers the move into its real destination
// by a tick, then reveals once the wrapper reports non-zero geometry via a
// requestAnimationFrame poll. Two animation frames comfortably clear both the
// deferred tick and at least one geometry check.
function waitForReparent(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve(undefined)));
  });
}

const settingsStore = {
  getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
  getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
};

describe("WorkspaceHost", () => {
  let originalFetch: typeof globalThis.fetch;
  let originalEventSource: typeof globalThis.EventSource;
  let hostContainer: HTMLElement;
  let tabSlot: HTMLElement;
  let prsSlot: HTMLElement;
  let instance: object | null = null;

  beforeEach(() => {
    resetWorkspaceHostForTest();
    navigate("/workspaces");

    const api = createMockApiFetch([workspaceRoutes()]);
    originalFetch = globalThis.fetch;
    originalEventSource = globalThis.EventSource;
    globalThis.fetch = api.fetch;
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

    hostContainer = document.createElement("div");
    tabSlot = document.createElement("div");
    prsSlot = document.createElement("div");
    // Explicit, definite dimensions: `.workspace-host-wrapper` is
    // `height: 100%`, which only resolves to a non-zero pixel height when
    // its immediate parent has a definite (non-auto) height. Without this,
    // the reveal loop's `getBoundingClientRect().height > 0` check would
    // never pass and `attachedVisible` would never flip true.
    tabSlot.style.cssText = "width: 400px; height: 300px;";
    prsSlot.style.cssText = "width: 400px; height: 300px;";
    document.body.append(hostContainer, tabSlot, prsSlot);
    registerSlotElement("tab", tabSlot);
    registerSlotElement("prs", prsSlot);
  });

  afterEach(() => {
    if (instance) flushSync(() => unmount(instance as never));
    instance = null;
    hostContainer.remove();
    tabSlot.remove();
    prsSlot.remove();
    globalThis.fetch = originalFetch;
    globalThis.EventSource = originalEventSource;
    resetWorkspaceHostForTest();
    navigate("/workspaces");
  });

  it("reparents the live WTV wrapper between slots without recreating it", async () => {
    instance = mount(WorkspaceHost, {
      target: hostContainer,
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    navigate("/terminal/ws-1");
    flushSync();
    await waitForReparent();

    const host = document.querySelector(".workspace-host-wrapper") as HTMLElement;
    expect(host).not.toBeNull();
    expect(host.parentElement).toBe(tabSlot);

    // The reveal is rAF-geometry-based (see WorkspaceHost.svelte's
    // placement effect): confirm the host actually becomes interactive
    // after attach, not just physically parented. A regression that left
    // `attachedVisible` stuck false (e.g. a broken geometry check) would
    // leave `inert` permanently true and pass silently without this.
    await vi.waitFor(() => {
      expect(host.inert).toBe(false);
    }, WAIT);

    const stage = await vi.waitFor(() => {
      const el = host.querySelector(".workspace-stage");
      expect(el).not.toBeNull();
      return el as HTMLElement;
    }, WAIT);

    // Inline claim + route to pulls: same hosted key -> reparent only, no
    // recreation of the WTV subtree underneath the wrapper.
    getInlineWorkspaceController("prs").claim(identityA, { id: "ws-1", status: "ready" });
    navigate("/pulls");
    flushSync();
    await waitForReparent();

    expect(host.parentElement).toBe(prsSlot);
    expect(host.querySelector(".workspace-stage")).toBe(stage);

    // Reveals again after the second reparent (tab -> prs), proving the
    // rAF-geometry reveal isn't a one-shot fluke of the first attach.
    await vi.waitFor(() => {
      expect(host.inert).toBe(false);
    }, WAIT);

    // No claim on the issues page -> parked, inert, hidden.
    navigate("/issues");
    flushSync();
    await waitForReparent();

    expect(host.parentElement?.classList.contains("workspace-host-parking")).toBe(true);
    expect(host.inert).toBe(true);
  });

  it("parks synchronously when the displaying slot's element is torn down", async () => {
    instance = mount(WorkspaceHost, {
      target: hostContainer,
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    const prs = getInlineWorkspaceController("prs");
    const slotFixtureTarget = document.createElement("div");
    document.body.appendChild(slotFixtureTarget);
    // Register the "prs" slot through the controller's real attachment
    // (mounted via a tiny fixture component, since {@attach ...} is
    // template-only syntax) rather than the plain divs from beforeEach, so
    // this test exercises the attachment's own teardown path.
    let slotFixture: object | null = mount(AttachmentHost, {
      target: slotFixtureTarget,
      props: { attachment: prs.slotAttachment },
    });
    try {
      flushSync();
      const slotEl = slotFixtureTarget.querySelector('[data-testid="attachment-host"]') as HTMLElement;
      expect(slotEl).not.toBeNull();

      prs.claim(identityA, { id: "ws-1", status: "ready" });
      navigate("/pulls");
      flushSync();
      await waitForReparent();

      const host = document.querySelector(".workspace-host-wrapper") as HTMLElement;
      expect(host.parentElement).toBe(slotEl);

      // Tear down the slot's element (simulating the inline dock unmounting
      // while it is still displaying the host). The attachment's cleanup
      // must move the host into parking synchronously as part of this
      // call, before the node it was living in is discarded.
      flushSync(() => unmount(slotFixture as never));
      slotFixture = null;

      expect(document.contains(host)).toBe(true);
      expect(host.parentElement?.classList.contains("workspace-host-parking")).toBe(true);
    } finally {
      if (slotFixture) flushSync(() => unmount(slotFixture as never));
      slotFixtureTarget.remove();
    }
  });

  it("focuses the host wrapper once it reveals after reopening a collapsed dock", async () => {
    instance = mount(WorkspaceHost, {
      target: hostContainer,
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    const prs = getInlineWorkspaceController("prs");
    // A hidden workspace pane unmounts its portal slot rather than merely
    // hiding it, so simulate "collapsed" here by unregistering the slot rather
    // than registering the beforeEach fixture div.
    prs.setDockMode("collapsed");
    registerSlotElement("prs", null);
    prs.claim(identityA, { id: "ws-1", status: "ready" });
    navigate("/pulls");
    flushSync();
    await waitForReparent();

    const host = document.querySelector(".workspace-host-wrapper") as HTMLElement;
    expect(host.parentElement?.classList.contains("workspace-host-parking")).toBe(true);
    expect(host.inert).toBe(true);

    // "Focus Terminal" (PullDetail/IssueDetail's button, or the dock's own
    // reopen strip) reveals a collapsed dock in split — never expanded —
    // and asks the host to take focus. The host is still parked/inert at
    // this point — its slot hasn't mounted — so the direct focus attempt
    // inside focusTerminal is expected to no-op; the pending-focus flag it
    // also sets is what must carry the request through to the reveal below.
    prs.focusTerminal();
    expect(prs.getDockMode()).toBe("split");
    expect(document.activeElement).not.toBe(host);

    // Mount the slot the now-reopened dock would render.
    const expandedSlot = document.createElement("div");
    expandedSlot.style.cssText = "width: 400px; height: 300px;";
    document.body.appendChild(expandedSlot);
    try {
      registerSlotElement("prs", expandedSlot);
      flushSync();
      await waitForReparent();

      await vi.waitFor(() => {
        expect(host.inert).toBe(false);
        expect(host.contains(document.activeElement) || document.activeElement === host).toBe(true);
      }, WAIT);
    } finally {
      expandedSlot.remove();
    }
  });

  it("keeps the workspace's own chrome out of every inline slot", async () => {
    instance = mount(WorkspaceHost, {
      target: hostContainer,
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    navigate("/terminal/ws-1");
    flushSync();
    await waitForReparent();

    const host = document.querySelector(".workspace-host-wrapper") as HTMLElement;
    expect(host).not.toBeNull();
    expect(host.parentElement).toBe(tabSlot);

    await vi.waitFor(() => {
      expect(host.inert).toBe(false);
      expect(host.querySelector(".header-end")).not.toBeNull();
    }, WAIT);

    // WorkspaceHost.svelte's inlineDockForSlot() only builds the inlineDock
    // prop for an inline dock slot (activity/prs/issues) — never for the
    // Workspaces tab. Hosted in the tab slot, WTV's toolbar must render
    // neither the toggle nor the collapse button.
    const tabScope = page.elementLocator(host);
    expect(tabScope.getByRole("button", { name: "Expand Terminal" }).query()).toBeNull();
    expect(tabScope.getByRole("button", { name: "Collapse Terminal" }).query()).toBeNull();

    // Inline claim + route to pulls: same hosted key -> reparent into the
    // prs slot, which is an inline dock slot.
    getInlineWorkspaceController("prs").claim(identityA, { id: "ws-1", status: "ready" });
    navigate("/pulls");
    flushSync();
    await waitForReparent();

    expect(host.parentElement).toBe(prsSlot);
    await vi.waitFor(() => {
      expect(host.inert).toBe(false);
    }, WAIT);

    // A detail pane never gets the workspace's own header: the pane's tab strip
    // already names it, and the dock modes move into the pane's controls popover,
    // which the surface renders outside this host.
    const prsScope = page.elementLocator(host);
    await vi.waitFor(() => {
      expect(host.querySelector(".header-end")).toBeNull();
    }, WAIT);
    expect(prsScope.getByRole("button", { name: "Expand Terminal" }).query()).toBeNull();
    expect(prsScope.getByRole("button", { name: "Collapse Terminal" }).query()).toBeNull();

    // No claim on the issues page -> parked (slot === null), which is also
    // not an inline dock slot: the buttons must disappear again.
    navigate("/issues");
    flushSync();
    await waitForReparent();

    expect(host.parentElement?.classList.contains("workspace-host-parking")).toBe(true);
    const parkedScope = page.elementLocator(host);
    expect(parkedScope.getByRole("button", { name: "Expand Terminal" }).query()).toBeNull();
    expect(parkedScope.getByRole("button", { name: "Collapse Terminal" }).query()).toBeNull();
  });

  it("fills the maximized PR pane with the existing hosted workspace shell", async () => {
    const prs = getInlineWorkspaceController("prs");
    // A fresh store rather than the surface-cached one: this spec owns the
    // arrangement it maximizes, and localStorage from another test must not
    // decide where the workspace pane sits.
    localStorage.removeItem("middleman-pane-layout-v1:prs");
    const layout = createPaneLayoutStore("prs", ["conversation", "workspace"], {
      type: "split",
      id: "split-root",
      direction: "vertical",
      ratio: 0.5,
      first: { type: "leaf", id: "leaf-detail", tabs: ["conversation"], activeTabKey: "conversation" },
      second: { type: "leaf", id: "leaf-workspace", tabs: ["workspace"], activeTabKey: "workspace" },
    });

    const panelTarget = document.createElement("div");
    // Wide enough that the layout keeps its tree: below its flatten threshold
    // the panes collapse into one strip and the workspace slot never mounts.
    panelTarget.style.cssText = "display: flex; width: 1600px; height: 600px;";
    document.body.appendChild(panelTarget);
    const panel = mount(InlineWorkspacePaneHarness, {
      target: panelTarget,
      props: { layout, controller: prs },
    });

    try {
      instance = mount(WorkspaceHost, {
        target: hostContainer,
        context: new Map([[STORES_KEY, { settings: settingsStore }]]),
      });

      prs.claim(identityA, { id: "ws-1", status: "ready" });
      navigate("/pulls");
      flushSync();
      await waitForReparent();

      const host = panelTarget.querySelector<HTMLElement>(".workspace-host-wrapper");
      expect(host).not.toBeNull();
      await vi.waitFor(() => expect(host?.inert).toBe(false), WAIT);
      const existingStage = host!.querySelector(".workspace-stage");
      expect(existingStage).not.toBeNull();

      // This workspace is running nothing, so the pane auto-opened its launcher
      // overlay, and no pane may be maximized over an open dialog. Dismiss it the
      // way a user would: what is under test here is the reparent, not the
      // launcher.
      await vi.waitFor(() => expect(document.querySelector("[role='dialog']")).not.toBeNull(), WAIT);
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      await vi.waitFor(() => expect(document.querySelector("[role='dialog']")).toBeNull(), WAIT);

      // Maximizing must reuse the live shell, not rebuild it: a remounted stage
      // means a dropped terminal socket.
      flushSync(() => layout.toggleZoom("leaf-workspace"));

      const paneRect = panelTarget.querySelector<HTMLElement>(".detail-pane-layout")!.getBoundingClientRect();
      const hostRect = host!.getBoundingClientRect();
      expect(host!.querySelector(".workspace-stage")).toBe(existingStage);
      expect(Math.round(hostRect.height)).toBeGreaterThan(Math.round(paneRect.height * 0.8));
      expect(Math.round(paneRect.bottom - hostRect.bottom)).toBe(0);
    } finally {
      flushSync(() => unmount(panel));
      panelTarget.remove();
    }
  });

  it("unmounts the right sidebar while parked and restores it on reveal", async () => {
    // Opening the right sidebar mounts the diff panel, which reads the diff
    // store from context. Created after beforeEach's fetch swap so its API
    // calls hit the mock (and fail harmlessly into the panel's error state
    // — this test only asserts sidebar presence).
    const diffStore = createDiffStore();
    localStorage.setItem("middleman-workspace-sidebar-open", "true");
    try {
      instance = mount(WorkspaceHost, {
        target: hostContainer,
        context: new Map([[STORES_KEY, { settings: settingsStore, diff: diffStore }]]),
      });

      navigate("/terminal/ws-1");
      flushSync();
      await waitForReparent();

      const host = document.querySelector(".workspace-host-wrapper") as HTMLElement;
      await vi.waitFor(() => {
        expect(host.inert).toBe(false);
        expect(host.querySelector(".right-sidebar")).not.toBeNull();
      }, WAIT);

      // No claim on the issues page -> parked in the display:none parking
      // node, possibly for the rest of the session. The right sidebar must
      // unmount — not merely hide — so its diff panel and listeners don't
      // keep running on every unrelated page.
      navigate("/issues");
      flushSync();
      await waitForReparent();

      expect(host.parentElement?.classList.contains("workspace-host-parking")).toBe(true);
      expect(host.querySelector(".right-sidebar")).toBeNull();
      // The terminal main subtree stays alive across parking — only the
      // sidebar unmounts.
      expect(host.querySelector(".workspace-stage")).not.toBeNull();

      // sidebarOpen persists (localStorage), so revealing again restores
      // the sidebar without user interaction.
      navigate("/terminal/ws-1");
      flushSync();
      await waitForReparent();
      await vi.waitFor(() => {
        expect(host.querySelector(".right-sidebar")).not.toBeNull();
      }, WAIT);
    } finally {
      localStorage.removeItem("middleman-workspace-sidebar-open");
      localStorage.removeItem("middleman-workspace-sidebar-width");
    }
  });
});
