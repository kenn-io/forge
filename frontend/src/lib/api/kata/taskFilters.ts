import type { KataTaskStatusFilter, KataTaskSummary } from "./taskTypes.js";

export function kataTaskStatusMatchesFilter(
  issue: Pick<KataTaskSummary, "status">,
  filter: KataTaskStatusFilter,
): boolean {
  if (filter === "all") return true;
  return issue.status === (filter === "ready" ? "open" : filter);
}
