import type { ConfigRepo } from "@middleman/ui/api/types";

// Shared by AppProviderConfiguredMock.svelte and the App-level tests.
// Lives in a plain TS module (not the component's <script module>) so
// `vp check` sees the named exports — the ambient *.svelte declaration
// only exposes a component's default export.

// Call counters for the store methods runAppStartup invokes after
// onReady. Tests import these to assert the post-startup loaders ran.
export const providerCalls = {
  loadPulls: 0,
  loadIssues: 0,
  loadActivity: 0,
  startPolling: 0,
  eventsConnect: 0,
};

export function resetProviderCalls(): void {
  providerCalls.loadPulls = 0;
  providerCalls.loadIssues = 0;
  providerCalls.loadActivity = 0;
  providerCalls.startPolling = 0;
  providerCalls.eventsConnect = 0;
}

// Mirrors the repos the e2e server seeds (acme/widgets and
// acme/tools on github.com) so RepoTypeahead's option pruning
// keeps route-synced selections.
const configuredRepos: ConfigRepo[] = [
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widgets",
    repo_path: "acme/widgets",
    is_glob: false,
    matched_repo_count: 1,
  },
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "tools",
    repo_path: "acme/tools",
    is_glob: false,
    matched_repo_count: 1,
  },
];

const noop = () => {};

// Same shape as AppProviderMock's stores, but with configured repos
// so App.svelte's syncGlobalRepoWithRoute path is reachable, plus
// the sync/events/loader methods the real runAppStartup touches.
export const configuredMockStores = {
  settings: {
    hasConfiguredRepos: () => true,
    getConfiguredRepos: () => configuredRepos,
    isSettingsLoaded: () => true,
    isModeVisible: () => true,
    setConfiguredRepos: noop,
    setModeVisibility: noop,
    setTerminalSettings: noop,
  },
  grouping: {
    getGroupByRepo: () => true,
  },
  events: {
    connect: () => {
      providerCalls.eventsConnect += 1;
    },
    disconnect: noop,
  },
  sync: {
    getSyncState: () => null,
    triggerSync: () => Promise.resolve(),
    startPolling: () => {
      providerCalls.startPolling += 1;
    },
  },
  pulls: {
    getSelectedPR: () => null,
    loadPulls: () => {
      providerCalls.loadPulls += 1;
      return Promise.resolve();
    },
    selectPR: noop,
    clearSelection: noop,
  },
  issues: {
    getSelectedIssue: () => null,
    loadIssues: () => {
      providerCalls.loadIssues += 1;
      return Promise.resolve();
    },
    selectIssue: noop,
    clearIssueSelection: noop,
  },
  activity: {
    hydrateDefaults: noop,
    loadActivity: () => {
      providerCalls.loadActivity += 1;
      return Promise.resolve();
    },
  },
  detail: {
    getDetail: () => null,
  },
};
