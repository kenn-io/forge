import { describe, it, expect, beforeEach } from "vitest";
import {
  acceptWorkspaceLaunch,
  beginWorkspaceCreate,
  beginWorkspaceDeletion,
  claimWorkspaceLaunch,
  clearCreatedWorkspaceById,
  completeAcceptedWorkspaceLaunch,
  createdWorkspaceRef,
  discardWorkspaceLaunch,
  endWorkspaceCreate,
  endWorkspaceDeletion,
  expireAcceptedWorkspaceLaunch,
  failWorkspaceLaunch,
  isWorkspaceCreatePending,
  isWorkspaceDeletionPending,
  markWorkspaceIdDeleted,
  nextWorkspaceLifecycleTick,
  pendingWorkspaceCreateLaunch,
  pendingWorkspaceLaunch,
  promoteWorkspaceCreateLaunch,
  queueWorkspaceLaunch,
  reconcileWorkspaceCreated,
  recordWorkspaceCreated,
  resetWorkspaceCreatePendingForTest,
  resolveControllerlessWorkspaceRef,
} from "./workspace-create-pending.svelte.js";
import type { WorkspaceItemIdentity, WorkspaceRefLite } from "../workspace-inline.js";

const pullIdentity: WorkspaceItemIdentity = {
  provider: "github",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 1,
  itemType: "pull",
};
const issueIdentity: WorkspaceItemIdentity = { ...pullIdentity, itemType: "issue" };
const createdRef: WorkspaceRefLite = { id: "ws-new", status: "provisioning" };
const staleRef: WorkspaceRefLite = { id: "ws-old", status: "ready" };

describe("workspace deletion pending", () => {
  beforeEach(() => {
    resetWorkspaceCreatePendingForTest();
  });

  it("tracks deletion by workspace and host identity", () => {
    beginWorkspaceDeletion("ws-1", undefined);
    beginWorkspaceDeletion("ws-1", "member-a");

    expect(isWorkspaceDeletionPending("ws-1", undefined)).toBe(true);
    expect(isWorkspaceDeletionPending("ws-1", "member-a")).toBe(true);
    expect(isWorkspaceDeletionPending("ws-1", "member-b")).toBe(false);

    endWorkspaceDeletion("ws-1", undefined);
    expect(isWorkspaceDeletionPending("ws-1", undefined)).toBe(false);
    expect(isWorkspaceDeletionPending("ws-1", "member-a")).toBe(true);
  });
});

describe("workspace creation state", () => {
  beforeEach(() => {
    resetWorkspaceCreatePendingForTest();
  });

  it("keeps pending creates independent by item identity until ended", () => {
    beginWorkspaceCreate(pullIdentity);
    expect(isWorkspaceCreatePending(pullIdentity)).toBe(true);
    expect(isWorkspaceCreatePending(issueIdentity)).toBe(false);

    beginWorkspaceCreate(issueIdentity);
    endWorkspaceCreate(pullIdentity);

    expect(isWorkspaceCreatePending(pullIdentity)).toBe(false);
    expect(isWorkspaceCreatePending(issueIdentity)).toBe(true);
  });

  it("records confirmed creations by identity and clears them by workspace ID", () => {
    const issueRef: WorkspaceRefLite = { id: "ws-issue", status: "ready" };
    recordWorkspaceCreated(pullIdentity, createdRef);
    recordWorkspaceCreated(issueIdentity, issueRef);

    expect(createdWorkspaceRef(pullIdentity)).toEqual(createdRef);
    expect(createdWorkspaceRef(issueIdentity)).toEqual(issueRef);

    clearCreatedWorkspaceById(createdRef.id);
    expect(createdWorkspaceRef(pullIdentity)).toBeNull();
    expect(createdWorkspaceRef(issueIdentity)).toEqual(issueRef);
  });

  it.each([
    ["null without a tick", null, "none", true],
    ["null from a pre-confirmation request", null, "before", true],
    ["a different ID from a pre-confirmation request", staleRef, "before", true],
    ["a same-ID envelope", createdRef, "before", false],
    ["null from a post-confirmation request", null, "after", false],
    ["a different ID from a post-confirmation request", staleRef, "after", false],
  ] as const)("reconciles %s", (_name, envelope, timing, retained) => {
    const beforeConfirmation = nextWorkspaceLifecycleTick();
    recordWorkspaceCreated(pullIdentity, createdRef);
    const envelopeTick =
      timing === "before" ? beforeConfirmation : timing === "after" ? nextWorkspaceLifecycleTick() : undefined;

    reconcileWorkspaceCreated(pullIdentity, envelope, envelopeTick);

    expect(createdWorkspaceRef(pullIdentity)).toEqual(retained ? createdRef : null);
  });

  it("resolves controller-less refs from creation records and deleted IDs", () => {
    expect(resolveControllerlessWorkspaceRef(pullIdentity, staleRef)).toEqual(staleRef);

    markWorkspaceIdDeleted(staleRef.id);
    expect(resolveControllerlessWorkspaceRef(pullIdentity, staleRef)).toBeNull();

    recordWorkspaceCreated(pullIdentity, createdRef);
    expect(resolveControllerlessWorkspaceRef(pullIdentity, staleRef)).toEqual(createdRef);
    expect(resolveControllerlessWorkspaceRef(pullIdentity, { id: "ws-other", status: "ready" })).toEqual(createdRef);
    expect(resolveControllerlessWorkspaceRef(pullIdentity, { ...createdRef, status: "ready" })).toEqual({
      ...createdRef,
      status: "ready",
    });
  });

  it("rejects delayed publication for a deleted ID but accepts a fresh recreation", () => {
    markWorkspaceIdDeleted(createdRef.id);
    recordWorkspaceCreated(pullIdentity, createdRef);
    expect(createdWorkspaceRef(pullIdentity)).toBeNull();

    const recreated: WorkspaceRefLite = { id: "ws-fresh", status: "provisioning" };
    recordWorkspaceCreated(pullIdentity, recreated);
    expect(createdWorkspaceRef(pullIdentity)).toEqual(recreated);
  });
});

describe("workspace-create-pending launch intent", () => {
  beforeEach(() => {
    resetWorkspaceCreatePendingForTest();
  });

  it("promotes an item-scoped target to its workspace without ending creation", () => {
    beginWorkspaceCreate(pullIdentity, " codex ");

    expect(pendingWorkspaceCreateLaunch(pullIdentity)).toBe("codex");

    promoteWorkspaceCreateLaunch(pullIdentity, "ws-1", undefined);

    expect(pendingWorkspaceLaunch("ws-1", undefined)).toMatchObject({ phase: "queued", targetKey: "codex" });
    expect(pendingWorkspaceCreateLaunch(pullIdentity)).toBeNull();
    expect(isWorkspaceCreatePending(pullIdentity)).toBe(true);
  });

  it("clears an item-scoped target when creation ends without a workspace", () => {
    beginWorkspaceCreate(pullIdentity, "codex");

    endWorkspaceCreate(pullIdentity);

    expect(isWorkspaceCreatePending(pullIdentity)).toBe(false);
    expect(pendingWorkspaceCreateLaunch(pullIdentity)).toBeNull();
    expect(pendingWorkspaceLaunch("ws-1", undefined)).toBeNull();
  });

  it("queues and discards an unclaimed target key exactly once per workspace", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    queueWorkspaceLaunch("ws-2", "claude", undefined);

    expect(pendingWorkspaceLaunch("ws-1", undefined)).toMatchObject({ phase: "queued", targetKey: "codex" });
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBe("codex");
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBeNull();
    expect(pendingWorkspaceLaunch("ws-2", undefined)).toMatchObject({ phase: "queued", targetKey: "claude" });
  });

  it("replaces an earlier target when the user makes a newer explicit choice", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    queueWorkspaceLaunch("ws-1", "claude", undefined);
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBe("claude");
  });

  it("keeps launch intents independent across hosts sharing a workspace ID", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    queueWorkspaceLaunch("ws-1", "claude", "member");

    expect(pendingWorkspaceLaunch("ws-1", undefined)?.targetKey).toBe("codex");
    expect(pendingWorkspaceLaunch("ws-1", "member")?.targetKey).toBe("claude");
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBe("codex");
    expect(pendingWorkspaceLaunch("ws-1", "member")?.targetKey).toBe("claude");
  });

  it("moves a claimed launch to awaiting-session until its exact session appears", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);

    const claim = claimWorkspaceLaunch("ws-1", undefined);
    expect(claim).toMatchObject({ workspaceId: "ws-1", targetKey: "codex" });
    expect(pendingWorkspaceLaunch("ws-1", undefined)).toMatchObject({ phase: "launching", targetKey: "codex" });
    expect(claimWorkspaceLaunch("ws-1", undefined)).toBeNull();
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBeNull();

    expect(acceptWorkspaceLaunch(claim!, "ws-1:codex", 1_000)).toBe(true);
    expect(pendingWorkspaceLaunch("ws-1", undefined)).toEqual({
      workspaceId: "ws-1",
      workspaceHostKey: undefined,
      targetKey: "codex",
      phase: "awaiting_session",
      sessionKey: "ws-1:codex",
      acceptedAt: 1_000,
    });
    expect(completeAcceptedWorkspaceLaunch("ws-1", undefined, "other-session")).toBe(false);
    expect(completeAcceptedWorkspaceLaunch("ws-1", undefined, "ws-1:codex")).toBe(true);
    expect(pendingWorkspaceLaunch("ws-1", undefined)).toBeNull();
  });

  it("settles a failed claimed launch without touching a newer intent", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    const claim = claimWorkspaceLaunch("ws-1", undefined);
    expect(claim).not.toBeNull();

    queueWorkspaceLaunch("ws-1", "claude", undefined);

    expect(failWorkspaceLaunch(claim!)).toBe(false);
    expect(pendingWorkspaceLaunch("ws-1", undefined)?.targetKey).toBe("claude");

    const nextClaim = claimWorkspaceLaunch("ws-1", undefined);
    expect(failWorkspaceLaunch(nextClaim!)).toBe(true);
    expect(pendingWorkspaceLaunch("ws-1", undefined)).toBeNull();
  });

  it("does not let an expired reconciliation clear a newer accepted launch", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    const oldClaim = claimWorkspaceLaunch("ws-1", undefined);
    expect(acceptWorkspaceLaunch(oldClaim!, "ws-1:codex", 1_000)).toBe(true);

    queueWorkspaceLaunch("ws-1", "codex", undefined);
    const newClaim = claimWorkspaceLaunch("ws-1", undefined);
    expect(acceptWorkspaceLaunch(newClaim!, "ws-1:codex", 2_000)).toBe(true);

    expect(expireAcceptedWorkspaceLaunch("ws-1", undefined, "ws-1:codex", 1_000)).toBe(false);
    expect(pendingWorkspaceLaunch("ws-1", undefined)).toMatchObject({
      phase: "awaiting_session",
      sessionKey: "ws-1:codex",
      acceptedAt: 2_000,
    });
  });

  it("clears explicit launch intent in the shared test reset", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    resetWorkspaceCreatePendingForTest();
    expect(pendingWorkspaceLaunch("ws-1", undefined)).toBeNull();
    expect(isWorkspaceDeletionPending("ws-1", undefined)).toBe(false);
  });
});
