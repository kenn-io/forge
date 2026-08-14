import { describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_MODE_VISIBILITY, DEFAULT_PULL_REQUEST_SETTINGS, DEFAULT_TERMINAL_SETTINGS } from "../api/types.js";
import type { ActivitySettings } from "../api/types.js";
import type { StartupSnapshot } from "../app/startup-workflow.js";
import { applySettingsHydration } from "./settings-hydration.js";
import { createSettingsStore } from "./settings.svelte.js";
import { beginTerminalSettingsHydration } from "./terminal-settings-persistence.js";
import { beginWorkspaceSettingsHydration } from "./workspace-settings-persistence.js";

const activitySettings: ActivitySettings = {
  view_mode: "threaded",
  time_range: "7d",
  hide_closed: false,
  hide_bots: false,
  collapse_threads: false,
  default_branch_retention_days: 90,
  default_branch_max_commits: 5000,
};

const codexTarget = {
  key: "codex",
  label: "Codex",
  kind: "agent",
  source: "builtin",
  command: ["codex"],
  available: true,
  disabled_reason: "",
};

const settingsPayload = {
  repos: [],
  repo_presets: [
    {
      name: "Review queue",
      repos: [
        { provider: "github", platform_host: "github.com", platform_repo_id: "R_widgets", repo_path: "acme/widgets" },
      ],
    },
  ],
  activity: activitySettings,
  issues: { hide_bots: true },
  terminal: DEFAULT_TERMINAL_SETTINGS,
  modes: DEFAULT_MODE_VISIBILITY,
  pull_requests: DEFAULT_PULL_REQUEST_SETTINGS,
  launch_targets: [codexTarget],
  agents: [],
  fleet: {
    enabled: false,
    sessions: {},
    peers: [],
    ssh_peers: [],
    restart_required: false,
  },
  kata_projects: [],
  notifications: { enabled: true },
  workspaces: { auto_assign_on_create: false, default_sidebar_view: "item" },
} satisfies StartupSnapshot;

function hydrate(
  launchTargets = [codexTarget],
  activity = { hydrateDefaults: vi.fn() },
  issues = { hydrateDefaults: vi.fn() },
) {
  const settingsStore = createSettingsStore();
  const terminalHydration = beginTerminalSettingsHydration(settingsStore);
  const workspaceHydration = beginWorkspaceSettingsHydration(settingsStore);
  applySettingsHydration(
    { settings: settingsStore, activity, issues },
    { ...settingsPayload, launch_targets: launchTargets },
    terminalHydration,
    workspaceHydration,
  );
  return { settingsStore, activity, issues };
}

describe("applySettingsHydration", () => {
  it("hydrates repository presets into the settings store", () => {
    const { settingsStore } = hydrate();
    expect(settingsStore.getRepoPresets()).toEqual(settingsPayload.repo_presets);
  });

  it("hydrates launch targets into the settings store", () => {
    const { settingsStore } = hydrate();
    expect(settingsStore.getLaunchTargets()).toEqual([codexTarget]);
  });

  it("hydrates workspace preferences into the settings store", () => {
    const { settingsStore } = hydrate();
    expect(settingsStore.getWorkspaceSettings()).toEqual(settingsPayload.workspaces);
  });

  it("clears stale launch targets when settings reports an empty inventory", () => {
    const settingsStore = createSettingsStore();
    settingsStore.setLaunchTargets([codexTarget]);
    const terminalHydration = beginTerminalSettingsHydration(settingsStore);
    const workspaceHydration = beginWorkspaceSettingsHydration(settingsStore);
    applySettingsHydration(
      { settings: settingsStore, activity: { hydrateDefaults: vi.fn() }, issues: { hydrateDefaults: vi.fn() } },
      { ...settingsPayload, launch_targets: [] },
      terminalHydration,
      workspaceHydration,
    );
    expect(settingsStore.getLaunchTargets()).toEqual([]);
  });

  it("also hydrates activity and issue defaults from the same payload", () => {
    const { activity, issues } = hydrate();
    expect(activity.hydrateDefaults).toHaveBeenCalledWith(activitySettings);
    expect(issues.hydrateDefaults).toHaveBeenCalledWith({ hide_bots: true });
  });
});
