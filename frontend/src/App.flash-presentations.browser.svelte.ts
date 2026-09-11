// Shared flashes remain visible across desktop, phone, and modal presentations.

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
    const { getFlashes, dismissFlash } = await import("./lib/stores/flash.svelte.js");
    for (const flash of getFlashes()) dismissFlash(flash.id);
  });

  afterEach(async () => {
    const { getFlashes, dismissFlash } = await import("./lib/stores/flash.svelte.js");
    for (const flash of getFlashes()) dismissFlash(flash.id);
    overlayUnmount?.();
    overlayUnmount = null;
    overlayTarget?.remove();
    overlayTarget = null;
    mounted?.unmount();
    mounted = null;
  });

  async function visibleFlash(message: string): Promise<HTMLElement> {
    const { showFlash } = await import("./lib/stores/flash.svelte.js");
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
    expect(stack.closest(".focus-layout, .desktop-layout")).toBeNull();
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
    expect(stack.closest(".focus-layout, .desktop-layout")).toBeNull();
    expect(stack.querySelector(".kit-flash-banner")?.getAttribute("data-kit-tone")).toBe("danger");
  });

  it("tracks the rendered height of a wrapping compact header", async () => {
    await page.viewport(390, 844);
    mounted = await mountBrowserApp("/docs");
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
});
