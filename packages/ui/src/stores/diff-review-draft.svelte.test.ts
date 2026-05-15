import { describe, expect, it, vi } from "vitest";

import type { MiddlemanClient } from "../types.js";
import { createDiffReviewDraftStore } from "./diff-review-draft.svelte.js";

interface MockDraftLoad {
  data: {
    comments: Array<{ id: string; body: string }>;
    supported_actions: string[];
    native_multiline_ranges: boolean;
  };
  response: { status: number; ok: boolean };
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
    const client = {
      GET: vi.fn(() => Promise.resolve({
        data: {
          comments: [],
          supported_actions: ["comment"],
          native_multiline_ranges: true,
        },
        response: { status: 200, ok: true },
      })),
      POST: vi.fn(() => Promise.resolve({
        response: { status: 200, ok: true },
      })),
    } as unknown as MiddlemanClient;
    const onPublished = vi.fn();
    const store = createDiffReviewDraftStore({ client, onPublished });
    const ref = {
      provider: "forgejo",
      platformHost: "codeberg.org",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    };

    store.setContext(ref, 42, true);
    await Promise.resolve();
    const ok = await store.publish("comment", "summary");

    expect(ok).toBe(true);
    expect(onPublished).toHaveBeenCalledWith(ref, 42);
  });

  it("keeps publish successful when detail refresh fails", async () => {
    const client = {
      GET: vi.fn(() => Promise.resolve({
        data: {
          comments: [],
          supported_actions: ["comment"],
          native_multiline_ranges: true,
        },
        response: { status: 200, ok: true },
      })),
      POST: vi.fn(() => Promise.resolve({
        response: { status: 200, ok: true },
      })),
    } as unknown as MiddlemanClient;
    const store = createDiffReviewDraftStore({
      client,
      onPublished: () => Promise.reject(new Error("refresh failed")),
    });

    store.setContext({
      provider: "forgejo",
      platformHost: "codeberg.org",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    }, 42, true);
    await Promise.resolve();

    await expect(store.publish("comment", "summary")).resolves.toBe(true);
    expect(store.getError()).toBeNull();
  });

  it("does not refresh PR detail when publish fails", async () => {
    const client = {
      GET: vi.fn(() => Promise.resolve({
        data: {
          comments: [],
          supported_actions: ["comment"],
          native_multiline_ranges: true,
        },
        response: { status: 200, ok: true },
      })),
      POST: vi.fn(() => Promise.resolve({
        error: { title: "failed" },
        response: { status: 502, ok: false },
      })),
    } as unknown as MiddlemanClient;
    const onPublished = vi.fn();
    const store = createDiffReviewDraftStore({ client, onPublished });

    store.setContext({
      provider: "forgejo",
      platformHost: "codeberg.org",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    }, 42, true);
    await Promise.resolve();
    await store.publish("comment", "summary");

    expect(onPublished).not.toHaveBeenCalled();
  });

  it("ignores draft loads from an older diff head", async () => {
    const oldLoad = deferred<MockDraftLoad>();
    const newLoad = deferred<MockDraftLoad>();
    const client = {
      GET: vi.fn()
        .mockReturnValueOnce(oldLoad.promise)
        .mockReturnValueOnce(newLoad.promise),
    } as unknown as MiddlemanClient;
    const store = createDiffReviewDraftStore({ client });
    const ref = {
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    };

    store.setContext(ref, 42, true, "old-head");
    await Promise.resolve();
    store.setContext(ref, 42, true, "new-head");
    await Promise.resolve();

    newLoad.resolve({
      data: {
        comments: [{ id: "new", body: "new draft" }],
        supported_actions: ["comment"],
        native_multiline_ranges: true,
      },
      response: { status: 200, ok: true },
    });
    await Promise.resolve();
    oldLoad.resolve({
      data: {
        comments: [{ id: "old", body: "old draft" }],
        supported_actions: ["comment"],
        native_multiline_ranges: true,
      },
      response: { status: 200, ok: true },
    });
    await Promise.resolve();

    expect(client.GET).toHaveBeenCalledTimes(2);
    expect(store.getComments()).toEqual([{ id: "new", body: "new draft" }]);
    expect(store.isLoading()).toBe(false);
  });

  it("surfaces partial publish status while clearing the draft", async () => {
    const client = {
      GET: vi.fn(() => Promise.resolve({
        data: {
          comments: [],
          supported_actions: ["comment"],
          native_multiline_ranges: false,
        },
        response: { status: 200, ok: true },
      })),
      POST: vi.fn(() => Promise.resolve({
        data: { status: "partially_published" },
        response: { status: 200, ok: true },
      })),
    } as unknown as MiddlemanClient;
    const onPublished = vi.fn();
    const store = createDiffReviewDraftStore({ client, onPublished });
    const ref = {
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group",
      name: "project",
      repoPath: "group/project",
    };

    store.setContext(ref, 7, true);
    await Promise.resolve();
    const ok = await store.publish("approve", "summary");

    expect(ok).toBe(true);
    expect(store.getDraft()?.comments).toEqual([]);
    expect(store.getWarning()).toContain("partially published");
    expect(onPublished).toHaveBeenCalledWith(ref, 7);
  });

  it("ignores an older same-PR load after publish refreshes the draft", async () => {
    const staleLoad = deferred<MockDraftLoad>();
    const client = {
      GET: vi
        .fn()
        .mockReturnValueOnce(staleLoad.promise)
        .mockResolvedValueOnce({
          data: {
            comments: [],
            supported_actions: ["comment"],
            native_multiline_ranges: true,
          },
          response: { status: 200, ok: true },
        }),
      POST: vi.fn(() => Promise.resolve({
        response: { status: 200, ok: true },
      })),
    } as unknown as MiddlemanClient;
    const store = createDiffReviewDraftStore({ client });

    store.setContext({
      provider: "forgejo",
      platformHost: "codeberg.org",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    }, 42, true);
    await Promise.resolve();

    await expect(store.publish("comment", "summary")).resolves.toBe(true);
    expect(store.getComments()).toEqual([]);

    staleLoad.resolve({
      data: {
        comments: [{ id: "stale", body: "old draft" }],
        supported_actions: ["comment"],
        native_multiline_ranges: true,
      },
      response: { status: 200, ok: true },
    });
    await staleLoad.promise;
    await Promise.resolve();

    expect(store.getComments()).toEqual([]);
  });

  it("does not stay loading when a mutation fails during an in-flight load", async () => {
    const staleLoad = deferred<MockDraftLoad>();
    const client = {
      GET: vi.fn().mockReturnValueOnce(staleLoad.promise),
      POST: vi.fn(() => Promise.resolve({
        error: { title: "failed" },
        response: { status: 502, ok: false },
      })),
    } as unknown as MiddlemanClient;
    const store = createDiffReviewDraftStore({ client });

    store.setContext({
      provider: "forgejo",
      platformHost: "codeberg.org",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    }, 42, true);
    await Promise.resolve();
    expect(store.isLoading()).toBe(true);

    await expect(store.publish("comment", "summary")).resolves.toBe(false);
    expect(store.isLoading()).toBe(false);

    staleLoad.resolve({
      data: {
        comments: [{ id: "stale", body: "old draft" }],
        supported_actions: ["comment"],
        native_multiline_ranges: true,
      },
      response: { status: 200, ok: true },
    });
    await staleLoad.promise;
    await Promise.resolve();

    expect(store.isLoading()).toBe(false);
    expect(store.getComments()).toEqual([]);
  });

  it("does not stay loading when discard succeeds during an in-flight load", async () => {
    const staleLoad = deferred<MockDraftLoad>();
    const client = {
      GET: vi.fn().mockReturnValueOnce(staleLoad.promise),
      DELETE: vi.fn(() => Promise.resolve({
        response: { status: 200, ok: true },
      })),
    } as unknown as MiddlemanClient;
    const store = createDiffReviewDraftStore({ client });

    store.setContext({
      provider: "forgejo",
      platformHost: "codeberg.org",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
    }, 42, true);
    await Promise.resolve();
    expect(store.isLoading()).toBe(true);

    await expect(store.discard()).resolves.toBe(true);
    expect(store.isLoading()).toBe(false);

    staleLoad.resolve({
      data: {
        comments: [{ id: "stale", body: "old draft" }],
        supported_actions: ["comment"],
        native_multiline_ranges: true,
      },
      response: { status: 200, ok: true },
    });
    await staleLoad.promise;
    await Promise.resolve();

    expect(store.isLoading()).toBe(false);
    expect(store.getComments()).toEqual([]);
  });
});
