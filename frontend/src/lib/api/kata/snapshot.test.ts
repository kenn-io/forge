import { Deferred, Effect, Fiber } from "effect";
import { describe, expect, it, vi } from "vite-plus/test";
import { GeneratedApi, makeGeneratedApiLayer } from "../generated-api.js";
import { createRuntimeClient } from "../runtime.js";

import {
  KATA_DAEMON_HEADER,
  fetchKataWorkspaceSnapshot,
  searchKataTaskReferences,
  type KataWorkspaceSnapshotResponse,
} from "./snapshot.js";

function snapshotResponse(): KataWorkspaceSnapshotResponse {
  return {
    server_instance_id: "server-a",
    daemon_id: "home",
    intent: { scope: "project", project_uid: "project-a", authority: "ready" },
    generation: 7,
    invalidation_epoch: 2,
    event_cursor: 41,
    fetched_at: "2026-07-20T12:00:00Z",
    projects: [],
    member_issue_uids: [],
    issues: [],
    enrichment: {},
  };
}

function requestURL(input: RequestInfo | URL): URL {
  if (typeof Request !== "undefined" && input instanceof Request) return new URL(input.url);
  return input instanceof URL ? input : new URL(String(input), window.location.origin);
}

function runSnapshot<A, E>(program: Effect.Effect<A, E, GeneratedApi>, fetchImpl: typeof fetch) {
  return Effect.runPromise(program.pipe(Effect.provide(makeGeneratedApiLayer(createRuntimeClient(fetchImpl)))));
}

describe("Kata snapshot client", () => {
  it("maps authority and independent enrichment intent onto the generated snapshot route", async () => {
    let seenRequest: Request | undefined;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seenRequest = input instanceof Request ? new Request(input, init) : new Request(requestURL(input), init);
      return Response.json(snapshotResponse());
    });

    await runSnapshot(
      fetchKataWorkspaceSnapshot({
        daemon_id: "home",
        scope: "project",
        project_uid: "project-a",
        authority: "ready",
        selected_issue_uid: "issue-selected",
        graph_source_uid: "issue-graph-root",
      }),
      fetchImpl,
    );

    const url = new URL(seenRequest!.url);
    expect(url.pathname).toBe("/api/v1/kata/tasks/snapshot");
    expect(Object.fromEntries(url.searchParams)).toEqual({
      scope: "project",
      project_uid: "project-a",
      authority: "ready",
      selected_issue_uid: "issue-selected",
      graph_source_uid: "issue-graph-root",
    });
    expect(seenRequest!.headers.get(KATA_DAEMON_HEADER)).toBe("home");
  });

  it("uses the effective default daemon only when the request omits a daemon", async () => {
    const seenHeaders: Array<Headers> = [];
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? new Request(input, init) : new Request(requestURL(input), init);
      seenHeaders.push(request.headers);
      return Response.json(snapshotResponse());
    });

    await runSnapshot(
      fetchKataWorkspaceSnapshot(
        { scope: "global", authority: "open" },
        {
          getDaemonId: () => undefined,
          getDefaultDaemonId: () => "home",
        },
      ),
      fetchImpl,
    );
    await runSnapshot(
      fetchKataWorkspaceSnapshot(
        { daemon_id: "work", scope: "global", authority: "open" },
        {
          getDaemonId: () => "home",
          getDefaultDaemonId: () => "home",
        },
      ),
      fetchImpl,
    );

    expect(seenHeaders.map((headers) => headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "work"]);
  });

  it("requests uncached authority explicitly for mutation reconciliation", async () => {
    let seenRequest: Request | undefined;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seenRequest = input instanceof Request ? new Request(input, init) : new Request(requestURL(input), init);
      return Response.json(snapshotResponse());
    });

    await runSnapshot(
      fetchKataWorkspaceSnapshot(
        { daemon_id: "home", scope: "global", authority: "all", selected_issue_uid: "issue-1" },
        { fresh: true },
      ),
      fetchImpl,
    );

    expect(new URL(seenRequest!.url).searchParams.get("fresh")).toBe("true");
  });

  it("maps an explicit all-status reference search onto the generated endpoint", async () => {
    let seenRequest: Request | undefined;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seenRequest = input instanceof Request ? new Request(input, init) : new Request(requestURL(input), init);
      return Response.json({
        server_instance_id: "server-a",
        daemon_id: "home",
        generation: 7,
        invalidation_epoch: 2,
        fetched_at: "2026-07-20T12:00:00Z",
        references: [],
      });
    });

    await runSnapshot(
      searchKataTaskReferences("project#a", { daemon_id: "home", limit: 12, status: "all" }),
      fetchImpl,
    );

    const url = new URL(seenRequest!.url);
    expect(url.pathname).toBe("/api/v1/kata/tasks/references");
    expect(Object.fromEntries(url.searchParams)).toEqual({ q: "project#a", limit: "12", status: "all" });
    expect(seenRequest!.headers.get(KATA_DAEMON_HEADER)).toBe("home");
  });

  it("aborts a pending reference search when its Effect is interrupted", async () => {
    await Effect.runPromise(
      Effect.gen(function* () {
        const requestStarted = yield* Deferred.make<AbortSignal>();
        const fetchImpl = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
          const request = _input instanceof Request ? _input : new Request(_input, init);
          const signal = request.signal;
          Deferred.doneUnsafe(requestStarted, Effect.succeed(signal));
          return new Promise<Response>((_resolve, reject) => {
            signal.addEventListener("abort", () => reject(signal.reason), { once: true });
          });
        });
        const fiber = yield* searchKataTaskReferences("project#a").pipe(
          Effect.provide(makeGeneratedApiLayer(createRuntimeClient(fetchImpl))),
          Effect.forkChild,
        );
        const requestSignal = yield* Deferred.await(requestStarted);
        expect(requestSignal.aborted).toBe(false);
        yield* Fiber.interrupt(fiber);
        expect(requestSignal.aborted).toBe(true);
      }),
    );
  });
});
