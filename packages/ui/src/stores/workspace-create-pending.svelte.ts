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
// arrives under a fresh ID and passes the guard. Reactive: the
// controller-less resolver masks deleted envelope IDs at render time, so a
// deletion must invalidate views currently showing the dead workspace.
let deletedIds = $state<ReadonlySet<string>>(new Set());

type PendingWorkspaceLaunch = {
  workspaceId: string;
  workspaceHostKey: string | undefined;
  targetKey: string;
  claimToken: symbol | null;
};

export type WorkspaceLaunchClaim = Readonly<{
  workspaceId: string;
  workspaceHostKey: string | undefined;
  targetKey: string;
  token: symbol;
}>;

let pendingWorkspaceLaunches = $state<PendingWorkspaceLaunch[]>([]);

export function markWorkspaceIdDeleted(workspaceId: string): void {
  if (deletedIds.has(workspaceId)) return;
  const next = new Set(deletedIds);
  next.add(workspaceId);
  deletedIds = next;
}

export function isWorkspaceIdDeleted(workspaceId: string): boolean {
  return deletedIds.has(workspaceId);
}

function workspaceLaunchMatches(
  entry: PendingWorkspaceLaunch,
  workspaceId: string,
  workspaceHostKey: string | undefined,
): boolean {
  return entry.workspaceId === workspaceId && entry.workspaceHostKey === workspaceHostKey;
}

export function queueWorkspaceLaunch(
  workspaceId: string,
  targetKey: string,
  workspaceHostKey: string | undefined,
): void {
  const normalized = targetKey.trim();
  if (!workspaceId || !normalized) return;
  pendingWorkspaceLaunches = [
    ...pendingWorkspaceLaunches.filter((entry) => !workspaceLaunchMatches(entry, workspaceId, workspaceHostKey)),
    { workspaceId, workspaceHostKey, targetKey: normalized, claimToken: null },
  ];
}

export function pendingWorkspaceLaunchTarget(workspaceId: string, workspaceHostKey: string | undefined): string | null {
  return (
    pendingWorkspaceLaunches.find((entry) => workspaceLaunchMatches(entry, workspaceId, workspaceHostKey))?.targetKey ??
    null
  );
}

export function claimWorkspaceLaunch(
  workspaceId: string,
  workspaceHostKey: string | undefined,
): WorkspaceLaunchClaim | null {
  const entry = pendingWorkspaceLaunches.find((candidate) =>
    workspaceLaunchMatches(candidate, workspaceId, workspaceHostKey),
  );
  if (!entry || entry.claimToken !== null) return null;
  // Keep the entry visible while one view owns the request. A second live view
  // for the same workspace must suppress its empty-state launcher without
  // starting a duplicate session.
  const token = Symbol("workspace-launch-claim");
  pendingWorkspaceLaunches = pendingWorkspaceLaunches.map((candidate) =>
    workspaceLaunchMatches(candidate, workspaceId, workspaceHostKey) ? { ...candidate, claimToken: token } : candidate,
  );
  return { workspaceId, workspaceHostKey, targetKey: entry.targetKey, token };
}

export function discardWorkspaceLaunch(workspaceId: string, workspaceHostKey: string | undefined): string | null {
  const entry = pendingWorkspaceLaunches.find((candidate) =>
    workspaceLaunchMatches(candidate, workspaceId, workspaceHostKey),
  );
  if (!entry || entry.claimToken !== null) return null;
  pendingWorkspaceLaunches = pendingWorkspaceLaunches.filter(
    (entry) => !workspaceLaunchMatches(entry, workspaceId, workspaceHostKey),
  );
  return entry.targetKey;
}

export function completeWorkspaceLaunch(claim: WorkspaceLaunchClaim): boolean {
  const owned = pendingWorkspaceLaunches.some(
    (entry) =>
      workspaceLaunchMatches(entry, claim.workspaceId, claim.workspaceHostKey) && entry.claimToken === claim.token,
  );
  if (!owned) return false;
  pendingWorkspaceLaunches = pendingWorkspaceLaunches.filter(
    (entry) =>
      !workspaceLaunchMatches(entry, claim.workspaceId, claim.workspaceHostKey) || entry.claimToken !== claim.token,
  );
  return true;
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
  // Mirrors the host store's CreatedOverride reconcile: a same-ID envelope
  // confirms the creation (server authoritative), and any request started
  // after the confirmation is authoritative whatever it carries — absence
  // or a replacement ID means the workspace was deleted (and possibly
  // recreated) elsewhere. A stale pre-create envelope, null or carrying a
  // different (e.g. previously deleted) ID, must not erase the record.
  const agrees =
    (envelopeRef != null && envelopeRef.id === entry.ref.id) || (envelopeTick != null && envelopeTick > entry.tick);
  if (!agrees) return;
  created = created.filter((e) => !identityEquals(e.identity, identity));
}

// Workspace resolution for detail instances WITHOUT an inline controller
// (focus/mobile views, DetailDrawer) — the controller-less mirror of the
// host store's effectiveRef. The confirmed creation record wins until
// reconciled away (it is fresher than any cached envelope), and an
// envelope still carrying a session-deleted ID is masked — the
// tombstone equivalent, since these views subscribe to no invalidation
// and their cached envelope outlives the deletion until the next fetch.
// A fresh-ID envelope stays authoritative.
export function resolveControllerlessWorkspaceRef(
  identity: WorkspaceItemIdentity,
  envelopeRef: WorkspaceRefLite | null | undefined,
): WorkspaceRefLite | null {
  const created = createdWorkspaceRef(identity);
  // A same-ID envelope is the server refreshing the workspace this record
  // confirmed — its status is fresher than the confirmation snapshot.
  if (created && envelopeRef?.id === created.id) return envelopeRef;
  if (created) return created;
  if (envelopeRef && !deletedIds.has(envelopeRef.id)) return envelopeRef;
  return null;
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
  deletedIds = new Set();
  pendingWorkspaceLaunches = [];
}
