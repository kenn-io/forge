import { Cause, Effect, Exit, Option } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { OwnedAppRuntime } from "../app/runtime.js";
import type { ProblemBody } from "../api/problems.js";
import {
  createDetailStore as createRuntimeDetailStore,
  type DetailStore,
  type DetailStoreOptions,
} from "./detail.svelte.js";
import type { GeneratedClient } from "../api/generated-api.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import { dismissFlash, getFlash, getFlashes } from "./flash.svelte.js";

let runtime: OwnedAppRuntime | undefined;

type TestDetailStoreOptions = Omit<DetailStoreOptions, "runtime"> & { readonly client: GeneratedClient };

function createDetailStore(options: TestDetailStoreOptions) {
  const { client, ...storeOptions } = options;
  runtime = makeTestAppRuntime(client);
  return createRuntimeDetailStore({ ...storeOptions, runtime });
}

async function loadDetail(store: DetailStore, ...args: Parameters<DetailStore["loadDetail"]>): Promise<void> {
  store.loadDetail(...args);
  await vi.waitFor(() => expect(store.isDetailLoading()).toBe(false));
}

function submitComment(
  store: DetailStore,
  owner: string,
  name: string,
  number: number,
  body: string,
): Promise<boolean> {
  const completion = Promise.withResolvers<boolean>();
  store.submitComment(owner, name, number, body, {
    onSuccess: () => completion.resolve(true),
    onFailure: () => completion.resolve(false),
  });
  return completion.promise;
}

function deleteComment(
  store: DetailStore,
  owner: string,
  name: string,
  number: number,
  commentID: number,
): Promise<boolean> {
  const completion = Promise.withResolvers<boolean>();
  store.deleteComment(owner, name, number, commentID, {
    onSuccess: () => completion.resolve(true),
    onFailure: () => completion.resolve(false),
  });
  return completion.promise;
}

function deletionSucceeded() {
  return { data: undefined, response: new Response(null, { status: 204 }) };
}

function deletionFailed(detail: string) {
  const error: ProblemBody = {
    code: "validationError",
    detail,
    title: "Invalid request",
    type: "about:blank",
  };
  return { error, response: new Response(null, { status: 400 }) };
}

beforeEach(() => {
  runtime = undefined;
  for (const flash of getFlashes()) dismissFlash(flash.id);
});

afterEach(async () => {
  if (runtime !== undefined) await Effect.runPromise(runtime.disposeEffect);
});

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
  it("preserves permanent API failures when reconciling provider events", async () => {
    let getCalls = 0;
    const store = createDetailStore({
      client: {
        GET: vi.fn(async () => {
          getCalls++;
          return getCalls === 1
            ? { data: makeDetail() }
            : {
                error: {
                  code: "pullNotFound",
                  detail: "pull request not found",
                  title: "Not found",
                  type: "about:blank",
                },
                response: new Response(null, { status: 404 }),
              };
        }),
        POST: vi.fn(),
        PUT: vi.fn(),
        DELETE: vi.fn(),
      } as unknown as GeneratedClient,
    });
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });
    const execution = runtime.runCommand(store.refreshDetailOnlyEffect("octo", "repo", 1, pullRef), {
      operation: "reconcile pull request after provider event",
      safeContext: {},
      onFailure: () => {},
    });
    const exit = await Effect.runPromise(execution.await);

    expect(Exit.isFailure(exit)).toBe(true);
    if (Exit.isFailure(exit)) {
      const failure = Cause.findErrorOption(exit.cause);
      expect(Option.isSome(failure)).toBe(true);
      if (Option.isSome(failure)) expect(failure.value).toMatchObject({ _tag: "ApiProblemError" });
    }
  });

  it("retries an event detail refresh superseded by a same-selection foreground load", async () => {
    const eventRead = Promise.withResolvers<{ data: MockDetail }>();
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: makeDetail([], 1) })
      .mockImplementationOnce(() => eventRead.promise)
      .mockResolvedValueOnce({ data: makeDetail([], 1) });
    const store = createDetailStore({
      client: { GET: get, POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() } as unknown as GeneratedClient,
    });
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });
    const execution = runtime.runCommand(store.refreshDetailOnlyEffect("octo", "repo", 1, pullRef), {
      operation: "reconcile pull request after provider event",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));

    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });
    eventRead.resolve({ data: makeDetail([], 1) });
    const exit = await Effect.runPromise(execution.await);

    expect(Exit.isFailure(exit)).toBe(true);
    if (Exit.isFailure(exit)) {
      const failure = Cause.findErrorOption(exit.cause);
      expect(Option.isSome(failure)).toBe(true);
      if (Option.isSome(failure)) expect(failure.value).toMatchObject({ _tag: "TransientTransportError" });
    }
  });

  it("acknowledges a posted PR comment before reporting reconciliation failure", async () => {
    let getCalls = 0;
    const store = createDetailStore({
      client: {
        GET: vi.fn(async () => {
          getCalls++;
          if (getCalls === 1) return { data: makeDetail() };
          throw new Error("offline");
        }),
        POST: vi.fn(async () => ({ data: { ID: 42 }, response: new Response(null, { status: 201 }) })),
        PUT: vi.fn(),
        DELETE: vi.fn(),
      } as unknown as GeneratedClient,
    });
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });
    const onSuccess = vi.fn();
    const onFailure = vi.fn();
    const settled = Promise.withResolvers<void>();

    store.submitComment("octo", "repo", 1, "hello", {
      onSuccess,
      onFailure,
      onSettled: settled.resolve,
    });
    await settled.promise;

    expect(onSuccess).toHaveBeenCalledOnce();
    expect(onFailure).not.toHaveBeenCalled();
    await vi.waitFor(() =>
      expect(getFlash()).toMatchObject({
        message: "Comment was posted, but the latest discussion could not be refreshed.",
        tone: "danger",
      }),
    );
  });

  it("does not reconcile a posted PR comment after navigation changes the selection", async () => {
    const posted = Promise.withResolvers<void>();
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: makeDetail([], 1) })
      .mockResolvedValueOnce({ data: makeDetail([], 2) });
    const post = vi.fn(async (path: string) => {
      if (path.includes("/comments")) {
        await posted.promise;
        return { data: { ID: 42 }, response: new Response(null, { status: 201 }) };
      }
      return { data: makeDetail([], 1) };
    });
    const store = createDetailStore({
      client: { GET: get, POST: post, PUT: vi.fn(), DELETE: vi.fn() } as unknown as GeneratedClient,
    });
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });
    const settled = Promise.withResolvers<void>();
    store.submitComment("octo", "repo", 1, "hello", { onSettled: settled.resolve });
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    await loadDetail(store, "octo", "repo", 2, { ...pullRef, sync: false });
    posted.resolve();
    await settled.promise;
    await Promise.resolve();

    expect(get).toHaveBeenCalledTimes(2);
    expect(post).toHaveBeenCalledTimes(1);
    expect(store.getDetail()?.merge_request.Number).toBe(2);
  });

  it("optimistically edits a PR comment and rolls it back when acknowledgement fails", async () => {
    const patch = Promise.withResolvers<ReturnType<typeof deletionFailed>>();
    const original = { EventType: "issue_comment", PlatformID: 44, Body: "before" };
    const store = createDetailStore({
      client: {
        GET: vi.fn(async () => ({ data: makeDetail([original]) })),
        POST: vi.fn(),
        PUT: vi.fn(),
        PATCH: vi.fn(() => patch.promise),
        DELETE: vi.fn(),
      } as unknown as GeneratedClient,
    });
    const settled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });

    store.editComment("octo", "repo", 1, 44, "after", { onSettled: settled.resolve });
    await vi.waitFor(() => expect(store.getDetail()?.events[0]?.Body).toBe("after"));
    patch.resolve(deletionFailed("provider denied edit"));
    await settled.promise;

    expect(store.getDetail()?.events[0]?.Body).toBe("before");
  });

  it("settles a failed discussion reply without stopping later comment work", async () => {
    const post = vi
      .fn()
      .mockResolvedValueOnce(deletionFailed("provider denied reply"))
      .mockResolvedValueOnce({ data: { ID: 45 }, response: new Response(null, { status: 201 }) });
    const store = createDetailStore({
      client: {
        GET: vi.fn(async () => ({ data: makeDetail() })),
        POST: post,
        PUT: vi.fn(),
        DELETE: vi.fn(),
      } as unknown as GeneratedClient,
    });
    const firstSettled = Promise.withResolvers<void>();
    const secondSettled = Promise.withResolvers<void>();
    const firstFailure = vi.fn();
    const secondSuccess = vi.fn();
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });

    store.replyToDiscussion("octo", "repo", 1, "thread-1", "first", {
      onFailure: firstFailure,
      onSettled: firstSettled.resolve,
    });
    store.replyToDiscussion("octo", "repo", 1, "thread-1", "second", {
      onSuccess: secondSuccess,
      onSettled: secondSettled.resolve,
    });
    await Promise.all([firstSettled.promise, secondSettled.promise]);

    expect(firstFailure).toHaveBeenCalledOnce();
    expect(secondSuccess).toHaveBeenCalledOnce();
    expect(post).toHaveBeenCalledTimes(2);
  });

  it("restores a rejected deletion accepted behind a discussion reply", async () => {
    const reply = Promise.withResolvers<{ data: { ID: number }; response: Response }>();
    const deletion = Promise.withResolvers<ReturnType<typeof deletionFailed>>();
    const original = { EventType: "issue_comment", PlatformID: 44, Body: "keep me" };
    const store = createDetailStore({
      client: {
        GET: vi.fn(async () => ({ data: makeDetail([original]) })),
        POST: vi.fn(() => reply.promise),
        PUT: vi.fn(),
        DELETE: vi.fn(() => deletion.promise),
      } as unknown as GeneratedClient,
    });
    const replySettled = Promise.withResolvers<void>();
    const deletionSettled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });

    store.replyToDiscussion("octo", "repo", 1, "thread-1", "reply", { onSettled: replySettled.resolve });
    await vi.waitFor(() => expect(store.getDetail()?.events).toEqual([original]));
    store.deleteComment("octo", "repo", 1, 44, { onSettled: deletionSettled.resolve });
    await vi.waitFor(() => expect(store.getDetail()?.events).toEqual([]));

    reply.resolve({ data: { ID: 45 }, response: new Response(null, { status: 201 }) });
    await replySettled.promise;
    deletion.resolve(deletionFailed("provider denied deletion"));
    await deletionSettled.promise;

    expect(store.getDetail()?.events).toEqual([original]);
  });

  it("does not confirm a rejected edit through a successful deletion of another comment", async () => {
    const edit = Promise.withResolvers<ReturnType<typeof deletionFailed>>();
    const deletion = Promise.withResolvers<ReturnType<typeof deletionSucceeded>>();
    const first = { EventType: "issue_comment", PlatformID: 44, Body: "before" };
    const second = { EventType: "issue_comment", PlatformID: 45, Body: "remove me" };
    const store = createDetailStore({
      client: {
        GET: vi.fn(async () => ({ data: makeDetail([first, second]) })),
        POST: vi.fn(),
        PUT: vi.fn(),
        PATCH: vi.fn(() => edit.promise),
        DELETE: vi.fn(() => deletion.promise),
      } as unknown as GeneratedClient,
    });
    const editSettled = Promise.withResolvers<void>();
    const deletionSettled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });

    store.editComment("octo", "repo", 1, 44, "after", { onSettled: editSettled.resolve });
    store.deleteComment("octo", "repo", 1, 45, { onSettled: deletionSettled.resolve });
    await vi.waitFor(() => expect(store.getDetail()?.events).toEqual([{ ...first, Body: "after" }]));

    edit.resolve(deletionFailed("provider denied edit"));
    await editSettled.promise;
    deletion.resolve(deletionSucceeded());
    await deletionSettled.promise;

    expect(store.getDetail()?.events).toEqual([first]);
  });

  it("optimistically hides a deleted PR comment and rolls it back when the command fails", async () => {
    const pendingDelete = Promise.withResolvers<ReturnType<typeof deletionFailed>>();
    const original = { EventType: "issue_comment", PlatformID: 44, Body: "keep me" };
    const store = createDetailStore({
      client: {
        GET: vi.fn(async () => ({ data: makeDetail([original]) })),
        POST: vi.fn(async () => ({ data: undefined })),
        PUT: vi.fn(),
        DELETE: vi.fn(() => pendingDelete.promise),
      } as unknown as GeneratedClient,
    });
    const settled = Promise.withResolvers<void>();
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });

    store.deleteComment("octo", "repo", 1, 44, { onSettled: settled.resolve });
    await vi.waitFor(() => expect(store.getDetail()?.events).toEqual([]));
    pendingDelete.resolve(deletionFailed("provider denied deletion"));
    await settled.promise;

    expect(store.getDetail()?.events).toEqual([original]);
  });

  it("hides a deleted PR comment while ordinary sync converges", async () => {
    const staleDetail = makeDetail([{ EventType: "issue_comment", PlatformID: 44 }]);
    const get = vi.fn(async () => ({ data: staleDetail }));
    const post = vi.fn(async () => ({ data: staleDetail }));
    const del = vi.fn(async () => deletionSucceeded());
    const store = createDetailStore({
      client: {
        GET: get,
        POST: post,
        PUT: vi.fn(),
        DELETE: del,
      } as unknown as GeneratedClient,
    });
    await loadDetail(store, "octo", "repo", 1, pullRef);
    await vi.waitFor(() => expect(post).toHaveBeenCalled());
    await vi.waitFor(() => expect(store.isDetailSyncing()).toBe(false));
    get.mockClear();

    const ok = await deleteComment(store, "octo", "repo", 1, 44);

    expect(ok).toBe(true);
    expect(del).toHaveBeenCalledWith("/pulls/{provider}/{owner}/{name}/{number}/comments/{comment_id}", {
      headers: { "Content-Type": "application/json" },
      params: {
        path: { provider: "github", owner: "octo", name: "repo", number: 1, comment_id: 44 },
      },
      signal: expect.any(AbortSignal),
    });
    expect(store.getDetail()?.events).toEqual([]);
    expect(post).toHaveBeenCalledWith("/pulls/{provider}/{owner}/{name}/{number}/sync", {
      params: { path: { provider: "github", owner: "octo", name: "repo", number: 1 } },
      signal: expect.any(AbortSignal),
    });
    expect(get).not.toHaveBeenCalled();
  });

  it("keeps PR detail unchanged when comment deletion fails", async () => {
    const original = { EventType: "issue_comment", PlatformID: 44, Body: "keep me" };
    const get = vi.fn(async () => ({ data: makeDetail([original]) }));
    const store = createDetailStore({
      client: {
        GET: get,
        POST: vi.fn(async () => ({ data: undefined })),
        PUT: vi.fn(),
        DELETE: vi.fn(async () => deletionFailed("provider denied deletion")),
      } as unknown as GeneratedClient,
    });
    await loadDetail(store, "octo", "repo", 1, pullRef);
    await Promise.resolve();
    get.mockClear();

    const ok = await deleteComment(store, "octo", "repo", 1, 44);

    expect(ok).toBe(false);
    expect(get).not.toHaveBeenCalled();
    expect(store.getDetailError()).toBe("provider denied deletion");
    expect(store.getDetail()?.events).toEqual([original]);
  });

  it("does not expose a failed deletion from a previous PR", async () => {
    let finishDelete: () => void = () => {};
    const deletePending = new Promise<void>((resolve) => {
      finishDelete = resolve;
    });
    const get = vi.fn(async (_path: string, request: { params: { path: { number: number } } }) => ({
      data: makeDetail([], request.params.path.number),
    }));
    const store = createDetailStore({
      client: {
        GET: get,
        POST: vi.fn(async () => ({ data: undefined })),
        PUT: vi.fn(),
        DELETE: vi.fn(async () => {
          await deletePending;
          return deletionFailed("old deletion failed");
        }),
      } as unknown as GeneratedClient,
    });
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });

    const deleting = deleteComment(store, "octo", "repo", 1, 44);
    await loadDetail(store, "octo", "repo", 2, { ...pullRef, sync: false });
    finishDelete();
    await deleting;

    expect(store.getDetail()?.merge_request.Number).toBe(2);
    expect(store.getDetailError()).toBeNull();
  });

  it("keeps a provider error after reloading the same PR", async () => {
    let finishDelete: () => void = () => {};
    const pending = new Promise<void>((resolve) => (finishDelete = resolve));
    const store = createDetailStore({
      client: {
        GET: vi.fn(async () => ({ data: makeDetail([{ EventType: "issue_comment", PlatformID: 44 }]) })),
        POST: vi.fn(),
        PUT: vi.fn(),
        DELETE: vi.fn(async () => {
          await pending;
          return deletionFailed("provider denied deletion");
        }),
      } as unknown as GeneratedClient,
    });
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });
    const deleting = deleteComment(store, "octo", "repo", 1, 44);
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });
    finishDelete();
    expect(await deleting).toBe(false);
    expect(store.getDetailError()).toBe("provider denied deletion");
  });

  it("does not restore the deleted PR over a newer selection", async () => {
    let finishDelete: () => void = () => {};
    const deletePending = new Promise<void>((resolve) => {
      finishDelete = resolve;
    });
    const get = vi.fn(async (_path: string, request: { params: { path: { number: number } } }) => ({
      data: makeDetail(request.params.path.number === 1 ? [] : [{ PlatformID: 99 }], request.params.path.number),
    }));
    const store = createDetailStore({
      client: {
        GET: get,
        POST: vi.fn(async () => ({ data: undefined })),
        PUT: vi.fn(),
        DELETE: vi.fn(async () => {
          await deletePending;
          return deletionSucceeded();
        }),
      } as unknown as GeneratedClient,
    });
    await loadDetail(store, "octo", "repo", 1, { ...pullRef, sync: false });

    const deleting = deleteComment(store, "octo", "repo", 1, 44);
    await loadDetail(store, "octo", "repo", 2, { ...pullRef, sync: false });
    finishDelete();
    await deleting;

    expect(store.getDetail()?.merge_request.Number).toBe(2);
    expect(store.getDetail()?.events).toEqual([{ PlatformID: 99 }]);
  });

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
    } as unknown as GeneratedClient;

    holder.store = createDetailStore({ client });

    await loadDetail(holder.store, "octo", "repo", 1, pullRef);
    // Allow background syncDetail microtasks to settle.
    await Promise.resolve();
    await Promise.resolve();

    await submitComment(holder.store, "octo", "repo", 1, "hello");

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
    } as unknown as GeneratedClient;

    const store = createDetailStore({ client });

    await loadDetail(store, "octo", "repo", 1, pullRef);

    // Fire submitComment without awaiting; refresh GET will block on refreshPromise.
    const submitPromise = submitComment(store, "octo", "repo", 1, "hi");
    await vi.waitFor(() => expect(getCallCount).toBe(2));

    // User navigates to a different PR before the refresh resolves.
    await loadDetail(store, "octo", "repo", 2, pullRef);
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
    } as unknown as GeneratedClient;

    const store = createDetailStore({
      client,
      getPage: () => "pulls",
      pulls: { loadPulls },
    });

    await loadDetail(store, "octo", "repo", 1, pullRef);
    // Drain the background syncDetail from the initial load.
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    loadPulls.mockClear();
    postCalls.length = 0;

    await submitComment(store, "octo", "repo", 1, "hi");
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
    } as unknown as GeneratedClient;

    const store = createDetailStore({ client });

    // loadDetail resolves after the initial GET, but fires a background
    // syncDetail that is still blocked on syncPromise.
    await loadDetail(store, "octo", "repo", 1, pullRef);

    // submitComment refreshes silently and should pick up the new event.
    await submitComment(store, "octo", "repo", 1, "hello");
    await vi.waitFor(() => expect(store.getDetail()?.events).toHaveLength(1));

    // The background sync now returns stale data (no comment).
    // It must be discarded rather than overwrite the fresh detail.
    syncResolve({ data: staleDetail, error: undefined });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(store.getDetail()?.events).toHaveLength(1);
  });
});
