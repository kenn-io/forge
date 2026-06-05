import { describe, expect, it, vi } from "vite-plus/test";
import type { PullRequest } from "../api/types.js";
import type { MiddlemanClient } from "../types.js";
import { createPullsStore } from "./pulls.svelte.js";

function pull(id: number, repoName: string, lastActivityAt: string): PullRequest {
  return {
    ID: id,
    Number: id,
    Title: `PR ${id}`,
    LastActivityAt: lastActivityAt,
    repo_owner: "acme",
    repo_name: repoName,
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: repoName,
      repo_path: `acme/${repoName}`,
    },
  } as PullRequest;
}

function clientWithPulls(data: PullRequest[]): MiddlemanClient {
  return {
    GET: vi.fn(async () => ({ data, error: undefined })),
  } as unknown as MiddlemanClient;
}

describe("pulls store display order", () => {
  it("preserves the API order for flat display", async () => {
    const store = createPullsStore({
      client: clientWithPulls([
        pull(1, "api", "2026-05-20T15:00:00Z"),
        pull(2, "web", "2026-05-20T14:00:00Z"),
        pull(3, "api", "2026-05-20T13:00:00Z"),
      ]),
      getGroupByRepo: () => false,
    });

    await store.loadPulls();

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([1, 2, 3]);
  });

  it("groups by repo in first-seen order rather than global activity order", async () => {
    const store = createPullsStore({
      client: clientWithPulls([
        pull(1, "api", "2026-05-20T15:00:00Z"),
        pull(2, "web", "2026-05-20T14:00:00Z"),
        pull(3, "api", "2026-05-20T13:00:00Z"),
      ]),
      getGroupByRepo: () => true,
    });

    await store.loadPulls();

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([1, 3, 2]);
  });
});
