import { beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { getActionsByOwner, getAllActions, resetRegistry } from "./registry.svelte.js";
import { registerPRDetailActions } from "./pr-detail-actions.js";
import type { PRDetailActionInput } from "../../../../../packages/ui/src/components/detail/keyboard-actions.js";
import type { Context } from "./types.js";

const ctx = {} as Context;

const expectedIds = ["pr.approve", "pr.ready", "pr.approveWorkflows"];

function buildOpenApprovableInput(overrides: Partial<PRDetailActionInput> = {}): PRDetailActionInput {
  const stores = {
    pulls: { loadPulls: vi.fn().mockResolvedValue(undefined) },
    detail: {
      loadDetail: vi.fn().mockResolvedValue(undefined),
      refreshDetailOnly: vi.fn().mockResolvedValue(undefined),
    },
  };
  return {
    pr: { State: "open", IsDraft: false, MergeableState: "clean" },
    ref: {
      provider: "github",
      owner: "acme",
      name: "core",
      repoPath: "acme/core",
    },
    number: 42,
    viewerCan: {
      approve: true,
      merge: true,
      markReady: true,
      approveWorkflows: true,
    },
    repoSettings: null,
    stale: false,
    stores: stores as unknown as PRDetailActionInput["stores"],
    client: {} as PRDetailActionInput["client"],
    approveCommentBody: "",
    ...overrides,
  };
}

describe("registerPRDetailActions", () => {
  beforeEach(() => resetRegistry());

  it("registers the three PR-detail palette commands", () => {
    registerPRDetailActions(() => null);
    const ids = getActionsByOwner("pr-detail-actions").map((a) => a.id);
    expect(ids).toEqual(expectedIds);
  });

  it("when getInput returns null, every action's when() is false", () => {
    registerPRDetailActions(() => null);
    const actions = getActionsByOwner("pr-detail-actions");
    for (const action of actions) {
      expect(action.when(ctx)).toBe(false);
    }
  });

  it("when getInput returns null, handler is a no-op (does not throw)", async () => {
    registerPRDetailActions(() => null);
    const actions = getActionsByOwner("pr-detail-actions");
    for (const action of actions) {
      await expect(Promise.resolve(action.handler(ctx))).resolves.toBeUndefined();
    }
  });

  it("pr.approve.when() is true when the input PR is approvable", () => {
    const input = buildOpenApprovableInput();
    registerPRDetailActions(() => input);
    const approve = getActionsByOwner("pr-detail-actions").find((a) => a.id === "pr.approve");
    expect(approve?.when(ctx)).toBe(true);
  });

  it("pr.ready.when() respects IsDraft on the input PR", () => {
    const draftInput = buildOpenApprovableInput({
      pr: { State: "open", IsDraft: true, MergeableState: "clean" },
    });
    registerPRDetailActions(() => draftInput);
    const ready = getActionsByOwner("pr-detail-actions").find((a) => a.id === "pr.ready");
    expect(ready?.when(ctx)).toBe(true);

    resetRegistry();
    const nonDraftInput = buildOpenApprovableInput();
    registerPRDetailActions(() => nonDraftInput);
    const readyOnNonDraft = getActionsByOwner("pr-detail-actions").find((a) => a.id === "pr.ready");
    expect(readyOnNonDraft?.when(ctx)).toBe(false);
  });

  it("pr.approve.when() is false when viewerCan.approve is false", () => {
    const input = buildOpenApprovableInput({
      viewerCan: {
        approve: false,
        merge: false,
        markReady: false,
        approveWorkflows: false,
      },
    });
    registerPRDetailActions(() => input);
    const approve = getActionsByOwner("pr-detail-actions").find((a) => a.id === "pr.approve");
    expect(approve?.when(ctx)).toBe(false);
  });

  it("pr.approve.when() is false when the PR is closed", () => {
    const input = buildOpenApprovableInput({
      pr: { State: "closed", IsDraft: false, MergeableState: "clean" },
    });
    registerPRDetailActions(() => input);
    const approve = getActionsByOwner("pr-detail-actions").find((a) => a.id === "pr.approve");
    expect(approve?.when(ctx)).toBe(false);
  });

  it("does NOT register a pr.merge command (deferred)", () => {
    registerPRDetailActions(() => null);
    const ids = getActionsByOwner("pr-detail-actions").map((a) => a.id);
    expect(ids).not.toContain("pr.merge");
  });

  it("cleanup removes the actions from the registry", () => {
    const cleanup = registerPRDetailActions(() => null);
    expect(getAllActions().some((a) => a.id === "pr.approve")).toBe(true);
    cleanup();
    expect(getAllActions().some((a) => a.id === "pr.approve")).toBe(false);
    expect(getActionsByOwner("pr-detail-actions")).toEqual([]);
  });

  it("re-registering replaces the previous actions in place", () => {
    const cleanup = registerPRDetailActions(() => null);
    const cleanup2 = registerPRDetailActions(() => null);
    // Only one set of three should be present, not six.
    expect(getActionsByOwner("pr-detail-actions")).toHaveLength(3);
    cleanup();
    cleanup2();
    expect(getActionsByOwner("pr-detail-actions")).toEqual([]);
  });
});
