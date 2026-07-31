import { describe, it, expect, beforeEach } from "vitest";
import {
  claimWorkspaceLaunch,
  completeWorkspaceLaunch,
  discardWorkspaceLaunch,
  queueWorkspaceLaunch,
  pendingWorkspaceLaunchTarget,
  resetWorkspaceCreatePendingForTest,
} from "./workspace-create-pending.svelte.js";

describe("workspace-create-pending launch intent", () => {
  beforeEach(() => {
    resetWorkspaceCreatePendingForTest();
  });

  it("queues and discards an unclaimed target key exactly once per workspace", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    queueWorkspaceLaunch("ws-2", "claude", undefined);

    expect(pendingWorkspaceLaunchTarget("ws-1", undefined)).toBe("codex");
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBe("codex");
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBeNull();
    expect(pendingWorkspaceLaunchTarget("ws-2", undefined)).toBe("claude");
  });

  it("replaces an earlier target when the user makes a newer explicit choice", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    queueWorkspaceLaunch("ws-1", "claude", undefined);
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBe("claude");
  });

  it("keeps launch intents independent across hosts sharing a workspace ID", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    queueWorkspaceLaunch("ws-1", "claude", "member");

    expect(pendingWorkspaceLaunchTarget("ws-1", undefined)).toBe("codex");
    expect(pendingWorkspaceLaunchTarget("ws-1", "member")).toBe("claude");
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBe("codex");
    expect(pendingWorkspaceLaunchTarget("ws-1", "member")).toBe("claude");
  });

  it("keeps a claimed target pending while preventing a second launcher from claiming it", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);

    const claim = claimWorkspaceLaunch("ws-1", undefined);
    expect(claim).toMatchObject({ workspaceId: "ws-1", targetKey: "codex" });
    expect(pendingWorkspaceLaunchTarget("ws-1", undefined)).toBe("codex");
    expect(claimWorkspaceLaunch("ws-1", undefined)).toBeNull();
    expect(discardWorkspaceLaunch("ws-1", undefined)).toBeNull();
    expect(pendingWorkspaceLaunchTarget("ws-1", undefined)).toBe("codex");
    expect(completeWorkspaceLaunch(claim!)).toBe(true);
    expect(pendingWorkspaceLaunchTarget("ws-1", undefined)).toBeNull();
    expect(completeWorkspaceLaunch(claim!)).toBe(false);
  });

  it("does not let an old claim complete a newer intent for the same workspace", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    const claim = claimWorkspaceLaunch("ws-1", undefined);
    expect(claim).not.toBeNull();

    queueWorkspaceLaunch("ws-1", "claude", undefined);

    expect(completeWorkspaceLaunch(claim!)).toBe(false);
    expect(pendingWorkspaceLaunchTarget("ws-1", undefined)).toBe("claude");
  });

  it("clears explicit launch intent in the shared test reset", () => {
    queueWorkspaceLaunch("ws-1", "codex", undefined);
    resetWorkspaceCreatePendingForTest();
    expect(pendingWorkspaceLaunchTarget("ws-1", undefined)).toBeNull();
  });
});
