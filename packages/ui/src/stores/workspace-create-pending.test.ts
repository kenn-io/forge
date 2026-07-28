import { describe, it, expect, beforeEach } from "vitest";
import {
  queueWorkspaceLaunch,
  pendingWorkspaceLaunchTarget,
  consumeWorkspaceLaunch,
  resetWorkspaceCreatePendingForTest,
} from "./workspace-create-pending.svelte.js";

describe("workspace-create-pending launch intent", () => {
  beforeEach(() => {
    resetWorkspaceCreatePendingForTest();
  });

  it("queues and consumes a target key exactly once per workspace", () => {
    queueWorkspaceLaunch("ws-1", "codex");
    queueWorkspaceLaunch("ws-2", "claude");

    expect(pendingWorkspaceLaunchTarget("ws-1")).toBe("codex");
    expect(consumeWorkspaceLaunch("ws-1")).toBe("codex");
    expect(consumeWorkspaceLaunch("ws-1")).toBeNull();
    expect(pendingWorkspaceLaunchTarget("ws-2")).toBe("claude");
  });

  it("replaces an earlier target when the user makes a newer explicit choice", () => {
    queueWorkspaceLaunch("ws-1", "codex");
    queueWorkspaceLaunch("ws-1", "claude");
    expect(consumeWorkspaceLaunch("ws-1")).toBe("claude");
  });

  it("clears explicit launch intent in the shared test reset", () => {
    queueWorkspaceLaunch("ws-1", "codex");
    resetWorkspaceCreatePendingForTest();
    expect(pendingWorkspaceLaunchTarget("ws-1")).toBeNull();
  });
});
