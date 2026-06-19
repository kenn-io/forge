import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { kbdAriaLabel, kbdGlyph } from "./useKbdLabel.js";

describe("kbdGlyph", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders the space key as 'Space'", () => {
    expect(kbdGlyph({ key: " " })).toBe("Space");
  });

  it("uppercases a single character key", () => {
    expect(kbdGlyph({ key: "a" })).toBe("A");
  });

  it("passes a named key through unchanged", () => {
    expect(kbdGlyph({ key: "ArrowRight" })).toBe("ArrowRight");
  });

  it("keeps macOS modifier glyph text compact", () => {
    vi.stubGlobal("navigator", {
      platform: "MacIntel",
      userAgent: "Mac",
    });

    expect(kbdGlyph({ key: "k", ctrlOrMeta: true })).toBe("⌘K");
  });
});

describe("kbdAriaLabel", () => {
  it("labels the space key as 'Space'", () => {
    expect(kbdAriaLabel({ key: " " })).toBe("Space");
  });

  it("labels a plain key by its key name", () => {
    expect(kbdAriaLabel({ key: "ArrowRight" })).toBe("ArrowRight");
  });
});
