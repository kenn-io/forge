import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { prefersReducedMotion } from "./prefers-reduced-motion.svelte.js";

describe("prefersReducedMotion", () => {
  afterEach(() => vi.restoreAllMocks());

  it("returns false when matchMedia is unavailable (SSR)", () => {
    const orig = window.matchMedia;
    (window as { matchMedia: typeof window.matchMedia | undefined }).matchMedia = undefined;
    try {
      expect(prefersReducedMotion()).toBe(false);
    } finally {
      window.matchMedia = orig;
    }
  });
});
