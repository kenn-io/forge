import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { initTheme, isDark, toggleTheme, cleanupTheme } from "./theme.svelte.js";

function mockMatchMedia(matches: boolean): void {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

beforeEach(() => {
  mockMatchMedia(false);
});

afterEach(() => {
  document.documentElement.classList.remove("dark");
  document.documentElement.style.cssText = "";
  try {
    localStorage.removeItem("kenn-forge-theme");
  } catch {
    /* storage blocked */
  }
  cleanupTheme();
});

describe("standalone mode (no config)", () => {
  it("toggleTheme flips dark state", () => {
    initTheme();
    const initial = isDark();
    toggleTheme();
    expect(isDark()).toBe(!initial);
  });

  it("persists theme to localStorage", () => {
    initTheme();
    toggleTheme();
    const stored = localStorage.getItem("kenn-forge-theme");
    expect(stored).toBeTruthy();
  });

  it("an explicit toggle overwrites an invalid stored value", () => {
    // kit ignores garbage (resolving to system) without deleting it; the
    // contract is that the next explicit choice replaces it.
    localStorage.setItem("kenn-forge-theme", "garbage");
    mockMatchMedia(true);
    initTheme();
    expect(isDark()).toBe(true);

    toggleTheme();
    expect(isDark()).toBe(false);
    expect(localStorage.getItem("kenn-forge-theme")).toBe("light");
  });
});
