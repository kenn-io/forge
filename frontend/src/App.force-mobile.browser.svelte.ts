// Browser-tier reimplementation of frontend/tests/e2e-full/force-mobile-routes.spec.ts.
// The __MIDDLEMAN_FORCE_MOBILE_ROUTES__ global forces the responsive focus
// presentation even on a desktop-width viewport, so the canonical /issues route
// renders the focus layout (no mobile shell, no app header). App.svelte reads
// the flag at render time via shouldForceMobileRoutes(), so the value present
// before mount is the one the initial render sees.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource. The presentation checks are element-count
// assertions on the real DOM, which the page locator API does not expose, so
// they stay as querySelectorAll wrapped in vi.waitFor for the async render.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";

const WAIT = 10_000;

function count(selector: string): number {
  return document.querySelectorAll(selector).length;
}

describe("force-mobile routes", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    // A desktop-width viewport so the focus presentation in the flagged case is
    // attributable to the flag, not to a compact viewport.
    await page.viewport(1280, 800);
    window.__MIDDLEMAN_FORCE_MOBILE_ROUTES__ = false;
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    window.__MIDDLEMAN_FORCE_MOBILE_ROUTES__ = false;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("renders the canonical issue route with focus presentation when forced", async () => {
    window.__MIDDLEMAN_FORCE_MOBILE_ROUTES__ = true;
    mounted = await mountBrowserApp("/issues");

    await vi.waitFor(() => expect(count(".focus-layout .focus-list")).toBe(1), WAIT);
    // Assert the focus list is actually visible, not merely mounted: a regression
    // that leaves the focus layout in the DOM but hidden by CSS must still fail.
    const focusList = document.querySelector(".focus-layout .focus-list")!;
    await expect.element(page.elementLocator(focusList)).toBeVisible();
    const rect = focusList.getBoundingClientRect();
    expect(rect.width).toBeGreaterThan(0);
    expect(rect.height).toBeGreaterThan(0);
    expect(window.location.pathname).toBe("/issues");
    expect(count(".mobile-shell")).toBe(0);
    expect(count(".app-header")).toBe(0);
  });

  it("renders the standard desktop shell at the same route without the flag", async () => {
    mounted = await mountBrowserApp("/issues");

    await vi.waitFor(() => expect(count(".app-header")).toBe(1), WAIT);
    expect(window.location.pathname).toBe("/issues");
    expect(count(".focus-layout")).toBe(0);
  });
});
