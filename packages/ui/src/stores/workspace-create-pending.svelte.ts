import { identityEquals, type WorkspaceItemIdentity } from "../workspace-inline.js";

// Pending workspace creations tracked by canonical item identity, at module
// scope rather than in component state: the detail's local creating flag is
// cleared by route-reset effects and remounts while the POST is still in
// flight, so a selection round-trip back to the same item would re-enable
// "Create Workspace" — inviting a duplicate request and a misleading
// "workspace already exists" conflict. Entries always clear in the request's
// finally, so a settled create re-enables the button everywhere at once.
let pending = $state<WorkspaceItemIdentity[]>([]);

export function beginWorkspaceCreate(identity: WorkspaceItemIdentity): void {
  if (isWorkspaceCreatePending(identity)) return;
  pending = [...pending, identity];
}

export function endWorkspaceCreate(identity: WorkspaceItemIdentity): void {
  pending = pending.filter((entry) => !identityEquals(entry, identity));
}

export function isWorkspaceCreatePending(identity: WorkspaceItemIdentity): boolean {
  return pending.some((entry) => identityEquals(entry, identity));
}

export function resetWorkspaceCreatePendingForTest(): void {
  pending = [];
}
