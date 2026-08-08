import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { OwnedAppRuntime } from "../app/runtime.js";
import type { ProblemBody } from "../api/problems.js";
import {
  createIssuesStore as createRuntimeIssuesStore,
  type IssuesStore,
  type IssuesStoreOptions,
} from "./issues.svelte.js";
import type { GeneratedClient } from "../api/generated-api.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import { dismissFlash, getFlash, getFlashes } from "./flash.svelte.js";

let runtime: OwnedAppRuntime | undefined;

type TestIssuesStoreOptions = Omit<IssuesStoreOptions, "runtime"> & { readonly client: GeneratedClient };

function createIssuesStore(options: TestIssuesStoreOptions) {
  const { client, ...storeOptions } = options;
  runtime = makeTestAppRuntime(client);
  return createRuntimeIssuesStore({ ...storeOptions, runtime });
}

async function loadIssueDetail(store: IssuesStore, ...args: Parameters<IssuesStore["loadIssueDetail"]>): Promise<void> {
  store.loadIssueDetail(...args);
  await vi.waitFor(() => expect(store.isIssueDetailLoading()).toBe(false));
}

function submitIssueComment(
  store: IssuesStore,
  owner: string,
  name: string,
  number: number,
  body: string,
): Promise<boolean> {
  const completion = Promise.withResolvers<boolean>();
  store.submitIssueComment(owner, name, number, body, {
    onSuccess: () => completion.resolve(true),
    onFailure: () => completion.resolve(false),
  });
  return completion.promise;
}

function deleteIssueComment(
  store: IssuesStore,
  owner: string,
  name: string,
  number: number,
  commentID: number,
): Promise<boolean> {
  const completion = Promise.withResolvers<boolean>();
  store.deleteIssueComment(owner, name, number, commentID, {
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

const issueRef = {
  provider: "github",
  platformHost: "github.com",
  repoPath: "octo/repo",
};

interface MockIssueDetail {
  repo_owner: string;
  repo_name: string;
  repo: {
    provider: string;
    platform_host: string;
    owner: string;
    name: string;
    repo_path: string;
  };
  issue: { Number: number };
  events: unknown[];
}

function makeDetail(events: unknown[] = [], number = 1): MockIssueDetail {
  return {
    repo_owner: "octo",
    repo_name: "repo",
    repo: {
      provider: issueRef.provider,
      platform_host: issueRef.platformHost,
      owner: "octo",
      name: "repo",
      repo_path: issueRef.repoPath,
    },
    issue: { Number: number },
    events,
  };
}

describe("createIssuesStore submitIssueComment", () => {
  it("acknowledges a posted issue comment before reporting reconciliation failure", async () => {
    let getCalls = 0;
    const store = createIssuesStore({
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
    await loadIssueDetail(store, "octo", "repo", 1, { ...issueRef, sync: false });
    const onSuccess = vi.fn();
    const onFailure = vi.fn();
    const settled = Promise.withResolvers<void>();

    store.submitIssueComment("octo", "repo", 1, "hello", {
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

  it("does not reconcile a posted issue comment after navigation changes the selection", async () => {
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
    const store = createIssuesStore({
      client: { GET: get, POST: post, PUT: vi.fn(), DELETE: vi.fn() } as unknown as GeneratedClient,
    });
    await loadIssueDetail(store, "octo", "repo", 1, { ...issueRef, sync: false });
    const settled = Promise.withResolvers<void>();
    store.submitIssueComment("octo", "repo", 1, "hello", { onSettled: settled.resolve });
    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());

    await loadIssueDetail(store, "octo", "repo", 2, { ...issueRef, sync: false });
    posted.resolve();
    await settled.promise;
    await Promise.resolve();

    expect(get).toHaveBeenCalledTimes(2);
    expect(post).toHaveBeenCalledTimes(1);
    expect(store.getIssueDetail()?.issue.Number).toBe(2);
  });

  it("optimistically edits an issue comment and rolls it back when acknowledgement fails", async () => {
    const patch = Promise.withResolvers<ReturnType<typeof deletionFailed>>();
    const original = { EventType: "issue_comment", PlatformID: 44, Body: "before" };
    const store = createIssuesStore({
      client: {
        GET: vi.fn(async () => ({ data: makeDetail([original]) })),
        POST: vi.fn(),
        PUT: vi.fn(),
        PATCH: vi.fn(() => patch.promise),
        DELETE: vi.fn(),
      } as unknown as GeneratedClient,
    });
    const settled = Promise.withResolvers<void>();
    await loadIssueDetail(store, "octo", "repo", 1, { ...issueRef, sync: false });

    store.editIssueComment("octo", "repo", 1, 44, "after", { onSettled: settled.resolve });
    await vi.waitFor(() => expect(store.getIssueDetail()?.events[0]?.Body).toBe("after"));
    patch.resolve(deletionFailed("provider denied edit"));
    await settled.promise;

    expect(store.getIssueDetail()?.events[0]?.Body).toBe("before");
  });

  it("does not confirm a rejected issue edit through a successful deletion of another comment", async () => {
    const edit = Promise.withResolvers<ReturnType<typeof deletionFailed>>();
    const deletion = Promise.withResolvers<ReturnType<typeof deletionSucceeded>>();
    const first = { EventType: "issue_comment", PlatformID: 44, Body: "before" };
    const second = { EventType: "issue_comment", PlatformID: 45, Body: "remove me" };
    const store = createIssuesStore({
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
    await loadIssueDetail(store, "octo", "repo", 1, { ...issueRef, sync: false });

    store.editIssueComment("octo", "repo", 1, 44, "after", { onSettled: editSettled.resolve });
    store.deleteIssueComment("octo", "repo", 1, 45, { onSettled: deletionSettled.resolve });
    await vi.waitFor(() => expect(store.getIssueDetail()?.events).toEqual([{ ...first, Body: "after" }]));

    edit.resolve(deletionFailed("provider denied edit"));
    await editSettled.promise;
    deletion.resolve(deletionSucceeded());
    await deletionSettled.promise;

    expect(store.getIssueDetail()?.events).toEqual([first]);
  });

  it("optimistically hides a deleted issue comment and rolls it back when the command fails", async () => {
    const pendingDelete = Promise.withResolvers<ReturnType<typeof deletionFailed>>();
    const original = { EventType: "issue_comment", PlatformID: 44, Body: "keep me" };
    const store = createIssuesStore({
      client: {
        GET: vi.fn(async () => ({ data: makeDetail([original]) })),
        POST: vi.fn(async () => ({ data: undefined })),
        PUT: vi.fn(),
        DELETE: vi.fn(() => pendingDelete.promise),
      } as unknown as GeneratedClient,
    });
    const settled = Promise.withResolvers<void>();
    await loadIssueDetail(store, "octo", "repo", 1, { ...issueRef, sync: false });

    store.deleteIssueComment("octo", "repo", 1, 44, { onSettled: settled.resolve });
    await vi.waitFor(() => expect(store.getIssueDetail()?.events).toEqual([]));
    pendingDelete.resolve(deletionFailed("provider denied deletion"));
    await settled.promise;

    expect(store.getIssueDetail()?.events).toEqual([original]);
  });

  it("hides a deleted issue comment while ordinary sync converges", async () => {
    const staleDetail = makeDetail([{ EventType: "issue_comment", PlatformID: 44 }]);
    const get = vi.fn(async () => ({ data: staleDetail }));
    const post = vi.fn(async () => ({ data: staleDetail }));
    const del = vi.fn(async () => deletionSucceeded());
    const store = createIssuesStore({
      client: {
        GET: get,
        POST: post,
        PUT: vi.fn(),
        DELETE: del,
      } as unknown as GeneratedClient,
    });
    await loadIssueDetail(store, "octo", "repo", 1, issueRef);
    await Promise.resolve();
    get.mockClear();

    const ok = await deleteIssueComment(store, "octo", "repo", 1, 44);

    expect(ok).toBe(true);
    expect(del).toHaveBeenCalledWith("/issues/{provider}/{owner}/{name}/{number}/comments/{comment_id}", {
      headers: { "Content-Type": "application/json" },
      params: {
        path: { provider: "github", owner: "octo", name: "repo", number: 1, comment_id: 44 },
      },
      signal: expect.any(AbortSignal),
    });
    expect(store.getIssueDetail()?.events).toEqual([]);
    expect(post).toHaveBeenCalledWith("/issues/{provider}/{owner}/{name}/{number}/sync", {
      params: { path: { provider: "github", owner: "octo", name: "repo", number: 1 } },
      signal: expect.any(AbortSignal),
    });
    expect(get).not.toHaveBeenCalled();
  });

  it("does not expose a failed deletion from a previous issue", async () => {
    let finishDelete: () => void = () => {};
    const deletePending = new Promise<void>((resolve) => {
      finishDelete = resolve;
    });
    const get = vi.fn(async (_path: string, request: { params: { path: { number: number } } }) => ({
      data: makeDetail([], request.params.path.number),
    }));
    const store = createIssuesStore({
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
    await loadIssueDetail(store, "octo", "repo", 1, { ...issueRef, sync: false });

    const deleting = deleteIssueComment(store, "octo", "repo", 1, 44);
    await loadIssueDetail(store, "octo", "repo", 2, { ...issueRef, sync: false });
    finishDelete();
    await deleting;

    expect(store.getIssueDetail()?.issue.Number).toBe(2);
    expect(store.getIssueDetailError()).toBeNull();
  });

  it("keeps a provider error after reloading the same issue", async () => {
    let finishDelete: () => void = () => {};
    const pending = new Promise<void>((resolve) => (finishDelete = resolve));
    const store = createIssuesStore({
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
    await loadIssueDetail(store, "octo", "repo", 1, { ...issueRef, sync: false });
    const deleting = deleteIssueComment(store, "octo", "repo", 1, 44);
    await loadIssueDetail(store, "octo", "repo", 1, { ...issueRef, sync: false });
    finishDelete();
    expect(await deleting).toBe(false);
    expect(store.getIssueDetailError()).toBe("provider denied deletion");
  });

  it("does not restore the deleted issue over a newer selection", async () => {
    let finishDelete: () => void = () => {};
    const deletePending = new Promise<void>((resolve) => {
      finishDelete = resolve;
    });
    const get = vi.fn(async (_path: string, request: { params: { path: { number: number } } }) => ({
      data: makeDetail(request.params.path.number === 1 ? [] : [{ PlatformID: 99 }], request.params.path.number),
    }));
    const store = createIssuesStore({
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
    await loadIssueDetail(store, "octo", "repo", 1, { ...issueRef, sync: false });

    const deleting = deleteIssueComment(store, "octo", "repo", 1, 44);
    await loadIssueDetail(store, "octo", "repo", 2, { ...issueRef, sync: false });
    finishDelete();
    await deleting;

    expect(store.getIssueDetail()?.issue.Number).toBe(2);
    expect(store.getIssueDetail()?.events).toEqual([{ PlatformID: 99 }]);
  });

  it("refreshes the issues list after posting a comment when on the issues page", async () => {
    const detailData = makeDetail();
    const getCalls: string[] = [];
    const client = {
      GET: vi.fn(async (path: string) => {
        getCalls.push(path);
        if (path === "/issues") return { data: [] };
        return { data: detailData };
      }),
      POST: vi.fn(async (path: string) => {
        if (path.includes("/sync")) return { data: detailData };
        if (path.includes("/comments")) return { data: { ID: 42 } };
        return { data: undefined };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;

    const store = createIssuesStore({
      client,
      getPage: () => "issues",
    });

    await loadIssueDetail(store, "octo", "repo", 1, issueRef);
    // Drain the background syncIssueDetail fired by loadIssueDetail.
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    const listCallsBefore = getCalls.filter((p) => p === "/issues").length;

    await submitIssueComment(store, "octo", "repo", 1, "hi");
    // Drain the background syncIssueDetail fired by submitIssueComment.
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    const listCallsAfter = getCalls.filter((p) => p === "/issues").length;
    expect(listCallsAfter).toBeGreaterThan(listCallsBefore);
  });

  it("does not refresh the issues list when on a different page", async () => {
    const detailData = makeDetail();
    const getCalls: string[] = [];
    const client = {
      GET: vi.fn(async (path: string) => {
        getCalls.push(path);
        return { data: detailData };
      }),
      POST: vi.fn(async (path: string) => {
        if (path.includes("/sync")) return { data: detailData };
        if (path.includes("/comments")) return { data: { ID: 42 } };
        return { data: undefined };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;

    const store = createIssuesStore({
      client,
      getPage: () => "pulls",
    });

    await loadIssueDetail(store, "octo", "repo", 1, issueRef);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await submitIssueComment(store, "octo", "repo", 1, "hi");
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(getCalls.some((p) => p === "/issues")).toBe(false);
  });

  it("does not overwrite a newly-loaded issue if the comment refresh resolves later", async () => {
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
        if (getCallCount === 1) return { data: detailA }; // initial loadIssueDetail 1
        if (getCallCount === 2) return await refreshPromise; // refreshIssueDetail inside submitIssueComment (deferred)
        return { data: detailB }; // loadIssueDetail 2
      }),
      POST: vi.fn(async (path: string) => {
        if (path.includes("/sync")) return { data: undefined };
        if (path.includes("/comments")) return { data: { ID: 42 } };
        return { data: undefined };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;

    const store = createIssuesStore({ client });

    await loadIssueDetail(store, "octo", "repo", 1, issueRef);

    // Fire submitIssueComment without awaiting; refresh GET will block on refreshPromise.
    const submitPromise = submitIssueComment(store, "octo", "repo", 1, "hi");
    await vi.waitFor(() => expect(getCallCount).toBe(2));

    // User navigates to a different issue before the refresh resolves.
    await loadIssueDetail(store, "octo", "repo", 2, issueRef);
    expect((store.getIssueDetail() as unknown as MockIssueDetail)?.issue.Number).toBe(2);

    // Now release the in-flight refresh — it must be discarded.
    refreshResolve({ data: detailA });
    await submitPromise;
    await Promise.resolve();
    await Promise.resolve();

    expect((store.getIssueDetail() as unknown as MockIssueDetail)?.issue.Number).toBe(2);
  });

  it("discards stale syncIssueDetail responses after posting a comment", async () => {
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
        // First call: initial loadIssueDetail — still no comment.
        // Second call: refreshIssueDetail inside submitIssueComment — comment present.
        if (getCallCount === 1) return { data: staleDetail };
        return { data: freshDetail };
      }),
      POST: vi.fn(async (path: string) => {
        if (path.includes("/sync")) {
          syncCallCount++;
          // First sync: background sync from initial loadIssueDetail,
          // blocked on deferred promise and resolves with stale data.
          // Second sync: post-comment sync from submitIssueComment,
          // returns fresh data immediately.
          if (syncCallCount === 1) return await syncPromise;
          return { data: freshDetail };
        }
        if (path.includes("/comments")) return { data: { ID: 42 } };
        return { data: undefined };
      }),
      PUT: vi.fn(),
      DELETE: vi.fn(),
    } as unknown as GeneratedClient;

    const store = createIssuesStore({ client });

    // loadIssueDetail resolves after the initial GET, but fires a
    // background syncIssueDetail that is still blocked on syncPromise.
    await loadIssueDetail(store, "octo", "repo", 1, issueRef);

    // submitIssueComment refreshes silently and should pick up the new event.
    await submitIssueComment(store, "octo", "repo", 1, "hello");
    await vi.waitFor(() => expect(store.getIssueDetail()?.events).toHaveLength(1));

    // The background sync now returns stale data (no comment).
    // It must be discarded rather than overwrite the fresh detail.
    syncResolve({ data: staleDetail, error: undefined });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(store.getIssueDetail()?.events).toHaveLength(1);
  });
});
