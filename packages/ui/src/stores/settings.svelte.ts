import {
  DEFAULT_TERMINAL_SETTINGS,
  type ConfigRepo,
  type TerminalRenderer,
  type TerminalSettings,
} from "../api/types.js";

export function createSettingsStore() {
  let repos = $state<ConfigRepo[]>([]);
  let terminalSettings = $state<TerminalSettings>({
    ...DEFAULT_TERMINAL_SETTINGS,
  });
  let loaded = $state(false);

  function getConfiguredRepos(): ConfigRepo[] {
    return repos;
  }

  function setConfiguredRepos(r: ConfigRepo[]): void {
    repos = r ?? [];
    loaded = true;
  }

  function getTerminalSettings(): TerminalSettings {
    return terminalSettings;
  }

  function setTerminalSettings(settings: TerminalSettings): void {
    terminalSettings = settings;
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

  function getTerminalRenderer(): TerminalRenderer {
    return terminalSettings.renderer;
  }

  function setTerminalRenderer(renderer: TerminalSettings["renderer"] | null | undefined): void {
    terminalSettings = {
      ...terminalSettings,
      renderer: renderer === "ghostty-web" ? "ghostty-web" : "xterm",
    };
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
    getTerminalSettings,
    setTerminalSettings,
    getTerminalFontFamily,
    setTerminalFontFamily,
    getTerminalFontSize,
    getTerminalScrollback,
    getTerminalLineHeight,
    getTerminalLetterSpacing,
    getTerminalCursorBlink,
    getTerminalFontLigatures,
    getTerminalRenderer,
    setTerminalRenderer,
    hasConfiguredRepos,
    isSettingsLoaded,
  };
}

export type SettingsStore = ReturnType<typeof createSettingsStore>;
