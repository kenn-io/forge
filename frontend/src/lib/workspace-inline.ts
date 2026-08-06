import type { Snippet } from "svelte";
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

/** A session of the hosted workspace that a detail surface may show as a pane. */
export interface PromotableSession {
  /** The layout key: `session:` + encoded workspace / host / session. */
  paneKey: string;
  label: string;
}

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
  /**
   * Report which pane of this surface the user focused.
   *
   * The host files it under the WORKSPACE, not the surface: an expand or a Focus
   * Terminal has to act on the terminal the user was last in, and that must survive
   * promoting it, demoting it, and visiting another item in between. Views forward
   * every focused pane; the host keeps only the ones that name a workspace.
   */
  notePaneFocused(tabKey: string): void;
  /** Expand the dock and move focus into the terminal host. */
  focusTerminal(): void;
  /** Navigate to /terminal/{id} (the secondary "Open in Workspaces" action). */
  openInWorkspaces(ref: WorkspaceRefLite): void;
  /** Fires when a claimed identity's workspace was deleted out from under this surface. */
  onIdentityInvalidated(cb: (identity: WorkspaceItemIdentity) => void): () => void;
  /** Attachment for the dock's host-slot element; the frontend host reparents into it. */
  slotAttachment: Attachment<HTMLElement>;
  /**
   * Sessions of the workspace this surface is currently hosting, or [] when it
   * hosts none. Only the hosting surface reports any: there is one live terminal
   * per session, so a second surface could not render it even while claiming the
   * same workspace.
   */
  promotableSessions(): readonly PromotableSession[];
  /** Label for the workspace container pane, narrowed to its sole unpromoted session when possible. */
  workspacePaneLabel(): string;
  /**
   * Whether the container pane would render nothing: it has sessions, and the
   * user promoted every one of them into a pane of its own.
   *
   * Its tab must go away while that holds, or dragging the last session out
   * leaves the container behind as an empty hole in the surface. The workspace
   * is still claimed and its controls still hosted, so Launch stays one click
   * away from the promoted pane, and demoting a session brings the tab back.
   */
  workspacePaneEmpty(): boolean;
  /**
   * Whether the container pane has only its bottom dock left to render.
   *
   * Its leaf should release the empty stage's saved share to the sibling above,
   * while the dock keeps its own height. This is transient: restoring a workflow
   * session must also restore the user's saved split ratio.
   */
  workspacePaneRowOnly(): boolean;
  /**
   * The workspace's collapsed terminal dock when the surface must render it
   * itself, or null.
   *
   * Non-null only while the container pane is retired: the dock lives inside that
   * pane, and losing it with the pane leaves no row to open a terminal from. The
   * surface anchors this at its own bottom edge, where the dock always sits.
   */
  dockRow(): Snippet<[]> | null;
  /**
   * The whole pane body for a promoted session, supplied by the frontend, or null
   * before it has been.
   *
   * A snippet rather than an attachment plus a visibility call: splitting them
   * hands the view a visibility API with no owner token, which is exactly the race
   * the owner-scoped session registry exists to prevent — a superseded source pane
   * could hide the destination. The frontend supplies a slot component that owns
   * both halves and cannot be called wrong.
   */
  sessionPane(): Snippet<[{ paneKey: string; visible: boolean }]> | null;
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
