import { Effect, Exit } from "effect";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";

import { makeAppRuntime, type AppServices, type OwnedAppRuntime } from "../app/runtime.js";
import type { Worklist, WorklistSummary } from "../api/worklists.js";
import { createMockApiFetch, jsonResponse, type MockApiHandle, type MockRequest } from "../../test/mockApiFetch.js";
import {
  WORKLIST_HANDLED_AT,
  worklistItem,
  worklistResponse,
  worklistRoutes,
  type WorklistServer,
} from "../../test/worklistFixtures.js";
import { createWorklistsStore, type WorklistsStore } from "./worklists.svelte.js";

function summary(id: number, title: string, dismissed_at: string | null = null): WorklistSummary {
  return {
    id,
    title,
    recipe: "",
    created_by: "triage-agent",
    workspace_id: "",
    item_count: 0,
    changed_count: 0,
    gone_count: 0,
    handled_count: 0,
    created_at: "2026-10-19T10:00:00Z",
    updated_at: "2026-10-19T10:00:00Z",
    dismissed_at,
  };
}

function worklist(id: number, title: string, dismissed_at: string | null = null): Worklist {
  return {
    id,
    title,
    recipe: "",
    created_by: "triage-agent",
    workspace_id: "",
    created_at: "2026-10-19T10:00:00Z",
    updated_at: "2026-10-19T10:00:00Z",
    dismissed_at,
    items: [],
  };
}

interface Server {
  summaries: WorklistSummary[];
  lists: Map<number, Worklist>;
  failDetail: boolean;
  failSummaries: boolean;
}

function notFound(id: number): Response {
  return jsonResponse(
    { title: "Not Found", status: 404, detail: "worklist not found", code: "worklistNotFound", worklist_id: id },
    404,
  );
}

function routes(server: Server) {
  return (request: MockRequest): Response | null => {
    const path = request.url.pathname;
    if (request.method === "GET" && path === "/api/v1/worklists") {
      if (server.failSummaries) {
        return jsonResponse(
          { title: "Service Unavailable", status: 503, detail: "hub unreachable", code: "hubUnavailable" },
          503,
        );
      }
      return jsonResponse({ worklists: server.summaries.filter((entry) => entry.dismissed_at === null) });
    }
    const detail = path.match(/^\/api\/v1\/worklists\/(\d+)$/);
    if (request.method === "GET" && detail) {
      const id = Number(detail[1]);
      if (server.failDetail) return jsonResponse({ title: "Bad Gateway", status: 502, code: "internal" }, 502);
      const list = server.lists.get(id);
      return list ? jsonResponse(list) : notFound(id);
    }
    const toggle = path.match(/^\/api\/v1\/worklists\/(\d+)\/(dismiss|undismiss)$/);
    if (request.method === "POST" && toggle) {
      const id = Number(toggle[1]);
      const entry = server.summaries.find((candidate) => candidate.id === id);
      if (!entry) return notFound(id);
      entry.dismissed_at = toggle[2] === "dismiss" ? "2026-10-20T09:00:00Z" : null;
      return jsonResponse(entry);
    }
    return null;
  };
}

describe("worklists store", () => {
  let runtime: OwnedAppRuntime;
  let api: MockApiHandle;
  let originalFetch: typeof globalThis.fetch;
  let server: Server;
  let store: WorklistsStore;

  function run<A, E>(effect: Effect.Effect<A, E, AppServices>) {
    return runtime.runCommand(effect, { operation: "test", safeContext: {}, onFailure: () => {} }).exit;
  }

  beforeEach(() => {
    originalFetch = globalThis.fetch;
    server = {
      summaries: [
        summary(2, "Review queue"),
        summary(1, "Stale issues"),
        summary(3, "Old list", "2026-10-18T00:00:00Z"),
      ],
      lists: new Map([
        [2, worklist(2, "Review queue")],
        [1, worklist(1, "Stale issues")],
        [3, worklist(3, "Old list", "2026-10-18T00:00:00Z")],
      ]),
      failDetail: false,
      failSummaries: false,
    };
    api = createMockApiFetch([routes(server)]);
    globalThis.fetch = api.fetch;
    runtime = makeAppRuntime();
    store = createWorklistsStore();
  });

  afterEach(async () => {
    globalThis.fetch = originalFetch;
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("keeps lists that are not dismissed in server order", async () => {
    server.summaries[0]!.dismissed_at = null;
    api = createMockApiFetch([
      (request) =>
        request.method === "GET" && request.url.pathname === "/api/v1/worklists"
          ? jsonResponse({ worklists: server.summaries })
          : null,
    ]);
    globalThis.fetch = api.fetch;

    await run(store.refreshSummariesEffect());

    expect(store.getSummaries().map((entry) => entry.title)).toEqual(["Review queue", "Stale issues"]);
    expect(store.summariesLoaded()).toBe(true);
  });

  it("re-reads the summaries and the open list when a list changes", async () => {
    await run(store.openEffect(2));
    expect(store.getDetail()?.title).toBe("Review queue");

    server.lists.set(2, worklist(2, "Review queue, rewritten"));
    server.summaries[0]!.title = "Review queue, rewritten";
    await run(store.refreshAllEffect());

    expect(store.getDetail()?.title).toBe("Review queue, rewritten");
    expect(store.getSummaries()[0]?.title).toBe("Review queue, rewritten");
  });

  it("reports an unknown list as not found and drops its summary", async () => {
    await run(store.refreshSummariesEffect());
    server.lists.delete(2);

    await run(store.openEffect(2));

    expect(store.getDetailError()).toEqual({ kind: "notFound" });
    expect(store.getDetail()).toBeNull();
    expect(store.getSummaries().map((entry) => entry.id)).toEqual([1]);
  });

  it("opens a dismissed list as the server returns it, outside the summaries", async () => {
    await run(store.refreshSummariesEffect());

    await run(store.openEffect(3));

    expect(store.getDetail()?.dismissed_at).toBe("2026-10-18T00:00:00Z");
    expect(store.getDetailError()).toBeNull();
    expect(store.getSummaries().map((entry) => entry.id)).toEqual([2, 1]);
  });

  it("reports a failed summaries read without claiming the read succeeded", async () => {
    server.failSummaries = true;

    const exit = await run(store.refreshSummariesEffect());

    expect(Exit.isFailure(exit)).toBe(true);
    expect(store.summariesLoaded()).toBe(false);
    expect(store.getSummariesError()).toBe("hub unreachable");

    server.failSummaries = false;
    await run(store.refreshSummariesEffect());
    expect(store.summariesLoaded()).toBe(true);
    expect(store.getSummariesError()).toBeNull();
  });

  it("still refreshes the open list when the summaries read fails", async () => {
    await run(store.openEffect(2));
    server.failSummaries = true;
    server.lists.set(2, worklist(2, "Review queue, rewritten"));

    const exit = await run(store.refreshAllEffect());

    expect(Exit.isFailure(exit)).toBe(true);
    expect(store.getDetail()?.title).toBe("Review queue, rewritten");
  });

  it("keeps the list on screen when a later refresh fails", async () => {
    await run(store.openEffect(1));
    server.failDetail = true;

    const exit = await run(store.refreshOpenDetailEffect());

    expect(Exit.isFailure(exit)).toBe(true);
    expect(store.getDetail()?.title).toBe("Stale issues");
    expect(store.getDetailError()).toBeNull();
  });

  it("dismisses a list and restores it on undo", async () => {
    await run(store.refreshSummariesEffect());

    const dismissed = await run(store.dismissEffect(2));
    expect(Exit.isSuccess(dismissed)).toBe(true);
    expect(store.getSummaries().map((entry) => entry.id)).toEqual([1]);
    expect(
      api.requests.some(
        (request) => request.method === "POST" && request.url.pathname === "/api/v1/worklists/2/dismiss",
      ),
    ).toBe(true);

    await run(store.undismissEffect(2));
    expect(
      api.requests.some(
        (request) => request.method === "POST" && request.url.pathname === "/api/v1/worklists/2/undismiss",
      ),
    ).toBe(true);
    expect(store.getSummaries().map((entry) => entry.id)).toEqual([2, 1]);
  });

  it("applies handled marks to the matching item by repository identity", async () => {
    const mirror = worklistItem({ position: 1, item_type: "pr", number: 42, title: "Mirror PR 42" });
    mirror.repo = { ...mirror.repo, platform_host: "example.com", platform_repo_id: 2002 };
    const fixtures: WorklistServer = {
      lists: [
        worklistResponse(5, "Review queue", [
          worklistItem({ position: 0, item_type: "pr", number: 42, title: "PR 42" }),
          mirror,
        ]),
      ],
      requests: [],
      marks: [],
    };
    globalThis.fetch = createMockApiFetch([worklistRoutes(fixtures)]).fetch;
    await run(store.refreshSummariesEffect());
    await run(store.openEffect(5));
    const target = store.getDetail()!.items[0]!;

    const exit = await run(store.setHandledEffect(5, [target], true));

    expect(Exit.isSuccess(exit)).toBe(true);
    expect(fixtures.marks).toEqual([
      {
        handled: true,
        items: [
          { provider: "github", platform_host: "github.com", platform_repo_id: 1001, item_type: "pr", number: 42 },
        ],
      },
    ]);
    expect(store.getDetail()?.items.map((item) => [item.title, item.handled_at])).toEqual([
      ["PR 42", WORKLIST_HANDLED_AT],
      ["Mirror PR 42", null],
    ]);
    expect(store.getSummaries()[0]?.handled_count).toBe(1);

    await run(store.setHandledEffect(5, [target], false));
    expect(store.getDetail()?.items.map((item) => item.handled_at)).toEqual([null, null]);
    expect(store.getSummaries()[0]?.handled_count).toBe(0);
  });

  it("closing the open list forgets it", async () => {
    await run(store.openEffect(1));
    await run(store.openEffect(null));

    expect(store.getOpenId()).toBeNull();
    expect(store.getDetail()).toBeNull();
  });
});
