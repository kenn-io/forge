import { describe, expect, it, vi } from "vite-plus/test";

import { createDetailStore } from "@middleman/ui/stores/detail";
import type { MiddlemanClient } from "@middleman/ui";

const pullRef = {
  provider: "github",
  platformHost: "github.com",
  repoPath: "octo/repo",
};

interface MockDetail {
  repo_owner: string;
  repo_name: string;
  repo: {
    provider: string;
    platform_host: string;
    owner: string;
    name: string;
    repo_path: string;
  };
  merge_request: { Number: number };
  events: unknown[];
}

function makeDetail(events: unknown[] = [], number = 1): MockDetail {
  return {
    repo_owner: "octo",
    repo_name: "repo",
    repo: {
      provider: pullRef.provider,
      platform_host: pullRef.platformHost,
      owner: "octo",
      name: "repo",
      repo_path: pullRef.repoPath,
    },
    merge_request: { Number: number },
    events,
  };
}

describe("createDetailStore submitComment", () => {
  it("never flips loading flag while refreshing after a comment", async () => {
    const detailData = makeDetail();
    const loadingDuringRefresh: boolean[] = [];
    let getCallCount = 0;
    const holder: {
      store: ReturnType<typeof createDetailStore> | null;
    } = { store: null };

    const client = {
      GET: vi.fn(async () => {
        getCallCount++;
        if (getCallCount > 1 && holder.store) {
          loadingDuringRefresh.push(holder.store.isDetailLoading());
        }
        return { data: detailData };
      }),
      POST: vi.fn(async (path: string) => {
        if (path.includes("/sync")) {
          return { data: detailData };
        }
        if (path.includes("/comments")) {
          return { data: { ID: 42 } };
        }
        return { data: undefined };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;

    holder.store = createDetailStore({ client });

    await holder.store.loadDetail("octo", "repo", 1, pullRef);
    // Allow background syncDetail microtasks to settle.
    await Promise.resolve();
    await Promise.resolve();

    await holder.store.submitComment("octo", "repo", 1, "hello");

    expect(getCallCount).toBeGreaterThan(1);
    expect(loadingDuringRefresh.length).toBeGreaterThan(0);
    expect(loadingDuringRefresh.every((v) => v === false)).toBe(true);
    expect(holder.store.isDetailLoading()).toBe(false);
  });

  it("does not overwrite a newly-loaded PR if the comment refresh resolves later", async () => {
    const detailA = makeDetail([], 1);
    const detailB = makeDetail([], 2);

    let refreshResolve: (value: unknown) => void = () => {};
    const refreshPromise = new Promise((resolve) => {
      refreshResolve = resolve;
    });

    let getCallCount = 0;
    const client = {
      GET: vi.fn(async () => {
        getCallCount++;
        if (getCallCount === 1) return { data: detailA }; // initial loadDetail PR 1
        if (getCallCount === 2) return await refreshPromise; // refreshDetail in submitComment (deferred)
        return { data: detailB }; // loadDetail PR 2
      }),
      POST: vi.fn(async (path: string) => {
        if (path.includes("/sync")) return { data: undefined };
        if (path.includes("/comments")) return { data: { ID: 42 } };
        return { data: undefined };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;

    const store = createDetailStore({ client });

    await store.loadDetail("octo", "repo", 1, pullRef);

    // Fire submitComment without awaiting; refresh GET will block on refreshPromise.
    const submitPromise = store.submitComment("octo", "repo", 1, "hi");
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    // User navigates to a different PR before the refresh resolves.
    await store.loadDetail("octo", "repo", 2, pullRef);
    expect(store.getDetail()?.merge_request.Number).toBe(2);

    // Now release the in-flight refresh — it must be discarded.
    refreshResolve({ data: detailA });
    await submitPromise;
    await Promise.resolve();
    await Promise.resolve();

    expect(store.getDetail()?.merge_request.Number).toBe(2);
  });

  it("triggers post-comment sync and pulls list refresh", async () => {
    const detailData = makeDetail([{ ID: 42, Kind: "comment" }]);
    const loadPulls = vi.fn(async () => {});
    const postCalls: string[] = [];

    const client = {
      GET: vi.fn(async () => ({ data: detailData })),
      POST: vi.fn(async (path: string) => {
        postCalls.push(path);
        if (path.includes("/sync")) return { data: detailData };
        if (path.includes("/comments")) return { data: { ID: 42 } };
        return { data: undefined };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;

    const store = createDetailStore({
      client,
      getPage: () => "pulls",
      pulls: { loadPulls },
    });

    await store.loadDetail("octo", "repo", 1, pullRef);
    // Drain the background syncDetail from the initial load.
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    loadPulls.mockClear();
    postCalls.length = 0;

    await store.submitComment("octo", "repo", 1, "hi");
    // Drain the background syncDetail fired by submitComment.
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(postCalls.some((p) => p.includes("/sync"))).toBe(true);
    expect(loadPulls).toHaveBeenCalled();
  });

  it("discards stale syncDetail responses after posting a comment", async () => {
    const staleDetail = makeDetail([]);
    const freshDetail = makeDetail([{ ID: 42, Kind: "comment" }]);

    let syncResolve: (value: unknown) => void = () => {};
    const syncPromise = new Promise((resolve) => {
      syncResolve = resolve;
    });

    let getCallCount = 0;
    let syncCallCount = 0;
    const client = {
      GET: vi.fn(async () => {
        getCallCount++;
        // First call: initial loadDetail — still no comment.
        // Second call: refreshDetail inside submitComment — comment present.
        if (getCallCount === 1) return { data: staleDetail };
        return { data: freshDetail };
      }),
      POST: vi.fn(async (path: string) => {
        if (path.includes("/sync")) {
          syncCallCount++;
          // First sync: background sync from initial loadDetail, blocked
          // on deferred promise and resolves with stale data later.
          // Second sync: post-comment sync from submitComment, returns
          // fresh data immediately.
          if (syncCallCount === 1) return await syncPromise;
          return { data: freshDetail };
        }
        if (path.includes("/comments")) return { data: { ID: 42 } };
        return { data: undefined };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as MiddlemanClient;

    const store = createDetailStore({ client });

    // loadDetail resolves after the initial GET, but fires a background
    // syncDetail that is still blocked on syncPromise.
    await store.loadDetail("octo", "repo", 1, pullRef);

    // submitComment refreshes silently and should pick up the new event.
    await store.submitComment("octo", "repo", 1, "hello");
    expect(store.getDetail()?.events).toHaveLength(1);

    // The background sync now returns stale data (no comment).
    // It must be discarded rather than overwrite the fresh detail.
    syncResolve({ data: staleDetail, error: undefined });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(store.getDetail()?.events).toHaveLength(1);
  });
});
