import type { KanbanStatus, PullRequest } from "../api/types.js";

export type WorkflowGroup = KanbanStatus | "closed";

export const workflowGroupOrder: WorkflowGroup[] = ["new", "reviewing", "waiting", "awaiting_merge", "closed"];

export const workflowGroupLabels: Record<WorkflowGroup, string> = {
  new: "New",
  reviewing: "Reviewing",
  waiting: "Waiting",
  awaiting_merge: "Awaiting Merge",
  closed: "Closed",
};

export interface WorkflowGroupEntry {
  group: WorkflowGroup;
  label: string;
  items: PullRequest[];
}

export function normalizeKanbanStatus(status: string | undefined): KanbanStatus {
  if (status === "new" || status === "reviewing" || status === "waiting" || status === "awaiting_merge") {
    return status;
  }
  return "new";
}

/**
 * Classify a PR into the Status grouping used by PR lists.
 *
 * Status grouping mirrors the kanban board, so worktree linkage stays item
 * metadata/actions and does not override the user's review status.
 */
export function classifyPR(pr: PullRequest): WorkflowGroup {
  if (pr.State === "closed" || pr.State === "merged") {
    return "closed";
  }
  return normalizeKanbanStatus(pr.KanbanStatus);
}

/**
 * Group PRs by kanban status.
 *
 * Returns groups in display order, omitting empty groups.
 * Items within each group are sorted by LastActivityAt
 * descending.
 */
export function groupByWorkflow(prs: PullRequest[]): WorkflowGroupEntry[] {
  const buckets = new Map<WorkflowGroup, PullRequest[]>();
  for (const g of workflowGroupOrder) {
    buckets.set(g, []);
  }

  for (const pr of prs) {
    const group = classifyPR(pr);
    buckets.get(group)!.push(pr);
  }

  const result: WorkflowGroupEntry[] = [];
  for (const group of workflowGroupOrder) {
    const items = buckets.get(group)!;
    if (items.length === 0) continue;
    items.sort((a, b) => new Date(b.LastActivityAt).getTime() - new Date(a.LastActivityAt).getTime());
    result.push({
      group,
      label: workflowGroupLabels[group],
      items,
    });
  }

  return result;
}
