// The issue detail action row is one kit AdaptiveActionGrid. Every control in
// it, including the compound Create Workspace split button, must sit at the
// grid's shared control height. The row used to be a hand-rolled flex row
// whose split button pinned its own 30px while the buttons beside it were
// 24px "sm" controls; only a real layout engine can show that they now match,
// so this runs in Chromium against the mounted app with fetch mocked at the
// network boundary.

import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";

const WAIT = 10_000;

describe("issue detail action grid", () => {
  vi.setConfig({ testTimeout: 30_000 });

  let mounted: MountedBrowserApp | null = null;

  beforeEach(async () => {
    await page.viewport(1280, 900);
  });

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  it("renders Create Workspace and Close issue at one shared control height", async () => {
    mounted = await mountBrowserApp("/issues/github/acme/widgets/7");

    const grid = page.getByRole("group", { name: "Issue actions" });
    await expect.element(grid).toBeVisible();
    const close = grid.getByRole("button", { name: "Close issue" });
    await expect.element(close).toBeVisible();

    await vi.waitFor(() => {
      const closeHeight = close.element().getBoundingClientRect().height;
      const primary = grid.getByRole("button", { name: "Create Workspace", exact: true }).element();
      const options = grid.getByRole("button", { name: "Create Workspace options" }).element();

      expect(closeHeight).toBeGreaterThan(0);
      expect(primary.getBoundingClientRect().height).toBeCloseTo(closeHeight, 1);
      expect(options.getBoundingClientRect().height).toBeCloseTo(closeHeight, 1);
      // Both segments are one control: the cap must not float above or below
      // the primary segment.
      expect(options.getBoundingClientRect().top).toBeCloseTo(primary.getBoundingClientRect().top, 1);
    }, WAIT);
  });
});
