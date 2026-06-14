// The ? shortcut opens the cheatsheet through the app shell's global
// keydown handler, view-scoped shortcuts appear under "On this view", and
// Escape closes it.

import { cleanup, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { installAppDomGlobals, mountApp, pressKey, resetKeyboardModuleState } from "./test/appHarness.js";

function cheatsheetDialog(): HTMLElement | null {
  return screen.queryByRole("dialog", { name: "Keyboard shortcuts" });
}

describe("cheatsheet", () => {
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

  it("? opens the cheatsheet and shows j/k under On this view", async () => {
    await mountApp("/pulls");
    await waitFor(() => expect(document.querySelector("[data-test='pr-list']")).not.toBeNull());

    pressKey("?", { shift: true });
    const sheet = await waitFor(() => {
      const dialog = cheatsheetDialog();
      expect(dialog).not.toBeNull();
      return dialog!;
    });

    // j and k navigate PRs on /pulls — they should appear under "On this view".
    const onThisView = Array.from(sheet.querySelectorAll(".cheatsheet-section")).find((section) =>
      section.textContent?.includes("On this view"),
    );
    expect(onThisView).toBeTruthy();
    expect(onThisView!.textContent).toMatch(/Next pull request|Previous pull request/i);
  });

  it("Escape closes the cheatsheet", async () => {
    await mountApp("/pulls");
    await waitFor(() => expect(document.querySelector("[data-test='pr-list']")).not.toBeNull());

    pressKey("?", { shift: true });
    await waitFor(() => expect(cheatsheetDialog()).not.toBeNull());

    pressKey("Escape");
    await waitFor(() => expect(cheatsheetDialog()).toBeNull());
  });
});
