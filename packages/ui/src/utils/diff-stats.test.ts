import { describe, expect, it } from "vitest";
import { formatDiffStat } from "./diff-stats.js";

describe("formatDiffStat", () => {
  it("keeps small values exact", () => {
    expect(formatDiffStat(0)).toBe("0");
    expect(formatDiffStat(216)).toBe("216");
    expect(formatDiffStat(999)).toBe("999");
  });

  it("uses compact thousands with up to three significant figures", () => {
    expect(formatDiffStat(1000)).toBe("1k");
    expect(formatDiffStat(1428)).toBe("1.43k");
    expect(formatDiffStat(2781)).toBe("2.78k");
    expect(formatDiffStat(9400)).toBe("9.4k");
    expect(formatDiffStat(11_000)).toBe("11k");
    expect(formatDiffStat(100_000)).toBe("100k");
    expect(formatDiffStat(250_000)).toBe("250k");
    expect(formatDiffStat(999_499)).toBe("999k");
  });

  it("uses compact millions", () => {
    expect(formatDiffStat(999_500)).toBe("1M");
    expect(formatDiffStat(1_500_000)).toBe("1.5M");
    expect(formatDiffStat(12_345_678)).toBe("12.3M");
  });
});
