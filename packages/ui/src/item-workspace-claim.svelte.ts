import {
  identityEquals,
  type InlineWorkspaceController,
  type WorkspaceItemIdentity,
  type WorkspaceRefLite,
} from "./workspace-inline.js";

export interface ItemWorkspaceClaimOptions {
  /** The surface's controller, or null when the surface has no inline workspace. */
  controller: () => InlineWorkspaceController | null;
  /** Identity of the current selection, or null when nothing is selected. */
  identity: () => WorkspaceItemIdentity | null;
  /** True when the loaded detail belongs to the current selection. */
  detailMatches: () => boolean;
  /** Workspace ref carried by the loaded detail envelope. */
  envelopeRef: () => WorkspaceRefLite | null | undefined;
  /** Refetch the detail; called when this identity's workspace was deleted elsewhere. */
  refresh: () => void;
}

/**
 * Claim/release the surface's inline workspace for the current selection.
 *
 * Extracted because PRListView, IssueListView, and ActivityFeedView all need
 * exactly this and had drifting copies of it. Three effects, deliberately kept
 * separate:
 *
 * 1. Claim while the loaded detail matches the selection, release otherwise. A
 *    stale detail must never claim: the envelope would carry another item's
 *    workspace.
 * 2. Release on teardown, so navigating away does not leave a surface holding a
 *    claim that outlives its view.
 * 3. Refetch when this identity's workspace is invalidated (deleted from the
 *    Workspaces tab or another client), so the detail stops advertising it.
 *
 * Must be called during component initialisation, like any rune.
 */
export function useItemWorkspaceClaim(options: ItemWorkspaceClaimOptions): void {
  $effect(() => {
    const controller = options.controller();
    if (!controller) return;
    const identity = options.identity();
    if (!identity || !options.detailMatches()) {
      controller.release();
      return;
    }
    // Overrides win over the envelope: a just-created workspace is not in the
    // envelope yet, and a just-deleted one may still be.
    const ref = controller.effectiveWorkspaceRef(identity, options.envelopeRef() ?? null);
    if (ref) controller.claim(identity, ref);
    else controller.release();
  });

  $effect(() => {
    const controller = options.controller();
    if (!controller) return;
    return () => controller.release();
  });

  $effect(() => {
    const controller = options.controller();
    if (!controller) return;
    return controller.onIdentityInvalidated((invalidated) => {
      const identity = options.identity();
      if (identity && identityEquals(invalidated, identity)) options.refresh();
    });
  });
}
