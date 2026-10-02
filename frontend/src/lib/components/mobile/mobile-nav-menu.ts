import { getContext, setContext } from "svelte";
import type { ModeVisibility } from "../../api/types.js";
import type { Page } from "../../stores/router.svelte.js";

// The phone shell has no top bar. Each phone screen renders the navigation
// menu at the end of its own first row, and only screens mounted inside the
// phone shell receive this context, so desktop and desktop-narrow views that
// share those rows render no menu.
export interface MobileNavMenuContext {
  page: () => Page;
  isModeVisible: (mode: keyof ModeVisibility) => boolean;
  onNavigate: (path: string) => void;
  onDesktopView: () => void;
}

const MOBILE_NAV_MENU_KEY = Symbol("kenn-forge-mobile-nav-menu");

export function setMobileNavMenuContext(context: MobileNavMenuContext): void {
  setContext(MOBILE_NAV_MENU_KEY, context);
}

export function getMobileNavMenuContext(): MobileNavMenuContext | undefined {
  return getContext<MobileNavMenuContext | undefined>(MOBILE_NAV_MENU_KEY);
}

export interface MobileNavMode {
  path: string;
  label: string;
}

const modes: Array<MobileNavMode & { mode: keyof ModeVisibility }> = [
  { mode: "activity", path: "/m", label: "Activity" },
  { mode: "pulls", path: "/m/pulls", label: "PRs" },
  { mode: "issues", path: "/m/issues", label: "Issues" },
  { mode: "workspaces", path: "/m/workspaces", label: "Workspaces" },
];

export function mobileNavModes(isModeVisible: (mode: keyof ModeVisibility) => boolean): MobileNavMode[] {
  return modes.filter((entry) => isModeVisible(entry.mode)).map(({ path, label }) => ({ path, label }));
}

export function mobileNavSelectedPath(page: Page): string {
  if (page === "mobile-workspaces" || page === "mobile-workspace-terminal" || page === "mobile-workspace-item") {
    return "/m/workspaces";
  }
  if (page === "mobile-pulls") return "/m/pulls";
  if (page === "mobile-issues") return "/m/issues";
  return "/m";
}
