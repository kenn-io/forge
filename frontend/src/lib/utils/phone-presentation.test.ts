import { describe, expect, it } from "vitest";
import { isPhoneLikeViewport } from "./phone-presentation.js";

describe("isPhoneLikeViewport", () => {
  it("treats a narrow viewport with either touch or a mobile user agent as a phone", () => {
    expect(isPhoneLikeViewport({ viewportWidth: 393, hasCoarsePointer: true, hasMobileUserAgent: false })).toBe(true);
    expect(isPhoneLikeViewport({ viewportWidth: 393, hasCoarsePointer: false, hasMobileUserAgent: true })).toBe(true);
    expect(isPhoneLikeViewport({ viewportWidth: 393, hasCoarsePointer: false, hasMobileUserAgent: false })).toBe(false);
  });

  it("keeps a phone rotated to landscape on the phone presentation", () => {
    expect(isPhoneLikeViewport({ viewportWidth: 851, hasCoarsePointer: true, hasMobileUserAgent: true })).toBe(true);
  });

  it("does not treat wide touch devices without a mobile user agent as phones", () => {
    expect(isPhoneLikeViewport({ viewportWidth: 851, hasCoarsePointer: true, hasMobileUserAgent: false })).toBe(false);
    expect(isPhoneLikeViewport({ viewportWidth: 851, hasCoarsePointer: false, hasMobileUserAgent: true })).toBe(false);
    expect(isPhoneLikeViewport({ viewportWidth: 1280, hasCoarsePointer: true, hasMobileUserAgent: true })).toBe(false);
  });
});
