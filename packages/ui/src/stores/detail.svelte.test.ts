import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { ProblemCodes, type ProblemBody } from "../api/problems.js";
import type { PullDetail } from "../api/types.js";
import type { MiddlemanClient } from "../types.js";
import { createDetailStore } from "./detail.svelte.js";

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

function conflictProblem(reason: string): ProblemBody {
  return {
    code: ProblemCodes.conflict,
    type: "about:blank",
    title: "Conflict",
    detail: "pull request state changed",
    details: { reason },
  };
}

function mockClient(overrides: Partial<MiddlemanClient> = {}): MiddlemanClient {
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
  } as unknown as MiddlemanClient;
}

describe("createDetailStore", () => {
  afterEach(() => {
    vi.useRealTimers();
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

    await store.syncDetailNow("acme", "widget", 7, {
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
    });

    expect(store.getDetail()?.platform_head_sha).toBe("fresh-head");
    expect(pulls.loadPulls).toHaveBeenCalledTimes(1);
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

  it("falls back to a cached refresh when the post-apply sync returns nothing", async () => {
    const get = vi.fn().mockResolvedValue({ data: pullDetail("cached-head") });
    const post = vi.fn(async (path: string) => {
      if (path.endsWith("/review-suggestions/apply")) {
        return { data: { status: "applied" }, error: undefined };
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
    const loadCalls = get.mock.calls.length;

    const ok = await store.applyReviewSuggestions("acme", "widget", 7, {
      suggestions: [{ threadID: "thread-1", replacement: "return publish();" }],
    });

    expect(ok).toBe(true);
    expect(get.mock.calls.length).toBeGreaterThan(loadCalls);
    expect(store.getDetail()?.platform_head_sha).toBe("cached-head");
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
});
