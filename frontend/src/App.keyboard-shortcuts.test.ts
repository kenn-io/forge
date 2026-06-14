// Global shortcuts served by the keyboard registry — j/k list navigation,
// the Cmd+[ sidebar toggle, and the routes where Cmd+[ is reserved
// (consumed without toggling) because no sidebar target exists.

import { cleanup, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { installAppDomGlobals, mountApp, pressKey, resetKeyboardModuleState } from "./test/appHarness.js";

describe("migrated global shortcuts", () => {
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

  it("j and k navigate the PR list", async () => {
    await mountApp("/pulls");
    await waitFor(() => expect(document.querySelector(".pr-list-row")).not.toBeNull());

    pressKey("j");
    await waitFor(() => expect(document.querySelector(".pr-list-row.selected")).not.toBeNull());

    pressKey("k");
    await waitFor(() => expect(document.querySelector(".pr-list-row.selected")).not.toBeNull());
  });

  it("Cmd+[ toggles the sidebar", async () => {
    await mountApp("/pulls");
    const sidebar = await waitFor(() => {
      const el = document.querySelector("[data-test='sidebar']");
      expect(el).not.toBeNull();
      return el!;
    });
    const wasCollapsed = sidebar.getAttribute("data-collapsed") === "true";

    pressKey("[", { meta: true });

    await waitFor(() =>
      expect(document.querySelector("[data-test='sidebar']")?.getAttribute("data-collapsed")).toBe(
        (!wasCollapsed).toString(),
      ),
    );
  });

  it("Cmd+[ is reserved on Activity without toggling the sidebar", async () => {
    localStorage.setItem("middleman-sidebar", "collapsed");
    await mountApp("/");

    const expandButton = await waitFor(() => screen.getByRole("button", { name: "Expand sidebar" }));
    expect(document.querySelectorAll("header kbd[aria-label$='-[']")).toHaveLength(0);

    // Toggle sidebar must not be offered in the palette on this page.
    pressKey("k", { meta: true });
    const palette = await waitFor(() => screen.getByRole("dialog", { name: "Command palette" }));
    expect(
      Array.from(palette.querySelectorAll(".palette-row")).filter((row) => row.textContent?.includes("Toggle sidebar")),
    ).toHaveLength(0);
    pressKey("Escape");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull());

    // Nor in the cheatsheet.
    pressKey("?", { shift: true });
    const cheatsheet = await waitFor(() => screen.getByRole("dialog", { name: "Keyboard shortcuts" }));
    expect(cheatsheet.textContent).not.toContain("Toggle sidebar");
    pressKey("Escape");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Keyboard shortcuts" })).toBeNull());

    // The chord is still consumed (reserved) so the browser default never
    // fires, but the collapsed sidebar stays collapsed.
    const event = pressKey("[", { meta: true });
    expect(event.defaultPrevented).toBe(true);
    expect(screen.getByRole("button", { name: "Expand sidebar" })).toBeTruthy();
  });

  it("Cmd+[ is reserved on compact PR list without toggling persisted sidebar state", async () => {
    await mountApp("/pulls", { viewportWidth: 480 });
    await waitFor(() => expect(document.body.textContent).toContain("Add browser regression coverage"));

    expect(document.querySelectorAll(".sidebar")).toHaveLength(0);
    localStorage.removeItem("middleman-sidebar");

    const event = pressKey("[", { meta: true });

    expect(event.defaultPrevented).toBe(true);
    expect(localStorage.getItem("middleman-sidebar")).toBeNull();
  });
});
