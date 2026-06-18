// Browser-tier analog of App.cheatsheet.test.ts. The ? shortcut opens the
// cheatsheet through the app shell's global keydown handler, view-scoped
// shortcuts appear under "On this view", and Escape closes it. A real Chromium
// page provides matchMedia/ResizeObserver/IntersectionObserver/canvas natively,
// so the jsdom installAppDomGlobals() shim is gone; the browser harness stubs
// only EventSource.
//
// Note on the PR-list gate: the jsdom source waited on
// document.querySelector("[data-test='pr-list']"). The browser build strips
// authoring-only data-test attributes (verified: no [data-test] node survives
// in the rendered DOM, though class names do), so the same element is matched
// here by its real class (.list-body, the div that carries data-test='pr-list'
// in PullList.svelte). The intent -- "wait until the PR list has populated" --
// is identical.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";

function cheatsheetDialogEl(): HTMLElement | null {
  // The cheatsheet renders as a real role="dialog" labelled "Keyboard
  // shortcuts". page.getByRole drives the visibility assertions, but the
  // section inspection below needs the live element, so resolve it directly.
  return document.querySelector<HTMLElement>("[role='dialog'][aria-label='Keyboard shortcuts']");
}

describe("cheatsheet", () => {
  vi.setConfig({ testTimeout: 20_000 });

  let mounted: MountedBrowserApp | null = null;

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("? opens the cheatsheet and shows j/k under On this view", async () => {
    mounted = await mountBrowserApp("/pulls");
    // Wait until the PR list has populated from the mock fixtures (a fixture PR
    // is visible) and its list body is in the DOM.
    await expect.element(page.getByText("Add browser regression coverage")).toBeVisible();
    await vi.waitFor(() => expect(document.querySelector(".list-body")).not.toBeNull());

    pressKey("?", { shift: true });

    await expect.element(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeVisible();

    // j and k navigate PRs on /pulls — they should appear under "On this view".
    const sheet = cheatsheetDialogEl();
    expect(sheet).not.toBeNull();
    const onThisView = Array.from(sheet!.querySelectorAll(".cheatsheet-section")).find((section) =>
      section.textContent?.includes("On this view"),
    );
    expect(onThisView).toBeTruthy();
    expect(onThisView!.textContent).toMatch(/Next pull request|Previous pull request/i);
  });

  it("Escape closes the cheatsheet", async () => {
    mounted = await mountBrowserApp("/pulls");
    await expect.element(page.getByText("Add browser regression coverage")).toBeVisible();
    await vi.waitFor(() => expect(document.querySelector(".list-body")).not.toBeNull());

    pressKey("?", { shift: true });
    await expect.element(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeVisible();

    pressKey("Escape");
    // Escape removes the dialog from the DOM (not just hides it). Poll the real
    // DOM for its removal, mirroring the jsdom source's
    // waitFor(() => expect(cheatsheetDialog()).toBeNull()).
    await vi.waitFor(() => expect(cheatsheetDialogEl()).toBeNull());
  });
});
