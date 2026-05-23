import { cleanup, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  EventsStoreOptions,
} from "@middleman/ui/stores/events";
import type { SyncStatus } from "@middleman/ui/api/types";
import type { MiddlemanClient } from "@middleman/ui";

interface CapturedEventsStore {
  options: EventsStoreOptions;
  connect: ReturnType<typeof vi.fn>;
  disconnect: ReturnType<typeof vi.fn>;
  isConnected: ReturnType<typeof vi.fn>;
}

const captured: { store: CapturedEventsStore | null } = {
  store: null,
};

vi.mock("@middleman/ui/stores/events", () => ({
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

vi.mock("@middleman/ui/stores/pulls", () => ({
  createPullsStore: () => ({
    loadPulls,
    optimisticKanbanUpdate: vi.fn(),
    getPullKanbanStatus: vi.fn(),
    getPulls: () => [],
    isLoading: () => false,
  }),
}));

vi.mock("@middleman/ui/stores/issues", () => ({
  createIssuesStore: () => ({
    loadIssues,
    getIssues: () => [],
    isLoading: () => false,
  }),
}));

vi.mock("@middleman/ui/stores/activity", () => ({
  createActivityStore: () => ({
    loadActivity,
    getActivity: () => [],
    isLoading: () => false,
  }),
}));

vi.mock("@middleman/ui/stores/sync", () => ({
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

vi.mock("@middleman/ui/stores/detail", () => ({
  createDetailStore: () => ({
    loadDetail: vi.fn(),
    refreshDetailOnly,
    isDetailLoading: () => false,
    getDetail: () => currentDetail,
  }),
}));

vi.mock("@middleman/ui/stores/diff", () => ({
  createDiffStore: () => ({
    loadDiff: vi.fn(),
    getDiff: () => null,
  }),
}));

vi.mock("@middleman/ui/stores/grouping", () => ({
  createGroupingStore: () => ({
    getGroupByRepo: () => false,
    setGroupByRepo: vi.fn(),
  }),
}));

vi.mock("@middleman/ui/stores/settings", () => ({
  createSettingsStore: () => ({
    getConfiguredRepos: () => [],
    setConfiguredRepos: vi.fn(),
    getTerminalFontFamily: () => "",
    setTerminalFontFamily: vi.fn(),
    hasConfiguredRepos: () => false,
    isSettingsLoaded: () => true,
  }),
}));

import Provider from "../../packages/ui/src/Provider.svelte";

const stubClient = {
  GET: vi.fn(),
  POST: vi.fn(),
  PUT: vi.fn(),
  DELETE: vi.fn(),
} as unknown as MiddlemanClient;

beforeEach(() => {
  captured.store = null;
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
  it("passes onDataChanged that refreshes pulls, issues, and activity", () => {
    render(Provider, { props: { client: stubClient } });

    expect(captured.store).not.toBeNull();
    const assert = expect;
    const cb = captured.store?.options.onDataChanged;
    assert(cb).toBeTypeOf("function");

    cb?.();

    assert(loadPulls).toHaveBeenCalledTimes(1);
    assert(loadIssues).toHaveBeenCalledTimes(1);
    assert(loadActivity).toHaveBeenCalledTimes(1);
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
    expect(refreshDetailOnly).toHaveBeenCalledWith(
      "acme",
      "widget",
      42,
      {
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
      },
    );
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
});
