import { assert, it } from "@effect/vitest";
import { describe, expect, test } from "vite-plus/test";
import { Deferred, Effect, Fiber } from "effect";

import {
  createKataTaskAPI,
  KataMutationOutcomeUnknownError,
  KataMutationPartiallyAppliedError,
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
    author: "kenn-forge",
    revision,
    created_at: "2026-05-15T12:00:00.000Z",
    updated_at: "2026-05-15T12:00:00.000Z",
  };
}

function runTaskEffect<A>(effect: Effect.Effect<A, unknown>): Promise<A> {
  return Effect.runPromise(effect);
}

describe("kata mutation and recurrence HTTP client", () => {
  it.effect("aborts an in-flight task mutation when its owner is interrupted", () =>
    Effect.gen(function* () {
      const requestStarted = yield* Deferred.make<void>();
      const requestAborted = yield* Deferred.make<void>();
      const fetchImpl: typeof fetch = (_input, init) =>
        new Promise<Response>((_resolve, reject) => {
          const signal = init?.signal;
          if (!signal) {
            reject(new Error("Kata mutation request did not include an AbortSignal"));
            return;
          }
          Deferred.doneUnsafe(requestStarted, Effect.void);
          signal.addEventListener(
            "abort",
            () => {
              Deferred.doneUnsafe(requestAborted, Effect.void);
              reject(new DOMException("request aborted", "AbortError"));
            },
            { once: true },
          );
        });
      const api = createKataTaskAPI({ fetchImpl });
      const fiber = yield* api
        .addComment({ project_id: 7, ref: "issue-1" }, "kenn-forge", "hello", { daemonId: "home" })
        .pipe(Effect.forkChild);

      yield* Deferred.await(requestStarted);
      yield* Fiber.interrupt(fiber);

      assert.isTrue(yield* Deferred.isDone(requestAborted));
    }),
  );

  test("describes task mutations as interruptible Effects", () => {
    const api = createKataTaskAPI({ fetchImpl: () => Promise.resolve(Response.json({ changed: true })) });

    expect(Effect.isEffect(api.createProject("New", { daemonId: "home" }))).toBe(true);
  });

  test("classifies a mutation transport failure as an unknown outcome", async () => {
    const api = createKataTaskAPI({ fetchImpl: () => Promise.reject(new Error("connection lost")) });

    await expect(
      runTaskEffect(api.addComment({ project_id: 7, ref: "issue-1" }, "kenn-forge", "hello", { daemonId: "home" })),
    ).rejects.toBeInstanceOf(KataMutationOutcomeUnknownError);
  });

  test("maps the Forge proxy outcome-unknown problem to mutation uncertainty", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues/issue-1/comments": {
        status: 502,
        body: {
          title: "Bad Gateway",
          status: 502,
          code: "mutationOutcomeUnknown",
          detail: "Kata could not confirm whether the mutation was applied.",
        },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(
      runTaskEffect(api.addComment({ project_id: 7, ref: "issue-1" }, "kenn-forge", "hello", { daemonId: "home" })),
    ).rejects.toBeInstanceOf(KataMutationOutcomeUnknownError);
  });

  test("treats a successful malformed mutation body as an unknown outcome", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues/issue-1/comments": { body: "not a mutation response" },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(
      runTaskEffect(api.addComment({ project_id: 7, ref: "issue-1" }, "kenn-forge", "hello", { daemonId: "home" })),
    ).rejects.toBeInstanceOf(KataMutationOutcomeUnknownError);
  });

  test("rejects empty successful acknowledgement objects for each identified mutation family", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects": { body: {} },
      "/api/v1/projects/7/issues": { body: {} },
      "/api/v1/projects/7/issues/issue-1/comments": { body: {} },
    });
    const api = createKataTaskAPI({ fetchImpl });
    const options = { daemonId: "home" };

    await expect(runTaskEffect(api.createProject("New", options))).rejects.toBeInstanceOf(
      KataMutationOutcomeUnknownError,
    );
    await expect(runTaskEffect(api.createIssue(7, "kenn-forge", { title: "Capture" }, options))).rejects.toBeInstanceOf(
      KataMutationOutcomeUnknownError,
    );
    await expect(
      runTaskEffect(api.addComment({ project_id: 7, ref: "issue-1" }, "kenn-forge", "hello", options)),
    ).rejects.toBeInstanceOf(KataMutationOutcomeUnknownError);
  });

  test("treats a successful mutation status as acknowledgement when changed is absent", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues/issue-1/comments": { body: { comment: { id: 1 } } },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(
      runTaskEffect(api.addComment({ project_id: 7, ref: "issue-1" }, "kenn-forge", "hello", { daemonId: "home" })),
    ).resolves.toEqual({ changed: true });
  });

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

    await runTaskEffect(api.createProject("New", accepted));
    await runTaskEffect(api.addComment(target, "kenn-forge", "hello", accepted));
    await runTaskEffect(api.moveIssue(target, "kenn-forge", "project-next", '"rev-1"', accepted));
    await runTaskEffect(api.recurrences(7, accepted));

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

    await expect(runTaskEffect(api.createProject("New", options))).resolves.toEqual({ changed: true });
    await expect(runTaskEffect(api.addComment(target, "kenn-forge", "hello", options))).resolves.toEqual({
      changed: true,
    });
    await expect(
      runTaskEffect(api.moveIssue(target, "kenn-forge", "project-next", '"rev-1"', options)),
    ).resolves.toEqual({
      changed: true,
    });
  });

  test("creates projects through the proxied daemon route", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects": { body: { project: project("project-new", "New") } },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(runTaskEffect(api.createProject("New", { daemonId: "home" }))).resolves.toEqual({ changed: true });
    expect(calls.map((call) => [proxyPath(call.url), call.method])).toEqual([["/api/v1/projects", "POST"]]);
  });

  test("keeps the Kata API path independent of Kenn Forge's configured base path", async () => {
    const previousBasePath = window.__BASE_PATH__;
    window.__BASE_PATH__ = "/kenn-forge/";
    try {
      const { calls, fetchImpl } = createFetchStub({
        "/api/v1/projects": { body: { project: project("project-new", "New") } },
      });
      const api = createKataTaskAPI({ fetchImpl });

      await runTaskEffect(api.createProject("New", { daemonId: "home" }));

      const requestURL = calls[0]?.url;
      if (requestURL === undefined) throw new Error("expected a Kata proxy request");
      expect(new URL(requestURL, window.location.origin).pathname).toBe(
        "/kenn-forge/api/v1/kata/proxy/api/v1/projects",
      );
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
      runTaskEffect(
        api.createIssue(
          7,
          "kenn-forge",
          { title: "Capture", metadata: { scheduled_on: "2026-05-20" } },
          { daemonId: "home" },
          "01KENN_FORGECAPTURE00000001",
        ),
      ),
    ).resolves.toEqual({ changed: true });

    expect(calls.map((call) => call.headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "home"]);
    expect(calls[0]!.headers.get("Idempotency-Key")).toBe("01KENN_FORGECAPTURE00000001");
    expect(calls[1]!.headers.get("Idempotency-Key")).toBe("01KENN_FORGECAPTURE00000001:metadata");
    expect(calls[1]!.headers.get("If-Match")).toBe('"rev-1"');
  });

  test("reports a created issue with unsent metadata as a known partial outcome", async () => {
    const { calls, fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues": {
        body: { changed: true, issue: issue("issue-capture", "Capture", "project-inbox") },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    const error = await runTaskEffect(
      api.createIssue(
        7,
        "kenn-forge",
        { title: "Capture", metadata: { scheduled_on: "2026-05-20" } },
        { daemonId: "home" },
      ),
    ).catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(KataMutationPartiallyAppliedError);
    expect(error).toMatchObject({
      operation: "create Kata issue with metadata",
      issueUID: "issue-capture",
      incompleteStep: "metadata",
    });
    expect(calls).toHaveLength(1);
  });

  test("treats a successful malformed issue create body as an unknown outcome", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects/7/issues": { body: "not an issue create response" },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(
      runTaskEffect(api.createIssue(7, "kenn-forge", { title: "Capture" }, { daemonId: "home" })),
    ).rejects.toBeInstanceOf(KataMutationOutcomeUnknownError);
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
      runTaskEffect(
        api.createIssue(
          7,
          "kenn-forge",
          { title: "Capture", metadata: { scheduled_on: "2026-05-20" } },
          { daemonId: "home" },
        ),
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
      "/api/v1/projects/7/issues/issue-1/labels/money?actor=kenn-forge": { body: { changed: true } },
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

    await runTaskEffect(api.addComment(target, "kenn-forge", "hello", options));
    await runTaskEffect(api.addLabel(target, "kenn-forge", "money", options));
    await runTaskEffect(api.removeLabel(target, "kenn-forge", "money", options));
    await runTaskEffect(api.assignOwner(target, "kenn-forge", "alice", options));
    await runTaskEffect(api.unassignOwner(target, "kenn-forge", options));
    await runTaskEffect(api.setPriority(target, "kenn-forge", 2, options));
    await runTaskEffect(api.closeIssue(target, "kenn-forge", { reason: "done", message: "done" }, options));
    await runTaskEffect(api.reopenIssue(target, "kenn-forge", options));
    await runTaskEffect(api.editIssue(target, "kenn-forge", { title: "Edited" }, options));
    await runTaskEffect(
      api.patchIssueMetadata(target, "kenn-forge", { scheduled_on: "2026-05-20" }, '"rev-1"', {
        daemonId: "home",
      }),
    );

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
      runTaskEffect(
        api.moveIssue({ project_id: 7, ref: "issue-1" }, "kenn-forge", "project-next", '"rev-1"', { daemonId: "home" }),
      ),
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
      "/api/v1/projects/7/recurrences/recurrence-existing?actor=kenn-forge": { status: 204 },
    });
    const api = createKataTaskAPI({ fetchImpl });
    const options = { daemonId: "home" };

    await expect(runTaskEffect(api.recurrences(7, { daemonId: "work" }))).resolves.toMatchObject({
      recurrences: [expect.objectContaining({ uid: "recurrence-created" })],
    });
    await expect(
      runTaskEffect(
        api.createRecurrence(
          7,
          {
            actor: "kenn-forge",
            rrule: "FREQ=WEEKLY",
            dtstart: "2026-05-20",
            timezone: "America/New_York",
            template: { title: "Weekly review" },
          },
          options,
        ),
      ),
    ).resolves.toMatchObject({ recurrence: expect.objectContaining({ uid: "recurrence-created" }) });
    await expect(runTaskEffect(api.showRecurrence(7, "recurrence-existing", options))).resolves.toMatchObject({
      etag: '"rev-2"',
    });
    await expect(
      runTaskEffect(
        api.patchRecurrence(7, "recurrence-existing", { actor: "kenn-forge", timezone: "UTC" }, '"rev-1"', options),
      ),
    ).resolves.toMatchObject({ changed: true, etag: '"rev-2"' });
    await expect(
      runTaskEffect(api.deleteRecurrence(7, "recurrence-existing", "kenn-forge", options, '"rev-2"')),
    ).resolves.toBeUndefined();

    expect(calls[0]!.headers.get(KATA_DAEMON_HEADER)).toBe("work");
    expect(calls.slice(1).every((call) => call.headers.get(KATA_DAEMON_HEADER) === "home")).toBe(true);
    expect(calls[3]!.headers.get("If-Match")).toBe('"rev-1"');
    expect(calls[4]!.headers.get("If-Match")).toBe('"rev-2"');
  });

  test("treats successful malformed recurrence mutations as unknown outcomes", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects/7/recurrences": { body: {} },
      "/api/v1/projects/7/recurrences/recurrence-existing": { body: {} },
    });
    const api = createKataTaskAPI({ fetchImpl });
    const options = { daemonId: "home" };

    await expect(
      runTaskEffect(
        api.createRecurrence(
          7,
          {
            actor: "kenn-forge",
            rrule: "FREQ=WEEKLY",
            dtstart: "2026-05-20",
            timezone: "America/New_York",
            template: { title: "Weekly review" },
          },
          options,
        ),
      ),
    ).rejects.toBeInstanceOf(KataMutationOutcomeUnknownError);
    await expect(
      runTaskEffect(
        api.patchRecurrence(7, "recurrence-existing", { actor: "kenn-forge", timezone: "UTC" }, '"rev-1"', options),
      ),
    ).rejects.toBeInstanceOf(KataMutationOutcomeUnknownError);
  });

  test("surfaces ordinary API errors", async () => {
    const { fetchImpl } = createFetchStub({
      "/api/v1/projects": {
        status: 503,
        body: { error: { code: "service_unavailable", message: "daemon unavailable" } },
      },
    });
    const api = createKataTaskAPI({ fetchImpl });

    await expect(runTaskEffect(api.createProject("New", { daemonId: "home" }))).rejects.toBeInstanceOf(
      KataTaskAPIError,
    );
  });
});
