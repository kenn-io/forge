import type { Attachment } from "svelte/attachments";
import { canonicalProvider, resolvedPlatformHost } from "./api/provider-routes.js";

export type WorkspaceRefLite = { id: string; status: string };

export type WorkspaceItemIdentity = {
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
  number: number;
  /**
   * Kind of item the workspace is attached to. Producers pass their native
   * vocabulary ("pull" | "pr" | "pull_request", "issue", "kata" |
   * "kata_task"); equality and cache keys compare the canonical form via
   * canonicalItemType. Without this, a PR and an issue sharing a repository
   * and number would share claims, overrides, and deletion tombstones.
   */
  itemType: string;
};

const ITEM_TYPE_ALIASES: Record<string, string> = {
  pr: "pull",
  pull_request: "pull",
  kata_task: "kata",
};

/** Canonical item-type name for identity comparison ("pr"/"pull_request" -> "pull"). */
export function canonicalItemType(itemType: string): string {
  return ITEM_TYPE_ALIASES[itemType] ?? itemType;
}

export type InlineWorkspaceSurface = "activity" | "prs" | "issues";

export type InlineDockMode = "split" | "collapsed" | "expanded";

export interface InlineWorkspaceController {
  readonly surface: InlineWorkspaceSurface;
  /** Override-aware workspace ref for an identity (overrides win over the envelope). */
  effectiveWorkspaceRef(
    identity: WorkspaceItemIdentity,
    envelopeRef: WorkspaceRefLite | null | undefined,
  ): WorkspaceRefLite | null;
  /** Claim/refresh this surface's claim. No-ops when identical (effect-safe). */
  claim(identity: WorkspaceItemIdentity, ref: WorkspaceRefLite): void;
  /** Drop this surface's claim. No-ops when already clear. */
  release(): void;
  /** True when this surface holds a claim for this identity. */
  isClaimedFor(identity: WorkspaceItemIdentity): boolean;
  /** Successful create: positive override; claims if the surface is still on this identity. */
  recordCreated(identity: WorkspaceItemIdentity, ref: WorkspaceRefLite): void;
  /**
   * Successful delete: tombstone override; releases a matching claim.
   * No production caller today — deletion initiated from the live WTV
   * (its own toolbar, or WorkspaceListSidebar's list-row delete) reports
   * through the host store's `notifyWorkspaceDeleted` instead, which
   * tombstones every surface's claim on that workspace ID at once. Kept
   * for non-WTV delete surfaces (e.g. a future inline delete action that
   * only ever needs to affect its own claiming surface) — do not remove.
   * The workspace ID scopes the tombstone: it suppresses stale envelopes
   * still carrying that workspace, not a recreation under a fresh ID.
   */
  recordDeleted(identity: WorkspaceItemIdentity, workspaceId: string): void;
  /**
   * Identity-matched refetch landed: drop the override if the envelope
   * agrees. A null envelope agrees with a positive (created) override only
   * when `envelopeTick` shows its request started after the creation was
   * recorded — otherwise it is a stale pre-create fetch and must not clear
   * a workspace that exists.
   */
  reconcile(
    identity: WorkspaceItemIdentity,
    envelopeRef: WorkspaceRefLite | null | undefined,
    envelopeTick?: number,
  ): void;
  /** Dock UI state (persisted height lives in the dock; mode lives here so detail buttons can drive it). */
  getDockMode(): InlineDockMode;
  setDockMode(mode: InlineDockMode): void;
  /** Expand the dock and move focus into the terminal host. */
  focusTerminal(): void;
  /** Navigate to /terminal/{id} (the secondary "Open in Workspaces" action). */
  openInWorkspaces(ref: WorkspaceRefLite): void;
  /** Fires when a claimed identity's workspace was deleted out from under this surface. */
  onIdentityInvalidated(cb: (identity: WorkspaceItemIdentity) => void): () => void;
  /** Attachment for the dock's host-slot element; the frontend host reparents into it. */
  slotAttachment: Attachment<HTMLElement>;
}

export function identityEquals(a: WorkspaceItemIdentity, b: WorkspaceItemIdentity): boolean {
  // Route segments may carry provider aliases (gh/gl/fj) while store data
  // uses canonical names; the same item must never compare as two identities.
  return (
    canonicalProvider(a.provider) === canonicalProvider(b.provider) &&
    canonicalItemType(a.itemType) === canonicalItemType(b.itemType) &&
    // Activity URLs and provider-default routes may omit the host while
    // API payloads carry the concrete default host — one item, not two.
    resolvedPlatformHost(a.provider, a.platformHost) === resolvedPlatformHost(b.provider, b.platformHost) &&
    a.owner === b.owner &&
    a.name === b.name &&
    a.repoPath === b.repoPath &&
    a.number === b.number
  );
}
