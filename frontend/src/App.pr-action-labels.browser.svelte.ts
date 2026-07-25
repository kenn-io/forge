import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";

describe("pull request action labels", () => {
  vi.setConfig({ testTimeout: 20_000 });

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

  it("keeps trimmed button-label ink visible", async () => {
    mounted = await mountBrowserApp("/pulls/github/acme/widgets/42");

    for (const name of ["Approve", "Merge", "Close", "Create Workspace"]) {
      const button = page.getByRole("button", { name, exact: true });
      await expect.element(button).toBeVisible();

      const visibleLabel = Array.from(
        button.element().querySelectorAll<HTMLElement>(".kit-button__label, .kit-button__short-label"),
      ).find((label) => getComputedStyle(label).display !== "none");

      expect(visibleLabel, `${name} should have a visible Kit UI label`).toBeDefined();
      expect(getComputedStyle(visibleLabel!).overflowY, `${name} should not clip trimmed glyphs`).toBe("visible");
    }
  });
});
