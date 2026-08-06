import { cleanup, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { EventsStoreOptions } from "@kenn-forge/ui/stores/events";
import type { Settings, SyncStatus } from "@kenn-forge/ui/api/types";
import type { ForgeClient } from "@kenn-forge/ui";

type LaunchTargets = NonNullable<Settings["launch_targets"]>;

interface CapturedEventsStore {
  options: EventsStoreOptions;
  connect: ReturnType<typeof vi.fn>;
  disconnect: ReturnType<typeof vi.fn>;
  isConnected: ReturnType<typeof vi.fn>;
}

interface CapturedSettingsStore {
  getLaunchTargets: () => LaunchTargets;
  setLaunchTargets: (targets: LaunchTargets) => void;
}

const captured: {
  store: CapturedEventsStore | null;
  settings: CapturedSettingsStore | null;
} = {
  store: null,
  settings: null,
};

vi.mock("@kenn-forge/ui/stores/events", () => ({
  createEventsStore: (opts: EventsStoreOptions) => {
    const store: CapturedEventsStore = {
      options: opts,
      connect: vi.fn(),
      disconnect: vi.fn(),
      isConnected: vi.fn(() => false),
    };
    captured.store = store;
    return store;
  },
}));

const loadPulls = vi.fn(async () => undefined);
const loadIssues = vi.fn(async () => undefined);
const loadActivity = vi.fn(async () => undefined);
const setSyncStatus = vi.fn();
const refreshDetailOnly = vi.fn(async () => undefined);
let currentDetail: unknown = null;

vi.mock("@kenn-forge/ui/stores/pulls", () => ({
  createPullsStore: () => ({
    loadPulls,
    optimisticKanbanUpdate: vi.fn(),
    getPullKanbanStatus: vi.fn(),
    getPulls: () => [],
    isLoading: () => false,
  }),
}));

vi.mock("@kenn-forge/ui/stores/issues", () => ({
  createIssuesStore: () => ({
    loadIssues,
    hydrateDefaults: vi.fn(),
    getIssues: () => [],
    isLoading: () => false,
  }),
}));

vi.mock("@kenn-forge/ui/stores/activity", () => ({
  createActivityStore: () => ({
    loadActivity,
    hydrateDefaults: vi.fn(),
    getActivity: () => [],
    isLoading: () => false,
  }),
}));

vi.mock("@kenn-forge/ui/stores/sync", () => ({
  createSyncStore: () => ({
    getSyncState: () => null,
    onNextSyncComplete: vi.fn(),
    subscribeSyncComplete: vi.fn(() => () => undefined),
    refreshSyncStatus: vi.fn(async () => undefined),
    setSyncStatus,
    triggerSync: vi.fn(async () => undefined),
    startPolling: vi.fn(),
    stopPolling: vi.fn(),
  }),
}));

vi.mock("@kenn-forge/ui/stores/detail", () => ({
  createDetailStore: () => ({
    loadDetail: vi.fn(),
    refreshDetailOnly,
    isDetailLoading: () => false,
    getDetail: () => currentDetail,
  }),
}));

vi.mock("@kenn-forge/ui/stores/diff", () => ({
  createDiffStore: () => ({
    loadDiff: vi.fn(),
    getDiff: () => null,
  }),
}));

vi.mock("@kenn-forge/ui/stores/grouping", () => ({
  createGroupingStore: () => ({
    getGroupByRepo: () => false,
    setGroupByRepo: vi.fn(),
  }),
}));

vi.mock("@kenn-forge/ui/stores/settings", () => ({
  createSettingsStore: () => {
    let launchTargets: LaunchTargets = [];
    const store = {
      getConfiguredRepos: () => [],
      setConfiguredRepos: vi.fn(),
      getPullRequestSettings: () => ({
        allow_mid_stack_merges: false,
        prefer_github_native_stacks: false,
      }),
      setPullRequestSettings: vi.fn(),
      getModeVisibility: () => ({
        activity: true,
        repos: true,
        kata: false,
        docs: false,
        pulls: true,
        issues: true,
        reviews: true,
        workspaces: true,
      }),
      setModeVisibility: vi.fn(),
      isModeVisible: vi.fn(() => true),
      getTerminalSettings: () => ({
        font_family: "",
        font_size: 12,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: false,
        font_ligatures: false,
        hide_tmux_status: false,
      }),
      setTerminalSettings: vi.fn(),
      getTerminalFontFamily: () => "",
      setTerminalFontFamily: vi.fn(),
      getLaunchTargets: () => launchTargets,
      setLaunchTargets: vi.fn((targets: LaunchTargets) => {
        launchTargets = [...targets];
      }),
      hasConfiguredRepos: () => false,
      isSettingsLoaded: () => true,
    };
    captured.settings = store;
    return store;
  },
}));

import Provider from "../../packages/ui/src/Provider.svelte";

const getSettings = vi.fn();
const stubClient = {
  GET: getSettings,
  POST: vi.fn(),
  PUT: vi.fn(),
  DELETE: vi.fn(),
} as unknown as ForgeClient;

beforeEach(() => {
  captured.store = null;
  captured.settings = null;
  getSettings.mockReset();
  loadPulls.mockClear();
  loadIssues.mockClear();
  loadActivity.mockClear();
  setSyncStatus.mockClear();
  refreshDetailOnly.mockClear();
  currentDetail = null;
});

afterEach(() => {
  cleanup();
});

describe("Provider events store wiring", () => {
  it("replaces stale launch targets after a valid config reload", async () => {
    const codexTarget = {
      key: "codex",
      label: "Codex",
      kind: "agent",
      source: "builtin",
      command: ["codex"],
      available: true,
      disabled_reason: "",
    } satisfies LaunchTargets[number];
    const staleTarget = { ...codexTarget, key: "claude", label: "Claude", command: ["claude"] };
    const settings = {
      repos: [],
      activity: {
        view_mode: "threaded",
        time_range: "7d",
        hide_closed: false,
        hide_bots: false,
        collapse_threads: false,
        default_branch_retention_days: 90,
        default_branch_max_commits: 5000,
      },
      issues: { hide_bots: true },
      terminal: {
        font_family: "",
        font_size: 12,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: false,
        font_ligatures: false,
        hide_tmux_status: false,
      },
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
      pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
      launch_targets: [codexTarget],
    } satisfies Pick<
      Settings,
      "repos" | "activity" | "issues" | "terminal" | "modes" | "pull_requests" | "launch_targets"
    >;
    getSettings.mockResolvedValue({ data: settings });

    render(Provider, { props: { client: stubClient } });
    captured.settings?.setLaunchTargets([staleTarget]);

    captured.store?.options.onConfigChanged?.({ valid: true, restart_required: false });

    await vi.waitFor(() => {
      expect(captured.settings?.getLaunchTargets()).toEqual([codexTarget]);
    });
  });

  it.each([
    { route: "pulls", pulls: 1, issues: 0, activity: 0 },
    { route: "mobile-pulls", pulls: 1, issues: 0, activity: 0 },
    { route: "issues", pulls: 0, issues: 1, activity: 0 },
    { route: "mobile-issues", pulls: 0, issues: 1, activity: 0 },
    { route: "activity", pulls: 0, issues: 0, activity: 1 },
    { route: "mobile-activity", pulls: 0, issues: 0, activity: 1 },
    { route: "focus", pulls: 1, issues: 1, activity: 0 },
    { route: "terminal", pulls: 0, issues: 0, activity: 0 },
    { route: "workspaces", pulls: 0, issues: 0, activity: 0 },
  ])("refreshes only the stores visible on the $route route", ({ route, pulls, issues, activity }) => {
    render(Provider, {
      props: { client: stubClient, getPage: () => route },
    });

    expect(captured.store).not.toBeNull();
    const cb = captured.store?.options.onDataChanged;
    expect(cb).toBeTypeOf("function");

    cb?.();

    expect(loadPulls).toHaveBeenCalledTimes(pulls);
    expect(loadIssues).toHaveBeenCalledTimes(issues);
    expect(loadActivity).toHaveBeenCalledTimes(activity);
  });

  it("refreshes the Activity drawer selection instead of stale displayed detail", () => {
    currentDetail = {
      repo: {
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/old-widget",
      },
      repo_owner: "acme",
      repo_name: "old-widget",
      merge_request: { Number: 41 },
    };
    render(Provider, {
      props: {
        client: stubClient,
        getPage: () => "activity",
        getActivitySelection: () => ({
          itemType: "pr",
          provider: "gitlab",
          platformHost: "gitlab.example.com",
          repoPath: "group/widget",
          owner: "group",
          name: "widget",
          number: 42,
        }),
      },
    });

    captured.store?.options.onDataChanged?.();

    expect(loadActivity).toHaveBeenCalledTimes(1);
    expect(refreshDetailOnly).toHaveBeenCalledTimes(1);
    expect(refreshDetailOnly).toHaveBeenCalledWith("group", "widget", 42, {
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      repoPath: "group/widget",
    });
  });

  it("refreshes the selected Activity PR after a stale reconnect", () => {
    render(Provider, {
      props: {
        client: stubClient,
        getPage: () => "activity",
        getActivitySelection: () => ({
          itemType: "pr",
          provider: "github",
          platformHost: "github.com",
          repoPath: "acme/widget",
          owner: "acme",
          name: "widget",
          number: 42,
        }),
      },
    });

    captured.store?.options.onReconnectStale?.();

    expect(loadPulls).toHaveBeenCalledTimes(1);
    expect(loadIssues).toHaveBeenCalledTimes(1);
    expect(loadActivity).toHaveBeenCalledTimes(1);
    expect(refreshDetailOnly).toHaveBeenCalledTimes(1);
    expect(refreshDetailOnly).toHaveBeenCalledWith("acme", "widget", 42, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    });
  });

  it("passes onSyncStatus that pushes the received status into sync store", () => {
    render(Provider, { props: { client: stubClient } });

    const cb = captured.store?.options.onSyncStatus;
    expect(cb).toBeTypeOf("function");

    const status: SyncStatus = {
      running: true,
      last_run_at: "2026-04-08T12:00:00Z",
      last_error: "",
    };
    cb?.(status);

    expect(setSyncStatus).toHaveBeenCalledTimes(1);
    expect(setSyncStatus).toHaveBeenCalledWith(status);
  });

  it("refreshes only the visible PR detail for matching targeted refresh events", () => {
    currentDetail = {
      repo: {
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widget",
      },
      repo_owner: "acme",
      repo_name: "widget",
      merge_request: { Number: 42 },
    };
    render(Provider, { props: { client: stubClient } });

    captured.store?.options.onPRDetailRefreshed?.({
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widget",
      owner: "acme",
      name: "widget",
      number: 42,
      head_sha: "2222222",
      synced_at: "2026-05-20T14:15:04Z",
      warnings: [],
    });

    expect(refreshDetailOnly).toHaveBeenCalledTimes(1);
    expect(refreshDetailOnly).toHaveBeenCalledWith("acme", "widget", 42, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    });
  });

  it("ignores targeted PR refreshes while an issue detail is visible", () => {
    currentDetail = {
      repo: {
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widget",
      },
      repo_owner: "acme",
      repo_name: "widget",
      issue: { Number: 7 },
    };
    render(Provider, { props: { client: stubClient } });

    expect(() =>
      captured.store?.options.onPRDetailRefreshed?.({
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widget",
        owner: "acme",
        name: "widget",
        number: 42,
        head_sha: "2222222",
        synced_at: "2026-05-20T14:15:04Z",
        warnings: [],
      }),
    ).not.toThrow();
    expect(() =>
      captured.store?.options.onPRCIRefreshed?.({
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widget",
        owner: "acme",
        name: "widget",
        number: 42,
        head_sha: "2222222",
        refreshed_at: "2026-05-20T14:15:20Z",
        warnings: [],
      }),
    ).not.toThrow();
    expect(refreshDetailOnly).not.toHaveBeenCalled();
  });

  it("ignores targeted PR detail refreshes for non-visible PRs", () => {
    currentDetail = {
      repo: {
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widget",
      },
      repo_owner: "acme",
      repo_name: "widget",
      merge_request: { Number: 42 },
    };
    render(Provider, { props: { client: stubClient } });

    captured.store?.options.onPRDetailRefreshed?.({
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widget",
      owner: "acme",
      name: "widget",
      number: 99,
      head_sha: "2222222",
      synced_at: "2026-05-20T14:15:04Z",
      warnings: [],
    });

    expect(refreshDetailOnly).not.toHaveBeenCalled();
  });

  it("forwards basePath getter when config.basePath is set", () => {
    render(Provider, {
      props: {
        client: stubClient,
        config: { basePath: "/prefix" },
      },
    });

    const getBasePath = captured.store?.options.getBasePath;
    expect(getBasePath).toBeTypeOf("function");
    expect(getBasePath?.()).toBe("/prefix");
  });

  it("omits getBasePath when config has no basePath", () => {
    render(Provider, { props: { client: stubClient } });
    expect(captured.store?.options.getBasePath).toBeUndefined();
  });

  it("routes deferred merge failures only through the error callback", () => {
    const onError = vi.fn();
    const onNotification = vi.fn();
    render(Provider, { props: { client: stubClient, onError, onNotification } });

    captured.store?.options.onDeferredMergeCompleted?.({
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widget",
      owner: "acme",
      name: "widget",
      number: 42,
      head_sha: "2222222",
      status: "failed",
      error: "checks did not pass",
      completed_at: "2026-07-10T15:00:00Z",
    });

    expect(onError).toHaveBeenCalledWith("Deferred merge for acme/widget#42 failed: checks did not pass");
    expect(onNotification).not.toHaveBeenCalled();
  });

  it("routes merged workspace cleanup failures through the warning callback", () => {
    const onWarning = vi.fn();
    const onNotification = vi.fn();
    render(Provider, { props: { client: stubClient, onWarning, onNotification } });

    captured.store?.options.onDeferredMergeCompleted?.({
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widget",
      owner: "acme",
      name: "widget",
      number: 42,
      head_sha: "2222222",
      status: "merged",
      merged: true,
      completed_at: "2026-07-10T15:00:00Z",
      workspace_cleanup_warning: "workspace has uncommitted changes",
    });

    expect(onWarning).toHaveBeenCalledWith(
      "acme/widget#42 merged, but the workspace was not pruned: workspace has uncommitted changes",
    );
    expect(onNotification).not.toHaveBeenCalled();
  });
});
