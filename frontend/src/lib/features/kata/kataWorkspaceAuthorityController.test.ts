import { describe, expect, it, vi } from "vite-plus/test";

import type { ReadKataEventStreamOptions } from "../../api/kata/eventStream.js";
import type { KataSnapshotIntent, KataWorkspaceSnapshotResponse } from "../../api/kata/snapshot.js";
import { createKataAuthorityStore } from "../../stores/kata-authority.svelte.js";
import type { KataWorkspaceAuthorityRequest } from "./kataWorkspaceAuthority.js";
import { createKataWorkspaceAuthorityController } from "./kataWorkspaceAuthorityController.svelte.js";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function request(overrides: Partial<KataSnapshotIntent> = {}): KataWorkspaceAuthorityRequest {
  return {
    intent: {
      daemon_id: "home",
      scope: "global",
      authority: "open",
      selected_issue_uid: "issue-a",
      graph_source_uid: "issue-root",
      ...overrides,
    },
    presentation: { text: "needle", owner: "alice", label: "urgent" },
  };
}

function snapshot(
  intent: KataSnapshotIntent,
  overrides: Partial<KataWorkspaceSnapshotResponse> = {},
): KataWorkspaceSnapshotResponse {
  return {
    server_instance_id: "server-a",
    daemon_id: intent.daemon_id ?? "home",
    intent: {
      scope: intent.scope,
      ...(intent.scope === "project" ? { project_uid: intent.project_uid! } : {}),
      authority: intent.authority,
    },
    generation: 1,
    invalidation_epoch: 3,
    event_cursor: 41,
    fetched_at: "2026-07-21T10:00:00Z",
    projects: [],
    member_issue_uids: ["issue-a"],
    issues: [
      {
        id: 1,
        uid: "issue-a",
        project_id: 7,
        project_uid: "project-a",
        project_name: "Project A",
        short_id: "a1",
        qualified_id: "Project A#a1",
        title: "Needle task",
        body: "Task body",
        status: "open",
        metadata: {},
        revision: 1,
        author: "alice",
        owner: "alice",
        labels: ["urgent"],
        blocks: null,
        blocked_by: null,
        related: null,
        created_at: "2026-07-21T09:00:00Z",
        updated_at: "2026-07-21T09:30:00Z",
      },
    ],
    ...(intent.graph_source_uid ? { graph_source_uid: intent.graph_source_uid } : {}),
    enrichment: intent.selected_issue_uid ? { selected_issue_uid: intent.selected_issue_uid } : {},
    ...overrides,
  };
}

function streamHarness() {
  const streams: ReadKataEventStreamOptions[] = [];
  const readEventStream = vi.fn(async (options: ReadKataEventStreamOptions) => {
    streams.push(options);
    await new Promise<void>((resolve) => {
      if (options.signal?.aborted) {
        resolve();
        return;
      }
      options.signal?.addEventListener("abort", () => resolve(), { once: true });
    });
  });
  return { streams, readEventStream };
}

describe("Kata workspace authority controller", () => {
  it("loads a complete request and starts replay from the accepted snapshot cursor", async () => {
    const desired = request();
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) => snapshot(intent));
    const authorityStore = createKataAuthorityStore({ loadSnapshot });
    const stream = streamHarness();
    const onSnapshotAccepted = vi.fn(async () => undefined);
    const controller = createKataWorkspaceAuthorityController({
      authorityStore,
      readEventStream: stream.readEventStream,
      onSnapshotAccepted,
    });

    await expect(controller.load(desired)).resolves.toBe(true);

    expect(loadSnapshot).toHaveBeenCalledWith(desired.intent);
    expect(authorityStore.presentation).toEqual(desired.presentation);
    expect(stream.streams).toHaveLength(1);
    expect(stream.streams[0]).toMatchObject({ daemonId: "home", lastEventID: 41 });
    expect(onSnapshotAccepted).toHaveBeenCalledWith(
      authorityStore.snapshot,
      expect.objectContaining({ source: "load" }),
    );
    controller.stop();
  });

  it("accepts frames only through the predicate and retries the exact desired intent once", async () => {
    const desired = request();
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockImplementationOnce(async (intent) => snapshot(intent))
      .mockImplementationOnce(async (intent) =>
        snapshot(intent, { generation: 2, invalidation_epoch: 4, event_cursor: 42 }),
      );
    const authorityStore = createKataAuthorityStore({ loadSnapshot });
    const stream = streamHarness();
    const controller = createKataWorkspaceAuthorityController({
      authorityStore,
      readEventStream: stream.readEventStream,
    });
    await controller.load(desired);

    await stream.streams[0]!.onMessage({
      kind: "invalidation",
      server_instance_id: "server-a",
      daemon_id: "foreign",
      epoch: 4,
      cursor: 42,
    });
    await stream.streams[0]!.onMessage({
      kind: "invalidation",
      server_instance_id: "server-a",
      daemon_id: "home",
      epoch: 4,
      cursor: 42,
    });

    expect(loadSnapshot).toHaveBeenCalledTimes(2);
    expect(loadSnapshot.mock.calls[1]?.[0]).toEqual(desired.intent);
    expect(authorityStore.snapshot).toMatchObject({ generation: 2, event_cursor: 42 });
    controller.stop();
  });

  it("reconciles an accepted new-server reset before resetting UI expansion", async () => {
    const calls: string[] = [];
    const desired = request();
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockImplementationOnce(async (intent) => snapshot(intent, { event_cursor: 91 }))
      .mockImplementationOnce(async (intent) =>
        snapshot(intent, {
          server_instance_id: "server-b",
          generation: 1,
          invalidation_epoch: 0,
          event_cursor: 1,
        }),
      );
    const authorityStore = createKataAuthorityStore({ loadSnapshot });
    const stream = streamHarness();
    const controller = createKataWorkspaceAuthorityController({
      authorityStore,
      readEventStream: stream.readEventStream,
      onSnapshotAccepted: async (_snapshot, context) => {
        if (context.source === "frame") calls.push("accepted");
      },
      resetIssueExpansion: () => calls.push("reset"),
    });
    await controller.load(desired);

    await stream.streams[0]!.onMessage({
      kind: "reset",
      server_instance_id: "server-b",
      daemon_id: "home",
      epoch: 0,
      cursor: 1,
    });

    expect(calls).toEqual(["accepted", "reset"]);
    expect(authorityStore.snapshot?.server_instance_id).toBe("server-b");
    controller.stop();
  });

  it("stops the old stream and fences a late frame reload during an intent switch", async () => {
    const home = request();
    const work = request({ daemon_id: "work", selected_issue_uid: "issue-work", graph_source_uid: "issue-work" });
    const lateHome = deferred<KataWorkspaceSnapshotResponse>();
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) => {
      if (loadSnapshot.mock.calls.length === 2) return lateHome.promise;
      return snapshot(intent, {
        daemon_id: intent.daemon_id ?? "home",
        ...(intent.daemon_id === "work"
          ? { enrichment: { selected_issue_uid: "issue-work" }, graph_source_uid: "issue-work", event_cursor: 9 }
          : {}),
      });
    });
    const authorityStore = createKataAuthorityStore({ loadSnapshot });
    const stream = streamHarness();
    const resetIssueExpansion = vi.fn();
    const controller = createKataWorkspaceAuthorityController({
      authorityStore,
      readEventStream: stream.readEventStream,
      resetIssueExpansion,
    });
    await controller.load(home);

    const oldDelivery = stream.streams[0]!.onMessage({
      kind: "reset",
      server_instance_id: "server-b",
      daemon_id: "home",
      epoch: 4,
      cursor: 42,
    });
    await vi.waitFor(() => expect(loadSnapshot).toHaveBeenCalledTimes(2));
    await expect(controller.load(work)).resolves.toBe(true);
    lateHome.resolve(snapshot(home.intent, { server_instance_id: "server-b", generation: 2, event_cursor: 42 }));
    await oldDelivery;

    expect(stream.streams[0]?.signal?.aborted).toBe(true);
    expect(stream.streams).toHaveLength(2);
    expect(stream.streams[1]).toMatchObject({ daemonId: "work", lastEventID: 9 });
    expect(authorityStore.snapshot).toMatchObject({ daemon_id: "work", selected_issue_uid: "issue-work" });
    expect(resetIssueExpansion).not.toHaveBeenCalled();
    controller.stop();
  });

  it("keeps the prior snapshot visible when a frame reload degrades", async () => {
    const desired = request();
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockImplementationOnce(async (intent) => snapshot(intent))
      .mockRejectedValueOnce(new Error("snapshot unavailable"));
    const authorityStore = createKataAuthorityStore({ loadSnapshot });
    const stream = streamHarness();
    const controller = createKataWorkspaceAuthorityController({
      authorityStore,
      readEventStream: stream.readEventStream,
    });
    await controller.load(desired);
    const accepted = authorityStore.snapshot;

    await expect(
      stream.streams[0]!.onMessage({
        kind: "invalidation",
        server_instance_id: "server-a",
        daemon_id: "home",
        epoch: 4,
        cursor: 42,
      }),
    ).resolves.toBeUndefined();

    expect(loadSnapshot).toHaveBeenCalledTimes(2);
    expect(authorityStore.state).toMatchObject({
      phase: "degraded",
      snapshot: accepted,
      intent: desired.intent,
      error: "snapshot unavailable",
    });
    expect(stream.streams[0]?.signal?.aborted).toBe(false);
    controller.stop();
  });
});
