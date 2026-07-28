import { hydrateTerminalSettings, type TerminalSettingsHydration } from "./terminal-settings-persistence.js";
import type { ActivitySettings, ConfigRepo, Settings, TerminalSettings } from "../api/types.js";

// Minimal structural shapes rather than the full store types: this module
// only needs the setters it calls, and narrowing here keeps it testable
// without constructing a whole Provider store graph.
export interface SettingsHydrationStore {
  setConfiguredRepos: (repos: ConfigRepo[]) => void;
  setModeVisibility: (modes: Settings["modes"]) => void;
  setPullRequestSettings: (pullRequests: Settings["pull_requests"]) => void;
  setLaunchTargets: (targets: Settings["launch_targets"]) => void;
}

export interface ActivityHydrationStore {
  hydrateDefaults: (activity: ActivitySettings) => void;
}

export interface IssuesHydrationStore {
  hydrateDefaults: (issues: Settings["issues"]) => void;
}

export interface SettingsHydrationPayload {
  repos: ConfigRepo[];
  activity: ActivitySettings;
  issues: Settings["issues"];
  terminal: TerminalSettings;
  modes: Settings["modes"];
  pullRequests: Settings["pull_requests"];
  launchTargets: Settings["launch_targets"];
}

/**
 * Applies a `GET /settings` payload to the settings, activity, and issues
 * stores. Shared by startup hydration and config-hot-reload hydration so
 * both apply the same fields — a field added to one and not the other
 * silently goes stale after a hot reload until the next full page load.
 */
export function applySettingsHydration(
  stores: {
    settings: SettingsHydrationStore;
    activity: ActivityHydrationStore;
    issues: IssuesHydrationStore;
  },
  payload: SettingsHydrationPayload,
  terminalHydration: TerminalSettingsHydration,
): void {
  stores.settings.setConfiguredRepos(payload.repos);
  hydrateTerminalSettings(terminalHydration, payload.terminal);
  stores.settings.setModeVisibility(payload.modes);
  stores.settings.setPullRequestSettings(payload.pullRequests);
  stores.settings.setLaunchTargets(payload.launchTargets);
  stores.activity.hydrateDefaults(payload.activity);
  stores.issues.hydrateDefaults(payload.issues);
}
