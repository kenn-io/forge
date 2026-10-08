import { Effect } from "effect";
import { expect, it, vi } from "vite-plus/test";
import type { GeneratedClient } from "../api/generated-api.js";
import type { ActivityItem } from "../api/types.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import { createActivityStore } from "./activity.svelte.js";
import { dismissFlash, getFlashes } from "./flash.svelte.js";

it("does not reuse another Activity filter's cached rows when changing project", async () => {
  localStorage.clear();
  let repo: string | undefined;
  let hold = false;
  const pending = Promise.withResolvers<void>();
  let pendingReads = 0;
  const client = {
    GET: async (path: string) => {
      if (path === "/activity/authors") return { data: { authors: [] }, error: null };
      if (hold) {
        pendingReads += 1;
        await pending.promise;
        return { data: { items: [], item_activity: [], capped: false }, error: null };
      }
      return {
        data: {
          items: [],
          item_activity: [
            {
              activity_at: new Date().toISOString(),
              item_type: "pr",
              item_number: 42,
              item_title: "Cached global subject",
              item_state: "open",
              platform_host: "github.com",
              repo_owner: "acme",
              repo_name: "widgets",
              repo: {
                provider: "github",
                platform_host: "github.com",
                repo_path: "acme/widgets",
                owner: "acme",
                name: "widgets",
                platform_repo_id: 1001,
              },
            },
          ],
          capped: false,
        },
        error: null,
      };
    },
  } as unknown as GeneratedClient;
  const runtime = makeTestAppRuntime(client);
  const store = createActivityStore({ runtime, getGlobalRepo: () => repo });
  try {
    store.loadActivity();
    await vi.waitFor(() => expect(store.getItemActivity()[0]?.item_title).toBe("Cached global subject"));
    hold = true;
    store.setActivityAuthor("bob");
    repo = "github|github.com/acme/widgets";
    store.loadActivity();
    await vi.waitFor(() => expect(pendingReads).toBe(1));
    expect(store.getItemActivity()).toEqual([]);
    pending.resolve();
    await vi.waitFor(() => expect(store.isActivityLoading()).toBe(false));
    expect(store.getItemActivity()).toEqual([]);
  } finally {
    pending.resolve();
    await Effect.runPromise(runtime.disposeEffect);
  }
});

it.each([true, false])(
  "keeps cached notification state consistent when marking read is accepted: %s",
  async (accepted) => {
    localStorage.clear();
    let repo: string | undefined;
    let hold = false;
    const reads = Promise.withResolvers<void>();
    const mutation = Promise.withResolvers<void>();
    const notification: ActivityItem = {
      id: "ntf:42",
      cursor: "notification-cursor",
      activity_type: "notification",
      author: "alice",
      body_preview: "review_requested",
      created_at: new Date().toISOString(),
      item_number: 42,
      item_state: "unread",
      item_title: "Review requested",
      item_type: "pr",
      item_url: "https://github.com/acme/widgets/pull/42",
      platform_host: "github.com",
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        repo_path: "acme/widgets",
        owner: "acme",
        name: "widgets",
        platform_repo_id: 1001,
      },
    };
    const post = vi.fn(async () => {
      await mutation.promise;
      return {
        data: { queued: accepted ? [42] : [], succeeded: [], failed: accepted ? [] : [{ id: 42, error: "not found" }] },
        error: null,
      };
    });
    const runtime = makeTestAppRuntime({
      GET: async (path: string) => {
        if (path === "/activity/authors") return { data: { authors: [] }, error: null };
        if (hold) await reads.promise;
        return { data: { items: [notification], capped: false }, error: null };
      },
      POST: post,
    } as unknown as GeneratedClient);
    const store = createActivityStore({ runtime, getGlobalRepo: () => repo });
    try {
      store.loadActivity();
      await vi.waitFor(() => expect(store.getActivityItems()[0]?.item_state).toBe("unread"));
      repo = "github|github.com/acme/widgets";
      store.loadActivity();
      await vi.waitFor(() => expect(store.isActivityLoading()).toBe(false));
      hold = true;
      store.markNotificationSeen(store.getActivityItems()[0]!);
      await vi.waitFor(() => expect(post).toHaveBeenCalledOnce());
      expect(store.getActivityItems()[0]?.item_state).toBe("read");
      repo = undefined;
      store.loadActivity();
      expect(store.getActivityItems()[0]?.item_state).toBe("read");
      mutation.resolve();
      if (!accepted) await vi.waitFor(() => expect(store.getActivityItems()[0]?.item_state).toBe("unread"));
      repo = "github|github.com/acme/widgets";
      store.loadActivity();
      expect(store.getActivityItems()[0]?.item_state).toBe(accepted ? "read" : "unread");
    } finally {
      reads.resolve();
      mutation.resolve();
      await Effect.runPromise(runtime.disposeEffect);
      for (const flash of getFlashes()) dismissFlash(flash.id);
    }
  },
);
