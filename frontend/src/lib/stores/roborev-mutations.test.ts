import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { createRoborevClient } from "../api/roborev/client.js";
import type { FetchFn } from "../api/csrf.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import { createJobsStore } from "./roborev/jobs.svelte.js";

let runtime: OwnedAppRuntime;

beforeEach(() => {
  runtime = makeAppRuntime();
});

afterEach(async () => {
  await Effect.runPromise(runtime.disposeEffect);
});

describe("Roborev mutation ownership", () => {
  it("lets later mutations run after a rerun baseline read fails before submission", async () => {
    const posts: string[] = [];
    let baselineFailed = false;
    const fetchFn: FetchFn = (input, init) => {
      const request = new Request(input, init);
      const url = new URL(request.url);
      if (url.pathname === "/api/jobs" && url.searchParams.get("id") === "17" && !baselineFailed) {
        baselineFailed = true;
        return Promise.reject(new TypeError("baseline unavailable"));
      }
      if (request.method === "POST") {
        posts.push(url.pathname);
        return Promise.resolve(Response.json({ success: true }));
      }
      return Promise.resolve(
        Response.json({
          jobs: [
            {
              id: Number(url.searchParams.get("id") ?? 18),
              agent: "codex",
              agentic: false,
              enqueued_at: "2026-08-04T12:00:00Z",
              git_ref: "deadbeef",
              job_type: "review",
              prompt_prebuilt: false,
              repo_id: 1,
              retry_count: 0,
              status: "canceled",
            },
          ],
          has_more: false,
          stats: { done: 1, closed: 0, open: 0 },
        }),
      );
    };
    const errors: string[] = [];
    const store = createJobsStore({
      client: createRoborevClient("http://localhost", fetchFn),
      runtime,
      owner: "rerun-preflight-failure-test",
      navigate: vi.fn(),
      onError: (message) => errors.push(message),
    });

    store.rerunJob(17);
    store.cancelJob(18);

    await vi.waitFor(() => expect(posts).toContain("/api/job/cancel"));
    expect(posts).not.toContain("/api/job/rerun");
    expect(errors).toContain("Failed to rerun job");
  });

  it("captures an authoritative rerun baseline before posting the mutation", async () => {
    const events: string[] = [];
    let rerunApplied = false;
    const get = vi.fn().mockImplementation((_path, request) => {
      events.push(`get:${request.params.query.id}`);
      return Promise.resolve({
        data: {
          jobs: [
            {
              id: 17,
              agent: "codex",
              agentic: false,
              enqueued_at: "2026-08-04T12:00:00Z",
              git_ref: "deadbeef",
              job_type: "review",
              prompt_prebuilt: false,
              repo_id: 1,
              retry_count: rerunApplied ? 5 : 4,
              status: rerunApplied ? "queued" : "done",
            },
          ],
          has_more: false,
          stats: { done: rerunApplied ? 0 : 1, closed: 0, open: rerunApplied ? 1 : 0 },
        },
        error: undefined,
      });
    });
    const post = vi.fn().mockImplementation(() => {
      events.push("post");
      rerunApplied = true;
      return Promise.resolve({ data: { success: true }, error: undefined });
    });
    const store = createJobsStore({
      client: { GET: get, POST: post } as never,
      runtime,
      owner: "rerun-baseline-test",
      navigate: vi.fn(),
    });

    store.rerunJob(17);

    await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
    expect(events.slice(0, 2)).toEqual(["get:17", "post"]);
  });

  it("keeps a delayed rerun preflight ordered ahead of later actions", async () => {
    const baseline = Promise.withResolvers<{
      data: { jobs: Array<{ id: number; retry_count: number; status: string }> };
      error: undefined;
    }>();
    const posts: string[] = [];
    const get = vi.fn().mockImplementation((_path, request) => {
      if (request.params.query.id === 17 && posts.length === 0) return baseline.promise;
      return Promise.resolve({
        data: { jobs: [{ id: request.params.query.id, retry_count: 1, status: "canceled" }] },
        error: undefined,
      });
    });
    const post = vi.fn().mockImplementation((path) => {
      posts.push(path);
      return Promise.resolve({ data: { success: true }, error: undefined });
    });
    const store = createJobsStore({
      client: { GET: get, POST: post } as never,
      runtime,
      owner: "rerun-preflight-order-test",
      navigate: vi.fn(),
    });

    store.rerunJob(17);
    store.cancelJob(17);
    await Promise.resolve();
    expect(posts).toEqual([]);

    baseline.resolve({ data: { jobs: [{ id: 17, retry_count: 0, status: "done" }] }, error: undefined });
    await vi.waitFor(() => expect(posts).toEqual(["/api/job/rerun", "/api/job/cancel"]));
  });

  it("does not start a later rerun before an accepted cancellation settles", async () => {
    const first = Promise.withResolvers<{ data: { success: boolean }; error: undefined }>();
    const requests: string[] = [];
    const fetchFn: FetchFn = (input) => {
      const path = new URL(new Request(input).url).pathname;
      requests.push(path);
      if (path === "/api/job/cancel") return first.promise.then((result) => Response.json(result.data));
      if (path === "/api/jobs") {
        return Promise.resolve(
          Response.json({
            jobs: [
              {
                id: 17,
                agent: "codex",
                agentic: false,
                enqueued_at: "2026-08-04T12:00:00Z",
                git_ref: "deadbeef",
                job_type: "review",
                prompt_prebuilt: false,
                repo_id: 1,
                retry_count: 0,
                status: "canceled",
              },
            ],
            has_more: false,
            stats: { done: 1, closed: 0, open: 0 },
          }),
        );
      }
      return Promise.resolve(Response.json({ success: true }));
    };
    const store = createJobsStore({
      client: createRoborevClient("http://localhost", fetchFn),
      runtime,
      owner: "mutation-test",
      navigate: vi.fn(),
    });

    store.cancelJob(17);
    await vi.waitFor(() => expect(requests).toHaveLength(1));
    store.rerunJob(17);
    await Promise.resolve();

    expect(requests.filter((path) => path === "/api/job/cancel" || path === "/api/job/rerun")).toHaveLength(1);
    first.resolve({ data: { success: true }, error: undefined });
    await vi.waitFor(() => expect(requests).toContain("/api/job/rerun"));
    expect(requests.filter((path) => path === "/api/job/cancel" || path === "/api/job/rerun")).toEqual([
      "/api/job/cancel",
      "/api/job/rerun",
    ]);
  });

  it("revalidates job rows and aggregate stats after a cancellation acknowledgement", async () => {
    let canceled = false;
    const get = vi.fn().mockImplementation(() =>
      Promise.resolve({
        data: {
          jobs: [
            {
              id: 17,
              agent: "codex",
              agentic: false,
              enqueued_at: "2026-08-04T12:00:00Z",
              git_ref: "deadbeef",
              job_type: "review",
              prompt_prebuilt: false,
              repo_id: 1,
              retry_count: 0,
              status: canceled ? "canceled" : "running",
            },
          ],
          has_more: false,
          stats: canceled ? { done: 1, closed: 0, open: 0 } : { done: 0, closed: 0, open: 1 },
        },
        error: undefined,
      }),
    );
    const post = vi.fn().mockImplementation(() => {
      canceled = true;
      return Promise.resolve({ data: { success: true }, error: undefined });
    });
    const store = createJobsStore({
      client: { GET: get, POST: post } as never,
      runtime,
      owner: "mutation-revalidation-test",
      navigate: vi.fn(),
    });
    const initial = runtime.runCommand(store.loadJobsEffect(), {
      operation: "load Roborev jobs",
      safeContext: {},
      onFailure: () => {},
    });
    await Effect.runPromise(initial.await);

    store.cancelJob(17);

    await vi.waitFor(() => expect(store.getStats()).toEqual({ done: 1, closed: 0, open: 0 }));
    expect(store.getJobs()[0]?.status).toBe("canceled");
    expect(get.mock.calls.length).toBeGreaterThan(2);
  });
});
