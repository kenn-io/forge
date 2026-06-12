import { describe, expect, it, vi } from "vite-plus/test";

import type { MiddlemanClient } from "../types.js";
import type { ProviderRouteRef } from "../api/provider-routes.js";
import { createDiffReviewDraftStore } from "./diff-review-draft.svelte.js";

interface MockDraftLoad {
  data: {
    comments: Array<{ id: string; body: string }>;
    supported_actions: string[];
    native_multiline_ranges: boolean;
  };
  response: { status: number; ok: boolean };
}

interface MockMutation {
  data?: { status?: string };
  error?: { code?: string; detail?: string; title?: string; details?: Record<string, unknown> };
  response: { status: number; ok: boolean };
}

function providerRef(overrides: Partial<ProviderRouteRef> = {}): ProviderRouteRef {
  return {
    provider: "forgejo",
    platformHost: "codeberg.org",
    owner: "acme",
    name: "widgets",
    repoPath: "acme/widgets",
    ...overrides,
  };
}

function draftLoad({
  comments = [],
  supportedActions = ["comment"],
  nativeMultilineRanges = true,
  status = 200,
  ok = true,
}: {
  comments?: MockDraftLoad["data"]["comments"];
  supportedActions?: string[];
  nativeMultilineRanges?: boolean;
  status?: number;
  ok?: boolean;
} = {}): MockDraftLoad {
  return {
    data: {
      comments,
      supported_actions: supportedActions,
      native_multiline_ranges: nativeMultilineRanges,
    },
    response: { status, ok },
  };
}

function mutation(overrides: Partial<MockMutation> = {}): MockMutation {
  return {
    response: { status: 200, ok: true },
    ...overrides,
  };
}

function failedMutation(): MockMutation {
  return mutation({
    error: { title: "failed" },
    response: { status: 502, ok: false },
  });
}

function mockGet(result: MockDraftLoad | Promise<MockDraftLoad> = draftLoad()) {
  return vi.fn(() => Promise.resolve(result));
}

function mockPost(result: MockMutation | Promise<MockMutation> = mutation()) {
  return vi.fn(() => Promise.resolve(result));
}

function mockDelete(result: MockMutation | Promise<MockMutation> = mutation()) {
  return vi.fn(() => Promise.resolve(result));
}

function mockClient({
  GET = mockGet(),
  POST = mockPost(),
  DELETE = mockDelete(),
}: {
  GET?: ReturnType<typeof vi.fn>;
  POST?: ReturnType<typeof vi.fn>;
  DELETE?: ReturnType<typeof vi.fn>;
} = {}): MiddlemanClient {
  return { GET, POST, DELETE } as unknown as MiddlemanClient;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

describe("createDiffReviewDraftStore", () => {
  it("refreshes PR detail after a successful publish", async () => {
    const client = mockClient();
    const onPublished = vi.fn();
    const store = createDiffReviewDraftStore({ client, onPublished });
    const ref = providerRef();

    store.setContext(ref, 42, true);
    await Promise.resolve();
    const ok = await store.publish("comment", "summary");

    expect(ok).toBe(true);
    expect(onPublished).toHaveBeenCalledWith(ref, 42);
  });

  it("keeps publish successful when detail refresh fails", async () => {
    const client = mockClient();
    const store = createDiffReviewDraftStore({
      client,
      onPublished: () => Promise.reject(new Error("refresh failed")),
    });

    store.setContext(providerRef(), 42, true);
    await Promise.resolve();

    await expect(store.publish("comment", "summary")).resolves.toBe(true);
    expect(store.getError()).toBeNull();
  });

  it("does not refresh PR detail when publish fails", async () => {
    const client = mockClient({ POST: mockPost(failedMutation()) });
    const onPublished = vi.fn();
    const store = createDiffReviewDraftStore({ client, onPublished });

    store.setContext(providerRef(), 42, true);
    await Promise.resolve();
    await store.publish("comment", "summary");

    expect(onPublished).not.toHaveBeenCalled();
  });

  it("reloads detail and draft after a stale publish conflict", async () => {
    const client = mockClient({
      GET: vi
        .fn()
        .mockResolvedValueOnce(
          draftLoad({
            comments: [{ id: "stale", body: "stale draft" }],
          }),
        )
        .mockResolvedValueOnce(
          draftLoad({
            comments: [{ id: "fresh", body: "fresh draft" }],
          }),
        ),
      POST: mockPost(
        mutation({
          error: {
            code: "conflict",
            detail: "review draft head is stale",
            details: { reason: "stale_state" },
          },
          response: { status: 409, ok: false },
        }),
      ),
    });
    const onStalePublish = vi.fn();
    const store = createDiffReviewDraftStore({ client, onStalePublish });
    const ref = providerRef();

    store.setContext(ref, 42, true);
    await Promise.resolve();
    const ok = await store.publish("approve", "summary");

    expect(ok).toBe(false);
    expect(onStalePublish).toHaveBeenCalledWith(ref, 42);
    expect(client.GET).toHaveBeenCalledTimes(2);
    expect(store.getComments()).toEqual([{ id: "fresh", body: "fresh draft" }]);
    expect(store.getError()).toBe("review draft head is stale");
  });

  it("ignores draft loads from an older diff head", async () => {
    const oldLoad = deferred<MockDraftLoad>();
    const newLoad = deferred<MockDraftLoad>();
    const client = mockClient({
      GET: vi.fn().mockReturnValueOnce(oldLoad.promise).mockReturnValueOnce(newLoad.promise),
    });
    const store = createDiffReviewDraftStore({ client });
    const ref = providerRef({
      provider: "github",
      platformHost: "github.com",
    });

    store.setContext(ref, 42, true, "old-head");
    await Promise.resolve();
    store.setContext(ref, 42, true, "new-head");
    await Promise.resolve();

    newLoad.resolve(
      draftLoad({
        comments: [{ id: "new", body: "new draft" }],
      }),
    );
    await Promise.resolve();
    oldLoad.resolve(
      draftLoad({
        comments: [{ id: "old", body: "old draft" }],
      }),
    );
    await Promise.resolve();

    expect(store.getComments()).toEqual([{ id: "new", body: "new draft" }]);
    expect(store.isLoading()).toBe(false);
  });

  it("surfaces partial publish status while clearing the draft", async () => {
    const client = mockClient({
      GET: mockGet(draftLoad({ nativeMultilineRanges: false })),
      POST: mockPost(
        mutation({
          data: { status: "partially_published" },
        }),
      ),
    });
    const onPublished = vi.fn();
    const store = createDiffReviewDraftStore({ client, onPublished });
    const ref = providerRef({
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group",
      name: "project",
      repoPath: "group/project",
    });

    store.setContext(ref, 7, true);
    await Promise.resolve();
    const ok = await store.publish("approve", "summary");

    expect(ok).toBe(true);
    expect(store.getDraft()?.comments).toEqual([]);
    expect(store.getWarning()).toBe(
      "Review was partially published. Some inline comments or the selected review action may not have been submitted.",
    );
    expect(onPublished).toHaveBeenCalledWith(ref, 7);
  });

  it("ignores an older same-PR load after publish refreshes the draft", async () => {
    const staleLoad = deferred<MockDraftLoad>();
    const client = mockClient({
      GET: vi.fn().mockReturnValueOnce(staleLoad.promise).mockResolvedValueOnce(draftLoad()),
    });
    const store = createDiffReviewDraftStore({ client });

    store.setContext(providerRef(), 42, true);
    await Promise.resolve();

    await expect(store.publish("comment", "summary")).resolves.toBe(true);
    expect(store.getComments()).toEqual([]);

    staleLoad.resolve(
      draftLoad({
        comments: [{ id: "stale", body: "old draft" }],
      }),
    );
    await staleLoad.promise;
    await Promise.resolve();

    expect(store.getComments()).toEqual([]);
  });

  it("does not stay loading when a mutation fails during an in-flight load", async () => {
    const staleLoad = deferred<MockDraftLoad>();
    const client = mockClient({
      GET: vi.fn().mockReturnValueOnce(staleLoad.promise),
      POST: mockPost(failedMutation()),
    });
    const store = createDiffReviewDraftStore({ client });

    store.setContext(providerRef(), 42, true);
    await Promise.resolve();
    expect(store.isLoading()).toBe(true);

    await expect(store.publish("comment", "summary")).resolves.toBe(false);
    expect(store.isLoading()).toBe(false);

    staleLoad.resolve(
      draftLoad({
        comments: [{ id: "stale", body: "old draft" }],
      }),
    );
    await staleLoad.promise;
    await Promise.resolve();

    expect(store.isLoading()).toBe(false);
    expect(store.getComments()).toEqual([]);
  });

  it("does not stay loading when discard succeeds during an in-flight load", async () => {
    const staleLoad = deferred<MockDraftLoad>();
    const client = mockClient({
      GET: vi.fn().mockReturnValueOnce(staleLoad.promise),
    });
    const store = createDiffReviewDraftStore({ client });

    store.setContext(providerRef(), 42, true);
    await Promise.resolve();
    expect(store.isLoading()).toBe(true);

    await expect(store.discard()).resolves.toBe(true);
    expect(store.isLoading()).toBe(false);

    staleLoad.resolve(
      draftLoad({
        comments: [{ id: "stale", body: "old draft" }],
      }),
    );
    await staleLoad.promise;
    await Promise.resolve();

    expect(store.isLoading()).toBe(false);
    expect(store.getComments()).toEqual([]);
  });
});
