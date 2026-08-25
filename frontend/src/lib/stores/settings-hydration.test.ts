import { describe, expect, it, vi } from "vite-plus/test";
import type { ActivitySettings } from "../api/types.js";
import { makeStartupSnapshot } from "../../test/startupSnapshot.js";
import { applySettingsHydration } from "./settings-hydration.js";
import { createSettingsStore } from "./settings.svelte.js";
import { beginTerminalSettingsHydration } from "./terminal-settings-persistence.js";
import { beginWorkspaceSettingsHydration } from "./workspace-settings-persistence.js";
import { beginRoborevSettingsHydration } from "./roborev-settings-persistence.js";

const activitySettings: ActivitySettings = {
  view_mode: "threaded",
  time_range: "7d",
  hide_closed: false,
  hide_bots: false,
  collapse_threads: false,
  default_branch_retention_days: 90,
  default_branch_max_commits: 5000,
  use_workspace_activity_for_recency: false,
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

const settingsPayload = makeStartupSnapshot({
  repo_presets: [
    {
      name: "Review queue",
      repos: [
        { provider: "github", platform_host: "github.com", platform_repo_id: "R_widgets", repo_path: "acme/widgets" },
      ],
    },
  ],
  activity: activitySettings,
  detail: { initial_timeline_entry_limit: 75 },
  launch_targets: [codexTarget],
  workspaces: { auto_assign_on_create: false, default_sidebar_view: "item" },
  roborev: { init_managed_clones: true },
});

function hydrate(
  launchTargets = [codexTarget],
  activity = { hydrateDefaults: vi.fn() },
  issues = { hydrateDefaults: vi.fn() },
) {
  const settingsStore = createSettingsStore();
  const terminalHydration = beginTerminalSettingsHydration(settingsStore);
  const workspaceHydration = beginWorkspaceSettingsHydration(settingsStore);
  const roborevHydration = beginRoborevSettingsHydration(settingsStore);
  applySettingsHydration(
    { settings: settingsStore, activity, issues },
    { ...settingsPayload, launch_targets: launchTargets },
    terminalHydration,
    workspaceHydration,
    roborevHydration,
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

  it("hydrates the managed-clone Roborev preference into its own store value", () => {
    const { settingsStore } = hydrate();
    expect(settingsStore.getRoborevSettings()).toEqual(settingsPayload.roborev);
  });

  it("hydrates the detail timeline limit into the settings store", () => {
    const { settingsStore } = hydrate();
    expect(settingsStore.getDetailSettings()).toEqual({ initial_timeline_entry_limit: 75 });
  });

  it("clears stale launch targets when settings reports an empty inventory", () => {
    const settingsStore = createSettingsStore();
    settingsStore.setLaunchTargets([codexTarget]);
    const terminalHydration = beginTerminalSettingsHydration(settingsStore);
    const workspaceHydration = beginWorkspaceSettingsHydration(settingsStore);
    const roborevHydration = beginRoborevSettingsHydration(settingsStore);
    applySettingsHydration(
      { settings: settingsStore, activity: { hydrateDefaults: vi.fn() }, issues: { hydrateDefaults: vi.fn() } },
      { ...settingsPayload, launch_targets: [] },
      terminalHydration,
      workspaceHydration,
      roborevHydration,
    );
    expect(settingsStore.getLaunchTargets()).toEqual([]);
  });

  it("also hydrates activity and issue defaults from the same payload", () => {
    const { activity, issues } = hydrate();
    expect(activity.hydrateDefaults).toHaveBeenCalledWith(activitySettings);
    expect(issues.hydrateDefaults).toHaveBeenCalledWith({ hide_bots: true });
  });
});
