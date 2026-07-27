// Whether the palette offers the structural pane commands depends on facts only
// a real browser produces: the measured width of the detail host (which decides
// the narrow-width flattened fallback) and whether a pane is actually rendered.
// The jsdom coverage in DetailPaneLayout.test.ts and actions.test.ts mocks both,
// so it can prove the gating logic but not that the real measurement reaches it.
//
// Everything here goes through the real App shell with the shared mock API
// fixtures, the same way App.palette-commands.browser.svelte.ts does.
//
// Hiding a pane is deliberately not covered here. Only hideable panes render a
// close control, which in PRs mode means the workspace pane and therefore a
// claimed workspace; and the rule ("a hidden pane is available but renders
// nothing") does not depend on real layout at all, so the jsdom coverage in
// DetailPaneLayout.test.ts proves it without the setup.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";

function paletteInput(): HTMLInputElement {
  const input = document.querySelector<HTMLInputElement>(".palette-input input");
  expect(input).not.toBeNull();
  return input!;
}

async function openPalette(): Promise<void> {
  pressKey("k", { meta: true });
  await vi.waitFor(() => {
    expect(document.querySelector("[role='dialog'][aria-label='Command palette']")).not.toBeNull();
    expect(document.activeElement).toBe(paletteInput());
  });
}

async function closePalette(): Promise<void> {
  pressKey("Escape");
  await vi.waitFor(() => {
    expect(document.querySelector("[role='dialog'][aria-label='Command palette']")).toBeNull();
  });
}

async function typeCommandQuery(query: string): Promise<void> {
  const input = paletteInput();
  input.value = `>${query}`;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await vi.waitFor(() => expect(input.value).toBe(`>${query}`));
}

/**
 * Palette rows matching a label. Rows render as `<button class="palette-row">`,
 * counted against the real DOM so the 0 / non-0 semantics stay exact — a matched
 * command can appear in more than one palette group.
 */
function paletteRowsNamed(pattern: RegExp): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>("button.palette-row")].filter((row) =>
    pattern.test(row.textContent ?? ""),
  );
}

async function openFirstPull(): Promise<void> {
  await vi.waitFor(() => expect(document.querySelector(".pr-list-row")).not.toBeNull());
  document.querySelector<HTMLElement>(".pr-list-row")?.click();
  await vi.waitFor(() => expect(document.querySelector(".tabbed-panel-leaf")).not.toBeNull());
}

describe("pane commands against a real detail layout", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  describe("at a desktop width", () => {
    beforeEach(async () => {
      await page.viewport(1280, 900);
    });

    it("offers the structural pane commands once a detail layout is on screen", async () => {
      mounted = await mountBrowserApp("/pulls");
      await openFirstPull();

      await openPalette();
      await typeCommandQuery("pane");

      expect(paletteRowsNamed(/Maximize pane/)).not.toHaveLength(0);
      expect(paletteRowsNamed(/Reset pane layout/)).not.toHaveLength(0);
      await closePalette();
    });
  });

  describe("below the flatten width", () => {
    beforeEach(async () => {
      // 600px of viewport leaves the detail host under the 720px threshold, so
      // the real ResizeObserver reports a flattened layout. Every structural
      // edit is disabled there, and a palette command must not quietly
      // rearrange a persisted tree that is showing as one strip.
      await page.viewport(600, 900);
    });

    it("withdraws the structural pane commands", async () => {
      mounted = await mountBrowserApp("/pulls/github/acme/widget/7");
      await vi.waitFor(() => expect(document.querySelector(".tabbed-panel-leaf")).not.toBeNull());
      // One strip: the flat fallback merges every pane into a single tablist.
      await vi.waitFor(() => {
        expect(document.querySelectorAll("[role='tablist']").length).toBe(1);
      });

      await openPalette();
      await typeCommandQuery("pane");

      expect(paletteRowsNamed(/Maximize pane/)).toHaveLength(0);
      expect(paletteRowsNamed(/Reset pane layout/)).toHaveLength(0);
      await closePalette();
    });
  });
});
