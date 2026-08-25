import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";

const WAIT = 10_000;

describe("app spacing token wiring", () => {
  let mounted: MountedBrowserApp | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
  });

  it("resolves the brand gap through the kit theme", async () => {
    await page.viewport(1280, 800);
    mounted = await mountBrowserApp("/pulls");

    await vi.waitFor(() => {
      expect(document.querySelector(".brand")).not.toBeNull();
    }, WAIT);

    const brand = document.querySelector<HTMLElement>(".brand")!;
    expect(getComputedStyle(brand).columnGap).toBe("6px");
  });
});
