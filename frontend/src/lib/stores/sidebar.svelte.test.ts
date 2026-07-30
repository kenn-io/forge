import { afterEach, describe, expect, it } from "vite-plus/test";
import {
  isSidebarCollapsed,
  getSidebarWidth,
  setSidebarWidth,
  toggleSidebar,
  isSidebarToggleEnabled,
  initSidebar,
} from "./sidebar.svelte.js";

const win = window as any;

afterEach(() => {
  delete win.__kenn_forge_config;
  try {
    localStorage.removeItem("kenn-forge-sidebar");
  } catch {
    /* noop */
  }
  try {
    localStorage.removeItem("kenn-forge-sidebar-width");
  } catch {
    /* noop */
  }
});

describe("standalone mode", () => {
  it("starts expanded by default", () => {
    initSidebar();
    expect(isSidebarCollapsed()).toBe(false);
  });

  it("toggle is enabled", () => {
    initSidebar();
    expect(isSidebarToggleEnabled()).toBe(true);
  });

  it("toggleSidebar flips state", () => {
    initSidebar();
    toggleSidebar();
    expect(isSidebarCollapsed()).toBe(true);
    toggleSidebar();
    expect(isSidebarCollapsed()).toBe(false);
  });

  it("persists to localStorage", () => {
    initSidebar();
    toggleSidebar();
    expect(localStorage.getItem("kenn-forge-sidebar")).toBe("collapsed");
  });

  it("starts with the default width", () => {
    initSidebar();
    expect(getSidebarWidth()).toBe(340);
  });

  it("persists a resized width", () => {
    initSidebar();
    setSidebarWidth(420);
    expect(getSidebarWidth()).toBe(420);
    expect(localStorage.getItem("kenn-forge-sidebar-width")).toBe("420");
  });
});

describe("embedded mode — embedder owns sidebar", () => {
  it("uses config value when set to true", () => {
    win.__kenn_forge_config = {
      ui: { sidebarCollapsed: true },
    };
    win.__kenn_forge_notify_config_changed?.();
    initSidebar();
    expect(isSidebarCollapsed()).toBe(true);
  });

  it("uses config value when set to false", () => {
    win.__kenn_forge_config = {
      ui: { sidebarCollapsed: false },
    };
    win.__kenn_forge_notify_config_changed?.();
    initSidebar();
    expect(isSidebarCollapsed()).toBe(false);
  });

  it("toggle is disabled when embedder owns", () => {
    win.__kenn_forge_config = {
      ui: { sidebarCollapsed: false },
    };
    win.__kenn_forge_notify_config_changed?.();
    initSidebar();
    expect(isSidebarToggleEnabled()).toBe(false);
  });

  it("uses the embedded width when provided", () => {
    win.__kenn_forge_config = {
      embed: { sidebarWidth: 410 },
    };
    win.__kenn_forge_notify_config_changed?.();
    initSidebar();
    expect(getSidebarWidth()).toBe(410);
  });
});

describe("embedded mode — user owns sidebar", () => {
  it("defaults to expanded when not set", () => {
    win.__kenn_forge_config = { ui: {} };
    win.__kenn_forge_notify_config_changed?.();
    initSidebar();
    expect(isSidebarCollapsed()).toBe(false);
  });

  it("toggle is enabled when not set", () => {
    win.__kenn_forge_config = { ui: {} };
    win.__kenn_forge_notify_config_changed?.();
    initSidebar();
    expect(isSidebarToggleEnabled()).toBe(true);
  });
});
