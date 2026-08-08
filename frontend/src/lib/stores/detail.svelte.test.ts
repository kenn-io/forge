import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { ProblemCodes, type ProblemBody } from "../api/problems.js";
import type { PullDetail } from "../api/types.js";
import type { ForgeClient } from "../types.js";
import { createDetailStore } from "./detail.svelte.js";
import { dismissFlash, getFlash, getFlashes } from "./flash.svelte.js";

function pullDetail(headSHA: string): PullDetail {
  return {
    merge_request: {
      Number: 7,
      State: "open",
      IsDraft: false,
      MergeableState: "",
      platform_head_sha: headSHA,
    },
    platform_head_sha: headSHA,
    reviewed_head_sha: headSHA,
    repo: {
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widget",
    },
    events: [],
    detail_loaded: true,
    repo_owner: "acme",
    repo_name: "widget",
  } as unknown as PullDetail;
}

function pullDetailFor(name: string, number: number, headSHA: string): PullDetail {
  const result = pullDetail(headSHA);
  result.repo_name = name;
  result.repo.repo_path = `acme/${name}`;
  result.merge_request.Number = number;
  return result;
}

function deferred<T>(): {
  promise: Promise<T>;
  resolve: (value: T) => void;
} {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function conflictProblem(reason: string): ProblemBody {
  return {
    code: ProblemCodes.conflict,
    type: "about:blank",
    title: "Conflict",
    detail: "pull request state changed",
    details: { reason },
  };
}

function mockClient(overrides: Partial<ForgeClient> = {}): ForgeClient {
  return {
    GET: vi.fn(),
    POST: vi.fn(),
    PUT: vi.fn(),
    PATCH: vi.fn(),
    DELETE: vi.fn(),
    OPTIONS: vi.fn(),
    HEAD: vi.fn(),
    TRACE: vi.fn(),
    ...overrides,
  } as unknown as ForgeClient;
}

describe("createDetailStore", () => {
  afterEach(() => {
    for (const item of getFlashes()) dismissFlash(item.id);
    localStorage.clear();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("keeps the displayed detail object when a refresh returns identical content", async () => {
    const routeIdentity = {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    };
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: { ...pullDetail("head"), detail_fetched_at: "2026-07-15T10:00:00Z" } })
      .mockResolvedValueOnce({ data: { ...pullDetail("head"), detail_fetched_at: "2026-07-15T10:01:00Z" } })
      .mockResolvedValueOnce({ data: { ...pullDetail("new-head"), detail_fetched_at: "2026-07-15T10:02:00Z" } });
    const store = createDetailStore({
      client: mockClient({ GET: get }),
      getPage: () => "pulls",
    });
    await store.loadDetail("acme", "widget", 7, { ...routeIdentity, sync: false });
    const displayed = store.getDetail();

    // Only the sync timestamp moved: the polling refresh must not swap
    // in an equal-but-new object, or the PR panel re-renders every cycle.
    await store.refreshDetailOnly("acme", "widget", 7, routeIdentity);
    expect(store.getDetail()).toBe(displayed);
    expect(store.getDetail()?.detail_fetched_at).toBe("2026-07-15T10:00:00Z");

    // Real content changes still apply.
    await store.refreshDetailOnly("acme", "widget", 7, routeIdentity);
    expect(store.getDetail()).not.toBe(displayed);
    expect(store.getDetail()?.platform_head_sha).toBe("new-head");
  });

  it("rejects an old selection refresh that resolves after the new selection", async () => {
    const loadB = deferred<{ data: PullDetail }>();
    const refreshA = deferred<{ data: PullDetail }>();
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: pullDetailFor("widget-a", 7, "head-a") })
      .mockReturnValueOnce(loadB.promise)
      .mockReturnValueOnce(refreshA.promise);
    const store = createDetailStore({ client: mockClient({ GET: get }) });
    const identity = (name: string) => ({
      provider: "github",
      platformHost: "github.com",
      repoPath: `acme/${name}`,
      sync: false as const,
    });
    await store.loadDetail("acme", "widget-a", 7, identity("widget-a"));

    const loadingB = store.loadDetail("acme", "widget-b", 8, identity("widget-b"));
    const refreshingA = store.refreshDetailOnly("acme", "widget-a", 7, identity("widget-a"));
    loadB.resolve({ data: pullDetailFor("widget-b", 8, "head-b") });
    await loadingB;
    refreshA.resolve({ data: pullDetailFor("widget-a", 7, "late-head-a") });
    await refreshingA;

    expect(store.getDetail()?.repo_name).toBe("widget-b");
    expect(store.getDetail()?.merge_request.Number).toBe(8);
    expect(store.getDetail()?.platform_head_sha).toBe("head-b");
  });

  it("rejects an initial load that resolves after a newer refresh for the same selection", async () => {
    const initialLoad = deferred<{ data: PullDetail }>();
    const newerRefresh = deferred<{ data: PullDetail }>();
    const get = vi.fn().mockReturnValueOnce(initialLoad.promise).mockReturnValueOnce(newerRefresh.promise);
    const store = createDetailStore({ client: mockClient({ GET: get }) });
    const identity = {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false as const,
    };

    const loading = store.loadDetail("acme", "widget", 7, identity);
    const refreshing = store.refreshDetailOnly("acme", "widget", 7, identity);
    newerRefresh.resolve({ data: pullDetail("newer-head") });
    await refreshing;
    initialLoad.resolve({ data: pullDetail("older-head") });
    await loading;

    expect(store.getDetail()?.platform_head_sha).toBe("newer-head");
  });

  it("an older-started sync response cannot overwrite a newer envelope", async () => {
    // The sync path has no per-selection sequence guard; without atomic
    // payload+tick application its stale response would replace newer
    // detail while the newer tick stands — letting pre-creation "no
    // workspace" data masquerade as an authoritative post-create absence.
    const routeIdentity = {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    };
    const withWorkspace = { ...pullDetail("head"), workspace: { id: "ws-1", status: "ready" } };
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: pullDetail("head") })
      .mockResolvedValueOnce({ data: withWorkspace });
    const syncPost = deferred<{ data: PullDetail }>();
    const post = vi.fn().mockReturnValueOnce(syncPost.promise);
    const store = createDetailStore({ client: mockClient({ GET: get, POST: post }) });
    await store.loadDetail("acme", "widget", 7, { ...routeIdentity, sync: false });

    // The sync's request starts before the refresh below applies a newer
    // envelope carrying the workspace.
    const syncing = store.syncDetailNow("acme", "widget", 7, routeIdentity);
    await store.refreshDetailOnly("acme", "widget", 7, routeIdentity);
    expect(store.getDetail()?.workspace?.id).toBe("ws-1");
    const newerTick = store.getDetailEnvelopeTick();

    syncPost.resolve({ data: pullDetail("head") });
    await syncing;

    expect(store.getDetail()?.workspace?.id).toBe("ws-1");
    expect(store.getDetailEnvelopeTick()).toBe(newerTick);
  });

  it("applies a pending initial load when a newer refresh fails for the same selection", async () => {
    const initialLoad = deferred<{ data?: PullDetail; error?: ProblemBody }>();
    const newerRefresh = deferred<{ data?: PullDetail; error?: ProblemBody }>();
    const get = vi.fn().mockReturnValueOnce(initialLoad.promise).mockReturnValueOnce(newerRefresh.promise);
    const store = createDetailStore({ client: mockClient({ GET: get }) });
    const identity = {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false as const,
    };

    const loading = store.loadDetail("acme", "widget", 7, identity);
    const refreshing = store.refreshDetailOnly("acme", "widget", 7, identity);
    newerRefresh.resolve({ error: conflictProblem("detail_refresh_failed") });
    await refreshing;
    initialLoad.resolve({ data: pullDetail("loaded-head") });
    await loading;

    expect(store.getDetail()?.platform_head_sha).toBe("loaded-head");
  });

  it.each([
    {
      loaded: { provider: "gh", platformHost: undefined },
      refreshed: { provider: "github", platformHost: "github.com" },
    },
    {
      loaded: { provider: "github", platformHost: "github.com" },
      refreshed: { provider: "gh", platformHost: undefined },
    },
  ])("treats provider aliases and omitted default hosts as the same selection", async ({ loaded, refreshed }) => {
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: pullDetail("head") })
      .mockResolvedValueOnce({ data: pullDetail("refreshed-head") });
    const store = createDetailStore({ client: mockClient({ GET: get }) });
    const identity = { repoPath: "acme/widget", sync: false as const };
    await store.loadDetail("acme", "widget", 7, { ...identity, ...loaded });

    await store.refreshDetailOnly("acme", "widget", 7, {
      repoPath: identity.repoPath,
      ...refreshed,
    });

    expect(store.getDetail()?.platform_head_sha).toBe("refreshed-head");
  });

  it("applies a warnings-only change since the panel renders warnings", async () => {
    const routeIdentity = {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    };
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: { ...pullDetail("head"), detail_fetched_at: "2026-07-15T10:00:00Z" } })
      .mockResolvedValueOnce({
        data: { ...pullDetail("head"), warnings: ["diff unavailable"], detail_fetched_at: "2026-07-15T10:01:00Z" },
      })
      .mockResolvedValueOnce({
        data: { ...pullDetail("head"), warnings: ["diff unavailable"], detail_fetched_at: "2026-07-15T10:02:00Z" },
      });
    const store = createDetailStore({
      client: mockClient({ GET: get }),
      getPage: () => "pulls",
    });
    await store.loadDetail("acme", "widget", 7, { ...routeIdentity, sync: false });

    await store.refreshDetailOnly("acme", "widget", 7, routeIdentity);
    expect((store.getDetail() as { warnings?: string[] } | null)?.warnings).toEqual(["diff unavailable"]);

    // The same warnings again are not a change.
    const displayed = store.getDetail();
    await store.refreshDetailOnly("acme", "widget", 7, routeIdentity);
    expect(store.getDetail()).toBe(displayed);
  });

  it("baselines polling convergence on the latest observed timestamp, not the frozen store one", async () => {
    vi.useFakeTimers();
    const routeIdentity = {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    };
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: { ...pullDetail("head"), detail_fetched_at: "2026-07-15T10:00:00Z" } })
      .mockResolvedValueOnce({ data: { ...pullDetail("head"), detail_fetched_at: "2026-07-15T10:01:00Z" } })
      .mockResolvedValueOnce({ data: { ...pullDetail("head"), detail_fetched_at: "2026-07-15T10:01:00Z" } })
      .mockResolvedValue({ data: { ...pullDetail("new-head"), detail_fetched_at: "2026-07-15T10:02:00Z" } });
    const post = vi.fn().mockResolvedValue({ error: undefined });
    const store = createDetailStore({
      client: mockClient({ GET: get, POST: post }),
      getPage: () => "pulls",
    });
    await store.loadDetail("acme", "widget", 7, { ...routeIdentity, sync: false });
    // Content-identical refresh: the store timestamp freezes at 10:00
    // while the server clock is at 10:01.
    await store.refreshDetailOnly("acme", "widget", 7, routeIdentity);

    // The next polling cycle's first re-GET still returns 10:01. Judged
    // against the frozen store timestamp that would look like completion
    // and drop the real change; against the observed baseline the loop
    // keeps going and applies the new head from the finished sync.
    store.startDetailPolling("acme", "widget", 7, routeIdentity);
    await vi.advanceTimersByTimeAsync(60_000 + 300 + 700 + 100);

    expect(store.getDetail()?.platform_head_sha).toBe("new-head");
    store.stopDetailPolling();
  });

  it("flashes failed optimistic state changes without poisoning detail load state", async () => {
    const optimisticKanbanUpdate = vi.fn();
    const store = createDetailStore({
      client: mockClient({
        GET: vi.fn().mockResolvedValue({ data: pullDetail("head") }),
        PUT: vi.fn().mockResolvedValue({ error: { detail: "permission denied" } }),
      }),
      getPage: () => "pulls",
      pulls: {
        loadPulls: vi.fn().mockResolvedValue(undefined),
        getPullKanbanStatus: vi.fn(() => "new"),
        optimisticKanbanUpdate,
      },
    });
    await store.loadDetail("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false,
    });

    await store.updateKanbanState("acme", "widget", 7, "reviewing");

    expect(getFlash()).toMatchObject({ message: "permission denied", tone: "danger" });
    expect(store.getDetailError()).toBeNull();
    expect(optimisticKanbanUpdate).toHaveBeenLastCalledWith(expect.anything(), 7, "new");
  });

  it("syncs detail and resolves after applying the refreshed head", async () => {
    const post = vi.fn().mockResolvedValue({
      data: pullDetail("fresh-head"),
      error: undefined,
      response: new Response("{}", { status: 200 }),
    });
    const pulls = { loadPulls: vi.fn().mockResolvedValue(undefined) };
    const store = createDetailStore({
      client: mockClient({ POST: post }),
      getPage: () => "pulls",
      pulls,
    });

    const refreshed = await store.syncDetailNow("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    });

    expect(refreshed).toBe(true);
    expect(store.getDetail()?.platform_head_sha).toBe("fresh-head");
    expect(pulls.loadPulls).toHaveBeenCalledTimes(1);
  });

  it("reports when an explicit detail sync cannot refresh state", async () => {
    const store = createDetailStore({
      client: mockClient({
        POST: vi.fn().mockResolvedValue({ error: { detail: "provider unavailable" } }),
      }),
    });

    const refreshed = await store.syncDetailNow("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    });

    expect(refreshed).toBe(false);
    expect(store.getDetail()).toBeNull();
  });

  it("enqueues background sync when active detail polling fires", async () => {
    vi.useFakeTimers();
    const post = vi.fn().mockResolvedValue({ error: undefined });
    const get = vi.fn().mockResolvedValue({ data: pullDetail("cached-head") });
    const store = createDetailStore({
      client: mockClient({ GET: get, POST: post }),
    });

    store.startDetailPolling("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    });

    await vi.advanceTimersByTimeAsync(60_000);

    expect(post).toHaveBeenCalledWith("/pulls/{provider}/{owner}/{name}/{number}/sync/async", {
      params: {
        path: {
          provider: "github",
          owner: "acme",
          name: "widget",
          number: 7,
        },
      },
    });
  });

  it("awaits a sync-enabled refresh after apply-suggestion success", async () => {
    const get = vi.fn().mockResolvedValue({ data: pullDetail("old-head") });
    const post = vi.fn(async (path: string) => {
      if (path.endsWith("/review-suggestions/apply")) {
        return { data: { status: "applied" }, error: undefined };
      }
      if (path.endsWith("/sync")) {
        return { data: pullDetail("new-head"), error: undefined };
      }
      return { error: undefined };
    });
    const store = createDetailStore({
      client: mockClient({ GET: get, POST: post }),
      getPage: () => "pulls",
      pulls: { loadPulls: vi.fn().mockResolvedValue(undefined) },
    });
    await store.loadDetail("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false,
    });

    const ok = await store.applyReviewSuggestions("acme", "widget", 7, {
      suggestions: [{ threadID: "thread-1", replacement: "return publish();" }],
    });

    expect(ok).toBe(true);
    expect(store.getDetail()?.platform_head_sha).toBe("new-head");
    expect(post).toHaveBeenCalledWith(
      "/pulls/{provider}/{owner}/{name}/{number}/sync",
      expect.objectContaining({
        params: expect.objectContaining({
          path: expect.objectContaining({ provider: "github", owner: "acme", name: "widget", number: 7 }),
        }),
      }),
    );
  });

  it("shows rate-limit retry timing in local time when applying a suggestion", async () => {
    const toLocaleTimeString = vi.spyOn(Date.prototype, "toLocaleTimeString").mockReturnValue("09:35");
    const get = vi.fn().mockResolvedValue({ data: pullDetail("reviewed-head") });
    const post = vi.fn(async (path: string) => {
      if (path.endsWith("/review-suggestions/apply")) {
        return {
          error: {
            code: ProblemCodes.rateLimited,
            type: "about:blank",
            title: "Too Many Requests",
            detail: "github.com rate-limited",
            details: { retryAfter: "2026-05-19T14:35:00Z" },
          },
        };
      }
      return { error: undefined };
    });
    const store = createDetailStore({ client: mockClient({ GET: get, POST: post }) });
    await store.loadDetail("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false,
    });

    const ok = await store.applyReviewSuggestions("acme", "widget", 7, {
      suggestions: [{ threadID: "thread-1", replacement: "return publish();" }],
    });

    expect(ok).toBe(false);
    expect(getFlash()).toMatchObject({
      message: "github.com rate-limited; retry at 09:35",
      tone: "danger",
    });
    expect(toLocaleTimeString).toHaveBeenCalledWith(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    });
  });

  it("syncs detail before returning false for apply-suggestion state conflicts", async () => {
    for (const reason of ["stale_state", "head_unknown", "not_open", "head_repo_unknown"] as const) {
      const problem = conflictProblem(reason);
      const get = vi.fn().mockResolvedValue({ data: pullDetail("old-head") });
      const post = vi.fn(async (path: string) => {
        if (path.endsWith("/review-suggestions/apply")) {
          return { error: problem };
        }
        if (path.endsWith("/sync")) {
          return { data: pullDetail(`fresh-${reason}`), error: undefined };
        }
        return { error: undefined };
      });
      const store = createDetailStore({
        client: mockClient({ GET: get, POST: post }),
        getPage: () => "pulls",
        pulls: { loadPulls: vi.fn().mockResolvedValue(undefined) },
      });
      await store.loadDetail("acme", "widget", 7, {
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
        sync: false,
      });

      const ok = await store.applyReviewSuggestions("acme", "widget", 7, {
        suggestions: [{ threadID: "thread-1", replacement: "return publish();" }],
      });

      expect(ok).toBe(false);
      expect(store.getDetail()?.platform_head_sha).toBe(`fresh-${reason}`);
      expect(store.getDetailError()).toBe("pull request state changed");
      expect(post).toHaveBeenCalledWith(
        "/pulls/{provider}/{owner}/{name}/{number}/sync",
        expect.objectContaining({
          params: expect.objectContaining({
            path: expect.objectContaining({ provider: "github", owner: "acme", name: "widget", number: 7 }),
          }),
        }),
      );
    }
  });

  it("reports a typed suggestion conflict with the submitted head and route identity", async () => {
    const get = vi.fn().mockResolvedValue({ data: pullDetail("reviewed-head") });
    const post = vi.fn(async (path: string) => {
      if (path.endsWith("/review-suggestions/apply")) {
        return { error: conflictProblem("stale_state") };
      }
      return { error: undefined };
    });
    const store = createDetailStore({ client: mockClient({ GET: get, POST: post }) });
    await store.loadDetail("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false,
    });
    const onConflict = vi.fn();

    const ok = await store.applyReviewSuggestions(
      "acme",
      "widget",
      7,
      { suggestions: [{ threadID: "thread-1", replacement: "return publish();" }] },
      onConflict,
    );

    expect(ok).toBe(false);
    expect(onConflict).toHaveBeenCalledWith({
      reason: "stale_state",
      context: undefined,
      expectedHeadSha: "reviewed-head",
      ref: {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
        number: 7,
      },
      number: 7,
    });
    expect(post.mock.calls.some(([path]) => String(path).endsWith("/sync"))).toBe(false);
  });

  it("ignores a delayed suggestion conflict after an A-to-B-to-A route cycle", async () => {
    let resolveApply!: (value: { error: ProblemBody }) => void;
    const applyResponse = new Promise<{ error: ProblemBody }>((resolve) => {
      resolveApply = resolve;
    });
    const get = vi.fn(async (_path: string, options: { params: { path: { name: string; number: number } } }) => {
      const loaded = pullDetail("reviewed-head");
      loaded.repo_name = options.params.path.name;
      loaded.repo.repo_path = `acme/${options.params.path.name}`;
      loaded.merge_request.Number = options.params.path.number;
      return { data: loaded };
    });
    const post = vi.fn((path: string) => {
      if (path.endsWith("/review-suggestions/apply")) return applyResponse;
      return Promise.resolve({ error: undefined });
    });
    const store = createDetailStore({ client: mockClient({ GET: get, POST: post }) });
    const load = (name: string, number: number) =>
      store.loadDetail("acme", name, number, {
        provider: "github",
        platformHost: "github.com",
        repoPath: `acme/${name}`,
        sync: false,
      });
    await load("widget", 7);
    const onConflict = vi.fn();
    const applying = store.applyReviewSuggestions(
      "acme",
      "widget",
      7,
      { suggestions: [{ threadID: "thread-1", replacement: "return publish();" }] },
      onConflict,
    );

    await load("other-widget", 8);
    await load("widget", 7);
    resolveApply({ error: conflictProblem("stale_state") });

    await expect(applying).resolves.toBe(false);
    expect(onConflict).not.toHaveBeenCalled();
    expect(store.getDetailError()).toBeNull();
    expect(store.getDetail()?.repo_name).toBe("widget");
  });

  it("accepts a delayed suggestion conflict after a same-pull refresh", async () => {
    let resolveApply!: (value: { error: ProblemBody }) => void;
    const applyResponse = new Promise<{ error: ProblemBody }>((resolve) => {
      resolveApply = resolve;
    });
    const get = vi.fn().mockResolvedValue({ data: pullDetail("reviewed-head") });
    const post = vi.fn((path: string) => {
      if (path.endsWith("/review-suggestions/apply")) return applyResponse;
      return Promise.resolve({ error: undefined });
    });
    const store = createDetailStore({ client: mockClient({ GET: get, POST: post }) });
    const options = {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false as const,
    };
    await store.loadDetail("acme", "widget", 7, options);
    const onConflict = vi.fn();
    const applying = store.applyReviewSuggestions(
      "acme",
      "widget",
      7,
      { suggestions: [{ threadID: "thread-1", replacement: "return publish();" }] },
      onConflict,
    );

    await store.loadDetail("acme", "widget", 7, options);
    resolveApply({ error: conflictProblem("stale_state") });

    await expect(applying).resolves.toBe(false);
    expect(onConflict).toHaveBeenCalledWith(
      expect.objectContaining({
        reason: "stale_state",
        expectedHeadSha: "reviewed-head",
      }),
    );
    expect(store.getDetailError()).toBe("pull request state changed");
  });

  it("does not retain a pending suggestion reconciliation after navigation", async () => {
    let resolveApply!: (value: { data: { status: string }; error: undefined }) => void;
    const applyResponse = new Promise<{ data: { status: string }; error: undefined }>((resolve) => {
      resolveApply = resolve;
    });
    const get = vi.fn(async (_path: string, options: { params: { path: { name: string; number: number } } }) => {
      const loaded = pullDetail(options.params.path.name === "widget" ? "reviewed-head" : "other-head");
      loaded.repo_name = options.params.path.name;
      loaded.repo.repo_path = `acme/${options.params.path.name}`;
      loaded.merge_request.Number = options.params.path.number;
      return { data: loaded };
    });
    const post = vi.fn((path: string) => {
      if (path.endsWith("/review-suggestions/apply")) return applyResponse;
      return Promise.resolve({ error: undefined });
    });
    const store = createDetailStore({ client: mockClient({ GET: get, POST: post }) });
    const load = (name: string, number: number) =>
      store.loadDetail("acme", name, number, {
        provider: "github",
        platformHost: "github.com",
        repoPath: `acme/${name}`,
        sync: false,
      });
    await load("widget", 7);
    const applying = store.applyReviewSuggestions("acme", "widget", 7, {
      suggestions: [{ threadID: "thread-1", replacement: "return publish();" }],
    });

    await load("other-widget", 8);
    resolveApply({ data: { status: "applied" }, error: undefined });

    await expect(applying).resolves.toBe(true);
    expect(store.getDetail()?.repo_name).toBe("other-widget");
    await load("widget", 7);
    expect(store.getDetail()?.platform_head_sha).toBe("reviewed-head");
    expect(post.mock.calls.some(([path]) => String(path).endsWith("/sync"))).toBe(false);
    expect(getFlash()).toMatchObject({
      message: "Suggestion was applied after navigation. Refresh before applying it again.",
      tone: "warning",
    });
  });

  it("flashes a delayed ordinary suggestion failure without changing the new selection", async () => {
    let resolveApply!: (value: { error: { detail: string } }) => void;
    const applyResponse = new Promise<{ error: { detail: string } }>((resolve) => {
      resolveApply = resolve;
    });
    const get = vi.fn(async (_path: string, options: { params: { path: { name: string; number: number } } }) => {
      const loaded = pullDetail(options.params.path.name === "widget" ? "reviewed-head" : "other-head");
      loaded.repo_name = options.params.path.name;
      loaded.repo.repo_path = `acme/${options.params.path.name}`;
      loaded.merge_request.Number = options.params.path.number;
      return { data: loaded };
    });
    const post = vi.fn((path: string) => {
      if (path.endsWith("/review-suggestions/apply")) return applyResponse;
      return Promise.resolve({ error: undefined });
    });
    const store = createDetailStore({ client: mockClient({ GET: get, POST: post }) });
    const load = (name: string, number: number) =>
      store.loadDetail("acme", name, number, {
        provider: "github",
        platformHost: "github.com",
        repoPath: `acme/${name}`,
        sync: false,
      });
    await load("widget", 7);
    const applying = store.applyReviewSuggestions("acme", "widget", 7, {
      suggestions: [{ threadID: "thread-1", replacement: "return publish();" }],
    });

    await load("other-widget", 8);
    resolveApply({ error: { detail: "provider rejected suggestion" } });

    await expect(applying).resolves.toBe(false);
    expect(store.getDetail()?.repo_name).toBe("other-widget");
    expect(store.getDetail()?.merge_request.Number).toBe(8);
    expect(getFlashes()).toHaveLength(1);
    expect(getFlash()).toMatchObject({ message: "provider rejected suggestion", tone: "danger" });
  });

  it("fails closed when apply-suggestion conflict refresh returns no detail", async () => {
    const tests = [
      {
        reason: "stale_state",
        assertDetail: (detail: PullDetail | null) => {
          expect(detail?.platform_head_sha).toBe("");
          expect(detail?.merge_request.State).toBe("open");
        },
      },
      {
        reason: "not_open",
        assertDetail: (detail: PullDetail | null) => {
          expect(detail?.platform_head_sha).toBe("old-head");
          expect(detail?.merge_request.State).toBe("closed");
        },
      },
    ] as const;
    for (const tt of tests) {
      const get = vi.fn().mockResolvedValue({ data: pullDetail("old-head") });
      const post = vi.fn(async (path: string) => {
        if (path.endsWith("/review-suggestions/apply")) {
          return { error: conflictProblem(tt.reason) };
        }
        if (path.endsWith("/sync")) {
          return { error: undefined };
        }
        return { error: undefined };
      });
      const store = createDetailStore({
        client: mockClient({ GET: get, POST: post }),
      });
      await store.loadDetail("acme", "widget", 7, {
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
        sync: false,
      });

      const ok = await store.applyReviewSuggestions("acme", "widget", 7, {
        suggestions: [{ threadID: "thread-1", replacement: "return publish();" }],
      });

      expect(ok).toBe(false);
      expect(store.getDetailError()).toBe("pull request state changed");
      tt.assertDetail(store.getDetail());
    }
  });

  it("applies a local body edit addressed through a provider alias and omitted host", async () => {
    // Task-list checkbox clicks pass the caller's route vocabulary; the
    // loaded payload is canonical ("github"/"github.com"). An exact
    // comparison would silently drop the optimistic toggle.
    const get = vi.fn().mockResolvedValueOnce({ data: pullDetail("head") });
    const store = createDetailStore({ client: mockClient({ GET: get }) });
    await store.loadDetail("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      sync: false,
    });

    store.setLocalPRBody("gh", undefined, "acme", "widget", 7, "- [x] done");

    expect(store.getDetail()?.merge_request.Body).toBe("- [x] done");
    expect(store.hasUnsavedLocalBody()).toBe(true);
  });
});
