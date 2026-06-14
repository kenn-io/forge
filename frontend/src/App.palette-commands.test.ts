// Palette open/close wiring and command dispatch through the real app
// shell's global keydown handler, with the API mocked at the fetch
// boundary.

import { cleanup, screen, waitFor } from "@testing-library/svelte";
import { fireEvent } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { installAppDomGlobals, mountApp, pressKey, resetKeyboardModuleState } from "./test/appHarness.js";

function paletteDialog(): HTMLElement | null {
  return screen.queryByRole("dialog", { name: "Command palette" });
}

function paletteInput(): HTMLInputElement {
  const input = document.querySelector<HTMLInputElement>(".palette-input");
  expect(input).not.toBeNull();
  return input!;
}

async function waitForPaletteOpenAndFocused(): Promise<void> {
  await waitFor(() => {
    expect(paletteDialog()).not.toBeNull();
    expect(document.activeElement).toBe(paletteInput());
  });
}

describe("palette command dispatch", () => {
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

  it("header trigger and Cmd+Shift+P open the palette", async () => {
    await mountApp("/pulls");
    const trigger = await waitFor(() => screen.getByRole("button", { name: "Open command palette" }));
    await fireEvent.click(trigger);
    await waitForPaletteOpenAndFocused();

    pressKey("Escape");
    await waitFor(() => expect(paletteDialog()).toBeNull());

    pressKey("P", { meta: true, shift: true });
    await waitForPaletteOpenAndFocused();

    pressKey("P", { meta: true, shift: true });
    await waitFor(() => expect(paletteDialog()).toBeNull());
  });

  it("'>' filters to commands; running Open settings navigates", async () => {
    await mountApp("/pulls");
    await waitFor(() => expect(document.querySelector("[data-test='pr-list']")).not.toBeNull());

    pressKey("k", { meta: true });
    await waitForPaletteOpenAndFocused();

    await fireEvent.input(paletteInput(), { target: { value: ">settings" } });
    pressKey("Enter", {}, paletteInput());

    await waitFor(() => expect(window.location.pathname).toBe("/settings"));
  });

  it("typing a single character in the search input does not fire global j", async () => {
    await mountApp("/pulls");
    // Wait for PR rows to render so .pr-list-row.selected has a chance
    // to appear if the j shortcut leaks through.
    await waitFor(() => expect(document.querySelector(".pr-list-row")).not.toBeNull());

    pressKey("k", { meta: true });
    await waitForPaletteOpenAndFocused();

    const before = document.querySelectorAll(".pr-list-row.selected").length;
    pressKey("j", {}, paletteInput());
    await fireEvent.input(paletteInput(), { target: { value: "j" } });
    const after = document.querySelectorAll(".pr-list-row.selected").length;
    expect(after).toBe(before);
  });

  it("Cmd+P inside the palette closes it instead of opening browser print", async () => {
    await mountApp("/pulls");

    pressKey("k", { meta: true });
    await waitForPaletteOpenAndFocused();

    const event = pressKey("p", { meta: true }, paletteInput());
    await waitFor(() => expect(paletteDialog()).toBeNull());
    // The shortcut must be consumed so the browser print dialog never opens.
    expect(event.defaultPrevented).toBe(true);
  });
});
