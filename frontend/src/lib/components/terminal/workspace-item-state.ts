// One lifecycle color rule for the linked-item badge on every workspace
// surface: the desktop sidebar bubble, the phone list badge, and the phone
// terminal pill. The precedence matches the PR list and PR detail, where a
// merged or closed PR outranks its draft flag.
export type WorkspaceItemState = "open" | "draft" | "merged" | "closed" | "issue" | "kata" | "unknown";

export function workspaceItemState(
  workspace: {
    readonly item_type: string;
    readonly mr_state?: string | null | undefined;
    readonly mr_is_draft?: boolean | null | undefined;
  },
  linkedItemType: "pr" | "issue",
): WorkspaceItemState {
  if (workspace.item_type === "kata_task") return "kata";
  if (linkedItemType === "issue") return workspace.mr_state === "closed" ? "closed" : "issue";
  // An issue workspace's mr_state describes the issue, not its associated
  // PR, so the PR's lifecycle is unknown here.
  if (workspace.item_type === "issue") return "unknown";
  if (workspace.mr_state === "merged") return "merged";
  if (workspace.mr_state === "closed") return "closed";
  if (workspace.mr_is_draft) return "draft";
  return "open";
}

// Issues and Kata tasks use blue so they read apart from an open PR's green.
export const workspaceItemAccent: Record<WorkspaceItemState, string> = {
  open: "var(--accent-green)",
  draft: "var(--accent-amber)",
  merged: "var(--accent-purple)",
  closed: "var(--accent-red)",
  issue: "var(--accent-blue)",
  kata: "var(--accent-blue)",
  unknown: "var(--text-muted)",
};
