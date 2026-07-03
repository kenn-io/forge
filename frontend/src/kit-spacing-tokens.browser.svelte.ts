// Guards the mechanism behind the gap-spacing migration: every migrated
// declaration is `gap: var(--space-N)`, and a typo'd or missing token does
// not error — the gap silently computes to `normal` (0) and the layout
// collapses. This mounts the real app with the real stylesheet chain
// (app.css -> kit theme.css) and asserts the full ladder resolves to its
// documented pixel values, plus one migrated consumer actually computing
// the ladder value rather than falling back.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";

const WAIT = 10_000;

// The kit spacing ladder contract: --space-1..8 in px.
const LADDER: Record<string, string> = {
  "--space-1": "2px",
  "--space-2": "4px",
  "--space-3": "6px",
  "--space-4": "8px",
  "--space-5": "12px",
  "--space-6": "16px",
  "--space-7": "24px",
  "--space-8": "32px",
};

describe("kit spacing ladder contract", () => {
  let mounted: MountedBrowserApp | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
  });

  it("resolves every ladder token and a migrated gap consumer", async () => {
    await page.viewport(1280, 800);
    mounted = await mountBrowserApp("/pulls");

    await vi.waitFor(() => {
      expect(document.querySelector(".brand")).not.toBeNull();
    }, WAIT);

    const rootStyle = getComputedStyle(document.documentElement);
    for (const [token, expected] of Object.entries(LADDER)) {
      expect(rootStyle.getPropertyValue(token).trim(), token).toBe(expected);
    }

    // AppHeader .brand declares gap: var(--space-3); an unresolvable token
    // would compute to "normal" here, not a pixel value.
    const brand = document.querySelector<HTMLElement>(".brand")!;
    expect(getComputedStyle(brand).columnGap).toBe(LADDER["--space-3"]);
  });
});
