import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { StartupSnapshot } from "../../app/startup-workflow.js";
import type { OwnedAppRuntime } from "../../app/runtime.js";
import { makeAppRuntime } from "../../app/runtime.js";
import { createAppStores } from "../../app-stores.svelte.js";
import { DEFAULT_TERMINAL_SETTINGS } from "../../api/types.js";
import { STORES_KEY } from "../../context.js";
import { replaceUrl } from "../../stores/router.svelte.js";

const mocks = vi.hoisted(() => ({
  runtime: undefined as unknown as OwnedAppRuntime,
  settingsRequested: () => {},
  settingsSignals: [] as AbortSignal[],
  showFlash: vi.fn(),
}));

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => mocks.runtime,
}));

vi.mock("../../stores/flash.svelte.js", () => ({
  showFlash: mocks.showFlash,
}));

import WorkspaceEmbedShell from "./WorkspaceEmbedShell.svelte";

function renderShell() {
  const stores = createAppStores({ runtime: mocks.runtime }).stores;
  return render(WorkspaceEmbedShell, { context: new Map([[STORES_KEY, stores]]) });
}

const settings = {
  activity: {
    view_mode: "threaded",
    time_range: "7d",
    hide_closed: false,
    hide_bots: false,
    collapse_threads: false,
    default_branch_retention_days: 90,
    default_branch_max_commits: 5000,
    use_workspace_activity_for_recency: false,
  },
  agents: [],
  fleet: {
    enabled: false,
    sessions: {},
    peers: [],
    ssh_peers: [],
    restart_required: false,
  },
  issues: { hide_bots: true },
  kata_projects: [],
  launch_targets: [],
  modes: {
    activity: true,
    repos: true,
    kata: false,
    docs: false,
    pulls: true,
    issues: true,
    reviews: true,
    workspaces: true,
  },
  notifications: { enabled: true },
  pull_requests: {
    allow_mid_stack_merges: false,
    prefer_github_native_stacks: false,
  },
  repos: [],
  terminal: {
    ...DEFAULT_TERMINAL_SETTINGS,
    font_size: 14,
  },
  workspaces: { auto_assign_on_create: false, default_sidebar_view: "diff" },
} satisfies StartupSnapshot;

describe("WorkspaceEmbedShell settings requests", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.runtime = makeAppRuntime();
    mocks.settingsRequested = () => {};
    mocks.settingsSignals = [];
    mocks.showFlash.mockReset();
    const fetch: typeof globalThis.fetch = (input, init) => {
      const request = input instanceof Request ? input : new Request(input, init);
      if (new URL(request.url).pathname.endsWith("/healthz")) {
        return Promise.resolve(Response.json({ ok: true }));
      }
      if (!new URL(request.url).pathname.endsWith("/api/v1/settings")) {
        return Promise.resolve(Response.json({}));
      }
      mocks.settingsSignals.push(request.signal);
      mocks.settingsRequested();
      return new Promise((_resolve, reject) => {
        request.signal.addEventListener("abort", () => reject(request.signal.reason), { once: true });
      });
    };
    vi.stubGlobal("fetch", fetch);
    replaceUrl("/workspaces/embed/terminal/ws-1");
  });

  afterEach(async () => {
    cleanup();
    await Effect.runPromise(mocks.runtime.disposeEffect);
    vi.unstubAllGlobals();
    vi.useRealTimers();
    replaceUrl("/");
  });

  it("aborts settings requests on timeout, retry, and unmount", async () => {
    const firstRequest = new Promise<void>((resolve) => {
      mocks.settingsRequested = resolve;
    });
    const mounted = renderShell();
    await firstRequest;
    const firstSignal = mocks.settingsSignals[0];

    await vi.advanceTimersByTimeAsync(8_000);
    await waitFor(() => expect(screen.getByRole("button", { name: "Retry terminal settings" })).toBeTruthy());
    expect(firstSignal?.aborted).toBe(true);

    const secondRequest = new Promise<void>((resolve) => {
      mocks.settingsRequested = resolve;
    });
    await fireEvent.click(screen.getByRole("button", { name: "Retry terminal settings" }));
    await secondRequest;
    const secondSignal = mocks.settingsSignals[1];
    expect(secondSignal?.aborted).toBe(false);

    mounted.unmount();
    await vi.waitFor(() => expect(secondSignal?.aborted).toBe(true));
  });

  it("loads terminal settings once when hydration succeeds", async () => {
    const fetch: typeof globalThis.fetch = (input, init) => {
      const request = input instanceof Request ? input : new Request(input, init);
      if (new URL(request.url).pathname.endsWith("/healthz")) {
        return Promise.resolve(Response.json({ ok: true }));
      }
      if (!new URL(request.url).pathname.endsWith("/api/v1/settings")) {
        return Promise.resolve(Response.json({}));
      }
      mocks.settingsSignals.push(request.signal);
      mocks.settingsRequested();
      return Promise.resolve(Response.json(settings));
    };
    vi.stubGlobal("fetch", fetch);
    replaceUrl("/workspaces/embed/empty/noSelection");

    const settingsRequest = new Promise<void>((resolve) => {
      mocks.settingsRequested = resolve;
    });
    renderShell();
    await settingsRequest;

    expect(mocks.settingsSignals).toHaveLength(1);
  });
});
