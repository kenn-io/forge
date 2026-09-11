import { afterEach, describe, expect, it } from "vite-plus/test";
import { isSidebarCollapsed, getSidebarWidth, setSidebarWidth, toggleSidebar, initSidebar } from "./sidebar.svelte.js";

afterEach(() => {
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
