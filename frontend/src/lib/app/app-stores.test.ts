import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "./runtime.js";
import { createAppStores } from "../app-stores.svelte.js";
import type { Issue, IssueDetail, PullDetail, PullRequest } from "../api/types.js";
import { createMockApiFetch } from "../../test/mockApiFetch.js";

let runtime: OwnedAppRuntime;
let eventSources: EventSourceStub[];

class EventSourceStub {
  readonly handlers = new Map<string, Set<(event: Event) => void>>();

  constructor(readonly url: string) {
    eventSources.push(this);
  }

  addEventListener(name: string, handler: (event: Event) => void): void {
    const handlers = this.handlers.get(name) ?? new Set();
    handlers.add(handler);
    this.handlers.set(name, handlers);
  }

  removeEventListener(name: string, handler: (event: Event) => void): void {
    this.handlers.get(name)?.delete(handler);
  }

  close(): void {}
}

function emit(source: EventSourceStub, name: string, payload: unknown): void {
  const event = new MessageEvent(name, { data: JSON.stringify(payload) });
  for (const handler of source.handlers.get(name) ?? []) handler(event);
}

beforeEach(() => {
  eventSources = [];
  vi.stubGlobal("EventSource", EventSourceStub);
  runtime = makeAppRuntime();
});

afterEach(async () => {
  await Effect.runPromise(runtime.disposeEffect);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("app store composition", () => {
  it("applies a sync refresh queued while the search cache is first loading", async () => {
    const fixture = createMockApiFetch();
    const firstRead = Promise.withResolvers<void>();
    const releaseFirstRead = Promise.withResolvers<void>();
    let pullReads = 0;
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      const response = await fixture.fetch(input, init);
      const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
      if (url.pathname !== "/api/v1/pulls") return response;
      if (++pullReads > 1) return Response.json([]);
      firstRead.resolve();
      await releaseFirstRead.promise;
      return response;
    });
    const { stores } = createAppStores({ runtime, getPage: () => "terminal" });
    stores.workspaceItemSearch.ensureLoaded();
    await firstRead.promise;
    stores.workspaceItemSearch.ensureLoaded();
    const refreshStarted = Promise.withResolvers<void>();
    const refresh = runtime.runCommand(
      Effect.sync(() => refreshStarted.resolve()).pipe(Effect.andThen(stores.workspaceItemSearch.refreshEffect)),
      { operation: "test overlapping sync refresh", safeContext: {}, onFailure: () => {} },
    );
    await refreshStarted.promise;
    releaseFirstRead.resolve();
    await refresh.exit;
    await vi.waitFor(() => expect(stores.workspaceItemSearch.isLoading()).toBe(false));
    expect(stores.workspaceItemSearch.search("#55").pulls).toEqual([]);
    expect(pullReads).toBe(2);
  });

  it.each([
    ["data_changed", {}],
    ["reconnect.stale", { hub_connected: true }],
    ["hub_connection_changed", { connected: true }],
  ])("refreshes the search cache on %s while the picker is closed", async (event, payload) => {
    const fixture = createMockApiFetch();
    let pulls: PullRequest[] = await (await fixture.fetch("/api/v1/pulls?state=open")).json();
    let issues: Issue[] = await (await fixture.fetch("/api/v1/issues?state=open")).json();
    const api = createMockApiFetch([
      ({ url }) => (url.pathname === "/api/v1/pulls" ? Response.json(pulls) : undefined),
      ({ url }) => (url.pathname === "/api/v1/issues" ? Response.json(issues) : undefined),
    ]);
    vi.stubGlobal("fetch", api.fetch);
    const { stores } = createAppStores({ runtime, getPage: () => "terminal" });
    stores.workspaceItemSearch.ensureLoaded();
    await vi.waitFor(() => expect(stores.workspaceItemSearch.isLoading()).toBe(false));
    expect(stores.workspaceItemSearch.search("#55").pulls[0]?.Title).toBe("Refactor theme system");
    runtime.runCommand(stores.events.streamEffect, {
      operation: "test search refresh events",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => expect(eventSources).toHaveLength(1));
    pulls = pulls.filter((pull) => pull.Number !== 55);
    issues = [{ ...issues[0]!, Title: "Updated issue title" }];
    emit(eventSources[0]!, event, payload);
    await vi.waitFor(() => expect(stores.workspaceItemSearch.search("Updated issue").issues[0]?.Number).toBe(7));
    expect(stores.workspaceItemSearch.search("#55").pulls).toEqual([]);
    expect(stores.pulls.getPulls()).toEqual([]);
    expect(stores.issues.getIssues()).toEqual([]);
  });

  it.each(["terminal", "mobile-workspace-item"])(
    "refreshes an open workspace PR on %s provider events while airplane mode pauses polling",
    async (page) => {
      const initialResponse = await createMockApiFetch().fetch("/api/v1/pulls/github/acme/widgets/42");
      let updatedDetail: PullDetail = await initialResponse.json();
      const api = createMockApiFetch([
        ({ method, url }) =>
          method === "GET" && url.pathname.endsWith("/pulls/github/acme/widgets/42")
            ? Response.json(updatedDetail)
            : undefined,
      ]);
      vi.stubGlobal("fetch", api.fetch);
      const { stores } = createAppStores({ runtime, getPage: () => page });
      stores.settings.setAirplaneMode(true);
      const ref = { provider: "github", platformHost: "github.com", repoPath: "acme/widgets" };
      stores.detail.loadDetail("acme", "widgets", 42, { ...ref, sync: false });
      await vi.waitFor(() => expect(stores.detail.getDetail()?.merge_request.Number).toBe(42));
      stores.detail.startDetailPolling("acme", "widgets", 42, ref);
      runtime.runCommand(stores.events.streamEffect, {
        operation: "test workspace PR events",
        safeContext: {},
        onFailure: () => {},
      });
      await vi.waitFor(() => expect(eventSources).toHaveLength(1));
      updatedDetail = {
        ...updatedDetail,
        merge_request: { ...updatedDetail.merge_request, Body: "Updated workspace PR body" },
      };
      emit(eventSources[0]!, "data_changed", {});
      await vi.waitFor(() => expect(stores.detail.getDetail()?.merge_request.Body).toBe("Updated workspace PR body"));

      stores.detail.stopDetailPolling();
      api.requests.length = 0;
      await runtime.runCommand(stores.detail.refreshActiveDetailEffect(), {
        operation: "test closed workspace PR",
        safeContext: {},
        onFailure: () => {},
      }).exit;
      expect(api.requests.filter(({ url }) => url.pathname.includes("/pulls/"))).toHaveLength(0);
    },
  );

  it.each(["issues", "activity", "focus", "workspaces"])(
    "refreshes an open issue after data_changed on %s without waiting for the detail poll",
    async (page) => {
      const initialResponse = await createMockApiFetch().fetch("/api/v1/issues/github/acme/widgets/7");
      let updatedDetail: IssueDetail = await initialResponse.json();
      updatedDetail.repo.platform_repo_id = "repo-acme-widgets";
      const api = createMockApiFetch([
        ({ method, url }) =>
          method === "GET" && url.pathname === "/api/v1/activity/authors"
            ? Response.json({ authors: [], use_workspace_activity_for_recency: false })
            : undefined,
        ({ method, url }) =>
          method === "GET" && url.pathname === "/api/v1/activity"
            ? Response.json({
                items: [],
                item_activity: [],
                workspace_activity: [],
                capped: false,
                item_activity_capped: false,
                event_cursor: "",
                use_workspace_activity_for_recency: false,
              })
            : undefined,
        ({ method, url }) =>
          method === "GET" && url.pathname.endsWith("/issues/github/acme/widgets/7") && updatedDetail
            ? Response.json(updatedDetail)
            : undefined,
      ]);
      vi.stubGlobal("fetch", api.fetch);
      const { stores } = createAppStores({ runtime, getPage: () => page });
      const ref = { provider: "github", platformHost: "github.com", repoPath: "acme/widgets" };
      stores.issues.loadIssueDetail("acme", "widgets", 7, { ...ref, sync: false });
      await vi.waitFor(() => expect(stores.issues.getIssueDetail()?.issue.Number).toBe(7));
      stores.issues.startIssueDetailPolling("acme", "widgets", 7, ref);
      runtime.runCommand(stores.events.streamEffect, {
        operation: "test provider events",
        safeContext: {},
        onFailure: () => {},
      });
      await vi.waitFor(() => expect(eventSources).toHaveLength(1));
      const original = stores.issues.getIssueDetail()!;
      updatedDetail = {
        ...original,
        issue: { ...original.issue, Body: "Updated upstream body" },
        events: [
          {
            ID: 1,
            IssueID: original.issue.ID,
            EventType: "issue_comment",
            Author: "user-a",
            Body: "New upstream comment",
            CreatedAt: "2026-09-17T10:00:00Z",
            DedupeKey: "comment:1",
            DirectURL: "",
            MetadataJSON: "{}",
            PlatformExternalID: "1",
            PlatformID: 1,
            Summary: "",
            ThreadID: null,
          },
        ],
      };

      emit(eventSources[0]!, "data_changed", {});

      await vi.waitFor(() =>
        expect(stores.issues.getIssueDetail()).toMatchObject({
          issue: { Body: "Updated upstream body" },
          events: [{ Body: "New upstream comment" }],
        }),
      );

      stores.issues.setLocalIssueBody("github", "github.com", "acme", "widgets", 7, "Draft still being edited");
      updatedDetail = { ...updatedDetail, events: [{ ...updatedDetail.events[0]!, Body: "Edited upstream comment" }] };
      emit(eventSources[0]!, "data_changed", {});

      await vi.waitFor(() =>
        expect(stores.issues.getIssueDetail()).toMatchObject({
          issue: { Body: "Draft still being edited" },
          events: [{ Body: "Edited upstream comment" }],
        }),
      );
      expect(api.requests.every(({ method }) => method === "GET")).toBe(true);
    },
  );

  it("builds the provider store graph directly from the application runtime", () => {
    const composition = createAppStores({
      runtime,
      getPage: () => "pulls",
      hostState: {
        getGlobalRepo: () => "github/github.com/acme/widgets",
        getGroupByRepo: () => false,
      },
    });

    expect(composition.stores.pulls.getPulls()).toEqual([]);
    expect(composition.stores.issues.getIssues()).toEqual([]);
    expect(composition.stores.activity.getActivityItems()).toEqual([]);
    expect(composition.stores.grouping.getGroupByRepo()).toBe(true);
    expect(
      composition.stores.workflowActions.getRuns({
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
      }),
    ).toEqual([]);
  });

  it("reconciles the visible list and search cache without populating hidden views after reconnect", async () => {
    const api = createMockApiFetch();
    vi.stubGlobal("fetch", api.fetch);
    const { stores } = createAppStores({ runtime, getPage: () => "pulls" });
    runtime.runCommand(stores.events.streamEffect, {
      operation: "test provider reconnect",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => expect(eventSources).toHaveLength(1));
    emit(eventSources[0]!, "reconnect.stale", { hub_connected: true });
    await vi.waitFor(() => expect(api.requests.some(({ url }) => url.pathname === "/api/v1/pulls")).toBe(true));
    await vi.waitFor(() => expect(stores.sync.getProviderAvailable()).toBe(true));
    expect(stores.issues.getIssues()).toEqual([]);
    expect(stores.workspaceItemSearch.search("").issues[0]?.Number).toBe(7);
    expect(api.requests.some(({ url }) => url.pathname === "/api/v1/activity")).toBe(false);
  });

  it("keeps provider data unavailable when reconnect reconciliation fails", async () => {
    const failures: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(
        async () =>
          new Response(
            JSON.stringify({
              type: "about:blank",
              title: "Not Found",
              status: 404,
              detail: "provider snapshot unavailable",
              code: "notFound",
            }),
            { status: 404, headers: { "content-type": "application/problem+json" } },
          ),
      ),
    );
    const composition = createAppStores({
      runtime,
      getPage: () => "pulls",
      onError: (message) => failures.push(message),
    });
    runtime.runCommand(composition.stores.events.streamEffect, {
      operation: "test provider events",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => expect(eventSources).toHaveLength(1));

    emit(eventSources[0]!, "hub_connection_changed", { connected: true });

    await vi.waitFor(() => expect(failures).toHaveLength(1));
    expect(composition.stores.sync.getProviderAvailable()).toBe(false);
  });

  it("restores provider availability when the hub probe succeeds after a projection failure", async () => {
    const failures: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((input: RequestInfo | URL) => {
        const url = input instanceof Request ? input.url : String(input);
        if (url.endsWith("/api/v1/sync/status")) {
          return Promise.resolve(
            new Response(JSON.stringify({ running: false, last_run_at: "", last_error: "" }), {
              status: 200,
              headers: { "content-type": "application/json" },
            }),
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({
              type: "about:blank",
              title: "Not Found",
              status: 404,
              detail: "provider projection unavailable",
              code: "notFound",
            }),
            { status: 404, headers: { "content-type": "application/problem+json" } },
          ),
        );
      }),
    );
    const composition = createAppStores({
      runtime,
      getPage: () => "pulls",
      onError: (message) => failures.push(message),
    });
    runtime.runCommand(composition.stores.events.streamEffect, {
      operation: "test provider events",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => expect(eventSources).toHaveLength(1));

    emit(eventSources[0]!, "hub_connection_changed", { connected: true });

    await vi.waitFor(() => expect(failures).toHaveLength(1));
    expect(composition.stores.sync.getProviderAvailable()).toBe(true);
  });

  it("marks provider data unavailable when a live refresh cannot reach the hub", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(
        async () =>
          new Response(
            JSON.stringify({
              type: "about:blank",
              title: "Hub unavailable",
              status: 503,
              detail: "provider data is unavailable because the federation hub cannot be reached",
              code: "hubUnavailable",
            }),
            { status: 503, headers: { "content-type": "application/problem+json" } },
          ),
      ),
    );
    const composition = createAppStores({ runtime, getPage: () => "pulls" });
    runtime.runCommand(composition.stores.events.streamEffect, {
      operation: "test provider events",
      safeContext: {},
      onFailure: () => {},
    });
    await vi.waitFor(() => expect(eventSources).toHaveLength(1));

    emit(eventSources[0]!, "data_changed", {});

    await vi.waitFor(() => expect(composition.stores.sync.getProviderAvailable()).toBe(false));
  });
});
