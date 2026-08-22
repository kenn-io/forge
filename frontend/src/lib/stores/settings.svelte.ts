import {
  DEFAULT_MODE_VISIBILITY,
  DEFAULT_DETAIL_SETTINGS,
  DEFAULT_PULL_REQUEST_SETTINGS,
  DEFAULT_TERMINAL_SETTINGS,
  type ConfigRepo,
  type DetailSettings,
  type LaunchTarget,
  type ModeVisibility,
  type PullRequestSettings,
  type Settings,
  type RepoPreset,
  type TerminalSettings,
} from "../api/types.js";

export function createSettingsStore() {
  let repos = $state.raw<ConfigRepo[]>([]);
  let terminalSettings = $state.raw<TerminalSettings>({
    ...DEFAULT_TERMINAL_SETTINGS,
  });
  let modeVisibility = $state.raw<ModeVisibility>({
    ...DEFAULT_MODE_VISIBILITY,
  });
  let pullRequestSettings = $state.raw<PullRequestSettings>({
    ...DEFAULT_PULL_REQUEST_SETTINGS,
  });
  let detailSettings = $state.raw<DetailSettings>({
    ...DEFAULT_DETAIL_SETTINGS,
  });
  let launchTargets = $state.raw<LaunchTarget[]>([]);
  let workspaceSettings = $state.raw<Settings["workspaces"]>({
    auto_assign_on_create: false,
    default_sidebar_view: "diff",
  });
  let roborevSettings = $state.raw<Settings["roborev"]>({
    init_managed_clones: false,
  });
  let repoPresets = $state.raw<RepoPreset[]>([]);
  let loaded = $state(false);

  function getConfiguredRepos(): ConfigRepo[] {
    return repos;
  }

  function setConfiguredRepos(r: ConfigRepo[]): void {
    repos = r ?? [];
    loaded = true;
  }

  function getRepoPresets(): RepoPreset[] {
    return repoPresets;
  }

  function setRepoPresets(presets: RepoPreset[] | null | undefined): void {
    repoPresets = (presets ?? []).map((preset) => ({
      ...preset,
      repos: [...preset.repos],
    }));
  }

  function getTerminalSettings(): TerminalSettings {
    return terminalSettings;
  }

  function setTerminalSettings(settings: TerminalSettings): void {
    terminalSettings = settings;
  }

  function getModeVisibility(): ModeVisibility {
    return modeVisibility;
  }

  function setModeVisibility(settings: ModeVisibility | null | undefined): void {
    modeVisibility = {
      ...DEFAULT_MODE_VISIBILITY,
      ...(settings ?? {}),
    };
  }

  function isModeVisible(mode: keyof ModeVisibility): boolean {
    return modeVisibility[mode] ?? DEFAULT_MODE_VISIBILITY[mode];
  }

  function getPullRequestSettings(): PullRequestSettings {
    return pullRequestSettings;
  }

  function setPullRequestSettings(settings: PullRequestSettings | null | undefined): void {
    pullRequestSettings = {
      ...DEFAULT_PULL_REQUEST_SETTINGS,
      ...(settings ?? {}),
    };
  }

  function getDetailSettings(): DetailSettings {
    return detailSettings;
  }

  function setDetailSettings(settings: DetailSettings | null | undefined): void {
    detailSettings = {
      ...DEFAULT_DETAIL_SETTINGS,
      ...(settings ?? {}),
    };
  }

  function getLaunchTargets(): LaunchTarget[] {
    return launchTargets;
  }

  function setLaunchTargets(targets: LaunchTarget[] | null | undefined): void {
    launchTargets = [...(targets ?? [])];
  }

  function getWorkspaceSettings(): Settings["workspaces"] {
    return workspaceSettings;
  }

  function setWorkspaceSettings(value: Settings["workspaces"] | null | undefined): void {
    workspaceSettings = {
      auto_assign_on_create: value?.auto_assign_on_create ?? false,
      default_sidebar_view: value?.default_sidebar_view ?? "diff",
    };
  }

  function getRoborevSettings(): Settings["roborev"] {
    return roborevSettings;
  }

  function setRoborevSettings(value: Settings["roborev"] | null | undefined): void {
    roborevSettings = {
      init_managed_clones: value?.init_managed_clones ?? false,
    };
  }

  function getTerminalFontFamily(): string {
    return terminalSettings.font_family;
  }

  function setTerminalFontFamily(fontFamily: TerminalSettings["font_family"] | null | undefined): void {
    terminalSettings = {
      ...terminalSettings,
      font_family: fontFamily ?? "",
    };
  }

  function getTerminalFontSize(): number {
    return terminalSettings.font_size;
  }

  function getTerminalScrollback(): number {
    return terminalSettings.scrollback;
  }

  function getTerminalLineHeight(): number {
    return terminalSettings.line_height;
  }

  function getTerminalLetterSpacing(): number {
    return terminalSettings.letter_spacing;
  }

  function getTerminalCursorBlink(): boolean {
    return terminalSettings.cursor_blink;
  }

  function getTerminalFontLigatures(): boolean {
    return terminalSettings.font_ligatures;
  }

  function getTerminalRetainedSessions(): number {
    return terminalSettings.retained_sessions;
  }

  function hasConfiguredRepos(): boolean {
    return repos.length > 0;
  }

  function isSettingsLoaded(): boolean {
    return loaded;
  }

  return {
    getConfiguredRepos,
    setConfiguredRepos,
    getRepoPresets,
    setRepoPresets,
    getTerminalSettings,
    setTerminalSettings,
    getModeVisibility,
    setModeVisibility,
    isModeVisible,
    getPullRequestSettings,
    setPullRequestSettings,
    getDetailSettings,
    setDetailSettings,
    getLaunchTargets,
    setLaunchTargets,
    getWorkspaceSettings,
    setWorkspaceSettings,
    getRoborevSettings,
    setRoborevSettings,
    getTerminalFontFamily,
    setTerminalFontFamily,
    getTerminalFontSize,
    getTerminalScrollback,
    getTerminalLineHeight,
    getTerminalLetterSpacing,
    getTerminalCursorBlink,
    getTerminalFontLigatures,
    getTerminalRetainedSessions,
    hasConfiguredRepos,
    isSettingsLoaded,
  };
}

export type SettingsStore = ReturnType<typeof createSettingsStore>;
