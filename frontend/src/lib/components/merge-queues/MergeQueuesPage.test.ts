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
  replaceUrl: (path: string) => mockNavigate(path),
  withBasePath: (path: string) => path,
}));

vi.mock("../../stores/filter.svelte.js", () => ({
  getGlobalRepo: () => globalRepo.current,
}));

vi.mock("../../views/PRListView.svelte", async () => ({
  default: (await import("./MergeQueuesPageTestListView.svelte")).default,
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
    repo_owner: "acme",
    repo_name: "widgets",
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

const fixture = () => [
  mkPR(12, { Title: "Second in line", ...queued(2, "awaiting_checks", "2026-07-10T15:05:00Z") }),
  mkPR(11, {
    Title: "Front of the queue",
    Author: "mona",
    AuthorDisplayName: "Mona",
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
];

function respondWith(pulls: PullRequest[]): void {
  mockGet.mockImplementation((path: string) =>
    Promise.resolve(path === "/pulls" ? { data: pulls } : { error: { status: 404 } }),
  );
}

function rowTitles(): string[] {
  return screen
    .queryAllByRole("link")
    .map((link) => link.querySelector(".merge-queue__pr-title")?.textContent ?? "")
    .filter(Boolean);
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
    localStorage.clear();
  });

  afterEach(async () => {
    cleanup();
    if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
    runtimeCapture.current = undefined;
    vi.useRealTimers();
  });

  it("shows each queue's pull requests in queue order with their state, and removals with the failed checks", async () => {
    respondWith(fixture());

    render(MergeQueuesPage);

    const queue = await screen.findByRole("region", { name: "acme/widgets main merge queue" });
    const rows = within(within(queue).getByRole("list", { name: "Queued pull requests" })).getAllByRole("link");
    expect(rows.map((row) => row.textContent?.replace(/\s+/g, " ").trim())).toEqual([
      "1 Front of the queue #11 Ready to merge Mona · 10m ago · merges in about 5 min",
      "2 Second in line #12 Awaiting checks octocat · 5m ago",
    ]);
    const removed = within(within(queue).getByRole("list", { name: "Removed pull requests" })).getByRole("link");
    expect(removed.textContent).toContain("Failed checks");
    expect(removed.textContent).toContain("e2e (chromium)");
    expect(within(queue).getByRole("link", { name: "Open this merge queue on GitHub" }).getAttribute("href")).toBe(
      "https://github.com/acme/widgets/queue/main",
    );
    expect(screen.getByText("2 queued · 1 removed")).toBeTruthy();
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

  it("opens the pull request beside the queues on a plain click, keeping the files tab", async () => {
    respondWith(fixture());

    render(MergeQueuesPage, { props: { detailTab: "files" } });

    const link = await screen.findByRole("link", { name: /Front of the queue/ });
    expect(link.getAttribute("href")).toBe("/queues/github/acme/widgets/11");
    await fireEvent.click(link);
    expect(mockNavigate).toHaveBeenCalledWith("/queues/github/acme/widgets/11/files");
  });

  it("marks the selected pull request's row", async () => {
    respondWith(fixture());

    render(MergeQueuesPage, {
      props: {
        selectedPR: {
          provider: "github",
          platformHost: "github.com",
          owner: "acme",
          name: "widgets",
          repoPath: "acme/widgets",
          number: 12,
        },
      },
    });

    const selected = await screen.findByRole("link", { current: true });
    expect(selected.textContent).toContain("Second in line");
  });

  it("narrows the queues by search words across title, number, author, and repository", async () => {
    respondWith(fixture());
    render(MergeQueuesPage);
    await screen.findByText("Front of the queue");
    const search = screen.getByRole("searchbox", { name: "Search merge queues" });

    await fireEvent.input(search, { target: { value: "mona front" } });
    expect(rowTitles()).toEqual(["Front of the queue"]);
    expect(screen.getByText("1 queued · 1 of 3 shown")).toBeTruthy();

    await fireEvent.input(search, { target: { value: "#9" } });
    expect(rowTitles()).toEqual(["Kicked out"]);

    await fireEvent.input(search, { target: { value: "nothing matches this" } });
    await fireEvent.click(screen.getByRole("button", { name: "Clear search and filters" }));
    expect(rowTitles()).toEqual(["Front of the queue", "Second in line", "Kicked out"]);
  });

  it("filters by status and author from the values present, and remembers the choice", async () => {
    respondWith(fixture());
    const first = render(MergeQueuesPage);
    await screen.findByText("Front of the queue");

    await fireEvent.click(screen.getByRole("button", { name: "Status" }));
    await fireEvent.click(await screen.findByRole("button", { name: /^Removed/ }));
    expect(rowTitles()).toEqual(["Kicked out"]);

    await fireEvent.click(screen.getByRole("button", { name: /^Awaiting checks/ }));
    expect(rowTitles()).toEqual(["Second in line", "Kicked out"]);

    first.unmount();
    render(MergeQueuesPage);
    await screen.findByText("Second in line");
    expect(rowTitles()).toEqual(["Second in line", "Kicked out"]);

    await fireEvent.click(screen.getByRole("button", { name: "Author" }));
    await fireEvent.click(await screen.findByRole("button", { name: /^Mona/ }));
    await waitFor(() => expect(screen.getByText("No pull requests match")).toBeTruthy());
  });

  it("reloads when live updates report changed data", async () => {
    respondWith(fixture());
    render(MergeQueuesPage);
    await screen.findByText("Front of the queue");

    respondWith([]);
    for (const subscriber of eventSubscribers) subscriber({ type: "data_changed" });

    await screen.findByText("No pull requests are in a merge queue");
  });

  it("keeps the last queues and reports a failed refresh", async () => {
    respondWith(fixture());
    render(MergeQueuesPage);
    await screen.findByText("Front of the queue");

    mockGet.mockResolvedValue({ error: { status: 500, detail: "database is locked" } });
    for (const subscriber of eventSubscribers) subscriber({ type: "data_changed" });

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("database is locked"));
    expect(screen.getByText("Front of the queue")).toBeTruthy();
  });

  it.each([
    ["no pull request is queued", [] as PullRequest[], "", "No pull requests are in a merge queue"],
    ["the search matches nothing", fixture(), "nothing matches this", "No pull requests match"],
  ])("reports a failed refresh when %s", async (_, pulls, query, emptyText) => {
    respondWith(pulls);
    render(MergeQueuesPage);
    await fireEvent.input(screen.getByRole("searchbox", { name: "Search merge queues" }), { target: { value: query } });
    await screen.findByText(emptyText);

    mockGet.mockResolvedValue({ error: { status: 500, detail: "database is locked" } });
    for (const subscriber of eventSubscribers) subscriber({ type: "data_changed" });

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("database is locked"));
    expect(screen.getByText(emptyText)).toBeTruthy();
  });

  it("keeps updating after the repository filter changes while a read is in flight", async () => {
    // The first read waits until aborted, as fetch does.
    let firstSignal: AbortSignal | undefined;
    mockGet.mockImplementationOnce((_path: string, options: { signal?: AbortSignal }) => {
      firstSignal = options.signal;
      return new Promise((_, reject) =>
        options.signal?.addEventListener("abort", () => reject(options.signal?.reason)),
      );
    });
    render(MergeQueuesPage);
    await waitFor(() => expect(mockGet).toHaveBeenCalledTimes(1));

    // A read for another repository replaces the one the first poll started.
    globalRepo.current = "github|github.com/acme/widgets";
    respondWith([]);
    for (const subscriber of eventSubscribers) subscriber({ type: "data_changed" });
    await screen.findByText("No pull requests are in a merge queue");
    await waitFor(() => expect(firstSignal?.aborted).toBe(true));
    await new Promise((resolve) => setTimeout(resolve, 0));

    respondWith(fixture());
    expect(eventSubscribers.size).toBe(1);
    for (const subscriber of eventSubscribers) subscriber({ type: "data_changed" });
    await screen.findByText("Front of the queue");
  });
});
