import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, type MockRouteOverride } from "./test/mockApiFetch.js";

const workspace = {
  id: "ws-123",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widgets",
  repo: {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
  },
  item_type: "pull_request",
  item_number: 42,
  git_head_ref: "feature/auth",
  worktree_path: "/tmp/worktrees/ws-123",
  tmux_session: "middleman-ws-123",
  tmux_pane_title: null,
  tmux_working: false,
  status: "ready",
  created_at: "2026-04-10T12:00:00Z",
  mr_title: "Add auth middleware",
  mr_state: "open",
  mr_is_draft: false,
};

const runtime = {
  launch_targets: [
    {
      key: "codex",
      label: "Codex",
      kind: "agent",
      source: "builtin",
      command: ["codex"],
      available: true,
    },
    {
      key: "missing",
      label: "Missing",
      kind: "agent",
      source: "builtin",
      command: ["missing"],
      available: false,
      disabled_reason: "missing not found on PATH",
    },
    {
      key: "disabled_config",
      label: "Disabled config",
      kind: "agent",
      source: "config",
      command: ["disabled"],
      available: false,
      disabled_reason: "disabled by config",
    },
    {
      key: "plain_shell",
      label: "Plain shell",
      kind: "plain_shell",
      source: "system",
      command: ["/bin/sh"],
      available: true,
    },
  ],
  sessions: [],
};

const workspaceRoutes: MockRouteOverride = (req) => {
  if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces") {
    return jsonResponse({ workspaces: [workspace] });
  }
  if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces/ws-123") {
    return jsonResponse(workspace);
  }
  if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces/ws-123/runtime") {
    return jsonResponse(runtime);
  }
  if (
    req.method === "GET" &&
    (req.url.pathname === "/api/v1/workspaces/ws-123/files" || req.url.pathname === "/api/v1/workspaces/ws-123/diff")
  ) {
    return jsonResponse({
      stale: false,
      whitespace_only_count: 0,
      files: [],
    });
  }
  if (req.method === "GET" && req.url.pathname === "/api/v1/workspaces/ws-123/commits") {
    return jsonResponse({ commits: [] });
  }
  return null;
};

let mounted: MountedBrowserApp | null = null;

describe("workspace launch targets (browser)", () => {
  vi.setConfig({ testTimeout: 20_000 });

  beforeEach(async () => {
    mounted = null;
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("hides disabled configured targets from home cards and the toolbar menu", async () => {
    mounted = await mountBrowserApp("/terminal/ws-123", {
      overrides: [workspaceRoutes],
    });

    await vi.waitFor(
      () => {
        expect(page.getByRole("region", { name: "Worktree Home" }).element()).toBeTruthy();
      },
      { timeout: 10_000, interval: 50 },
    );

    const home = page.getByRole("region", { name: "Worktree Home" });
    const homeEl = home.element();
    expect(home.getByRole("button", { name: "Codex" }).element()).toBeTruthy();
    expect(home.getByRole("button", { name: "Missing" }).element().hasAttribute("disabled")).toBe(true);
    expect(home.getByRole("button", { name: "Shell" }).element()).toBeTruthy();
    expect(homeEl.textContent).not.toContain("Disabled config");

    await page.getByRole("button", { name: "Launch" }).click();

    const menu = document.querySelector(".launch-popover");
    expect(menu).not.toBeNull();
    const options = Array.from(menu!.querySelectorAll("button"));
    const optionText = (button: HTMLButtonElement) => button.textContent ?? "";
    const missingOption = options.find((button) => optionText(button).includes("Missing"));
    expect(options.some((button) => optionText(button).includes("Codex"))).toBe(true);
    expect(missingOption?.hasAttribute("disabled")).toBe(true);
    expect(options.some((button) => optionText(button).includes("Shell"))).toBe(true);
    expect(menu!.textContent).not.toContain("Disabled config");
  });
});
