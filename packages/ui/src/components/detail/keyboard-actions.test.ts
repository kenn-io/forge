import { describe, expect, it, vi } from "vite-plus/test";
import type { Mock } from "vite-plus/test";

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

type FakeClient = {
  POST: Mock;
  GET: Mock;
  PUT: Mock;
  PATCH: Mock;
  DELETE: Mock;
  OPTIONS: Mock;
  HEAD: Mock;
  TRACE: Mock;
};

function fakeClient(): FakeClient {
  const ok: Mock = vi.fn().mockResolvedValue({
    data: {},
    error: undefined,
    response: new Response("{}"),
  });
  return {
    POST: ok,
    GET: vi.fn(),
    PUT: vi.fn(),
    PATCH: vi.fn(),
    DELETE: vi.fn(),
    OPTIONS: vi.fn(),
    HEAD: vi.fn(),
    TRACE: vi.fn(),
  };
}

interface FakeStores {
  detail: {
    loadDetail: Mock;
    refreshDetailOnly: Mock;
  };
  pulls: {
    loadPulls: Mock;
  };
}

function fakeStores(): FakeStores {
  return {
    detail: {
      loadDetail: vi.fn().mockResolvedValue(undefined),
      refreshDetailOnly: vi.fn().mockResolvedValue(undefined),
    },
    pulls: {
      loadPulls: vi.fn().mockResolvedValue(undefined),
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
  client?: FakeClient;
  stores?: FakeStores;
  setMergeModalOpen?: (open: boolean) => void;
  onAfterOpenMerge?: () => void;
  onCompleted?: () => void;
  onError?: (msg: string) => void;
  approveCommentBody?: string;
  platformHost?: string;
  expectedHeadSha?: string;
  onHeadConflict?: (reason: "stale_state" | "head_unknown") => void;
}

function buildInput(opts: BuildOpts = {}): PRDetailActionInput {
  const client = (opts.client ?? fakeClient()) as unknown as PRDetailActionInput["client"];
  const stores = opts.stores ?? fakeStores();
  return {
    pr: {
      State: opts.state ?? "open",
      IsDraft: opts.isDraft ?? false,
      MergeableState: opts.mergeableState ?? "clean",
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
    stores: stores as unknown as PRDetailActionInput["stores"],
    client,
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
  return {
    type: "about:blank",
    title: "Conflict",
    status: 409,
    detail: "target changed since it was reviewed; refresh and retry",
    code: "conflict",
    details: { reason, ...(context !== undefined && { context }) },
  };
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
});

// runApprovePR --------------------------------------------------------

describe("runApprovePR", () => {
  it("POSTs to /approve and refreshes detail+pulls on success", async () => {
    const client = fakeClient();
    const stores = fakeStores();
    await runApprovePR(
      buildInput({
        client,
        stores,
        approveCommentBody: " hello ",
      }),
    );

    expect(client.POST).toHaveBeenCalledTimes(1);
    const [path, init] = client.POST.mock.calls[0];
    expect(path).toBe("/pulls/{provider}/{owner}/{name}/{number}/approve");
    expect(init).toEqual({
      params: {
        path: {
          provider: "github",
          owner: "octo",
          name: "repo",
          number: 42,
        },
      },
      body: { body: "hello" },
    });
    expect(stores.detail.loadDetail).toHaveBeenCalledTimes(1);
    expect(stores.detail.loadDetail.mock.calls[0]).toEqual([
      "octo",
      "repo",
      42,
      {
        provider: "github",
        platformHost: "github.com",
        repoPath: "octo/repo",
      },
    ]);
    expect(stores.pulls.loadPulls).toHaveBeenCalledTimes(1);
  });

  it("uses host route when platformHost differs from default", async () => {
    const client = fakeClient();
    await runApprovePR(
      buildInput({
        client,
        platformHost: "ghe.example.com",
      }),
    );
    const [path, init] = client.POST.mock.calls[0];
    expect(path).toBe("/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/approve");
    expect(init.params.path.platform_host).toBe("ghe.example.com");
  });

  it("calls onError and throws on API error", async () => {
    const client = fakeClient();
    client.POST.mockResolvedValueOnce({
      data: undefined,
      error: { detail: "boom" },
      response: new Response("{}"),
    });
    const onError = vi.fn();
    await expect(runApprovePR(buildInput({ client, onError }))).rejects.toThrow("boom");
    expect(onError).toHaveBeenCalledWith("boom");
  });

  it("does nothing when canApprovePR is false", async () => {
    const client = fakeClient();
    await runApprovePR(buildInput({ client, state: "closed" }));
    expect(client.POST).not.toHaveBeenCalled();
  });

  it("echoes the reviewed head as expected_head_sha when provided", async () => {
    const client = fakeClient();
    await runApprovePR(
      buildInput({
        client,
        expectedHeadSha: " abc123 ",
      }),
    );
    const [, init] = client.POST.mock.calls[0];
    expect(init.body).toEqual({ body: "", expected_head_sha: "abc123" });
  });

  it("omits expected_head_sha when the rendered head is unknown", async () => {
    const client = fakeClient();
    await runApprovePR(buildInput({ client, expectedHeadSha: "" }));
    const [, init] = client.POST.mock.calls[0];
    expect(init.body).toEqual({ body: "" });
  });

  it("reports stale_state and head_unknown conflicts via onHeadConflict", async () => {
    for (const reason of ["stale_state", "head_unknown"] as const) {
      const client = fakeClient();
      client.POST.mockResolvedValueOnce({
        data: undefined,
        error: conflictProblem(reason),
        response: new Response("{}", { status: 409 }),
      });
      const stores = fakeStores();
      const onHeadConflict = vi.fn();
      await expect(
        runApprovePR(buildInput({ client, stores, expectedHeadSha: "abc123", onHeadConflict })),
      ).rejects.toThrow("target changed since it was reviewed; refresh and retry");
      expect(onHeadConflict).toHaveBeenCalledWith(reason, undefined);
      // The conflict owner refreshes; the closure must not double-load.
      expect(stores.detail.loadDetail).not.toHaveBeenCalled();
      expect(stores.pulls.loadPulls).not.toHaveBeenCalled();
    }
  });

  it("forwards provider side-effect context to onHeadConflict", async () => {
    const sideEffect = "approval 31 may stand on a moved head: dismissal failed";
    const client = fakeClient();
    client.POST.mockResolvedValueOnce({
      data: undefined,
      error: conflictProblem("stale_state", sideEffect),
      response: new Response("{}", { status: 409 }),
    });
    const onHeadConflict = vi.fn();
    await expect(runApprovePR(buildInput({ client, expectedHeadSha: "abc123", onHeadConflict }))).rejects.toThrow();
    expect(onHeadConflict).toHaveBeenCalledWith("stale_state", sideEffect);
  });

  it("does not report generic conflicts via onHeadConflict", async () => {
    const client = fakeClient();
    client.POST.mockResolvedValueOnce({
      data: undefined,
      error: conflictProblem("merge_conflict_or_unknown"),
      response: new Response("{}", { status: 409 }),
    });
    const onHeadConflict = vi.fn();
    await expect(runApprovePR(buildInput({ client, expectedHeadSha: "abc123", onHeadConflict }))).rejects.toThrow();
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
  it("POSTs to /ready-for-review and refreshes on success", async () => {
    const client = fakeClient();
    const stores = fakeStores();
    const onCompleted = vi.fn();
    await runMarkReady(
      buildInput({
        client,
        stores,
        isDraft: true,
        onCompleted,
      }),
    );
    expect(client.POST).toHaveBeenCalledTimes(1);
    const [path] = client.POST.mock.calls[0];
    expect(path).toBe("/pulls/{provider}/{owner}/{name}/{number}/ready-for-review");
    expect(stores.detail.loadDetail).toHaveBeenCalledTimes(1);
    expect(stores.pulls.loadPulls).toHaveBeenCalledTimes(1);
    expect(onCompleted).toHaveBeenCalledTimes(1);
  });

  it("does nothing when not a draft", async () => {
    const client = fakeClient();
    await runMarkReady(buildInput({ client, isDraft: false }));
    expect(client.POST).not.toHaveBeenCalled();
  });

  it("refreshes state and reports error on stale-draft 404", async () => {
    const client = fakeClient();
    client.POST.mockResolvedValueOnce({
      data: undefined,
      error: {
        title: "failed to mark pull request ready for review: 404 Not Found",
      },
      response: new Response("{}"),
    });
    const stores = fakeStores();
    const onError = vi.fn();
    await expect(
      runMarkReady(
        buildInput({
          client,
          stores,
          isDraft: true,
          onError,
        }),
      ),
    ).rejects.toThrow(/ready for review.*404/);
    expect(stores.detail.loadDetail).toHaveBeenCalledTimes(1);
    expect(stores.pulls.loadPulls).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalled();
  });

  it("does not refresh on a generic mutation error", async () => {
    const client = fakeClient();
    client.POST.mockResolvedValueOnce({
      data: undefined,
      error: { detail: "permission denied" },
      response: new Response("{}"),
    });
    const stores = fakeStores();
    const onError = vi.fn();
    await expect(
      runMarkReady(
        buildInput({
          client,
          stores,
          isDraft: true,
          onError,
        }),
      ),
    ).rejects.toThrow("permission denied");
    expect(stores.detail.loadDetail).not.toHaveBeenCalled();
    expect(stores.pulls.loadPulls).not.toHaveBeenCalled();
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
  it("POSTs to /approve-workflows and refreshes via refreshDetailOnly", async () => {
    const client = fakeClient();
    const stores = fakeStores();
    const onCompleted = vi.fn();
    await runApproveWorkflows(
      buildInput({
        client,
        stores,
        onCompleted,
      }),
    );
    expect(client.POST).toHaveBeenCalledTimes(1);
    const [path, init] = client.POST.mock.calls[0];
    expect(path).toBe("/pulls/{provider}/{owner}/{name}/{number}/approve-workflows");
    expect(init).toEqual({
      params: {
        path: {
          provider: "github",
          owner: "octo",
          name: "repo",
          number: 42,
        },
      },
    });
    expect(stores.detail.refreshDetailOnly).toHaveBeenCalledTimes(1);
    expect(stores.detail.loadDetail).not.toHaveBeenCalled();
    expect(stores.pulls.loadPulls).toHaveBeenCalledTimes(1);
    expect(onCompleted).toHaveBeenCalledTimes(1);
  });

  it("calls onError and throws on API error", async () => {
    const client = fakeClient();
    client.POST.mockResolvedValueOnce({
      data: undefined,
      error: { title: "no pending workflows" },
      response: new Response("{}"),
    });
    const onError = vi.fn();
    await expect(runApproveWorkflows(buildInput({ client, onError }))).rejects.toThrow("no pending workflows");
    expect(onError).toHaveBeenCalledWith("no pending workflows");
  });

  it("does nothing when canApproveWorkflows is false", async () => {
    const client = fakeClient();
    await runApproveWorkflows(
      buildInput({
        client,
        approveWorkflows: false,
      }),
    );
    expect(client.POST).not.toHaveBeenCalled();
  });
});
