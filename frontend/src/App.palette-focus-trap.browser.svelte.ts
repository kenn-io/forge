// Browser-tier analog of App.palette-focus-trap.test.ts. Tab and Shift+Tab must
// keep focus inside the open palette. The trap is programmatic (the dialog's
// keydown handler moves focus and prevents default), so the same code path runs
// in a real Chromium page as in jsdom -- but here a genuine focus model and real
// layout exercise it. A real page provides matchMedia/ResizeObserver/
// IntersectionObserver/canvas natively, so the jsdom installAppDomGlobals() shim
// is gone; the browser harness stubs only EventSource.
//
// The palette open/focus gate and the focus-containment/defaultPrevented facts
// stay as document.activeElement / event inspection against the real DOM, where
// the page locator API (getByText/getByRole/getByTitle/getByTestId) cannot
// reach. The "Escape closes the dialog" teardown is asserted by polling for the
// role="dialog"[aria-label="Command palette"] element to leave the DOM, the
// faithful translation of the jsdom screen.queryByRole(...).toBeNull() check
// (the element is genuinely removed by {#if isPaletteOpen()}).

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
// `page` drives the real Chromium viewport sizing in beforeEach.

import {
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";

describe("palette focus trap", () => {
  vi.setConfig({ testTimeout: 20_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    // The container store classifies layout by #app's clientWidth; a narrow
    // viewport renders the mobile branch (no AppHeader, no palette trigger). The
    // jsdom harness forced a 1280px desktop width via viewportWidth; the browser
    // analog is sizing the real Chromium viewport so the desktop shell renders.
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("Tab and Shift+Tab cycle within the open palette", async () => {
    mounted = await mountBrowserApp("/pulls");

    // The palette is opened from anywhere via Meta+K (Ctrl+K on non-mac);
    // both bindings are wired to palette.open in defaultActions.
    pressKey("k", { meta: true });

    // The dialog mounts and its input takes focus on open. Poll the real DOM for
    // both facts the way the jsdom waitFor() did.
    let input!: HTMLInputElement;
    await vi.waitFor(() => {
      const el = document.querySelector<HTMLInputElement>(".palette-input");
      expect(el).not.toBeNull();
      expect(document.activeElement).toBe(el);
      input = el!;
    });

    const palette = document.querySelector(".palette");
    expect(palette).not.toBeNull();

    // Forward Tab: focus must stay inside the .palette dialog.
    const tab = pressKey("Tab", {}, input);
    expect(palette!.contains(document.activeElement)).toBe(true);
    expect(tab.defaultPrevented).toBe(true);

    // Reverse Tab: same containment guarantee.
    pressKey("Tab", { shift: true }, document.activeElement!);
    expect(palette!.contains(document.activeElement)).toBe(true);

    // Escape closes the palette and tears down the modal frame.
    pressKey("Escape", {}, document.activeElement!);
    await vi.waitFor(() => expect(document.querySelector("[role='dialog'][aria-label='Command palette']")).toBeNull());
  });
});
