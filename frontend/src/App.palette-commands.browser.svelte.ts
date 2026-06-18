// Browser-tier analog of App.palette-commands.test.ts. Palette open/close wiring
// and command dispatch run through the real app shell's global keydown handler,
// with the API mocked at the fetch boundary. A real Chromium page provides
// matchMedia/ResizeObserver/IntersectionObserver/canvas natively, so the jsdom
// installAppDomGlobals() shim is gone; the browser harness stubs only
// EventSource.
//
// The palette renders behind {#if isPaletteOpen()}, so opening/closing genuinely
// adds/removes the role="dialog" element. Visibility is driven through the page
// locator; the "open AND input focused" gate and the focus/defaultPrevented
// facts stay as document.activeElement / event inspection against the real DOM,
// where the page locator API (getByText/getByRole/getByTitle/getByTestId) cannot
// reach.
//
// PR-list gate: the jsdom source waited on
// document.querySelector("[data-test='pr-list']") / ".pr-list-row". The browser
// build strips authoring-only data-test attributes, so the list-body element is
// matched here by its real class (.list-body) and the rows by .pr-list-row
// (real class on PullItem.svelte). The intent -- "wait until the PR list has
// populated" -- is identical.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";

function paletteDialogEl(): HTMLElement | null {
  return document.querySelector<HTMLElement>("[role='dialog'][aria-label='Command palette']");
}

function paletteInput(): HTMLInputElement {
  const input = document.querySelector<HTMLInputElement>(".palette-input");
  expect(input).not.toBeNull();
  return input!;
}

async function waitForPaletteOpenAndFocused(): Promise<void> {
  // The dialog mounts and its input takes focus on open. Poll the real DOM for
  // both facts the way the jsdom waitForPaletteOpenAndFocused() did.
  await vi.waitFor(() => {
    expect(paletteDialogEl()).not.toBeNull();
    expect(document.activeElement).toBe(paletteInput());
  });
}

describe("palette command dispatch", () => {
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

  it("header trigger and Cmd+Shift+P open the palette", async () => {
    mounted = await mountBrowserApp("/pulls");
    const trigger = page.getByRole("button", { name: "Open command palette" });
    await expect.element(trigger).toBeVisible();
    await trigger.click();
    await waitForPaletteOpenAndFocused();

    pressKey("Escape");
    await vi.waitFor(() => expect(paletteDialogEl()).toBeNull());

    pressKey("P", { meta: true, shift: true });
    await waitForPaletteOpenAndFocused();

    pressKey("P", { meta: true, shift: true });
    await vi.waitFor(() => expect(paletteDialogEl()).toBeNull());
  });

  it("'>' filters to commands; running Open settings navigates", async () => {
    mounted = await mountBrowserApp("/pulls");
    // Wait until the PR list has populated from the mock fixtures.
    await expect.element(page.getByText("Add browser regression coverage")).toBeVisible();
    await vi.waitFor(() => expect(document.querySelector(".list-body")).not.toBeNull());

    pressKey("k", { meta: true });
    await waitForPaletteOpenAndFocused();

    const input = paletteInput();
    input.value = ">settings";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    pressKey("Enter", {}, input);

    await vi.waitFor(() => expect(window.location.pathname).toBe("/settings"));
  });

  it("typing a single character in the search input does not fire global j", async () => {
    mounted = await mountBrowserApp("/pulls");
    // Wait for PR rows to render so .pr-list-row.selected has a chance to appear
    // if the j shortcut leaks through.
    await vi.waitFor(() => expect(document.querySelector(".pr-list-row")).not.toBeNull());

    pressKey("k", { meta: true });
    await waitForPaletteOpenAndFocused();

    const before = document.querySelectorAll(".pr-list-row.selected").length;
    const input = paletteInput();
    pressKey("j", {}, input);
    input.value = "j";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    const after = document.querySelectorAll(".pr-list-row.selected").length;
    expect(after).toBe(before);
  });

  it("Cmd+P inside the palette closes it instead of opening browser print", async () => {
    mounted = await mountBrowserApp("/pulls");

    pressKey("k", { meta: true });
    await waitForPaletteOpenAndFocused();

    const event = pressKey("p", { meta: true }, paletteInput());
    await vi.waitFor(() => expect(paletteDialogEl()).toBeNull());
    // The shortcut must be consumed so the browser print dialog never opens.
    expect(event.defaultPrevented).toBe(true);
  });
});
