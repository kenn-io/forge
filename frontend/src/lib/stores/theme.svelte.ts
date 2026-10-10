import {
  cleanupTheme,
  getThemeMode,
  getThemeName,
  initTheme as kitInitTheme,
  isDark,
  setThemeMode,
  setThemeName,
  type ThemeMode,
} from "@kenn-io/kit-ui";

export { cleanupTheme, isDark };

export const FORGE_THEME_NAME = "quiet";
export type AppearanceMode = ThemeMode | "e-reader";

export function isEReader(): boolean {
  return getThemeName() === "e-reader";
}

export function getAppearanceMode(): AppearanceMode {
  return isEReader() ? "e-reader" : getThemeMode();
}

export function setAppearanceMode(mode: AppearanceMode): void {
  setThemeName(mode === "e-reader" ? "e-reader" : FORGE_THEME_NAME);
  setThemeMode(mode === "e-reader" ? "light" : mode);
}

export function initTheme(): void {
  kitInitTheme({ storageKey: "kenn-forge-theme" });
  if (isEReader()) setThemeMode("light");
  else setThemeName(FORGE_THEME_NAME);
}

export function toggleTheme(): void {
  setAppearanceMode(isDark() ? "light" : "dark");
}
