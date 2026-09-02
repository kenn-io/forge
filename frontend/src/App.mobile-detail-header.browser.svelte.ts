// Phone-like PR and issue detail routes render inside the phone shell: the
// same top bar as every other phone view plus a detail header whose Back
// control returns to the list that opened the item. The forced-mobile flag
// makes a desktop-width Chromium page phone-like, so the shell is
// attributable to the flag rather than the viewport.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";

const WAIT = 10_000;

function count(selector: string): number {
  return document.querySelectorAll(selector).length;
}

function text(selector: string): string {
  return (document.querySelector(selector)?.textContent ?? "").replace(/\s+/g, " ").trim();
}

describe("phone detail header", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 800);
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = true;
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ = false;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("opens a PR from the phone list inside the shell and Back returns to that list", async () => {
    mounted = await mountBrowserApp("/m/pulls");
    await vi.waitFor(() => expect(count(".mobile-shell .pull-item")).toBeGreaterThan(0), WAIT);

    const firstItem = document.querySelector<HTMLElement>(".mobile-shell .pull-item")!;
    firstItem.click();

    await vi.waitFor(() => expect(count(".mobile-shell .focus-layout--phone .pull-detail")).toBe(1), WAIT);
    expect(window.location.pathname).toMatch(/^\/focus\/pulls\//);
    expect(count(".mobile-shell .mobile-topbar")).toBe(1);
    expect(count(".app-top-bar")).toBe(0);
    expect(text(".mobile-detail-header__badge")).toMatch(/^PR #\d+$/);
    expect(text(".mobile-detail-header__back")).toBe("Pull requests");
    // The shell's main is the page's only main landmark: the detail renders
    // inside it as a plain container, never as a nested main.
    expect(count("main")).toBe(1);
    expect(count("main.mobile-main .focus-layout--phone")).toBe(1);
    // The list recorded itself on the detail's history entry: Back pops that
    // entry rather than replacing the detail with a fresh list.
    expect((history.state as Record<string, unknown> | null)?.kennForgeMobileListOrigin).toBe("pulls");

    // Switching to the Files tab writes a new history entry; it must keep the
    // list origin, or Back from that tab falls back to a fresh list instead of
    // returning to the entry the list left behind.
    document.querySelector<HTMLElement>("[role='tab'][aria-label='Files changed']")!.click();
    await vi.waitFor(() => expect(window.location.pathname).toMatch(/\/files$/), WAIT);
    expect((history.state as Record<string, unknown> | null)?.kennForgeMobileListOrigin).toBe("pulls");
    expect(text(".mobile-detail-header__back")).toBe("Pull requests");

    document.querySelector<HTMLElement>(".mobile-detail-header__back")!.click();

    await vi.waitFor(() => expect(window.location.pathname).toBe("/m/pulls"), WAIT);
    await vi.waitFor(() => expect(count(".mobile-shell .pull-item")).toBeGreaterThan(0), WAIT);
    expect(count(".mobile-detail-header")).toBe(0);
  });

  it("restores the list's scroll offset through Back and starts a fresh visit at the top", async () => {
    // Short enough that the five fixture rows overflow the list viewport.
    await page.viewport(390, 360);
    mounted = await mountBrowserApp("/m/pulls");
    await vi.waitFor(() => expect(count(".mobile-shell .pull-item")).toBeGreaterThan(0), WAIT);
    const rows = count(".mobile-shell .pull-item");

    const viewport = () => document.querySelector<HTMLElement>(".mobile-shell .focus-list .kit-scrollbox__viewport")!;
    await vi.waitFor(() => expect(viewport().scrollHeight).toBeGreaterThan(viewport().clientHeight), WAIT);
    viewport().scrollTop = 80;
    const scrolled = viewport().scrollTop;
    expect(scrolled).toBeGreaterThan(0);

    document.querySelector<HTMLElement>(".mobile-shell .pull-item")!.click();
    await vi.waitFor(() => expect(count(".mobile-shell .focus-layout--phone .pull-detail")).toBe(1), WAIT);
    document.querySelector<HTMLElement>(".mobile-detail-header__back")!.click();

    // Back remounts the list on the entry it left: same rows, same offset.
    await vi.waitFor(() => expect(window.location.pathname).toBe("/m/pulls"), WAIT);
    await vi.waitFor(() => expect(count(".mobile-shell .pull-item")).toBe(rows), WAIT);
    await vi.waitFor(() => expect(viewport().scrollTop).toBe(scrolled), WAIT);

    // Leaving the detail any other way (here the Activity route, as the mode
    // picker does) and coming back is a fresh visit, so the parked offset is
    // discarded and the list starts at the top.
    document.querySelector<HTMLElement>(".mobile-shell .pull-item")!.click();
    await vi.waitFor(() => expect(count(".mobile-shell .focus-layout--phone .pull-detail")).toBe(1), WAIT);
    window.__kenn_forge_navigate_to_route!("/m");
    await vi.waitFor(() => expect(count(".mobile-shell .focus-list")).toBe(0), WAIT);
    window.__kenn_forge_navigate_to_route!("/m/pulls");
    await vi.waitFor(() => expect(count(".mobile-shell .pull-item")).toBe(rows), WAIT);
    await vi.waitFor(() => expect(viewport().scrollHeight).toBeGreaterThan(viewport().clientHeight), WAIT);
    expect(viewport().scrollTop).toBe(0);
  });

  it("gives a deep-linked issue the shell, an issue badge, and a Back that lands on the issue list", async () => {
    mounted = await mountBrowserApp("/issues/github/acme/widgets/7");
    await vi.waitFor(() => expect(count(".mobile-shell .focus-layout--phone .issue-detail")).toBe(1), WAIT);

    expect(window.location.pathname).toBe("/issues/github/acme/widgets/7");
    expect(count(".mobile-shell .mobile-topbar")).toBe(1);
    expect(text(".mobile-detail-header__badge")).toBe("Issue #7");
    expect(count(".mobile-detail-header__badge.issue")).toBe(1);
    expect(text(".mobile-detail-header__back")).toBe("Issues");

    document.querySelector<HTMLElement>(".mobile-detail-header__back")!.click();

    await vi.waitFor(() => expect(window.location.pathname).toBe("/m/issues"), WAIT);
  });

  it("carries the origin through a canonical tab switch so Back returns to the canonical list", async () => {
    mounted = await mountBrowserApp("/pulls");
    await vi.waitFor(() => expect(count(".mobile-shell .pull-item")).toBeGreaterThan(0), WAIT);

    document.querySelector<HTMLElement>(".mobile-shell .pull-item")!.click();
    await vi.waitFor(() => expect(count(".mobile-shell .focus-layout--phone .pull-detail")).toBe(1), WAIT);
    expect(window.location.pathname).toMatch(/^\/pulls\/github\//);
    expect((history.state as Record<string, unknown> | null)?.kennForgeMobileListOrigin).toBe("pulls");

    document.querySelector<HTMLElement>("[role='tab'][aria-label='Files changed']")!.click();
    await vi.waitFor(() => expect(window.location.pathname).toMatch(/\/files$/), WAIT);
    expect((history.state as Record<string, unknown> | null)?.kennForgeMobileListOrigin).toBe("pulls");
    expect((history.state as Record<string, unknown> | null)?.kennForgeMobileListBackDepth).toBe(2);

    document.querySelector<HTMLElement>(".mobile-detail-header__back")!.click();
    await vi.waitFor(() => expect(window.location.pathname).toBe("/pulls"), WAIT);
    await vi.waitFor(() => expect(count(".mobile-shell .pull-item")).toBeGreaterThan(0), WAIT);
  });

  it("keeps the phone list routes free of the detail header", async () => {
    mounted = await mountBrowserApp("/pulls");
    await vi.waitFor(() => expect(count(".mobile-shell .focus-list")).toBe(1), WAIT);
    expect(count(".mobile-detail-header")).toBe(0);
    expect(count(".mobile-topbar")).toBe(1);
  });
});
