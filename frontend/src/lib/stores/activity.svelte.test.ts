import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { GeneratedClient } from "../api/generated-api.js";
import type { OwnedAppRuntime } from "../app/runtime.js";
import type { ActivityItem, ActivitySettings, WorkspaceActivitySubject } from "../api/types.js";
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
import { involvesMeFilterStorageKey } from "./involves-me-filter.js";

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

function workspaceActivity(itemNumber: number): WorkspaceActivitySubject {
  return {
    activity_at: `2026-08-09T12:00:0${itemNumber}Z`,
    item_number: itemNumber,
    item_state: "open",
    item_title: `PR ${itemNumber}`,
    item_type: "pr",
    item_url: `https://github.com/acme/widgets/pull/${itemNumber}`,
    platform_host: "github.com",
    repo: { host: "github.com", owner: "acme", name: "widgets" },
    repo_name: "widgets",
    repo_owner: "acme",
    workspace: { id: `workspace-${itemNumber}`, status: "ready" },
  };
}

beforeEach(() => {
  runtime = undefined;
  localStorage.clear();
  window.history.replaceState(null, "", "/");
});

afterEach(async () => {
  for (const item of getFlashes()) dismissFlash(item.id);
  if (runtime !== undefined) await Effect.runPromise(runtime.disposeEffect);
});

describe("activity store workspace activity", () => {
  it("persists and sends the Involves me filter", async () => {
    const get = vi.fn(async () => ({ data: { items: [], capped: false }, error: null }));
    const store = createActivityStore({ client: { GET: get } as unknown as GeneratedClient });

    store.setInvolvesMe(true);
    store.loadActivity();
    await vi.waitFor(() => expect(store.isActivityLoading()).toBe(false));

    expect(localStorage.getItem(involvesMeFilterStorageKey("activity"))).toBe("1");
    expect(get).toHaveBeenCalledWith(
      "/activity",
      expect.objectContaining({
        params: { query: expect.objectContaining({ involves_me: true }) },
      }),
    );
  });

  it("retains the complete workspace snapshot returned with an activity read", async () => {
    const snapshot = [workspaceActivity(7)];
    const client = {
      GET: async () => ({ data: { items: [], capped: false, workspace_activity: snapshot }, error: null }),
    } as unknown as GeneratedClient;
    const store = createActivityStore({ client });

    store.loadActivity();
    await vi.waitFor(() => expect(store.isActivityLoading()).toBe(false));

    expect(store.getWorkspaceActivity()).toEqual(snapshot);
  });
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
  it("round trips the selected author", () => {
    window.history.replaceState(null, "", "/?author=Alice");
    const s = makeStore();
    s.initializeFromMount();

    expect(s.getActivityAuthor()).toBe("Alice");
    s.setActivityAuthor(undefined);
    s.syncToURL();
    expect(new URLSearchParams(window.location.search).has("author")).toBe(false);
  });

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

describe("activity store author candidates", () => {
  it("keeps a URL-selected author available when it is absent from the current candidates", () => {
    window.history.replaceState(null, "", "/?author=FormerUser");
    const s = makeStore();
    s.initializeFromMount();

    expect(s.getActivityAuthors()).toEqual(["FormerUser"]);
  });

  it("preserves the selected spelling when a candidate differs only by case", async () => {
    window.history.replaceState(null, "", "/?author=Alice");
    const get = vi.fn(async (path: string) => {
      if (path === "/activity/authors") {
        return { data: { authors: ["ALICE", "Bob"] }, error: null };
      }
      return { data: { items: [], capped: false }, error: null };
    });
    const s = createActivityStore({
      client: { GET: get } as unknown as Parameters<typeof createActivityStore>[0]["client"],
    });
    s.initializeFromMount();

    s.loadActivityAuthors();

    await vi.waitFor(() => expect(s.getActivityAuthors()).toEqual(["Alice", "Bob"]));
    expect(s.getActivityAuthor()).toBe("Alice");
  });

  it("filters the feed by author while candidate requests only use repo and time range", async () => {
    const get = vi.fn(async (path: string) => {
      if (path === "/activity/authors") return { data: { authors: ["Alice"] }, error: null };
      return { data: { items: [], capped: false }, error: null };
    });
    const s = createActivityStore({
      client: { GET: get } as unknown as Parameters<typeof createActivityStore>[0]["client"],
      getGlobalRepo: () => "github|github.com/acme/widget",
    });
    s.setActivityAuthor("Alice");
    s.setActivitySearch("cache");
    s.setActivityFilterTypes(["comment"]);

    s.loadActivityAuthors();
    s.loadActivity();
    await vi.waitFor(() => expect(get.mock.calls.some(([path]) => path === "/activity/authors")).toBe(true));
    await vi.waitFor(() => expect(get.mock.calls.some(([path]) => path === "/activity")).toBe(true));

    const authorCall = get.mock.calls.find(([path]) => path === "/activity/authors");
    expect(authorCall?.[1]).toEqual({
      params: {
        query: {
          repo: "github|github.com/acme/widget",
          since: expect.any(String),
        },
      },
      signal: expect.any(AbortSignal),
    });
    const feedCall = get.mock.calls.find(([path]) => path === "/activity");
    expect(feedCall?.[1]).toEqual({
      params: {
        query: expect.objectContaining({
          author: "Alice",
          search: "cache",
          types: ["comment"],
        }),
      },
      signal: expect.any(AbortSignal),
    });
  });

  it("reports candidate errors independently from feed errors", async () => {
    const get = vi.fn(async (path: string) => {
      if (path === "/activity/authors") {
        return { error: { detail: "authors unavailable" }, response: new Response(null, { status: 500 }) };
      }
      return { data: { items: [], capped: false }, error: null };
    });
    const s = createActivityStore({
      client: { GET: get } as unknown as Parameters<typeof createActivityStore>[0]["client"],
    });

    s.loadActivityAuthors();
    await vi.waitFor(() => expect(s.getActivityAuthorsError()).toBe("authors unavailable"));

    expect(s.getActivityAuthorsError()).toBe("authors unavailable");
    expect(s.getActivityError()).toBeNull();
  });

  it("clears candidates for a new scope and retries that scope after failure", async () => {
    let repo = "github|github.com/acme/widgets";
    let secondScopeFails = true;
    const get = vi.fn(async (path: string) => {
      if (path !== "/activity/authors") {
        return { data: { items: [], capped: false }, error: null };
      }
      if (repo.endsWith("widgets")) {
        return { data: { authors: ["Alice"] }, error: null };
      }
      if (secondScopeFails) {
        return { error: { detail: "authors unavailable" }, response: new Response(null, { status: 500 }) };
      }
      return { data: { authors: ["Bob"] }, error: null };
    });
    const s = createActivityStore({
      client: { GET: get } as unknown as Parameters<typeof createActivityStore>[0]["client"],
      getGlobalRepo: () => repo,
    });

    s.loadActivityAuthors();
    await vi.waitFor(() => expect(s.getActivityAuthors()).toEqual(["Alice"]));

    repo = "github|github.com/acme/tools";
    s.setTimeRange("30d");
    s.loadActivityAuthors();
    await vi.waitFor(() => expect(s.getActivityAuthorsError()).toBe("authors unavailable"));
    expect(s.getActivityAuthors()).toEqual([]);

    secondScopeFails = false;
    s.loadActivityAuthors();
    await vi.waitFor(() => expect(s.getActivityAuthors()).toEqual(["Bob"]));
    expect(s.getActivityAuthorsError()).toBeNull();
    expect(get.mock.calls.filter(([path]) => path === "/activity/authors")).toHaveLength(3);
  });

  it("refreshes same-scope candidates during activity reconciliation", async () => {
    let authors = ["Alice"];
    const get = vi.fn(async (path: string) => {
      if (path === "/activity/authors") return { data: { authors }, error: null };
      return { data: { items: [], capped: false }, error: null };
    });
    const s = createActivityStore({
      client: { GET: get } as unknown as Parameters<typeof createActivityStore>[0]["client"],
    });

    s.loadActivityAuthors();
    await vi.waitFor(() => expect(s.getActivityAuthors()).toEqual(["Alice"]));

    authors = ["Alice", "FreshActor"];
    if (runtime === undefined) throw new Error("test runtime was not created");
    await runtime.runCommand(s.reconcileActivityEffect(), {
      operation: "reconcile activity in test",
      safeContext: {},
      onFailure: () => {},
    }).exit;

    expect(s.getActivityAuthors()).toEqual(["Alice", "FreshActor"]);
    expect(get.mock.calls.filter(([path]) => path === "/activity/authors")).toHaveLength(2);
  });

  it("force-refreshes same-scope candidates with an Activity load", async () => {
    let authors = ["Alice"];
    let feedReads = 0;
    const get = vi.fn(async (path: string) => {
      if (path === "/activity/authors") return { data: { authors }, error: null };
      feedReads += 1;
      return { data: { items: [], capped: false }, error: null };
    });
    const s = createActivityStore({
      client: { GET: get } as unknown as Parameters<typeof createActivityStore>[0]["client"],
    });

    s.loadActivity();
    await vi.waitFor(() => expect(s.getActivityAuthors()).toEqual(["Alice"]));

    authors = ["Bob"];
    s.loadActivity(true);
    await vi.waitFor(() => expect(feedReads).toBe(2));
    await vi.waitFor(() => expect(s.getActivityAuthors()).toEqual(["Bob"]));
    expect(get.mock.calls.filter(([path]) => path === "/activity/authors")).toHaveLength(2);
  });

  it("replaces an in-flight same-scope author refresh during a foreground activity load", async () => {
    type ActivityResponse = { data: { items: never[]; capped: false }; error: null };

    let activityRequests = 0;
    let authorRequests = 0;
    let resolveReconciliation!: (response: ActivityResponse) => void;
    const pendingReconciliation = new Promise<ActivityResponse>((resolve) => {
      resolveReconciliation = resolve;
    });
    const get = vi.fn((path: string, options?: { signal?: AbortSignal }) => {
      if (path === "/activity/authors") {
        authorRequests += 1;
        if (authorRequests > 1) {
          return Promise.resolve({ data: { authors: ["Bob"] }, error: null });
        }
        return new Promise((_, reject) => {
          options?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), {
            once: true,
          });
        });
      }

      activityRequests += 1;
      if (activityRequests === 1) return pendingReconciliation;
      return Promise.resolve({ data: { items: [], capped: false }, error: null });
    });
    const s = createActivityStore({
      client: { GET: get } as unknown as Parameters<typeof createActivityStore>[0]["client"],
    });

    if (runtime === undefined) throw new Error("test runtime was not created");
    const reconciliation = runtime.runCommand(s.reconcileActivityEffect(), {
      operation: "reconcile activity in test",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => {
      expect(activityRequests).toBe(1);
      expect(authorRequests).toBe(1);
      expect(s.isActivityAuthorsLoading()).toBe(true);
    });

    s.loadActivity();
    await vi.waitFor(() => {
      expect(activityRequests).toBe(2);
      expect(authorRequests).toBe(2);
    });
    resolveReconciliation({ data: { items: [], capped: false }, error: null });
    await reconciliation.exit;

    await vi.waitFor(() => expect(s.isActivityAuthorsLoading()).toBe(false));
    await vi.waitFor(() => expect(s.getActivityAuthors()).toEqual(["Bob"]));
    expect(authorRequests).toBe(2);
  });
});

describe("activity store projection scope", () => {
  it("rejects an older unfiltered reconciliation after an Involves me load fails", async () => {
    const pendingReconciliation = Promise.withResolvers<{
      data: { items: ActivityItem[]; capped: boolean };
      error: null;
    }>();
    const staleItem = notificationItem("ntf:stale", "unread");
    let feedReads = 0;
    const get = vi.fn((path: string, options?: { params?: { query?: { involves_me?: boolean } } }) => {
      if (path === "/activity/authors") return Promise.resolve({ data: { authors: [] }, error: null });
      feedReads += 1;
      if (feedReads === 1) {
        expect(options?.params?.query?.involves_me).toBeUndefined();
        return pendingReconciliation.promise;
      }
      expect(options?.params?.query?.involves_me).toBe(true);
      return Promise.resolve({
        error: {
          code: "validationError",
          detail: "filtered activity unavailable",
          title: "Invalid request",
          type: "about:blank",
        },
        response: new Response(null, { status: 400 }),
      });
    });
    const store = createActivityStore({ client: { GET: get } as unknown as GeneratedClient });

    if (runtime === undefined) throw new Error("test runtime was not created");
    const reconciliation = runtime.runCommand(store.reconcileActivityEffect(), {
      operation: "reconcile activity in test",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => expect(feedReads).toBe(1));

    store.setInvolvesMe(true);
    store.loadActivity();
    await vi.waitFor(() => expect(store.getActivityError()).toBe("filtered activity unavailable"));

    pendingReconciliation.resolve({ data: { items: [staleItem], capped: false }, error: null });
    await reconciliation.exit;

    expect(store.getActivityItems()).toEqual([]);
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
    let feedReads = 0;
    const get = vi.fn((path: string) => {
      if (path === "/activity/authors") return Promise.resolve({ data: { authors: [] }, error: null });
      feedReads++;
      if (feedReads === 1) {
        return Promise.resolve({
          data: { items: [notificationItem("ntf:42", "unread")], capped: false },
          error: null,
        });
      }
      return olderRead.promise;
    });
    const post = vi.fn().mockResolvedValue({
      data: { queued: [42], succeeded: [], failed: [] },
      error: null,
    });
    const s = createActivityStore({ client: { GET: get, POST: post } as unknown as GeneratedClient });
    s.loadActivity();
    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));

    s.loadActivity();
    await vi.waitFor(() => expect(feedReads).toBe(2));
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
    let feedReads = 0;
    const get = vi.fn((path: string) => {
      if (path === "/activity/authors") return Promise.resolve({ data: { authors: [] }, error: null });
      feedReads++;
      if (feedReads === 1) {
        return Promise.resolve({
          data: { items: [notificationItem("ntf:42", "unread")], capped: false },
          error: null,
        });
      }
      return pendingRead.promise;
    });
    const post = vi.fn(() => acknowledgement.promise);
    const s = createActivityStore({ client: { GET: get, POST: post } as unknown as GeneratedClient });
    s.loadActivity();
    await vi.waitFor(() => expect(s.isActivityLoading()).toBe(false));

    s.markNotificationSeen(s.getActivityItems()[0]!);
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    s.loadActivity();
    await vi.waitFor(() => expect(feedReads).toBe(2));
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
  it("refreshes author candidates when polling appends new activity", async () => {
    let authors = ["Alice"];
    let feedReads = 0;
    const now = new Date().toISOString();
    const initial = { ...notificationItem("ntf:1", "unread"), author: "Alice", created_at: now };
    const fresh = { ...notificationItem("ntf:2", "unread"), author: "FreshActor", created_at: now };
    const client = {
      GET: vi.fn(async (path: string) => {
        if (path === "/activity/authors") return { data: { authors }, error: null };
        feedReads += 1;
        if (feedReads === 1) return { data: { items: [initial], capped: false }, error: null };
        return { data: { items: [fresh], capped: false }, error: null };
      }),
    } as unknown as GeneratedClient;
    const store = createActivityStore({ client });
    store.loadActivity();
    await vi.waitFor(() => expect(store.getActivityItems().map((item) => item.id)).toEqual(["ntf:1"]));
    await vi.waitFor(() => expect(store.getActivityAuthors()).toEqual(["Alice"]));

    authors = ["FreshActor", "Alice"];
    store.startActivityPolling();
    await vi.waitFor(() => expect(store.getActivityItems().map((item) => item.id)).toEqual(["ntf:2", "ntf:1"]));
    await vi.waitFor(() => expect(store.getActivityAuthors()).toEqual(["FreshActor", "Alice"]));
  });

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
    let feedReads = 0;
    const client = {
      GET: vi.fn(async (path: string) => {
        if (path === "/activity/authors") return { data: { authors: [] }, error: null };
        feedReads += 1;
        if (feedReads === 1) return { data: { items: [initial], capped: false }, error: null };
        if (feedReads === 2) {
          const response = await pendingPoll.promise;
          pollReturned.resolve();
          return response;
        }
        if (feedReads === 3) return { data: { items: [foregroundItem], capped: false }, error: null };
        throw new Error(`unexpected activity request ${feedReads}`);
      }),
    } as unknown as GeneratedClient;
    const store = createActivityStore({ client });
    store.loadActivity();
    await vi.waitFor(() => expect(store.getActivityItems().map((item) => item.id)).toEqual(["ntf:1"]));

    store.startActivityPolling();
    await vi.waitFor(() => expect(feedReads).toBe(2));
    store.setActivitySearch("new selection");
    store.loadActivity();
    await vi.waitFor(() => expect(store.getActivityItems().map((item) => item.id)).toEqual(["ntf:3"]));

    pendingPoll.resolve({ data: { items: [stalePollItem], capped: false }, error: null });
    await pollReturned.promise;
    await new Promise<void>((resolve) => setTimeout(resolve, 0));

    expect(store.getActivityItems().map((item) => item.id)).toEqual(["ntf:3"]);
  });

  it("clears loading after an empty-feed poll reload fails", async () => {
    let feedReads = 0;
    const client = {
      GET: vi.fn(async (path: string) => {
        if (path === "/activity/authors") return { data: { authors: [] }, error: null };
        feedReads += 1;
        if (feedReads === 1) return { data: { items: [], capped: false } };
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
    await vi.waitFor(() => expect(feedReads).toBe(2));

    expect(store.isActivityLoading()).toBe(false);
  });

  it("clears loading after a capped poll reload fails", async () => {
    let feedReads = 0;
    const client = {
      GET: vi.fn(async (path: string) => {
        if (path === "/activity/authors") return { data: { authors: [] }, error: null };
        feedReads += 1;
        if (feedReads === 1) return { data: { items: [notificationItem("ntf:42", "unread")], capped: false } };
        if (feedReads === 2) return { data: { items: [], capped: true } };
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
    await vi.waitFor(() => expect(feedReads).toBe(3));

    expect(store.isActivityLoading()).toBe(false);
  });
});
