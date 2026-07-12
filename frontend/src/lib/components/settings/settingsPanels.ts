export interface SettingsPanelMeta {
  id: string;
  label: string;
  title: string;
  group: string;
  description: string;
  /** Extra search-only terms; never rendered. */
  keywords: string;
  requiresKata?: boolean;
}

export const SETTINGS_PANELS: SettingsPanelMeta[] = [
  {
    id: "settings-repositories",
    label: "Repositories",
    title: "Repositories",
    group: "Providers",
    description: "Tracked repositories and import tools",
    keywords: "repos repositories providers github gitlab forgejo gitea import glob",
  },
  {
    id: "settings-pull-requests",
    label: "Pull requests",
    title: "Pull request safeguards",
    group: "Workflow",
    description: "Merge safeguards for stacked branches",
    keywords: "pull requests merge stack stacked branches safety",
  },
  {
    id: "settings-activity",
    label: "Activity",
    title: "Activity feed defaults",
    group: "Workflow",
    description: "Default activity feed filters",
    keywords: "activity feed defaults filters time range closed bots",
  },
  {
    id: "settings-terminal",
    label: "Terminal",
    title: "Workspace terminal",
    group: "Workspace",
    description: "Workspace terminal rendering and behavior",
    keywords: "workspace terminal font renderer cursor scrollback ligatures",
  },
  {
    id: "settings-kata-projects",
    label: "Kata mappings",
    title: "Kata project mappings",
    group: "Workspace",
    description: "Kata project repository identity overrides",
    keywords: "kata projects repositories mappings workspaces daemon project uid",
    requiresKata: true,
  },
  {
    id: "settings-agents",
    label: "Workspace agents",
    title: "Workspace agents",
    group: "Workspace",
    description: "Agent commands available in workspaces",
    keywords: "workspace agents codex claude gemini opencode aider binary arguments",
  },
  {
    id: "settings-fleet",
    label: "Fleet federation",
    title: "Fleet federation",
    group: "Workspace",
    description: "Remote hosts and fleet membership",
    keywords: "fleet federation remote hosts peers ssh http membership",
  },
  {
    id: "settings-modes",
    label: "Visible modes",
    title: "Visible modes",
    group: "Navigation",
    description: "Modes shown in the app header",
    keywords: "visible modes navigation tabs prs issues board reviews docs messages kata",
  },
];

export function settingsPanelsForModes(kataEnabled: boolean): SettingsPanelMeta[] {
  return SETTINGS_PANELS.filter((panel) => !panel.requiresKata || kataEnabled);
}
