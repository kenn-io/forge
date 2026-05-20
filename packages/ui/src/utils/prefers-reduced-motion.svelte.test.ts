import { afterEach, describe, expect, it, vi } from "vitest";
import { prefersReducedMotion } from "./prefers-reduced-motion.svelte.js";

function stubMatchMedia(matches: boolean) {
  vi.spyOn(window, "matchMedia").mockReturnValue({ matches } as MediaQueryList);
}

describe("prefersReducedMotion", () => {
  afterEach(() => vi.restoreAllMocks());

  it("returns the current matchMedia value", () => {
    stubMatchMedia(true);
    expect(prefersReducedMotion()).toBe(true);
    stubMatchMedia(false);
    expect(prefersReducedMotion()).toBe(false);
  });

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
