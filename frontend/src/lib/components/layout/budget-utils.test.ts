import { describe, expect, it } from "vite-plus/test";
import { budgetColor, worstCaseRatio, aggregateBudget, formatCompact, type HostBudgetEntry } from "./budget-utils";

describe("budgetColor", () => {
  it("returns green when above 20%", () => {
    expect(budgetColor(0.5)).toBe("var(--budget-green)");
  });
  it("returns yellow between 5% and 20%", () => {
    expect(budgetColor(0.15)).toBe("var(--budget-yellow)");
  });
  it("returns red below 5%", () => {
    expect(budgetColor(0.03)).toBe("var(--budget-red)");
  });
  it("returns yellow at exactly 20% boundary", () => {
    expect(budgetColor(0.2)).toBe("var(--budget-yellow)");
  });
  it("returns red at exactly 5% boundary", () => {
    expect(budgetColor(0.05)).toBe("var(--budget-red)");
  });
});

describe("worstCaseRatio", () => {
  it("picks lowest ratio from known hosts", () => {
    const entries: HostBudgetEntry[] = [
      { remaining: 4000, limit: 5000, known: true },
      { remaining: 500, limit: 5000, known: true },
    ];
    expect(worstCaseRatio(entries)).toBeCloseTo(0.1);
  });

  it("excludes unknown hosts", () => {
    const entries: HostBudgetEntry[] = [
      { remaining: 4000, limit: 5000, known: true },
      { remaining: -1, limit: -1, known: false },
    ];
    expect(worstCaseRatio(entries)).toBeCloseTo(0.8);
  });

  it("returns -1 when all hosts unknown", () => {
    const entries: HostBudgetEntry[] = [{ remaining: -1, limit: -1, known: false }];
    expect(worstCaseRatio(entries)).toBe(-1);
  });

  it("returns -1 for empty array", () => {
    expect(worstCaseRatio([])).toBe(-1);
  });

  it("excludes stale trackers with known=true but remaining=-1", () => {
    const entries: HostBudgetEntry[] = [
      { remaining: 4000, limit: 5000, known: true },
      { remaining: -1, limit: 5000, known: true }, // stale after window reset
    ];
    expect(worstCaseRatio(entries)).toBeCloseTo(0.8);
  });
});

describe("aggregateBudget", () => {
  it("sums budget from enabled hosts", () => {
    const result = aggregateBudget([
      { budget_limit: 500, budget_spent: 42 },
      { budget_limit: 300, budget_spent: 10 },
    ]);
    expect(result).toEqual({ spent: 52, limit: 800, hasAny: true });
  });

  it("excludes disabled hosts", () => {
    const result = aggregateBudget([
      { budget_limit: 500, budget_spent: 42 },
      { budget_limit: 0, budget_spent: 0 },
    ]);
    expect(result).toEqual({ spent: 42, limit: 500, hasAny: true });
  });

  it("includes unknown hosts with budget enabled (budget is independent of rate observation)", () => {
    const result = aggregateBudget([
      { budget_limit: 500, budget_spent: 42 },
      { budget_limit: 500, budget_spent: 10 },
    ]);
    expect(result).toEqual({ spent: 52, limit: 1000, hasAny: true });
  });

  it("returns hasAny false when all disabled", () => {
    const result = aggregateBudget([{ budget_limit: 0, budget_spent: 0 }]);
    expect(result).toEqual({ spent: 0, limit: 0, hasAny: false });
  });
});

describe("formatCompact", () => {
  it("returns number as-is below 1000", () => {
    expect(formatCompact(75)).toBe("75");
    expect(formatCompact(0)).toBe("0");
    expect(formatCompact(999)).toBe("999");
  });
  it("formats even thousands without decimal", () => {
    expect(formatCompact(5000)).toBe("5k");
    expect(formatCompact(1000)).toBe("1k");
    expect(formatCompact(10000)).toBe("10k");
  });
  it("formats non-even thousands with one decimal", () => {
    expect(formatCompact(4200)).toBe("4.2k");
    expect(formatCompact(4800)).toBe("4.8k");
    expect(formatCompact(1500)).toBe("1.5k");
  });
});
