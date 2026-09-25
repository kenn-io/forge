import { describe, expect, it } from "vite-plus/test";
import { applyGroupRenderBudget, effectiveRenderBudget } from "./render-budget.js";

const group = (key: string, size: number, collapsed = false) => ({
  key,
  collapsed,
  items: Array.from({ length: size }, (_, index) => `${key}${index}`),
});

describe("applyGroupRenderBudget", () => {
  it("mounts rows across groups in display order up to the budget", () => {
    const result = applyGroupRenderBudget([group("a", 3), group("b", 4), group("c", 2)], 5);

    expect(result.truncated).toBe(true);
    expect(result.groups.map((g) => [g.key, g.items])).toEqual([
      ["a", ["a0", "a1", "a2"]],
      ["b", ["b0", "b1"]],
    ]);
  });

  it("does not charge collapsed groups against the budget", () => {
    const result = applyGroupRenderBudget([group("a", 50, true), group("b", 2)], 2);

    expect(result.truncated).toBe(false);
    expect(result.groups.map((g) => [g.key, g.items.length])).toEqual([
      ["a", 50],
      ["b", 2],
    ]);
  });

  it("reports no truncation when everything fits", () => {
    expect(applyGroupRenderBudget([group("a", 2)], 2).truncated).toBe(false);
  });
});

describe("effectiveRenderBudget", () => {
  it("extends the budget to mount a selected row beyond it", () => {
    expect(effectiveRenderBudget(100, -1)).toBe(100);
    expect(effectiveRenderBudget(100, 10)).toBe(100);
    expect(effectiveRenderBudget(100, 500)).toBeGreaterThan(500);
  });
});
