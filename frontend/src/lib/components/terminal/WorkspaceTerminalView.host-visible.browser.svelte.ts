import { page } from "vite-plus/test/browser";
import { flushSync, mount, unmount } from "svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS } from "../../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { createDiffStore } from "../../stores/diff.svelte.js";
import { getStackDepth } from "../../stores/keyboard/modal-stack.svelte.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";

import { STORES_KEY } from "../../context.js";
import { createMockApiFetch, jsonResponse, type MockRouteOverride } from "../../../test/mockApiFetch.js";
import WorkspaceTerminalView from "./WorkspaceTerminalViewTestHarness.svelte";

const WAIT = 10_000;

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
  git_head_ref: "feature/host-visible",
  worktree_path: "/tmp/worktree",
  tmux_session: "kenn-forge-ws-1",
  status: "ready",
  enrichment_status: "fresh",
  created_at: "2026-04-29T00:00:00Z",
};

const emptyRuntime = { launch_targets: [], sessions: [] };
const agentRuntime = {
  launch_targets: [],
  sessions: [
    {
      key: "ws-1:helper",
      workspace_id: "ws-1",
      target_key: "helper",
      label: "Helper",
      kind: "agent",
      status: "running",
      display_region: "workflow",
      created_at: "2026-04-29T00:00:00Z",
    },
  ],
};

const controlledSockets: ControlledWebSocket[] = [];

class ControlledWebSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readonly CONNECTING = ControlledWebSocket.CONNECTING;
  readonly OPEN = ControlledWebSocket.OPEN;
  readonly CLOSING = ControlledWebSocket.CLOSING;
  readonly CLOSED = ControlledWebSocket.CLOSED;
  binaryType: BinaryType = "arraybuffer";
  readyState = ControlledWebSocket.CONNECTING;
  readonly sent: Array<string | ArrayBufferView> = [];

  constructor(readonly url: string) {
    super();
    controlledSockets.push(this);
    queueMicrotask(() => {
      this.readyState = ControlledWebSocket.OPEN;
      this.dispatchEvent(new Event("open"));
    });
  }

  close(): void {
    this.readyState = ControlledWebSocket.CLOSED;
  }

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
    if (typeof data === "string" || ArrayBuffer.isView(data)) this.sent.push(data);
  }
}

function workspaceRoutes(): MockRouteOverride {
  return (req) => {
    if (req.url.pathname === "/api/v1/workspaces/ws-1" && req.method === "GET") {
      return jsonResponse(workspace);
    }
    if (req.url.pathname === "/api/v1/workspaces/ws-1/runtime" && req.method === "GET") {
      return jsonResponse(emptyRuntime);
    }
    if (req.url.pathname === "/api/v1/workspaces" && req.method === "GET") {
      return jsonResponse({ workspaces: [workspace] });
    }
    // "Delete" always fires a real DELETE first; a 409 here is what opens
    // the force-delete confirmation dialog under test — there is no
    // separate "are you sure" step before the first delete attempt.
    if (req.url.pathname === "/api/v1/workspaces/ws-1" && req.method === "DELETE") {
      return jsonResponse({ detail: "Workspace has uncommitted changes." }, 409);
    }
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

describe("WorkspaceTerminalView hostVisible", () => {
  let runtime: OwnedAppRuntime;

  beforeEach(() => {
    runtime = makeAppRuntime();
    controlledSockets.length = 0;
  });

  it("routes wheel input for an agent session mounted by the workspace", async () => {
    const api = createMockApiFetch([
      (request) =>
        request.url.pathname === "/api/v1/workspaces/ws-1/runtime" && request.method === "GET"
          ? jsonResponse(agentRuntime)
          : null,
      workspaceRoutes(),
    ]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    globalThis.fetch = api.fetch;
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;
    vi.stubGlobal("WebSocket", ControlledWebSocket);
    localStorage.setItem("kenn-forge-workspace-active-tab:ws-1", "session:ws-1:helper");
    const target = document.createElement("div");
    target.style.width = "800px";
    target.style.height = "600px";
    document.body.appendChild(target);
    const settingsStore = createSettingsStore();
    const instance = mount(WorkspaceTerminalView, {
      target,
      props: {
        runtime,
        workspaceId: "ws-1",
        hideWorkspaceList: true,
        hideRightSidebar: true,
      },
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    try {
      await vi.waitFor(() => {
        expect(controlledSockets).toHaveLength(1);
        expect(target.querySelector(".xterm-screen")).not.toBeNull();
      }, WAIT);
      const socket = controlledSockets[0];
      if (socket === undefined) throw new Error("agent terminal socket was not created");
      socket.sent.length = 0;

      const screen = target.querySelector(".xterm-screen");
      if (screen === null) throw new Error("agent terminal screen was not created");
      const defaultAllowed = screen.dispatchEvent(
        new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: -120 }),
      );

      expect(defaultAllowed).toBe(false);
      await vi.waitFor(() => {
        const frames = socket.sent.map((frame) =>
          typeof frame === "string" ? frame : new TextDecoder().decode(frame),
        );
        expect(frames).toContain("\x1b[A");
      }, WAIT);
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
      vi.unstubAllGlobals();
      localStorage.removeItem("kenn-forge-workspace-active-tab:ws-1");
    }
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("unmounts an open dialog while hidden and restores it when visible", async () => {
    const api = createMockApiFetch([workspaceRoutes()]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    globalThis.fetch = api.fetch;
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

    // TerminalOptionsMenu (always present in the ready-workspace toolbar)
    // reads settingsStore.getTerminalSettings() at initialization, so the
    // main subtree cannot mount without a settings store on STORES_KEY.
    const settingsStore = {
      getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
      getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
    };

    // Mounted with Svelte's own `mount()` (rather than vitest-browser-svelte's
    // render/rerender) so flipping `hostVisible` is a real fine-grained prop
    // update: rerender()'s prop diffing coalesces every prop into one raw
    // `$state` object, so any prop change re-invalidates all of them —
    // including WTV's route-scoped effect that unconditionally clears
    // renamePrompt/stopPromptSession/forcePromptMessage on every re-run.
    // That would wipe the open dialog's state on the very rerender meant to
    // hide it, defeating the test before hostVisible is even considered. A
    // getter-backed prop plus flushSync() gives WTV real per-prop
    // reactivity, matching how a parent passes hostVisible in production.
    let hostVisible = $state(true);
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceTerminalView, {
      target,
      props: {
        runtime,
        workspaceId: "ws-1",
        hideWorkspaceList: true,
        hideRightSidebar: true,
        get hostVisible() {
          return hostVisible;
        },
      },
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    try {
      await vi.waitFor(() => {
        const el = document.querySelector(".header-btn.danger");
        expect(el).not.toBeNull();
      }, WAIT);

      // Delete now confirms first; confirming issues the DELETE, whose 409
      // opens the force-delete dialog — the state this test hides and restores.
      await page.getByRole("button", { name: "Delete" }).click();
      await page.getByRole("button", { name: "Delete workspace", exact: true }).click();
      await expect.element(page.getByRole("dialog", { name: "Force delete workspace?" })).toBeVisible();

      hostVisible = false;
      flushSync();

      // Unmounted entirely: no dialog, no kit modal overlay, no Escape owner.
      expect(document.querySelector(".kit-modal-overlay")).toBeNull();

      hostVisible = true;
      flushSync();

      // Reappears with no further interaction: the open-state variable
      // (forcePromptMessage) survived the hidden window.
      await expect.element(page.getByRole("dialog")).toBeVisible();
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    }
  });

  it("hidden host releases the sidebar shortcut and never clamps geometry from hidden layout", async () => {
    const api = createMockApiFetch([workspaceRoutes()]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    globalThis.fetch = api.fetch;
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

    const settingsStore = {
      getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
      getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
    };
    // Opening the right sidebar mounts the diff panel, which reads the
    // diff store from context. Created after the fetch swap so its API
    // calls hit the mock (and fail harmlessly into the panel's error
    // state — this test only asserts sidebar presence).
    const diffStore = createDiffStore({ runtime });

    localStorage.removeItem("kenn-forge-workspace-sidebar-open");
    localStorage.setItem("kenn-forge-workspace-sidebar-width", "400");

    let hostVisible = $state(true);
    const target = document.createElement("div");
    document.body.appendChild(target);

    // hideRightSidebar is false here: the Cmd/Ctrl+] shortcut and the
    // width-clamp effects only exist for a view that renders the right
    // sidebar at all.
    const instance = mount(WorkspaceTerminalView, {
      target,
      props: {
        runtime,
        workspaceId: "ws-1",
        hideWorkspaceList: true,
        hideRightSidebar: false,
        get hostVisible() {
          return hostVisible;
        },
      },
      context: new Map([[STORES_KEY, { settings: settingsStore, diff: diffStore }]]),
    });

    const pressSidebarToggle = () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "]", metaKey: true, cancelable: true }));
      flushSync();
    };

    try {
      await vi.waitFor(() => {
        const el = document.querySelector(".header-btn.danger");
        expect(el).not.toBeNull();
      }, WAIT);

      // Visible: the window-level shortcut opens the right sidebar. The
      // clamp against the (small) test viewport may legitimately shrink
      // the stored width here; what's persisted now is the baseline.
      pressSidebarToggle();
      expect(document.querySelector(".right-sidebar")).not.toBeNull();
      const visibleWidth = localStorage.getItem("kenn-forge-workspace-sidebar-width");
      expect(visibleWidth).not.toBe("0");

      hostVisible = false;
      flushSync();

      // Hidden (parked host): the shortcut belongs to whatever page is
      // actually on screen — it must not toggle this view's sidebar.
      pressSidebarToggle();
      expect(document.querySelector(".right-sidebar")).not.toBeNull();

      // Hidden layout has zero geometry (the parking node is
      // display:none); a window resize must not clamp the sidebar width
      // against it and persist a collapsed value.
      target.style.display = "none";
      window.dispatchEvent(new Event("resize"));
      flushSync();
      expect(localStorage.getItem("kenn-forge-workspace-sidebar-width")).toBe(visibleWidth);
      target.style.display = "";

      // Visible again: the shortcut is re-armed.
      hostVisible = true;
      flushSync();
      pressSidebarToggle();
      expect(document.querySelector(".right-sidebar")).toBeNull();
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      localStorage.removeItem("kenn-forge-workspace-sidebar-open");
      localStorage.removeItem("kenn-forge-workspace-sidebar-width");
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    }
  });

  it("restores the preferred sidebar width after a temporary visible constraint", async () => {
    const api = createMockApiFetch([workspaceRoutes()]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    globalThis.fetch = api.fetch;
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

    const settingsStore = {
      getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
      getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
    };
    const diffStore = createDiffStore({ runtime });

    localStorage.removeItem("kenn-forge-workspace-sidebar-open");
    localStorage.setItem("kenn-forge-workspace-sidebar-width", "400");

    const target = document.createElement("div");
    target.style.width = "804px";
    target.style.height = "600px";
    document.body.appendChild(target);

    const instance = mount(WorkspaceTerminalView, {
      target,
      props: {
        runtime,
        workspaceId: "ws-1",
        hideWorkspaceList: true,
        hideRightSidebar: false,
      },
      context: new Map([[STORES_KEY, { settings: settingsStore, diff: diffStore }]]),
    });

    const resizeWindow = () => {
      window.dispatchEvent(new Event("resize"));
      flushSync();
    };

    try {
      await vi.waitFor(() => {
        expect(document.querySelector(".header-btn.danger")).not.toBeNull();
      }, WAIT);

      await page.getByRole("button", { name: "Diff", exact: true }).click();
      const sidebar = document.querySelector<HTMLElement>(".right-sidebar");
      expect(sidebar).not.toBeNull();
      expect(sidebar!.style.width).toBe("400px");

      target.style.width = "404px";
      resizeWindow();
      await vi.waitFor(() => expect(sidebar!.style.width).toBe("100px"), WAIT);
      expect(localStorage.getItem("kenn-forge-workspace-sidebar-width")).toBe("400");

      target.style.width = "804px";
      resizeWindow();
      await vi.waitFor(() => expect(sidebar!.style.width).toBe("400px"), WAIT);
      expect(localStorage.getItem("kenn-forge-workspace-sidebar-width")).toBe("400");
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      localStorage.removeItem("kenn-forge-workspace-sidebar-open");
      localStorage.removeItem("kenn-forge-workspace-sidebar-width");
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    }
  });

  it("parking closes toolbar menus and their nested modal instead of leaving an invisible stack", async () => {
    const api = createMockApiFetch([workspaceRoutes()]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    globalThis.fetch = api.fetch;
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

    // Unlike the other tests, this one actually mounts TerminalSettings
    // (inside the options popover) with livePreview, which writes settings
    // back through the store — so the stub needs a setter too.
    const settingsStore = {
      getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
      getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
      setTerminalSettings: () => {},
    };

    let hostVisible = $state(true);
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceTerminalView, {
      target,
      props: {
        runtime,
        workspaceId: "ws-1",
        hideWorkspaceList: true,
        hideRightSidebar: true,
        get hostVisible() {
          return hostVisible;
        },
      },
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    try {
      await vi.waitFor(() => {
        const el = document.querySelector(".header-btn.danger");
        expect(el).not.toBeNull();
      }, WAIT);

      // Terminal options popover, then the font picker Modal nested inside
      // it — the deepest overlay stack a toolbar menu can build.
      await page.getByRole("button", { name: "Terminal options" }).click();
      await expect.element(page.getByRole("dialog", { name: "Terminal options" })).toBeVisible();
      await page.getByRole("button", { name: "Choose" }).click();
      await expect.element(page.getByRole("dialog", { name: "Choose monospace font" })).toBeVisible();
      expect(getStackDepth()).toBeGreaterThan(0);

      hostVisible = false;
      flushSync();

      // Parked with the stack open (e.g. browser Back mid-dialog): nothing
      // may survive invisibly — no overlay blocking clicks, no focus trap,
      // no modal-stack frame owning Escape, no popover window listeners.
      expect(document.querySelector(".options-popover")).toBeNull();
      expect(document.querySelector(".kit-modal-overlay")).toBeNull();
      expect(getStackDepth()).toBe(0);

      hostVisible = true;
      flushSync();

      // Menus are transient (unlike the confirm dialogs above, which
      // restore): re-revealing must not resurrect the popover.
      expect(document.querySelector(".options-popover")).toBeNull();
      expect(getStackDepth()).toBe(0);
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    }
  });

  it("disables terminal zoom while an options save is in flight", async () => {
    const api = createMockApiFetch([workspaceRoutes()]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    let resolveSettingsSave: (() => void) | undefined;
    let terminalSettings = $state({ ...DEFAULT_TERMINAL_SETTINGS });

    globalThis.fetch = async (input, init) => {
      const request =
        input instanceof Request ? input : new Request(new URL(String(input), window.location.href), init);
      const url = new URL(request.url);
      if (request.method === "PUT" && url.pathname === "/api/v1/settings") {
        const body = JSON.parse(await request.clone().text()) as {
          terminal: typeof DEFAULT_TERMINAL_SETTINGS;
        };
        return new Promise<Response>((resolve) => {
          resolveSettingsSave = () => {
            resolve(jsonResponse({ terminal: body.terminal }));
          };
        });
      }
      return api.fetch(request);
    };
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

    const settingsStore = {
      getTerminalFontSize: () => terminalSettings.font_size,
      getTerminalSettings: () => terminalSettings,
      setTerminalSettings: (settings: typeof DEFAULT_TERMINAL_SETTINGS) => {
        terminalSettings = settings;
      },
    };
    const target = document.createElement("div");
    document.body.appendChild(target);
    const instance = mount(WorkspaceTerminalView, {
      target,
      props: {
        runtime,
        workspaceId: "ws-1",
        hideWorkspaceList: true,
        hideRightSidebar: true,
      },
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    try {
      await vi.waitFor(() => {
        expect(document.querySelector(".header-btn.danger")).not.toBeNull();
      }, WAIT);

      await page.getByRole("button", { name: "Terminal options" }).click();
      await page.getByRole("textbox", { name: "Monospace font family" }).fill("Iosevka Term");
      await page.getByRole("button", { name: "Save" }).click();
      await expect.element(page.getByRole("button", { name: "Saving..." })).toBeVisible();

      await expect.element(page.getByRole("button", { name: "Increase terminal font size" })).toBeDisabled();

      await vi.waitFor(() => expect(resolveSettingsSave).toBeTypeOf("function"), WAIT);
      resolveSettingsSave!();
      await expect.element(page.getByRole("button", { name: "Save" })).toBeVisible();
      await expect.element(page.getByRole("button", { name: "Increase terminal font size" })).toBeEnabled();
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    }
  });

  it("keeps terminal options disabled until a rejected zoom rolls back", async () => {
    const api = createMockApiFetch([workspaceRoutes()]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    let rejectZoomSave: (() => void) | undefined;
    let terminalSettings = $state({ ...DEFAULT_TERMINAL_SETTINGS });

    globalThis.fetch = async (input, init) => {
      const request =
        input instanceof Request ? input : new Request(new URL(String(input), window.location.href), init);
      const url = new URL(request.url);
      if (request.method === "PUT" && url.pathname === "/api/v1/settings") {
        return new Promise<Response>((resolve) => {
          rejectZoomSave = () => {
            resolve(jsonResponse({ title: "Settings unavailable" }, 503));
          };
        });
      }
      return api.fetch(request);
    };
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

    const settingsStore = {
      getTerminalFontSize: () => terminalSettings.font_size,
      getTerminalSettings: () => terminalSettings,
      setTerminalSettings: (settings: typeof DEFAULT_TERMINAL_SETTINGS) => {
        terminalSettings = settings;
      },
    };
    const target = document.createElement("div");
    document.body.appendChild(target);
    const instance = mount(WorkspaceTerminalView, {
      target,
      props: {
        runtime,
        workspaceId: "ws-1",
        hideWorkspaceList: true,
        hideRightSidebar: true,
      },
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    try {
      await vi.waitFor(() => {
        expect(document.querySelector(".header-btn.danger")).not.toBeNull();
      }, WAIT);

      const optionsButton = page.getByRole("button", { name: "Terminal options" });
      await page.getByRole("button", { name: "Increase terminal font size" }).click();
      await vi.waitFor(() => expect(rejectZoomSave).toBeTypeOf("function"), WAIT);

      await expect.element(optionsButton).toBeDisabled();

      rejectZoomSave!();
      await expect.element(optionsButton).toBeEnabled();
      await optionsButton.click();
      await expect.element(page.getByRole("dialog", { name: "Terminal options" })).toBeVisible();
      expect(document.querySelector<HTMLInputElement>("#terminal-font-size")?.value).toBe("12");
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    }
  });

  it("unmounts the workspace list's own delete dialog while hidden and restores it when visible", async () => {
    const api = createMockApiFetch([workspaceRoutes()]);
    const originalFetch = globalThis.fetch;
    const originalEventSource = globalThis.EventSource;
    globalThis.fetch = api.fetch;
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

    const settingsStore = {
      getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
      getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
    };

    // Same getter-backed prop + flushSync() rationale as the test above:
    // real per-prop reactivity, matching how WorkspaceHost passes
    // hostVisible down in production. hideWorkspaceList is false here (the
    // opposite of the test above) so WorkspaceListSidebar — not WTV's own
    // toolbar — is what's under test.
    let hostVisible = $state(true);
    const target = document.createElement("div");
    document.body.appendChild(target);

    const instance = mount(WorkspaceTerminalView, {
      target,
      props: {
        runtime,
        workspaceId: "ws-1",
        hideWorkspaceList: false,
        hideRightSidebar: true,
        get hostVisible() {
          return hostVisible;
        },
      },
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });

    try {
      const row = await vi.waitFor(() => {
        const el = document.querySelector(".ws-row");
        expect(el).not.toBeNull();
        return el as HTMLElement;
      }, WAIT);

      // Opens the row's context menu, then its delete confirmation — no
      // network round trip needed; the dialog's open state is local
      // (deleteConfirmWorkspace) and is what this test exercises.
      row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
      await page.getByRole("menuitem", { name: "Delete workspace..." }).click();
      await expect.element(page.getByRole("dialog", { name: "Delete workspace?" })).toBeVisible();
      expect(getStackDepth()).toBeGreaterThan(0);

      hostVisible = false;
      flushSync();

      // Unmounted entirely: no dialog, no kit modal overlay, no lingering
      // modal-stack frame or Escape owner left behind for the page under
      // it (see WorkspaceListSidebar.svelte's hostVisible-gated
      // ConfirmDialog).
      expect(document.querySelector(".kit-modal-overlay")).toBeNull();
      expect(getStackDepth()).toBe(0);

      hostVisible = true;
      flushSync();

      // Reappears with no further interaction: deleteConfirmWorkspace
      // survived the hidden window, matching WTV's own three dialogs.
      await expect.element(page.getByRole("dialog", { name: "Delete workspace?" })).toBeVisible();
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    }
  });
});
