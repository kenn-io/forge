import { describe, expect, it, vi } from "vite-plus/test";

import type { KataSnapshotIntent, KataWorkspaceSnapshotResponse } from "../api/kata/snapshot.js";
import { createKataAuthorityStore } from "./kata-authority.svelte.js";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function task(uid: string, title: string, owner = "alice", labels: string[] = ["urgent"]) {
  return {
    id: uid === "issue-a" ? 1 : 2,
    uid,
    project_id: 1,
    project_uid: "project-a",
    project_name: "Project A",
    short_id: uid,
    qualified_id: `Project A#${uid}`,
    title,
    body: `${title} body`,
    status: "open",
    metadata: {},
    revision: 1,
    author: "fixture-user",
    owner,
    labels,
    blocks: [],
    blocked_by: [],
    related: [],
    created_at: "2026-07-20T10:00:00Z",
    updated_at: "2026-07-20T11:00:00Z",
  };
}

function snapshot(
  overrides: Partial<KataWorkspaceSnapshotResponse> & Pick<KataWorkspaceSnapshotResponse, "generation"> = {
    generation: 1,
  },
): KataWorkspaceSnapshotResponse {
  const { generation, ...rest } = overrides;
  return {
    server_instance_id: "server-a",
    daemon_id: "home",
    intent: { scope: "global", authority: "open" },
    generation,
    invalidation_epoch: 0,
    event_cursor: generation,
    fetched_at: "2026-07-20T12:00:00Z",
    projects: [
      {
        id: 1,
        uid: "project-a",
        name: "Project A",
        metadata: {},
        revision: 1,
        created_at: "2026-07-20T09:00:00Z",
        open_count: 2,
        closed_count: 0,
      },
    ],
    member_issue_uids: ["issue-a", "issue-b"],
    issues: [task("issue-a", "Needle task"), task("issue-b", "Other task", "bob", ["later"])],
    enrichment: {},
    ...rest,
  };
}

describe("Kata authority store", () => {
  it("normalizes and immutably installs a complete accepted snapshot", async () => {
    const accepted = snapshot({
      generation: 7,
      invalidation_epoch: 3,
      event_cursor: 42,
      graph_source_uid: "issue-b",
      enrichment: {
        selected_issue_uid: "issue-a",
        selected_detail: {
          detail: { issue: { uid: "issue-a", title: "Needle task" } },
          etag: '"rev-7"',
          workspace_target: { available: true, item_key: "kata:issue-a" },
        },
        selected_history: [
          {
            actor: "fixture-user",
            content_hash: "hash",
            created_at: "2026-07-20T11:30:00Z",
            event_id: 42,
            event_uid: "event-42",
            hlc_counter: 0,
            hlc_physical_ms: 1,
            issue_uid: "issue-a",
            origin_instance_uid: "kata-a",
            project_id: 1,
            project_name: "Project A",
            project_uid: "project-a",
            type: "issue_updated",
          },
        ],
        graph: { source_uid: "issue-b", depth: "full", hide_done: false, nodes: [], edges: [] },
        graph_fetched_at: "2026-07-20T12:00:00Z",
      },
    });
    const store = createKataAuthorityStore({ loadSnapshot: vi.fn(async () => accepted) });

    await expect(
      store.loadSnapshot({
        daemon_id: "home",
        scope: "global",
        authority: "open",
        selected_issue_uid: "issue-a",
        graph_source_uid: "issue-b",
      }),
    ).resolves.toBe(true);

    expect(store.state.phase).toBe("accepted");
    expect(store.snapshot).not.toBe(accepted);
    expect(store.snapshot).toMatchObject({
      projects: [{ uid: "project-a", name: "Project A", open_count: 2 }],
      member_issue_uids: accepted.member_issue_uids,
      issues: accepted.issues,
      generation: 7,
      invalidation_epoch: 3,
      event_cursor: 42,
      selected_issue_uid: "issue-a",
      graph_source_uid: "issue-b",
    });
    expect(Object.isFrozen(store.snapshot)).toBe(true);
    expect(Object.isFrozen(store.snapshot?.issues)).toBe(true);
    accepted.issues![0]!.title = "Mutated after acceptance";
    expect(store.snapshot?.issues[0]?.title).toBe("Needle task");
  });

  it("accepts authority data even when request-local enrichment contains errors", async () => {
    const accepted = snapshot({
      generation: 2,
      graph_source_uid: "issue-b",
      enrichment: {
        selected_issue_uid: "issue-a",
        errors: {
          detail: { code: "upstream_error", message: "Could not load selected task detail." },
          graph: { code: "upstream_error", message: "Could not load reachable graph." },
        },
      },
    });
    const store = createKataAuthorityStore({ loadSnapshot: vi.fn(async () => accepted) });

    await store.loadSnapshot({
      daemon_id: "home",
      scope: "global",
      authority: "open",
      selected_issue_uid: "issue-a",
      graph_source_uid: "issue-b",
    });

    expect(store.state.phase).toBe("accepted");
    expect(store.snapshot?.issues).toEqual(accepted.issues);
    expect(store.snapshot?.enrichment_errors).toEqual(accepted.enrichment.errors);
  });

  it("projects only issues that belong to the accepted authority membership", async () => {
    const accepted = snapshot({
      generation: 2,
      member_issue_uids: ["issue-a"],
    });
    const store = createKataAuthorityStore({ loadSnapshot: vi.fn(async () => accepted) });

    await store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "open" });

    expect(store.snapshot?.issues.map((issue) => issue.uid)).toEqual(["issue-a", "issue-b"]);
    expect(store.projection.issues.map((issue) => issue.uid)).toEqual(["issue-a"]);
  });

  it("accepts authority when requested selection is not an enrichment outcome", async () => {
    const store = createKataAuthorityStore({
      loadSnapshot: vi.fn(async () => snapshot({ generation: 2 })),
    });

    await expect(
      store.loadSnapshot({
        daemon_id: "home",
        scope: "global",
        authority: "open",
        selected_issue_uid: "issue-a",
      }),
    ).resolves.toBe(true);

    expect(store.snapshot?.selected_issue_uid).toBeUndefined();
    expect(store.state.phase).toBe("accepted");
  });

  it.each([
    ["a different requested selection", { selected_issue_uid: "issue-a" }, "issue-b"],
    ["an unrequested selection", {}, "issue-a"],
  ])("rejects %s returned as a non-empty enrichment outcome", async (_name, intentOverrides, selectedIssueUID) => {
    const store = createKataAuthorityStore({
      loadSnapshot: vi.fn(async () =>
        snapshot({
          generation: 2,
          enrichment: { selected_issue_uid: selectedIssueUID },
        }),
      ),
    });

    await expect(
      store.loadSnapshot({
        daemon_id: "home",
        scope: "global",
        authority: "open",
        ...intentOverrides,
      }),
    ).rejects.toThrow("does not match the current request intent");

    expect(store.snapshot).toBeNull();
    expect(store.state.phase).toBe("degraded");
  });

  it("requests a new authority for Ready and ignores the superseded Open response", async () => {
    const open = deferred<KataWorkspaceSnapshotResponse>();
    const ready = deferred<KataWorkspaceSnapshotResponse>();
    const loadSnapshot = vi.fn((intent: KataSnapshotIntent) =>
      intent.authority === "ready" ? ready.promise : open.promise,
    );
    const store = createKataAuthorityStore({ loadSnapshot });

    const openLoad = store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "open" });
    const readyLoad = store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "ready" });
    ready.resolve(snapshot({ generation: 8, intent: { scope: "global", authority: "ready" } }));
    await expect(readyLoad).resolves.toBe(true);
    open.resolve(snapshot({ generation: 9, intent: { scope: "global", authority: "open" } }));
    await expect(openLoad).resolves.toBe(false);

    expect(loadSnapshot.mock.calls.map(([intent]) => intent.authority)).toEqual(["open", "ready"]);
    expect(store.authorityKey).toEqual({
      daemon_id: "home",
      scope: "global",
      authority: "ready",
    });
  });

  it("rejects a lower generation for the same server and authority key", async () => {
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockResolvedValueOnce(snapshot({ generation: 9 }))
      .mockResolvedValueOnce(snapshot({ generation: 8 }));
    const store = createKataAuthorityStore({ loadSnapshot });
    const intent = { daemon_id: "home", scope: "global", authority: "open" } as const;

    await expect(store.loadSnapshot(intent)).resolves.toBe(true);
    await expect(store.loadSnapshot(intent)).resolves.toBe(false);

    expect(store.snapshot?.generation).toBe(9);
    expect(store.state.phase).toBe("accepted");
  });

  it("clears prior authority while keeping a stale cross-authority request pending for retry", async () => {
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockResolvedValueOnce(snapshot({ generation: 9, intent: { scope: "global", authority: "open" } }))
      .mockResolvedValueOnce(snapshot({ generation: 10, intent: { scope: "global", authority: "ready" } }))
      .mockResolvedValueOnce(snapshot({ generation: 8, intent: { scope: "global", authority: "open" } }))
      .mockResolvedValueOnce(snapshot({ generation: 11, intent: { scope: "global", authority: "open" } }));
    const store = createKataAuthorityStore({ loadSnapshot });

    await store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "open" });
    await store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "ready" });
    await expect(store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "open" })).resolves.toBe(false);

    expect(store.authorityKey).toBeNull();
    expect(store.state.phase).toBe("degraded");
    expect(store.state.intent?.authority).toBe("open");
    expect(store.snapshot).toBeNull();

    await expect(store.retry()).resolves.toBe(true);
    expect(loadSnapshot.mock.calls[3]?.[0].authority).toBe("open");
    expect(store.authorityKey?.authority).toBe("open");
  });

  it("retries the desired enrichment targets after a stale same-authority response", async () => {
    const acceptedIntent = {
      daemon_id: "home",
      scope: "global",
      authority: "open",
      selected_issue_uid: "issue-a",
      graph_source_uid: "issue-a",
    } as const;
    const desiredIntent = {
      ...acceptedIntent,
      selected_issue_uid: "issue-b",
      graph_source_uid: "issue-b",
    } as const;
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockResolvedValueOnce(
        snapshot({
          generation: 9,
          graph_source_uid: "issue-a",
          enrichment: { selected_issue_uid: "issue-a" },
        }),
      )
      .mockResolvedValueOnce(
        snapshot({
          generation: 8,
          graph_source_uid: "issue-b",
          enrichment: { selected_issue_uid: "issue-b" },
        }),
      )
      .mockResolvedValueOnce(
        snapshot({
          generation: 10,
          graph_source_uid: "issue-b",
          enrichment: { selected_issue_uid: "issue-b" },
        }),
      );
    const store = createKataAuthorityStore({ loadSnapshot });

    await expect(store.loadSnapshot(acceptedIntent)).resolves.toBe(true);
    await expect(store.loadSnapshot(desiredIntent)).resolves.toBe(false);

    expect(store.state.phase).toBe("degraded");
    expect(store.state.intent).toEqual(desiredIntent);
    expect(store.snapshot).toMatchObject({
      generation: 9,
      selected_issue_uid: "issue-a",
      graph_source_uid: "issue-a",
    });

    await expect(store.retry()).resolves.toBe(true);
    expect(loadSnapshot.mock.calls[2]?.[0]).toEqual(desiredIntent);
    expect(store.snapshot).toMatchObject({
      generation: 10,
      selected_issue_uid: "issue-b",
      graph_source_uid: "issue-b",
    });
  });

  it("accepts a lower generation after the Kenn Forge server instance changes", async () => {
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockResolvedValueOnce(snapshot({ generation: 9, server_instance_id: "server-a" }))
      .mockResolvedValueOnce(snapshot({ generation: 1, server_instance_id: "server-b" }));
    const store = createKataAuthorityStore({ loadSnapshot });
    const intent = { daemon_id: "home", scope: "global", authority: "open" } as const;

    await store.loadSnapshot(intent);
    await expect(store.loadSnapshot(intent)).resolves.toBe(true);

    expect(store.snapshot).toMatchObject({ server_instance_id: "server-b", generation: 1 });
  });

  it("clears accepted authority when the routed daemon is abandoned", async () => {
    const store = createKataAuthorityStore({ loadSnapshot: vi.fn(async () => snapshot({ generation: 4 })) });

    await store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "open" });
    store.abandon("Kata daemon missing is not configured.");

    expect(store.state).toMatchObject({
      phase: "abandoned",
      snapshot: null,
      intent: null,
      error: "Kata daemon missing is not configured.",
    });
  });

  it("abandons accepted authority and fences a pending response", async () => {
    const pending = deferred<KataWorkspaceSnapshotResponse>();
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockResolvedValueOnce(snapshot({ generation: 4 }))
      .mockImplementationOnce(() => pending.promise);
    const store = createKataAuthorityStore({ loadSnapshot });

    await store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "open" });
    const pendingLoad = store.loadSnapshot({ daemon_id: "work", scope: "global", authority: "open" });

    store.abandon("Kata daemon missing is not configured.");
    pending.resolve(snapshot({ daemon_id: "work", generation: 1 }));

    await expect(pendingLoad).resolves.toBe(false);
    expect(store.state).toMatchObject({
      phase: "abandoned",
      intent: null,
      error: "Kata daemon missing is not configured.",
      snapshot: null,
    });
    expect(store.authorityKey).toBeNull();
    await expect(store.retry()).resolves.toBe(false);
  });

  it("keeps graph source independent from selected detail", async () => {
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) =>
      snapshot({
        generation: 4,
        ...(intent.graph_source_uid ? { graph_source_uid: intent.graph_source_uid } : {}),
        enrichment: {
          ...(intent.selected_issue_uid ? { selected_issue_uid: intent.selected_issue_uid } : {}),
          selected_detail: { detail: {}, workspace_target: { available: false } },
          graph: {
            source_uid: intent.graph_source_uid ?? "",
            depth: "full",
            hide_done: false,
            nodes: [],
            edges: [],
          },
        },
      }),
    );
    const store = createKataAuthorityStore({ loadSnapshot });

    await store.loadSnapshot({
      daemon_id: "home",
      scope: "global",
      authority: "open",
      selected_issue_uid: "issue-a",
      graph_source_uid: "issue-b",
    });
    expect(loadSnapshot).toHaveBeenCalledTimes(1);
    expect(loadSnapshot).toHaveBeenCalledWith(
      expect.objectContaining({ selected_issue_uid: "issue-a", graph_source_uid: "issue-b" }),
    );
    expect(store.snapshot?.graph?.source_uid).toBe("issue-b");
  });

  it("projects text, owner, and label locally without loading another snapshot", async () => {
    const loadSnapshot = vi.fn(async () => snapshot({ generation: 3 }));
    const store = createKataAuthorityStore({ loadSnapshot });
    await store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "open" });

    store.updatePresentation({ text: "needle", owner: "alice", label: "urgent" });

    expect(loadSnapshot).toHaveBeenCalledTimes(1);
    expect(store.projection.issues.map((issue) => issue.uid)).toEqual(["issue-a"]);
  });

  it("includes owner and labels in free-text projection matching", async () => {
    const store = createKataAuthorityStore({ loadSnapshot: vi.fn(async () => snapshot({ generation: 3 })) });
    await store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "open" });

    store.updatePresentation({ text: "alice" });
    expect(store.projection.issues.map((issue) => issue.uid)).toEqual(["issue-a"]);

    store.updatePresentation({ text: "later" });
    expect(store.projection.issues.map((issue) => issue.uid)).toEqual(["issue-b"]);
  });

  it("rejects malformed authority responses before storing them", async () => {
    const malformed = snapshot({ generation: 3 });
    malformed.issues![0]!.status = "paused";
    const store = createKataAuthorityStore({ loadSnapshot: vi.fn(async () => malformed) });

    await expect(store.loadSnapshot({ daemon_id: "home", scope: "global", authority: "open" })).rejects.toThrow(
      /invalid/i,
    );

    expect(store.snapshot).toBeNull();
    expect(store.state.phase).toBe("degraded");
  });

  it("retries the explicit current intent instead of patching accepted authority", async () => {
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockRejectedValueOnce(new Error("snapshot unavailable"))
      .mockResolvedValueOnce(snapshot({ generation: 5 }));
    const store = createKataAuthorityStore({ loadSnapshot });
    const intent = { daemon_id: "home", scope: "global", authority: "open" } as const;

    await expect(store.loadSnapshot(intent)).rejects.toThrow("snapshot unavailable");
    expect(store.state.phase).toBe("degraded");
    await expect(store.retry()).resolves.toBe(true);

    expect(loadSnapshot).toHaveBeenCalledTimes(2);
    expect(loadSnapshot.mock.calls[1]?.[0]).toEqual(intent);
    expect(store.snapshot?.generation).toBe(5);
  });
});
