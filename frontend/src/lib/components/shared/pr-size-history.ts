import type { PRDiffSizePoint } from "../../api/generated/models/pRDiffSizePoint.js";

/** Returns null below two observations, where there is no trend to draw. */
export function prSizeSummary(
  history: readonly PRDiffSizePoint[],
  currentHead: string | undefined,
  currentBase?: string,
) {
  if (history.length < 2) return null;
  const totals = history.map((point) => point.additions + point.deletions);
  const first = totals[0]!;
  const latest = totals.at(-1)!;
  const last = history.at(-1)!;
  const past = last.head_sha !== currentHead || (currentBase !== undefined && last.base_sha !== currentBase);
  const delta = latest - first;
  const signed = delta < 0 ? `−${Math.abs(delta)}` : `+${delta}`;
  const label = past
    ? `PR size: ${latest} lines · past revision`
    : `PR size: ${latest} lines · ${signed} since first observation`;
  const max = Math.max(1, ...totals);
  let tone = "neutral";
  if (!past && first !== latest) {
    if (latest * 2 >= first * 3) tone = "warning";
    else if (latest * 2 <= first) tone = "success";
  }
  const points = totals.map((total, index) => ({
    total,
    x: 3 + (index / (totals.length - 1)) * 64,
    y: 21 - (total / max) * 18,
  }));
  return { label, tooltip: `${signed} lines since open`, delta, past, points, tone };
}
