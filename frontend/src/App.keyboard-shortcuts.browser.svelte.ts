// Browser-tier analog of App.keyboard-shortcuts.test.ts. The global shortcuts
// served by the keyboard registry -- j/k list navigation, the Cmd+[ sidebar
// toggle, and the routes where Cmd+[ is reserved (consumed without toggling)
// because no sidebar target exists -- are exercised through the real app shell
// with the API mocked at the fetch boundary.
//
// A real Chromium page provides matchMedia/ResizeObserver/IntersectionObserver/
// canvas natively, so the jsdom installAppDomGlobals() shim is gone; the browser
// harness stubs only EventSource.
//
// Two translation notes:
//   - The jsdom harness simulated layout width via mountApp's `viewportWidth`
//     option (jsdom has no real layout). The container store classifies #app's
//     clientWidth: <500px is "narrow", and the PR-list / Activity views render a
//     different mobile DOM in narrow mode (no .sidebar). The browser analog is
//     sizing the real Chromium viewport: the desktop cases set a wide viewport so
//     the sidebar renders, and the compact case sets a 480px viewport (mirroring
//     the jsdom `{ viewportWidth: 480 }`).
//   - The .pr-list-row / [data-test='sidebar'] / data-collapsed / .palette-row
//     selectors all survive in the rendered browser DOM (verified), so the
//     DOM-shape assertions stay as querySelector against the real DOM. The page
//     locator API (getByText/getByRole/getByTitle/getByTestId) drives the
//     visibility/role assertions where natural.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import {
  mountBrowserApp,
  pressKey,
  resetKeyboardModuleState,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";

describe("migrated global shortcuts", () => {
  vi.setConfig({ testTimeout: 20_000 });

  let mounted: MountedBrowserApp | null = null;

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("j and k navigate the PR list", async () => {
    // Desktop width so the PR list renders inside the sidebar with .pr-list-row
    // rows (the narrow mobile layout uses a different DOM without them).
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/pulls");
    await vi.waitFor(() => expect(document.querySelector(".pr-list-row")).not.toBeNull());

    pressKey("j");
    await vi.waitFor(() => expect(document.querySelector(".pr-list-row.selected")).not.toBeNull());

    pressKey("k");
    await vi.waitFor(() => expect(document.querySelector(".pr-list-row.selected")).not.toBeNull());
  });

  it("Cmd+[ toggles the sidebar", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/pulls");
    const sidebar = await vi.waitFor(() => {
      const el = document.querySelector("[data-test='sidebar']");
      expect(el).not.toBeNull();
      return el!;
    });
    const wasCollapsed = sidebar.getAttribute("data-collapsed") === "true";

    pressKey("[", { meta: true });

    await vi.waitFor(() =>
      expect(document.querySelector("[data-test='sidebar']")?.getAttribute("data-collapsed")).toBe(
        (!wasCollapsed).toString(),
      ),
    );
  });

  it("Cmd+[ is reserved on Activity without toggling the sidebar", async () => {
    // Desktop width so the collapsed sidebar strip (with its Expand button) is
    // present on Activity; in narrow mode there is no sidebar at all.
    await page.viewport(1280, 900);
    localStorage.setItem("middleman-sidebar", "collapsed");
    mounted = await mountBrowserApp("/");

    await expect.element(page.getByRole("button", { name: "Expand sidebar" })).toBeVisible();
    expect(document.querySelectorAll("header kbd[aria-label$='-[']")).toHaveLength(0);

    // Toggle sidebar must not be offered in the palette on this page.
    pressKey("k", { meta: true });
    await expect.element(page.getByRole("dialog", { name: "Command palette" })).toBeVisible();
    const palette = document.querySelector<HTMLElement>("[role='dialog'][aria-label='Command palette']");
    expect(palette).not.toBeNull();
    expect(
      Array.from(palette!.querySelectorAll(".palette-row")).filter((row) =>
        row.textContent?.includes("Toggle sidebar"),
      ),
    ).toHaveLength(0);
    pressKey("Escape");
    await vi.waitFor(() => expect(document.querySelector("[role='dialog'][aria-label='Command palette']")).toBeNull());

    // Nor in the cheatsheet.
    pressKey("?", { shift: true });
    await expect.element(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeVisible();
    const cheatsheet = document.querySelector<HTMLElement>("[role='dialog'][aria-label='Keyboard shortcuts']");
    expect(cheatsheet).not.toBeNull();
    expect(cheatsheet!.textContent).not.toContain("Toggle sidebar");
    pressKey("Escape");
    await vi.waitFor(() =>
      expect(document.querySelector("[role='dialog'][aria-label='Keyboard shortcuts']")).toBeNull(),
    );

    // The chord is still consumed (reserved) so the browser default never
    // fires, but the collapsed sidebar stays collapsed.
    const event = pressKey("[", { meta: true });
    expect(event.defaultPrevented).toBe(true);
    await expect.element(page.getByRole("button", { name: "Expand sidebar" })).toBeVisible();
  });

  it("Cmd+[ is reserved on compact PR list without toggling persisted sidebar state", async () => {
    // 480px viewport mirrors the jsdom `{ viewportWidth: 480 }`: #app classifies
    // as "narrow", the PR list renders the compact mobile layout, and no .sidebar
    // exists for Cmd+[ to toggle.
    await page.viewport(480, 900);
    mounted = await mountBrowserApp("/pulls");
    await vi.waitFor(() => expect(document.body.textContent).toContain("Add browser regression coverage"));

    expect(document.querySelectorAll(".sidebar")).toHaveLength(0);
    localStorage.removeItem("middleman-sidebar");

    const event = pressKey("[", { meta: true });

    expect(event.defaultPrevented).toBe(true);
    expect(localStorage.getItem("middleman-sidebar")).toBeNull();
  });
});
