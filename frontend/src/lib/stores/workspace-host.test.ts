import { describe, expect, it, beforeEach, vi } from "vite-plus/test";
import { getPaneLayoutStore, promoteSessionBesideWorkspace } from "@middleman/ui/stores/paneLayout";
import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
import { consumeSessionFocus, registerSessionSlot, resetSessionHostForTest } from "./session-host.svelte.ts";
import {
  createdWorkspaceRef,
  markWorkspaceIdDeleted,
  nextWorkspaceLifecycleTick,
  reconcileWorkspaceCreated,
  recordWorkspaceCreated,
  resetWorkspaceCreatePendingForTest,
  resolveControllerlessWorkspaceRef,
} from "@middleman/ui/stores/workspace-create-pending";
import { getLastWorkspaceRoute, navigate } from "./router.svelte.ts";
import {
  activeHostedSession,
  desiredKey,
  desiredSlot,
  getInlineWorkspaceController,
  publishHostedSessions,
  hostedWorkspaceLauncher,
  registerWorkspaceLauncher,
  hostedSessionRegistryKey,
  isHostVisible,
  notifyWorkspaceDeleted,
  onIdentityInvalidated,
  registerSlotElement,
  rememberTerminalRouteKey,
  resetWorkspaceHostForTest,
} from "./workspace-host.svelte.ts";

const identityA = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 7,
  itemType: "pull",
};
const identityB = { ...identityA, number: 8 };
const refA = { id: "ws-a", status: "ready" };

/**
 * Stand in for the pane rendering: the portal slot element exists only while the
 * workspace pane is on screen, and `isHostVisible()` reads exactly that.
 */
function mountWorkspaceSlot(surface: "prs" | "issues" | "activity", mounted: boolean): void {
  registerSlotElement(surface, mounted ? document.createElement("div") : null);
}

describe("workspace host store", () => {
  beforeEach(() => {
    resetWorkspaceHostForTest();
    resetWorkspaceCreatePendingForTest();
    navigate("/pulls");
  });

  it("tab owns the host on workspaces and terminal routes regardless of claims", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    navigate("/terminal/ws-z");
    expect(desiredSlot()).toBe("tab");
    expect(desiredKey()).toEqual({ workspaceId: "ws-z", hostKey: undefined });
    navigate("/workspaces");
    expect(desiredSlot()).toBe("tab");
    expect(desiredKey()).toEqual({ workspaceId: "", hostKey: undefined });
  });

  it("an inline claim displays only on its own page", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    expect(desiredSlot()).toBe("prs");
    expect(desiredKey()).toEqual({ workspaceId: "ws-a", hostKey: undefined });
    navigate("/activity");
    expect(desiredSlot()).toBeNull(); // parked, not shown on another surface's page
  });

  it("parking keeps the hosted key sticky so sockets survive a detour", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    navigate("/activity");
    expect(desiredSlot()).toBeNull();
    expect(desiredKey()).toEqual({ workspaceId: "ws-a", hostKey: undefined });
    navigate("/pulls");
    expect(desiredSlot()).toBe("prs");
  });

  it("claim/release are effect-safe no-ops on identical input", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    prs.claim(identityA, { ...refA }); // same values, new object: no state churn
    expect(desiredSlot()).toBe("prs");
    prs.release();
    prs.release();
    expect(desiredSlot()).toBeNull();
  });

  it("overrides win over the envelope until reconciled", () => {
    const prs = getInlineWorkspaceController("prs");
    // create override: appears without waiting for refetch
    prs.recordCreated(identityA, refA);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(refA);
    // stale envelope still lacking the workspace does not remove it
    expect(prs.effectiveWorkspaceRef(identityA, undefined)).toEqual(refA);
    // identity-matched refetch that agrees drops the override
    prs.reconcile(identityA, refA);
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toEqual(refA);
    // a disagreeing (stale) refetch payload leaves the override in force
    prs.recordDeleted(identityA, refA.id);
    prs.reconcile(identityA, refA); // envelope still carries deleted ws — stale
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
    prs.reconcile(identityA, null); // now it agrees
    // The tombstone is gone, but a session-deleted ID never resurfaces —
    // envelope authority returns only for live workspaces.
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
    const fresh = { id: "ws-a2", status: "ready" };
    expect(prs.effectiveWorkspaceRef(identityA, fresh)).toEqual(fresh);
  });

  it("a tombstone never flickers back into a claim", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    prs.recordDeleted(identityA, refA.id);
    expect(desiredSlot()).toBeNull();
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull(); // stale envelope
  });

  it("overrides are identity-scoped", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.recordDeleted(identityA, refA.id);
    // identityA's tombstone leaves another item's workspace untouched.
    // (The deleted ID itself is masked for every identity — workspace IDs
    // are unique and a workspace belongs to exactly one item, so ws-a
    // appearing under identityB could only ever be stale data.)
    const refB = { id: "ws-b", status: "ready" };
    expect(prs.effectiveWorkspaceRef(identityB, refB)).toEqual(refB);
    expect(prs.effectiveWorkspaceRef(identityB, refA)).toBeNull();
  });

  it("a creation recorded without a controller surfaces through effectiveWorkspaceRef", () => {
    // A create that starts in a controller-less focus/mobile view records
    // only the shared created entry; if the layout switches to an inline
    // surface before the response lands, no recordCreated override exists.
    // The inline view must still see the workspace or it re-offers
    // "Create Workspace" for a workspace that exists.
    const prs = getInlineWorkspaceController("prs");
    recordWorkspaceCreated(identityA, refA);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(refA);
    expect(prs.effectiveWorkspaceRef(identityB, null)).toBeNull();
    // An envelope carrying the workspace stays authoritative.
    const envelope = { id: "ws-a", status: "provisioning" };
    expect(prs.effectiveWorkspaceRef(identityA, envelope)).toEqual(envelope);
  });

  it("a post-creation null envelope authoritatively clears a created override", () => {
    const prs = getInlineWorkspaceController("prs");
    const preCreateTick = nextWorkspaceLifecycleTick();
    prs.recordCreated(identityA, refA);
    // A null envelope from a request that started before the creation is
    // a stale pre-create fetch: the override must survive it, with or
    // without a tick.
    prs.reconcile(identityA, null, preCreateTick);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(refA);
    prs.reconcile(identityA, null);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(refA);
    // A request started after the creation reporting no workspace means
    // another client deleted it: the override must clear.
    prs.reconcile(identityA, null, nextWorkspaceLifecycleTick());
    expect(prs.effectiveWorkspaceRef(identityA, null)).toBeNull();
  });

  it("a post-creation null envelope clears a controller-less created record", () => {
    const prs = getInlineWorkspaceController("prs");
    const preCreateTick = nextWorkspaceLifecycleTick();
    recordWorkspaceCreated(identityA, refA);
    reconcileWorkspaceCreated(identityA, null, preCreateTick);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(refA);
    reconcileWorkspaceCreated(identityA, null);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(refA);
    reconcileWorkspaceCreated(identityA, null, nextWorkspaceLifecycleTick());
    expect(prs.effectiveWorkspaceRef(identityA, null)).toBeNull();
  });

  it("a controller-less recreation under a fresh ID supersedes the tombstone", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    notifyWorkspaceDeleted("ws-a", undefined, identityA);
    // Focus/mobile recreate the workspace: only the shared created record
    // exists (no controller recordCreated ran). The tombstone masks only
    // its own deleted ID, not the recreation — hiding it would re-offer
    // "Create Workspace" for a workspace that exists.
    const recreated = { id: "ws-b", status: "provisioning" };
    recordWorkspaceCreated(identityA, recreated);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(recreated);
    // A stale envelope still carrying the deleted ID stays masked in
    // favor of the recreation.
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toEqual(recreated);
  });

  it("a post-override envelope carrying a replacement workspace clears the override", () => {
    const prs = getInlineWorkspaceController("prs");
    const preCreateTick = nextWorkspaceLifecycleTick();
    prs.recordCreated(identityA, refA);
    const replacement = { id: "ws-b", status: "ready" };
    // A pre-create envelope with a different ID is stale and stays masked.
    prs.reconcile(identityA, replacement, preCreateTick);
    expect(prs.effectiveWorkspaceRef(identityA, replacement)).toEqual(refA);
    // A request started after the creation carrying a different workspace
    // means delete+recreate elsewhere: the envelope is authoritative.
    prs.reconcile(identityA, replacement, nextWorkspaceLifecycleTick());
    expect(prs.effectiveWorkspaceRef(identityA, replacement)).toEqual(replacement);
  });

  it("a deletion still masks a controller-less created record", () => {
    const prs = getInlineWorkspaceController("prs");
    recordWorkspaceCreated(identityA, refA);
    notifyWorkspaceDeleted("ws-a", undefined, identityA);
    // The tombstone masks stale envelopes AND the record was cleared by
    // ID, so the dead workspace cannot resurface through the fallback.
    expect(prs.effectiveWorkspaceRef(identityA, null)).toBeNull();
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
  });

  it("a delayed create response cannot overwrite its own deletion tombstone", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    notifyWorkspaceDeleted("ws-a", undefined, identityA);
    // The original create response lands after the workspace was deleted
    // from another surface: recording it would replace the tombstone with
    // a positive override advertising a dead workspace.
    prs.recordCreated(identityA, refA);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toBeNull();
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull(); // stale envelope stays masked
  });

  it("a delayed create response cannot republish a deleted controller-less record", () => {
    const prs = getInlineWorkspaceController("prs");
    // Deleted from the Workspaces tab before anything claimed or remembered
    // the identity: no tombstone exists, so only the deleted-ID memory can
    // block the late publication.
    notifyWorkspaceDeleted("ws-a");
    recordWorkspaceCreated(identityA, refA);
    expect(createdWorkspaceRef(identityA)).toBeNull();
    expect(prs.effectiveWorkspaceRef(identityA, null)).toBeNull();
  });

  it("a delayed create response under a fresh ID supersedes the deletion tombstone", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    notifyWorkspaceDeleted("ws-a", undefined, identityA);
    // A create retried after the deletion resolves under a new ID: that is
    // a genuine recreation, not the lost race, and must surface.
    const recreated = { id: "ws-b", status: "provisioning" };
    prs.recordCreated(identityA, recreated);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(recreated);
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toEqual(recreated);
  });

  it("recordDeleted blocks a later same-ID create publication in both stores", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.recordDeleted(identityA, refA.id);
    prs.recordCreated(identityA, refA);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toBeNull();
    recordWorkspaceCreated(identityA, refA);
    expect(createdWorkspaceRef(identityA)).toBeNull();
    expect(prs.effectiveWorkspaceRef(identityA, null)).toBeNull();
  });

  it("a stale earlier-generation envelope does not shadow a recreation past a newer tombstone", () => {
    const prs = getInlineWorkspaceController("prs");
    // Generation one: created and deleted (tombstone remembers ws-0).
    prs.claim(identityA, { id: "ws-0", status: "ready" });
    notifyWorkspaceDeleted("ws-0", undefined, identityA);
    // Generation two: recreated, then deleted from the Workspaces tab —
    // the tombstone now remembers ws-a, no longer ws-0.
    recordWorkspaceCreated(identityA, refA);
    notifyWorkspaceDeleted("ws-a", undefined, identityA);
    // Generation three: controller-less recreation, then a layout switch
    // to the inline surface while a stale envelope still carries the
    // FIRST deleted workspace. Its ID differs from the tombstone's, but
    // it is just as dead — surfacing it would let the dock claim a
    // deleted workspace over the confirmed recreation.
    const recreated = { id: "ws-b", status: "provisioning" };
    recordWorkspaceCreated(identityA, recreated);
    expect(prs.effectiveWorkspaceRef(identityA, { id: "ws-0", status: "ready" })).toEqual(recreated);
  });

  it("a created record shadows a different-ID envelope until reconciled", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    notifyWorkspaceDeleted("ws-a", undefined, identityA);
    const recreated = { id: "ws-b", status: "provisioning" };
    recordWorkspaceCreated(identityA, recreated);
    // A different-ID envelope — even one never deleted — is accepted only
    // after reconciliation removes the newer confirmed record; until then
    // the record is fresher than any cached envelope.
    expect(prs.effectiveWorkspaceRef(identityA, { id: "ws-c", status: "ready" })).toEqual(recreated);
    // A same-ID envelope is the server refreshing the recorded workspace:
    // its status wins.
    const refreshed = { id: "ws-b", status: "ready" };
    expect(prs.effectiveWorkspaceRef(identityA, refreshed)).toEqual(refreshed);
  });

  it("a created record shadows a stale deleted envelope without any tombstone", () => {
    const prs = getInlineWorkspaceController("prs");
    // Deleted from the Workspaces tab before anything claimed the
    // identity: no tombstone exists for it, only the deleted-ID memory.
    notifyWorkspaceDeleted("ws-0");
    const recreated = { id: "ws-b", status: "provisioning" };
    recordWorkspaceCreated(identityA, recreated);
    expect(prs.effectiveWorkspaceRef(identityA, { id: "ws-0", status: "ready" })).toEqual(recreated);
    expect(prs.effectiveWorkspaceRef(identityA, { id: "ws-c", status: "ready" })).toEqual(recreated);
  });

  it("a stale different-ID envelope does not clear a fresh created record", () => {
    // Delete ws-old, recreate as ws-a: a pre-create fetch still carrying
    // ws-old must not erase the recreation's record — only a same-ID
    // envelope or a request started after the confirmation reconciles it.
    const staleTick = nextWorkspaceLifecycleTick();
    const stale = { id: "ws-old", status: "ready" };
    recordWorkspaceCreated(identityA, refA);
    reconcileWorkspaceCreated(identityA, stale, staleTick);
    expect(createdWorkspaceRef(identityA)).toEqual(refA);
    reconcileWorkspaceCreated(identityA, stale); // no tick: still stale
    expect(createdWorkspaceRef(identityA)).toEqual(refA);
    // A same-ID envelope confirms the creation regardless of tick.
    reconcileWorkspaceCreated(identityA, refA, staleTick);
    expect(createdWorkspaceRef(identityA)).toBeNull();
    // A post-confirmation different-ID envelope is authoritative
    // (deleted and recreated elsewhere).
    recordWorkspaceCreated(identityA, refA);
    reconcileWorkspaceCreated(identityA, stale, nextWorkspaceLifecycleTick());
    expect(createdWorkspaceRef(identityA)).toBeNull();
  });

  it("the controller-less resolver prefers the created record and masks deleted envelopes", () => {
    // A clean envelope resolves as-is.
    expect(resolveControllerlessWorkspaceRef(identityA, refA)).toEqual(refA);
    // A session-deleted envelope is masked even though the cached detail
    // still carries it (controller-less views get no invalidation).
    markWorkspaceIdDeleted("ws-a");
    expect(resolveControllerlessWorkspaceRef(identityA, refA)).toBeNull();
    // The confirmed recreation wins over the stale envelope.
    const recreated = { id: "ws-b", status: "provisioning" };
    recordWorkspaceCreated(identityA, recreated);
    expect(resolveControllerlessWorkspaceRef(identityA, refA)).toEqual(recreated);
    // The record stays authoritative until reconciled, matching the host
    // store's positive-override semantics.
    expect(resolveControllerlessWorkspaceRef(identityA, { id: "ws-c", status: "ready" })).toEqual(recreated);
    // Except for a same-ID envelope: that is the server refreshing the
    // recorded workspace, and its status is fresher.
    const refreshed = { id: "ws-b", status: "ready" };
    expect(resolveControllerlessWorkspaceRef(identityA, refreshed)).toEqual(refreshed);
  });

  it("a tombstone does not mask a recreated workspace under a fresh ID", () => {
    const prs = getInlineWorkspaceController("prs");
    const recreated = { id: "ws-a2", status: "provisioning" };
    prs.recordDeleted(identityA, refA.id);
    // Stale envelopes still carrying the deleted workspace stay masked.
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
    // An envelope carrying a different ID is a recreation (Workspaces
    // tab, another client) — the "workspace absent" envelope an ID-less
    // tombstone would wait for never arrives, so it must show now.
    expect(prs.effectiveWorkspaceRef(identityA, recreated)).toEqual(recreated);
    // And the refetch that surfaced it clears the tombstone entirely; the
    // recreation keeps surfacing while the deleted ID stays masked.
    prs.reconcile(identityA, recreated);
    expect(prs.effectiveWorkspaceRef(identityA, recreated)).toEqual(recreated);
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
  });

  it("a deletion reported without an ID match keeps masking only its own workspace", () => {
    const prs = getInlineWorkspaceController("prs");
    const recreated = { id: "ws-a2", status: "ready" };
    prs.claim(identityA, refA);
    prs.release();
    notifyWorkspaceDeleted("ws-a"); // deleted from the Workspaces tab
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
    expect(prs.effectiveWorkspaceRef(identityA, recreated)).toEqual(recreated);
  });

  it("recordCreated only claims when the surface is still on that identity", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityB, { id: "ws-b", status: "ready" }); // selection moved to B
    prs.recordCreated(identityA, refA); // A's late response
    expect(desiredKey().workspaceId).toBe("ws-b"); // no claim theft
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(refA); // override still recorded
  });

  it("a late create response records the override without activating a released surface", () => {
    const prs = getInlineWorkspaceController("prs");
    const issues = getInlineWorkspaceController("issues");
    // PR A's create was still in flight when its views unmounted and the
    // user moved to the issues page, where another item claims workspace B.
    prs.claim(identityA, refA);
    prs.release();
    navigate("/issues");
    const issueIdentity = { ...identityA, number: 9, itemType: "issue" };
    issues.claim(issueIdentity, { id: "ws-b", status: "ready" });
    // The late response must land its override, but claiming the released
    // prs surface would move the hosted key under B's visible dock.
    prs.recordCreated(identityA, { id: "ws-a2", status: "provisioning" });
    expect(desiredSlot()).toBe("issues");
    expect(desiredKey()).toEqual({ workspaceId: "ws-b", hostKey: undefined });
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual({ id: "ws-a2", status: "provisioning" });
    // Returning to pulls: the surface stays unclaimed until a live
    // selection confirms the identity through the claim effect.
    navigate("/pulls");
    expect(desiredSlot()).toBeNull();
  });

  it("the visible surface's claim keys the host even when the sticky key drifted", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    // Leaving a terminal route reasserts the sticky key with the tab's
    // workspace; the pulls page's own claim must still win or the dock
    // would host the tab's workspace under the claimed item's detail.
    rememberTerminalRouteKey({ workspaceId: "ws-tab", hostKey: "fleet-host" });
    expect(desiredKey()).toEqual({ workspaceId: "ws-a", hostKey: undefined });
  });

  it("notifyWorkspaceDeleted tombstones, releases, and invalidates the claiming identity", () => {
    const prs = getInlineWorkspaceController("prs");
    const invalidated: unknown[] = [];
    const off = onIdentityInvalidated((identity) => invalidated.push(identity));
    prs.claim(identityA, refA);
    notifyWorkspaceDeleted("ws-a");
    expect(desiredSlot()).toBeNull();
    expect(desiredKey().workspaceId).toBe(""); // hosted key cleared: WTV leaves the dead workspace
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
    expect(invalidated).toEqual([identityA]);
    off();
  });

  it("deletion tombstones a released claim so a stale envelope cannot re-claim it", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    prs.release(); // view unmounted: claim gone, but the workspace still exists
    const invalidated: unknown[] = [];
    const off = onIdentityInvalidated((identity) => invalidated.push(identity));
    notifyWorkspaceDeleted("ws-a"); // deleted from the Workspaces tab
    // The list view's cached envelope still carries the dead ref; revisiting
    // must not re-claim it.
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
    expect(invalidated).toEqual([identityA]);
    off();
  });

  it("deletion with a caller-supplied identity tombstones a never-claimed workspace", () => {
    const prs = getInlineWorkspaceController("prs");
    const invalidated: unknown[] = [];
    const off = onIdentityInvalidated((identity) => invalidated.push(identity));
    // No claim, no recordCreated: the workspace was only ever opened in the
    // tab (or deleted straight from the sidebar list), so the store has no
    // identity metadata of its own — the deletion callback supplies it.
    notifyWorkspaceDeleted("ws-never", undefined, identityA);
    expect(prs.effectiveWorkspaceRef(identityA, { id: "ws-never", status: "ready" })).toBeNull();
    expect(invalidated).toEqual([identityA]);
    off();
  });

  it("deletion tombstones a created-but-never-claimed workspace after release", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.recordCreated(identityA, refA);
    prs.reconcile(identityA, refA); // refetch agreed: create override dropped
    prs.release();
    notifyWorkspaceDeleted("ws-a");
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
  });

  it("deleting a workspace forgets its remembered terminal route", () => {
    navigate("/terminal/ws-host-del-1");
    navigate("/pulls");
    expect(getLastWorkspaceRoute()).toBe("/terminal/ws-host-del-1");
    notifyWorkspaceDeleted("ws-host-del-1");
    expect(getLastWorkspaceRoute()).toBe("/workspaces");
  });

  it("fleet deletion matches the host key and leaves local claims alone", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA); // local ws-a claim
    navigate("/terminal/fleet/build-host/ws-a");
    navigate("/pulls");
    notifyWorkspaceDeleted("ws-a", "build-host");
    expect(desiredSlot()).toBe("prs"); // same id on a fleet host is a different workspace
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toEqual(refA); // no tombstone
    expect(getLastWorkspaceRoute()).toBe("/workspaces"); // fleet route forgotten
  });

  it("dock collapse means claimed-but-hidden", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    const layout = getPaneLayoutStore("prs");
    mountWorkspaceSlot("prs", true);
    expect(isHostVisible()).toBe(true);
    prs.setDockMode("collapsed");
    expect(layout.hiddenTabKeys()).toContain("workspace");
    // Collapsing hides the pane, which unmounts its slot; the host then reads as
    // claimed but not visible.
    mountWorkspaceSlot("prs", false);
    expect(desiredSlot()).toBe("prs"); // still claimed
    expect(isHostVisible()).toBe(false); // hostVisible contract applies
    prs.setDockMode("split");
    expect(layout.hiddenTabKeys()).not.toContain("workspace");
    mountWorkspaceSlot("prs", true);
    expect(isHostVisible()).toBe(true);
  });

  it("a PR and an issue sharing a repo and number never cross claims, overrides, or tombstones", () => {
    const prs = getInlineWorkspaceController("prs");
    const issues = getInlineWorkspaceController("issues");
    const issueIdentity = { ...identityA, itemType: "issue" };
    const issueRef = { id: "ws-issue", status: "ready" };
    // Same repo, same number 7 — but PR #7 and Issue #7 are unrelated items
    // owning unrelated workspaces.
    prs.claim(identityA, refA);
    expect(issues.isClaimedFor(issueIdentity)).toBe(false);
    expect(prs.isClaimedFor(issueIdentity)).toBe(false);
    // A creation override for the issue must not display under the PR.
    issues.recordCreated(issueIdentity, issueRef);
    expect(prs.effectiveWorkspaceRef(identityA, null)).toBeNull();
    // Deleting the PR's workspace tombstones only the PR identity.
    notifyWorkspaceDeleted("ws-a");
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
    expect(issues.effectiveWorkspaceRef(issueIdentity, null)).toEqual(issueRef);
  });

  it("an omitted host keys the same identity as the provider default host", () => {
    const prs = getInlineWorkspaceController("prs");
    // Activity URLs may omit platform_host while workspace payloads carry
    // github.com; claims and tombstones must not split across the two.
    const hostless = { ...identityA, platformHost: undefined };
    prs.claim(hostless, refA);
    expect(prs.isClaimedFor(identityA)).toBe(true);
    prs.recordDeleted(identityA, refA.id);
    expect(prs.effectiveWorkspaceRef(hostless, refA)).toBeNull();
  });

  it("item-type vocabularies key the same identity for claims and tombstones", () => {
    const prs = getInlineWorkspaceController("prs");
    // The activity drawer says "pr" and workspace envelopes say
    // "pull_request"; a tombstone recorded through either vocabulary must
    // hide the detail-claimed "pull" identity too.
    prs.claim({ ...identityA, itemType: "pr" }, refA);
    expect(prs.isClaimedFor(identityA)).toBe(true);
    prs.recordDeleted({ ...identityA, itemType: "pull_request" }, refA.id);
    expect(prs.effectiveWorkspaceRef(identityA, refA)).toBeNull();
  });

  it("provider aliases key the same identity for claims, overrides, and tombstones", () => {
    const prs = getInlineWorkspaceController("prs");
    const aliasIdentity = { ...identityA, provider: "gh" };
    prs.claim(aliasIdentity, refA);
    expect(prs.isClaimedFor(identityA)).toBe(true);
    prs.recordCreated(aliasIdentity, refA);
    // Canonical form resolves the alias-recorded override slot.
    expect(prs.effectiveWorkspaceRef(identityA, null)).toEqual(refA);
    // A tombstone recorded canonically hides the alias-keyed item too;
    // otherwise a deleted workspace could be reclaimed through the alias
    // route from stale detail data.
    prs.recordDeleted(identityA, refA.id);
    expect(prs.effectiveWorkspaceRef(aliasIdentity, refA)).toBeNull();
  });

  it("replacing a claim with a different identity resets an expanded dock", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    prs.setDockMode("expanded");
    // Direct replacement (selection change whose new detail already matches)
    // never makes the workspace pane unavailable, so the layout host's zoom
    // reconciliation never fires and the store must un-zoom itself or B's
    // detail opens hidden behind a maximized terminal.
    prs.claim(identityB, { id: "ws-b", status: "ready" });
    expect(prs.getDockMode()).toBe("split");
    // Same-identity re-asserts (ref status changes) keep expanded intact.
    prs.setDockMode("expanded");
    prs.claim(identityB, { id: "ws-b", status: "running" });
    expect(prs.getDockMode()).toBe("expanded");
  });

  it("an expanded dock resets to split across a same-update release and reclaim", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    prs.setDockMode("expanded");
    // Selection change to an item whose detail is already cached: the old
    // view's effect cleanup releases and the new claim lands in the same
    // update. The layout host only ever sees the final claimed state — no
    // availability gap — and setClaim sees no previous claim to detect the
    // replacement, so the release itself must un-zoom or B's detail opens
    // hidden behind a maximized terminal.
    prs.release();
    prs.claim(identityB, { id: "ws-b", status: "ready" });
    expect(prs.getDockMode()).toBe("split");
  });

  it("focusTerminal reveals a collapsed dock in split and never maximizes", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    // Collapsed: reveal restores the same split layout the workspace
    // first appeared in — maximizing is the terminal toolbar's own
    // action, never a side effect of asking for focus.
    prs.setDockMode("collapsed");
    prs.focusTerminal();
    expect(prs.getDockMode()).toBe("split");
    // Already visible: the mode is left alone.
    prs.focusTerminal();
    expect(prs.getDockMode()).toBe("split");
    prs.setDockMode("expanded");
    prs.focusTerminal();
    expect(prs.getDockMode()).toBe("expanded");
  });

  it("focusTerminal opens the launcher when the workspace is running nothing", () => {
    const prs = getInlineWorkspaceController("prs");
    const openLauncher = vi.fn();
    registerWorkspaceLauncher(openLauncher);
    prs.claim(identityA, refA);
    prs.setDockMode("collapsed");
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, []);

    prs.focusTerminal();

    // There is no terminal to focus, so revealing the pane alone would land the
    // user on an empty surface and look like the command did nothing.
    expect(openLauncher).toHaveBeenCalled();
    expect(prs.getDockMode()).toBe("split");
  });

  it("reveals the pane before opening the launcher the palette asked for", () => {
    const prs = getInlineWorkspaceController("prs");
    const openLauncher = vi.fn();
    registerWorkspaceLauncher(openLauncher);
    prs.claim(identityA, refA);
    prs.setDockMode("collapsed");

    hostedWorkspaceLauncher("prs")?.();

    // The overlay is rendered by the embedded view, so a collapsed pane has
    // nowhere to draw it: the command would report success and produce no UI.
    expect(openLauncher).toHaveBeenCalled();
    expect(prs.getDockMode()).toBe("split");
  });

  it("focusTerminal focuses a running session rather than the launcher", () => {
    const prs = getInlineWorkspaceController("prs");
    const openLauncher = vi.fn();
    registerWorkspaceLauncher(openLauncher);
    prs.claim(identityA, refA);
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, [
      {
        paneKey: "session:ws-a//ws-a%3Ahelper",
        label: "Helper",
        hostKey: "ws-a//ws-a%3Ahelper/gen-1",
        active: true,
      },
    ]);

    prs.focusTerminal();

    expect(openLauncher).not.toHaveBeenCalled();
  });

  it("reveals a workspace buried under another leaf's zoom", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    const layout = getPaneLayoutStore("prs");
    const detailLeaf = layout.leafIDForTab("conversation");

    // Maximizing the conversation leaves the workspace structurally present and
    // completely invisible, with its portal slot unmounted — "split" by dock
    // mode, but nothing on screen to focus.
    layout.toggleZoom(detailLeaf!);
    expect(prs.getDockMode()).toBe("split");

    prs.focusTerminal();

    // The foreign zoom is what was hiding it, so reveal has to drop that rather
    // than merely unhide a pane that was never hidden.
    expect(layout.zoomedLeafID()).toBeNull();
    expect(layout.hiddenTabKeys()).not.toContain("workspace");
    expect(layout.isTabActive("workspace")).toBe(true);
  });

  it("reveals a workspace tabbed behind a sibling pane", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    const layout = getPaneLayoutStore("prs");
    // Drag the workspace into the detail group and switch away from it: still
    // unhidden, still not rendered.
    layout.appendTabToLeaf("workspace", layout.leafIDForTab("conversation")!);
    layout.activateTab("conversation");
    expect(prs.getDockMode()).toBe("split"); // neither hidden nor maximized
    expect(layout.isTabActive("workspace")).toBe(false);

    prs.focusTerminal();

    expect(layout.isTabActive("workspace")).toBe(true);
    // Focus is noted as well as activation, because the narrow-width flattened
    // rendering picks its single visible tab from the last-focused one: without
    // this, revealing the terminal did nothing at all below the flatten width.
    expect(layout.lastFocusedTabKey()).toBe("workspace");
  });

  it("leaves the workspace's own zoom alone when revealing it", () => {
    // Focus Terminal reveals, it never maximizes - and it must not un-maximize
    // either, or asking for focus silently undoes the user's own layout.
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    prs.setDockMode("expanded");

    prs.focusTerminal();

    expect(prs.getDockMode()).toBe("expanded");
  });

  it("refuses to reshape the dock while a modal frame is open", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    prs.setDockMode("collapsed");
    const pop = pushModalFrame("test-frame", []);
    prs.focusTerminal();
    expect(prs.getDockMode()).toBe("collapsed");
    pop();
    prs.focusTerminal();
    expect(prs.getDockMode()).toBe("split");
  });
});

describe("promotable sessions", () => {
  beforeEach(() => {
    resetWorkspaceHostForTest();
    resetWorkspaceCreatePendingForTest();
    navigate("/pulls");
  });

  const sessions = [
    { paneKey: "session:ws-a//ws-a%3Ahelper", label: "Helper", hostKey: "ws-a//ws-a%3Ahelper/gen-1", active: true },
  ];

  it("offers the hosted workspace's sessions to the surface showing it", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, sessions);

    expect(prs.promotableSessions()).toEqual([{ paneKey: sessions[0]!.paneKey, label: "Helper" }]);
    // The registry key, which the pane's slot needs, stays behind the frontend
    // boundary rather than travelling to the views.
    expect(hostedSessionRegistryKey(sessions[0]!.paneKey)).toBe(sessions[0]!.hostKey);
  });

  it("offers nothing to a surface that is not hosting the workspace", () => {
    const prs = getInlineWorkspaceController("prs");
    const issues = getInlineWorkspaceController("issues");
    prs.claim(identityA, refA);
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, sessions);

    // One live terminal per session: a second surface claiming the same workspace
    // could not render it, so offering the pane there would give an empty one.
    expect(issues.promotableSessions()).toEqual([]);

    // And nothing while parked: no detail pane is rendering any of it.
    navigate("/activity");
    expect(prs.promotableSessions()).toEqual([]);
  });

  it("offers nothing while the published list names another workspace", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    publishHostedSessions({ workspaceId: "ws-other", hostKey: undefined }, sessions);

    // A stale list would offer panes for a workspace the surface stopped showing.
    expect(prs.promotableSessions()).toEqual([]);
    expect(hostedSessionRegistryKey(sessions[0]!.paneKey)).toBe(sessions[0]!.hostKey);
  });

  it("names the session the workspace pane is showing, for keyboard promotion", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, sessions);

    expect(activeHostedSession("prs")).toEqual({ paneKey: sessions[0]!.paneKey, label: "Helper" });
    // Surface-scoped like the pane list: a command must not reach a terminal
    // rendered on a page the user is not looking at.
    expect(activeHostedSession("issues")).toBeNull();
  });

  it("names no session while the workspace pane shows none", () => {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    // What the view publishes when the dock is collapsed and no workflow tab
    // holds a session: there is a session, but none of it is on screen.
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, [{ ...sessions[0]!, active: false }]);

    expect(prs.promotableSessions()).toHaveLength(1);
    expect(activeHostedSession("prs")).toBeNull();
  });
});

describe("a workspace spread across several panes", () => {
  beforeEach(() => {
    resetWorkspaceHostForTest();
    resetWorkspaceCreatePendingForTest();
    resetSessionHostForTest();
    navigate("/pulls");
  });

  const helperPane = "session:ws-a//ws-a%3Ahelper";
  const shellPane = "session:ws-a//ws-a%3Ashell";
  const sessions = [
    { paneKey: helperPane, label: "Helper", hostKey: "ws-a//ws-a%3Ahelper/gen-1", active: true },
    { paneKey: shellPane, label: "Shell", hostKey: "ws-a//ws-a%3Ashell/gen-1", active: false },
  ];

  /** A claimed workspace with `promoted` of its sessions in panes of their own. */
  function hostWithPromoted(promoted: readonly string[]): {
    prs: ReturnType<typeof getInlineWorkspaceController>;
    layout: ReturnType<typeof getPaneLayoutStore>;
  } {
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityA, refA);
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, sessions);
    const layout = getPaneLayoutStore("prs");
    layout.notePaneRender({
      editableTabs: ["conversation", "files", "workspace"],
      onScreenTabs: ["conversation", "files", "workspace"],
      flattened: false,
    });
    for (const paneKey of promoted) {
      expect(promoteSessionBesideWorkspace(layout, paneKey)).toBe(true);
    }
    return { prs, layout };
  }

  it("is not collapsed while one of its terminals holds a pane of its own", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);

    layout.setHidden("workspace", true);

    // The workspace is right there in the promoted pane, so reporting "collapsed"
    // would offer a Show Terminal button for something already on screen.
    expect(prs.getDockMode()).toBe("split");
  });

  it("collapses every pane of the workspace and restores exactly that set", () => {
    const { prs, layout } = hostWithPromoted([helperPane, shellPane]);
    // One promoted pane the user closed by hand before collapsing.
    layout.setHidden(shellPane, true);

    prs.setDockMode("collapsed");

    expect(prs.getDockMode()).toBe("collapsed");
    expect([...layout.hiddenTabKeys()].sort()).toEqual([helperPane, shellPane, "workspace"].sort());

    prs.setDockMode("split");

    // Expanding restores what the collapse hid, not the pane the user had already
    // put away: reappearing panes would undo their arrangement.
    expect(layout.hiddenTabKeys()).toEqual([shellPane]);
    expect(prs.getDockMode()).toBe("split");
  });

  it("does not let another workspace's expand restore this one's panes", () => {
    const { prs, layout } = hostWithPromoted([helperPane, shellPane]);
    layout.setHidden(shellPane, true);
    prs.setDockMode("collapsed");

    // The same surface then hosts another workspace and expands its dock. The
    // container pane is shared, so that expand legitimately reveals it - but
    // ws-a's promoted panes are not ws-b's to restore, and a ledger keyed by
    // surface alone would hand them over and consume the record ws-a still needs.
    prs.claim(identityB, { id: "ws-b", status: "ready" });
    publishHostedSessions({ workspaceId: "ws-b", hostKey: undefined }, []);
    prs.setDockMode("split");

    expect(layout.hiddenTabKeys()).toContain(helperPane);
    expect(layout.hiddenTabKeys()).toContain(shellPane);
  });

  it("is not expanded while the maximized leaf shows a sibling tab", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    // The promoted pane shares a leaf with the conversation and the conversation is
    // active: the leaf is maximized, but this workspace's terminal is behind it.
    layout.appendTabToLeaf(helperPane, layout.leafIDForTab("conversation")!);
    layout.activateTab("conversation");
    layout.toggleZoom(layout.leafIDForTab("conversation")!);

    // Reporting "expanded" here makes the control refuse the expand the user is
    // asking for, leaving the terminal behind a tab with no way forward.
    expect(prs.getDockMode()).toBe("split");
  });

  it("maximizes the pane holding the session the user was last in", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused(helperPane);

    prs.setDockMode("expanded");

    // Zooming the container would cover the very terminal the user asked to see.
    expect(prs.getDockMode()).toBe("expanded");
    expect(layout.zoomedLeafID()).toBe(layout.leafIDForTab(helperPane));
  });

  it("maximizes the container when the last-focused pane is the container", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused(helperPane);
    prs.notePaneFocused("workspace");

    prs.setDockMode("expanded");

    expect(layout.zoomedLeafID()).toBe(layout.leafIDForTab("workspace"));
  });

  it("files a focused pane under the workspace it names, not the one on screen", () => {
    // A focus event that lands while the surface is switching selections: the pane
    // names ws-a, the surface is already showing ws-b. Filing it under whatever is
    // hosted would give ws-b an expand target from ws-a's terminal, and lose ws-a's.
    const prs = getInlineWorkspaceController("prs");
    prs.claim(identityB, { id: "ws-b", status: "ready" });
    prs.notePaneFocused(helperPane);

    const { layout } = hostWithPromoted([helperPane]);
    prs.setDockMode("expanded");

    expect(layout.zoomedLeafID()).toBe(layout.leafIDForTab(helperPane));
  });

  it("keeps the last-focused session across a demotion and a visit elsewhere", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused(helperPane);

    // Another item, then back: the surface's own last-focused tab moves with every
    // click, so only a per-workspace record survives this.
    const issues = getInlineWorkspaceController("issues");
    navigate("/issues");
    issues.claim(identityB, { id: "ws-b", status: "ready" });
    navigate("/pulls");

    prs.setDockMode("expanded");
    expect(layout.zoomedLeafID()).toBe(layout.leafIDForTab(helperPane));

    // And a demotion falls back to the container rather than to a pane that is gone.
    prs.setDockMode("split");
    layout.demoteTab(helperPane);
    prs.setDockMode("expanded");
    expect(layout.zoomedLeafID()).toBe(layout.leafIDForTab("workspace"));
  });

  it("focusTerminal brings back every pane the collapse hid", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused("workspace");
    prs.setDockMode("collapsed");

    prs.focusTerminal();

    // The way back has to be the inverse of the collapse. Revealing only the
    // remembered pane returns an empty container here: a container masks the
    // sessions its workspace has promoted, so this workspace's one terminal would
    // stay hidden in a pane the user never asked to close.
    expect(layout.hiddenTabKeys()).toEqual([]);
  });

  it("restores a collapsed workspace after another one expanded on the same surface", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused("workspace");
    prs.setDockMode("collapsed");

    // Another item, expanded. The container tab is shared by every workspace on the
    // surface, so B's expand unhides it and A stops reporting "collapsed" while its
    // promoted terminal is still hidden.
    prs.claim(identityB, { id: "ws-b", status: "ready" });
    prs.setDockMode("expanded");
    prs.claim(identityA, refA);
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, sessions);

    prs.focusTerminal();

    // Keying the ledger stopped B from consuming A's record; reading the dock mode
    // instead of the record left A unable to spend it, which is the same one-way door
    // by another route.
    expect(layout.hiddenTabKeys()).not.toContain(helperPane);
  });

  it("keeps its collapse record when it is collapsed a second time", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused("workspace");
    prs.setDockMode("collapsed");

    // B's expand puts the shared container back, so A reports "split" again and the
    // user presses Collapse Terminal a second time - which sees only the container.
    prs.claim(identityB, { id: "ws-b", status: "ready" });
    prs.setDockMode("expanded");
    prs.claim(identityA, refA);
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, sessions);
    prs.setDockMode("collapsed");

    prs.focusTerminal();

    // Replacing the record instead of adding to it loses the promoted pane, and with
    // it the only route back to a terminal the user never closed.
    expect(layout.hiddenTabKeys()).toEqual([]);
  });

  it("focusTerminal reveals the promoted pane holding the last-focused session", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused(helperPane);
    layout.setHidden(helperPane, true);

    prs.focusTerminal();

    // Focus Terminal means "the terminal I was working in", which is no longer the
    // container: revealing that instead would leave the promoted pane closed.
    expect(layout.hiddenTabKeys()).not.toContain(helperPane);
    expect(layout.lastFocusedTabKey()).toBe(helperPane);
  });

  it("focusTerminal puts the keyboard in the promoted session's own terminal", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused(helperPane);
    // What the pane renders: the slot, with the pool's wrapper reparented into it.
    const slot = document.createElement("div");
    const wrapper = document.createElement("div");
    wrapper.setAttribute("data-session-host", sessions[0]!.hostKey);
    wrapper.tabIndex = -1;
    slot.appendChild(wrapper);
    document.body.appendChild(slot);
    registerSessionSlot(sessions[0]!.hostKey, slot);

    prs.focusTerminal();

    // The pool renders a promoted session outside the workspace host, so focusing
    // the host would land on the container the session was promoted out of.
    expect(document.activeElement).toBe(wrapper);
    expect(layout.hiddenTabKeys()).not.toContain(helperPane);
    slot.remove();
  });

  it("cancels a deferred session focus when the surface moves to another item", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused(helperPane);
    layout.setHidden(helperPane, true);

    // No slot registered: the pane's terminal has not mounted yet, so the request is
    // deferred for the pool to consume on attach.
    prs.focusTerminal();
    prs.claim(identityB, { id: "ws-b", status: "ready" });

    // The user is looking at another item now. Handing that request to whatever mounts
    // under the key next pulls the keyboard out of the detail they just opened.
    expect(consumeSessionFocus(sessions[0]!.hostKey)).toBe(false);
  });

  it("un-maximizes a promoted pane when the surface moves to another item", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);
    prs.notePaneFocused(helperPane);
    prs.setDockMode("expanded");
    expect(layout.zoomedLeafID()).toBe(layout.leafIDForTab(helperPane));

    // Selecting another item: the new detail must not open behind a fullscreen
    // terminal belonging to the item the user just left. The container's own leaf is
    // not the only one that can hold that zoom any more.
    prs.claim(identityB, { id: "ws-b", status: "ready" });

    expect(layout.zoomedLeafID()).toBeNull();
  });

  it("keeps a promoted pane across a relaunch under a new generation", () => {
    const { layout } = hostWithPromoted([helperPane]);

    // A relaunch reuses the session key and mints a new generation, so the pane key
    // is unchanged while the REGISTRY key changes - which is what keeps the new
    // terminal from being handed the dead one's subtree.
    const relaunched = { ...sessions[0]!, hostKey: "ws-a//ws-a%3Ahelper/gen-2" };
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, [relaunched, sessions[1]!]);

    expect(layout.hasTab(helperPane)).toBe(true);
    expect(hostedSessionRegistryKey(helperPane)).toBe("ws-a//ws-a%3Ahelper/gen-2");
  });

  it("drops a deleted workspace's panes from every surface", () => {
    const { layout } = hostWithPromoted([helperPane]);
    // A pane for another workspace on another surface, which the deletion must not
    // touch, and one for the doomed workspace, which it must.
    const issuesLayout = getPaneLayoutStore("issues");
    issuesLayout.notePaneRender({
      editableTabs: ["conversation", "workspace"],
      onScreenTabs: ["conversation", "workspace"],
      flattened: false,
    });
    expect(promoteSessionBesideWorkspace(issuesLayout, helperPane)).toBe(true);
    const survivor = "session:ws-b//ws-b%3Ahelper";
    expect(promoteSessionBesideWorkspace(issuesLayout, survivor)).toBe(true);

    notifyWorkspaceDeleted("ws-a");

    // The ID can never come back, so a stored pane for it is a pane that renders
    // nothing forever. Every surface, not just the claiming one: promotion is per
    // surface and the deletion arrives once.
    expect(layout.hasTab(helperPane)).toBe(false);
    expect(issuesLayout.hasTab(helperPane)).toBe(false);
    expect(issuesLayout.hasTab(survivor)).toBe(true);
  });

  it("keeps a stopped session's pane in the dock so a relaunch lands back in it", () => {
    const { prs, layout } = hostWithPromoted([helperPane]);

    // What a stop, an exit, or a reconnect gap looks like from here: the session is
    // simply absent from the published list. Purging on that would throw away the
    // placement the user chose every time a shell exited.
    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, [sessions[1]!]);
    expect(layout.hasTab(helperPane)).toBe(true);

    prs.setDockMode("collapsed");

    // Still part of the dock while absent: a retained pane left out of the collapse
    // would pop back on relaunch and flip the dock to split with no user action.
    expect([...layout.hiddenTabKeys()].sort()).toEqual([helperPane, "workspace"].sort());
    expect(prs.getDockMode()).toBe("collapsed");

    publishHostedSessions({ workspaceId: "ws-a", hostKey: undefined }, sessions);

    expect(prs.getDockMode()).toBe("collapsed");
    expect(layout.hasTab(helperPane)).toBe(true);
  });
});
