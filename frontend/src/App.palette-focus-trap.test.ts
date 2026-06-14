// Tab and Shift+Tab must keep focus inside the open palette. The trap is
// programmatic (the dialog's keydown handler moves focus and prevents
// default), so jsdom exercises the same code path a browser does.

import { cleanup, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { installAppDomGlobals, mountApp, pressKey, resetKeyboardModuleState } from "./test/appHarness.js";

describe("palette focus trap", () => {
  vi.setConfig({ testTimeout: 20_000 });

  beforeEach(() => {
    installAppDomGlobals();
  });

  afterEach(async () => {
    cleanup();
    vi.unstubAllGlobals();
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("Tab and Shift+Tab cycle within the open palette", async () => {
    await mountApp("/pulls");

    // The palette is opened from anywhere via Meta+K (Ctrl+K on non-mac);
    // both bindings are wired to palette.open in defaultActions.
    pressKey("k", { meta: true });

    const input = await waitFor(() => {
      const el = document.querySelector<HTMLInputElement>(".palette-input");
      expect(el).not.toBeNull();
      expect(document.activeElement).toBe(el);
      return el!;
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
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull());
  });
});
