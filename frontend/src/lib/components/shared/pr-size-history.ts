import type { PRDiffSizePoint } from "../../api/generated/models/pRDiffSizePoint.js";

export function prSizeSummary(
  history: readonly PRDiffSizePoint[],
  currentHead: string | undefined,
  currentBase?: string,
) {
  const totals = history.map((point) => point.additions + point.deletions);
  const first = totals[0];
  const latest = totals.at(-1);
  const past =
    history.length > 0 &&
    (history.at(-1)?.head_sha !== currentHead ||
      (currentBase !== undefined && history.at(-1)?.base_sha !== currentBase));
  let tone: "neutral" | "warning" | "success" = "neutral";
  if (!past && first !== undefined && latest !== undefined) {
    if (first === 0) {
      if (latest > 0) tone = "warning";
    } else if (latest * 2 >= first * 3) {
      tone = "warning";
    } else if (latest * 2 <= first) {
      tone = "success";
    }
  }
  let label = "PR diff size history unavailable";
  if (first !== undefined && latest !== undefined) {
    label =
      history.length === 1
        ? `PR diff size: ${latest} lines, 1 observed revision`
        : `PR diff size: ${first} to ${latest} lines, ${history.length} observed revisions`;
    if (past) label += ". Current revision unmeasured.";
    else if (tone === "warning") label += ". Warning.";
    else if (tone === "success") label += ". Success.";
  }
  const max = Math.max(1, ...totals);
  const points = totals.map((total, index) => ({
    total,
    x: totals.length === 1 ? 50 : 2 + (index / (totals.length - 1)) * 48,
    y: 18 - (total / max) * 16,
  }));
  return { label, tone, past, points };
}
