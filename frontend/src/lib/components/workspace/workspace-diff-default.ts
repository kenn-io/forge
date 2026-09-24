import type { WorkspaceDiffBase } from "../../stores/diff.svelte.js";

export interface WorkspaceDiffGitState {
  readonly worktreeDirty?: boolean | undefined;
  readonly commitsAhead?: number | undefined;
  readonly commitsVsPRHead?: boolean | undefined;
  readonly branchUpstreamMissing?: boolean | undefined;
}

// Open the diff on the narrowest base that still shows pending work:
// uncommitted edits against HEAD, unpushed commits against the pushed
// branch, and otherwise the whole change against the merge target.
// Returns null until git-state enrichment has reported the worktree.
export function defaultWorkspaceDiffBase(
  state: WorkspaceDiffGitState,
  hasMergeTarget: boolean,
): WorkspaceDiffBase | null {
  if (state.worktreeDirty === undefined) return null;
  if (state.worktreeDirty) return "head";
  // The pushed base diffs against @{upstream}; counts measured against a
  // fork PR head or a missing tracking ref have no upstream to compare with.
  const hasUpstream = state.commitsVsPRHead !== true && state.branchUpstreamMissing !== true;
  if (hasUpstream && (state.commitsAhead ?? 0) > 0) return "pushed";
  return hasMergeTarget ? "merge-target" : "head";
}
