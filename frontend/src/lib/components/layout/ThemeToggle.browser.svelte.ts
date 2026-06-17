// Browser-tier migration of the rendering behavior that
// frontend/tests/e2e/theme-toggle.spec.ts ("toggles dark mode from the header
// control") verified by driving the real header toggle in Chromium.
//
// The e2e checked four real-rendering facts about the theme control:
//   1. the icon button centers its glyph with a real flex box
//      (display/alignItems/justifyContent),
//   2. clicking it toggles the html.dark class,
//   3. the rendered SVG swaps when toggled (moon <-> sun),
//   4. the light-mode moon path computes a filled fill / no stroke.
//
// Facts 1-3 are component-tier: they exercise the shipped HeaderIconButton
// scoped CSS, the shipped lucide Moon/Sun icons, and the real theme.svelte.ts
// store. They are migrated here against a real Chromium page (jsdom cannot
// resolve getComputedStyle layout). Fact 4 depends on a fill rule scoped to
// AppHeader.svelte; reproducing it here would only assert a copied stylesheet,
// so it stays with AppHeader.test.ts / the e2e and is intentionally not
// duplicated. See the migration report notes.

import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

// app.css carries the design tokens and the :root.dark overrides the real
// theme store toggles. A real page has native localStorage/matchMedia, so no
// jsdom shims are needed (the browser project deliberately omits setup.ts).
import "../../../app.css";

import { cleanupTheme, initTheme, isDark } from "../../stores/theme.svelte.js";
import ThemeToggleControl from "./ThemeToggleControl.svelte";

function setLightBaseline(): void {
  // Mirror the e2e's emulateMedia({ colorScheme: "light" }) + fresh load:
  // clear any persisted choice and force a light starting point so the first
  // toggle deterministically goes light -> dark.
  try {
    localStorage.removeItem("middleman-theme");
    localStorage.setItem("middleman-theme", "light");
  } catch {
    // Storage blocked is irrelevant here; initTheme still honors the value.
  }
  document.documentElement.classList.remove("dark");
  initTheme();
}

describe("theme toggle control (browser)", () => {
  beforeEach(() => {
    setLightBaseline();
  });

  afterEach(() => {
    cleanupTheme();
    document.documentElement.classList.remove("dark");
    try {
      localStorage.removeItem("middleman-theme");
    } catch {
      // ignore
    }
  });

  it("renders the toggle as a real flex-centered icon button", async () => {
    const { container } = render(ThemeToggleControl);

    const button = page.getByTitle("Toggle theme");
    await expect.element(button).toBeVisible();

    const node = button.element();
    expect(node.tagName).toBe("BUTTON");

    // Real-rendering layout: HeaderIconButton centers its glyph with flexbox.
    // jsdom returns empty strings for these; only a real engine resolves them.
    const style = getComputedStyle(node);
    expect(style.display).toBe("inline-flex");
    expect(style.alignItems).toBe("center");
    expect(style.justifyContent).toBe("center");

    // The icon glyph is actually painted (non-zero box) inside the button.
    const svg = container.querySelector("button[title='Toggle theme'] svg");
    expect(svg).not.toBeNull();
    const svgBox = (svg as SVGElement).getBoundingClientRect();
    expect(svgBox.width).toBeGreaterThan(0);
    expect(svgBox.height).toBeGreaterThan(0);
  });

  it("toggles the html.dark class and swaps the icon when clicked", async () => {
    const { container } = render(ThemeToggleControl);

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
    render(ThemeToggleControl);

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
