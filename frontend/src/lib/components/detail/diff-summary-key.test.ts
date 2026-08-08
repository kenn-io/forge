import { describe, expect, it } from "vite-plus/test";
import { buildDiffSummaryKey } from "./diff-summary-key.js";

describe("buildDiffSummaryKey", () => {
  it("changes when either platform SHAs or stored diff range changes", () => {
    const pr = {
      UpdatedAt: "2026-04-28T12:00:00Z",
      Additions: 10,
      Deletions: 2,
    };
    const initialDetail = {
      platform_head_sha: "head-1",
      platform_base_sha: "base-1",
      diff_head_sha: "head-1",
      merge_base_sha: "merge-base-1",
    };
    const initial = buildDiffSummaryKey("acme", "widget", 42, initialDetail, pr);

    for (const detail of [
      { ...initialDetail, platform_head_sha: "head-2" },
      { ...initialDetail, platform_base_sha: "base-2" },
      { ...initialDetail, diff_head_sha: "head-2" },
      { ...initialDetail, merge_base_sha: "merge-base-2" },
    ]) {
      expect(buildDiffSummaryKey("acme", "widget", 42, detail, pr)).not.toBe(initial);
    }
  });
});
