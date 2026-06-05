import { describe, expect, it, vi } from "vite-plus/test";

import { createIssuesStore } from "@middleman/ui/stores/issues";
import type { MiddlemanClient } from "@middleman/ui";

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
    } as unknown as MiddlemanClient;

    const store = createIssuesStore({
      client,
      getPage: () => "issues",
    });

    await store.loadIssueDetail("octo", "repo", 1, issueRef);
    // Drain the background syncIssueDetail fired by loadIssueDetail.
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    const listCallsBefore = getCalls.filter((p) => p === "/issues").length;

    await store.submitIssueComment("octo", "repo", 1, "hi");
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
    } as unknown as MiddlemanClient;

    const store = createIssuesStore({
      client,
      getPage: () => "pulls",
    });

    await store.loadIssueDetail("octo", "repo", 1, issueRef);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await store.submitIssueComment("octo", "repo", 1, "hi");
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
    } as unknown as MiddlemanClient;

    const store = createIssuesStore({ client });

    await store.loadIssueDetail("octo", "repo", 1, issueRef);

    // Fire submitIssueComment without awaiting; refresh GET will block on refreshPromise.
    const submitPromise = store.submitIssueComment("octo", "repo", 1, "hi");
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    // User navigates to a different issue before the refresh resolves.
    await store.loadIssueDetail("octo", "repo", 2, issueRef);
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
    } as unknown as MiddlemanClient;

    const store = createIssuesStore({ client });

    // loadIssueDetail resolves after the initial GET, but fires a
    // background syncIssueDetail that is still blocked on syncPromise.
    await store.loadIssueDetail("octo", "repo", 1, issueRef);

    // submitIssueComment refreshes silently and should pick up the new event.
    await store.submitIssueComment("octo", "repo", 1, "hello");
    expect(store.getIssueDetail()?.events).toHaveLength(1);

    // The background sync now returns stale data (no comment).
    // It must be discarded rather than overwrite the fresh detail.
    syncResolve({ data: staleDetail, error: undefined });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(store.getIssueDetail()?.events).toHaveLength(1);
  });
});
