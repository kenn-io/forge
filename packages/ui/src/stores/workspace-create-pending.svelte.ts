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

type CreatedEntry = { identity: WorkspaceItemIdentity; ref: WorkspaceRefLite; tick: number };
let created = $state<CreatedEntry[]>([]);

// Monotonic ordering shared by creation confirmations and detail-envelope
// requests: a null envelope can only be trusted to mean "the workspace is
// gone" when its request STARTED after the creation was confirmed. Detail
// stores stamp each envelope-producing request at request start; created
// records stamp at confirmation. Comparing the two distinguishes a stale
// pre-create fetch (must not clear) from a fresh post-create fetch whose
// null is authoritative (another client deleted the workspace).
let lifecycleClock = 0;

export function nextWorkspaceLifecycleTick(): number {
  return ++lifecycleClock;
}

// Deleted workspace IDs persist for the session: a delayed create response
// can land after its workspace was already deleted from another surface
// (Workspaces tab, terminal toolbar), and publishing it would resurrect a
// dead ref. Workspace IDs are never reused, so a genuine recreation always
// arrives under a fresh ID and passes the guard. A plain Set, not $state:
// it is only consulted at publication time, never rendered.
const deletedIds = new Set<string>();

export function markWorkspaceIdDeleted(workspaceId: string): void {
  deletedIds.add(workspaceId);
}

export function isWorkspaceIdDeleted(workspaceId: string): boolean {
  return deletedIds.has(workspaceId);
}

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
// envelope carries a workspace (the server is authoritative then), a
// post-confirmation envelope reports the workspace absent (deleted by
// another client), or a deletion event clears it by ID. A null envelope
// from a request that started before the confirmation is a stale
// pre-create fetch and must not drop the record.
export function recordWorkspaceCreated(identity: WorkspaceItemIdentity, ref: WorkspaceRefLite): void {
  // A create response that lost the race with its own deletion must not
  // republish the dead workspace.
  if (deletedIds.has(ref.id)) return;
  created = [
    ...created.filter((entry) => !identityEquals(entry.identity, identity)),
    { identity, ref, tick: nextWorkspaceLifecycleTick() },
  ];
}

export function createdWorkspaceRef(identity: WorkspaceItemIdentity): WorkspaceRefLite | null {
  return created.find((entry) => identityEquals(entry.identity, identity))?.ref ?? null;
}

export function reconcileWorkspaceCreated(
  identity: WorkspaceItemIdentity,
  envelopeRef: WorkspaceRefLite | null | undefined,
  envelopeTick?: number,
): void {
  const entry = created.find((e) => identityEquals(e.identity, identity));
  if (!entry) return;
  if (!envelopeRef && (envelopeTick == null || envelopeTick <= entry.tick)) return;
  created = created.filter((e) => !identityEquals(e.identity, identity));
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
  lifecycleClock = 0;
  deletedIds.clear();
}
