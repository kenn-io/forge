import { describe, expect, it, vi } from "vite-plus/test";

import type { ReadKataEventStreamOptions } from "../../api/kata/eventStream.js";
import type { KataSnapshotIntent, KataWorkspaceSnapshotResponse } from "../../api/kata/snapshot.js";
import { createKataAuxiliaryAuthority } from "./kataAuxiliaryAuthority.svelte.js";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function snapshot(
  intent: KataSnapshotIntent,
  overrides: Partial<KataWorkspaceSnapshotResponse> = {},
): KataWorkspaceSnapshotResponse {
  const generation = overrides.generation ?? 1;
  return {
    server_instance_id: "server-a",
    daemon_id: intent.daemon_id ?? "home",
    intent: { scope: "global", authority: "all" },
    generation,
    invalidation_epoch: overrides.invalidation_epoch ?? generation,
    event_cursor: overrides.event_cursor ?? generation,
    fetched_at: "2026-07-21T10:00:00Z",
    projects: [],
    member_issue_uids: [`issue-${generation}`],
    issues: [
      {
        id: generation,
        uid: `issue-${generation}`,
        project_id: 7,
        project_uid: "project-a",
        project_name: "Project A",
        short_id: `a${generation}`,
        qualified_id: `Project A#a${generation}`,
        title: `Authority task ${generation}`,
        body: "Task body",
        status: generation === 1 ? "open" : "closed",
        metadata: {},
        revision: generation,
        author: "alice",
        labels: [],
        blocks: null,
        blocked_by: null,
        related: null,
        created_at: "2026-07-21T09:00:00Z",
        updated_at: "2026-07-21T09:30:00Z",
      },
    ],
    enrichment: intent.selected_issue_uid
      ? {
          selected_issue_uid: intent.selected_issue_uid,
          selected_detail: {
            etag: `"rev-${generation}"`,
            workspace_target: { available: false },
            detail: {
              issue: {
                id: generation,
                uid: intent.selected_issue_uid,
                project_id: 7,
                project_uid: "project-a",
                project_name: "Project A",
                short_id: `a${generation}`,
                qualified_id: `Project A#a${generation}`,
                title: `Authority task ${generation}`,
                body: "Task body",
                status: "open",
                metadata: {},
                revision: generation,
                author: "alice",
                labels: [],
                blocks: null,
                blocked_by: null,
                related: null,
                created_at: "2026-07-21T09:00:00Z",
                updated_at: "2026-07-21T09:30:00Z",
              },
              comments: [],
              labels: [],
              links: [],
              children: [],
            },
          },
        }
      : {},
    ...overrides,
  };
}

function streamHarness() {
  const streams: ReadKataEventStreamOptions[] = [];
  const readEventStream = vi.fn(async (options: ReadKataEventStreamOptions) => {
    streams.push(options);
    await new Promise<void>((resolve) => {
      options.signal?.addEventListener("abort", () => resolve(), { once: true });
    });
  });
  return { streams, readEventStream };
}

describe("Kata auxiliary authority", () => {
  it("shares one global-all accepted snapshot and replaces it once per invalidation", async () => {
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockImplementationOnce(async (intent) => snapshot(intent))
      .mockImplementationOnce(async (intent) => snapshot(intent, { generation: 2 }));
    const stream = streamHarness();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot, readEventStream: stream.readEventStream });

    await expect(authority.load("home")).resolves.toBe(true);
    expect(loadSnapshot).toHaveBeenCalledWith({ daemon_id: "home", scope: "global", authority: "all" });
    expect(authority.issues.map((issue) => issue.uid)).toEqual(["issue-1"]);
    expect(stream.streams).toHaveLength(1);

    await stream.streams[0]!.onMessage({
      kind: "invalidation",
      server_instance_id: "server-a",
      daemon_id: "home",
      epoch: 2,
      cursor: 2,
    });

    expect(loadSnapshot).toHaveBeenCalledTimes(2);
    expect(loadSnapshot.mock.calls[1]?.[0]).toEqual({ daemon_id: "home", scope: "global", authority: "all" });
    expect(authority.issues.map((issue) => issue.uid)).toEqual(["issue-2"]);
    authority.stop();
  });

  it("reloads the same global-all authority with selected enrichment", async () => {
    let generation = 0;
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) =>
      snapshot(intent, {
        generation: ++generation,
        invalidation_epoch: generation,
        event_cursor: generation,
      }),
    );
    const stream = streamHarness();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot, readEventStream: stream.readEventStream });

    await authority.load("home");
    const selected = await authority.selectIssue("issue-selected");

    expect(loadSnapshot.mock.calls[1]?.[0]).toEqual({
      daemon_id: "home",
      scope: "global",
      authority: "all",
      selected_issue_uid: "issue-selected",
    });
    expect(selected).toMatchObject({
      daemonID: "home",
      detail: { issue: { uid: "issue-selected" }, etag: '"rev-2"' },
    });

    await stream.streams[1]!.onMessage({
      kind: "invalidation",
      server_instance_id: "server-a",
      daemon_id: "home",
      epoch: 3,
      cursor: 3,
    });

    expect(loadSnapshot.mock.calls[2]?.[0]).toEqual({
      daemon_id: "home",
      scope: "global",
      authority: "all",
      selected_issue_uid: "issue-selected",
    });
    authority.stop();
  });

  it("selects against the desired daemon while its base load is still pending", async () => {
    const pendingWork = deferred<KataWorkspaceSnapshotResponse>();
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) => {
      if (intent.daemon_id === "work" && !intent.selected_issue_uid) return pendingWork.promise;
      return snapshot(intent, { generation: intent.daemon_id === "work" ? 2 : 1 });
    });
    const stream = streamHarness();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot, readEventStream: stream.readEventStream });
    await authority.load("home");

    const switching = authority.load("work");
    await vi.waitFor(() => expect(loadSnapshot).toHaveBeenCalledTimes(2));
    const selected = await authority.selectIssue("issue-work");

    expect(loadSnapshot.mock.calls[2]?.[0]).toMatchObject({
      daemon_id: "work",
      selected_issue_uid: "issue-work",
    });
    expect(selected.daemonID).toBe("work");
    pendingWork.resolve(snapshot({ scope: "global", authority: "all", daemon_id: "work" }, { generation: 2 }));
    await expect(switching).resolves.toBe(false);
    authority.stop();
  });

  it("keeps the desired daemon after its base load degrades", async () => {
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) => {
      if (intent.daemon_id === "work" && !intent.selected_issue_uid) throw new Error("work unavailable");
      return snapshot(intent, { generation: intent.daemon_id === "work" ? 2 : 1 });
    });
    const stream = streamHarness();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot, readEventStream: stream.readEventStream });
    await authority.load("home");
    await expect(authority.load("work")).rejects.toThrow("work unavailable");

    const selected = await authority.selectIssue("issue-work");

    expect(loadSnapshot.mock.calls[2]?.[0]).toMatchObject({
      daemon_id: "work",
      selected_issue_uid: "issue-work",
    });
    expect(selected.daemonID).toBe("work");
    authority.stop();
  });
});
