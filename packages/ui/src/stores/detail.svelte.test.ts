import { afterEach, describe, expect, it, vi } from "vite-plus/test";

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
  } as unknown as PullDetail;
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
});
