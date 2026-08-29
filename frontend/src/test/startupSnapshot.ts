import {
  DEFAULT_DETAIL_SETTINGS,
  DEFAULT_MODE_VISIBILITY,
  DEFAULT_PULL_REQUEST_SETTINGS,
  DEFAULT_TERMINAL_SETTINGS,
} from "../lib/api/types.js";
import type { StartupSnapshot } from "../lib/app/startup-workflow.js";

export function makeStartupSnapshot(overrides: Partial<StartupSnapshot> = {}): StartupSnapshot {
  const defaults = {
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
    detail: { ...DEFAULT_DETAIL_SETTINGS },
    fleet: {
      enabled: false,
      role: "hub",
      members: [],
      enrollments: [],
      sessions: {},
      restart_required: false,
    },
    mcp: {
      enabled: false,
      restart_required: false,
      active_requires_auth: false,
    },
    issues: { hide_bots: true },
    kata_projects: [],
    launch_targets: [],
    modes: { ...DEFAULT_MODE_VISIBILITY },
    notifications: { enabled: true },
    pull_requests: { ...DEFAULT_PULL_REQUEST_SETTINGS },
    roborev: { init_managed_clones: false },
    repo_presets: [],
    repos: [],
    terminal: { ...DEFAULT_TERMINAL_SETTINGS },
    workspaces: {
      auto_assign_on_create: false,
      default_sidebar_view: "diff",
    },
  } satisfies StartupSnapshot;

  return { ...defaults, ...overrides };
}
