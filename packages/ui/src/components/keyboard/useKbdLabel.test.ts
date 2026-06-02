import { describe, expect, it } from "vite-plus/test";

import { kbdAriaLabel, kbdGlyph } from "./useKbdLabel.js";

describe("kbdGlyph", () => {
  it("renders the space key as 'Space'", () => {
    expect(kbdGlyph({ key: " " })).toBe("Space");
  });

  it("uppercases a single character key", () => {
    expect(kbdGlyph({ key: "a" })).toBe("A");
  });

  it("passes a named key through unchanged", () => {
    expect(kbdGlyph({ key: "ArrowRight" })).toBe("ArrowRight");
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
