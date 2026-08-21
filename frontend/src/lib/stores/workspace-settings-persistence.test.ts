import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { Settings } from "../api/types.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import type { StartupSnapshot } from "../app/startup-workflow.js";
import { createSettingsStore } from "./settings.svelte.js";
import {
  beginWorkspaceSettingsHydration,
  hydrateWorkspaceSettings,
  saveWorkspaceSettings,
} from "./workspace-settings-persistence.js";

type WorkspaceSettings = Settings["workspaces"];

const initial: WorkspaceSettings = {
  auto_assign_on_create: false,
  default_sidebar_view: "diff",
};

function settingsResponse(workspaces: WorkspaceSettings): StartupSnapshot {
  return {
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
    fleet: { enabled: false, sessions: {}, peers: [], ssh_peers: [], restart_required: false },
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
    pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
    repos: [],
    terminal: {
      font_family: "",
      font_size: 14,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      hide_tmux_status: false,
      graphics: true,
      tmux_mouse: true,
      retained_sessions: 10,
    },
    workspaces,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("workspace settings persistence", () => {
  let runtime: OwnedAppRuntime;
  let persisted: WorkspaceSettings;
  let requests: Partial<WorkspaceSettings>[];
  let holdFirstSave: ReturnType<typeof deferred<void>> | null;

  beforeEach(() => {
    runtime = makeAppRuntime();
    persisted = { ...initial };
    requests = [];
    holdFirstSave = null;
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
      if (request.method === "GET") return Response.json(settingsResponse(persisted));
      const body = (await request.clone().json()) as { workspaces: Partial<WorkspaceSettings> };
      requests.push(body.workspaces);
      if (requests.length === 1 && holdFirstSave) await holdFirstSave.promise;
      persisted = { ...persisted, ...body.workspaces };
      return Response.json(settingsResponse(persisted));
    });
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
    vi.unstubAllGlobals();
  });

  function runSave(
    store: ReturnType<typeof createSettingsStore>,
    changes: Partial<WorkspaceSettings>,
    baseline = store.getWorkspaceSettings(),
  ) {
    const execution = runtime.runCommand(saveWorkspaceSettings({ store, changes, baseline }), {
      operation: "test workspace settings persistence",
      safeContext: {},
      onFailure: () => undefined,
    });
    return Effect.runPromise(execution.await.pipe(Effect.flatMap((exit) => exit)));
  }

  it("keeps a confirmed save when an older hydration finishes later", async () => {
    const store = createSettingsStore();
    const hydration = beginWorkspaceSettingsHydration(store);

    await runSave(store, { default_sidebar_view: "item" });
    hydrateWorkspaceSettings(hydration, initial);

    expect(store.getWorkspaceSettings()).toEqual({ ...initial, default_sidebar_view: "item" });
  });

  it("rebases queued field changes onto the latest confirmed workspace settings", async () => {
    const store = createSettingsStore();
    holdFirstSave = deferred<void>();

    const sidebarSave = runSave(store, { default_sidebar_view: "item" });
    await vi.waitFor(() => expect(requests).toHaveLength(1));
    const assignmentSave = runSave(store, { auto_assign_on_create: true });
    holdFirstSave.resolve();
    await Promise.all([sidebarSave, assignmentSave]);

    expect(requests).toEqual([{ default_sidebar_view: "item" }, { auto_assign_on_create: true }]);
    expect(store.getWorkspaceSettings()).toEqual({
      auto_assign_on_create: true,
      default_sidebar_view: "item",
    });
  });

  it("keeps a hydrated sibling field when an older save response finishes later", async () => {
    const store = createSettingsStore();
    holdFirstSave = deferred<void>();

    const sidebarSave = runSave(store, { default_sidebar_view: "item" });
    await vi.waitFor(() => expect(requests).toHaveLength(1));

    persisted = { auto_assign_on_create: true, default_sidebar_view: "diff" };
    const hydration = beginWorkspaceSettingsHydration(store);
    hydrateWorkspaceSettings(hydration, persisted);

    holdFirstSave.resolve();
    await sidebarSave;

    expect(store.getWorkspaceSettings()).toEqual({
      auto_assign_on_create: true,
      default_sidebar_view: "item",
    });
    expect(persisted).toEqual({
      auto_assign_on_create: true,
      default_sidebar_view: "item",
    });
    expect(requests).toEqual([{ default_sidebar_view: "item" }]);
  });
});
