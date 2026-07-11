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
import { render } from "vitest-browser-svelte";
import { Modal } from "@kenn-io/kit-ui";

import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";

const WAIT = 10_000;

describe("flash rendering across app shells", () => {
  let mounted: MountedBrowserApp | null = null;
  let overlayUnmount: (() => void) | null = null;
  let overlayTarget: HTMLElement | null = null;

  beforeEach(async () => {
    const { getFlashes, dismissFlash } = await import("@middleman/ui/stores/flash");
    for (const flash of getFlashes()) dismissFlash(flash.id);
  });

  afterEach(async () => {
    const { getFlashes, dismissFlash } = await import("@middleman/ui/stores/flash");
    for (const flash of getFlashes()) dismissFlash(flash.id);
    overlayUnmount?.();
    overlayUnmount = null;
    overlayTarget?.remove();
    overlayTarget = null;
    mounted?.unmount();
    mounted = null;
    delete window.__middleman_config;
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

  it("tracks the rendered height of a wrapping compact header", async () => {
    await page.viewport(390, 844);
    mounted = await mountBrowserApp("/kata");
    await vi.waitFor(() => expect(document.querySelector(".app-top-bar")).not.toBeNull(), WAIT);

    expectBelowHeader(await visibleFlash("compact header flash"));
  });

  it("tracks the rendered height of the phone-route header", async () => {
    await page.viewport(390, 844);
    mounted = await mountBrowserApp("/m");
    const header = await vi.waitFor(() => {
      const element = document.querySelector<HTMLElement>(".mobile-topbar");
      expect(element).not.toBeNull();
      return element!;
    }, WAIT);

    const stack = await visibleFlash("phone header flash");
    expect(Math.abs(stack.getBoundingClientRect().top - header.getBoundingClientRect().bottom)).toBeLessThan(1);
  });

  it("pins flashes to the page edge when embed config hides the header", async () => {
    await page.viewport(1280, 900);
    window.__middleman_config = { embed: { hideHeader: true } };
    mounted = await mountBrowserApp("/settings");
    await vi.waitFor(() => expect(document.querySelector(".app-main")).not.toBeNull(), WAIT);

    const stack = await visibleFlash("hidden header flash");
    expect(document.querySelector(".app-top-bar")).toBeNull();
    expect(stack.getBoundingClientRect().top).toBe(0);
  });

  it("keeps flashes above an open modal backdrop", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/pulls");
    overlayTarget = document.createElement("div");
    document.body.appendChild(overlayTarget);
    ({ unmount: overlayUnmount } = render(Modal, {
      target: overlayTarget,
      props: { title: "Retry action" },
    }));
    const overlay = await vi.waitFor(() => {
      const element = document.querySelector<HTMLElement>(".kit-modal-overlay");
      expect(element).not.toBeNull();
      return element!;
    }, WAIT);

    const stack = await visibleFlash("modal action flash");
    expect(Number.parseInt(getComputedStyle(stack).zIndex, 10)).toBeGreaterThan(
      Number.parseInt(getComputedStyle(overlay).zIndex, 10),
    );
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
