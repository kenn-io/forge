import { describe, expect, it } from "vitest";
import { prSizeSummary } from "./pr-size-history";

const point = (head_sha: string, additions: number, deletions = 0) => ({
  head_sha,
  base_sha: "base",
  additions,
  deletions,
  observed_at: "2026-10-27T12:00:00Z",
});

describe("PR size history", () => {
  it("reports unavailable and single observations without inventing a trend", () => {
    expect(prSizeSummary([], "a").label).toBe("PR diff size history unavailable");
    const single = prSizeSummary([point("a", 100, 20)], "a");
    expect(single.label).toBe("PR diff size: 120 lines, 1 observed revision");
    expect(single.points).toHaveLength(1);
    expect(single.tone).toBe("neutral");
  });
  it("plots total diff size, including shrinkage and zero", () => {
    const summary = prSizeSummary([point("a", 100, 20), point("b", 200, 40), point("c", 0)], "c");
    expect(summary.label).toBe("PR diff size: 120 to 0 lines, 3 observed revisions. Success.");
    expect(summary.points.map((p) => p.total)).toEqual([120, 240, 0]);
    expect(summary.points.map((p) => p.x)).toEqual([2, 26, 50]);
    expect(summary.points.at(-1)?.y).toBe(18);
  });
  it("marks unmeasured current head or base without classifying stale data", () => {
    expect(prSizeSummary([point("a", 100)], "a", "new-base").past).toBe(true);
    expect(prSizeSummary([point("a", 100)], "unmeasured").label).toBe(
      "PR diff size: 100 lines, 1 observed revision. Current revision unmeasured.",
    );
    expect(prSizeSummary([point("a", 100), point("b", 200)], "unmeasured").tone).toBe("neutral");
  });
  it.each([
    [49, "success"],
    [50, "success"],
    [51, "neutral"],
    [100, "neutral"],
    [149, "neutral"],
    [150, "warning"],
    [151, "warning"],
  ])("uses inclusive thresholds for 100 to %i lines", (latest, tone) => {
    expect(prSizeSummary([point("a", 100), point("b", latest)], "b").tone).toBe(tone);
  });
  it("handles zero initial size without a ratio or invented baseline", () => {
    expect(prSizeSummary([point("a", 0), point("b", 0)], "b").tone).toBe("neutral");
    expect(prSizeSummary([point("a", 0), point("b", 1)], "b").tone).toBe("warning");
    expect(prSizeSummary([point("a", 100), point("b", 0)], "b").tone).toBe("success");
  });
  it("keeps the zero anchor when every observation is above zero", () => {
    const summary = prSizeSummary([point("a", 100), point("b", 110)], "b");
    expect(summary.points[0]?.y).toBeGreaterThan(2);
    expect(summary.points[1]?.y).toBe(2);
  });
});
