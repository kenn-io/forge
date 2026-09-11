import { cleanupTheme, initTheme as kitInitTheme, isDark, setThemeMode } from "@kenn-io/kit-ui";

export { cleanupTheme, isDark };

export function initTheme(): void {
  kitInitTheme({ storageKey: "kenn-forge-theme" });
}

export function toggleTheme(): void {
  setThemeMode(isDark() ? "light" : "dark");
}
