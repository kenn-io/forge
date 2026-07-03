import {
  cleanupTheme as kitCleanupTheme,
  initTheme as kitInitTheme,
  isDark as kitIsDark,
  setThemeMode,
} from "@kenn-io/kit-ui";
import { getThemeMode, getThemeColors, getThemeFonts, getThemeRadii } from "./embed-config.svelte.js";

/*
 * Adapter over kit-ui's theme store. Standalone dark/light resolution,
 * persistence, and the `dark` root class are kit's; the storage key stays
 * "middleman-theme" and kit reads the same "dark"/"light" values the old
 * store wrote, so existing preferences carry over (kit adds "system" as a
 * persistable mode).
 *
 * What stays app-side, and why:
 * - Embed hosts can FORCE a mode through embed-config. kit's setThemeMode
 *   always persists, which would let a host's config overwrite the user's
 *   standalone preference — so the forced path applies classes directly and
 *   never touches kit's storage.
 * - applyThemeOverrides: embed-config color/font/radius CSS variable
 *   injection is a middleman embed feature, not a kit concern.
 */

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

// Non-null while an embed-config mode is forced; kit owns the theme
// otherwise. Svelte state so isDark() stays reactive on the forced path.
let forcedDark = $state<boolean | null>(null);
let forcedCleanup: (() => void) | null = null;
// The user's manual choice this session. Re-asserted through kit's
// setThemeMode on reapply so it survives blocked storage (the old store's
// manualDark fallback) and wins over a stale stored value kit would re-read.
let manualMode: "light" | "dark" | null = null;

// Track which CSS variables we've set so we can clear them on reset.
const appliedVars = new Set<string>();

function applyDarkClass(isDarkMode: boolean): void {
  // This IS the adapter over kit's theme store: the embed-forced path
  // applies classes directly by design (see context/ui-design-system.md).
  // kit-ui-check-ignore: sanctioned forced-mode class wiring in the adapter
  document.documentElement.classList.toggle("dark", isDarkMode);
}

function applyForcedMode(configMode: string): void {
  // resolveTheme() runs inside App's reapply $effect. Applying the dark class
  // from a local rather than reading `forcedDark` back is deliberate: reading
  // the same $state this function just wrote would make the effect depend on a
  // signal it mutates, self-retriggering into effect_update_depth_exceeded.
  let isDarkMode: boolean;
  if (configMode === "system") {
    // kit-ui-check-ignore: forced "system" mode tracks the OS directly, never persisting
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    isDarkMode = mq.matches;
    forcedDark = isDarkMode;
    const handler = (e: MediaQueryListEvent) => {
      forcedDark = e.matches;
      applyDarkClass(e.matches);
    };
    mq.addEventListener("change", handler);
    forcedCleanup = () => mq.removeEventListener("change", handler);
  } else {
    isDarkMode = configMode === "dark";
    forcedDark = isDarkMode;
  }
  applyDarkClass(isDarkMode);
}

function resolveTheme(): void {
  forcedCleanup?.();
  forcedCleanup = null;
  forcedDark = null;
  kitCleanupTheme();

  const configMode = getThemeMode();
  if (configMode) {
    // Initialize kit even on the forced path so its storage key is bound
    // (a later toggle persists to the right place), then immediately drop
    // its OS listener and override the classes with the forced mode.
    // Lifecycle contract this leans on (covered by the forced-toggle unit
    // test): kit cleanupTheme tears down only the OS-preference listener;
    // the bound storage key and in-memory mode survive, so setThemeMode
    // still works after cleanup.
    kitInitTheme({ storageKey: THEME_KEY });
    kitCleanupTheme();
    applyForcedMode(configMode);
  } else if (manualMode !== null) {
    // Re-assert the session's manual choice instead of re-reading storage:
    // when storage is blocked, kit's re-init would fall back to "system"
    // and lose the toggle.
    setThemeMode(manualMode);
  } else {
    // kit re-reads storage and re-arms its own OS-preference listener.
    kitInitTheme({ storageKey: THEME_KEY });
  }

  applyThemeOverrides(getThemeColors(), getThemeFonts(), getThemeRadii());
}

export function initTheme(): void {
  resolveTheme();
}

export function reapplyTheme(): void {
  resolveTheme();
}

export function cleanupTheme(): void {
  forcedCleanup?.();
  forcedCleanup = null;
  manualMode = null;
  kitCleanupTheme();
}

export function isDark(): boolean {
  return forcedDark ?? kitIsDark();
}

export function isThemeToggleVisible(): boolean {
  return getThemeMode() === undefined;
}

export function toggleTheme(): void {
  // Manual control beats a forced embed mode until the next reapply (the
  // keyboard shortcut can fire even while the header toggle is hidden), and
  // persisting an explicit mode stops OS-preference tracking — both matching
  // the old store's manual-override behavior.
  const next = isDark() ? "light" : "dark";
  forcedCleanup?.();
  forcedCleanup = null;
  forcedDark = null;
  manualMode = next;
  setThemeMode(next);
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
