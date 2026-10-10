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
export type EReaderMode = "e-reader" | "e-reader-color";
export type AppearanceMode = ThemeMode | EReaderMode;

function isEReaderMode(value: string | null): value is EReaderMode {
  return value === "e-reader" || value === "e-reader-color";
}

/** True for both e-reader modes: always-light white pages with black text. */
export function isEReader(): boolean {
  return isEReaderMode(getThemeName());
}

/** The e-reader variant that keeps status, diff, code, and terminal color. */
export function isColorEReader(): boolean {
  return getThemeName() === "e-reader-color";
}

export function getAppearanceMode(): AppearanceMode {
  const name = getThemeName();
  return isEReaderMode(name) ? name : getThemeMode();
}

export function setAppearanceMode(mode: AppearanceMode): void {
  setThemeName(isEReaderMode(mode) ? mode : FORGE_THEME_NAME);
  setThemeMode(isEReaderMode(mode) ? "light" : mode);
}

export function initTheme(): void {
  kitInitTheme({ storageKey: "kenn-forge-theme" });
  if (isEReader()) setThemeMode("light");
  else setThemeName(FORGE_THEME_NAME);
}

export function toggleTheme(): void {
  setAppearanceMode(isDark() ? "light" : "dark");
}
