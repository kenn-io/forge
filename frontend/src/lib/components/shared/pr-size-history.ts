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
  const delta = first !== undefined && latest !== undefined && history.length > 1 ? latest - first : null;
  const signed = delta === null ? "" : delta < 0 ? `−${Math.abs(delta)}` : `+${delta}`;
  const label =
    latest === undefined
      ? "PR size: no history"
      : past
        ? `PR size: ${latest} lines · past revision`
        : delta === null
          ? `PR size: ${latest} lines · 1 revision`
          : `PR size: ${latest} lines · ${signed} since first observation`;
  const max = Math.max(1, ...totals);
  let tone = "neutral";
  if (!past && first !== undefined && latest !== undefined && first !== latest) {
    if (latest * 2 >= first * 3) tone = "warning";
    else if (latest * 2 <= first) tone = "success";
  }
  const points = totals.map((total, index) => ({
    total,
    x: totals.length === 1 ? 35 : 3 + (index / (totals.length - 1)) * 64,
    y: 21 - (total / max) * 18,
  }));
  return { label, delta, past, points, tone };
}
