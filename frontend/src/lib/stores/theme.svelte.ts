import { cleanupTheme, initTheme as kitInitTheme, isDark, setThemeMode, setThemeName } from "@kenn-io/kit-ui";

export { cleanupTheme, isDark };

export const FORGE_THEME_NAME = "quiet";

export function initTheme(): void {
  kitInitTheme({ storageKey: "kenn-forge-theme" });
  setThemeName(FORGE_THEME_NAME);
}

export function toggleTheme(): void {
  setThemeMode(isDark() ? "light" : "dark");
}
