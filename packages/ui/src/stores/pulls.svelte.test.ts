import { describe, expect, it, vi } from "vite-plus/test";
import type { PullRequest } from "../api/types.js";
import type { MiddlemanClient } from "../types.js";
import { createPullsStore } from "./pulls.svelte.js";

function pull(id: number, repoName: string, lastActivityAt: string, overrides: Partial<PullRequest> = {}): PullRequest {
  const provider = overrides.repo?.provider ?? "github";
  const platformHost = overrides.repo?.platform_host ?? "github.com";
  const owner = overrides.repo?.owner ?? overrides.repo_owner ?? "acme";
  const name = overrides.repo?.name ?? overrides.repo_name ?? repoName;
  const repoPath = overrides.repo?.repo_path ?? `${owner}/${name}`;
  return {
    ID: id,
    Number: id,
    Title: `PR ${id}`,
    LastActivityAt: lastActivityAt,
    repo_owner: owner,
    repo_name: name,
    platform_host: platformHost,
    repo: {
      provider,
      platform_host: platformHost,
      owner,
      name,
      repo_path: repoPath,
    },
    State: "open",
    IsDraft: false,
    ReviewDecision: "",
    CIStatus: "success",
    CIChecksJSON: "[]",
    MergeableState: "clean",
    KanbanStatus: "new",
    ...overrides,
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

  it("filters pull requests by review state, readiness, CI, merge conflicts, and multiple kanban statuses", async () => {
    const store = createPullsStore({
      client: clientWithPulls([
        pull(1, "api", "2026-05-20T15:00:00Z", {
          ReviewDecision: "APPROVED",
          KanbanStatus: "reviewing",
        }),
        pull(2, "web", "2026-05-20T14:00:00Z", {
          IsDraft: true,
          KanbanStatus: "waiting",
        }),
        pull(3, "worker", "2026-05-20T13:00:00Z", {
          CIStatus: "failure",
          KanbanStatus: "awaiting_merge",
        }),
        pull(4, "api", "2026-05-20T12:00:00Z", {
          MergeableState: "dirty",
          KanbanStatus: "new",
        }),
      ]),
    });

    await store.loadPulls();

    store.toggleAttributeFilter("ready");
    store.toggleKanbanStatusFilter("reviewing");
    store.toggleKanbanStatusFilter("awaiting_merge");

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([1, 3]);

    store.toggleAttributeFilter("failed_ci");

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([3]);

    store.toggleAttributeFilter("failed_ci");
    store.toggleAttributeFilter("merge_conflicts");
    store.toggleKanbanStatusFilter("reviewing");
    store.toggleKanbanStatusFilter("awaiting_merge");

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([4]);
  });

  it("matches empty, missing, and unknown kanban statuses as New", async () => {
    const store = createPullsStore({
      client: clientWithPulls([
        pull(1, "api", "2026-05-20T15:00:00Z", { KanbanStatus: "" as PullRequest["KanbanStatus"] }),
        pull(2, "web", "2026-05-20T14:00:00Z", {
          KanbanStatus: undefined as unknown as PullRequest["KanbanStatus"],
        }),
        pull(3, "worker", "2026-05-20T13:00:00Z", { KanbanStatus: "later" as PullRequest["KanbanStatus"] }),
        pull(4, "api", "2026-05-20T12:00:00Z", { KanbanStatus: "reviewing" }),
      ]),
    });

    await store.loadPulls();

    store.toggleKanbanStatusFilter("new");

    expect(store.getDisplayOrderPRs().map((pr) => pr.ID)).toEqual([1, 2, 3]);
  });
});
