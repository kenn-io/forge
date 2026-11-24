import type { PRDiffSizePoint } from "../../api/generated/models/pRDiffSizePoint.js";

const count = (value: number) => value.toLocaleString("en-US");

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
  // Size is additions plus deletions. A signed "+N" would read as N added
  // lines next to the diff stats, so describe the change in words.
  let change = "unchanged";
  if (delta > 0) change = `up ${count(delta)}`;
  else if (delta < 0) change = `down ${count(-delta)}`;
  const label = past
    ? `PR size: ${count(latest)} changed lines · past revision`
    : `PR size: ${count(latest)} changed lines · ${change} since the earliest revision shown`;
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
  const tooltip = `Changed lines: ${count(first)} → ${count(latest)} over ${history.length} revisions`;
  return { label, tooltip, delta, past, points, tone };
}
