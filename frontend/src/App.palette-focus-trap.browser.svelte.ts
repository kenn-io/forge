// The palette opens from Meta+K with focus in its query input, and Escape
// closes it. Tab containment inside the open palette is kit-ui's trapFocus
// behavior and is tested there (context/testing.md, Library-owned behavior).
// A real page provides matchMedia/ResizeObserver/IntersectionObserver/canvas
// natively; the browser harness stubs only EventSource.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
// `page` drives the real Chromium viewport sizing in beforeEach.

import {
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";

describe("palette open and close", () => {
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

  it("opens from Meta+K with focus in the query, and Escape closes it", async () => {
    mounted = await mountBrowserApp("/pulls");

    // The palette is opened from anywhere via Meta+K (Ctrl+K on non-mac);
    // both bindings are wired to palette.open in defaultActions.
    pressKey("k", { meta: true });

    // The dialog mounts and its input takes focus on open. Poll the real DOM for
    // both facts the way the jsdom waitFor() did.
    let input!: HTMLInputElement;
    await vi.waitFor(() => {
      const el = document.querySelector<HTMLInputElement>(".palette-input input");
      expect(el).not.toBeNull();
      expect(document.activeElement).toBe(el);
      input = el!;
    });

    // Escape closes the palette and tears down the modal frame.
    pressKey("Escape", {}, input);
    await vi.waitFor(() => expect(document.querySelector("[role='dialog'][aria-label='Command palette']")).toBeNull());
  });
});
