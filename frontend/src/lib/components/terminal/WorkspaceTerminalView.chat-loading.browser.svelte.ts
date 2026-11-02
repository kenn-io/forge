import { page } from "vite-plus/test/browser";
import { flushSync, mount, unmount } from "svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";
import { noteSessionDiscarded, resetSessionHostForTest, sessionHostKey } from "../../stores/session-host.svelte.js";
import "../../../app.css";

import { STORES_KEY } from "../../context.js";
import { createMockApiFetch, jsonResponse } from "../../../test/mockApiFetch.js";
import WorkspaceTerminalView from "./WorkspaceTerminalViewTestHarness.svelte";

const WAIT = 10_000;
const eventsStore = {
  selectWorkspace: () => () => undefined,
  subscribeWorkspaceEvents: () => () => undefined,
};

function workspaceFixture(id: string, number: number) {
  return {
    id,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widget",
    repo: { provider: "github", platform_host: "github.com", owner: "acme", name: "widget", repo_path: "acme/widget" },
    item_type: "pull_request",
    item_number: number,
    source_item_visible: true,
    git_head_ref: `feature/${id}`,
    worktree_path: `/tmp/${id}`,
    tmux_session: `kenn-forge-${id}`,
    status: "ready",
    enrichment_status: "fresh",
    tmux_activity_source: "none",
    tmux_last_output_at: null,
    tmux_working: false,
    created_at: "2026-04-29T00:00:00Z",
  };
}

const chatSession = {
  key: "ws-1:chat",
  workspace_id: "ws-1",
  target_key: "codex",
  label: "Codex",
  kind: "acp",
  status: "running",
  display_region: "workflow",
  created_at: "2026-04-29T00:00:00Z",
};

// Opens at once and then stays silent, like a host that has not sent the
// chat's state yet; these tests are about whether the pane shows up at all.
const chatSockets: string[] = [];
class SilentWebSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  binaryType: BinaryType = "arraybuffer";
  readyState = SilentWebSocket.CONNECTING;

  constructor(readonly url: string) {
    super();
    chatSockets.push(url);
    queueMicrotask(() => {
      this.readyState = SilentWebSocket.OPEN;
      this.dispatchEvent(new Event("open"));
    });
  }

  close(): void {
    this.readyState = SilentWebSocket.CLOSED;
  }

  send(): void {}
}

class NoopEventSource {
  url: string;
  readyState = 0;
  withCredentials = false;
  onopen = null;
  onmessage = null;
  onerror = null;
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

describe("WorkspaceTerminalView chat loading", () => {
  let runtime: OwnedAppRuntime;
  const originalFetch = globalThis.fetch;
  const originalEventSource = globalThis.EventSource;

  beforeEach(async () => {
    await page.viewport(1200, 800);
    runtime = makeAppRuntime();
    chatSockets.length = 0;
    resetSessionHostForTest();
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;
    vi.stubGlobal("WebSocket", SilentWebSocket);
    localStorage.setItem("kenn-forge-workspace-active-tab:ws-1", "session:ws-1:chat");
  });

  afterEach(async () => {
    globalThis.fetch = originalFetch;
    globalThis.EventSource = originalEventSource;
    vi.unstubAllGlobals();
    localStorage.removeItem("kenn-forge-workspace-active-tab:ws-1");
    resetSessionHostForTest();
    await Effect.runPromise(runtime.disposeEffect);
  });

  // A repeat visit restores the workspace from memory but mounts only the
  // sessions the pool still holds until a runtime read succeeds. When the
  // daemon stalls and both reads fail, the chat pane must say it is loading
  // and then recover, not sit blank for good.
  it("shows a returning chat loading through failed reads and mounts it once the daemon answers", async () => {
    let failing = false;
    const unavailable = () => jsonResponse({ status: 500, detail: "daemon stalled" }, 500);
    const api = createMockApiFetch([
      (request) => {
        if (request.method !== "GET") return null;
        const path = request.url.pathname;
        if (path === "/api/v1/workspaces/ws-1") return failing ? unavailable() : jsonResponse(workspaceFixture("ws-1", 7));
        if (path === "/api/v1/workspaces/ws-1/runtime") {
          return failing ? unavailable() : jsonResponse({ launch_targets: [], sessions: [chatSession] });
        }
        if (path === "/api/v1/workspaces/ws-2") return jsonResponse(workspaceFixture("ws-2", 8));
        if (path === "/api/v1/workspaces/ws-2/runtime") return jsonResponse({ launch_targets: [], sessions: [] });
        if (path === "/api/v1/workspaces") {
          return jsonResponse({ workspaces: [workspaceFixture("ws-1", 7), workspaceFixture("ws-2", 8)] });
        }
        return null;
      },
    ]);
    globalThis.fetch = api.fetch;
    let workspaceId = $state("ws-1");
    const target = document.createElement("div");
    target.style.width = "900px";
    target.style.height = "600px";
    document.body.appendChild(target);
    const instance = mount(WorkspaceTerminalView, {
      target,
      props: {
        runtime,
        get workspaceId() {
          return workspaceId;
        },
        hideWorkspaceList: true,
        hideRightSidebar: true,
      },
      context: new Map([[STORES_KEY, { events: eventsStore, settings: createSettingsStore() }]]),
    });
    const composer = page.getByRole("textbox", { name: "Message agent" });
    const loadingSession = page.getByRole("status").filter({ hasText: "Loading session…" });

    try {
      await expect.element(composer, { timeout: WAIT }).toBeInTheDocument();
      expect(chatSockets).toHaveLength(1);

      flushSync(() => {
        workspaceId = "ws-2";
      });
      await expect.element(composer, { timeout: WAIT }).not.toBeInTheDocument();
      // The released chat leaves the pool, as the retention limit would evict it.
      noteSessionDiscarded(sessionHostKey("ws-1", undefined, chatSession.key, chatSession.created_at));

      failing = true;
      flushSync(() => {
        workspaceId = "ws-1";
      });
      await expect.element(loadingSession, { timeout: WAIT }).toBeVisible();
      expect(composer.query()).toBeNull();

      failing = false;
      await expect.element(composer, { timeout: WAIT }).toBeInTheDocument();
      await expect.element(loadingSession).not.toBeInTheDocument();
      expect(chatSockets).toHaveLength(2);
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
    }
  }, 30_000);
});
