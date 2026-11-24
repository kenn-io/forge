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
  it("has no trend below two observations", () => {
    expect(prSizeSummary([], "a")).toBeNull();
    expect(prSizeSummary([point("a", 100, 20)], "a")).toBeNull();
  });
  it("plots total diff size over observed revisions, including shrinkage and zero", () => {
    const summary = prSizeSummary([point("a", 100, 20), point("b", 200, 40), point("c", 0)], "c")!;
    expect(summary.label).toBe("PR size: 0 changed lines · down 120 since the earliest revision shown");
    expect(summary.delta).toBe(-120);
    expect(summary.tooltip).toBe("Changed lines: 120 → 0 over 3 revisions");
    expect(summary.points.map((p) => p.total)).toEqual([120, 240, 0]);
    expect(summary.points.map((p) => p.x)).toEqual([3, 35, 67]);
  });
  it("marks history as past when the current head has no confirmed counts", () => {
    const history = [point("a", 50), point("b", 100)];
    expect(prSizeSummary(history, "b", "new-base")?.past).toBe(true);
    expect(prSizeSummary(history, "unmeasured")?.label).toBe("PR size: 100 changed lines · past revision");
  });
  it("describes growth in words so a net-deletion PR does not read as added lines", () => {
    const summary = prSizeSummary([point("a", 300, 854), point("b", 378, 1520)], "b")!;
    expect(summary.tooltip).toBe("Changed lines: 1,154 → 1,898 over 2 revisions");
    expect(summary.label).toBe("PR size: 1,898 changed lines · up 744 since the earliest revision shown");
  });
  it("calls an equal-size history unchanged", () => {
    expect(prSizeSummary([point("a", 50), point("b", 50)], "b")?.label).toBe(
      "PR size: 50 changed lines · unchanged since the earliest revision shown",
    );
  });
});
