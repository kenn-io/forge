// Guards the CSS contract behind the popover-chrome migration: docs menus,
// context menus, pickers, and the approve popover dropped their local
// border/radius/shadow rules for the kit-popover-card class that
// @kenn-io/kit-ui/theme.css defines (imported at the top of app.css). If
// that class or import ever went missing, every popover would render as an
// unframed transparent box — this mounts the real stylesheet chain and
// asserts the class actually computes to framed surface chrome.

import { afterEach, describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";

describe("kit-popover-card contract", () => {
  let mounted: MountedBrowserApp | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
  });

  it("computes to a framed elevated surface in the real stylesheet", async () => {
    await page.viewport(1280, 800);
    mounted = await mountBrowserApp("/pulls");

    const card = document.createElement("div");
    card.className = "kit-popover-card";
    document.body.appendChild(card);
    try {
      const style = getComputedStyle(card);
      // Opaque surface, not transparent fallthrough.
      expect(style.backgroundColor).toMatch(/^rgb\(/);
      expect(style.borderTopWidth).toBe("1px");
      expect(style.borderTopStyle).toBe("solid");
      // --radius-md and --shadow-lg resolved to real values.
      expect(style.borderTopLeftRadius).toMatch(/px$/);
      expect(style.borderTopLeftRadius).not.toBe("0px");
      expect(style.boxShadow).toContain("rgba");
    } finally {
      card.remove();
    }
  });
});
