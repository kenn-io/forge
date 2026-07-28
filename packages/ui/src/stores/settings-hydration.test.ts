import { describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_MODE_VISIBILITY, DEFAULT_PULL_REQUEST_SETTINGS, DEFAULT_TERMINAL_SETTINGS } from "../api/types.js";
import type { ActivitySettings } from "../api/types.js";
import { applySettingsHydration } from "./settings-hydration.js";
import { createSettingsStore } from "./settings.svelte.js";
import { beginTerminalSettingsHydration } from "./terminal-settings-persistence.js";

const activitySettings: ActivitySettings = {
  view_mode: "threaded",
  time_range: "7d",
  hide_closed: false,
  hide_bots: false,
  collapse_threads: false,
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

function hydrate(
  launchTargets = [codexTarget],
  activity = { hydrateDefaults: vi.fn() },
  issues = { hydrateDefaults: vi.fn() },
) {
  const settingsStore = createSettingsStore();
  const terminalHydration = beginTerminalSettingsHydration(settingsStore);
  applySettingsHydration(
    { settings: settingsStore, activity, issues },
    {
      repos: [],
      activity: activitySettings,
      issues: { hide_bots: true },
      terminal: DEFAULT_TERMINAL_SETTINGS,
      modes: DEFAULT_MODE_VISIBILITY,
      pullRequests: DEFAULT_PULL_REQUEST_SETTINGS,
      launchTargets,
    },
    terminalHydration,
  );
  return { settingsStore, activity, issues };
}

describe("applySettingsHydration", () => {
  it("hydrates launch targets into the settings store", () => {
    const { settingsStore } = hydrate();
    expect(settingsStore.getLaunchTargets()).toEqual([codexTarget]);
  });

  it("clears stale launch targets when settings reports an empty inventory", () => {
    const settingsStore = createSettingsStore();
    settingsStore.setLaunchTargets([codexTarget]);
    const terminalHydration = beginTerminalSettingsHydration(settingsStore);
    applySettingsHydration(
      { settings: settingsStore, activity: { hydrateDefaults: vi.fn() }, issues: { hydrateDefaults: vi.fn() } },
      {
        repos: [],
        activity: activitySettings,
        issues: { hide_bots: true },
        terminal: DEFAULT_TERMINAL_SETTINGS,
        modes: DEFAULT_MODE_VISIBILITY,
        pullRequests: DEFAULT_PULL_REQUEST_SETTINGS,
        launchTargets: [],
      },
      terminalHydration,
    );
    expect(settingsStore.getLaunchTargets()).toEqual([]);
  });

  it("also hydrates activity and issue defaults from the same payload", () => {
    const { activity, issues } = hydrate();
    expect(activity.hydrateDefaults).toHaveBeenCalledWith(activitySettings);
    expect(issues.hydrateDefaults).toHaveBeenCalledWith({ hide_bots: true });
  });
});
