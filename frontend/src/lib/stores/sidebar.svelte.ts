import { getUIConfig, getSidebarWidth as getEmbeddedSidebarWidth } from "./embed-config.svelte.js";

const STORAGE_KEY = "middleman-sidebar";
const WIDTH_STORAGE_KEY = "middleman-sidebar-width";
const DEFAULT_WIDTH = 340;
const MIN_WIDTH = 200;
const MAX_WIDTH = 600;

let collapsed = $state(false);
let width = $state(DEFAULT_WIDTH);
// When the container enters narrow mode, we force-collapse the sidebar.
// narrowCollapsed forces closed; narrowOpened forces open (user override).
// Both are cleared when leaving narrow mode.
let narrowCollapsed = $state(false);
let narrowOpened = $state(false);

function clampWidth(value: number): number {
  return Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, Math.round(value)));
}

function loadPersisted(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === "collapsed";
  } catch {
    return false;
  }
}

function loadPersistedWidth(): number {
  try {
    const raw = localStorage.getItem(WIDTH_STORAGE_KEY);
    if (!raw) {
      return DEFAULT_WIDTH;
    }
    const parsed = Number(raw);
    if (Number.isFinite(parsed)) {
      return clampWidth(parsed);
    }
  } catch {
    // Storage blocked
  }
  return DEFAULT_WIDTH;
}

function persist(value: boolean): void {
  try {
    if (value) {
      localStorage.setItem(STORAGE_KEY, "collapsed");
    } else {
      localStorage.removeItem(STORAGE_KEY);
    }
  } catch {
    // Storage blocked
  }
}

function persistWidth(value: number): void {
  try {
    localStorage.setItem(WIDTH_STORAGE_KEY, String(clampWidth(value)));
  } catch {
    // Storage blocked
  }
}

export function initSidebar(): void {
  const ui = getUIConfig();
  if (ui.sidebarCollapsed !== undefined) {
    collapsed = ui.sidebarCollapsed;
  } else {
    collapsed = loadPersisted();
  }

  const embeddedWidth = getEmbeddedSidebarWidth();
  if (embeddedWidth !== undefined) {
    width = clampWidth(embeddedWidth);
  } else {
    width = loadPersistedWidth();
  }
}

export function isSidebarCollapsed(): boolean {
  if (narrowOpened) return false;
  if (narrowCollapsed) return true;
  return collapsed;
}

export function isSidebarToggleEnabled(): boolean {
  return getUIConfig().sidebarCollapsed === undefined;
}

export function toggleSidebar(): void {
  if (!isSidebarToggleEnabled()) return;
  if (narrowCollapsed || narrowOpened) {
    // Toggle the transient narrow state without touching the
    // persisted preference. Both flags reset on widen.
    const wasOpen = narrowOpened;
    narrowOpened = !wasOpen;
    narrowCollapsed = wasOpen;
    return;
  }
  collapsed = !collapsed;
  persist(collapsed);
}

export function setSidebarCollapsed(value: boolean): void {
  collapsed = value;
}

export function getSidebarWidth(): number {
  return width;
}

export function setSidebarWidth(value: number): void {
  width = clampWidth(value);
  if (getEmbeddedSidebarWidth() === undefined) {
    persistWidth(width);
  }
}

export function setNarrowOverride(narrow: boolean): void {
  if (narrow) {
    // Only reset on transition into narrow, not repeated calls.
    if (!narrowCollapsed && !narrowOpened) {
      narrowCollapsed = true;
    }
  } else {
    narrowCollapsed = false;
    narrowOpened = false;
  }
}
