import { describe, expect, it, vi } from "vite-plus/test";

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

describe("Kata snapshot client", () => {
  it("maps authority and independent enrichment intent onto the generated snapshot route", async () => {
    let seenRequest: Request | undefined;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seenRequest = input instanceof Request ? new Request(input, init) : new Request(requestURL(input), init);
      return Response.json(snapshotResponse());
    });

    await fetchKataWorkspaceSnapshot(
      {
        daemon_id: "home",
        scope: "project",
        project_uid: "project-a",
        authority: "ready",
        selected_issue_uid: "issue-selected",
        graph_source_uid: "issue-graph-root",
      },
      { fetchImpl },
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

    await fetchKataWorkspaceSnapshot(
      { scope: "global", authority: "open" },
      {
        fetchImpl,
        getDaemonId: () => undefined,
        getDefaultDaemonId: () => "home",
      },
    );
    await fetchKataWorkspaceSnapshot(
      { daemon_id: "work", scope: "global", authority: "open" },
      {
        fetchImpl,
        getDaemonId: () => "home",
        getDefaultDaemonId: () => "home",
      },
    );

    expect(seenHeaders.map((headers) => headers.get(KATA_DAEMON_HEADER))).toEqual(["home", "work"]);
  });

  it("searches references through the separate generated global/open endpoint", async () => {
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

    await searchKataTaskReferences("project#a", { daemon_id: "home", limit: 12, fetchImpl });

    const url = new URL(seenRequest!.url);
    expect(url.pathname).toBe("/api/v1/kata/tasks/references");
    expect(Object.fromEntries(url.searchParams)).toEqual({ q: "project#a", limit: "12" });
    expect(seenRequest!.headers.get(KATA_DAEMON_HEADER)).toBe("home");
  });
});
