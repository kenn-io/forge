import { describe, expect, it, vi } from "vite-plus/test";
import type { ProblemBody } from "../../api/problems.js";
import type { DetailStore, ProviderActionCallbacks } from "../../stores/detail.svelte.js";

import {
  canApprovePR,
  canApproveWorkflows,
  canMarkReady,
  canOpenMerge,
  runApprovePR,
  runApproveWorkflows,
  runMarkReady,
  runOpenMerge,
  type PRDetailActionInput,
} from "./keyboard-actions.js";

function fakeStores() {
  return {
    detail: {
      approvePull: vi.fn<DetailStore["approvePull"]>(),
      markPullReady: vi.fn<DetailStore["markPullReady"]>(),
      approvePullWorkflows: vi.fn<DetailStore["approvePullWorkflows"]>(),
    },
  };
}

interface BuildOpts {
  state?: "open" | "closed" | "merged";
  isDraft?: boolean;
  mergeableState?: string;
  approve?: boolean;
  merge?: boolean;
  markReady?: boolean;
  approveWorkflows?: boolean;
  stale?: boolean;
  withRepoSettings?: boolean;
  repoSettings?: PRDetailActionInput["repoSettings"];
  stores?: ReturnType<typeof fakeStores>;
  setMergeModalOpen?: (open: boolean) => void;
  onAfterOpenMerge?: () => void;
  onCompleted?: () => void;
  onError?: (msg: string) => void;
  approveCommentBody?: string;
  platformHost?: string;
  platformHeadSha?: string;
  expectedHeadSha?: string;
  requireHeadPin?: boolean;
  onHeadConflict?: (
    reason: "stale_state" | "head_unknown",
    context: string | undefined,
    expectedHeadSha: string,
  ) => void;
}

function buildInput(opts: BuildOpts = {}): PRDetailActionInput {
  const stores = opts.stores ?? fakeStores();
  return {
    pr: {
      State: opts.state ?? "open",
      IsDraft: opts.isDraft ?? false,
      MergeableState: opts.mergeableState ?? "clean",
      platform_head_sha: opts.platformHeadSha,
    },
    ref: {
      provider: "github",
      platformHost: opts.platformHost ?? "github.com",
      owner: "octo",
      name: "repo",
      repoPath: "octo/repo",
    },
    number: 42,
    viewerCan: {
      approve: opts.approve ?? true,
      merge: opts.merge ?? true,
      markReady: opts.markReady ?? true,
      approveWorkflows: opts.approveWorkflows ?? true,
    },
    repoSettings:
      opts.repoSettings ??
      (opts.withRepoSettings === false
        ? null
        : {
            allowSquash: true,
            allowMerge: true,
            allowRebase: true,
            viewerCanMerge: true,
          }),
    stale: opts.stale ?? false,
    requireHeadPin: opts.requireHeadPin ?? false,
    stores,
    ...(opts.approveCommentBody !== undefined && {
      approveCommentBody: opts.approveCommentBody,
    }),
    ...(opts.setMergeModalOpen !== undefined && {
      setMergeModalOpen: opts.setMergeModalOpen,
    }),
    ...(opts.onAfterOpenMerge !== undefined && {
      onAfterOpenMerge: opts.onAfterOpenMerge,
    }),
    ...(opts.onCompleted !== undefined && {
      onCompleted: opts.onCompleted,
    }),
    ...(opts.onError !== undefined && {
      onError: opts.onError,
    }),
    ...(opts.expectedHeadSha !== undefined && {
      expectedHeadSha: opts.expectedHeadSha,
    }),
    ...(opts.onHeadConflict !== undefined && {
      onHeadConflict: opts.onHeadConflict,
    }),
  };
}

function conflictProblem(reason: string, context?: string) {
  const problem: ProblemBody = {
    type: "about:blank",
    title: "Conflict",
    status: 409,
    detail: "target changed since it was reviewed; refresh and retry",
    code: "conflict",
    details: { reason, ...(context !== undefined && { context }) },
  };
  return problem;
}

// canApprovePR --------------------------------------------------------

describe("canApprovePR", () => {
  it("returns false for closed PR", () => {
    expect(canApprovePR(buildInput({ state: "closed" }))).toBe(false);
  });

  it("returns false when viewer lacks approve capability", () => {
    expect(canApprovePR(buildInput({ approve: false }))).toBe(false);
  });

  it("returns false when stale", () => {
    expect(canApprovePR(buildInput({ stale: true }))).toBe(false);
  });

  it("returns true for open PR with approve capability", () => {
    expect(canApprovePR(buildInput())).toBe(true);
  });

  it("returns true when approval head pinning is unavailable", () => {
    expect(canApprovePR(buildInput({ requireHeadPin: true }))).toBe(true);
  });
});

// runApprovePR --------------------------------------------------------

describe("runApprovePR", () => {
  it("launches approval through the detail store with a trimmed generated body", () => {
    const stores = fakeStores();
    runApprovePR(
      buildInput({
        stores,
        approveCommentBody: " hello ",
        platformHeadSha: " platform-head ",
      }),
    );

    expect(stores.detail.approvePull).toHaveBeenCalledWith(
      {
        provider: "github",
        platformHost: "github.com",
        owner: "octo",
        name: "repo",
        repoPath: "octo/repo",
      },
      42,
      { body: "hello", expected_head_sha: "platform-head" },
      expect.any(Object),
    );
  });

  it("wires acknowledged failure and success callbacks", () => {
    const stores = fakeStores();
    const onError = vi.fn();
    const onCompleted = vi.fn();
    runApprovePR(buildInput({ stores, onError, onCompleted }));
    const callbacks = stores.detail.approvePull.mock.calls[0]?.[3];

    callbacks?.onFailure?.("boom");
    callbacks?.onSuccess?.();

    expect(onError).toHaveBeenCalledWith("boom");
    expect(onCompleted).toHaveBeenCalledOnce();
  });

  it("does nothing when canApprovePR is false", () => {
    const stores = fakeStores();
    runApprovePR(buildInput({ stores, state: "closed" }));
    expect(stores.detail.approvePull).not.toHaveBeenCalled();
  });

  it("uses the reviewed head when the platform head is unavailable", () => {
    const stores = fakeStores();
    runApprovePR(
      buildInput({
        stores,
        expectedHeadSha: " abc123 ",
      }),
    );
    expect(stores.detail.approvePull.mock.calls[0]?.[2]).toEqual({
      body: "",
      expected_head_sha: "abc123",
    });
  });

  it("omits the head pin when no synced or reviewed head is known", () => {
    const stores = fakeStores();
    runApprovePR(buildInput({ stores, expectedHeadSha: "" }));
    expect(stores.detail.approvePull.mock.calls[0]?.[2]).toEqual({ body: "" });
  });

  it("reports stale_state and head_unknown problems via onHeadConflict", () => {
    for (const reason of ["stale_state", "head_unknown"] as const) {
      const stores = fakeStores();
      const onHeadConflict = vi.fn();
      runApprovePR(buildInput({ stores, expectedHeadSha: "abc123", onHeadConflict }));
      const callbacks: ProviderActionCallbacks | undefined = stores.detail.approvePull.mock.calls[0]?.[3];
      callbacks?.onProblem?.(conflictProblem(reason));

      expect(onHeadConflict).toHaveBeenCalledWith(
        reason,
        undefined,
        "abc123",
        {
          provider: "github",
          platformHost: "github.com",
          owner: "octo",
          name: "repo",
          repoPath: "octo/repo",
        },
        42,
      );
    }
  });

  it("forwards provider side-effect context to onHeadConflict", () => {
    const sideEffect = "approval 31 may stand on a moved head: dismissal failed";
    const stores = fakeStores();
    const onHeadConflict = vi.fn();
    runApprovePR(buildInput({ stores, expectedHeadSha: "abc123", onHeadConflict }));
    stores.detail.approvePull.mock.calls[0]?.[3]?.onProblem?.(conflictProblem("stale_state", sideEffect));

    expect(onHeadConflict).toHaveBeenCalledWith(
      "stale_state",
      sideEffect,
      "abc123",
      {
        provider: "github",
        platformHost: "github.com",
        owner: "octo",
        name: "repo",
        repoPath: "octo/repo",
      },
      42,
    );
  });

  it("does not report generic conflicts via onHeadConflict", () => {
    const stores = fakeStores();
    const onHeadConflict = vi.fn();
    runApprovePR(buildInput({ stores, expectedHeadSha: "abc123", onHeadConflict }));
    stores.detail.approvePull.mock.calls[0]?.[3]?.onProblem?.(conflictProblem("merge_conflict_or_unknown"));

    expect(onHeadConflict).not.toHaveBeenCalled();
  });
});

// canOpenMerge --------------------------------------------------------

describe("canOpenMerge", () => {
  it("returns false when repoSettings has not loaded", () => {
    expect(canOpenMerge(buildInput({ withRepoSettings: false }))).toBe(false);
  });

  it("returns false for closed PR", () => {
    expect(canOpenMerge(buildInput({ state: "closed" }))).toBe(false);
  });

  it("returns false when viewer lacks merge capability", () => {
    expect(canOpenMerge(buildInput({ merge: false }))).toBe(false);
  });

  it("returns false when the viewer lacks repo merge permission", () => {
    expect(
      canOpenMerge(
        buildInput({
          repoSettings: {
            allowSquash: true,
            allowMerge: true,
            allowRebase: true,
            viewerCanMerge: false,
          },
        }),
      ),
    ).toBe(false);
  });

  it("returns false when PR has merge conflicts (dirty)", () => {
    expect(canOpenMerge(buildInput({ mergeableState: "dirty" }))).toBe(false);
  });

  it("returns false when head binding requires a reviewed head and only the platform head is known", () => {
    expect(
      canOpenMerge(
        buildInput({
          requireHeadPin: true,
          platformHeadSha: "synced-head",
          expectedHeadSha: "",
        }),
      ),
    ).toBe(false);
  });

  it("returns true when head binding requires and has a reviewed head", () => {
    expect(canOpenMerge(buildInput({ requireHeadPin: true, expectedHeadSha: "reviewed-head" }))).toBe(true);
  });

  it("returns true for clean open PR with merge capability", () => {
    expect(canOpenMerge(buildInput())).toBe(true);
  });
});

// runOpenMerge --------------------------------------------------------

describe("runOpenMerge", () => {
  it("flips setMergeModalOpen(true) and calls onAfterOpenMerge", () => {
    const setOpen = vi.fn();
    const after = vi.fn();
    runOpenMerge(
      buildInput({
        setMergeModalOpen: setOpen,
        onAfterOpenMerge: after,
      }),
    );
    expect(setOpen).toHaveBeenCalledWith(true);
    expect(after).toHaveBeenCalledTimes(1);
  });

  it("does nothing when canOpenMerge is false (e.g. dirty)", () => {
    const setOpen = vi.fn();
    runOpenMerge(
      buildInput({
        mergeableState: "dirty",
        setMergeModalOpen: setOpen,
      }),
    );
    expect(setOpen).not.toHaveBeenCalled();
  });
});

// canMarkReady -------------------------------------------------------

describe("canMarkReady", () => {
  it("returns false when PR is not a draft", () => {
    expect(canMarkReady(buildInput({ isDraft: false }))).toBe(false);
  });

  it("returns false when viewer lacks markReady capability", () => {
    expect(canMarkReady(buildInput({ isDraft: true, markReady: false }))).toBe(false);
  });

  it("returns true for draft PR with markReady capability", () => {
    expect(canMarkReady(buildInput({ isDraft: true }))).toBe(true);
  });
});

// runMarkReady --------------------------------------------------------

describe("runMarkReady", () => {
  it("launches the acknowledged ready command and forwards completion", () => {
    const stores = fakeStores();
    const onCompleted = vi.fn();
    runMarkReady(
      buildInput({
        stores,
        isDraft: true,
        onCompleted,
      }),
    );
    expect(stores.detail.markPullReady).toHaveBeenCalledWith(
      {
        provider: "github",
        platformHost: "github.com",
        owner: "octo",
        name: "repo",
        repoPath: "octo/repo",
      },
      42,
      expect.any(Object),
    );

    stores.detail.markPullReady.mock.calls[0]?.[2]?.onSuccess?.();

    expect(onCompleted).toHaveBeenCalledTimes(1);
  });

  it("does nothing when not a draft", () => {
    const stores = fakeStores();
    runMarkReady(buildInput({ stores, isDraft: false }));
    expect(stores.detail.markPullReady).not.toHaveBeenCalled();
  });

  it("forwards acknowledged failures", () => {
    const stores = fakeStores();
    const onError = vi.fn();
    runMarkReady(buildInput({ stores, isDraft: true, onError }));

    stores.detail.markPullReady.mock.calls[0]?.[2]?.onFailure?.("permission denied");

    expect(onError).toHaveBeenCalledWith("permission denied");
  });
});

// canApproveWorkflows ------------------------------------------------

describe("canApproveWorkflows", () => {
  it("returns false for closed PR", () => {
    expect(canApproveWorkflows(buildInput({ state: "closed" }))).toBe(false);
  });

  it("returns false when viewer lacks approveWorkflows", () => {
    expect(canApproveWorkflows(buildInput({ approveWorkflows: false }))).toBe(false);
  });

  it("returns true for open PR with workflow capability", () => {
    expect(canApproveWorkflows(buildInput())).toBe(true);
  });
});

// runApproveWorkflows ------------------------------------------------

describe("runApproveWorkflows", () => {
  it("launches the acknowledged workflow approval command", () => {
    const stores = fakeStores();
    const onCompleted = vi.fn();
    runApproveWorkflows(
      buildInput({
        stores,
        onCompleted,
      }),
    );
    expect(stores.detail.approvePullWorkflows).toHaveBeenCalledWith(
      {
        provider: "github",
        platformHost: "github.com",
        owner: "octo",
        name: "repo",
        repoPath: "octo/repo",
      },
      42,
      expect.any(Object),
    );

    stores.detail.approvePullWorkflows.mock.calls[0]?.[2]?.onSuccess?.();

    expect(onCompleted).toHaveBeenCalledTimes(1);
  });

  it("forwards acknowledged workflow failures", () => {
    const stores = fakeStores();
    const onError = vi.fn();
    runApproveWorkflows(buildInput({ stores, onError }));
    stores.detail.approvePullWorkflows.mock.calls[0]?.[2]?.onFailure?.("no pending workflows");

    expect(onError).toHaveBeenCalledWith("no pending workflows");
  });

  it("does nothing when canApproveWorkflows is false", () => {
    const stores = fakeStores();
    runApproveWorkflows(
      buildInput({
        stores,
        approveWorkflows: false,
      }),
    );
    expect(stores.detail.approvePullWorkflows).not.toHaveBeenCalled();
  });
});
