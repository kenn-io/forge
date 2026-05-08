import { beforeAll, describe, expect, it, vi } from "vitest";

beforeAll(() => {
  (globalThis as typeof globalThis & { $state: <T>(value: T) => T }).$state = <T>(value: T) => value;
});

function notification(id: number) {
  return {
    id,
    platform_host: "github.com",
    platform_thread_id: `thread-${id}`,
    repo_owner: "acme",
    repo_name: "widget",
    subject_type: "PullRequest",
    subject_title: `Notification ${id}`,
    subject_url: "",
    subject_latest_comment_url: "",
    web_url: `https://github.com/acme/widget/pull/${id}`,
    item_number: id,
    item_type: "pr",
    item_author: "octocat",
    reason: id === 1 ? "mention" : "comment",
    unread: true,
    participating: true,
    github_updated_at: "2026-05-01T10:00:00Z",
    github_last_read_at: "",
    synced_at: "2026-05-01T10:00:00Z",
    done_at: "",
    done_reason: "",
    github_read_queued_at: "",
    github_read_synced_at: "",
    github_read_error: "",
    github_read_attempts: 0,
    github_read_last_attempt_at: "",
    github_read_next_attempt_at: "",
  };
}

function summary() {
  return {
    total_active: 2,
    unread: 2,
    done: 0,
    by_reason: { mention: 1, comment: 1 },
    by_repo: { "acme/widget": 2 },
  };
}

async function createStore(options: Record<string, unknown>) {
  const mod = await import("./notifications.svelte.js");
  return mod.createNotificationsStore(options as never);
}

describe("createNotificationsStore", () => {
  it("loads unread notifications with repo and search filters", async () => {
    const get = vi.fn().mockResolvedValue({
      data: { items: [notification(1), notification(2)], summary: summary() },
    });
    const store = await createStore({
      client: { GET: get },
      getGlobalRepo: () => "acme/widget",
    });

    store.setSearchQuery(" octocat ");
    await store.loadNotifications();

    expect(get).toHaveBeenCalledWith("/notifications", {
      params: {
        query: {
          state: "unread",
          limit: 100,
          sort: "priority",
          repo: "acme/widget",
          q: "octocat",
        },
      },
    });
    expect(store.getNotifications().map((item) => item.platform_thread_id)).toEqual(["thread-1", "thread-2"]);
    expect(store.getSummary()).toEqual(summary());
  });

  it("marks only selected visible notifications as done", async () => {
    const refreshedSummary = { ...summary(), total_active: 1, unread: 1, done: 1 };
    const get = vi.fn()
      .mockResolvedValueOnce({
        data: { items: [notification(1), notification(2)], summary: summary() },
      })
      .mockResolvedValueOnce({
        data: { items: [notification(2)], summary: refreshedSummary },
      });
    const post = vi.fn().mockResolvedValue({ data: { succeeded: [1], queued: [1], failed: [] } });
    const store = await createStore({ client: { GET: get, POST: post } });

    await store.loadNotifications();
    store.toggleSelected(1);
    await store.markSelectedDone();

    expect(post).toHaveBeenCalledWith("/notifications/done", { body: { ids: [1] } });
    expect(store.getSelectedCount()).toBe(0);
    expect(store.getNotifications().map((item) => item.id)).toEqual([2]);
    expect(store.getSummary()).toEqual(refreshedSummary);
  });

  it("surfaces background sync failures discovered while polling", async () => {
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout").mockImplementation((
      (handler: TimerHandler, _timeout?: number, ...args: unknown[]) => {
        queueMicrotask(() => {
          if (typeof handler === "function") handler(...args);
        });
        return 0 as ReturnType<typeof setTimeout>;
      }
    ) as typeof setTimeout);
    try {
      const get = vi.fn()
        .mockResolvedValueOnce({
          data: {
            items: [],
            summary: summary(),
            sync: { running: true, last_error: "" },
          },
        })
        .mockResolvedValue({
          data: {
            items: [],
            summary: summary(),
            sync: { running: false, last_error: "notification API unavailable" },
          },
        });
      const post = vi.fn().mockResolvedValue({ data: {} });
      const store = await createStore({ client: { GET: get, POST: post } });

      await store.triggerSync();
      for (let i = 0; i < 20; i++) await Promise.resolve();

      expect(store.getError()).toBe("notification API unavailable");
    } finally {
      setTimeoutSpy.mockRestore();
    }
  });

  it("reloads immediately and polls after triggering inbox sync", async () => {
    const delays: number[] = [];
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout").mockImplementation((
      (handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
        delays.push(Number(timeout ?? 0));
        queueMicrotask(() => {
          if (typeof handler === "function") handler(...args);
        });
        return 0 as ReturnType<typeof setTimeout>;
      }
    ) as typeof setTimeout);
    try {
      const get = vi.fn().mockResolvedValue({
        data: { items: [notification(1)], summary: summary() },
      });
      const post = vi.fn().mockResolvedValue({ data: {} });
      const store = await createStore({ client: { GET: get, POST: post } });

      await store.triggerSync();
      for (let i = 0; i < 100; i++) await Promise.resolve();

      expect(post).toHaveBeenCalledWith("/notifications/sync", {
        headers: { "Content-Type": "application/json" },
      });
      expect(get).toHaveBeenCalledTimes(5);
      expect(delays).toEqual([500, 1_500, 3_000, 5_000]);
    } finally {
      setTimeoutSpy.mockRestore();
    }
  });
});
