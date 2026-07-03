// Guards the CSS contract behind the sr-only migration: visually-hidden
// content relies on the kit-sr-only class from @kenn-io/kit-ui/theme.css
// (imported at the top of app.css). If that import or the class ever went
// missing, every migrated label would render as visible text — this mounts
// the real app and asserts the class actually computes to a clipped 1px box.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";

const WAIT = 10_000;

describe("kit-sr-only contract", () => {
  let mounted: MountedBrowserApp | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
  });

  it("computes to visually hidden in the real stylesheet", async () => {
    await page.viewport(1280, 800);
    mounted = await mountBrowserApp("/pulls");

    // The pull list renders kit-sr-only CI/aria spans per row.
    await vi.waitFor(() => {
      expect(document.querySelector(".kit-sr-only")).not.toBeNull();
    }, WAIT);

    const el = document.querySelector<HTMLElement>(".kit-sr-only")!;
    const style = getComputedStyle(el);
    expect(style.position).toBe("absolute");
    expect(style.overflow).toBe("hidden");
    const rect = el.getBoundingClientRect();
    expect(rect.width).toBeLessThanOrEqual(1);
    expect(rect.height).toBeLessThanOrEqual(1);
    // Still present for assistive tech.
    expect(el.textContent?.length).toBeGreaterThan(0);
  });
});
