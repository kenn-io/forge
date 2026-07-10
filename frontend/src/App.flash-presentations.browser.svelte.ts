// Shell coverage matrix for the shared flash store: a flash raised through
// @middleman/ui/stores/flash must render in a mounted kit FlashBanner in
// every app presentation, not just the desktop shell. The jsdom App test
// covers the focus/phone presentation (its 1024px #app classifies compact);
// this browser suite covers the desktop shell (wide viewport) and the
// workspace embed shell (no header, banner pinned to the pane top), which
// previously had no banner at all — its showFlash calls went to the shared
// store and were never rendered.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";

const WAIT = 10_000;

describe("flash rendering across app shells", () => {
  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    const { getFlashes, dismissFlash } = await import("@middleman/ui/stores/flash");
    for (const flash of getFlashes()) dismissFlash(flash.id);
  });

  afterEach(async () => {
    const { getFlashes, dismissFlash } = await import("@middleman/ui/stores/flash");
    for (const flash of getFlashes()) dismissFlash(flash.id);
    mounted?.unmount();
    mounted = null;
  });

  async function visibleFlash(message: string): Promise<HTMLElement> {
    const { showFlash } = await import("@middleman/ui/stores/flash");
    showFlash(message, { tone: "danger" });
    return vi.waitFor(() => {
      const stack = document.querySelector<HTMLElement>(".kit-flash-stack");
      expect(stack?.textContent).toContain(message);
      return stack!;
    }, WAIT);
  }

  function expectBelowHeader(stack: HTMLElement): void {
    const header = document.querySelector<HTMLElement>(".app-top-bar");
    expect(header).not.toBeNull();
    expect(Math.abs(stack.getBoundingClientRect().top - header!.getBoundingClientRect().bottom)).toBeLessThan(1);
    expect(stack.closest(".focus-layout, .desktop-layout, .embed-layout")).toBeNull();
    expect(stack.querySelector(".kit-flash-banner")?.getAttribute("data-kit-tone")).toBe("danger");
  }

  it("renders shared-store flashes in the desktop shell", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/pulls");
    await vi.waitFor(() => expect(document.querySelector(".kit-sidebar-layout__sidebar")).not.toBeNull(), WAIT);

    expectBelowHeader(await visibleFlash("desktop shell flash"));
  });

  it("renders danger flashes at the compact page edge", async () => {
    await page.viewport(390, 844);
    mounted = await mountBrowserApp("/pulls");
    await vi.waitFor(() => expect(document.querySelector(".focus-layout")).not.toBeNull(), WAIT);

    const stack = await visibleFlash("compact shell flash");
    expect(stack.getBoundingClientRect().top).toBe(0);
    expect(stack.closest(".focus-layout, .desktop-layout, .embed-layout")).toBeNull();
    expect(stack.querySelector(".kit-flash-banner")?.getAttribute("data-kit-tone")).toBe("danger");
  });

  it("renders shared-store flashes in the workspace embed shell", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/workspaces/embed/empty/noSelection");
    await vi.waitFor(() => expect(document.querySelector(".embed-layout")).not.toBeNull(), WAIT);

    const stack = await visibleFlash("embed shell flash");
    expect(stack.getBoundingClientRect().top).toBe(0);
    expect(stack.closest(".focus-layout, .desktop-layout, .embed-layout")).toBeNull();
    expect(stack.querySelector(".kit-flash-banner")?.getAttribute("data-kit-tone")).toBe("danger");
  });
});
