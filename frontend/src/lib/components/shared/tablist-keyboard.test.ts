import { describe, expect, it } from "vite-plus/test";
import { tablistKeyTarget } from "./tablist-keyboard.js";

describe("tablistKeyTarget", () => {
  it("moves along the strip with the arrow keys and wraps at both ends", () => {
    expect(tablistKeyTarget("ArrowRight", 0, 3)).toBe(1);
    expect(tablistKeyTarget("ArrowRight", 2, 3)).toBe(0);
    expect(tablistKeyTarget("ArrowLeft", 1, 3)).toBe(0);
    expect(tablistKeyTarget("ArrowLeft", 0, 3)).toBe(2);
  });

  it("jumps to the ends with Home and End", () => {
    expect(tablistKeyTarget("Home", 2, 3)).toBe(0);
    expect(tablistKeyTarget("End", 0, 3)).toBe(2);
  });

  it("leaves every other key to the browser", () => {
    expect(tablistKeyTarget("Tab", 0, 3)).toBeNull();
    expect(tablistKeyTarget("ArrowDown", 0, 3)).toBeNull();
    expect(tablistKeyTarget("ArrowRight", 0, 0)).toBeNull();
  });
});
