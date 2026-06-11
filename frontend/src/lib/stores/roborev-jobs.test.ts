import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { createJobsStore } from "@middleman/ui";
import type { components } from "@middleman/ui/api/roborev/schema";

type ReviewJob = components["schemas"]["ReviewJob"];

function makeJob(id: number, startedAt?: string, finishedAt?: string): ReviewJob {
  return {
    id,
    agent: "codex",
    agentic: false,
    enqueued_at: "2026-04-11T11:00:00Z",
    git_ref: `deadbeef${id}`,
    job_type: "review",
    prompt_prebuilt: false,
    repo_id: 1,
    retry_count: 0,
    status: "done",
    ...(startedAt ? { started_at: startedAt } : {}),
    ...(finishedAt ? { finished_at: finishedAt } : {}),
  };
}

describe("createJobsStore cost sorting", () => {
  function makeCostJob(id: number, tokenUsage?: string): ReviewJob {
    return {
      ...makeJob(id),
      ...(tokenUsage !== undefined ? { token_usage: tokenUsage } : {}),
    };
  }

  it("sorts missing cost before zero-dollar jobs", async () => {
    const jobs: ReviewJob[] = [
      makeCostJob(8),
      makeCostJob(2, JSON.stringify({ has_cost: true, cost_usd: 0.5 })),
      makeCostJob(6, JSON.stringify({ has_cost: true, cost_usd: 0 })),
      makeCostJob(5, "not json"),
    ];
    const client = {
      GET: vi.fn().mockResolvedValue({
        data: {
          jobs,
          has_more: false,
          stats: { done: 1, closed: 0, open: 0 },
        },
        error: undefined,
      }),
    };

    const store = createJobsStore({
      client: client as never,
      navigate: vi.fn(),
    });

    await store.loadJobs();
    store.setSortColumn("cost");

    expect(store.getSortColumn()).toBe("cost");
    expect(store.getSortDirection()).toBe("asc");
    expect(store.getJobs().map((job) => job.id)).toEqual([8, 5, 6, 2]);

    store.setSortColumn("cost");

    expect(store.getSortDirection()).toBe("desc");
    expect(store.getJobs().map((job) => job.id)).toEqual([2, 6, 8, 5]);
  });
});

describe("createJobsStore elapsed sorting", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-11T12:00:00Z"));
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("sorts missing elapsed before zero-second durations", async () => {
    const jobs: ReviewJob[] = [
      makeJob(8, "2026-04-11T11:45:00Z"),
      makeJob(2, "2026-04-11T11:00:00Z", "2026-04-11T11:05:00Z"),
      makeJob(6, "2026-04-11T11:30:00Z", "2026-04-11T11:30:00Z"),
      makeJob(5),
    ];
    const client = {
      GET: vi.fn().mockResolvedValue({
        data: {
          jobs,
          has_more: false,
          stats: { done: 1, closed: 0, open: 0 },
        },
        error: undefined,
      }),
    };

    const store = createJobsStore({
      client: client as never,
      navigate: vi.fn(),
    });

    await store.loadJobs();
    store.setSortColumn("elapsed");

    expect(store.getSortColumn()).toBe("elapsed");
    expect(store.getSortDirection()).toBe("asc");
    expect(store.getJobs().map((job) => job.id)).toEqual([5, 6, 2, 8]);

    store.setSortColumn("elapsed");

    expect(store.getSortDirection()).toBe("desc");
    expect(store.getJobs().map((job) => job.id)).toEqual([8, 2, 6, 5]);
  });
});
