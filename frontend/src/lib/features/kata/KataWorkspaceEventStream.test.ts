import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataWorkspaceSnapshotResponse } from "../../api/kata/snapshot.js";
import type { KataTaskSummary } from "../../api/kata/taskTypes.js";
import KataWorkspace from "./KataWorkspace.svelte";
import { saveKataWorkspaceState } from "./kataWorkspacePersistence.js";
import {
  createWorkspaceAPI,
  deferred,
  detail,
  fetchedAt,
  initialIssues,
  projects,
  resetKataWorkspaceTestState,
} from "./test/KataWorkspaceSupport.js";

type SnapshotIssue = NonNullable<KataWorkspaceSnapshotResponse["issues"]>[number];
type SnapshotProject = NonNullable<KataWorkspaceSnapshotResponse["projects"]>[number];

function snapshot(
  issue: KataTaskSummary,
  overrides: Partial<KataWorkspaceSnapshotResponse> = {},
  selected = false,
): KataWorkspaceSnapshotResponse {
  return {
    server_instance_id: "server-a",
    daemon_id: "home",
    intent: { scope: "global", authority: "open" },
    generation: 1,
    invalidation_epoch: 1,
    event_cursor: 5,
    fetched_at: fetchedAt,
    projects: projects as SnapshotProject[],
    member_issue_uids: [issue.uid],
    issues: [issue as SnapshotIssue],
    enrichment: selected
      ? {
          selected_issue_uid: issue.uid,
          selected_detail: {
            etag: '"revision-1"',
            workspace_target: { available: false },
            detail: detail(issue.uid, [issue]),
          },
          selected_history: [],
        }
      : {},
    ...overrides,
  };
}

function compactFrame(
  cursor: number,
  overrides: Partial<{
    event: "kata.tasks.invalidated" | "kata.tasks.reset" | "issue.updated";
    server_instance_id: string;
    daemon_id: string;
    epoch: number;
  }> = {},
): Uint8Array {
  const event = overrides.event ?? "kata.tasks.invalidated";
  return new TextEncoder().encode(
    `id: ${cursor}\nevent: ${event}\ndata: ${JSON.stringify({
      server_instance_id: overrides.server_instance_id ?? "server-a",
      daemon_id: overrides.daemon_id ?? "home",
      epoch: overrides.epoch ?? 2,
      cursor,
    })}\n\n`,
  );
}

interface FetchHarness {
  stream: () => ReadableStreamDefaultController<Uint8Array> | undefined;
  streamCancelCount: () => number;
  snapshotRequests: Request[];
  setSnapshots: (snapshots: Array<KataWorkspaceSnapshotResponse | Response | Promise<Response>>) => void;
}

function installFetchHarness(
  initialSnapshots: Array<KataWorkspaceSnapshotResponse | Response | Promise<Response>>,
  daemons = [
    { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
    { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
  ],
): FetchHarness {
  let streamController: ReadableStreamDefaultController<Uint8Array> | undefined;
  let streamCancelCount = 0;
  let snapshots = [...initialSnapshots];
  const snapshotRequests: Request[] = [];

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const request =
      input instanceof Request
        ? new Request(input, init)
        : new Request(new URL(String(input), window.location.origin), init);
    const url = new URL(request.url, window.location.origin);
    if (url.pathname === "/api/v1/kata/daemons") {
      return Response.json({ daemons });
    }
    if (url.pathname === "/api/v1/kata/tasks/snapshot") {
      snapshotRequests.push(request);
      const next = snapshots.shift();
      if (!next) return new Response("unexpected snapshot request", { status: 500 });
      if (next instanceof Response) return next;
      if (next instanceof Promise) return next;
      return Response.json(next);
    }
    if (url.pathname === "/api/v1/kata/tasks/events") {
      return new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            streamController = controller;
          },
          cancel() {
            streamCancelCount += 1;
            streamController = undefined;
          },
        }),
        { status: 200, headers: { "Content-Type": "text/event-stream" } },
      );
    }
    return new Response("not found", { status: 404 });
  });

  return {
    stream: () => streamController,
    streamCancelCount: () => streamCancelCount,
    snapshotRequests,
    setSnapshots(next) {
      snapshots = [...next];
    },
  };
}

describe("KataWorkspace compact snapshot stream", () => {
  beforeEach(() => {
    resetKataWorkspaceTestState();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("starts the compact stream at the accepted snapshot cursor", async () => {
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([snapshot(initialIssues[0]!)]);

    render(KataWorkspace, { props: { api } });

    await waitFor(() => expect(harness.stream()).toBeTruthy());
    const streamRequest = vi
      .mocked(globalThis.fetch)
      .mock.calls.map(([input, init]) =>
        input instanceof Request
          ? new Request(input, init)
          : new Request(new URL(String(input), window.location.origin), init),
      )
      .find((request) => new URL(request.url, window.location.origin).pathname === "/api/v1/kata/tasks/events");
    expect(streamRequest?.headers.get("X-Middleman-Kata-Daemon")).toBe("home");
    expect(streamRequest?.headers.get("Last-Event-ID")).toBe("5");
  });

  it("reloads the exact accepted intent once and replaces row and detail atomically", async () => {
    const replacement = { ...initialIssues[0]!, title: "Replacement detail" };
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      snapshot(initialIssues[0]!, {}, true),
      snapshot(replacement, { generation: 2, invalidation_epoch: 2, event_cursor: 6 }, true),
    ]);

    render(KataWorkspace, { props: { api, selectedIssueUID: initialIssues[0]!.uid } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    await waitFor(() => expect(harness.stream()).toBeTruthy());
    harness.stream()?.enqueue(compactFrame(6));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Replacement detail" })).toBeTruthy());
    expect(screen.queryByRole("button", { name: /Pay rent/ })).toBeNull();
    expect(harness.snapshotRequests.map((request) => Object.fromEntries(new URL(request.url).searchParams))).toEqual([
      { scope: "global", authority: "open", selected_issue_uid: initialIssues[0]!.uid },
      { scope: "global", authority: "open", selected_issue_uid: initialIssues[0]!.uid },
    ]);
  });

  it("fences mutations while event-driven replacement authority is loading", async () => {
    const replacement = { ...initialIssues[0]!, title: "Replacement detail" };
    const pendingReplacement = deferred<Response>();
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([snapshot(initialIssues[0]!), pendingReplacement.promise]);

    try {
      render(KataWorkspace, { props: { api } });
      await waitFor(() => expect(harness.stream()).toBeTruthy());
      harness.stream()?.enqueue(compactFrame(6));

      await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
      expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(true);

      pendingReplacement.resolve(
        Response.json(snapshot(replacement, { generation: 2, invalidation_epoch: 2, event_cursor: 6 })),
      );
      await screen.findByRole("button", { name: new RegExp(replacement.title) });
      expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false);
    } finally {
      pendingReplacement.resolve(
        Response.json(snapshot(replacement, { generation: 2, invalidation_epoch: 2, event_cursor: 6 })),
      );
    }
  });

  it("clears a selected member when replacement authority drops its membership", async () => {
    const selected = initialIssues[0]!;
    const onRouteStateChange = vi.fn();
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      snapshot(selected, {}, true),
      snapshot(selected, {
        generation: 2,
        invalidation_epoch: 2,
        event_cursor: 6,
        member_issue_uids: [],
        enrichment: {},
      }),
    ]);

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid, onRouteStateChange } });
    await waitFor(() => expect(screen.getByRole("heading", { name: selected.title })).toBeTruthy());
    await waitFor(() => expect(harness.stream()).toBeTruthy());
    harness.stream()?.enqueue(compactFrame(6));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    await waitFor(() => expect(onRouteStateChange).toHaveBeenCalledWith({ issue: null }, { replace: true }));
    expect(screen.queryByRole("button", { name: new RegExp(selected.title) })).toBeNull();
    expect(screen.queryByRole("heading", { name: selected.title })).toBeNull();
    expect(screen.getByText("Select a task")).toBeTruthy();
  });

  it.each([
    ["stale", compactFrame(5, { epoch: 1 })],
    ["foreign daemon", compactFrame(6, { daemon_id: "work" })],
    ["foreign server invalidation", compactFrame(6, { server_instance_id: "server-b" })],
    ["raw issue event", compactFrame(6, { event: "issue.updated" })],
  ])("ignores a %s frame without issuing a snapshot request", async (_name, frame) => {
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([snapshot(initialIssues[0]!)]);

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(harness.stream()).toBeTruthy());
    harness.stream()?.enqueue(frame);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(harness.snapshotRequests).toHaveLength(1);
  });

  it("accepts a new-server reset even when its numeric cursor is lower", async () => {
    const replacement = { ...initialIssues[1]!, title: "After restart" };
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      snapshot(initialIssues[0]!, { generation: 9, invalidation_epoch: 9, event_cursor: 41 }),
      snapshot(replacement, {
        server_instance_id: "server-b",
        generation: 1,
        invalidation_epoch: 1,
        event_cursor: 2,
      }),
    ]);

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(harness.stream()).toBeTruthy());
    harness.stream()?.enqueue(
      compactFrame(2, {
        event: "kata.tasks.reset",
        server_instance_id: "server-b",
        epoch: 1,
      }),
    );

    await waitFor(() => expect(screen.getByRole("button", { name: /After restart/ })).toBeTruthy());
    expect(harness.snapshotRequests).toHaveLength(2);
  });

  it("keeps the prior accepted snapshot visible when an invalidation reload fails", async () => {
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      snapshot(initialIssues[0]!),
      new Response(JSON.stringify({ error: "snapshot unavailable" }), {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }),
    ]);

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(harness.stream()).toBeTruthy());
    harness.stream()?.enqueue(compactFrame(6));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
  });

  it("recovers an initial snapshot failure through an in-app snapshot retry", async () => {
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      new Response(JSON.stringify({ error: "snapshot unavailable" }), {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }),
      snapshot(initialIssues[0]!),
    ]);

    render(KataWorkspace, { props: { api } });

    const retry = await screen.findByRole("button", { name: "Retry Kata snapshot" });
    expect((await screen.findByRole("alert")).textContent).toContain("Could not load Kata task snapshot (503)");
    await fireEvent.click(retry);

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    await screen.findByRole("button", { name: /Pay rent/ });
    expect(screen.queryByRole("button", { name: "Retry Kata snapshot" })).toBeNull();
  });

  it("treats mutation acknowledgement as success while fencing actions until snapshot recovery", async () => {
    const selected = initialIssues[0]!;
    const replacement = { ...selected, title: "Comment accepted by snapshot" };
    const { api } = createWorkspaceAPI();
    api.addComment = vi.fn(async () => ({ changed: true }));
    const harness = installFetchHarness([
      snapshot(selected, {}, true),
      new Response(JSON.stringify({ error: "refresh unavailable" }), {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }),
      snapshot(replacement, { generation: 2, invalidation_epoch: 2, event_cursor: 6 }, true),
    ]);

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });
    await screen.findByRole("heading", { name: selected.title });
    await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
      target: { value: "Persist this once" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));

    await waitFor(() => expect(api.addComment).toHaveBeenCalledOnce());
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    expect((await screen.findByRole("alert")).textContent).toContain("saved");
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(true);

    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    expect(api.addComment).toHaveBeenCalledOnce();

    await fireEvent.click(screen.getByRole("button", { name: "Retry Kata snapshot" }));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
    await screen.findByRole("heading", { name: replacement.title });
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false),
    );
    expect(harness.snapshotRequests.map((request) => Object.fromEntries(new URL(request.url).searchParams))).toEqual([
      { scope: "global", authority: "open", selected_issue_uid: selected.uid },
      { scope: "global", authority: "open", selected_issue_uid: selected.uid },
      { scope: "global", authority: "open", selected_issue_uid: selected.uid },
    ]);
  });

  it("retries a newer failed selection instead of the superseded mutation refresh intent", async () => {
    const source = initialIssues[0]!;
    const target = initialIssues[1]!;
    const catalog = [source, target] as SnapshotIssue[];
    const { api } = createWorkspaceAPI();
    api.addComment = vi.fn(async () => ({ changed: true }));
    const onSelectedIssueChange = vi.fn();
    const harness = installFetchHarness([
      snapshot(
        source,
        {
          generation: 9,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
      new Response(JSON.stringify({ error: "mutation refresh unavailable" }), {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }),
      new Response(JSON.stringify({ error: "selection unavailable" }), {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }),
      snapshot(
        target,
        {
          generation: 10,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
    ]);

    render(KataWorkspace, {
      props: { api, selectedIssueUID: source.uid, onSelectedIssueChange },
    });
    await screen.findByRole("heading", { name: source.title });
    await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
      target: { value: "Persist before navigating" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    await screen.findByRole("alert");
    await fireEvent.click(screen.getByRole("button", { name: new RegExp(target.title) }));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
    await waitFor(() => expect(screen.queryByText("Loading snapshot")).toBeNull());
    await fireEvent.click(screen.getByRole("button", { name: "Retry Kata snapshot" }));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(4));

    expect(Object.fromEntries(new URL(harness.snapshotRequests[3]!.url).searchParams)).toEqual({
      scope: "global",
      authority: "open",
      selected_issue_uid: target.uid,
    });
    await screen.findByRole("heading", { name: target.title });
    expect(onSelectedIssueChange).toHaveBeenCalledWith(target.uid);
  });

  it("revalidates the newest selection when an older mutation acknowledges late", async () => {
    const source = initialIssues[0]!;
    const target = initialIssues[1]!;
    const catalog = [source, target] as SnapshotIssue[];
    const mutation = deferred<{ changed: boolean }>();
    const { api } = createWorkspaceAPI();
    api.addComment = vi.fn(() => mutation.promise);
    const onSelectedIssueChange = vi.fn();
    const onRouteStateChange = vi.fn();
    const harness = installFetchHarness([
      snapshot(
        source,
        {
          generation: 9,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
      snapshot(
        target,
        {
          generation: 10,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
      snapshot(
        target,
        {
          generation: 11,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
    ]);

    const { rerender } = render(KataWorkspace, {
      props: {
        api,
        selectedIssueUID: source.uid,
        onSelectedIssueChange,
        onRouteStateChange,
      },
    });
    await screen.findByRole("heading", { name: source.title });
    await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
      target: { value: "Persist before navigating" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    await waitFor(() => expect(api.addComment).toHaveBeenCalledOnce());
    expect((screen.getByRole("region", { name: "Task detail" }) as HTMLElement & { inert: boolean }).inert).toBe(false);
    expect((screen.getByRole("button", { name: "Add comment" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "More actions" }) as HTMLButtonElement).matches(":disabled")).toBe(true);
    expect((screen.getByRole("textbox", { name: "Comment" }) as HTMLTextAreaElement).value).toBe(
      "Persist before navigating",
    );

    await fireEvent.click(screen.getByRole("button", { name: new RegExp(target.title) }));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    await screen.findByRole("heading", { name: target.title });
    await waitFor(() => expect(onSelectedIssueChange).toHaveBeenCalledWith(target.uid));
    await rerender({
      api,
      selectedIssueUID: target.uid,
      onSelectedIssueChange,
      onRouteStateChange,
    });

    mutation.resolve({ changed: true });
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
    await screen.findByRole("heading", { name: target.title });

    expect(api.addComment).toHaveBeenCalledOnce();
    expect(
      harness.snapshotRequests.slice(1).map((request) => Object.fromEntries(new URL(request.url).searchParams)),
    ).toEqual([
      { scope: "global", authority: "open", selected_issue_uid: target.uid },
      { scope: "global", authority: "open", selected_issue_uid: target.uid },
    ]);
    expect(onSelectedIssueChange).toHaveBeenCalledWith(target.uid);
    expect(onRouteStateChange).not.toHaveBeenCalledWith({ issue: null }, { replace: true });
  });

  it("transfers an in-flight selection notification to post-acknowledgement revalidation", async () => {
    const source = initialIssues[0]!;
    const target = initialIssues[1]!;
    const catalog = [source, target] as SnapshotIssue[];
    const mutation = deferred<{ changed: boolean }>();
    const pendingSelection = deferred<Response>();
    const { api } = createWorkspaceAPI();
    api.addComment = vi.fn(() => mutation.promise);
    const onSelectedIssueChange = vi.fn();
    const onRouteStateChange = vi.fn();
    const harness = installFetchHarness([
      snapshot(
        source,
        {
          generation: 9,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
      pendingSelection.promise,
      snapshot(
        target,
        {
          generation: 11,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
    ]);

    try {
      render(KataWorkspace, {
        props: {
          api,
          selectedIssueUID: source.uid,
          onSelectedIssueChange,
          onRouteStateChange,
        },
      });
      await screen.findByRole("heading", { name: source.title });
      await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
        target: { value: "Persist during selection" },
      });
      await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
      await waitFor(() => expect(api.addComment).toHaveBeenCalledOnce());

      await fireEvent.click(screen.getByRole("button", { name: new RegExp(target.title) }));
      await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));

      mutation.resolve({ changed: true });
      await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
      await screen.findByRole("heading", { name: target.title });

      expect(api.addComment).toHaveBeenCalledOnce();
      expect(
        harness.snapshotRequests.slice(1).map((request) => Object.fromEntries(new URL(request.url).searchParams)),
      ).toEqual([
        { scope: "global", authority: "open", selected_issue_uid: target.uid },
        { scope: "global", authority: "open", selected_issue_uid: target.uid },
      ]);
      expect(onSelectedIssueChange).toHaveBeenCalledTimes(1);
      expect(onSelectedIssueChange).toHaveBeenCalledWith(target.uid);
      expect(onRouteStateChange).not.toHaveBeenCalledWith({ issue: null }, { replace: true });
    } finally {
      mutation.resolve({ changed: true });
      pendingSelection.resolve(
        Response.json(
          snapshot(
            target,
            {
              generation: 10,
              member_issue_uids: catalog.map((issue) => issue.uid),
              issues: catalog,
            },
            true,
          ),
        ),
      );
    }
  });

  it("keeps recovery on a newer selection that supersedes mutation replacement", async () => {
    const source = initialIssues[0]!;
    const target = initialIssues[1]!;
    const catalog = [source, target] as SnapshotIssue[];
    const pendingMutationReplacement = deferred<Response>();
    const pendingSelection = deferred<Response>();
    const { api } = createWorkspaceAPI();
    api.addComment = vi.fn(async () => ({ changed: true }));
    const onSelectedIssueChange = vi.fn();
    const onRouteStateChange = vi.fn();
    const harness = installFetchHarness([
      snapshot(
        source,
        {
          generation: 9,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
      pendingMutationReplacement.promise,
      pendingSelection.promise,
      snapshot(
        target,
        {
          generation: 11,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
    ]);

    try {
      render(KataWorkspace, {
        props: {
          api,
          selectedIssueUID: source.uid,
          onSelectedIssueChange,
          onRouteStateChange,
        },
      });
      await screen.findByRole("heading", { name: source.title });
      await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
        target: { value: "Persist before selecting" },
      });
      await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
      await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));

      await fireEvent.click(screen.getByRole("button", { name: new RegExp(target.title) }));
      await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
      pendingMutationReplacement.resolve(
        Response.json(
          snapshot(
            source,
            {
              generation: 10,
              member_issue_uids: catalog.map((issue) => issue.uid),
              issues: catalog,
            },
            true,
          ),
        ),
      );

      const retry = await screen.findByRole("button", { name: "Retry Kata snapshot" });
      await fireEvent.click(retry);
      await waitFor(() => expect(harness.snapshotRequests).toHaveLength(4));
      await screen.findByRole("heading", { name: target.title });
      pendingSelection.resolve(
        Response.json(
          snapshot(
            target,
            {
              generation: 10,
              member_issue_uids: catalog.map((issue) => issue.uid),
              issues: catalog,
            },
            true,
          ),
        ),
      );
      await new Promise((resolve) => setTimeout(resolve, 0));

      expect(api.addComment).toHaveBeenCalledOnce();
      expect(
        harness.snapshotRequests.slice(1).map((request) => Object.fromEntries(new URL(request.url).searchParams)),
      ).toEqual([
        { scope: "global", authority: "open", selected_issue_uid: source.uid },
        { scope: "global", authority: "open", selected_issue_uid: target.uid },
        { scope: "global", authority: "open", selected_issue_uid: target.uid },
      ]);
      expect(onSelectedIssueChange).toHaveBeenCalledTimes(1);
      expect(onSelectedIssueChange).toHaveBeenCalledWith(target.uid);
      expect(onRouteStateChange).not.toHaveBeenCalledWith({ issue: null }, { replace: true });
    } finally {
      pendingMutationReplacement.resolve(Response.json(snapshot(source, { generation: 10 }, true)));
      pendingSelection.resolve(
        Response.json(
          snapshot(
            target,
            {
              generation: 10,
              member_issue_uids: catalog.map((issue) => issue.uid),
              issues: catalog,
            },
            true,
          ),
        ),
      );
    }
  });

  it("recovers the nominal daemon after a failed daemon switch without restoring stale authority", async () => {
    const home = snapshot(initialIssues[0]!);
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      home,
      new Response(JSON.stringify({ error: "work unavailable" }), {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }),
      snapshot(initialIssues[0]!, { generation: 2, invalidation_epoch: 2, event_cursor: 6 }),
    ]);

    render(KataWorkspace, { props: { api } });
    await screen.findByRole("button", { name: /Pay rent/ });
    await fireEvent.click(screen.getByRole("button", { name: /Switch Kata daemon: home/ }));
    await fireEvent.click(await screen.findByRole("menuitemradio", { name: /work/ }));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    expect(screen.queryByRole("button", { name: /Pay rent/ })).toBeNull();
    expect((await screen.findByRole("alert")).textContent).toContain("Could not load Kata task snapshot (503)");

    await fireEvent.click(screen.getByRole("button", { name: "Retry Kata snapshot" }));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
    expect(harness.snapshotRequests[1]!.headers.get("X-Middleman-Kata-Daemon")).toBe("work");
    expect(harness.snapshotRequests[2]!.headers.get("X-Middleman-Kata-Daemon")).toBe("home");
    await screen.findByRole("button", { name: /Pay rent/ });
  });

  it("drops a source-only project scope when switching to a daemon without saved state", async () => {
    const source = initialIssues[0]!;
    const target = initialIssues[1]!;
    const onRouteStateChange = vi.fn();
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      snapshot(
        source,
        {
          intent: { scope: "project", project_uid: source.project_uid, authority: "open" },
        },
        true,
      ),
      snapshot(target, {
        daemon_id: "work",
        generation: 1,
        event_cursor: 7,
      }),
    ]);

    render(KataWorkspace, {
      props: {
        api,
        routeScopeUID: source.project_uid,
        selectedIssueUID: source.uid,
        onRouteStateChange,
      },
    });
    await screen.findByRole("heading", { name: source.title });

    await fireEvent.click(screen.getByRole("button", { name: /Switch Kata daemon: home/ }));
    await fireEvent.click(await screen.findByRole("menuitemradio", { name: /work/ }));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    expect(Object.fromEntries(new URL(harness.snapshotRequests[1]!.url).searchParams)).toEqual({
      scope: "global",
      authority: "open",
    });
    await screen.findByRole("button", { name: new RegExp(target.title) });
    await waitFor(() =>
      expect(onRouteStateChange).toHaveBeenCalledWith(
        { view: null, scope: null, issue: null, daemon: null },
        { replace: true },
      ),
    );
  });

  it("falls back to global defaults when the target daemon's saved project no longer exists", async () => {
    const source = initialIssues[0]!;
    const target = initialIssues[1]!;
    saveKataWorkspaceState("work", {
      view: "today",
      filters: {
        scope: { kind: "project", project_uid: "project-removed" },
        status: "ready",
        owner: "work-owner",
        label: "work-label",
        query: "work-query",
      },
      selectedIssueUID: "issue-removed",
    });
    const onRouteStateChange = vi.fn();
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      snapshot(source),
      Response.json(
        {
          type: "about:blank",
          title: "Kata project not found",
          status: 404,
          code: "projectNotFound",
          detail: "Kata project not found",
        },
        { status: 404 },
      ),
      snapshot(target, {
        daemon_id: "work",
        generation: 1,
        event_cursor: 7,
      }),
    ]);

    render(KataWorkspace, { props: { api, onRouteStateChange } });
    await screen.findByRole("button", { name: new RegExp(source.title) });

    await fireEvent.click(screen.getByRole("button", { name: /Switch Kata daemon: home/ }));
    await fireEvent.click(await screen.findByRole("menuitemradio", { name: /work/ }));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
    expect(Object.fromEntries(new URL(harness.snapshotRequests[1]!.url).searchParams)).toEqual({
      scope: "project",
      authority: "ready",
      project_uid: "project-removed",
      selected_issue_uid: "issue-removed",
    });
    expect(Object.fromEntries(new URL(harness.snapshotRequests[2]!.url).searchParams)).toEqual({
      scope: "global",
      authority: "open",
    });
    await screen.findByRole("button", { name: new RegExp(target.title) });
    await waitFor(() =>
      expect(onRouteStateChange).toHaveBeenCalledWith(
        { view: null, scope: null, issue: null, daemon: null },
        { replace: true },
      ),
    );
  });

  it("retries changed selection and graph intent after a stale generation response", async () => {
    const source = initialIssues[0]!;
    const target = initialIssues[1]!;
    const catalog = [source, target] as SnapshotIssue[];
    const graph = {
      source_uid: target.uid,
      depth: "full" as const,
      hide_done: false,
      nodes: catalog,
      edges: [],
      unresolved_refs: [],
    };
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      snapshot(
        source,
        {
          generation: 9,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
        },
        true,
      ),
      snapshot(
        source,
        {
          generation: 10,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
          graph_source_uid: target.uid,
          enrichment: {
            selected_issue_uid: source.uid,
            selected_detail: {
              etag: '"revision-1"',
              workspace_target: { available: false },
              detail: detail(source.uid, [source, target]),
            },
            selected_history: [],
            graph,
            graph_fetched_at: fetchedAt,
          },
        },
        true,
      ),
      snapshot(
        target,
        {
          generation: 9,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
          graph_source_uid: target.uid,
          enrichment: {
            selected_issue_uid: target.uid,
            selected_detail: {
              etag: '"revision-1"',
              workspace_target: { available: false },
              detail: detail(target.uid, [source, target]),
            },
            selected_history: [],
            graph,
            graph_fetched_at: fetchedAt,
          },
        },
        true,
      ),
      snapshot(
        target,
        {
          generation: 11,
          member_issue_uids: catalog.map((issue) => issue.uid),
          issues: catalog,
          graph_source_uid: target.uid,
          enrichment: {
            selected_issue_uid: target.uid,
            selected_detail: {
              etag: '"revision-1"',
              workspace_target: { available: false },
              detail: detail(target.uid, [source, target]),
            },
            selected_history: [],
            graph,
            graph_fetched_at: fetchedAt,
          },
        },
        true,
      ),
    ]);

    render(KataWorkspace, { props: { api, selectedIssueUID: source.uid } });
    await screen.findByRole("heading", { name: source.title });
    const targetRow = screen.getByRole("button", { name: new RegExp(target.title) });
    await fireEvent.click(targetRow.parentElement!.querySelector('button[aria-label="Open reachable graph"]')!);
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));

    await fireEvent.click(await screen.findByRole("button", { name: new RegExp(target.title) }));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
    expect((await screen.findByRole("alert")).textContent).toContain("generation moved backwards");

    await fireEvent.click(screen.getByRole("button", { name: "Retry Kata snapshot" }));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(4));
    await screen.findByRole("heading", { name: target.title });
    expect(screen.getByRole("button", { name: "Back to task list" })).toBeTruthy();
    expect(
      harness.snapshotRequests.slice(2).map((request) => Object.fromEntries(new URL(request.url).searchParams)),
    ).toEqual([
      {
        scope: "global",
        authority: "open",
        selected_issue_uid: target.uid,
        graph_source_uid: target.uid,
      },
      {
        scope: "global",
        authority: "open",
        selected_issue_uid: target.uid,
        graph_source_uid: target.uid,
      },
    ]);
  });

  it("does not render prior Open authority when a Ready authority load fails", async () => {
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      snapshot(initialIssues[0]!),
      new Response(JSON.stringify({ error: "ready unavailable" }), {
        status: 503,
        headers: { "Content-Type": "application/json" },
      }),
    ]);

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy());

    await fireEvent.click(screen.getByRole("combobox", { name: "Status: Open" }));
    await fireEvent.click(screen.getByRole("option", { name: "Ready" }));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
    expect(screen.queryByRole("button", { name: /Pay rent/ })).toBeNull();
  });

  it("fences a pending old-daemon snapshot after a new daemon intent starts", async () => {
    const pendingHome = deferred<Response>();
    const home = snapshot(initialIssues[0]!);
    const work = snapshot(initialIssues[1]!, { daemon_id: "work", generation: 1, event_cursor: 7 });
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([home, pendingHome.promise, work]);

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(harness.stream()).toBeTruthy());
    harness.stream()?.enqueue(compactFrame(6));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));

    harness.setSnapshots([work]);
    const switcher = screen.getByRole("button", { name: /Switch Kata daemon: home/ });
    switcher.click();
    const workOption = await screen.findByRole("menuitemradio", { name: /work/ });
    workOption.click();
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
    pendingHome.resolve(Response.json(snapshot(initialIssues[0]!, { generation: 2, event_cursor: 6 })));

    await waitFor(() => expect(screen.getByRole("button", { name: /Email Susan re: Q3/ })).toBeTruthy());
    expect(screen.queryByRole("button", { name: /Pay rent/ })).toBeNull();
  });

  it("reprojects query, owner, and label filters without reloading snapshot authority", async () => {
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([snapshot(initialIssues[0]!)]);

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(1));

    await fireEvent.input(screen.getByRole("searchbox", { name: "Search tasks" }), {
      target: { value: "Pay" },
    });
    await fireEvent.input(screen.getByRole("textbox", { name: "Owner" }), {
      target: { value: "fixture-user" },
    });
    await fireEvent.input(screen.getByRole("textbox", { name: "Label" }), {
      target: { value: "home" },
    });
    await Promise.resolve();

    expect(harness.snapshotRequests).toHaveLength(1);
    expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy();
  });

  it("reconciles rerendered route authority and selection through one superseding snapshot", async () => {
    const { api } = createWorkspaceAPI();
    const routed = snapshot(
      initialIssues[1]!,
      {
        daemon_id: "work",
        intent: { scope: "project", project_uid: "project-kata", authority: "open" },
        generation: 1,
        event_cursor: 7,
      },
      true,
    );
    const harness = installFetchHarness([snapshot(initialIssues[0]!), routed]);
    const { rerender } = render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(1));

    await rerender({
      api,
      requestedDaemonId: "work",
      routeViewName: "inbox",
      routeScopeUID: "project-kata",
      selectedIssueUID: initialIssues[1]!.uid,
    });

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    expect(Object.fromEntries(new URL(harness.snapshotRequests[1]!.url).searchParams)).toEqual({
      scope: "project",
      authority: "open",
      project_uid: "project-kata",
      selected_issue_uid: initialIssues[1]!.uid,
    });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Email Susan re: Q3" })).toBeTruthy());
  });

  it("clears an unchanged routed selection when a daemon switch has no matching member", async () => {
    const selected = initialIssues[0]!;
    const onRouteStateChange = vi.fn();
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([
      snapshot(selected, {}, true),
      snapshot(selected, {
        daemon_id: "work",
        generation: 1,
        event_cursor: 7,
        member_issue_uids: [],
        issues: [],
        enrichment: {},
      }),
    ]);

    render(KataWorkspace, {
      props: { api, selectedIssueUID: selected.uid, onRouteStateChange },
    });
    await waitFor(() => expect(screen.getByRole("heading", { name: selected.title })).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: /Switch Kata daemon: home/ }));
    await fireEvent.click(await screen.findByRole("menuitemradio", { name: /work/ }));

    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    await waitFor(() => expect(screen.getByText("Select a task")).toBeTruthy());
    await waitFor(() =>
      expect(onRouteStateChange).toHaveBeenCalledWith(
        { view: null, scope: null, issue: null, daemon: null },
        { replace: true },
      ),
    );
  });

  it("opens a created project after explicit replacement snapshot authority reveals its UID", async () => {
    const createdProject: SnapshotProject = {
      id: 99,
      uid: "project-snapshot",
      name: "Snapshot Project",
      metadata: { area: "Work" },
      revision: 1,
      created_at: fetchedAt,
      open_count: 0,
      closed_count: 0,
    };
    const { api } = createWorkspaceAPI();
    api.createProject = vi.fn(async () => ({ changed: true }));
    const onRouteStateChange = vi.fn();
    const harness = installFetchHarness([
      snapshot(initialIssues[0]!),
      snapshot(initialIssues[0]!, {
        generation: 2,
        invalidation_epoch: 2,
        event_cursor: 6,
        projects: [...(projects as SnapshotProject[]), createdProject],
      }),
    ]);

    render(KataWorkspace, { props: { api, onRouteStateChange } });
    await waitFor(() => expect(harness.stream()).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "New project" }));
    await fireEvent.input(screen.getByRole("textbox", { name: "New project name" }), {
      target: { value: "Snapshot Project" },
    });
    await fireEvent.submit(screen.getByRole("textbox", { name: "New project name" }).closest("form")!);

    await waitFor(() => expect(api.createProject).toHaveBeenCalledWith("Snapshot Project", { daemonId: "home" }));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    await waitFor(() =>
      expect(onRouteStateChange).toHaveBeenCalledWith({
        view: null,
        scope: "project-snapshot",
        issue: null,
      }),
    );
  });

  it("abandons a valid daemon authority when the route changes to an unknown daemon", async () => {
    const { api } = createWorkspaceAPI();
    const home = snapshot(initialIssues[0]!, {}, true);
    const work = snapshot(initialIssues[1]!, { daemon_id: "work", generation: 1, event_cursor: 7 });
    const harness = installFetchHarness([home, work]);
    const { rerender } = render(KataWorkspace, { props: { api, selectedIssueUID: initialIssues[0]!.uid } });

    await waitFor(() => expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy());
    await waitFor(() => expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy());
    await waitFor(() => expect(harness.stream()).toBeTruthy());
    const oldHomeStream = harness.stream()!;

    await rerender({ api, requestedDaemonId: "missing", selectedIssueUID: initialIssues[0]!.uid });

    await waitFor(() => expect(harness.streamCancelCount()).toBe(1));
    await waitFor(() => expect(document.querySelector(".kata-layout")?.getAttribute("aria-busy")).toBe("true"));
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("status", { name: "Connection: error" }).textContent).toContain(
      "Kata daemon missing is not configured.",
    );
    expect(screen.queryByRole("button", { name: "Retry Kata snapshot" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Pay rent/ })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Pay rent" })).toBeNull();
    expect(harness.snapshotRequests).toHaveLength(1);

    try {
      oldHomeStream.enqueue(compactFrame(6));
    } catch {
      // A cancelled stream may reject the enqueue before the detached reader sees it.
    }
    await Promise.resolve();
    expect(harness.snapshotRequests).toHaveLength(1);
    expect(document.querySelector(".kata-layout")?.getAttribute("aria-busy")).toBe("true");

    await rerender({ api, requestedDaemonId: "work", selectedIssueUID: initialIssues[0]!.uid });
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    await waitFor(() => expect(screen.getByRole("button", { name: /Email Susan re: Q3/ })).toBeTruthy());
    await waitFor(() => expect(document.querySelector(".kata-layout")?.getAttribute("aria-busy")).toBe("false"));
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("does not offer snapshot retry when an empty daemon roster rejects the requested daemon", async () => {
    const { api } = createWorkspaceAPI();
    const harness = installFetchHarness([], []);

    render(KataWorkspace, { props: { api, requestedDaemonId: "missing" } });

    await screen.findAllByText("Kata daemon missing is not configured.");
    expect(screen.queryByRole("button", { name: "Retry Kata snapshot" })).toBeNull();
    expect(harness.snapshotRequests).toHaveLength(0);
  });

  it("reconciles graph source identity through the same authority path", async () => {
    const { api } = createWorkspaceAPI();
    const sourceIssue = initialIssues[0]!;
    const sourceUID = sourceIssue.uid;
    const harness = installFetchHarness([
      snapshot(initialIssues[0]!),
      snapshot(initialIssues[0]!, {
        generation: 2,
        event_cursor: 6,
        graph_source_uid: sourceUID,
        enrichment: {
          graph: {
            source_uid: sourceUID,
            depth: "full",
            hide_done: false,
            nodes: [
              {
                author: sourceIssue.author,
                body: sourceIssue.body ?? "",
                created_at: sourceIssue.created_at,
                id: sourceIssue.id,
                metadata: sourceIssue.metadata,
                project_id: sourceIssue.project_id,
                project_uid: sourceIssue.project_uid,
                qualified_id: sourceIssue.qualified_id,
                revision: sourceIssue.revision,
                short_id: sourceIssue.short_id,
                status: sourceIssue.status,
                title: sourceIssue.title,
                uid: sourceIssue.uid,
                updated_at: sourceIssue.updated_at,
              },
            ],
            edges: [],
            unresolved_refs: [],
          },
          graph_fetched_at: fetchedAt,
        },
      }),
      snapshot(initialIssues[0]!, { generation: 3, event_cursor: 7 }),
    ]);

    render(KataWorkspace, { props: { api } });
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole("button", { name: /Pay rent/ })).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Open reachable graph" }));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(2));
    expect(new URL(harness.snapshotRequests[1]!.url).searchParams.get("graph_source_uid")).toBe(sourceUID);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(
      harness.snapshotRequests.map((request) => new URL(request.url).searchParams.get("graph_source_uid")),
    ).toEqual([null, sourceUID]);

    await fireEvent.click(await screen.findByRole("button", { name: "Back to task list" }));
    await waitFor(() => expect(harness.snapshotRequests).toHaveLength(3));
    expect(new URL(harness.snapshotRequests[2]!.url).searchParams.has("graph_source_uid")).toBe(false);
  });
});
