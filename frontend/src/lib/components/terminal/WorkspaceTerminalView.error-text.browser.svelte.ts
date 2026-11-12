import { page } from "vite-plus/test/browser";
import { flushSync, mount, unmount } from "svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";
import "../../../app.css";

import { STORES_KEY } from "../../context.js";
import { createMockApiFetch, jsonResponse } from "../../../test/mockApiFetch.js";
import WorkspaceTerminalView from "./WorkspaceTerminalViewTestHarness.svelte";

const WAIT = 10_000;
const eventsStore = {
  selectWorkspace: () => () => undefined,
  subscribeWorkspaceEvents: () => () => undefined,
};

// A dirty-worktree refusal names every changed path, so a large branch makes
// the stored error thousands of characters long.
const longError =
  "workspace has uncommitted changes: " +
  Array.from({ length: 400 }, (_, i) => `internal/pkg${i}/file${i}_test.go`).join(", ");

const failedWorkspace = {
  id: "ws-1",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widget",
  repo: { provider: "github", platform_host: "github.com", owner: "acme", name: "widget", repo_path: "acme/widget" },
  item_type: "pull_request",
  item_number: 7,
  source_item_visible: true,
  git_head_ref: "feature/ws-1",
  worktree_path: "/tmp/ws-1",
  tmux_session: "kenn-forge-ws-1",
  status: "deletion_failed",
  error_message: longError,
  enrichment_status: "fresh",
  tmux_activity_source: "none",
  tmux_last_output_at: null,
  tmux_working: false,
  created_at: "2026-04-29T00:00:00Z",
};

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

describe("WorkspaceTerminalView error text", () => {
  let runtime: OwnedAppRuntime;
  const originalFetch = globalThis.fetch;
  const originalEventSource = globalThis.EventSource;

  beforeEach(async () => {
    await page.viewport(1200, 800);
    runtime = makeAppRuntime();
    globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;
  });

  afterEach(async () => {
    globalThis.fetch = originalFetch;
    globalThis.EventSource = originalEventSource;
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("scrolls a long deletion error inside the pane and keeps force delete on screen", async () => {
    globalThis.fetch = createMockApiFetch([
      (request) => {
        if (request.method !== "GET") return null;
        const path = request.url.pathname;
        if (path === "/api/v1/workspaces/ws-1") return jsonResponse(failedWorkspace);
        if (path === "/api/v1/workspaces/ws-1/runtime") return jsonResponse({ launch_targets: [], sessions: [] });
        if (path === "/api/v1/workspaces") return jsonResponse({ workspaces: [failedWorkspace] });
        return null;
      },
    ]).fetch;
    const target = document.createElement("div");
    target.style.width = "900px";
    target.style.height = "600px";
    target.style.display = "flex";
    document.body.appendChild(target);
    const instance = mount(WorkspaceTerminalView, {
      target,
      props: { runtime, workspaceId: "ws-1", hideWorkspaceList: true, hideRightSidebar: true },
      context: new Map([[STORES_KEY, { events: eventsStore, settings: createSettingsStore() }]]),
    });

    try {
      const forceDelete = page.getByRole("button", { name: "Force delete workspace" });
      await expect.element(forceDelete, { timeout: WAIT }).toBeVisible();
      const text = page.getByText(longError).element() as HTMLElement;

      const box = target.getBoundingClientRect();
      const textBox = text.getBoundingClientRect();
      expect(textBox.height).toBeLessThan(box.height / 2);
      expect(text.scrollHeight).toBeGreaterThan(text.clientHeight);

      const buttonBox = forceDelete.element().getBoundingClientRect();
      expect(buttonBox.top).toBeGreaterThanOrEqual(box.top);
      expect(buttonBox.bottom).toBeLessThanOrEqual(box.bottom);
      expect(buttonBox.right).toBeLessThanOrEqual(box.right);
    } finally {
      flushSync(() => unmount(instance));
      target.remove();
    }
  }, 30_000);
});
