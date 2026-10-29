// Browser-tier test for the real ThemeToggle component
// (frontend/src/lib/components/layout/ThemeToggle.svelte), which AppHeader
// renders in the header. This replaces frontend/tests/e2e/theme-toggle.spec.ts:
// the toggle is now a standalone component, so the real-rendering facts the
// Playwright e2e drove through the full app shell run here in-process against a
// real Chromium page, and the e2e spec was deleted.
//
// Facts covered (jsdom resolves none of these):
//   1. the light-mode moon path computes a filled fill / no stroke,
//   2. clicking it toggles the html.dark class and swaps moon <-> sun,
//   3. toggling on applies the real :root.dark design-token override.

import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

// app.css carries the design tokens and the :root.dark overrides the real theme
// store toggles. A real page has native localStorage/matchMedia, so no jsdom
// shims are needed (the browser project deliberately omits setup.ts).
import "../../../app.css";

import { cleanupTheme, initTheme, isDark } from "../../stores/theme.svelte.js";
import ThemeToggle from "./ThemeToggle.svelte";

function setLightBaseline(): void {
  // Mirror the e2e's emulateMedia({ colorScheme: "light" }) + fresh load: clear
  // any persisted choice and force a light start so the first toggle goes dark.
  try {
    localStorage.removeItem("kenn-forge-theme");
    localStorage.setItem("kenn-forge-theme", "light");
  } catch {
    // Storage blocked is irrelevant here; initTheme still honors the value.
  }
  // kit-ui-check-ignore: test harness resets the dark class between cases
  document.documentElement.classList.remove("dark");
  initTheme();
}

describe("ThemeToggle (browser)", () => {
  beforeEach(() => {
    setLightBaseline();
  });

  afterEach(() => {
    cleanupTheme();
    // kit-ui-check-ignore: test harness resets the dark class between cases
    document.documentElement.classList.remove("dark");
    try {
      localStorage.removeItem("kenn-forge-theme");
    } catch {
      // ignore
    }
  });

  it("renders the light-mode moon as a filled glyph (fill, no stroke)", async () => {
    const { container } = await render(ThemeToggle);

    await expect.element(page.getByTitle("Toggle theme")).toBeVisible();
    expect(isDark()).toBe(false);

    // The component-scoped [data-filled-icon="moon"] rule overrides lucide's
    // stroke-only glyph. This is the assertion the old harness could not make
    // (it carried no styling); the real component now owns the rule.
    const path = container.querySelector("[data-filled-icon='moon'] svg path");
    expect(path).not.toBeNull();
    const pathStyle = getComputedStyle(path as SVGElement);
    expect(pathStyle.stroke).toBe("none");
    expect(pathStyle.fill).not.toBe("none");
    expect(pathStyle.fill.length).toBeGreaterThan(0);
  });

  it("toggles the html.dark class and swaps the icon when clicked", async () => {
    const { container } = await render(ThemeToggle);

    const root = document.documentElement;
    const button = page.getByTitle("Toggle theme");
    await expect.element(button).toBeVisible();

    expect(root.classList.contains("dark")).toBe(false);
    expect(isDark()).toBe(false);

    const beforeIcon = container.querySelector("button[title='Toggle theme'] svg")?.innerHTML ?? null;
    expect(beforeIcon).toBeTruthy();

    await button.click();

    expect(root.classList.contains("dark")).toBe(true);
    expect(isDark()).toBe(true);

    const afterSvg = container.querySelector("button[title='Toggle theme'] svg");
    expect(afterSvg).not.toBeNull();
    const afterIcon = afterSvg?.innerHTML ?? null;
    expect(afterIcon).toBeTruthy();
    // moon -> sun: the rendered glyph genuinely changes, not just a class flip.
    expect(afterIcon).not.toBe(beforeIcon);

    await button.click();

    expect(root.classList.contains("dark")).toBe(false);
    expect(isDark()).toBe(false);
    expect(container.querySelector("button[title='Toggle theme'] svg")).not.toBeNull();
  });

  it("applies a real dark token override to html when toggled on", async () => {
    await render(ThemeToggle);

    const root = document.documentElement;
    const lightSurface = getComputedStyle(root).getPropertyValue("--bg-surface").trim();
    expect(lightSurface.length).toBeGreaterThan(0);

    const button = page.getByTitle("Toggle theme");
    await button.click();

    // :root.dark in app.css redefines tokens; a real page resolves the cascade.
    const darkSurface = getComputedStyle(root).getPropertyValue("--bg-surface").trim();
    expect(darkSurface.length).toBeGreaterThan(0);
    expect(darkSurface).not.toBe(lightSurface);
  });
});
