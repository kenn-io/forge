import { formatRelativeTime } from "@kenn-io/kit-ui";

import { workspaceItemState } from "./workspace-item-state.js";
import type { WorkspaceListItem } from "./workspace-list-schema.js";
import {
  workspacePullStatus,
  workspacePullStatusLines,
  type WorkspaceStatusNote,
  type WorkspaceStatusTone,
} from "./workspace-pull-status.js";

export interface WorkspaceHoverRow {
  readonly label: string;
  readonly value: string;
  readonly tone: WorkspaceStatusTone;
  readonly notes?: readonly WorkspaceStatusNote[];
  /** Set on the Changes row, which draws line counts in place of its value. */
  readonly diff?: { readonly additions: number; readonly deletions: number };
}

/** The rows of a workspace row's hover card: its linked item, then the workspace itself. */
export interface WorkspaceHoverDetails {
  readonly item: readonly WorkspaceHoverRow[];
  readonly workspace: readonly WorkspaceHoverRow[];
}

const itemStates: Record<string, { label: string; tone: WorkspaceStatusTone }> = {
  open: { label: "Open", tone: "success" },
  draft: { label: "Draft", tone: "warning" },
  merged: { label: "Merged", tone: "waiting" },
  closed: { label: "Closed", tone: "danger" },
  issue: { label: "Open", tone: "info" },
};

const agentStates: Record<string, { label: string; tone: WorkspaceStatusTone }> = {
  working: { label: "Working", tone: "success" },
  approval: { label: "Approval", tone: "warning" },
  input: { label: "Input", tone: "waiting" },
  done: { label: "Done", tone: "success" },
  idle: { label: "Idle", tone: "neutral" },
};

const workspaceStatuses: Record<string, { label: string; tone: WorkspaceStatusTone }> = {
  creating: { label: "Creating", tone: "neutral" },
  error: { label: "Error", tone: "danger" },
  deleting: { label: "Deleting", tone: "neutral" },
  deletion_failed: { label: "Deletion failed", tone: "danger" },
};

function row(label: string, value: string, tone: WorkspaceStatusTone = "neutral"): WorkspaceHoverRow {
  return { label, value, tone };
}

function linkedItemRows(workspace: WorkspaceListItem): WorkspaceHoverRow[] {
  if (workspace.item_type === "kata_task") {
    const id = workspace.kata?.short_id?.trim() || workspace.kata?.qualified_id?.trim();
    return id ? [row("Task", id)] : [];
  }
  const rows: WorkspaceHoverRow[] = [];
  if (workspace.item_type === "issue") {
    const state = itemStates[workspaceItemState(workspace, "issue")];
    rows.push(row("Issue", `#${workspace.item_number} ${state?.label ?? ""}`.trim(), state?.tone));
  }
  const pullNumber = workspace.item_type === "pull_request" ? workspace.item_number : workspace.associated_pr_number;
  if (pullNumber != null && pullNumber > 0) {
    const state = itemStates[workspaceItemState(workspace, "pr")];
    rows.push(row("Pull request", `#${pullNumber} ${state?.label ?? ""}`.trim(), state?.tone));
  }
  return rows;
}

function worktreeRow(workspace: WorkspaceListItem): WorkspaceHoverRow | null {
  const ahead = workspace.commits_ahead ?? 0;
  const behind = workspace.commits_behind ?? 0;
  const parts: string[] = [];
  if (ahead > 0 || behind > 0) {
    parts.push(`${ahead} ahead, ${behind} behind ${workspace.commits_vs_pr_head === true ? "PR head" : "upstream"}`);
  }
  if (workspace.worktree_dirty) parts.push("dirty");
  return parts.length > 0 ? row("Worktree", parts.join(", ")) : null;
}

function agentRow(workspace: WorkspaceListItem): WorkspaceHoverRow | null {
  const state = workspace.agent_state ? agentStates[workspace.agent_state] : undefined;
  if (!state) return null;
  const since = workspace.agent_state_updated_at ? `, ${formatRelativeTime(workspace.agent_state_updated_at)}` : "";
  const paneTitle = workspace.tmux_pane_title?.trim();
  return {
    label: "Agent",
    value: `${state.label}${since}`,
    tone: state.tone,
    ...(paneTitle ? { notes: [{ text: paneTitle, tone: "neutral" as const }] } : {}),
  };
}

function statusRow(workspace: WorkspaceListItem): WorkspaceHoverRow | null {
  const status = workspaceStatuses[workspace.status];
  if (!status) return null;
  const message = workspace.error_message?.trim();
  return {
    label: "Status",
    value: status.label,
    tone: status.tone,
    ...(message ? { notes: [{ text: message, tone: "neutral" as const }] } : {}),
  };
}

/** What a workspace row's hover card shows. hostName is set only when the fleet has more than one host. */
export function workspaceHoverDetails(
  workspace: WorkspaceListItem,
  hostName: string | undefined,
): WorkspaceHoverDetails {
  const pullStatus = workspacePullStatus(workspace);
  const additions = workspace.mr_additions ?? 0;
  const deletions = workspace.mr_deletions ?? 0;
  const author = workspace.item_author?.trim();
  const item: (WorkspaceHoverRow | null)[] = [
    ...linkedItemRows(workspace),
    author ? row("Author", author) : null,
    ...(pullStatus ? workspacePullStatusLines(pullStatus) : []),
    additions > 0 || deletions > 0 ? { ...row("Changes", ""), diff: { additions, deletions } } : null,
    pullStatus && pullStatus.commentCount > 0 ? row("Comments", String(pullStatus.commentCount)) : null,
    workspace.item_last_activity_at ? row("Updated", formatRelativeTime(workspace.item_last_activity_at)) : null,
  ];
  const details: (WorkspaceHoverRow | null)[] = [
    statusRow(workspace),
    agentRow(workspace),
    worktreeRow(workspace),
    hostName ? row("Host", hostName) : null,
    workspace.tmux_last_output_at ? row("Last output", formatRelativeTime(workspace.tmux_last_output_at)) : null,
    row("Created", formatRelativeTime(workspace.created_at)),
    row("Path", workspace.worktree_path),
  ];
  const present = (entry: WorkspaceHoverRow | null): entry is WorkspaceHoverRow => entry !== null;
  return { item: item.filter(present), workspace: details.filter(present) };
}
