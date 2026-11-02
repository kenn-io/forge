import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import type { PullRequest } from "../../api/types.js";

const mockGet = vi.fn();
const mockNavigate = vi.fn();
const globalRepo = vi.hoisted(() => ({ current: undefined as string | undefined }));
const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));
const eventSubscribers = vi.hoisted(() => new Set<(event: { type: string }) => void>());

vi.mock("../../app/runtime.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/runtime.js")>();
  const { makeGeneratedClientFromRouteMocks } = await import("../../testing/test/route-mock-client.js");
  return {
    ...actual,
    makeAppRuntime: () =>
      actual.makeAppRuntime(makeGeneratedClientFromRouteMocks({ GET: (...args: unknown[]) => mockGet(...args) })),
  };
});

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => {
    const runtime = runtimeCapture.current;
    if (runtime === undefined) throw new Error("merge queues test runtime is not initialized");
    return runtime;
  },
}));

vi.mock("../../stores/router.svelte.js", () => ({
  navigate: (path: string) => mockNavigate(path),
  withBasePath: (path: string) => path,
}));

vi.mock("../../stores/filter.svelte.js", () => ({
  getGlobalRepo: () => globalRepo.current,
}));

vi.mock("../../context.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../context.js")>();
  return {
    ...actual,
    getStores: () => ({
      events: {
        subscribeWorkspaceEvents: (subscriber: (event: { type: string }) => void) => {
          eventSubscribers.add(subscriber);
          return () => eventSubscribers.delete(subscriber);
        },
      },
    }),
  };
});

import MergeQueuesPage from "./MergeQueuesPage.svelte";

function mkPR(number: number, overrides: Partial<PullRequest> = {}): PullRequest {
  return {
    ID: number,
    Number: number,
    Title: `Change ${number}`,
    Author: "octocat",
    AuthorDisplayName: "",
    BaseBranch: "main",
    URL: `https://github.com/acme/widgets/pull/${number}`,
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
    ...overrides,
  } as PullRequest;
}

const queued = (position: number, state: string, enqueuedAt: string): Pick<PullRequest, "merge_queue"> => ({
  merge_queue: { position, state, enqueued_at: enqueuedAt, observed_at: enqueuedAt },
});

function respondWith(pulls: PullRequest[]): void {
  mockGet.mockImplementation((path: string) =>
    Promise.resolve(path === "/pulls" ? { data: pulls } : { error: { status: 404 } }),
  );
}

describe("MergeQueuesPage", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-07-10T15:10:00Z"));
    runtimeCapture.current = makeAppRuntime();
    globalRepo.current = undefined;
    mockGet.mockReset();
    mockNavigate.mockReset();
    eventSubscribers.clear();
  });

  afterEach(async () => {
    cleanup();
    if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
    runtimeCapture.current = undefined;
    vi.useRealTimers();
  });

  it("shows each queue's pull requests in queue order with their state, and removals with the failed checks", async () => {
    respondWith([
      mkPR(12, { Title: "Second in line", ...queued(2, "awaiting_checks", "2026-07-10T15:05:00Z") }),
      mkPR(11, {
        Title: "Front of the queue",
        AuthorDisplayName: "Mona",
        ...queued(1, "mergeable", "2026-07-10T15:00:00Z"),
        merge_queue: {
          position: 1,
          state: "mergeable",
          enqueued_at: "2026-07-10T15:00:00Z",
          observed_at: "2026-07-10T15:09:00Z",
          estimated_time_to_merge_seconds: 300,
        },
      }),
      mkPR(9, {
        Title: "Kicked out",
        merge_queue_removal: {
          reason: "failed_checks",
          removed_at: "2026-07-10T14:40:00Z",
          failed_checks: [{ name: "e2e (chromium)", url: "https://ci.example/run/1" }],
        },
      }),
    ]);

    render(MergeQueuesPage);

    const queue = await screen.findByRole("region", { name: "acme/widgets main merge queue" });
    const rows = within(within(queue).getByRole("list", { name: "Queued pull requests" })).getAllByRole("listitem");
    expect(rows.map((row) => row.textContent?.replace(/\s+/g, " ").trim())).toEqual([
      "1 Front of the queue #11 Mona · queued 10m ago · merges in about 5 min Ready to merge",
      "2 Second in line #12 octocat · queued 5m ago Awaiting checks",
    ]);

    const removed = within(within(queue).getByRole("list", { name: "Removed pull requests" })).getByRole("listitem");
    expect(within(removed).getByText("Failed checks")).toBeTruthy();
    expect(within(removed).getByRole("link", { name: "e2e (chromium)" }).getAttribute("href")).toBe(
      "https://ci.example/run/1",
    );
    expect(
      within(queue)
        .getByRole("link", { name: /GitHub/ })
        .getAttribute("href"),
    ).toBe("https://github.com/acme/widgets/queue/main");
    expect(screen.getByText("2 queued · 1 removed across 1 queue")).toBeTruthy();
  });

  it("asks the server only for open pull requests with merge queue state, narrowed by the repository filter", async () => {
    globalRepo.current = "github|github.com/acme/widgets";
    respondWith([]);

    render(MergeQueuesPage);

    await screen.findByText("No pull requests are in a merge queue");
    expect(mockGet).toHaveBeenCalledWith(
      "/pulls",
      expect.objectContaining({
        params: { query: { state: "open", attributes: "merge_queue", repo: "github|github.com/acme/widgets" } },
      }),
    );
  });

  it("opens the pull request in Forge on a plain click", async () => {
    respondWith([mkPR(11, { Title: "Front of the queue", ...queued(1, "queued", "2026-07-10T15:00:00Z") })]);

    render(MergeQueuesPage);

    const link = await screen.findByRole("link", { name: /Front of the queue/ });
    expect(link.getAttribute("href")).toBe("/pulls/github/acme/widgets/11");
    await fireEvent.click(link);
    expect(mockNavigate).toHaveBeenCalledWith("/pulls/github/acme/widgets/11");
  });

  it("reloads when live updates report changed data", async () => {
    respondWith([mkPR(11, { Title: "Front of the queue", ...queued(1, "queued", "2026-07-10T15:00:00Z") })]);
    render(MergeQueuesPage);
    await screen.findByText("Front of the queue");

    respondWith([]);
    for (const subscriber of eventSubscribers) subscriber({ type: "data_changed" });

    await screen.findByText("No pull requests are in a merge queue");
  });

  it("keeps the last queues and reports a failed refresh", async () => {
    respondWith([mkPR(11, { Title: "Front of the queue", ...queued(1, "queued", "2026-07-10T15:00:00Z") })]);
    render(MergeQueuesPage);
    await screen.findByText("Front of the queue");

    mockGet.mockResolvedValue({ error: { status: 500, detail: "database is locked" } });
    for (const subscriber of eventSubscribers) subscriber({ type: "data_changed" });

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("database is locked"));
    expect(screen.getByText("Front of the queue")).toBeTruthy();
  });
});
