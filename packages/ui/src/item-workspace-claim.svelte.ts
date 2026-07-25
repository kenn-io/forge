import { untrack } from "svelte";
import {
  identityEquals,
  type InlineWorkspaceController,
  type WorkspaceItemIdentity,
  type WorkspaceRefLite,
} from "./workspace-inline.js";

export interface ItemWorkspaceClaim {
  /** The workspace ref for the current selection, or null when it has none. */
  ref(): WorkspaceRefLite | null;
}

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
 * 2. Release when the view is destroyed, so navigating away does not leave a
 *    surface holding a claim that outlives its view.
 * 3. Refetch when this identity's workspace is invalidated (deleted from the
 *    Workspaces tab or another client), so the detail stops advertising it.
 *
 * Returns the resolved workspace ref so callers decide pane availability from the
 * same value the claim uses, with no one-tick lag.
 *
 * Must be called during component initialisation, like any rune.
 */
export function useItemWorkspaceClaim(options: ItemWorkspaceClaimOptions): ItemWorkspaceClaim {
  /**
   * The workspace this selection should show, resolved at render time.
   *
   * Derived rather than read back off the controller with `isClaimedFor`: a claim
   * is made in an effect, which runs AFTER render, so availability read from the
   * claim lags one tick on every selection change. That tick is enough for the
   * workspace pane to prune out of the tree, collapse a split into a bare leaf,
   * and remount the whole subtree — losing diff scroll and reparenting the live
   * terminal for nothing.
   */
  const resolvedRef = $derived.by<WorkspaceRefLite | null>(() => {
    const controller = options.controller();
    if (!controller) return null;
    const identity = options.identity();
    if (!identity || !options.detailMatches()) return null;
    // Overrides win over the envelope: a just-created workspace is not in the
    // envelope yet, and a just-deleted one may still be.
    return controller.effectiveWorkspaceRef(identity, options.envelopeRef() ?? null);
  });

  $effect(() => {
    const controller = options.controller();
    if (!controller) return;
    const identity = options.identity();
    const ref = resolvedRef;
    if (identity && ref) controller.claim(identity, ref);
    else controller.release();
  });

  // Reads the controller UNTRACKED so this effect has no dependencies and its
  // cleanup therefore runs only at destruction. Reading it reactively re-runs
  // the effect whenever the prop is reassigned — even to the same object — and
  // the cleanup would then release a claim the effect above just made in the
  // same flush, unmounting the workspace pane and reparenting the live terminal
  // for nothing. A surface's controller is stable for the life of the view, so
  // there is nothing to react to.
  $effect(() => {
    const controller = untrack(() => options.controller());
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

  return { ref: () => resolvedRef };
}
