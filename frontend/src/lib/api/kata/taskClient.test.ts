import { describe, expect, test } from "vite-plus/test";

import { createKataTaskAPI, KataTaskAPIError, KataTaskRevisionConflictError } from "./taskClient.js";
import { KATA_DAEMON_HEADER } from "./daemons.js";
import type { KataProjectSummary, KataTaskSummary } from "./taskTypes.js";

type FetchCall = {
  url: string;
  method: string;
  headers: Headers;
  requestJSON: () => Promise<unknown>;
};

function project(uid: string, name: string, metadata: KataProjectSummary["metadata"] = {}): KataProjectSummary {
  return { id: 1, uid, name, metadata, open_count: 0 };
}

function issue(
  uid: string,
  title: string,
  projectUID = "project-work",
  metadata: KataTaskSummary["metadata"] = {},
): KataTaskSummary {
  return {
    id: 1,
    uid,
    project_id: 7,
    short_id: uid,
    qualified_id: `Work#${uid}`,
    title,
    status: "open",
    project_uid: projectUID,
    project_name: projectUID === "project-inbox" ? "Inbox" : "Work",
    metadata,
    revision: 1,
    author: "fixture-user",
    created_at: "2026-05-01T12:00:00.000Z",
    updated_at: "2026-05-15T16:00:00.000Z",
  };
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
    const body = status === 204 ? null : JSON.stringify(route.body ?? {});
    return new Response(body, {
      status,
      headers: { "content-type": "application/json", ...route.headers },
    });
  };
  return { calls, fetchImpl };
}

function recurrence(uid: string, revision = 1) {
  return {
    id: 9,
    uid,
    project_id: 7,
    rrule: "FREQ=WEEKLY",
    dtstart: "2026-05-20",
    timezone: "America/New_York",
    template_title: "Weekly review",
    template_body: "Review open loops.",
    template_labels: "[]",
    template_metadata: "{}",
    author: "middleman",
    revision,
    created_at: "2026-05-15T12:00:00.000Z",
    updated_at: "2026-05-15T12:00:00.000Z",
  };
}

describe("kata mutation and recurrence HTTP client", () => {
  test("pins mutation and recurrence operations to the explicit accepted daemon", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects": { body: { project: project("project-new", "New") } },
      "/api/v1/projects/7/issues/issue-1/comments": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1/actions/move": { body: { changed: true } },
      "/api/v1/projects/7/recurrences": { body: { recurrences: [] } },
    });
    const api = createKataTaskAPI({ fetchImpl });
    const accepted = { daemonId: "accepted" };
    const target = { project_id: 7, ref: "issue-1" };

    await api.createProject("New", accepted);
    await api.addComment(target, "middleman", "hello", accepted);
    await api.moveIssue(target, "middleman", "project-next", '"rev-1"', accepted);
    await api.recurrences(7, accepted);

    expect(calls.map((call) => call.headers.get(KATA_DAEMON_HEADER))).toEqual([
      "accepted",
      "accepted",
      "accepted",
      "accepted",
    ]);
  });

  test("returns acknowledgement-only public mutation results", async () => {
    const authorityPayload = {
      changed: true,
      issue: issue("issue-1", "Server response"),
      comment: { id: 1 },
      label: { label: "money" },
      event: { event_id: 1 },
      new_short_id: "moved",
    };
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects": {
        body: { ...authorityPayload, project: project("project-conflict", "Conflicting project") },
      },
      "/api/v1/projects/7/issues/issue-1/comments": {
        headers: { etag: '"rev-2"' },
        body: authorityPayload,
      },
      "/api/v1/projects/7/issues/issue-1/actions/move": {
        headers: { etag: '"rev-2"' },
        body: authorityPayload,
      },
    });
    const api = createKataTaskAPI({ fetchImpl });
    const target = { project_id: 7, ref: "issue-1" };
    const options = { daemonId: "accepted" };

    await expect(api.createProject("New", options)).resolves.toEqual({ changed: true });
    await expect(api.addComment(target, "middleman", "hello", options)).resolves.toEqual({ changed: true });
    await expect(api.moveIssue(target, "middleman", "project-next", '"rev-1"', options)).resolves.toEqual({
      changed: true,
    });
  });

  test("creates projects through the proxied daemon route", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects": { body: { project: project("project-new", "New") } },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.createProject("New", { daemonId: "home" })).resolves.toEqual({ changed: true });
    expect(calls.map((call) => [proxyPath(call.url), call.method])).toEqual([["/api/v1/projects", "POST"]]);
  });

  test("keeps the Kata API path independent of Middleman's configured base path", async () => {
    const previousBasePath = window.__BASE_PATH__;
    window.__BASE_PATH__ = "/middleman/";
    try {
      const { calls, fetchImpl } = createFetchStub({
        "/api/v1/projects": { body: { project: project("project-new", "New") } },
      });
      const api = createKataTaskAPI({ fetchImpl });

      await api.createProject("New", { daemonId: "home" });

      const requestURL = calls[0]?.url;
      if (requestURL === undefined) throw new Error("expected a Kata proxy request");
      expect(new URL(requestURL, window.location.origin).pathname).toBe("/middleman/api/v1/kata/proxy/api/v1/projects");
    } finally {
      if (previousBasePath === undefined) {
        delete window.__BASE_PATH__;
      } else {
        window.__BASE_PATH__ = previousBasePath;
      }
    }
  });

  test("creates tasks with metadata on one explicitly pinned daemon", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues": {
        headers: { etag: '"rev-1"' },
        body: { changed: true, issue: issue("issue-capture", "Capture", "project-inbox") },
      },
      "/api/v1/projects/7/issues/issue-capture/metadata": {
        headers: { etag: '"rev-2"' },
        body: {
          changed: true,
          issue: issue("issue-capture", "Capture", "project-inbox", { scheduled_on: "2026-05-20" }),
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(
      api.createIssue(
        7,
        "middleman",
        { title: "Capture", metadata: { scheduled_on: "2026-05-20" } },
        { daemonId: "home" },
        "01MIDDLEMANCAPTURE00000001",
      ),
    ).resolves.toEqual({ changed: true });

    expect(calls.map((call) => call.headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "home"]);
    expect(calls[0]!.headers.get("Idempotency-Key")).toBe("01MIDDLEMANCAPTURE00000001");
    expect(calls[1]!.headers.get("Idempotency-Key")).toBe("01MIDDLEMANCAPTURE00000001:metadata");
    expect(calls[1]!.headers.get("If-Match")).toBe('"rev-1"');
  });

  test("does not synthesize a revision ETag for create metadata follow-up", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues": {
        body: { changed: true, issue: issue("issue-capture", "Capture", "project-inbox") },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(
      api.createIssue(
        7,
        "middleman",
        { title: "Capture", metadata: { scheduled_on: "2026-05-20" } },
        { daemonId: "home" },
      ),
    ).rejects.toMatchObject({ code: "mutation_precondition_unavailable" });
    expect(calls).toHaveLength(1);
  });

  test("propagates metadata revision conflict without a direct detail-read reconciliation fallback", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues": {
        headers: { etag: '"rev-1"' },
        body: { changed: true, issue: issue("issue-capture", "Capture", "project-inbox") },
      },
      "/api/v1/projects/7/issues/issue-capture/metadata": {
        status: 412,
        body: { error: { code: "revision_conflict", message: "issue revision is 2" } },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(
      api.createIssue(
        7,
        "middleman",
        { title: "Capture", metadata: { scheduled_on: "2026-05-20" } },
        { daemonId: "home" },
      ),
    ).rejects.toBeInstanceOf(KataTaskRevisionConflictError);
    expect(calls.map((call) => proxyPath(call.url))).toEqual([
      "/api/v1/projects/7/issues",
      "/api/v1/projects/7/issues/issue-capture/metadata",
    ]);
  });

  test("posts comment, label, action, edit, and metadata mutations", async () => {
    const routes = {
      "/api/v1/projects/7/issues/issue-1/comments": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1/labels": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1/labels/money?actor=middleman": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1/actions/assign": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1/actions/unassign": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1/actions/priority": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1/actions/close": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1/actions/reopen": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1": { body: { changed: true } },
      "/api/v1/projects/7/issues/issue-1/metadata": {
        headers: { etag: '"rev-2"' },
        body: { changed: true },
      },
    };
    const { calls, fetchImpl } = createFetchStub(routes);
    const api = createKataTaskAPI({ fetchImpl });
    const target = { project_id: 7, ref: "issue-1" };
    const options = { daemonId: "home" };

    await api.addComment(target, "middleman", "hello", options);
    await api.addLabel(target, "middleman", "money", options);
    await api.removeLabel(target, "middleman", "money", options);
    await api.assignOwner(target, "middleman", "alice", options);
    await api.unassignOwner(target, "middleman", options);
    await api.setPriority(target, "middleman", 2, options);
    await api.closeIssue(target, "middleman", { reason: "done", message: "done" }, options);
    await api.reopenIssue(target, "middleman", options);
    await api.editIssue(target, "middleman", { title: "Edited" }, options);
    await api.patchIssueMetadata(target, "middleman", { scheduled_on: "2026-05-20" }, '"rev-1"', {
      daemonId: "home",
    });

    expect(calls.every((call) => call.headers.get(KATA_DAEMON_HEADER) === "home")).toBe(true);
    expect(calls.at(-1)?.headers.get("If-Match")).toBe('"rev-1"');
    expect(calls.at(-1)?.headers.get(KATA_DAEMON_HEADER)).toBe("home");
  });

  test("moves tasks with an accepted ETag", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues/issue-1/actions/move": {
        headers: { etag: '"rev-2"' },
        body: { changed: true, new_short_id: "moved" },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(
      api.moveIssue({ project_id: 7, ref: "issue-1" }, "middleman", "project-next", '"rev-1"', { daemonId: "home" }),
    ).resolves.toEqual({ changed: true });
    expect(calls[0]!.headers.get("If-Match")).toBe('"rev-1"');
  });

  test("keeps recurrence reads and CRUD on the separate recurrence seam", async () => {
    const created = recurrence("recurrence-created");
    const patched = recurrence("recurrence-existing", 2);
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/recurrences": { body: { recurrences: [created], recurrence: created } },
      "/api/v1/projects/7/recurrences/recurrence-existing": {
        headers: { etag: '"rev-2"' },
        body: { recurrence: patched, changed: true },
      },
      "/api/v1/projects/7/recurrences/recurrence-existing?actor=middleman": { status: 204 },
    });
    const api = createKataTaskAPI({ fetchImpl });
    const options = { daemonId: "home" };

    await expect(api.recurrences(7, { daemonId: "work" })).resolves.toMatchObject({
      recurrences: [expect.objectContaining({ uid: "recurrence-created" })],
    });
    await expect(
      api.createRecurrence(
        7,
        {
          actor: "middleman",
          rrule: "FREQ=WEEKLY",
          dtstart: "2026-05-20",
          timezone: "America/New_York",
          template: { title: "Weekly review" },
        },
        options,
      ),
    ).resolves.toMatchObject({ recurrence: expect.objectContaining({ uid: "recurrence-created" }) });
    await expect(api.showRecurrence(7, "recurrence-existing", options)).resolves.toMatchObject({ etag: '"rev-2"' });
    await expect(
      api.patchRecurrence(7, "recurrence-existing", { actor: "middleman", timezone: "UTC" }, '"rev-1"', options),
    ).resolves.toMatchObject({ changed: true, etag: '"rev-2"' });
    await expect(
      api.deleteRecurrence(7, "recurrence-existing", "middleman", options, '"rev-2"'),
    ).resolves.toBeUndefined();

    expect(calls[0]!.headers.get(KATA_DAEMON_HEADER)).toBe("work");
    expect(calls.slice(1).every((call) => call.headers.get(KATA_DAEMON_HEADER) === "home")).toBe(true);
    expect(calls[3]!.headers.get("If-Match")).toBe('"rev-1"');
    expect(calls[4]!.headers.get("If-Match")).toBe('"rev-2"');
  });

  test("surfaces ordinary API errors", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects": {
        status: 503,
        body: { error: { code: "service_unavailable", message: "daemon unavailable" } },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(api.createProject("New", { daemonId: "home" })).rejects.toBeInstanceOf(KataTaskAPIError);
  });
});
