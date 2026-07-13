import { describe, expect, it } from "vitest";
import { getScrollIndicatorGeometry } from "./scrollIndicator.js";

describe("getScrollIndicatorGeometry", () => {
  it("hides the indicator when content fits", () => {
    expect(getScrollIndicatorGeometry(200, 200, 0)).toEqual({
      scrollable: false,
      height: 0,
      top: 0,
    });
  });

  it("sizes and positions the indicator within the viewport", () => {
    expect(getScrollIndicatorGeometry(100, 400, 150)).toEqual({
      scrollable: true,
      height: 25,
      top: 37.5,
    });
  });

  it("keeps a usable minimum thumb and clamps overscroll", () => {
    expect(getScrollIndicatorGeometry(100, 1000, 1200)).toEqual({
      scrollable: true,
      height: 24,
      top: 76,
    });
  });
});
