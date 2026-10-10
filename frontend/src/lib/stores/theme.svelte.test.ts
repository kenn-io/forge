import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { initTheme, isDark, toggleTheme, cleanupTheme, setAppearanceMode, getAppearanceMode } from "./theme.svelte.js";

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
  document.documentElement.classList.remove("dark", "high-contrast");
  document.documentElement.style.cssText = "";
  document.documentElement.removeAttribute("data-kit-theme");
  try {
    localStorage.removeItem("kenn-forge-theme");
    localStorage.removeItem("kenn-forge-theme-name");
    localStorage.removeItem("kenn-forge-theme-high-contrast");
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

describe("kit identity", () => {
  it("restores e-reader mode even when the system is dark", () => {
    mockMatchMedia(true);
    initTheme();
    setAppearanceMode("e-reader");
    cleanupTheme();
    initTheme();
    expect(getAppearanceMode()).toBe("e-reader");
    expect(document.documentElement.dataset["kitTheme"]).toBe("e-reader");
    expect(isDark()).toBe(false);
  });

  it("leaves e-reader mode when the header switches to dark", () => {
    initTheme();
    setAppearanceMode("e-reader");
    toggleTheme();
    expect(getAppearanceMode()).toBe("dark");
    expect(document.documentElement.dataset["kitTheme"]).toBe("quiet");
    cleanupTheme();
    initTheme();
    expect(isDark()).toBe(true);
  });

  it("selects the quiet theme even when no theme name is stored", () => {
    initTheme();
    expect(document.documentElement.dataset["kitTheme"]).toBe("quiet");
  });

  it("keeps the quiet theme across a dark-mode toggle", () => {
    initTheme();
    toggleTheme();
    expect(document.documentElement.dataset["kitTheme"]).toBe("quiet");
  });
});
