import { describe, expect, it, beforeEach } from "vite-plus/test";
import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
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
  desiredKey,
  desiredSlot,
  getInlineWorkspaceController,
  isHostVisible,
  notifyWorkspaceDeleted,
  onIdentityInvalidated,
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
    expect(isHostVisible()).toBe(true);
    prs.setDockMode("collapsed");
    expect(desiredSlot()).toBe("prs"); // still claimed
    expect(isHostVisible()).toBe(false); // hostVisible contract applies
    prs.setDockMode("split");
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
    // Direct replacement (selection change whose new detail already
    // matches) never gives WorkspaceDockPanel an inactive gap, so the
    // store must reset the mode itself or B's detail opens hidden behind
    // a fullscreen terminal.
    prs.claim(identityB, { id: "ws-b", status: "ready" });
    expect(prs.getDockMode()).toBe("split");
    // Same-identity re-asserts (ref status changes) keep expanded intact.
    prs.setDockMode("expanded");
    prs.claim(identityB, { id: "ws-b", status: "running" });
    expect(prs.getDockMode()).toBe("expanded");
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
