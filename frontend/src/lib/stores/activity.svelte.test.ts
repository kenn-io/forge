import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { GeneratedClient } from "../api/generated-api.js";
import type { OwnedAppRuntime } from "../app/runtime.js";
import type { ActivityItem, ActivitySettings } from "../api/types.js";
import { makeTestAppRuntime } from "../testing/effect-layers.js";
import {
  buildActivityItemTypeFilter,
  buildActivityFilterTypes,
  createActivityStore as createRuntimeActivityStore,
  type ActivityStoreOptions,
  DEFAULT_ACTIVITY_ITEM_TYPES,
  DEFAULT_EVENT_TYPES,
  isActivityItemTypeEnabled,
  notificationDbId,
} from "./activity.svelte.js";
import { dismissFlash, getFlash, getFlashes } from "./flash.svelte.js";

let runtime: OwnedAppRuntime | undefined;

const fakeClient = {
  GET: async () => ({
    data: { items: [], capped: false },
    error: null,
  }),
} as unknown as GeneratedClient;

type TestActivityStoreOptions = Omit<ActivityStoreOptions, "runtime"> & { readonly client: GeneratedClient };

function createActivityStore(options: TestActivityStoreOptions) {
  const { client, ...storeOptions } = options;
  runtime = makeTestAppRuntime(client);
  return createRuntimeActivityStore({ ...storeOptions, runtime });
}

function settings(collapse: boolean): ActivitySettings {
  return {
    view_mode: "threaded",
    time_range: "7d",
    hide_closed: false,
    hide_bots: false,
    collapse_threads: collapse,
    default_branch_retention_days: 90,
    default_branch_max_commits: 5000,
  };
}

function makeStore() {
  return createActivityStore({ client: fakeClient });
}

beforeEach(() => {
  runtime = undefined;
  window.history.replaceState(null, "", "/");
});

afterEach(async () => {
  for (const item of getFlashes()) dismissFlash(item.id);
  if (runtime !== undefined) await Effect.runPromise(runtime.disposeEffect);
});

describe("activity store collapse state", () => {
  it("treats threads as expanded when the collapse default is false", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(false));
    expect(s.getCollapseThreads()).toBe(false);
    expect(s.isThreadItemExpanded("k1")).toBe(true);
  });

  it("collapseAllThreads collapses everything and clears overrides", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(false));
    s.toggleThreadItem("k1");
    expect(s.isThreadItemExpanded("k1")).toBe(false);
    s.collapseAllThreads();
    expect(s.getCollapseThreads()).toBe(true);
    expect(s.isThreadItemExpanded("k1")).toBe(false);
    expect(s.isThreadItemExpanded("k2")).toBe(false);
  });

  it("toggleThreadItem expands a single item when globally collapsed", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(true));
    expect(s.isThreadItemExpanded("k1")).toBe(false);
    s.toggleThreadItem("k1");
    expect(s.isThreadItemExpanded("k1")).toBe(true);
    expect(s.isThreadItemExpanded("k2")).toBe(false);
  });

  it("toggleThreadItem twice returns an item to the global state", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(false));
    s.toggleThreadItem("k1");
    expect(s.isThreadItemExpanded("k1")).toBe(false);
    s.toggleThreadItem("k1");
    expect(s.isThreadItemExpanded("k1")).toBe(true);
  });

  it("writes collapsed to the URL only when it differs from the server default", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(false));
    s.collapseAllThreads();
    expect(new URLSearchParams(window.location.search).get("collapsed")).toBe("1");
    s.expandAllThreads();
    expect(new URLSearchParams(window.location.search).has("collapsed")).toBe(false);
  });

  it("writes collapsed=0 when expanding against a collapsed server default", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(true));
    s.expandAllThreads();
    expect(new URLSearchParams(window.location.search).get("collapsed")).toBe("0");
    s.collapseAllThreads();
    expect(new URLSearchParams(window.location.search).has("collapsed")).toBe(false);
  });

  it("applies collapsed=0 from the URL over a collapsed server default", () => {
    window.history.replaceState(null, "", "/?collapsed=0");
    const s = makeStore();
    s.hydrateDefaults(settings(true));
    s.initializeFromMount();
    expect(s.getCollapseThreads()).toBe(false);
  });

  it("preserves a live collapsed override when settings reload after init", () => {
    window.history.replaceState(null, "", "/?collapsed=0");
    const s = makeStore();
    s.hydrateDefaults(settings(true));
    s.initializeFromMount();
    expect(s.getCollapseThreads()).toBe(false);
    s.hydrateDefaults(settings(true));
    expect(s.getCollapseThreads()).toBe(false);
  });

  it("clears a redundant collapsed param when the default catches up to it", () => {
    window.history.replaceState(null, "", "/?collapsed=1");
    const s = makeStore();
    s.hydrateDefaults(settings(false));
    s.initializeFromMount();
    expect(s.getCollapseThreads()).toBe(true);
    expect(new URLSearchParams(window.location.search).get("collapsed")).toBe("1");

    // The server default changes to match the live override; the now-redundant
    // param is dropped so a later default change is not shadowed by it.
    s.hydrateDefaults(settings(true));
    expect(s.getCollapseThreads()).toBe(true);
    expect(new URLSearchParams(window.location.search).has("collapsed")).toBe(false);

    s.hydrateDefaults(settings(false));
    expect(s.getCollapseThreads()).toBe(false);
  });
});

describe("buildActivityFilterTypes", () => {
  const allItemTypes = new Set(DEFAULT_ACTIVITY_ITEM_TYPES);
  const allEvents = new Set<string>(DEFAULT_EVENT_TYPES);

  it("returns no filter when everything is selected", () => {
    expect(buildActivityFilterTypes(allItemTypes, allEvents, false)).toEqual([]);
  });

  it("drops default-branch commits when the commit event is deselected", () => {
    const enabled = new Set(["comment", "review", "force_push"]);
    expect(buildActivityFilterTypes(allItemTypes, enabled, false)).toEqual([
      "new_pr",
      "new_issue",
      "default_branch_force_push",
      "comment",
      "review",
      "force_push",
      "notification",
    ]);
  });

  it("drops default-branch force pushes when the force-push event is deselected", () => {
    const enabled = new Set(["comment", "review", "commit"]);
    expect(buildActivityFilterTypes(allItemTypes, enabled, false)).toEqual([
      "new_pr",
      "new_issue",
      "default_branch_commit",
      "comment",
      "review",
      "commit",
      "notification",
    ]);
  });

  it("excludes all default-branch activity when it is hidden", () => {
    expect(buildActivityFilterTypes(allItemTypes, allEvents, true)).toEqual([
      "new_pr",
      "new_issue",
      "comment",
      "review",
      "commit",
      "force_push",
      "notification",
    ]);
  });

  it("independently controls PR and issue opening events", () => {
    expect(buildActivityFilterTypes(new Set(["issue"]), allEvents, false)).toEqual([
      "new_issue",
      "default_branch_commit",
      "default_branch_force_push",
      "comment",
      "review",
      "commit",
      "force_push",
      "notification",
    ]);
  });

  it("keeps the all-selected shortcut only while notifications stay on", () => {
    expect(buildActivityFilterTypes(allItemTypes, allEvents, false, true)).toEqual([]);
  });

  it("builds an explicit list omitting notifications when they are hidden", () => {
    expect(buildActivityFilterTypes(allItemTypes, allEvents, false, false)).toEqual([
      "new_pr",
      "new_issue",
      "default_branch_commit",
      "default_branch_force_push",
      "comment",
      "review",
      "commit",
      "force_push",
    ]);
  });

  it("preserves notification-only filtering for the default item scope", () => {
    expect(buildActivityFilterTypes(allItemTypes, new Set(), false, true)).toEqual(["notification"]);
  });

  it("supports repository-level commits with both item types hidden", () => {
    expect(buildActivityFilterTypes(new Set(), new Set(["commit"]), false, false)).toEqual([
      "none",
      "default_branch_commit",
      "commit",
    ]);
  });

  it("marks an empty item scope when notifications remain enabled", () => {
    expect(buildActivityFilterTypes(new Set(), new Set(), true, true)).toEqual(["none", "notification"]);
  });

  it("encodes a fully empty selection as an explicit nonmatching filter", () => {
    expect(buildActivityFilterTypes(new Set(), new Set(), true, false)).toEqual(["none"]);
  });
});

describe("buildActivityItemTypeFilter", () => {
  it("keeps repository activity eligible while filtering item-scoped rows before the cap", () => {
    expect(buildActivityItemTypeFilter(new Set(["issue"]))).toEqual(["issue", "repo"]);
    expect(buildActivityItemTypeFilter(new Set())).toEqual(["repo"]);
  });
});

describe("isActivityItemTypeEnabled", () => {
  it("filters PR and issue rows without filtering repository-level rows", () => {
    const issuesOnly = new Set<"pr" | "issue">(["issue"]);
    expect(isActivityItemTypeEnabled("pr", issuesOnly)).toBe(false);
    expect(isActivityItemTypeEnabled("issue", issuesOnly)).toBe(true);
    expect(isActivityItemTypeEnabled("", new Set())).toBe(true);
  });
});

describe("activity store URL hydration", () => {
  it("normalizes legacy URLs that kept default-branch commits while commit was deselected", () => {
    window.history.replaceState(
      null,
      "",
      "/?types=new_pr,new_issue,default_branch_commit,default_branch_force_push,comment,review,force_push",
    );
    const s = makeStore();
    s.initializeFromMount();
    // Notifications default on, so a legacy filtered URL (no notif=0)
    // gains the notification type on hydration.
    expect(s.getActivityFilterTypes()).toEqual([
      "new_pr",
      "new_issue",
      "default_branch_force_push",
      "comment",
      "review",
      "force_push",
      "notification",
    ]);
    expect(new URLSearchParams(window.location.search).get("types")).toBe(
      "new_pr,new_issue,default_branch_force_push,comment,review,force_push,notification",
    );
  });

  it("normalizes a legacy all-selected types list back to no filter", () => {
    window.history.replaceState(
      null,
      "",
      "/?types=new_pr,new_issue,default_branch_commit,default_branch_force_push,comment,review,commit,force_push",
    );
    const s = makeStore();
    s.initializeFromMount();
    expect(s.getActivityFilterTypes()).toEqual([]);
    expect(new URLSearchParams(window.location.search).has("types")).toBe(false);
  });

  it.each([
    {
      name: "PRs only",
      types: "new_pr,comment",
      expected: ["pr"],
      normalized: "new_pr,comment,notification",
    },
    {
      name: "issues only",
      types: "new_issue,comment",
      expected: ["issue"],
      normalized: "new_issue,comment,notification",
    },
    {
      name: "neither item type",
      types: "default_branch_commit,commit",
      expected: [],
      normalized: "none,default_branch_commit,commit,notification",
    },
  ])("hydrates $name from the types parameter", ({ types, expected, normalized }) => {
    window.history.replaceState(null, "", `/?types=${types}`);
    const s = makeStore();
    s.initializeFromMount();
    expect([...s.getEnabledItemTypes()]).toEqual(expected);
    expect(new URLSearchParams(window.location.search).get("types")).toBe(normalized);
  });

  it("hydrates notification-only URLs with the default item scope", () => {
    window.history.replaceState(null, "", "/?types=notification");
    const s = makeStore();
    s.initializeFromMount();

    expect([...s.getEnabledItemTypes()]).toEqual(DEFAULT_ACTIVITY_ITEM_TYPES);
    expect([...s.getEnabledEvents()]).toEqual([]);
    expect(s.getActivityFilterTypes()).toEqual(["notification"]);
    expect(new URLSearchParams(window.location.search).get("types")).toBe("notification");
  });

  it("round trips a fully empty selection without restoring defaults", () => {
    window.history.replaceState(null, "", "/?types=none&notif=0&hide_branch=1");
    const s = makeStore();
    s.initializeFromMount();

    expect([...s.getEnabledItemTypes()]).toEqual([]);
    expect([...s.getEnabledEvents()]).toEqual([]);
    expect(s.getShowNotifications()).toBe(false);
    expect(s.getHideDefaultBranchActivity()).toBe(true);
    expect(s.getActivityFilterTypes()).toEqual(["none"]);
    expect(new URLSearchParams(window.location.search).get("types")).toBe("none");
  });
});

describe("activity store notification visibility", () => {
  it("shows notifications by default and persists hiding them via the notif param", () => {
    const s = makeStore();
    s.initializeFromMount();
    expect(s.getShowNotifications()).toBe(true);
    // Default-on, all-selected: no explicit type filter at all.
    expect(s.getActivityFilterTypes()).toEqual([]);
    expect(new URLSearchParams(window.location.search).has("notif")).toBe(false);

    s.setShowNotifications(false);
    s.setActivityFilterTypes(
      buildActivityFilterTypes(s.getEnabledItemTypes(), s.getEnabledEvents(), s.getHideDefaultBranchActivity(), false),
    );
    s.syncToURL();
    expect(new URLSearchParams(window.location.search).get("notif")).toBe("0");
    // Hiding notifications sends the explicit non-notification list.
    expect(s.getActivityFilterTypes()).not.toContain("notification");

    const next = makeStore();
    next.initializeFromMount();
    expect(next.getShowNotifications()).toBe(false);
    expect(next.getActivityFilterTypes()).not.toContain("notification");
  });
});

describe("activity store default-branch visibility", () => {
  it("shows default-branch activity by default and persists the hide flag", () => {
    const s = makeStore();
    s.initializeFromMount();
    expect(s.getHideDefaultBranchActivity()).toBe(false);

    s.setHideDefaultBranchActivity(true);
    s.syncToURL();
    expect(new URLSearchParams(window.location.search).get("hide_branch")).toBe("1");

    const next = makeStore();
    next.initializeFromMount();
    expect(next.getHideDefaultBranchActivity()).toBe(true);

    next.setHideDefaultBranchActivity(false);
    next.syncToURL();
    expect(new URLSearchParams(window.location.search).has("hide_branch")).toBe(false);
  });
});

describe("activity store commit roll-up", () => {
  it("shows individual commits by default and persists the URL override for rolled-up commits", () => {
    const s = makeStore();
    s.initializeFromMount();
    expect(s.getRollUpCommits()).toBe(false);

    s.setRollUpCommits(true);
    s.syncToURL();
    expect(new URLSearchParams(window.location.search).get("rollup_commits")).toBe("1");

    const next = makeStore();
    next.initializeFromMount();
    expect(next.getRollUpCommits()).toBe(true);

    next.setRollUpCommits(false);
    next.syncToURL();
    expect(new URLSearchParams(window.location.search).has("rollup_commits")).toBe(false);
  });
});

function notificationItem(id: string, state: "unread" | "read"): ActivityItem {
  return {
    id,
    cursor: id,
    activity_type: "notification",
    item_type: "pr",
    item_number: 1,
    item_state: state,
    body_preview: "review_requested",
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
  } as unknown as ActivityItem;
}

describe("notificationDbId", () => {
  it("parses ntf-source ids and rejects everything else", () => {
    expect(notificationDbId("ntf:42")).toBe(42);
    expect(notificationDbId("pr:1")).toBeNull();
    expect(notificationDbId("ntf:0")).toBeNull();
    expect(notificationDbId("ntf:-3")).toBeNull();
    expect(notificationDbId("ntf:abc")).toBeNull();
    expect(notificationDbId("ntf:")).toBeNull();
  });
});

describe("activity store markNotificationSeen", () => {
  function storeWith(post: ReturnType<typeof vi.fn>) {
    const client = {
      GET: async () => ({ data: { items: [notificationItem("ntf:42", "unread")], capped: false }, error: null }),
      POST: post,
    } as unknown as GeneratedClient;
    return createActivityStore({ client });
  }

  it("flips the row to read and queues the upstream GitHub read", async () => {
    const post = vi.fn(async () => ({ data: { queued: [42], succeeded: [], failed: [] }, error: null }));
    const s = storeWith(post);
    s.loadActivity();
    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));
    expect(s.getActivityItems()[0]!.item_state).toBe("unread");

    const result = s.markNotificationSeen(s.getActivityItems()[0]!);

    expect(result).toBeUndefined();
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post).toHaveBeenCalledWith("/notifications/read", {
      body: { ids: [42] },
      signal: expect.any(AbortSignal),
    });
    expect(s.getActivityItems()[0]!.item_state).toBe("read");
  });

  it("rolls back the optimistic flip when the request fails", async () => {
    const post = vi.fn(async () => ({ error: { detail: "boom" }, response: new Response(null, { status: 500 }) }));
    const s = storeWith(post);
    s.loadActivity();
    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));

    const result = s.markNotificationSeen(s.getActivityItems()[0]!);

    expect(result).toBeUndefined();
    await vi.waitFor(() => expect(s.getActivityItems()[0]!.item_state).toBe("unread"));
    expect(s.getActivityItems()[0]!.item_state).toBe("unread");
    expect(getFlash()).toMatchObject({ message: "boom", tone: "danger" });
  });

  it("rolls back when the bulk response reports the id as failed despite a 200", async () => {
    const post = vi.fn(async () => ({
      data: { succeeded: [], queued: [], failed: [{ id: 42, error: "not found" }] },
      error: null,
    }));
    const s = storeWith(post);
    s.loadActivity();
    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));

    const result = s.markNotificationSeen(s.getActivityItems()[0]!);

    expect(result).toBeUndefined();
    await vi.waitFor(() => expect(s.getActivityItems()[0]!.item_state).toBe("unread"));
    expect(s.getActivityItems()[0]!.item_state).toBe("unread");
    expect(getFlash()).toMatchObject({ message: "Failed to mark notification as read.", tone: "danger" });
  });

  it("does not let an older failed acknowledgement roll back a newer read", async () => {
    const first = Promise.withResolvers<{
      error: { detail: string };
      response: Response;
    }>();
    const post = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockResolvedValue({ data: { queued: [42], succeeded: [], failed: [] }, error: null });
    const s = storeWith(post);
    s.loadActivity();
    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));

    s.markNotificationSeen(s.getActivityItems()[0]!);
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    s.markNotificationSeen(s.getActivityItems()[0]!);
    first.resolve({ error: { detail: "boom" }, response: new Response(null, { status: 500 }) });

    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    await vi.waitFor(() => expect(s.getActivityItems()[0]!.item_state).toBe("read"));
  });

  it("does not let an activity read started before acknowledgement restore unread state", async () => {
    const olderRead = Promise.withResolvers<{
      data: { items: ActivityItem[]; capped: boolean };
      error: null;
    }>();
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: { items: [notificationItem("ntf:42", "unread")], capped: false }, error: null })
      .mockReturnValueOnce(olderRead.promise);
    const post = vi.fn().mockResolvedValue({
      data: { queued: [42], succeeded: [], failed: [] },
      error: null,
    });
    const s = createActivityStore({ client: { GET: get, POST: post } as unknown as GeneratedClient });
    s.loadActivity();
    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));

    s.loadActivity();
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    s.markNotificationSeen(s.getActivityItems()[0]!);
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    olderRead.resolve({ data: { items: [notificationItem("ntf:42", "unread")], capped: false }, error: null });

    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));
    expect(s.getActivityItems()[0]!.item_state).toBe("read");
  });

  it("keeps the acknowledgement authoritative over a read started while the mutation is pending", async () => {
    const pendingRead = Promise.withResolvers<{
      data: { items: ActivityItem[]; capped: boolean };
      error: null;
    }>();
    const acknowledgement = Promise.withResolvers<{
      data: { queued: number[]; succeeded: number[]; failed: never[] };
      error: null;
    }>();
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: { items: [notificationItem("ntf:42", "unread")], capped: false }, error: null })
      .mockReturnValueOnce(pendingRead.promise);
    const post = vi.fn(() => acknowledgement.promise);
    const s = createActivityStore({ client: { GET: get, POST: post } as unknown as GeneratedClient });
    s.loadActivity();
    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));

    s.markNotificationSeen(s.getActivityItems()[0]!);
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    s.loadActivity();
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    acknowledgement.resolve({ data: { queued: [42], succeeded: [], failed: [] }, error: null });
    await vi.waitFor(() => expect(s.getActivityItems()[0]!.item_state).toBe("read"));
    pendingRead.resolve({ data: { items: [notificationItem("ntf:42", "unread")], capped: false }, error: null });

    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));
    expect(s.getActivityItems()[0]!.item_state).toBe("read");
  });

  it("ignores rows that are not notification feed rows", async () => {
    const post = vi.fn(async () => ({ data: { queued: [], succeeded: [], failed: [] }, error: null }));
    const s = storeWith(post);
    s.loadActivity();
    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));

    const result = s.markNotificationSeen({ ...s.getActivityItems()[0]!, id: "pr:7" });

    expect(result).toBeUndefined();
    expect(post).not.toHaveBeenCalled();
  });
});

describe("activity polling recovery", () => {
  it("does not project a poll started before a newer foreground search", async () => {
    const pendingPoll = Promise.withResolvers<{
      data: { items: ActivityItem[]; capped: boolean };
      error: null;
    }>();
    const pollReturned = Promise.withResolvers<void>();
    const now = new Date().toISOString();
    const initial = { ...notificationItem("ntf:1", "unread"), created_at: now };
    const stalePollItem = { ...notificationItem("ntf:2", "unread"), created_at: now };
    const foregroundItem = { ...notificationItem("ntf:3", "unread"), created_at: now };
    let calls = 0;
    const client = {
      GET: vi.fn(async () => {
        calls += 1;
        if (calls === 1) return { data: { items: [initial], capped: false }, error: null };
        if (calls === 2) {
          const response = await pendingPoll.promise;
          pollReturned.resolve();
          return response;
        }
        if (calls === 3) return { data: { items: [foregroundItem], capped: false }, error: null };
        throw new Error(`unexpected activity request ${calls}`);
      }),
    } as unknown as GeneratedClient;
    const store = createActivityStore({ client });
    store.loadActivity();
    await vi.waitFor(() => expect(store.getActivityItems().map((item) => item.id)).toEqual(["ntf:1"]));

    store.startActivityPolling();
    await vi.waitFor(() => expect(calls).toBe(2));
    store.setActivitySearch("new selection");
    store.loadActivity();
    await vi.waitFor(() => expect(store.getActivityItems().map((item) => item.id)).toEqual(["ntf:3"]));

    pendingPoll.resolve({ data: { items: [stalePollItem], capped: false }, error: null });
    await pollReturned.promise;
    await new Promise<void>((resolve) => setTimeout(resolve, 0));

    expect(store.getActivityItems().map((item) => item.id)).toEqual(["ntf:3"]);
  });

  it("clears loading after an empty-feed poll reload fails", async () => {
    let calls = 0;
    const client = {
      GET: vi.fn(async () => {
        calls += 1;
        if (calls === 1) return { data: { items: [], capped: false } };
        return {
          error: {
            code: "validationError",
            detail: "activity unavailable",
            title: "Invalid request",
            type: "about:blank",
          },
          response: new Response(null, { status: 400 }),
        };
      }),
    } as unknown as GeneratedClient;
    const store = createActivityStore({ client });
    store.loadActivity();
    await vi.waitFor(() => expect(store.isActivityLoading()).toBe(false));

    store.startActivityPolling();
    await vi.waitFor(() => expect(calls).toBe(2));

    expect(store.isActivityLoading()).toBe(false);
  });

  it("clears loading after a capped poll reload fails", async () => {
    let calls = 0;
    const client = {
      GET: vi.fn(async () => {
        calls += 1;
        if (calls === 1) return { data: { items: [notificationItem("ntf:42", "unread")], capped: false } };
        if (calls === 2) return { data: { items: [], capped: true } };
        return {
          error: {
            code: "validationError",
            detail: "activity unavailable",
            title: "Invalid request",
            type: "about:blank",
          },
          response: new Response(null, { status: 400 }),
        };
      }),
    } as unknown as GeneratedClient;
    const store = createActivityStore({ client });
    store.loadActivity();
    await vi.waitFor(() => expect(store.isActivityLoading()).toBe(false));

    store.startActivityPolling();
    await vi.waitFor(() => expect(calls).toBe(3));

    expect(store.isActivityLoading()).toBe(false);
  });
});
