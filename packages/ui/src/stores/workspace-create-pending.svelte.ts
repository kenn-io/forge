import { identityEquals, type WorkspaceItemIdentity, type WorkspaceRefLite } from "../workspace-inline.js";

// Workspace-create lifecycle tracked by canonical item identity, at module
// scope rather than in component state: the detail's local creating flag is
// cleared by route-reset effects and remounts while the POST is still in
// flight, so a selection round-trip back to the same item would re-enable
// "Create Workspace" — inviting a duplicate request and a misleading
// "workspace already exists" conflict. Pending entries always clear in the
// request's finally; confirmed creations are recorded here as well so a
// replacement detail instance WITHOUT an inline controller (focus/mobile
// views, DetailDrawer) still learns the workspace exists even when the
// response landed after unmount or a selection change.
let pending = $state<WorkspaceItemIdentity[]>([]);

type CreatedEntry = { identity: WorkspaceItemIdentity; ref: WorkspaceRefLite };
let created = $state<CreatedEntry[]>([]);

export function beginWorkspaceCreate(identity: WorkspaceItemIdentity): void {
  if (isWorkspaceCreatePending(identity)) return;
  pending = [...pending, identity];
}

// Mutations below no-op without a state write when nothing matches:
// callers include $effect bodies, and an unconditional reassignment there
// reads and writes the same state every run (effect_update_depth_exceeded).
export function endWorkspaceCreate(identity: WorkspaceItemIdentity): void {
  if (!isWorkspaceCreatePending(identity)) return;
  pending = pending.filter((entry) => !identityEquals(entry, identity));
}

export function isWorkspaceCreatePending(identity: WorkspaceItemIdentity): boolean {
  return pending.some((entry) => identityEquals(entry, identity));
}

// Confirmed creation, published for every detail instance regardless of the
// optional inline controller. Kept until a fresh identity-matched detail
// envelope carries a workspace (the server is authoritative then) or the
// workspace is deleted; a null envelope is a stale pre-create fetch and
// must not drop the record.
export function recordWorkspaceCreated(identity: WorkspaceItemIdentity, ref: WorkspaceRefLite): void {
  created = [...created.filter((entry) => !identityEquals(entry.identity, identity)), { identity, ref }];
}

export function createdWorkspaceRef(identity: WorkspaceItemIdentity): WorkspaceRefLite | null {
  return created.find((entry) => identityEquals(entry.identity, identity))?.ref ?? null;
}

export function reconcileWorkspaceCreated(
  identity: WorkspaceItemIdentity,
  envelopeRef: WorkspaceRefLite | null | undefined,
): void {
  if (!envelopeRef) return;
  if (!created.some((entry) => identityEquals(entry.identity, identity))) return;
  created = created.filter((entry) => !identityEquals(entry.identity, identity));
}

// Deletions arrive by workspace ID (Workspaces tab, terminal toolbar),
// possibly with no detail instance mounted for the item at all.
export function clearCreatedWorkspaceById(workspaceId: string): void {
  if (!created.some((entry) => entry.ref.id === workspaceId)) return;
  created = created.filter((entry) => entry.ref.id !== workspaceId);
}

export function resetWorkspaceCreatePendingForTest(): void {
  pending = [];
  created = [];
}
