import { afterEach, describe, expect, test } from "vite-plus/test";

import {
  createKataTaskAPI,
  getLastKataTaskResponseHeaders,
  KataTaskAPIError,
  KataTaskRevisionConflictError,
} from "./taskClient.js";
import { KATA_DAEMON_HEADER } from "./daemons.js";
import type { KataProjectSummary, KataTaskSummary } from "./taskTypes.js";

type FetchCall = {
  url: string;
  method: string;
  headers: Headers;
  requestJSON: () => Promise<unknown>;
};

function project(uid: string, name: string, metadata: KataProjectSummary["metadata"] = {}): KataProjectSummary {
  return {
    id: 1,
    uid,
    name,
    metadata,
    open_count: 0,
  };
}

function issue(
  uid: string,
  title: string,
  project_uid = "project-work",
  metadata: KataTaskSummary["metadata"] = {},
  status: KataTaskSummary["status"] = "open",
): KataTaskSummary {
  const out: KataTaskSummary = {
    id: 1,
    uid,
    project_id: 1,
    short_id: uid,
    qualified_id: `Work#${uid}`,
    title,
    status,
    project_uid,
    project_name: project_uid === "project-inbox" ? "Inbox" : "Work",
    metadata,
    revision: 1,
    author: "fixture-user",
    created_at: "2026-05-01T12:00:00.000Z",
    updated_at: "2026-05-15T16:00:00.000Z",
  };
  if (status === "closed") {
    out.closed_at = "2026-05-15T12:00:00.000Z";
  }
  return out;
}

function proxyPath(url: string): string {
  const parsed = new URL(url, window.location.origin);
  const marker = "/api/v1/kata/proxy";
  const index = parsed.pathname.indexOf(marker);
  const path = index >= 0 ? parsed.pathname.slice(index + marker.length) : parsed.pathname;
  return `${path}${parsed.search}`;
}

function createFetchStub(
  routes: Record<string, { status?: number; body?: unknown; headers?: Record<string, string> }>,
) {
  const calls: FetchCall[] = [];
  const fetchImpl: typeof fetch = async (input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
    const headers = new Headers(init?.headers);
    calls.push({
      url,
      method: init?.method ?? "GET",
      headers,
      requestJSON: async () => (init?.body ? JSON.parse(String(init.body)) : undefined),
    });
    const route = routes[proxyPath(url)];
    if (!route) {
      return new Response(JSON.stringify({ error: { code: "not_found", message: `unhandled ${proxyPath(url)}` } }), {
        status: 404,
        headers: { "content-type": "application/json" },
      });
    }
    const status = route.status ?? 200;
    const bodyText = status === 204 || status === 205 || status === 304 ? null : JSON.stringify(route.body ?? {});
    return new Response(bodyText, {
      status,
      headers: { "content-type": "application/json", ...route.headers },
    });
  };

  return { calls, fetchImpl };
}

describe("kata task HTTP client", () => {
  afterEach(() => {
    delete window.__BASE_PATH__;
  });

  test("uses middleman proxy routes for instance, projects, and open issue views", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/instance": {
        body: { instance_uid: "instance-1", version: "dev", schema_version: 10 },
        headers: { etag: '"instance-etag"' },
      },
      "/api/v1/projects?include=stats": {
        body: { projects: [project("project-work", "Work")] },
      },
      "/api/v1/issues?status=open": {
        body: {
          issues: [issue("issue-old", "Overdue", "project-work", { scheduled_on: "2000-01-01" })],
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.instance()).resolves.toMatchObject({ instance_uid: "instance-1" });
    expect(getLastKataTaskResponseHeaders(api)?.get("etag")).toBe('"instance-etag"');
    await expect(api.projects()).resolves.toMatchObject({ projects: [expect.objectContaining({ name: "Work" })] });
    await expect(api.issues({ view: "today" })).resolves.toMatchObject({
      view: "today",
      groups: [{ id: "overdue", issues: [expect.objectContaining({ title: "Overdue" })] }],
    });

    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/instance",
      "/api/v1/projects?include=stats",
      "/api/v1/issues?status=open",
      "/api/v1/projects?include=stats",
    ]);
    expect(calls.map((call) => new URL(call.url, window.location.origin).pathname)).toEqual([
      "/api/v1/kata/proxy/api/v1/instance",
      "/api/v1/kata/proxy/api/v1/projects",
      "/api/v1/kata/proxy/api/v1/issues",
      "/api/v1/kata/proxy/api/v1/projects",
    ]);
  });

  test("respects the configured base path and active daemon header", async () => {
    window.__BASE_PATH__ = "/middleman/";
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/instance": {
        body: { instance_uid: "instance-1", version: "dev", schema_version: 10 },
      },
    });
    const api = createKataTaskAPI({ fetchImpl, getDaemonId: () => "work" });

    await api.instance();

    const url = new URL(calls[0]!.url, window.location.origin);
    expect(url.pathname).toBe("/middleman/api/v1/kata/proxy/api/v1/instance");
    expect(calls[0]!.headers.get(KATA_DAEMON_HEADER)).toBe("work");
  });

  test("creates projects through the proxied daemon route", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects": {
        body: { project: project("project-sabbatical", "Sabbatical") },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.createProject("Sabbatical")).resolves.toMatchObject({
      uid: "project-sabbatical",
      name: "Sabbatical",
    });

    expect(proxyPath(calls[0]!.url)).toBe("/api/v1/projects");
    expect(await calls[0]!.requestJSON()).toEqual({ name: "Sabbatical" });
  });

  test("patches project metadata with If-Match and preserves mutation response headers", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/12/metadata": {
        headers: { etag: '"rev-2"' },
        body: {
          changed: true,
          project: {
            id: 12,
            uid: "project-task-inbox",
            name: "Task Inbox",
            metadata: '{"role":"inbox"}',
            revision: 2,
            created_at: "2026-05-15T12:00:00.000Z",
            open_count: 0,
          },
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.patchProjectMetadata(12, "middleman", { role: "inbox" }, '"rev-1"')).resolves.toMatchObject({
      changed: true,
      etag: '"rev-2"',
      project: expect.objectContaining({
        id: 12,
        name: "Task Inbox",
        metadata: { role: "inbox" },
        revision: 2,
      }),
    });

    expect(calls.map((call) => proxyPath(call.url))).toEqual(["/api/v1/projects/12/metadata"]);
    expect(calls[0]!.method).toBe("POST");
    expect(calls[0]!.headers.get("If-Match")).toBe('"rev-1"');
    expect(await calls[0]!.requestJSON()).toEqual({
      actor: "middleman",
      patch: { role: "inbox" },
    });
    expect(getLastKataTaskResponseHeaders(api)?.get("etag")).toBe('"rev-2"');
  });

  test("creates tasks with metadata and an idempotency key through the proxied daemon route", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues": {
        headers: { etag: '"rev-1"' },
        body: {
          changed: true,
          issue: issue("issue-capture", "Capture from notes", "project-inbox"),
        },
      },
      "/api/v1/projects/7/issues/issue-capture/metadata": {
        headers: { etag: '"rev-2"' },
        body: {
          changed: true,
          issue: {
            ...issue("issue-capture", "Capture from notes", "project-inbox", {
              scheduled_on: "2026-05-20",
            }),
            revision: 2,
          },
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl, getDaemonId: () => "home" });

    await expect(
      api.createIssue(
        7,
        "middleman",
        {
          title: "Capture from notes",
          body: "Markdown **note**",
          labels: ["notes"],
          metadata: { scheduled_on: "2026-05-20" },
          force_new: true,
        },
        "01MIDDLEMANCAPTURE00000001",
      ),
    ).resolves.toMatchObject({
      changed: true,
      etag: '"rev-2"',
      issue: expect.objectContaining({
        title: "Capture from notes",
        metadata: { scheduled_on: "2026-05-20" },
      }),
    });

    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/projects/7/issues",
      "/api/v1/projects/7/issues/issue-capture/metadata",
    ]);
    expect(calls[0]!.headers.get(KATA_DAEMON_HEADER)).toBe("home");
    expect(calls[1]!.headers.get(KATA_DAEMON_HEADER)).toBe("home");
    expect(calls[0]!.headers.get("Idempotency-Key")).toBe("01MIDDLEMANCAPTURE00000001");
    expect(calls[1]!.headers.get("Idempotency-Key")).toBe("01MIDDLEMANCAPTURE00000001:metadata");
    expect(calls[1]!.headers.get("If-Match")).toBe('"rev-1"');
    expect(await calls[0]!.requestJSON()).toEqual({
      actor: "middleman",
      title: "Capture from notes",
      body: "Markdown **note**",
      labels: ["notes"],
      force_new: true,
    });
    expect(await calls[1]!.requestJSON()).toEqual({
      actor: "middleman",
      patch: { scheduled_on: "2026-05-20" },
    });
  });

  test("treats create retry metadata conflicts as success when desired metadata is already applied", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues": {
        headers: { etag: '"rev-1"' },
        body: {
          changed: true,
          issue: issue("issue-capture", "Capture retry", "project-inbox"),
        },
      },
      "/api/v1/projects/7/issues/issue-capture/metadata": {
        status: 412,
        body: {
          error: {
            code: "revision_conflict",
            message: "issue revision is 2",
            details: { current_revision: 2 },
          },
        },
      },
      "/api/v1/issues/issue-capture": {
        headers: { etag: '"rev-2"' },
        body: {
          issue: {
            ...issue("issue-capture", "Capture retry", "project-inbox", {
              scheduled_on: "2026-05-20",
            }),
            revision: 2,
          },
          comments: [],
          labels: [],
          links: [],
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl, getDaemonId: () => "home" });

    await expect(
      api.createIssue(
        7,
        "middleman",
        {
          title: "Capture retry",
          metadata: { scheduled_on: "2026-05-20" },
        },
        "01MIDDLEMANCAPTURE00000004",
      ),
    ).resolves.toMatchObject({
      changed: false,
      etag: '"rev-2"',
      issue: expect.objectContaining({
        metadata: { scheduled_on: "2026-05-20" },
      }),
    });
    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/projects/7/issues",
      "/api/v1/projects/7/issues/issue-capture/metadata",
      "/api/v1/issues/issue-capture",
    ]);
    expect(calls[0]!.headers.get(KATA_DAEMON_HEADER)).toBe("home");
    expect(calls[1]!.headers.get(KATA_DAEMON_HEADER)).toBe("home");
    expect(calls[2]!.headers.get(KATA_DAEMON_HEADER)).toBe("home");
  });

  test("renames projects through the proxied daemon route", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/12": {
        body: { project: project("project-wellness", "Wellness") },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.renameProject(12, "Wellness")).resolves.toMatchObject({
      uid: "project-wellness",
      name: "Wellness",
    });

    expect(proxyPath(calls[0]!.url)).toBe("/api/v1/projects/12");
    expect(await calls[0]!.requestJSON()).toEqual({ name: "Wellness" });
  });

  test("fetches logbook through the bounded closed issue list", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects?include=stats": { body: { projects: [project("project-work", "Work")] } },
      "/api/v1/issues?status=closed&limit=500": {
        body: { issues: [issue("issue-done", "Done", "project-work", {}, "closed")] },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.issues({ view: "logbook" })).resolves.toMatchObject({
      view: "logbook",
      groups: [{ id: "2026-05-15" }],
    });

    expect(proxyPath(calls[0]!.url)).toBe("/api/v1/issues?status=closed&limit=500");
  });

  test("filters issue views by project and area after normalizing generic lists", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects?include=stats": {
        body: {
          projects: [
            project("project-health", "Health", { area: "Personal" }),
            project("project-work", "Work", { area: "Work" }),
          ],
        },
      },
      "/api/v1/issues?status=open": {
        body: {
          issues: [
            issue("issue-health", "Health task", "project-health"),
            issue("issue-work", "Work task", "project-work"),
          ],
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const view = await api.issues({ view: "all", area: "Personal", project_uid: "project-health" });

    expect(view.groups.flatMap((group) => group.issues).map((item) => item.title)).toEqual(["Health task"]);
  });

  test("searches a project by integer project id and normalizes server search results", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects?include=stats": {
        body: { projects: [project("project-work", "Work")] },
      },
      "/api/v1/projects/1/search?q=rent": {
        body: {
          query: "rent",
          results: [
            { issue: issue("issue-rent", "Pay rent", "project-work", {}, "open"), score: 12, matched_in: ["title"] },
          ],
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const results = await api.search({
      scope: { kind: "project", project_uid: "project-work" },
      status: "open",
      owner: "",
      label: "",
      query: "rent",
    });

    expect(results.issues.map((item) => item.title)).toEqual(["Pay rent"]);
    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/projects?include=stats",
      "/api/v1/projects/1/search?q=rent",
    ]);
  });

  test("project-scoped search without a query uses generic issue lists", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/issues?status=open": {
        body: {
          issues: [
            issue("issue-work", "Work backlog", "project-work", {}, "open"),
            issue("issue-personal", "Personal backlog", "project-personal", {}, "open"),
          ],
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const results = await api.search({
      scope: { kind: "project", project_uid: "project-work" },
      status: "open",
      owner: "",
      label: "",
      query: "",
    });

    expect(results.issues.map((item) => item.title)).toEqual(["Work backlog"]);
    expect(calls.map((call) => proxyPath(call.url))).toEqual(["/api/v1/issues?status=open"]);
  });

  test("project search enriches raw search issues with the scoped project identity", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects?include=stats": {
        body: { projects: [project("project-work", "Work")] },
      },
      "/api/v1/projects/1/search?q=rent": {
        body: {
          query: "rent",
          results: [
            {
              issue: {
                id: 5,
                uid: "issue-rent",
                project_id: 1,
                short_id: "ABC",
                title: "Pay rent",
                status: "open",
                metadata: {},
                revision: 1,
                author: "fixture-user",
                created_at: "2026-05-01T12:00:00.000Z",
                updated_at: "2026-05-15T16:00:00.000Z",
              },
              score: 12,
              matched_in: ["title"],
            },
          ],
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const results = await api.search({
      scope: { kind: "project", project_uid: "project-work" },
      status: "open",
      owner: "",
      label: "",
      query: "rent",
    });

    expect(results.issues).toEqual([
      expect.objectContaining({
        title: "Pay rent",
        project_uid: "project-work",
        project_name: "Work",
        qualified_id: "Work#ABC",
      }),
    ]);
  });

  test("project search hydrates labels from issue lists before applying the label filter", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects?include=stats": {
        body: { projects: [project("project-work", "Work")] },
      },
      "/api/v1/projects/1/search?q=rent": {
        body: {
          query: "rent",
          results: [
            {
              issue: {
                id: 5,
                uid: "issue-rent",
                project_id: 1,
                short_id: "ABC",
                title: "Pay rent",
                status: "open",
                metadata: {},
                revision: 1,
                author: "fixture-user",
                created_at: "2026-05-01T12:00:00.000Z",
                updated_at: "2026-05-15T16:00:00.000Z",
              },
              score: 12,
              matched_in: ["title"],
            },
          ],
        },
      },
      "/api/v1/issues?status=open": {
        body: {
          issues: [{ ...issue("issue-rent", "Pay rent", "project-work", {}, "open"), labels: ["money"] }],
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const results = await api.search({
      scope: { kind: "project", project_uid: "project-work" },
      status: "open",
      owner: "",
      label: "money",
      query: "rent",
    });

    expect(results.issues.map((item) => item.title)).toEqual(["Pay rent"]);
    expect(results.issues[0]?.labels).toEqual(["money"]);
    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/projects?include=stats",
      "/api/v1/projects/1/search?q=rent",
      "/api/v1/issues?status=open",
    ]);
  });

  test("project search trusts server-ranked text matches outside the returned issue summary", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects?include=stats": {
        body: { projects: [project("project-work", "Work")] },
      },
      "/api/v1/projects/1/search?q=comment-only": {
        body: {
          query: "comment-only",
          results: [
            {
              issue: {
                id: 5,
                uid: "issue-found",
                project_id: 1,
                short_id: "ABC",
                title: "Search hit",
                status: "open",
                metadata: {},
                revision: 1,
                author: "fixture-user",
                created_at: "2026-05-01T12:00:00.000Z",
                updated_at: "2026-05-15T16:00:00.000Z",
              },
              score: 12,
              matched_in: ["comments"],
            },
          ],
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const results = await api.search({
      scope: { kind: "project", project_uid: "project-work" },
      status: "open",
      owner: "",
      label: "",
      query: "comment-only",
    });

    expect(results.issues.map((item) => item.title)).toEqual(["Search hit"]);
  });

  test("all-project search falls back to generic open and closed issue lists", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/issues?status=open": {
        body: { issues: [issue("issue-open", "Open rent task", "project-work", {}, "open")] },
      },
      "/api/v1/issues?status=closed&limit=500": {
        body: { issues: [issue("issue-closed", "Closed rent task", "project-work", {}, "closed")] },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const results = await api.search({
      scope: { kind: "all" },
      status: "all",
      owner: "",
      label: "",
      query: "rent",
    });

    expect(results.issues.map((item) => item.title)).toEqual(["Open rent task", "Closed rent task"]);
    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/issues?status=open",
      "/api/v1/issues?status=closed&limit=500",
    ]);
  });

  test("calls issue detail by uid and exposes response etags", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/issues/issue-1": {
        headers: { etag: '"rev-3"' },
        body: { issue: { ...issue("issue-1", "Shown issue"), body: "Body" }, comments: [], labels: [], links: [] },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.issue("issue-1")).resolves.toMatchObject({ etag: '"rev-3"', issue: { title: "Shown issue" } });

    expect(proxyPath(calls[0]!.url)).toBe("/api/v1/issues/issue-1");
  });

  test("uses an explicit daemon for pinned issue detail reads", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/issues/issue-1": {
        body: { issue: { ...issue("issue-1", "Shown issue"), body: "Body" }, comments: [], labels: [], links: [] },
      },
    });
    const api = createKataTaskAPI({ fetchImpl, getDaemonId: () => "default" });

    await api.issue("issue-1", { daemonId: "work", pinned: true });

    expect(calls[0]!.headers.get(KATA_DAEMON_HEADER)).toBe("work");
  });

  test("fetches events with supported server params and filters issue_uid client-side", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/events?project_id=2&after_id=4": {
        body: {
          reset_required: false,
          events: [
            { event_id: 5, event_uid: "event-5", project_id: 2, issue_uid: "issue-1", type: "issue.created" },
            { event_id: 6, event_uid: "event-6", project_id: 2, issue_uid: "issue-2", type: "issue.created" },
            { event_id: 7, event_uid: "event-7", project_id: 3, issue_uid: "issue-1", type: "issue.created" },
          ],
          next_after_id: 7,
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const events = await api.events({ project_id: 2, issue_uid: "issue-1", after_id: 4 });

    expect(proxyPath(calls[0]!.url)).toBe("/api/v1/events?project_id=2&after_id=4");
    expect(calls[0]!.url.includes("issue_uid")).toBe(false);
    expect(events.events.map((event) => event.event_uid)).toEqual(["event-5"]);
  });

  test("paginates issue-scoped event reads until the requested filtered limit is reached", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/events?after_id=4&limit=2": {
        body: {
          reset_required: false,
          events: [
            { event_id: 5, event_uid: "event-5", issue_uid: "issue-other", type: "issue.created" },
            { event_id: 6, event_uid: "event-6", issue_uid: "issue-1", type: "issue.updated" },
          ],
          next_after_id: 6,
        },
      },
      "/api/v1/events?after_id=6&limit=2": {
        body: {
          reset_required: false,
          events: [
            { event_id: 7, event_uid: "event-7", issue_uid: "issue-other", type: "issue.created" },
            { event_id: 8, event_uid: "event-8", issue_uid: "issue-1", type: "issue.commented" },
          ],
          next_after_id: 8,
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const events = await api.events({ issue_uid: "issue-1", after_id: 4, limit: 2 });

    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/events?after_id=4&limit=2",
      "/api/v1/events?after_id=6&limit=2",
    ]);
    expect(events.events.map((event) => event.event_uid)).toEqual(["event-6", "event-8"]);
  });

  test("posts comment and label mutations to project-id issue-ref routes", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues/issue-1/comments": {
        body: {
          changed: true,
          issue: issue("issue-1", "Task"),
          comment: { id: 4, body: "note", author: "fixture-user" },
        },
      },
      "/api/v1/projects/7/issues/issue-1/labels": {
        body: { changed: true, issue: issue("issue-1", "Task"), label: { label: "bug", author: "fixture-user" } },
      },
      "/api/v1/projects/7/issues/issue-1/labels/bug?actor=fixture-user": {
        body: { changed: true, issue: issue("issue-1", "Task") },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });
    const target = { project_id: 7, ref: "issue-1" };

    await expect(api.addComment(target, "fixture-user", "note")).resolves.toMatchObject({ changed: true });
    await expect(api.addLabel(target, "fixture-user", "bug")).resolves.toMatchObject({ changed: true });
    await expect(api.removeLabel(target, "fixture-user", "bug")).resolves.toMatchObject({ changed: true });

    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/projects/7/issues/issue-1/comments",
      "/api/v1/projects/7/issues/issue-1/labels",
      "/api/v1/projects/7/issues/issue-1/labels/bug?actor=fixture-user",
    ]);
    expect(await calls[0]!.requestJSON()).toEqual({ actor: "fixture-user", body: "note" });
    expect(await calls[1]!.requestJSON()).toEqual({ actor: "fixture-user", label: "bug" });
  });

  test("posts issue action and edit mutations to project-id issue-ref routes", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues/issue-1/actions/assign": { body: { changed: true, issue: issue("issue-1", "Task") } },
      "/api/v1/projects/7/issues/issue-1/actions/unassign": {
        body: { changed: true, issue: issue("issue-1", "Task") },
      },
      "/api/v1/projects/7/issues/issue-1/actions/priority": {
        body: { changed: true, issue: issue("issue-1", "Task") },
      },
      "/api/v1/projects/7/issues/issue-1/actions/close": { body: { changed: true, issue: issue("issue-1", "Task") } },
      "/api/v1/projects/7/issues/issue-1/actions/reopen": {
        body: { changed: true, issue: issue("issue-1", "Task") },
      },
      "/api/v1/projects/7/issues/issue-1": { body: { changed: true, issue: issue("issue-1", "Renamed") } },
      "/api/v1/projects/7/issues/issue-1-links": {
        body: { changed: true, issue: issue("issue-1-links", "Linked") },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });
    const target = { project_id: 7, ref: "issue-1" };

    await api.assignOwner(target, "fixture-user", "agent:planner");
    await api.unassignOwner(target, "fixture-user");
    await api.setPriority(target, "fixture-user", 2);
    await api.closeIssue(target, "fixture-user", { reason: "done", message: "finished", source: "ui" });
    await api.reopenIssue(target, "fixture-user");
    await api.editIssue(target, "fixture-user", { title: "Renamed", body: "Body" });
    await api.editIssue({ project_id: 7, ref: "issue-1-links" }, "fixture-user", {
      title: "Linked",
      body: "Body",
      links_delta: { add_related: ["dent"] },
    });

    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/projects/7/issues/issue-1/actions/assign",
      "/api/v1/projects/7/issues/issue-1/actions/unassign",
      "/api/v1/projects/7/issues/issue-1/actions/priority",
      "/api/v1/projects/7/issues/issue-1/actions/close",
      "/api/v1/projects/7/issues/issue-1/actions/reopen",
      "/api/v1/projects/7/issues/issue-1",
      "/api/v1/projects/7/issues/issue-1-links",
    ]);
    expect(await calls[0]!.requestJSON()).toEqual({ actor: "fixture-user", owner: "agent:planner" });
    expect(await calls[1]!.requestJSON()).toEqual({ actor: "fixture-user" });
    expect(await calls[2]!.requestJSON()).toEqual({ actor: "fixture-user", priority: 2 });
    expect(await calls[3]!.requestJSON()).toEqual({
      actor: "fixture-user",
      reason: "done",
      message: "finished",
      source: "ui",
    });
    expect(await calls[4]!.requestJSON()).toEqual({ actor: "fixture-user" });
    expect(await calls[5]!.requestJSON()).toEqual({ actor: "fixture-user", title: "Renamed", body: "Body" });
    expect(await calls[6]!.requestJSON()).toEqual({
      actor: "fixture-user",
      title: "Linked",
      body: "Body",
      links_delta: { add_related: ["dent"] },
    });
  });

  test("patches issue metadata with If-Match and preserves mutation response headers", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues/issue-1/metadata": {
        body: {
          changed: true,
          issue: issue("issue-1", "Task", "project-work", {
            scheduled_on: "2026-05-20",
            deadline_on: "2026-05-21",
          }),
        },
        headers: { etag: '"rev-2"' },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });
    const target = { project_id: 7, ref: "issue-1" };

    await expect(
      api.patchIssueMetadata(
        target,
        "fixture-user",
        { scheduled_on: "2026-05-20", deadline_on: "2026-05-21" },
        '"rev-1"',
      ),
    ).resolves.toMatchObject({
      changed: true,
      etag: '"rev-2"',
      issue: expect.objectContaining({
        metadata: expect.objectContaining({ scheduled_on: "2026-05-20", deadline_on: "2026-05-21" }),
      }),
    });

    expect(calls.map((call) => proxyPath(call.url))).toEqual(["/api/v1/projects/7/issues/issue-1/metadata"]);
    expect(calls[0]!.headers.get("If-Match")).toBe('"rev-1"');
    expect(await calls[0]!.requestJSON()).toEqual({
      actor: "fixture-user",
      patch: { scheduled_on: "2026-05-20", deadline_on: "2026-05-21" },
    });
    expect(getLastKataTaskResponseHeaders(api)?.get("etag")).toBe('"rev-2"');
  });

  test("parses project metadata revision conflicts from current 412 envelopes", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects/7/metadata": {
        status: 412,
        headers: { etag: '"rev-3"' },
        body: {
          error: {
            code: "revision_conflict",
            message: "project revision is 3",
            details: { current_revision: 3 },
          },
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.patchProjectMetadata(7, "middleman", { role: "inbox" }, '"rev-2"')).rejects.toMatchObject({
      name: "KataTaskRevisionConflictError",
      status: 412,
      code: "revision_conflict",
      details: { current_revision: 3 },
    });
  });

  test("moves issues with If-Match and exposes the moved short id", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues/issue-1/actions/move": {
        body: {
          changed: true,
          issue: issue("issue-1", "Task", "project-next"),
          new_short_id: "NEXT-9",
        },
        headers: { etag: '"rev-2"' },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(
      api.moveIssue({ project_id: 7, ref: "issue-1" }, "fixture-user", "project-next", '"rev-1"'),
    ).resolves.toEqual(
      expect.objectContaining({
        changed: true,
        etag: '"rev-2"',
        new_short_id: "NEXT-9",
        issue: expect.objectContaining({ uid: "issue-1" }),
      }),
    );

    expect(calls.map((call) => proxyPath(call.url))).toEqual(["/api/v1/projects/7/issues/issue-1/actions/move"]);
    expect(calls[0]!.headers.get("If-Match")).toBe('"rev-1"');
    expect(await calls[0]!.requestJSON()).toEqual({ actor: "fixture-user", to_project_uid: "project-next" });
  });

  test("lists project recurrences through the proxied daemon route", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/recurrences": {
        body: {
          recurrences: [
            {
              id: 1,
              uid: "01HZX4Y5Z6A7B8C9D0E1F2G3H4",
              project_id: 7,
              rrule: "FREQ=WEEKLY;COUNT=2",
              dtstart: "2026-05-20",
              timezone: "America/Chicago",
              template_title: "Weekly review",
              template_body: "Review open loops.",
              template_labels: '["routine"]',
              template_metadata: "{}",
              next_occurrence_key: "2026-05-20",
              author: "fixture-user",
              revision: 1,
              created_at: "2026-05-15T12:00:00.000Z",
              updated_at: "2026-05-15T12:00:00.000Z",
            },
          ],
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.recurrences(7)).resolves.toMatchObject({
      recurrences: [
        expect.objectContaining({
          uid: "01HZX4Y5Z6A7B8C9D0E1F2G3H4",
          template_labels: ["routine"],
          next_occurrence_key: "2026-05-20",
        }),
      ],
    });
    expect(proxyPath(calls[0]!.url)).toBe("/api/v1/projects/7/recurrences");
  });

  test("creates, patches, shows, and deletes recurrences with ETag handling", async () => {
    const created = {
      id: 9,
      uid: "01HZX4Y5Z6A7B8C9D0E1F2G3R9",
      project_id: 7,
      rrule: "FREQ=DAILY",
      dtstart: "2026-06-01",
      timezone: "America/Chicago",
      template_title: "Stand-up",
      template_body: "",
      template_labels: "[]",
      template_metadata: "{}",
      author: "fixture-user",
      revision: 1,
      created_at: "2026-06-01T12:00:00.000Z",
      updated_at: "2026-06-01T12:00:00.000Z",
    };
    const patched = {
      ...created,
      uid: "01HZX4Y5Z6A7B8C9D0E1F2G3R1",
      rrule: "FREQ=WEEKLY",
      template_title: "Weekly review v2",
      revision: 3,
    };
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/recurrences": {
        status: 201,
        body: { recurrence: created },
        headers: { etag: '"rev-1"' },
      },
      "/api/v1/projects/7/recurrences/01HZX4Y5Z6A7B8C9D0E1F2G3R1": {
        body: { recurrence: patched, changed: true },
        headers: { etag: '"rev-3"' },
      },
      "/api/v1/projects/7/recurrences/01HZX4Y5Z6A7B8C9D0E1F2G3R1?actor=fixture-user": {
        status: 204,
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const createResponse = await api.createRecurrence(7, {
      actor: "fixture-user",
      rrule: "FREQ=DAILY",
      dtstart: "2026-06-01",
      timezone: "America/Chicago",
      template: { title: "Stand-up" },
    });
    const showResponse = await api.showRecurrence(7, "01HZX4Y5Z6A7B8C9D0E1F2G3R1");
    const patchResponse = await api.patchRecurrence(
      7,
      "01HZX4Y5Z6A7B8C9D0E1F2G3R1",
      { actor: "fixture-user", template: { title: "Weekly review v2" } },
      '"rev-2"',
    );
    await expect(
      api.deleteRecurrence(7, "01HZX4Y5Z6A7B8C9D0E1F2G3R1", "fixture-user", '"rev-3"'),
    ).resolves.toBeUndefined();

    expect(createResponse.recurrence.uid).toBe("01HZX4Y5Z6A7B8C9D0E1F2G3R9");
    expect(createResponse.etag).toBe('"rev-1"');
    expect(showResponse.etag).toBe('"rev-3"');
    expect(patchResponse).toMatchObject({
      changed: true,
      etag: '"rev-3"',
      recurrence: { template_title: "Weekly review v2" },
    });
    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/projects/7/recurrences",
      "/api/v1/projects/7/recurrences/01HZX4Y5Z6A7B8C9D0E1F2G3R1",
      "/api/v1/projects/7/recurrences/01HZX4Y5Z6A7B8C9D0E1F2G3R1",
      "/api/v1/projects/7/recurrences/01HZX4Y5Z6A7B8C9D0E1F2G3R1?actor=fixture-user",
    ]);
    expect(await calls[0]!.requestJSON()).toMatchObject({
      actor: "fixture-user",
      rrule: "FREQ=DAILY",
      template: { title: "Stand-up" },
    });
    expect(calls[2]!.headers.get("If-Match")).toBe('"rev-2"');
    expect(calls[3]!.headers.get("If-Match")).toBe('"rev-3"');
  });

  test("throws typed API errors for non-2xx envelopes", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/instance": {
        status: 500,
        body: { error: { code: "server_error", message: "daemon failed" } },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.instance()).rejects.toMatchObject({
      name: "KataTaskAPIError",
      status: 500,
      code: "server_error",
      message: "daemon failed",
    });
    await expect(api.instance()).rejects.toBeInstanceOf(KataTaskAPIError);
  });

  test("throws revision conflicts only for current revision_conflict envelopes", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/issues/issue-1": {
        status: 412,
        body: { error: { code: "revision_conflict", message: "stale revision" } },
      },
      "/api/v1/issues/issue-2": {
        status: 412,
        body: { error: { code: "precondition_failed", message: "missing dependency" } },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.issue("issue-1")).rejects.toBeInstanceOf(KataTaskRevisionConflictError);
    await expect(api.issue("issue-2")).rejects.toBeInstanceOf(KataTaskAPIError);
    await expect(api.issue("issue-2")).rejects.not.toBeInstanceOf(KataTaskRevisionConflictError);
  });
});
