import { describe, expect, it } from "vitest";
import { prSizeSummary } from "./pr-size-history";

const point = (head_sha: string, additions: number, deletions = 0) => ({
  head_sha,
  base_sha: "base",
  base_branch: "main",
  additions,
  deletions,
  observed_at: "2026-10-27T12:00:00Z",
});

describe("PR size history", () => {
  it("reports unavailable and single observations without inventing a trend", () => {
    expect(prSizeSummary([], "a").label).toBe("PR size: no history");
    const single = prSizeSummary([point("a", 100, 20)], "a");
    expect(single.label).toBe("PR size: 120 lines · 1 revision");
    expect(single.delta).toBeNull();
    expect(single.points).toHaveLength(1);
  });
  it("plots total diff size over observed revisions, including shrinkage and zero", () => {
    const summary = prSizeSummary([point("a", 100, 20), point("b", 200, 40), point("c", 0)], "c");
    expect(summary.label).toBe("PR size: 0 lines · −120 since first observation");
    expect(summary.delta).toBe(-120);
    expect(summary.points.map((p) => p.total)).toEqual([120, 240, 0]);
    expect(summary.points.map((p) => p.x)).toEqual([3, 35, 67]);
  });
  it("marks history as past when the current head has no confirmed counts", () => {
    expect(prSizeSummary([point("a", 100)], "a", "new-base").past).toBe(true);
    expect(prSizeSummary([point("a", 100)], "unmeasured").label).toBe("PR size: 100 lines · past revision");
  });
});
