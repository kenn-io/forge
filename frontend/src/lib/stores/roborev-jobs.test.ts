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

describe("createJobsStore auto-design filter", () => {
  function makeClient() {
    return {
      GET: vi.fn().mockResolvedValue({
        data: { jobs: [], has_more: false, stats: { done: 0, closed: 0, open: 0 } },
        error: undefined,
      }),
    };
  }

  it("sends hide_classify_jobs by default and drops it when showAutoDesign is on", async () => {
    const client = makeClient();
    const store = createJobsStore({ client: client as never, navigate: vi.fn() });

    await store.loadJobs();

    expect(store.getFilterShowAutoDesign()).toBe(false);
    expect(client.GET).toHaveBeenLastCalledWith("/api/jobs", {
      params: { query: expect.objectContaining({ hide_classify_jobs: "true" }) },
    });

    store.setFilter("showAutoDesign", true);
    await vi.waitFor(() => {
      expect(client.GET.mock.calls.length).toBeGreaterThan(1);
    });

    const lastQuery = client.GET.mock.calls.at(-1)?.[1]?.params?.query as Record<string, unknown>;
    expect(lastQuery).not.toHaveProperty("hide_classify_jobs");
  });
});

describe("createJobsStore panel expansion", () => {
  function makePanelParent(id: number): ReviewJob {
    return {
      ...makeJob(id),
      job_type: "synthesis",
      panel_role: "synthesis",
      panel_run_uuid: `run-${id}`,
      panel_summary: {
        panel_run_uuid: `run-${id}`,
        members_total: 2,
        members_terminal: 2,
        members_succeeded: 2,
        members_failed: 0,
        members_canceled: 0,
        members_skipped: 0,
      },
    };
  }

  function makeMember(id: number, runUuid: string, index: number): ReviewJob {
    return {
      ...makeJob(id),
      panel_role: "member",
      panel_run_uuid: runUuid,
      panel_member_index: index,
      panel_member_name: index === 0 ? "default" : "security",
    };
  }

  it("lazily fetches members sorted by panel_member_index on first expand", async () => {
    const parent = makePanelParent(10);
    const members = [makeMember(12, "run-10", 1), makeMember(11, "run-10", 0)];
    const client = {
      GET: vi.fn().mockImplementation((_path: string, opts: { params: { query: Record<string, unknown> } }) => {
        if (opts.params.query.panel_run === "run-10") {
          return Promise.resolve({
            data: { jobs: [parent, ...members], has_more: false, stats: { done: 0, closed: 0, open: 0 } },
            error: undefined,
          });
        }
        return Promise.resolve({
          data: { jobs: [parent], has_more: false, stats: { done: 0, closed: 0, open: 0 } },
          error: undefined,
        });
      }),
    };
    const store = createJobsStore({ client: client as never, navigate: vi.fn() });
    await store.loadJobs();

    expect(store.isPanelExpanded("run-10")).toBe(false);
    store.togglePanel(parent);
    expect(store.isPanelExpanded("run-10")).toBe(true);
    await vi.waitFor(() => {
      expect(store.getPanelMembers("run-10")).toBeDefined();
    });
    expect(store.getPanelMembers("run-10")?.map((j) => j.id)).toEqual([11, 12]);
    expect(client.GET).toHaveBeenCalledWith("/api/jobs", {
      params: { query: { panel_run: "run-10", limit: 0 } },
    });

    const calls = client.GET.mock.calls.length;
    store.togglePanel(parent);
    store.togglePanel(parent);
    expect(store.isPanelExpanded("run-10")).toBe(true);
    expect(client.GET.mock.calls.length).toBe(calls);
  });

  it("refreshes members of expanded panels when the listing reloads", async () => {
    const parent = makePanelParent(10);
    const client = {
      GET: vi.fn().mockResolvedValue({
        data: { jobs: [parent], has_more: false, stats: { done: 0, closed: 0, open: 0 } },
        error: undefined,
      }),
    };
    const store = createJobsStore({ client: client as never, navigate: vi.fn() });
    await store.loadJobs();
    store.togglePanel(parent);
    await vi.waitFor(() => expect(store.getPanelMembers("run-10")).toBeDefined());

    const before = client.GET.mock.calls.filter(
      (c) => (c[1] as { params: { query: Record<string, unknown> } }).params.query.panel_run === "run-10",
    ).length;
    await store.loadJobs();
    await vi.waitFor(() => {
      const after = client.GET.mock.calls.filter(
        (c) => (c[1] as { params: { query: Record<string, unknown> } }).params.query.panel_run === "run-10",
      ).length;
      expect(after).toBe(before + 1);
    });
  });
});
