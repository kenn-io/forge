import { describe, it, expect, beforeEach } from "vitest";
import {
  acceptWorkspaceLaunch,
  beginWorkspaceCreate,
  beginWorkspaceDeletion,
  claimWorkspaceLaunch,
  completeAcceptedWorkspaceLaunch,
  discardWorkspaceLaunch,
  endWorkspaceCreate,
  endWorkspaceDeletion,
  failWorkspaceLaunch,
  isWorkspaceCreatePending,
  isWorkspaceDeletionPending,
  pendingWorkspaceCreateLaunch,
  pendingWorkspaceLaunch,
  promoteWorkspaceCreateLaunch,
  queueWorkspaceLaunch,
  resetWorkspaceCreatePendingForTest,
} from "./workspace-create-pending.svelte.js";
import type { WorkspaceItemIdentity } from "../workspace-inline.js";

const pullIdentity: WorkspaceItemIdentity = {
  provider: "github",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 1,
  itemType: "pull",
};

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

  it("clears explicit launch intent in the shared test reset", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    resetWorkspaceCreatePendingForTest();
    expect(pendingWorkspaceLaunch("ws-1", undefined)).toBeNull();
    expect(isWorkspaceDeletionPending("ws-1", undefined)).toBe(false);
  });
});
