import { getThemeMode, getThemeColors, getThemeFonts, getThemeRadii } from "./embed-config.svelte.js";

const THEME_KEY = "middleman-theme";

const COLOR_MAP: Record<string, string> = {
  bgPrimary: "--bg-primary",
  bgSurface: "--bg-surface",
  bgSurfaceHover: "--bg-surface-hover",
  bgInset: "--bg-inset",
  borderDefault: "--border-default",
  borderMuted: "--border-muted",
  textPrimary: "--text-primary",
  textSecondary: "--text-secondary",
  textMuted: "--text-muted",
  accentBlue: "--accent-blue",
  accentAmber: "--accent-amber",
  accentPurple: "--accent-purple",
  accentGreen: "--accent-green",
  accentRed: "--accent-red",
  accentTeal: "--accent-teal",
  overlayBg: "--overlay-bg",
  shadowSm: "--shadow-sm",
  shadowMd: "--shadow-md",
  shadowLg: "--shadow-lg",
  kanbanNew: "--kanban-new",
  kanbanReviewing: "--kanban-reviewing",
  kanbanWaiting: "--kanban-waiting",
  kanbanAwaitingMerge: "--kanban-awaiting-merge",
};

const FONT_MAP: Record<string, string> = {
  sans: "--font-sans",
  mono: "--font-mono",
};

const RADII_MAP: Record<string, string> = {
  sm: "--radius-sm",
  md: "--radius-md",
  lg: "--radius-lg",
};

let dark = $state(false);
let mediaCleanup: (() => void) | null = null;
// In-memory manual override — survives when localStorage is blocked.
let manualDark: boolean | null = null;
// Track which CSS variables we've set so we can clear them on reset.
const appliedVars = new Set<string>();

function storedTheme(): string | null {
  try {
    const v = localStorage.getItem(THEME_KEY);
    if (v === "dark" || v === "light") return v;
    if (v !== null) localStorage.removeItem(THEME_KEY);
  } catch {
    // Storage blocked
  }
  return null;
}

function applyDarkClass(isDarkMode: boolean): void {
  document.documentElement.classList.toggle("dark", isDarkMode);
  document.documentElement.style.setProperty("color-scheme", isDarkMode ? "dark" : "light");
}

export function initTheme(): void {
  const configMode = getThemeMode();

  if (configMode) {
    if (configMode === "system") {
      const mq = window.matchMedia("(prefers-color-scheme: dark)");
      dark = mq.matches;
      const handler = (e: MediaQueryListEvent) => {
        dark = e.matches;
        applyDarkClass(dark);
      };
      mq.addEventListener("change", handler);
      mediaCleanup = () => mq.removeEventListener("change", handler);
    } else {
      dark = configMode === "dark";
    }
  } else {
    const stored = storedTheme();
    if (stored !== null) {
      dark = stored === "dark";
    } else {
      // No stored preference — follow OS and track changes until
      // the user manually toggles.
      const mq = window.matchMedia("(prefers-color-scheme: dark)");
      dark = mq.matches;
      const handler = (e: MediaQueryListEvent) => {
        dark = e.matches;
        applyDarkClass(dark);
      };
      mq.addEventListener("change", handler);
      mediaCleanup = () => mq.removeEventListener("change", handler);
    }
  }

  applyDarkClass(dark);

  applyThemeOverrides(getThemeColors(), getThemeFonts(), getThemeRadii());
}

export function reapplyTheme(): void {
  // Tear down any existing matchMedia listener before re-evaluating.
  mediaCleanup?.();
  mediaCleanup = null;

  const configMode = getThemeMode();
  if (configMode) {
    if (configMode === "system") {
      const mq = window.matchMedia("(prefers-color-scheme: dark)");
      dark = mq.matches;
      const handler = (e: MediaQueryListEvent) => {
        dark = e.matches;
        applyDarkClass(dark);
      };
      mq.addEventListener("change", handler);
      mediaCleanup = () => mq.removeEventListener("change", handler);
    } else {
      dark = configMode === "dark";
    }
  } else {
    // Mode removed — restore standalone behavior. Check in order:
    // 1. localStorage preference
    // 2. In-memory manual toggle (survives storage-blocked contexts)
    // 3. OS system preference with live listener
    const stored = storedTheme();
    if (stored !== null) {
      dark = stored === "dark";
    } else if (manualDark !== null) {
      dark = manualDark;
    } else {
      const mq = window.matchMedia("(prefers-color-scheme: dark)");
      dark = mq.matches;
      const handler = (e: MediaQueryListEvent) => {
        dark = e.matches;
        applyDarkClass(dark);
      };
      mq.addEventListener("change", handler);
      mediaCleanup = () => mq.removeEventListener("change", handler);
    }
  }

  applyDarkClass(dark);
  applyThemeOverrides(getThemeColors(), getThemeFonts(), getThemeRadii());
}

export function cleanupTheme(): void {
  mediaCleanup?.();
  mediaCleanup = null;
}

export function isDark(): boolean {
  return dark;
}

export function isThemeToggleVisible(): boolean {
  return getThemeMode() === undefined;
}

export function toggleTheme(): void {
  // User took manual control — stop following OS preference.
  mediaCleanup?.();
  mediaCleanup = null;

  dark = !dark;
  manualDark = dark;
  applyDarkClass(dark);
  try {
    localStorage.setItem(THEME_KEY, dark ? "dark" : "light");
  } catch {
    // Storage blocked — manualDark preserves the choice in memory
  }
}

export function applyThemeOverrides(
  colors: Record<string, string> | undefined | null,
  fonts: Record<string, string> | undefined | null,
  radii: Record<string, string> | undefined | null,
): void {
  const style = document.documentElement.style;

  // Clear any previously applied overrides so removed keys revert
  // to the stylesheet defaults.
  for (const cssVar of appliedVars) {
    style.removeProperty(cssVar);
  }
  appliedVars.clear();

  function apply(map: Record<string, string>, values: Record<string, string>): void {
    for (const [key, value] of Object.entries(values)) {
      const cssVar = map[key];
      if (cssVar) {
        style.setProperty(cssVar, value);
        appliedVars.add(cssVar);
      }
    }
  }

  if (colors) apply(COLOR_MAP, colors);
  if (fonts) apply(FONT_MAP, fonts);
  if (radii) apply(RADII_MAP, radii);
}
